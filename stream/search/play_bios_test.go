package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Firmware from the household's own library.
//
// The tests here are of three kinds and the middle one is the reason the file
// exists at all:
//
//   - DERIVATION. There is no core -> platform table in play_bios.go, only a
//     walk of archivePlaySystems. If that walk ever stops covering a machine,
//     the failure is silent -- firmware looked for under a slug the library does
//     not use is firmware nobody finds -- so it is asserted rather than trusted.
//   - EQUIVALENCE. A file the visitor supplied and a file the library holds must
//     unblock a machine identically, and everything ELSE about the item must
//     still be judged. A library that made a 460 MB disc image playable would be
//     a worse lie than the refusal it replaced.
//   - DEGRADATION. With no library, an unreachable library, or a library that
//     holds nothing for this machine, the answer must be byte-for-byte the one
//     this endpoint gave before any of this existed.

// --- a library that is not a RomM -------------------------------------------

// fakeLibrary is a firmware source with no network. It counts the calls that
// would have been network calls, because one of the properties under test is
// that a particular endpoint makes none.
type fakeLibrary struct {
	held map[string]libraryFirmware
	body map[string][]byte
	fail bool

	mu      sync.Mutex
	lookups int // calls that are allowed to refresh, i.e. to touch the library
	opens   int
}

func (f *fakeLibrary) Firmware(_ context.Context, core string) (libraryFirmware, bool) {
	f.mu.Lock()
	f.lookups++
	f.mu.Unlock()
	if f.fail {
		return libraryFirmware{}, false
	}
	fw, ok := f.held[core]
	return fw, ok
}

func (f *fakeLibrary) Cached(core string) (libraryFirmware, bool) {
	if f.fail {
		return libraryFirmware{}, false
	}
	fw, ok := f.held[core]
	return fw, ok
}

func (f *fakeLibrary) Open(_ context.Context, core string) ([]byte, libraryFirmware, error) {
	f.mu.Lock()
	f.opens++
	f.mu.Unlock()
	fw, ok := f.held[core]
	if !ok || f.fail {
		return nil, libraryFirmware{}, fmt.Errorf("no firmware for %s", core)
	}
	return f.body[core], fw, nil
}

func (f *fakeLibrary) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups, f.opens
}

// colecoLibrary is Wade's RomM, as measured on 2026-08-08: one ColecoVision
// firmware file, called `coleco.rom`, 8192 bytes.
func colecoLibrary() *fakeLibrary {
	return &fakeLibrary{
		held: map[string]libraryFirmware{
			"coleco": {Core: "coleco", Name: "coleco.rom", ID: 635, Size: 8192,
				SHA1: "45bedc4cbdeac66c7df59e9e599195c778d86a92"},
		},
		body: map[string][]byte{"coleco": make([]byte, 8192)},
	}
}

// biosColecoItem is the shape of a real Console Living Room ColecoVision item.
func biosColecoItem(t *testing.T, id string, size string) string {
	t.Helper()
	return itemJSON(t, map[string]any{
		"identifier": id, "title": "Donkey Kong (ColecoVision)",
		"mediatype": "software", "emulator": "coleco", "emulator_ext": "col",
		"collection": []string{"consolelivingroom"},
	}, []fakeFile{{"dkong.col", size}})
}

// --- derivation --------------------------------------------------------------

// There is no table mapping a core to a library platform, only a walk of the
// one that already exists. If a machine ever falls out of that walk, firmware
// for it is looked for under a slug nothing uses and is never found -- and the
// symptom is not an error, it is the same "add your BIOS" screen as before.
func TestEveryFirmwareBlockedCoreDerivesALibraryPlatform(t *testing.T) {
	cores := biosCores()
	if len(cores) == 0 {
		t.Fatal("no core is blocked on firmware; the derivation is guarding nothing")
	}
	for _, core := range cores {
		slugs := biosPlatformSlugs(core)
		if len(slugs) == 0 {
			t.Errorf("core %q is blocked on firmware but no archivePlaySystems row "+
				"names a library platform for it, so its firmware can never be found", core)
		}
		for _, slug := range slugs {
			if !romHubPlayableSlugs[slug] {
				t.Errorf("core %q derives platform %q, which ROM Hub calls catalogue-only",
					core, slug)
			}
		}
	}
}

