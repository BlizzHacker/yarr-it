package main

import "testing"

func TestParseReleaseFilm(t *testing.T) {
	p := parseRelease("The.Matrix.1999.2160p.UHD.BluRay.x265.10bit.HDR.DTS-HD.MA.5.1-SWTYBLZ")
	if p.Title != "The Matrix" {
		t.Errorf("Title = %q, want %q", p.Title, "The Matrix")
	}
	if p.Year != 1999 {
		t.Errorf("Year = %d, want 1999", p.Year)
	}
	if p.Quality != "2160p" {
		t.Errorf("Quality = %q, want 2160p", p.Quality)
	}
	if p.Codec != "x265" {
		t.Errorf("Codec = %q, want x265", p.Codec)
	}
	if p.WebSafe {
		t.Error("x265 marked web-safe; it needs the WebCodecs path")
	}
	if p.Group != "SWTYBLZ" {
		t.Errorf("Group = %q, want SWTYBLZ", p.Group)
	}
}

func TestParseReleaseWebSafe(t *testing.T) {
	p := parseRelease("Big.Buck.Bunny.2008.1080p.WEB-DL.x264.AAC-GRP")
	if p.Title != "Big Buck Bunny" {
		t.Errorf("Title = %q, want %q", p.Title, "Big Buck Bunny")
	}
	if !p.WebSafe {
		t.Error("x264 should be web-safe (direct MediaSource playback)")
	}
	if p.Quality != "1080p" {
		t.Errorf("Quality = %q, want 1080p", p.Quality)
	}
}

func TestParseReleaseSeries(t *testing.T) {
	p := parseRelease("Breaking.Bad.S05E14.1080p.BluRay.x264-DEMAND")
	if !p.IsSeries {
		t.Fatal("IsSeries = false, want true")
	}
	if p.Season != 5 || p.Episode != 14 {
		t.Errorf("S%dE%d, want S5E14", p.Season, p.Episode)
	}
	if p.Title != "Breaking Bad" {
		t.Errorf("Title = %q, want %q", p.Title, "Breaking Bad")
	}
}

func TestParseReleaseAltSeriesFormat(t *testing.T) {
	p := parseRelease("Some Show 3x07 720p HDTV XviD")
	if !p.IsSeries || p.Season != 3 || p.Episode != 7 {
		t.Errorf("got series=%v S%dE%d, want true S3E7", p.IsSeries, p.Season, p.Episode)
	}
}

// The whole point of parsing is that many torrents collapse into one card.
func TestGroupKeyCollapsesQualities(t *testing.T) {
	names := []string{
		"The.Matrix.1999.2160p.BluRay.x265-A",
		"The.Matrix.1999.1080p.WEB-DL.x264-B",
		"The Matrix 1999 720p BRRip XviD-C",
		"the.matrix.1999.480p.DVDRip-D",
	}
	want := parseRelease(names[0]).groupKey()
	for _, n := range names[1:] {
		if got := parseRelease(n).groupKey(); got != want {
			t.Errorf("groupKey(%q) = %q, want %q -- these are the same film", n, got, want)
		}
	}
}

func TestGroupKeySeparatesEpisodes(t *testing.T) {
	a := parseRelease("Breaking.Bad.S05E14.1080p.x264").groupKey()
	b := parseRelease("Breaking.Bad.S05E15.1080p.x264").groupKey()
	if a == b {
		t.Error("different episodes collapsed into one card")
	}
}

func TestGroupKeySeparatesDifferentFilms(t *testing.T) {
	a := parseRelease("The.Matrix.1999.1080p.x264").groupKey()
	b := parseRelease("The.Matrix.Reloaded.2003.1080p.x264").groupKey()
	if a == b {
		t.Error("distinct films collapsed into one card")
	}
}

func TestQualityRankOrdering(t *testing.T) {
	if qualityRank("2160p") <= qualityRank("1080p") {
		t.Error("2160p should outrank 1080p")
	}
	if qualityRank("1080p") <= qualityRank("720p") {
		t.Error("1080p should outrank 720p")
	}
	if qualityRank("") != 0 {
		t.Error("unknown quality should rank 0")
	}
}

// Release tags are short, and several of them are common letter pairs. Matched
// as substrings they fire inside ordinary words -- "TC" inside "Witcher", "TS"
// inside "Presents", "CAM" inside "Camelot" -- and because the title is cut at
// the first tag found, the cut lands in the middle of the name.
//
// Measured against a stub answering like Prowlarr: a search for "The Witcher 3:
// Wild Hunt" produced a card titled "The Wi". The truncated title is what the
// tile's match score is computed against, so the correct release scored zero
// and the tile reported finding nothing at all.
func TestATagInsideAWordIsNotATag(t *testing.T) {
	cases := []struct{ name, wantTitle string }{
		{"The.Witcher.3.Wild.Hunt.2015.2160p.BluRay.x265-STUB", "The Witcher 3 Wild Hunt"},
		{"Star.Wars.Visions.Presents.The.Ninth.Jedi.1080p.WEB-DL.x264-GRP",
			"Star Wars Visions Presents The Ninth Jedi"},
		{"Camelot.2011.S01E01.720p.HDTV.x264", "Camelot"},
		{"Antscape.2020.1080p.WEBRip", "Antscape"},
	}
	for _, c := range cases {
		if got := parseRelease(c.name).Title; got != c.wantTitle {
			t.Errorf("parseRelease(%q).Title = %q, want %q", c.name, got, c.wantTitle)
		}
	}
}

// The tags themselves must still be found where they really appear, including
// the ones carrying their own punctuation.
func TestRealTagsAreStillRecognised(t *testing.T) {
	p := parseRelease("The.Matrix.1999.2160p.UHD.BluRay.x265.10bit.HDR.DTS-HD.MA.5.1-SWTYBLZ")
	if p.Title != "The Matrix" || p.Year != 1999 {
		t.Fatalf("title/year = %q/%d", p.Title, p.Year)
	}
	if p.Quality != "2160p" || p.Codec != "x265" || p.Audio != "DTS-HD" {
		t.Errorf("quality=%q codec=%q audio=%q", p.Quality, p.Codec, p.Audio)
	}
	if p.Source != "BluRay" && p.Source != "UHD" {
		t.Errorf("source = %q", p.Source)
	}
	// A genuine CAM, which is the tag this boundary rule must not weaken: it is
	// how a rip nobody wants is kept off the top of a search.
	if got := parseRelease("Some.Film.2026.CAM.x264").Source; got != "CAM" {
		t.Errorf("a real CAM tag was missed: %q", got)
	}
	if got := parseRelease("Show.S01E02.WEB-DL.h264").Source; got != "WEB-DL" {
		t.Errorf("a hyphenated tag was missed: %q", got)
	}
}
