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
	// An explicit kind always wins.
	if got := kindFor(url.Values{"kind": {"audio"}, "groups": {"games"}}); got != "audio" {
		t.Errorf("explicit kind -> %q", got)
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