// The derivation must produce what a real library actually files these under.
// Measured against RomM on 2026-08-08: colecovision (id 72), psx (id 278) and
// two platforms sharing the slug `amiga` (ids 14 and 17).
func TestTheDerivedSlugsAreTheOnesALibraryUses(t *testing.T) {
	want := map[string][]string{
		"coleco": {"colecovision"},
		"psx":    {"psx"},
		"amiga":  {"amiga"},
	}
	for core, expect := range want {
		got := biosPlatformSlugs(core)
		if strings.Join(got, ",") != strings.Join(expect, ",") {
			t.Errorf("core %q derives %v, want %v", core, got, expect)
		}
	}
}

// biosCores is the intersection of "blocked for firmware" and "we can name the
// file". A core in only one of those is one we could never confirm we had.
func TestOnlyFirmwareBlockedCoresAreOffered(t *testing.T) {
	for _, core := range biosCores() {
		if blockedSystems[core].Reason != reasonNeedsBIOS {
			t.Errorf("%q is offered firmware but is not blocked for firmware", core)
		}
		need, ok := biosRequirements[core]
		if !ok || len(need.Files) == 0 {
			t.Errorf("%q is offered firmware without naming a single file", core)
		}
	}
	// And the reverse: a machine blocked for firmware that biosCores forgot
	// would be one nobody can ever unblock from a library.
	for core, b := range blockedSystems {
		if b.Reason != reasonNeedsBIOS {
			continue
		}
		found := false
		for _, c := range biosCores() {
			if c == core {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is blocked for firmware but biosCores() omits it", core)
		}
	}
}

// --- choosing the file -------------------------------------------------------

// THE NAME IS THE WHOLE THING. EmulatorJS writes the firmware into the emulator
// filesystem under the last path segment of the URL, and the core looks for it
// by name; a file under any other name is a core that reports no BIOS over a
// game that never booted. So the match is exact, in libretro's stated order, and
// nothing else is accepted.
func TestFirmwareIsChosenByNameAndNothingElse(t *testing.T) {
	held := []rommFirmwareRow{
		{ID: 1, FileName: "colecovision-bios-final.rom", Size: 8192}, // right size, wrong name
		{ID: 2, FileName: "coleco.rom", Size: 8192, SHA1: "aa"},      // the fallback name
		{ID: 3, FileName: "Donkey Kong.col", Size: 16384},            // a game
	}
	got, ok := pickFirmware("coleco", biosRequirements["coleco"].Files, held)
	if !ok {
		t.Fatal("gearcoleco falls back to coleco.rom; that file should have been chosen")
	}
	if got.Name != "coleco.rom" || got.ID != 2 {
		t.Errorf("chose %q (id %d); a name the core does not look for is a silent failure",
			got.Name, got.ID)
	}

	// Nothing at all rather than a plausible-looking guess.
	if _, ok := pickFirmware("coleco", biosRequirements["coleco"].Files, []rommFirmwareRow{
		{ID: 9, FileName: "bios.rom", Size: 8192},
	}); ok {
		t.Error("a file whose name no core asks for was offered anyway")
	}
}

// libretro's order is a preference, and it is the only preference anybody has
// stated. gearcoleco tries colecovision.rom first.
func TestTheFirstNameTheCoreAsksForWins(t *testing.T) {
	held := []rommFirmwareRow{
		{ID: 2, FileName: "coleco.rom", Size: 8192},
		{ID: 1, FileName: "colecovision.rom", Size: 8192},
	}
	got, _ := pickFirmware("coleco", biosRequirements["coleco"].Files, held)
	if got.Name != "colecovision.rom" {
		t.Errorf("chose %q; libretro names colecovision.rom first", got.Name)
	}
}

// A row the library has and the disk does not is a download that fails after
// the game has already started loading.
func TestFirmwareMissingFromDiskIsNotOffered(t *testing.T) {
	if _, ok := pickFirmware("coleco", biosRequirements["coleco"].Files, []rommFirmwareRow{
		{ID: 1, FileName: "coleco.rom", Size: 8192, Missing: true},
	}); ok {
		t.Error("firmware the library says is missing from disk was offered")
	}
}

// The ceiling matches web/src/bios.js so both sources of firmware refuse the
// same things -- and the thing being refused is somebody's disc image.
func TestFirmwareTooLargeToBeFirmwareIsNotOffered(t *testing.T) {
	for _, size := range []int64{0, -1, maxBIOSBytes + 1} {
		if _, ok := pickFirmware("psx", biosRequirements["psx"].Files, []rommFirmwareRow{
			{ID: 1, FileName: "scph5500.bin", Size: size},
		}); ok {
			t.Errorf("a %d-byte file was accepted as firmware", size)
		}
	}
}

// --- the verdict -------------------------------------------------------------

// THE CHANGE, stated as a test. Wade's RomM has held a ColecoVision BIOS since
// August; he was still being shown a screen asking him to go and find one.
func TestLibraryFirmwareUnblocksColecoVision(t *testing.T) {
	p := fakeArchive(t, map[string]string{"dkong_coleco": biosColecoItem(t, "dkong_coleco", "16384")})
	p.firmware = colecoLibrary()

	got := p.Resolve(context.Background(), "dkong_coleco")

	if got.Route != routeEmulatorJS || !got.Playable {
		t.Fatalf("route=%q playable=%v reasons=%v; the library holds this machine's firmware",
			got.Route, got.Playable, reasonCodes(got))
	}
	if got.Core != "coleco" || got.CoreFile != "gearcoleco" {
		t.Errorf("core=%q coreFile=%q, want coleco/gearcoleco", got.Core, got.CoreFile)
	}
	if got.BiosNeeded == nil {
		t.Fatal("the answer does not say which firmware it is running with")
	}
	if got.BiosNeeded.Source != biosSourceLibrary {
		t.Errorf("source=%q, want %q", got.BiosNeeded.Source, biosSourceLibrary)
	}
	if got.BiosNeeded.File != "coleco.rom" {
		t.Errorf("file=%q; the core looks for the file by name", got.BiosNeeded.File)
	}
	if got.BiosNeeded.URL != "/api/play/bios/library/coleco/coleco.rom" {
		t.Errorf("url=%q, want the relay path ending in the firmware's own name",
			got.BiosNeeded.URL)
	}
	if got.BiosNeeded.SizeBytes != 8192 {
		t.Errorf("sizeBytes=%d, want 8192", got.BiosNeeded.SizeBytes)
	}
	if len(got.Reasons) != 0 {
		t.Errorf("reasons=%v; nothing was given up", reasonCodes(got))
	}
}

// The URL the client is given must be OURS, relative, and free of anything that
// could be mistaken for a way in to the library. The credential never appears in
// an answer, and neither does the library's address.
func TestTheFirmwareURLNeverLeavesThisServer(t *testing.T) {
	p := fakeArchive(t, map[string]string{"dkong_coleco": biosColecoItem(t, "dkong_coleco", "16384")})
	p.firmware = colecoLibrary()

	got := p.Resolve(context.Background(), "dkong_coleco")
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	url := got.BiosNeeded.URL
	if !strings.HasPrefix(url, "/api/play/bios/") {
		t.Errorf("url=%q; firmware must be fetched from this server", url)
	}
	if strings.Contains(url, "://") || strings.Contains(url, "@") {
		t.Errorf("url=%q points somewhere else", url)
	}
	for _, forbidden := range []string{"Bearer", "access_token", "password", "token=", "/api/firmware/"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("the answer carries %q", forbidden)
		}
	}
}

