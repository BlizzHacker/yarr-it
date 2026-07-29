package main

import (
	"testing"
	"time"
)

// Warming only works if an entry is still cached when the warmer comes back
// round to it. This held for a long time in the wrong direction -- a full cycle
// took 18 minutes against a 15 minute TTL -- and nothing failed, because the
// symptom is a search that is merely slow.
func TestWarmCycleFitsInsideCacheTTL(t *testing.T) {
	cycle := warmInterval*time.Duration(warmBatchSize) + warmStagger
	if cycle >= defaultTTL {
		t.Fatalf("a warm cycle takes %s but entries expire after %s: "+
			"the warmer can never keep the batch warm", cycle, defaultTTL)
	}
	// Insist on real headroom rather than a cycle that only just fits: a slow
	// Prowlarr stretches the cycle, and at exactly TTL the warmer wins races
	// only by luck.
	if margin := defaultTTL - cycle; margin < cycle/2 {
		t.Errorf("only %s of headroom for an %s cycle; a slow run would still "+
			"let entries expire", margin, cycle)
	}
}
