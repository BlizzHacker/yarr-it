package main

// Vimm's Lair as a search source.
//
// Every other source in this service is a network call: archive.org is a Solr
// query, the indexers are a Prowlarr fan-out. This one is a file on the box,
// scraped from vimm.net by a browser extension Wade runs and imported here.
// That single difference decides almost everything below.
//
// WHY IT IS THE FASTEST SOURCE, AND WHERE IT RUNS
//
// A local catalogue can answer inside the first-paint budget with room to
// spare, so it does -- see vimmStage in progressive.go, which runs it
// SYNCHRONOUSLY at the top of the job, before archive.org's goroutine is even
// started. That ordering is not a micro-optimisation, it is a correctness
// property: firstWave is released by whichever fast source finishes first, and
// a source that adds its cards after the release contributes nothing to the
// paint it was supposed to be part of. Running first is the only way to be
// certain.
//
// The cost of that is a lock and a scan on the job goroutine, so the scan has
// to be genuinely cheap. It is: one strings.Contains per entry per typed word
// against a lower-cased title computed at load. Measured shape rather than
// guessed -- 5,586 entries today, ~23,000 once Wade's scan completes, which is
// a fraction of a millisecond either way. It is deliberately a scan and not an
// index, for the reason localCards gives: an index is a second copy of the
// truth that drifts from it.
//
// NOTHING IS PARSED ON A REQUEST PATH. The export is 9.2 MB of JSON and must
// never be near one. It is read once by the importer, reduced to the entries
// that are actually publishable, and written to a small file that the server
// reads once at startup. See vimm_import.go.
//
// WHAT A RESULT FROM HERE IS, AND IS NOT
//
// This site does not host any of it and cannot play any of it. A Vimm entry is
// a link to somebody else's website, and the whole of the card built here is
// arranged so that no part of the UI can accidentally claim otherwise:
//
//   - Instant is false. That flag means "served over HTTP by a host that is
//     always up" and it is what puts the INSTANT badge and the word PLAY on a
//     tile. Setting it here would be the exact lie this file exists to avoid.
//   - External is set, which is the positive statement: this lives on another
//     site, here is its name and host, every source on this card leaves.
//   - The two facts Vimm publishes about an entry are kept apart. `playable`
//     becomes a source whose action is "play" and whose target is Vimm's own
//     in-browser player; `downloadable` becomes a source whose action is
//     "download" and whose target is their file host. An entry with both gets
//     both rows, an entry with one gets one. They are not the same fact and
//     collapsing them into a single "open" would throw away the only thing a
//     person needs to know before clicking.
//
// WHY THERE IS NO ARTWORK
//
// Vimm has box art, but nothing in this export addresses it and guessing a URL
// from a vault id would put a broken image in a 2:3 box on every tile. The
// placeholder the client already draws says "no cover" properly. Art is a thing
// to add when the extension captures it, not to invent here.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
)

// ------------------------------------------------------------ the machines --

// vimmHost is the only site an imported row may point at.
//
// The export comes from an extension running in a browser, which is to say from
// the least trustworthy place in this system: whatever the page said, it
// believed. Publishing those URLs unchecked would make this catalogue a
// redirector to anywhere, so every one of the three URLs on an entry is
// required to be an https URL on this host or a subdomain of it -- their file
// host is dl2/dl3.vimm.net, so subdomains have to be allowed and the check is
// a suffix on a label boundary rather than a Contains.
const vimmHost = "vimm.net"

// domainGame is schema.json's id for the games domain. Spelled once here rather
// than as a literal at each use, the way music.go spells its own.
const domainGame = "game"

// vimmSiteName and vimmShortName are what a person is told. The short form is
// what fits in the corner of a tile.
const (
	vimmSiteName  = "Vimm's Lair"
	vimmShortName = "Vimm"
)

