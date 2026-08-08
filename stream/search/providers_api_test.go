package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

type sickProvider struct {
	fakeProvider
	panics bool
	hangs  bool
	state  HealthState
}

func (p sickProvider) Health(ctx context.Context) Health {
	if p.panics {
		panic("this adapter is broken")
	}
	if p.hangs {
		<-ctx.Done() // Held until the caller's timeout fires.
		return Health{State: HealthUnreachable}
	}
	return Health{State: p.state}
}

type activityProvider struct {
	fakeProvider
	items []ActivityItem
	err   error
	panic bool
}

func (p activityProvider) Activity(context.Context) ([]ActivityItem, error) {
	if p.panic {
		panic("boom")
	}
	return p.items, p.err
}

func providerServer(t *testing.T, ps ...Provider) *server {
	t.Helper()
	r := &Registry{}
	for _, p := range ps {
		if err := r.Add(p); err != nil {
			t.Fatalf("registering %s: %v", p.ID(), err)
		}
	}
	// Owned, and probed as the owner. /api/providers is filtered by audience
	// now, so a test that asked anonymously would assert the empty answer and
	// prove nothing about the fan-out these tests are actually about. That a
	// non-owner gets nothing here is asserted in owner_test.go instead.
	return &server{providers: r, cache: map[string]cacheEntry{}, auth: ownerAuth()}
}

// A fresh self-host has nothing configured. That is a normal state, not a
// crash, and the screen that would let someone configure something must paint.
func TestProvidersWithNothingConfigured(t *testing.T) {
	s := &server{cache: map[string]cacheEntry{}, auth: ownerAuth()}
	rec := httptest.NewRecorder()
	s.handleProviders(rec, ownerRequest(t, s.auth, "GET", "/api/providers", ""))

	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got["providers"] == nil {
		t.Error("providers must be an empty list, not null -- clients iterate it")
	}
}

// The rule this endpoint exists to honour: a settings screen that cannot paint
// because one instance is sick is a screen that cannot fix the sick instance.
func TestOneBrokenProviderDoesNotHideTheHealthyOnes(t *testing.T) {
	healthy := sickProvider{fakeProvider: radarrLike(), state: HealthOK}
	broken := sickProvider{fakeProvider: fakeProvider{
		id: "sonarr", name: "Sonarr", domains: []string{"video"},
		roles: []string{"acquisition"}, caps: []string{"health"},
	}, panics: true}

	s := providerServer(t, healthy, broken)
	rec := httptest.NewRecorder()
	s.handleProviders(rec, ownerRequest(t, s.auth, "GET", "/api/providers", ""))

	if rec.Code != 200 {
		t.Fatalf("a panicking adapter took the whole endpoint down: status %d", rec.Code)
	}
	var got struct {
		Providers []providerView `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got.Providers) != 2 {
		t.Fatalf("got %d providers, want both listed", len(got.Providers))
	}
	for _, p := range got.Providers {
		switch p.ID {
		case "radarr":
			if p.Health.State != HealthOK {
				t.Errorf("healthy provider reported %q", p.Health.State)
			}
		case "sonarr":
			// Reported as degraded, not silently omitted: a provider that
			// vanishes from the list looks unconfigured, and the user removes
			// the wrong thing trying to fix it.
			if p.Health.State != HealthDegraded {
				t.Errorf("broken provider reported %q, want degraded", p.Health.State)
			}
		}
	}
}

// Ten providers behind a dead VPN must cost one timeout, not ten.
func TestSlowProvidersAreProbedConcurrently(t *testing.T) {
	var ps []Provider
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		ps = append(ps, sickProvider{fakeProvider: fakeProvider{
			id: id, name: id, domains: []string{"video"},
			roles: []string{"library"}, caps: []string{"health"},
		}, hangs: true})
	}
	s := providerServer(t, ps...)

	req := ownerRequest(t, s.auth, "GET", "/api/providers", "")
	ctx, cancel := context.WithTimeout(req.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	rec := httptest.NewRecorder()
	s.handleProviders(rec, req.WithContext(ctx))
	elapsed := time.Since(start)

	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	// Serial would be five times the timeout. Allow generous slack for CI.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("five hanging providers took %v; they are being probed serially", elapsed)
	}
}

// "Nothing is downloading" and "we could not ask" must not look identical.
func TestActivityNamesTheProvidersItCouldNotReach(t *testing.T) {
	good := activityProvider{
		fakeProvider: radarrLike(),
		items: []ActivityItem{
			{CanonicalID: "m1", Title: "Dune", Domain: "video", Stage: "downloading", Progress: 0.72},
		},
	}
	bad := activityProvider{fakeProvider: fakeProvider{
		id: "lidarr", name: "Lidarr", domains: []string{"music"},
		roles: []string{"acquisition"}, caps: []string{"activity"},
	}, err: errors.New("connection refused")}

	s := providerServer(t, good, bad)
	rec := httptest.NewRecorder()
	s.handleActivity(rec, httptest.NewRequest("GET", "/api/activity", nil), "wade")

	var got activityView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got.Items) != 1 {
		t.Errorf("got %d items, want the healthy provider's work", len(got.Items))
	}
	if len(got.Failed) != 1 || got.Failed[0] != "lidarr" {
		t.Errorf("failed = %v; an unreachable provider must be named, or a short list looks complete", got.Failed)
	}
}

// A provider with no activity to report is asked nothing at all.
func TestActivitySkipsProvidersThatDoNotReportIt(t *testing.T) {
	// radarrLike is a plain fakeProvider: it does not implement ActivityProvider.
	s := providerServer(t, radarrLike())
	rec := httptest.NewRecorder()
	s.handleActivity(rec, httptest.NewRequest("GET", "/api/activity", nil), "wade")

	var got activityView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got.Failed) != 0 {
		t.Errorf("failed = %v; a provider without the capability is not a failure", got.Failed)
	}
	if got.Items == nil {
		t.Error("items must be an empty list, not null")
	}
}

func TestActivitySurvivesAPanickingProvider(t *testing.T) {
	s := providerServer(t, activityProvider{fakeProvider: radarrLike(), panic: true})
	rec := httptest.NewRecorder()
	s.handleActivity(rec, httptest.NewRequest("GET", "/api/activity", nil), "wade")

	if rec.Code != 200 {
		t.Fatalf("a panicking adapter took the endpoint down: %d", rec.Code)
	}
	var got activityView
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Failed) != 1 {
		t.Errorf("the panicking provider was not named as failed: %v", got.Failed)
	}
}
