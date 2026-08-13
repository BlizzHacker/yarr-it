package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func depotStoreWith(t *testing.T, items ...depotItem) *depotStore {
	t.Helper()
	p := filepath.Join(t.TempDir(), "theromdepot.json")
	raw, err := json.Marshal(depotCatalogue{
		Version: 1, GeneratedAt: "2026-08-12T00:00:00Z", Items: items,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadDepotStore(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDepotSearchPublishesExplicitLoginRequiredDownload(t *testing.T) {
	s := depotStoreWith(t, depotItem{
		ID:       "Nintendo 64/Europe/Mario Party (Europe) (En,Fr,De).z64",
		Title:    "Mario Party (Europe) (En,Fr,De).z64",
		Name:     "Mario Party (Europe) (En,Fr,De).z64",
		Platform: "Nintendo 64",
		Region:   "Europe",
		Size:     33554432,
	})
	cards := s.search("Mario Party Europe", domainGame, nil, 10)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	c := cards[0]
	if c.Title != "Mario Party (Europe) (En,Fr,De)" || c.System != "n64" {
		t.Errorf("card metadata = title %q, system %q", c.Title, c.System)
	}
	if c.Instant || c.External == nil || c.External.Name != depotSiteName ||
		!strings.Contains(c.External.Host, "login required") {
		t.Errorf("external contract is wrong: instant=%v external=%+v", c.Instant, c.External)
	}
	if len(c.Sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(c.Sources))
	}
	src := c.Sources[0]
	want := "https://theromdepot.com/api/download/Nintendo%2064/Europe/Mario%20Party%20%28Europe%29%20%28En%2CFr%2CDe%29.z64"
	if src.Magnet != want {
		t.Errorf("download URL\n got %s\nwant %s", src.Magnet, want)
	}
	if !src.Offsite || src.Action != "download" || src.Indexer != depotSiteName {
		t.Errorf("source action contract is wrong: %+v", src)
	}
	if !strings.Contains(src.Source, "Login required") || src.SizeHuman != "32.0 MiB" {
		t.Errorf("source facts are wrong: %+v", src)
	}
}

func TestDepotRejectsUnsafeCataloguePaths(t *testing.T) {
	for _, id := range []string{"../secret.zip", "Nintendo 64/../secret.zip", "Nintendo 64\\secret.zip", ""} {
		if got, ok := depotDownloadURL(id); ok {
			t.Errorf("unsafe path %q accepted as %q", id, got)
		}
	}
}

func TestDepotStageIsFirstClassAndSiteFileFilterIncludesIt(t *testing.T) {
	s := depotStoreWith(t, depotItem{
		ID: "Super Nintendo/USA/Spider-Man (USA).sfc", Title: "Spider-Man (USA).sfc",
		Platform: "Super Nintendo", Region: "USA",
	})
	j := newSearchJob("Spider-Man", domainGame)
	j.depotStage(&server{depot: s})
	snap := j.snapshot()
	if snap.sources["theromdepot"] != stageOK || len(snap.cards) != 1 {
		t.Fatalf("stage = %q, cards = %d", snap.sources["theromdepot"], len(snap.cards))
	}
	got := filters{Source: "instant"}.apply(snap.cards)
	if len(got) != 1 {
		t.Fatalf("Site files filter hid The ROM Depot: %d cards", len(got))
	}
	f := buildFacets(snap.cards)
	if f.InstantCount != 1 || f.ExternalCount != 1 || f.SwarmCount != 0 {
		t.Errorf("facets = %+v", f)
	}
}
