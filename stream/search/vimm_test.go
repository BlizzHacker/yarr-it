package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- the map --

// Every platform in the export must be in the map, and every slug in the map
// must be a machine systems.go actually knows. A typo in either direction is
// invisible at runtime -- entries simply never appear under a chip -- so it is
// pinned here.
func TestVimmPlatformsMapOntoRealSystems(t *testing.T) {
	for platform, slug := range vimmSystems {
		if strings.TrimSpace(platform) == "" {
			t.Errorf("empty platform key in vimmSystems")
		}
		if slug == "" {
			continue // deliberate: Vimm has it, this site has no slug
		}
		if systemByID[slug] == nil {
			t.Errorf("Vimm platform %q maps to %q, which is not a system in systems.go",
				platform, slug)
		}
	}
}

// An unknown platform must stop the import and name itself. Dropping it
// silently is how a thousand new Dreamcast entries would arrive and never
// appear, with nothing anywhere saying why.
func TestAnUnknownPlatformFailsTheImportLoudly(t *testing.T) {
	dir := t.TempDir()
	export := writeExport(t, dir, `{"items":[
		{"vault_id":"1","platform":"Bandai Pippin","filename":"Racer (USA).bin",
		 "page_url":"https://vimm.net/vault/1","download_url":"https://dl3.vimm.net/?mediaId=1",
		 "downloadable":true}
	]}`)

	rep, err := importVimm(export, filepath.Join(dir, "vimm.json"))
	if err == nil {
		t.Fatal("an unknown platform imported without complaint")
	}
	if !strings.Contains(err.Error(), "Bandai Pippin") {
		t.Errorf("the error does not name the platform: %v", err)
	}
	if rep == nil || rep.UnknownPlatforms["Bandai Pippin"] != 1 {
		t.Errorf("the report does not count the unknown platform: %+v", rep)
	}
	// And nothing may have been written.
	if _, err := os.Stat(filepath.Join(dir, "vimm.json")); !os.IsNotExist(err) {
		t.Error("a failed import wrote a catalogue anyway")
	}
}

// ------------------------------------------------------------- the titles --

func TestTitlesAreRecoveredFromFilenames(t *testing.T) {
	cases := []struct{ file, want string }{
		{"10-Yard Fight (USA, Europe).nes", "10-Yard Fight"},
		{"1943 - The Battle of Midway (USA).nes", "1943 - The Battle of Midway"},
		{"Last Story, The (USA) (En,Fr,Es).iso", "The Last Story"},
		{"Addams Family, The - Pugsley's Scavenger Hunt (USA).nes",
			"The Addams Family - Pugsley's Scavenger Hunt"},
		{"Final Fantasy VII (USA) (Disc 1)", "Final Fantasy VII"},
		{"Chrono Cross (USA, Canada) (Disc 1)", "Chrono Cross"},
		{"[BIOS] 32X M68000 (USA).bin", "32X M68000"},
		{"Sonic & Knuckles (World)", "Sonic & Knuckles"},
		{"Elder Scrolls III, The - Morrowind - Game of the Year Edition (USA).iso",
			"The Elder Scrolls III - Morrowind - Game of the Year Edition"},
		// No extension at all: every disc platform in the export is like this.
		{"Diablo (USA) (En,Fr,De,Sv)", "Diablo"},
	}
	for _, c := range cases {
		if got := vimmTitleFromFile(c.file); got != c.want {
			t.Errorf("vimmTitleFromFile(%q) = %q, want %q", c.file, got, c.want)
		}
	}
}

// The trailing-noise strip decompose does is right for archive.org uploads and
// destructive here. This is the case that proves it: `isSuffixNoise` holds
// "wii", so a rule that applied it would sell "Mario Kart Wii" as "Mario Kart".
func TestAMachineInTheNameSurvives(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{"Mario Kart Wii (USA).iso", "Mario Kart Wii"},
		{"New Super Mario Bros. Wii (USA) (En,Fr,Es).iso", "New Super Mario Bros. Wii"},
		{"[BIOS] Atari Jaguar (World).j64", "Atari Jaguar"},
		{"Wii Sports (USA).iso", "Wii Sports"},
	} {
		if got := vimmTitleFromFile(c.file); got != c.want {
			t.Errorf("vimmTitleFromFile(%q) = %q, want %q", c.file, got, c.want)
		}
	}
}

