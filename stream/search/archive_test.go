package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArchiveQueryScopesToEmulationCollections(t *testing.T) {
	q := archiveQuery("mario")
	if !strings.Contains(q, `title:("mario")`) {
		t.Fatalf("query does not search the title: %s", q)
	}
	if !strings.Contains(q, "consolelivingroom") {
		t.Fatalf("query is not scoped to the emulation collections: %s", q)
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
	if !strings.Contains(q, "consolelivingroom") {
		t.Fatalf("collection scope was escaped: %s", q)
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
	if got := c.Sources[0].Magnet; got != "https://archive.org/details/dk_coleco" {
		t.Errorf("play target = %q", got)
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