// vimmSystems maps Vimm's own platform name onto this site's system slug.
//
// EXPLICIT, AND LOUD WHEN IT IS WRONG. A platform absent from this map is an
// import error that names the platform and the entries it affects; it is never
// a quiet skip. Vimm adds systems, and the failure mode of a silent default is
// that a thousand Dreamcast games arrive one week and simply do not appear,
// with nothing anywhere saying why.
//
// A platform mapped to the EMPTY STRING is a different and deliberate answer:
// Vimm has this machine, this site has no slug for it, and the entries are
// published anyway carrying Vimm's own name for the machine. They are findable
// by title and they say what they are; they take no part in the system facet,
// because there is no chip that could select them, and they are excluded by a
// `system=` filter, because they are not the machine that was asked for.
//
// The alternative -- inventing slugs, or folding Sega CD into `genesis` and
// TurboGrafx-CD into `tg16` because the machines are related -- was rejected.
// systems.go is derived from archive.org's own emulator census and ROM Hub's
// platform slugs, a Sega CD game is not a Genesis game, and a facet that
// answers "Genesis" to a Sega CD disc is a worse failure than one that says
// nothing. That file is also the wrong place to grow: an entry there without
// archive.org emulator ids would build the Solr fragment `emulator:()`.
var vimmSystems = map[string]string{
	// --- Nintendo ---------------------------------------------------------
	"Nintendo":       "nes",
	"Super Nintendo": "snes",
	"Nintendo 64":    "n64",
	"Game Boy":       "gb",
	"Game Boy Color": "gbc",
	"Game Boy Adv":   "gba",
	"Nintendo DS":    "nds",
	"Virtual Boy":    "",
	"GameCube":       "",
	"Wii":            "",
	"WiiWare":        "",
	"Wii U":          "",
	"Nintendo 3DS":   "",

	// --- Sega -------------------------------------------------------------
	"Genesis":       "genesis",
	"Master System": "sms",
	"Game Gear":     "gamegear",
	"Sega 32X":      "sega32",
	"Sega CD":       "",
	"Saturn":        "",
	"Dreamcast":     "",

	// --- Atari ------------------------------------------------------------
	"Atari 2600": "atari2600",
	"Atari 5200": "atari5200",
	"Atari 7800": "atari7800",
	"Lynx":       "lynx",
	"Jaguar":     "",
	"Jaguar CD":  "",

	// --- Everything else --------------------------------------------------
	"PlayStation":        "psx",
	"PlayStation 2":      "",
	"PlayStation 3":      "",
	"PS Portable":        "",
	"Xbox":               "",
	"Xbox 360":           "",
	"Xbox 360 (Digital)": "",
	"TurboGrafx-16":      "tg16",
	"TurboGrafx-CD":      "",
	"CD-i":               "",
}

// vimmSystemFor resolves a Vimm platform name. The second return distinguishes
// "this site has no slug for it" from "nobody has ever heard of it", which are
// answered very differently -- the first publishes, the second fails the
// import.
func vimmSystemFor(platform string) (string, bool) {
	id, known := vimmSystems[strings.TrimSpace(platform)]
	return id, known
}

// ------------------------------------------------------------- the entries --

// vimmEntry is one row of the Vault, reduced to what this service publishes.
//
// The raw export carries eleven fields per item and 17,384 rows that hold
// nothing usable at all; none of that reaches disk here. What is kept is what a
// card is built from, plus the two raw inputs the TITLE is derived from -- so
// that a re-import can improve a title it once had to guess at, rather than
// being stuck with the guess. See vimmTitle.
type vimmEntry struct {
	// VaultID is Vimm's page id and this catalogue's primary key. It is stable
	// across exports, which is what makes an import a merge rather than an
	// append. Note that it is NOT the id in the download and play URLs: those
	// carry a separate `mediaId` which differs for 5,582 of the 5,586 rows
	// measured, so neither can be derived from the other and both are stored.
	VaultID string `json:"vault_id"`

	// Title is the name shown to a person, already derived.
	Title string `json:"title"`
	// TitleFrom records which input Title came from -- "page" or "file" -- so
	// the merge can prefer a real page title over one recovered from a
	// filename without having to re-derive both to find out which it holds.
	TitleFrom string `json:"title_from"`
	// PageTitle is the extension's title for the row, kept only when it is a
	// real one. Stored rather than discarded so a later export that regresses
	// (an entry re-scanned by an older build) cannot take a good title away.
	PageTitle string `json:"page_title,omitempty"`
	// File is the ROM or disc filename, the fallback the title is recovered
	// from and the label on a source row -- it is the one place the region,
	// revision and disc number survive.
	File string `json:"file,omitempty"`

	// Platform is Vimm's own name for the machine, shown to a person.
	Platform string `json:"platform"`
	// System is this site's slug for the same machine, or empty where this
	// site has no slug. This is what the `system=` filter and the system facet
	// key on, so it must stay in systems.go's vocabulary.
	System string `json:"system,omitempty"`

	Page     string `json:"page"`
	Play     string `json:"play,omitempty"`
	Download string `json:"download,omitempty"`
	Size     int64  `json:"size,omitempty"`

	// Core is the EmulatorJS system the vault will serve this entry as, or
	// empty for the half of the Vault no browser can run -- Xbox 360, PS3,
	// Wii, GameCube, PS2, Dreamcast, CD-i.
	//
	// It is COPIED FROM THE VAULT, never worked out here. The vault reads
	// EJS_core off vimm.net's own player page and checks it against the core
	// list of the EmulatorJS release it serves, so it is the only thing that
	// knows both halves of the answer. Deriving it a second time in this
	// process would be a copy of that truth that drifts from it, and the way
	// it would drift is silent: a name this side invented 404s at the core
	// download, behind a loading bar that never finishes.
	//
	// An import from a plain export file leaves this empty, and a card with no
	// core is exactly the external link-out it has always been. See
	// vimmVaultBase.
	Core string `json:"core,omitempty"`
}

