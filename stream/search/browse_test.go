package main

import (
	"net/url"
	"strings"
	"testing"
)

func browseTestServer() *server {
	return &server{tmdb: newTMDB(""), browse: newBrowseCache()}
}

// A category is a page somebody can link to, so its URL has to resolve on a
// cold load -- not only when the app navigates to it from inside.
func TestACategoryPathResolvesToItsRow(t *testing.T) {
	s := browseTestServer()
	for path, want := range map[string]string{
		"games/snes":      "ia:sys:snes",
		"games/c64":       "ia:sys:c64",
		"games/atari8bit": "ia:sys:atari8bit",
		"games/arcade":    "ia:sys:arcade",
		"books/gutenberg": "ia:col:gutenberg",
		"comics/fawcett":  "ia:col:fawcett",
		"music/etree":     "ia:col:etree",
		"movies/silent":   "ia:col:silent",
		"tv/classic-tv":   "ia:col:classic-tv",
		"/games/snes/":    "ia:sys:snes", // slashes either side are tolerated
	} {
		got, ok := s.keyForPath(path)
		if !ok || got != want {
			t.Errorf("keyForPath(%q) = %q,%v; want %s", path, got, ok, want)
		}
	}
}

// A path is resolved against the registry, never used to build a query. This is
// the boundary: everything else here trusts that nothing arbitrary gets past.
func TestAPathThatIsNotACategoryDoesNotResolve(t *testing.T) {
	s := browseTestServer()
	for _, bad := range []string{
		"", "games", "games/", "/", "games/snes/extra", "games/nintendo-switch",
		"books/snes",   // right slug, wrong domain
		"movies/etree", // a music collection asked for under movies
		"games/../../etc", "books/) OR collection:(nsfw", "nonsense/nonsense",
		"movies/action", // TMDB genres need a configured key; without one, nothing
	} {
		if key, ok := s.keyForPath(bad); ok {
			t.Errorf("path %q resolved to %q; it must not", bad, key)
		}
	}
}

// The URL a person sends someone has to be the one the tree publishes, and no
// two categories may claim the same page.
func TestEveryPublishedCategoryPathResolvesBack(t *testing.T) {
	s := browseTestServer()
	counts := map[string]int{}
	for i := range gameSystems {
		counts[gameSystems[i].ID] = 1
	}
	seen := map[string]bool{}
	for _, d := range s.categoryTree(counts) {
		for _, g := range d.Groups {
			for _, c := range g.Categories {
				if c.Path == "" {
					t.Errorf("%s has no path", c.Key)
					continue
				}
				if seen[c.Path] {
					t.Errorf("two categories claim %q", c.Path)
				}
				seen[c.Path] = true
				if !strings.HasPrefix(c.Path, d.Key+"/") {
					t.Errorf("%s is published under domain %s", c.Path, d.Key)
				}
				key, ok := s.keyForPath(c.Path)
				if !ok || key != c.Key {
					t.Errorf("path %q resolved to %q, want %q", c.Path, key, c.Key)
				}
			}
		}
	}
}

// The domain a URL names has to be the same word the category chips use, or a
// visitor learns two vocabularies for one site.
func TestDomainKeysMatchTheSitesOwnCategoryNames(t *testing.T) {
	known := map[string]bool{
		"movies": true, "tv": true, "anime": true, "music": true,
		"games": true, "comics": true, "apps": true, "books": true, "other": true,
	}
	s := browseTestServer()
	for _, d := range s.categoryTree(map[string]int{}) {
		if !known[d.Key] {
			t.Errorf("domain %q is not one of the site's categories", d.Key)
		}
	}
	for _, c := range iaCollections {
		if !known[c.Domain] {
			t.Errorf("collection %s is filed under %q, which is not a category", c.Slug, c.Domain)
		}
	}
}

