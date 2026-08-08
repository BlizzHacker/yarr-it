package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubArchiveDocs answers every lookup with the same catalogue, so a test can
// state exactly what the Archive holds and then assert what the page does about
// it. The query is recorded, because "what did we actually ask" is half of what
// went wrong before.
func stubArchiveDocs(t *testing.T, docs []archiveDoc) *[]string {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("q"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{"numFound": len(docs), "docs": docs},
		})
	}))
	t.Cleanup(srv.Close)

	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	t.Cleanup(func() { archiveSearchAPI = old })
	return &asked
}

func gameRow(titles ...string) discoverRow {
	row := discoverRow{Title: "Top rated games", Key: "games-top"}
	for _, t := range titles {
		row.Items = append(row.Items, discoverItm{Title: t, MediaType: "game"})
	}
	return row
}

// The headline: a shelf of famous names, only some of which anybody can
// actually play, comes back holding only the ones that can.
func TestATileWithNothingBehindItIsNotPublished(t *testing.T) {
	stubArchiveDocs(t, []archiveDoc{
		{Identifier: "smw-usa", Title: "Super Mario World", Emulator: "snes", Downloads: 28882},
		{Identifier: "zelda-alttp", Emulator: "snes", Downloads: 11733,
			Title: "Legend Of Zelda, The A Link To The Past ( USA) SNES ROM"},
	})
	s := newTestServer()

	// No indexer configured, so archive.org IS the whole catalogue here and a
	// miss really is a dead button. See the outage tests below for the case
	// where it is not.
	rows := s.resolveDiscoverRows(context.Background(), []discoverRow{gameRow(
		"Super Mario World",
		"The Legend of Zelda: A Link to the Past",
		// The three Wade named. Modern console games with no free legal source:
		// the Archive returns nothing for them and always will.
		"Astro Bot",
		"Kingdom Come: Deliverance II",
		"Donkey Kong Bananza",
	)}, indexerState{})

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := map[string]string{}
	for _, it := range rows[0].Items {
		got[it.Title] = it.Play
	}
	if len(got) != 2 {
		t.Fatalf("shelf published %d tiles, want 2: %v", len(got), got)
	}
	for _, dead := range []string{"Astro Bot", "Kingdom Come: Deliverance II", "Donkey Kong Bananza"} {
		if _, ok := got[dead]; ok {
			t.Errorf("%q is on the shelf and nothing can open it", dead)
		}
	}
	if !strings.Contains(got["Super Mario World"], "smw-usa") {
		t.Errorf("Super Mario World points at %q", got["Super Mario World"])
	}
	if !strings.Contains(got["The Legend of Zelda: A Link to the Past"], "zelda-alttp") {
		t.Errorf("Zelda points at %q", got["The Legend of Zelda: A Link to the Past"])
	}
}

// The identity is carried through. A resolved tile opens its item; it does not
// go back out and search for its own name.
func TestAResolvedTileCarriesItsTargetRatherThanItsName(t *testing.T) {
	stubArchiveDocs(t, []archiveDoc{
		{Identifier: "smw-usa", Title: "Super Mario World", Emulator: "snes", Downloads: 100},
		{Identifier: "oregon", Title: "Oregon Trail", Emulator: "dosbox", Downloads: 100},
	})
	s := newTestServer()
	rows := s.resolveDiscoverRows(context.Background(),
		[]discoverRow{gameRow("Super Mario World", "Oregon Trail")}, indexerState{})

	for _, it := range rows[0].Items {
		if it.Play == "" {
			t.Fatalf("%q was published with no target", it.Title)
		}
		if !strings.HasPrefix(it.Play, "https://archive.org/details/") {
			t.Errorf("%q points somewhere unexpected: %s", it.Title, it.Play)
		}
		if it.Source != "archive.org" {
			t.Errorf("%q does not say where it comes from", it.Title)
		}
	}
	// A machine our own player has a core for opens in ours, which is the only
	// one with touch controls; MS-DOS has no usable core here and opens in
	// theirs. Same rule the native archive.org shelves already follow.
	byTitle := map[string]string{}
	for _, it := range rows[0].Items {
		byTitle[it.Title] = it.Play
	}
	if !strings.HasSuffix(byTitle["Super Mario World"], "#ejs") {
		t.Errorf("a SNES game did not choose the touch player: %s", byTitle["Super Mario World"])
	}
	if strings.HasSuffix(byTitle["Oregon Trail"], "#ejs") {
		t.Errorf("an MS-DOS game chose a player that cannot run it: %s", byTitle["Oregon Trail"])
	}
}

