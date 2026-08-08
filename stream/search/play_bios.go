package main

// Firmware the household already owns.
//
// THE PROBLEM THIS SOLVES, IN ONE SENTENCE: somebody whose own RomM has held a
// ColecoVision BIOS for years was still being shown "add your ColecoVision
// BIOS" and asked to go and find a file that was already on their own disk.
//
// play_archive.go refuses three machines -- ColecoVision, PlayStation, Amiga --
// because their firmware is not this project's to ship. That refusal is about
// DISTRIBUTION, not about capability, and it has always had a way out: the
// console's owner may supply the file. web/src/bios.js is one way (their
// browser, never uploaded). This file is the other, and it is the one that
// matters for an operator, because their library server is already the place
// firmware lives.
//
// ---------------------------------------------------------------------------
// WHY THIS DOES NOT BECOME A SECOND CORE TABLE
// ---------------------------------------------------------------------------
//
// It would be very easy to write "coleco -> colecovision, psx -> psx, amiga ->
// amiga" here and be wrong about it six months later. There is no such table.
// The chain is derived from the two that already exist:
//
//	blockedSystems    which EmulatorJS cores are refused, and for which reason
//	archivePlaySystems  core -> library platform slug (ROM Hub's vocabulary)
//	biosRequirements  core -> the file names libretro says the core looks for
//
// so `biosPlatformSlugs("coleco")` is computed by walking archivePlaySystems
// for rows whose Core is "coleco" and collecting their Platform. If a fourth
// machine is ever blocked for firmware, this file learns it the same day, and
// a test asserts the derivation covers every reasonNeedsBIOS core.
//
// ---------------------------------------------------------------------------
// WHY THE BYTES COME THROUGH US AND NOT STRAIGHT FROM RomM
// ---------------------------------------------------------------------------
//
// Two independent reasons, either of which alone would settle it:
//
//   - RomM's /api/firmware is 403 without a credential (measured), and that
//     credential is the operator's. It stays on this side of the wire. There is
//     no URL below that carries it and no answer that reveals it.
//   - The browser asking may be nowhere near the RomM. A phone on mobile data
//     pointed at a LAN address gets a connection timeout, which EmulatorJS
//     reports as "Network Error" -- indistinguishable from a broken feature.
//
// So the relay is not a convenience; it is the only shape that works.
//
// ---------------------------------------------------------------------------
// THE FILE NAME IS LOAD-BEARING, WHICH IS NOT OBVIOUS
// ---------------------------------------------------------------------------
//
// EmulatorJS writes whatever EJS_biosUrl points at into the emulator's virtual
// filesystem under the LAST PATH SEGMENT of that URL, with any query and
// fragment stripped (emulator.min.js, downloadGameFile). The core then looks
// for its firmware BY NAME in that directory: gearcoleco's libretro.cpp asks
// for "colecovision.rom" and falls back to "coleco.rom", and finds nothing if
// the file is called something else -- which is exactly the NO BIOS screen this
// work exists to remove, drawn at a healthy frame rate over a game that never
// booted.
//
// Hence: the URL below ends in the real firmware name, and the firmware chosen
// is one whose name the core actually asks for. Nothing is renamed and nothing
// is guessed. A file RomM holds under a name no core looks for is not offered,
// because offering it would produce precisely the silent failure above.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The ceiling on a firmware file, matching MAX_BIOS_BYTES in web/src/bios.js so
// the two sources of firmware refuse the same things. Every file any of these
// three cores asks for is between 1 KB and 512 KB; two megabytes is generous by
// a factor of four and still refuses the thing that is obviously not firmware.
const maxBIOSBytes = 2 << 20

// How long the library's firmware index is trusted before it is rebuilt.
// Firmware does not change -- a Kickstart ROM is a Kickstart ROM -- so this is
// not about staleness of content but about an operator ADDING a file and
// wanting it to work without a restart.
const biosIndexTTL = 15 * time.Minute

// How long to wait before asking again after the library refused or did not
// answer. Without this, every verdict for a ColecoVision item pays the full
// timeout while RomM is down, and a slow refusal is worse than a fast one: the
// answer is the same either way.
const biosIndexRetryAfter = 60 * time.Second

// How long the library gets to answer. Deliberately far shorter than
// playMetadataTimeout: this call sits inside painting a button, and a library
// that is thinking about it must cost the LIBRARY's firmware, never the verdict.
const biosLibraryTimeout = 6 * time.Second

