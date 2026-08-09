package main

import (
	"fmt"
	"sort"
	"strings"
)

// Naming, grouping and querying the machines behind the games.
//
// WHAT THIS IS NOT. It is not a second playability table. play_archive.go owns
// that question completely -- which EmulatorJS system runs a machine, which
// cores need firmware, which are threaded, which archive.org emulator ids exist
// at all -- and it is checked against EmulatorJS's own getCores(), against ROM
// Hub's playability tables, and against the archive-org plugin's id census.
// Nothing here restates any of it: `playsHere` below asks ejsCoreFor, so there
// is exactly one answer to "will pressing Play produce a game" and it lives
// next to the evidence for it.
//
// WHAT THIS IS. Three things that table has no reason to hold, and that a
// person browsing needs:
//
//  1. A FAMILY, so fifty-odd machines can be shown as Nintendo / Sega / Atari /
//     Commodore / home computers / consoles / handhelds rather than as one
//     alphabetical wall.
//  2. ALIASES, because archive.org's field says `megadriv` and a person says
//     "Genesis" or "Mega Drive", and both should reach the same shelf.
//  3. A SOLR QUERY per machine, which is the part that is genuinely hard and is
//     the reason this file exists at all -- see systemQuery.
//
// WHERE THE IDS CAME FROM. The emulator ids are archive.org's own, tallied
// across all 272,327 items in this site's game scope (`emulator:[* TO *] AND
// mediatype:(software)`) through their bulk scrape API rather than guessed:
//
//	curl 'https://archive.org/services/search/v1/scrape?q=emulator:[*+TO+*]+AND+mediatype:(software)&fields=emulator&count=10000'
//
// paging on the returned cursor. That census found 4,162 distinct values, of
// which 4,045 are MAME driver names -- "contra", "gberet", "outrun" -- which is
// why arcade is defined by its collection and not by that field.
//
// WHY THE SLUGS ARE ROM HUB'S. Every system whose emulator ids appear in
// archivePlaySystems uses that table's Platform slug as its ID, so a URL, a
// filter, a facet and a playability verdict all say the same word.
// `mega-duck-slash-cougar-boy` is an ugly URL and it is IGDB's spelling, which
// is the vocabulary the rest of this service already speaks; inventing a
// prettier second name for an 18-item system would be a worse trade.
// systems_test.go pins the agreement so the two cannot drift.
//
// WHY SYSTEMS AND NOT GENRES. igdb.go could supply game genres, and they would
// be the wrong axis here. archive.org's catalogue has no genre field at all, so
// a genre shelf would have to be assembled by matching 271,000 archive items
// against IGDB titles -- and the axis people actually browse retro games on is
// the machine, which is already in the data, free, and exact.

// gameSystem is one machine, as a person would name it.
type gameSystem struct {
	// ID is the slug in URLs, filters and facets. For anything ROM Hub knows,
	// it is ROM Hub's platform slug; systems_test.go enforces that.
	ID string
	// Name is what a person calls the machine. archive.org's field says "pce",
	// "ws", "vb"; nobody looking for a game thinks in those terms.
	Name string
	// Short is the badge that fits on a poster tile.
	Short string
	// Family groups machines by who made them, so a picker is scannable.
	Family string

	// Emulators are archive.org `emulator` values that mean this machine,
	// exact.
	Emulators []string
	// Collection defines the machine instead, for the one case where the
	// emulator field cannot: an arcade item carries the MAME driver name.
	Collection string
	// Exclude removes ids a query built from Emulators would otherwise drag in.
	// See systemQuery -- this is not tidiness, it is the difference between a
	// TurboGrafx-16 shelf and a shelf of Macintosh disk images.
	Exclude []string

	// Aliases are the other things people type.
	Aliases []string
}

// Family keys, in the order a picker shows them.
const (
	famNintendo  = "nintendo"
	famSega      = "sega"
	famAtari     = "atari"
	famCommodore = "commodore"
	famComputer  = "computer"
	famConsole   = "console"
	famHandheld  = "handheld"
	famElsewhere = "elsewhere"
)