// PRECEDENCE. A file somebody went and found for themselves wins, because they
// chose it -- and because this server must never learn anything about it beyond
// the name of the machine.
func TestAVisitorsOwnFirmwareBeatsTheLibrarys(t *testing.T) {
	p := fakeArchive(t, map[string]string{"dkong_coleco": biosColecoItem(t, "dkong_coleco", "16384")})
	lib := colecoLibrary()
	p.firmware = lib

	got := p.ResolveWith(context.Background(), "dkong_coleco",
		playOptions{BIOS: map[string]bool{"coleco": true}})

	if got.Route != routeEmulatorJS {
		t.Fatalf("route=%q; the visitor declared firmware", got.Route)
	}
	if got.BiosNeeded == nil || got.BiosNeeded.Source != biosSourceYours {
		t.Fatalf("source=%v, want %q", got.BiosNeeded, biosSourceYours)
	}
	if got.BiosNeeded.URL != "" || got.BiosNeeded.File != "" {
		t.Errorf("url=%q file=%q; their file is in their browser and this server "+
			"has no business naming it", got.BiosNeeded.URL, got.BiosNeeded.File)
	}
	if opens, _ := lib.counts(); opens == 0 {
		_ = opens // the lookup is short-circuited; nothing to assert but the absence below
	}
	if _, opens := lib.counts(); opens != 0 {
		t.Errorf("the library was read %d times for a visitor who supplied their own file", opens)
	}
}

