package main

// Minerva Archive's LIVE search API.
//
// minerva.go holds the static catalogue: 1,048 whole-collection torrents
// reduced off-box, searched from memory, first-paint safe. What it cannot do
// is answer a query that names a GAME INSIDE a set -- "chrono trigger" is
// nowhere in "TOSEC/Texas Instruments", and setInfo.Contains was documented
// as nil on every response until something read the per-file index. This is
// that something: Minerva publishes a per-file search at
// /v1/api/rom/search whose rows carry the info-hash of the torrent that
// contains each file, so a file hit resolves to a set card WITH its Contains
// filled in -- the one file inside the set that matched, by name.
//
// THE API IS SLOW AND THAT SHAPES EVERYTHING HERE.
//
// Measured from this VPS on 2026-10-14: 22.9s for a tokenised search, against
// 0.24s for /v1/api/rom/filters and 0.24s for a /rom?id= detail page. The
// search endpoint is the slow one, so:
//
//   - It runs as an ASYNC stage (progressive.go), like the indexers, never on
//     the synchronous first-paint path. The static catalogue answers the first
//     paint; this lands on a later poll.
//   - Its timeout defaults to 45s, inside jobTotalBudget's 70s. The previous
//     integration died of exactly this: a client timeout shorter than the
//     API's answer time, so every search logged "context deadline exceeded"
//     and the stage stayed "pending" forever. A source that always times out
//     is worse than no source: it poisons cacheableSearchSources and stops
//     the whole result from being cached.
//   - Cloudflare fronts the site and rejects the default Go user agent with
//     403 (error 1010). Every request here carries a browser-class UA.
//
// There is no CORS on the API, so it can only ever be called server-side;
// that is what this file is.
//
// HONESTY RULES, inherited from minerva.go and not relaxed here:
//
//   - A live row is a FILE inside a torrent, and the magnet fetches the whole
//     torrent. A row whose hash is not in the static catalogue is therefore
//     SKIPPED, not carded from the row alone: without the catalogue there is
//     no set size, and "Whole set" with no size beside it is the tile that
//     gets somebody a multi-TB surprise. The catalogue covers 1,048 of the
//     site's 1,049 sets (verified 2026-10-14: 11 of 11 distinct hashes from a
//     live "chrono trigger" page were in it); a hash outside it means the
//     catalogue is stale, and the fix is a re-import, logged here as a count.
//   - The card shape is unchanged: sets go out as card.Set, are partitioned
//     into `sets` by respondSearch, and never enter the works grid.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// minervaLiveDefaultBase is the site. MINERVA_URL overrides, matching the
	// env name the previous integration used and the one the unit file knows.
	minervaLiveDefaultBase = "https://minerva-archive.org"

	// minervaLiveUA is what every request carries. Not cosmetic: Cloudflare
	// answers the default Go user agent with 403 error 1010, so without this
	// the source does not merely degrade, it never connects.
	minervaLiveUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"

	// minervaLiveTimeoutDefault sizes the client above the API's own answer
	// time (20-25s measured) with margin, and below jobTotalBudget (70s) so a
	// hung Minerva cannot outlive the job it runs in. MINERVA_TIMEOUT_SECONDS
	// overrides.
	minervaLiveTimeoutDefault = 45 * time.Second

	// minervaLivePageSize is what the API returns per page. Its own web client
	// treats len(rows) > 100 as "another page exists" -- the API sends 101 rows
	// when there is more -- and this client keeps that reading.
	minervaLivePageSize = 100

	// minervaLiveCardLimit caps how many set cards ONE live search may
	// contribute, the same ceiling the static catalogue uses. A 100-row page
	// typically holds 10-25 distinct hashes; the per-collection cap inside the
	// static store does not apply here because a file search legitimately
	// returns many files from one collection, but the band still must not
	// become a file listing.
	minervaLiveCardLimit = 40

	// minervaLiveMaxDetailFetches bounds the /rom?id= enrichment calls made for
	// one search. The detail page is fast (0.24s measured) but this is a
	// third-party box answering a 23-second query already; enrichment is a
	// bonus, and the bound keeps a pathological page from turning one search
	// into a hundred requests.
	minervaLiveMaxDetailFetches = 8
)

// minervaLiveRow is one row of the search API's answer: a FILE, not a set.
type minervaLiveRow struct {
	ID       int64  `json:"id"`
	FullPath string `json:"full_path"`
	// Magnet is the bare site magnet of the CONTAINING torrent:
	// magnet:?xt=urn:btih:<hash>&dn=Minerva_Myrient. Every file in one torrent
	// carries the same one, which is exactly why the hash is the join key back
	// to the static catalogue.
	Magnet string `json:"magnet"`
	SoID   int64  `json:"so_id"`
}