var systemFamilies = []struct{ Key, Title string }{
	{famNintendo, "Nintendo"},
	{famSega, "Sega"},
	{famAtari, "Atari"},
	{famCommodore, "Commodore"},
	{famComputer, "Home computers"},
	{famConsole, "Consoles"},
	{famHandheld, "Handhelds"},
	{famElsewhere, "Arcade & the web"},
}

// gameSystems is the catalogue. The counts in the comments are what
// archive.org held on 2026-08-08, and they are there to show which entries earn
// a shelf and which are a curiosity; the live numbers the UI shows come from
// the background count refresh in browse.go.
var gameSystems = []gameSystem{
	// --- Nintendo ---------------------------------------------------------
	{ID: "nes", Name: "Nintendo Entertainment System", Short: "NES", Family: famNintendo,
		Emulators: []string{"nes", "nesp", "nespal"},
		Aliases:   []string{"nes", "famicom", "fc", "nintendo entertainment system"}}, // 543
	{ID: "snes", Name: "Super Nintendo", Short: "SNES", Family: famNintendo,
		Emulators: []string{"snes", "snesp"},
		Aliases:   []string{"snes", "super nintendo", "super nes", "super famicom", "sfc"}}, // 547
	{ID: "n64", Name: "Nintendo 64", Short: "N64", Family: famNintendo,
		Emulators: []string{"n64"},
		Aliases:   []string{"n64", "nintendo 64"}}, // 8
	{ID: "gb", Name: "Game Boy", Short: "Game Boy", Family: famNintendo,
		Emulators: []string{"gameboy"},
		Aliases:   []string{"game boy", "gameboy", "gb", "dmg"}}, // 311
	{ID: "gbc", Name: "Game Boy Color", Short: "GBC", Family: famNintendo,
		Emulators: []string{"gbcolor"},
		Aliases:   []string{"game boy color", "gameboy color", "gbc"}}, // 552
	{ID: "gba", Name: "Game Boy Advance", Short: "GBA", Family: famNintendo,
		Emulators: []string{"gba"},
		Aliases:   []string{"game boy advance", "gameboy advance", "gba"}}, // 480
	// `nds` is not in archivePlaySystems, so ejsCoreFor already refuses it and
	// these route to archive.org's player -- which is the right answer twice
	// over: the most-downloaded DS item is a 61MB .nds, and the relay carries
	// 48MB. Named here so the three items say "Nintendo DS" rather than
	// nothing.
	{ID: "nds", Name: "Nintendo DS", Short: "DS", Family: famNintendo,
		Emulators: []string{"nds"},
		Aliases:   []string{"nintendo ds", "nds", "ds"}}, // 3

	// --- Sega -------------------------------------------------------------
	{ID: "genesis", Name: "Sega Genesis / Mega Drive", Short: "Genesis", Family: famSega,
		Emulators: []string{"genesis", "megadriv", "megadrij"},
		Aliases:   []string{"genesis", "sega genesis", "mega drive", "megadrive", "megadriv", "md"}}, // 12,975
	{ID: "sms", Name: "Sega Master System", Short: "Master System", Family: famSega,
		Emulators: []string{"sms", "smsj", "sms-phaser"},
		Aliases:   []string{"master system", "sega master system", "sms", "mark iii"}}, // 713
	{ID: "gamegear", Name: "Sega Game Gear", Short: "Game Gear", Family: famSega,
		Emulators: []string{"gamegear"},
		Aliases:   []string{"game gear", "gamegear", "gg"}}, // 602
	{ID: "sega32", Name: "Sega 32X", Short: "32X", Family: famSega,
		Emulators: []string{"32x"},
		Aliases:   []string{"32x", "sega 32x", "mega 32x"}}, // 37
	{ID: "sg1000", Name: "Sega SG-1000", Short: "SG-1000", Family: famSega,
		Emulators: []string{"sg1000"},
		Aliases:   []string{"sg-1000", "sg1000"}}, // 118

	// --- Atari ------------------------------------------------------------
	{ID: "atari2600", Name: "Atari 2600", Short: "2600", Family: famAtari,
		Emulators: []string{"a2600", "a2600p", "atari2600"},
		Aliases:   []string{"atari 2600", "2600", "vcs"}}, // 3,196
	{ID: "atari5200", Name: "Atari 5200", Short: "5200", Family: famAtari,
		Emulators: []string{"a5200"},
		Aliases:   []string{"atari 5200", "5200"}}, // 221
	{ID: "atari7800", Name: "Atari 7800", Short: "7800", Family: famAtari,
		Emulators: []string{"a7800"},
		Aliases:   []string{"atari 7800", "7800"}}, // 739
	{ID: "atari8bit", Name: "Atari 8-bit", Short: "Atari 800", Family: famAtari,
		Emulators: []string{"a800", "a800xl", "a800cart", "a800xlp"},
		Aliases:   []string{"atari 800", "atari 8-bit", "atari xl", "800xl", "atari 400"}}, // 14,400
	{ID: "lynx", Name: "Atari Lynx", Short: "Lynx", Family: famAtari,
		Emulators: []string{"lynx"},
		Aliases:   []string{"lynx", "atari lynx"}}, // 97
	{ID: "atari-st", Name: "Atari ST", Short: "Atari ST", Family: famAtari,
		Emulators: []string{"pce-atarist-color", "pce-atarist"},
		Aliases:   []string{"atari st", "st", "1040st"}}, // 896

	// --- Commodore --------------------------------------------------------
	// The largest machine in the whole catalogue by a wide margin, and the
	// label map it used to be read from did not have it: that map listed
	// `vice_x64`, an id archive.org uses for nothing at all.
	{ID: "c64", Name: "Commodore 64", Short: "C64", Family: famCommodore,
		Emulators: []string{"vice-resid", "vice-cassette", "vice", "vice-c64", "vice32", "c64"},
		Exclude:   []string{"vice-pet"},
		Aliases:   []string{"c64", "commodore 64", "commodore64", "cbm 64"}}, // 99,993
	{ID: "amiga", Name: "Commodore Amiga", Short: "Amiga", Family: famCommodore,
		Emulators: []string{"sae-a500p", "sae-a1200", "sae-a500"},
		Aliases:   []string{"amiga", "commodore amiga", "a500", "a1200"}}, // 13,261
	{ID: "cpet", Name: "Commodore PET", Short: "PET", Family: famCommodore,
		Emulators: []string{"vice-pet", "vice-pet-2001", "vice-pet-4032"},
		Aliases:   []string{"pet", "commodore pet", "cbm"}}, // 381
	{ID: "vic-20", Name: "Commodore VIC-20", Short: "VIC-20", Family: famCommodore,
		Emulators: []string{"vic20", "vice-vic20"},
		Aliases:   []string{"vic-20", "vic20"}}, // 1

	// --- Home computers ---------------------------------------------------
	{ID: "dos", Name: "MS-DOS", Short: "MS-DOS", Family: famComputer,
		Emulators: []string{"dosbox", "dosbox-sync", "dosbox-x", "sb486"},
		Aliases:   []string{"dos", "ms-dos", "msdos", "pc", "dosbox"}}, // 36,158
	{ID: "appleii", Name: "Apple II", Short: "Apple II", Family: famComputer,
		Emulators: []string{"apple2ee", "apple2woz", "apple2e", "apple2ee-helper", "apple2eeecho",
			"apple2pwoz", "apple2iwoz", "apple2p", "apple2c", "apple2cffa2j", "apple2cffa2",
			"apple2", "apple2ee-paddles"},
		Aliases: []string{"apple ii", "apple 2", "apple2", "apple iie"}}, // 27,772
	{ID: "apple-iigs", Name: "Apple IIgs", Short: "Apple IIgs", Family: famComputer,
		Emulators: []string{"apple2gs", "apple2gsr1"},
		Aliases:   []string{"apple iigs", "iigs", "apple 2gs"}}, // 3,030
	{ID: "appleiii", Name: "Apple III", Short: "Apple III", Family: famComputer,
		Emulators: []string{"apple3"},
		Aliases:   []string{"apple iii", "apple 3"}}, // 76
	{ID: "zxs", Name: "ZX Spectrum", Short: "Spectrum", Family: famComputer,
		Emulators: []string{"spectrum", "spec128", "spectrumcass"},
		Aliases:   []string{"zx spectrum", "spectrum", "speccy", "sinclair"}}, // 12,332
	{ID: "zx81", Name: "Sinclair ZX81", Short: "ZX81", Family: famComputer,
		Emulators: []string{"zx81"},
		Aliases:   []string{"zx81", "zx-81"}}, // 1,099
	{ID: "acpc", Name: "Amstrad CPC", Short: "Amstrad", Family: famComputer,
		Emulators: []string{"cpc6128"},
		Aliases:   []string{"amstrad", "cpc", "amstrad cpc", "cpc6128"}}, // 3,819
	{ID: "amstrad-gx4000", Name: "Amstrad GX4000", Short: "GX4000", Family: famComputer,
		Emulators: []string{"gx4000"},
		Aliases:   []string{"gx4000"}}, // 23
	{ID: "mac", Name: "Apple Macintosh", Short: "Macintosh", Family: famComputer,
		Emulators: []string{"pce-macplus", "macplus", "vmac-colormac", "mac128", "mac128k",
			"pce-macse", "pce-macclassic"},
		Aliases: []string{"macintosh", "mac", "mac plus", "classic mac", "system 7"}}, // 4,351
	{ID: "trs-80-color-computer", Name: "TRS-80 Color Computer", Short: "CoCo", Family: famComputer,
		Emulators: []string{"coco3disk", "coco2cart", "coco2disk"},
		Aliases:   []string{"coco", "trs-80", "trs80", "tandy", "color computer"}}, // 361
	{ID: "trs-80-mc-10", Name: "TRS-80 MC-10", Short: "MC-10", Family: famComputer,
		Emulators: []string{"mc10"},
		Aliases:   []string{"mc-10", "mc10"}}, // 945

	// --- Consoles ---------------------------------------------------------
	{ID: "psx", Name: "Sony PlayStation", Short: "PlayStation", Family: famConsole,
		Emulators: []string{"psx"},
		Aliases:   []string{"playstation", "psx", "ps1", "psone", "sony playstation"}}, // 2,739
	{ID: "tg16", Name: "TurboGrafx-16 / PC Engine", Short: "TurboGrafx-16", Family: famConsole,
		Emulators: []string{"tg16"},
		Aliases:   []string{"turbografx", "turbografx-16", "turbografx 16", "tg16", "pc engine", "pce"}}, // 312
	{ID: "supergrafx", Name: "PC Engine SuperGrafx", Short: "SuperGrafx", Family: famConsole,
		Emulators: []string{"sgx"},
		Aliases:   []string{"supergrafx", "sgx"}}, // 6
	{ID: "colecovision", Name: "ColecoVision", Short: "ColecoVision", Family: famConsole,
		Emulators: []string{"coleco"},
		Aliases:   []string{"colecovision", "coleco"}}, // 266
	{ID: "intellivision", Name: "Mattel Intellivision", Short: "Intellivision", Family: famConsole,
		Emulators: []string{"intv2", "intv", "intvsrs"},
		Aliases:   []string{"intellivision", "intv", "mattel"}}, // 195
	{ID: "odyssey-2", Name: "Magnavox Odyssey 2", Short: "Odyssey 2", Family: famConsole,
		Emulators: []string{"odyssey2"},
		Aliases:   []string{"odyssey 2", "odyssey2", "videopac", "magnavox"}}, // 133
	{ID: "vectrex", Name: "GCE Vectrex", Short: "Vectrex", Family: famConsole,
		Emulators: []string{"vectrex"},
		Aliases:   []string{"vectrex"}}, // 152
	{ID: "fairchild-channel-f", Name: "Fairchild Channel F", Short: "Channel F", Family: famConsole,
		Emulators: []string{"channelf"},
		Aliases:   []string{"channel f", "channelf", "fairchild"}}, // 48
	{ID: "arcadia-2001", Name: "Emerson Arcadia 2001", Short: "Arcadia 2001", Family: famConsole,
		Emulators: []string{"arcadia"},
		Aliases:   []string{"arcadia", "arcadia 2001", "emerson"}}, // 58
	{ID: "epoch-super-cassette-vision", Name: "Epoch Super Cassette Vision",
		Short: "Super Cassette Vision", Family: famConsole,
		Emulators: []string{"scv"},
		Aliases:   []string{"super cassette vision", "scv", "epoch"}}, // 36
	{ID: "astrocade", Name: "Bally Astrocade", Short: "Astrocade", Family: famConsole,
		Emulators: []string{"bally"},
		Aliases:   []string{"astrocade", "bally"}}, // 20
	{ID: "creativision", Name: "VTech CreatiVision", Short: "CreatiVision", Family: famConsole,
		Emulators: []string{"crvision"},
		Aliases:   []string{"creativision", "vtech"}}, // 16
	{ID: "apf-mp1000", Name: "APF MP1000", Short: "APF MP1000", Family: famConsole,
		Emulators: []string{"apfm1000"},
		Aliases:   []string{"apf", "mp1000", "m1000"}}, // 15
	{ID: "super-acan", Name: "Super A'Can", Short: "Super A'Can", Family: famConsole,
		Emulators: []string{"supracan"},
		Aliases:   []string{"super acan", "supracan", "super a'can"}}, // 10
	{ID: "super-vision-8000", Name: "Bandai Super Vision 8000",
		Short: "Super Vision 8000", Family: famConsole,
		Emulators: []string{"sv8000"},
		Aliases:   []string{"super vision 8000", "sv8000"}}, // 7
	{ID: "aquarius", Name: "Mattel Aquarius", Short: "Aquarius", Family: famConsole,
		Emulators: []string{"aquarius"},
		Aliases:   []string{"aquarius"}}, // 13
	{ID: "socrates", Name: "VTech Socrates", Short: "Socrates", Family: famConsole,
		Emulators: []string{"socrates"},
		Aliases:   []string{"socrates"}}, // 8

	// --- Handhelds --------------------------------------------------------
	{ID: "neo-geo-pocket", Name: "Neo Geo Pocket", Short: "Neo Geo Pocket", Family: famHandheld,
		Emulators: []string{"ngp"},
		Aliases:   []string{"neo geo pocket", "neogeo pocket", "ngp", "snk"}}, // 19
	{ID: "neo-geo-pocket-color", Name: "Neo Geo Pocket Color", Short: "NGPC", Family: famHandheld,
		Emulators: []string{"ngpc"},
		Aliases:   []string{"neo geo pocket color", "ngpc"}}, // 235
	{ID: "wonderswan", Name: "WonderSwan", Short: "WonderSwan", Family: famHandheld,
		Emulators: []string{"wswan"},
		Aliases:   []string{"wonderswan", "wonder swan", "ws", "bandai"}}, // 171
	{ID: "wonderswan-color", Name: "WonderSwan Color", Short: "WS Color", Family: famHandheld,
		Emulators: []string{"wscolor"},
		Aliases:   []string{"wonderswan color", "swancrystal", "wsc"}}, // 159
	{ID: "supervision", Name: "Watara Supervision", Short: "Supervision", Family: famHandheld,
		Emulators: []string{"svision"},
		Aliases:   []string{"supervision", "watara", "quickshot"}}, // 75
	{ID: "game-dot-com", Name: "Tiger Game.com", Short: "Game.com", Family: famHandheld,
		Emulators: []string{"gamecom"},
		Aliases:   []string{"game.com", "gamecom", "tiger"}}, // 27
	{ID: "mega-duck-slash-cougar-boy", Name: "Mega Duck", Short: "Mega Duck", Family: famHandheld,
		Emulators: []string{"megaduck"},
		Aliases:   []string{"mega duck", "megaduck", "cougar boy"}}, // 18
	{ID: "game-pocket-computer", Name: "Epoch Game Pocket Computer",
		Short: "Game Pocket", Family: famHandheld,
		Emulators: []string{"gamepock"},
		Aliases:   []string{"game pocket computer", "gamepock"}}, // 11
	{ID: "adventure-vision", Name: "Entex Adventure Vision",
		Short: "Adventure Vision", Family: famHandheld,
		Emulators: []string{"advision"},
		Aliases:   []string{"adventure vision", "entex"}}, // 5
	{ID: "palm", Name: "Palm OS", Short: "Palm", Family: famHandheld,
		Emulators: []string{"cloudpilot-iii", "cloudpilot-m515", "cloudpilot-pilot", "cloudpilot-tungstenw"},
		Aliases:   []string{"palm", "palmos", "palm os", "palm pilot", "pilot"}}, // 926

	// --- Arcade and the web -----------------------------------------------
	// Arcade cannot be defined by `emulator`: for a cabinet that field holds the
	// MAME driver name, so "contra" and "outrun" are values -- 4,045 of the
	// 4,162 distinct values in the catalogue are driver names. The collection is
	// the only thing coarse enough to be right, and it is the same reasoning
	// play_archive.go gives for refusing our own player for arcade at all.
	{ID: "arcade", Name: "Arcade", Short: "Arcade", Family: famElsewhere,
		Collection: "internetarcade",
		Aliases:    []string{"arcade", "mame", "coin-op", "coin op", "cabinet", "jamma"}}, // 2,662
	// Flash has no EmulatorJS core and needs none: sourcesFor routes a SWF to
	// Ruffle, which is a real in-browser player with touch support.
	{ID: "flash", Name: "Flash", Short: "Flash", Family: famElsewhere,
		Emulators: []string{"ruffle-swf", "ruffle_swf", "ruffle", "ruffle.swf"},
		Aliases:   []string{"flash", "swf", "shockwave", "newgrounds", "ruffle"}}, // 22,512
}

