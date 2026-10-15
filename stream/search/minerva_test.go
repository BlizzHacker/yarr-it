package main

// A Minerva card must never claim to be a game.
//
// That is the governing sentence of this file, and it is the set-shaped version
// of offsite.test.js's "a tile must not promise something a click cannot
// deliver". Every result here is a container holding thousands of things; the
// one honest offer it can make is "the whole of this, and here is what it
// weighs". Each test below pins one way that offer could quietly become a
// different one.

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ----------------------------------------------------------------- helpers --

func minervaStoreWith(t *testing.T, trackers []string, entries ...minervaEntry) *minervaStore {
	t.Helper()
	s := &minervaStore{path: filepath.Join(t.TempDir(), "minerva.json")}
	s.replace(minervaCatalogue{
		Version:  minervaCatalogueVersion,
		Source:   minervaSiteName,
		Trackers: trackers,
		Entries:  entries,
	})
	return s
}

// snesSet is the worked example the whole feature is designed around: the
// No-Intro Super Nintendo set, one torrent, every cartridge.
func snesSet() minervaEntry {
	return minervaEntry{
		InfoHash:   "0bcb99da1111111111111111111111111111aaaa",
		Path:       "No-Intro/Nintendo - Super Nintendo Entertainment System",
		Collection: "No-Intro",
		Name:       "Nintendo - Super Nintendo Entertainment System",
		System:     "snes",
		Platform:   "Nintendo - Super Nintendo Entertainment System",
		SizeBytes:  8_400_000_000,
		MeasuredAt: "2026-04-14T00:11:35Z",
	}
}

// -------------------------------------------------------------- the maps --

// Every slug either collection map produces must be a machine systems.go
// actually knows. A typo is invisible at runtime -- the set simply never
// appears under any chip -- so it is pinned here rather than found later.
func TestMinervaSystemsMapOntoRealSystems(t *testing.T) {
	for key, slug := range minervaSystems {
		if strings.TrimSpace(key) == "" {
			t.Errorf("empty key in minervaSystems")
		}
		if !strings.Contains(key, "/") {
			t.Errorf("minervaSystems key %q is not Collection/Component", key)
		}
		if slug == "" {
			continue // deliberate: Minerva has it, this site has no slug
		}
		if systemByID[slug] == nil {
			t.Errorf("minerva path %q maps to %q, which is not a system in systems.go",
				key, slug)
		}
	}
}

// Only a collection that is genuinely cut per machine may carry a slug. TOSEC
// is cut per PUBLISHER, so "TOSEC/Bandai" is one torrent spanning every machine
// Bandai made -- and a facet answering "WonderSwan" to it would be the exact
// failure vimm.go refuses when it declines to fold Sega CD into genesis.
func TestOnlyFacetCollectionsAppearInTheSystemMap(t *testing.T) {
	for key := range minervaSystems {
		collection, _, _ := strings.Cut(key, "/")
		coll, known := minervaCollectionFor(collection)
		if !known {
			t.Errorf("minervaSystems names collection %q, which is not in minervaCollections", collection)
			continue
		}
		if !coll.Facet {
			t.Errorf("minervaSystems carries %q, but %q is not cut per machine; "+
				"one of its torrents spans several machines and must take no part in the facet",
				key, collection)
		}
	}
}

// Every collection must say what shelf it is on, or say deliberately that it
// has none. A romset with no domain would be invisible; a docs collection
// routed to `game` would put 1,124 vendor manual shelves in a games search.
func TestEveryCollectionIsClassified(t *testing.T) {
	kinds := map[string]bool{
		minervaRomset: true, minervaArcade: true, minervaDOS: true,
		minervaDocs: true, minervaMedia: true, minervaMixed: true,
		minervaExcluded: true,
	}
	for name, c := range minervaCollections {
		if !kinds[c.Kind] {
			t.Errorf("collection %q has kind %q, which is not one of the seven", name, c.Kind)
		}
		if c.Facet && c.Domain != domainGame {
			t.Errorf("collection %q takes part in the system facet but is not filed under games", name)
		}
		if c.Domain != "" && canonicalDomain(c.Domain) == "" {
			t.Errorf("collection %q is filed under %q, which schema.json does not know", name, c.Domain)
		}
	}
	// The two the research named as not-games must not have drifted onto the
	// games shelf, because that is a one-character change with a large blast
	// radius.
	if minervaCollections["bitsavers"].Domain == domainGame {
		t.Error("bitsavers is a documentation mirror and must not be filed under games")
	}
	if minervaCollections["TOSEC-PIX"].Domain == domainGame {
		t.Error("TOSEC-PIX is scans and artwork and must not be filed under games")
	}
}

