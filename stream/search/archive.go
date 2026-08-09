package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// archive.org as a search source.
//
// Torrents were the only way to find a game here, and for games that is close
// to useless: ROM torrents are old, thinly seeded, and usually 200-game sets
// rather than the one game somebody searched for. Meanwhile the Internet
// Archive hosts tens of thousands of individually-titled games that are free,
// legal, permanently seeded by an actual institution, and playable in a browser
// through their own emulator.
//
// So a game search asks them too. This costs us nothing at runtime: the search
// is a metadata query, and playback is an iframe pointed at their player, so no
// game byte ever crosses our relay.

// A var rather than a const so a test can point it at a stub. archive.org is
// now the source a person actually waits on, and "does the first paint carry
// its results" is not a question that can be answered honestly by calling the
// real archive.org from a test.
var archiveSearchAPI = "https://archive.org/advancedsearch.php"

// One client for the whole process, with a warm connection pool.
//
// This was a fresh `&http.Client{}` per call, which is a fresh TLS handshake
// per call. Measured against archive.org from the VPS: 187ms of the 310ms
// round trip was the handshake, and the query itself was 120ms. Since
// archive.org is now the source a person actually waits on, that handshake was
// most of the wait -- paid again on every single search, for nothing.
//
// The timeout is per request and generous, because this client is no longer on
// anybody's critical path: the first paint gives up on it after its own budget
// and collects the answer later.
var archiveClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     5 * time.Minute,
		ForceAttemptHTTP2:   true,
	},
}

// warmArchiveConnection opens the TLS connection to archive.org before anybody
// needs it.
//
// The handshake is 187ms of a 310ms round trip, and without this the first
// search after a restart pays all of it -- which is the search a person runs
// immediately after a deploy, and therefore the one they judge it by. The
// warmer's own traffic keeps the pool alive after that; this only covers the
// gap between starting up and the first thing it does.
func warmArchiveConnection() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, archiveSearchAPI, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")
	resp, err := archiveClient.Do(req)
	if err != nil {
		// Not worth reporting. A cold pool costs one search 180ms; a log line
		// about it at every start would say nothing anybody can act on.
		return
	}
	_ = resp.Body.Close()
}

// archiveCacheKey namespaces archive.org results away from merged search
// results in the same map. They must not collide: one is a whole answer, the
// other is one source's contribution to it, and serving the second as the
// first is how a search silently loses its torrents.
func archiveCacheKey(q, kind string) string {
	return "ia\x00" + kind + "\x00" + strings.ToLower(strings.TrimSpace(q))
}

// searchArchiveCached is searchArchive with the result kept.
//
// The point is not to save archive.org the traffic. It is that type-ahead asks
// this same question a few hundred milliseconds before the search does -- so by
// the time somebody presses Enter, the answer is already in memory and the
// first paint carries real results instead of a promise.
//
// Concurrent callers for the same query collapse onto one request: the search
// and the keystroke that triggered it would otherwise both go out.
func (s *server) searchArchiveCached(ctx context.Context, q, kind string) ([]card, error) {
	key := archiveCacheKey(q, kind)
	if cards, ok := s.getCached(key); ok {
		return cards, nil
	}

	wait, leader := s.claim(key)
	if !leader {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if cards, ok := s.getAny(key); ok {
			return cards, nil
		}
		// The leader failed. Falling through and asking again is right: two
		// failures are cheaper than a silent empty result.
	} else {
		defer s.release(key)
	}

	cards, err := s.searchArchive(ctx, q, kind)
	if err != nil {
		return nil, err
	}
	if len(cards) > 0 {
		s.putCached(key, cards)
	}
	return cards, nil
}

