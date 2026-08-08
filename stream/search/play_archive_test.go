package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The tests that matter here are of two kinds, and the first kind is unusual
// enough to say why it exists.
//
// TABLE TESTS guard against the failure that has cost this project the most:
// a name that looks right, browses fine, and fails only when somebody presses
// Play. `gbc` is not an EmulatorJS system. `vice_x64` is not an EmulatorJS
// system. Neither is `pcecd` or `mame2003`. All four look exactly like the ones
// that are real, and in a real library those four names cost 1,451 games that
// worked until the button was pressed. So the table is checked against
// EmulatorJS's own vocabulary and against ROM Hub's, rather than reviewed.
//
// DECISION TESTS drive the resolver against a fake archive.org built from the
// SHAPES of real items -- the metadata below is trimmed from live responses
// captured on 2026-08-07, including the two things that trip a naive parser:
// `size` is a decimal string, and `collection` is a list except when an item is
// in exactly one, where it is a bare string.

// --- fixtures copied from ROM Hub ------------------------------------------
//
// Vendored on 2026-08-07 with the release stated, exactly the way ROM Hub
// vendors RomM's own tables and for the same reason: a stale copy that says
// what it is stale relative to can be checked, and one that does not, cannot.
// When either upstream changes, these are re-read and the tests below say which
// rows disagree.

// romHubPlayableSlugs is `rom_hub.playability.EJS_CORES`, read from ROM Hub at
// RomM 4.9.2. A platform slug in here has an EmulatorJS core in a default RomM;
// one absent from it is catalogue-only.
var romHubPlayableSlugs = map[string]bool{
	"3do": true, "acpc": true, "amiga": true, "amiga-cd32": true, "arcade": true,
	"atari-2600-plus": true, "atari-lynx-mkii": true, "atari2600": true, "atari5200": true,
	"atari7800": true, "c-plus-4": true, "c128": true, "c64": true, "colecovision": true,
	"commmodore-128": true, "commodore-64c": true, "cpet": true, "doom": true, "dos": true,
	"famicom": true, "fds": true, "game-boy-adavance-sp": true, "game-boy-light": true,
	"game-boy-micro": true, "game-boy-pocket": true, "game-televisison": true,
	"gamegear": true, "gb": true, "gba": true, "gbc": true, "genesis": true,
	"ique-player": true, "jaguar": true, "lynx": true, "master-system-girl": true,
	"master-system-super-compact": true, "mega-pc": true, "n64": true, "nds": true,
	"neo-geo-pocket": true, "neo-geo-pocket-color": true, "neogeoaes": true,
	"neogeomvs": true, "nes": true, "new-style-nes": true,
	"new-style-super-nes-model-sns-101": true, "nintendo-ds-lite": true, "nintendo-dsi": true,
	"nintendo-dsi-xl": true, "pc-fx": true, "philips-cd-i": true, "psp": true, "psx": true,
	"saturn": true, "sega-game-box-9": true, "sega-mark-iii": true,
	"sega-master-system-ii": true, "sega-mega-drive-2-slash-genesis": true,
	"sega-mega-jet": true, "sega-nomad": true, "sega32": true, "segacd": true, "sfam": true,
	"sms": true, "snes": true, "super-famicom-jr-model-shvc-101": true,
	"super-famicom-shvc-001": true, "super-nintendo-original-european-version": true,
	"supergrafx": true, "swancrystal": true, "tera-drive": true, "tg16": true,
	"turbografx-cd": true, "vic-20": true, "virtualboy": true, "wonderswan": true,
	"wonderswan-color": true, "zxs": true,
}

// romHubArchiveEmulators is `EMULATOR_PLATFORMS` from ROM Hub's archive-org
// plugin: the archive.org emulator id -> library platform slug table, built by
// sampling 2,000 live items and censusing all 132 distinct emulator values in
// the Console Living Room. It is the authority on which ids exist and which
// machine each one is.
var romHubArchiveEmulators = map[string]string{
	"32x": "sega32", "a2600": "atari2600", "a2600p": "atari2600", "a5200": "atari5200",
	"a7800": "atari7800", "a800": "atari8bit", "a800cart": "atari8bit", "a800xl": "atari8bit",
	"a800xlp": "atari8bit", "advision": "adventure-vision", "apple2": "appleii",
	"apple2e": "appleii", "apple2ee": "appleii", "apple2ee-helper": "appleii",
	"apple2eeecho": "appleii", "apple2gs": "apple-iigs", "apple2p": "appleii",
	"apple2woz": "appleii", "apple3": "appleiii", "aquarius": "aquarius",
	"arcadia": "arcadia-2001", "channelf": "fairchild-channel-f",
	"coco2cart": "trs-80-color-computer", "coco2disk": "trs-80-color-computer",
	"coco3disk": "trs-80-color-computer", "coleco": "colecovision", "cpc6128": "acpc",
	"crvision": "creativision", "dosbox": "dos", "dosbox-sync": "dos", "gameboy": "gb",
	"gamecom": "game-dot-com", "gamegear": "gamegear", "gba": "gba", "gbcolor": "gbc",
	"genesis": "genesis", "gx4000": "amstrad-gx4000", "intv": "intellivision",
	"intv2": "intellivision", "intvsrs": "intellivision", "lynx": "lynx", "mame": "arcade",
	"mc10": "trs-80-mc-10", "megadrij": "genesis", "megadriv": "genesis",
	"megaduck": "mega-duck-slash-cougar-boy", "nes": "nes", "nesp": "nes", "nespal": "nes",
	"ngp": "neo-geo-pocket", "ngpc": "neo-geo-pocket-color", "odyssey2": "odyssey-2",
	"pce-atarist": "atari-st", "pce-atarist-color": "atari-st", "pce-macplus": "mac",
	"psx": "psx", "sae-a1200": "amiga", "sae-a500": "amiga", "sae-a500p": "amiga",
	"scv": "epoch-super-cassette-vision", "sg1000": "sg1000", "sgx": "supergrafx",
	"sms": "sms", "sms-phaser": "sms", "smsj": "sms", "snes": "snes", "snesp": "snes",
	"spec128": "zxs", "spectrum": "zxs", "supracan": "super-acan", "svision": "supervision",
	"tg16": "tg16", "vectrex": "vectrex", "vice": "c64", "vice-c64": "c64",
	"vice-pet": "cpet", "vice-resid": "c64", "vice-vic20": "vic-20", "vmac-colormac": "mac",
	"wscolor": "wonderswan-color", "wswan": "wonderswan", "zx81": "zx81",
}