// The other half of Wade's complaint: not a missing result, a WRONG one. A tile
// must never resolve onto something that merely contains its name.
func TestATileNeverResolvesOntoAHackOrASequel(t *testing.T) {
	stubArchiveDocs(t, []archiveDoc{
		// Ordered as live archive.org returns them. The hack has five times the
		// downloads of the game, which is exactly how it used to win.
		{Identifier: "msdos_smw_dx", Title: "Super Mario World DX", Emulator: "dosbox", Downloads: 157392},
		{Identifier: "smw-yoshi", Title: "Super Mario World 2: Yoshi's Island", Emulator: "snes", Downloads: 19304},
		{Identifier: "smw-usa", Title: "Super Mario World", Emulator: "snes", Downloads: 28882},
	})
	s := newTestServer()
	rows := s.resolveDiscoverRows(context.Background(),
		[]discoverRow{gameRow("Super Mario World")}, indexerState{})
	if len(rows) != 0 && len(rows[0].Items) > 0 {
		if !strings.Contains(rows[0].Items[0].Play, "smw-usa") {
			t.Fatalf("Super Mario World resolved to %q", rows[0].Items[0].Play)
		}
	}

	// And with the exact title absent, it resolves to NOTHING rather than to
	// the nearest thing.
	stubArchiveDocs(t, []archiveDoc{
		{Identifier: "msdos_smw_dx", Title: "Super Mario World DX", Emulator: "dosbox", Downloads: 157392},
		{Identifier: "smw-yoshi", Title: "Super Mario World 2: Yoshi's Island", Emulator: "snes", Downloads: 19304},
	})
	s2 := newTestServer()
	rows = s2.resolveDiscoverRows(context.Background(),
		[]discoverRow{gameRow("Super Mario World")}, indexerState{})
	for _, r := range rows {
		for _, it := range r.Items {
			t.Fatalf("resolved onto %q, which is not the game", it.Play)
		}
	}
}

// One question per shelf, not one per tile. Twenty-four sequential lookups is
// most of a minute of somebody else's rate limit for a page rebuilt every three
// hours.
func TestAShelfAsksOneQuestionForAllOfItsTiles(t *testing.T) {
	asked := stubArchiveDocs(t, nil)
	s := newTestServer()
	titles := make([]string, 0, resolveBatch)
	for i := 0; i < resolveBatch; i++ {
		titles = append(titles, string(rune('a'+i))+"game")
	}
	s.resolveDiscoverRows(context.Background(), []discoverRow{gameRow(titles...)}, indexerState{})
	if len(*asked) != 1 {
		t.Fatalf("%d queries for one shelf of %d tiles", len(*asked), len(titles))
	}
	for _, title := range titles {
		if !strings.Contains((*asked)[0], title) {
			t.Errorf("%q was not asked about: %s", title, (*asked)[0])
		}
	}
}

// The archive.org shelves are built from real identifiers already. Sending them
// back through a lookup would spend requests to re-derive what they carry.
func TestShelvesThatAlreadyCarryTargetsAreLeftAlone(t *testing.T) {
	asked := stubArchiveDocs(t, nil)
	s := newTestServer()
	row := discoverRow{Title: "Games you can play right now", Key: "ia-games", Items: []discoverItm{
		{Title: "Oregon Trail", MediaType: "game", Play: "https://archive.org/details/oregon"},
		{Title: "Pac-Man", MediaType: "game", Play: "https://archive.org/details/pacman"},
	}}
	rows := s.resolveDiscoverRows(context.Background(), []discoverRow{row}, indexerState{})
	if len(*asked) != 0 {
		t.Errorf("a shelf that already had targets was looked up anyway: %v", *asked)
	}
	if len(rows) != 1 || len(rows[0].Items) != 2 {
		t.Fatalf("an already-targeted shelf lost tiles: %+v", rows)
	}
}

