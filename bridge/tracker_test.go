package main

import "testing"

// The tracker host is supplied by the client, so the proxy must refuse anything
// that resolves into private space -- otherwise it becomes a way to probe the
// estate behind the WireGuard tunnel via http://10.10.10.1/.
func TestResolvesPublicRefusesPrivateHosts(t *testing.T) {
	for _, h := range []string{"localhost", "10.10.10.1", "192.168.0.115", "127.0.0.1", ""} {
		if resolvesPublic(h) {
			t.Errorf("resolvesPublic(%q) = true, want false", h)
		}
	}
}

func TestParseBencodeCompactPeers(t *testing.T) {
	// d8:intervali1800e5:peers12:<12 bytes>e  -- two compact peers.
	body := []byte("d8:intervali1800e8:completei5e5:peers12:")
	body = append(body, 1, 1, 1, 1, 0x1a, 0xe1) // 1.1.1.1:6881
	body = append(body, 8, 8, 8, 8, 0x1a, 0xe2) // 8.8.8.8:6882
	body = append(body, 'e')

	got := parseBencodeAnnounce(body)
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if len(got.Peers) != 2 {
		t.Fatalf("got %d peers, want 2: %+v", len(got.Peers), got.Peers)
	}
	if got.Peers[0].Host != "1.1.1.1" || got.Peers[0].Port != 6881 {
		t.Errorf("peer0 = %+v, want 1.1.1.1:6881", got.Peers[0])
	}
	if got.Interval != 1800 || got.Seeders != 5 {
		t.Errorf("interval=%d seeders=%d, want 1800/5", got.Interval, got.Seeders)
	}
}

// A tracker handing back private addresses must not have them passed on.
func TestParseBencodeDropsPrivatePeers(t *testing.T) {
	body := []byte("d5:peers12:")
	body = append(body, 10, 10, 10, 1, 0x1a, 0xe1) // 10.10.10.1 -- the tunnel
	body = append(body, 1, 1, 1, 1, 0x1a, 0xe1)    // public
	body = append(body, 'e')

	got := parseBencodeAnnounce(body)
	if len(got.Peers) != 1 || got.Peers[0].Host != "1.1.1.1" {
		t.Errorf("private peer leaked through: %+v", got.Peers)
	}
}

func TestParseBencodeFailureReason(t *testing.T) {
	got := parseBencodeAnnounce([]byte("d14:failure reason17:torrent not foundе"))
	if got.Error == "" {
		t.Error("failure reason not surfaced")
	}
}