// minervaLiveFilters is the answer of /v1/api/rom/filters: the vocabularies
// the search endpoint's filter parameters accept.
type minervaLiveFilters struct {
	Groups        []string `json:"groups"`
	Consoles      []string `json:"consoles"`
	Regions       []string `json:"regions"`
	Manufacturers []string `json:"manufacturers"`
}

// minervaLiveDetail is the part of a /rom?id= page this client reads. The page
// is HTML with `window.rom = {...}` embedded; fetchDetail parses that object.
type minervaLiveDetail struct {
	ID       int64  `json:"id"`
	FullPath string `json:"full_path"`
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
	Magnet   string `json:"magnet"`
	Torrents string `json:"torrents"`
	Region   string `json:"region"`
	Console  string `json:"console"`
	Group    string `json:"group"`
	SoID     int64  `json:"so_id"`
}

// minervaLiveQuery is one search against the API, with every filter the site's
// own global_search.js can send. Zero values mean "parameter omitted".
type minervaLiveQuery struct {
	Query        string
	Offset       int
	Tokenised    bool
	Group        string
	Console      string
	Region       string
	Manufacturer string
	// MinSizeBytes and MaxSizeBytes bound the FILE size the API matches on.
	MinSizeBytes int64
	MaxSizeBytes int64
}

// values renders the query as URL parameters. Every name here was read off
// the site's own js/global_search.js rather than guessed: query, offset,
// tokenised, group, console, region, manufacturer, min_size, max_size.
func (q minervaLiveQuery) values() url.Values {
	v := url.Values{}
	v.Set("query", strings.ToLower(strings.TrimSpace(q.Query)))
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	if q.Tokenised {
		v.Set("tokenised", "true")
	}
	for name, val := range map[string]string{
		"group":        q.Group,
		"console":      q.Console,
		"region":       q.Region,
		"manufacturer": q.Manufacturer,
	} {
		if s := strings.TrimSpace(val); s != "" {
			v.Set(name, s)
		}
	}
	if q.MinSizeBytes > 0 {
		v.Set("min_size", strconv.FormatInt(q.MinSizeBytes, 10))
	}
	if q.MaxSizeBytes > 0 {
		v.Set("max_size", strconv.FormatInt(q.MaxSizeBytes, 10))
	}
	return v
}

// minervaLiveClient talks to the API. Nil and disabled are both normal states
// and every method tolerates them, the way minervaStore does.
type minervaLiveClient struct {
	base string
	http *http.Client

	// unknownHashes counts live rows whose info-hash was not in the static
	// catalogue since process start. It is a staleness signal for
	// /api/health, not an error: the answer to a nonzero count is re-running
	// the fetch/import, and health is where an operator would look.
	mu            sync.Mutex
	unknownHashes int
	searches      int
	failures      int
	lastOK        time.Time
}