// Only known ROM and disc suffixes come off. A title whose last word contains a
// dot is far commoner in this catalogue than an unknown extension.
func TestOnlyKnownExtensionsAreStripped(t *testing.T) {
	if got := vimmTitleFromFile("R.C. Pro-Am (USA).nes"); got != "R.C. Pro-Am" {
		t.Errorf("got %q", got)
	}
	if got, unknown := stripVimmExtension("Dot Hack Part 1 - Infection"); unknown || got != "Dot Hack Part 1 - Infection" {
		t.Errorf("got %q unknown=%v", got, unknown)
	}
	if _, unknown := stripVimmExtension("Something (USA).qqq"); !unknown {
		t.Error("an unrecognised dotted suffix was not reported")
	}
}

// The extension build that could not find a game name returned page furniture
// for all 4,470 visited rows. None of it may ever reach a card.
func TestPageFurnitureIsNeverATitle(t *testing.T) {
	for _, noise := range []string{
		"Upload it to The Vault", "upload it to the vault", "  The Vault  ",
		"Vimm's Lair", "Home", "Login", "Register", "Donate",
	} {
		if !isVimmNoiseTitle(noise) {
			t.Errorf("%q was not recognised as page furniture", noise)
		}
		title, from := vimmTitle(noise, "Contra (USA).nes")
		if title != "Contra" || from != "file" {
			t.Errorf("with title %q the entry became %q (%s); want the filename", noise, title, from)
		}
	}
}

// A real page title is better information than a filename -- it keeps the
// colon, the accents and the casing -- so it wins wherever both exist.
func TestARealPageTitleBeatsTheFilename(t *testing.T) {
	title, from := vimmTitle("Sonic the Hedgehog 3: Angel Island", "Sonic 3 (USA).md")
	if title != "Sonic the Hedgehog 3: Angel Island" || from != "page" {
		t.Fatalf("got %q from %q", title, from)
	}
	// And with neither, there is nothing to publish.
	if title, from := vimmTitle("The Vault", ""); title != "" || from != "" {
		t.Errorf("got %q from %q; want nothing", title, from)
	}
}

// ---------------------------------------------------------------- the URLs --

// The export comes from a browser extension, which believed whatever the page
// said. Publishing those URLs unchecked would make this catalogue a redirector.
func TestOnlyVimmURLsArePublished(t *testing.T) {
	good := []string{
		"https://vimm.net/vault/3",
		"https://dl3.vimm.net/?mediaId=3",
		"https://VIMM.NET/vault/?p=play&mediaId=3",
	}
	for _, u := range good {
		if vimmURL(u) == "" {
			t.Errorf("%q was rejected", u)
		}
	}
	bad := []string{
		"http://vimm.net/vault/3",          // not https
		"https://evil.example/vault/3",     // another host
		"https://notvimm.net/vault/3",      // suffix without a label boundary
		"https://vimm.net.evil.com/vault",  // host is evil.com
		"javascript:alert(1)",              // not a URL we would ever follow
		"//dl3.vimm.net/?mediaId=3",        // no scheme
	}
	for _, u := range bad {
		if got := vimmURL(u); got != "" {
			t.Errorf("%q was accepted as %q", u, got)
		}
	}
}

// -------------------------------------------------------------- the cards --

func TestAVimmCardNeverClaimsToPlayHere(t *testing.T) {
	store := storeWith(t,
		vimmEntry{VaultID: "3", Title: "10-Yard Fight", Platform: "Nintendo", System: "nes",
			Page: "https://vimm.net/vault/3",
			Play: "https://vimm.net/vault/?p=play&mediaId=3",
			Download: "https://dl3.vimm.net/?mediaId=3", Size: 16384},
	)
	cards := store.search("10-Yard Fight", "game", nil, 10)
	if len(cards) != 1 {
		t.Fatalf("got %d cards", len(cards))
	}
	c := cards[0]

	if c.Instant {
		t.Error("Instant is set; that flag means this site hosts and plays it, and it does not")
	}
	if c.External == nil {
		t.Fatal("no External block; nothing tells a client this leaves the site")
	}
	if c.External.Host != "vimm.net" || c.External.Name == "" || c.External.Short == "" {
		t.Errorf("External is incomplete: %+v", c.External)
	}
	if c.System != "nes" || c.Platform != "Nintendo" {
		t.Errorf("machine lost: system=%q platform=%q", c.System, c.Platform)
	}
	for _, s := range c.Sources {
		if !s.Offsite {
			t.Errorf("source %q is not marked offsite", s.Title)
		}
		if s.WebSafe {
			t.Errorf("source %q claims to be web-safe; nothing here plays in our player", s.Title)
		}
		if !strings.HasPrefix(s.Magnet, "https://") || !strings.Contains(s.Magnet, "vimm.net") {
			t.Errorf("source target %q is not a vimm.net URL", s.Magnet)
		}
	}
}

