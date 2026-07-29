package main

import (
	"strings"
	"testing"
)

func mkCards() []card {
	return []card{
		{Title: "Alpha", Seeders: 100, Sources: []source{
			{Title: "Alpha 2160p x265", Quality: "2160p", Codec: "x265", Seeders: 100, Size: 20 << 30, Indexer: "TPB", WebSafe: false},
			{Title: "Alpha 1080p x264", Quality: "1080p", Codec: "x264", Seeders: 40, Size: 2 << 30, Indexer: "YTS", WebSafe: true},
		}},
		{Title: "Beta", Seeders: 3, Sources: []source{
			{Title: "Beta 720p x264", Quality: "720p", Codec: "x264", Seeders: 3, Size: 700 << 20, Indexer: "YTS", WebSafe: true},
		}},
		{Title: "Gamma", Seeders: 0, Sources: []source{
			{Title: "Gamma 1080p x265", Quality: "1080p", Codec: "x265", Seeders: 0, Size: 5 << 30, Indexer: "TPB", WebSafe: false},
		}},
	}
}

func TestFilterMinSeedersDropsDeadCards(t *testing.T) {
	got := filters{MinSeeders: 1, Sort: "seeders"}.apply(mkCards())
	for _, c := range got {
		if c.Title == "Gamma" {
			t.Error("Gamma has 0 seeders and should have been dropped entirely")
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d cards, want 2", len(got))
	}
}

func TestFilterWebSafeOnly(t *testing.T) {
	got := filters{WebSafe: true, Sort: "seeders"}.apply(mkCards())
	for _, c := range got {
		for _, s := range c.Sources {
			if !s.WebSafe {
				t.Errorf("%s kept a non-websafe source %q", c.Title, s.Title)
			}
		}
	}
	// Alpha keeps only its x264 source, so its seeder count must drop to 40.
	for _, c := range got {
		if c.Title == "Alpha" && c.Seeders != 40 {
			t.Errorf("Alpha seeders = %d, want 40 after filtering", c.Seeders)
		}
	}
}

func TestFilterSizeRange(t *testing.T) {
	got := filters{MinSizeMB: 1024, MaxSizeMB: 4096, Sort: "seeders"}.apply(mkCards())
	for _, c := range got {
		for _, s := range c.Sources {
			mb := s.Size / (1024 * 1024)
			if mb < 1024 || mb > 4096 {
				t.Errorf("%s kept %d MB, outside range", c.Title, mb)
			}
		}
	}
}

func TestFilterQualityAndCodec(t *testing.T) {
	got := filters{Qualities: []string{"1080p"}, Codecs: []string{"x264"}, Sort: "seeders"}.apply(mkCards())
	if len(got) != 1 || got[0].Title != "Alpha" {
		t.Fatalf("got %v, want just Alpha", titles(got))
	}
	if len(got[0].Sources) != 1 || got[0].Sources[0].Quality != "1080p" {
		t.Errorf("wrong source kept: %+v", got[0].Sources)
	}
}

func TestSortBySize(t *testing.T) {
	got := filters{Sort: "size"}.apply(mkCards())
	if got[0].Title != "Alpha" {
		t.Errorf("largest first failed: %v", titles(got))
	}
}

func TestSortByTitle(t *testing.T) {
	got := filters{Sort: "title"}.apply(mkCards())
	if got[0].Title != "Alpha" || got[len(got)-1].Title != "Gamma" {
		t.Errorf("alphabetical sort failed: %v", titles(got))
	}
}

func TestFacetsReportOnlyPresentValues(t *testing.T) {
	f := buildFacets(mkCards())
	if f.MaxSeed != 100 {
		t.Errorf("MaxSeed = %d, want 100", f.MaxSeed)
	}
	if len(f.Qualities) != 3 {
		t.Errorf("qualities = %v, want 3 distinct", f.Qualities)
	}
	// Highest quality must lead so the UI lists 2160p first.
	if f.Qualities[0].Value != "2160p" {
		t.Errorf("facet order wrong: %v", f.Qualities)
	}
}

func titles(cards []card) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Title
	}
	return out
}

