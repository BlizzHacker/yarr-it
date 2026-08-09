package main

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// The three refusals the brief names explicitly, kept together at the top so
// the answer to "does it block Wade's LAN" is the first thing anyone reads.
func TestRefusesLoopbackPrivateAndTunnel(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"loopback by ip", "http://127.0.0.1/"},
		{"loopback with port", "http://127.0.0.1:8802/api/search?q=x"},
		{"loopback by name", "http://localhost:9696/"},
		{"loopback ipv6", "http://[::1]:80/"},
		{"prowlarr over the tunnel", "http://192.168.0.115:9696/api/v1/indexer"},
		{"prowlarr https", "https://192.168.0.115/"},
		{"wade's lan gateway", "http://192.168.0.1/"},
		{"rfc1918 10/8", "http://10.10.10.1/"},
		{"rfc1918 172.16/12", "http://172.20.0.5/"},
		{"link-local", "http://169.254.169.254/latest/meta-data/"},
		{"cgnat", "http://100.64.0.1/"},
		{"unspecified", "http://0.0.0.0/"},
		{"ipv4-mapped loopback", "http://[::ffff:127.0.0.1]/"},
		{"ipv4-mapped prowlarr", "http://[::ffff:192.168.0.115]/"},
		{"nat64 wrapping prowlarr", "http://[64:ff9b::c0a8:73]/"},
		{"6to4 wrapping prowlarr", "http://[2002:c0a8:0073::]/"},
		{"ipv6 unique local", "http://[fd00::1]/"},
		{"ipv6 link-local", "http://[fe80::1]/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveTarget(context.Background(), nil, tc.url); err == nil {
				t.Fatalf("resolveTarget(%q) was ALLOWED; it must be refused", tc.url)
			}
		})
	}
}

// 169.254.169.254 is the cloud metadata address. It is in the list above, but
// it earns its own test because it is the single most valuable SSRF target on
// any hosted box and a regression here would be worse than the rest combined.
func TestRefusesCloudMetadataEndpoint(t *testing.T) {
	if publicRoutable(netip.MustParseAddr("169.254.169.254")) {
		t.Fatal("cloud metadata address classified as publicly routable")
	}
}

func TestAllowsOrdinaryPublicAddresses(t *testing.T) {
	for _, s := range []string{"93.184.216.34", "8.8.8.8", "104.129.28.137", "2606:2800:220:1::1"} {
		if !publicRoutable(netip.MustParseAddr(s)) {
			t.Errorf("publicRoutable(%s) = false, want true", s)
		}
	}
}

func TestRefusesNonHTTPSchemes(t *testing.T) {
	// gopher:// is the classic way to make a fetcher speak an arbitrary line
	// protocol at an internal service; file:// reads the disk.
	for _, u := range []string{
		"file:///etc/passwd",
		"gopher://192.168.0.115:6379/_SET%20x%20y",
		"ftp://example.com/x",
		"dict://127.0.0.1:11211/stat",
		"jar:http://example.com/a!/b",
		"data:text/html,hi",
		"//example.com/protocol-relative",
	} {
		if _, err := parseTargetURL(u); err == nil {
			t.Errorf("parseTargetURL(%q) was allowed; want refusal", u)
		}
	}
}

func TestRefusesEmbeddedCredentials(t *testing.T) {
	// http://expected.com@192.168.0.115/ is a host confusion trick: it reads
	// as the public host to a person and resolves to the private one.
	if _, err := parseTargetURL("http://youtube.com@192.168.0.115/"); err == nil {
		t.Fatal("URL with embedded credentials was allowed")
	}
}

func TestRefusesOverlongURL(t *testing.T) {
	if _, err := parseTargetURL("https://example.com/" + strings.Repeat("a", maxURLLen)); err == nil {
		t.Fatal("overlong URL was allowed")
	}
}

func TestAcceptsOrdinaryPublicURLShapes(t *testing.T) {
	for _, u := range []string{
		"https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		"https://vimeo.com/123456",
		"http://example.com/plain",
	} {
		if _, err := parseTargetURL(u); err != nil {
			t.Errorf("parseTargetURL(%q) refused a legitimate URL: %v", u, err)
		}
	}
}

// dialGuarded is the last line: even if a URL somehow reached the dialer, the
// dialer resolves and refuses on its own rather than trusting its caller.
func TestDialGuardedRefusesPrivateTargets(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9696", "192.168.0.115:9696", "localhost:80", "[::1]:80"} {
		conn, err := dialGuarded(context.Background(), "tcp", addr)
		if err == nil {
			conn.Close()
			t.Errorf("dialGuarded connected to %s; it must refuse", addr)
		}
	}
}

