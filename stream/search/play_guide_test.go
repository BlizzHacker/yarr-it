package main

// Tests for the guide and for the two capabilities a visitor can declare.
//
// The thing being defended here is subtler than "does it compile". A control
// list that is plausible and wrong is worse than none, because a player who
// presses the key it names and gets nothing concludes the emulator is broken and
// leaves. So the key map is checked against EmulatorJS's own vocabulary, and the
// two capability switches are checked to move in ONE direction only: they may
// turn a refusal into a game, and they may never turn a game into a refusal.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// --- the key map ------------------------------------------------------------

// Every button in every scheme must be a RetroPad id EmulatorJS knows. An id
// outside 0..29 is silently dropped by EmulatorJS's own setup loop, so a typo
// here would produce a control row for a button that can never be pressed.
func TestEveryControlSchemeUsesRealButtonIDs(t *testing.T) {
	check := func(name string, scheme []ejsButton) {
		seen := map[int]bool{}
		for _, b := range scheme {
			if b.ID < 0 || b.ID > 29 {
				t.Errorf("%s: button %q has id %d, outside the 0-29 EmulatorJS reads",
					name, b.Label, b.ID)
			}
			if seen[b.ID] {
				t.Errorf("%s: id %d appears twice, so one of the two labels is wrong",
					name, b.ID)
			}
			seen[b.ID] = true
			if strings.TrimSpace(b.Label) == "" {
				t.Errorf("%s: id %d has no label", name, b.ID)
			}
		}
	}
	check("default", ejsDefaultScheme)
	for name, scheme := range ejsControlSchemes {
		check(name, scheme)
	}
	check("save buttons", ejsSaveButtons)
}

// A scheme keyed on a name EmulatorJS never produces is a scheme that can never
// be selected -- the exact shape of the `gbc`/`pcecd`/`vice_x64` trap that cost
// a real library 1,451 games, one layer up.
func TestEveryControlSchemeNamesARealEmulatorJSSystem(t *testing.T) {
	for name := range ejsControlSchemes {
		if _, ok := emulatorJSSystems[name]; !ok {
			t.Errorf("control scheme %q is not an EmulatorJS system, so nothing will ever select it",
				name)
		}
	}
	for name := range coinSchemes {
		if _, ok := emulatorJSSystems[name]; !ok {
			t.Errorf("coin scheme %q is not an EmulatorJS system", name)
		}
	}
}

// The whole point of the panel: every machine we will actually boot must be able
// to answer "which key is fire", and must answer with a direction pad too --
// a game you cannot move in is not a game you can play.
func TestEveryPlayableMachineHasAUsableKeyMap(t *testing.T) {
	for emulator, plat := range archivePlaySystems {
		if plat.Core == "" {
			continue
		}
		if _, blocked := blockedSystems[plat.Core]; blocked {
			continue
		}
		controls := controlsFor(plat.Core)
		if len(controls) < 5 {
			t.Errorf("%s (%s): only %d controls; a person cannot play from that",
				emulator, plat.Core, len(controls))
			continue
		}
		var dirs, action int
		for _, c := range controls {
			switch c.Key {
			case "↑", "↓", "←", "→":
				dirs++
			}
			if c.Key != "" && c.Key != "↑" && c.Key != "↓" && c.Key != "←" && c.Key != "→" {
				action++
			}
		}
		if dirs != 4 {
			t.Errorf("%s (%s): %d direction keys, want 4", emulator, plat.Core, dirs)
		}
		if action == 0 {
			t.Errorf("%s (%s): no key does anything but move", emulator, plat.Core)
		}
	}
}

