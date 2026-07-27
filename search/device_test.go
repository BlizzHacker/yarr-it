package main

import "testing"

// Roku shows a bare "cannot play" error with no explanation, so offering a
// source it cannot handle makes the app look broken when it is fine.
func TestRokuRejectsUnplayableContainers(t *testing.T) {
	p := deviceProfileFor("roku")
	if p == nil {
		t.Fatal("roku profile missing")
	}
	bad := []source{
		{Title: "Some.Movie.2020.DVDRip.XviD-GRP.avi", Codec: "XviD"},
		{Title: "Some.Movie.2020.wmv", Codec: ""},
		{Title: "Some.Movie.2020.rmvb", Codec: ""},
		{Title: "Some.Movie.2020.XviD.mkv", Codec: "XviD"},
	}
	for _, s := range bad {
		if p.playable(s) {
			t.Errorf("roku should not be offered %q", s.Title)
		}
	}

	good := []source{
		{Title: "Some.Movie.2020.1080p.BluRay.x264-GRP.mp4", Codec: "x264"},
		{Title: "Some.Movie.2020.2160p.HEVC.mkv", Codec: "HEVC"},
		{Title: "Some.Movie.2020.1080p.WEB-DL.H264", Codec: "H264"}, // no extension
	}
	for _, s := range good {
		if !p.playable(s) {
			t.Errorf("roku should accept %q", s.Title)
		}
	}
}

func TestRokuDropsCardsWithNoPlayableSource(t *testing.T) {
	p := deviceProfileFor("roku")
	cards := []card{
		{Title: "Playable Film", Groups: []string{"movies"}, Seeders: 10,
			Sources: []source{
				{Title: "Film.1080p.x264.mp4", Codec: "x264", Seeders: 10},
				{Title: "Film.XviD.avi", Codec: "XviD", Seeders: 99},
			}},
		{Title: "AVI Only Film", Groups: []string{"movies"}, Seeders: 50,
			Sources: []source{{Title: "Old.Film.XviD.avi", Codec: "XviD", Seeders: 50}}},
	}
	got := p.applyDevice(cards)
	if len(got) != 1 || got[0].Title != "Playable Film" {
		t.Fatalf("got %d cards (%v), want only the playable one", len(got), titles(got))
	}
	if len(got[0].Sources) != 1 {
		t.Errorf("the AVI source should have been stripped, got %d", len(got[0].Sources))
	}
	// Seeders must be recomputed from what survived, or the UI advertises a
	// count belonging to a source the user can no longer pick.
	if got[0].Seeders != 10 {
		t.Errorf("seeders = %d, want 10 after filtering", got[0].Seeders)
	}
}

func TestRokuKeepsMusicAndAudiobooksButNotGames(t *testing.T) {
	p := deviceProfileFor("roku")
	cards := []card{
		{Title: "Some Album", Groups: []string{"music"},
			Sources: []source{{Title: "Album.FLAC.mp4", Codec: "x264"}}},
		{Title: "Some Novel Unabridged Audiobook", Groups: []string{"books"},
			Sources: []source{{Title: "Novel.Audiobook.m4b.mp4", Codec: "x264"}}},
		{Title: "Some Game", Groups: []string{"games"},
			Sources: []source{{Title: "Game.Repack.iso", Codec: ""}}},
	}
	got := p.applyDevice(cards)
	names := titles(got)
	if len(got) != 2 {
		t.Fatalf("got %v, want album + audiobook", names)
	}
	for _, n := range names {
		if n == "Some Game" {
			t.Error("games should not appear on a TV")
		}
	}
}

func TestNilProfileIsPassThrough(t *testing.T) {
	var p *deviceProfile
	cards := []card{{Title: "X", Sources: []source{{Title: "x.avi"}}}}
	if len(p.applyDevice(cards)) != 1 {
		t.Error("no device hint must not filter anything")
	}
}