// An unknown collection or an unknown machine directory must be answerable as
// "never heard of it", distinctly from "known, no slug".
func TestTheMapsAreThreeValued(t *testing.T) {
	if _, known := minervaCollectionFor("Nintendo Switch Dumps 2027"); known {
		t.Error("an invented collection was reported as known")
	}
	if _, known := minervaSystemFor("No-Intro", "Nintendo - Some Machine From 2027"); known {
		t.Error("an invented directory was reported as known")
	}
	slug, known := minervaSystemFor("No-Intro", "Nintendo - Super Nintendo Entertainment System")
	if !known || slug != "snes" {
		t.Errorf("the No-Intro SNES set resolved to (%q, %v), want (\"snes\", true)", slug, known)
	}
	// Known, and deliberately without a slug: Minerva has the machine, this
	// site does not. Published, findable by name, absent from the facet.
	slug, known = minervaSystemFor("No-Intro", "Nintendo - Virtual Boy")
	if !known {
		t.Error("the Virtual Boy set is in the tree and must be known")
	}
	if slug != "" {
		t.Errorf("Virtual Boy resolved to %q; this site has no slug for it", slug)
	}
}

// ------------------------------------------------- the three exclusive states --

// THE governing invariant. A set is not hosted here and does not open somebody
// else's website; claiming either is the promise this whole source is arranged
// to avoid.
func TestASetNeverClaimsToPlayHereOrToLeaveTheSite(t *testing.T) {
	s := minervaStoreWith(t, []string{"udp://tracker.example:80"}, snesSet(),
		minervaEntry{
			InfoHash: "aaaa99da2222222222222222222222222222bbbb", Path: "bitsavers",
			Collection: "bitsavers", Name: "bitsavers", SizeBytes: 1_640_000_000_000,
		})
	cards := s.search("bitsavers", "", nil, 10)
	cards = append(cards, s.search("super nintendo", "", nil, 10)...)
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}
	for _, c := range cards {
		if c.Set == nil {
			t.Fatalf("%q carries no set", c.Title)
		}
		if c.Instant {
			t.Errorf("%q is marked Instant — nothing here is served over HTTP by anybody", c.Title)
		}
		if c.External != nil {
			t.Errorf("%q is marked External — a magnet does not open a website", c.Title)
		}
		if c.Seeders != 0 {
			t.Errorf("%q publishes a seeder count of %d", c.Title, c.Seeders)
		}
		if c.Set.PeersKnown {
			t.Errorf("%q claims its peers are known; nobody has counted them since April", c.Title)
		}
	}
}

// A set tile shows no source rows, so if the size is not on the card itself it
// is nowhere at all -- and "Whole set" with no number is how somebody queues
// 6.75 TB by accident.
func TestASetAlwaysStatesItsSize(t *testing.T) {
	s := minervaStoreWith(t, nil, snesSet())
	cards := s.search("super nintendo", "", nil, 10)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	set := cards[0].Set
	if set.SizeBytes != 8_400_000_000 {
		t.Errorf("size is %d, want the real one", set.SizeBytes)
	}
	if set.SizeHuman == "" || strings.Contains(set.SizeHuman, "0 B") {
		t.Errorf("human size is %q", set.SizeHuman)
	}
}

// An unsized set is never published. A plausible-looking zero beside "Whole
// set" is worse than no row at all.
func TestAnUnsizedSetIsNeverPublished(t *testing.T) {
	e := snesSet()
	e.SizeBytes = 0
	s := minervaStoreWith(t, nil, e)
	if n := s.count(); n != 0 {
		t.Fatalf("the store kept %d unsized rows", n)
	}
	if cards := s.search("super nintendo", "", nil, 10); len(cards) != 0 {
		t.Errorf("an unsized set produced %d cards", len(cards))
	}
}

// A magnet-less browse entry has no info-hash, so it can produce no tile
// anywhere. TOSEC/Commodore/C64's 24.55 GB subtree is the real instance.
func TestAMagnetlessEntryProducesNoTile(t *testing.T) {
	e := snesSet()
	e.InfoHash = ""
	s := minervaStoreWith(t, nil, e)
	if n := s.count(); n != 0 {
		t.Fatalf("the store kept %d hash-less rows", n)
	}
}

