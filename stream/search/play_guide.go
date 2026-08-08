package main

// What the game is, and which key is fire.
//
// A player who is handed a booted emulator and no instructions cannot play. The
// picture is right there and every key does nothing until the correct one does
// something, and on a television with a remote there is not even a keyboard to
// experiment with. So every verdict carries a guide, and the guide is assembled
// from three sources in descending order of authority:
//
//  1. `emulator_instructions` -- the Internet Archive's OWN control notes for
//     the item. Measured on live items 2026-08-07: River Raid carries "Use the
//     arrow keys to move Up/Down/Left/Right with the Joystick. Pressing the left
//     CTRL button or space bar will press the fire button"; Pitfall! carries
//     "The SPACE bar makes Pitfall Harry jump". This is real, per-game, written
//     by the people who prepared the item.
//  2. `description` -- what the game is. Present on nearly everything, HTML.
//  3. The emulator's own default key map for the machine, when the item says
//     nothing. Most items say nothing: Mike Tyson's Punch-Out!! and Mortal
//     Kombat, both in the top ten by downloads, have no `emulator_instructions`
//     at all. A default key map is not as good as the game's own manual and is
//     enormously better than a black rectangle.
//
// THE HONESTY PROBLEM, AND HOW IT IS HANDLED.
//
// `emulator_instructions` describes THEIR player's keys, not ours. "The 1 key
// pushes the SELECT switch" is true in the Emularity player on archive.org and
// false in EmulatorJS, where SELECT is `v`. Presenting that text as our controls
// would be worse than presenting nothing, because it would be confidently wrong.
//
// So the two are kept apart and each is labelled with whose player it describes:
// `Notes`/`NotesFor` is the Archive's text about the Archive's player, and
// `Controls`/`ControlsFor` is our key map for our player. A UI shows the one
// that matches the route it is on and may show the other as background.

import (
	"encoding/json"
	"html"
	"net/url"
	"path"
	"sort"
	"strings"
)

// --- EmulatorJS's own defaults ----------------------------------------------

// ejsDefaultKeys is `defaultControllers[0]` read out of EmulatorJS's own
// emulator.min.js (https://cdn.emulatorjs.org/stable/data/emulator.min.js,
// 2026-08-07). The index is the RetroPad button id EmulatorJS uses everywhere;
// the value is the key it binds by default for player one.
//
// Read rather than remembered, because being approximately right here is the
// worst possible outcome: a player told the wrong key concludes the emulator is
// broken, which is exactly the impression this work exists to remove.
//
// 14 and 15 (the analogue stick clicks) are deliberately empty in EmulatorJS
// itself. They are kept here as empty strings rather than omitted so that
// `controlsFor` can distinguish "this machine has no such button" from "this
// button exists and has no key", and buttonsFor drops them either way.
var ejsDefaultKeys = map[int]string{
	0: "x", 1: "s", 2: "v", 3: "enter",
	4: "up arrow", 5: "down arrow", 6: "left arrow", 7: "right arrow",
	8: "z", 9: "a", 10: "q", 11: "e", 12: "tab", 13: "r",
	14: "", 15: "",
	16: "h", 17: "f", 18: "g", 19: "t",
	20: "l", 21: "j", 22: "k", 23: "i",
	24: "1", 25: "2", 26: "3",
	// 27 (fast forward), 28 (rewind) and 29 (slow motion) exist in EmulatorJS
	// and are unbound by default. Omitted rather than listed empty: a control
	// with no key is not a control.
}

type ejsButton struct {
	ID    int
	Label string
}

