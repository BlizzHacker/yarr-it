package main

// The Minerva Archive as a search source.
//
// Every source before this one publishes WORKS: a film, an album, one game.
// This one publishes SETS. A Minerva result is a single torrent that covers a
// whole curated collection -- every No-Intro SNES cartridge in one 8.4 GB
// file, every bitsavers manual in one 1.64 TB file -- and that one difference
// decides the whole of this file.
//
// WHAT A SET IS, AND WHAT IT IS NOT
//
// It is not a game. There is no per-title torrent anywhere on the site: the
// magnet drawn beside a single file in their browser is the magnet of the
// torrent that CONTAINS it, and all four files in a four-file directory show
// the same info-hash. The smallest thing anybody can actually fetch from
// bitsavers is 1.64 TB. A tile that offered one of these under the word "Play"
// would be the worst promise this codebase has ever made, so:
//
//   - Instant is false and always false. Nothing here is served over HTTP by
//     anybody; it is a swarm.
//   - External is nil and always nil. A magnet does not open somebody else's
//     website -- it is handed to a torrent client, which is generally not a
//     browser at all -- so the offsite vocabulary ("opens minerva-archive.org",
//     "leaves this site") would be false in both halves.
//   - Set is the positive statement, and it is the third member of a set of
//     three that are mutually exclusive by construction. It carries the SIZE,
//     because the size is the fact a person most needs before clicking, and
//     minervaCard is the single fork that decides which of the three a card
//     gets.
//
// WHY THE SEEDER COUNT IS NOT HERE
//
// Minerva publishes seeders and leechers. They are four months stale -- their
// poller stopped in April 2026 -- and a stale swarm number is worse than none:
// "0 seeders" renders in the dead-source colour and reads as "this will not
// work", while "412 seeders" reads as a promise about right now. So nothing
// counts a peer here. PeersKnown is false on every row this importer writes,
// the client is required to print no number at all rather than a zero, and
// MeasuredAt carries the date so the one honest sentence -- "Minerva last
// measured this swarm in April 2026" -- can still be said.
//
// The fix is a live tracker scrape, and it belongs in the workstation fetcher
// rather than on the VPS. Until somebody does it, PeersKnown stays false.
//
// WHERE THE DATA COMES FROM, AND WHERE IT DOES NOT
//
// This process makes NO network request to minerva-archive.org, ever. The
// fetch is a separate off-box mode (-fetch-minerva) run from a workstation and
// the reduction is another (-import-minerva); the server reads only the small
// file they leave behind. That keeps a third party off the first-paint budget,
// keeps the scraping off the hosted IP, and means an upstream HTML change can
// only ever fail a laptop run. See minerva_fetch.go and minerva_import.go.
//
// NOTHING IS PARSED ON A REQUEST PATH, for the same reason vimm.go says it.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------- the site --

const (
	minervaSiteName  = "Minerva Archive"
	minervaShortName = "Minerva"
	// minervaHost is the only host an imported row may point at. Checked as a
	// suffix on a label boundary rather than with Contains, for the reason
	// vimm.go gives: "minerva-archive.org.evil.example" contains it too.
	minervaHost = "minerva-archive.org"
)

// ------------------------------------------------------ the 21 collections --

// What kind of thing a collection holds. This is the FIRST of the two maps and
// it is the one that decides whether a set may be a game at all.
const (
	minervaRomset   = "romset"   // curated per-machine ROM sets
	minervaArcade   = "arcade"   // arcade romsets and dumps
	minervaDOS      = "dos"      // DOS/Windows game collections
	minervaDocs     = "docs"     // documentation, manuals, scans
	minervaMedia    = "media"    // artwork, photography, A/V rips
	minervaMixed    = "mixed"    // a grab-bag; nobody can say what one torrent is
	minervaExcluded = "excluded" // never imported, never carded, never banded
)

