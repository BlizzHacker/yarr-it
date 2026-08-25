package main

// The sets band on a category page.
//
// One sentence governs all of it: a set is a container of the things in the
// grid, so it must be visible on the page and absent from every number and
// every list that describes the grid.

import (
	"strings"
	"testing"
)

func bandStore(t *testing.T) *minervaStore {
	t.Helper()
	return minervaStoreWith(t, []string{"udp://tracker.example:80"},
		snesSet(),
		minervaEntry{
			InfoHash:   strings.Repeat("c", 40),
			Path:       "RetroAchievements/RA - Super Nintendo Entertainment System",
			Collection: "RetroAchievements", Name: "RA - Super Nintendo Entertainment System",
			System: "snes", SizeBytes: 41_000_000_000,
		},
		// A different machine, which must not appear on the SNES page.
		minervaEntry{
			InfoHash: strings.Repeat("d", 40),
			Path:     "No-Intro/Nintendo - Game Boy", Collection: "No-Intro",
			Name: "Nintendo - Game Boy", System: "gb", SizeBytes: 1 << 30,
		},
		// A documentation torrent, which carries no machine at all and so can
		// never reach a machine's page.
		minervaEntry{
			InfoHash: strings.Repeat("e", 40), Path: "bitsavers",
			Collection: "bitsavers", Name: "bitsavers", SizeBytes: 1_640_000_000_000,
		},
	)
}

func TestTheBandHoldsOnlyThisMachinesSets(t *testing.T) {
	items := bandStore(t).setsForSystem("snes")
	if len(items) != 2 {
		t.Fatalf("the SNES band holds %d sets, want 2", len(items))
	}
	for _, it := range items {
		if it.Set == nil {
			t.Fatalf("a band item carries no set: %+v", it)
		}
		if !strings.Contains(strings.ToLower(it.Title), "super nintendo") {
			t.Errorf("a set from another machine reached the SNES band: %q", it.Title)
		}
	}
	// Smallest first: on a page whose job is to warn somebody what they are
	// about to fetch, the cheapest real option leads.
	if items[0].Set.SizeBytes > items[1].Set.SizeBytes {
		t.Errorf("the band is not smallest-first: %d then %d",
			items[0].Set.SizeBytes, items[1].Set.SizeBytes)
	}
}

// A documentation or artwork torrent has no machine slug, so there is no page
// it could reach. Pinned because routing bitsavers to `literature` is exactly
// the kind of change that could accidentally give it one.
func TestNonGameSetsNeverReachAMachinePage(t *testing.T) {
	s := bandStore(t)
	for _, slug := range []string{"snes", "gb", "dos", "arcade"} {
		for _, it := range s.setsForSystem(slug) {
			if strings.Contains(strings.ToLower(it.Title), "bitsavers") {
				t.Errorf("a documentation torrent reached the %s page", slug)
			}
		}
	}
}

// Every band item must carry a real target and its size. A tile with neither is
// the thing this site refuses to draw.
func TestEveryBandItemCarriesAMagnetAndASize(t *testing.T) {
	for _, it := range bandStore(t).setsForSystem("snes") {
		if !strings.HasPrefix(it.Play, "magnet:?xt=urn:btih:") {
			t.Errorf("band item %q has no magnet: %q", it.Title, it.Play)
		}
		if !strings.Contains(it.Play, "tr=") {
			t.Errorf("band item %q has a tracker-less magnet", it.Title)
		}
		if it.Set.SizeHuman == "" {
			t.Errorf("band item %q states no size", it.Title)
		}
		if it.Set.PeersKnown {
			t.Errorf("band item %q claims measured peers", it.Title)
		}
		// State stays empty: this IS the verified target, not something to go
		// looking for.
		if it.State != "" {
			t.Errorf("band item %q carries state %q", it.Title, it.State)
		}
	}
}

