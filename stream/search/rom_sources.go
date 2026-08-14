package main

// Public ROMarr providers and the browser-captured catalogue they share.
//
// These adapters intentionally stop at the controls each site publishes. A
// hosted player becomes a PLAY THERE row. A normal item page becomes OPEN
// PAGE. A direct file becomes DOWNLOAD only when the provider actually exposes
// one; none of the four live HTML providers below currently does. This keeps a
// provider useful without turning an iframe's implementation detail or a
// signed-in form token into an invented download API.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const romProviderCardLimit = 80

type romCatalogDocument struct {
	Version     int              `json:"version"`
	GeneratedAt string           `json:"generated_at"`
	Items       []romCatalogItem `json:"items"`
}

type romCatalogItem struct {
	Source     string `json:"source"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	Platform   string `json:"platform"`
	Region     string `json:"region"`
	Version    string `json:"version"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	URL        string `json:"url"`
	CapturedAt string `json:"captured_at"`
}

type romCatalogRow struct {
	romCatalogItem
	provider romProvider
	lower    string
	system   string
	target   string
}

type romCatalogStore struct {
	mu        sync.RWMutex
	rows      []romCatalogRow
	generated string
}

func romCatalogPath() string {
	if p := strings.TrimSpace(os.Getenv("ROMARR_CATALOG")); p != "" {
		return p
	}
	return "/var/lib/mw-search/romarr-catalog-metadata.json"
}

func loadROMCatalogStore(filename string) (*romCatalogStore, error) {
	s := &romCatalogStore{}
	raw, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) || filename == "" {
			return s, nil
		}
		return nil, err
	}
	var doc romCatalogDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("ROMarr catalogue %s is unreadable: %w", filename, err)
	}
	if doc.Version != 1 {
		return nil, fmt.Errorf("ROMarr catalogue %s has version %d; only version 1 is supported", filename, doc.Version)
	}
	seen := map[string]bool{}
	for _, item := range doc.Items {
		provider, ok := romProviderByID(item.Source)
		item.ID = strings.TrimSpace(item.ID)
		if !ok || provider.id == "vimm" || provider.id == "theromdepot" || item.ID == "" {
			continue
		}
		key := provider.id + "\x00" + item.ID
		if seen[key] {
			continue
		}
		target, ok := provider.itemURL(item.URL)
		if !ok || strings.TrimSpace(item.Title) == "" {
			continue
		}
		seen[key] = true
		system, platform := romSystem(item.Platform)
		if platform == "" {
			platform = strings.TrimSpace(item.Platform)
		}
		s.rows = append(s.rows, romCatalogRow{
			romCatalogItem: item,
			provider:       provider,
			lower: strings.ToLower(strings.Join([]string{
				item.Title, item.Name, item.Platform, item.Region, item.Version,
			}, " ")),
			system: system,
			target: target,
		})
	}
	s.generated = doc.GeneratedAt
	return s, nil
}

func (s *romCatalogStore) count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rows)
}

func (s *romCatalogStore) stats() map[string]any {
	out := map[string]any{"entries": s.count()}
	if s != nil && s.generated != "" {
		out["generatedAt"] = s.generated
	}
	return out
}

func (s *romCatalogStore) search(q, kind string, systems map[string]bool, limit int) []card {
	if s == nil || (kind != "" && !sameDomain(kind, domainGame)) {
		return nil
	}
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(q)))
	if len(terms) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = romProviderCardLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byProvider := map[string][]card{}
	for _, row := range s.rows {
		if systems != nil && !systems[row.system] || !lowerHasAll(row.lower, terms) {
			continue
		}
		byProvider[row.provider.id] = append(byProvider[row.provider.id],
			row.provider.card(row.ID, row.Title, row.Platform, row.system,
				row.Region, row.Size, row.target))
	}
	// Round-robin across sources. A 30,000-entry CoolROM capture must not use
	// the entire response before the first CDRomance row is reached.
	order := []string{"webmulator", "retrostic", "emuparadise", "romulation", "coolrom", "cdromance"}
	out := make([]card, 0, limit)
	for row := 0; len(out) < limit; row++ {
		added := false
		for _, id := range order {
			cards := byProvider[id]
			if row < len(cards) {
				out = append(out, cards[row])
				added = true
				if len(out) == limit {
					break
				}
			}
		}
		if !added {
			break
		}
	}
	return out
}

type romSourceSet struct {
	providers []romProvider
}

func newROMSourceSet() *romSourceSet {
	ids := []string{"webmulator", "retrostic", "emuparadise", "romulation"}
	set := &romSourceSet{providers: make([]romProvider, 0, len(ids))}
	for _, id := range ids {
		p := romProviders[id]
		p.runtime = newROMProviderRuntime()
		set.providers = append(set.providers, p)
	}
	return set
}

