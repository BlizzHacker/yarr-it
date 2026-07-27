// mw-bridge is a byte relay that lets a browser talk to BitTorrent peers.
//
// Browsers cannot open TCP sockets or send UDP. This service gives a page
// exactly one capability it otherwise lacks: "connect me to host:port and pipe
// bytes". It does not speak BitTorrent. It never parses the peer wire protocol,
// never assembles a piece, never learns a filename or an infohash, never writes
// payload to disk, and keeps no cache.
//
// That restraint is the point. It is what makes this a conduit rather than a
// host, so it is enforced here rather than left as a convention:
//   - the peer address arrives in the first WebSocket frame, never in the URL,
//     so a reverse-proxy access log cannot record who was contacted;
//   - only counters are logged, never addresses or payload;
//   - private and loopback ranges are refused, so this cannot be used to probe
//     the tunnel or the home LAN behind it.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const (
	dialTimeout   = 10 * time.Second
	idleTimeout   = 120 * time.Second
	udpTimeout    = 15 * time.Second
	maxFrameBytes = 1 << 20 // 1 MiB; BitTorrent blocks are 16 KiB
)

type limits struct {
	perIP  int
	global int
}

type counters struct {
	active    atomic.Int64
	dialed    atomic.Int64
	refused   atomic.Int64
	bytesUp   atomic.Int64
	bytesDown atomic.Int64
}

type server struct {
	lim        limits
	cnt        counters
	mu         sync.Mutex
	perIP      map[string]int
	budget     *budget
	iptvBudget *budget
}

// hello is the first frame a client sends. Keeping the target here rather than
// in the query string is deliberate: it stays out of proxy access logs.
type hello struct {
	Proto string `json:"proto"` // "tcp" or "udp"
	Host  string `json:"host"`
	Port  int    `json:"port"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8801", "listen address")
	perIP := flag.Int("per-ip", 40, "max concurrent relayed sockets per client IP")
	global := flag.Int("global", 800, "max concurrent relayed sockets overall")
	budgetGiB := flag.Int64("budget-gib", 2600, "monthly relay budget in GiB before degrading")
	iptvBudgetGiB := flag.Int64("iptv-budget-gib", 0,
		"monthly IPTV proxy budget in GiB (0 = a quarter of -budget-gib)")
	statePath := flag.String("state", "/var/lib/mw-bridge/budget.json", "budget state file")
	flag.Parse()

	if *iptvBudgetGiB == 0 {
		*iptvBudgetGiB = defaultIPTVBudgetGiB(*budgetGiB)
	}

	s := &server{
		lim:        limits{perIP: *perIP, global: *global},
		perIP:      make(map[string]int),
		budget:     newBudget(*statePath, *budgetGiB<<30),
		iptvBudget: newBudget(*statePath+".iptv", *iptvBudgetGiB<<30),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/bridge/socket", s.handleSocket)
	mux.HandleFunc("/bridge/announce", s.handleAnnounce)
	mux.HandleFunc("/bridge/health", s.handleHealth)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go s.budget.persistLoop()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("mw-bridge listening on %s (per-ip=%d global=%d budget=%d GiB)",
		*addr, *perIP, *global, *budgetGiB)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
	s.budget.persist()
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	used, cap_ := s.budget.snapshot()
	iptvUsed, iptvCap := s.iptvBudget.snapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"active":           s.cnt.active.Load(),
		"dialed":           s.cnt.dialed.Load(),
		"refused":          s.cnt.refused.Load(),
		"relayed_up":       s.cnt.bytesUp.Load(),
		"relayed_down":     s.cnt.bytesDown.Load(),
		"budget_used":      used,
		"budget_cap":       cap_,
		"iptv_budget_used": iptvUsed,
		"iptv_budget_cap":  iptvCap,
		"degraded":         s.budget.degraded(),
	})
}

// clientIP prefers the reverse proxy's forwarded address; Caddy sets it.
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		if i := len(f); i > 0 {
			for j := 0; j < len(f); j++ {
				if f[j] == ',' {
					return f[:j]
				}
			}
			return f
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) acquire(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.perIP {
		total += n
	}
	if total >= s.lim.global || s.perIP[ip] >= s.lim.perIP {
		return false
	}
	s.perIP[ip]++
	return true
}

func (s *server) release(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perIP[ip] <= 1 {
		delete(s.perIP, ip)
		return
	}
	s.perIP[ip]--
}

func (s *server) handleSocket(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)

	// When the monthly budget is spent the relay stops accepting new sockets.
	// Clients fall back to web seeds and WebRTC peers, which cost nothing and
	// still serve anything popular.
	if s.budget.degraded() {
		s.cnt.refused.Add(1)
		http.Error(w, "relay budget exhausted; use webseed/webrtc", http.StatusServiceUnavailable)
		return
	}
	if !s.acquire(ip) {
		s.cnt.refused.Add(1)
		http.Error(w, "too many concurrent relays", http.StatusTooManyRequests)
		return
	}
	defer s.release(ip)

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  []string{"stream.moveweight.com", "localhost:*"},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(maxFrameBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var h hello
	readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
	typ, data, err := c.Read(readCtx)
	readCancel()
	if err != nil || typ != websocket.MessageText {
		_ = c.Close(websocket.StatusUnsupportedData, "expected hello")
		return
	}
	if err := json.Unmarshal(data, &h); err != nil {
		_ = c.Close(websocket.StatusUnsupportedData, "bad hello")
		return
	}

	ipAddr := net.ParseIP(h.Host)
	if ipAddr == nil || !publicUnicast(ipAddr) {
		// Refuse names (no DNS from here) and anything not publicly routable.
		// This is what stops the bridge being used to reach 10.10.10.1 or the
		// 192.168.0.0/24 estate behind the tunnel.
		_ = c.Close(websocket.StatusPolicyViolation, "target not permitted")
		return
	}
	if h.Port <= 0 || h.Port > 65535 {
		_ = c.Close(websocket.StatusPolicyViolation, "bad port")
		return
	}

	target := net.JoinHostPort(ipAddr.String(), strconv.Itoa(h.Port))
	s.cnt.dialed.Add(1)
	s.cnt.active.Add(1)
	defer s.cnt.active.Add(-1)

	switch h.Proto {
	case "udp":
		s.relayUDP(ctx, c, target)
	default:
		s.relayTCP(ctx, c, target)
	}
}

func (s *server) relayTCP(ctx context.Context, c *websocket.Conn, target string) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		_ = c.Close(websocket.StatusAbnormalClosure, "dial failed")
		return
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}

	// Signal readiness so the client can start the BitTorrent handshake.
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"ok":true}`))

	var wg sync.WaitGroup
	wg.Add(2)

	// peer -> browser
	go func() {
		defer wg.Done()
		buf := make([]byte, 32<<10)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
			n, err := conn.Read(buf)
			if n > 0 {
				s.cnt.bytesDown.Add(int64(n))
				s.budget.add(int64(n))
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
	}()

	// browser -> peer
	go func() {
		defer wg.Done()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				break
			}
			if typ != websocket.MessageBinary {
				continue
			}
			s.cnt.bytesUp.Add(int64(len(data)))
			s.budget.add(int64(len(data)))
			_ = conn.SetWriteDeadline(time.Now().Add(idleTimeout))
			if _, err := conn.Write(data); err != nil {
				break
			}
		}
		_ = conn.Close()
	}()

	wg.Wait()
}