// vaultable reports whether this entry can be played here rather than linked.
func (e vimmEntry) vaultable() bool { return e.Core != "" }

// playable and downloadable are derived from whether a verified target
// survived the import, never from the export's own boolean. The two agreed on
// every row measured, but the URL is the thing that gets clicked and a flag
// that disagreed with it would publish a button to nowhere.
func (e vimmEntry) playable() bool     { return e.Play != "" }
func (e vimmEntry) downloadable() bool { return e.Download != "" }

// vimmCatalogue is the file on disk.
type vimmCatalogue struct {
	Version int    `json:"version"`
	Source  string `json:"source"`
	// Generated is the export's own timestamp, carried through so it is
	// possible to tell which scan a row came from.
	Generated string `json:"generated_at,omitempty"`
	Imported  string `json:"imported_at,omitempty"`
	// Entries are sorted by vault id, so re-importing an unchanged export
	// produces a byte-identical file and a diff shows only what really moved.
	Entries []vimmEntry `json:"entries"`
}

const vimmCatalogueVersion = 1

// -------------------------------------------------------------- the titles --

// vimmNoiseTitles are the strings the extension returns when it has not found a
// game name at all.
//
// This list is not defensive padding. A vault page has no `h1`, no
// `.game-title` and no `[itemprop=name]`; the name exists only in the document
// title. An extension build that looked for those selectors found nothing and
// fell through to page furniture, and every one of the 4,470 playable rows in
// the export came back as "Upload it to The Vault". The extension now reads
// `<title>`, but exports made by both builds exist and one file can hold rows
// from each, so the reject list is what tells them apart.
var vimmNoiseTitles = map[string]bool{
	"upload it to the vault": true,
	"the vault":              true,
	"vimm's lair":            true,
	"vimms lair":             true,
	"home":                   true,
	"login":                  true,
	"register":               true,
	"donate":                 true,
}

func isVimmNoiseTitle(s string) bool {
	return vimmNoiseTitles[strings.ToLower(strings.TrimSpace(s))]
}

// vimmROMExtensions are the file suffixes that may be stripped from a filename.
//
// An allowlist rather than "everything after the last dot", because 1,668 of
// the filenames measured -- every disc-based platform -- carry no extension at
// all, and their titles contain dots that are part of the name. `Dot Hack Part
// 1 - Infection` and `R.C. Pro-Am` both lose a word to a rule that trusts the
// last dot. An unrecognised suffix is left alone and counted in the import
// report, so a new one shows up as a number rather than as damaged titles.
var vimmROMExtensions = map[string]bool{
	"nes": true, "sfc": true, "smc": true, "fig": true,
	"md": true, "gen": true, "smd": true, "32x": true, "sms": true, "gg": true,
	"a26": true, "a52": true, "a78": true, "j64": true, "jag": true, "lyx": true, "lnx": true,
	"gb": true, "gbc": true, "gba": true, "nds": true, "3ds": true, "vb": true,
	"n64": true, "z64": true, "v64": true, "wad": true,
	"pce": true, "sgx": true, "ngp": true, "ngc": true, "ws": true, "wsc": true,
	"iso": true, "bin": true, "cue": true, "chd": true, "img": true, "gdi": true,
	"xex": true, "gcm": true, "rvz": true, "wbfs": true, "cso": true, "wud": true,
}