// --- table integrity --------------------------------------------------------

// Every core we would set as EJS_core must be a system EmulatorJS defines.
//
// This is the 1,451-game test. A core name that does not exist does not error:
// EmulatorJS asks its CDN for a core file that is not there, and the player
// shows its own "check console" screen. Nothing before the click can tell.
func TestEveryOfferedCoreIsARealEmulatorJSSystem(t *testing.T) {
	for emulator, plat := range archivePlaySystems {
		if plat.Core == "" {
			continue // deliberately coreless; the no_core path names the machine
		}
		if _, ok := emulatorJSSystems[plat.Core]; !ok {
			t.Errorf("emulator %q offers core %q, which is not an EmulatorJS system",
				emulator, plat.Core)
		}
	}
}

// The four names that look real and are not. Named individually because each
// one was a real outage, and a test that only checked the map would pass again
// the moment somebody re-added one to it.
func TestTheFourPlausibleCoreNamesAreStillRefused(t *testing.T) {
	for _, name := range []string{"gbc", "pcecd", "vice_x64", "mame2003"} {
		if _, ok := emulatorJSSystems[name]; ok {
			t.Errorf("%q is listed as an EmulatorJS system; it is not one, and a "+
				"platform mapped to it browses normally and dies on the button press", name)
		}
		for emulator, plat := range archivePlaySystems {
			if plat.Core == name {
				t.Errorf("emulator %q is mapped to %q, which does not exist", emulator, name)
			}
		}
	}
}

// Game Boy Color is the specific case the `gbc` trap comes from: EmulatorJS has
// no `gbc` system, gambatte serves both generations, and the system is `gb`.
func TestGameBoyColourUsesTheGameBoySystem(t *testing.T) {
	plat, ok := archivePlaySystems["gbcolor"]
	if !ok {
		t.Fatal("gbcolor is not in the table; 541 downloadable items lose their player")
	}
	if plat.Core != "gb" {
		t.Errorf("gbcolor offers core %q, want \"gb\" -- gambatte serves both generations",
			plat.Core)
	}
	if plat.Platform != "gbc" {
		t.Errorf("gbcolor has platform %q, want \"gbc\": the CORE is shared, the MACHINE is not",
			plat.Platform)
	}
}

// Composing ROM Hub's two tables through libretro core names is ambiguous, and
// this is the assertion that the ambiguity was resolved the right way. A
// mechanical join resolves all five of these to `segaMS`, because
// genesis_plus_gx and picodrive each appear under several EmulatorJS systems --
// which would boot a Mega Drive game as a Master System: successfully, silently,
// and to a dead screen.
func TestTheSegaMachinesAreNotCollapsedIntoOne(t *testing.T) {
	for emulator, want := range map[string]string{
		"genesis": "segaMD", "megadriv": "segaMD", "megadrij": "segaMD",
		"32x": "sega32x", "sms": "segaMS", "smsj": "segaMS", "gamegear": "segaGG",
	} {
		if got := archivePlaySystems[emulator].Core; got != want {
			t.Errorf("emulator %q offers core %q, want %q", emulator, got, want)
		}
	}
}

// If we offer Play for a machine, ROM Hub must agree that machine has a core.
// A disagreement means one of the two copies is wrong, and the point of having
// both is that neither gets to be wrong quietly.
func TestEveryPlayableMachineIsOneRomHubAlsoCallsPlayable(t *testing.T) {
	for emulator, plat := range archivePlaySystems {
		if plat.Core == "" {
			continue
		}
		if _, blocked := blockedSystems[plat.Core]; blocked {
			continue // refused here for a reason of our own; ROM Hub's view is moot
		}
		if !romHubPlayableSlugs[plat.Platform] {
			t.Errorf("emulator %q -> platform %q is offered a player here, but ROM Hub's "+
				"playability table calls that platform catalogue-only", emulator, plat.Platform)
		}
	}
}

// The reverse direction is NOT an error, and this test records why rather than
// leaving the gap to look like an oversight.
//
// ROM Hub's table is RomM's, and RomM self-hosts an EmulatorJS build that
// carries `fuse` and `cap32`. EmulatorJS's public CDN -- which is what a Yarr.It
// visitor loads -- defines neither a ZX Spectrum nor an Amstrad CPC system. So
// ROM Hub says those platforms play and here they honestly do not.
func TestSpectrumAndCPCAreRefusedEvenThoughRomHubCallsThemPlayable(t *testing.T) {
	for _, emulator := range []string{"spectrum", "spec128", "cpc6128"} {
		plat, ok := archivePlaySystems[emulator]
		if !ok {
			t.Fatalf("%q is missing from the table; its refusal cannot name the machine", emulator)
		}
		if plat.Core != "" {
			t.Errorf("%q offers core %q; EmulatorJS's CDN has no such system", emulator, plat.Core)
		}
		if !romHubPlayableSlugs[plat.Platform] {
			t.Errorf("fixture drift: ROM Hub no longer calls %q playable, so this test "+
				"no longer describes a divergence", plat.Platform)
		}
	}
}

// Ids this table carries that ROM Hub's archive-org plugin does not, each with
// the evidence that it is a real archive.org id and not something invented here.
//
// The list is deliberately tiny and deliberately explicit. An unexplained id is
// the misfiling ROM Hub's platforms.py exists to prevent; a NAMED one, with a
// count somebody can re-measure, is a gap to send upstream.
var emulatorIDsAheadOfRomHub = map[string]string{
	"n64": "8 live items, all downloadable, .n64/.z64 payloads of 4-33 MB " +
		"(measured 2026-08-07). Missed by the plugin's 2,000-item sample of a " +
		"272,000-item corpus. Worth a row in archive_org/platforms.py.",
}

// Every id must be one ROM Hub's plugin recognises, and must agree with it about
// which machine it is. This is what stops the table quietly learning an id from
// somewhere else.
func TestEveryEmulatorIDAgreesWithRomHubsPlugin(t *testing.T) {
	for emulator, plat := range archivePlaySystems {
		want, known := romHubArchiveEmulators[emulator]
		if !known {
			if _, excused := emulatorIDsAheadOfRomHub[emulator]; excused {
				continue
			}
			t.Errorf("emulator %q is not in ROM Hub's archive-org plugin table and is not "+
				"in emulatorIDsAheadOfRomHub; either it is not a real archive.org id, or "+
				"it needs a row upstream and an entry there saying so", emulator)
			continue
		}
		if plat.Platform != want {
			t.Errorf("emulator %q is filed under %q here and %q in ROM Hub's plugin",
				emulator, plat.Platform, want)
		}
	}
}