// relayUDP carries tracker announces and DHT, which browsers also cannot send.
// Each WebSocket frame is one datagram, length-prefixed on the way back so the
// client can recover datagram boundaries over a stream-shaped transport.
func (s *server) relayUDP(ctx context.Context, c *websocket.Conn, target string) {
	conn, err := net.DialTimeout("udp", target, dialTimeout)
	if err != nil {
		_ = c.Close(websocket.StatusAbnormalClosure, "dial failed")
		return
	}
	defer conn.Close()
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"ok":true}`))

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		out := make([]byte, 2+2048)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(udpTimeout))
			n, err := conn.Read(buf)
			if n > 0 {
				s.cnt.bytesDown.Add(int64(n))
				s.budget.add(int64(n))
				binary.BigEndian.PutUint16(out[:2], uint16(n))
				copy(out[2:], buf[:n])
				if werr := c.Write(ctx, websocket.MessageBinary, out[:2+n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		s.cnt.bytesUp.Add(int64(len(data)))
		s.budget.add(int64(len(data)))
		if _, err := conn.Write(data); err != nil {
			break
		}
	}
	_ = conn.Close()
	<-done
}

// publicUnicast reports whether addr is a globally routable unicast address.
// Everything else -- loopback, link-local, RFC1918, CGNAT, multicast -- is
// refused so the relay cannot be turned into a scanner for the private estate.
func publicUnicast(addr net.IP) bool {
	if addr == nil || addr.IsLoopback() || addr.IsUnspecified() ||
		addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsPrivate() {
		return false
	}
	if v4 := addr.To4(); v4 != nil {
		// 100.64.0.0/10 carrier-grade NAT, and 0.0.0.0/8.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		if v4[0] == 0 {
			return false
		}
	}
	return true
}

var _ = io.Discard
var _ = os.Getenv
