package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func fixture(t *testing.T, repo, name string) string {
	t.Helper()
	root := `C:\Users\wadei\OneDrive\Documents\PC & Server\romarr-ecosystem`
	raw, err := os.ReadFile(filepath.Join(root, repo, "tests", "fixtures", name))
	if err != nil {
		t.Skipf("shared plugin fixture not available: %v", err)
	}
	return string(raw)
}

func parseFixture(t *testing.T, markup string) *htmlNodeDocument {
	t.Helper()
	return &htmlNodeDocument{markup: markup}
}

// Small wrapper keeps the table below readable while still feeding the real
// HTML parser used in production.
type htmlNodeDocument struct{ markup string }

func (d *htmlNodeDocument) hits(t *testing.T, p romProvider) []romHit {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(d.markup))
	if err != nil {
		t.Fatal(err)
	}
	return p.parse(doc, p)
}

func TestProviderParsersReusePluginFixtureContracts(t *testing.T) {
	tests := []struct {
		id, repo, fixture, first string
		count                    int
	}{
		// Webmulator pads search pages with unrelated popular titles. The raw
		// parser preserves all six cards; romProvider.search applies the query
		// contract and keeps only Contra.
		{"webmulator", "romarr-plugin-webmulator", "search_contra.html", "Marvel Vs.Capcom 2 New Age of Heroes", 6},
		{"retrostic", "romarr-plugin-retrostic", "search_contra.html", "Contra (USA)", 5},
		{"emuparadise", "romarr-plugin-emuparadise", "search_contra.html", "Contra (USA)", 7},
		{"romulation", "romarr-plugin-romulation", "search_contra.html", "Contra", 5},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			p := romProviders[tt.id]
			hits := parseFixture(t, fixture(t, tt.repo, tt.fixture)).hits(t, p)
			if len(hits) != tt.count {
				t.Fatalf("hits=%d, want %d: %+v", len(hits), tt.count, hits)
			}
			if hits[0].title != tt.first {
				t.Fatalf("first title=%q, want %q", hits[0].title, tt.first)
			}
		})
	}
}

func TestWebmulatorCardUsesOfficialMobilePlayerInsideYarrit(t *testing.T) {
	p := romProviders["webmulator"]
	target, ok := p.itemURL("https://www.webmulator.com/games/nintendo/contra")
	if !ok || target != "https://www.webmulator.com/games/nintendo/contra/mobile" {
		t.Fatalf("target=%q ok=%v", target, ok)
	}
	c := p.card("nintendo/contra", "Contra", "NES", "nes", "", 0, target)
	if !c.Instant || c.External != nil || len(c.Sources) != 1 || c.Sources[0].Offsite || !c.Sources[0].WebSafe {
		t.Fatalf("card is not an in-page hosted player: %+v", c)
	}
	if c.Sources[0].Action != "play" || strings.Contains(c.Sources[0].Magnet, "rom_url") || strings.Contains(c.Sources[0].Magnet, "downloads.webmulator.com") {
		t.Fatalf("unsafe or wrong source: %+v", c.Sources[0])
	}
}

func TestMetadataOnlyProvidersNeverClaimPlayOrDownload(t *testing.T) {
	for _, id := range []string{"emuparadise", "romulation", "coolrom", "cdromance"} {
		p := romProviders[id]
		if p.action != "open" {
			t.Errorf("%s action=%q", id, p.action)
		}
	}
}

func TestProviderRedirectCannotLeaveItsHost(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer evil.Close()
	p := romProviders["webmulator"]
	c := p.client()
	req, _ := http.NewRequest(http.MethodGet, evil.URL, nil)
	if err := c.CheckRedirect(req, []*http.Request{{}}); err == nil {
		t.Fatal("cross-host/insecure redirect was accepted")
	}
}

func TestSearchTurnsProviderFixtureIntoClickableCards(t *testing.T) {
	markup := fixture(t, "romarr-plugin-webmulator", "search_contra.html")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(markup))
	}))
	defer ts.Close()
	p := romProviders["webmulator"]
	p.searchURL = func(string) string { return ts.URL }
	p.httpClient = ts.Client()
	// itemURL remains strict; only fetching is substituted by the fixture.
	cards, err := p.search(context.Background(), "contra", 20)
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards=%+v err=%v", cards, err)
	}
	if cards[0].Origin != "Webmulator" || cards[0].Sources[0].Action != "play" {
		t.Fatalf("unexpected card: %+v", cards[0])
	}
}
