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
	Kind     string   `json:"kind"` // video | audio | image | game | other
	Sources  []source `json:"sources"`
	Best     int      `json:"best"`    // index into Sources
	Seeders  int      `json:"seeders"` // max across sources
	Art      artwork  `json:"art"`
	Groups   []string `json:"groups"`
	Adult    bool     `json:"adult"`

	// Instant marks a result that is served over HTTP by a host that is always
	// up, rather than by whoever happens to be seeding. It has no seeder count
	// because the question does not apply -- it will play.
	Instant bool `json:"instant,omitempty"`
	// Popular is the host's own demand signal (archive.org download count),
	// used where seeders would be for a torrent.
	Popular int `json:"popular,omitempty"`
	// Platform is the console or system, for game results.
	Platform string `json:"platform,omitempty"`
}

type cacheEntry struct {
	cards   []card
	expires time.Time
}

type server struct {
	prowlarrURL string
	apiKey      string
	ttl         time.Duration

	mu    sync.RWMutex
	cache map[string]cacheEntry

	// inflight collapses concurrent identical queries into one upstream call.
	// A search fans out to ~28 indexers and can take a minute; without this,
	// three people searching the same title triple the load on Prowlarr and
	// make the timeouts that much likelier.
	flightMu sync.Mutex
	inflight map[string]chan struct{}

	// The enabled-indexer list, so the fan-out does not re-read it per search.
	ixMu      sync.Mutex
	ixCache   []indexerRef
	ixExpires time.Time

	tmdb     *tmdbClient
	igdb     *igdbClient
	discover discoverCache
	warm     *warmer
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8802", "listen address")
	prowlarr := flag.String("prowlarr", "http://192.168.0.115:9696", "Prowlarr base URL (over the tunnel)")
	ttl := flag.Duration("ttl", defaultTTL, "cache TTL for search results")
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
		inflight:    make(map[string]chan struct{}),
		tmdb:        newTMDB(os.Getenv("TMDB_API_KEY")),
		// Reuses the RomM installation's IGDB credentials. That is the part of
		// RomM that describes games in general; its own API is library-bound
		// and would only describe this installation's shelf.
		igdb: newIGDB(os.Getenv("IGDB_CLIENT_ID"), os.Getenv("IGDB_CLIENT_SECRET")),
	}
	s.warm = newWarmer(s)
	go s.evictLoop()
	// Pre-search the titles on the landing rails so the common path --
	// browse trending, click a poster -- hits cache instead of a 14s fan-out.
	go s.warm.run()

	mux := http.NewServeMux()
	auth := loadAuthConfig()
	if auth.Enabled {
		switch auth.Scope {
		case scopeAll:
			log.Printf("sign-in required for everyone (client %s…)", auth.ClientID[:8])
		case scopeOff:
			log.Printf("sign-in configured but switched off (AUTH_SCOPE=off)")
		default:
			log.Printf("sign-in required for TV clients only (client %s…)", auth.ClientID[:8])
		}
	} else {
		log.Printf("sign-in NOT configured; the site is open")
	}

	// Sign-in endpoints are deliberately outside the gate: a person who cannot
	// reach the login page cannot ever get through it.
	mux.HandleFunc("/auth/login", auth.handleLogin)
	mux.HandleFunc("/auth/callback", auth.handleCallback)
	mux.HandleFunc("/auth/logout", auth.handleLogout)
	mux.HandleFunc("/auth/verify", auth.handleVerify)
	mux.HandleFunc("/auth/me", auth.handleMe)

	// The data is what actually needs protecting. Gating it here means the API
	// is safe even if the edge is ever misconfigured -- the check does not
	// depend on Caddy getting its forward_auth right.
	mux.HandleFunc("/api/search", auth.requireAuth(s.handleSearch))
	mux.HandleFunc("/api/discover", auth.requireAuth(s.handleDiscover))
	// Health stays open so a monitor does not need a session to see the
	// service is alive.
	mux.HandleFunc("/api/health", s.handleHealth)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      130 * time.Second,
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
	if len(q) > 128 {
		q = q[:128]
	}
	f := parseFilters(r.URL.Query())
	kind := f.Kind

	// A category with no words is a browse: "show me games". Requiring a query
	// meant the category chips could only ever narrow results that a text
	// search had already produced -- so there was no way to simply ask for a
	// category, which is the first thing anyone tries.
	// A kind with no words is a browse for the same reason a group is --
	// "show me comics". Only groups were accepted, so picking Comics or Images
	// without typing anything returned "missing q", which on a TV remote is
	// the normal way to use it.
	if q == "" && len(f.Groups) == 0 && f.Kind == "" {
		writeJSON(w, 400, map[string]string{"error": "missing q"})
		return
	}
	// A set-top box cannot fall back to software decode the way a browser can,
	// so unplayable sources are removed rather than shown and failed.
	dev := deviceProfileFor(r.URL.Query().Get("device"))
	cacheKey := searchCacheKey(q, kind)

	// Set when the indexers failed but archive.org answered, so the response
	// can admit it is incomplete rather than presenting a partial result as a
	// whole one.
	partial := false

	// Filters are applied to the cached result set, so changing one is instant
	// and costs no indexer traffic.
	respond := func(cards []card, cacheState string, stale bool) {
		visible := dev.applyDevice(cards)
		body := map[string]any{
			"query":  q,
			"cards":  f.apply(visible),
			"facets": buildFacets(visible),
			"total":  len(visible),
		}
		if dev != nil {
			body["device"] = dev.Name
			body["filteredOut"] = len(cards) - len(visible)
		}
		if stale {
			body["stale"] = true
		}
		if partial {
			body["partial"] = true
		}
		w.Header().Set("X-Cache", cacheState)
		writeJSON(w, 200, body)
	}

	if cards, ok := s.getCached(cacheKey); ok {
		respond(cards, "HIT", false)
		return
	}

	// Collapse duplicate concurrent queries; the loser waits and then reads
	// whatever the winner cached.
	if wait, leader := s.claim(cacheKey); !leader {
		select {
		case <-wait:
		case <-r.Context().Done():
			return
		}
		if cards, ok := s.getAny(cacheKey); ok {
			respond(cards, "COALESCED", false)
			return
		}
	} else {
		defer s.release(cacheKey)
	}

	// archive.org runs concurrently with the indexers rather than after them:
	// it answers in well under a second while a 28-indexer fan-out can take
	// most of a minute, so making it wait would be pure added latency.
	type iaResult struct {
		cards []card
		err   error
	}
	ia := make(chan iaResult, 1)
	// archive.org is asked for any kind it has a scope for, not just games.
	// Restricting it to games was why `kind=image` and `kind=comic` had no
	// source of their own -- the torrent indexers carry almost no images, so
	// the honest answer for those kinds comes from here.
	_, wantArchive := scopeFor(kind)
	if wantArchive {
		go func() {
			c, err := s.searchArchive(r.Context(), q, kind)
			ia <- iaResult{c, err}
		}()
	}

	cards, err := s.searchProwlarr(r.Context(), q, kind)

	if wantArchive {
		got := <-ia
		if got.err != nil {
			// A dead archive.org must not take the torrent results down with
			// it, so this is logged and dropped rather than returned.
			log.Printf("archive.org search %q: %v", q, got.err)
		} else if len(got.cards) > 0 {
			cards = append(cards, got.cards...)
			// archive.org alone is a usable answer, but saying so silently
			// turned "the indexers failed" into "there are only six results",
			// which is indistinguishable from a working search that found
			// little -- and cost an hour of chasing a break that was not one.
			if err != nil {
				log.Printf("search %q: indexers failed, serving %d archive.org results only: %v",
					q, len(got.cards), err)
				partial = true
			}
			err = nil
		}
	}

	if err != nil {
		log.Printf("search %q: %v", q, err)
		// A slow indexer should not turn into a dead end. If we have ever had
		// results for this query, stale ones beat an error page.
		if stale, ok := s.getAny(cacheKey); ok {
			respond(stale, "STALE", true)
			return
		}
		writeJSON(w, 504, map[string]string{
			"error": "indexers are taking too long right now — try again in a moment",
		})
		return
	}

	// Artwork only for the leading cards: enriching 200 of them would be slow
	// and most are never scrolled to.
	s.tmdb.enrich(r.Context(), cards, 40)

	s.putCached(cacheKey, cards)
	respond(cards, "MISS", false)
}