var (
	systemByID       = map[string]*gameSystem{}
	systemByEmulator = map[string]*gameSystem{}
	systemByAlias    = map[string]*gameSystem{}
)

func init() {
	for i := range gameSystems {
		s := &gameSystems[i]
		systemByID[s.ID] = s
		for _, e := range s.Emulators {
			// First registration wins, so a duplicated id is a bug the tests
			// catch rather than a silent reassignment at process start.
			if _, dup := systemByEmulator[e]; !dup {
				systemByEmulator[e] = s
			}
		}
		systemByAlias[s.ID] = s
		systemByAlias[strings.ToLower(s.Name)] = s
		systemByAlias[strings.ToLower(s.Short)] = s
		for _, a := range s.Aliases {
			if _, dup := systemByAlias[a]; !dup {
				systemByAlias[a] = s
			}
		}
	}
}

// systemFor resolves an archive.org emulator id to a machine, exactly. An
// unrecognised id is a MAME driver name far more often than it is a system, so
// this reports nothing rather than guessing -- the mistake that once labelled a
// Contra result's platform "Contra".
func systemFor(emulator string) *gameSystem {
	return systemByEmulator[emulator]
}

// lookupSystem resolves whatever a person typed or a URL carried: the slug, the
// full name, the short name, or any alias. "genesis" and "megadrive" land on
// the same shelf, which is the whole point of having aliases.
func lookupSystem(name string) *gameSystem {
	return systemByAlias[strings.ToLower(strings.TrimSpace(name))]
}