// Where a machine's firmware came from. Three values, in precedence order, and
// the ordering is the whole of the rule in firmwareFor.
const (
	// biosSourceYours is a file this visitor added by hand, held in their own
	// browser. It never touches this server.
	biosSourceYours = "yours"
	// biosSourceDisk is a file in the operator's own firmware directory, read
	// straight off this machine. See play_bios_dir.go.
	biosSourceDisk = "disk"
	// biosSourceLibrary is a file the household's library server holds, relayed
	// through us.
	biosSourceLibrary = "library"
	// biosSourceArchive is the file the Internet Archive's OWN player fetches to
	// run the same machine, relayed through us. Nothing is hosted here; see
	// play_bios_archive.go.
	biosSourceArchive = "archive"
	// biosSourceBuiltIn is a free replacement the CORE ALREADY CONTAINS, turned
	// on with a core option. No file exists anywhere in this project for these:
	// see builtInFirmware.
	biosSourceBuiltIn = "builtin"
)

// --- firmware nobody has to supply, because the core already has it ----------

// builtInFirmware is the machines that need no firmware from anybody, because
// the emulator core EmulatorJS already serves carries a free replacement and
// only has to be told to use it.
//
// THIS PROJECT SHIPS NO FIRMWARE, AND THIS TABLE IS WHY IT DOES NOT HAVE TO.
// Every entry here is a CORE OPTION -- a few bytes of configuration -- and the
// replacement it switches on is already inside the WASM core that
// cdn.emulatorjs.org serves and that resolvers/game.js already loads. Nothing
// is vendored, nothing is re-hosted, and the licence question that made
// resolvers/game.js load EmulatorJS from its own CDN rather than bundling it is
// answered here the same way, for the same reason.
//
// WHAT WAS CONSIDERED AND REJECTED, because the absences matter more than the
// entries (all checked 2026-08-08):
//
//   - ColecoVision. There is no permissively-licensed replacement. The only
//     free one that exists is 8bitworkshop's `minbios.asm`, and it fails on two
//     independent counts: it is GPL-3.0 in an MIT repository -- the exact line
//     resolvers/game.js draws when it refuses to vendor EmulatorJS -- and its
//     own documentation calls it "minimal (yet incompatible)", written for a C
//     library that deliberately does not call BIOS routines, where commercial
//     ColecoVision cartridges call them constantly. Shipping it would produce a
//     machine that boots and then misbehaves, which is worse than the honest
//     refusal it would replace. ColecoVision therefore stays on the library and
//     bring-your-own path.
//   - MSX / C-BIOS. Raised as a candidate and it does not apply: EmulatorJS's
//     own getCores() has no `msx` system at all (read from emulator.min.js,
//     2026-08-08), so there is no MSX core here for C-BIOS to serve. The
//     machine is not refused for firmware; it is not offered.
//   - The EmulatorJS CDN itself. Probed: it publishes cores and no firmware
//     (`/stable/data/bios/` is a 404), so there was nothing of theirs to reuse.
var builtInFirmware = map[string]builtInBIOS{
	"amiga": {
		Label: "AROS Kickstart replacement",
		// libretro-uae compiles the AROS ROM into the core itself --
		// `sources/src/aros.rom.c`, a 3.4 MB gzipped byte array -- and
		// `puae_kickstart = "aros"` selects it. libretro-core.c is explicit that
		// this path skips the file check entirely: "No path validations for
		// AROS". So an Amiga boots with no Kickstart on disk anywhere.
		Options: map[string]string{"puae_kickstart": "aros"},
		Licence: "AROS Public License",
		Origin:  "the AROS project, compiled into libretro-uae as sources/src/aros.rom.c",
		Detail: "The Amiga runs on the AROS Kickstart replacement that is built " +
			"into the emulator core — open source, and nothing to supply. It is " +
			"not a real Kickstart, so software that depends on Commodore's own " +
			"ROM may behave differently; a real Kickstart in your library is " +
			"used in preference wherever there is one.",
	},
	"psx": {
		Label: "pcsx_rearmed's built-in BIOS emulation",
		// pcsx_rearmed's `pcsx_rearmed_bios` option: "'Auto' will attempt to
		// load a real bios file from the frontend 'system' directory, falling
		// back to high level emulation if unavailable." Set explicitly rather
		// than relying on the default, so what is running is what was chosen.
		Options: map[string]string{"pcsx_rearmed_bios": "HLE"},
		Licence: "part of pcsx_rearmed itself (GPL-2.0), never distributed by us",
		Origin:  "high-level BIOS emulation inside the pcsx_rearmed core",
		Detail: "PlayStation runs on pcsx_rearmed's own high-level BIOS " +
			"emulation, which needs no firmware at all. It is less compatible " +
			"than a real console BIOS, so one from your library is used in " +
			"preference wherever there is one.",
	},
}