// claim returns (wait, true) for the goroutine that should do the upstream
// call, or (wait, false) for one that should wait on the leader.
func (s *server) claim(k string) (<-chan struct{}, bool) {
	s.flightMu.Lock()
	defer s.flightMu.Unlock()
	if ch, ok := s.inflight[k]; ok {
		return ch, false
	}
	ch := make(chan struct{})
	s.inflight[k] = ch
	return ch, true
}

func (s *server) release(k string) {
	s.flightMu.Lock()
	defer s.flightMu.Unlock()
	if ch, ok := s.inflight[k]; ok {
		close(ch)
		delete(s.inflight, k)
	}
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

// getAny returns cached cards regardless of freshness. Used only when the
// upstream has failed, where stale results beat an error page.
func (s *server) getAny(k string) ([]card, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.cache[k]
	if !ok || len(e.cards) == 0 {
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
		// Expired means "re-query", not "discard": expired entries are the
		// fallback when indexers time out. Only drop genuinely ancient ones so
		// memory stays bounded on a 1 GB box.
		cutoff := time.Now().Add(-24 * time.Hour)
		s.mu.Lock()
		for k, e := range s.cache {
			if e.expires.Before(cutoff) {
				delete(s.cache, k)
			}
		}
		s.mu.Unlock()
	}
}

// searchProwlarr answers from a per-indexer fan-out, so a slow indexer delays
// only itself. It falls back to the single aggregate call if the fan-out cannot
// start -- that path still works, it is just as slow as its slowest indexer.
func (s *server) searchProwlarr(ctx context.Context, q, kind string) ([]card, error) {
	// Stragglers must outlive this request: the whole point is to answer now
	// and let the rest land in the cache for the next one. A child of the
	// request context would be cancelled the moment the response is written.
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		fanoutDeadline+stragglerBudget)

	res, err := s.searchFanout(detached, q, kind, fanoutDeadline, func(full []card) {
		defer cancel()
		if len(full) > 0 {
			s.putCached(searchCacheKey(q, kind), full)
		}
	})
	if err == nil {
		if !res.partial {
			cancel()
		}
		if res.partial {
			log.Printf("search %q: answered with %d/%d indexers, rest landing in cache",
				q, res.answered, res.total)
		}
		return res.cards, nil
	}
	cancel()
	log.Printf("search %q: fan-out unavailable (%v), using the aggregate call", q, err)

	return s.searchProwlarrAggregate(ctx, q, kind)
}

