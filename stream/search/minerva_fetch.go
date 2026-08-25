package main

// The Minerva Archive snapshot fetcher. RUN THIS OFF THE SERVER.
//
// WHY THIS IS A SEPARATE MODE AND A SEPARATE MACHINE
//
// The production VPS makes zero requests to minerva-archive.org. Not few --
// zero. Three things fall out of that, and each of them is worth the split on
// its own:
//
//   - A third party is not on the first-paint budget. The catalogue answers
//     from memory in microseconds; asking somebody else's web server during a
//     search would put a 200 ms floor and an availability dependency on it.
//   - The scraping is not on the hosted IP. That is a posture decision rather
//     than a technical one, and it is the cheapest defensible answer to it.
//   - An upstream HTML change can only fail a LAPTOP RUN. If this ran on the
//     server, a template change at Minerva would turn into a corrupted
//     catalogue on a live deployment. Here it turns into a non-zero exit on a
//     workstation and nothing else moves.
//
// THIRTEEN REQUESTS, AND WHY IT IS NOT TWO
//
// Two GETs very nearly do it. /browse/ carries all 1,049 magnets (its
// per-collection "download all" buttons embed the fully recursive hash list)
// and /v1/api/dashboard/latest carries every size. What neither carries is the
// join between them: HASH TO PATH.
//
// The magnet cannot supply it -- every magnet on the site is
// `dn=Minerva_Myrient`, identical on all 1,049. The API's `name` cannot supply
// it either: it joins path components with " - " and the components themselves
// contain " - ", so "Minerva_Myrient - Redump - IBM - PC compatible - Q" is
// five tokens for a three-component path and there is no way back.
//
// So the root page is read for the collections, and then a directory is
// descended ONLY when both of these are true: the row itself carries no magnet
// (so its torrent is cut somewhere below it), and its collection still has
// hashes with no path. That terminates, and it terminates quickly -- 12 of the
// 21 collections are one torrent each and are answered by the root page alone.
// A hard cap on depth and on total requests sits behind the argument in case
// the argument is wrong.
//
// POLITENESS IS A FLOOR, NOT AN AVERAGE. One request per second, a contact
// User-Agent, and 429/Retry-After honoured. There is no exhaustive crawl here
// and there must never be one: /v1/api/rom/search pages 2.7 million file rows
// at 101 per page, which is 27,000 requests, and that is the thing this file
// exists to not do.

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// minervaBase is the site. Every request below is built from this constant and
// then re-checked through minervaURL, so a redirect to somewhere else cannot
// turn into a fetch from somewhere else.
const minervaBase = "https://" + minervaHost

// minervaUA identifies this fetcher and says where to complain. A scraper that
// does not say who it is gives an operator no option except to block the IP.
const minervaUA = "yarr.it-minerva-import/1.0 (+https://yarrit.com; one request per second)"

// The politeness budget, and the belt-and-braces behind the termination
// argument in the file comment.
const (
	minervaRate     = time.Second // minimum spacing between requests
	minervaMaxReqs  = 40          // hard ceiling on requests for one snapshot
	minervaMaxDepth = 4           // hard ceiling on directory descent
)

// -------------------------------------------------------------- the client --

// minervaFetcher owns the pacing and the request budget for one run.
type minervaFetcher struct {
	client *http.Client
	last   time.Time
	spent  int
	// raw is every response body, keyed by the path it came from, so the
	// snapshot on disk is exactly what the site said and the reducer can be
	// re-run against it without touching the network again.
	raw map[string][]byte
}

func newMinervaFetcher() *minervaFetcher {
	return &minervaFetcher{
		// Redirects are followed, but only the two known ones: /search and
		// /dashboard 301 to their trailing-slash forms. checkRedirect refuses
		// anything that would leave the host.
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return fmt.Errorf("too many redirects")
				}
				if minervaURL(req.URL.String()) == "" {
					return fmt.Errorf("redirect leaves %s: %s", minervaHost, req.URL)
				}
				return nil
			},
		},
		raw: map[string][]byte{},
	}
}