// DEGRADATION, and it is the property that matters most: with no library this
// must be exactly what it was.
func TestWithNoLibraryTheAnswerIsUnchanged(t *testing.T) {
	items := map[string]string{"dkong_coleco": biosColecoItem(t, "dkong_coleco", "16384")}

	none := fakeArchive(t, items)
	empty := fakeArchive(t, items)
	empty.firmware = &fakeLibrary{held: map[string]libraryFirmware{}}
	broken := fakeArchive(t, items)
	broken.firmware = &fakeLibrary{fail: true}
	// The Internet Archive source is absent in all three: this test is about
	// what happens when the LIBRARY cannot answer, and leaving another source
	// live would answer for it and prove nothing.
	none.publicFirmware, empty.publicFirmware, broken.publicFirmware = nil, nil, nil

	for name, p := range map[string]*playArchive{
		"no library at all": none, "a library holding nothing": empty, "a library that is down": broken,
	} {
		got := p.Resolve(context.Background(), "dkong_coleco")
		if got.Route != routeArchive {
			t.Errorf("%s: route=%q, want the Internet Archive's own player", name, got.Route)
		}
		if !hasReason(got, reasonNeedsBIOS) {
			t.Errorf("%s: reasons=%v, want needs_bios", name, reasonCodes(got))
		}
		// The bring-your-own offer survives, unchanged, with none of the new
		// fields set -- so the JSON is what it was.
		if got.BiosNeeded == nil {
			t.Fatalf("%s: the offer to supply a file was dropped", name)
		}
		if got.BiosNeeded.Source != "" || got.BiosNeeded.URL != "" || got.BiosNeeded.File != "" {
			t.Errorf("%s: an invitation is describing firmware that is in use: %+v",
				name, *got.BiosNeeded)
		}
		body, err := json.Marshal(got.BiosNeeded)
		if err != nil {
			t.Fatal(err)
		}
		for _, added := range []string{"source", "url", "file", "sizeBytes"} {
			if strings.Contains(string(body), `"`+added+`"`) {
				t.Errorf("%s: the offer's JSON gained a %q field it did not have before: %s",
					name, added, body)
			}
		}
	}
}

// A library that holds firmware for ONE machine must not have that file
// credited to another. The Amiga still plays -- AROS is built into the core --
// but the answer must say it is running on the built-in replacement, not on a
// ColecoVision BIOS that has nothing to do with it.
func TestLibraryFirmwareIsNotCreditedToAnotherMachine(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"amiga_item": itemJSON(t, map[string]any{
			"identifier": "amiga_item", "emulator": "sae-a500", "emulator_ext": "adf",
			"collection": []string{"x"},
		}, []fakeFile{{"game.adf", "901120"}}),
	})
	p.firmware = colecoLibrary() // holds ColecoVision firmware and nothing else

	got := p.Resolve(context.Background(), "amiga_item")
	if got.BiosNeeded == nil || got.BiosNeeded.Source != biosSourceBuiltIn {
		t.Fatalf("bios=%+v; the library has no Kickstart, so the built-in AROS is "+
			"what is running", got.BiosNeeded)
	}
	if got.BiosNeeded.File == "coleco.rom" {
		t.Error("a ColecoVision BIOS was credited to an Amiga")
	}
}

// A machine with no firmware from anybody -- no file of theirs, no library, no
// Internet Archive source, and no replacement inside the core -- is refused
// exactly as it always was. This is the degradation floor, asserted on the one
// machine that has no free answer.
func TestAMachineWithNoFirmwareAnywhereIsStillRefused(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"dkong_coleco": biosColecoItem(t, "dkong_coleco", "16384"),
	})
	// Every source absent, which is what a household with no library looks like
	// when the Internet Archive cannot be reached either.
	p.firmware, p.publicFirmware = nil, nil

	got := p.Resolve(context.Background(), "dkong_coleco")
	if got.Route != routeArchive || !hasReason(got, reasonNeedsBIOS) {
		t.Fatalf("route=%q reasons=%v, want the archive route and needs_bios",
			got.Route, reasonCodes(got))
	}
	if got.BiosNeeded == nil || got.BiosNeeded.Source != "" {
		t.Errorf("bios=%+v; with nothing available the offer must be an invitation "+
			"and name no source", got.BiosNeeded)
	}
}