func (s *server) searchProwlarrAggregate(ctx context.Context, q, kind string) ([]card, error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
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
	case "game":
		// 1000-1999 is console, 4050-4069 is PC games.
		return []int{1000, 4050}
	case "comic":
		return []int{7030}
	default:
		return nil
	}
}

func kindOf(r prowlarrResult) string {
	for _, c := range r.Categories {
		switch {
		case c.ID >= 1000 && c.ID < 2000, c.ID >= 4050 && c.ID < 4070:
			return "game"
		case c.ID >= 2000 && c.ID < 3000, c.ID >= 5000 && c.ID < 6000:
			return "video"
		case c.ID >= 3000 && c.ID < 4000:
			return "audio"
		// 7030 is comics, which sits inside the 7000-7999 book range -- so a
		// bare "is it 7000-8000" test swallows it and every comic is filed as
		// a book. Checked first for that reason.
		case c.ID == 7030:
			return "comic"
		case c.ID >= 7000 && c.ID < 8000:
			return "book"
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
				Groups: groupsFor(r), Adult: isAdult(r, r.Title),
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
		if isAdult(r, r.Title) {
			c.Adult = true
		}
		for _, g := range groupsFor(r) {
			if !containsFold(c.Groups, g) || len(c.Groups) == 0 {
				if !hasString(c.Groups, g) {
					c.Groups = append(c.Groups, g)
				}
			}
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
		// A player with a touch pad outranks one without, because the archive's
		// own emulator expects a keyboard and offers no on-screen controls --
		// on a phone that is a game you can watch but not play.
		if s.Indexer == "EmulatorJS" || s.Indexer == "Ruffle" {
			n += 200
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
