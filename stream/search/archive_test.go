package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArchiveQueryScopesToBrowserPlayableItems(t *testing.T) {
	q := archiveQuery("mario")
	if !strings.Contains(q, `title:("mario")`) {
		t.Fatalf("query does not search the title: %s", q)
	}
	if !strings.Contains(q, "emulator:[* TO *]") {
		t.Fatalf("query is not scoped to browser-playable items: %s", q)
	}
}

// A search box accepts anything. Interpolating it raw into a Solr query means a
// stray colon or quote either errors or silently searches for something else.
func TestArchiveQueryNeutralisesSolrSyntax(t *testing.T) {
	q := archiveQuery(`doom" OR collection:(nsfw`)
	if strings.Contains(q, `"doom"`) {
		t.Fatalf("injected quote survived: %s", q)
	}
	// The user's text must end up inside exactly one quoted phrase, so the
	// collection scope cannot be escaped.
	if strings.Count(q, `title:(`) != 1 {
		t.Fatalf("query structure was broken by input: %s", q)
	}
	if !strings.Contains(q, "emulator:[* TO *]") {
		t.Fatalf("scope was escaped: %s", q)
	}
}

func TestArchiveCardsAreInstantAndCarryTheirOwnArt(t *testing.T) {
	cards := archiveCards([]archiveDoc{{
		Identifier: "dk_coleco",
		Title:      "Donkey Kong",
		Emulator:   "coleco",
		Collection: []string{"consolelivingroom"},
		Downloads:  4200,
	}})
	if len(cards) != 1 {
		t.Fatalf("want 1 card, got %d", len(cards))
	}
	c := cards[0]
	if !c.Instant {
		t.Error("an archive.org item is always up; it should be marked instant")
	}
	if c.Kind != "game" {
		t.Errorf("kind = %q, want game", c.Kind)
	}
	if c.Platform != "ColecoVision" {
		t.Errorf("platform = %q, want ColecoVision", c.Platform)
	}
	if c.Popular != 4200 {
		t.Errorf("popular = %d, want 4200", c.Popular)
	}
	if c.Seeders != 0 {
		t.Error("an archive.org item has no swarm; seeders must not be invented")
	}
	// Colecovision has an EmulatorJS core, so both players are offered and the
	// one with touch controls leads.
	if len(c.Sources) != 2 {
		t.Fatalf("want both play options, got %d", len(c.Sources))
	}
	if c.Sources[0].Indexer != "EmulatorJS" {
		t.Errorf("source[0] = %q, want the touch-capable player first", c.Sources[0].Indexer)
	}
	if got := c.Sources[0].Magnet; got != "https://archive.org/details/dk_coleco#ejs" {
		t.Errorf("emulatorjs target = %q", got)
	}
	if got := c.Sources[1].Magnet; got != "https://archive.org/details/dk_coleco" {
		t.Errorf("archive.org target = %q", got)
	}
	if !strings.HasPrefix(c.Art.Poster, "https://archive.org/services/img/") {
		t.Errorf("art should come from archive.org, got %q", c.Art.Poster)
	}
}

func TestArchiveCardsAreOrderedByDemandAndDeduplicated(t *testing.T) {
	cards := archiveCards([]archiveDoc{
		{Identifier: "a", Title: "Quiet", Downloads: 10},
		{Identifier: "b", Title: "Popular", Downloads: 90000},
		{Identifier: "a", Title: "Quiet again", Downloads: 10},
	})
	if len(cards) != 2 {
		t.Fatalf("duplicate identifier was not collapsed: %d cards", len(cards))
	}
	if cards[0].Title != "Popular" {
		t.Errorf("most-downloaded should lead, got %q", cards[0].Title)
	}
}

func TestArchiveYearAcceptsBothShapesTheApiReturns(t *testing.T) {
	if got := archiveYear(json.RawMessage(`1985`)); got != 1985 {
		t.Errorf("numeric year = %d", got)
	}
	if got := archiveYear(json.RawMessage(`"1985-01-01"`)); got != 1985 {
		t.Errorf("string year = %d", got)
	}
	if got := archiveYear(nil); got != 0 {
		t.Errorf("missing year = %d, want 0", got)
	}
}

// For arcade items the `emulator` field is the MAME *driver* name, not a
// system, so passing an unrecognised id through as a label once produced a
// Contra result whose platform read "Contra".
func TestArcadeDriverNamesAreNotShownAsPlatforms(t *testing.T) {
	if got := friendlySystem("nes", nil); got != "NES" {
		t.Errorf("known system = %q", got)
	}
	if got := friendlySystem("contra", []string{"internetarcade"}); got != "Arcade" {
		t.Errorf("mame driver = %q, want the collection's name", got)
	}
	if got := friendlySystem("ruffle-swf", nil); got != "Flash" {
		t.Errorf("flash = %q", got)
	}
	if got := friendlySystem("dosbox", nil); got != "MS-DOS" {
		t.Errorf("dos = %q", got)
	}
	// Nothing recognisable at all is better shown blank than wrong.
	if got := friendlySystem("wat", []string{"fav-someone"}); got != "" {
		t.Errorf("unknown = %q, want empty", got)
	}
}