// deepenBySystem tops a result set up with games from the machines asked for.
//
// Every other filter narrows the cached results, and for every other filter
// that is right: the cache holds everything the indexers returned. A system
// filter is different in kind, because the cache holds 60 games out of a
// 272,000-item catalogue -- so narrowing "mario" to SNES returns whichever
// handful of the top 60 happened to be SNES, which for a popular title is close
// to none. The fix is to ask archive.org the narrowed question, which is a
// sub-second metadata call and, unlike re-running the search, touches no
// indexer.
//
// The narrowed answer is cached UNDER ITS OWN KEY and never written back into
// the unnarrowed one. That is the trap filter.go's `Lang` comment describes and
// it is not hypothetical: an earlier draft of this narrowed the main archive.org
// fetch, which stored a SNES-only "mario" under the shared (query, kind) key --
// so the next visitor's unfiltered search returned only SNES games and its
// facet claimed SNES was the only machine Mario ever appeared on.
//
// Best effort throughout: a failure leaves the unnarrowed results in place,
// which is the same page the site showed before this existed.
func (s *server) deepenBySystem(ctx context.Context, q, kind string, f filters, cards []card) []card {
	systems := f.systems()
	if len(systems) == 0 {
		return cards
	}

	ids := make([]string, 0, len(systems))
	for _, sys := range systems {
		ids = append(ids, sys.ID)
	}
	sort.Strings(ids)
	key := archiveCacheKey(q, kind) + "\x00sys\x00" + strings.Join(ids, ",")

	extra, ok := s.getCached(key)
	if !ok {
		var err error
		extra, err = s.searchArchiveOn(ctx, q, kind, systems)
		if err != nil {
			log.Printf("system search %q %v: %v", q, ids, err)
			return cards
		}
		if len(extra) > 0 {
			s.putCached(key, extra)
		}
	}

	have := make(map[string]bool, len(cards))
	for _, c := range cards {
		have[c.Key] = true
	}
	for _, c := range extra {
		if !have[c.Key] {
			cards = append(cards, c)
		}
	}
	return cards
}

// What counts as a playable game.
//
// This was a hand-written list of collections, which was both wrong and
// narrow: it was guessed rather than derived, and it missed roughly half the
// catalogue (52 Zelda items against 27, 313 Tetris against 119) because games
// live in far more collections than anyone can enumerate.
//
// An item that declares an `emulator` is exactly an item archive.org will run
// in a browser -- that field is the definition, not a proxy for it. Pairing it
// with mediatype:software keeps out anything that is not a program.
const archiveScope = `emulator:[* TO *] AND mediatype:(software)`

// Scopes for the other kinds. Each is the narrowest query that returns only
// things of that kind, for the same reason archiveScope uses `emulator`: a
// mediatype is a fact archive.org asserts, not a keyword we hope appears.
//
// Without these, `kind=image` and `kind=comic` had no source at all -- they
// searched the games scope, matched nothing of their own, and the results the
// caller saw were whatever the torrent indexers happened to return. A filter
// that changes nothing is worse than one that is absent, because it looks like
// an answer.
var archiveScopes = map[string]string{
	"game":  archiveScope,
	"image": `mediatype:(image)`,
	// Comics live in texts. The collection narrows it to scanned comics rather
	// than the whole book library, which is otherwise almost entirely prose.
	"comic": `mediatype:(texts) AND collection:(comics OR comicbooks)`,
	// Books and audiobooks. Without this entry `kind=literature` had no
	// archive.org source at all, so a search for a book returned EPUB torrents
	// and nothing that could actually be opened -- while the Archive holds
	// Gutenberg's texts and LibriVox's recordings, both free and both readable
	// or playable here.
	//
	// Comics are excluded explicitly. They are also mediatype:(texts) and they
	// dominate a plain text search, so without this a search for a novel comes
	// back full of comic books -- and they already have their own scope above.
	// The collections are named deliberately and the omissions matter more than
	// the inclusions. `internetarchivebooks` and `inlibrary` are far larger --
	// 1,383 and 937 hits for one query against gutenberg's 8 -- but they are
	// controlled digital lending: a reader needs an account and a loan, and most
	// of the time the loan is unavailable. Listing them would fill this domain
	// with results that open onto a waiting list, which is the dead end this
	// whole section exists to avoid. What is here is free to read or hear now.
	//
	// Comics are excluded explicitly: they are also mediatype:(texts), they
	// dominate a plain text search, and they already have their own scope above.
	"literature": `(mediatype:(texts) OR mediatype:(audio)) ` +
		`AND collection:(gutenberg OR librivoxaudio OR americana) ` +
		`AND NOT collection:(comics OR comicbooks)`,
	"video": `mediatype:(movies)`,
}

