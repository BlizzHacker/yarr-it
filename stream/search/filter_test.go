package main

import "testing"

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
