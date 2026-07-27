package main

import (
	"path/filepath"
	"testing"
)

// The IPTV sub-cap exists so continuous video can never consume the whole
// month. Exhausting it must not degrade torrent relaying, which shares the box
// with the mail edge.
func TestIPTVBudgetIsIndependentOfTheMainBudget(t *testing.T) {
	dir := t.TempDir()
	main_ := newBudget(filepath.Join(dir, "main.json"), 1000)
	iptv := newBudget(filepath.Join(dir, "iptv.json"), 250)

	iptv.add(250)

	if !iptv.degraded() {
		t.Fatal("iptv budget should be degraded at its cap")
	}
	if main_.degraded() {
		t.Fatal("main budget must not degrade when only the iptv sub-cap is spent")
	}
}

func TestIPTVBudgetDefaultIsAQuarterOfTheMonthlyCap(t *testing.T) {
	if got := defaultIPTVBudgetGiB(2600); got != 650 {
		t.Fatalf("want 650, got %d", got)
	}
}