// EQUIVALENCE, in the direction that matters. Firmware answers exactly one
// question. Every other check still runs, and a PlayStation disc image is still
// hundreds of megabytes past what this relay carries.
func TestLibraryFirmwareDoesNotMakeADiscImagePlayable(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"psx_big": itemJSON(t, map[string]any{
			"identifier": "psx_big", "emulator": "psx", "emulator_ext": "chd",
			"collection": []string{"x"},
		}, []fakeFile{{"game.chd", "446676992"}}),
	})
	p.firmware = &fakeLibrary{
		held: map[string]libraryFirmware{
			"psx": {Core: "psx", Name: "scph5500.bin", ID: 6, Size: 524288},
		},
	}

	got := p.Resolve(context.Background(), "psx_big")
	if got.Route != routeArchive {
		t.Fatalf("route=%q; 426 MiB is past the relay ceiling whatever firmware exists",
			got.Route)
	}
	if !hasReason(got, reasonTooLarge) {
		t.Errorf("reasons=%v, want too_large -- the firmware question is answered, "+
			"so the next honest reason is the size", reasonCodes(got))
	}
	if got.ROM != nil {
		t.Error("an archive route must not carry a ROM")
	}
}

// A stream-only item plays here, with the library's firmware, and is not
// offered as a download. That is the whole of what the Archive's marker asks
// for: their own player fetches the ROM into the browser to run it, and so does
// ours -- the only thing they do not offer is the file itself.
func TestAStreamOnlyItemPlaysHereAndIsNotOfferedAsADownload(t *testing.T) {
	p := fakeArchive(t, map[string]string{
		"coleco_stream": itemJSON(t, map[string]any{
			"identifier": "coleco_stream", "emulator": "coleco", "emulator_ext": "col",
			"collection": []string{"stream_only"},
		}, []fakeFile{{"g.col", "16384"}}),
	})
	p.firmware = colecoLibrary()

	got := p.Resolve(context.Background(), "coleco_stream")
	if got.Route != routeEmulatorJS {
		t.Fatalf("route=%q reasons=%v; a stream-only item plays here",
			got.Route, reasonCodes(got))
	}
	if !got.StreamOnly {
		t.Error("the Archive's marker was dropped")
	}
	if got.ROM == nil || got.ROM.Direct != "" {
		t.Errorf("rom=%+v; play yes, download no", got.ROM)
	}
	if got.BiosNeeded == nil || got.BiosNeeded.Source != biosSourceLibrary {
		t.Errorf("bios=%+v, want the library's copy", got.BiosNeeded)
	}
}

// The library is asked ONLY about a machine that is blocked on firmware. Asking
// about an NES would be a credentialled call into the household's library for a
// question with a known answer.
func TestTheLibraryIsNotAskedAboutMachinesThatNeedNoFirmware(t *testing.T) {
	lib := colecoLibrary()
	p := fakeArchive(t, map[string]string{
		"nes_item": itemJSON(t, map[string]any{
			"identifier": "nes_item", "emulator": "nes", "emulator_ext": "nes",
			"collection": []string{"x"},
		}, []fakeFile{{"g.nes", "40976"}}),
		"mame_item": itemJSON(t, map[string]any{
			"identifier": "mame_item", "emulator": "mame", "emulator_ext": "zip",
			"collection": []string{"x"},
		}, []fakeFile{{"g.zip", "40976"}}),
		"dos_item": itemJSON(t, map[string]any{
			"identifier": "dos_item", "emulator": "dosbox", "emulator_ext": "zip",
			"collection": []string{"x"},
		}, []fakeFile{{"g.zip", "40976"}}),
	})
	p.firmware = lib

	for _, id := range []string{"nes_item", "mame_item", "dos_item"} {
		p.Resolve(context.Background(), id)
	}
	if lookups, _ := lib.counts(); lookups != 0 {
		t.Errorf("the library was asked %d times about machines that need no firmware", lookups)
	}
}

// --- serving the bytes -------------------------------------------------------

func biosServer(t *testing.T, lib biosLibrary) *httptest.Server {
	t.Helper()
	p := newPlayArchive(nil)
	p.firmware = lib
	// The library source is the owner's own RomM, so it is owner-only now and
	// the client below signs in. The anonymous case is asserted separately, in
	// owner_test.go and in TestTheFirmwareEndpointIsNotAProxyIntoTheLibrary
	// just below.
	c := ownerAuth()
	p.auth = c
	mux := http.NewServeMux()
	p.register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ownerJar(t, c, srv)
	return srv
}

