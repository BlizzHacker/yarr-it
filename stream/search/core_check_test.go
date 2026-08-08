package main

import "testing"

// The five ids in the old table that archive.org does not use, and the two
// blocked-by-isolation systems, must all refuse a touch player.
func TestOldTableGhostsAndBlockedSystemsRefuse(t *testing.T) {
	for _, e := range []string{"famicom", "superfamicom", "segaMD", "gg", "vb", "dosbox", "psp"} {
		if got := ejsCoreFor(e); got != "" {
			t.Errorf("ejsCoreFor(%q) = %q; that button cannot play", e, got)
		}
	}
	// And the six real ids the old table lacked must now work.
	for _, e := range []string{"megadrij", "nesp", "nespal", "a2600p", "snesp"} {
		if got := ejsCoreFor(e); got == "" {
			t.Errorf("ejsCoreFor(%q) = \"\"; those games were never offered our player", e)
		}
	}
}