// playable and downloadable are separate facts and must arrive as separate
// rows, or a person cannot tell which of the two a click will give them.
func TestPlayableAndDownloadableAreDistinctRows(t *testing.T) {
	store := storeWith(t,
		vimmEntry{VaultID: "1", Title: "Both", Platform: "Nintendo", System: "nes",
			Play: "https://vimm.net/vault/?p=play&mediaId=1", Download: "https://dl3.vimm.net/?mediaId=1"},
		vimmEntry{VaultID: "2", Title: "Download Only", Platform: "Saturn",
			Download: "https://dl3.vimm.net/?mediaId=2"},
	)

	both := store.search("Both", "game", nil, 10)
	if len(both) != 1 {
		t.Fatalf("got %d cards", len(both))
	}
	actions := map[string]string{}
	for _, s := range both[0].Sources {
		actions[s.Action] = s.Magnet
	}
	if len(actions) != 2 || actions["play"] == "" || actions["download"] == "" {
		t.Fatalf("want one play row and one download row, got %+v", actions)
	}
	if actions["play"] == actions["download"] {
		t.Error("both rows point at the same URL; they are not the same offer")
	}

	dl := store.search("Download Only", "game", nil, 10)
	if len(dl) != 1 || len(dl[0].Sources) != 1 || dl[0].Sources[0].Action != "download" {
		t.Fatalf("a download-only entry must offer exactly one download row, got %+v", dl)
	}
}

// One work on one machine is one card, however many discs, regions and
// revisions of it Vimm holds. Two machines are two cards, because System is a
// single value and the facet keys on it.
func TestOneWorkOnOneMachineIsOneCard(t *testing.T) {
	store := storeWith(t,
		vimmEntry{VaultID: "10", Title: "Final Fantasy VII", File: "Final Fantasy VII (USA) (Disc 1)",
			Platform: "PlayStation", System: "psx", Download: "https://dl3.vimm.net/?mediaId=10"},
		vimmEntry{VaultID: "11", Title: "Final Fantasy VII", File: "Final Fantasy VII (USA) (Disc 2)",
			Platform: "PlayStation", System: "psx", Download: "https://dl3.vimm.net/?mediaId=11"},
		vimmEntry{VaultID: "12", Title: "Aladdin", Platform: "Super Nintendo", System: "snes",
			Download: "https://dl3.vimm.net/?mediaId=12"},
		vimmEntry{VaultID: "13", Title: "Aladdin", Platform: "Genesis", System: "genesis",
			Download: "https://dl3.vimm.net/?mediaId=13"},
	)

	ff := store.search("Final Fantasy VII", "game", nil, 10)
	if len(ff) != 1 {
		t.Fatalf("three discs became %d cards", len(ff))
	}
	if len(ff[0].Sources) != 2 {
		t.Errorf("the discs did not become source rows: %+v", ff[0].Sources)
	}

	al := store.search("Aladdin", "game", nil, 10)
	if len(al) != 2 {
		t.Fatalf("two machines became %d cards; a system facet cannot key on that", len(al))
	}
}

// Wade's rule: "label things on where they're from so it's easy to see." Every
// class of result has to be able to answer it, and the client draws one badge
// from one rule -- so a card must either NAME its origin or carry a source
// whose indexer names it. A card that can do neither renders as a blank badge.
func TestEveryResultCanSayWhereItCameFrom(t *testing.T) {
	labelled := func(c card) string {
		if c.Origin != "" {
			return c.Origin
		}
		if len(c.Sources) > 0 {
			return c.Sources[c.Best].Indexer
		}
		return ""
	}

	// The catalogue.
	store := storeWith(t,
		vimmEntry{VaultID: "1", Title: "Contra", Platform: "Nintendo", System: "nes",
			Download: "https://dl3.vimm.net/?mediaId=1"})
	got := store.search("Contra", "game", nil, 5)
	if len(got) != 1 || labelled(got[0]) != vimmSiteName {
		t.Errorf("a catalogue card is labelled %q, want %q", labelled(got[0]), vimmSiteName)
	}

	// archive.org. Its best source is named for the PLAYER -- "EmulatorJS" --
	// so without Origin the tile would credit a runtime instead of a place.
	ia := archiveCards([]archiveDoc{{
		Identifier: "contra_nes",
		Title:      flexString("Contra"),
		Emulator:   flexString("nes"),
	}}, "game")
	if len(ia) != 1 {
		t.Fatalf("archiveCards produced %d cards", len(ia))
	}
	if labelled(ia[0]) != archiveOrigin {
		t.Errorf("an archive.org card is labelled %q, want %q", labelled(ia[0]), archiveOrigin)
	}
	if ia[0].Sources[ia[0].Best].Indexer == archiveOrigin {
		t.Log("note: the best source happens to be named for the host here, " +
			"but Origin is what the label must not depend on losing")
	}

	// A torrent has no single origin and must not claim one -- its sources are
	// re-ranked after this point, so a frozen name would drift from the row a
	// click uses.
	tor := buildCards([]prowlarrResult{{
		Title: "Contra 1988 1080p BluRay x264-GROUP", Indexer: "YTS", Seeders: 12,
		Protocol: "torrent", MagnetURL: "magnet:?xt=urn:btih:abc",
	}})
	if len(tor) != 1 {
		t.Fatalf("buildCards produced %d cards", len(tor))
	}
	if tor[0].Origin != "" {
		t.Errorf("a torrent card froze an origin (%q); it is an aggregate and must not",
			tor[0].Origin)
	}
	if labelled(tor[0]) != "YTS" {
		t.Errorf("a torrent card is labelled %q, want the indexer", labelled(tor[0]))
	}
}