// scopeFor returns the archive.org scope for a kind, and whether one exists.
// An unknown kind has no scope rather than falling back to games: silently
// searching the wrong catalogue is how "comics" returned emulators.
func scopeFor(kind string) (string, bool) {
	if kind == "" {
		return archiveScope, true
	}
	scope, ok := archiveScopes[kind]
	return scope, ok
}

// Collections archive.org uses for erotica. Their texts carry no Newznab
// category, so without this an adult scan is indistinguishable from any other
// book and reaches a Comics browse untagged.
// isAdultItem is the one answer to "is this adult", shared by the search path
// and the landing shelves.
//
// It was only ever applied on the search path, which was survivable while the
// shelves were games and Gutenberg. It stopped being survivable when the
// shelves gained feature films: their `feature_films` collection is a general
// public-domain library, and its most-downloaded twenty-four included three
// titles nobody wants on an unauthenticated front page.
func isAdultItem(title, identifier string, collections []string) bool {
	if looksAdult(title) || looksAdult(identifier) {
		return true
	}
	for _, col := range collections {
		if looksAdult(col) || adultArchiveCollections[strings.ToLower(strings.TrimSpace(col))] {
			return true
		}
	}
	return false
}

var adultArchiveCollections = map[string]bool{
	"eroticabooks":                true,
	"adultmagazines":              true,
	"tijuanabibles":               true,
	"eroticacomics":               true,
	"vintageerotica":              true,
	"pulpmagazinearchive_erotica": true,
}

// flexString is a Solr field that is USUALLY a string and occasionally a list.
//
// Every field archive.org exposes is multi-valued in the schema, and an item
// that was given two titles comes back as `"title": ["...", "..."]`. Declaring
// it `string` meant encoding/json failed the whole page -- not that one
// document, the entire response -- so a single oddly-catalogued item silently
// emptied a search that had sixty results in it.
//
// Found while resolving the live landing page: four of the twelve batched
// lookups failed this way, and the games shelf lost A Link to the Past and
// Super Mario World to a JSON error that nothing surfaced. `year` was already
// handled this way; the rest were not.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		if len(list) > 0 {
			// The first value, which is the one their own item pages display.
			*f = flexString(list[0])
		}
		return nil
	}
	// A number, a null, an object: not something to fail a page over.
	*f = ""
	return nil
}

func (f flexString) String() string { return string(f) }

// flexStrings is the same tolerance for a field that is usually a list. A
// single-collection item comes back as a bare string.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*f = list
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = []string{s}
		return nil
	}
	*f = nil
	return nil
}

type archiveDoc struct {
	Identifier string          `json:"identifier"`
	Title      flexString      `json:"title"`
	Emulator   flexString      `json:"emulator"`
	Downloads  int             `json:"downloads"`
	Year       json.RawMessage `json:"year"`
	Collection flexStrings     `json:"collection"`
}

type archiveResponse struct {
	Response struct {
		NumFound int          `json:"numFound"`
		Docs     []archiveDoc `json:"docs"`
	} `json:"response"`
}

// archiveQuery builds a Solr query scoped to the emulation collections.
//
// The user's terms USED to go in as a quoted phrase, on the reasoning that
// quoting is what stops a stray `:` or `AND` from becoming Solr syntax. The
// escaping goal was right and is still met -- see match.go, where the terms are
// reduced to bare alphanumeric words and nothing else can survive -- but the
// phrase was catastrophic for matching, because a quoted phrase demands those
// words in that order with nothing between them.
//
// No archive.org ROM title is ever an exact substring of a catalogue title.
// Measured against live archive.org:
//
//	title:("The Legend of Zelda: A Link to the Past")   0 results
//	title:(legend AND zelda AND link AND past)          2 results, both the game
//
// So the terms are now REQUIRED rather than quoted, and the ordering that used
// to be implied by the phrase is done properly afterwards by rankByMatch.
func archiveQuery(q string) string { return archiveQueryFor(q, "") }

func archiveQueryFor(q, kind string) string { return archiveQueryOn(q, kind, nil) }