func TestTheFirmwareEndpointServesTheFile(t *testing.T) {
	lib := colecoLibrary()
	srv := biosServer(t, lib)

	res, err := srv.Client().Get(srv.URL + "/api/play/bios/library/coleco/coleco.rom")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if len(body) != 8192 {
		t.Errorf("served %d bytes, want 8192", len(body))
	}
	// A WASM emulator fetches this itself; a text/plain answer is how a BIOS
	// arrives mangled. (RomM's own content endpoint answers text/plain, which
	// is exactly why this is not a pass-through of upstream headers.)
	if ct := res.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("content-type=%q", ct)
	}
	if tag := res.Header.Get("ETag"); !strings.Contains(tag, "45bedc4c") {
		t.Errorf("etag=%q; the library's own hash is what makes a replaced file "+
			"invalidate rather than being masked by a cache", tag)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("cache-control=%q; this is one household's file", cc)
	}
	if !strings.Contains(res.Header.Get("Content-Disposition"), "coleco.rom") {
		t.Errorf("content-disposition=%q", res.Header.Get("Content-Disposition"))
	}
}

// NOT A PROXY. The only file that can be asked for is the one the index already
// chose, for one of the three machines that are blocked on firmware. Everything
// else is a 404 rather than a search.
func TestTheFirmwareEndpointIsNotAWayIntoTheLibrary(t *testing.T) {
	srv := biosServer(t, colecoLibrary())

	for _, path := range []string{
		"/api/play/bios/library/coleco/colecovision.rom", // a real name, not the one held
		"/api/play/bios/library/coleco/scph5500.bin",     // another machine's firmware
		"/api/play/bios/library/nes/coleco.rom",          // a machine that needs none
		"/api/play/bios/library/psx/scph5500.bin",        // nothing held for it
		"/api/play/bios/library/amiga/kick34005.A500",
		"/api/play/bios/library/coleco/..%2f..%2fetc%2fpasswd",
		// A source nobody configured, and one that does not exist.
		"/api/play/bios/archive/coleco/colecovision.rom",
		"/api/play/bios/nowhere/coleco/coleco.rom",
	} {
		res, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status=%d, want 404", path, res.StatusCode)
		}
	}
}

// An anonymous request must never be able to make this server call into the
// household's library. The index is built at startup and by real verdicts; the
// bytes endpoint reads it and nothing more.
func TestTheFirmwareEndpointNeverRefreshesTheIndex(t *testing.T) {
	lib := colecoLibrary()
	srv := biosServer(t, lib)

	for _, path := range []string{
		"/api/play/bios/library/coleco/coleco.rom",
		"/api/play/bios/library/psx/scph5500.bin",
		"/api/play/bios/library/amiga/kick34005.A500",
	} {
		res, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if lookups, _ := lib.counts(); lookups != 0 {
		t.Errorf("an anonymous request caused %d lookups into the library", lookups)
	}
}

// The bytes are fetched once. A BIOS is a few kilobytes and never changes, so a
// second request must not be a second call into the library.
func TestTheFirmwareEndpointDoesNotRefetchWhatItHas(t *testing.T) {
	lib := colecoLibrary()
	srv := biosServer(t, lib)

	first, err := srv.Client().Get(srv.URL + "/api/play/bios/library/coleco/coleco.rom")
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	etag := first.Header.Get("ETag")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/play/bios/library/coleco/coleco.rom", nil)
	req.Header.Set("If-None-Match", etag)
	second, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusNotModified {
		t.Errorf("status=%d, want 304 for a browser that already has the file",
			second.StatusCode)
	}
	if _, opens := lib.counts(); opens != 1 {
		t.Errorf("the library was read %d times; a 304 must cost nothing", opens)
	}
}

// A library that will not serve its own file must not produce a 200 of nothing,
// which is the shape that boots the emulator into its own error screen.
func TestAFirmwareFetchThatFailsSaysSoWithoutQuotingTheLibrary(t *testing.T) {
	lib := &fakeLibrary{
		held: map[string]libraryFirmware{
			"coleco": {Core: "coleco", Name: "coleco.rom", ID: 635, Size: 8192},
		},
		// No body for it: Open fails.
	}
	srv := biosServer(t, lib)
	res, err := srv.Client().Get(srv.URL + "/api/play/bios/library/coleco/coleco.rom")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusOK {
		t.Fatalf("a failed fetch answered 200 with %d bytes", len(body))
	}
	for _, leak := range []string{"http://", "https://", "Bearer", "password"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the error carries %q: %s", leak, body)
		}
	}
}

