package main

// The SSRF guard for the paste-a-link resolver.
//
// /api/link/* takes an arbitrary URL from an anonymous visitor and fetches it.
// That is server-side request forgery by construction, and it is worse here
// than on a generic box: mw-search reaches Prowlarr at 192.168.0.115 across a
// WireGuard tunnel, so an unguarded fetcher is a door onto the home LAN for
// anyone on the internet.
//
// Three separate things do the fetching and all three have to be guarded, or
// the weakest one is the whole security posture:
//
//  1. This process, probing format sizes (guardedClient).
//  2. This process, streaming the chosen format back (same client).
//  3. yt-dlp, which fetches web pages, player APIs and DASH manifests on its
//     own and knows nothing about any of this. It is forced through
//     egressProxy -- an HTTP proxy bound to loopback that applies the same
//     rules to every hop -- because a check in Go that yt-dlp does not consult
//     protects nothing.
//
// The check is "resolve, then dial the resolved literal". Validating a
// hostname and then handing the *name* to the dialer leaves a DNS-rebinding
// window: the guard resolves a public address, the attacker's DNS server
// answers the dialer with 127.0.0.1 a millisecond later. Pinning the address
// closes it -- see dialGuarded.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// errBlocked is returned for anything the policy refuses. It is deliberately
// one error for every refusal reason: telling a caller *why* their target was
// rejected turns this endpoint into a network scanner that reports whether
// 192.168.0.115 exists.
var errBlocked = errors.New("refusing non-public address")

// Ranges that are not publicly routable but that net.IP's own predicates miss.
// The v6 entries matter more than they look: several of them embed an IPv4
// address, so 64:ff9b::192.168.0.115 and 2002:c0a8:73:: are both ways of
// spelling "the Prowlarr box" that pass an IsPrivate() check unscathed.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, incl. 255.255.255.255
	netip.MustParsePrefix("::/128"),          // unspecified
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 -- embeds an IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001::/32"),       // Teredo -- embeds an IPv4 address
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4 -- embeds an IPv4 address
	netip.MustParsePrefix("fc00::/7"),        // unique local
	netip.MustParsePrefix("fe80::/10"),       // link-local
	netip.MustParsePrefix("ff00::/8"),        // multicast
}

// publicRoutable reports whether addr is a globally routable unicast address.
//
// IPv4-mapped IPv6 (::ffff:127.0.0.1) is unwrapped first rather than tested as
// a v6 address: left wrapped it matches no v6 block and no v4 block, so it
// would sail through as "public" while dialing straight to loopback.
func publicRoutable(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if addr.IsLoopback() || addr.IsUnspecified() || addr.IsMulticast() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsPrivate() {
		return false
	}
	for _, p := range blockedPrefixes {
		// A v4 address can never sit inside a v6 prefix and vice versa, so the
		// family check keeps 0.0.0.0/8 from matching ::/128 by accident.
		if p.Addr().Is4() != addr.Is4() {
			continue
		}
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// linkTarget is a URL that has passed the scheme/shape checks. It carries the
// addresses the host resolved to so the dialer can use those exact addresses
// rather than resolving a second time.
type linkTarget struct {
	URL   *url.URL
	Addrs []netip.Addr
}

const (
	maxURLLen = 2048
	// Named for this module: discover_resolve.go already owns `resolveTimeout`.
	linkDNSTimeout = 5 * time.Second
)

// parseTargetURL applies every check that can be made without touching the
// network. Doing these first means an obviously bad input never costs a DNS
// lookup, and it is the only place that decides what a "URL" means here.
func parseTargetURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("missing url")
	}
	if len(raw) > maxURLLen {
		return nil, errors.New("url is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("that is not a URL")
	}
	// http(s) only. file:, gopher:, dict:, ftp: and friends are all ways of
	// reaching something that is not a web page, and gopher:// in particular is
	// the classic trick for making a fetcher speak an arbitrary line protocol
	// at an internal service.
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return nil, errors.New("only http and https links work here")
	}
	if u.Host == "" {
		return nil, errors.New("that URL has no host")
	}
	// Credentials in a URL are never needed for a public post and are a neat
	// way to smuggle a different host past a naive parser, so they are refused
	// rather than stripped.
	if u.User != nil {
		return nil, errors.New("URLs with embedded credentials are not accepted")
	}
	if p := u.Port(); p != "" {
		n, err := netip.ParseAddrPort(net.JoinHostPort("0.0.0.0", p))
		if err != nil || n.Port() == 0 {
			return nil, errors.New("bad port")
		}
	}
	return u, nil
}