// archiveQueryOn is the same query, optionally narrowed to particular machines.
//
// The narrowing happens HERE rather than by filtering what came back, and that
// is the whole reason a system filter is worth having: a search returns 60 rows
// out of a 272,000-item catalogue, so filtering "mario" down to SNES afterwards
// returns whichever handful of the top 60 happened to be SNES -- for a popular
// title, close to none. Asking archive.org for SNES Marios returns SNES Marios.
//
// This does NOT contradict filter.go's rule that filtering happens on the
// cached set. The cached set stays unnarrowed; see deepenBySystem in main.go,
// which caches the narrowed answer separately for exactly the reason the `Lang`
// comment gives -- so the first person to use a filter never decides what
// everybody else is shown.
func archiveQueryOn(q, kind string, systems []*gameSystem) string {
	scope, ok := scopeFor(kind)
	if !ok {
		return ""
	}
	if clause := systemsClause(systems); clause != "" {
		scope += " AND " + clause
	}
	// An empty term is a browse rather than a search: everything playable,
	// which the caller then orders by how often it has been downloaded. Without
	// this, `title:("")` is a syntax error and picking a category with no query
	// returns nothing.
	clause := archiveTitleClause(q)
	if clause == "" {
		return scope
	}
	return clause + " AND " + scope
}

// collectionSystems is the fallback, and it exists because of a real failure
// mode: for arcade items the `emulator` field is the MAME *driver* name, not a
// system. "contra", "gberet" and "drgnunit" are all arcade machines, so passing
// an unrecognised id through as a label produced a Contra result whose platform
// read "Contra". The collection an item lives in is coarser but never wrong.
// Ordered most-specific first, because items belong to several collections at
// once and the order they arrive in is arbitrary. An arcade cabinet also sits
// in consolelivingroom, so matching on arrival order labelled it "Console".
var collectionSystems = []struct{ collection, id, name string }{
	{"internetarcade", "arcade", "Arcade"},
	{"softwarelibrary_msdos_games", "dos", "MS-DOS"},
	{"softwarelibrary_msdos", "dos", "MS-DOS"},
	{"softwarelibrary_flash_games", "flash", "Flash"},
	{"softwarelibrary_flash", "flash", "Flash"},
	{"consolelivingroom", "", "Console"},
}

// identifySystem resolves an item to (slug, label).
//
// The label comes from archivePlaySystems, which is the table checked against
// ROM Hub's id census -- not from the hand-written map that used to live here.
// That map had `vice_x64`, an id archive.org uses for nothing, and no id for
// the Amiga that their catalogue actually emits, so the two largest machines in
// the whole catalogue showed no platform at all: 99,993 Commodore 64 items and
// 13,261 Amiga items, every one of them unlabelled.
//
// The slug is what a filter, a facet and a browse URL are keyed on. It is
// stable; the label is for eyes and may be reworded.
func identifySystem(emulator string, collections []string) (string, string) {
	// The site's own catalogue first, because it is the one with families,
	// aliases and a slug -- and its ids are a superset of the play table's for
	// machines that have no core at all (Flash, Palm, Astrocade).
	if s := systemFor(emulator); s != nil {
		label := s.Short
		// Where the play table names the same machine, its label wins: it is
		// the one a playability answer will also use, and two names for one
		// machine on one page is a bug somebody has to notice.
		if p, ok := archivePlaySystems[emulator]; ok && p.Label != "" {
			label = p.Label
		}
		return s.ID, label
	}
	if p, ok := archivePlaySystems[emulator]; ok {
		return p.Platform, p.Label
	}
	for _, cs := range collectionSystems {
		for _, c := range collections {
			if c == cs.collection {
				return cs.id, cs.name
			}
		}
	}
	// Better to say nothing than to label a game with a MAME driver name.
	return "", ""
}

func friendlySystem(emulator string, collections []string) string {
	_, name := identifySystem(emulator, collections)
	return name
}