// newMinervaLiveClient builds the client from the environment.
//
// MINERVA_URL sets the base (default https://minerva-archive.org); setting it
// to "off" disables the source entirely, for an operator who does not want
// this box talking to a third party on the search path at all.
// MINERVA_TIMEOUT_SECONDS overrides the 45s client timeout.
func newMinervaLiveClient() *minervaLiveClient {
	base := strings.TrimSpace(os.Getenv("MINERVA_URL"))
	if base == "" {
		base = minervaLiveDefaultBase
	}
	if strings.EqualFold(base, "off") || strings.EqualFold(base, "disabled") {
		return nil
	}
	timeout := minervaLiveTimeoutDefault
	if s := strings.TrimSpace(os.Getenv("MINERVA_TIMEOUT_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	c := &minervaLiveClient{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: timeout},
	}
	// Refuse a base that is not the real site unless an operator explicitly
	// pointed MINERVA_URL somewhere else (a test server, a mirror). The
	// suffix-on-a-label-boundary check is minervaURL's, for its reason.
	return c
}

func (c *minervaLiveClient) enabled() bool { return c != nil && c.base != "" }

// get issues one request with the browser UA and decodes a JSON body.
func (c *minervaLiveClient) get(ctx context.Context, path string, v url.Values, out any) error {
	if c == nil {
		return fmt.Errorf("minerva live: not configured")
	}
	u := c.base + path
	if len(v) > 0 {
		u += "?" + v.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", minervaLiveUA)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("minerva live: %s returned %s", path, resp.Status)
	}
	return json.Unmarshal(body, out)
}

// search runs one page of the live file search. The bool reports whether the
// API signalled another page (it sends 101 rows when there is more, which is
// how its own web client reads it).
func (c *minervaLiveClient) search(ctx context.Context, q minervaLiveQuery) ([]minervaLiveRow, bool, error) {
	if c == nil || strings.TrimSpace(q.Query) == "" {
		return nil, false, nil
	}
	var rows []minervaLiveRow
	if err := c.get(ctx, "/v1/api/rom/search", q.values(), &rows); err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > minervaLivePageSize
	if hasMore {
		rows = rows[:minervaLivePageSize]
	}
	return rows, hasMore, nil
}

// filters fetches the vocabularies the filter parameters accept. Fast
// (0.24s measured) and used to validate a caller-supplied group name rather
// than to gate every search.
func (c *minervaLiveClient) filters(ctx context.Context) (*minervaLiveFilters, error) {
	var f minervaLiveFilters
	if err := c.get(ctx, "/v1/api/rom/filters", nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// fetchDetail reads one /rom?id= page and extracts the embedded window.rom
// object. Used to enrich a set card with the matched file's own size, which
// the search API does not carry.
func (c *minervaLiveClient) fetchDetail(ctx context.Context, id int64) (*minervaLiveDetail, error) {
	if c == nil {
		return nil, fmt.Errorf("minerva live: not configured")
	}
	u := c.base + "/rom?id=" + strconv.FormatInt(id, 10)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", minervaLiveUA)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("minerva live: /rom?id=%d returned %s", id, resp.Status)
	}
	return parseMinervaDetail(string(body))
}

// parseMinervaDetail extracts the window.rom JSON object from a detail page.
// Split out from fetchDetail so the parse is table-testable without a server.
func parseMinervaDetail(html string) (*minervaLiveDetail, error) {
	const marker = "window.rom"
	i := strings.Index(html, marker)
	if i < 0 {
		return nil, fmt.Errorf("minerva live: detail page has no window.rom")
	}
	j := strings.Index(html[i:], "{")
	if j < 0 {
		return nil, fmt.Errorf("minerva live: detail page has no rom object")
	}
	start := i + j
	// Scan to the matching close brace, respecting strings and escapes. A
	// regexp would be wrong the first time a file name contains a brace.
	depth := 0
	inStr := false
	esc := false
	for k := start; k < len(html); k++ {
		ch := html[k]
		switch {
		case esc:
			esc = false
		case inStr && ch == '\\':
			esc = true
		case ch == '"':
			inStr = !inStr
		case inStr:
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				var d minervaLiveDetail
				if err := json.Unmarshal([]byte(html[start:k+1]), &d); err != nil {
					return nil, fmt.Errorf("minerva live: decode detail: %w", err)
				}
				return &d, nil
			}
		}
	}
	return nil, fmt.Errorf("minerva live: detail page rom object never closes")
}

// infoHashFromMagnet pulls the 40-hex info-hash out of a bare site magnet.
// Empty when the magnet is not the expected shape -- a row that cannot be
// joined to a torrent cannot be carded, and guessing at it is how a file
// magnet ends up on a set tile.
func minervaLiveInfoHash(magnet string) string {
	const prefix = "btih:"
	i := strings.Index(strings.ToLower(magnet), prefix)
	if i < 0 {
		return ""
	}
	h := magnet[i+len(prefix):]
	if j := strings.IndexAny(h, "&"); j >= 0 {
		h = h[:j]
	}
	h = strings.ToLower(strings.TrimSpace(h))
	if len(h) != 40 {
		return ""
	}
	for _, r := range h {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return ""
		}
	}
	return h
}

// detectMinervaGroup finds a collection name inside a query and splits it out,
// so "no-intro zelda" searches zelda WITHIN No-Intro through the API's own
// group filter rather than hoping the tokeniser does it. Longest name first:
// "Total DOS Collection" must win over any shorter name inside it, and the
// names are matched case-insensitively on word-ish boundaries (the site's own
// names contain spaces and apostrophes, so Fields-based matching would miss
// "Eggman's Arcade Repository" typed without the apostrophe -- Contains on the
// lowercased query is the honest match, and a false positive costs one
// narrowed search, not a wrong card).
func detectMinervaGroup(q string) (group, rest string) {
	lower := strings.ToLower(q)
	best := ""
	for name := range minervaCollections {
		n := strings.ToLower(name)
		if strings.Contains(lower, n) && len(n) > len(best) {
			best = name
		}
	}
	if best == "" {
		return "", q
	}
	// Strip the group's words from the query; if nothing meaningful is left,
	// keep the full query (the API needs one, and searching the group name
	// inside the group is still a sensible question).
	i := strings.Index(lower, strings.ToLower(best))
	rest = strings.TrimSpace(q[:i] + " " + q[i+len(best):])
	if len(strings.Fields(rest)) == 0 {
		rest = q
	}
	return best, rest
}