// get fetches one path, paced and budgeted.
func (f *minervaFetcher) get(path string) ([]byte, error) {
	if f.spent >= minervaMaxReqs {
		return nil, fmt.Errorf("request budget of %d exhausted at %s "+
			"(the tree is deeper or wider than this fetcher expects)", minervaMaxReqs, path)
	}
	target := minervaURL(minervaBase + path)
	if target == "" {
		return nil, fmt.Errorf("refusing to fetch %q: not an https URL on %s", path, minervaHost)
	}

	for attempt := 0; ; attempt++ {
		if wait := minervaRate - time.Since(f.last); wait > 0 {
			time.Sleep(wait)
		}
		req, err := http.NewRequest("GET", target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", minervaUA)
		req.Header.Set("Accept", "text/html,application/json")

		f.last = time.Now()
		f.spent++
		resp, err := f.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()

		// Their rate limit, honoured on their terms rather than ours. Twice,
		// then give up: a third 429 means the budget is wrong, not that one
		// more sleep will help.
		if resp.StatusCode == 429 && attempt < 2 {
			delay := 30 * time.Second
			if v := resp.Header.Get("Retry-After"); v != "" {
				if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
					delay = time.Duration(secs) * time.Second
				}
			}
			log.Printf("minerva: 429 on %s, waiting %s", path, delay)
			time.Sleep(delay)
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
		}
		if readErr != nil {
			return nil, fmt.Errorf("%s: %w", path, readErr)
		}
		f.raw[path] = body
		return body, nil
	}
}

// --------------------------------------------------------------- the parse --

// minervaEntryRe matches one row of a directory listing.
//
// The href is taken and `data-name` is IGNORED, and that is the single most
// important line in this file. data-name is LOWER-CASED by their template:
// /browse/TOSEC/Commodore/c64/ and /browse/Redump/IBM - PC compatible/q/ both
// 404, because the real names are "C64" and "Q". A fetcher built on data-name
// would 404 on a large fraction of the tree and would do it silently, one
// directory at a time.
var minervaEntryRe = regexp.MustCompile(
	`(?is)<div class="entry"[^>]*>(.*?)</div>\s*(?:</div>|<div class="entry")`)

// minervaHrefRe pulls the directory or file link out of an entry.
var minervaHrefRe = regexp.MustCompile(`(?i)href="(/browse/[^"]*)"`)

// minervaMagnetRe pulls the info-hash out of any magnet on the page.
var minervaMagnetRe = regexp.MustCompile(`(?i)magnet:\?xt=urn:btih:([0-9a-f]{40})`)

// minervaTrackerRe pulls the tracker list the page inlines for its click
// handler. Every browse page carries the same `const trackers = [...]`.
var minervaTrackerRe = regexp.MustCompile(`(?is)const\s+trackers\s*=\s*\[(.*?)\]`)

// minervaQuotedRe pulls each quoted string out of that array.
var minervaQuotedRe = regexp.MustCompile(`["']([a-z0-9]+://[^"']+)["']`)

// minervaListing is one directory: its rows, and which of them carry magnets.
type minervaListing struct {
	// dirs are child directory paths, relative to the collection root, with the
	// case the href gave them.
	dirs []minervaRowRef
	// hashes is every info-hash that appeared anywhere on the page.
	hashes map[string]bool
}

// minervaRowRef is one row of a listing.
type minervaRowRef struct {
	// name is the decoded final path component: "C64", "Eggman's Arcade
	// Repository". HTML entities first, then percent-decoding -- in that order,
	// because the href attribute in the document is `Eggman&#39;s%20Arcade...`
	// and reversing the two leaves a literal "&#39;" in a filename.
	name string
	// path is the full browse path, decoded the same way.
	path string
	// hash is the info-hash shown on this row, or "" where the row showed an
	// empty <span> instead. An empty one is a real and load-bearing state: it
	// means the torrent is cut somewhere BELOW this row, or that there is no
	// torrent covering it at all.
	hash string
}

// parseListing reads one /browse/ page.
func parseListing(body []byte) minervaListing {
	out := minervaListing{hashes: map[string]bool{}}
	doc := string(body)

	for _, h := range minervaMagnetRe.FindAllStringSubmatch(doc, -1) {
		out.hashes[strings.ToLower(h[1])] = true
	}

	for _, m := range minervaEntryRe.FindAllStringSubmatch(doc, -1) {
		row := m[1]
		href := minervaHrefRe.FindStringSubmatch(row)
		if href == nil {
			continue
		}
		path, ok := minervaDecodeBrowseHref(href[1])
		if !ok || path == "" {
			continue
		}
		ref := minervaRowRef{path: path}
		if i := strings.LastIndex(path, "/"); i >= 0 {
			ref.name = path[i+1:]
		} else {
			ref.name = path
		}
		if h := minervaMagnetRe.FindStringSubmatch(row); h != nil {
			ref.hash = strings.ToLower(h[1])
		}
		// A row whose href ends in "/" is a directory. Files are linked to
		// /rom?id= and never match the /browse/ pattern above, so anything
		// arriving here is a directory already; the check is belt and braces.
		if strings.HasSuffix(href[1], "/") {
			out.dirs = append(out.dirs, ref)
		}
	}
	return out
}

