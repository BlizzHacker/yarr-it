package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The adapters ship registerArrRoutes and RegisterRoutes, which register their
// handlers unwrapped. That is right for the packages that own them and wrong
// for this server, so main.go registers each route individually with the gate
// it needs.
//
// This test exists because the convenient thing -- calling the one-line
// register helper -- silently opens three routes that spend the household's
// disk and expose what it owns. The failure is invisible: everything works, and
// works for strangers too.

// buildTestMux mirrors main.go's registration for the routes under test.
func buildTestMux(auth *authConfig, reg *Registry) *http.ServeMux {
	mux := http.NewServeMux()
	arr := &arrAPI{reg: reg}
	mux.HandleFunc("/api/arr/search", publicCORS(arr.handleSearch))
	mux.HandleFunc("/api/arr/details", publicCORS(arr.handleDetails))
	mux.HandleFunc("/api/arr/library", auth.requireUser(
		func(w http.ResponseWriter, r *http.Request, _ string) { arr.handleLibrary(w, r) }))
	mux.HandleFunc("/api/arr/status", auth.requireUser(
		func(w http.ResponseWriter, r *http.Request, _ string) { arr.handleStatus(w, r) }))
	mux.HandleFunc("/api/arr/request", auth.requireUser(
		func(w http.ResponseWriter, r *http.Request, _ string) { arr.handleRequest(w, r) }))
	return mux
}

// Spending someone's bandwidth, and listing what they own, must require a
// session. An anonymous caller gets a 401 or a 503, never the data.
func TestHouseholdArrRoutesRefuseAnonymousCallers(t *testing.T) {
	mux := buildTestMux(testAuth(), &Registry{})

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/arr/library?domain=video"},
		{"GET", "/api/arr/status?id=tmdb:movie:78"},
		{"POST", "/api/arr/request"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

		if rec.Code == http.StatusOK {
			t.Errorf("%s %s answered 200 with no session; it is registered bare", tc.method, tc.path)
		}
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s returned %d, want 401 or 503", tc.method, tc.path, rec.Code)
		}
	}
}

// The other two describe things that exist in the world rather than anything
// this household has, and a television reads them from another origin.
func TestArrCatalogueRoutesAreReadableCrossOrigin(t *testing.T) {
	mux := buildTestMux(testAuth(), &Registry{})

	for _, path := range []string{"/api/arr/search?q=dune", "/api/arr/details?id=tmdb:movie:78"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Origin", "https://someone-else.example")
		mux.ServeHTTP(rec, req)

		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s needs a session; a TV cannot browse", path)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s sent Allow-Origin %q; a TV is a different origin", path, got)
		}
	}
}

// A preflight must be answered, or the browser never sends the real request.
func TestArrCataloguePreflightIsAnswered(t *testing.T) {
	mux := buildTestMux(testAuth(), &Registry{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/arr/search", nil)
	req.Header.Set("Origin", "https://someone-else.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight got %d, want 204", rec.Code)
	}
}

// The household routes must not carry CORS headers at all. A wildcard origin
// cannot carry credentials -- the browser refuses the pairing -- so leaving
// them uncovered is what stops a session ever reaching another instance.
func TestHouseholdArrRoutesAreNotCORSOpen(t *testing.T) {
	mux := buildTestMux(testAuth(), &Registry{})

	for _, path := range []string{"/api/arr/library", "/api/arr/status", "/api/arr/request"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Origin", "https://someone-else.example")
		mux.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s sent Allow-Origin %q; household data must stay same-origin", path, got)
		}
	}
}
