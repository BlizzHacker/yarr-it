package main

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUnconfinedEgressRunsInProcess(t *testing.T) {
	e, err := newEgress("")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.confined {
		t.Error("an in-process proxy must not report itself as confined")
	}
	if !strings.HasPrefix(e.URL(), "http://127.0.0.1:") {
		t.Errorf("in-process proxy URL = %q, want a loopback address", e.URL())
	}
}

// A configured-but-unreachable proxy has to be fatal. The failure mode this
// guards against is a production box that believes it is confined, silently
// falls back to fetching from the host, and is therefore fetching from the
// machine that holds the tunnel.
func TestConfinedEgressRefusesToStartWhenTheProxyIsMissing(t *testing.T) {
	start := time.Now()
	// Port 1 on loopback: nothing listens there, and nothing will.
	if _, err := newEgressWaiting("127.0.0.1:1", time.Second); err == nil {
		t.Fatal("newEgress accepted an unreachable proxy address; a silent " +
			"downgrade to unconfined is exactly what must not happen")
	} else if !strings.Contains(err.Error(), "yarrit-egress-proxy") {
		t.Errorf("the error should name the unit that is not running: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("startup blocked for too long on a dead proxy")
	}
}

// The parent reads the guard's counters out of the proxy over the veth. That
// is what lets handleResolve tell "the guard refused a redirect" apart from
// "the extractor broke", even when the guard lives in another namespace.
func TestEgressStatsAreReadableOverHTTP(t *testing.T) {
	p, err := newEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Cause one refusal so the counter is not trivially zero, going through the
	// proxy exactly as yt-dlp does.
	proxyURL, err := url.Parse(p.URL())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}
	resp, err := client.Get("http://192.168.0.115:9696/")
	if err != nil {
		t.Fatalf("proxy gave no answer: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("proxy answered %d for Wade's Prowlarr, want 403", resp.StatusCode)
	}

	// Read the counters the way a parent in another namespace would.
	e := &egress{url: p.URL(), addr: p.addr, confined: true}
	if _, refused := e.stats(); refused < 1 {
		t.Errorf("refused = %d over the stats endpoint, want at least 1 — "+
			"without this, a guard refusal is indistinguishable from a broken extractor",
			refused)
	}

	// Origin-form only, so it can never be turned into a fetch of another host.
	if strings.Contains(egressStatsPath, "://") {
		t.Error("the stats path must be origin-form only")
	}
}

// ---------------------------------------------------------- the real proof --

// The demonstration that matters: with no guard anywhere in the path, the
// namespace alone must make the LAN unreachable.
//
// deploy/yarrit-egress-verify.sh drives plain curl inside the namespace, so
// link_guard.go could be deleted entirely and these assertions would still
// hold. It needs Linux, root and the namespace, so it runs only when asked:
//
//	sudo YARRIT_NETNS_PROOF=1 go test -run TestNetnsConfinementProof -v ./...
func TestNetnsConfinementProof(t *testing.T) {
	if os.Getenv("YARRIT_NETNS_PROOF") != "1" {
		t.Skip("set YARRIT_NETNS_PROOF=1 (as root, on Linux, with the namespace up) to run the isolation proof")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("network namespaces are a Linux facility; this is %s", runtime.GOOS)
	}
	script := filepath.Join("..", "deploy", "yarrit-egress-verify.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("verification script missing: %v", err)
	}
	out, err := exec.Command("bash", script).CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatalf("the namespace does NOT confine egress on its own: %v", err)
	}
}
