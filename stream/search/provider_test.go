package main

import (
	"context"
	"strings"
	"testing"
)

type fakeProvider struct {
	id      string
	name    string
	domains []string
	roles   []string
	caps    []string
}

func (f fakeProvider) ID() string             { return f.id }
func (f fakeProvider) Name() string           { return f.name }
func (f fakeProvider) Domains() []string      { return f.domains }
func (f fakeProvider) Roles() []string        { return f.roles }
func (f fakeProvider) Capabilities() []string { return f.caps }
func (f fakeProvider) Health(context.Context) Health {
	return Health{State: HealthOK}
}

func radarrLike() fakeProvider {
	return fakeProvider{
		id: "radarr", name: "Radarr",
		domains: []string{"video"},
		roles:   []string{"discovery", "acquisition", "library"},
		caps:    []string{"health", "search", "request", "library", "activity"},
	}
}

// The original bug, one layer up: a provider that claims "movies" instead of
// "video" would match no query and report nothing wrong. Registration is the
// last place this can be caught cheaply.
func TestRegistryRejectsNonCanonicalDomains(t *testing.T) {
	r := &Registry{}
	p := radarrLike()
	p.domains = []string{"movies"}

	err := r.Add(p)
	if err == nil {
		t.Fatal("a provider claiming \"movies\" was accepted; it would match nothing")
	}
	if !strings.Contains(err.Error(), "video") {
		t.Errorf("error should suggest the canonical form, got: %v", err)
	}
}

func TestRegistryRejectsUnknownRoles(t *testing.T) {
	r := &Registry{}
	p := radarrLike()
	p.roles = []string{"discovery", "teleportation"}

	if err := r.Add(p); err == nil {
		t.Fatal("an unknown role was accepted")
	}
}

// Two instances of one type is normal -- Radarr and Radarr-4K -- but two with
// the same id means requests route by coin flip.
func TestRegistryRejectsDuplicateIDs(t *testing.T) {
	r := &Registry{}
	if err := r.Add(radarrLike()); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(radarrLike()); err == nil {
		t.Fatal("a duplicate provider id was accepted")
	}
}

func TestRegistryAcceptsMultipleInstancesOfOneType(t *testing.T) {
	r := &Registry{}
	a := radarrLike()
	b := radarrLike()
	b.id, b.name = "radarr-4k", "Radarr 4K"

	if err := r.Add(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(b); err != nil {
		t.Fatalf("a second Radarr instance was refused: %v", err)
	}
	if got := len(r.For("video", "acquisition")); got != 2 {
		t.Errorf("For(video, acquisition) returned %d, want 2", got)
	}
}

// Callers may name a domain any way a user would. Routing must not depend on
// the caller knowing the internal spelling.
func TestRegistryLookupAcceptsAnySpelling(t *testing.T) {
	r := &Registry{}
	if err := r.Add(radarrLike()); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{"video", "movies", "movie", "tv", "Movies"} {
		if got := len(r.For(spelling, "acquisition")); got != 1 {
			t.Errorf("For(%q, acquisition) returned %d, want 1", spelling, got)
		}
	}
}

// The point of optional interfaces: asking Radarr for a guide must be a
// compile-safe, runtime-cheap "no", not an error return nobody reads.
func TestCapabilitiesAreDiscoveredNotAssumed(t *testing.T) {
	var p Provider = radarrLike()

	if _, ok := p.(GuideProvider); ok {
		t.Error("Radarr should not satisfy GuideProvider")
	}
	if _, ok := p.(ChannelProvider); ok {
		t.Error("Radarr should not satisfy ChannelProvider")
	}
	// And it must not be asked for one either.
	if contains(p.Roles(), "linear-tv") {
		t.Error("Radarr claims linear-tv")
	}
}

// A provider that serves no domain any query names is invisible; the settings
// screen needs the real list so it can show only tabs that will answer.
func TestRegistryReportsConfiguredDomains(t *testing.T) {
	r := &Registry{}
	_ = r.Add(radarrLike())
	_ = r.Add(fakeProvider{
		id: "komga", name: "Komga",
		domains: []string{"comic"},
		roles:   []string{"library", "reader"},
		caps:    []string{"health", "library"},
	})

	got := r.Domains()
	if len(got) != 2 || got[0] != "comic" || got[1] != "video" {
		t.Errorf("Domains() = %v, want [comic video]", got)
	}
}

// Every health constant must be one the client knows how to render.
func TestHealthStatesMatchTheSchema(t *testing.T) {
	known := map[string]bool{}
	for _, h := range schema.Health {
		known[h] = true
	}
	for _, s := range []HealthState{
		HealthOK, HealthNotConfigured, HealthUnreachable,
		HealthAuthFailed, HealthIncompatible, HealthDegraded,
	} {
		if !known[string(s)] {
			t.Errorf("health state %q is not in schema.json; the client cannot render it", s)
		}
	}
}
