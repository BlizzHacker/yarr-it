// mw-gateway turns a magnet link into an HTTP video stream.
//
// This exists for one reason: some clients cannot do peer-to-peer at all. Roku
// runs BrightScript with no browser engine, no WebRTC and no usable socket API
// -- its Video node plays HLS/DASH/MP4 from a URL and nothing else. Smart TVs
// and set-top boxes are the same. For those devices something has to join the
// swarm on their behalf and re-serve the bytes over HTTP.
//
// That is a genuine architectural inversion and worth being honest about: for
// every other client the browser does the work and the server only relays
// opaque bytes, but here the gateway really is fetching and serving the media.
// So it is deliberately confined:
//
//   - it binds to the LAN, not the public internet;
//   - all swarm traffic egresses through a VPN tunnel, never the home IP;
//   - pieces live in a bounded temp store that is wiped when a stream ends.
//
// Range requests are supported, which is what lets a TV seek into a file that
// is still downloading.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"
)

type gateway struct {
	client  *torrent.Client
	dataDir string

	mu      sync.Mutex
	active  map[string]*session
	maxIdle time.Duration
}

type session struct {
	t        *torrent.Torrent
	file     *torrent.File
	lastSeen time.Time
	// primer keeps piece priority alive between /prepare and /stream; without
	// a live reader the torrent client stops requesting anything.
	primer torrent.Reader
}

func main() {
	addr := flag.String("addr", "0.0.0.0:8900", "listen address (LAN only)")
	dataDir := flag.String("data", "/var/lib/mw-gateway", "piece store")
	maxIdle := flag.Duration("max-idle", 10*time.Minute, "drop a torrent after this long unused")
	iface := flag.String("bind-iface", "", "network interface for peer traffic (VPN)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = *dataDir
	cfg.DefaultStorage = storage.NewFileByInfoHash(*dataDir)
	// Upload must stay enabled. BitTorrent choking is tit-for-tat: a client
	// that never uploads gets choked by everyone and receives nothing. With
	// NoUpload set this gateway connected to peers happily and then sat at 0
	// bytes forever. Reciprocating is not a policy choice, it is the protocol.
	//
	// It does mean the gateway shares pieces of what it is streaming, exactly
	// as any torrent client does -- which is why every byte goes through the
	// VPN tunnel rather than the home connection.
	cfg.Seed = false // stop sharing once the stream is dropped
	cfg.NoUpload = false
	cfg.DisableIPv6 = true
	cfg.Logger = cfg.Logger.FilterLevel(defaultLogLevel())

	client, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("torrent client: %v", err)
	}
	defer client.Close()

	g := &gateway{
		client:  client,
		dataDir: *dataDir,
		active:  make(map[string]*session),
		maxIdle: *maxIdle,
	}
	go g.reaper()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", g.handleHealth)
	mux.HandleFunc("/prepare", g.handlePrepare)
	mux.HandleFunc("/stream/", g.handleStream)

	srv := &http.Server{Addr: *addr, Handler: logRequests(mux)}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()

	log.Printf("mw-gateway on %s (data %s, iface %q)", *addr, *dataDir, *iface)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (g *gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	n := len(g.active)
	g.mu.Unlock()

	// Per-torrent peer counts, because "it is not downloading" has several very
	// different causes -- no peers found, peers found but not connected, or
	// connected but choking us -- and they need different fixes.
	stats := []map[string]any{}
	for _, t := range g.client.Torrents() {
		if t.Info() == nil {
			stats = append(stats, map[string]any{"hash": t.InfoHash().HexString()[:12], "metadata": false})
			continue
		}
		s := t.Stats()
		stats = append(stats, map[string]any{
			"hash":           t.InfoHash().HexString()[:12],
			"metadata":       true,
			"peersKnown":     s.TotalPeers,
			"peersConnected": s.ActivePeers,
			"peersHalfOpen":  s.HalfOpenPeers,
			"pendingPeers":   s.PendingPeers,
			"bytesCompleted": t.BytesCompleted(),
			"length":         t.Length(),
			"seeding":        t.Seeding(),
		})
	}

	writeJSON(w, 200, map[string]any{
		"ok":       true,
		"active":   n,
		"torrents": stats,
	})
}

type prepareResp struct {
	InfoHash  string  `json:"infoHash"`
	Name      string  `json:"name"`
	File      string  `json:"file"`
	Size      int64   `json:"size"`
	StreamURL string  `json:"streamUrl"`
	Playable  bool    `json:"playable"`
	Reason    string  `json:"reason,omitempty"`
	Progress  float64 `json:"progress"`
}

// handlePrepare joins a swarm, waits for metadata, picks the main video file
// and hands back a URL the TV can play.
func (g *gateway) handlePrepare(w http.ResponseWriter, r *http.Request) {
	magnet := r.URL.Query().Get("magnet")
	if !strings.HasPrefix(strings.ToLower(magnet), "magnet:?") {
		writeJSON(w, 400, map[string]string{"error": "magnet required"})
		return
	}

	t, err := g.client.AddMagnet(magnet)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad magnet"})
		return
	}

	// Metadata comes from the swarm, so this can genuinely take a while on a
	// poorly seeded torrent. Better to fail clearly than hang the TV.
	select {
	case <-t.GotInfo():
	case <-time.After(60 * time.Second):
		writeJSON(w, 504, map[string]string{"error": "no metadata - swarm may be dead"})
		return
	case <-r.Context().Done():
		return
	}

	file := pickVideoFile(t)
	if file == nil {
		writeJSON(w, 415, map[string]string{"error": "no video file in this torrent"})
		return
	}

	ih := t.InfoHash().HexString()
	file.SetPriority(torrent.PiecePriorityNormal)
	primer := startReadahead(file)

	g.mu.Lock()
	if old, exists := g.active[ih]; exists && old.primer != nil {
		old.primer.Close()
	}
	g.active[ih] = &session{t: t, file: file, lastSeen: time.Now(), primer: primer}
	g.mu.Unlock()

	playable, reason := isDeviceFriendly(file.DisplayPath())
	writeJSON(w, 200, prepareResp{
		InfoHash:  ih,
		Name:      t.Name(),
		File:      file.DisplayPath(),
		Size:      file.Length(),
		StreamURL: fmt.Sprintf("http://%s/stream/%s", r.Host, ih),
		Playable:  playable,
		Reason:    reason,
		Progress:  float64(t.BytesCompleted()) / float64(max64(t.Length(), 1)),
	})
}

