package main

import (
	"net/url"
	"strings"
	"testing"
)

// Comics (7030) sit inside the books range (7000-8000), so whichever bucket is
// scanned first would otherwise claim every comic as a book.
func TestComicsAreNotSwallowedByBooks(t *testing.T) {
	if got := groupForCategory(7030); got != "comics" {
		t.Errorf("7030 = %q, want comics", got)
	}
	if got := groupForCategory(7020); got != "books" {
		t.Errorf("7020 = %q, want books", got)
	}
	// Anime inside the TV range must still work, which is the same rule.
	if got := groupForCategory(5070); got != "anime" {
		t.Errorf("5070 = %q, want anime", got)
	}
	if got := groupForCategory(5010); got != "tv" {
		t.Errorf("5010 = %q, want tv", got)
	}
}

// Picking one category should narrow the indexer query itself, not just hide
// rows after the fact.
func TestASingleCategoryNarrowsTheIndexerQuery(t *testing.T) {
	kind := func(groups string) string {
		return kindFor(url.Values{"groups": {groups}})
	}
	if got := kind("games"); got != "game" {
		t.Errorf("games -> %q", got)
	}
	if got := kind("comics"); got != "comic" {
		t.Errorf("comics -> %q", got)
	}
	if got := kind("movies"); got != "video" {
		t.Errorf("movies -> %q", got)
	}
	// Several at once imply no single Newznab bucket.
	if got := kind("games,movies"); got != "" {
		t.Errorf("two categories -> %q, want no narrowing", got)
	}
	// An explicit kind always wins. Written as the canonical form of whatever
	// was asked for, not a literal: "audio" is an accepted spelling of the
	// music domain, and hard-coding either side of that is what let the client
	// and server disagree in the first place.
	want := canonicalDomain("audio")
	if got := kindFor(url.Values{"kind": {"audio"}, "groups": {"games"}}); got != want {
		t.Errorf("explicit kind -> %q, want %q", got, want)
	}
}

// A category with no words is a browse; `title:("")` is a Solr syntax error.
func TestAnEmptyTermBrowsesRatherThanErroring(t *testing.T) {
	q := archiveQuery("")
	if strings.Contains(q, `title:`) {
		t.Errorf("empty term should not build a title clause: %s", q)
	}
	if !strings.Contains(q, "emulator:[* TO *]") {
		t.Errorf("browse lost its scope: %s", q)
	}
}

// "books" and "other" both map to kind "", so without the categories in the
// key one cache entry answered for every category browse.
func TestEachCategoryBrowseGetsItsOwnCacheEntry(t *testing.T) {
	key := func(vals url.Values) string {
		f := parseFilters(vals)
		return f.Kind + "\x00" + strings.ToLower(vals.Get("q")) + "\x00" + strings.Join(f.Groups, ",")
	}
	books := key(url.Values{"groups": {"books"}})
	other := key(url.Values{"groups": {"other"}})
	if books == other {
		t.Errorf("books and other share a cache key: %q", books)
	}
	// And a browse must not collide with a text search.
	if key(url.Values{"groups": {"games"}}) == key(url.Values{"q": {"zelda"}}) {
		t.Error("a category browse collides with a text search")
	}
}

// A hosted backup and a torrent behave so differently -- one always plays, one
// might not -- that mixing them with no way to choose makes the list harder to
// reason about.
func TestSourceFilterSeparatesBackupsFromTorrents(t *testing.T) {
	cards := []card{
		{Title: "Contra", Kind: "game", Instant: true,
			Sources: []source{{Indexer: "Archive.org"}}},
		{Title: "Contra ROM Set", Seeders: 40,
			Sources: []source{{Indexer: "TPB", Seeders: 40}}},
	}
	only := func(src string) []card {
		return filters{Source: src, Sort: "seeders"}.apply(cards)
	}
	if got := only("instant"); len(got) != 1 || !got[0].Instant {
		t.Errorf("instant filter returned %d cards", len(got))
	}
	if got := only("swarm"); len(got) != 1 || got[0].Instant {
		t.Errorf("swarm filter returned %d cards", len(got))
	}
	if got := only(""); len(got) != 2 {
		t.Errorf("no filter should return both, got %d", len(got))
	}
	f := buildFacets(cards)
	if f.InstantCount != 1 || f.SwarmCount != 1 {
		t.Errorf("facet counts wrong: instant=%d swarm=%d", f.InstantCount, f.SwarmCount)
	}
}