// A control with no key is not a control. EmulatorJS leaves the stick clicks and
// the fast-forward/rewind shortcuts unbound, and listing them would put three
// rows saying nothing in the middle of the answer.
func TestUnboundButtonsAreNotOffered(t *testing.T) {
	// atari2600 is the case that proves it: EmulatorJS lists COLOR and B/W for
	// the 2600 and binds neither.
	for _, c := range controlsFor("atari2600") {
		if c.Key == "" {
			t.Errorf("atari2600 offers %q with no key", c.Button)
		}
		if c.Button == "COLOR" || c.Button == "B/W" {
			t.Errorf("atari2600 offers %q, which EmulatorJS leaves unbound", c.Button)
		}
	}
	// And the one that matters most on a 2600: fire is the left CTRL... in the
	// Archive's player. In ours it is `x`, and saying so is the whole job.
	fire := ""
	for _, c := range controlsFor("atari2600") {
		if c.Button == "FIRE" {
			fire = c.Key
		}
	}
	if fire != "X" {
		t.Errorf("2600 FIRE = %q, want X -- EmulatorJS binds RetroPad button 0 to x", fire)
	}
}

// An arcade cabinet does nothing at all until a coin goes in, so a player who is
// not told which key inserts one concludes it is broken in about four seconds.
func TestArcadeSaysWhichKeyInsertsACoin(t *testing.T) {
	var coin, plainSelect bool
	for _, c := range controlsFor("mame") {
		if c.Button == "INSERT COIN" {
			coin = true
			if c.Key != "V" {
				t.Errorf("INSERT COIN = %q, want V", c.Key)
			}
		}
		if c.Button == "SELECT" {
			plainSelect = true
		}
	}
	if !coin {
		t.Error("no INSERT COIN row for an arcade machine")
	}
	if plainSelect {
		t.Error("arcade still shows SELECT; EmulatorJS relabels it")
	}
	// A console must NOT get the relabel.
	for _, c := range controlsFor("nes") {
		if c.Button == "INSERT COIN" {
			t.Error("the NES does not take coins")
		}
	}
}

// An unknown machine gets the RetroPad rather than nothing: EmulatorJS itself
// falls back to that layout, so reporting it is describing what will happen.
func TestAnUnknownMachineFallsBackToTheRetroPad(t *testing.T) {
	// segaMD is real, playable, and has no scheme of its own in EmulatorJS.
	if _, special := ejsControlSchemes["segaMD"]; special {
		t.Skip("segaMD gained its own scheme upstream; this test has served its purpose")
	}
	got := controlsFor("segaMD")
	if len(got) == 0 {
		t.Fatal("Mega Drive got no controls at all")
	}
	if controlsFor("") != nil {
		t.Error("a machine we do not know is not the same as a machine with a default pad")
	}
}

// --- flattening the Archive's HTML ------------------------------------------