// minervaDecodeBrowseHref turns an href into a clean browse path.
//
// The order is HTML entities THEN percent-decoding, and it matters: the
// attribute in the document reads `/browse/./Eggman&#39;s%20Arcade%20Repository/`.
// Decode the entity first and percent-decoding sees "Eggman's%20Arcade..." and
// produces the right name; do it the other way around and the apostrophe is
// still "&#39;" when it reaches a filename.
func minervaDecodeBrowseHref(href string) (string, bool) {
	s := html.UnescapeString(href)
	s = strings.TrimPrefix(s, "/browse/")
	s = strings.TrimPrefix(s, "./")
	s = strings.Trim(s, "/")
	if s == "" || s == "." {
		return "", false
	}
	dec, err := url.PathUnescape(s)
	if err != nil {
		return "", false
	}
	// A path component that tries to climb is not a path component.
	for _, part := range strings.Split(dec, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return dec, true
}

// parseTrackers reads the tracker array every browse page inlines.
func parseTrackers(body []byte) []string {
	m := minervaTrackerRe.FindSubmatch(body)
	if m == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, q := range minervaQuotedRe.FindAllSubmatch(m[1], -1) {
		t := strings.TrimSpace(string(q[1]))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ------------------------------------------------------------ the snapshot --

// minervaDashboard is the shape of /v1/api/dashboard/latest that this cares
// about. It contributes EXACTLY ONE FIELD to the catalogue -- size_bytes --
// and its `name` is display text that must never be parsed back into a path.
// Its seeders and leechers are deliberately not read: see minerva.go.
type minervaDashboard struct {
	Torrents []struct {
		InfoHash  string `json:"info_hash"`
		Name      string `json:"name"`
		SizeBytes int64  `json:"size_bytes"`
		Timestamp string `json:"timestamp"`
	} `json:"torrents"`
	Total int `json:"total_torrents"`
}

// minervaSnapshot is what lands on disk, and what the reducer reads.
type minervaSnapshot struct {
	FetchedAt string   `json:"fetched_at"`
	Trackers  []string `json:"trackers"`
	// Paths is the hash-to-path join, which is the only thing the walk exists
	// to produce.
	Paths map[string]string `json:"paths"`
	// Sizes and Measured come from the dashboard, keyed by hash.
	Sizes    map[string]int64  `json:"sizes"`
	Measured map[string]string `json:"measured"`
	// Names is Minerva's own display name per hash, kept for the report only.
	Names map[string]string `json:"names"`
	// BrowseHashes and DashHashes are the two independent censuses, kept so the
	// reducer can check them against each other rather than trusting one.
	BrowseHashes []string `json:"browse_hashes"`
	DashHashes   []string `json:"dash_hashes"`
	// Requests is how many GETs this snapshot cost, for a person deciding
	// whether the walk is still behaving.
	Requests int `json:"requests"`
}

// fetchMinerva walks the site once and writes a snapshot directory.
//
// It writes NOTHING unless the whole walk succeeded and the hash accounting
// closed. A half-snapshot that reduces to a half-catalogue is the failure this
// guards: it looks exactly like a successful import of a shrunken site.
func fetchMinerva(dir string) error {
	f := newMinervaFetcher()

	root, err := f.get("/browse/")
	if err != nil {
		return fmt.Errorf("root listing: %w", err)
	}
	rootList := parseListing(root)
	trackers := parseTrackers(root)
	if len(trackers) == 0 {
		// Not fatal to the fetch, but loud: without them every magnet this
		// service publishes is DHT-only and presents as a dead torrent.
		log.Printf("minerva: WARNING no tracker list found on /browse/; "+
			"magnets would be DHT-only. Check whether the page still inlines %q",
			"const trackers")
	}
	if len(rootList.hashes) == 0 {
		return fmt.Errorf("root listing carried no magnets at all; the page shape has changed")
	}

	dash, err := f.get("/v1/api/dashboard/latest")
	if err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}
	var db minervaDashboard
	if err := json.Unmarshal(dash, &db); err != nil {
		return fmt.Errorf("dashboard is not the JSON this expects: %w", err)
	}
	if len(db.Torrents) == 0 {
		return fmt.Errorf("dashboard listed no torrents; the API shape has changed")
	}

	snap := minervaSnapshot{
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Trackers:  trackers,
		Paths:     map[string]string{},
		Sizes:     map[string]int64{},
		Measured:  map[string]string{},
		Names:     map[string]string{},
	}
	for _, t := range db.Torrents {
		h := strings.ToLower(strings.TrimSpace(t.InfoHash))
		if h == "" {
			continue
		}
		snap.Sizes[h] = t.SizeBytes
		snap.Measured[h] = t.Timestamp
		snap.Names[h] = t.Name
		snap.DashHashes = append(snap.DashHashes, h)
	}
	for h := range rootList.hashes {
		snap.BrowseHashes = append(snap.BrowseHashes, h)
	}
	sort.Strings(snap.BrowseHashes)
	sort.Strings(snap.DashHashes)

	// The walk. Every root row is a collection; descend only where the row
	// itself has no magnet and hashes under it are still unassigned.
	unassigned := map[string]bool{}
	for h := range rootList.hashes {
		unassigned[h] = true
	}
	for _, coll := range rootList.dirs {
		if err := f.walk(coll, coll.name, 1, snap.Paths, unassigned); err != nil {
			return err
		}
	}

	// THE GATE. Every hash the browse tree published must have exactly one
	// path, or the join this whole walk exists to produce has holes in it and
	// the reducer would silently publish the rows that did resolve.
	var missing []string
	for h := range rootList.hashes {
		if snap.Paths[h] == "" {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		if len(missing) > 8 {
			missing = missing[:8]
		}
		return fmt.Errorf("hash accounting did not close: %d of %d browse hashes "+
			"have no path (first few: %s). Nothing written",
			len(missing), len(rootList.hashes), strings.Join(missing, ", "))
	}

	snap.Requests = f.spent
	return writeMinervaSnapshot(dir, snap, f.raw)
}

// walk descends one directory, assigning paths to hashes.
func (f *minervaFetcher) walk(ref minervaRowRef, path string, depth int,
	paths map[string]string, unassigned map[string]bool) error {

	// The row carries a magnet: this IS the torrent's cut level, and nothing
	// below it can be a different torrent. Twelve of the twenty-one
	// collections are answered here, on the root page, for free.
	if ref.hash != "" {
		if paths[ref.hash] == "" {
			paths[ref.hash] = path
			delete(unassigned, ref.hash)
		}
		return nil
	}
	if depth > minervaMaxDepth {
		// Not fatal here: the caller's accounting gate decides. A directory
		// this deep with no magnet is either genuinely torrent-less (the
		// TOSEC/Commodore/C64 subtree is) or a shape change worth failing on,
		// and only the accounting can tell those apart.
		log.Printf("minerva: depth cap reached at %s", path)
		return nil
	}
	if len(unassigned) == 0 {
		return nil
	}

	body, err := f.get("/browse/" + minervaEncodePath(path) + "/")
	if err != nil {
		return fmt.Errorf("listing %s: %w", path, err)
	}
	list := parseListing(body)

	// Nothing under here is still wanted, so do not descend further.
	wanted := false
	for h := range list.hashes {
		if unassigned[h] {
			wanted = true
			break
		}
	}
	if !wanted {
		return nil
	}

	for _, child := range list.dirs {
		if len(unassigned) == 0 {
			return nil
		}
		if err := f.walk(child, child.path, depth+1, paths, unassigned); err != nil {
			return err
		}
	}
	return nil
}

// minervaEncodePath percent-encodes each component, preserving case. Case is
// the point: see minervaEntryRe's comment on data-name.
func minervaEncodePath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// writeMinervaSnapshot lays the run down on disk: the reduced join, plus every
// raw body exactly as it arrived.
//
// The raw bodies are kept so the reducer can be re-run, and re-run again after
// a parser fix, without going back to the site. That is what makes it cheap to
// be careful.
func writeMinervaSnapshot(dir string, snap minervaSnapshot, raw map[string][]byte) error {
	if err := os.MkdirAll(filepath.Join(dir, "raw"), 0o755); err != nil {
		return err
	}
	for path, body := range raw {
		name := strings.ReplaceAll(strings.Trim(path, "/"), "/", "_")
		name = minervaSafeFilename(name)
		if name == "" {
			name = "root"
		}
		if err := os.WriteFile(filepath.Join(dir, "raw", name+".body"), body, 0o644); err != nil {
			return err
		}
	}
	out, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), out, 0o644); err != nil {
		return err
	}
	log.Printf("minerva: %d requests, %d hashes resolved to a path, %d in the dashboard",
		snap.Requests, len(snap.Paths), len(snap.DashHashes))
	return nil
}
