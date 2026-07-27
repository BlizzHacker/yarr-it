package main

import (
	"net"
	"testing"
)

// The bridge sits on a box with a WireGuard tunnel into the home estate.
// If publicUnicast lets anything private through, any visitor's browser could
// use the relay to reach 10.10.10.1, the 192.168.0.0/24 LAN, or localhost
// services on the VPS itself. This is the single most important check here.
func TestPublicUnicastRefusesPrivateTargets(t *testing.T) {
	refuse := []string{
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"10.10.10.1",      // the WireGuard peer (Hestia mail)
		"10.10.10.3",      // the bridge host itself on the tunnel
		"192.168.0.115",   // Prowlarr
		"192.168.0.197",   // Traefik
		"192.168.0.6",     // Proxmox
		"172.17.0.1",      // docker bridge
		"169.254.169.254", // cloud metadata
		"100.64.0.1",      // CGNAT
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
		"fe80::1",         // link-local v6
		"fd00::1",         // unique-local v6
	}
	for _, s := range refuse {
		if publicUnicast(net.ParseIP(s)) {
			t.Errorf("publicUnicast(%s) = true, want false -- relay could reach private host", s)
		}
	}

	allow := []string{
		"1.1.1.1",
		"8.8.8.8",
		"104.129.28.137",
		"173.207.168.55",
		"2606:4700:4700::1111",
	}
	for _, s := range allow {
		if !publicUnicast(net.ParseIP(s)) {
			t.Errorf("publicUnicast(%s) = false, want true -- legitimate peer refused", s)
		}
	}
}

func TestPublicUnicastRefusesNil(t *testing.T) {
	if publicUnicast(nil) {
		t.Error("publicUnicast(nil) = true, want false")
	}
}

func TestBudgetDegradesAtCap(t *testing.T) {
	b := newBudget(t.TempDir()+"/budget.json", 1000)
	if b.degraded() {
		t.Fatal("fresh budget reports degraded")
	}
	b.add(999)
	if b.degraded() {
		t.Error("degraded below cap")
	}
	b.add(1)
	if !b.degraded() {
		t.Error("not degraded at cap; relay would keep spending past the allowance")
	}
}

func TestBudgetSurvivesRestartWithinMonth(t *testing.T) {
	path := t.TempDir() + "/budget.json"
	b := newBudget(path, 1<<30)
	b.add(4242)
	b.persist()

	restored := newBudget(path, 1<<30)
	if got, _ := restored.snapshot(); got != 4242 {
		t.Errorf("restored used = %d, want 4242 -- a restart would reset the month's accounting", got)
	}
}