// resolveTarget parses raw and resolves its host, refusing unless *every*
// address is publicly routable.
//
// Every, not any: a host that answers with both 93.184.216.34 and 127.0.0.1 is
// an attack, not a multi-homed server, and picking the public one would mean
// the same name resolves differently on the retry.
func resolveTarget(ctx context.Context, res *net.Resolver, raw string) (*linkTarget, error) {
	u, err := parseTargetURL(raw)
	if err != nil {
		return nil, err
	}
	addrs, err := resolveHostPublic(ctx, res, u.Hostname())
	if err != nil {
		return nil, err
	}
	return &linkTarget{URL: u, Addrs: addrs}, nil
}

func resolveHostPublic(ctx context.Context, res *net.Resolver, host string) ([]netip.Addr, error) {
	if host == "" {
		return nil, errBlocked
	}
	// A bare IP literal needs no lookup -- and must not get one, because
	// LookupNetIP on "127.0.0.1" happily returns 127.0.0.1 and a caller that
	// forgot this check would then dial it.
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if !publicRoutable(addr) {
			return nil, errBlocked
		}
		return []netip.Addr{addr}, nil
	}

	if res == nil {
		res = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(ctx, linkDNSTimeout)
	defer cancel()
	addrs, err := res.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("could not resolve %q", host)
	}
	for _, a := range addrs {
		if !publicRoutable(a) {
			return nil, errBlocked
		}
	}
	return addrs, nil
}

