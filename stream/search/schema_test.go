package main

import (
	"encoding/json"
	"os"
	"testing"
)

type conformance struct {
	Resolves   [][]string `json:"resolves"`
	Unknown    []string   `json:"unknown"`
	SameDomain [][]string `json:"sameDomain"`
	Distinct   [][]string `json:"distinct"`
}

func loadConformance(t *testing.T) conformance {
	t.Helper()
	b, err := os.ReadFile("schema_conformance.json")
	if err != nil {
		t.Fatalf("conformance fixture missing: %v", err)
	}
	var c conformance
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("conformance fixture is not valid JSON: %v", err)
	}
	return c
}

// The same fixture is asserted by the web client. If the two ever disagree,
// one of these suites fails -- which is the check that did not exist when
// `kind=movies` matched nothing while `groups=movies` worked.
func TestConformanceResolutions(t *testing.T) {
	for _, pair := range loadConformance(t).Resolves {
		in, want := pair[0], pair[1]
		if got := canonicalDomain(in); got != want {
			t.Errorf("canonicalDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConformanceUnknownResolvesToNothing(t *testing.T) {
	for _, in := range loadConformance(t).Unknown {
		if got := canonicalDomain(in); got != "" {
			t.Errorf("canonicalDomain(%q) = %q; unknown names must not filter", in, got)
		}
	}
}

func TestConformanceSameDomain(t *testing.T) {
	c := loadConformance(t)
	for _, pair := range c.SameDomain {
		if !sameDomain(pair[0], pair[1]) {
			t.Errorf("%q and %q should be the same domain", pair[0], pair[1])
		}
	}
	for _, pair := range c.Distinct {
		if sameDomain(pair[0], pair[1]) {
			t.Errorf("%q and %q must NOT be the same domain", pair[0], pair[1])
		}
	}
}

// An alias claimed by two domains resolves by map iteration order, which is
// random in Go -- the filter would work intermittently, which is worse than
// failing.
func TestNoAliasIsClaimedTwice(t *testing.T) {
	owner := map[string]string{}
	for id, d := range schema.Domains {
		for _, a := range d.Aliases {
			tok := normaliseToken(a)
			if prev, dup := owner[tok]; dup && prev != id {
				t.Errorf("alias %q is claimed by both %q and %q", a, prev, id)
			}
			owner[tok] = id
		}
		for _, ty := range d.Types {
			tok := normaliseToken(ty)
			if prev, dup := owner[tok]; dup && prev != id {
				t.Errorf("type %q collides with an alias of %q", ty, prev)
			}
			owner[tok] = id
		}
	}
}

// Every domain must round-trip through its own id, or a caller echoing back a
// value we gave them gets nothing.
func TestEveryDomainResolvesToItself(t *testing.T) {
	for id := range schema.Domains {
		if got := canonicalDomain(id); got != id {
			t.Errorf("domain %q resolves to %q", id, got)
		}
	}
}

// A domain the search path cannot turn into indexer categories is a domain the
// user can select and get nothing from.
func TestEveryDomainHasSearchCategories(t *testing.T) {
	for id := range schema.Domains {
		if categoriesFor(id) == nil {
			t.Errorf("domain %q has no indexer categories; selecting it returns nothing", id)
		}
	}
}
