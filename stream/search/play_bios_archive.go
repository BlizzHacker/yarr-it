package main

// Firmware from the Internet Archive's own player.
//
// THE OBSERVATION THAT MAKES THIS LEGITIMATE: their Emularity player boots a
// ColecoVision game in a browser today, and it does not ask anybody for a file.
// So the firmware is on a public URL that they serve, at their expense, to
// anybody who opens an item page. We already fetch the ROM from exactly such a
// URL and play it. Fetching the firmware the same way is the same act.
//
// Nothing is hosted here. There is no file in this repository, no file on the
// server's disk, and no file in any build artefact. The bytes are fetched from
// the Archive when a real verdict needs them, held in memory, and relayed --
// exactly as the ROM is, and for the same two reasons: the browser may be on a
// network that cannot reach the source, and the relay is where the caching goes.
//
// ---------------------------------------------------------------------------
// HOW THE URL WAS FOUND, so nobody has to find it again
// ---------------------------------------------------------------------------
//
// It is not in the item. `https://archive.org/metadata/dk_coleco` lists the ROM
// and some screenshots and no firmware at all, and the obvious guesses --
// `/services/emularity/engine/coleco.zip`, an item called `mame-bios` -- are
// 404 and empty respectively.
//
// The answer is in the Emularity loader itself, which the embed page pulls from
// `https://emularity-config.ux-b.archive.org/loader.js`:
//
//	var get_bios_url = function (bios_filename) {
//	  return get_cors_url('emularity-bios', bios_filename);
//	};
//
// and `get_cors_url` resolves the three magic item names -- `emularity-engine`,
// `emularity-config`, `emularity-bios` -- to `//<name>.ux-b.archive.org/<path>`.
// So the firmware lives on a HOST OF ITS OWN, which is why every search under
// `/services/emularity/engine/` came up empty. Measured 2026-08-08:
//
//	GET https://emularity-engine.ux-b.archive.org/coleco.json
//	    200, {"bios_filenames":["coleco.zip"], "driver":"coleco", ...}
//	GET https://emularity-bios.ux-b.archive.org/coleco.zip
//	    200, 11,291 bytes, application/zip, Access-Control-Allow-Origin: *
//	    -> "313 10031-4005 73108a.u2"  8,192 bytes  md5 2c66f5911e5b42b8ebe113403548eee7
//
// ---------------------------------------------------------------------------
// WHY A HASH AND NOT A NAME
// ---------------------------------------------------------------------------
//
// What comes back is a MAME romset, and MAME names its ROMs after the chip they
// were dumped from: the ColecoVision BIOS inside that zip is called
// `313 10031-4005 73108a.u2`. Nothing about that name says "BIOS", and the same
// zip carries a second 8,192-byte file (`colecoa.rom`, a different revision).
// Choosing by name is impossible and choosing by size would pick either.
//
// So the choice is made by MD5, against the hash the CORE ITSELF publishes --
// gearcoleco's core info carries it in so many words. A hash cannot be right
// about the wrong file, and "the right size, the wrong contents" is the exact
// failure this whole area of the codebase keeps paying for: the emulator loads
// it, rejects it, and draws its own error screen at a healthy frame rate over a
// game that never booted, with nothing downstream able to tell.
//
// A core whose firmware hash we cannot state is therefore NOT served from here.
// That is not caution for its own sake; it is the difference between a game and
// a lie.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// The two hosts the Emularity loader resolves its magic item names to. Written
// out rather than derived because they ARE the finding -- see the header -- and
// a future reader needs to be able to check them.
const (
	archiveEngineHost = "https://emularity-engine.ux-b.archive.org/"
	archiveBIOSHost   = "https://emularity-bios.ux-b.archive.org/"
)

// How large a firmware container may be before it is refused. Larger than
// maxBIOSBytes because what arrives is a romset: the ColecoVision one is 11 KB
// and the PlayStation one is 2 MB, and the FILE pulled out of it is still held
// to maxBIOSBytes.
const maxArchiveBIOSBundleBytes = 16 << 20