func TestSlugsAreWhatSomebodyWouldGuess(t *testing.T) {
	for in, want := range map[string]string{
		"Action":             "action",
		"Science Fiction":    "science-fiction",
		"Sci-Fi & Fantasy":   "sci-fi-fantasy",
		"War & Politics":     "war-politics",
		"TV Movie":           "tv-movie",
		"  Kids  ":           "kids",
		"Action & Adventure": "action-adventure",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// A row key is looked up, never interpolated. Anything not in the registry has
// to fail closed, because the alternative is a query string a caller wrote.
func TestRowKeysResolveOnlyFromTheRegistry(t *testing.T) {
	s := browseTestServer()
	for _, bad := range []string{
		"", "nonsense", "ia:sys", "ia:sys:nintendo-switch",
		"ia:col:) OR collection:(nsfw", "tmdb:movie:abc", "tmdb:movie:-1",
		"ia:raw:collection:(everything)", "sys:ia:snes",
	} {
		if _, ok := s.sourceFor(bad); ok {
			t.Errorf("key %q resolved; it must not", bad)
		}
	}
	src, ok := s.sourceFor("ia:sys:snes")
	if !ok {
		t.Fatal("ia:sys:snes did not resolve")
	}
	if src.title != "Super Nintendo" {
		t.Errorf("title = %q, want the name a person reads", src.title)
	}
	if src.mediaType != "game" || !strings.Contains(src.solr, `emulator:("snes"`) {
		t.Errorf("source = %+v", src)
	}
}

// Every collection in the tree was probed live before it was written down.
// This is the structural half: one with no mediatype or no domain would build
// a query that matches nothing.
func TestEveryPublishedCollectionIsWellFormed(t *testing.T) {
	s := browseTestServer()
	seen := map[string]bool{}
	for _, c := range iaCollections {
		if seen[c.Slug] {
			t.Errorf("duplicate slug %q", c.Slug)
		}
		seen[c.Slug] = true
		if c.Collection == "" || c.MediaType == "" || c.Domain == "" || c.Title == "" {
			t.Errorf("%s is incomplete: %+v", c.Slug, c)
		}
		src, ok := s.sourceFor("ia:col:" + c.Slug)
		if !ok {
			t.Fatalf("ia:col:%s does not resolve", c.Slug)
		}
		if !strings.Contains(src.solr, "collection:("+c.Collection+")") {
			t.Errorf("%s query = %q", c.Slug, src.solr)
		}
	}
}

// The tree is what a domain page is drawn from, so it must be buildable with
// no upstream call at all -- that is what makes opening one instant.
func TestTheTreeIsBuiltWithoutTouchingTheNetwork(t *testing.T) {
	s := browseTestServer()
	tree := s.categoryTree(map[string]int{}) // counts as they are on a cold process
	if len(tree) == 0 {
		t.Fatal("an empty count map produced an empty tree; a cold process would serve nothing")
	}
	var games *browseDomain
	for i := range tree {
		if tree[i].Key == "games" {
			games = &tree[i]
		}
	}
	if games == nil {
		t.Fatal("no games domain")
	}
	if len(games.Groups) < 5 {
		t.Errorf("games has %d groups, want the manufacturers", len(games.Groups))
	}
}

// A category that would open onto an empty page is worse than no category.
// Small is not the same as empty: an 18-item shelf is a real shelf.
func TestEmptyCategoriesAreNotPublishedButSmallOnesAre(t *testing.T) {
	s := browseTestServer()
	counts := map[string]int{"snes": 548, "nes": 544, "mega-duck-slash-cougar-boy": 18, "n64": 0}
	var got []string
	for _, d := range s.categoryTree(counts) {
		for _, g := range d.Groups {
			for _, c := range g.Categories {
				got = append(got, c.Path)
			}
		}
	}
	has := func(p string) bool {
		for _, g := range got {
			if g == p {
				return true
			}
		}
		return false
	}
	if has("games/n64") {
		t.Error("a machine with zero items was published")
	}
	if !has("games/mega-duck-slash-cougar-boy") {
		t.Error("an 18-item machine was dropped; small is not empty")
	}
}

// Ordering is the whole reason a domain page is usable: the first thing under
// Commodore should be the 99,993-item C64, not the one-item VIC-20.
func TestBiggestCategoriesLeadTheirGroup(t *testing.T) {
	s := browseTestServer()
	counts := map[string]int{"c64": 99993, "amiga": 13261, "cpet": 381, "vic-20": 1}
	for _, d := range s.categoryTree(counts) {
		for _, g := range d.Groups {
			if g.Key != famCommodore {
				continue
			}
			want := []string{"games/c64", "games/amiga", "games/cpet", "games/vic-20"}
			if len(g.Categories) != len(want) {
				t.Fatalf("got %d categories, want %d", len(g.Categories), len(want))
			}
			for i, p := range want {
				if g.Categories[i].Path != p {
					t.Errorf("position %d = %s, want %s", i, g.Categories[i].Path, p)
				}
			}
		}
	}
}

// The tree says which categories this site's own player can run, because that
// is the difference between a game with a touch pad and one you can only watch
// on a phone. Answered by play_archive.go, not asserted here.
func TestTheTreeSaysWhatOurPlayerCanRun(t *testing.T) {
	s := browseTestServer()
	counts := map[string]int{
		"snes": 548, "intellivision": 195, "psx": 2740, "arcade": 2662, "dos": 36165,
	}
	got := map[string]bool{}
	for _, d := range s.categoryTree(counts) {
		for _, g := range d.Groups {
			for _, c := range g.Categories {
				got[c.Path] = c.Plays
			}
		}
	}
	if !got["games/snes"] {
		t.Error("SNES should play here")
	}
	for _, p := range []string{"games/intellivision", "games/psx", "games/arcade", "games/dos"} {
		if got[p] {
			t.Errorf("%s claims to play here and play_archive.go says it cannot", p)
		}
	}
}

// The facet exists so the UI never offers a filter that returns nothing.
func TestTheSystemFacetOnlyListsMachinesInTheResults(t *testing.T) {
	f := buildFacets([]card{
		{System: "snes", Platform: "SNES", Sources: []source{{}}},
		{System: "snes", Platform: "SNES", Sources: []source{{}}},
		{System: "genesis", Platform: "Genesis", Sources: []source{{}}},
		{Sources: []source{{}}}, // a film: no machine at all
	})
	if len(f.Systems) != 2 {
		t.Fatalf("facet lists %d machines, want the 2 present: %+v", len(f.Systems), f.Systems)
	}
	if f.Systems[0].Value != "snes" || f.Systems[0].Count != 2 {
		t.Errorf("leading facet = %+v, want snes x2", f.Systems[0])
	}
	// The chip has to read "Super Nintendo", not "snes".
	if f.Systems[0].Label != "Super Nintendo" {
		t.Errorf("label = %q", f.Systems[0].Label)
	}
	// No games, no facet at all -- an empty array is "none present", and a
	// client must not read it as "show everything".
	if got := buildFacets([]card{{Sources: []source{{}}}}); got.Systems != nil {
		t.Errorf("a result set with no games published a system facet: %+v", got.Systems)
	}
}

// Narrowing to a machine is narrowing to games. Letting through everything
// without a machine would leave every film that shares the query on the page.
func TestFilteringBySystemKeepsOnlyThatMachine(t *testing.T) {
	cards := []card{
		{Key: "a", Title: "Super Mario World", System: "snes", Instant: true, Sources: []source{{WebSafe: true}}},
		{Key: "b", Title: "Super Mario Bros", System: "nes", Instant: true, Sources: []source{{WebSafe: true}}},
		{Key: "c", Title: "The Super Mario Bros Movie", Sources: []source{{Seeders: 900}}},
	}
	got := parseFilters(url.Values{"system": {"snes"}}).apply(cards)
	if len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("got %d cards %v, want only the SNES one", len(got), got)
	}
	viaAlias := parseFilters(url.Values{"system": {"super nintendo"}}).apply(cards)
	if len(viaAlias) != 1 || viaAlias[0].Key != "a" {
		t.Errorf("the alias did not reach the same results: %v", viaAlias)
	}
	if len(parseFilters(url.Values{}).apply(cards)) != 3 {
		t.Error("an unfiltered search lost cards")
	}
}

// The facet is built from the cached, UNNARROWED result set. If a scoped search
// were ever written into that cache, the next unfiltered visitor would get a
// "mario" page containing only SNES games and a facet claiming SNES is the only
// machine Mario ever appeared on. filter.go's `Lang` comment describes exactly
// this trap for its own filter; this one deepens as well as narrows, so it has
// to be careful in both directions.
func TestTheFacetDescribesTheWholeResultSetNotTheNarrowedOne(t *testing.T) {
	cached := []card{
		{Key: "a", System: "snes", Sources: []source{{}}},
		{Key: "b", System: "genesis", Sources: []source{{}}},
		{Key: "c", System: "nes", Sources: []source{{}}},
	}
	if n := len(buildFacets(cached).Systems); n != 3 {
		t.Fatalf("facet lists %d machines, want all 3 in the cache", n)
	}
	if got := parseFilters(url.Values{"system": {"snes"}}).apply(cached); len(got) != 1 {
		t.Errorf("narrowed to %d cards, want 1", len(got))
	}
	if n := len(buildFacets(cached).Systems); n != 3 {
		t.Error("filtering mutated the set the facet is built from")
	}
}

// A count of zero means "not measured yet" on a cold process, and hiding
// everything then would serve an empty tree for the first minute of its life.
func TestACollectionIsShownBeforeItsCountIsKnown(t *testing.T) {
	s := browseTestServer()
	found := false
	for _, d := range s.categoryTree(map[string]int{}) {
		for _, g := range d.Groups {
			for _, c := range g.Categories {
				if c.Path == "books/gutenberg" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("a cold process published no collections at all")
	}
}

// The find box on a domain page filters a list already in the browser, so the
// alias has to travel with the tree. Without it "megadrive" matched nothing --
// the title says "Mega Drive" with a space.
func TestCategoriesCarryTheirOtherNames(t *testing.T) {
	s := browseTestServer()
	counts := map[string]int{"genesis": 12975, "nes": 543, "zxs": 12332}
	found := map[string]string{}
	for _, d := range s.categoryTree(counts) {
		for _, g := range d.Groups {
			for _, c := range g.Categories {
				found[c.Path] = c.Find
			}
		}
	}
	for path, word := range map[string]string{
		"games/genesis": "megadrive",
		"games/nes":     "famicom",
		"games/zxs":     "speccy",
	} {
		if !strings.Contains(found[path], word) {
			t.Errorf("%s does not publish %q: %q", path, word, found[path])
		}
	}
}