// Raw seeder count ranks language packs, course recordings and porn rips above
// the film someone searched for. A confirmed TMDB match must win.
func TestRelevanceRanksRealMatchesAboveWellSeededJunk(t *testing.T) {
	cards := []card{
		{Title: "Udemy - Lucid Dreaming", Seeders: 400,
			Sources: []source{{Seeders: 400, Quality: "720p"}}},
		{Title: "Inception XXX Parody", Seeders: 250,
			Sources: []source{{Seeders: 250, Quality: "1080p"}}},
		{Title: "Inception", Year: 2010, Seeders: 90,
			Art:     artwork{Found: true, Poster: "p.jpg", Rating: 8.4},
			Sources: []source{{Seeders: 90, Quality: "1080p"}, {Seeders: 40}, {Seeders: 20}, {Seeders: 9}, {Seeders: 3}}},
	}
	got := filters{Sort: "relevance"}.apply(cards)
	if got[0].Title != "Inception" {
		t.Errorf("top result = %q, want the actual film (order: %v)", got[0].Title, titles(got))
	}
}

func TestExplicitSeedersSortStillPureSeeders(t *testing.T) {
	cards := []card{
		{Title: "Junk", Seeders: 400, Sources: []source{{Seeders: 400}}},
		{Title: "Film", Seeders: 90, Art: artwork{Found: true}, Sources: []source{{Seeders: 90}}},
	}
	got := filters{Sort: "seeders"}.apply(cards)
	if got[0].Title != "Junk" {
		t.Errorf("explicit seeders sort should be literal; got %v", titles(got))
	}
}

// The exact failure seen live: searching "inception" ranked American Pie third,
// because TMDB had correctly identified it and artwork dominated the score.
// Being a real film is not the same as being the film that was asked for.
func TestRelevanceKeepsUnrelatedFilmsBelowTheQuery(t *testing.T) {
	cards := []card{
		{Title: "American Pie", Year: 1999, Seeders: 11,
			Art:     artwork{Found: true, Poster: "p.jpg", Rating: 6.6},
			Sources: []source{{Seeders: 11}}},
		{Title: "Inception", Year: 2010, Seeders: 285,
			Art:     artwork{Found: true, Poster: "p.jpg", Rating: 8.4},
			Sources: []source{{Seeders: 285}, {Seeders: 90}, {Seeders: 40}, {Seeders: 8}, {Seeders: 2}}},
		{Title: "Inception Of Eternity - Nature Always Wins", Seeders: 12,
			Sources: []source{{Seeders: 12}}},
	}
	got := filters{Sort: "relevance", Query: "inception"}.apply(cards)
	if got[0].Title != "Inception" {
		t.Fatalf("top = %q, want Inception (order: %v)", got[0].Title, titles(got))
	}
	// A well-rated, artwork-bearing film that shares no query term must fall
	// below even an unmatched-but-on-topic result.
	last := got[len(got)-1].Title
	if last != "American Pie" {
		t.Errorf("American Pie should rank last for this query; order: %v", titles(got))
	}
}