// minervaCollection is what this service will say about a whole collection.
type minervaCollection struct {
	// Kind is what it holds, from the list above.
	Kind string
	// Domain is the shelf it belongs on, in schema.json's vocabulary, or empty
	// where there is no honest answer. bitsavers is a mirror of a documentation
	// archive and belongs under literature; TOSEC-PIX is scans and artwork and
	// belongs under image. Routing them to `game` would put 1,124 vendor
	// document shelves into a games search, and dropping them would hide 3.9 TB
	// that people genuinely look for.
	//
	// A domain here does NOT give a set tile that domain's verb. See
	// tileAction in home.js: the set rung runs first and prints the size, so a
	// bitsavers torrent is never labelled "Read".
	Domain string
	// Facet says whether this collection's torrents are cut per machine, and so
	// whether its entries may carry a system slug and join the system facet.
	//
	// True for exactly four of the twenty-one. The rest are cut somewhere else
	// entirely -- TOSEC and TOSEC-ISO cut per PUBLISHER, so one torrent spans
	// every machine that publisher ever made, and answering "Sinclair" with
	// "ZX Spectrum" would be a facet lying about a torrent that also holds
	// ZX80 and ZX81 material. The twelve whole-collection torrents cut at the
	// collection, which is no machine at all.
	Facet bool
}

// minervaCollections is the loud table, and the reason it is loud is the same
// reason vimmSystems is: a collection absent from here FAILS THE IMPORT and
// names itself, rather than being quietly dropped or quietly published as a
// game. Minerva adds collections; the failure mode of a silent default is
// either 40 TB of unclassified material appearing in a games search, or a
// collection silently vanishing with nothing anywhere saying why.
//
// Verified against the live site on 2026-08-24: exactly these twenty-one
// appear under /browse/, they hold 1,049 magnets between them, and no hash is
// shared across two of them.
var minervaCollections = map[string]minervaCollection{
	// --- cut per machine: these four may carry a system slug ---------------
	"No-Intro":          {Kind: minervaRomset, Domain: domainGame, Facet: true},
	"Redump":            {Kind: minervaRomset, Domain: domainGame, Facet: true},
	"RetroAchievements": {Kind: minervaRomset, Domain: domainGame, Facet: true},
	"T-En Collection":   {Kind: minervaRomset, Domain: domainGame, Facet: true},

	// --- cut per publisher: one torrent spans several machines -------------
	// Confirmed by reading the tree: /browse/TOSEC/ is 212 PUBLISHER
	// directories and the machine only appears one level further down, so
	// "TOSEC/Bandai" is a torrent holding WonderSwan and everything else
	// Bandai made. Published, findable by name, no facet.
	"TOSEC":     {Kind: minervaRomset, Domain: domainGame},
	"TOSEC-ISO": {Kind: minervaRomset, Domain: domainGame},

	// --- one torrent for the whole collection ------------------------------
	"MAME":                          {Kind: minervaArcade, Domain: domainGame},
	"HBMAME":                        {Kind: minervaArcade, Domain: domainGame},
	"FinalBurn Neo":                 {Kind: minervaArcade, Domain: domainGame},
	"TeknoParrot":                   {Kind: minervaArcade, Domain: domainGame},
	"Eggman's Arcade Repository":    {Kind: minervaArcade, Domain: domainGame},
	"eXo":                           {Kind: minervaDOS, Domain: domainGame},
	"Total DOS Collection":          {Kind: minervaDOS, Domain: domainGame},
	"Hardware Target Game Database": {Kind: minervaRomset, Domain: domainGame},
	"Touhou Project Collection":     {Kind: minervaRomset, Domain: domainGame},
	"Lost Level":                    {Kind: minervaRomset, Domain: domainGame},

	// --- not games ---------------------------------------------------------
	// A verbatim mirror of bitsavers.org: manuals, magazines, databooks and
	// scans of historical computing documentation. 1.64 TB in ONE torrent.
	"bitsavers": {Kind: minervaDocs, Domain: "literature"},
	// Scans and artwork accompanying TOSEC. No executable content at all.
	"TOSEC-PIX": {Kind: minervaMedia, Domain: "image"},
	// Laserdisc rips, plus the arcade laserdisc games under daphne. Mostly A/V.
	"Laserdisc Collection": {Kind: minervaMedia, Domain: "video"},

	// --- published, but nobody can say what one torrent of it is -----------
	// Thirty mirrored archive.org uploader accounts, heavily overlapping the
	// curated sets. Domain is deliberately EMPTY: a grab-bag is not a games
	// shelf, and claiming one would put 40 TB of unclassified material under
	// the word "play". An empty domain is refused by every kind filter, which
	// is the correct answer to "is this a game" when nobody knows.
	"Internet Archive": {Kind: minervaMixed},

	// --- excluded, with no way to switch it on -----------------------------
	// Not dumped published media. Its contents are a CDN archive, prototype
	// builds, gameplay-video leaks and unreleased material -- a materially
	// different posture from indexing a No-Intro set, and one nobody has signed
	// off.
	//
	// There WAS an opt-in here, MINERVA_COLLECTIONS, and it did not work: the
	// importer honoured it while minervaCard and setsForSystem refused
	// minervaExcluded unconditionally, so opting in imported 6.62 TB of rows
	// that no surface could ever render -- and /api/health then reported
	// `optedIn: 1` about a collection that could not produce a single tile.
	// The refusals are the behaviour anybody actually wants, so the switch
	// that contradicted them is gone rather than repaired. Excluded means
	// excluded, in one direction, with nothing to set.
	"Miscellaneous": {Kind: minervaExcluded},
}