// The excuse list must stay an excuse list. Once ROM Hub gains the row, the
// entry here is dead weight that would hide the next one.
func TestTheExceptionsToRomHubAreStillExceptions(t *testing.T) {
	for emulator, why := range emulatorIDsAheadOfRomHub {
		if _, ok := romHubArchiveEmulators[emulator]; ok {
			t.Errorf("%q is in ROM Hub's plugin now; drop it from emulatorIDsAheadOfRomHub", emulator)
		}
		if _, ok := archivePlaySystems[emulator]; !ok {
			t.Errorf("%q is excused but not in the table", emulator)
		}
		if len(why) < 40 {
			t.Errorf("%q is excused without evidence anybody could re-check", emulator)
		}
	}
	// Still held to both halves of the chain, exception or not.
	for emulator := range emulatorIDsAheadOfRomHub {
		plat := archivePlaySystems[emulator]
		if plat.Core != "" {
			if _, ok := emulatorJSSystems[plat.Core]; !ok {
				t.Errorf("%q offers core %q, which is not an EmulatorJS system", emulator, plat.Core)
			}
			if !romHubPlayableSlugs[plat.Platform] {
				t.Errorf("%q is filed under %q, which ROM Hub calls catalogue-only",
					emulator, plat.Platform)
			}
		}
	}
}

// The five ids the shipped archive.go table carries that archive.org does not
// use. Measured 2026-08-07: each matches exactly zero items. Harmless, but a
// table with invented rows is one nobody can trust the rest of.
func TestTheTableCarriesNoInventedEmulatorIDs(t *testing.T) {
	for _, invented := range []string{"famicom", "superfamicom", "segaMD", "gg", "vb"} {
		if _, ok := archivePlaySystems[invented]; ok {
			t.Errorf("%q is in the table; archive.org has no items with that emulator id", invented)
		}
	}
}

// The real ids the shipped table lacked -- about 195 downloadable games that
// were never offered our player.
func TestTheRegionalSpellingsAreCovered(t *testing.T) {
	for _, emulator := range []string{"megadrij", "nesp", "nespal", "a2600p", "snesp", "sgx"} {
		plat, ok := archivePlaySystems[emulator]
		if !ok || plat.Core == "" {
			t.Errorf("%q has no core; a region suffix is a region, not a different machine",
				emulator)
		}
	}
}

// Every blocked system must be a real system, or the block is a no-op that
// looks like a safeguard.
func TestBlockedSystemsAreRealSystemsWithRealReasons(t *testing.T) {
	for system, block := range blockedSystems {
		if _, ok := emulatorJSSystems[system]; !ok {
			t.Errorf("blocked system %q is not an EmulatorJS system, so the block guards nothing",
				system)
		}
		if block.Reason == "" || len(block.Detail) < 40 {
			t.Errorf("blocked system %q has no usable reason to show a person", system)
		}
	}
}

// --- the resolver -----------------------------------------------------------

// item builds one archive.org metadata response. The JSON is assembled by hand
// rather than from a struct because the shapes that break parsers -- a string
// size, a bare-string collection -- are exactly what must be exercised.
type fakeFile struct {
	name string
	size string // decimal string, or "" for absent, as archive.org sends it
}

func itemJSON(t *testing.T, meta map[string]any, files []fakeFile) string {
	t.Helper()
	list := make([]map[string]any, 0, len(files))
	for _, f := range files {
		entry := map[string]any{"name": f.name, "format": "Unknown"}
		if f.size != "" {
			entry["size"] = f.size
		}
		list = append(list, entry)
	}
	body, err := json.Marshal(map[string]any{"metadata": meta, "files": list})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// fakeArchive serves item metadata by identifier. An identifier it does not
// know gets `{}` with a 200, which is what archive.org really does.
func fakeArchive(t *testing.T, items map[string]string) *playArchive {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/metadata/")
		body, ok := items[id]
		if !ok {
			body = "{}"
		}
		if body == "!503" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	p := newPlayArchive(srv.Client())
	p.metadataBase = srv.URL + "/metadata/"
	return p
}

func reasonCodes(a playAnswer) []string {
	out := make([]string, 0, len(a.Reasons))
	for _, r := range a.Reasons {
		out = append(out, r.Code)
	}
	return out
}

func hasReason(a playAnswer, code string) bool {
	for _, r := range a.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

// The positive case, shaped exactly like the real `pacman_nes_2`.
func TestANESItemResolvesToOurOwnPlayer(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"pacman_nes_2": itemJSON(t, map[string]any{
			"identifier": "pacman_nes_2", "title": "Pac-Man (NES)",
			"mediatype": "software", "emulator": "nes", "emulator_ext": "nes",
			"collection": []string{"miscconsoles", "consolelivingroom", "emulation"},
		}, []fakeFile{
			{"__ia_thumb.jpg", "18031"},
			{"pacman.nes", "24592"},
			{"pacman_nes_2_screenshot.gif", "309249"}, // larger than the ROM, and not it
			{"pacman_nes_2_meta.xml", "4680"},
		}),
	})

	got := p.Resolve(context.Background(), "pacman_nes_2")

	if !got.Playable || got.Route != routeEmulatorJS {
		t.Fatalf("route=%q playable=%v reasons=%v; want an emulatorjs route",
			got.Route, got.Playable, reasonCodes(got))
	}
	if got.Core != "nes" || got.CoreFile != "fceumm" {
		t.Errorf("core=%q coreFile=%q, want nes/fceumm", got.Core, got.CoreFile)
	}
	if got.ROM == nil {
		t.Fatal("no ROM offered on a playable answer")
	}
	if got.ROM.Name != "pacman.nes" {
		t.Errorf("picked %q; emulator_ext says the payload is the .nes, not the screenshot",
			got.ROM.Name)
	}
	if got.ROM.SizeBytes != 24592 {
		t.Errorf("size=%d, want 24592 (archive.org sends it as a string)", got.ROM.SizeBytes)
	}
	if !strings.HasPrefix(got.ROM.URL, "/bridge/iptv?u=") {
		t.Errorf("rom url is %q; archive.org sends no CORS header, so it must go via the relay",
			got.ROM.URL)
	}
	if !strings.Contains(got.ROM.Direct, "/download/pacman_nes_2/pacman.nes") {
		t.Errorf("direct url is %q", got.ROM.Direct)
	}
	if !got.Touch {
		t.Error("our player has a virtual gamepad; touch should be true")
	}
	if got.Embed == "" {
		t.Error("their player is still a valid fallback and should be offered")
	}
	if got.Domain != "game" || got.Type != "release" {
		t.Errorf("domain/type = %q/%q, want game/release from the frozen vocabulary",
			got.Domain, got.Type)
	}
	if len(got.Reasons) != 0 {
		t.Errorf("a clean emulatorjs answer carries no reasons, got %v", reasonCodes(got))
	}
}