// ejsCores are the archive.org emulator ids EmulatorJS also has a core for.
//
// This is what decides whether a game can be offered with touch controls. The
// Internet Archive's own player is excellent on a desktop and close to
// unusable on a phone: it expects a keyboard and offers no on-screen pad, so a
// console game there is a screen you can watch and not play. EmulatorJS has a
// virtual gamepad, which for a phone is the difference between a playable game
// and a demo.
//
// So where both can run a title, both are offered -- EmulatorJS first.
var ejsCores = map[string]string{
	"nes": "nes", "famicom": "nes",
	"snes": "snes", "superfamicom": "snes",
	"gameboy": "gb", "gb": "gb", "gbcolor": "gb", "gbc": "gb",
	"gba":      "gba",
	"n64":      "n64",
	"genesis":  "segaMD",
	"megadriv": "segaMD",
	"segaMD":   "segaMD",
	"32x":      "sega32x",
	"sms":      "segaMS", "smsj": "segaMS",
	"gamegear": "segaGG", "gg": "segaGG",
	"psx":    "psx",
	"coleco": "coleco",
	"a2600":  "atari2600",
	"a7800":  "atari7800",
	"lynx":   "lynx",
	"intv2":  "atari2600", // Intellivision has no EmulatorJS core; excluded below
	"tg16":   "pce",
	"wswan":  "ws", "wscolor": "ws",
	"ngpc": "ngp", "ngp": "ngp",
	"vb": "vb",
}

// Intellivision maps to no EmulatorJS core; listing it above would offer a
// player that cannot run it.
func ejsCoreFor(emulator string) string {
	if emulator == "intv2" || emulator == "intv" || emulator == "intvsrs" {
		return ""
	}
	// Answered from the one table that was checked against EmulatorJS's own
	// getCores() and against ROM Hub's plugin, rather than from the list below.
	//
	// The list below was written from names that looked right and contains five
	// ids archive.org does not use -- famicom, superfamicom, segaMD, gg, vb,
	// zero items each -- while missing six real ones worth about 195 games. The
	// difference never showed up as an error: browsing worked, and the failure
	// arrived later as a Play button that did nothing.
	p, ok := archivePlaySystems[emulator]
	if !ok || p.Core == "" {
		return ""
	}
	// A core existing is not the same as it being runnable here. MS-DOS and PSP
	// map to real cores that EmulatorJS publishes only as threaded builds, and
	// threads need a cross-origin-isolated page this site is not; ColecoVision,
	// PlayStation and Amiga need firmware that is not ours to ship. Each of
	// those starts, draws its own error screen and never boots the game -- which
	// looks exactly like it is working.
	if _, blocked := blockedSystems[p.Core]; blocked {
		return ""
	}
	return p.Core
}

// sourcesFor builds the play options for an item.
//
// The EmulatorJS option carries an `#ejs` fragment; the resolver reads that and
// fetches the item's file list at play time rather than here, because finding
// the ROM inside an item costs one metadata request each and doing sixty of
// them per search would make every search slower for a link most people never
// click.
func sourcesFor(d archiveDoc, title, system string) []source {
	details := "https://archive.org/details/" + d.Identifier
	out := make([]source, 0, 2)

	// Flash is the same trade as a console game: their player works, ours has
	// touch support and our own controls. A SWF is about a megabyte, so
	// relaying one costs almost nothing -- much less than a ROM.
	if d.Emulator == "ruffle-swf" || d.Emulator == "flash" {
		out = append(out, source{
			Title:   title,
			Indexer: "Ruffle",
			Magnet:  details + "#swf",
			Source:  system,
			Quality: "TOUCH",
			WebSafe: true,
		})
	}

	if core := ejsCoreFor(d.Emulator.String()); core != "" {
		out = append(out, source{
			Title:   title,
			Indexer: "EmulatorJS",
			Magnet:  details + "#ejs",
			Source:  system,
			Quality: "TOUCH",
			WebSafe: true,
		})
	}

	out = append(out, source{
		Title:   title,
		Indexer: "Archive.org",
		Magnet:  details,
		Source:  system,
		WebSafe: true,
	})
	return out
}

// searchArchive queries archive.org and returns one card per game.
func (s *server) searchArchive(ctx context.Context, q, kind string) ([]card, error) {
	return s.searchArchiveOn(ctx, q, kind, nil)
}