// ----------------------------------------------------------- the domains --

// docs, media and mixed collections must not produce a game tile. bitsavers is
// a manual mirror, TOSEC-PIX is artwork, Internet Archive is a grab-bag nobody
// can classify one torrent of.
func TestDocsMediaAndMixedProduceNoGameTile(t *testing.T) {
	s := minervaStoreWith(t, nil,
		minervaEntry{InfoHash: "1111111111111111111111111111111111111111",
			Path: "bitsavers", Collection: "bitsavers", Name: "bitsavers", SizeBytes: 1_640_000_000_000},
		minervaEntry{InfoHash: "2222222222222222222222222222222222222222",
			Path: "TOSEC-PIX/Atari", Collection: "TOSEC-PIX", Name: "Atari", SizeBytes: 2_250_000_000_000},
		minervaEntry{InfoHash: "3333333333333333333333333333333333333333",
			Path: "Internet Archive/aitus95", Collection: "Internet Archive",
			Name: "aitus95", SizeBytes: 15_570_000_000_000},
	)
	for _, q := range []string{"bitsavers", "atari", "aitus95"} {
		if cards := s.search(q, domainGame, nil, 10); len(cards) != 0 {
			t.Errorf("a games search for %q returned %d non-game sets", q, len(cards))
		}
	}
	// And they are still findable on their own shelves, or routing them there
	// achieved nothing.
	if cards := s.search("bitsavers", "literature", nil, 10); len(cards) != 1 {
		t.Errorf("bitsavers is not findable under literature: %d cards", len(cards))
	}
	if cards := s.search("atari", "image", nil, 10); len(cards) != 1 {
		t.Errorf("TOSEC-PIX is not findable under image: %d cards", len(cards))
	}
}

// An excluded collection is never published, however it is searched for.
func TestAnExcludedCollectionIsNeverPublished(t *testing.T) {
	s := minervaStoreWith(t, nil, minervaEntry{
		InfoHash: "4444444444444444444444444444444444444444", Path: "Miscellaneous",
		Collection: "Miscellaneous", Name: "Miscellaneous", SizeBytes: 6_620_000_000_000,
	})
	for _, kind := range []string{"", domainGame, "video"} {
		if cards := s.search("miscellaneous", kind, nil, 10); len(cards) != 0 {
			t.Errorf("kind %q returned %d cards from an excluded collection", kind, len(cards))
		}
	}
}

// ---------------------------------------------------------------- magnets --

// Every emitted magnet must carry the trackers and a dn that identifies it.
// Theirs carries neither: a bare magnet is DHT-only and presents as dead, and
// dn=Minerva_Myrient on all 1,049 makes a client show 1,049 identical torrents.
func TestEveryMagnetCarriesTrackersAndARealName(t *testing.T) {
	trackers := []string{"udp://tracker.one.example:80/announce", "udp://tracker.two.example:6969"}
	s := minervaStoreWith(t, trackers, snesSet())
	cards := s.search("super nintendo", "", nil, 10)
	if len(cards) != 1 || len(cards[0].Sources) == 0 {
		t.Fatalf("no sources built")
	}
	magnet := cards[0].Sources[0].Magnet
	if !strings.HasPrefix(magnet, "magnet:?xt=urn:btih:0bcb99da") {
		t.Fatalf("magnet does not start with the info-hash: %q", magnet)
	}
	for _, tr := range trackers {
		if !strings.Contains(magnet, url.QueryEscape(tr)) {
			t.Errorf("magnet is missing tracker %q: %q", tr, magnet)
		}
	}
	u, err := url.Parse(magnet)
	if err != nil {
		t.Fatalf("magnet does not parse: %v", err)
	}
	dn := u.Query().Get("dn")
	if dn == "Minerva_Myrient" || dn == "" {
		t.Fatalf("dn is %q; it must identify the set", dn)
	}
	if !strings.Contains(dn, "Super Nintendo") {
		t.Errorf("dn %q does not name the set", dn)
	}
}

