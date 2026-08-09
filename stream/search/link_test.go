package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestLinkService(t *testing.T) *linkService {
	t.Helper()
	// "yt-dlp-not-here" is deliberate: these tests exercise the HTTP surface,
	// the guard and the token, none of which should need a subprocess.
	s, err := newLinkService("yt-dlp-not-here", 5*time.Second, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.egress.Close() })
	return s
}

// ---------------------------------------------------- the endpoint guard --

// The endpoint refuses the private targets before it ever starts a
// subprocess. Same three the brief names, this time through the real handler.
func TestResolveEndpointRefusesPrivateTargets(t *testing.T) {
	s := newTestLinkService(t)
	for _, target := range []string{
		"http://127.0.0.1:8802/api/search?q=x",
		"http://192.168.0.115:9696/api/v1/indexer",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
		"gopher://192.168.0.115:6379/_INFO",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/link/resolve?u="+url.QueryEscape(target), nil)
		req.RemoteAddr = "203.0.113.9:1234"
		s.handleResolve(rec, req)

		if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
			t.Errorf("%s -> %d; want a refusal", target, rec.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["code"] == codeOK {
			t.Errorf("%s was reported as a success", target)
		}
		// The refusal must not say *why* in a way that reveals whether the
		// host exists -- that would make this a network scanner.
		if s, _ := body["error"].(string); strings.Contains(s, "connection refused") ||
			strings.Contains(s, "timeout") {
			t.Errorf("%s: refusal leaked reachability detail: %q", target, s)
		}
	}
}

func TestResolveEndpointRequiresAURL(t *testing.T) {
	s := newTestLinkService(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/link/resolve", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	s.handleResolve(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty u -> %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------- media tokens --

// Without a signature the media endpoint is a second open proxy that also
// bypasses the resolve rate limit.
func TestMediaEndpointRejectsUnsignedAndTamperedTokens(t *testing.T) {
	s := newTestLinkService(t)
	good := s.mediaURL(
		linkFormat{rawURL: "https://cdn.example.com/v.mp4", Ext: "mp4", Label: "720p"},
		&ytInfo{Title: "Clip"})
	tok := strings.TrimPrefix(good, "/api/link/media?t=")
	if tok == "" {
		t.Fatal("no token minted")
	}

	if _, err := s.openToken(tok); err != nil {
		t.Fatalf("a freshly minted token was rejected: %v", err)
	}

	bad := []string{
		"",
		"garbage",
		tok[:len(tok)-3] + "AAA",           // flipped signature
		strings.Replace(tok, ".", "x.", 1), // flipped payload
		strings.SplitN(tok, ".", 2)[0],     // payload with no signature
	}
	for _, b := range bad {
		if _, err := s.openToken(b); err == nil {
			t.Errorf("token %q was accepted", truncate(b, 30))
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/link/media?t="+b, nil)
		req.RemoteAddr = "203.0.113.9:1234"
		s.handleMedia(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("media with token %q -> %d, want 403", truncate(b, 20), rec.Code)
		}
	}
}

// A token signed by a different process must not work here. Each start
// generates its own key, so a token cannot outlive a restart or be replayed
// against another instance.
func TestMediaTokensAreNotPortableBetweenInstances(t *testing.T) {
	a, b := newTestLinkService(t), newTestLinkService(t)
	url := a.mediaURL(linkFormat{rawURL: "https://cdn.example.com/v.mp4", Ext: "mp4"}, &ytInfo{Title: "x"})
	tok := strings.TrimPrefix(url, "/api/link/media?t=")
	if _, err := b.openToken(tok); err == nil {
		t.Fatal("a token minted by one instance was accepted by another")
	}
}

func TestExpiredMediaTokenIsRefused(t *testing.T) {
	s := newTestLinkService(t)
	// Mint by hand with an expiry in the past.
	stale := s.signToken(mediaToken{URL: "https://cdn.example.com/v.mp4",
		Expires: time.Now().Add(-time.Minute).Unix()})
	if _, err := s.openToken(stale); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

// Even a validly signed token gets the address check again on use: it was
// minted up to half an hour ago and DNS can have moved.
func TestMediaRechecksTheAddressAtStreamTime(t *testing.T) {
	s := newTestLinkService(t)
	tok := s.signToken(mediaToken{URL: "http://192.168.0.115:9696/api/v1/indexer",
		Expires: time.Now().Add(time.Hour).Unix()})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/link/media?t="+tok, nil)
	req.RemoteAddr = "203.0.113.9:1234"
	s.handleMedia(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a signed token pointing at the LAN streamed with status %d", rec.Code)
	}
}

// ----------------------------------------------------------- rate limits --

func TestPerIPRateLimit(t *testing.T) {
	l := newIPLimiter(3, time.Hour) // no meaningful refill during the test
	for i := 0; i < 3; i++ {
		if !l.allow("198.51.100.7") {
			t.Fatalf("request %d was refused inside the burst", i+1)
		}
	}
	if l.allow("198.51.100.7") {
		t.Fatal("the 4th request in a 3-token bucket was allowed")
	}
	// A different visitor is unaffected.
	if !l.allow("198.51.100.8") {
		t.Fatal("one address exhausting its bucket blocked a different address")
	}
}

func TestResolveEndpointRateLimits(t *testing.T) {
	s := newTestLinkService(t)
	s.limiter = newIPLimiter(2, time.Hour)
	hit := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/link/resolve?u=https%3A%2F%2Fexample.com%2Fv", nil)
		req.RemoteAddr = "198.51.100.20:5555"
		s.handleResolve(rec, req)
		return rec.Code
	}
	hit()
	hit()
	if code := hit(); code != http.StatusTooManyRequests {
		t.Fatalf("third request -> %d, want 429", code)
	}
}

func TestRateLimitUsesForwardedAddress(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:9999" // Caddy, on the same box
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := linkClientIP(req); got != "203.0.113.5" {
		t.Fatalf("linkClientIP = %q; behind a proxy every visitor would share one bucket", got)
	}
}

func TestStreamGateCapsPerAddressAndGlobally(t *testing.T) {
	g := newStreamGate(3, 2)
	r1, ok := g.acquire("a")
	if !ok {
		t.Fatal("first stream refused")
	}
	if _, ok := g.acquire("a"); !ok {
		t.Fatal("second stream from the same address refused")
	}
	if _, ok := g.acquire("a"); ok {
		t.Fatal("third concurrent stream from one address was allowed past the per-IP cap of 2")
	}
	if _, ok := g.acquire("b"); !ok {
		t.Fatal("a different address was blocked by another's usage")
	}
	r1()
	if _, ok := g.acquire("a"); !ok {
		t.Fatal("releasing a stream did not free the slot")
	}
}

// ------------------------------------------------------------ filenames --

func TestDownloadNameIsSafe(t *testing.T) {
	cases := []struct{ title, want string }{
		{"Big Buck Bunny", "Big Buck Bunny [720p].mp4"},
		{"../../etc/passwd", "-..-etc-passwd [720p].mp4"},
		{`a"b<c>d|e`, "a-b-c-d-e [720p].mp4"},
		{"with\r\nCRLF: injected", "withCRLF- injected [720p].mp4"},
		{"", "download [720p].mp4"},
	}
	f := linkFormat{Label: "720p", Ext: "mp4"}
	for _, tc := range cases {
		got := downloadName(tc.title, f)
		if got != tc.want {
			t.Errorf("downloadName(%q) = %q, want %q", tc.title, got, tc.want)
		}
		// A header value with a newline in it is a response-splitting bug.
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("downloadName(%q) kept a newline: %q", tc.title, got)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("downloadName(%q) kept a path separator: %q", tc.title, got)
		}
	}
}

func TestDownloadNameIsBounded(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := downloadName(long, linkFormat{Label: "1080p", Ext: "mp4"})
	if len(got) > 160 {
		t.Fatalf("filename is %d bytes; some filesystems cap at 255 and the header has to fit too", len(got))
	}
}

// ---------------------------------------------------------------- notes --

func TestNotesSayTheQuietPartOutLoud(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Duration: 100, Formats: []ytFormat{
		{FormatID: "silent", Ext: "mp4", Height: 2160, VCodec: "av01", ACodec: "none", URL: "https://x/1"},
		{FormatID: "ok", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2", URL: "https://x/2"},
		{FormatID: "hls", Ext: "mp4", Height: 480, VCodec: "avc1", ACodec: "mp4a.40.2",
			Protocol: "m3u8_native", URL: "https://x/3.m3u8"},
	}})
	notes := strings.Join(formatNotes(fs, bestFormat(fs, true)), " | ")
	for _, want := range []string{"no audio track", "highest quality with sound is 720p", "segmented streams"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q; got: %s", want, notes)
		}
	}
}

func TestNotesAreSilentWhenThereIsNothingToWarnAbout(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Formats: []ytFormat{
		{FormatID: "ok", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2",
			Filesize: 1 << 20, URL: "https://x/2"},
	}})
	if n := formatNotes(fs, 0); len(n) != 0 {
		t.Fatalf("a clean result produced warnings: %v", n)
	}
}