// minervaCollectionFor resolves a collection name. The second return is the
// whole point, exactly as vimmSystemFor's is: "this site publishes it as X" and
// "nobody has ever heard of it" are answered very differently, and only the
// second one fails an import.
func minervaCollectionFor(name string) (minervaCollection, bool) {
	c, known := minervaCollections[strings.TrimSpace(name)]
	return c, known
}

// minervaSystemFor resolves the machine for a path inside a facet collection.
//
// Three-valued, and the three values are the same three vimmSystemFor uses:
//
//	slug     this machine, joins the system facet and the system= filter
//	""       Minerva has this machine, this site has no slug for it; the set
//	         is published carrying Minerva's own name for it and is findable
//	         by name, but takes no part in the facet
//	absent   FAILS THE IMPORT, naming every unknown path at once
//
// The key is "Collection/FirstPathComponentBelowIt", which is the level the
// torrent is actually cut at in all four facet collections. See
// minerva_systems.go, which holds the table.
func minervaSystemFor(collection, component string) (string, bool) {
	slug, known := minervaSystems[strings.TrimSpace(collection)+"/"+strings.TrimSpace(component)]
	return slug, known
}

// -------------------------------------------------------------- the entries --

// minervaEntry is one torrent -- which is to say one SET -- reduced to what
// this service publishes.
type minervaEntry struct {
	// InfoHash is the torrent's own identity and this catalogue's primary key.
	// It is content-addressed, so a merge on it is exact and a re-import can
	// never confuse two sets. Lower-case hex, 40 characters.
	InfoHash string `json:"info_hash"`

	// Path is the browse path this hash was found at, with no leading or
	// trailing slash: "No-Intro/Nintendo - Super Nintendo Entertainment System".
	//
	// It is the PRIMARY KEY OF MEANING and it is why the importer walks the
	// browse HTML at all. Neither the dashboard API nor the magnet carries it:
	// every magnet on the site is `dn=Minerva_Myrient`, identical on all 1,049,
	// and the API's `name` field joins path components with " - " while the
	// components themselves contain " - ", so "Minerva_Myrient - Redump - IBM -
	// PC compatible - Q" cannot be parsed back into its three parts.
	Path string `json:"path"`

	// Collection is the first path component, and the key into
	// minervaCollections.
	Collection string `json:"collection"`

	// Name is what a person is shown. Entity-decoded and NOT sanitised: it is
	// display text and "Eggman's Arcade Repository" is the name.
	Name string `json:"name"`

	// System is this site's slug for the machine, or empty. Only ever set from
	// a facet collection; see minervaCollection.Facet.
	System string `json:"system,omitempty"`
	// Platform is Minerva's own name for the machine, shown to a person where
	// there is one -- the path component the slug was resolved from.
	Platform string `json:"platform,omitempty"`

	// SizeBytes is the torrent's real size in bytes, from the dashboard API.
	// An entry without one is NEVER published: "Whole set" with no size beside
	// it is precisely the tile that gets somebody a 6.75 TB surprise.
	SizeBytes int64 `json:"size_bytes"`

	// Files is how many files the torrent holds, or 0 for unknown. It can only
	// ever come from the torrent's own file list -- the site publishes no file
	// count anywhere -- so it is 0 unless the optional titles tier was built.
	Files int `json:"files,omitempty"`

	// MeasuredAt is when Minerva last measured this swarm. Carried so the one
	// honest sentence about swarm health can be said, and deliberately NOT
	// accompanied by the counts themselves. See the file comment.
	MeasuredAt string `json:"measured_at,omitempty"`
}

