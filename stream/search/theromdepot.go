package main

// The ROM Depot as a local catalogue search source.
//
// The browser extension captures the site's public directory API into a JSON
// catalogue. The search service reads that file once at startup and answers
// from memory, exactly as it does for Vimm's Lair. No request to Yarrit causes
// a scrape, login, or download; the result is an explicit off-site link and
// the UI tells the user that The ROM Depot requires an account.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
)

const (
	depotSiteName  = "The ROM Depot"
	depotShortName = "ROM Depot"
	depotHost      = "theromdepot.com"
	depotCardLimit = 120
)

type depotCatalogue struct {
	Version     int         `json:"version"`
	GeneratedAt string      `json:"generated_at"`
	Source      string      `json:"source"`
	Items       []depotItem `json:"items"`
}

type depotItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Region   string `json:"region"`
	Size     int64  `json:"size"`
}

type depotRow struct {
	depotItem
	title  string
	lower  string
	system string
	target string
}

type depotStore struct {
	mu        sync.RWMutex
	rows      []depotRow
	path      string
	generated string
}

func depotCataloguePath() string {
	if p := strings.TrimSpace(os.Getenv("THEROMDEPOT_CATALOG")); p != "" {
		return p
	}
	return "/var/lib/mw-search/theromdepot-catalog.json"
}

func loadDepotStore(filename string) (*depotStore, error) {
	s := &depotStore{path: filename}
	if filename == "" {
		return s, nil
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	var cat depotCatalogue
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("The ROM Depot catalogue %s is unreadable: %w", filename, err)
	}
	rows := make([]depotRow, 0, len(cat.Items))
	for _, item := range cat.Items {
		title := depotTitle(item)
		target, ok := depotDownloadURL(item.ID)
		if !ok || title == "" || strings.TrimSpace(item.Platform) == "" {
			continue
		}
		searchText := strings.Join([]string{title, item.Name, item.Platform, item.Region}, " ")
		rows = append(rows, depotRow{
			depotItem: item,
			title:     title,
			lower:     strings.ToLower(searchText),
			system:    depotSystemFor(item.Platform),
			target:    target,
		})
	}
	s.rows = rows
	s.generated = cat.GeneratedAt
	return s, nil
}

func (s *depotStore) count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rows)
}

func (s *depotStore) stats() map[string]any {
	if s == nil {
		return map[string]any{"entries": 0}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]any{"entries": len(s.rows)}
	if s.generated != "" {
		out["generatedAt"] = s.generated
	}
	return out
}

func (s *depotStore) search(q, kind string, systems map[string]bool, limit int) []card {
	if s == nil || (kind != "" && !sameDomain(kind, domainGame)) {
		return nil
	}
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(q)))
	if len(terms) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = depotCardLimit
	}

	s.mu.RLock()
	matches := make([]depotRow, 0, limit*2)
	for i := range s.rows {
		r := &s.rows[i]
		if systems != nil && !systems[r.system] {
			continue
		}
		if lowerHasAll(r.lower, terms) {
			matches = append(matches, *r)
		}
	}
	s.mu.RUnlock()

	// One card per title and machine, with region/format variants as source
	// rows. That keeps a full directory useful without turning ten equivalent
	// dumps into ten identical tiles.
	groups := make(map[string][]depotRow, len(matches))
	order := make([]string, 0, len(matches))
	for _, r := range matches {
		machine := r.system
		if machine == "" {
			machine = strings.ToLower(strings.TrimSpace(r.Platform))
		}
		key := machine + "\x00" + canonicalTitle(r.title)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}

	cards := make([]card, 0, len(order))
	for _, key := range order {
		rows := groups[key]
		head := rows[0]
		sources := make([]source, 0, len(rows))
		for _, r := range rows {
			facts := []string{"Login required", r.Platform}
			if region := strings.TrimSpace(r.Region); region != "" {
				facts = append(facts, region)
			}
			label := strings.TrimSpace(r.Name)
			if label == "" {
				label = path.Base(r.ID)
			}
			sources = append(sources, source{
				Title:     label,
				Indexer:   depotSiteName,
				Size:      r.Size,
				SizeHuman: humanBytes(r.Size),
				Magnet:    r.target,
				Source:    strings.Join(facts, " · "),
				Offsite:   true,
				Action:    "download",
			})
		}
		cards = append(cards, card{
			Key:      "theromdepot:" + strings.ReplaceAll(key, "\x00", ":"),
			Title:    head.title,
			Kind:     domainGame,
			Sources:  sources,
			Origin:   depotSiteName,
			Platform: head.Platform,
			System:   head.system,
			Groups:   []string{"games"},
			Adult:    isAdultItem(head.title, "", nil),
			External: &externalSite{
				Name:  depotSiteName,
				Short: depotShortName,
				Host:  depotHost + " (login required)",
			},
		})
	}
	// The catalogue match above deliberately includes title, platform and
	// region. Do not run the shared title-only ranker here: it used to accept
	// "Mario Party Nintendo 64 Europe" from the catalogue, then throw that same
	// card away because "Nintendo 64" is metadata rather than title text.
	rankDepotCards(cards, q)
	if len(cards) > limit {
		cards = cards[:limit]
	}
	return cards
}