// With no library configured at all, the endpoint exists and says nothing.
func TestTheFirmwareEndpointWithNoLibraryIsAFlat404(t *testing.T) {
	srv := biosServer(t, nil)
	res, err := srv.Client().Get(srv.URL + "/api/play/bios/library/coleco/coleco.rom")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", res.StatusCode)
	}
}

// --- reading a real RomM -----------------------------------------------------

// rommPlatformsFixture is the shape of RomM's own /api/platforms, trimmed from
// the live response on 2026-08-08. Three things in it are the point:
//
//   - firmware is EMBEDDED in the platform row, which is what makes the whole
//     index one request and means the slug and its files can never be read from
//     two different states of the library;
//   - TWO platforms share the slug "amiga" and differ only by fs_slug, so a
//     lookup that read one spelling would find half the Amiga firmware;
//   - the ColecoVision file is called `coleco.rom`, not `colecovision.rom`.
const rommPlatformsFixture = `[
 {"id":72,"slug":"colecovision","fs_slug":"colecovision","firmware":[
   {"id":635,"file_name":"coleco.rom","file_size_bytes":8192,
    "sha1_hash":"45bedc4cbdeac66c7df59e9e599195c778d86a92","missing_from_fs":false}]},
 {"id":278,"slug":"psx","fs_slug":"psx","firmware":[
   {"id":3,"file_name":"ps1_rom.bin","file_size_bytes":524288,"sha1_hash":"a","missing_from_fs":false},
   {"id":6,"file_name":"scph5500.bin","file_size_bytes":524288,"sha1_hash":"b","missing_from_fs":false},
   {"id":4,"file_name":"scph5501.bin","file_size_bytes":524288,"sha1_hash":"c","missing_from_fs":false}]},
 {"id":14,"slug":"amiga","fs_slug":"amiga","firmware":[
   {"id":207,"file_name":"Amiga Kickstart 1.3 (34.5).rom","file_size_bytes":262144,"sha1_hash":"d","missing_from_fs":false}]},
 {"id":17,"slug":"amiga","fs_slug":"amiga1200","firmware":[
   {"id":439,"file_name":"kick40068.A1200","file_size_bytes":524288,"sha1_hash":"e","missing_from_fs":false}]},
 {"id":100,"slug":"nes","fs_slug":"nes","firmware":[]}
]`

// fakeRomMFirmware speaks the two calls the firmware source makes, and asserts on the
// credential it is handed rather than on anything the test could fake.
func fakeRomMFirmware(t *testing.T, platforms string, files map[string][]byte) (*rommFirmware, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/token":
			_ = r.ParseForm()
			seen = append(seen, "scope="+r.Form.Get("scope"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires":900}`))
		case r.URL.Path == "/api/platforms":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			seen = append(seen, "platforms")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(platforms))
		case strings.HasPrefix(r.URL.Path, "/api/firmware/"):
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			seen = append(seen, "content "+r.URL.Path)
			body, ok := files[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// RomM really does answer text/plain here, which is why nothing
			// downstream trusts its content type.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	f := newRommFirmware(rommConfig{
		BaseURL: srv.URL, Username: "u", Password: "p", HTTPClient: srv.Client(),
	})
	return f, &seen
}

// The index is built from what a real RomM sends, including the two Amiga
// platforms that share a slug.
func TestTheIndexIsBuiltFromWhatARealRomMSends(t *testing.T) {
	f, seen := fakeRomMFirmware(t, rommPlatformsFixture, nil)

	for _, tc := range []struct {
		core, file string
		size       int64
	}{
		{"coleco", "coleco.rom", 8192},
		{"psx", "scph5500.bin", 524288},
		// The A1200 Kickstart is on the SECOND amiga platform, reachable only
		// because both `slug` and `fs_slug` are read.
		{"amiga", "kick40068.A1200", 524288},
	} {
		fw, ok := f.Firmware(context.Background(), tc.core)
		if !ok {
			t.Errorf("%s: nothing found", tc.core)
			continue
		}
		if fw.Name != tc.file || fw.Size != tc.size {
			t.Errorf("%s: got %q/%d, want %q/%d", tc.core, fw.Name, fw.Size, tc.file, tc.size)
		}
	}

	// One request built the whole index, and the token asked for the firmware
	// scope rather than the library's.
	platformCalls := 0
	for _, s := range *seen {
		if s == "platforms" {
			platformCalls++
		}
	}
	if platformCalls != 1 {
		t.Errorf("the index cost %d platform requests; it should cost one", platformCalls)
	}
	if !strings.Contains(strings.Join(*seen, " "), "firmware.read") {
		t.Errorf("the token was not asked for the firmware scope: %v", *seen)
	}
}