// minervaCatalogue is the file on disk.
type minervaCatalogue struct {
	Version int    `json:"version"`
	Source  string `json:"source"`

	// Trackers are stored ONCE, here at the head, and composed onto every
	// magnet at load time.
	//
	// They have to be composed at all: every magnet in Minerva's HTML is bare
	// `magnet:?xt=urn:btih:<hash>&dn=Minerva_Myrient` with no tracker at all,
	// and their page appends a 37-tracker string in JavaScript at click time.
	// Handing out the bare form gives a DHT-only magnet, which in a torrent
	// client looks exactly like a dead torrent.
	//
	// Stored once rather than on each of 1,049 rows because it is one fact
	// about the site, it is identical on every row, and repeating it would put
	// roughly 1.4 MB of duplicated tracker URLs into a file whose whole purpose
	// is to be small enough to parse at startup.
	Trackers []string `json:"trackers,omitempty"`

	Generated string `json:"generated_at,omitempty"`
	Imported  string `json:"imported_at,omitempty"`
	// Entries are sorted by info-hash, so re-importing an unchanged snapshot
	// produces a byte-identical file and a diff shows only what really moved.
	Entries []minervaEntry `json:"entries"`
}

const minervaCatalogueVersion = 1

// --------------------------------------------------------------- the store --

// minervaRow is an entry plus what a scan and a card need, computed once at
// load rather than per query.
type minervaRow struct {
	minervaEntry
	// lower is the whole path lower-cased. The path rather than the name,
	// because the collection is part of how people look for these: "no-intro
	// snes" should find the No-Intro SNES set, and the collection name only
	// appears in the path.
	lower string
	// magnet is the finished magnet URI with trackers and a real dn already
	// composed. Built here so a request path never does string work.
	magnet string
}

// minervaStore is the catalogue in memory.
//
// A nil store answers every question with nothing, so a server built by a test
// with a struct literal behaves exactly like a deployment that has never
// imported anything. Every read below tolerates it.
type minervaStore struct {
	path string

	mu        sync.RWMutex
	rows      []minervaRow
	generated string
	imported  string
	trackers  []string
}

// minervaCataloguePath is where the imported catalogue lives. MINERVA_PATH
// overrides; the default is beside library.json and vimm.json in the unit's
// StateDirectory, which under ProtectSystem=strict is the only writable
// directory the service has.
func minervaCataloguePath() string {
	if p := strings.TrimSpace(os.Getenv("MINERVA_PATH")); p != "" {
		return p
	}
	return "/var/lib/mw-search/minerva.json"
}