// A public first hop that redirects to a private address must be refused at
// the redirect, not followed.
//
// The test server is on loopback, so dialGuarded would refuse hop 1 on its own
// and the redirect policy would never be reached. To exercise the redirect
// policy itself, this client keeps guardedClient's real CheckRedirect and
// swaps only the dialer. That is deliberately the weaker of the two guards
// under test: it proves the redirect check stands up by itself, with the
// address check taken away.
func TestRedirectToPrivateIsRefused(t *testing.T) {
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the guard followed a redirect onto a private address — request reached %s", r.URL)
		w.WriteHeader(http.StatusOK)
	}))
	defer private.Close()

	for _, target := range []string{
		"http://127.0.0.1:9696/api/v1/indexer",
		"http://192.168.0.115:9696/api/v1/indexer",
		private.URL + "/reached",
	} {
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		}))

		client := guardedClient(10 * time.Second)
		client.Transport = &http.Transport{} // ordinary dialer: only the redirect guard is left

		resp, err := client.Get(redirector.URL)
		if err == nil {
			resp.Body.Close()
			// net/http surfaces a CheckRedirect refusal as an error. If it
			// somehow returned a response, it must be the 302 itself and never
			// the private body.
			if resp.StatusCode != http.StatusFound {
				t.Errorf("redirect to %s produced status %d; expected refusal", target, resp.StatusCode)
			}
		} else if !strings.Contains(err.Error(), errBlocked.Error()) &&
			!strings.Contains(err.Error(), "refus") && !strings.Contains(err.Error(), "resolve") {
			t.Errorf("redirect to %s failed with an unexpected error: %v", target, err)
		}
		redirector.Close()
	}
}

func TestRedirectHopsAreCapped(t *testing.T) {
	client := guardedClient(time.Second)
	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	via := make([]*http.Request, 5)
	for i := range via {
		via[i] = req
	}
	if err := client.CheckRedirect(req, via); err == nil {
		t.Fatal("a 6th redirect hop was allowed; CheckRedirect replaces net/http's own cap so it must impose one")
	}
}

// ------------------------------------------------------------- egress ----

// The egress proxy is the guard for everything yt-dlp does. If it can be
// talked into reaching the LAN, the Go-side checks are decoration.
func TestEgressProxyRefusesPrivateAbsoluteForm(t *testing.T) {
	p, err := newEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	for _, target := range []string{
		"http://127.0.0.1:8802/api/search?q=x",
		"http://192.168.0.115:9696/api/v1/indexer",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
	} {
		req, err := http.NewRequest("GET", target, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Send it the way a proxy client does: absolute-form, to the proxy.
		conn, err := net.DialTimeout("tcp", p.addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := req.WriteProxy(conn); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), req)
		if err != nil {
			conn.Close()
			t.Fatalf("proxy gave no response for %s: %v", target, err)
		}
		resp.Body.Close()
		conn.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("egress proxy answered %d for %s; want 403", resp.StatusCode, target)
		}
	}
	if _, refused := p.stats(); refused == 0 {
		t.Error("refusals were not counted, so a probe would be invisible on /api/link/health")
	}
}

func TestEgressProxyRefusesPrivateConnect(t *testing.T) {
	p, err := newEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	for _, hostport := range []string{"192.168.0.115:443", "127.0.0.1:443", "localhost:443"} {
		code, err := proxyConnect(p.addr, hostport)
		if err != nil {
			t.Fatalf("no response to CONNECT %s: %v", hostport, err)
		}
		if code == http.StatusOK {
			t.Errorf("egress proxy opened a tunnel to %s", hostport)
		}
	}
}

// proxyConnect speaks a raw CONNECT to the proxy and returns the status code.
// Written by hand rather than through http.Request.Write so the request line
// is unambiguously "CONNECT host:port HTTP/1.1".
func proxyConnect(proxyAddr, hostport string) (int, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("CONNECT " + hostport + " HTTP/1.1\r\nHost: " + hostport + "\r\n\r\n")); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// A CONNECT to a port that is not 80/443 must be refused even for a public
// host: this is a media resolver, not a port forwarder.
func TestEgressProxyRefusesOddPorts(t *testing.T) {
	p, err := newEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	code, err := proxyConnect(p.addr, "example.com:22")
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusForbidden {
		t.Errorf("CONNECT to port 22 answered %d; want 403", code)
	}
}

func TestResolveHostPublicRejectsUnresolvable(t *testing.T) {
	if _, err := resolveHostPublic(context.Background(), nil,
		"this-name-does-not-exist.invalid"); err == nil {
		t.Fatal("an unresolvable host was accepted")
	}
}
