package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testAuth() *authConfig {
	return &authConfig{Secret: []byte("test-secret"), ClientID: "x", ClientSecret: "y", Enabled: true}
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
	if _, ok := c.decode(v[:len(v)-1] + "0"); ok {
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