// loadMinervaStore reads the catalogue file. A missing file is not an error:
// an instance that has never imported is a normal state, and the only
// difference it makes is that this source contributes nothing.
func loadMinervaStore(path string) (*minervaStore, error) {
	s := &minervaStore{path: path}
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
	var cat minervaCatalogue
	if err := json.Unmarshal(raw, &cat); err != nil {
		// Same judgement as the library and the Vimm catalogue: a catalogue
		// that cannot be read is a data problem to look at, not a reason to
		// serve a broken one. main.go turns this into log.Fatalf at startup.
		return nil, fmt.Errorf("minerva catalogue %s is unreadable: %w", path, err)
	}
	s.replace(cat)
	return s, nil
}

// replace swaps in a whole catalogue, composing every magnet exactly once.
func (s *minervaStore) replace(cat minervaCatalogue) {
	trackers := append([]string(nil), cat.Trackers...)
	rows := make([]minervaRow, 0, len(cat.Entries))
	for _, e := range cat.Entries {
		// A row that cannot make a real offer is not a row. Each of these is
		// also refused by the importer; the check is repeated here because a
		// catalogue file can be older than the binary reading it.
		if e.InfoHash == "" || e.Path == "" || e.SizeBytes <= 0 {
			continue
		}
		rows = append(rows, minervaRow{
			minervaEntry: e,
			lower:        strings.ToLower(e.Path),
			magnet:       minervaMagnet(e, trackers),
		})
	}
	s.mu.Lock()
	s.rows = rows
	s.generated = cat.Generated
	s.imported = cat.Imported
	s.trackers = trackers
	s.mu.Unlock()
}

func (s *minervaStore) count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rows)
}

// stats reports what is loaded, for /api/health.
func (s *minervaStore) stats() map[string]any {
	if s == nil {
		return map[string]any{"sets": 0}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]any{"sets": len(s.rows)}
	if s.generated != "" {
		out["generatedAt"] = s.generated
	}
	if s.imported != "" {
		out["importedAt"] = s.imported
	}
	out["trackers"] = len(s.trackers)
	// Stated positively so an operator reading health is not left wondering
	// whether the zeros mean "no peers" or "not measured". They mean the
	// second, always, and this server never learns the first.
	out["peerCounts"] = "not-published"
	// Nothing here about opted-in collections, because there is no longer any
	// such thing. This line USED to report `optedIn: 1` for `Miscellaneous`
	// while minervaCard and setsForSystem refused every one of its rows, so
	// health described a collection that could not produce a single tile as
	// switched on. The switch is gone; see minervaCollections.
	return out
}

// -------------------------------------------------------------- the magnet --

// minervaMagnet composes the finished magnet URI for a set.
//
// Two things are fixed here that the site's own magnet gets wrong for anybody
// downloading it outside their page:
//
//  1. TRACKERS. Theirs carries none; the 37 are appended by JavaScript on
//     click. A bare magnet is DHT-only and presents as a dead torrent.
//  2. dn. Theirs is the literal string "Minerva_Myrient" on all 1,049, so a
//     client that added ten of them would show ten identically-named torrents
//     and write them all into the same directory. The resolved path goes in
//     instead, which is the only identifying text that exists.
func minervaMagnet(e minervaEntry, trackers []string) string {
	var b strings.Builder
	b.WriteString("magnet:?xt=urn:btih:")
	b.WriteString(e.InfoHash)
	b.WriteString("&dn=")
	b.WriteString(url.QueryEscape(minervaTorrentName(e.Path)))
	for _, t := range trackers {
		b.WriteString("&tr=")
		b.WriteString(url.QueryEscape(t))
	}
	return b.String()
}