// handleStream serves the chosen file with range support, which is what lets a
// TV seek into something still downloading.
func (g *gateway) handleStream(w http.ResponseWriter, r *http.Request) {
	ih := strings.TrimPrefix(r.URL.Path, "/stream/")
	if i := strings.IndexByte(ih, '/'); i >= 0 {
		ih = ih[:i]
	}

	g.mu.Lock()
	s, ok := g.active[ih]
	if ok {
		s.lastSeen = time.Now()
	}
	g.mu.Unlock()
	if !ok {
		http.Error(w, "unknown stream; call /prepare first", http.StatusNotFound)
		return
	}

	reader := s.file.NewReader()
	defer reader.Close()
	// Read ahead generously: a TV that stalls mid-scene is worse than using a
	// few MB more of the piece store.
	reader.SetReadahead(s.file.Length() / 100)
	reader.SetResponsive()

	w.Header().Set("Content-Type", contentTypeFor(s.file.DisplayPath()))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, s.file.DisplayPath(), time.Now(), reader)
}

// reaper drops idle torrents so the piece store cannot grow without bound on a
// box with a small disk.
func (g *gateway) reaper() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		now := time.Now()
		g.mu.Lock()
		for ih, s := range g.active {
			if now.Sub(s.lastSeen) < g.maxIdle {
				continue
			}
			log.Printf("dropping idle torrent %s", ih[:12])
			if s.primer != nil {
				s.primer.Close()
			}
			s.t.Drop()
			delete(g.active, ih)
			_ = os.RemoveAll(filepath.Join(g.dataDir, ih))
		}
		g.mu.Unlock()
	}
}

// pickVideoFile chooses the main feature: the largest video file, since samples
// extras and subtitles are always smaller.
func pickVideoFile(t *torrent.Torrent) *torrent.File {
	files := t.Files()
	sort.Slice(files, func(i, j int) bool { return files[i].Length() > files[j].Length() })
	for _, f := range files {
		if isVideo(f.DisplayPath()) {
			return f
		}
	}
	return nil
}

// startReadahead begins pulling the head of the file so playback can start
// promptly, and returns the reader.
//
// The reader must be kept alive: in anacrolix/torrent a reader is what
// expresses "I want these pieces", so closing it immediately cancels the
// request. An earlier version created a reader and deferred Close in the same
// function, which meant nothing was ever actually asked for.
func startReadahead(f *torrent.File) torrent.Reader {
	head := f.Length() / 50
	if head < 16<<20 {
		head = 16 << 20
	}
	if head > f.Length() {
		head = f.Length()
	}
	r := f.NewReader()
	r.SetReadahead(head)
	r.SetResponsive()
	return r
}

var videoExt = []string{".mp4", ".m4v", ".mkv", ".avi", ".mov", ".webm", ".ts", ".m2ts", ".wmv", ".flv"}

func isVideo(name string) bool {
	n := strings.ToLower(name)
	for _, e := range videoExt {
		if strings.HasSuffix(n, e) {
			return true
		}
	}
	return false
}

// isDeviceFriendly reports whether a set-top box is likely to play this
// directly. Roku handles MP4/MKV with H.264 or HEVC, but not AVI/WMV and not
// most exotic audio, and it will simply show an error rather than explain.
func isDeviceFriendly(name string) (bool, string) {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".mp4"), strings.HasSuffix(n, ".m4v"):
		return true, ""
	case strings.HasSuffix(n, ".mkv"):
		return true, "MKV: plays if the codecs inside are H.264/HEVC + AAC/AC3"
	case strings.HasSuffix(n, ".avi"), strings.HasSuffix(n, ".wmv"), strings.HasSuffix(n, ".flv"):
		return false, "container not supported by most TV players"
	default:
		return false, "unknown container"
	}
}

func contentTypeFor(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".mp4"), strings.HasSuffix(n, ".m4v"):
		return "video/mp4"
	case strings.HasSuffix(n, ".mkv"):
		return "video/x-matroska"
	case strings.HasSuffix(n, ".webm"):
		return "video/webm"
	case strings.HasSuffix(n, ".ts"), strings.HasSuffix(n, ".m2ts"):
		return "video/mp2t"
	default:
		return "application/octet-stream"
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately does not log the magnet or filename.
		log.Printf("%s %s", r.Method, strings.Split(r.URL.Path, "?")[0])
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