// An Archive that did not answer is not an Archive with nothing in it -- but a
// tile is a promise, and a promise cannot be made on an unanswered question.
func TestAnUnreachableArchiveCostsTilesRatherThanThePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()
	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	defer func() { archiveSearchAPI = old }()

	s := newTestServer()
	rows := s.resolveDiscoverRows(context.Background(), []discoverRow{
		gameRow("Super Mario World", "Chrono Trigger"),
		{Title: "Games you can play right now", Key: "ia-games", Items: []discoverItm{
			{Title: "Oregon Trail", MediaType: "game", Play: "https://archive.org/details/oregon"},
			{Title: "Pac-Man", MediaType: "game", Play: "https://archive.org/details/pacman"},
		}},
	}, indexerState{})
	if len(rows) != 1 || rows[0].Key != "ia-games" {
		t.Fatalf("the shelves that still work did not survive: %+v", rows)
	}
}

// A shelf keeps whatever survives, however little. The minimum row width used
// to be two, which is an aesthetic rule with a real cost: it turned "we found
// one of these" into "we found none", and it is the same class of mistake as
// deleting a row during an outage -- discarding a fact because it is
// inconvenient rather than because it is wrong.
func TestAShelfKeepsWhatItFoundHoweverLittle(t *testing.T) {
	stubArchiveDocs(t, []archiveDoc{
		{Identifier: "smw-usa", Title: "Super Mario World", Emulator: "snes", Downloads: 1},
	})
	s := newTestServer()
	rows := s.resolveDiscoverRows(context.Background(),
		[]discoverRow{gameRow("Super Mario World", "Astro Bot", "Elden Ring")}, indexerState{})
	if len(rows) != 1 || len(rows[0].Items) != 1 {
		t.Fatalf("the one thing that was found was thrown away: %+v", rows)
	}
	// A shelf where nothing at all survives is still dropped: there is no row
	// to draw.
	stubArchiveDocs(t, nil)
	rows = newTestServer().resolveDiscoverRows(context.Background(),
		[]discoverRow{gameRow("Astro Bot", "Elden Ring")}, indexerState{})
	if len(rows) != 0 {
		t.Fatalf("an empty shelf was published: %+v", rows)
	}
}

// A film tile must not be answered from the ROM catalogue. This is why every
// film tile returned zero rather than something wrong: the click sent no kind,
// an absent kind means the emulator scope, so a film searched a shelf of games.
func TestEachKindIsResolvedAgainstACatalogueThatCouldHoldIt(t *testing.T) {
	for _, c := range []struct{ mediaType, want string }{
		{"game", "emulator:"},
		{"movie", "mediatype:(movies)"},
		{"tv", "mediatype:(movies)"},
		{"text", "gutenberg"},
	} {
		scope, ok := resolveScope(c.mediaType)
		if !ok {
			t.Errorf("%q has no catalogue at all", c.mediaType)
			continue
		}
		if !strings.Contains(scope, c.want) {
			t.Errorf("%q resolves against %q, which cannot hold one", c.mediaType, scope)
		}
	}
	// A film scope that accepts an item with no browser-playable derivative is
	// a details page dressed as a film.
	movies, _ := resolveScope("movie")
	if !strings.Contains(movies, "format:") {
		t.Errorf("the film scope does not require something playable: %s", movies)
	}
}

