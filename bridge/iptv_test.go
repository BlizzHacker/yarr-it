package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// testServer mirrors main.go's real defaults (per-ip=40, global=800) so that
// tests exercising validation logic (bad target, SSRF, sub-cap) are not
// incidentally refused by the concurrency gate added to handleIPTV.
func testServer(t *testing.T, capBytes int64) *server {
	t.Helper()
	return &server{
		lim:        limits{perIP: 40, global: 800},
		perIP:      make(map[string]int),
		budget:     newBudget(filepath.Join(t.TempDir(), "m.json"), 1<<40),
		iptvBudget: newBudget(filepath.Join(t.TempDir(), "i.json"), capBytes),
	}
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

// iptvCheckRedirect must cap the hop count the same way tracker.go's
// announceClient does (len(via) > 3). CheckRedirect entirely replaces Go's
// default 10-redirect limit, and the iptv client has Timeout: 0, so without
// this an attacker-controlled upstream that keeps redirecting to itself would
// loop forever, uncharged.
func TestIPTVCheckRedirectCapsHopCount(t *testing.T) {
	req := httptest.NewRequest("GET", "http://1.1.1.1/x.m3u8", nil)

	// One prior redirect: still within the cap tracker.go uses.
	if err := iptvCheckRedirect(req, make([]*http.Request, 1)); err != nil {
		t.Errorf("2nd hop: want nil, got %v", err)
	}

	// Four prior redirects (a 4th+ hop): tracker.go refuses at len(via) > 3.
	if err := iptvCheckRedirect(req, make([]*http.Request, 4)); err != http.ErrUseLastResponse {
		t.Errorf("4th+ hop: want ErrUseLastResponse, got %v", err)
	}
}

// A stream that trips the sub-cap partway through must be cut off, not run to
// completion. Before the fix, io.Copy only reported bytes moved after the
// reader was exhausted, so degraded() never fired while a stream was live.
//
// This drives copyIPTV directly (as iptv.go's own comments note is the
// pattern for exercising CheckRedirect-shaped logic in isolation) rather than
// through handleIPTV end-to-end, because handleIPTV's SSRF guard
// (resolvesPublic) refuses loopback targets like an httptest.Server -- and
// that guard must not be weakened or bypassed for testing.
func TestIPTVCopyStopsWhenSubCapTripsMidStream(t *testing.T) {
	const chunk = iptvCopyBufSize
	upstream := strings.Repeat("x", 10*chunk) // far more than the cap allows through

	s := &server{
		budget:     newBudget(filepath.Join(t.TempDir(), "m.json"), 1<<40),
		iptvBudget: newBudget(filepath.Join(t.TempDir(), "i.json"), 100000),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/bridge/iptv?u=http://example.invalid/x.m3u8", nil)
	s.copyIPTV(rec, req, strings.NewReader(upstream))

	if rec.Body.Len() >= len(upstream) {
		t.Fatalf("stream ran to completion (%d bytes) instead of being cut off at the cap", rec.Body.Len())
	}
	if used, cap_ := s.iptvBudget.snapshot(); used < cap_ {
		t.Errorf("iptv budget used=%d cap=%d, want used >= cap after a mid-stream cutoff", used, cap_)
	}
}

// IPTV bytes must land on both budgets: the sub-budget that independently
// caps IPTV, and the main budget, since IPTV traffic is carved out of the
// shared monthly allowance rather than additive to it.
func TestIPTVCopyChargesBothBudgetsAtDoubleTheByteCount(t *testing.T) {
	const data = "hello iptv stream, this is upstream media data"

	s := &server{
		budget:     newBudget(filepath.Join(t.TempDir(), "m.json"), 1<<40),
		iptvBudget: newBudget(filepath.Join(t.TempDir(), "i.json"), 1<<40),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/bridge/iptv?u=http://example.invalid/x.m3u8", nil)
	s.copyIPTV(rec, req, strings.NewReader(data))

	want := int64(len(data)) * 2
	if used, _ := s.budget.snapshot(); used != want {
		t.Errorf("main budget used=%d, want %d", used, want)
	}
	if used, _ := s.iptvBudget.snapshot(); used != want {
		t.Errorf("iptv budget used=%d, want %d", used, want)
	}
}

// When the concurrency gate refuses (the same s.acquire(ip) mechanism
// handleSocket uses), handleIPTV must return 429 and must never contact the
// upstream -- otherwise the sub-cap and gate are decorative and the endpoint
// remains a free anonymising proxy for anyone who floods it with concurrent
// requests.
func TestIPTVReturns429WhenAcquireRefusesAndDoesNotContactUpstream(t *testing.T) {
	hit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer upstream.Close()

	s := testServer(t, 1<<30)
	s.lim = limits{perIP: 1, global: 1}
	const ip = "203.0.113.5"
	s.perIP[ip] = 1 // the one available slot is already taken

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/bridge/iptv?u="+upstream.URL, nil)
	req.RemoteAddr = ip + ":54321"
	s.handleIPTV(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}
	if hit {
		t.Error("upstream was contacted despite the concurrency gate refusing the request")
	}
}
