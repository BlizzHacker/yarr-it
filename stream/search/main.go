// mw-search fronts Prowlarr for the browser.
//
// It exists for three reasons the raw Prowlarr API cannot serve:
//
//  1. Prowlarr lives on the home LAN, reachable only across the WireGuard
//     tunnel. Only search metadata crosses it -- never media bytes -- so the
//     home IP is never in the streaming path.
//  2. A query fans out to ~28 indexers and takes 8 seconds. Caching makes the
//     site feel instant and keeps that fan-out rare.
//  3. Prowlarr returns flat scene filenames. Grouping them into title cards is
//     what makes this feel like a library instead of a directory listing.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type prowlarrResult struct {
	Title       string `json:"title"`
	Indexer     string `json:"indexer"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	Leechers    int    `json:"leechers"`
	Protocol    string `json:"protocol"`
	PublishDate string `json:"publishDate"`
	MagnetURL   string `json:"magnetUrl"`
	DownloadURL string `json:"downloadUrl"`
	InfoURL     string `json:"infoUrl"`
	InfoHash    string `json:"infoHash"`
	GUID        string `json:"guid"`
	Categories  []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"categories"`
}

// source is one torrent -- one row in a title card's quality picker.
type source struct {
	Title     string `json:"title"`
	Indexer   string `json:"indexer"`
	Size      int64  `json:"size"`
	SizeHuman string `json:"sizeHuman"`
	Seeders   int    `json:"seeders"`
	Leechers  int    `json:"leechers"`
	Magnet    string `json:"magnet"`
	Quality   string `json:"quality"`
	Source    string `json:"source"`
	Codec     string `json:"codec"`
	Audio     string `json:"audio"`
	Group     string `json:"group"`
	WebSafe   bool   `json:"webSafe"`
	Published string `json:"published"`
}

// card is a single work (a film, or one episode) with every torrent for it.
type card struct {
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Year     int      `json:"year"`
	IsSeries bool     `json:"isSeries"`
	Season   int      `json:"season,omitempty"`
	Episode  int      `json:"episode,omitempty"`
	Kind     string   `json:"kind"` // video | audio | image | other
	Sources  []source `json:"sources"`
	Best     int      `json:"best"`     // index into Sources
	Seeders  int       `json:"seeders"` // max across sources
}

type cacheEntry struct {
	cards   []card
	expires time.Time
}

type server struct {
	prowlarrURL string
	apiKey      string
	ttl         time.Duration

	mu     sync.RWMutex
	cache  map[string]cacheEntry
	single map[string]*sync.WaitGroup
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8802", "listen address")
	prowlarr := flag.String("prowlarr", "http://192.168.0.115:9696", "Prowlarr base URL (over the tunnel)")
	ttl := flag.Duration("ttl", 15*time.Minute, "cache TTL for search results")
	flag.Parse()

	key := os.Getenv("PROWLARR_API_KEY")
	if key == "" {
		log.Fatal("PROWLARR_API_KEY not set")
	}

	s := &server{
		prowlarrURL: strings.TrimRight(*prowlarr, "/"),
		apiKey:      key,
		ttl:         *ttl,
		cache:       make(map[string]cacheEntry),
		single:      make(map[string]*sync.WaitGroup),
	}
	go s.evictLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/health", s.handleHealth)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      90 * time.Second,
	}
	log.Printf("mw-search listening on %s -> %s (ttl %s)", *addr, s.prowlarrURL, *ttl)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.prowlarrURL+"/api/v1/health", nil)
	req.Header.Set("X-Api-Key", s.apiKey)
	resp, err := http.DefaultClient.Do(req)
	status := "down"
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			status = "ok"
		}
	}
	s.mu.RLock()
	n := len(s.cache)
	s.mu.RUnlock()
	writeJSON(w, 200, map[string]any{"prowlarr": status, "cachedQueries": n})
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 400, map[string]string{"error": "missing q"})
		return
	}
	if len(q) > 128 {
		q = q[:128]
	}
	kind := r.URL.Query().Get("kind")
	cacheKey := kind + "\x00" + strings.ToLower(q)

	if cards, ok := s.getCached(cacheKey); ok {
		w.Header().Set("X-Cache", "HIT")
		writeJSON(w, 200, map[string]any{"query": q, "cards": cards})
		return
	}

	cards, err := s.searchProwlarr(r.Context(), q, kind)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "upstream search failed"})
		log.Printf("search %q: %v", q, err)
		return
	}
	s.putCached(cacheKey, cards)
	w.Header().Set("X-Cache", "MISS")
	writeJSON(w, 200, map[string]any{"query": q, "cards": cards})
}