// stripVimmExtension removes a known ROM or disc suffix. The bool reports
// whether the filename ended in a dotted suffix that is NOT known, which the
// importer counts rather than acting on.
func stripVimmExtension(name string) (string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return name, false
	}
	ext := name[i+1:]
	if len(ext) > 5 {
		return name, false
	}
	for _, r := range ext {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return name, false
		}
	}
	if vimmROMExtensions[strings.ToLower(ext)] {
		return name[:i], false
	}
	return name, true
}

// vimmTitleFromFile recovers a game's name from its filename.
//
// Vimm files are named to the No-Intro / Redump convention, which is two of the
// conventions match.go already knows and nothing else:
//
//	10-Yard Fight (USA, Europe).nes      -> 10-Yard Fight
//	Last Story, The (USA) (En,Fr,Es).iso -> The Last Story
//	[BIOS] 32X M68000 (USA).bin          -> 32X M68000
//
// So it is built out of match.go's own primitives rather than out of a second
// parser: splitBrackets removes every parenthesised and bracketed group --
// region, languages, revision, disc number, dump flags, the [BIOS] marker --
// and restoreArticle undoes the inversion. The order is splitBrackets FIRST,
// exactly as decompose does it, because `Chrono Cross (USA, Canada)` has a
// comma inside the brackets that would otherwise be read as an inversion.
//
// What it deliberately does NOT do is decompose's trailing-noise strip. That
// exists because archive.org uploaders end titles with the machine -- "Chrono
// Trigger SNES ROM" -- and Vimm never does. Applied here it would be actively
// destructive: `isSuffixNoise` holds "wii", "jaguar" and "lynx", so
// `Mario Kart Wii` becomes `Mario Kart`, `New Super Mario Bros. Wii` loses its
// machine, and the Jaguar BIOS is retitled `Atari`. Matching still goes through
// the full normalisation at search time -- see vimmCards, which ranks with
// rankByMatch -- so nothing is lost by keeping the display name whole.
func vimmTitleFromFile(file string) string {
	name, _ := stripVimmExtension(strings.TrimSpace(file))
	base, _ := splitBrackets(name)
	base = restoreArticle(base)
	// Fields collapses the runs of whitespace splitBrackets leaves where a
	// group used to be, so "Zelda (USA) - Gold" does not keep a double space.
	base = strings.Join(strings.Fields(base), " ")
	return strings.Trim(base, " -–—_")
}

// vimmTitle decides an entry's display name from the two inputs that can carry
// one, and says which it used.
//
// The page title wins whenever it is real. A filename is a lossy rendering of a
// name -- it has lost the colon in "Sonic 3: Angel Island", the accents, and
// anything the filesystem would not take -- so a real page title is strictly
// better information. The filename is the fallback, and it is a good one: 4,465
// of the 4,470 playable rows measured carry one.
func vimmTitle(pageTitle, file string) (string, string) {
	t := strings.TrimSpace(pageTitle)
	if t != "" && !isVimmNoiseTitle(t) {
		return t, "page"
	}
	if f := vimmTitleFromFile(file); f != "" {
		return f, "file"
	}
	return "", ""
}

// ---------------------------------------------------------------- the URLs --

// vimmURL checks that a URL from the export is an https URL on vimm.net or a
// subdomain, and returns it cleaned. Anything else returns "", which the
// importer counts and the entry then simply does not offer that action.
func vimmURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h != vimmHost && !strings.HasSuffix(h, "."+vimmHost) {
		return ""
	}
	return u.String()
}

// --------------------------------------------------------------- the store --

// vimmRow is an entry plus what the scan needs, computed once at load.
type vimmRow struct {
	vimmEntry
	lower string // the title, lower-cased, so a scan is one Contains per word
}

// vimmStore is the catalogue in memory.
//
// A nil store answers every question with nothing, so a server built by a test
// with a struct literal -- which is how most of the tests in this package build
// one -- behaves exactly like a deployment that has never imported anything.
type vimmStore struct {
	path string

	mu        sync.RWMutex
	rows      []vimmRow
	generated string
	imported  string
	// systemCounts is how many browsable works each machine holds, computed
	// once when the catalogue is swapped in rather than per request.
	//
	// It is read by /api/categories, which the browse landing page blocks on,
	// and computing it there costs a full scan plus a title normalisation per
	// row -- measured at 12.7 ms for a 23,000-row catalogue. The catalogue only
	// changes on import, so paying that once at load is strictly better. See
	// vimmSystemCounts.
	systemCounts map[string]int
}

