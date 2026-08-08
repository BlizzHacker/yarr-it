package main

// Firmware from a directory on this machine.
//
// The shape an operator already has. Every retro stack -- RetroArch, Batocera,
// EmulationStation, RomM -- keeps firmware as `<root>/<platform>/<file>`, and an
// operator who has been collecting for years has that tree already. Wade's is
// `/mnt/usb1/bios`, 34 platform directories, and the ColecoVision BIOS that
// started all of this is one file inside it.
//
// ---------------------------------------------------------------------------
// WHO THIS ACTUALLY HELPS, SAID PLAINLY
// ---------------------------------------------------------------------------
//
// A SELF-HOSTER. The search service reads the tree directly, so their whole
// collection is live with one environment variable and no import step.
//
// NOT a visitor to the hosted site. yarrit.com runs on a VPS with no sight of
// anybody's USB disk, and there is no version of this file that changes that.
// The hosted instance is served by the sources that do not need local disk --
// the operator's RomM over the network, the Internet Archive's own URLs, and
// the replacements built into the cores. Saying otherwise would be inventing a
// capability, which is the one thing this endpoint exists not to do.
//
// ---------------------------------------------------------------------------
// THE FILENAMES ARE HOSTILE AND THAT IS THE INTERESTING PART
// ---------------------------------------------------------------------------
//
// A real firmware tree is not tidy. Measured on Wade's:
//
//	[BIOS]_lynxboot].img
//	[BIOS] Atari 5200 (USA).a52
//	[BIOS] Family Computer Disk System (Japan) (En).bin
//
// Square brackets, spaces, parentheses, an unmatched bracket, mixed case. Two
// consequences, and both of them fail SILENTLY if they are got wrong -- the
// firmware is simply never found, which looks exactly like not having any:
//
//   - matching is case-insensitive, because a tree written on one filesystem is
//     read on another;
//   - the name goes into a URL, so it is escaped there and un-escaped back, and
//     it is compared against the INDEX rather than being joined onto a path.
//
// That last point is also the security property. No path component in a request
// ever reaches the filesystem: the index is built by reading a directory this
// server chose, and a request can only name something already in it. There is
// no listing endpoint, and nothing outside the configured root is reachable
// even by a caller who guesses perfectly.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dirFirmware serves firmware from a local tree. It satisfies biosLibrary, so
// it takes its place in the same ordered chain as every other source.
type dirFirmware struct {
	root string

	mu      sync.Mutex
	index   map[string]dirEntry
	builtAt time.Time
	ok      bool
}

type dirEntry struct {
	fw   libraryFirmware
	path string
}

// newDirFirmwareFromEnv reads the tree an operator points us at. nil -- no
// directory configured, which is the case on the hosted instance -- is a
// first-class answer that costs nothing.
func newDirFirmwareFromEnv() *dirFirmware {
	root := strings.TrimSpace(os.Getenv("YARRIT_BIOS_DIR"))
	if root == "" {
		return nil
	}
	return newDirFirmware(root)
}

func newDirFirmware(root string) *dirFirmware {
	return &dirFirmware{root: root, index: map[string]dirEntry{}}
}

func (d *dirFirmware) Firmware(_ context.Context, core string) (libraryFirmware, bool) {
	if d == nil {
		return libraryFirmware{}, false
	}
	d.refresh()
	return d.Cached(core)
}

func (d *dirFirmware) Cached(core string) (libraryFirmware, bool) {
	if d == nil {
		return libraryFirmware{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.index[core]
	return e.fw, ok
}

func (d *dirFirmware) Open(_ context.Context, core string) ([]byte, libraryFirmware, error) {
	if d == nil {
		return nil, libraryFirmware{}, errors.New("no firmware directory configured")
	}
	d.refresh()
	d.mu.Lock()
	e, ok := d.index[core]
	d.mu.Unlock()
	if !ok {
		return nil, libraryFirmware{}, fmt.Errorf("no firmware on disk for %s", core)
	}
	f, err := os.Open(e.path)
	if err != nil {
		return nil, e.fw, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxBIOSBytes+1))
	if err != nil {
		return nil, e.fw, err
	}
	if len(body) == 0 || int64(len(body)) > maxBIOSBytes {
		return nil, e.fw, fmt.Errorf("%s is not a plausible firmware file", e.fw.Name)
	}
	return body, e.fw, nil
}

// refresh rebuilds the index from the tree, at most every biosIndexTTL.
//
// Cheap: it reads at most one directory per firmware-blocked machine, never
// walks the tree, and never opens a file it has not already decided it wants.
func (d *dirFirmware) refresh() {
	d.mu.Lock()
	if d.ok && time.Since(d.builtAt) < biosIndexTTL {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	index := map[string]dirEntry{}
	for _, core := range biosCores() {
		need := biosRequirements[core]
		if e, ok := d.findFor(core, need); ok {
			index[core] = e
		}
	}

	d.mu.Lock()
	d.index, d.builtAt, d.ok = index, time.Now(), true
	d.mu.Unlock()
}

// findFor looks in every platform directory this core could live under, for
// every name the core actually opens, in the order libretro states them.
func (d *dirFirmware) findFor(core string, need playBIOS) (dirEntry, bool) {
	// The platform directories are derived from archivePlaySystems, exactly as
	// the RomM slugs are -- and they are the same words, because RomM's own
	// firmware folders ARE this layout: RomM reports `coleco.rom` at
	// `bios/colecovision/coleco.rom`.
	for _, slug := range biosPlatformSlugs(core) {
		dir := filepath.Join(d.root, slug)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		// Names come off the filesystem, never off a request. A request can
		// only ever match what is already here.
		byLower := map[string]string{}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			byLower[strings.ToLower(e.Name())] = e.Name()
		}
		for _, want := range need.Files {
			actual, ok := byLower[strings.ToLower(want)]
			if !ok {
				continue
			}
			path := filepath.Join(dir, actual)
			info, err := os.Stat(path)
			if err != nil || info.Size() <= 0 || info.Size() > maxBIOSBytes {
				continue
			}
			sum, err := md5File(path)
			if err != nil {
				continue
			}
			// Where the core publishes what its firmware must be, a file that
			// is not it is not offered. A BIOS of the right name and the wrong
			// contents boots the emulator into its own error screen and reports
			// success, which is worse than having none.
			if len(need.MD5) > 0 && !biosHashMatches(need.MD5, sum) {
				continue
			}
			return dirEntry{
				fw: libraryFirmware{
					Core: core,
					// Served under the name on disk, which is by construction
					// one of the names the core opens: EmulatorJS writes the
					// firmware under the last path segment of the URL.
					Name: actual,
					Size: info.Size(),
					SHA1: sum,
				},
				path: path,
			}, true
		}
	}
	return dirEntry{}, false
}

func biosHashMatches(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

func md5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxBIOSBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