// builtInBIOS is a free replacement that lives inside the core.
type builtInBIOS struct {
	Label string
	// Options are the EmulatorJS core options that switch the replacement on.
	// They are the entire payload: there is no file.
	Options map[string]string
	// Licence and Origin are recorded because "we may ship this" is a claim
	// that has to be re-checkable by somebody who was not here. Neither is sent
	// to a client; both exist so the next person can audit the decision.
	Licence string
	Origin  string
	Detail  string
}

// --- what the library holds --------------------------------------------------

// libraryFirmware is one firmware file, already matched to the core that wants
// it. It exists only for files a core actually asks for by name; see the header.
type libraryFirmware struct {
	// Core is the EmulatorJS system name this unblocks.
	Core string
	// Name is the file name, and is BOTH what the library calls it and what the
	// core looks for -- they are the same string by construction, because a file
	// whose name no core asks for is never chosen.
	Name string
	// ID is the library's own id for the file, which is how the bytes are asked
	// for. Not published to a client: it says nothing a client can use and
	// naming it invites somebody to try their own numbers.
	ID   int
	Size int64
	// SHA1 is the library's own hash. Used as the ETag, so a browser that
	// already has the file does not fetch it twice, and so a REPLACED file
	// invalidates rather than being served from a stale cache.
	SHA1 string
}

// biosLibrary is the one thing play_archive.go asks of a firmware source.
//
// An interface rather than the concrete RomM client because the decision --
// which machines are unblocked, what the client is told, what is refused -- is
// the part worth testing, and none of it needs a RomM.
type biosLibrary interface {
	// Firmware reports what the library holds for one core, refreshing its index
	// if it is stale. Only ever called for a core that is blocked ON FIRMWARE and
	// only while answering a real verdict.
	Firmware(ctx context.Context, core string) (libraryFirmware, bool)
	// Cached answers the same question from the index ALONE, without going near
	// the network. It is what the bytes endpoint uses, so that an anonymous
	// request can never cause a credentialled call into the household's library.
	Cached(core string) (libraryFirmware, bool)
	// Open returns the bytes of the file Firmware named.
	Open(ctx context.Context, core string) ([]byte, libraryFirmware, error)
}

// --- core -> library platform, derived rather than written -------------------