// minervaTorrentName is the path as a torrent client will show it, and as a
// directory name it can actually create.
//
// The separator matches Minerva's own convention for the .torrent filenames
// ("Minerva_Myrient - Redump - IBM - PC compatible - Q"), so the two agree.
// Then the characters Windows refuses in a filename are replaced -- several
// real path components contain ':' and '?' -- because the failure this
// prevents is a torrent client that cannot create the directory and stalls
// with no explanation.
//
// The DISPLAY name is not put through this. minervaEntry.Name keeps the real
// text, punctuation and all; only the thing that becomes a filename is
// flattened.
func minervaTorrentName(path string) string {
	return minervaSafeFilename(strings.ReplaceAll(path, "/", " - "))
}

// minervaIllegalFilename are the characters Windows will not accept in a file
// or directory name. '/' is deliberately absent: callers replace separators
// before they get here, and a '/' arriving at this point would be a bug worth
// seeing rather than one worth papering over.
const minervaIllegalFilename = "<>:\"\\|?*"

func minervaSafeFilename(s string) string {
	out := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(minervaIllegalFilename, r) {
			return '_'
		}
		return r
	}, s)
	// Windows also refuses a trailing dot or space on a directory name.
	return strings.TrimRight(out, " .")
}

// ---------------------------------------------------------- the URL guard --

// minervaURL returns raw when it is an https URL on minerva-archive.org or a
// subdomain, and "" otherwise.
//
// Same paranoia as vimmURL and for a stronger reason: the input here is
// scraped HTML from a site nobody controls, so an href is whatever that page
// said. Publishing one unchecked would make this catalogue a redirector to
// anywhere. The host test is a suffix ON A LABEL BOUNDARY, never a Contains --
// "minerva-archive.org.evil.example" contains the string and is not the site.
func minervaURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != minervaHost && !strings.HasSuffix(host, "."+minervaHost) {
		return ""
	}
	return u.String()
}

// minervaTorrentURL is the .torrent file for a set.
//
// The filename convention is deterministic and was verified against the live
// site: /assets/Minerva_Myrient_v0.3/Minerva_Myrient - <components joined by
// " - ">.torrent. The directory itself has no index, so this is built rather
// than discovered -- and it goes through minervaURL like everything else.
func minervaTorrentURL(path string) string {
	name := "Minerva_Myrient - " + strings.ReplaceAll(path, "/", " - ") + ".torrent"
	return minervaURL("https://" + minervaHost + "/assets/Minerva_Myrient_v0.3/" +
		url.PathEscape(name))
}

// --------------------------------------------------------------- searching --

// minervaCardLimit is how many set cards one query may contribute.
//
// Much smaller than vimmCardLimit's 120, deliberately. These are not works,
// they are containers, and a person who searches "nintendo" wants games with
// a handful of sets alongside -- not 463 No-Intro directories. The
// per-collection cap below is the half of this that actually bites.
const minervaCardLimit = 40

// minervaPerCollection is the hard ceiling on cards from ONE collection for
// ONE query, and it is the rule that makes this source usable at all.
//
// Without it "nintendo" returns sixty No-Intro rows -- every directory whose
// name contains the word -- and the sets band becomes a directory listing that
// buries the four sets anybody wanted. Three is enough to show that a
// collection has several relevant sets while leaving room for the other twenty.
const minervaPerCollection = 3

