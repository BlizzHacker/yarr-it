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