func rankDepotCards(cards []card, q string) {
	terms := queryTerms(q)
	sort.SliceStable(cards, func(i, j int) bool {
		// Ask the inverse question first: is the card's complete title present
		// in the longer metadata-qualified query? That puts "Mario Party" above
		// Mario Party 2/3 even when Nintendo 64 and Europe follow the title.
		ti := matchScore(cards[i].Title, q)
		tj := matchScore(cards[j].Title, q)
		if ti != tj {
			return ti > tj
		}
		return titleOverlap(depotCardSearchText(cards[i]), terms) >
			titleOverlap(depotCardSearchText(cards[j]), terms)
	})
}

func depotCardSearchText(c card) string {
	var b strings.Builder
	b.WriteString(c.Title)
	b.WriteByte(' ')
	b.WriteString(c.Platform)
	for _, s := range c.Sources {
		b.WriteByte(' ')
		b.WriteString(s.Title)
		b.WriteByte(' ')
		b.WriteString(s.Source)
	}
	return b.String()
}

func depotTitle(item depotItem) string {
	name := strings.TrimSpace(item.Title)
	if name == "" {
		name = strings.TrimSpace(item.Name)
	}
	for {
		ext := path.Ext(name)
		if ext == "" || len(ext) > 8 {
			break
		}
		switch strings.ToLower(ext) {
		case ".zip", ".7z", ".rar", ".gz", ".iso", ".bin", ".cue", ".chd",
			".rom", ".nes", ".sfc", ".smc", ".z64", ".n64", ".v64", ".gba",
			".gb", ".gbc", ".nds", ".3ds", ".cia", ".md", ".gen", ".sms":
			name = strings.TrimSpace(strings.TrimSuffix(name, ext))
		default:
			return name
		}
	}
	return name
}

func depotDownloadURL(id string) (string, bool) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	if id == "" || strings.Contains(id, "\\") {
		return "", false
	}
	parts := strings.Split(id, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\r\n\x00") {
			return "", false
		}
		parts[i] = url.PathEscape(part)
	}
	return "https://" + depotHost + "/api/download/" + strings.Join(parts, "/"), true
}

// Stable slugs already used by Yarrit/ROM Hub. Unknown machines remain fully
// searchable and keep their display name; they simply do not claim a system
// facet whose meaning we cannot prove.
func depotSystemFor(platform string) string {
	key := strings.ToLower(strings.TrimSpace(platform))
	return map[string]string{
		"nintendo entertainment system": "nes", "nintendo nes": "nes", "nes": "nes",
		"super nintendo": "snes", "super nintendo entertainment system": "snes",
		"nintendo 64": "n64", "nintendo game boy": "gb", "game boy": "gb",
		"nintendo game boy color": "gbc", "game boy color": "gbc",
		"nintendo game boy advance": "gba", "game boy advance": "gba",
		"nintendo ds": "nds", "sega genesis": "genesis", "sega mega drive": "genesis",
		"sega master system": "sms", "sega game gear": "gamegear",
		"sony playstation": "psx", "playstation": "psx",
		"atari 2600": "atari2600", "atari 5200": "atari5200", "atari 7800": "atari7800",
		"atari lynx": "lynx", "nec turbografx-16": "tg16", "turbografx-16": "tg16",
	}[key]
}
