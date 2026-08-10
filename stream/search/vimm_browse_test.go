package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeArchive stands in for archive.org's Solr endpoint, answering every query
// with `n` synthetic items per page. The titles carry the page number so a
// paging test can tell page 2's items from page 1's.
func fakeArchiveShelf(t *testing.T, perPage int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		rows := perPage
		docs := make([]map[string]any, 0, rows)
		for i := 0; i < rows; i++ {
			docs = append(docs, map[string]any{
				"identifier": fmt.Sprintf("ia-p%s-%d", page, i),
				"title":      fmt.Sprintf("Archive p%s #%d", page, i),
				"emulator":   "snes",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{"docs": docs},
		})
	}))
	t.Cleanup(srv.Close)
	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	t.Cleanup(func() { archiveSearchAPI = old })
}

// deadArchive stands in for archive.org being down.
func deadArchive(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	t.Cleanup(func() { archiveSearchAPI = old })
}

// snesEntries builds n publishable SNES rows.
func snesEntries(n int) []vimmEntry {
	out := make([]vimmEntry, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, vimmEntry{
			VaultID: fmt.Sprint(i), Title: fmt.Sprintf("Vimm Game %03d", i),
			Platform: "Super Nintendo", System: "snes",
			Page:     fmt.Sprintf("https://vimm.net/vault/%d", i),
			Download: fmt.Sprintf("https://dl3.vimm.net/?mediaId=%d", i),
		})
	}
	return out
}

func gameBrowseServer(t *testing.T, entries ...vimmEntry) *server {
	t.Helper()
	s := browseTestServer()
	s.vimm = storeWith(t, entries...)
	return s
}

// --------------------------------------------------------------- labelling --

// The whole point of putting Vimm on a category page is that it stays visibly
// Vimm. A tile that lost its External block is one the client labels with a
// verb -- "Play", or at best "Open" -- over a link to somebody else's website.
func TestAVimmBrowseTileSaysItLeavesTheSite(t *testing.T) {
	fakeArchiveShelf(t, 4)
	s := gameBrowseServer(t, snesEntries(3)...)

	row, err := s.browseRow(context.Background(), "ia:sys:snes", 6, 1)
	if err != nil {
		t.Fatalf("browseRow: %v", err)
	}

	var vimm []discoverItm
	for _, it := range row.Items {
		if it.Source == vimmSiteName {
			vimm = append(vimm, it)
		}
	}
	if len(vimm) == 0 {
		t.Fatal("no Vimm tiles on the SNES page at all")
	}
	for _, it := range vimm {
		if it.External == nil {
			t.Fatalf("%q has no External block; the client would label it with a verb", it.Title)
		}
		if it.External.Host != vimmHost || it.External.Short == "" || it.External.Name == "" {
			t.Errorf("%q has an incomplete External: %+v", it.Title, it.External)
		}
		if !strings.HasPrefix(it.Play, "https://vimm.net/vault/") {
			t.Errorf("%q opens %q; a tile that leaves should open the vault page", it.Title, it.Play)
		}
	}
}

// An archive.org tile is the thing itself and must NOT acquire an External
// block just because something else on the page has one -- that would relabel
// every game on the site as an outbound link.
func TestArchiveTilesOnAMixedPageStayInternal(t *testing.T) {
	fakeArchiveShelf(t, 4)
	s := gameBrowseServer(t, snesEntries(3)...)

	row, err := s.browseRow(context.Background(), "ia:sys:snes", 6, 1)
	if err != nil {
		t.Fatalf("browseRow: %v", err)
	}
	seen := 0
	for _, it := range row.Items {
		if it.Source == vimmSiteName {
			continue
		}
		seen++
		if it.External != nil {
			t.Errorf("archive.org item %q carries External %+v", it.Title, it.External)
		}
	}
	if seen == 0 {
		t.Fatal("no archive.org tiles on the page")
	}
}

// ------------------------------------------------------------------ mixing --