type romProvider struct {
	id         string
	name       string
	short      string
	host       string
	searchURL  func(string) string
	path       *regexp.Regexp
	action     string
	note       string
	account    bool
	parse      func(*html.Node, romProvider) []romHit
	httpClient *http.Client
	runtime    *romProviderRuntime
}

type romProviderCache struct {
	cards   []card
	expires time.Time
}

type romProviderRuntime struct {
	mu    sync.Mutex
	last  time.Time
	cache map[string]romProviderCache
}

func newROMProviderRuntime() *romProviderRuntime {
	return &romProviderRuntime{cache: map[string]romProviderCache{}}
}

type romHit struct {
	id       string
	title    string
	platform string
	region   string
	size     int64
	url      string
}

var romProviders = map[string]romProvider{
	"webmulator": {
		id: "webmulator", name: "Webmulator", short: "Webmulator", host: "webmulator.com",
		searchURL: func(q string) string {
			return "https://www.webmulator.com/search.php?search_term_string=" + url.QueryEscape(q)
		},
		path:   regexp.MustCompile(`^/games/([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*)/?$`),
		action: "play", note: "Hosted player", parse: parseWebmulator,
	},
	"retrostic": {
		id: "retrostic", name: "Retrostic", short: "Retrostic", host: "retrostic.com",
		searchURL: func(q string) string {
			return "https://www.retrostic.com/search?search_term_string=" + url.QueryEscape(q)
		},
		path:   regexp.MustCompile(`^/roms/([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*)/?$`),
		action: "play", note: "Hosted player · browser required to download", parse: parseRetrostic,
	},
	"emuparadise": {
		id: "emuparadise", name: "EmuParadise", short: "EmuParadise", host: "emuparadise.me",
		searchURL: func(q string) string { return "https://www.emuparadise.me/roms/search.php?query=" + url.QueryEscape(q) },
		path:      regexp.MustCompile(`^/[^?#]+/[^?#]+/[0-9]+/?$`),
		action:    "open", note: "Metadata only · files are no longer offered", parse: parseEmuParadise,
	},
	"romulation": {
		id: "romulation", name: "RomuLation", short: "RomuLation", host: "romulation.org",
		searchURL: func(q string) string { return "https://www.romulation.org/roms/search?query=" + url.QueryEscape(q) },
		path:      regexp.MustCompile(`^/rom/([^/]+)/([^/?#]+)(?:/[0-9]+)?/?$`),
		action:    "open", note: "Paid account required to download", account: true, parse: parseRomulation,
	},
	"coolrom": {
		id: "coolrom", name: "CoolROM", short: "CoolROM", host: "coolrom.com",
		path:   regexp.MustCompile(`^/roms/[^/]+/[0-9]+/[^/]+\.php/?$`),
		action: "open", note: "Browser-captured page",
	},
	"cdromance": {
		id: "cdromance", name: "CDRomance", short: "CDRomance", host: "cdromance.org",
		path:   regexp.MustCompile(`^/[^/]+/[^/]+/?$`),
		action: "open", note: "Browser clearance required",
	},
}

func romProviderByID(id string) (romProvider, bool) {
	p, ok := romProviders[strings.ToLower(strings.TrimSpace(id))]
	return p, ok
}

func (p romProvider) client() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 || req.URL.Scheme != "https" || !sameROMHost(req.URL.Hostname(), p.host) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func (p romProvider) itemURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !sameROMHost(u.Hostname(), p.host) || !p.path.MatchString(u.Path) {
		return "", false
	}
	u.RawQuery = ""
	u.Fragment = ""
	if p.id == "retrostic" && !strings.HasSuffix(u.Path, "/play") {
		u.Path = strings.TrimRight(u.Path, "/") + "/play"
	}
	if p.id == "webmulator" && !strings.HasSuffix(u.Path, "/mobile") {
		u.Path = strings.TrimRight(u.Path, "/") + "/mobile"
	}
	return u.String(), true
}

func sameROMHost(got, base string) bool {
	got, base = strings.ToLower(got), strings.ToLower(base)
	return got == base || got == "www."+base || strings.HasSuffix(got, "."+base)
}