// resolveSystems turns a list of user-supplied names into machines, dropping
// anything unknown. Unknown input must never reach a Solr query.
func resolveSystems(names []string) []*gameSystem {
	out := make([]*gameSystem, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		s := lookupSystem(n)
		if s == nil || seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	return out
}

// playsHere reports whether this site's own player can run the machine.
//
// Delegated rather than restated: play_archive.go decides, and it knows about
// missing firmware, threaded cores and ids EmulatorJS has never heard of. A
// second copy of that judgement here could only drift from it, and the drift
// would show up as a Play button that does nothing.
func (s *gameSystem) playsHere() bool {
	if s == nil {
		return false
	}
	for _, e := range s.Emulators {
		if ejsCoreFor(e) != "" {
			return true
		}
	}
	return false
}

// quotedOr renders `"a" OR "b"`. Values come from the table above, never from a
// request, so there is nothing here to escape -- but they are quoted anyway so
// an id containing a hyphen is one term rather than a boolean expression.
func quotedOr(vals []string) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, fmt.Sprintf("%q", v))
	}
	return strings.Join(parts, " OR ")
}

// systemQuery is the Solr fragment that selects one machine's items.
//
// The Exclude clause is not defensive tidiness. archive.org ANALYSES the
// `emulator` field, so `pce-macplus` is indexed as the tokens [pce, macplus]
// and a query for `pce` matches it. Two ids in this catalogue collide that way
// and both were measured against live archive.org: a bare `pce` pulled 4,785
// Macintosh and Atari ST disk images into what should be a 312-item
// TurboGrafx-16 shelf, whose top result was a Macintosh System 7 disk image;
// and a bare `vice` pulled all 381 Commodore PET items into the Commodore 64.
//
// With the exclusions, live numFound matches the exact per-id tally from the
// full 272,327-item census to within the couple of items archive.org ingested
// while the measurement ran.
func systemQuery(s *gameSystem) string {
	var q string
	if s.Collection != "" {
		q = fmt.Sprintf("collection:(%s)", s.Collection)
	} else {
		q = fmt.Sprintf("emulator:(%s)", quotedOr(s.Emulators))
	}
	if len(s.Exclude) > 0 {
		q += fmt.Sprintf(" AND NOT emulator:(%s)", quotedOr(s.Exclude))
	}
	return q + " AND mediatype:(software)"
}