// The page has to show both catalogues. This is the regression the work exists
// to fix: browseRow used to switch on solr/tmdb with no third branch, so a
// category page was archive.org and nothing else.
func TestAGameCategoryPageShowsBothCatalogues(t *testing.T) {
	fakeArchiveShelf(t, 40)
	s := gameBrowseServer(t, snesEntries(50)...)

	row, err := s.browseRow(context.Background(), "ia:sys:snes", 60, 1)
	if err != nil {
		t.Fatalf("browseRow: %v", err)
	}
	var fromVimm, fromArchive int
	for _, it := range row.Items {
		if it.Source == vimmSiteName {
			fromVimm++
		} else {
			fromArchive++
		}
	}
	if fromVimm == 0 || fromArchive == 0 {
		t.Fatalf("page is not mixed: %d from Vimm, %d from archive.org", fromVimm, fromArchive)
	}
	if want := vimmShareOf(60); fromVimm != want {
		t.Errorf("Vimm contributed %d tiles, want the fixed share of %d", fromVimm, want)
	}
}

// Concatenating instead of interleaving puts twenty Vimm tiles at the top of
// every page. On a phone that is five scrolls before the Archive appears, which
// reads as a Vimm page.
func TestTheTwoCataloguesAreInterleavedNotStacked(t *testing.T) {
	fakeArchiveShelf(t, 40)
	s := gameBrowseServer(t, snesEntries(50)...)

	row, err := s.browseRow(context.Background(), "ia:sys:snes", 60, 1)
	if err != nil {
		t.Fatalf("browseRow: %v", err)
	}
	// In the first twelve tiles -- roughly a phone screen and a half -- both
	// sources must appear.
	head := row.Items
	if len(head) > 12 {
		head = head[:12]
	}
	var v, a int
	for _, it := range head {
		if it.Source == vimmSiteName {
			v++
		} else {
			a++
		}
	}
	if v == 0 || a == 0 {
		t.Errorf("first %d tiles are one-sided: %d Vimm, %d archive.org", len(head), v, a)
	}
}

// ------------------------------------------------------------------ paging --

// Paging is the property most easily broken by mixing two sources with
// different depths: an offset that depends on what survived earlier pages
// skips or repeats items. Nothing may appear twice, and nothing may be lost.
func TestPagingAGameCategoryNeitherRepeatsNorSkips(t *testing.T) {
	fakeArchiveShelf(t, 40)
	s := gameBrowseServer(t, snesEntries(50)...)

	seen := map[string]int{}
	var vimmTitles []string
	for page := 1; page <= 3; page++ {
		row, err := s.browseRow(context.Background(), "ia:sys:snes", 60, page)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, it := range row.Items {
			seen[it.Title]++
			if it.Source == vimmSiteName {
				vimmTitles = append(vimmTitles, it.Title)
			}
		}
	}
	for title, n := range seen {
		if n > 1 {
			t.Errorf("%q appeared %d times across three pages", title, n)
		}
	}
	// Three pages at a share of 20 should have walked exactly 50 catalogue
	// entries -- all of them -- and then stopped rather than wrapping.
	if len(vimmTitles) != 50 {
		t.Errorf("saw %d catalogue tiles over three pages; the machine has 50", len(vimmTitles))
	}
}

// The share is a function of the page number alone. If it were not, an
// archive.org offset would drift as soon as the catalogue ran out.
func TestTheCatalogueShareDependsOnlyOnThePageNumber(t *testing.T) {
	for _, limit := range []int{2, 6, 24, 60, 100} {
		share := vimmShareOf(limit)
		if share < 1 {
			t.Errorf("limit %d gives the catalogue no share at all", limit)
		}
		if share >= limit {
			t.Errorf("limit %d gives the catalogue the whole page (%d)", limit, share)
		}
	}
	// A rail of one is archive.org's: there is no honest way to split it, and
	// halving a one-item rail to show a link would be worse than not.
	if got := vimmShareOf(1); got != 0 {
		t.Errorf("vimmShareOf(1) = %d, want 0", got)
	}
}

