package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Firmware from the Internet Archive's own player.
//
// The single property worth defending here is that a file is chosen by the hash
// THE CORE PUBLISHES and never by anything else. What comes back is a MAME
// romset whose members are named after the chips they were dumped from -- the
// ColecoVision BIOS is `313 10031-4005 73108a.u2` -- and that same zip carries a
// second file of exactly the same size. Name and size can both be right about
// the wrong file; a hash cannot.

// The real ColecoVision BIOS's MD5, as gearcoleco's own core info states it.
const colecoBIOSMD5 = "2c66f5911e5b42b8ebe113403548eee7"

// realColecoBIOS builds 8,192 bytes that hash to the value gearcoleco wants.
// It cannot -- so the tests below drive the hash the other way, asserting that
// whatever md5 is demanded is the one that gets picked. That is the property;
// the specific constant is checked against the live Archive separately.
func md5of(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func zipOf(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeEmularity serves the two things the Archive's player serves: a module
// config that names the firmware container, and the container.
func fakeEmularity(t *testing.T, configs map[string]string, files map[string][]byte) (*archiveFirmware, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		seen = append(seen, name)
		if body, ok := configs[name]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		if body, ok := files[name]; ok {
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	a := newArchiveFirmware(srv.Client())
	a.engineBase = srv.URL + "/"
	a.biosBase = srv.URL + "/"
	return a, &seen
}

// The Archive's own emulator ids for a core are derived, not written down --
// the same walk of archivePlaySystems that derives library slugs.
func TestTheArchiveModulesAreDerivedFromTheTableWeAlreadyHave(t *testing.T) {
	if got := strings.Join(biosArchiveModules("coleco"), ","); got != "coleco" {
		t.Errorf("coleco derives %q", got)
	}
	// All three Amiga spellings, because a machine can have several and the
	// Archive names its config after each.
	if got := strings.Join(biosArchiveModules("amiga"), ","); got != "sae-a1200,sae-a500,sae-a500p" {
		t.Errorf("amiga derives %q", got)
	}
	for _, core := range biosCores() {
		if len(biosArchiveModules(core)) == 0 {
			t.Errorf("core %q has no archive.org emulator id, so their player can "+
				"never be asked what firmware it uses", core)
		}
	}
}

// THE PROPERTY. Two members of the same size, one of them the real BIOS, and
// the name of the real one says nothing about what it is.
func TestTheFileIsChosenByTheHashTheCorePublishes(t *testing.T) {
	real := bytes.Repeat([]byte{0x31}, 8192)
	decoy := bytes.Repeat([]byte{0x99}, 8192)
	romset := zipOf(t, map[string][]byte{
		"313 10031-4005 73108a.u2": real,
		"colecoa.rom":              decoy,
	})

	body, sum, ok := pickHashedFirmware(romset, map[string]bool{md5of(real): true})
	if !ok {
		t.Fatal("the real BIOS was not found in the romset")
	}
	if !bytes.Equal(body, real) {
		t.Error("the wrong member was chosen")
	}
	if sum != md5of(real) {
		t.Errorf("hash reported as %q", sum)
	}

	// And a hash nothing matches yields nothing, rather than the closest thing.
	if _, _, ok := pickHashedFirmware(romset, map[string]bool{md5of([]byte("neither")): true}); ok {
		t.Error("a romset with no matching file produced one anyway")
	}
}

// A bare file, which is the other shape the Archive serves.
func TestABareFirmwareFileIsAcceptedToo(t *testing.T) {
	raw := bytes.Repeat([]byte{7}, 1024)
	body, _, ok := pickHashedFirmware(raw, map[string]bool{md5of(raw): true})
	if !ok || !bytes.Equal(body, raw) {
		t.Error("a bare firmware file was not accepted")
	}
}

// The whole chain: their config names the container, the container is fetched,
// the hash picks the file, and it is served under the name THE CORE opens --
// not the name the romset used, which is a chip.
func TestTheArchiveChainResolvesToTheNameTheCoreOpens(t *testing.T) {
	real := bytes.Repeat([]byte{0x31}, 8192)
	a, seen := fakeEmularity(t,
		map[string]string{"coleco.json": `{"name":"ColecoVision","bios_filenames":["coleco.zip"],"driver":"coleco"}`},
		map[string][]byte{"coleco.zip": zipOf(t, map[string][]byte{
			"313 10031-4005 73108a.u2": real,
			"colecoa.rom":              bytes.Repeat([]byte{0x99}, 8192),
		})},
	)
	// Drive the hash the core asks for, so the test does not depend on owning a
	// real BIOS while still exercising the real rule.
	need := biosRequirements["coleco"]
	restore := need.MD5
	need.MD5 = []string{md5of(real)}
	biosRequirements["coleco"] = need
	t.Cleanup(func() {
		need.MD5 = restore
		biosRequirements["coleco"] = need
	})

	fw, ok := a.Firmware(context.Background(), "coleco")
	if !ok {
		t.Fatalf("nothing resolved; requests were %v", *seen)
	}
	if fw.Name != "colecovision.rom" {
		t.Errorf("name=%q; EmulatorJS writes the firmware under the last path "+
			"segment of the URL and gearcoleco opens it by name", fw.Name)
	}
	if fw.Size != 8192 {
		t.Errorf("size=%d", fw.Size)
	}
	body, _, err := a.Open(context.Background(), "coleco")
	if err != nil || !bytes.Equal(body, real) {
		t.Errorf("Open returned %d bytes, err=%v", len(body), err)
	}
}

// A core whose firmware hash nobody publishes is NOT served from somebody
// else's archive. Without a hash there is no way to tell its firmware from
// anything else of the same size, and a wrong guess boots and reports success.
func TestACoreWithNoPublishedHashIsRefused(t *testing.T) {
	a, _ := fakeEmularity(t,
		map[string]string{"psx.json": `{"name":"Sony Playstation","bios_filenames":["psu.zip"],"driver":"psu"}`},
		map[string][]byte{"psu.zip": zipOf(t, map[string][]byte{
			"SCPH1001.BIN": bytes.Repeat([]byte{1}, 524288),
		})},
	)
	if len(biosRequirements["psx"].MD5) != 0 {
		t.Skip("psx now publishes a hash; this test needs a different core")
	}
	if _, ok := a.Firmware(context.Background(), "psx"); ok {
		t.Error("a file was picked out of a romset with no hash to check it against")
	}
}

// An Archive that will not answer costs the Archive's firmware and nothing
// else, and is not asked again on every verdict.
func TestAnUnreachableArchiveIsAskedOnceAndThenLeftAlone(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	a := newArchiveFirmware(srv.Client())
	a.engineBase, a.biosBase = srv.URL+"/", srv.URL+"/"

	for i := 0; i < 5; i++ {
		if _, ok := a.Firmware(context.Background(), "coleco"); ok {
			t.Fatal("an archive answering 503 produced firmware")
		}
	}
	if calls > 1 {
		t.Errorf("a dead archive was asked %d times", calls)
	}
}