// search answers a query from the catalogue.
//
// `systems` is optional: nil means every machine, and a non-empty set narrows
// to those slugs exactly -- which is only ever satisfied by the four facet
// collections, because nothing else carries a slug.
func (s *minervaStore) search(q, kind string, systems map[string]bool, limit int) []card {
	if s == nil {
		return nil
	}
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(q)))
	if len(terms) == 0 {
		// An empty query would match every row, which is not a search result,
		// it is the catalogue. Browsing is minerva_browse.go's job.
		return nil
	}
	if limit <= 0 {
		limit = minervaCardLimit
	}

	// The domain gate, decided ONCE for the whole query instead of once per
	// row. A film search must not return a bitsavers manual torrent and a games
	// search must not return one either -- but `kind` does not change between
	// rows, and neither does a collection's domain, so the answer is a property
	// of the twenty-one collections rather than of the 1,049 rows.
	//
	// Asked per row it was measured at 2.2ms and roughly 14 MB of garbage per
	// search, all of it on the synchronous first-wave path: sameDomain
	// normalises both sides through a strings.Replacer it BUILDS EVERY CALL,
	// which costs 2.1us and 13 KB a time. Twenty-one calls is 44us and the loop
	// below is a map lookup.
	serves := minervaCollectionsServing(kind)

	s.mu.RLock()
	matched := make([]minervaRow, 0, 32)
	for i := range s.rows {
		r := &s.rows[i]
		// Absent from the set means either a collection this binary has never
		// heard of -- a catalogue written by a newer importer, which is refused
		// rather than guessed at -- or one whose domain is not the one asked
		// for.
		if !serves[strings.TrimSpace(r.Collection)] {
			continue
		}
		if systems != nil && !systems[r.System] {
			continue
		}
		if !lowerHasAll(r.lower, terms) {
			continue
		}
		matched = append(matched, *r)
	}
	s.mu.RUnlock()

	// Deterministic, and NOT through rankByMatch.
	//
	// That scorer is built for work titles -- it compares a query against the
	// name of one film or one game and drops anything below matchFloor. A set
	// is named after a container ("Nintendo - Super Nintendo Entertainment
	// System"), so scoring it as a title would rank the SNES set below a
	// directory that happens to repeat a query word, and matchFloor would drop
	// most of the catalogue for most queries. The gate here is instead the
	// same one vimm.go applies BEFORE ranking: every typed word must appear.
	// Among rows that pass it, the shortest path is the most canonical answer
	// -- "No-Intro/Nintendo - Game Boy" beats "No-Intro/Nintendo - Game Boy
	// Advance (e-Reader) (Aftermarket)" for the query "game boy".
	sort.SliceStable(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if len(a.Path) != len(b.Path) {
			return len(a.Path) < len(b.Path)
		}
		return a.Path < b.Path
	})

	perColl := map[string]int{}
	cards := make([]card, 0, limit)
	for i := range matched {
		if len(cards) >= limit {
			break
		}
		if perColl[matched[i].Collection] >= minervaPerCollection {
			continue
		}
		c, ok := minervaCard(matched[i])
		if !ok {
			continue
		}
		perColl[matched[i].Collection]++
		cards = append(cards, c)
	}
	return cards
}

// ---------------------------------------------------------------- the card --