// ------------------------------------------------------------- the filter --

func TestTheSystemFilterNarrowsVimmResults(t *testing.T) {
	store := storeWith(t,
		vimmEntry{VaultID: "1", Title: "Aladdin", Platform: "Super Nintendo", System: "snes",
			Download: "https://dl3.vimm.net/?mediaId=1"},
		vimmEntry{VaultID: "2", Title: "Aladdin", Platform: "Genesis", System: "genesis",
			Download: "https://dl3.vimm.net/?mediaId=2"},
		vimmEntry{VaultID: "3", Title: "Aladdin", Platform: "CD-i",
			Download: "https://dl3.vimm.net/?mediaId=3"},
	)
	s := &server{vimm: store}

	all := store.search("Aladdin", "game", nil, 10)
	if len(all) != 3 {
		t.Fatalf("unfiltered got %d", len(all))
	}

	// The filter itself, applied the way a request applies it.
	f := parseFilters(map[string][]string{"system": {"snes"}})
	got := f.apply(all)
	if len(got) != 1 || got[0].System != "snes" {
		t.Fatalf("system=snes gave %d cards: %+v", len(got), got)
	}

	// An alias reaches the same shelf, because resolveSystems says so.
	f = parseFilters(map[string][]string{"system": {"megadrive"}})
	if got := f.apply(all); len(got) != 1 || got[0].System != "genesis" {
		t.Fatalf("system=megadrive gave %d cards", len(got))
	}

	// A machine with no slug on this site takes no part in the filter. It is
	// findable by name and it says what it is; it is not the machine asked for.
	f = parseFilters(map[string][]string{"system": {"snes"}})
	for _, c := range f.apply(all) {
		if c.Platform == "CD-i" {
			t.Error("a CD-i disc answered a request for SNES")
		}
	}

	// And the deepening asks the catalogue the narrowed question directly,
	// rather than re-narrowing whatever slice happened to arrive.
	deep := s.vimmDeepen("Aladdin", "game", f, nil)
	if len(deep) != 1 || deep[0].System != "snes" {
		t.Fatalf("vimmDeepen gave %d cards: %+v", len(deep), deep)
	}
}

func TestVimmResultsCarryTheSystemFacet(t *testing.T) {
	store := storeWith(t,
		vimmEntry{VaultID: "1", Title: "Aladdin", Platform: "Super Nintendo", System: "snes",
			Download: "https://dl3.vimm.net/?mediaId=1"},
		vimmEntry{VaultID: "2", Title: "Aladdin", Platform: "Genesis", System: "genesis",
			Download: "https://dl3.vimm.net/?mediaId=2"},
	)
	f := buildFacets(store.search("Aladdin", "game", nil, 10))

	seen := map[string]string{}
	for _, fc := range f.Systems {
		seen[fc.Value] = fc.Label
	}
	if seen["snes"] == "" || seen["genesis"] == "" {
		t.Fatalf("system facet is missing a machine: %+v", f.Systems)
	}
	if f.InstantCount != 0 {
		t.Errorf("InstantCount=%d; an off-site result is not hosted here", f.InstantCount)
	}
	if f.SwarmCount != 0 {
		t.Errorf("SwarmCount=%d; an off-site result has no peers either", f.SwarmCount)
	}
	if f.ExternalCount != 2 {
		t.Errorf("ExternalCount=%d, want 2", f.ExternalCount)
	}
}

// The seeder floor is a swarm property. Applying it to a catalogue entry would
// delete every Vimm result the moment somebody asked for a healthy torrent.
func TestASeederFloorDoesNotDeleteOffsiteResults(t *testing.T) {
	store := storeWith(t,
		vimmEntry{VaultID: "1", Title: "Contra", Platform: "Nintendo", System: "nes",
			Download: "https://dl3.vimm.net/?mediaId=1"},
	)
	f := parseFilters(map[string][]string{"minSeeders": {"5"}})
	if got := f.apply(store.search("Contra", "game", nil, 10)); len(got) != 1 {
		t.Fatalf("minSeeders=5 removed the catalogue entry (%d left)", len(got))
	}
}

