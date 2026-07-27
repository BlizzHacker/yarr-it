package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func testServer(t *testing.T, capBytes int64) *server {
	t.Helper()
	return &server{iptvBudget: newBudget(filepath.Join(t.TempDir(), "i.json"), capBytes)}
}

// The box runs a WireGuard tunnel into the home estate, so a proxy that will
// fetch any URL is a route into the LAN. This must never regress.
func TestIPTVRefusesPrivateAddresses(t *testing.T) {
	s := testServer(t, 1<<30)
	for _, target := range []string{
		"http://192.168.0.115:9696/x.m3u8",
		"http://127.0.0.1:8801/x.m3u8",
		"http://10.0.0.5/x.m3u8",
		"http://[::1]/x.m3u8",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/bridge/iptv?u="+target, nil)
		s.handleIPTV(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d", target, rec.Code)
		}
	}
}

func TestIPTVRejectsMissingOrNonHTTPTarget(t *testing.T) {
	s := testServer(t, 1<<30)
	for _, q := range []string{"", "u=", "u=file:///etc/passwd", "u=gopher://x/1"} {
		rec := httptest.NewRecorder()
		s.handleIPTV(rec, httptest.NewRequest("GET", "/bridge/iptv?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: want 400, got %d", q, rec.Code)
		}
	}
}

// Exhausting the sub-cap must refuse new IPTV work. This is the failure that
// would otherwise drain the month and take the mail edge down with it.
func TestIPTVRefusesWhenSubCapExhausted(t *testing.T) {
	s := testServer(t, 100)
	s.iptvBudget.add(100)

	rec := httptest.NewRecorder()
	s.handleIPTV(rec, httptest.NewRequest("GET", "/bridge/iptv?u=http://example.com/a.m3u8", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 at the cap, got %d", rec.Code)
	}
}

// The upstream can pass the initial resolvesPublic check and still redirect
// into the LAN on a later hop -- e.g. http://example.com/x redirecting to
// http://192.168.0.115/. iptvCheckRedirect is the exact function wired into
// the client as CheckRedirect, so exercising it directly proves the guard
// re-fires on every hop, not just before the first request.
//
// IP literals are used for the hosts here (rather than names) so this stays
// a deterministic unit test: net.LookupIP short-circuits on an IP literal
// without touching the network, matching the pattern already used by
// TestResolvesPublicRefusesPrivateHosts in tracker_test.go.
func TestIPTVCheckRedirectRefusesPrivateHop(t *testing.T) {
	for _, target := range []string{
		"http://192.168.0.115:9696/x.m3u8",
		"http://127.0.0.1:8801/x.m3u8",
		"http://10.0.0.5/x.m3u8",
	} {
		req := httptest.NewRequest("GET", target, nil)
		if err := iptvCheckRedirect(req, nil); err != http.ErrUseLastResponse {
			t.Errorf("%s: want ErrUseLastResponse for a redirect hop into private space, got %v", target, err)
		}
	}
}

func TestIPTVCheckRedirectAllowsPublicHop(t *testing.T) {
	req := httptest.NewRequest("GET", "http://1.1.1.1/x.m3u8", nil)
	if err := iptvCheckRedirect(req, nil); err != nil {
		t.Errorf("want nil for a redirect hop that stays public, got %v", err)
	}
}