// dialGuarded is the DialContext every outbound connection in this module
// uses. It resolves the host itself, refuses unless all addresses are public,
// and then dials the resolved address literal.
//
// Dialing the literal is the entire point. Handing the hostname back to
// net.Dialer would make it resolve a second time, and a DNS server under the
// caller's control can answer differently on that second lookup -- the classic
// rebinding attack. There is no window here because nothing is looked up twice.
func dialGuarded(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := resolveHostPublic(ctx, nil, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, a := range addrs {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errBlocked
	}
	return nil, lastErr
}

// guardedClient is an http.Client that cannot be aimed at a private address,
// including via redirect.
//
// CheckRedirect replaces net/http's default 10-hop limit entirely, so the hop
// cap has to be re-imposed here or a cooperating upstream can redirect to
// itself forever. Each hop is re-validated because passing the check once
// says nothing about where hop two points -- that is precisely the bypass
// this endpoint would otherwise ship with.
func guardedClient(timeout time.Duration) *http.Client {
	return guardedClientVia(timeout, "")
}

// guardedClientVia is guardedClient routed through an upstream proxy.
//
// When the link feature is confined to a network namespace, this process's own
// fetches -- size probes and media streaming -- must leave through that
// namespace too, or half the egress would still originate on the host that
// holds the tunnel. Pointing the transport at the in-namespace proxy is what
// puts them on the same path as yt-dlp's.
//
// The dialer is left guarded even then. Reaching the proxy itself is a private
// address by design, so the proxy URL is dialled directly rather than through
// dialGuarded, which would refuse it.
func guardedClientVia(timeout time.Duration, proxyURL string) *http.Client {
	tr := &http.Transport{
		DialContext:           dialGuarded,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableCompression:    false,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			tr.Proxy = http.ProxyURL(u)
			// The only hop this transport now makes itself is to the proxy,
			// which lives on a deliberately private veth address. The guard
			// still applies -- it is simply applied inside the namespace, by
			// the same code, one process along.
			tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if _, err := parseTargetURL(req.URL.String()); err != nil {
				return err
			}
			if _, err := resolveHostPublic(req.Context(), nil, req.URL.Hostname()); err != nil {
				return err
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------- egress --

// egressProxy is a loopback HTTP proxy that yt-dlp is pointed at with
// --proxy, so that every request yt-dlp makes -- the watch page, the player
// API, the DASH manifest, each redirect hop -- is subject to the same policy
// as this process's own fetches.
//
// This is not belt-and-braces, it is the actual guard for the yt-dlp half.
// Validating the URL the user pasted and then handing it to a tool that will
// follow a 302 anywhere protects nothing at all. Because yt-dlp reaches the
// network only through CONNECT/absolute-form requests to this proxy, DNS
// resolution happens *here*, under the guard, and never inside yt-dlp.
type egressProxy struct {
	ln   net.Listener
	srv  *http.Server
	addr string

	mu      sync.Mutex
	refused int
	allowed int
}

// perTunnelByteCap bounds one CONNECT tunnel. Metadata extraction moves
// kilobytes; anything approaching this is either a broken extractor pulling a
// media file or somebody using the resolver as a download pipe on the relay's
// bandwidth.
const perTunnelByteCap = 48 << 20

func newEgressProxy() (*egressProxy, error) {
	// Loopback only. A proxy that will dial the public internet is a thing
	// people scan for, and this one needs no reachability beyond this box.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &egressProxy{ln: ln, addr: ln.Addr().String()}
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.serve),
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// URL is what gets handed to yt-dlp's --proxy.
func (p *egressProxy) URL() string { return "http://" + p.addr }

func (p *egressProxy) Close() error { return p.srv.Close() }

func (p *egressProxy) count(ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ok {
		p.allowed++
	} else {
		p.refused++
	}
}

func (p *egressProxy) stats() (allowed, refused int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allowed, p.refused
}

func (p *egressProxy) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}
	// An origin-form request for the stats path. This is how the parent reads
	// the guard's counters when the proxy is a child in another network
	// namespace -- two integers, nothing else, and only over the veth.
	if r.URL != nil && !r.URL.IsAbs() && r.URL.Path == egressStatsPath {
		a, ref := p.stats()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "%d %d", a, ref)
		return
	}
	p.serveAbsolute(w, r)
}

// serveConnect handles the https path: CONNECT host:443, then raw bytes.
func (p *egressProxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "443"
	}
	// Only the two web ports. A CONNECT tunnel to 22 or 9696 is not a video
	// site under any reading, and allowing arbitrary ports would turn this
	// into a general-purpose port forwarder the moment the address check is
	// ever loosened.
	if port != "443" && port != "80" {
		p.count(false)
		http.Error(w, "port not permitted", http.StatusForbidden)
		return
	}
	addrs, err := resolveHostPublic(r.Context(), nil, host)
	if err != nil {
		p.count(false)
		http.Error(w, errBlocked.Error(), http.StatusForbidden)
		return
	}

	d := net.Dialer{Timeout: 10 * time.Second}
	var upstream net.Conn
	for _, a := range addrs {
		upstream, err = d.DialContext(r.Context(), "tcp", net.JoinHostPort(a.String(), port))
		if err == nil {
			break
		}
	}
	if upstream == nil {
		p.count(false)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	p.count(true)

	_ = upstream.SetDeadline(time.Now().Add(2 * time.Minute))
	_ = client.SetDeadline(time.Now().Add(2 * time.Minute))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, io.LimitReader(client, perTunnelByteCap))
		_ = upstream.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, io.LimitReader(upstream, perTunnelByteCap))
		_ = client.Close()
	}()
	wg.Wait()
}

// serveAbsolute handles the plain-http path, where a proxied request arrives
// in absolute form (GET http://host/path HTTP/1.1).
func (p *egressProxy) serveAbsolute(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || !r.URL.IsAbs() {
		p.count(false)
		http.Error(w, "absolute-form request required", http.StatusBadRequest)
		return
	}
	if _, err := parseTargetURL(r.URL.String()); err != nil {
		p.count(false)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if _, err := resolveHostPublic(r.Context(), nil, r.URL.Hostname()); err != nil {
		p.count(false)
		http.Error(w, errBlocked.Error(), http.StatusForbidden)
		return
	}

	out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		p.count(false)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for k, vs := range r.Header {
		if strings.EqualFold(k, "Proxy-Connection") || strings.EqualFold(k, "Proxy-Authorization") {
			continue
		}
		for _, v := range vs {
			out.Header.Add(k, v)
		}
	}

	// Redirects are returned to yt-dlp rather than followed here, so each hop
	// comes back through this proxy and gets its own address check.
	client := guardedClient(60 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Do(out)
	if err != nil {
		p.count(false)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	p.count(true)

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, perTunnelByteCap))
}