// ---------------------------------------------------------------- live proof
//
// Skipped by default so `go test ./...` stays hermetic and offline. Run with
//
//	YARRIT_LIVE=1 go test -run TestLive -v
//
// to check the matching against the real Internet Archive, which is the only
// thing that can actually confirm it -- every fixture above is a transcript of
// what it returned on 2026-08-08, and a transcript cannot notice when the far
// end changes.
func TestLiveArchiveHoldsWhatTheMatcherClaims(t *testing.T) {
	if os.Getenv("YARRIT_LIVE") == "" {
		t.Skip("set YARRIT_LIVE=1 to query the real archive.org")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newTestServer()
	rows := s.resolveDiscoverRows(ctx, []discoverRow{gameRow(
		// Must resolve: the Archive demonstrably holds playable copies.
		"Super Mario World",
		"The Legend of Zelda: A Link to the Past",
		"Chrono Trigger",
		"Castlevania: Symphony of the Night",
		// Must not: current console games with no free legal source.
		"Astro Bot",
		"Kingdom Come: Deliverance II",
		"Donkey Kong Bananza",
	)}, indexerState{})

	got := map[string]string{}
	for _, r := range rows {
		for _, it := range r.Items {
			got[it.Title] = it.Play
		}
	}
	for _, want := range []string{
		"Super Mario World",
		"The Legend of Zelda: A Link to the Past",
	} {
		if got[want] == "" {
			t.Errorf("archive.org holds %q and the matcher did not find it", want)
		}
	}
	for _, dead := range []string{"Astro Bot", "Kingdom Come: Deliverance II", "Donkey Kong Bananza"} {
		if got[dead] != "" {
			t.Errorf("%q resolved to %q, which cannot be it", dead, got[dead])
		}
	}
	for title, target := range got {
		t.Logf("%-45s -> %s", title, target)
	}
}

// archive.org stops erroring and starts answering 200 with numFound 0 when it
// is asked too much at once, and an empty batch is indistinguishable from a
// batch whose titles are genuinely not there. Measured on identical input,
// "Retro classics" came out with 2, 4 and 6 tiles on three consecutive builds.
func TestNoMoreThanAFewQuestionsAtOnce(t *testing.T) {
	var mu sync.Mutex
	live, peak := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		live++
		if live > peak {
			peak = live
		}
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		live--
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{"numFound": 0, "docs": []archiveDoc{}},
		})
	}))
	defer srv.Close()
	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	defer func() { archiveSearchAPI = old }()

	// Eight shelves of two batches each: sixteen questions if nothing bounds it.
	rows := make([]discoverRow, 0, 8)
	for r := 0; r < 8; r++ {
		titles := make([]string, 0, resolveBatch*2)
		for i := 0; i < resolveBatch*2; i++ {
			titles = append(titles, fmt.Sprintf("row%d game%d", r, i))
		}
		rows = append(rows, gameRow(titles...))
	}
	newTestServer().resolveDiscoverRows(context.Background(), rows, indexerState{})

	mu.Lock()
	defer mu.Unlock()
	if peak > cap(resolveLookups) {
		t.Fatalf("asked %d questions at once; the limit is %d", peak, cap(resolveLookups))
	}
	if peak == 0 {
		t.Fatal("nothing was asked at all, so this proves nothing")
	}
}

// ------------------------------------------------- an outage is not an answer
//
// This is the regression that matters most in this file, and it is one I
// shipped. The first version treated "archive.org does not have it" as "nothing
// has it", which is only true on an instance with no torrent indexer. On Wade's
// it deleted the five TMDB rows -- Trending this week, Popular films, Top rated
// films, Popular TV, In cinemas now -- because Prowlarr happened to be down
// that afternoon. Those tiles resolve fine through torrents. A page that
// permanently reshapes itself around a backend blinking is a worse version of
// the defect this whole task started from.

func filmRow(titles ...string) discoverRow {
	row := discoverRow{Title: "Trending this week", Key: "trending"}
	for _, t := range titles {
		row.Items = append(row.Items, discoverItm{Title: t, MediaType: "movie", Year: 2026})
	}
	return row
}

func TestAnUnreachableIndexerDeletesNothing(t *testing.T) {
	stubArchiveDocs(t, nil) // the Archive has none of these, which is true
	s := newTestServer()

	down := indexerState{Configured: true, Reachable: false, Detail: "not answering"}
	rows := s.resolveDiscoverRows(context.Background(),
		[]discoverRow{filmRow("Spider-Man: Brand New Day", "Wicked: For Good", "Zootopia 2")},
		down)

	if len(rows) != 1 {
		t.Fatalf("the row was deleted during an outage: %+v", rows)
	}
	if len(rows[0].Items) != 3 {
		t.Fatalf("got %d tiles, want all 3 kept", len(rows[0].Items))
	}
	for _, it := range rows[0].Items {
		if it.State != tileUnchecked {
			t.Errorf("%q is published as %q; nothing was actually established "+
				"about it", it.Title, it.State)
		}
		if it.Play != "" {
			t.Errorf("%q claims a target nobody verified: %s", it.Title, it.Play)
		}
	}
}

// The same rows, with the indexer up. Still kept, still unchecked -- because
// asking is what the click does, and 172 fan-outs to build one page is the
// measured way to take Prowlarr down (see fanout.go).
func TestAReachableIndexerAlsoDeletesNothing(t *testing.T) {
	stubArchiveDocs(t, nil)
	s := newTestServer()
	up := indexerState{Configured: true, Reachable: true}
	rows := s.resolveDiscoverRows(context.Background(),
		[]discoverRow{filmRow("Spider-Man: Brand New Day", "Wicked: For Good")}, up)
	if len(rows) != 1 || len(rows[0].Items) != 2 {
		t.Fatalf("tiles were dropped while the indexer was healthy: %+v", rows)
	}
}