// A name that becomes a directory must not contain characters Windows refuses,
// or the torrent client stalls with no explanation. The DISPLAY name keeps its
// real punctuation.
func TestTorrentNamesAreSafeButDisplayNamesAreNot(t *testing.T) {
	got := minervaTorrentName(`Redump/Sega - Dreamcast: "GD-ROM" <beta>|?*`)
	for _, r := range minervaIllegalFilename {
		if strings.ContainsRune(got, r) {
			t.Errorf("torrent name %q still contains the illegal character %q", got, string(r))
		}
	}
	if strings.Contains(got, "/") {
		t.Errorf("torrent name %q still contains a path separator", got)
	}
	if !strings.Contains(got, "Redump - Sega - Dreamcast") {
		t.Errorf("torrent name %q lost the path", got)
	}
	// The card's own title is display text and keeps its apostrophe.
	s := minervaStoreWith(t, nil, minervaEntry{
		InfoHash: "5555555555555555555555555555555555555555",
		Path:     "Eggman's Arcade Repository", Collection: "Eggman's Arcade Repository",
		Name: "Eggman's Arcade Repository", SizeBytes: 6_750_000_000_000,
	})
	cards := s.search("eggman", "", nil, 10)
	if len(cards) != 1 {
		t.Fatalf("got %d cards", len(cards))
	}
	if !strings.Contains(cards[0].Title, "Eggman's") {
		t.Errorf("the display title lost its apostrophe: %q", cards[0].Title)
	}
}

// ------------------------------------------------------------ the URL guard --

// The allowlist is a suffix on a label boundary, never a Contains, and https
// only. The input is scraped HTML from a site nobody here controls.
func TestTheURLAllowlistRefusesLookalikes(t *testing.T) {
	ok := []string{
		"https://minerva-archive.org/assets/x.torrent",
		"https://cdn.minerva-archive.org/assets/x.torrent",
	}
	bad := []string{
		"http://minerva-archive.org/assets/x.torrent", // not https
		"https://minerva-archive.org.evil.example/x",  // Contains would pass this
		"https://evil.example/?u=minerva-archive.org", // and this
		"https://notminerva-archive.org/x",            // not a label boundary
		"javascript:alert(1)//minerva-archive.org",
		"",
	}
	for _, u := range ok {
		if minervaURL(u) == "" {
			t.Errorf("a real Minerva URL was refused: %q", u)
		}
	}
	for _, u := range bad {
		if got := minervaURL(u); got != "" {
			t.Errorf("a lookalike was accepted: %q -> %q", u, got)
		}
	}
}

// The .torrent address is built rather than discovered, so it must land on the
// site and nowhere else, whatever the path holds.
func TestTheTorrentURLStaysOnTheSite(t *testing.T) {
	got := minervaTorrentURL("Redump/IBM - PC compatible/Q")
	if got == "" {
		t.Fatal("a real path produced no .torrent URL")
	}
	if !strings.HasPrefix(got, "https://"+minervaHost+"/assets/") {
		t.Errorf("the .torrent URL left the site: %q", got)
	}
	if !strings.Contains(got, ".torrent") {
		t.Errorf("the .torrent URL is not one: %q", got)
	}
	// A path that tries to climb must not produce an address outside /assets/.
	if got := minervaTorrentURL("../../etc/passwd"); strings.Contains(got, "/etc/passwd") {
		t.Errorf("a climbing path escaped the assets directory: %q", got)
	}
}

// ------------------------------------------------------------------ limits --

// "nintendo" must not return sixty No-Intro directories. Without the
// per-collection cap the band becomes a directory listing.
func TestOneCollectionCannotFloodAQuery(t *testing.T) {
	entries := make([]minervaEntry, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, minervaEntry{
			InfoHash:   strings.Repeat(string(rune('a'+i%26)), 40),
			Path:       "No-Intro/Nintendo - Machine " + string(rune('A'+i)),
			Collection: "No-Intro",
			Name:       "Nintendo - Machine " + string(rune('A'+i)),
			SizeBytes:  1 << 30,
		})
	}
	s := minervaStoreWith(t, nil, entries...)
	cards := s.search("nintendo", "", nil, 40)
	if len(cards) > minervaPerCollection {
		t.Errorf("one collection contributed %d cards, cap is %d", len(cards), minervaPerCollection)
	}
}