// vimmVaultBase is the origin of the Vimm vault, or empty when there is none.
//
// THIS FLAG IS WHAT TURNS A LINK INTO A GAME. The vault holds the ROMs behind
// an origin that answers CORS and always up, which is the one thing this site
// never had for Vimm: until it existed, a Vimm result could only ever be a link
// to somebody else's website, and vimm.go says so at length. With it, an entry
// the vault can serve is played here like any archive.org ROM.
//
// Empty by default and empty for every self-host, so nothing changes for an
// instance that has no vault: those cards stay external, exactly as before. It
// is deliberately not a compiled-in constant -- a self-hoster runs their own
// vault at their own hostname, and this site's is not special.
func vimmVaultBase() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("VIMM_VAULT")), "/")
}

// vimmCataloguePath is where the imported catalogue lives.
//
// VIMM_PATH overrides it; the default is beside library.json in the unit's
// StateDirectory, which under ProtectSystem=strict is the only writable
// directory the service has. A self-host with neither gets the relative path,
// which is the same shape selfhost.sh already uses for the library.
func vimmCataloguePath() string {
	if p := strings.TrimSpace(os.Getenv("VIMM_PATH")); p != "" {
		return p
	}
	return "/var/lib/mw-search/vimm.json"
}

// loadVimmStore reads the catalogue file. A missing file is not an error: an
// instance that has never imported is a normal state, and the only difference
// it makes is that this source contributes nothing.
func loadVimmStore(path string) (*vimmStore, error) {
	s := &vimmStore{path: path}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	var cat vimmCatalogue
	if err := json.Unmarshal(raw, &cat); err != nil {
		// A corrupt catalogue must not take the service down, but it must not
		// be silently ignored either -- an empty games shelf with no
		// explanation is the failure this whole file is trying to avoid.
		return nil, fmt.Errorf("vimm catalogue %s is unreadable: %w", path, err)
	}
	s.replace(cat)
	return s, nil
}

// replace swaps in a whole catalogue, computing the scan keys.
func (s *vimmStore) replace(cat vimmCatalogue) {
	rows := make([]vimmRow, 0, len(cat.Entries))
	for _, e := range cat.Entries {
		if e.Title == "" {
			continue
		}
		rows = append(rows, vimmRow{vimmEntry: e, lower: strings.ToLower(e.Title)})
	}
	counts := countVimmWorks(rows)
	s.mu.Lock()
	s.rows = rows
	s.generated = cat.Generated
	s.imported = cat.Imported
	s.systemCounts = counts
	s.mu.Unlock()
}

func (s *vimmStore) count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rows)
}

// stats reports what is loaded, for /api/health.
func (s *vimmStore) stats() map[string]any {
	if s == nil {
		return map[string]any{"entries": 0}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]any{"entries": len(s.rows)}
	if s.generated != "" {
		out["generatedAt"] = s.generated
	}
	if s.imported != "" {
		out["importedAt"] = s.imported
	}
	return out
}

// --------------------------------------------------------------- searching --

// vimmCardLimit is how many cards one query may contribute.
//
// Generous, because these cost nothing to produce and the first paint shows
// sixty. The cap exists so a one-word query against a 23,000-row catalogue
// cannot dominate a mixed result set, not because producing them is expensive.
const vimmCardLimit = 120