// The note is the price on the invitation, and only the server knows it.
func TestTheNoteStatesTheMachineAndTheSizeRange(t *testing.T) {
	items := bandStore(t).setsForSystem("snes")
	note := minervaSetsNote("snes", items)
	for _, want := range []string{"SNES", "one torrent each"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not contain %q: %q", want, note)
		}
	}
	// Both ends of the range, because the range is the point. Asserted through
	// humanSize rather than against literal text: it renders binary units, so
	// an 8.4 GB set reads "7.8 GiB", and a test that hard-coded the decimal
	// form would be pinning a number the page never shows.
	lo, hi := humanSize(items[0].Set.SizeBytes), humanSize(items[len(items)-1].Set.SizeBytes)
	if lo == hi {
		t.Fatalf("the fixture has no range to state")
	}
	for _, want := range []string{lo, hi} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not state %q: %q", want, note)
		}
	}
	// A machine with nothing gets no sentence at all rather than an empty one.
	if got := minervaSetsNote("snes", nil); got != "" {
		t.Errorf("an empty band produced a note: %q", got)
	}
}

// A single set states one size rather than a range from a number to itself.
func TestASingleSetStatesOneSize(t *testing.T) {
	items := bandStore(t).setsForSystem("gb")
	if len(items) != 1 {
		t.Fatalf("the Game Boy band holds %d sets", len(items))
	}
	note := minervaSetsNote("gb", items)
	if strings.Contains(note, " - 1.0") || strings.Count(note, "GiB") > 1 {
		t.Errorf("a single set was described as a range: %q", note)
	}
}

// THE one that matters most on this page. A category total promises tiles the
// grid can produce; a set is not one of them, so adding sets to the count would
// promise SNES games that do not exist.
func TestCategoryCountsNeverCountSets(t *testing.T) {
	s := &server{minerva: bandStore(t), browse: newBrowseCache()}
	before := s.categoryTotal("ia:sys:snes")
	all := s.allCounts()

	// The store holds two SNES sets. Neither may show up in either number.
	if before != 0 {
		t.Errorf("categoryTotal counted %d sets as browsable items", before)
	}
	if all["snes"] != 0 {
		t.Errorf("allCounts counted %d sets as browsable items", all["snes"])
	}
}

// The band hangs off its own field. A set inside Items would render on the
// Xbox, Roku and Cartridge clients as a playable game.
func TestTheBandIsASiblingOfItemsNeverAnItem(t *testing.T) {
	s := &server{minerva: bandStore(t)}
	row := s.attachMinervaSets("ia:sys:snes", discoverRow{
		Title: "Super Nintendo", Key: "ia:sys:snes",
		Items: []discoverItm{{Title: "Super Mario World", MediaType: domainGame}},
	})
	if len(row.Items) != 1 {
		t.Fatalf("the band leaked into Items: %d items", len(row.Items))
	}
	for _, it := range row.Items {
		if it.Set != nil {
			t.Errorf("a set is in Items: %q", it.Title)
		}
	}
	if len(row.Sets) != 2 {
		t.Errorf("Sets holds %d, want 2", len(row.Sets))
	}
	if row.SetsNote == "" {
		t.Error("the band has no note")
	}
}

// A category with no sets is byte-for-byte the row it has always been, which is
// what omitempty on both fields buys.
func TestACategoryWithNoSetsIsUnchanged(t *testing.T) {
	s := &server{minerva: bandStore(t)}
	in := discoverRow{Title: "MS-DOS", Key: "ia:sys:dos",
		Items: []discoverItm{{Title: "Doom", MediaType: domainGame}}}
	out := s.attachMinervaSets("ia:sys:dos", in)
	if len(out.Sets) != 0 || out.SetsNote != "" {
		t.Errorf("a machine with no sets grew a band: %+v", out)
	}
	// And a non-machine key is left alone entirely.
	film := discoverRow{Title: "Trending", Key: "trending"}
	if got := s.attachMinervaSets("trending", film); len(got.Sets) != 0 {
		t.Error("a film row grew a sets band")
	}
}

// An instance with no catalogue must behave exactly like one that has never
// imported: no band, no note, no change to the row.
func TestNoCatalogueMeansNoBand(t *testing.T) {
	s := &server{}
	in := discoverRow{Title: "Super Nintendo", Key: "ia:sys:snes"}
	out := s.attachMinervaSets("ia:sys:snes", in)
	if len(out.Sets) != 0 || out.SetsNote != "" {
		t.Errorf("a server with no catalogue produced a band: %+v", out)
	}
}
