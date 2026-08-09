package main

import (
	"net/url"
	"strings"
	"testing"
)

// The one vocabulary rule this file exists to keep. play_archive.go already
// holds the machine table that is checked against EmulatorJS's getCores() and
// against ROM Hub's plugin census; this catalogue adds families, aliases and a
// Solr query on top. If the two disagree about what a machine is CALLED in a
// URL, then a filter, a facet and a playability verdict stop being about the
// same thing -- and nothing would fail visibly until somebody noticed a
// category page that never matched its own cards.
func TestEverySystemUsesThePlatformSlugTheServiceAlreadyUses(t *testing.T) {
	for i := range gameSystems {
		s := &gameSystems[i]
		for _, e := range s.Emulators {
			p, ok := archivePlaySystems[e]
			if !ok {
				continue // an id only this catalogue knows; nothing to agree with
			}
			if p.Platform != s.ID {
				t.Errorf("%s claims emulator %q, which play_archive.go files under %q",
					s.ID, e, p.Platform)
			}
		}
	}
}

// Playability is asked, never restated. A second copy of that judgement here
// could only drift from the one with the evidence next to it.
func TestPlayabilityIsAnsweredByThePlayTableAndNotByThisOne(t *testing.T) {
	// Machines EmulatorJS runs.
	for _, id := range []string{"snes", "nes", "genesis", "gbc", "c64"} {
		if s := systemByID[id]; s == nil || !s.playsHere() {
			t.Errorf("%s should play here", id)
		}
	}
	// Machines it does not, each for a reason play_archive.go documents:
	// no core at all, firmware that is not ours to ship, a threaded core on a
	// page that is not cross-origin isolated, or a MAME romset.
	for _, id := range []string{
		"intellivision", "atari8bit", "zxs", "appleii", "arcade", "sg1000",
		"colecovision", "psx", "amiga", "dos", "nds", "flash",
	} {
		s := systemByID[id]
		if s == nil {
			t.Fatalf("%s is missing from the catalogue", id)
		}
		if s.playsHere() {
			t.Errorf("%s is offered to our player, which play_archive.go refuses", id)
		}
	}
}

// Nobody types "megadriv". Somebody looking for Mega Drive games and somebody
// looking for Genesis games want the same shelf.
func TestPeopleFindAMachineByAnyOfItsNames(t *testing.T) {
	for _, name := range []string{
		"genesis", "Genesis", "mega drive", "megadrive", "MEGADRIVE", "md",
		"Sega Genesis / Mega Drive", "sega genesis",
	} {
		if s := lookupSystem(name); s == nil || s.ID != "genesis" {
			t.Errorf("lookupSystem(%q) did not reach the Genesis", name)
		}
	}
	for name, want := range map[string]string{
		"snes": "snes", "super nintendo": "snes", "sfc": "snes",
		"turbografx-16": "tg16", "pc engine": "tg16", "tg16": "tg16",
		"wonderswan": "wonderswan", "vcs": "atari2600",
		"c64": "c64", "commodore 64": "c64", "speccy": "zxs",
		"nintendo entertainment system": "nes", "famicom": "nes",
		"mega duck": "mega-duck-slash-cougar-boy",
	} {
		if s := lookupSystem(name); s == nil || s.ID != want {
			t.Errorf("lookupSystem(%q) = %v, want %s", name, s, want)
		}
	}
	if lookupSystem("nintendo switch") != nil {
		t.Error("an unknown name must resolve to nothing, not to a guess")
	}
}

// archive.org ANALYSES the emulator field, so `pce-macplus` indexes as the
// tokens [pce, macplus] and a query for `pce` matches it. Measured live: the
// bare id pulled 4,785 Macintosh and Atari ST disk images into a 312-item
// TurboGrafx-16 shelf, whose top result was a Macintosh System 7 disk image.
func TestQueriesDoNotDragInNeighbouringMachines(t *testing.T) {
	c64 := systemQuery(systemByID["c64"])
	if !strings.Contains(c64, `NOT emulator:("vice-pet")`) {
		t.Errorf("the C64 query does not exclude the PET: %s", c64)
	}
	if strings.Contains(systemQuery(systemByID["tg16"]), `"pce"`) {
		t.Errorf("the TurboGrafx query carries the bare `pce` id: %s", systemQuery(systemByID["tg16"]))
	}
	// The belt to that query's braces: whatever Solr matched, the exact id is
	// checked again before anything is published.
	if belongsTo(systemByID["tg16"], "pce-macplus", nil) {
		t.Error("a Macintosh item was accepted as a TurboGrafx item")
	}
	if !belongsTo(systemByID["tg16"], "tg16", nil) {
		t.Error("a real TurboGrafx item was rejected")
	}
	if belongsTo(systemByID["c64"], "vice-pet", nil) {
		t.Error("a PET item was accepted as a Commodore 64 item")
	}
	if !belongsTo(systemByID["c64"], "vice-resid", nil) {
		t.Error("the id holding 98,072 of the C64's items was rejected")
	}
}

