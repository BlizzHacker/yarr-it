package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A self-hoster's client is served from their origin and must be able to read
// a catalogue from any instance, including ours.
func TestCatalogueIsReadableFromAnotherOrigin(t *testing.T) {
	h := publicCORS(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/search?q=x", nil)
	req.Header.Set("Origin", "https://someone-else.example")
	h(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin is %q; another instance cannot read this", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}

// A preflight must be answered without running the handler behind it.
func TestPreflightIsAnsweredWithoutReachingTheHandler(t *testing.T) {
	reached := false
	h := publicCORS(func(w http.ResponseWriter, r *http.Request) { reached = true })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/search", nil)
	req.Header.Set("Origin", "https://someone-else.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	h(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight got %d, want 204", rec.Code)
	}
	if reached {
		t.Error("the preflight ran the real handler; an OPTIONS is not a search")
	}
	if rec.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Error("Authorization is not allowed, so a TV token cannot be sent cross-origin")
	}
}

// The security property that makes the wildcard safe: a shared cache must not
// hand one origin's response to another.
func TestVaryOriginIsSet(t *testing.T) {
	h := publicCORS(func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Header().Get("Vary") != "Origin" {
		t.Errorf("Vary is %q, want Origin", rec.Header().Get("Vary"))
	}
}

// Personal data must never gain these headers. A wildcard origin cannot carry
// credentials -- the browser refuses -- so keeping the library uncovered is
// what stops a cookie ever reaching another instance.
func TestPersonalEndpointsAreNotWrapped(t *testing.T) {
	s, c := libServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/library", nil)
	req.Header.Set("Origin", "https://someone-else.example")

	c.requireUser(s.handleLibrary)(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("the library sent Allow-Origin %q; personal data must stay same-origin", got)
	}
}