// searchArchiveOn is searchArchive narrowed to particular machines.
func (s *server) searchArchiveOn(ctx context.Context, q, kind string, systems []*gameSystem) ([]card, error) {
	query := archiveQueryOn(q, kind, systems)
	if query == "" {
		return nil, nil
	}
	params := url.Values{}
	params.Set("q", query)
	for _, f := range []string{"identifier", "title", "emulator", "downloads", "year", "collection"} {
		params.Add("fl[]", f)
	}
	// Over-fetch when a machine was asked for. The query is a superset by
	// construction -- archive.org tokenises the emulator field -- so some rows
	// are dropped again below, and asking for exactly 60 would return fewer.
	if len(systems) > 0 {
		params.Set("rows", "120")
	} else {
		params.Set("rows", "60")
	}
	params.Set("page", "1")
	params.Set("output", "json")
	// Their own popularity signal. Downloads is the closest thing they have to
	// a health metric, and it does the same job seeders do for a torrent:
	// separating the canonical upload from the six near-duplicate ones.
	params.Add("sort[]", "downloads desc")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		archiveSearchAPI+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")

	resp, err := archiveClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("archive.org search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive.org search: status %d", resp.StatusCode)
	}

	var out archiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("archive.org search: %w", err)
	}
	// Ordering is not a presentation detail here. archive.org sorts by download
	// count, and download count put an MS-DOS fan hack above the game it hacks:
	// a search for Super Mario World whose first result was "Super Mario World
	// DX". rankByMatch puts the thing that was asked for first and drops what
	// merely shares a word with it -- see match.go.
	return rankByMatch(archiveCards(keepSystems(out.Response.Docs, systems), kind), q), nil
}

// keepSystems drops rows the query matched but the machine did not actually
// have. Solr analyses `emulator`, so a query mentioning `pce` also matches
// `pce-macplus`; without this, a TurboGrafx-16 shelf contains Macintosh disk
// images. Verified against the exact id rather than by re-running the same
// fuzzy match that let them in.
func keepSystems(docs []archiveDoc, systems []*gameSystem) []archiveDoc {
	if len(systems) == 0 {
		return docs
	}
	out := make([]archiveDoc, 0, len(docs))
	for _, d := range docs {
		for _, s := range systems {
			if belongsTo(s, d.Emulator.String(), d.Collection) {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

func archiveCards(docs []archiveDoc, kind string) []card {
	if kind == "" {
		kind = "game"
	}
	cards := make([]card, 0, len(docs))
	seen := make(map[string]bool, len(docs))
	for _, d := range docs {
		if d.Identifier == "" || seen[d.Identifier] {
			continue
		}
		seen[d.Identifier] = true

		systemID, system := identifySystem(d.Emulator.String(), d.Collection)
		title := strings.TrimSpace(d.Title.String())
		if title == "" {
			title = d.Identifier
		}

		// The kind and group must match what was asked for, or the Kind
		// filter discards every card the archive just returned.
		group := map[string]string{
			"game": "games", "image": "images", "comic": "comics",
			"video": "movies",
		}[kind]

		// archive.org results never passed through isAdult, which only ever
		// saw Prowlarr rows -- so nothing from the archive was ever marked
		// adult and erotica surfaced in a plain Comics browse. Their texts
		// carry no Newznab category to key on, so the title and the
		// collections it sits in are the signals available.
		adult := isAdultItem(title, d.Identifier, d.Collection)

		cards = append(cards, card{
			Adult:    adult,
			Key:      "ia:" + d.Identifier,
			Title:    title,
			Year:     archiveYear(d.Year),
			Kind:     kind,
			Instant:  true,
			Seeders:  0,
			Popular:  d.Downloads,
			Platform: system,
			System:   systemID,
			// The "Games" chip filters on this, and a card without it would be
			// hidden the moment somebody narrowed to exactly what they wanted.
			Groups:  []string{group},
			Sources: sourcesFor(d, title, system),
			Art: artwork{
				// Their thumbnail service. An <img> is not subject to CORS, so
				// this loads directly from them and costs us nothing.
				Poster: "https://archive.org/services/img/" + d.Identifier,
				Found:  true,
			},
		})
	}
	// Most-downloaded first, which is their equivalent of best-seeded.
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].Popular > cards[j].Popular })
	return cards
}

// year arrives as a number for some items and a string for others.
func archiveYear(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil && len(str) >= 4 {
		var parsed int
		if _, err := fmt.Sscanf(str[:4], "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}