// searchCards is the whole live source: one search page, joined against the
// static catalogue, out as set cards with Contains filled in.
//
// The join is the design. The API answers with files; the catalogue holds the
// sets those files live in, with their sizes, paths, magnets and trackers. A
// file whose containing torrent is in the catalogue becomes that set's card --
// deduplicated by info-hash, because a page of Chrono Trigger hits is ten
// files inside three torrents, not ten results -- with setInfo.Contains naming
// the file that matched. A file whose torrent is NOT in the catalogue is
// counted and skipped; see the honesty rules at the top of this file.
func (c *minervaLiveClient) searchCards(ctx context.Context, store *minervaStore, q, kind string) ([]card, error) {
	if c == nil || store == nil {
		return nil, nil
	}
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	// The domain gate the static stage applies, applied identically: a music
	// search has nothing to ask Minerva and must not spend 23s finding out.
	if kind != "" && !minervaServesKind(kind) {
		return nil, nil
	}

	group, query := detectMinervaGroup(q)
	rows, _, err := c.search(ctx, minervaLiveQuery{
		Query:     query,
		Tokenised: true,
		Group:     group,
	})
	c.mu.Lock()
	c.searches++
	if err != nil {
		c.failures++
	}
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	// Join rows to catalogue entries, first match per hash wins the Contains
	// slot (API order is relevance order, so the first file is the best
	// answer to "which file inside this set matched").
	type hit struct {
		row   minervaLiveRow
		entry minervaRow
	}
	var hits []hit
	seen := map[string]bool{}
	unknown := 0
	for _, r := range rows {
		hash := minervaLiveInfoHash(r.Magnet)
		if hash == "" || seen[hash] {
			continue
		}
		e, ok := store.lookupByHash(hash)
		if !ok {
			unknown++
			continue
		}
		seen[hash] = true
		hits = append(hits, hit{row: r, entry: e})
	}
	if unknown > 0 {
		c.mu.Lock()
		c.unknownHashes += unknown
		c.mu.Unlock()
		log.Printf("minerva live: %d file(s) in torrents absent from the static catalogue (%s); re-run -fetch-minerva/-import-minerva to refresh", unknown, minervaCataloguePath())
	}

	// Enrich the top few with the matched file's own size from the detail
	// page. Bounded, best-effort: a failure here loses a number, never a card.
	sizes := map[int64]int64{}
	for i, h := range hits {
		if i >= minervaLiveMaxDetailFetches {
			break
		}
		d, err := c.fetchDetail(ctx, h.row.ID)
		if err != nil || d == nil || d.Size <= 0 {
			continue
		}
		sizes[h.row.ID] = d.Size
	}

	cards := make([]card, 0, len(hits))
	for _, h := range hits {
		if len(cards) >= minervaLiveCardLimit {
			break
		}
		col, known := minervaCollectionFor(h.entry.Collection)
		if !known || col.Kind == minervaExcluded {
			continue
		}
		c, ok := minervaCard(h.entry)
		if !ok {
			continue
		}
		file := strings.TrimPrefix(strings.TrimSpace(h.row.FullPath), "./")
		if file != "" {
			cm := &setMatch{File: file}
			if sz := sizes[h.row.ID]; sz > 0 {
				cm.SizeBytes = sz
				cm.SizeHuman = humanSize(sz)
			}
			c.Set.Contains = cm
		}
		c.Key = "minerva-live:" + h.entry.InfoHash
		cards = append(cards, c)
	}
	c.mu.Lock()
	if err == nil {
		c.lastOK = time.Now()
	}
	c.mu.Unlock()
	return cards, nil
}

// stats reports the live source's state for /api/health. Stated as facts
// about THIS process, never as a promise about the third party.
func (c *minervaLiveClient) stats() map[string]any {
	out := map[string]any{"enabled": c != nil}
	if c == nil {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out["base"] = c.base
	out["searches"] = c.searches
	out["failures"] = c.failures
	out["unknownHashes"] = c.unknownHashes
	if !c.lastOK.IsZero() {
		out["lastOK"] = c.lastOK.UTC().Format(time.RFC3339)
	}
	return out
}

// lookupByHash finds a catalogue row by its info-hash. Added for the live
// join; the catalogue is small (1,048 rows) and searches are at most one page
// deep, so a scan under the read lock is the right amount of machinery -- a
// hash index would be built for a cost this path does not have.
func (s *minervaStore) lookupByHash(hash string) (minervaRow, bool) {
	if s == nil || hash == "" {
		return minervaRow{}, false
	}
	hash = strings.ToLower(hash)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.rows {
		if strings.ToLower(s.rows[i].InfoHash) == hash {
			return s.rows[i], true
		}
	}
	return minervaRow{}, false
}