func TestPlainTextFlattensWhatTheArchiveActuallySends(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"tags go, text stays", "<b>Pitfall!</b> is a game", "Pitfall! is a game"},
		{"br becomes a break", "one<br />two", "one\ntwo"},
		// A blank line between paragraphs, not a single break: the client splits
		// on the blank line to get paragraphs, and a single \n inside one of them
		// is a line break the source asked for.
		{"paragraphs survive as paragraphs", "<p>one</p><p>two</p>", "one\n\ntwo"},
		{"entities decode", "Tom &amp; Jerry &quot;fun&quot;", `Tom & Jerry "fun"`},
		{
			"a link is flattened to its words",
			`Click <a href="https://archive.org/x" rel="ugc nofollow">here</a> for the manual.`,
			"Click here for the manual.",
		},
		{
			// The one that a naive `<[^>]*>` strip gets wrong.
			"a > inside an attribute does not close the tag",
			`<a title="a > b">text</a>`,
			"text",
		},
		{
			// And the one that makes stripping-then-decoding the right order:
			// decode first and this would produce a live tag.
			"an escaped tag stays escaped text",
			"&lt;script&gt;alert(1)&lt;/script&gt;",
			"<script>alert(1)</script>",
		},
		{"runs of space collapse", "a    b\t\tc", "a b c"},
		// Six newlines' worth of markup collapses to one paragraph break, not six.
		{"runs of blank lines collapse", "<p>a</p><br /><br /><br /><p>b</p>", "a\n\nb"},
		{"empty stays empty", "", ""},
	} {
		if got := plainText(tc.in); got != tc.want {
			t.Errorf("%s:\n  in   %q\n  got  %q\n  want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// Whatever comes out must be text, never markup: it is going into a page.
func TestPlainTextNeverEmitsATag(t *testing.T) {
	for _, in := range []string{
		`<img src=x onerror="alert(1)">`,
		`<script>alert(1)</script>`,
		`<div onclick='x'>hi</div>`,
		`<a href="javascript:alert(1)">click</a>`,
		"unclosed <b tag that never ends",
	} {
		got := plainText(in)
		if strings.ContainsAny(got, "<>") && !strings.Contains(in, "&lt;") {
			t.Errorf("plainText(%q) = %q, which still carries angle brackets", in, got)
		}
	}
}

func TestClipStopsOnAWordAndNotInsideARune(t *testing.T) {
	long := strings.Repeat("word ", 400)
	got := clip(long, 40)
	if len([]rune(got)) > 41 {
		t.Errorf("clip returned %d runes for a cap of 40", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("clipped text does not say it was clipped: %q", got)
	}
	// Multi-byte input must not come back with a replacement character.
	if got := clip(strings.Repeat("日本語テキスト", 50), 10); strings.Contains(got, "\uFFFD") {
		t.Errorf("clip cut a rune in half: %q", got)
	}
	if got := clip("short", 40); got != "short" {
		t.Errorf("clip shortened something already short: %q", got)
	}
}

// --- the guide on a real-shaped item ----------------------------------------

// riverRaidJSON is shaped exactly like the live item
// `atari_2600_river_raid_...`, whose `emulator_instructions` was read from
// archive.org on 2026-08-07 and is quoted here verbatim. That field is the
// single most useful thing on the whole item and nothing else in the corpus
// replaces it.
func riverRaidJSON(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"identifier": "river_raid", "title": "River Raid",
			"mediatype": "software", "emulator": "a2600", "emulator_ext": "bin",
			"collection": []string{"consolelivingroom", "emulation"},
			"controller": "joystick",
			"description": "<b>Click <a href=\"https://archive.org/details/x\" rel=\"ugc nofollow\">here</a> " +
				"to view the manual to this game.</b><br />River Raid is a scrolling shooter " +
				"designed by Carol Shaw &amp; published by Activision in 1982.",
			"emulator_instructions": "The Atari 2600 looks like <a href=\"https://x/\">this</a>. " +
				"The 1 key pushes the SELECT switch, and the 2 key pushes the RESET switch. " +
				"Use the arrow keys to move Up/Down/Left/Right with the Joystick.",
		},
		"files": []map[string]any{
			{"name": "river_raid.bin", "format": "Unknown", "size": "4096"},
			{"name": "river_raid_manual.pdf", "format": "Text PDF", "size": "2400000"},
			{"name": "notes.txt", "format": "Text", "size": "80"},
			{"name": "river_raid_meta.xml", "format": "Metadata", "size": "3640"},
			{"name": "screenshot_00.png", "format": "PNG", "size": "3579"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestTheGuideCarriesTheArchivesOwnInstructionsAndSaysWhoseTheyAre(t *testing.T) {
	p := fakeArchive(t, map[string]string{"river_raid": riverRaidJSON(t)})
	got := p.Resolve(context.Background(), "river_raid")

	if got.Guide == nil {
		t.Fatal("no guide on an item that has a description AND instructions")
	}
	g := got.Guide

	if !strings.Contains(g.Notes, "arrow keys") {
		t.Errorf("notes lost the control text: %q", g.Notes)
	}
	if strings.ContainsAny(g.Notes, "<>") {
		t.Errorf("notes still carry markup: %q", g.Notes)
	}
	// The honesty rule. "The 1 key pushes the SELECT switch" is true in THEIR
	// player and false in ours, where 1 is quick-save. Presenting it unlabelled
	// would be confidently wrong, which is worse than silent.
	if g.NotesFor != routeArchive {
		t.Errorf("notesFor=%q; the Archive writes these against its own player", g.NotesFor)
	}
	if !strings.Contains(g.Description, "Carol Shaw") || strings.Contains(g.Description, "href") {
		t.Errorf("description = %q", g.Description)
	}
	if g.Controller != "joystick" {
		t.Errorf("controller=%q", g.Controller)
	}
	if g.ControlsFor != "Atari 2600" {
		t.Errorf("controlsFor=%q, want the machine a person recognises", g.ControlsFor)
	}
	if len(g.Controls) == 0 {
		t.Fatal("no key map for a machine we are about to boot")
	}
	if len(g.Manuals) != 1 || g.Manuals[0].Name != "river_raid_manual.pdf" {
		t.Errorf("manuals = %+v; want just the scan, not the metadata XML", g.Manuals)
	}
	if !strings.Contains(g.Manuals[0].URL, "/download/river_raid/river_raid_manual.pdf") {
		t.Errorf("manual URL = %q", g.Manuals[0].URL)
	}
}

// A refusal needs the guide MORE than a success does: the person is being sent
// to a player with no on-screen pad at all.
func TestTheGuideSurvivesEveryRefusal(t *testing.T) {
	items := map[string]string{
		"river_raid": riverRaidJSON(t),
		"arcade_item": itemJSON(t, map[string]any{
			"identifier": "arcade_item", "title": "Contra",
			"mediatype": "software", "emulator": "mame", "emulator_ext": "zip",
			"description": "<p>An arcade game.</p>",
		}, []fakeFile{{"contra.zip", "1024"}}),
		"intv_item": itemJSON(t, map[string]any{
			"identifier": "intv_item", "title": "Astrosmash",
			"mediatype": "software", "emulator": "intv", "emulator_ext": "int",
			"description": "<p>An Intellivision game.</p>",
		}, []fakeFile{{"astro.int", "8192"}}),
	}
	p := fakeArchive(t, items)

	for _, id := range []string{"arcade_item", "intv_item"} {
		got := p.Resolve(context.Background(), id)
		if got.Route != routeArchive {
			t.Fatalf("%s: route=%q, expected the fixture to be refused", id, got.Route)
		}
		if got.Guide == nil || got.Guide.Description == "" {
			t.Errorf("%s: refused to their player with nothing to read", id)
		}
	}

	// An Intellivision has no EmulatorJS core, so there is no key map to offer
	// and inventing one would be a lie about a player that will not run it.
	if g := p.Resolve(context.Background(), "intv_item").Guide; g != nil && len(g.Controls) > 0 {
		t.Error("offered a key map for a machine no core here can run")
	}
	// Arcade DOES have a core name in the table, and is refused for a reason no
	// visitor can lift -- but the buttons an arcade cabinet has are still worth
	// naming, because the Archive's player has the same ones.
	if g := p.Resolve(context.Background(), "arcade_item").Guide; g == nil || len(g.Controls) == 0 {
		t.Error("arcade got no button list at all")
	}
}

// An item with nothing to say gets no guide, rather than an empty panel.
func TestAnItemWithNothingToSayGetsNoGuide(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"bare": itemJSON(t, map[string]any{
			"identifier": "bare", "title": "Bare", "mediatype": "texts",
		}, []fakeFile{{"bare_meta.xml", "10"}}),
	})
	if got := p.Resolve(context.Background(), "bare"); got.Guide != nil {
		t.Errorf("guide = %+v on an item with no description, no notes and no machine", got.Guide)
	}
}

// archive.org sends a bare string for one value and a list for several. A
// two-part description must not decode as an outage -- that trap has already
// been paid for once in this file's sibling, over `emulator_ext`.
func TestATwoPartDescriptionIsNotAnOutage(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"identifier": "two", "title": "Two", "mediatype": "software",
			"emulator": "nes", "emulator_ext": "nes",
			"description": []string{"<p>First half.</p>", "<p>Second half.</p>"},
		},
		"files": []map[string]any{{"name": "g.nes", "format": "Unknown", "size": "16"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := fakeArchive(t, map[string]string{"two": string(body)})

	got := p.Resolve(context.Background(), "two")
	if got.Route != routeEmulatorJS {
		t.Fatalf("route=%q reasons=%v; a list-valued description broke the decode",
			got.Route, reasonCodes(got))
	}
	if got.Guide == nil || !strings.Contains(got.Guide.Description, "First half") ||
		!strings.Contains(got.Guide.Description, "Second half") {
		t.Errorf("guide lost half the description: %+v", got.Guide)
	}
}

// --- bring your own BIOS -----------------------------------------------------

// Every machine that can be unblocked by firmware must actually BE blocked for
// firmware, or the offer leads nowhere.
func TestEveryBIOSOfferMatchesARealBlock(t *testing.T) {
	for system, need := range biosRequirements {
		if _, ok := emulatorJSSystems[system]; !ok {
			t.Errorf("BIOS offered for %q, which is not an EmulatorJS system", system)
		}
		b, blocked := blockedSystems[system]
		if !blocked || b.Reason != reasonNeedsBIOS {
			t.Errorf("BIOS offered for %q, which is not blocked for firmware", system)
		}
		if need.System != system {
			t.Errorf("BIOS for %q files itself under %q", system, need.System)
		}
		if len(need.Files) == 0 || need.Label == "" || len(need.Detail) < 40 {
			t.Errorf("BIOS offer for %q does not tell a person what to find", system)
		}
	}
	// And the converse: every firmware block must be liftable, or somebody is
	// refused for a reason they could have fixed and is never told so.
	for system, b := range blockedSystems {
		if b.Reason == reasonNeedsBIOS {
			if _, ok := biosRequirements[system]; !ok {
				t.Errorf("%q is blocked for firmware with no way to supply it", system)
			}
		}
	}
}

func colecoItem(t *testing.T, extra map[string]any, files []fakeFile) string {
	t.Helper()
	meta := map[string]any{
		"identifier": "coleco_game", "title": "Donkey Kong (ColecoVision)",
		"mediatype": "software", "emulator": "coleco", "emulator_ext": "col",
		"description": "<p>A ColecoVision game.</p>",
	}
	for k, v := range extra {
		meta[k] = v
	}
	if files == nil {
		files = []fakeFile{{"dk.col", "32768"}}
	}
	return itemJSON(t, meta, files)
}

func TestSupplyingTheBIOSUnblocksTheMachine(t *testing.T) {
	p := fakeArchive(t, map[string]string{"coleco_game": colecoItem(t, nil, nil)})

	// Without it: refused, and told exactly what would lift the refusal.
	before := p.Resolve(context.Background(), "coleco_game")
	if before.Route != routeArchive || !hasReason(before, reasonNeedsBIOS) {
		t.Fatalf("route=%q reasons=%v", before.Route, reasonCodes(before))
	}
	if before.BiosNeeded == nil {
		t.Fatal("refused for firmware without naming the firmware")
	}
	if before.BiosNeeded.Files[0] != "colecovision.rom" {
		t.Errorf("named %q", before.BiosNeeded.Files)
	}

	// With it: our own player, the ROM, and the BIOS still named so the client
	// knows which of its stored files to hand over.
	after := p.ResolveWith(context.Background(), "coleco_game",
		playOptions{BIOS: map[string]bool{"coleco": true}})
	if after.Route != routeEmulatorJS || !after.Playable {
		t.Fatalf("route=%q reasons=%v; a supplied BIOS did not unblock the machine",
			after.Route, reasonCodes(after))
	}
	if after.Core != "coleco" || after.ROM == nil || after.ROM.Name != "dk.col" {
		t.Errorf("core=%q rom=%+v", after.Core, after.ROM)
	}
	if after.BiosNeeded == nil || after.BiosNeeded.System != "coleco" {
		t.Error("unblocked without saying which BIOS the player must be handed")
	}
	if len(after.Reasons) != 0 {
		t.Errorf("unblocked and still complaining: %v", reasonCodes(after))
	}
}

// A BIOS lifts the firmware block and nothing else. Every other refusal is about
// the ITEM, and no file a visitor supplies changes an item.
func TestABIOSDoesNotLiftAnyOtherRefusal(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"stream_only_coleco": colecoItem(t,
			map[string]any{"collection": []string{"consolelivingroom", "stream_only"}}, nil),
		// 768 MiB, past maxDirectROMBytes. This fixture used to be 95 MiB, which
		// was past the single 48 MiB ceiling of the day; the ceiling that matters
		// is now what a BROWSER holds rather than what our relay carries, so the
		// number moved and the point did not.
		"huge_coleco": colecoItem(t, nil, []fakeFile{{"dk.col", "805306368"}}),
		"no_payload_coleco": colecoItem(t, nil,
			[]fakeFile{{"readme.txt", "10"}}),
		"arcade_item": itemJSON(t, map[string]any{
			"identifier": "arcade_item", "title": "Contra",
			"mediatype": "software", "emulator": "mame", "emulator_ext": "zip",
		}, []fakeFile{{"contra.zip", "1024"}}),
	})
	all := playOptions{BIOS: map[string]bool{"coleco": true, "psx": true, "amiga": true}, Isolated: true}

	// stream_only is deliberately NOT in this list any more: it never was a
	// technical obstacle, and it no longer routes anywhere. See
	// TestAStreamOnlyItemPlaysHereAndIsNotOfferedAsADownload.
	for id, want := range map[string]string{
		"huge_coleco":       reasonTooLarge,
		"no_payload_coleco": reasonNoPayload,
		"arcade_item":       reasonNoCore,
	} {
		got := p.ResolveWith(context.Background(), id, all)
		if got.Route != routeArchive {
			t.Errorf("%s: route=%q; a BIOS is not a licence to ignore %s", id, got.Route, want)
		}
		if !hasReason(got, want) {
			t.Errorf("%s: reasons=%v, want %s", id, reasonCodes(got), want)
		}
		// And no offer to fetch firmware for a game that would still be refused.
		if got.BiosNeeded != nil && id != "no_payload_coleco" {
			t.Errorf("%s: offered a BIOS for an item that would still not play", id)
		}
	}
}

// The offer must only appear where taking it up actually helps. Sending somebody
// off to find a 512 KB firmware dump for a game that will then be refused for its
// size is a longer route to the same no.
func TestTheBIOSOfferIsWithheldWhereItWouldNotHelp(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		// Past what a browser will hold -- see the sibling test for why this is
		// 768 MiB rather than the 95 MiB it once was.
		"huge_coleco": colecoItem(t, nil, []fakeFile{{"dk.col", "805306368"}}),
		"stream_only_coleco": colecoItem(t,
			map[string]any{"collection": []string{"stream_only"}}, nil),
	})
	// Only the size case now. A stream-only item DOES play here once it has
	// firmware, so withholding the offer for one would be hiding the one thing
	// that would help.
	if got := p.Resolve(context.Background(), "huge_coleco"); got.BiosNeeded != nil {
		t.Error("huge_coleco: offered a BIOS that would not make it play")
	}
	if got := p.Resolve(context.Background(), "stream_only_coleco"); got.BiosNeeded == nil {
		t.Error("stream_only_coleco: firmware IS the only thing between this and " +
			"our player, so the offer belongs here")
	}
}

// --- cross-origin isolation --------------------------------------------------

func TestIsolationUnblocksTheThreadedCores(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"dos_game": itemJSON(t, map[string]any{
			"identifier": "dos_game", "title": "Commander Keen",
			"mediatype": "software", "emulator": "dosbox", "emulator_ext": "zip",
			"description": "<p>A DOS game.</p>",
		}, []fakeFile{{"keen.zip", "500000"}}),
	})

	before := p.Resolve(context.Background(), "dos_game")
	if before.Route != routeArchive || !hasReason(before, reasonNeedsIsolation) {
		t.Fatalf("route=%q reasons=%v", before.Route, reasonCodes(before))
	}
	// Isolation is a fact about the PAGE, not about firmware, so no BIOS is
	// named and none is needed.
	if before.BiosNeeded != nil {
		t.Error("a threaded core does not need firmware")
	}

	after := p.ResolveWith(context.Background(), "dos_game", playOptions{Isolated: true})
	if after.Route != routeEmulatorJS || after.Core != "dos" {
		t.Fatalf("route=%q core=%q reasons=%v; an isolated page can run dosbox_pure",
			after.Route, after.Core, reasonCodes(after))
	}
	if after.CoreFile != "dosbox_pure" {
		t.Errorf("coreFile=%q", after.CoreFile)
	}

	// Isolation must not be mistaken for a BIOS.
	coleco := fakeArchive(t, map[string]string{"coleco_game": colecoItem(t, nil, nil)})
	if got := coleco.ResolveWith(context.Background(), "coleco_game",
		playOptions{Isolated: true}); got.Route != routeArchive {
		t.Error("an isolated page was treated as though it had a ColecoVision BIOS")
	}
}

