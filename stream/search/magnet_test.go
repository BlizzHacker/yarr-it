package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const fakeKey = "PROWLARR_API_KEY_REDACTED"

// Prowlarr fills magnetUrl/downloadUrl with links back to itself carrying the
// API key in the query string. Emitting those on a public page would publish
// the key and the indexer's LAN address to every visitor. This must never
// regress.
func TestUsableMagnetNeverLeaksProwlarrProxyURL(t *testing.T) {
	leaky := []prowlarrResult{
		{
			Title:     "ubuntu-24.04.1-desktop-amd64.iso",
			InfoHash:  "4A3F5E08BCEF825718EDA30637230585E3330599",
			MagnetURL: "http://192.168.0.115:9696/2/download?apikey=" + fakeKey + "&link=abc",
		},
		{
			Title:       "Some Release",
			GUID:        "https://rutracker.org/forum/viewtopic.php?t=6555887",
			DownloadURL: "http://localhost:9696/14/download?apikey=" + fakeKey + "&link=xyz",
		},
	}
	for _, r := range leaky {
		got := usableMagnet(r)
		if strings.Contains(got, "apikey") || strings.Contains(got, fakeKey) {
			t.Fatalf("magnet leaked the API key: %q", got)
		}
		if strings.Contains(got, "192.168.") || strings.Contains(got, "localhost") {
			t.Fatalf("magnet leaked a private address: %q", got)
		}
		if got != "" && !strings.HasPrefix(got, "magnet:?") {
			t.Fatalf("emitted a non-magnet: %q", got)
		}
	}
}

func TestUsableMagnetRebuildsFromInfoHash(t *testing.T) {
	r := prowlarrResult{
		Title:     "ubuntu-24.04.1-desktop-amd64.iso",
		InfoHash:  "4A3F5E08BCEF825718EDA30637230585E3330599",
		MagnetURL: "http://192.168.0.115:9696/2/download?apikey=" + fakeKey,
	}
	got := usableMagnet(r)
	if !strings.HasPrefix(got, "magnet:?xt=urn:btih:4a3f5e08bcef825718eda30637230585e3330599") {
		t.Fatalf("infohash not used: %q", got)
	}
	if !strings.Contains(got, "tr=udp") {
		t.Error("rebuilt magnet has no trackers; it would find no peers")
	}
}

func TestUsableMagnetPrefersRealMagnet(t *testing.T) {
	real := "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel"
	r := prowlarrResult{GUID: real, InfoHash: "4A3F5E08BCEF825718EDA30637230585E3330599"}
	got := usableMagnet(r)

	// The real magnet's identity must be preserved -- its infohash, not the
	// one we would have rebuilt from the infoHash field.
	if !strings.HasPrefix(got, real) {
		t.Errorf("got %q, want it to start with the real magnet from guid", got)
	}
	if strings.Contains(got, "4a3f5e08") {
		t.Error("rebuilt hash leaked into a magnet that already had one")
	}
	// Trackers are appended so the browser has somewhere to announce.
	if !strings.Contains(got, "tr=udp") {
		t.Error("no udp trackers appended; this magnet could never find peers")
	}
}

func TestUsableMagnetRefusesUnusable(t *testing.T) {
	r := prowlarrResult{
		Title:       "No hash anywhere",
		GUID:        "https://limetorrents.fun/some-page",
		DownloadURL: "http://localhost:9696/5/download?apikey=" + fakeKey,
	}
	if got := usableMagnet(r); got != "" {
		t.Errorf("got %q, want empty -- nothing streamable here", got)
	}
}

// End-to-end guard on the actual serialized payload.
func TestBuildCardsOutputContainsNoSecrets(t *testing.T) {
	raw := []prowlarrResult{
		{
			Title: "Big.Buck.Bunny.2008.1080p.WEB-DL.x264-GRP", Protocol: "torrent",
			Seeders: 12, Size: 1 << 30, Indexer: "The Pirate Bay",
			InfoHash:  "C39FE3EEFBDB62DA9C27EB6398FF4A7D2E26E7AB",
			MagnetURL: "http://192.168.0.115:9696/2/download?apikey=" + fakeKey,
		},
	}
	cards := buildCards(raw)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	blob, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(blob)
	for _, forbidden := range []string{fakeKey, "apikey", "192.168.", "localhost", "9696"} {
		if strings.Contains(payload, forbidden) {
			t.Errorf("serialized response contains %q:\n%s", forbidden, payload)
		}
	}
}

func TestBuildCardsSkipsUsenetAndUnstreamable(t *testing.T) {
	raw := []prowlarrResult{
		{Title: "Movie.2020.1080p", Protocol: "usenet", InfoHash: "C39FE3EEFBDB62DA9C27EB6398FF4A7D2E26E7AB"},
		{Title: "Movie.2021.1080p", Protocol: "torrent", GUID: "https://example.com/page"},
	}
	if got := buildCards(raw); len(got) != 0 {
		t.Errorf("got %d cards, want 0 -- neither is streamable", len(got))
	}
}

// A magnet with no udp trackers can never find peers from a browser, however
// many seeders the indexer reports.
func TestEveryEmittedMagnetHasUdpTrackers(t *testing.T) {
	cases := []prowlarrResult{
		{GUID: "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel"},
		{GUID: "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&tr=http%3A%2F%2Fx%2Fannounce"},
		{Title: "rebuilt", InfoHash: "4A3F5E08BCEF825718EDA30637230585E3330599"},
	}
	for i, r := range cases {
		got := usableMagnet(r)
		if !strings.Contains(got, "tr=udp") {
			t.Errorf("case %d has no udp tracker, cannot find peers: %q", i, got)
		}
	}
}

func TestWithDefaultTrackersDoesNotDuplicate(t *testing.T) {
	base := "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10" +
		"&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce"
	got := withDefaultTrackers(base)
	if n := strings.Count(got, "tracker.opentrackr.org"); n != 1 {
		t.Errorf("opentrackr appears %d times, want 1", n)
	}
}