// search answers a query from the catalogue.
//
// `systems` is optional: nil means every machine, and a non-empty set narrows
// to those slugs exactly. The narrowed form is what makes a `system=` filter
// honest here -- see vimmDeepen, and deepenBySystem in archive.go for the
// problem it solves.
func (s *vimmStore) search(q, kind string, systems map[string]bool, limit int) []card {
	if s == nil {
		return nil
	}
	// Games only, and a browse is browse.go's job. An empty query would match
	// every row, which is not a search result, it is the catalogue.
	if kind != "" && !sameDomain(kind, domainGame) {
		return nil
	}
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(q)))
	if len(terms) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = vimmCardLimit
	}

	s.mu.RLock()
	// Grouped as they are gathered: one card is one work on one machine, and
	// Vimm holds a work several times over -- three discs of Final Fantasy VII,
	// a USA and a Europe dump, a revision. Those are rows in the same card's
	// source list, which is what a card has always been. Two machines are two
	// cards, because System is a single value and the facet keys on it.
	groups := map[string][]vimmEntry{}
	order := []string{}
	for i := range s.rows {
		r := &s.rows[i]
		if systems != nil && !systems[r.System] {
			continue
		}
		if !lowerHasAll(r.lower, terms) {
			continue
		}
		k := vimmGroupKey(r.vimmEntry)
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r.vimmEntry)
	}
	s.mu.RUnlock()

	cards := make([]card, 0, len(order))
	for _, k := range order {
		if c, ok := vimmCard(k, groups[k]); ok {
			cards = append(cards, c)
		}
	}

	// Ranked exactly as archive.org's results are, through the same scorer, so
	// an exact title leads and a bundle that merely contains the words does
	// not. rankByMatch also drops what scores below matchFloor, which is what
	// keeps a search for "mario" from returning Marionette.
	cards = rankByMatch(cards, q)
	if len(cards) > limit {
		cards = cards[:limit]
	}
	return cards
}

// lowerHasAll is titleHasAll against a string that is already lower-cased.
// Same rule -- every typed word appears somewhere in the title, substring
// rather than whole word -- against a key computed once at load rather than per
// row per query.
func lowerHasAll(lower string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(lower, t) {
			return false
		}
	}
	return true
}

// vimmGroupKey identifies one work on one machine.
//
// The machine comes first and is the slug where there is one and Vimm's own
// name where there is not, so a Sega CD disc and a Genesis cartridge of the
// same game stay apart even though neither has a slug conflict to keep them so.
// The title is canonicalised through match.go, which is what makes "Legend of
// Zelda, The" and "The Legend of Zelda" one card rather than two.
func vimmGroupKey(e vimmEntry) string {
	machine := e.System
	if machine == "" {
		machine = strings.ToLower(e.Platform)
	}
	return machine + "\x00" + canonicalTitle(e.Title)
}

// vimmCard builds one card from the rows that share a work and a machine.
func vimmCard(key string, entries []vimmEntry) (card, bool) {
	if len(entries) == 0 {
		return card{}, false
	}
	// Deterministic: the same export must produce the same card every time, and
	// map iteration order is not that. Sorted by vault id numerically where
	// both are numbers, so "Disc 1" leads "Disc 2".
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if len(a.VaultID) != len(b.VaultID) {
			return len(a.VaultID) < len(b.VaultID)
		}
		return a.VaultID < b.VaultID
	})

	head := entries[0]
	// The shortest title in the group is the one with the fewest qualifiers
	// left on it, which is the one to show. A tie keeps the first.
	title := head.Title
	for _, e := range entries[1:] {
		if len(e.Title) < len(title) {
			title = e.Title
		}
	}

	vault := vimmVaultBase()

	sources := make([]source, 0, len(entries)*3)
	vaulted := false
	for _, e := range entries {
		label := e.File
		if strings.TrimSpace(label) == "" {
			label = e.Title
		}
		// The vault's row goes FIRST, because Best is index 0 and the first
		// row is the one a click gets. A card that can be played here must
		// offer that before it offers a trip to another website.
		if vault != "" && e.vaultable() {
			sources = append(sources, vimmVaultSource(e, label, vault))
			vaulted = true
		}
		// Playing and downloading are separate offers because they are separate
		// facts about the entry, and because they land in different places:
		// one opens Vimm's own in-browser player, the other starts a file
		// transfer from their download host. A row that said only "open" would
		// leave a person to find out which by clicking.
		if e.playable() {
			sources = append(sources, vimmSource(e, label, "play", e.Play))
		}
		if e.downloadable() {
			sources = append(sources, vimmSource(e, label, "download", e.Download))
		}
	}
	if len(sources) == 0 {
		// Nothing verified survived the import, so there is nothing to click.
		// Under this site's rule a tile carries a real target or is not shown.
		return card{}, false
	}

	c := card{
		Key:   "vimm:" + strings.ReplaceAll(key, "\x00", ":"),
		Title: title,
		Kind:  domainGame,
		// The place, for the tile's source label. Every source row on this card
		// is labelled with the same name, but a tile shows no rows.
		Origin:   vimmSiteName,
		Platform: head.Platform,
		System:   head.System,
		Groups:   []string{"games"},
		Adult:    isAdultItem(title, "", nil),
		Sources:  sources,
	}

	if !vaulted {
		// Nothing here can be played on this site, so the card says so in the
		// one way the client understands. See the file comment: External and
		// Instant are opposites and a card may never carry both.
		c.External = &externalSite{
			Name:  vimmSiteName,
			Short: vimmShortName,
			Host:  vimmHost,
			// The vault page, so a client that wants to send somebody to the
			// thing itself rather than to one of its files has an address for
			// it that is not a download.
			Page: head.Page,
		}
		return c, true
	}

	// The vault serves this one. Instant is now the truth rather than the lie
	// it would have been before the vault existed: the ROM comes over HTTP
	// from a host that is always up, and it plays in this site's own player.
	// External is therefore absent -- not forgotten. Origin still names Vimm's
	// Lair, which is where the game really comes from and what the tile shows.
	c.Instant = true
	// The vault proxies box art from vimm.net, which is the only reason there
	// is any: hotlinking it fails (they require their own Referer) and
	// guessing a URL from a vault id put a broken image in a 2:3 box, which is
	// why this card carried no artwork at all until now.
	c.Art = artwork{Poster: vault + "/api/art/" + head.VaultID, Found: true}
	return c, true
}