// The negative that matters most in volume: 96% of Game Gear items, 89% of
// Master System, 88% of ColecoVision and 95% of PlayStation are stream-only.
// Every one of those is a Play button the shipped code offers and cannot honour.
func TestAStreamOnlyItemIsRoutedToTheArchivesOwnPlayer(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"davidrobinsonssupremecourtprototype": itemJSON(t, map[string]any{
			"identifier": "davidrobinsonssupremecourtprototype",
			"title":      "David Robinson's Supreme Court (prototype)",
			"mediatype":  "software", "emulator": "gamegear", "emulator_ext": "gg",
			"collection":             []string{"gamegear_library", "stream_only", "consolelivingroom"},
			"access-restricted-item": "true",
		}, []fakeFile{{"DavidRobinsonsSupremeCourtPrototype.gg", "262144"}}),
	})

	got := p.Resolve(context.Background(), "davidrobinsonssupremecourtprototype")

	// PLAY YES. This used to route to the Archive's own player, and that was
	// wrong: their player fetches the ROM into the browser to run it, which is
	// exactly what ours does. Refusing here honoured nothing -- it moved the
	// same act onto a page with no touch controls.
	if got.Route != routeEmulatorJS || !got.Playable {
		t.Fatalf("route=%q playable=%v; a stream-only item plays here", got.Route, got.Playable)
	}
	if !got.StreamOnly {
		t.Error("the Archive's marker was dropped; a client cannot honour what it is not told")
	}
	if got.ROM == nil || got.ROM.URL == "" {
		t.Fatal("nothing to play")
	}
	// DOWNLOAD NO. That is the line the marker actually draws, and it is drawn
	// by publishing no direct link -- there is nothing to save and nothing to
	// hand on. The relay URL is the play, fetched by the emulator.
	if got.ROM.Direct != "" {
		t.Errorf("direct=%q; a stream-only item must not be handed a download URL",
			got.ROM.Direct)
	}
	if !got.Touch {
		t.Error("our player has a virtual gamepad; touch should be true")
	}
}

// `access-restricted-item` alone is enough. On every item sampled it agrees
// with the collection, and taking either as sufficient means an item that ever
// carries only one is still honoured.
func TestAccessRestrictionAloneIsHonoured(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"restricted_only": itemJSON(t, map[string]any{
			"identifier": "restricted_only", "emulator": "nes", "emulator_ext": "nes",
			"collection":             "consolelivingroom", // bare string, not a list
			"access-restricted-item": "true",
		}, []fakeFile{{"game.nes", "40976"}}),
	})

	got := p.Resolve(context.Background(), "restricted_only")
	if !got.StreamOnly {
		t.Errorf("the marker was missed when it arrived on its own: reasons=%v",
			reasonCodes(got))
	}
	if got.ROM == nil || got.ROM.Direct != "" {
		t.Errorf("rom=%+v; play yes, download no", got.ROM)
	}
}

// THE BIOS GATE. gearcoleco's core info marks colecovision.rom as required
// (firmware0_opt = "false"). Without it EmulatorJS starts, draws its own error
// screen, and reports itself as started -- which looks exactly like success.
func TestColecoVisionIsRefusedForItsMissingBIOS(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"coleco_game": itemJSON(t, map[string]any{
			"identifier": "coleco_game", "title": "A ColecoVision game",
			"emulator": "coleco", "emulator_ext": "col",
			"collection": []string{"consolelivingroom"}, // downloadable, deliberately
		}, []fakeFile{{"game.col", "16384"}}),
	})

	got := p.Resolve(context.Background(), "coleco_game")

	if got.Route == routeEmulatorJS {
		t.Fatal("offered our own player for a ColecoVision game: it would boot, draw a " +
			"BIOS error and report success")
	}
	if !hasReason(got, reasonNeedsBIOS) {
		t.Errorf("reasons=%v, want needs_bios", reasonCodes(got))
	}
	if got.System != "ColecoVision" {
		t.Errorf("system=%q; the refusal must name the machine", got.System)
	}
	if got.ROM != nil {
		t.Error("a BIOS-blocked answer must not carry a ROM")
	}
}

// The Amiga is no longer gated on firmware AT ALL, and this test records why
// rather than being deleted.
//
// puae's core info does mark a Kickstart as required, and this endpoint did
// refuse every Amiga item on that basis. What changed is not the licence
// position but a fact about the core: libretro-uae compiles the AROS Kickstart
// replacement into itself -- `sources/src/aros.rom.c` -- and
// `puae_kickstart = "aros"` selects it, skipping the file check entirely
// ("No path validations for AROS", libretro-core.c). The Internet Archive's own
// Amiga player reaches the same conclusion by a different route: its
// `sae-a500.json` names `aros-amiga-m68k-rom.bin` as its BIOS.
//
// So an Amiga now plays with no file from anybody, and the answer says which
// firmware it is running on. See builtInFirmware in play_bios.go.
func TestTheAmigaRunsOnTheCoresOwnFreeKickstart(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"amiga_item": itemJSON(t, map[string]any{
			"identifier": "amiga_item", "emulator": "sae-a500", "emulator_ext": "adf",
			"collection": []string{"softwarelibrary_amiga"},
		}, []fakeFile{{"disk.adf", "901120"}}),
	})
	got := p.Resolve(context.Background(), "amiga_item")
	if got.Route != routeEmulatorJS {
		t.Fatalf("route=%q reasons=%v; AROS is built into puae", got.Route, reasonCodes(got))
	}
	if got.BiosNeeded == nil || got.BiosNeeded.Source != biosSourceBuiltIn {
		t.Fatalf("bios=%+v, want the built-in source named", got.BiosNeeded)
	}
	// The whole of the firmware is a core option, and the client cannot invent
	// it: without this the core falls back to looking for a Kickstart file that
	// is not there, and reports it by refusing to boot.
	if got.BiosNeeded.Options["puae_kickstart"] != "aros" {
		t.Errorf("options=%v, want puae_kickstart=aros", got.BiosNeeded.Options)
	}
	if got.BiosNeeded.URL != "" || got.BiosNeeded.File != "" {
		t.Errorf("a built-in replacement named a file: url=%q file=%q",
			got.BiosNeeded.URL, got.BiosNeeded.File)
	}
}