// systemsClause selects any of several machines at once. Each machine's own
// query already carries its exclusions, so they are OR'd whole.
func systemsClause(systems []*gameSystem) string {
	if len(systems) == 0 {
		return ""
	}
	parts := make([]string, 0, len(systems))
	for _, s := range systems {
		parts = append(parts, "("+systemQuery(s)+")")
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// belongsTo reports whether a document really is this machine's, by exact
// emulator id. The query above is a superset by construction -- tokenisation
// guarantees it -- so every row is checked again before it is published. An
// arcade cabinet's emulator is its MAME driver name and matches nothing, so
// that one is identified by its collection instead.
func belongsTo(s *gameSystem, emulator string, collections []string) bool {
	if s.Collection != "" {
		for _, c := range collections {
			if c == s.Collection {
				return true
			}
		}
		return false
	}
	for _, e := range s.Emulators {
		if e == emulator {
			return true
		}
	}
	return false
}

// systemsInFamily returns one family's machines, biggest catalogue first so a
// picker leads with the shelves worth opening.
func systemsInFamily(family string, counts map[string]int) []*gameSystem {
	out := []*gameSystem{}
	for i := range gameSystems {
		if gameSystems[i].Family == family {
			out = append(out, &gameSystems[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := counts[out[i].ID], counts[out[j].ID]
		if ci != cj {
			return ci > cj
		}
		return out[i].Name < out[j].Name
	})
	return out
}