// The shortest path is the most canonical answer: a query for "game boy" wants
// the Game Boy set, not "Game Boy Advance (e-Reader) (Aftermarket)".
func TestTheMostCanonicalSetLeads(t *testing.T) {
	s := minervaStoreWith(t, nil,
		minervaEntry{InfoHash: strings.Repeat("b", 40),
			Path:       "No-Intro/Nintendo - Game Boy Advance (e-Reader) (Aftermarket)",
			Collection: "No-Intro", Name: "Nintendo - Game Boy Advance (e-Reader) (Aftermarket)",
			SizeBytes: 1 << 30},
		minervaEntry{InfoHash: strings.Repeat("a", 40),
			Path:       "No-Intro/Nintendo - Game Boy",
			Collection: "No-Intro", Name: "Nintendo - Game Boy", SizeBytes: 1 << 30},
	)
	cards := s.search("game boy", "", nil, 10)
	if len(cards) == 0 {
		t.Fatal("no cards")
	}
	if cards[0].Title != "Nintendo - Game Boy" {
		t.Errorf("first card is %q, want the plain Game Boy set", cards[0].Title)
	}
}

// With no per-file index there is nothing honest to say about what a set holds,
// so a set must surface only when a query names the SET, the collection or the
// machine -- never when it names a game inside one.
//
// This is the whole of the "absent index" behaviour, and it is a property of
// matching on the path rather than a switch anybody has to remember to set.
// The failure it prevents is a search for "super mario world" answering with an
// 8.4 GB torrent on the grounds that the game is probably in there somewhere.
func TestASetIsNotFoundByTheNameOfAGameInsideIt(t *testing.T) {
	s := minervaStoreWith(t, nil, snesSet())
	for _, title := range []string{"super mario world", "zelda", "chrono trigger", "f-zero"} {
		if cards := s.search(title, domainGame, nil, 10); len(cards) != 0 {
			t.Errorf("searching for the game %q returned %d sets; with no file list "+
				"nobody knows whether it is in one", title, len(cards))
		}
	}
	// And the set is still findable by what it actually is.
	for _, q := range []string{"no-intro", "super nintendo", "no-intro nintendo"} {
		if cards := s.search(q, domainGame, nil, 10); len(cards) != 1 {
			t.Errorf("searching for %q returned %d sets, want 1", q, len(cards))
		}
	}
	// Nothing may claim to know its contents.
	cards := s.search("no-intro", domainGame, nil, 10)
	if cards[0].Set.Contains != nil {
		t.Error("a set claimed to know which file matched, with no file list to know it from")
	}
	for _, src := range cards[0].Sources {
		if src.Within != "" {
			t.Errorf("a source claimed an in-set path %q, which can only come from a file list",
				src.Within)
		}
	}
}

// An empty query is a browse, not a search: it must not return the catalogue.
func TestAnEmptyQueryReturnsNothing(t *testing.T) {
	s := minervaStoreWith(t, nil, snesSet())
	for _, q := range []string{"", "   "} {
		if cards := s.search(q, "", nil, 10); len(cards) != 0 {
			t.Errorf("query %q returned %d cards", q, len(cards))
		}
	}
}

// ------------------------------------------------------------- the store --

// A nil store answers every question with nothing, so a server built by a test
// with a struct literal behaves like one that has never imported.
func TestANilStoreAnswersNothing(t *testing.T) {
	var s *minervaStore
	if s.count() != 0 {
		t.Error("a nil store counted rows")
	}
	if cards := s.search("mario", "", nil, 10); cards != nil {
		t.Error("a nil store returned cards")
	}
	if st := s.stats(); st["sets"] != 0 {
		t.Errorf("a nil store reported %v sets", st["sets"])
	}
	if items := s.setsForSystem("snes"); items != nil {
		t.Error("a nil store returned browse items")
	}
}