// vimmVaultSource is the row that plays here.
func vimmVaultSource(e vimmEntry, label, vault string) source {
	return source{
		Title:     label,
		Indexer:   vimmSiteName,
		Size:      e.Size,
		SizeHuman: humanSize(e.Size),
		// The core travels with the URI in the fragment, the same shape
		// archive.go uses for its `#ejs`. It must never be inferred at the
		// other end from a file extension: `.bin` is Colecovision, Atari 2600
		// and Mega Drive at once, and the wrong core boots successfully and
		// then runs a black screen with no error at all.
		// The name rides along because the URI has nothing else to offer one:
		// its last path segment is the vault id, and a player that named the
		// game from the URL would put "Play 3" on the button.
		Magnet: vault + "/api/rom/" + e.VaultID +
			"#ejs=" + url.QueryEscape(e.Core) + "&name=" + url.QueryEscape(label),
		Source:  e.Platform,
		Quality: "TOUCH",
		// Plays in this site's player, so it must survive the webSafe filter
		// and must NOT be marked offsite -- both of those are what route a
		// source into the player rather than into a new tab.
		WebSafe: true,
		Action:  "play",
	}
}

func vimmSource(e vimmEntry, label, action, target string) source {
	return source{
		Title:     label,
		Indexer:   vimmSiteName,
		Size:      e.Size,
		SizeHuman: humanSize(e.Size),
		// The field a client follows. It is named Magnet for historical
		// reasons and has carried https URLs since archive.org was added; see
		// sourcesFor in archive.go, which puts a details page in it.
		Magnet: target,
		Source: e.Platform,
		// Not web-safe: nothing here plays in this site's player. The `webSafe`
		// filter drops these, which is correct.
		WebSafe: false,
		Offsite: true,
		Action:  action,
	}
}

// --------------------------------------------------------------- the server --

// vimmDeepen tops a result set up with catalogue entries for the machines a
// filter asked for.
//
// The same asymmetry deepenBySystem describes applies here, for a smaller
// reason: a query contributes at most vimmCardLimit cards, so narrowing that
// slice to one machine can leave less than the catalogue really holds. Unlike
// archive.org the narrowed question costs nothing at all -- it is the same scan
// with one extra comparison -- so it is simply asked again.
func (s *server) vimmDeepen(q, kind string, f filters, cards []card) []card {
	if s == nil || s.vimm == nil {
		return cards
	}
	systems := f.systems()
	if len(systems) == 0 {
		return cards
	}
	want := make(map[string]bool, len(systems))
	for _, sys := range systems {
		want[sys.ID] = true
	}

	have := make(map[string]bool, len(cards))
	for _, c := range cards {
		have[c.Key] = true
	}
	for _, c := range s.vimm.search(q, kind, want, vimmCardLimit) {
		if !have[c.Key] {
			cards = append(cards, c)
		}
	}
	return cards
}