// ejsDefaultScheme is what EmulatorJS shows for any machine it has no specific
// layout for -- a bare RetroPad. It is the fallback branch of
// `createControlSettingMenu()`, and it is what Mega Drive, Commodore 64, DOS,
// Atari 5200 and the Amiga actually get.
var ejsDefaultScheme = []ejsButton{
	{8, "A"}, {0, "B"}, {9, "X"}, {1, "Y"},
	{2, "SELECT"}, {3, "START"},
	{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	{10, "L"}, {11, "R"}, {12, "L2"}, {13, "R2"},
	{19, "L STICK UP"}, {18, "L STICK DOWN"}, {17, "L STICK LEFT"}, {16, "L STICK RIGHT"},
	{23, "R STICK UP"}, {22, "R STICK DOWN"}, {21, "R STICK LEFT"}, {20, "R STICK RIGHT"},
}

// ejsControlSchemes is the per-machine button layout, transcribed from the
// chain of `getControlScheme()` comparisons in `createControlSettingMenu()`.
// The key is the EmulatorJS system name -- the same vocabulary as
// emulatorJSSystems in play_archive.go -- because that is what EmulatorJS keys
// this decision on itself (getControlScheme falls back to getCore(true)).
//
// The order is EmulatorJS's own, which puts the buttons a person actually
// presses first and the difficulty switches last. It is preserved rather than
// sorted so a printed list reads like the console's own manual.
var ejsControlSchemes = map[string][]ejsButton{
	"gb": {
		{8, "A"}, {0, "B"}, {2, "SELECT"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"gba": {
		{8, "A"}, {0, "B"}, {10, "L"}, {11, "R"}, {2, "SELECT"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"nes": {
		{8, "A"}, {0, "B"}, {2, "SELECT"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
		{10, "SWAP DISKS"}, {11, "EJECT/INSERT DISK"},
	},
	"snes": {
		{8, "A"}, {0, "B"}, {9, "X"}, {1, "Y"}, {2, "SELECT"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"}, {10, "L"}, {11, "R"},
	},
	"n64": {
		{0, "A"}, {1, "B"}, {3, "START"},
		{4, "D-PAD UP"}, {5, "D-PAD DOWN"}, {6, "D-PAD LEFT"}, {7, "D-PAD RIGHT"},
		{10, "L"}, {11, "R"}, {12, "Z"},
		{19, "STICK UP"}, {18, "STICK DOWN"}, {17, "STICK LEFT"}, {16, "STICK RIGHT"},
		{23, "C-PAD UP"}, {22, "C-PAD DOWN"}, {21, "C-PAD LEFT"}, {20, "C-PAD RIGHT"},
	},
	"nds": {
		{8, "A"}, {0, "B"}, {9, "X"}, {1, "Y"}, {2, "SELECT"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"}, {10, "L"}, {11, "R"},
		{14, "Microphone"},
	},
	"vb": {
		{8, "A"}, {0, "B"}, {10, "L"}, {11, "R"}, {2, "SELECT"}, {3, "START"},
		{4, "LEFT D-PAD UP"}, {5, "LEFT D-PAD DOWN"},
		{6, "LEFT D-PAD LEFT"}, {7, "LEFT D-PAD RIGHT"},
		{19, "RIGHT D-PAD UP"}, {18, "RIGHT D-PAD DOWN"},
		{17, "RIGHT D-PAD LEFT"}, {16, "RIGHT D-PAD RIGHT"},
	},
	"segaCD": {
		{1, "A"}, {0, "B"}, {8, "C"}, {10, "X"}, {9, "Y"}, {11, "Z"},
		{3, "START"}, {2, "MODE"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"sega32x": {
		{1, "A"}, {0, "B"}, {8, "C"}, {10, "X"}, {9, "Y"}, {11, "Z"},
		{3, "START"}, {2, "MODE"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"segaMS": {
		{0, "BUTTON 1 / START"}, {8, "BUTTON 2"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"segaGG": {
		{0, "BUTTON 1"}, {8, "BUTTON 2"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"segaSaturn": {
		{1, "A"}, {0, "B"}, {8, "C"}, {9, "X"}, {10, "Y"}, {11, "Z"},
		{12, "L"}, {13, "R"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"3do": {
		{1, "A"}, {0, "B"}, {8, "C"}, {10, "L"}, {11, "R"}, {2, "X"}, {3, "P"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"atari2600": {
		{0, "FIRE"}, {2, "SELECT"}, {3, "RESET"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
		{10, "LEFT DIFFICULTY A"}, {12, "LEFT DIFFICULTY B"},
		{11, "RIGHT DIFFICULTY A"}, {13, "RIGHT DIFFICULTY B"},
		// COLOR (14) and B/W (15) are in EmulatorJS's list and have no default
		// key. They are omitted here rather than shown unbound.
	},
	"atari7800": {
		{0, "BUTTON 1"}, {8, "BUTTON 2"}, {2, "SELECT"}, {3, "PAUSE"}, {9, "RESET"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
		{10, "LEFT DIFFICULTY"}, {11, "RIGHT DIFFICULTY"},
	},
	"lynx": {
		{8, "A"}, {0, "B"}, {10, "OPTION 1"}, {11, "OPTION 2"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"jaguar": {
		{8, "A"}, {0, "B"}, {1, "C"}, {2, "PAUSE"}, {3, "OPTION"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"pce": {
		{8, "I"}, {0, "II"}, {2, "SELECT"}, {3, "RUN"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"pcfx": {
		{8, "I"}, {0, "II"}, {9, "III"}, {1, "IV"}, {10, "V"}, {11, "VI"},
		{3, "RUN"}, {2, "SELECT"}, {12, "MODE1"}, {13, "MODE2"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"ngp": {
		{0, "A"}, {8, "B"}, {3, "OPTION"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"ws": {
		{8, "A"}, {0, "B"}, {3, "START"},
		{4, "X UP"}, {5, "X DOWN"}, {6, "X LEFT"}, {7, "X RIGHT"},
		{13, "Y UP"}, {12, "Y DOWN"}, {10, "Y LEFT"}, {11, "Y RIGHT"},
	},
	"coleco": {
		{8, "LEFT BUTTON"}, {0, "RIGHT BUTTON"},
		{9, "1"}, {1, "2"}, {11, "3"}, {10, "4"},
		{13, "5"}, {12, "6"}, {15, "7"}, {14, "8"},
		{2, "*"}, {3, "#"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"},
	},
	"psp": {
		{9, "TRIANGLE"}, {1, "SQUARE"}, {0, "CROSS"}, {8, "CIRCLE"},
		{2, "SELECT"}, {3, "START"},
		{4, "UP"}, {5, "DOWN"}, {6, "LEFT"}, {7, "RIGHT"}, {10, "L"}, {11, "R"},
		{19, "STICK UP"}, {18, "STICK DOWN"}, {17, "STICK LEFT"}, {16, "STICK RIGHT"},
	},
}

// ejsSaveButtons are the three EmulatorJS shortcuts that exist on every machine
// and are bound by default. Worth stating: a save state is the difference
// between a 1982 arcade port being playable in a browser tab and being a
// punishment.
var ejsSaveButtons = []ejsButton{
	{24, "QUICK SAVE STATE"}, {25, "QUICK LOAD STATE"}, {26, "CHANGE STATE SLOT"},
}

// keyboardMachines are the ones where the pad is not the answer.
//
// A DOS game, a Commodore 64 program and an Amiga disk are driven by the actual
// keyboard: dosbox_pure and VICE pass keystrokes straight through. EmulatorJS
// still exposes a RetroPad for them -- it has no per-machine layout for these,
// so `getControlScheme()` falls through to the generic one -- and printing 25
// rows of L3/R3/R-STICK for Commander Keen would be accurate about the emulator
// and useless about the game. Naming the machine as a keyboard machine is the
// one sentence that makes the list make sense instead of being noise.
var keyboardMachines = map[string]bool{
	"dos": true, "amiga": true,
	"c64": true, "c128": true, "vic20": true, "pet": true, "plus4": true,
}

// coinSchemes are the machines where EmulatorJS relabels SELECT as INSERT COIN.
// It matters more than a label usually does: on an arcade cabinet nothing at all
// happens until a coin goes in, so a player who does not know this concludes the
// emulator is broken within about four seconds.
var coinSchemes = map[string]bool{"arcade": true, "mame": true}

// playControl is one row of the key map.
type playControl struct {
	Button string `json:"button"`
	Key    string `json:"key"`
}

// keyLabel turns EmulatorJS's internal key name into something printable.
// EmulatorJS's names are already close to English ("up arrow", "enter"), so this
// is presentation only -- the arrows become glyphs because on a control list
// four words that all start with the same two are hard to scan.
func keyLabel(name string) string {
	switch name {
	case "up arrow":
		return "↑"
	case "down arrow":
		return "↓"
	case "left arrow":
		return "←"
	case "right arrow":
		return "→"
	case "":
		return ""
	}
	if len(name) == 1 {
		return strings.ToUpper(name)
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// controlsFor is the key map for one machine, in EmulatorJS's own order.
//
// A button with no default key is dropped rather than shown blank: the list
// exists to answer "which key is fire", and a row that answers "none" is noise
// in the middle of the answer.
func controlsFor(core string) []playControl {
	if core == "" {
		return nil
	}
	scheme, ok := ejsControlSchemes[core]
	if !ok {
		scheme = ejsDefaultScheme
	}
	out := make([]playControl, 0, len(scheme)+len(ejsSaveButtons))
	for _, b := range append(append([]ejsButton{}, scheme...), ejsSaveButtons...) {
		key := keyLabel(ejsDefaultKeys[b.ID])
		if key == "" {
			continue
		}
		label := b.Label
		if b.ID == 2 && coinSchemes[core] {
			label = "INSERT COIN"
		}
		out = append(out, playControl{Button: label, Key: key})
	}
	return out
}

// --- bring your own BIOS ----------------------------------------------------

// playBIOS describes firmware a machine needs and this project may not ship.
//
// The distinction that makes this worth building: the firmware is not ours to
// distribute, and it IS the owner's to use. Somebody with a ColecoVision in a
// cupboard has every right to the BIOS in it, and refusing to let them supply it
// turns a licensing fact into a capability we simply do not have. RomM lets an
// operator drop firmware in; this lets a visitor do the same, held in their own
// browser and never uploaded here -- which is both the correct place for it and
// the only place it can legally live.
type playBIOS struct {
	// System is the EmulatorJS system name the file unblocks, and is what a
	// client stores it under.
	System string `json:"system"`
	Label  string `json:"label"`
	// Files are the names libretro's core info gives, so somebody looking at a
	// folder of dumps knows which one to pick.
	//
	// The names are not decoration. EmulatorJS writes the firmware into the
	// emulator's filesystem under the last path segment of the URL it fetched
	// it from, and the core then looks for it BY NAME -- so a file under any
	// other name is a core that reports no BIOS over a game that never booted.
	Files  []string `json:"files"`
	Detail string   `json:"detail"`

	// MD5 is what the file must actually BE, published by the core itself.
	//
	// This is the field that makes an automatic choice safe. Every other signal
	// -- the name, the size, the folder it was in -- can be right about a file
	// that is wrong, and a BIOS of the right size and the wrong contents is
	// loaded by the core, rejected, and reported by drawing its own error screen
	// at a healthy frame rate over a game that never booted. A hash cannot be
	// right about the wrong file.
	//
	// Not sent to a client: it is how THIS server picks a file out of somebody
	// else's archive, and a client picks nothing.
	MD5 []string `json:"-"`

	// The four fields below describe firmware that is ACTUALLY IN USE, and are
	// absent everywhere else -- on the invitation shown to somebody who has
	// none, and on the catalogue listing, both of which are about firmware in
	// the abstract. `omitempty` is doing real work: without any of this set the
	// JSON is byte-for-byte what it was before there was a library to ask.

	// Source is "yours" for a file this visitor supplied and "library" for one
	// the household's library server holds. A client shows which, because a
	// person who supplied a file deserves to know it is the one being used, and
	// a person who supplied nothing deserves to know why it worked anyway.
	Source string `json:"source,omitempty"`
	// File is the library's name for it, and is the name the core will look
	// for. Absent for "yours": that file never leaves the browser and this
	// server never learns its name.
	File string `json:"file,omitempty"`
	// URL is where the client fetches the library's copy -- our own relay, never
	// the library, because the library needs a credential the client must never
	// have and may be on a network the client cannot reach.
	URL       string `json:"url,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`

	// Options are EmulatorJS core options that have to be set for this firmware
	// to be the one used. Only ever present for the "builtin" source, where
	// there is no file at all and the whole of the firmware is a setting -- see
	// builtInFirmware in play_bios.go.
	Options map[string]string `json:"options,omitempty"`
}

// biosRequirements is what each blocked-for-firmware machine needs, taken from
// libretro's published core info files
// (raw.githubusercontent.com/libretro/libretro-super/master/dist/info/), read
// 2026-08-07 -- the same source blockedSystems cites.
//
// A note on PlayStation, because the two sources disagree and the disagreement
// should be visible rather than smoothed over: pcsx_rearmed marks all four of
// its BIOS files `firmware<N>_opt = "true"`, i.e. optional, because it can high-
// level-emulate the BIOS. blockedSystems still refuses PlayStation, and the
// second half of its reason is the one that carries the weight -- the Archive's
// PlayStation items are disc images of several hundred megabytes, well past what
// this relay will carry. Supplying a BIOS therefore unblocks the machine and
// most individual items still fail the size check, which is the honest outcome.
var biosRequirements = map[string]playBIOS{
	"coleco": {
		System: "coleco",
		Label:  "ColecoVision BIOS",
		// Two names, and the second is not a guess: gearcoleco's own
		// libretro.cpp tries `colecovision.rom` and then falls back to
		// `coleco.rom` before giving up (load_bios, read 2026-08-08). Both are
		// therefore names the core will actually find, and a library that files
		// its ColecoVision BIOS under the shorter one -- which RomM does -- is
		// filing it under a name that works.
		Files: []string{"colecovision.rom", "coleco.rom"},
		// gearcoleco's own core info publishes this, verbatim:
		//   notes = "(!) colecovision.rom (md5): 2c66f5911e5b42b8ebe113403548eee7"
		// The same 8,192 bytes appear inside the ColecoVision romset the
		// Internet Archive's own player loads, and in RomM's `coleco.rom`.
		// Three independent sources, one hash -- which is what makes picking a
		// file out of an archive automatically defensible.
		MD5: []string{"2c66f5911e5b42b8ebe113403548eee7"},
		Detail: "gearcoleco marks colecovision.rom as required firmware. Supply " +
			"your own copy and ColecoVision games play here.",
	},
	"psx": {
		System: "psx",
		Label:  "PlayStation BIOS",
		Files:  []string{"scph5500.bin", "scph5501.bin", "scph5502.bin", "psxonpsp660.bin"},
		Detail: "Any one of the PlayStation BIOS dumps pcsx_rearmed knows. Most " +
			"PlayStation items here are disc images far past the size this relay " +
			"carries, so a BIOS unblocks the machine and each item is still judged " +
			"on its size.",
	},
	"amiga": {
		System: "amiga",
		Label:  "Amiga Kickstart ROM",
		Files:  []string{"kick34005.A500", "kick40068.A1200"},
		Detail: "puae marks the A500 (v1.3) and A1200 (v3.1) Kickstart ROMs as " +
			"required firmware. Supply your own copy and Amiga software runs here.",
	},
}

// --- the guide ---------------------------------------------------------------

// playFile is a document that ships inside the item.
type playFile struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Format string `json:"format,omitempty"`
	Size   int64  `json:"sizeBytes,omitempty"`
}

// playGuide is everything a person needs in order to actually play.
type playGuide struct {
	// Description is what the game is, in plain text. archive.org sends HTML
	// with links; it is flattened here rather than in the browser because a
	// client that trusted it would be one innerHTML away from rendering somebody
	// else's markup.
	Description string `json:"description,omitempty"`

	// Notes is the Archive's own `emulator_instructions`, and NotesFor says
	// whose player its key names refer to. It is always "archive": the field
	// documents the Emularity player, and its keys are wrong for ours.
	Notes    string `json:"notes,omitempty"`
	NotesFor string `json:"notesFor,omitempty"`

	// Controller is the Archive's `controller` field -- "joystick", "keyboard",
	// "paddle". Worth carrying because a paddle game played on a d-pad feels
	// broken rather than difficult.
	Controller string `json:"controller,omitempty"`

	// Controls is OUR key map for this machine, and ControlsFor names the
	// machine. Present whenever the machine is known, whichever route was
	// offered: a person deciding between the two players wants to know what
	// switching would give them.
	Controls    []playControl `json:"controls,omitempty"`
	ControlsFor string        `json:"controlsFor,omitempty"`

	// Keyboard says the machine is driven by the actual keyboard rather than by
	// the pad below -- see keyboardMachines. Without it, a DOS game's panel is
	// 25 rows of RetroPad bindings and no mention of the thing you actually
	// type on.
	Keyboard bool `json:"keyboard,omitempty"`

	// Manuals are scanned documents inside the item. Linked straight at
	// archive.org rather than through the relay: it is a link a person clicks,
	// not something an emulator fetches, so there is no CORS problem to solve
	// and no reason to pay for the bytes.
	Manuals []playFile `json:"manuals,omitempty"`
}

// Caps. Long enough to be a real description, short enough that a verdict stays
// a verdict rather than becoming a page fetch.
const (
	maxGuideDescription = 1400
	maxGuideNotes       = 900
)

// manualFormats are archive.org `format` values that are a document somebody
// would read. Matched on the Archive's own assertion first, because a filename
// is a guess and `format` is not.
var manualFormats = map[string]bool{
	"Text PDF": true, "Image Container PDF": true, "Additional Text PDF": true,
	"DjVuTXT": true, "PDF": true, "Djvu XML": false,
}

// manualWords are the filename fallback, for items whose scans were uploaded
// without a recognised format.
var manualWords = []string{"manual", "instruction", "guide", "booklet", "docs"}

// looksLikeManual decides whether a file is a document worth offering.
//
// Deliberately narrow. An item's file list is mostly screenshots, torrents and
// the Archive's own XML; offering all of it would bury the two files that are
// actually the manual under fifteen that are not.
func looksLikeManual(name, format string) bool {
	lower := strings.ToLower(name)
	// The Archive's own bookkeeping is never a manual, and `_meta.xml` would
	// otherwise match nothing and `_djvu.txt` would match everything.
	for _, skip := range []string{"_meta.", "_files.xml", "_reviews.xml", "_archive.torrent", "_chocr.", "_hocr."} {
		if strings.Contains(lower, skip) {
			return false
		}
	}
	if manualFormats[format] {
		return true
	}
	if !strings.HasSuffix(lower, ".pdf") && !strings.HasSuffix(lower, ".txt") {
		return false
	}
	for _, w := range manualWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// joinRaw reads a field the Archive sends as one string or as a list of them,
// and rejoins a list with blank lines so a two-part description reads as two
// paragraphs rather than as one run-on sentence.
func joinRaw(raw json.RawMessage) string {
	return strings.Join(jsonStrings(raw), "\n\n")
}

// plainText flattens archive.org's description HTML.
//
// Written out rather than pulled in because the requirement is narrow and the
// failure mode of getting it wrong is somebody else's markup in our page: this
// only ever EMITS text, never markup, so there is no sanitiser to be bypassed.
// Block-level tags become newlines so paragraphs survive; everything else is
// dropped; entities are decoded last, after the tags are gone, so a `&lt;b&gt;`
// in the source cannot decode into a tag that then goes unstripped.
func plainText(raw string) string {
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))

	inTag, tagStart := false, 0
	quote := byte(0)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inTag {
			// A `>` inside an attribute value does not close the tag. Tracking
			// quotes is what stops `<a title="a > b">` from leaking `b">`.
			switch {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '>':
				inTag = false
				name := strings.ToLower(strings.Trim(raw[tagStart:i], "/ \t\r\n"))
				if idx := strings.IndexAny(name, " \t\r\n/"); idx >= 0 {
					name = name[:idx]
				}
				switch name {
				case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "blockquote":
					b.WriteByte('\n')
				}
			}
			continue
		}
		if c == '<' {
			inTag, tagStart, quote = true, i+1, 0
			continue
		}
		b.WriteByte(c)
	}

	text := html.UnescapeString(b.String())

	// Collapse runs of whitespace inside a line, and runs of blank lines to one.
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.Join(strings.Fields(line), " "))
		if line == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// clip shortens on a word boundary, so a description never ends mid-word.
// Counted in runes rather than bytes: cutting a UTF-8 sequence in half produces
// a replacement character, which reads as a rendering bug.
func clip(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cut := string(runes[:max])
	if i := strings.LastIndexAny(cut, " \n"); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \n.,;:") + "…"
}

// guideFor assembles the guide for one item.
//
// `core` may be empty -- an Intellivision item has no EmulatorJS machine -- and
// the guide is still worth having, because the description and the Archive's own
// instructions are exactly what somebody sent to their player needs.
func (p *playArchive) guideFor(meta *playItemMetadata, core, machine string) *playGuide {
	g := &playGuide{
		Description: clip(plainText(joinRaw(meta.Metadata.Description)), maxGuideDescription),
		Controller:  strings.TrimSpace(strings.Join(jsonStrings(meta.Metadata.Controller), ", ")),
	}

	if notes := clip(plainText(joinRaw(meta.Metadata.EmulatorInstructions)), maxGuideNotes); notes != "" {
		g.Notes = notes
		// Named every time it is present. The Archive writes these against its
		// own player, and a reader who assumes otherwise will press 1 for SELECT
		// in a player where 1 is quick-save.
		g.NotesFor = routeArchive
	}

	if core != "" {
		g.Controls = controlsFor(core)
		g.ControlsFor = machine
		g.Keyboard = keyboardMachines[core]
		if g.ControlsFor == "" {
			g.ControlsFor = core
		}
	}

	g.Manuals = p.manualsIn(meta)

	if g.Description == "" && g.Notes == "" && len(g.Controls) == 0 && len(g.Manuals) == 0 {
		// Nothing to say. An empty panel is worse than no panel, and a nil guide
		// is how a client is told which of the two to draw.
		return nil
	}
	return g
}

// manualsIn finds the documents in an item, largest first -- a 6 MB scan is the
// manual and a 4 KB text file beside it is a note about the upload.
func (p *playArchive) manualsIn(meta *playItemMetadata) []playFile {
	id := meta.Metadata.Identifier
	if id == "" {
		return nil
	}
	var out []playFile
	for _, f := range meta.Files {
		if !looksLikeManual(f.Name, f.Format) {
			continue
		}
		size := parseArchiveSize(f.Size)
		if size < 0 {
			size = 0
		}
		out = append(out, playFile{
			Name:   path.Base(f.Name),
			URL:    p.downloadBase + url.PathEscape(id) + "/" + archiveFilePath(f.Name),
			Format: f.Format,
			Size:   size,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Size != out[j].Size {
			return out[i].Size > out[j].Size
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}
