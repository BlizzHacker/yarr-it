package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testAuth() *authConfig {
	return &authConfig{
		Secret: []byte("test-secret"), ClientID: "x", ClientSecret: "y",
		Enabled: true, Scope: scopeAll,
	}
}

func TestSessionRoundTrips(t *testing.T) {
	c := testAuth()
	v, err := c.encode(session{User: "wade", Email: "w@example.com", Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.decode(v)
	if !ok || got.User != "wade" {
		t.Fatalf("did not round trip: %+v ok=%v", got, ok)
	}
}

// The signature is the whole security property: without this the cookie is a
// suggestion.
func TestTamperedSessionRejected(t *testing.T) {
	c := testAuth()
	v, _ := c.encode(session{User: "wade", Exp: time.Now().Add(time.Hour).Unix()})
	// Flip the last character to one it certainly is not. Substituting a fixed
	// digit tampers with nothing on the roughly one run in sixteen where the
	// signature already ends in it, and the test passes for the wrong reason.
	tampered := "0"
	if strings.HasSuffix(v, "0") {
		tampered = "1"
	}
	if _, ok := c.decode(v[:len(v)-1] + tampered); ok {
		t.Fatal("a tampered signature was accepted")
	}
	// A cookie signed with someone else's secret must not be honoured either.
	other := &authConfig{Secret: []byte("different")}
	forged, _ := other.encode(session{User: "attacker", Exp: time.Now().Add(time.Hour).Unix()})
	if _, ok := c.decode(forged); ok {
		t.Fatal("a cookie signed with a foreign secret was accepted")
	}
}

// The expiry lives inside the signature, so it cannot be extended by editing
// the cookie.
func TestExpiredSessionRejected(t *testing.T) {
	c := testAuth()
	v, _ := c.encode(session{User: "wade", Exp: time.Now().Add(-time.Minute).Unix()})
	if _, ok := c.decode(v); ok {
		t.Fatal("an expired session was accepted")
	}
}

func TestApiRequiresSignIn(t *testing.T) {
	c := testAuth()
	reached := false
	h := c.requireAuth(func(w http.ResponseWriter, r *http.Request) { reached = true })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/search?q=x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request got %d, want 401", rec.Code)
	}
	if reached {
		t.Fatal("the handler ran for an unauthenticated request")
	}

	v, _ := c.encode(session{User: "wade", Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest("GET", "/api/search?q=x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
	rec = httptest.NewRecorder()
	h(rec, req)
	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("a signed-in request was blocked: code=%d reached=%v", rec.Code, reached)
	}
}

// A half-configured gate that rejects everyone looks like an outage and gets
// switched off, so an unconfigured build stays open rather than broken.
func TestUnconfiguredAuthDoesNotBlock(t *testing.T) {
	c := &authConfig{Enabled: false}
	reached := false
	h := c.requireAuth(func(w http.ResponseWriter, r *http.Request) { reached = true })
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/search", nil))
	if !reached {
		t.Fatal("an unconfigured gate blocked a request")
	}
}

// Caddy asks this before serving the site.
func TestVerifyEndpoint(t *testing.T) {
	c := testAuth()
	rec := httptest.NewRecorder()
	c.handleVerify(rec, httptest.NewRequest("GET", "/auth/verify", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("verify without a session got %d, want 401", rec.Code)
	}

	v, _ := c.encode(session{User: "wade", Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest("GET", "/auth/verify", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
	rec = httptest.NewRecorder()
	c.handleVerify(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Yarrit-User") != "wade" {
		t.Fatalf("verify with a session got %d user=%q", rec.Code, rec.Header().Get("X-Yarrit-User"))
	}
}

// An absolute `next` is an open redirect, which is how a sign-in page becomes
// a phishing hop.
func TestLoginRefusesOffsiteNext(t *testing.T) {
	c := testAuth()
	rec := httptest.NewRecorder()
	c.handleLogin(rec, httptest.NewRequest("GET", "/auth/login?next=https://evil.example/x", nil))
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "yarrit_next" && ck.Value != "/" {
			t.Fatalf("offsite next was kept: %q", ck.Value)
		}
	}
}

// --- gate scope -----------------------------------------------------------
//
// The gate exists so the TV apps can show an account behind every viewer. It
// is not meant to stand between a browser and the site, because that is who we
// hand the URL to for testing.

func served(c *authConfig, req *http.Request) int {
	rec := httptest.NewRecorder()
	c.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})(rec, req)
	return rec.Code
}

func TestTVScopeLetsBrowsersThrough(t *testing.T) {
	c := testAuth()
	c.Scope = scopeTV
	req := httptest.NewRequest("GET", "/api/search?q=x", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if got := served(c, req); got != http.StatusOK {
		t.Fatalf("browser was gated under TV scope: got %d, want 200", got)
	}
}

func TestTVScopeGatesDeclaredTVDevices(t *testing.T) {
	c := testAuth()
	c.Scope = scopeTV
	for device := range tvDevices {
		req := httptest.NewRequest("GET", "/api/search?q=x&device="+device, nil)
		if got := served(c, req); got != http.StatusUnauthorized {
			t.Errorf("device=%s was not gated: got %d, want 401", device, got)
		}
	}
}

// A TV app that forgets ?device= must not fall through to the open path.
func TestTVScopeGatesRokuByUserAgent(t *testing.T) {
	c := testAuth()
	c.Scope = scopeTV
	req := httptest.NewRequest("GET", "/api/search?q=x", nil)
	req.Header.Set("User-Agent", "Roku/DVP-13.0 (13.0.0.4183-88)")
	if got := served(c, req); got != http.StatusUnauthorized {
		t.Fatalf("Roku user-agent was not gated: got %d, want 401", got)
	}
}

// A signed-in TV is exactly what the gate is asking for, and must be let in.
func TestTVScopeAdmitsSignedInTV(t *testing.T) {
	c := testAuth()
	c.Scope = scopeTV
	v, _ := c.encode(session{User: "wade", Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest("GET", "/api/search?q=x&device=roku", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
	if got := served(c, req); got != http.StatusOK {
		t.Fatalf("signed-in TV was refused: got %d, want 200", got)
	}
}

func TestScopeOffOpensEverything(t *testing.T) {
	c := testAuth()
	c.Scope = scopeOff
	req := httptest.NewRequest("GET", "/api/search?q=x&device=roku", nil)
	if got := served(c, req); got != http.StatusOK {
		t.Fatalf("scope off still gated a TV: got %d, want 200", got)
	}
}

// A typo in AUTH_SCOPE must not lock out every visitor.
func TestUnknownScopeFallsBackToTV(t *testing.T) {
	t.Setenv("SSO_CLIENT_ID", "x")
	t.Setenv("SSO_CLIENT_SECRET", "y")
	for _, raw := range []string{"", "everyone", "ALL "} {
		t.Setenv("AUTH_SCOPE", raw)
		got := loadAuthConfig().Scope
		want := scopeTV
		if raw == "ALL " {
			want = scopeAll // trimmed and lower-cased, so this one is valid
		}
		if got != want {
			t.Errorf("AUTH_SCOPE=%q gave scope %q, want %q", raw, got, want)
		}
	}
}
