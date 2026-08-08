package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// plexEpisodes makes n half-hour episodes of one show, in the shape Plex
// actually returns them: no genres, no studio, no rating -- those live on the
// show row and nowhere else.
func plexEpisodes(n int) []plexMetadata {
	out := make([]plexMetadata, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, plexMetadata{
			RatingKey: fmt.Sprintf("%d", 1000+i), Type: "episode",
			Title:    fmt.Sprintf("Episode %d", i),
			Duration: 1_800_000, // 30 minutes in milliseconds
			Index:    i, ParentIndex: 1,
			GrandparentTitle: "A Show", GrandparentRatingKey: "900",
			Thumb: fmt.Sprintf("/library/metadata/%d/thumb/1", 1000+i),
			Media: []plexMedia{{
				ID: int64(i), Duration: 1_800_000, Container: "mkv",
				Part: []plexPart{{
					ID: int64(i), Key: fmt.Sprintf("/library/parts/%d/1/file.mkv", i),
					Duration: 1_800_000, Container: "mkv",
				}},
			}},
		})
	}
	return out
}

func plexShowRow() plexMetadata {
	return plexMetadata{
		RatingKey: "900", Type: "show", Title: "A Show", Year: 2011,
		Genre:  []plexTag{{Tag: "Comedy"}, {Tag: "Family"}},
		Studio: "Boulder Media", ContentRating: "TV-Y7",
	}
}

func plexLibraryFixture(episodes []plexMetadata) *plexFixture {
	f := plexHealthyFixture()
	f.pages = map[string][]plexMetadata{
		"6/4": episodes,
		"6/2": {plexShowRow()},
		"1/1": {plexMovie("174572", 6_102_954)},
	}
	f.items = map[string]plexMetadata{"174572": plexMovie("174572", 6_102_954)}
	for _, e := range episodes {
		f.items[e.RatingKey] = e
	}
	return f
}

// --- library ----------------------------------------------------------------

// The reason showFacets exists. Plex returns episodes with no genres, so
// without the fold a "genre is Comedy" channel on a TV library matches nothing
// and looks correct while doing it.
func TestPlexLinearLibraryFoldsShowGenresOntoEpisodes(t *testing.T) {
	f := plexLibraryFixture(plexEpisodes(5))
	p := newTestPlex(t, f)
	lib := &plexLinearLibrary{p: p, section: plexDirectory{Key: "6", Type: "show", Title: "Cartoons"}}

	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("got %d items", len(items))
	}
	for _, it := range items {
		if len(it.Genres) != 2 || it.Genres[0] != "Comedy" {
			t.Fatalf("%q has genres %v; Plex puts them on the show, and they were not folded down",
				it.Title, it.Genres)
		}
		if it.Network != "Boulder Media" {
			t.Errorf("%q has network %q", it.Title, it.Network)
		}
		if it.DurationSeconds != 1800 {
			t.Errorf("%q has duration %d, want 1800", it.Title, it.DurationSeconds)
		}
		if it.LibraryID != "Cartoons" {
			t.Errorf("libraryId %q, want the section title", it.LibraryID)
		}
	}

	// And a rule naming that genre must now actually select them, which is the
	// thing a person building a channel is trying to do.
	matched := linearApplyRules(items, LinearRuleGroup{
		Match: LinearMatchAll,
		Rules: []LinearRule{{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Comedy"}},
	})
	if len(matched) != 5 {
		t.Fatalf(`"genre is Comedy" matched %d of 5 episodes`, len(matched))
	}
}

// A movie section needs no fold and must not pay for one.
func TestPlexLinearLibraryDoesNotFetchShowsForAMovieSection(t *testing.T) {
	f := plexLibraryFixture(nil)
	p := newTestPlex(t, f)
	lib := &plexLinearLibrary{p: p, section: plexDirectory{Key: "1", Type: "movie", Title: "Movies"}}

	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].CanonicalID != "plex:movie:174572" {
		t.Fatalf("items %+v", items)
	}
	for _, q := range f.pageRequests {
		if q.Get("type") == "2" {
			t.Error("a movie section asked Plex for show rows it has no use for")
		}
	}
}

func TestPlexLinearLibraryPagesTheWholeSection(t *testing.T) {
	f := plexLibraryFixture(plexEpisodes(1250))
	p := newTestPlex(t, f)
	lib := &plexLinearLibrary{p: p, section: plexDirectory{Key: "6", Type: "show", Title: "Cartoons"}}

	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1250 {
		t.Fatalf("got %d items from a 1,250-episode section", len(items))
	}
}

func TestPlexLinearLibraryStopsAtThePoolLimit(t *testing.T) {
	f := plexLibraryFixture(plexEpisodes(1250))
	p := newTestPlex(t, f)
	p.poolLimit = 600
	lib := &plexLinearLibrary{p: p, section: plexDirectory{Key: "6", Type: "show", Title: "Cartoons"}}

	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 600 {
		t.Fatalf("got %d items, want the 600-item cap", len(items))
	}
}

