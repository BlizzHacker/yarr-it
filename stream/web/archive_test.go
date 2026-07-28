
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