// An arcade cabinet's emulator field is its MAME driver name -- "contra",
// "outrun" -- and 4,045 of the catalogue's 4,162 distinct values are names like
// that. Only the collection can identify one.
func TestArcadeIsIdentifiedByCollectionNotByEmulator(t *testing.T) {
	arcade := systemByID["arcade"]
	if arcade.Collection != "internetarcade" {
		t.Fatalf("arcade collection = %q", arcade.Collection)
	}
	if !belongsTo(arcade, "outrun", []string{"internetarcade"}) {
		t.Error("a cabinet in internetarcade was not recognised")
	}
	if belongsTo(arcade, "outrun", []string{"consolelivingroom"}) {
		t.Error("a driver name alone must not make something arcade")
	}
	if !strings.Contains(systemQuery(arcade), "collection:(internetarcade)") {
		t.Errorf("arcade query = %s", systemQuery(arcade))
	}
}

// The two largest machines in the catalogue were unlabelled, because the map
// the label came from listed `vice_x64` -- an id archive.org uses for nothing --
// and no Amiga id their catalogue emits. 99,993 Commodore 64 items and 13,261
// Amiga items each showed a blank platform.
func TestTheBiggestMachinesAreNamedAtAll(t *testing.T) {
	for emulator, want := range map[string]string{
		"vice-resid": "c64",   // 98,072 items
		"sae-a500p":  "amiga", // 13,224 items
		"a800":       "atari8bit",
		"apple2ee":   "appleii",
		"spectrum":   "zxs",
		"dosbox":     "dos",
		"ruffle-swf": "flash",
	} {
		s := systemFor(emulator)
		if s == nil || s.ID != want {
			t.Errorf("systemFor(%q) = %v, want %s", emulator, s, want)
		}
		if got := friendlySystem(emulator, nil); got == "" {
			t.Errorf("friendlySystem(%q) is blank; this is the defect", emulator)
		}
	}
}

// A label is what a person reads; a slug is what a URL and a filter carry.
// Renaming one must never silently change the other.
func TestCardsCarryBothASlugAndALabel(t *testing.T) {
	cards := archiveCards([]archiveDoc{
		{Identifier: "smw", Title: "Super Mario World", Emulator: "snes"},
		{Identifier: "c64", Title: "Popples", Emulator: "vice-resid"},
		{Identifier: "cab", Title: "Out Run", Emulator: "outrun", Collection: []string{"internetarcade"}},
		{Identifier: "huh", Title: "Mystery", Emulator: "no-such-machine"},
	}, "game")
	byKey := map[string]card{}
	for _, c := range cards {
		byKey[c.Key] = c
	}
	if c := byKey["ia:smw"]; c.System != "snes" || c.Platform != "SNES" {
		t.Errorf("snes card = %q / %q", c.System, c.Platform)
	}
	if c := byKey["ia:c64"]; c.System != "c64" || c.Platform != "Commodore 64" {
		t.Errorf("c64 card = %q / %q", c.System, c.Platform)
	}
	if c := byKey["ia:cab"]; c.System != "arcade" || c.Platform != "Arcade" {
		t.Errorf("arcade card = %q / %q", c.System, c.Platform)
	}
	if c := byKey["ia:huh"]; c.System != "" || c.Platform != "" {
		t.Errorf("unknown card invented %q / %q", c.System, c.Platform)
	}
}

// Narrowing to a machine has to narrow the question asked upstream, not the 60
// rows that came back: 60 of 272,000 filtered down to SNES is almost nothing.
func TestNarrowingToAMachineNarrowsTheUpstreamQuery(t *testing.T) {
	q := archiveQueryOn("mario", "game", resolveSystems([]string{"snes"}))
	if !strings.Contains(q, "mario") {
		t.Errorf("the title search was lost: %s", q)
	}
	if !strings.Contains(q, `emulator:("snes" OR "snesp")`) {
		t.Errorf("the machine was not applied upstream: %s", q)
	}
	// Aliases of one machine collapse to one clause; two machines is a union.
	both := archiveQueryOn("sonic", "game", resolveSystems([]string{"genesis", "megadrive", "sms"}))
	if strings.Count(both, "emulator:(") != 2 {
		t.Errorf("aliases of one machine should collapse to one clause: %s", both)
	}
	if !strings.Contains(both, " OR ") {
		t.Errorf("two machines should be OR'd: %s", both)
	}
	// The unnarrowed query is byte-for-byte what it was.
	if archiveQueryOn("mario", "game", nil) != archiveQueryFor("mario", "game") {
		t.Error("an unnarrowed query changed shape")
	}
}

// Nothing a caller types may reach Solr. Only names in the catalogue resolve.
func TestUnknownMachineNamesNeverReachAQuery(t *testing.T) {
	f := parseFilters(url.Values{"system": {`snes,") OR collection:(nsfw`}})
	systems := f.systems()
	if len(systems) != 1 || systems[0].ID != "snes" {
		t.Fatalf("resolved %v, want just snes", systems)
	}
	if q := archiveQueryOn("x", "game", systems); strings.Contains(q, "nsfw") {
		t.Errorf("injected text survived: %s", q)
	}
}

// Solr matched a superset by construction; the exact id decides what is shown.
func TestResultsFromTheWrongMachineAreDropped(t *testing.T) {
	docs := []archiveDoc{
		{Identifier: "tg", Emulator: "tg16"},
		{Identifier: "mac", Emulator: "pce-macplus"},
	}
	kept := keepSystems(docs, resolveSystems([]string{"tg16"}))
	if len(kept) != 1 || kept[0].Identifier != "tg" {
		t.Errorf("kept %v, want only the TurboGrafx item", kept)
	}
	if len(keepSystems(docs, nil)) != 2 {
		t.Error("an unfiltered search must keep everything")
	}
}
