package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func resetBearerCache() {
	bearerMu.Lock()
	bearerCache = map[string]bearerEntry{}
	bearerMu.Unlock()
}

// A fake Authentik that accepts exactly one token.
func fakeIDP(t *testing.T, good string, hits *int32) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if hits != nil {
			*hits++
		}
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+good {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"sub": "abc", "preferred_username": "wade", "email": "w@example.com",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func bearerAuth(srv *httptest.Server) *authConfig {
	c := testAuth()
	// Issuer is shaped like Authentik's, so userinfoURL() has something real
	// to derive from.
	c.Issuer = srv.URL + "/application/o/yarrit-web/"
	return c
}

func withToken(tok string) *http.Request {
	r := httptest.NewRequest("GET", "/api/search?q=x&device=roku", nil)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

// The whole point: a TV holds a token, not a cookie.
func TestBearerTokenIsAcceptedAsASession(t *testing.T) {
	resetBearerCache()
	srv := fakeIDP(t, "good-token", nil)
	c := bearerAuth(srv)

	s, ok := c.sessionFrom(withToken("good-token"))
	if !ok {
		t.Fatal("a valid bearer token was refused; the TV signs in and is then locked out")
	}
	if s.User != "wade" {
		t.Errorf("user is %q, want wade", s.User)
	}
}

func TestBadBearerTokenIsRefused(t *testing.T) {
	resetBearerCache()
	srv := fakeIDP(t, "good-token", nil)
	c := bearerAuth(srv)

	if _, ok := c.sessionFrom(withToken("wrong")); ok {
		t.Fatal("an invalid token was accepted")
	}
	if _, ok := c.sessionFrom(withToken("")); ok {
		t.Fatal("a request with no credential was accepted")
	}
}

// Browsing a TV UI fires many requests; each must not re-ask the IdP.
func TestValidTokensAreCached(t *testing.T) {
	resetBearerCache()
	var hits int32
	srv := fakeIDP(t, "good-token", &hits)
	c := bearerAuth(srv)

	for i := 0; i < 5; i++ {
		if _, ok := c.sessionFrom(withToken("good-token")); !ok {
			t.Fatalf("call %d was refused", i)
		}
	}
	if hits != 1 {
		t.Errorf("asked the identity provider %d times for 5 requests, want 1", hits)
	}
}

// A rejection must NOT be cached: a TV that has just been approved would
// otherwise stay locked out for the whole TTL.
func TestRejectionsAreNotCached(t *testing.T) {
	resetBearerCache()
	var hits int32
	srv := fakeIDP(t, "good-token", &hits)
	c := bearerAuth(srv)

	c.sessionFrom(withToken("not-yet-valid"))
	c.sessionFrom(withToken("not-yet-valid"))
	if hits != 2 {
		t.Errorf("a failed check was cached (%d calls for 2 attempts)", hits)
	}
}

// The cookie path must not have gained a network round trip.
func TestCookieStillWorksWithoutTouchingTheIdP(t *testing.T) {
	resetBearerCache()
	var hits int32
	srv := fakeIDP(t, "good-token", &hits)
	c := bearerAuth(srv)

	v, _ := c.encode(session{User: "browser", Exp: time.Now().Add(time.Hour).Unix()})
	r := httptest.NewRequest("GET", "/api/search?q=x", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})

	s, ok := c.sessionFrom(r)
	if !ok || s.User != "browser" {
		t.Fatalf("cookie session broke: %+v ok=%v", s, ok)
	}
	if hits != 0 {
		t.Errorf("a cookie request called the identity provider %d times", hits)
	}
}

func TestUserinfoURLIsDerivedFromTheIssuer(t *testing.T) {
	c := testAuth()
	c.Issuer = "https://auth.yarrit.com/application/o/yarrit-web/"
	got := c.userinfoURL()
	want := "https://auth.yarrit.com/application/o/userinfo/"
	if got != want {
		t.Errorf("userinfo URL is %q, want %q", got, want)
	}
	if strings.Contains(got, "yarrit-web") {
		t.Error("the application slug leaked into the userinfo endpoint")
	}
}