func (p romProvider) search(ctx context.Context, q string, limit int) ([]card, error) {
	q = strings.TrimSpace(q)
	if q == "" || p.searchURL == nil || p.parse == nil {
		return nil, nil
	}
	cacheKey := strings.ToLower(q) + "\x00" + strconv.Itoa(limit)
	if p.runtime != nil {
		p.runtime.mu.Lock()
		defer p.runtime.mu.Unlock()
		if cached, ok := p.runtime.cache[cacheKey]; ok && time.Now().Before(cached.expires) {
			return append([]card(nil), cached.cards...), nil
		}
		if wait := 750*time.Millisecond - time.Since(p.runtime.last); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		p.runtime.last = time.Now()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.searchURL(q), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Yarr.It/1.0 (+https://yarrit.com; public provider search)")
	res, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body := io.LimitReader(res.Body, 4<<20)
	doc, err := html.Parse(body)
	if err != nil {
		return nil, err
	}
	hits := p.parse(doc, p)
	terms := queryTerms(q)
	out := make([]card, 0, min(limit, len(hits)))
	seen := map[string]bool{}
	for _, hit := range hits {
		if len(terms) > 0 && titleOverlap(hit.title, terms) < len(terms) {
			continue
		}
		target, ok := p.itemURL(hit.url)
		if !ok {
			continue
		}
		key := p.id + ":" + hit.id
		if seen[key] {
			continue
		}
		seen[key] = true
		system, platform := romSystem(hit.platform)
		if system == "" {
			// Match the plugin contract: an unknown or ambiguous machine is
			// dropped, never filed under a best guess.
			continue
		}
		out = append(out, p.card(hit.id, hit.title, platform, system, hit.region, hit.size, target))
		if len(out) >= limit {
			break
		}
	}
	if p.runtime != nil {
		p.runtime.cache[cacheKey] = romProviderCache{
			cards: append([]card(nil), out...), expires: time.Now().Add(10 * time.Minute),
		}
	}
	return out, nil
}

func (p romProvider) card(id, title, platform, system, region string, size int64, target string) card {
	parts := []string{p.note}
	if platform != "" {
		parts = append(parts, platform)
	}
	if region != "" {
		parts = append(parts, region)
	}
	label := title
	if p.action == "play" {
		label = "Play " + title
	}
	c := card{
		Key: "romprovider:" + p.id + ":" + id, Title: title, Kind: domainGame,
		Platform: platform, System: system, Groups: []string{"games"}, Origin: p.name,
		Sources: []source{{
			Title: label, Indexer: p.name, Size: size, SizeHuman: humanBytes(size),
			Magnet: target, Source: strings.Join(parts, " · "), Offsite: true, Action: p.action,
		}},
		External: &externalSite{Name: p.name, Short: p.short, Host: p.host, Page: target},
		Adult:    isAdultItem(title, "", nil),
	}
	// Webmulator's /mobile URL is an official hosted EmulatorJS page that may be
	// framed. It plays inside Yarr.It through the dedicated resolver. It is not a
	// direct ROM URL and it is not a download.
	if p.id == "webmulator" {
		c.Instant = true
		c.External = nil
		c.Sources[0].Offsite = false
		c.Sources[0].WebSafe = true
		c.Sources[0].Source = "Official hosted EmulatorJS"
	}
	return c
}

func parseWebmulator(doc *html.Node, p romProvider) []romHit {
	return hitsFromAnchors(doc, p, func(a *html.Node, m []string) (string, string, string) {
		title := descendantTextByClass(a, "roms-title")
		if title == "" {
			title = strings.TrimSuffix(attr(a, "title"), " ROM")
		}
		return title, m[1], regionInTitle(title)
	})
}

func parseRetrostic(doc *html.Node, p romProvider) []romHit {
	return hitsFromAnchors(doc, p, func(a *html.Node, m []string) (string, string, string) {
		// Each result is rendered three times for responsive layouts. The two
		// thumbnail links carry shortened title attributes; the unadorned wide
		// link contains the canonical title and region used by the plugin.
		if strings.TrimSpace(attr(a, "title")) != "" {
			return "", "", ""
		}
		title := nodeText(a)
		if strings.Contains(title, " for ") {
			title = strings.Split(title, " for ")[0]
		}
		if img := firstDescendant(a, "img", ""); title == "" && img != nil {
			title = strings.TrimSuffix(strings.TrimSuffix(attr(img, "alt"), " Roms"), " Rom")
		}
		return title, m[1], regionInTitle(title)
	})
}

func parseEmuParadise(doc *html.Node, p romProvider) []romHit {
	return hitsFromAnchors(doc, p, func(a *html.Node, _ []string) (string, string, string) {
		if attr(a, "data-filter") == "" {
			return "", "", ""
		}
		title := regexp.MustCompile(`(?i)\s+(ROMs?|ISOs?|Game)$`).ReplaceAllString(nodeText(a), "")
		platform := ""
		if parent := ancestorByClass(a, "roms"); parent != nil {
			platform = descendantTextByClass(parent, "sysname")
		}
		return title, platform, regionInTitle(title)
	})
}

func parseRomulation(doc *html.Node, p romProvider) []romHit {
	return hitsFromAnchors(doc, p, func(a *html.Node, m []string) (string, string, string) {
		title := regexp.MustCompile(`^\s*\[[^]]+\]\s*`).ReplaceAllString(nodeText(a), "")
		region := ""
		if tr := ancestorTag(a, "tr"); tr != nil {
			for n := tr.FirstChild; n != nil; n = n.NextSibling {
				if n.Type == html.ElementNode && n.Data == "td" {
					text := strings.TrimSpace(nodeText(n))
					if text != "" && !strings.Contains(text, title) && !strings.Contains(strings.ToLower(text), "kb") && !strings.Contains(strings.ToLower(text), "mb") && !strings.Contains(strings.ToLower(text), "gb") {
						region = text
						break
					}
				}
			}
		}
		return title, m[1], region
	})
}

func hitsFromAnchors(doc *html.Node, p romProvider, facts func(*html.Node, []string) (string, string, string)) []romHit {
	out := []romHit{}
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := strings.TrimSpace(attr(n, "href"))
			u, err := url.Parse(href)
			if err == nil {
				m := p.path.FindStringSubmatch(u.Path)
				if m != nil {
					absolute := "https://www." + p.host + u.Path
					if !seen[absolute] {
						title, platform, region := facts(n, m)
						title = strings.TrimSpace(title)
						if title != "" {
							seen[absolute] = true
							out = append(out, romHit{id: strings.Trim(u.Path, "/"), title: title, platform: platform, region: region, url: absolute})
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func firstDescendant(n *html.Node, tag, class string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (tag == "" || c.Data == tag) && (class == "" || hasClass(c, class)) {
			return c
		}
		if found := firstDescendant(c, tag, class); found != nil {
			return found
		}
	}
	return nil
}

func descendantTextByClass(n *html.Node, class string) string {
	if found := firstDescendant(n, "", class); found != nil {
		return nodeText(found)
	}
	return ""
}

func ancestorByClass(n *html.Node, class string) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if hasClass(p, class) {
			return p
		}
	}
	return nil
}

func ancestorTag(n *html.Node, tag string) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == tag {
			return p
		}
	}
	return nil
}

func regionInTitle(title string) string {
	m := regexp.MustCompile(`\(([^)]{1,24})\)`).FindStringSubmatch(title)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func romSystem(platform string) (string, string) {
	label := strings.TrimSpace(platform)
	key := strings.ToLower(label)
	aliases := map[string]string{
		"nintendo": "nes", "nes": "nes", "nintendo entertainment system": "nes",
		"super-nintendo": "snes", "snes": "snes", "super nintendo": "snes",
		"nintendo-64": "n64", "n64": "n64", "nintendo 64": "n64",
		"gameboy": "gb", "gb": "gb", "game boy": "gb",
		"gameboy-color": "gbc", "game boy color": "gbc", "gbc": "gbc",
		"gameboy-advance": "gba", "game boy advance": "gba", "gba": "gba",
		"nintendo-ds": "nds", "nintendo ds": "nds", "nds": "nds",
		"sega-genesis": "genesis", "sega genesis": "genesis", "genesis": "genesis",
		"sega-master-system": "sms", "sega master system": "sms", "sms": "sms",
		"sega-game-gear": "gamegear", "game gear": "gamegear",
		"playstation-1": "psx", "sony playstation": "psx", "psx": "psx",
		"ps2": "ps2", "sony playstation 2": "ps2",
		"mame": "arcade", "arcade": "arcade",
		"amiga": "amiga", "c64-preservation": "c64", "commodore 64 preservation project": "c64",
		"atari-2600": "atari2600", "atari-5200": "atari5200", "atari-7800": "atari7800",
		"atari-lynx": "lynx", "sega-saturn": "saturn", "sega-32x": "sega32", "sega-cd": "segacd",
	}
	id := aliases[key]
	if id == "" {
		if sys := lookupSystem(strings.ReplaceAll(key, "-", " ")); sys != nil {
			id = sys.ID
		}
	}
	if sys := systemByID[id]; sys != nil {
		return id, sys.Name
	}
	return "", label
}

func parseSize(text string) int64 {
	m := regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*([KMGT])B?`).FindStringSubmatch(text)
	if len(m) != 3 {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	power := map[string]int{"K": 1, "M": 2, "G": 3, "T": 4}[strings.ToUpper(m[2])]
	for range power {
		v *= 1024
	}
	return int64(v)
}
