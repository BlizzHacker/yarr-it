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
// register helper -- silently opens routes that spend the household's disk and
// expose what it owns. The failure is invisible: everything works, and works
// for strangers too.
//
// WHAT CHANGED, AND WHY THE SPLIT IN HERE IS GONE
//
// This file used to assert that two of the five *arr routes were public: search
// and details "describe things that exist in the world". That was true of the
// English and false of the JSON. Every MediaItem those two return carries a
// State -- this household's answer to "do I have this" -- and the ProviderID of
// the instance that answered. A stranger could walk a film list through
// /api/arr/search and read the shelf a title at a time without ever touching
// /api/arr/library, which was the route everyone was watching.
//
// So all five are behind the owner boundary now, and the assertions below have
// been inverted rather than deleted: the old ones are still here in negative
// form, because "these two are readable cross-origin" is exactly the claim that
// must never quietly become true again.

// buildTestMux mirrors main.go's registration for the routes under test.
func buildTestMux(auth *authConfig, reg *Registry) *http.ServeMux {
	mux := http.NewServeMux()
	arr := &arrAPI{reg: reg}
	mux.HandleFunc("/api/arr/search", auth.requireOwner(arr.handleSearch))
	mux.HandleFunc("/api/arr/details", auth.requireOwner(arr.handleDetails))
	mux.HandleFunc("/api/arr/library", auth.requireOwner(arr.handleLibrary))
	mux.HandleFunc("/api/arr/status", auth.requireOwner(arr.handleStatus))
	mux.HandleFunc("/api/arr/request", auth.requireOwner(arr.handleRequest))
	return mux
}

// arrRoutePaths is every route this file covers, in the shape a caller would
// actually use it.
var arrRoutePaths = []string{
	"/api/arr/search?q=dune",
	"/api/arr/details?id=tmdb:movie:78",
	"/api/arr/library?domain=video",
	"/api/arr/status?id=tmdb:movie:78",
	"/api/arr/request",
}

// Listing what a household owns, asking whether it owns one thing, and spending
// its bandwidth all require being the household.
func TestArrRoutesRefuseAnonymousCallers(t *testing.T) {
	mux := buildTestMux(ownerAuth(), &Registry{})

	for _, path := range arrRoutePaths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))

		if rec.Code != http.StatusNotFound {
			t.Errorf("anonymous GET %s returned %d, want 404", path, rec.Code)
		}
	}
}

// The claim this file is really about: being signed in is not being the owner.
// Every one of these answered 200 to any Authentik account before the boundary
// existed.
func TestArrRoutesRefuseSignedInStrangers(t *testing.T) {
	c := ownerAuth()
	mux := buildTestMux(c, &Registry{})

	for _, path := range arrRoutePaths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, strangerRequest(t, c, "GET", path, ""))

		if rec.Code != http.StatusNotFound {
			t.Errorf("signed-in stranger GET %s returned %d, want 404", path, rec.Code)
		}
	}
}

// None of them may carry CORS headers -- not even the two that used to. A
// wildcard origin cannot carry credentials, the browser refuses the pairing, so
// leaving these uncovered is what stops a session ever reaching another
// instance.
func TestArrRoutesAreNotCORSOpen(t *testing.T) {
	c := ownerAuth()
	mux := buildTestMux(c, &Registry{})

	for _, path := range arrRoutePaths {
		for _, req := range []*http.Request{
			httptest.NewRequest("GET", path, nil),
			ownerRequest(t, c, "GET", path, ""),
		} {
			req.Header.Set("Origin", "https://someone-else.example")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("%s sent Allow-Origin %q; household data stays same-origin", path, got)
			}
		}
	}
}

// A preflight is not a way around the gate. It used to be answered 204 by
// publicCORS before the handler ran; now there is no publicCORS on these, and
// an OPTIONS gets the same constant refusal as everything else.
func TestArrPreflightDoesNotBypassTheGate(t *testing.T) {
	mux := buildTestMux(ownerAuth(), &Registry{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/arr/search", nil)
	req.Header.Set("Origin", "https://someone-else.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("an OPTIONS preflight got %d, want the same 404 as everything else", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("the preflight was answered with Allow-Origin %q", got)
	}
}

// And the owner gets through all five, or the boundary has simply broken the
// feature.
//
// Compared against the gate's exact body rather than the status: with an empty
// registry /api/arr/details answers its own 404 -- "no configured provider can
// describe tmdb:movie:78" -- which is the handler having run, not the gate
// having refused.
func TestTheOwnerReachesEveryArrRoute(t *testing.T) {
	c := ownerAuth()
	mux := buildTestMux(c, &Registry{})

	for _, path := range arrRoutePaths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, ownerRequest(t, c, "GET", path, ""))

		if rec.Body.String() == gateRefusal {
			t.Errorf("the owner was refused GET %s with the stranger's constant 404", path)
		}
	}
}