// -------------------------------------------------------------- the search --

// The catalogue must reach the FIRST PAINT, not the poll after it. It is the
// only source that answers from memory, and running it after the goroutine that
// releases firstWave would mean it routinely missed the response it was meant
// to be part of.
func TestTheCatalogueLandsInTheFirstWave(t *testing.T) {
	withoutArchiveScope(t, "game")

	s := &server{
		cache:    map[string]cacheEntry{},
		inflight: map[string]chan struct{}{},
		vimm: storeWith(t,
			vimmEntry{VaultID: "3", Title: "10-Yard Fight", Platform: "Nintendo", System: "nes",
				Play: "https://vimm.net/vault/?p=play&mediaId=3"}),
	}
	s.tmdb = newTMDB("")
	s.igdb = newIGDB("", "")

	rec := httptest.NewRecorder()
	s.handleSearch(rec, httptest.NewRequest("GET", "/api/search?q=10-Yard+Fight&kind=game", nil))

	var body struct {
		Cards []struct {
			Title    string `json:"title"`
			System   string `json:"system"`
			Instant  bool   `json:"instant"`
			External *struct {
				Host string `json:"host"`
			} `json:"external"`
		} `json:"cards"`
		Sources map[string]string `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad response: %v -- %s", err, rec.Body.String())
	}
	if len(body.Cards) == 0 {
		t.Fatal("the first paint carried nothing; the local catalogue did not make it")
	}
	c := body.Cards[0]
	if c.Title != "10-Yard Fight" || c.System != "nes" {
		t.Errorf("wrong card: %+v", c)
	}
	if c.Instant {
		t.Error("the card claims to play here")
	}
	if c.External == nil || c.External.Host != "vimm.net" {
		t.Error("the card does not say it lives on another site")
	}
	if body.Sources["vimm"] != stageOK {
		t.Errorf("sources[vimm]=%q, want %q -- a client cannot see the source answered",
			body.Sources["vimm"], stageOK)
	}
}

// A server with nothing imported must say so rather than silently having no
// such source. "not-configured" is actionable; silence is not.
func TestAnEmptyCatalogueSaysSoRatherThanVanishing(t *testing.T) {
	j := newSearchJob("anything", "game")
	j.vimmStage(&server{})
	if got := j.snapshot().sources["vimm"]; got != stageNotConfigured {
		t.Errorf("sources[vimm]=%q, want %q", got, stageNotConfigured)
	}

	// A film search is not a source that failed and not one that found
	// nothing -- it is one that does not apply, which is the distinction
	// archive.go's stage already makes with the same word.
	j = newSearchJob("the matrix", "video")
	j.vimmStage(&server{vimm: storeWith(t,
		vimmEntry{VaultID: "1", Title: "Contra", Platform: "Nintendo", System: "nes",
			Download: "https://dl3.vimm.net/?mediaId=1"})})
	if got := j.snapshot().sources["vimm"]; got != stageNone {
		t.Errorf("on a film search sources[vimm]=%q, want %q", got, stageNone)
	}
}

// Vimm holds games and nothing else, so a film search must not be answered by
// it -- and an empty query is a browse, which is a different endpoint's job.
func TestTheCatalogueAnswersOnlyGameSearches(t *testing.T) {
	store := storeWith(t,
		vimmEntry{VaultID: "1", Title: "Aladdin", Platform: "Genesis", System: "genesis",
			Download: "https://dl3.vimm.net/?mediaId=1"},
	)
	if got := store.search("Aladdin", "video", nil, 10); len(got) != 0 {
		t.Errorf("a video search was answered with %d games", len(got))
	}
	if got := store.search("", "game", nil, 10); len(got) != 0 {
		t.Errorf("an empty query returned %d cards; that is the catalogue, not a search", len(got))
	}
	// No kind at all means every domain, and games are part of every domain.
	if got := store.search("Aladdin", "", nil, 10); len(got) != 1 {
		t.Errorf("an unscoped search got %d cards", len(got))
	}
}

// ------------------------------------------------------------- the import --

// The scan is a quarter done and will be imported again as it finishes. Doing
// so must update rows rather than duplicate them, and must let a row that had
// to guess its title get a real one.
func TestReimportingUpdatesRatherThanDuplicates(t *testing.T) {
	dir := t.TempDir()
	cat := filepath.Join(dir, "vimm.json")

	// First pass: the broken extension build. Every title is page furniture, so
	// every title has to come from the filename.
	first := writeExport(t, dir, `{"generated_at":"2026-08-10T00:22:09Z","items":[
		{"vault_id":"3","title":"Upload it to The Vault","filename":"10-Yard Fight (USA, Europe).nes",
		 "platform":"Nintendo","page_url":"https://vimm.net/vault/3",
		 "play_url":"https://vimm.net/vault/?p=play&mediaId=3",
		 "download_url":"https://dl3.vimm.net/?mediaId=3","playable":true,"downloadable":true,
		 "size_bytes":16384}
	]}`)
	rep, err := importVimm(first, cat)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Published != 1 || rep.Added != 1 || rep.TitlesFromFile != 1 {
		t.Fatalf("first import: %+v", rep)
	}

	// Same file again: nothing changes.
	rep, err = importVimm(first, cat)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Published != 1 || rep.Added != 0 || rep.Updated != 0 || rep.Unchanged != 1 {
		t.Fatalf("re-importing the same export was not a no-op: %+v", rep)
	}

	// Second pass: the fixed build, which reads the page title, plus a row the
	// scan had not reached the first time.
	second := writeExport(t, dir, `{"generated_at":"2026-08-11T00:00:00Z","items":[
		{"vault_id":"3","title":"10-Yard Fight","filename":"10-Yard Fight (USA, Europe).nes",
		 "platform":"Nintendo","page_url":"https://vimm.net/vault/3",
		 "play_url":"https://vimm.net/vault/?p=play&mediaId=3",
		 "download_url":"https://dl3.vimm.net/?mediaId=3","playable":true,"downloadable":true,
		 "size_bytes":16384},
		{"vault_id":"4","title":"Sonic the Hedgehog 3: Angel Island","filename":"Sonic 3 (USA).md",
		 "platform":"Genesis","page_url":"https://vimm.net/vault/4",
		 "download_url":"https://dl3.vimm.net/?mediaId=4","downloadable":true}
	]}`)
	rep, err = importVimm(second, cat)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Published != 2 {
		t.Fatalf("re-import duplicated or lost rows: published %d", rep.Published)
	}
	if rep.Added != 1 || rep.Updated != 1 {
		t.Errorf("want 1 added and 1 updated, got %+v", rep)
	}

	store, err := loadVimmStore(cat)
	if err != nil {
		t.Fatal(err)
	}
	// The repaired row keeps its id and gains a page title.
	got := store.search("Sonic the Hedgehog 3", "game", nil, 5)
	if len(got) != 1 || got[0].Title != "Sonic the Hedgehog 3: Angel Island" {
		t.Fatalf("the colon-carrying page title did not survive: %+v", got)
	}

	// Third pass: an OLDER build re-scans row 3 and reports furniture again.
	// The good title must not be given back.
	third := writeExport(t, dir, `{"items":[
		{"vault_id":"3","title":"Upload it to The Vault","filename":"10-Yard Fight (USA, Europe).nes",
		 "platform":"Nintendo","page_url":"https://vimm.net/vault/3",
		 "download_url":"https://dl3.vimm.net/?mediaId=3","downloadable":true}
	]}`)
	if _, err := importVimm(third, cat); err != nil {
		t.Fatal(err)
	}
	store, err = loadVimmStore(cat)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.search("10-Yard Fight", "game", nil, 5); len(got) != 1 ||
		got[0].Title != "10-Yard Fight" {
		t.Fatalf("a regressed export damaged a good title: %+v", got)
	}
}

// The un-scanned three quarters carry a real name and nothing else. A name is
// not a target, and this site does not publish a tile it cannot point at.
func TestUnscannedRowsAreNotPublished(t *testing.T) {
	dir := t.TempDir()
	export := writeExport(t, dir, `{"items":[
		{"vault_id":"109289","title":"'Splosion Man","page_url":"https://vimm.net/vault/109289",
		 "playable":false,"downloadable":false},
		{"vault_id":"3","title":"Upload it to The Vault","filename":"10-Yard Fight (USA).nes",
		 "platform":"Nintendo","page_url":"https://vimm.net/vault/3",
		 "download_url":"https://dl3.vimm.net/?mediaId=3","downloadable":true}
	]}`)
	rep, err := importVimm(export, filepath.Join(dir, "vimm.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Published != 1 {
		t.Errorf("published %d, want 1", rep.Published)
	}
	if rep.SkipNoPlatform != 1 {
		t.Errorf("SkipNoPlatform=%d, want 1", rep.SkipNoPlatform)
	}
	// Every row read is accounted for by exactly one outcome.
	accounted := rep.Added + rep.Updated + rep.Unchanged +
		rep.SkipNoVaultID + rep.SkipNoPlatform + rep.SkipNoTitle + rep.SkipNoTarget
	if accounted != rep.Read {
		t.Errorf("%d of %d rows accounted for: %+v", accounted, rep.Read, rep)
	}
}

// A row whose URLs are not on vimm.net loses those URLs, and with no target
// left it is not published at all.
func TestARowPointingElsewhereIsNotPublished(t *testing.T) {
	dir := t.TempDir()
	export := writeExport(t, dir, `{"items":[
		{"vault_id":"9","title":"Redirect","filename":"Redirect (USA).nes","platform":"Nintendo",
		 "page_url":"https://vimm.net/vault/9","download_url":"https://evil.example/?mediaId=9",
		 "downloadable":true}
	]}`)
	rep, err := importVimm(export, filepath.Join(dir, "vimm.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Published != 0 {
		t.Errorf("published %d; a row pointing off vimm.net must not be published", rep.Published)
	}
	if rep.SkipNoTarget != 1 || rep.RejectedURLs != 1 {
		t.Errorf("the rejection was not reported: %+v", rep)
	}
}

// A catalogue that cannot be read is a data problem to look at, not a thing to
// overwrite with whatever this export happens to hold.
func TestACorruptCatalogueIsNotSilentlyReplaced(t *testing.T) {
	dir := t.TempDir()
	cat := filepath.Join(dir, "vimm.json")
	if err := os.WriteFile(cat, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	export := writeExport(t, dir, `{"items":[]}`)
	if _, err := importVimm(export, cat); err == nil {
		t.Fatal("a corrupt catalogue was overwritten without complaint")
	}
	raw, _ := os.ReadFile(cat)
	if string(raw) != "{not json" {
		t.Error("the unreadable catalogue was modified")
	}
}

// A missing catalogue is a normal state, not a startup failure.
func TestAMissingCatalogueIsNotAnError(t *testing.T) {
	store, err := loadVimmStore(filepath.Join(t.TempDir(), "nothing.json"))
	if err != nil {
		t.Fatalf("a missing catalogue failed to load: %v", err)
	}
	if store.count() != 0 {
		t.Errorf("count=%d", store.count())
	}
	if got := store.search("mario", "game", nil, 10); len(got) != 0 {
		t.Errorf("an empty store answered with %d cards", len(got))
	}
	// And a nil store behaves the same, because most tests build a server with
	// a struct literal and never set one.
	var nilStore *vimmStore
	if got := nilStore.search("mario", "game", nil, 10); len(got) != 0 {
		t.Errorf("a nil store answered with %d cards", len(got))
	}
	if nilStore.count() != 0 || nilStore.stats() == nil {
		t.Error("a nil store did not answer safely")
	}
}

// ---------------------------------------------------------------- helpers --

func writeExport(t *testing.T, dir, body string) string {
	t.Helper()
	// Named per-call so several exports can live in one temp dir.
	p := filepath.Join(dir, "export-"+strings.ReplaceAll(t.Name(), "/", "_")+
		"-"+randomishName(body)+".json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// randomishName keeps two exports in one test apart without pulling in a
// counter that would make the tests order-dependent.
func randomishName(body string) string {
	var n uint32
	for i := 0; i < len(body); i++ {
		n = n*31 + uint32(body[i])
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i := range out {
		out[i] = hex[n&0xf]
		n >>= 4
	}
	return string(out)
}

func storeWith(t *testing.T, entries ...vimmEntry) *vimmStore {
	t.Helper()
	s := &vimmStore{}
	s.replace(vimmCatalogue{Version: vimmCatalogueVersion, Entries: entries})
	return s
}

// ------------------------------------------------------- the vault --------

// vaultEntry is the same row as the external tests use, plus the one field
// that changes everything: the core the vault resolved for it.
func vaultEntry() vimmEntry {
	return vimmEntry{
		VaultID: "3", Title: "10-Yard Fight", Platform: "Nintendo", System: "nes",
		Page:     "https://vimm.net/vault/3",
		Play:     "https://vimm.net/vault/?p=play&mediaId=3",
		Download: "https://dl3.vimm.net/?mediaId=3", Size: 16384,
		Core: "nes",
	}
}

// A vault entry is the one case where Instant is TRUE for Vimm, and it is only
// true because the vault exists: the ROM comes over HTTP from a host that is
// always up and plays in this site's own player.
func TestAVaultedVimmCardPlaysHere(t *testing.T) {
	t.Setenv("VIMM_VAULT", "https://vimm.example/")

	store := storeWith(t, vaultEntry())
	cards := store.search("10-Yard Fight", "game", nil, 10)
	if len(cards) != 1 {
		t.Fatalf("got %d cards", len(cards))
	}
	c := cards[0]

	if !c.Instant {
		t.Error("Instant is not set; the vault serves this and the player runs it")
	}
	if c.External != nil {
		t.Error("External is set alongside Instant; a card may never carry both")
	}
	if c.Origin != vimmSiteName {
		t.Errorf("Origin=%q; the game still comes from Vimm and the tile says so", c.Origin)
	}
	if c.Art.Poster != "https://vimm.example/api/art/3" || !c.Art.Found {
		t.Errorf("no vault artwork: %+v", c.Art)
	}
}

// Best is index 0, so the row a click gets must be the one that plays here.
func TestTheVaultRowComesFirst(t *testing.T) {
	t.Setenv("VIMM_VAULT", "https://vimm.example")

	cards := storeWith(t, vaultEntry()).search("10-Yard Fight", "game", nil, 10)
	first := cards[0].Sources[0]

	if !strings.HasPrefix(first.Magnet, "https://vimm.example/api/rom/3#ejs=nes") {
		t.Errorf("first source is %q, not the vault ROM", first.Magnet)
	}
	// The name rides along because the URI's last path segment is the vault
	// id, and a player naming the game from the URL would say "Play 3".
	if !strings.Contains(first.Magnet, "name=10-Yard+Fight") {
		t.Errorf("no name on the URI: %q", first.Magnet)
	}
	if first.Action != "play" {
		t.Errorf("first source action=%q, want play", first.Action)
	}
	if !first.WebSafe {
		t.Error("the vault row is not webSafe, so the webSafe filter would drop the only playable row")
	}
	if first.Offsite {
		t.Error("the vault row is marked offsite, which routes it to a new tab instead of the player")
	}
}

// The core must travel with the URI. Inferring it at the other end from a file
// extension is how a Colecovision game got booted as an NES: the wrong core
// starts cleanly and then runs a black screen with no error.
func TestTheVaultRowCarriesTheCore(t *testing.T) {
	t.Setenv("VIMM_VAULT", "https://vimm.example")

	e := vaultEntry()
	e.Core = "segaMD"
	e.Platform, e.System = "Genesis", "genesis"

	cards := storeWith(t, e).search("10-Yard Fight", "game", nil, 10)
	if got := cards[0].Sources[0].Magnet; !strings.Contains(got, "#ejs=segaMD&") {
		t.Errorf("core lost from the URI: %q", got)
	}
}

// Vimm's own player and download host are still offered underneath. They are
// separate facts about the entry and the vault does not replace them.
func TestVimmsOwnRowsSurviveAlongsideTheVault(t *testing.T) {
	t.Setenv("VIMM_VAULT", "https://vimm.example")

	cards := storeWith(t, vaultEntry()).search("10-Yard Fight", "game", nil, 10)
	var offsite, here int
	for _, s := range cards[0].Sources {
		if s.Offsite {
			offsite++
		} else {
			here++
		}
	}
	if here != 1 {
		t.Errorf("%d rows play here, want exactly 1", here)
	}
	if offsite != 2 {
		t.Errorf("%d offsite rows, want 2 (their player and their download)", offsite)
	}
}

// Half the Vault is Xbox 360, PS3, Wii, GameCube, PS2, Dreamcast and CD-i. The
// vault resolves no core for those, and they must stay exactly what they were.
func TestAnEntryTheVaultCannotPlayStaysExternal(t *testing.T) {
	t.Setenv("VIMM_VAULT", "https://vimm.example")

	e := vaultEntry()
	e.Core = ""
	e.Platform, e.System = "PlayStation 2", ""

	c := storeWith(t, e).search("10-Yard Fight", "game", nil, 10)[0]
	if c.Instant {
		t.Error("Instant on an entry with no core; nothing can play it here")
	}
	if c.External == nil {
		t.Fatal("External missing; nothing tells a client this leaves the site")
	}
	if c.Art.Poster != "" {
		t.Errorf("artwork %q on a card the vault does not hold", c.Art.Poster)
	}
}

// A self-host with no vault must be unaffected, which is what makes shipping
// this safe: the flag is empty everywhere it has not been set on purpose.
func TestWithNoVaultConfiguredNothingChanges(t *testing.T) {
	t.Setenv("VIMM_VAULT", "")

	c := storeWith(t, vaultEntry()).search("10-Yard Fight", "game", nil, 10)[0]
	if c.Instant {
		t.Error("Instant with no vault configured; there is nowhere to play it")
	}
	if c.External == nil {
		t.Error("External missing with no vault configured")
	}
	for _, s := range c.Sources {
		if !s.Offsite {
			t.Errorf("source %q is not offsite, but there is no vault", s.Title)
		}
	}
}