// MS-DOS is the single largest emulated category on archive.org -- 36,190 items
// -- and it cannot run here. EmulatorJS publishes dosbox_pure only as a threaded
// build; threads need SharedArrayBuffer, which needs a cross-origin-isolated
// page. Yarr.It sends Cross-Origin-Opener-Policy but no Embedder-Policy, so it
// is not isolated. Verified 2026-08-07 against both the live headers and the
// CDN, where `dosbox_pure-wasm.data` is a 404 and only the `-thread` build exists.
func TestMSDOSIsRefusedForCrossOriginIsolation(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"dos_game": itemJSON(t, map[string]any{
			"identifier": "dos_game", "title": "A DOS game",
			"emulator": "dosbox", "emulator_ext": "zip",
			"collection": []string{"softwarelibrary_msdos_games"},
		}, []fakeFile{{"game.zip", "1048576"}}),
	})

	got := p.Resolve(context.Background(), "dos_game")
	if got.Route != routeArchive {
		t.Fatalf("route=%q, want the archive's player", got.Route)
	}
	if !hasReason(got, reasonNeedsIsolation) {
		t.Errorf("reasons=%v, want needs_isolation", reasonCodes(got))
	}
	if !got.Playable {
		t.Error("their player runs DOS games perfectly well")
	}
}

// An arcade item's `emulator` is the MAME DRIVER name, not a machine. Passing
// an unrecognised id through as a platform is what once produced a Contra
// result whose platform read "Contra".
func TestAnArcadeDriverNameIsNotTreatedAsAMachine(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"arcade_contra": itemJSON(t, map[string]any{
			"identifier": "arcade_contra", "title": "Contra",
			"emulator": "contra", "emulator_ext": "zip",
			"collection": []string{"internetarcade"},
		}, []fakeFile{{"contra.zip", "262144"}}),
	})

	got := p.Resolve(context.Background(), "arcade_contra")
	if got.Route != routeArchive || !hasReason(got, reasonNoCore) {
		t.Fatalf("route=%q reasons=%v, want the archive route with no_core",
			got.Route, reasonCodes(got))
	}
	if got.System == "Contra" || got.Platform == "contra" {
		t.Error("the driver name leaked into the platform")
	}
	if !strings.Contains(got.Reasons[0].Detail, "contra") {
		t.Errorf("the refusal should name the emulator it did not recognise: %q",
			got.Reasons[0].Detail)
	}
}

// A machine EmulatorJS has no core for is refused BY NAME. ROM Hub's
// NO_EQUIVALENT records why each of these was not quietly refiled under a
// platform that does play, which is the tempting and wrong fix.
func TestAMachineWithNoCoreIsRefusedByName(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"intv_game": itemJSON(t, map[string]any{
			"identifier": "intv_game", "emulator": "intv2", "emulator_ext": "int",
			"collection": []string{"consolelivingroom"},
		}, []fakeFile{{"game.int", "8192"}}),
	})

	got := p.Resolve(context.Background(), "intv_game")
	if got.Route != routeArchive || !hasReason(got, reasonNoCore) {
		t.Fatalf("route=%q reasons=%v", got.Route, reasonCodes(got))
	}
	if got.System != "Intellivision" {
		t.Errorf("system=%q, want Intellivision", got.System)
	}
	if !strings.Contains(got.Reasons[0].Detail, "Intellivision") {
		t.Errorf("the refusal does not name the machine: %q", got.Reasons[0].Detail)
	}
}

// A disc image is past the point where relaying it is worth anything. The
// ceiling is enforced here so the button is never drawn, not in the browser
// where it is a message after the click.
func TestADiscImageIsTooLargeToRelay(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"big_genesis": itemJSON(t, map[string]any{
			"identifier": "big_genesis", "emulator": "genesis", "emulator_ext": "bin",
			"collection": []string{"consolelivingroom"},
		}, []fakeFile{{"huge.bin", "446954699"}}),
	})

	got := p.Resolve(context.Background(), "big_genesis")
	if got.Route != routeArchive || !hasReason(got, reasonTooLarge) {
		t.Fatalf("route=%q reasons=%v, want too_large", got.Route, reasonCodes(got))
	}
	if !strings.Contains(got.Reasons[0].Detail, "MiB") {
		t.Errorf("the size should be readable: %q", got.Reasons[0].Detail)
	}
	if got.ROM != nil {
		t.Error("an oversized answer must not hand over a URL to try anyway")
	}
}

// An item whose declared payload extension matches nothing. This is the case
// where guessing from the file list would produce a plausible wrong answer.
func TestAnItemWithNoPayloadFileIsRefused(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"empty_item": itemJSON(t, map[string]any{
			"identifier": "empty_item", "emulator": "nes", "emulator_ext": "nes",
			"collection": []string{"consolelivingroom"},
		}, []fakeFile{{"cover.jpg", "5614"}, {"readme.txt", "204"}}),
	})

	got := p.Resolve(context.Background(), "empty_item")
	if got.Route != routeArchive || !hasReason(got, reasonNoPayload) {
		t.Fatalf("route=%q reasons=%v, want no_payload", got.Route, reasonCodes(got))
	}
	if !strings.Contains(got.Reasons[0].Detail, "nes") {
		t.Errorf("the refusal should name the extension it looked for: %q", got.Reasons[0].Detail)
	}
}

// The only answer that is genuinely not playable at all. An item that declares
// no emulator is not an emulated item, and there is nothing anywhere that runs
// it -- so Route is none and Playable is false. This is the one case where a UI
// must draw no Play button of any kind.
func TestAnItemThatIsNotEmulatedIsNotPlayableAtAll(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"a_book": itemJSON(t, map[string]any{
			"identifier": "a_book", "title": "Some scanned book", "mediatype": "texts",
			"collection": []string{"americana"},
		}, []fakeFile{{"book.pdf", "9000000"}}),
	})

	got := p.Resolve(context.Background(), "a_book")
	if got.Playable || got.Route != routeNone {
		t.Fatalf("route=%q playable=%v, want none/false", got.Route, got.Playable)
	}
	if !hasReason(got, reasonNotEmulated) {
		t.Errorf("reasons=%v, want not_emulated", reasonCodes(got))
	}
	if got.Embed != "" {
		t.Error("no embed should be offered for an item with no emulator")
	}
}