// ------------------------------------------------------------ responses --

func TestPlaylistBecomesACollection(t *testing.T) {
	s := newTestLinkService(t)
	entries := make([]ytInfo, 80)
	for i := range entries {
		entries[i] = ytInfo{ID: "id", Title: "Track", WebpageURL: "https://example.com/v"}
	}
	resp := s.buildResponse(t.Context(), "https://example.com/list",
		&ytInfo{Type: "playlist", Title: "A list", Entries: entries})
	if resp.Kind != "collection" {
		t.Fatalf("kind = %q, want collection", resp.Kind)
	}
	if len(resp.Items) != maxPlaylist {
		t.Fatalf("items = %d, want the cap of %d", len(resp.Items), maxPlaylist)
	}
	if len(resp.Notes) == 0 || !strings.Contains(resp.Notes[0], "80") {
		t.Errorf("truncation was not disclosed: %v", resp.Notes)
	}
}

func TestEveryFormatGetsAPlayableMediaURL(t *testing.T) {
	s := newTestLinkService(t)
	resp := s.buildResponse(t.Context(), "https://example.com/v", &ytInfo{
		Title: "Clip", Extractor: "example",
		Formats: []ytFormat{
			{FormatID: "a", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2",
				Filesize: 1 << 20, URL: "https://cdn.example.com/a.mp4"},
		},
	})
	if len(resp.Formats) != 1 {
		t.Fatalf("got %d formats", len(resp.Formats))
	}
	if !strings.HasPrefix(resp.Formats[0].Media, "/api/link/media?t=") {
		t.Fatalf("media URL = %q; the player and the download button both need this",
			resp.Formats[0].Media)
	}
	// The raw CDN URL must never reach the browser: it is signed, expiring,
	// and often carries the resolver's own address in the signature.
	body, _ := json.Marshal(resp)
	if strings.Contains(string(body), "cdn.example.com") {
		t.Error("the raw CDN URL was serialised to the client")
	}
}

func TestStatusCodesAreMeaningful(t *testing.T) {
	cases := map[string]int{
		codeUnsupported:   http.StatusNotFound,
		codeLoginRequired: http.StatusForbidden,
		codeNoVideoInPost: http.StatusUnprocessableEntity,
		codeRateLimited:   http.StatusTooManyRequests,
		codeTimeout:       http.StatusGatewayTimeout,
		codeToolMissing:   http.StatusServiceUnavailable,
	}
	for code, want := range cases {
		if got := statusFor(code); got != want {
			t.Errorf("statusFor(%s) = %d, want %d", code, got, want)
		}
	}
}

func TestRedactURLKeepsShareTokensOutOfTheLog(t *testing.T) {
	got := redactURL("https://www.instagram.com/reel/ABC/?igsh=SECRETSHARETOKEN")
	if strings.Contains(got, "SECRETSHARETOKEN") {
		t.Fatalf("redactURL leaked a share token: %s", got)
	}
}
