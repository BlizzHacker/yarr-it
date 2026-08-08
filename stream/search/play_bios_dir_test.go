package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Firmware from a directory on this machine.
//
// The two properties worth defending are the two that fail SILENTLY -- a
// firmware tree that is not read looks exactly like not having one:
//
//   - the names are hostile. A real tree has `[BIOS] Atari 5200 (USA).a52` and
//     `[BIOS]_lynxboot].img` in it: brackets, spaces, parentheses, an unmatched
//     bracket, mixed case. A naive join or a case-sensitive compare finds
//     nothing and says nothing about it.
//   - no request may reach the filesystem. The index is built by reading a
//     directory this server chose; a request can only name what is in it.

func writeBIOS(t *testing.T, root, slug, name string, body []byte) string {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func md5str(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// withColecoHash points the ColecoVision requirement at a hash the test owns,
// so the rule is exercised without the test needing a real console BIOS.
func withColecoHash(t *testing.T, sum string) {
	t.Helper()
	need := biosRequirements["coleco"]
	restore := need.MD5
	need.MD5 = []string{sum}
	biosRequirements["coleco"] = need
	t.Cleanup(func() {
		need.MD5 = restore
		biosRequirements["coleco"] = need
	})
}

// The layout an operator already has: <root>/<platform>/<file>, which is also
// exactly how RomM reports its own firmware (bios/colecovision/coleco.rom).
func TestFirmwareIsFoundInTheTreeAnOperatorAlreadyHas(t *testing.T) {
	root := t.TempDir()
	body := []byte(strings.Repeat("c", 8192))
	writeBIOS(t, root, "colecovision", "coleco.rom", body)
	withColecoHash(t, md5str(body))

	d := newDirFirmware(root)
	fw, ok := d.Firmware(context.Background(), "coleco")
	if !ok {
		t.Fatal("the ColecoVision BIOS in the tree was not found")
	}
	if fw.Name != "coleco.rom" || fw.Size != 8192 {
		t.Errorf("got %q/%d", fw.Name, fw.Size)
	}
	got, _, err := d.Open(context.Background(), "coleco")
	if err != nil || string(got) != string(body) {
		t.Errorf("Open returned %d bytes, err=%v", len(got), err)
	}
}

// A tree written on one filesystem is read on another, and a case-sensitive
// compare finds nothing and says nothing about it.
func TestTheDiskMatchIsCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	body := []byte(strings.Repeat("c", 8192))
	writeBIOS(t, root, "colecovision", "COLECO.ROM", body)
	withColecoHash(t, md5str(body))

	fw, ok := newDirFirmware(root).Firmware(context.Background(), "coleco")
	if !ok {
		t.Fatal("COLECO.ROM was not matched against coleco.rom")
	}
	// Served under the name ON DISK, because that is the name that will be
	// fetched and the name EmulatorJS will write it under.
	if fw.Name != "COLECO.ROM" {
		t.Errorf("name=%q, want the name as it actually is on disk", fw.Name)
	}
}

// The hash is what makes an automatic choice safe. A file of the right name and
// the wrong contents is loaded by the core, rejected, and reported by drawing
// its own error screen over a game that never booted.
func TestAFileOfTheRightNameAndWrongContentsIsRefused(t *testing.T) {
	root := t.TempDir()
	writeBIOS(t, root, "colecovision", "coleco.rom", []byte(strings.Repeat("x", 8192)))
	withColecoHash(t, md5str([]byte("something else entirely")))

	if _, ok := newDirFirmware(root).Firmware(context.Background(), "coleco"); ok {
		t.Error("a file whose hash the core does not publish was offered anyway")
	}
}

// Nothing configured, nothing there, and a root that does not exist are all the
// same answer: no firmware, no error, no noise.
func TestAnAbsentTreeIsSimplyNoFirmware(t *testing.T) {
	for name, d := range map[string]*dirFirmware{
		"no directory configured":  newDirFirmwareFromEnv(),
		"a root that is not there": newDirFirmware(filepath.Join(t.TempDir(), "nope")),
		"an empty root":            newDirFirmware(t.TempDir()),
	} {
		if _, ok := d.Firmware(context.Background(), "coleco"); ok {
			t.Errorf("%s: produced firmware", name)
		}
		if _, _, err := d.Open(context.Background(), "coleco"); err == nil {
			t.Errorf("%s: Open succeeded", name)
		}
	}
}

// NO PATH FROM A REQUEST REACHES THE FILESYSTEM. The index holds one name per
// machine; a request that names anything else is a 404, whatever it names and
// however real the file behind it is.
func TestTheDiskEndpointCannotBeWalkedOutOf(t *testing.T) {
	root := t.TempDir()
	body := []byte(strings.Repeat("c", 8192))
	writeBIOS(t, root, "colecovision", "coleco.rom", body)
	// A file that exists, in the tree, that no core asks for. It must be
	// unreachable even though its path is perfectly valid.
	writeBIOS(t, root, "colecovision", "secrets.txt", []byte("not firmware"))
	withColecoHash(t, md5str(body))

	p := newPlayArchive(nil)
	dir := newDirFirmware(root)
	// Warmed the way a real verdict warms it. The bytes endpoint deliberately
	// never builds an index of its own -- an anonymous request must not be able
	// to make this server go and do work -- so a cold one there is a 404.
	dir.Firmware(context.Background(), "coleco")
	p.diskFirmware = dir
	// The disk source reads the operator's own firmware tree, so it is
	// owner-only; the traversal claims below are about what the OWNER cannot
	// reach, which is the stronger form of the test.
	c := ownerAuth()
	p.auth = c
	mux := http.NewServeMux()
	p.register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ownerJar(t, c, srv)

	res, err := srv.Client().Get(srv.URL + "/api/play/bios/disk/coleco/coleco.rom")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the indexed file answered %d", res.StatusCode)
	}

	for _, path := range []string{
		"/api/play/bios/disk/coleco/secrets.txt",
		"/api/play/bios/disk/coleco/..%2f..%2fetc%2fpasswd",
		"/api/play/bios/disk/coleco/%2e%2e%2fsecrets.txt",
		"/api/play/bios/disk/nes/coleco.rom",
		"/api/play/bios/disk/psx/scph5500.bin",
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

// PRECEDENCE. Their own disk beats their library server, which beats the
// Internet Archive's copy, which beats the replacement inside the core -- and a
// file they added by hand beats all four.
func TestTheirOwnDiskComesBeforeEveryOtherSource(t *testing.T) {
	root := t.TempDir()
	body := []byte(strings.Repeat("c", 8192))
	writeBIOS(t, root, "colecovision", "colecovision.rom", body)
	withColecoHash(t, md5str(body))

	p := fakeArchive(t, map[string]string{
		"dkong_coleco": biosColecoItem(t, "dkong_coleco", "16384"),
	})
	p.diskFirmware = newDirFirmware(root)
	p.firmware = colecoLibrary()
	p.publicFirmware = &fakeLibrary{held: map[string]libraryFirmware{
		"coleco": {Core: "coleco", Name: "colecovision.rom", Size: 8192},
	}}

	got := p.Resolve(context.Background(), "dkong_coleco")
	if got.BiosNeeded == nil || got.BiosNeeded.Source != biosSourceDisk {
		t.Fatalf("source=%+v, want %q", got.BiosNeeded, biosSourceDisk)
	}
	if got.BiosNeeded.URL != "/api/play/bios/disk/coleco/colecovision.rom" {
		t.Errorf("url=%q", got.BiosNeeded.URL)
	}

	// And their own hand-added file still beats it.
	own := p.ResolveWith(context.Background(), "dkong_coleco",
		playOptions{BIOS: map[string]bool{"coleco": true}})
	if own.BiosNeeded.Source != biosSourceYours {
		t.Errorf("source=%q, want %q", own.BiosNeeded.Source, biosSourceYours)
	}
}

// The order is a single ordered slice, and it is the whole rule. If it is ever
// reordered by accident this is what says so.
func TestThePrecedenceOrderIsTheOneStated(t *testing.T) {
	p := newPlayArchive(nil)
	p.diskFirmware, p.firmware, p.publicFirmware = &fakeLibrary{}, &fakeLibrary{}, &fakeLibrary{}
	var order []string
	for _, s := range p.firmwareSources() {
		order = append(order, s.name)
	}
	want := []string{biosSourceDisk, biosSourceLibrary, biosSourceArchive}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("precedence is %v, want %v", order, want)
	}
}