// archive.org answers a 200 with `{}` for an identifier that does not exist.
func TestAMissingItemSaysSoRatherThanGuessing(t *testing.T) {
	p := fakeArchive(t, map[string]string{})
	got := p.Resolve(context.Background(), "no-such-item-anywhere")
	if got.Playable || !hasReason(got, reasonNotFound) {
		t.Errorf("playable=%v reasons=%v, want not_found", got.Playable, reasonCodes(got))
	}
}

// An Archive that will not answer is "unknown", never "unplayable". Reporting a
// transport failure as a property of the game is how a temporary outage becomes
// a permanent-looking absence.
func TestAnUnreachableArchiveIsUnknownNotUnplayable(t *testing.T) {
	p := fakeArchive(t, map[string]string{"flaky": "!503"})
	got := p.Resolve(context.Background(), "flaky")
	if !hasReason(got, reasonUpstream) {
		t.Errorf("reasons=%v, want upstream", reasonCodes(got))
	}
	if got.Route != routeNone || got.Playable {
		t.Errorf("route=%q playable=%v", got.Route, got.Playable)
	}
}

// Rate limiting and maintenance pages both arrive as 200 + HTML.
func TestAnHTMLErrorPageIsAnUpstreamProblem(t *testing.T) {
	p := fakeArchive(t, map[string]string{"ratelimited": "<html>Too many requests</html>"})
	got := p.Resolve(context.Background(), "ratelimited")
	if !hasReason(got, reasonUpstream) {
		t.Errorf("reasons=%v, want upstream", reasonCodes(got))
	}
}

// The invariant that makes the whole thing trustworthy: an emulatorjs route is
// never offered without both a real core and a real file. Checked across every
// case above at once, so a new branch cannot forget it.
func TestAnEmulatorJSRouteAlwaysCarriesACoreAndAROM(t *testing.T) {
	bodies := map[string]string{
		"good":    itemJSON(t, map[string]any{"identifier": "good", "emulator": "nes", "emulator_ext": "nes", "collection": []string{"x"}}, []fakeFile{{"g.nes", "40976"}}),
		"stream":  itemJSON(t, map[string]any{"identifier": "stream", "emulator": "nes", "emulator_ext": "nes", "collection": []string{"stream_only"}}, []fakeFile{{"g.nes", "40976"}}),
		"bios":    itemJSON(t, map[string]any{"identifier": "bios", "emulator": "coleco", "emulator_ext": "col", "collection": []string{"x"}}, []fakeFile{{"g.col", "16384"}}),
		"nocore":  itemJSON(t, map[string]any{"identifier": "nocore", "emulator": "vectrex", "emulator_ext": "vec", "collection": []string{"x"}}, []fakeFile{{"g.vec", "4096"}}),
		"toobig":  itemJSON(t, map[string]any{"identifier": "toobig", "emulator": "nes", "emulator_ext": "nes", "collection": []string{"x"}}, []fakeFile{{"g.nes", "999999999"}}),
		"nofile":  itemJSON(t, map[string]any{"identifier": "nofile", "emulator": "nes", "emulator_ext": "nes", "collection": []string{"x"}}, []fakeFile{{"a.jpg", "10"}}),
		"nothing": itemJSON(t, map[string]any{"identifier": "nothing", "collection": []string{"x"}}, []fakeFile{{"a.pdf", "10"}}),
	}
	p := fakeArchive(t, bodies)

	for id := range bodies {
		got := p.Resolve(context.Background(), id)
		switch got.Route {
		case routeEmulatorJS:
			if got.Core == "" || got.ROM == nil || got.ROM.URL == "" {
				t.Errorf("%s: emulatorjs route with core=%q rom=%v", id, got.Core, got.ROM)
			}
			if _, ok := emulatorJSSystems[got.Core]; !ok {
				t.Errorf("%s: core %q is not an EmulatorJS system", id, got.Core)
			}
		case routeArchive:
			if got.ROM != nil {
				t.Errorf("%s: archive route must not carry a ROM", id)
			}
			if len(got.Reasons) == 0 {
				t.Errorf("%s: a downgrade with no reason is exactly the silence this replaces", id)
			}
			if got.Embed == "" {
				t.Errorf("%s: archive route with nowhere to go", id)
			}
		case routeNone:
			if got.Playable {
				t.Errorf("%s: route none but playable", id)
			}
			if len(got.Reasons) == 0 {
				t.Errorf("%s: refused with no reason", id)
			}
		default:
			t.Errorf("%s: unknown route %q", id, got.Route)
		}
	}
}

// --- payload selection ------------------------------------------------------

// The largest file with the declared extension wins, and a missing size sorts
// below every real one -- so a metadata stub can never outrank the game.
func TestTheLargestMatchingFileWinsAndAMissingSizeLoses(t *testing.T) {
	meta := &playItemMetadata{}
	meta.Metadata.EmulatorExt = json.RawMessage(`"nes"`)
	meta.Files = []struct {
		Name   string `json:"name"`
		Format string `json:"format"`
		Size   string `json:"size"`
	}{
		{Name: "stub.nes"}, // no size at all
		{Name: "small.nes", Size: "16384"},
		{Name: "big.nes", Size: "262160"},
		{Name: "cover.jpg", Size: "9999999"}, // bigger, wrong extension
	}

	name, size, ok := meta.payload()
	if !ok || name != "big.nes" || size != 262160 {
		t.Fatalf("payload() = %q, %d, %v; want big.nes, 262160, true", name, size, ok)
	}
}