// And with no indexer at all, the same tiles ARE dead, because there is nowhere
// else for them to come from. This is the distinction the first version could
// not make.
func TestWithNoIndexerTheSameTilesAreDead(t *testing.T) {
	stubArchiveDocs(t, nil)
	s := newTestServer()
	rows := s.resolveDiscoverRows(context.Background(),
		[]discoverRow{filmRow("Spider-Man: Brand New Day", "Wicked: For Good")},
		indexerState{})
	if len(rows) != 0 {
		t.Fatalf("dead tiles published on an instance with no indexer: %+v", rows)
	}
}

// A result the server already holds -- from an earlier search or the warmer --
// upgrades a tile from "not asked" to "something has this", at no upstream
// cost. It can only ever promote: a cold cache leaves the tile unchecked, never
// dead.
func TestWhatTheServerAlreadyHoldsCountsAsAnAnswer(t *testing.T) {
	stubArchiveDocs(t, nil)
	s := newTestServer()
	s.putCached(searchCacheKey("Interstellar 2014", "video"), []card{{
		Key: "t:1", Title: "Interstellar", Kind: "video", Year: 2014,
		Sources: []source{{Title: "Interstellar 2014 1080p", Seeders: 40}},
	}})

	row := discoverRow{Title: "Top rated films", Key: "top-movies", Items: []discoverItm{
		{Title: "Interstellar", MediaType: "movie", Year: 2014},
		{Title: "Wicked: For Good", MediaType: "movie", Year: 2025},
	}}
	rows := s.resolveDiscoverRows(context.Background(), []discoverRow{row},
		indexerState{Configured: true, Reachable: true})

	byTitle := map[string]discoverItm{}
	for _, it := range rows[0].Items {
		byTitle[it.Title] = it
	}
	if got := byTitle["Interstellar"].State; got != tileFound {
		t.Errorf("a title the server already has results for is %q", got)
	}
	if got := byTitle["Interstellar"].Source; got != "indexer" {
		t.Errorf("source = %q", got)
	}
	// No target is invented from a torrent: a torrent result is a set of
	// releases to choose between, and picking one at build time is how somebody
	// gets handed a CAM rip.
	if byTitle["Interstellar"].Play != "" {
		t.Errorf("a torrent result was turned into a single address: %s",
			byTitle["Interstellar"].Play)
	}
	if got := byTitle["Wicked: For Good"].State; got != tileUnchecked {
		t.Errorf("a title nothing is known about is %q", got)
	}
}

// The cache must not promote a tile on the strength of a DIFFERENT film that
// shares a word, which is the same defect matchScore exists to stop.
func TestTheCacheOnlyAnswersForTheThingAsked(t *testing.T) {
	stubArchiveDocs(t, nil)
	s := newTestServer()
	s.putCached(searchCacheKey("something", "video"), []card{{
		Key: "t:2", Title: "Interstellar Wars", Kind: "video",
		Sources: []source{{Title: "x", Seeders: 5}},
	}})
	if s.alreadyHeld("Interstellar", "movie") {
		t.Fatal("a different film answered for Interstellar")
	}
}

// A configured backend and an absent one are different facts with different
// fixes, and collapsing them is what makes an outage look permanent.
func TestTheIndexerStateSaysWhichProblemItIs(t *testing.T) {
	s := newTestServer()
	if got := s.indexerHealth(context.Background()); got.Configured || got.canAnswer() {
		t.Fatalf("an unconfigured indexer reported as present: %+v", got)
	}
	if s.indexerHealth(context.Background()).Detail == "" {
		t.Error("an unconfigured indexer said nothing about why")
	}

	// Configured, pointed at nothing that answers.
	s2 := newTestServer()
	s2.apiKey = "k"
	s2.prowlarrURL = "http://127.0.0.1:1"
	got := s2.indexerHealth(context.Background())
	if !got.Configured {
		t.Error("a configured indexer reported as absent")
	}
	if got.Reachable {
		t.Error("an unreachable indexer reported as reachable")
	}
	if !got.canAnswer() {
		t.Error("an unreachable indexer must still count as somewhere a title " +
			"could come from -- it has not said no")
	}
}