// --- the wire ----------------------------------------------------------------

func TestParsePlayOptionsOnlyBelievesWhatItCanActOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  playOptions
	}{
		{"nothing declared", "", playOptions{}},
		{"one machine", "bios=coleco", playOptions{BIOS: map[string]bool{"coleco": true}}},
		{
			"a comma list",
			"bios=coleco,psx",
			playOptions{BIOS: map[string]bool{"coleco": true, "psx": true}},
		},
		{
			"repeated params",
			"bios=coleco&bios=amiga",
			playOptions{BIOS: map[string]bool{"coleco": true, "amiga": true}},
		},
		{"spaces are trimmed", "bios= coleco , psx ", playOptions{BIOS: map[string]bool{"coleco": true, "psx": true}}},
		// A machine no BIOS could unblock must not become a declared capability,
		// or a typo starts looking like one.
		{"an unrelated machine is ignored", "bios=nes", playOptions{}},
		{"a typo is ignored", "bios=colecovision", playOptions{}},
		{"empty entries are ignored", "bios=,,", playOptions{}},
		{"isolation", "isolated=1", playOptions{Isolated: true}},
		{"isolation must be exactly 1", "isolated=true", playOptions{}},
	} {
		q, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := parsePlayOptions(q)
		if got.Isolated != tc.want.Isolated {
			t.Errorf("%s: isolated=%v want %v", tc.name, got.Isolated, tc.want.Isolated)
		}
		if len(got.BIOS) != len(tc.want.BIOS) {
			t.Errorf("%s: bios=%v want %v", tc.name, got.BIOS, tc.want.BIOS)
			continue
		}
		for k := range tc.want.BIOS {
			if !got.BIOS[k] {
				t.Errorf("%s: bios missing %q (got %v)", tc.name, k, got.BIOS)
			}
		}
	}
}