// The library adapter's own token must NOT quietly gain the firmware scope: an
// account without it would then fail to get a token at all and lose its games.
func TestTheLibraryProviderStillAsksForItsOwnScope(t *testing.T) {
	c := newRommClient(rommConfig{BaseURL: "http://example.invalid", Username: "u", Password: "p"})
	if c.scope != rommLibraryScope {
		t.Errorf("scope=%q, want %q", c.scope, rommLibraryScope)
	}
	if strings.Contains(c.scope, "firmware") {
		t.Error("the library adapter asks for a scope it does not need")
	}
}

// The bytes are fetched with the credential, once, and are handed on with our
// own content type rather than the library's.
func TestTheBytesAreFetchedWithTheCredentialAndCachedAfterwards(t *testing.T) {
	want := []byte(strings.Repeat("x", 8192))
	f, seen := fakeRomMFirmware(t, rommPlatformsFixture, map[string][]byte{
		"/api/firmware/635/content/coleco.rom": want,
	})

	for i := 0; i < 3; i++ {
		body, fw, err := f.Open(context.Background(), "coleco")
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if len(body) != len(want) || fw.Name != "coleco.rom" {
			t.Fatalf("attempt %d: %d bytes, name %q", i, len(body), fw.Name)
		}
	}
	contentCalls := 0
	for _, s := range *seen {
		if strings.HasPrefix(s, "content ") {
			contentCalls++
		}
	}
	if contentCalls != 1 {
		t.Errorf("the file was fetched %d times; a BIOS never changes", contentCalls)
	}
}

// A library that answers with something enormous must cost a refusal, not this
// process's memory.
func TestAnEnormousFirmwareFileIsRefusedRatherThanRead(t *testing.T) {
	// The index will not offer it (the size is declared), and even if it did the
	// read is bounded. Both halves are asserted, because either one alone is a
	// safeguard that can be removed by accident.
	if _, ok := pickFirmware("coleco", biosRequirements["coleco"].Files, []rommFirmwareRow{
		{ID: 1, FileName: "coleco.rom", Size: 4 << 20},
	}); ok {
		t.Error("a 4 MiB file was indexed as firmware")
	}

	f, _ := fakeRomMFirmware(t, `[{"id":72,"slug":"colecovision","fs_slug":"colecovision","firmware":[
		{"id":635,"file_name":"coleco.rom","file_size_bytes":8192,"sha1_hash":"z","missing_from_fs":false}]}]`,
		map[string][]byte{
			// Declared 8 KB, actually enormous. The declared size is the
			// library's claim; the read is what protects us.
			"/api/firmware/635/content/coleco.rom": make([]byte, maxBIOSBytes+4096),
		})
	if _, _, err := f.Open(context.Background(), "coleco"); err == nil {
		t.Error("a file far past the ceiling was read and served")
	}
}

// A library that is down must not be asked again on every single verdict, and
// must not take a previously-good index with it.
func TestAnIndexSurvivesTheLibraryGoingAway(t *testing.T) {
	var down bool
	var platformCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/token" {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires":900}`))
			return
		}
		if r.URL.Path == "/api/platforms" {
			platformCalls++
			if down {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(rommPlatformsFixture))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	f := newRommFirmware(rommConfig{
		BaseURL: srv.URL, Username: "u", Password: "p", HTTPClient: srv.Client(),
	})
	if _, ok := f.Firmware(context.Background(), "coleco"); !ok {
		t.Fatal("the index was not built while the library was up")
	}

	// Force the index stale, then take the library away.
	down = true
	f.mu.Lock()
	f.builtAt = f.builtAt.Add(-2 * biosIndexTTL)
	f.mu.Unlock()

	if _, ok := f.Firmware(context.Background(), "coleco"); !ok {
		t.Error("a blip took the whole index with it; a game that was playing " +
			"a second ago must not stop being playable")
	}
	before := platformCalls
	for i := 0; i < 5; i++ {
		f.Firmware(context.Background(), "coleco")
	}
	if platformCalls != before {
		t.Errorf("a library that is down was asked %d more times; the answer would "+
			"be the same and the wait would not", platformCalls-before)
	}
}