// `emulator_ext` is a string on 150 of 150 items sampled -- and a LIST on
// `f-zero-x-usa_202506`, which declares ["zip","z64"] and ships both files.
//
// This is the test that matters most for honesty, because getting it wrong is
// invisible: declared as a Go `string`, that one item fails to decode, and a
// decode failure is reported as "the Internet Archive did not answer". One
// unusual item would look like an outage.
func TestAListOfPayloadExtensionsIsNotAnOutage(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"f-zero-x-usa_202506": `{"metadata":{"identifier":"f-zero-x-usa_202506",` +
			`"title":"F-Zero X (USA)","emulator":"n64","emulator_ext":["zip","z64"],` +
			`"collection":["consolelivingroom"]},"files":[` +
			`{"name":"F-Zero X (USA).z64","size":"16777216"},` +
			`{"name":"F-Zero X (USA).zip","size":"12953815"}]}`,
	})

	got := p.Resolve(context.Background(), "f-zero-x-usa_202506")

	if hasReason(got, reasonUpstream) {
		t.Fatal("a list-shaped emulator_ext was reported as the Archive being down")
	}
	if got.Route != routeEmulatorJS {
		t.Fatalf("route=%q reasons=%v", got.Route, reasonCodes(got))
	}
	// The Archive declared zip first. Its ordering is the only preference
	// anybody has stated, so it is honoured rather than second-guessed.
	if got.ROM == nil || got.ROM.Name != "F-Zero X (USA).zip" {
		t.Errorf("picked %v; the Archive declared \"zip\" first", got.ROM)
	}
	if got.Core != "n64" {
		t.Errorf("core=%q, want n64", got.Core)
	}
}

// A declared extension that matches nothing falls through to the next one
// rather than giving up, which is what makes the list ordering safe.
func TestAnUnmatchedExtensionFallsThroughToTheNext(t *testing.T) {
	meta := &playItemMetadata{}
	meta.Metadata.EmulatorExt = json.RawMessage(`["chd","z64"]`)
	meta.Files = []struct {
		Name   string `json:"name"`
		Format string `json:"format"`
		Size   string `json:"size"`
	}{{Name: "game.z64", Size: "8388608"}}

	name, size, ok := meta.payload()
	if !ok || name != "game.z64" || size != 8388608 {
		t.Fatalf("payload() = %q, %d, %v", name, size, ok)
	}
}

// An item with no emulator_ext gives no payload at all rather than a guess.
// `.bin` alone is ColecoVision, Atari 2600, Mega Drive and PC Engine.
func TestNoDeclaredExtensionMeansNoGuess(t *testing.T) {
	meta := &playItemMetadata{}
	meta.Files = []struct {
		Name   string `json:"name"`
		Format string `json:"format"`
		Size   string `json:"size"`
	}{{Name: "game.bin", Size: "32768"}}

	if _, _, ok := meta.payload(); ok {
		t.Error("payload() guessed from the file list; .bin is four different machines")
	}
}

// --- routing ----------------------------------------------------------------

// A television is a different origin and must be able to ask.
func TestPlayRoutesAreReadableCrossOrigin(t *testing.T) {
	mux := http.NewServeMux()
	registerPlayRoutes(mux, ownerAuth())

	for _, path := range []string{"/api/play/systems", "/api/play/archive?id="} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Origin", "https://someone-else.example")
		mux.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s sent Allow-Origin %q; a TV is a different origin", path, got)
		}
	}
}

func TestPlayPreflightIsAnswered(t *testing.T) {
	mux := http.NewServeMux()
	registerPlayRoutes(mux, ownerAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/play/archive", nil)
	req.Header.Set("Origin", "https://someone-else.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight got %d, want 204", rec.Code)
	}
}