// A missing catalogue is a normal state. A CORRUPT one is a data problem to
// look at, and must never be silently replaced by an empty catalogue.
func TestACorruptSetCatalogueIsNotSilentlyReplaced(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nothing-here.json")
	s, err := loadMinervaStore(missing)
	if err != nil || s == nil || s.count() != 0 {
		t.Fatalf("a missing catalogue was treated as an error: %v", err)
	}

	corrupt := filepath.Join(dir, "minerva.json")
	if err := os.WriteFile(corrupt, []byte(`{"entries": [ this is not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMinervaStore(corrupt); err == nil {
		t.Fatal("a corrupt catalogue loaded without complaint")
	}
}

// Health must never publish a peer count, and must say so positively rather
// than leaving a reader to guess what the absent numbers mean.
func TestHealthNeverPublishesPeerCounts(t *testing.T) {
	s := minervaStoreWith(t, []string{"udp://t.example:80"}, snesSet())
	st := s.stats()
	if st["sets"] != 1 {
		t.Errorf("sets = %v", st["sets"])
	}
	if st["peerCounts"] != "not-published" {
		t.Errorf("health does not state that peer counts are withheld: %v", st)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"seeders", "leechers"} {
		if strings.Contains(string(raw), word) {
			t.Errorf("health mentions %q: %s", word, raw)
		}
	}
}

// The catalogue must reach the first paint, which is what running it
// synchronously at the top of the job buys. A stage that reported
// "not-configured" when a catalogue is loaded would be a source that never ran.
func TestTheSetCatalogueLandsInTheFirstWave(t *testing.T) {
	s := &server{minerva: minervaStoreWith(t, nil, snesSet())}
	j := newSearchJob("super nintendo", domainGame)
	j.minervaStage(s)

	snap := j.snapshot()
	if len(snap.cards) != 1 {
		t.Fatalf("the stage contributed %d cards, want 1", len(snap.cards))
	}
	if snap.sources["minerva"] != stageOK {
		t.Errorf("stage reported %q, want %q", snap.sources["minerva"], stageOK)
	}
}

// An instance with no catalogue reports the state whose fix is running the
// importer -- never "failed", which would mean something broke.
func TestAnEmptyCatalogueIsNotConfiguredRatherThanFailed(t *testing.T) {
	j := newSearchJob("mario", domainGame)
	j.minervaStage(&server{})
	if got := j.snapshot().sources["minerva"]; got != stageNotConfigured {
		t.Errorf("stage reported %q, want %q", got, stageNotConfigured)
	}
}

// A domain no collection is filed under is "none" -- it did not apply -- rather
// than "ok", which would misdescribe an empty music search as a source that
// looked and found nothing.
func TestAKindNoCollectionServesIsNoneRatherThanOK(t *testing.T) {
	j := newSearchJob("miles davis", domainMusic)
	j.minervaStage(&server{minerva: minervaStoreWith(t, nil, snesSet())})
	if got := j.snapshot().sources["minerva"]; got != stageNone {
		t.Errorf("a music search reported %q, want %q", got, stageNone)
	}
}

// ------------------------------------------------------------- the filters --

// The seeder floor must not delete every set. Their Seeders is 0 because it is
// UNKNOWN, and the default floor is 1.
func TestTheSeederFloorDoesNotDeleteSets(t *testing.T) {
	s := minervaStoreWith(t, nil, snesSet())
	cards := s.search("super nintendo", "", nil, 10)
	if len(cards) != 1 {
		t.Fatalf("no cards to filter")
	}
	got := (filters{MinSeeders: 1, Sort: "relevance"}).apply(cards)
	if len(got) != 1 {
		t.Fatalf("the default seeder floor deleted the set")
	}
	if len(got[0].Sources) == 0 {
		t.Error("the seeder floor deleted every source on the set")
	}
	// But the moment somebody measures the swarm, the floor applies like any
	// other torrent's -- no change to filter.go required.
	measured := cards
	measured[0].Set.PeersKnown = true
	if got := (filters{MinSeeders: 1, Sort: "relevance"}).apply(measured); len(got) != 0 {
		t.Error("a measured swarm with no seeders survived the floor")
	}
}

// Size stays filterable. It is the one number a set really publishes and the
// most useful filter this source has.
func TestASetsSizeStaysFilterable(t *testing.T) {
	s := minervaStoreWith(t, nil, snesSet()) // 8.4 GB
	cards := s.search("super nintendo", "", nil, 10)
	if got := (filters{MaxSizeMB: 100, Sort: "relevance"}).apply(cards); len(got) != 0 {
		t.Error("an 8.4 GB set survived a 100 MB ceiling")
	}
	if got := (filters{MinSizeMB: 100, Sort: "relevance"}).apply(cards); len(got) != 1 {
		t.Error("an 8.4 GB set was dropped by a 100 MB floor")
	}
}

// The chips must agree with the badges: a set is a swarm and belongs to that
// chip, is not hosted and so is excluded from "instant", and has its own.
func TestTheSourceChipsAgreeWithWhatASetIs(t *testing.T) {
	s := minervaStoreWith(t, nil, snesSet())
	cards := s.search("super nintendo", "", nil, 10)

	if got := (filters{Source: "swarm", Sort: "relevance"}).apply(cards); len(got) != 1 {
		t.Error("the swarm chip hid a torrent, which is the one thing it must not do")
	}
	if got := (filters{Source: "instant", Sort: "relevance"}).apply(cards); len(got) != 0 {
		t.Error("a set appeared under the chip that means hosted-and-always-up")
	}
	if got := (filters{Source: "sets", Sort: "relevance"}).apply(cards); len(got) != 1 {
		t.Error("the sets chip did not keep a set")
	}
	// And the sets chip keeps nothing else.
	other := []card{{Key: "x", Title: "A Film", Kind: "video",
		Sources: []source{{Title: "x", Seeders: 5}}, Seeders: 5}}
	if got := (filters{Source: "sets", Sort: "relevance"}).apply(other); len(got) != 0 {
		t.Error("the sets chip kept something that is not a set")
	}
}

// ------------------------------------------------------------ the response --

// Sets must never be in `cards`, and must never be counted in `total` or in
// the facets -- those are promises about the grid, and a set is not in it.
func TestSetsArePartitionedOutOfTheGrid(t *testing.T) {
	s := minervaStoreWith(t, nil, snesSet())
	sets := s.search("super nintendo", "", nil, 10)
	work := card{Key: "ia:sm", Title: "Super Mario World", Kind: domainGame,
		System: "snes", Instant: true, Origin: "archive.org",
		Sources: []source{{Title: "Super Mario World", WebSafe: true}}}

	w := httptest.NewRecorder()
	respondSearch(w, "super nintendo", filters{Sort: "relevance"}, nil,
		append([]card{work}, sets...), searchState{cache: "HIT"}, false)

	var body struct {
		Cards []card `json:"cards"`
		Sets  []card `json:"sets"`
		Total int    `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response does not parse: %v", err)
	}
	if len(body.Sets) != 1 {
		t.Fatalf("sets = %d, want 1", len(body.Sets))
	}
	if len(body.Cards) != 1 {
		t.Fatalf("cards = %d, want only the work", len(body.Cards))
	}
	for _, c := range body.Cards {
		if c.Set != nil {
			t.Errorf("a set leaked into cards: %q", c.Title)
		}
	}
	// total counts the grid, which the set is not part of.
	if body.Total != 1 {
		t.Errorf("total = %d, want 1 — a set must not be counted as a result", body.Total)
	}
}

// A set-top box has no torrent client and no way to use a magnet, so it must
// never be offered one.
//
// This falls out of the device profile rather than needing a rule of its own --
// a set source names no container and no codec, and a game needs an
// interactive device -- but it is pinned because it is the exact promise this
// source could most easily make by accident on the client least able to keep
// it. The Roku profile is the one that exists; the property is about the class.
func TestASetTopBoxIsNeverOfferedAMagnet(t *testing.T) {
	s := minervaStoreWith(t, nil, snesSet())
	sets := s.search("super nintendo", "", nil, 10)
	if len(sets) != 1 {
		t.Fatalf("no set to filter")
	}

	dev := deviceProfileFor("roku")
	if dev == nil {
		t.Skip("no set-top profile on this build")
	}
	w := httptest.NewRecorder()
	respondSearch(w, "super nintendo", filters{Sort: "relevance"}, dev, sets,
		searchState{cache: "HIT"}, false)

	var body struct {
		Cards []card `json:"cards"`
		Sets  []card `json:"sets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sets) != 0 || len(body.Cards) != 0 {
		t.Errorf("a set-top box was offered %d sets and %d cards it cannot open",
			len(body.Sets), len(body.Cards))
	}
}

// partitionSets is the one function that separates them, and it must preserve
// order in both halves or the ranking above it means nothing.
func TestPartitionSetsPreservesOrder(t *testing.T) {
	in := []card{
		{Key: "a"}, {Key: "s1", Set: &setInfo{Name: "s1"}}, {Key: "b"},
		{Key: "s2", Set: &setInfo{Name: "s2"}}, {Key: "c"},
	}
	works, sets := partitionSets(in)
	if got := keysJoined(works); got != "a,b,c" {
		t.Errorf("works = %s", got)
	}
	if got := keysJoined(sets); got != "s1,s2" {
		t.Errorf("sets = %s", got)
	}
}

func keysJoined(cards []card) string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.Key)
	}
	return strings.Join(out, ",")
}
