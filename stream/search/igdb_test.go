package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestGameRowsAreDisabledWithoutCredentials(t *testing.T) {
	if newIGDB("", "").enabled() {
		t.Error("no credentials should mean no game rows, not a broken shelf")
	}
	if newIGDB("id", "").enabled() {
		t.Error("half a credential is not a credential")
	}
	if !newIGDB("id", "secret").enabled() {
		t.Error("both present should enable")
	}
	// A disabled client must return nothing rather than panic, so a deployment
	// with no IGDB key still serves a landing page.
	if rows := newIGDB("", "").discoverGames(t.Context()); rows != nil {
		t.Errorf("want nil rows, got %d", len(rows))
	}
}

// Without a floor on the rating count the lists fill with obscure entries a
// handful of people scored 100, and without game_type the lists fill with DLC,
// bundles and ports -- neither is what anybody means by "top rated games".
func TestEveryRowFiltersForRealGamesWithRealRatings(t *testing.T) {
	for _, row := range igdbRows {
		if !strings.Contains(row.query, "total_rating_count >") {
			t.Errorf("%s: no floor on rating count", row.key)
		}
		if !strings.Contains(row.query, "game_type = 0") {
			t.Errorf("%s: does not restrict to main games", row.key)
		}
		if !strings.Contains(row.query, "cover.image_id") {
			t.Errorf("%s: does not request cover art", row.key)
		}
	}
}

func TestItemsRenderLikeTheFilmRows(t *testing.T) {
	games := []igdbGame{{
		Name:         "Super Metroid",
		Summary:      "Samus returns to Zebes.",
		TotalRating:  96.1,
		FirstRelease: 762393600, // 1994
	}}
	games[0].Cover.ImageID = "co2r7f"

	items := gameItems(games)
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Year != 1994 {
		t.Errorf("year = %d, want 1994", it.Year)
	}
	// The film rows show a 10-point score; IGDB rates out of 100.
	if it.Rating < 9.6 || it.Rating > 9.62 {
		t.Errorf("rating = %v, want ~9.61 on the film scale", it.Rating)
	}
	if !strings.HasPrefix(it.Poster, "https://images.igdb.com/") {
		t.Errorf("poster = %q", it.Poster)
	}
	// t_cover_big matches the film poster aspect, so the shelves line up.
	if !strings.Contains(it.Poster, "t_cover_big") {
		t.Errorf("poster is not the poster-shaped size: %q", it.Poster)
	}
	if it.MediaType != "game" {
		t.Errorf("mediaType = %q", it.MediaType)
	}
}

// The whole point of the design: a shelf entry is a recommendation, not a
// pointer at anybody's library. An empty Play makes a click run a search,
// exactly as a film row does.
func TestAGameShelfNeverPointsAtALibraryFile(t *testing.T) {
	games := []igdbGame{{Name: "Chrono Trigger", TotalRating: 92}}
	games[0].Cover.ImageID = "abc123"
	it := gameItems(games)[0]
	if it.Play != "" {
		t.Errorf("Play = %q; a game shelf must search, not serve a file", it.Play)
	}
}

// A cover-less entry renders as a grey box with a title in it, which looks
// broken beside the film rows.
func TestEntriesWithoutCoverArtAreDropped(t *testing.T) {
	withCover := igdbGame{Name: "Has Art"}
	withCover.Cover.ImageID = "co1"
	items := gameItems([]igdbGame{{Name: "No Art"}, withCover})
	if len(items) != 1 || items[0].Title != "Has Art" {
		t.Errorf("cover-less entry survived: %+v", items)
	}
}

func TestMissingReleaseDateBecomesNoYearRatherThan1970(t *testing.T) {
	if got := igdbYear(0); got != 0 {
		t.Errorf("igdbYear(0) = %d, want 0", got)
	}
	if got := igdbYear(-5); got != 0 {
		t.Errorf("igdbYear(-5) = %d, want 0", got)
	}
	if got := igdbYear(762393600); got != 1994 {
		t.Errorf("igdbYear = %d, want 1994", got)
	}
}

// The highest-rated games ever made are largely the retro ones, so "Top rated"
// and "Retro classics" came back as the same three games in the same order --
// the same shelf printed twice, which reads as a bug in the page.
func TestATitleAppearsOnOnlyOneShelf(t *testing.T) {
	mk := func(name string) igdbGame {
		g := igdbGame{Name: name, TotalRating: 90}
		g.Cover.ImageID = "co" + name
		return g
	}
	rows := []discoverRow{
		{Key: "games-top", Items: gameItems([]igdbGame{mk("Super Metroid"), mk("Zelda")})},
		{Key: "games-classics", Items: gameItems([]igdbGame{mk("Super Metroid"), mk("Contra")})},
	}
	deduped := dedupeShelves(rows)

	if len(deduped) != 2 {
		t.Fatalf("want 2 shelves, got %d", len(deduped))
	}
	if len(deduped[1].Items) != 1 || deduped[1].Items[0].Title != "Contra" {
		t.Errorf("second shelf should keep only what the first did not take: %+v",
			deduped[1].Items)
	}
}

// A shelf that loses every entry to an earlier one must vanish rather than
// render as an empty rail.
func TestAShelfEmptiedByDedupeIsDropped(t *testing.T) {
	g := igdbGame{Name: "Only Game", TotalRating: 90}
	g.Cover.ImageID = "co1"
	rows := []discoverRow{
		{Key: "a", Items: gameItems([]igdbGame{g})},
		{Key: "b", Items: gameItems([]igdbGame{g})},
	}
	if got := dedupeShelves(rows); len(got) != 1 {
		t.Errorf("want the emptied shelf dropped, got %d shelves", len(got))
	}
}

func TestShelvesAreTrimmedToOneRowOfCovers(t *testing.T) {
	var games []igdbGame
	for i := 0; i < perGameRow*3; i++ {
		g := igdbGame{Name: fmt.Sprintf("Game %d", i), TotalRating: 90}
		g.Cover.ImageID = fmt.Sprintf("co%d", i)
		games = append(games, g)
	}
	got := dedupeShelves([]discoverRow{{Key: "a", Items: gameItems(games)}})
	if len(got[0].Items) != perGameRow {
		t.Errorf("shelf holds %d covers, want %d", len(got[0].Items), perGameRow)
	}
}