func TestTheEndpointHonoursTheDeclaredCapabilities(t *testing.T) {
	p := fakeArchive(t, map[string]string{"coleco_game": colecoItem(t, nil, nil)})
	mux := http.NewServeMux()
	p.register(mux)

	ask := func(query string) playAnswer {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/play/archive?"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", query, rec.Code)
		}
		var out playAnswer
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}

	if got := ask("id=coleco_game"); got.Route != routeArchive {
		t.Errorf("undeclared: route=%q, want the conservative answer", got.Route)
	}
	if got := ask("id=coleco_game&bios=coleco"); got.Route != routeEmulatorJS {
		t.Errorf("declared: route=%q reasons=%v", got.Route, reasonCodes(got))
	}
	// The guide has to survive the round trip, or the panel is empty in the one
	// place it is actually rendered.
	if got := ask("id=coleco_game"); got.Guide == nil || got.Guide.Description == "" {
		t.Errorf("guide did not survive JSON: %+v", got.Guide)
	}
}

// The systems endpoint has to say which refusals a visitor could lift, or a
// settings screen would need its own copy of that judgement.
func TestTheSystemsEndpointSaysWhatCanBeUnlocked(t *testing.T) {
	mux := http.NewServeMux()
	registerPlayRoutes(mux, ownerAuth())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/play/systems", nil))

	var body struct {
		Systems []struct {
			Emulator   string `json:"emulator"`
			Reason     string `json:"reason"`
			Unlockable string `json:"unlockable"`
			Playable   bool   `json:"playable"`
		} `json:"systems"`
		BIOS []playBIOS `json:"bios"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	if len(body.BIOS) != len(biosRequirements) {
		t.Errorf("published %d BIOS offers, table has %d", len(body.BIOS), len(biosRequirements))
	}
	// ColecoVision is the only machine left whose firmware a PERSON has to
	// supply, and so the only one with anything to unlock.
	//
	// PlayStation and Amiga were here and are not any more, and that is a
	// correction rather than a loss: builtInFirmware answers both with a core
	// option -- pcsx_rearmed's HLE BIOS, libretro-uae's AROS -- so there is no
	// file for anybody to go and find. Offering to unlock a machine that is not
	// locked is its own small dishonesty, and it was sending people after
	// firmware they never needed.
	want := map[string]string{
		"coleco": "bios", "psx": "", "sae-a500": "",
		"dosbox": "isolation", "mame": "",
	}
	for _, s := range body.Systems {
		if expect, ok := want[s.Emulator]; ok && s.Unlockable != expect {
			t.Errorf("%s: unlockable=%q want %q (reason %q)",
				s.Emulator, s.Unlockable, expect, s.Reason)
		}
	}
	// ...and the two that stopped being locked must now say they PLAY, rather
	// than falling into some third state that is neither refused nor offered.
	for _, s := range body.Systems {
		if s.Emulator == "psx" || s.Emulator == "sae-a500" {
			if !s.Playable || s.Reason != "" {
				t.Errorf("%s: playable=%v reason=%q; the core supplies its own firmware",
					s.Emulator, s.Playable, s.Reason)
			}
		}
	}
}

// A DOS game is played on the keyboard. EmulatorJS has no per-machine layout
// for DOS -- getControlScheme() falls through to the generic RetroPad -- so its
// control menu shows 25 rows of L3/R3/R-STICK. Printing that for Commander Keen
// and never mentioning the keyboard is accurate about the emulator and useless
// about the game.
func TestKeyboardMachinesSayTheyAreKeyboardMachines(t *testing.T) {
	for _, core := range []string{"dos", "amiga", "c64"} {
		if !keyboardMachines[core] {
			t.Errorf("%q is a keyboard machine and is not marked as one", core)
		}
	}
	for _, core := range []string{"nes", "snes", "segaMD", "atari2600", "gb"} {
		if keyboardMachines[core] {
			t.Errorf("%q is a console; nobody types at it", core)
		}
	}
	// Every one must be a real EmulatorJS system, or the flag guards nothing.
	for core := range keyboardMachines {
		if _, ok := emulatorJSSystems[core]; !ok {
			t.Errorf("keyboard machine %q is not an EmulatorJS system", core)
		}
	}

	p := fakeArchive(t, map[string]string{
		"dos_game": itemJSON(t, map[string]any{
			"identifier": "dos_game", "title": "Keen", "mediatype": "software",
			"emulator": "dosbox", "emulator_ext": "zip", "description": "<p>A DOS game.</p>",
		}, []fakeFile{{"keen.zip", "500000"}}),
		"nes_game": itemJSON(t, map[string]any{
			"identifier": "nes_game", "title": "Punch-Out", "mediatype": "software",
			"emulator": "nes", "emulator_ext": "nes", "description": "<p>A NES game.</p>",
		}, []fakeFile{{"po.nes", "262160"}}),
	})
	if g := p.Resolve(context.Background(), "dos_game").Guide; g == nil || !g.Keyboard {
		t.Error("a DOS item did not say it is played on the keyboard")
	}
	if g := p.Resolve(context.Background(), "nes_game").Guide; g == nil || g.Keyboard {
		t.Error("a NES item claimed to be a keyboard machine")
	}
}