// How long the Archive gets. Same reasoning as biosLibraryTimeout: this sits
// inside painting a button.
const archiveBIOSTimeout = 10 * time.Second

// archiveFirmware serves firmware from the Internet Archive's own player.
//
// It satisfies biosLibrary, which is what lets play_archive.go hold an ORDERED
// CHAIN of firmware sources rather than a special case per source: the
// precedence rule is then the order of a slice, in one place, instead of a
// branch that has to be got right again every time a source is added.
type archiveFirmware struct {
	client *http.Client
	// engineBase and biosBase are fields so a test can point them at a local
	// server. There is no other reason to change them.
	engineBase string
	biosBase   string

	mu    sync.Mutex
	index map[string]libraryFirmware
	bytes map[string][]byte
	tried map[string]time.Time
}

func newArchiveFirmware(client *http.Client) *archiveFirmware {
	if client == nil {
		client = &http.Client{Timeout: archiveBIOSTimeout}
	}
	return &archiveFirmware{
		client:     client,
		engineBase: archiveEngineHost,
		biosBase:   archiveBIOSHost,
		index:      map[string]libraryFirmware{},
		bytes:      map[string][]byte{},
		tried:      map[string]time.Time{},
	}
}

// biosArchiveModules is which of the Archive's own emulator ids belong to a
// core -- their `emulator` metadata field, which is also the name of their
// module config.
//
// Derived from archivePlaySystems exactly as biosPlatformSlugs derives library
// slugs, and for the same reason: a hand-written "coleco -> coleco" pair would
// be a second table, and the way it would fail is silent.
func biosArchiveModules(core string) []string {
	seen := map[string]bool{}
	for emulator, plat := range archivePlaySystems {
		if plat.Core == core {
			seen[emulator] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// archiveModuleConfig is the Archive's own description of one machine. Only the
// field that matters here is read; `bios_filenames` is authoritative and is why
// there is no per-machine firmware table in this file.
type archiveModuleConfig struct {
	Name          string   `json:"name"`
	Driver        string   `json:"driver"`
	BIOSFilenames []string `json:"bios_filenames"`
}

func (a *archiveFirmware) Firmware(ctx context.Context, core string) (libraryFirmware, bool) {
	if a == nil {
		return libraryFirmware{}, false
	}
	if fw, ok := a.Cached(core); ok {
		return fw, true
	}

	// One attempt per core per minute. The Archive being slow must cost the
	// Archive's firmware and never the verdict.
	a.mu.Lock()
	if last, ok := a.tried[core]; ok && time.Since(last) < biosIndexRetryAfter {
		a.mu.Unlock()
		return libraryFirmware{}, false
	}
	a.tried[core] = time.Now()
	a.mu.Unlock()

	fw, body, err := a.resolve(ctx, core)
	if err != nil {
		return libraryFirmware{}, false
	}
	a.mu.Lock()
	a.index[core] = fw
	a.bytes[core] = body
	a.mu.Unlock()
	return fw, true
}

func (a *archiveFirmware) Cached(core string) (libraryFirmware, bool) {
	if a == nil {
		return libraryFirmware{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	fw, ok := a.index[core]
	return fw, ok
}

func (a *archiveFirmware) Open(ctx context.Context, core string) ([]byte, libraryFirmware, error) {
	if a == nil {
		return nil, libraryFirmware{}, errors.New("no archive firmware source")
	}
	fw, ok := a.Firmware(ctx, core)
	if !ok {
		return nil, libraryFirmware{}, fmt.Errorf("the Internet Archive serves no firmware for %s", core)
	}
	a.mu.Lock()
	body := a.bytes[core]
	a.mu.Unlock()
	if len(body) == 0 {
		return nil, fw, errors.New("the firmware was indexed without its bytes")
	}
	return body, fw, nil
}

// resolve does the whole chain for one core: their module config, their
// firmware container, and the one file inside it whose hash the core publishes.
//
// It fetches the BYTES as part of resolving rather than lazily, because the
// hash check IS the resolution -- there is no way to say "the Archive has this
// machine's firmware" without having looked at the file and confirmed it is the
// file. An index that claimed otherwise would be a promise redeemed at boot.
func (a *archiveFirmware) resolve(ctx context.Context, core string) (libraryFirmware, []byte, error) {
	need, ok := biosRequirements[core]
	if !ok || len(need.Files) == 0 {
		return libraryFirmware{}, nil, fmt.Errorf("%s asks for no firmware", core)
	}
	if len(need.MD5) == 0 {
		// The deliberate refusal. Without a hash from the core there is no way
		// to tell its firmware from anything else of the same size in somebody
		// else's romset, and a wrong guess boots and reports success.
		return libraryFirmware{}, nil, fmt.Errorf(
			"%s publishes no firmware hash, so a file cannot be identified in an archive", core)
	}

	ctx, cancel := context.WithTimeout(ctx, archiveBIOSTimeout)
	defer cancel()

	wanted := map[string]bool{}
	for _, sum := range need.MD5 {
		wanted[strings.ToLower(strings.TrimSpace(sum))] = true
	}

	for _, module := range biosArchiveModules(core) {
		cfg, err := a.moduleConfig(ctx, module)
		if err != nil {
			continue
		}
		for _, name := range cfg.BIOSFilenames {
			if strings.TrimSpace(name) == "" {
				continue
			}
			raw, err := a.fetch(ctx, a.biosBase+name)
			if err != nil {
				continue
			}
			body, sum, found := pickHashedFirmware(raw, wanted)
			if !found {
				continue
			}
			return libraryFirmware{
				Core: core,
				// Served under the name the CORE looks for, not the name the
				// romset uses. `313 10031-4005 73108a.u2` is a chip, and
				// EmulatorJS writes the firmware into the emulator's filesystem
				// under the last path segment of the URL it fetched -- so the
				// URL has to end in the name gearcoleco actually opens.
				Name: need.Files[0],
				Size: int64(len(body)),
				// The MD5 stands in for the ETag. It is the Archive's file
				// identity as far as anything here is concerned, and it is what
				// was checked rather than something merely recorded.
				SHA1: sum,
			}, body, nil
		}
	}
	return libraryFirmware{}, nil, fmt.Errorf("no Internet Archive firmware matched %s's published hash", core)
}

// pickHashedFirmware finds the one file whose MD5 the core published.
//
// Handles both shapes the Archive serves: a bare file, and a romset zip whose
// members are named after the chips they were dumped from.
func pickHashedFirmware(raw []byte, wanted map[string]bool) ([]byte, string, bool) {
	if sum := md5hex(raw); wanted[sum] && len(raw) <= maxBIOSBytes {
		return raw, sum, true
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, "", false
	}
	// Sorted so the same archive always resolves to the same file, even in the
	// case that should be impossible -- two members with the wanted hash.
	members := make([]*zip.File, 0, len(zr.File))
	members = append(members, zr.File...)
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })

	for _, f := range members {
		if f.UncompressedSize64 == 0 || f.UncompressedSize64 > maxBIOSBytes {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxBIOSBytes+1))
		rc.Close()
		if err != nil || len(body) == 0 || int64(len(body)) > maxBIOSBytes {
			continue
		}
		if sum := md5hex(body); wanted[sum] {
			return body, sum, true
		}
	}
	return nil, "", false
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func (a *archiveFirmware) moduleConfig(ctx context.Context, module string) (archiveModuleConfig, error) {
	var cfg archiveModuleConfig
	body, err := a.fetch(ctx, a.engineBase+module+".json")
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// fetch performs one bounded, anonymous GET. Anonymous is the point: there is
// no credential anywhere in this file, because these URLs need none -- which is
// precisely what makes them ours to use.
func (a *archiveFirmware) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		drain(resp.Body)
		return nil, fmt.Errorf("archive firmware: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveBIOSBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxArchiveBIOSBundleBytes {
		return nil, errors.New("archive firmware: past the size ceiling")
	}
	return body, nil
}