func TestPlexLinearLibraryIDIsTheSectionTitle(t *testing.T) {
	lib := &plexLinearLibrary{
		p:       newPlexProvider(plexConfig{BaseURL: "http://plex.test", Token: "t"}),
		section: plexDirectory{Key: "6", Type: "show", Title: "Cartoons"},
	}
	if got := lib.LinearLibraryID(); got != "Cartoons" {
		t.Fatalf("LinearLibraryID = %q", got)
	}
	if got := lib.LinearProviderID(); got != "plex" {
		t.Fatalf("LinearProviderID = %q", got)
	}
}

func TestNewPlexLinearSkipsMusicSections(t *testing.T) {
	f := plexLibraryFixture(plexEpisodes(2))
	srv := f.server(t)

	_, libs, resolver, err := newPlexLinear(context.Background(), plexConfig{
		BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolver == nil {
		t.Fatal("no resolver")
	}
	var names []string
	for _, l := range libs {
		names = append(names, l.LinearLibraryID())
	}
	if len(names) != 2 {
		t.Fatalf("libraries %v, want the two video sections", names)
	}
	for _, n := range names {
		if n == "Music" {
			t.Error("a music section was offered as a source for a video channel")
		}
	}
}

func TestNewPlexLinearStillReturnsAProviderWhenDiscoveryFails(t *testing.T) {
	f := plexHealthyFixture()
	srv := f.server(t)
	addr := srv.URL
	srv.Close()

	p, libs, resolver, err := newPlexLinear(context.Background(), plexConfig{BaseURL: addr, Token: "t"})
	if err == nil {
		t.Fatal("discovery against a dead server must report an error")
	}
	if p == nil || resolver == nil {
		t.Fatal("the provider and resolver must come back anyway, so the instance is still visible")
	}
	if len(libs) != 0 {
		t.Errorf("libraries %v, want none", libs)
	}
	if got := p.Health(context.Background()).State; got != HealthUnreachable {
		t.Errorf("health %q, want %q", got, HealthUnreachable)
	}
}

// --- resolver ---------------------------------------------------------------

func TestPlexResolverRejectsAnIDItDidNotIssue(t *testing.T) {
	r := plexLinearResolver{p: newTestPlex(t, plexHealthyFixture())}
	_, err := r.LinearResolve(context.Background(), Program{CanonicalID: "jellyfin:movie:1"}, StreamOptions{})
	if !errors.Is(err, ErrLinearMissingMedia) {
		t.Fatalf("err = %v, want ErrLinearMissingMedia", err)
	}
}

func TestPlexResolverReportsADeletedFileAsMissingMedia(t *testing.T) {
	r := plexLinearResolver{p: newTestPlex(t, plexHealthyFixture())}
	_, err := r.LinearResolve(context.Background(),
		Program{CanonicalID: "plex:movie:404", Title: "A Film"}, StreamOptions{})
	if !errors.Is(err, ErrLinearMissingMedia) {
		t.Fatalf("err = %v, want ErrLinearMissingMedia", err)
	}
	if !strings.Contains(err.Error(), "A Film") {
		t.Errorf("the message must name the programme, got %q", err)
	}
}

func TestPlexResolverKeepsTransientFailuresRetryable(t *testing.T) {
	f := plexHealthyFixture()
	srv := f.server(t)
	addr := srv.URL
	srv.Close() // the server is down, not the file
	r := plexLinearResolver{p: newPlexProvider(plexConfig{BaseURL: addr, Token: "t"})}

	_, err := r.LinearResolve(context.Background(), Program{CanonicalID: "plex:movie:1"}, StreamOptions{})
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrLinearMissingMedia) {
		t.Fatal("an unreachable server was reported as missing media; the engine would stop retrying")
	}
}

// --- the whole path ---------------------------------------------------------

// The Plex equivalent of the Jellyfin end-to-end test: a real engine, a real
// channel, a Plex library behind an HTTP server, and a clock standing part-way
// into a programme. What comes back must be a URL that begins there.
func TestLinearTuneInHandsBackAPlexURLAtTheLiveOffset(t *testing.T) {
	f := plexLibraryFixture(plexEpisodes(12))
	p := newTestPlex(t, f)

	at := time.Date(2026, 6, 1, 20, 37, 0, 0, time.UTC)
	clk := &linearClock{t: at}
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL, e.retryBackoff = clk.now, time.Hour, 24*time.Hour, 0
	e.AddLibrary(&plexLinearLibrary{p: p, section: plexDirectory{Key: "6", Type: "show", Title: "Cartoons"}})
	e.SetResolver(plexLinearResolver{p: p})

	if _, err := e.SaveChannel(LinearChannel{
		ID: "plex-tv", Number: 8, Name: "Plex Cartoons", Enabled: true,
		SourceProvider: "plex", SourceLibraries: []string{"Cartoons"},
		ScheduleStrategy: StrategyCyclic, Timezone: "UTC", EPGDays: 1, Seed: 99,
	}); err != nil {
		t.Fatal(err)
	}

	tune := e.TuneIn(context.Background(), "plex-tv", "UTC")
	if tune.State != LinearStateOnAir {
		t.Fatalf("state %q (%s)", tune.State, tune.Detail)
	}
	if !tune.Available || tune.Source == nil {
		t.Fatalf("nothing playable came back: %s", tune.Detail)
	}
	// Half-hour programmes from local midnight: at 20:37 the live one began at
	// 20:30, so the channel is 7 minutes in.
	if tune.OffsetSeconds != 420 {
		t.Fatalf("live offset %v, want 420 seconds", tune.OffsetSeconds)
	}

	u, err := url.Parse(tune.Source.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("offset"); got != "420" {
		t.Fatalf("the schedule says 420 seconds in, the stream URL asks for %q", got)
	}
	if tune.Source.DirectPlay || tune.Source.Seekable {
		t.Errorf("a Plex offset start is a session transcode with no ranges: "+
			"directPlay=%v seekable=%v, want both false", tune.Source.DirectPlay, tune.Source.Seekable)
	}

	// And it must roll over on its own.
	before := tune.Program.CanonicalID
	clk.advance(25 * time.Minute) // 21:02
	next := e.TuneIn(context.Background(), "plex-tv", "UTC")
	if next.Program == nil || next.Program.CanonicalID == before {
		t.Fatal("the channel did not roll over to the next programme")
	}
	if next.OffsetSeconds != 120 {
		t.Fatalf("after rollover the offset is %v, want 120", next.OffsetSeconds)
	}
	u2, _ := url.Parse(next.Source.URL)
	if got := u2.Query().Get("offset"); got != "120" {
		t.Fatalf("after rollover the URL asks for offset %q, want 120", got)
	}
}

// One engine, two media servers, one door. This is what the chain is for.
func TestOneEngineCanServeChannelsFromJellyfinAndPlexTogether(t *testing.T) {
	jf := jellyfinLibraryFixture(jellyfinEpisodes(12))
	jp := newTestJellyfin(t, jf)
	pf := plexLibraryFixture(plexEpisodes(12))
	pp := newTestPlex(t, pf)

	at := time.Date(2026, 6, 1, 20, 37, 0, 0, time.UTC)
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL, e.retryBackoff = (&linearClock{t: at}).now, time.Hour, 24*time.Hour, 0
	e.AddLibrary(&jellyfinLinearLibrary{p: jp, view: jf.views[0], itemTypes: []string{"Episode"}})
	e.AddLibrary(&plexLinearLibrary{p: pp, section: plexDirectory{Key: "6", Type: "show", Title: "Cartoons"}})
	e.SetResolver(newLinearResolverChain(
		jellyfinLinearResolver{p: jp},
		plexLinearResolver{p: pp},
	))

	for _, ch := range []LinearChannel{
		{ID: "jf", Number: 1, Name: "From Jellyfin", Enabled: true, SourceProvider: "jellyfin",
			ScheduleStrategy: StrategyCyclic, Timezone: "UTC", EPGDays: 1, Seed: 1},
		{ID: "px", Number: 2, Name: "From Plex", Enabled: true, SourceProvider: "plex",
			ScheduleStrategy: StrategyCyclic, Timezone: "UTC", EPGDays: 1, Seed: 2},
	} {
		if _, err := e.SaveChannel(ch); err != nil {
			t.Fatal(err)
		}
	}

	jfTune := e.TuneIn(context.Background(), "jf", "")
	if !jfTune.Available || jfTune.Source == nil {
		t.Fatalf("the Jellyfin channel is not playable: %s", jfTune.Detail)
	}
	if !strings.Contains(jfTune.Source.URL, jp.c.baseURL) {
		t.Errorf("the Jellyfin channel resolved to %s", jellyfinRedact(jfTune.Source.URL))
	}

	pxTune := e.TuneIn(context.Background(), "px", "")
	if !pxTune.Available || pxTune.Source == nil {
		t.Fatalf("the Plex channel is not playable: %s", pxTune.Detail)
	}
	if !strings.Contains(pxTune.Source.URL, pp.c.baseURL) {
		t.Errorf("the Plex channel resolved to %s", jellyfinRedact(pxTune.Source.URL))
	}

	// Both are 7 minutes into a half-hour programme, each expressed in the
	// units its own server reads.
	jfURL, _ := url.Parse(jfTune.Source.URL)
	if got := jellyfinFirstQuery(jfURL, "startTimeTicks", "StartTimeTicks"); got != "4200000000" {
		t.Errorf("Jellyfin startTimeTicks=%q, want 4200000000 (420s)", got)
	}
	pxURL, _ := url.Parse(pxTune.Source.URL)
	if got := pxURL.Query().Get("offset"); got != "420" {
		t.Errorf("Plex offset=%q, want 420", got)
	}
}