// The complaint that started this: results with confidently wrong artwork.
// TMDB is a film database, so a game must never be looked up in it.
func TestGamesAreNotLookedUpInTheFilmDatabase(t *testing.T) {
	c := newTMDB("test-key")
	cards := []card{
		{Title: "Sonic the Hedgehog (Genesis)", Kind: "game",
			Art: artwork{Poster: "https://archive.org/services/img/sonic", Found: true}},
	}
	// A real lookup would need the network; if enrich tried, this would fail or
	// blank the art it already has.
	c.enrich(t.Context(), cards, 10)
	if cards[0].Art.Poster != "https://archive.org/services/img/sonic" {
		t.Errorf("game art was overwritten with a film poster: %q", cards[0].Art.Poster)
	}
}

// An always-up host answers "will this play" with yes, which is what the
// seeder score exists to estimate.
func TestAnInstantResultOutranksAThinlySeededTorrent(t *testing.T) {
	terms := []string{"mario"}
	instant := card{Title: "Super Mario Bros (NES)", Kind: "game", Instant: true, Popular: 50000}
	thin := card{Title: "Super Mario Bros ROM Set", Seeders: 2}
	if relevance(instant, terms) <= relevance(thin, terms) {
		t.Errorf("instant %d should beat 2-seeder %d",
			relevance(instant, terms), relevance(thin, terms))
	}
}

// Ranking must still be about the query. A wildly popular unrelated item should
// not beat the thing that was actually asked for.
func TestPopularityDoesNotOverrideRelevance(t *testing.T) {
	terms := []string{"contra"}
	wrong := card{Title: "Super Mario Bros", Kind: "game", Instant: true, Popular: 900000}
	right := card{Title: "Contra", Kind: "game", Instant: true, Popular: 100}
	if relevance(right, terms) <= relevance(wrong, terms) {
		t.Errorf("the matching title must win: contra %d vs mario %d",
			relevance(right, terms), relevance(wrong, terms))
	}
}

// Roku has no WebAssembly and no third-party iframe, so a game card there is a
// card that can never be opened.
func TestGamesAreHiddenOnDevicesThatCannotRunThem(t *testing.T) {
	games := []card{{
		Title: "Donkey Kong", Kind: "game", Instant: true,
		Sources: []source{{Title: "Donkey Kong", Magnet: "https://archive.org/details/dk"}},
	}}
	roku := rokuProfile
	if got := roku.applyDevice(games); len(got) != 0 {
		t.Errorf("roku cannot emulate; want 0 game cards, got %d", len(got))
	}
	// A browser can, and must still get them.
	if got := (*deviceProfile)(nil).applyDevice(games); len(got) != 1 {
		t.Errorf("browser should keep the game, got %d cards", len(got))
	}
}

// The default filter asks for at least one seeder. A hosted game has none --
// the concept does not apply -- so applying that filter to it makes a search
// that found 142 games report nothing.
func TestTheDefaultSeederFilterDoesNotHideHostedGames(t *testing.T) {
	games := []card{{
		Title: "Contra", Kind: "game", Instant: true, Popular: 5000,
		Sources: []source{{Title: "Contra", Indexer: "Archive.org", Seeders: 0}},
	}}
	f := filters{MinSeeders: 1, Sort: "seeders"}
	if got := f.apply(games); len(got) != 1 {
		t.Fatalf("hosted game was filtered out by a seeder floor: %d cards", len(got))
	}
	// A genuinely dead torrent must still be removed.
	dead := []card{{Title: "Contra", Sources: []source{{Title: "Contra", Seeders: 0}}}}
	if got := f.apply(dead); len(got) != 0 {
		t.Errorf("dead torrent survived the seeder floor: %d cards", len(got))
	}
}

// Size is likewise a swarm property, and a hosted game reports none.
func TestSizeFiltersDoNotHideHostedGames(t *testing.T) {
	games := []card{{
		Title: "Doom", Kind: "game", Instant: true,
		Sources: []source{{Title: "Doom", Indexer: "Archive.org", Size: 0}},
	}}
	f := filters{MinSizeMB: 100, Sort: "seeders"}
	if got := f.apply(games); len(got) != 1 {
		t.Fatalf("hosted game was filtered out by a size floor: %d cards", len(got))
	}
}