// ------------------------------------------------------------------- depth --

// "Load more" must not disappear because the smaller catalogue ran out. This is
// the bug the server-side `more` exists to kill: a short page used to mean the
// end of the category.
func TestLoadMoreSurvivesOneCatalogueRunningOut(t *testing.T) {
	fakeArchiveShelf(t, 40)
	// Ten entries: exhausted on page 1 at a share of 20.
	s := gameBrowseServer(t, snesEntries(10)...)

	_, more, err := s.browsePage(context.Background(), "ia:sys:snes", 60, 1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if !more {
		t.Fatal("no more pages offered, but archive.org returned a full share")
	}

	row2, more2, err := s.browsePage(context.Background(), "ia:sys:snes", 60, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if !more2 {
		t.Error("page 2 says the category ends, but archive.org is still full")
	}
	for _, it := range row2.Items {
		if it.Source == vimmSiteName {
			t.Errorf("catalogue tile %q on page 2; only 10 entries exist and page 1 took them", it.Title)
		}
	}
}

// The other direction: a catalogue with depth left keeps "Load more" even when
// archive.org has run dry.
func TestLoadMoreSurvivesArchiveRunningOut(t *testing.T) {
	fakeArchiveShelf(t, 0) // the Archive holds nothing for this machine
	s := gameBrowseServer(t, snesEntries(50)...)

	_, more, err := s.browsePage(context.Background(), "ia:sys:snes", 60, 1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if !more {
		t.Error("category ends after page 1, but 30 catalogue entries are unread")
	}
}

// Both exhausted is the one case where the answer really is "no more".
func TestAFullyReadCategoryOffersNoMore(t *testing.T) {
	fakeArchiveShelf(t, 0)
	s := gameBrowseServer(t, snesEntries(5)...)

	_, more, err := s.browsePage(context.Background(), "ia:sys:snes", 60, 1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if more {
		t.Error("more pages offered over an empty Archive and a five-entry catalogue")
	}
}

// ---------------------------------------------------------------- grouping --

// Three discs of one game are one game. Search already groups them; a browse
// page that listed them separately would be a second answer to "how many of
// this is there".
func TestDiscsOfOneGameAreOneTile(t *testing.T) {
	s := gameBrowseServer(t,
		vimmEntry{VaultID: "1", Title: "Final Fantasy VII (Disc 1)", Platform: "PlayStation",
			System: "psx", Page: "https://vimm.net/vault/1",
			Download: "https://dl3.vimm.net/?mediaId=1"},
		vimmEntry{VaultID: "2", Title: "Final Fantasy VII (Disc 2)", Platform: "PlayStation",
			System: "psx", Page: "https://vimm.net/vault/2",
			Download: "https://dl3.vimm.net/?mediaId=2"},
		vimmEntry{VaultID: "3", Title: "Final Fantasy VII (Disc 3)", Platform: "PlayStation",
			System: "psx", Page: "https://vimm.net/vault/3",
			Download: "https://dl3.vimm.net/?mediaId=3"},
	)
	works := s.vimm.worksBySystem("psx")
	if len(works) != 1 {
		t.Fatalf("got %d tiles for one three-disc game: %+v", len(works), works)
	}
	if works[0].Page != "https://vimm.net/vault/1" {
		t.Errorf("the tile opens %q; it should open the lowest-numbered disc's page", works[0].Page)
	}
}

// A machine this site has no slug for has no category page, so its entries
// belong on none. They stay findable by title -- that is vimm.go's decision --
// but they must not leak onto somebody else's shelf.
func TestEntriesWithNoSlugReachNoCategoryPage(t *testing.T) {
	s := gameBrowseServer(t,
		vimmEntry{VaultID: "1", Title: "Shenmue", Platform: "Dreamcast", System: "",
			Page: "https://vimm.net/vault/1", Download: "https://dl3.vimm.net/?mediaId=1"},
		vimmEntry{VaultID: "2", Title: "Super Mario World", Platform: "Super Nintendo",
			System: "snes", Page: "https://vimm.net/vault/2",
			Download: "https://dl3.vimm.net/?mediaId=2"},
	)
	if n := len(s.vimm.worksBySystem("")); n != 0 {
		t.Errorf("the empty slug returned %d works; it is not a machine", n)
	}
	for _, slug := range []string{"snes", "genesis", "psx"} {
		for _, w := range s.vimm.worksBySystem(slug) {
			if w.Title == "Shenmue" {
				t.Errorf("a Dreamcast entry surfaced on the %s page", slug)
			}
		}
	}
}

// An entry the import could not verify a target for has nothing to click, and
// this site's rule is that such a tile is not shown.
func TestAnEntryWithNoTargetIsNotBrowsable(t *testing.T) {
	s := gameBrowseServer(t,
		vimmEntry{VaultID: "1", Title: "Nothing To Click", Platform: "Super Nintendo",
			System: "snes", Page: "https://vimm.net/vault/1"},
	)
	if n := len(s.vimm.worksBySystem("snes")); n != 0 {
		t.Errorf("published %d tiles for an entry with no play or download URL", n)
	}
}

// ------------------------------------------------------------------ counts --

// A machine archive.org holds nothing for used to be dropped from the tree as
// empty. If the catalogue has games for it, it is a real shelf.
func TestAMachineOnlyTheCatalogueHasStillGetsAPage(t *testing.T) {
	s := gameBrowseServer(t, snesEntries(41)...)
	counts := s.allCounts()
	if counts["snes"] != 41 {
		t.Fatalf("snes counted %d, want 41 from the catalogue alone", counts["snes"])
	}

	var found bool
	for _, d := range s.categoryTree(counts) {
		for _, g := range d.Groups {
			for _, c := range g.Categories {
				if c.Path == "games/snes" {
					found, _ = true, c
					if c.Count != 41 {
						t.Errorf("the SNES tile says %d items, want 41", c.Count)
					}
				}
			}
		}
	}
	if !found {
		t.Error("the SNES category is missing from the tree despite holding 41 games")
	}
}

// The number on the page has to count every catalogue behind it, or it is
// visibly wrong the moment somebody pages to the end.
func TestACategoryTotalCountsBothCatalogues(t *testing.T) {
	s := gameBrowseServer(t, snesEntries(50)...)
	s.browse.putCounts(map[string]int{"snes": 547})
	if got := s.categoryTotal("ia:sys:snes"); got != 597 {
		t.Errorf("total = %d, want 547 from archive.org + 50 from the catalogue", got)
	}
	// A non-game category has one catalogue and must be untouched.
	s.browse.putCounts(map[string]int{"ia:col:gutenberg": 56051})
	if got := s.categoryTotal("ia:col:gutenberg"); got != 56051 {
		t.Errorf("Gutenberg total = %d, want 56051", got)
	}
}

// ---------------------------------------------------------------- outages --

// The catalogue is a file on this box. An Archive outage should cost the
// Archive's share of a page, not the page -- and a half-page must not then be
// cached for three hours and outlive the outage.
func TestAnArchiveOutageStillServesTheCatalogue(t *testing.T) {
	deadArchive(t)
	s := gameBrowseServer(t, snesEntries(50)...)

	row, _, err := s.browsePage(context.Background(), "ia:sys:snes", 60, 1)
	if err != nil {
		t.Fatalf("the whole page failed on an Archive outage: %v", err)
	}
	if len(row.Items) == 0 {
		t.Fatal("no items served, though the catalogue holds 50 for this machine")
	}
	for _, it := range row.Items {
		if it.Source != vimmSiteName {
			t.Errorf("%q came from somewhere other than the catalogue", it.Title)
		}
	}
	if _, cached := s.browse.row("ia:sys:snes\x0060\x001"); cached {
		t.Error("the degraded page was cached; it would outlive the outage that caused it")
	}
}

// A machine with neither catalogue behind it is still an error rather than an
// empty page, so the client says "could not load" instead of "nothing here".
func TestAnOutageWithNoCatalogueIsStillAnError(t *testing.T) {
	deadArchive(t)
	s := gameBrowseServer(t) // no entries at all

	if _, _, err := s.browsePage(context.Background(), "ia:sys:snes", 60, 1); err == nil {
		t.Error("an Archive outage with an empty catalogue reported success")
	}
}

// A server that has never imported a catalogue is a normal deployment, and
// every path through this must behave exactly as it did before Vimm existed.
func TestNoCatalogueBehavesAsBefore(t *testing.T) {
	fakeArchiveShelf(t, 40)
	s := browseTestServer() // s.vimm is nil

	row, more, err := s.browsePage(context.Background(), "ia:sys:snes", 60, 1)
	if err != nil {
		t.Fatalf("browsePage: %v", err)
	}
	if len(row.Items) == 0 {
		t.Fatal("no items")
	}
	for _, it := range row.Items {
		if it.External != nil {
			t.Errorf("%q carries External with no catalogue loaded", it.Title)
		}
	}
	if !more {
		t.Error("a full archive.org page offered no more")
	}
	if got := s.categoryTotal("ia:sys:snes"); got != 0 {
		t.Errorf("total = %d with nothing counted yet", got)
	}
	if n := len(s.allCounts()); n != 0 {
		t.Errorf("allCounts returned %d entries on a cold process", n)
	}
}

// ------------------------------------------------------------- interleave --

// interleave is the one piece of arithmetic here, so it is pinned directly:
// nothing may be dropped, and the minor list must be spread rather than
// clumped at either end.
func TestInterleaveKeepsEverything(t *testing.T) {
	for _, tc := range []struct{ major, minor int }{
		{40, 20}, {40, 4}, {5, 5}, {1, 3}, {0, 3}, {3, 0}, {60, 1},
	} {
		major := make([]discoverItm, tc.major)
		for i := range major {
			major[i] = discoverItm{Title: fmt.Sprintf("A%d", i)}
		}
		minor := make([]discoverItm, tc.minor)
		for i := range minor {
			minor[i] = discoverItm{Title: fmt.Sprintf("V%d", i), Source: vimmSiteName}
		}
		out := interleave(major, minor)
		if len(out) != tc.major+tc.minor {
			t.Errorf("major=%d minor=%d produced %d items", tc.major, tc.minor, len(out))
		}
		seen := map[string]bool{}
		for _, it := range out {
			if seen[it.Title] {
				t.Errorf("major=%d minor=%d duplicated %q", tc.major, tc.minor, it.Title)
			}
			seen[it.Title] = true
		}
	}
}

// The number on a category tile and the tiles the page can actually produce
// are computed by two different functions, and a count that promised more than
// the page could show would be a lie that only appears at the last page.
func TestTheCountAndThePageAgreeExactly(t *testing.T) {
	s := gameBrowseServer(t,
		// Three discs of one game: one work.
		vimmEntry{VaultID: "1", Title: "Final Fantasy VII (Disc 1)", Platform: "PlayStation",
			System: "psx", Page: "https://vimm.net/vault/1", Download: "https://dl3.vimm.net/?mediaId=1"},
		vimmEntry{VaultID: "2", Title: "Final Fantasy VII (Disc 2)", Platform: "PlayStation",
			System: "psx", Page: "https://vimm.net/vault/2", Download: "https://dl3.vimm.net/?mediaId=2"},
		// A second work on the same machine.
		vimmEntry{VaultID: "3", Title: "Tekken 3", Platform: "PlayStation",
			System: "psx", Page: "https://vimm.net/vault/3", Download: "https://dl3.vimm.net/?mediaId=3"},
		// No verified target: not browsable, so not counted.
		vimmEntry{VaultID: "4", Title: "Ghost", Platform: "PlayStation",
			System: "psx", Page: "https://vimm.net/vault/4"},
		// No addressable page: not browsable, so not counted.
		vimmEntry{VaultID: "5", Title: "Pageless", Platform: "PlayStation",
			System: "psx", Download: "https://dl3.vimm.net/?mediaId=5"},
		// A different machine entirely.
		vimmEntry{VaultID: "6", Title: "Super Mario World", Platform: "Super Nintendo",
			System: "snes", Page: "https://vimm.net/vault/6", Download: "https://dl3.vimm.net/?mediaId=6"},
	)
	counts := s.vimm.vimmSystemCounts()
	for _, slug := range []string{"psx", "snes"} {
		if got, want := counts[slug], len(s.vimm.worksBySystem(slug)); got != want {
			t.Errorf("%s: counted %d, page produces %d", slug, got, want)
		}
	}
	if counts["psx"] != 2 {
		t.Errorf("psx counted %d, want 2 (three discs are one game; two rows are unpublishable)", counts["psx"])
	}
}

// A work whose lowest-numbered row lost its page URL is still a work. Taking
// the lowest id unconditionally would drop the whole game because disc 1
// happened to be the row that failed the import's URL check.
func TestAWorkSurvivesItsFirstRowHavingNoPage(t *testing.T) {
	s := gameBrowseServer(t,
		vimmEntry{VaultID: "1", Title: "Chrono Cross (Disc 1)", Platform: "PlayStation",
			System: "psx", Download: "https://dl3.vimm.net/?mediaId=1"},
		vimmEntry{VaultID: "2", Title: "Chrono Cross (Disc 2)", Platform: "PlayStation",
			System: "psx", Page: "https://vimm.net/vault/2", Download: "https://dl3.vimm.net/?mediaId=2"},
	)
	works := s.vimm.worksBySystem("psx")
	if len(works) != 1 {
		t.Fatalf("got %d works, want 1", len(works))
	}
	if works[0].Page != "https://vimm.net/vault/2" {
		t.Errorf("the tile opens %q; it should fall through to the row that has a page", works[0].Page)
	}
	if n := s.vimm.vimmSystemCounts()["psx"]; n != 1 {
		t.Errorf("counted %d, want 1 -- the count must agree with the page", n)
	}
}

// /api/categories is what the browse landing page blocks on, and it now asks
// the catalogue how much each machine holds. Answering that per request costs a
// full scan plus a title normalisation per row -- 41 ms for a 23,000-row
// catalogue when this was first written, on the paint path.
//
// The count is computed once when the catalogue is swapped in instead. This
// benchmark is the guard on that: if it ever climbs back into milliseconds,
// somebody has moved the work back onto the request.
func BenchmarkCategoryCountsAtFullCatalogueSize(b *testing.B) {
	systems := []string{"snes", "nes", "genesis", "psx", "gb", "gbc", "gba", "n64", "sms",
		"gamegear", "atari2600", "atari5200", "atari7800", "lynx", "tg16", "sega32", "nds"}
	// 23,000 rows is what Wade's scan is expected to reach; 5,586 is today's.
	entries := make([]vimmEntry, 0, 23000)
	for i := 1; i <= 23000; i++ {
		entries = append(entries, vimmEntry{
			VaultID: fmt.Sprint(i), Title: fmt.Sprintf("Game %05d", i),
			Platform: "X", System: systems[i%len(systems)],
			Page:     fmt.Sprintf("https://vimm.net/vault/%d", i),
			Download: fmt.Sprintf("https://dl3.vimm.net/?mediaId=%d", i),
		})
	}
	st := &vimmStore{}
	st.replace(vimmCatalogue{Version: vimmCatalogueVersion, Entries: entries})
	s := &server{browse: newBrowseCache(), vimm: st}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.allCounts()
	}
}