func (s *server) getCached(k string) ([]card, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.cache[k]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.cards, true
}

func (s *server) putCached(k string, cards []card) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[k] = cacheEntry{cards: cards, expires: time.Now().Add(s.ttl)}
}

func (s *server) evictLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		s.mu.Lock()
		for k, e := range s.cache {
			if now.After(e.expires) {
				delete(s.cache, k)
			}
		}
		s.mu.Unlock()
	}
}

func (s *server) searchProwlarr(ctx context.Context, q, kind string) ([]card, error) {
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()

	u := fmt.Sprintf("%s/api/v1/search?query=%s&limit=200", s.prowlarrURL, url.QueryEscape(q))
	for _, c := range categoriesFor(kind) {
		u += "&categories=" + strconv.Itoa(c)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", s.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("prowlarr status %d", resp.StatusCode)
	}

	var raw []prowlarrResult
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return buildCards(raw), nil
}

// categoriesFor maps the UI's media filter onto Newznab category ids.
func categoriesFor(kind string) []int {
	switch kind {
	case "video":
		return []int{2000, 5000}
	case "audio":
		return []int{3000}
	case "image":
		return []int{}
	default:
		return nil
	}
}

func kindOf(r prowlarrResult) string {
	for _, c := range r.Categories {
		switch {
		case c.ID >= 2000 && c.ID < 3000, c.ID >= 5000 && c.ID < 6000:
			return "video"
		case c.ID >= 3000 && c.ID < 4000:
			return "audio"
		}
	}
	return "other"
}

func buildCards(raw []prowlarrResult) []card {
	byKey := map[string]*card{}

	for _, r := range raw {
		// Streaming needs a swarm to join. Usenet has no peers.
		if r.Protocol != "torrent" {
			continue
		}
		magnet := usableMagnet(r)
		if magnet == "" {
			continue
		}
		p := parseRelease(r.Title)
		if p.Title == "" {
			continue
		}
		k := p.groupKey()
		c, ok := byKey[k]
		if !ok {
			c = &card{
				Key: k, Title: p.Title, Year: p.Year, IsSeries: p.IsSeries,
				Season: p.Season, Episode: p.Episode, Kind: kindOf(r),
			}
			byKey[k] = c
		}
		c.Sources = append(c.Sources, source{
			Title: r.Title, Indexer: r.Indexer, Size: r.Size, SizeHuman: humanSize(r.Size),
			Seeders: r.Seeders, Leechers: r.Leechers, Magnet: magnet,
			Quality: p.Quality, Source: p.Source, Codec: p.Codec, Audio: p.Audio,
			Group: p.Group, WebSafe: p.WebSafe, Published: r.PublishDate,
		})
		if r.Seeders > c.Seeders {
			c.Seeders = r.Seeders
		}
	}

	out := make([]card, 0, len(byKey))
	for _, c := range byKey {
		rankSources(c)
		out = append(out, *c)
	}
	// Most-seeded first: seeders are the best available proxy for "will this
	// actually start playing".
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seeders != out[j].Seeders {
			return out[i].Seeders > out[j].Seeders
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// rankSources orders a card's torrents and picks the default. A well-seeded
// web-safe 1080p beats a dead 4K HEVC every time, because the first one plays.
func rankSources(c *card) {
	score := func(s source) int {
		n := 0
		switch {
		case s.Seeders >= 50:
			n += 60
		case s.Seeders >= 10:
			n += 45
		case s.Seeders >= 3:
			n += 25
		case s.Seeders >= 1:
			n += 10
		}
		if s.WebSafe {
			n += 25
		}
		n += qualityRank(s.Quality) * 3
		return n
	}
	sort.Slice(c.Sources, func(i, j int) bool {
		si, sj := score(c.Sources[i]), score(c.Sources[j])
		if si != sj {
			return si > sj
		}
		return c.Sources[i].Seeders > c.Sources[j].Seeders
	})
	c.Best = 0
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
