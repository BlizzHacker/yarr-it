package main

// Network-level confinement for everything /api/link/* fetches.
//
// link_guard.go is an application-level defence against a network-level
// problem. It is thorough -- NAT64, 6to4, IPv4-mapped, userinfo confusion and
// live redirects are all refused -- but it is still Go code deciding what to
// dial, and yt-dlp and ffmpeg fetch on their own. The durable answer to "this
// process can reach 192.168.0.115" is to do the fetching somewhere that
// address is not routable at all.
//
// The arrangement that achieves that is less obvious than it first looks.
// Putting yt-dlp in a namespace while it proxies through a helper on the HOST
// buys nothing: the host does the connecting, and the host is the machine
// holding the WireGuard tunnel. So it is the PROXY that moves into the
// namespace, and everything else simply talks to it:
//
//	 host netns                          yarrit-egress netns
//	 ----------                          -------------------
//	 mw-search ────────┐
//	 yt-dlp  --proxy ──┼── veth ──►  mw-search -egress-proxy   (link_guard.go)
//	 ffmpeg  http_proxy┘                      │                 SSRF guard
//	 (tunnel to 192.168.0.0/16)               ▼
//	                                   default route → internet
//	                                   blackhole → 192.168.0.0/16, 10/8, …
//
// Every outbound connection for this feature is therefore *originated* inside
// the namespace, by a process that has no route to the house. The host-side
// programs only ever connect to the veth peer.
//
// Two layers, failing independently:
//
//   - Delete the guard, and the kernel still answers ENETUNREACH.
//   - Delete the namespace, and the guard still refuses the address.
//
// Proof of the first is deploy/yarrit-egress-verify.sh, which runs plain curl
// inside the namespace -- no guard anywhere in the path -- and requires
// 192.168.0.115 to fail while the public internet succeeds.
//
// The proxy is placed in the namespace by systemd (NetworkNamespacePath=), not
// by this process. That is deliberate: `ip netns exec` needs CAP_SYS_ADMIN, and
// granting mw-search that capability to improve its confinement would be an
// odd trade. See deploy/yarrit-egress-proxy.service.

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// egressStatsPath is answered by the proxy in origin form, so the parent can
// read the guard's refusal counters out of a process in another namespace.
// Two integers cross it and nothing else.
const egressStatsPath = "/__egress-stats"

// egress is whichever guarded proxy the link feature fetches through: an
// in-process one on loopback, or one in a namespace across the veth.
type egress struct {
	url  string // proxy URL for yt-dlp, ffmpeg and this process
	addr string
	// confined records that the proxy is somewhere this process is not, which
	// is the difference between the two deployments and the thing
	// logLinkPolicy has to be honest about.
	confined bool

	local *egressProxy // set only when unconfined

	mu         sync.Mutex
	lastStats  [2]int
	statsFresh time.Time
}

// newEgress attaches to the confined proxy, or starts an in-process one.
//
// A configured-but-unreachable proxy is fatal rather than a silent downgrade:
// on the public VPS, "confined" and "not confined" must never be
// indistinguishable from the outside, and quietly running unconfined is how a
// deployment comes to believe in an isolation it does not have.
func newEgress(proxyAddr string) (*egress, error) {
	return newEgressWaiting(proxyAddr, 15*time.Second)
}

// newEgressWaiting is newEgress with the readiness budget exposed, so a test
// does not have to sit through the production one.
func newEgressWaiting(proxyAddr string, limit time.Duration) (*egress, error) {
	if proxyAddr == "" {
		p, err := newEgressProxy()
		if err != nil {
			return nil, err
		}
		return &egress{url: p.URL(), addr: p.addr, local: p}, nil
	}
	e := &egress{url: "http://" + proxyAddr, addr: proxyAddr, confined: true}
	if err := e.waitReady(limit); err != nil {
		return nil, err
	}
	return e, nil
}

// waitReady blocks until the confined proxy answers, so a resolve arriving in
// the first second does not fail against a socket nothing is listening on yet.
func (e *egress) waitReady(limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", e.addr, time.Second)
		if err == nil {
			c.Close()
			return nil
		}
		last = err
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("the confined egress proxy at %s never answered "+
		"(is yarrit-egress-proxy.service running?): %w", e.addr, last)
}

func (e *egress) Close() error {
	if e != nil && e.local != nil {
		return e.local.Close()
	}
	return nil
}

// URL is the proxy address yt-dlp, ffmpeg and this process all fetch through.
func (e *egress) URL() string {
	if e == nil {
		return ""
	}
	return e.url
}

// stats returns the guard's allow/refuse counters wherever the proxy is.
//
// In-process, this reads them directly. Confined, it asks over the veth, with
// a short cache so the per-resolve comparison in handleResolve cannot become a
// request storm.
func (e *egress) stats() (allowed, refused int) {
	if e == nil {
		return 0, 0
	}
	if e.local != nil {
		return e.local.stats()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.statsFresh) < 250*time.Millisecond {
		return e.lastStats[0], e.lastStats[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+e.addr+egressStatsPath, nil)
	if err != nil {
		return e.lastStats[0], e.lastStats[1]
	}
	// A plain client, not the guarded one: the target is the veth peer, which
	// is a private address by design and which the guard would refuse.
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return e.lastStats[0], e.lastStats[1]
	}
	defer resp.Body.Close()
	var a, r int
	if _, err := fmt.Fscanf(resp.Body, "%d %d", &a, &r); err != nil {
		return e.lastStats[0], e.lastStats[1]
	}
	e.lastStats = [2]int{a, r}
	e.statsFresh = time.Now()
	return a, r
}

// logLinkPolicy states the isolation posture once at startup, in the same
// spirit as logOwnerPolicy. "Confined" and "not confined" must be visible
// without reading the unit file.
func logLinkPolicy(s *linkService) {
	if s.egress != nil && s.egress.confined {
		log.Printf("link resolver: all /api/link egress originates in the confined "+
			"namespace via %s, which has no route to any private network", s.egress.url)
		return
	}
	log.Printf("link resolver: NOT confined (-link-egress unset). The SSRF guard is " +
		"the only thing between this endpoint and any network this host can route to, " +
		"including anything behind the WireGuard tunnel. Fine locally; set -link-egress " +
		"in production.")
}

// runEgressProxyOnly is the confined mode. systemd starts this inside the
// namespace; it serves the guard's proxy and nothing else -- no Prowlarr key,
// no library, no routes, no owner boundary to get wrong.
func runEgressProxyOnly(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("egress proxy: %v", err)
	}
	p := &egressProxy{ln: ln, addr: addr}
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.serve),
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("guarded egress proxy listening on %s", addr)
	if err := p.srv.Serve(ln); err != nil {
		log.Fatalf("egress proxy: %v", err)
	}
}