// A missing id is a 400 that says what was wanted, not an empty answer that
// looks like "this game cannot be played".
func TestAMissingIDIsARefusalNotAVerdict(t *testing.T) {
	mux := http.NewServeMux()
	registerPlayRoutes(mux, ownerAuth())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/play/archive", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

// The id may be given as a bare identifier or as any archive.org URL, because a
// card carries the URL and a client should not have to take it apart.
func TestTheIDAcceptsAnArchiveURL(t *testing.T) {
	for _, in := range []string{
		"pacman_nes_2",
		"https://archive.org/details/pacman_nes_2",
		"https://archive.org/details/pacman_nes_2#ejs",
		"ia:pacman_nes_2",
	} {
		if got := archiveIdentifier(in); got != "pacman_nes_2" {
			t.Errorf("archiveIdentifier(%q) = %q", in, got)
		}
	}
}

// The systems endpoint must publish a verdict for every row, and must never
// claim a core it would not use.
func TestTheSystemsEndpointAgreesWithTheResolver(t *testing.T) {
	mux := http.NewServeMux()
	registerPlayRoutes(mux, ownerAuth())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/play/systems", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}

	var body struct {
		Domain  string `json:"domain"`
		Systems []struct {
			Emulator   string `json:"emulator"`
			Core       string `json:"core"`
			Playable   bool   `json:"playable"`
			Reason     string `json:"reason"`
			Detail     string `json:"detail"`
			Unlockable string `json:"unlockable"`
			BIOS       *struct {
				System string   `json:"system"`
				Files  []string `json:"files"`
			} `json:"bios"`
		} `json:"systems"`
		MaxRelayBytes int64 `json:"maxRelayBytes"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Domain != "game" {
		t.Errorf("domain=%q", body.Domain)
	}
	if body.MaxRelayBytes != maxRelayROMBytes {
		t.Errorf("maxRelayBytes=%d", body.MaxRelayBytes)
	}
	if len(body.Systems) != len(archivePlaySystems) {
		t.Fatalf("published %d systems, table has %d", len(body.Systems), len(archivePlaySystems))
	}
	for _, s := range body.Systems {
		if s.Playable {
			if s.Core == "" {
				t.Errorf("%s: playable with no core", s.Emulator)
			}
			if s.Reason != "" {
				t.Errorf("%s: playable and refused at once", s.Emulator)
			}
			continue
		}
		if s.Reason == "" || s.Detail == "" {
			t.Errorf("%s: refused with nothing to show a person", s.Emulator)
		}
		// A refused row may name a core only when the refusal is one a visitor
		// can actually lift -- firmware they own, or a page that is isolated.
		// Anywhere else a core name is an advertisement for something this
		// endpoint will never choose, which is the original rule this test was
		// written to hold and is unchanged for every non-liftable refusal.
		if s.Unlockable == "" && s.Core != "" {
			t.Errorf("%s: refused for good and still advertising core %q", s.Emulator, s.Core)
		}
		if s.Unlockable == "bios" {
			if s.Core == "" {
				t.Errorf("%s: unlockable by BIOS but names no core to unlock", s.Emulator)
			}
			if s.BIOS == nil || len(s.BIOS.Files) == 0 {
				t.Errorf("%s: unlockable by BIOS without naming a single file", s.Emulator)
			} else if s.BIOS.System != s.Core {
				t.Errorf("%s: BIOS filed under %q but core is %q", s.Emulator, s.BIOS.System, s.Core)
			}
		}
	}
}

// --- against the real Internet Archive --------------------------------------

// Everything above runs against a fake. That is right for a suite -- it is fast,
// offline and deterministic -- and it is also exactly how a table can be
// internally consistent and still wrong about the world.
//
// This one runs against live archive.org and live EmulatorJS. It is skipped
// unless YARRIT_LIVE=1 so it never makes CI depend on somebody else's uptime,
// and it is kept in the suite rather than done by hand once, so the claim can be
// re-checked by anybody who doubts it:
//
//	YARRIT_LIVE=1 go test -run TestAgainstTheRealArchive -v ./...
//
// It asserts the two things a verdict is worth nothing without: that the ROM
// bytes are really fetchable, and that the core chosen is really published.
func TestAgainstTheRealArchive(t *testing.T) {
	if os.Getenv("YARRIT_LIVE") != "1" {
		t.Skip("set YARRIT_LIVE=1 to check the tables against the real archive.org")
	}
	p := newPlayArchive(nil)

	t.Run("a real NES game resolves and its bytes are there", func(t *testing.T) {
		got := p.Resolve(context.Background(), "pacman_nes_2")
		if got.Route != routeEmulatorJS {
			t.Fatalf("route=%q reasons=%v", got.Route, reasonCodes(got))
		}
		if got.Core != "nes" {
			t.Fatalf("core=%q, want nes", got.Core)
		}
		if got.ROM == nil {
			t.Fatal("no ROM")
		}

		// The bytes. A verdict that says a file is fetchable and is wrong is
		// the same dead button in a different costume.
		resp, err := http.Get(got.ROM.Direct)
		if err != nil {
			t.Fatalf("fetching %s: %v", got.ROM.Direct, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: HTTP %d", got.ROM.Direct, resp.StatusCode)
		}
		head := make([]byte, 16)
		if _, err := io.ReadFull(resp.Body, head); err != nil {
			t.Fatalf("reading %s: %v", got.ROM.Name, err)
		}
		// An iNES file begins "NES\x1a". This is the same evidence rom-core.js
		// treats as authoritative, and it is what proves the core matches the
		// file rather than merely matching the Archive's label for it.
		if string(head[:4]) != "NES\x1a" {
			t.Fatalf("%s does not start with the iNES magic: % x", got.ROM.Name, head[:4])
		}
		t.Logf("OK  %s -> core %q, %s, %d bytes, magic %q",
			got.ID, got.Core, got.ROM.Name, got.ROM.SizeBytes, head[:4])
	})

	// The trap this guards is that a core NAME being valid and the core being
	// PUBLISHED are different things -- and the failure is EmulatorJS's own
	// "check console" screen, long after the button was drawn.
	t.Run("every core we would ask for is actually published", func(t *testing.T) {
		seen := map[string]bool{}
		for _, plat := range archivePlaySystems {
			if plat.Core == "" || seen[plat.Core] {
				continue
			}
			if _, blocked := blockedSystems[plat.Core]; blocked {
				continue
			}
			seen[plat.Core] = true

			file := emulatorJSSystems[plat.Core]
			url := "https://cdn.emulatorjs.org/stable/data/cores/" + file + "-wasm.data"
			req, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Range", "bytes=0-1") // one byte is proof enough
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("core %s (%s): %v", plat.Core, file, err)
				continue
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
				t.Errorf("core %q -> %s: HTTP %d. EmulatorJS would show its own error screen",
					plat.Core, file, resp.StatusCode)
				continue
			}
			t.Logf("OK  core %-10s -> %s", plat.Core, file)
		}
	})

	// The negatives, against the real items they were derived from.
	t.Run("the refusals are refusals in the real world too", func(t *testing.T) {
		for _, tc := range []struct{ id, want, note string }{
			{"psx_kasparov", reasonNeedsBIOS, "PlayStation, 426 MiB .chd"},
		} {
			got := p.Resolve(context.Background(), tc.id)
			if !hasReason(got, tc.want) {
				t.Errorf("%s (%s): reasons=%v, want %s", tc.id, tc.note, reasonCodes(got), tc.want)
				continue
			}
			if got.ROM != nil {
				t.Errorf("%s: refused and still handed over a ROM URL", tc.id)
			}
			t.Logf("OK  %-38s %-14s route=%s", tc.id, tc.want, got.Route)
		}
	})
}

// --- readability ------------------------------------------------------------

func TestHumanBytesReadsLikeASentence(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{24592, "24.0 KiB"},
		{maxRelayROMBytes, "48.0 MiB"},
		{446954699, "426.2 MiB"}, // a real PlayStation .chd from psx_kasparov
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A filename with spaces and brackets must survive into a URL, and the slashes
// that separate directories inside an item must not be escaped.
func TestFilenamesSurviveIntoTheURL(t *testing.T) {
	got := archiveFilePath("roms/Mike Tyson's Punch-Out!! (USA) (Rev-A).nes")
	if strings.Count(got, "/") != 1 {
		t.Errorf("%q: the directory separator was escaped", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("%q: the space was not escaped", got)
	}
	if _, err := fmt.Sscanf(got, "roms/%s", new(string)); err != nil {
		t.Errorf("%q does not start with the directory", got)
	}
}

// The same multi-valued trap, in the parser that decides whether to draw a Play
// button -- which is worse, because the failure reads as "the Internet Archive
// did not answer" and the button does nothing.
//
// `PacMan1981Atari` is a real item on the live "Games you can play right now"
// shelf. It declares `"emulator": ["a2600","a2600"]`, its metadata endpoint
// answers 200, and its tile was the one dead button left on that row.
func TestAListValuedEmulatorStillProducesAVerdict(t *testing.T) {
	body := `{"metadata":{"identifier":"PacMan1981Atari","title":"Pac Man (1981) (Atari)",
	  "mediatype":"software","emulator":["a2600","a2600"],"emulator_ext":"bin",
	  "collection":["historicalsoftware","stream_only","emulation"]},
	  "files":[{"name":"pacman.bin","format":"Atari 2600 ROM","size":"4096"}]}`

	var meta playItemMetadata
	if err := json.Unmarshal([]byte(body), &meta); err != nil {
		t.Fatalf("a list-valued emulator failed the decode: %v", err)
	}
	if got := meta.Metadata.Emulator.String(); got != "a2600" {
		t.Fatalf("emulator = %q, want a2600", got)
	}
	if got := meta.Metadata.Title.String(); got != "Pac Man (1981) (Atari)" {
		t.Fatalf("title = %q", got)
	}
	if got := meta.Metadata.MediaType.String(); got != "software" {
		t.Fatalf("mediatype = %q", got)
	}
}