// biosPlatformSlugs is which library platforms could hold firmware for a core.
//
// Derived from archivePlaySystems, which already maps every archive.org
// emulator id to both an EmulatorJS core and a library platform slug. Writing
// the three pairs out by hand would be a second table that could disagree with
// the first, and the failure it would cause is the silent one: firmware looked
// for under a slug the library does not use is firmware nobody ever finds.
//
// Sorted so the lookup order is stable and a test can assert on it.
func biosPlatformSlugs(core string) []string {
	seen := map[string]bool{}
	for _, plat := range archivePlaySystems {
		if plat.Core == core && plat.Platform != "" {
			seen[plat.Platform] = true
		}
	}
	out := make([]string, 0, len(seen))
	for slug := range seen {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

// biosCores is every core a firmware file could unblock: the ones blockedSystems
// refuses for reasonNeedsBIOS and that biosRequirements can name a file for.
// Both halves are required -- a core blocked for firmware we cannot name a file
// for is one we could never confirm we had.
func biosCores() []string {
	out := make([]string, 0, len(blockedSystems))
	for core, b := range blockedSystems {
		if b.Reason != reasonNeedsBIOS {
			continue
		}
		if _, ok := biosRequirements[core]; !ok {
			continue
		}
		out = append(out, core)
	}
	sort.Strings(out)
	return out
}

// --- the RomM firmware source ------------------------------------------------

// RomM's firmware endpoints, confirmed against its own openapi.json:
//
//	GET /api/firmware?platform_id=N        the files for one platform
//	GET /api/firmware/{id}/content/{name}  the bytes
//
// /api/platforms is used instead of the first, because RomM embeds each
// platform's firmware in its platform row -- so one request answers both "which
// platform id is this slug" and "what firmware does it hold", and there is no
// window in which the two could be read from different states of the library.
type rommFirmware struct {
	c *rommClient

	mu    sync.Mutex
	index map[string]libraryFirmware
	// builtAt is when the index was last built SUCCESSFULLY; triedAt is when it
	// was last attempted at all. Two fields because a failure and a success have
	// different waits, and collapsing them makes an outage retry every request.
	builtAt time.Time
	triedAt time.Time
	ok      bool

	bytesMu sync.Mutex
	// bytes is keyed by the library's file id, so replacing a file in the
	// library replaces what is served rather than being masked by a cache. At
	// most three entries of at most maxBIOSBytes each.
	bytes map[int][]byte
}

// newRommFirmwareFromEnv builds the firmware source from the same environment
// the RomM library provider reads, so an operator who has already configured
// RomM has already configured this. nil -- meaning "no library" -- is a
// first-class answer and is what every path below degrades to.
func newRommFirmwareFromEnv() *rommFirmware {
	base := strings.TrimSpace(os.Getenv("ROMM_URL"))
	user := firstNonEmptyGame(os.Getenv("ROMM_USERNAME"), os.Getenv("ROMM_USER"))
	pass := firstNonEmptyGame(os.Getenv("ROMM_PASSWORD"), os.Getenv("ROMM_PASS"))
	if base == "" || user == "" || pass == "" {
		return nil
	}
	return newRommFirmware(rommConfig{
		BaseURL: base, Username: user, Password: pass,
		HTTPClient: &http.Client{Timeout: biosLibraryTimeout},
	})
}

func newRommFirmware(cfg rommConfig) *rommFirmware {
	c := newRommClient(cfg)
	// Its OWN token, with its own scope, deliberately not shared with the
	// library provider's client. RomM refuses a token request for a scope the
	// account does not hold, so asking for firmware.read on the shared client
	// would mean an account without it loses its game library too -- a much
	// bigger failure than losing this.
	c.scope = "firmware.read platforms.read"
	return &rommFirmware{c: c, bytes: map[int][]byte{}}
}

// Warm builds the index once, off the request path.
//
// Not required -- every call below refreshes on demand -- but the first person
// to open a ColecoVision item should not be the one who pays for discovering
// the library. Failure is silent on purpose: a library that is not up yet at
// boot is not an error, it is a library that will be asked again.
func (f *rommFirmware) Warm(ctx context.Context) {
	if f == nil {
		return
	}
	f.refresh(ctx)
}

// Firmware answers from the index, refreshing it when stale.
func (f *rommFirmware) Firmware(ctx context.Context, core string) (libraryFirmware, bool) {
	if f == nil {
		return libraryFirmware{}, false
	}
	f.refresh(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()
	fw, ok := f.index[core]
	return fw, ok
}

// Cached answers what the index already holds WITHOUT going near the network.
//
// This is what any endpoint that is not resolving a real item must use. The
// difference matters: a firmware lookup is a credentialled call into the
// household's library, and an anonymous caller must never be able to cause one
// by asking a question.
func (f *rommFirmware) Cached(core string) (libraryFirmware, bool) {
	if f == nil {
		return libraryFirmware{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fw, ok := f.index[core]
	return fw, ok
}

// refresh rebuilds the index if it is stale, and at most one caller at a time.
func (f *rommFirmware) refresh(ctx context.Context) {
	f.mu.Lock()
	now := time.Now()
	fresh := f.ok && now.Sub(f.builtAt) < biosIndexTTL
	// The backoff is keyed on the last ATTEMPT, not on the last failure, and it
	// applies whether or not there is a usable index behind it. Keying it on
	// failure alone reads correctly and is wrong in the case that actually
	// happens: an index that has gone stale while the library is down is not
	// "fresh" and not "never built", so every single verdict would pay the full
	// timeout to be told the same thing.
	recentlyTried := !f.triedAt.IsZero() && now.Sub(f.triedAt) < biosIndexRetryAfter
	if fresh || recentlyTried {
		f.mu.Unlock()
		return
	}
	f.triedAt = now
	f.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, biosLibraryTimeout)
	defer cancel()

	index, err := f.build(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		// The PREVIOUS index is kept. A library that has gone away has not taken
		// its firmware with it, and dropping the index would turn a blip into a
		// game that stopped working mid-session.
		f.ok = f.index != nil
		return
	}
	f.index, f.builtAt, f.ok = index, time.Now(), true
}

// rommPlatformRow is the narrow view of RomM's platform. It embeds the
// platform's firmware, which is what makes one request enough.
type rommPlatformRow struct {
	ID       int               `json:"id"`
	Slug     string            `json:"slug"`
	FsSlug   string            `json:"fs_slug"`
	Firmware []rommFirmwareRow `json:"firmware"`
}

type rommFirmwareRow struct {
	ID       int    `json:"id"`
	FileName string `json:"file_name"`
	Size     int64  `json:"file_size_bytes"`
	SHA1     string `json:"sha1_hash"`
	Missing  bool   `json:"missing_from_fs"`
}

// build asks the library once and matches what it holds against what each core
// asks for.
func (f *rommFirmware) build(ctx context.Context) (map[string]libraryFirmware, error) {
	var platforms []rommPlatformRow
	if err := f.c.getAuthed(ctx, "/api/platforms", nil, &platforms); err != nil {
		return nil, err
	}

	// slug -> every file the library files under it. Both spellings are read:
	// RomM's `slug` is the catalogue name and `fs_slug` is the folder, and they
	// differ for exactly the case that matters here -- two Amiga platforms share
	// the slug "amiga" and are distinguished only by their folders.
	held := map[string][]rommFirmwareRow{}
	for _, p := range platforms {
		for _, slug := range []string{p.Slug, p.FsSlug} {
			slug = strings.ToLower(strings.TrimSpace(slug))
			if slug == "" {
				continue
			}
			held[slug] = append(held[slug], p.Firmware...)
		}
	}

	index := map[string]libraryFirmware{}
	for _, core := range biosCores() {
		need := biosRequirements[core]
		var candidates []rommFirmwareRow
		for _, slug := range biosPlatformSlugs(core) {
			candidates = append(candidates, held[slug]...)
		}
		if fw, ok := pickFirmware(core, need.Files, candidates); ok {
			index[core] = fw
		}
	}
	return index, nil
}

// pickFirmware chooses the file, and refuses to choose one it cannot justify.
//
// The rule is a name match, in the order libretro lists the names, and nothing
// else. Not "the only file in the folder", not "the one that is the right
// size": a file of the right SIZE and the wrong contents is accepted by us and
// rejected by the core, which draws its own error screen at a healthy frame rate
// and reports itself started. Nothing downstream can tell that from a working
// game, so the only safe place to be strict is here.
func pickFirmware(core string, wanted []string, held []rommFirmwareRow) (libraryFirmware, bool) {
	for _, name := range wanted {
		for _, row := range held {
			if !strings.EqualFold(strings.TrimSpace(row.FileName), name) {
				continue
			}
			if row.Missing {
				// The library has a row and the disk does not have the file.
				// Offering it would be a game that fails after the download.
				continue
			}
			if row.Size <= 0 || row.Size > maxBIOSBytes {
				continue
			}
			return libraryFirmware{
				Core: core,
				// The library's own spelling, not the one we asked for: it is
				// the name the file is fetched under and the name the emulator
				// will write it as, and those two must be the same string.
				Name: strings.TrimSpace(row.FileName),
				ID:   row.ID,
				Size: row.Size,
				SHA1: strings.TrimSpace(row.SHA1),
			}, true
		}
	}
	return libraryFirmware{}, false
}

// Open fetches the bytes, once, and remembers them.
//
// A BIOS is between 1 KB and 512 KB and never changes, so this is the rare cache
// with no invalidation problem: it is keyed by the library's own file id, and an
// operator who replaces the file gets a new id or a new index entry either way.
func (f *rommFirmware) Open(ctx context.Context, core string) ([]byte, libraryFirmware, error) {
	if f == nil {
		return nil, libraryFirmware{}, errors.New("no library configured")
	}
	fw, ok := f.Firmware(ctx, core)
	if !ok {
		return nil, libraryFirmware{}, fmt.Errorf("the library holds no firmware for %s", core)
	}

	f.bytesMu.Lock()
	cached, hit := f.bytes[fw.ID]
	f.bytesMu.Unlock()
	if hit {
		return cached, fw, nil
	}

	ctx, cancel := context.WithTimeout(ctx, biosLibraryTimeout)
	defer cancel()

	body, err := f.fetch(ctx, fw)
	if err != nil {
		return nil, fw, err
	}

	f.bytesMu.Lock()
	f.bytes[fw.ID] = body
	f.bytesMu.Unlock()
	return body, fw, nil
}

// fetch performs the one authenticated download.
//
// Bounded by maxBIOSBytes+1 rather than trusting the declared size: a library
// that answers with something enormous must cost a refusal, not this process's
// memory.
func (f *rommFirmware) fetch(ctx context.Context, fw libraryFirmware) ([]byte, error) {
	tok, err := f.c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/api/firmware/%d/content/%s",
		f.c.baseURL, fw.ID, url.PathEscape(fw.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := f.c.hc.Do(req)
	if err != nil {
		return nil, &gameTransportError{Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		drain(resp.Body)
		return nil, &gameHTTPError{Status: resp.StatusCode, Method: "GET", URL: "firmware content"}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBIOSBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("the library served an empty firmware file")
	}
	if int64(len(body)) > maxBIOSBytes {
		return nil, fmt.Errorf("%s is past the %s ceiling for a firmware file",
			fw.Name, humanBytes(maxBIOSBytes))
	}
	return body, nil
}

// --- what a verdict says about firmware --------------------------------------

// biosUse is which firmware a playable answer is going to run with. Nil means
// none is available, which is what keeps the machine blocked.
type biosUse struct {
	Source string
	Name   string
	URL    string
	Size   int64
	// Options is set only for the built-in source, where there is no file and
	// the firmware is entirely a core setting.
	Options map[string]string
	// Detail overrides the generic "supply your own" sentence when what is
	// running is not a file the person supplied.
	Detail string
}

// firmwareFor decides WHOSE firmware unblocks this machine, and is the single
// place the precedence rule lives.
//
// A file the visitor added by hand wins over the library's, and the reason is
// not technical: they chose it. Somebody who went and found a specific Kickstart
// revision because the game they want needs it has said something, and silently
// running the library's 1.3 instead would be us overruling them about their own
// machine. It is also the cheaper answer -- their file is already in the tab.
//
// Returns nil for a core that is not blocked on firmware at all, so a caller
// cannot accidentally attach firmware to a refusal that was about something else.
func (p *playArchive) firmwareFor(ctx context.Context, core string, opts playOptions) *biosUse {
	if b, blocked := blockedSystems[core]; !blocked || b.Reason != reasonNeedsBIOS {
		return nil
	}
	if _, named := biosRequirements[core]; !named {
		return nil
	}

	// 1. THEIRS. This server never learns anything about the file beyond the
	//    name of the machine it is for, which is the whole design of bios.js.
	//    First because they chose it: somebody who went and found a specific
	//    Kickstart revision because the game they want needs it has said
	//    something, and running anything else instead would be us overruling
	//    them about their own machine.
	if opts.hasBIOS(core) {
		return &biosUse{Source: biosSourceYours}
	}

	// 2. and 3. THEIR LIBRARY, then THE INTERNET ARCHIVE'S OWN. An ordered
	//    slice rather than two branches, so that the precedence rule is one
	//    readable line and adding a fourth source cannot silently reorder it.
	//    Their own library first because those are their files, verified by
	//    their own server, and cost the Archive nothing.
	for _, src := range p.firmwareSources() {
		if src.lib == nil {
			continue
		}
		fw, ok := src.lib.Firmware(ctx, core)
		if !ok {
			continue
		}
		return &biosUse{
			Source: src.name,
			Name:   fw.Name,
			URL:    biosContentPath(src.name, core, fw.Name),
			Size:   fw.Size,
		}
	}

	// 4. NOBODY'S -- because the core already has one. No file exists for these
	//    anywhere; the whole of the firmware is a core option. Last because a
	//    real BIOS beats a replacement wherever there is one to be had.
	if built, ok := builtInFirmware[core]; ok {
		return &biosUse{Source: biosSourceBuiltIn, Options: built.Options, Detail: built.Detail}
	}
	return nil
}

// firmwareSource pairs a source with the name that identifies it in a URL, so
// the relay path says which archive a file came out of rather than leaving the
// handler to guess.
type firmwareSource struct {
	name string
	lib  biosLibrary
}

// firmwareSources is the precedence order, and it is the only place it exists.
func (p *playArchive) firmwareSources() []firmwareSource {
	return []firmwareSource{
		// Their own disk first: no credential, no network, and it is the same
		// tree their library server reads, so it can only ever be as fresh or
		// fresher.
		{biosSourceDisk, p.diskFirmware},
		{biosSourceLibrary, p.firmware},
		{biosSourceArchive, p.publicFirmware},
	}
}

// biosContentPath is the one place the relay URL is spelled, so the handler and
// the answer cannot disagree about it.
func biosContentPath(source, core, name string) string {
	return "/api/play/bios/" + url.PathEscape(source) + "/" +
		url.PathEscape(core) + "/" + url.PathEscape(name)
}

// --- serving the bytes -------------------------------------------------------

// handleBIOS serves one firmware file to the emulator.
//
// WHAT THIS IS NOT: a proxy into the library. It serves exactly one file per
// machine -- the one the index chose -- and only for the three machines that are
// blocked on firmware. A caller cannot name a library id, cannot name a path,
// and cannot ask for a file the index did not already pick. Getting the name
// wrong is a 404, not a search.
//
// CORS: open, like every other route on this surface and like the ROM relay the
// same emulator fetches the game from. They are fetched by the same XHR from the
// same page, and a client pointed at a self-hosted instance -- which is the
// deployment this project exists for -- would otherwise get a game that loads
// its ROM and fails on its firmware. The credential never crosses; what crosses
// is one console BIOS the household already owns.
func (p *playArchive) handleBIOS(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	core := r.PathValue("system")
	name := r.PathValue("file")

	// Only a machine that is actually blocked on firmware. Not a filter for
	// safety's sake: a request for any other machine is a client that has
	// misunderstood, and answering it would be inventing a capability.
	if _, ok := biosRequirements[core]; !ok {
		http.NotFound(w, r)
		return
	}

	var lib biosLibrary
	for _, src := range p.firmwareSources() {
		if src.name == source && src.lib != nil {
			lib = src.lib
		}
	}
	if lib == nil {
		http.NotFound(w, r)
		return
	}

	// The index is read WITHOUT refreshing. An anonymous request must never be
	// able to make this server call into the household's library, or spend
	// somebody else's bandwidth at the Internet Archive; the index is built at
	// startup and by real verdicts, and a cold one here is a 404 that the next
	// verdict fixes.
	fw, ok := lib.Cached(core)
	if !ok || !strings.EqualFold(fw.Name, name) {
		http.NotFound(w, r)
		return
	}

	// The library's own hash. A browser that has the file already does not fetch
	// it twice, and a REPLACED file gets a different tag rather than being
	// masked by a cache -- which for firmware is the difference between a game
	// that boots and one that shows NO BIOS.
	etag := `"` + fw.SHA1 + `"`
	if fw.SHA1 != "" && matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body, fw, err := lib.Open(r.Context(), core)
	// An EMPTY body is treated as a failure rather than served, and this is the
	// single most important line in the handler. A 200 carrying nothing is not a
	// missing BIOS from the emulator's point of view -- it is a BIOS of zero
	// bytes, which the core loads, rejects, and reports by drawing its own error
	// screen at a healthy frame rate over a game that never booted. That failure
	// is invisible to everything downstream, so it has to be refused here.
	if err == nil && len(body) == 0 {
		err = errors.New("the library served an empty firmware file")
	}
	if err != nil {
		// Deliberately vague, and deliberately not the upstream's words: an
		// error body from the library can carry its address or a stack trace,
		// and neither belongs in an answer to a browser.
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "the " + source + " firmware source did not serve a file for " + core,
		})
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	// The name the emulator will write it under. Belt and braces -- the URL's
	// last segment is what EmulatorJS actually reads -- but a client that saves
	// the file should get the name the core asks for.
	w.Header().Set("Content-Disposition",
		`inline; filename="`+path.Base(fw.Name)+`"`)
	if fw.SHA1 != "" {
		w.Header().Set("ETag", etag)
	}
	// Private: this is one household's file relayed through their own server,
	// and a shared cache in front of it has no business holding it.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// matchesETag reads an If-None-Match header, which may be a list and may be `*`.
func matchesETag(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}