func TestQueryTermsDropStopWordsAndNoise(t *testing.T) {
	got := queryTerms("The Lord of the Rings!")
	want := []string{"lord", "rings"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestTitleOverlapHandlesPunctuationAndJoining(t *testing.T) {
	cases := []struct {
		title string
		terms []string
		want  int
	}{
		{"Spider-Man: No Way Home", []string{"spiderman"}, 1},
		{"The.Matrix.1999.1080p", []string{"matrix"}, 1},
		{"Inception", []string{"inception"}, 1},
		{"American Pie", []string{"inception"}, 0},
		{"Breaking Bad", []string{"breaking", "bad"}, 2},
	}
	for _, c := range cases {
		if got := titleOverlap(c.title, c.terms); got != c.want {
			t.Errorf("titleOverlap(%q, %v) = %d, want %d", c.title, c.terms, got, c.want)
		}
	}
}

func TestRelevanceIgnoresQueryWhenBrowsing(t *testing.T) {
	cards := []card{
		{Title: "Alpha", Seeders: 5, Art: artwork{Found: true}, Sources: []source{{Seeders: 5}}},
		{Title: "Beta", Seeders: 900, Sources: []source{{Seeders: 900}}},
	}
	// No query at all: nothing should be penalised for not matching.
	got := filters{Sort: "relevance"}.apply(cards)
	if got[0].Title != "Alpha" {
		t.Errorf("with no query the TMDB-matched card should lead; got %v", titles(got))
	}
}

// A porn parody sharing a word with the query legitimately matches the term,
// so term scoring alone cannot separate it from the film people meant.
func TestAdultParodyDoesNotOutrankTheFilm(t *testing.T) {
	cards := []card{
		{Title: "S3XUS E23 Laney Grey Inception XXX", Seeders: 36, Sources: []source{{Seeders: 36}}},
		{Title: "Inception", Year: 2010, Seeders: 30,
			Art: artwork{Found: true, Poster: "p.jpg", Rating: 8.4}, Sources: []source{{Seeders: 30}}},
	}
	got := filters{Sort: "relevance", Query: "inception"}.apply(cards)
	if got[0].Title != "Inception" {
		t.Errorf("top = %q, want the film; order %v", got[0].Title, titles(got))
	}
}

// ...but if that is what was searched for, it must not be demoted.
func TestAdultResultsNotDemotedWhenAskedFor(t *testing.T) {
	cards := []card{
		{Title: "Some Movie", Year: 2010, Seeders: 5,
			Art: artwork{Found: true}, Sources: []source{{Seeders: 5}}},
		{Title: "Brazzers Collection", Seeders: 40, Sources: []source{{Seeders: 40}}},
	}
	got := filters{Sort: "relevance", Query: "brazzers"}.apply(cards)
	if got[0].Title != "Brazzers Collection" {
		t.Errorf("top = %q, want the asked-for result; order %v", got[0].Title, titles(got))
	}
}

func TestAdultHiddenByDefaultAndShownOnRequest(t *testing.T) {
	cards := []card{
		{Title: "Family Film", Seeders: 10, Groups: []string{"movies"},
			Sources: []source{{Seeders: 10}}},
		{Title: "Some Parody XXX", Seeders: 99, Adult: true, Groups: []string{"adult"},
			Sources: []source{{Seeders: 99}}},
	}
	hidden := filters{Sort: "seeders"}.apply(cards)
	if len(hidden) != 1 || hidden[0].Title != "Family Film" {
		t.Errorf("adult should be hidden by default; got %v", titles(hidden))
	}
	shown := filters{Sort: "seeders", ShowAdult: true}.apply(cards)
	if len(shown) != 2 {
		t.Errorf("adult should appear when asked for; got %v", titles(shown))
	}
}

func TestCategoryGroupFiltering(t *testing.T) {
	cards := []card{
		{Title: "A Film", Seeders: 5, Groups: []string{"movies"}, Sources: []source{{Seeders: 5}}},
		{Title: "A Show", Seeders: 5, Groups: []string{"tv"}, Sources: []source{{Seeders: 5}}},
		{Title: "An Album", Seeders: 5, Groups: []string{"music"}, Sources: []source{{Seeders: 5}}},
	}
	got := filters{Sort: "seeders", Groups: []string{"movies", "music"}}.apply(cards)
	if len(got) != 2 {
		t.Fatalf("got %v, want Film + Album", titles(got))
	}
	for _, c := range got {
		if c.Title == "A Show" {
			t.Error("TV leaked through a movies+music filter")
		}
	}
	// No selection means no constraint.
	if len(filters{Sort: "seeders"}.apply(cards)) != 3 {
		t.Error("empty group filter should not exclude anything")
	}
}

func TestNewznabCategoryMapping(t *testing.T) {
	cases := map[int]string{
		2000: "movies", 2040: "movies",
		5000: "tv", 5040: "tv",
		5070: "anime", // overlaps TV and must win
		3000: "music",
		1000: "games",
		4000: "apps",
		7000: "books",
		6000: "adult", 6060: "adult",
	}
	for id, want := range cases {
		if got := groupForCategory(id); got != want {
			t.Errorf("groupForCategory(%d) = %q, want %q", id, got, want)
		}
	}
}

// Dedicated porn trackers frequently send no category at all.
func TestAdultDetectedFromIndexerWithoutCategory(t *testing.T) {
	r := prowlarrResult{Title: "Some Release 1080p", Indexer: "Free JAV Torrent"}
	if !isAdult(r, r.Title) {
		t.Error("adult indexer not detected without a category")
	}
	clean := prowlarrResult{Title: "Some Release 1080p", Indexer: "The Pirate Bay"}
	if isAdult(clean, clean.Title) {
		t.Error("false positive on a general indexer")
	}
}

// A mis-matched poster used to outrank a healthy swarm by an order of
// magnitude, which is how junk with the wrong image reached the top of a
// search. In a SEARCH, health has to win.
func TestSearchRanksSeedersAboveArtwork(t *testing.T) {
	terms := queryTerms("zelda")
	pretty := card{Title: "Zelda", Seeders: 2, Art: artwork{Found: true, Poster: "p.jpg", Rating: 9}}
	healthy := card{Title: "Zelda", Seeders: 800}

	if relevance(healthy, terms) <= relevance(pretty, terms) {
		t.Fatalf("a 800-seeder result must outrank a 2-seeder one with art: healthy=%d pretty=%d",
			relevance(healthy, terms), relevance(pretty, terms))
	}
}

// Browsing is the opposite case: with no query it is a poster wall, and a card
// with artwork is the entire point of it.
func TestBrowseStillLeadsWithArtwork(t *testing.T) {
	var none []string
	pretty := card{Title: "Something", Seeders: 2, Art: artwork{Found: true, Poster: "p.jpg", Rating: 9}}
	bare := card{Title: "Something", Seeders: 800}

	if relevance(pretty, none) <= relevance(bare, none) {
		t.Fatal("with no query the card with artwork should still lead")
	}
}

func TestSeederRankingIsMonotonic(t *testing.T) {
	terms := queryTerms("zelda")
	prev := -1 << 30
	for _, s := range []int{0, 4, 5, 20, 50, 100, 500, 5000} {
		got := relevance(card{Title: "Zelda", Seeders: s}, terms)
		if got < prev {
			t.Fatalf("relevance fell as seeders rose at %d seeders", s)
		}
		prev = got
	}
}

func TestConfidentHitRejectsAnUnrelatedFilm(t *testing.T) {
	hits := []tmdbHit{{ID: 1, Title: "The Super Mario Bros. Movie"}}
	if _, ok := pickConfidentHit(hits, "Super Mario World SNES"); ok {
		t.Fatal("a film must not be matched to a SNES ROM release")
	}
	if _, ok := pickConfidentHit(hits, "The Super Mario Bros Movie"); !ok {
		t.Fatal("the actual film should still match itself")
	}
}

func TestConfidentHitIgnoresPunctuationAndCase(t *testing.T) {
	hits := []tmdbHit{{ID: 2, Title: "Blade Runner 2049"}}
	if _, ok := pickConfidentHit(hits, "blade runner 2049"); !ok {
		t.Fatal("case and punctuation must not defeat a real match")
	}
	if _, ok := pickConfidentHit(hits, "Blade Runner"); !ok {
		t.Fatal("every significant word of the query is present, so this matches")
	}
	// The reverse must NOT match: "2049" is not in the shorter title.
	hits2 := []tmdbHit{{ID: 3, Title: "Blade Runner"}}
	if _, ok := pickConfidentHit(hits2, "Blade Runner 2049"); ok {
		t.Fatal("the 1982 film must not be attached to the 2049 release")
	}
}

// The kind filter was parsed, used for the cache key, and then never applied.
// `kind=image` returned videos, which reads as an answer rather than a bug.
func TestKindFilterActuallyExcludes(t *testing.T) {
	src := []source{{Title: "s", Seeders: 5, Size: 100 << 20, WebSafe: true}}
	cards := []card{
		{Key: "a", Title: "A Movie", Kind: "video", Seeders: 5, Sources: src},
		{Key: "b", Title: "A Photo", Kind: "image", Seeders: 5, Sources: src},
		{Key: "c", Title: "A Comic", Kind: "comic", Seeders: 5, Sources: src},
	}
	for _, tc := range []struct {
		kind string
		want string
	}{{"image", "b"}, {"comic", "c"}, {"video", "a"}} {
		got := filters{Kind: tc.kind, Sort: "relevance"}.apply(cards)
		if len(got) != 1 || got[0].Key != tc.want {
			t.Fatalf("kind=%q: got %d cards %v, want just %q",
				tc.kind, len(got), keysOf(got), tc.want)
		}
	}
	// No kind means no narrowing.
	if got := (filters{Sort: "relevance"}).apply(cards); len(got) != 3 {
		t.Fatalf("empty kind should keep all 3, got %d", len(got))
	}
}

func keysOf(cards []card) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.Key)
	}
	return out
}

// An unknown kind must not silently fall back to the games catalogue.
func TestUnknownKindHasNoArchiveScope(t *testing.T) {
	if q := archiveQueryFor("zelda", "teleport"); q != "" {
		t.Fatalf("unknown kind produced a query: %q", q)
	}
	if q := archiveQueryFor("nasa", "image"); !strings.Contains(q, "mediatype:(image)") {
		t.Fatalf("image scope missing from %q", q)
	}
	if q := archiveQueryFor("batman", "comic"); !strings.Contains(q, "mediatype:(texts)") {
		t.Fatalf("comic scope missing from %q", q)
	}
}