// Narrowing to exactly what you wanted must not hide it.
func TestClickingTheGamesChipKeepsHostedGames(t *testing.T) {
	games := archiveCards([]archiveDoc{{Identifier: "dk", Title: "Donkey Kong", Emulator: "coleco"}})
	f := filters{Groups: []string{"games"}, Sort: "seeders"}
	if got := f.apply(games); len(got) != 1 {
		t.Fatalf("the Games filter hid a game: %d cards", len(got))
	}
}

// An arcade cabinet also sits in consolelivingroom, so matching on the order
// the collections happen to arrive in labelled Contra "Console".
func TestTheMostSpecificCollectionWins(t *testing.T) {
	got := friendlySystem("contra", []string{"consolelivingroom", "internetarcade", "emulation"})
	if got != "Arcade" {
		t.Errorf("platform = %q, want Arcade", got)
	}
}

// "Most seeders" means "healthiest first". A hosted result is the top of that
// scale, so sorting it by its literal zero buries the only guaranteed results.
func TestSortingBySeedersDoesNotBuryGuaranteedResults(t *testing.T) {
	cards := []card{
		{Title: "Sonic ROM Set", Sources: []source{{Seeders: 2}}, Seeders: 2},
		{Title: "Sonic the Hedgehog", Kind: "game", Instant: true, Popular: 9000,
			Sources: []source{{Indexer: "Archive.org"}}},
	}
	filters{Sort: "seeders"}.sortCards(cards)
	if cards[0].Title != "Sonic the Hedgehog" {
		t.Errorf("a guaranteed result lost to a 2-seeder torrent: %q leads", cards[0].Title)
	}
}

// But a word shared by a game and a film must not bury the well-seeded film.
func TestAWellSeededTorrentStillOutranksAHostedGame(t *testing.T) {
	cards := []card{
		{Title: "Batman (NES)", Kind: "game", Instant: true, Popular: 9000,
			Sources: []source{{Indexer: "Archive.org"}}},
		{Title: "The Batman 2022", Sources: []source{{Seeders: 800}}, Seeders: 800},
	}
	filters{Sort: "seeders"}.sortCards(cards)
	if cards[0].Title != "The Batman 2022" {
		t.Errorf("an 800-seeder film should still lead: %q leads", cards[0].Title)
	}
}

// The archive's own player expects a keyboard and has no on-screen controls,
// so on a phone a console game there is something you can watch but not play.
// Where both can run a title, the one with a touch pad has to lead.
func TestTheTouchCapablePlayerIsRankedFirst(t *testing.T) {
	cards := archiveCards([]archiveDoc{{
		Identifier: "sonic", Title: "Sonic", Emulator: "genesis", Downloads: 500,
	}})
	c := cards[0]
	rankSources(&c)
	if c.Sources[c.Best].Indexer != "EmulatorJS" {
		t.Errorf("best source = %q, want EmulatorJS", c.Sources[c.Best].Indexer)
	}
}

// A system EmulatorJS has no core for must not be offered with a player that
// cannot run it -- that is a button which loads and then does nothing.
func TestASystemWithNoCoreOffersOnlyTheArchivePlayer(t *testing.T) {
	cards := archiveCards([]archiveDoc{{
		Identifier: "intv_game", Title: "Astrosmash", Emulator: "intv2",
		Collection: []string{"consolelivingroom"},
	}})
	if n := len(cards[0].Sources); n != 1 {
		t.Fatalf("want only the archive player, got %d sources", n)
	}
	if cards[0].Sources[0].Indexer != "Archive.org" {
		t.Errorf("source = %q", cards[0].Sources[0].Indexer)
	}
}

// An MS-DOS game plays in their player only. Offering "touch controls" for
// something that wants a keyboard and a mouse would be a lie.
func TestDosKeepsTheArchivePlayerOnly(t *testing.T) {
	cards := archiveCards([]archiveDoc{{
		Identifier: "x_dos", Title: "X", Emulator: "dosbox",
	}})
	if n := len(cards[0].Sources); n != 1 {
		t.Errorf("want 1 source for MS-DOS, got %d", n)
	}
}

// Flash is the same trade as a console game, and a SWF is about a megabyte --
// far cheaper to relay than a ROM -- so both players are offered and ours,
// which has touch support, leads.
func TestFlashIsAlsoOfferedThroughOurOwnRuffle(t *testing.T) {
	cards := archiveCards([]archiveDoc{{
		Identifier: "waluigigame", Title: "Waluigi Game", Emulator: "ruffle-swf",
	}})
	c := cards[0]
	if len(c.Sources) != 2 {
		t.Fatalf("want both players for Flash, got %d", len(c.Sources))
	}
	rankSources(&c)
	if c.Sources[c.Best].Indexer != "Ruffle" {
		t.Errorf("best = %q, want Ruffle to lead", c.Sources[c.Best].Indexer)
	}
	if got := c.Sources[c.Best].Magnet; !strings.HasSuffix(got, "#swf") {
		t.Errorf("ruffle target = %q, want the #swf fragment", got)
	}
}