// minervaCollectionsServing names the collections whose domain answers this
// kind. An empty kind asks for no domain at all and so serves every
// collection this binary knows.
//
// Built per query rather than per row, and per query rather than once at init
// because `kind` is whatever a caller sent. Membership doubles as the
// "collection is known" test: a name absent from minervaCollections cannot be
// in here, which is the same refusal minervaCollectionFor gives and for the
// same reason.
func minervaCollectionsServing(kind string) map[string]bool {
	out := make(map[string]bool, len(minervaCollections))
	for name, c := range minervaCollections {
		if kind == "" || sameDomain(kind, c.Domain) {
			out[name] = true
		}
	}
	return out
}
// minervaCard builds the card for one set.
//
// THIS IS THE ONE FORK. Instant, External and Set are mutually exclusive, and
// they are exclusive because exactly one of them is assigned here and the other
// two are never touched. There is no path through this function that produces
// two of them, which is what makes the honesty rule a property of the type
// rather than a thing to remember.
func minervaCard(r minervaRow) (card, bool) {
	coll, known := minervaCollectionFor(r.Collection)
	if !known || coll.Kind == minervaExcluded {
		return card{}, false
	}
	if r.magnet == "" || r.SizeBytes <= 0 {
		// Under this site's rule a tile carries a real target or is not shown,
		// and for a set the size is part of the target: "Whole set" with no
		// number beside it is the tile that gets somebody 6.75 TB by surprise.
		return card{}, false
	}

	sources := []source{minervaMagnetSource(r)}
	if t := minervaTorrentURL(r.Path); t != "" {
		sources = append(sources, minervaTorrentSource(r, t))
	}

	c := card{
		Key:   "minerva:" + r.InfoHash,
		Title: r.Name,
		// The shelf, from the collection map. Empty for `Internet Archive`,
		// which is a grab-bag nobody can classify per torrent -- and an empty
		// Kind is refused by every kind filter, which is the correct answer to
		// "is this a game" when nobody knows.
		Kind: coll.Domain,
		// The place, for the tile's source label. Every source row on this card
		// is labelled the same, but a tile shows no rows.
		Origin:   minervaSiteName,
		Platform: r.Platform,
		System:   r.System,
		Sources:  sources,
		// Seeders stays ZERO and unpublished-as-a-number. See Set.PeersKnown
		// and the file comment: the counts Minerva holds are four months stale.
		Seeders: 0,
		Set: &setInfo{
			Name:       r.Name,
			Collection: r.Collection,
			Path:       r.Path,
			SizeBytes:  r.SizeBytes,
			SizeHuman:  humanSize(r.SizeBytes),
			Files:      r.Files,
			// Always false from this importer. The client is required to print
			// no number rather than a zero, and never the dead-source colour.
			PeersKnown: false,
			MeasuredAt: r.MeasuredAt,
		},
	}
	if coll.Domain == domainGame {
		c.Groups = []string{"games"}
	}
	return c, true
}

// minervaMagnetSource is the row a torrent client is handed.
//
// Not Offsite, and that is a considered answer rather than an omission. Offsite
// means "following this leaves the site and lands on somebody else's page";
// a magnet lands in a torrent client, which is usually not a browser at all.
// The offsite vocabulary -- "leaves this site", "opens minerva-archive.org" --
// would be false in both halves. The client routes this by card.set instead,
// and renders it as an anchor for the reason offsiteSourceRow gives: an anchor
// cannot accidentally be routed into the player, because there is no handler.
func minervaMagnetSource(r minervaRow) source {
	return source{
		Title:     r.Name,
		Indexer:   minervaSiteName,
		Size:      r.SizeBytes,
		SizeHuman: humanSize(r.SizeBytes),
		Magnet:    r.magnet,
		Source:    r.Collection,
		// No Quality, no Codec, no Audio, no Group. Every one of them is
		// torrent-release vocabulary that renders as "unknown" on a set row.
		// Not web-safe: nothing here plays in this site's player, and the
		// webSafe filter dropping these is correct.
		WebSafe: false,
		Action:  "download",
	}
}

// minervaTorrentSource is the .torrent file, for a client that wants one.
func minervaTorrentSource(r minervaRow, target string) source {
	return source{
		Title:     r.Name + ".torrent",
		Indexer:   minervaSiteName,
		Size:      r.SizeBytes,
		SizeHuman: humanSize(r.SizeBytes),
		Magnet:    target,
		Source:    r.Collection,
		WebSafe:   false,
		Action:    "download",
	}
}

// -------------------------------------------------------------- the server --

// minervaDeepen tops a result set up with sets for the machines a filter asked
// for, for the same reason vimmDeepen does: a query contributes at most
// minervaCardLimit cards, and narrowing that slice to one machine afterwards
// returns whichever handful of the top N happened to be that machine.
func (s *server) minervaDeepen(q, kind string, f filters, cards []card) []card {
	if s == nil || s.minerva == nil {
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
	for _, c := range s.minerva.search(q, kind, want, minervaCardLimit) {
		if !have[c.Key] {
			cards = append(cards, c)
		}
	}
	return cards
}
