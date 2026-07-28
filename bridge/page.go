package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// handlePage extracts the playable links out of a web page.
//
// Pasting a torrent site's description page into the app fails in the browser
// and cannot be made to work there: cross-origin HTML needs an
// Access-Control-Allow-Origin header, and no torrent site sends one. The
// browser console just says the fetch was blocked by CORS policy, which looks
// like a bug in this app rather than a rule of the platform.
//
// Fetching the page here instead sidesteps it, and is much cheaper than the
// IPTV relay next door: a page is read once, capped, and thrown away, where a
// stream is relayed byte for byte for hours. Only the extracted links come
// back -- never the page itself -- so this cannot be used as a general web
// proxy to read sites through.
const (
	// Generous for a description page, small enough that this cannot be aimed
	// at a large file to burn the allowance.
	pageMaxBytes = 2 << 20
	// Past this many links a page is an index, not a description, and the rest
	// are noise.
	pageMaxLinks = 60
)

var (
	reMagnet = regexp.MustCompile(`magnet:\?[^"'\s<>\\]+`)
	// A .torrent URL, which carries metadata inline and so is more useful than
	// a magnet -- it needs no peer before the file list is known.
	reTorrentFile = regexp.MustCompile(`https?://[^"'\s<>\\]+\.torrent\b[^"'\s<>\\]*`)
	reTitle       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

type pageLink struct {
	URL  string `json:"url"`
	Kind string `json:"kind"` // magnet | torrent
	Name string `json:"name,omitempty"`
}

type pageResult struct {
	Source string     `json:"source"`
	Title  string     `json:"title,omitempty"`
	Links  []pageLink `json:"links"`
}

func (s *server) handlePage(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.acquire(ip) {
		http.Error(w, "too many concurrent relays", http.StatusTooManyRequests)
		return
	}
	defer s.release(ip)

	raw := r.URL.Query().Get("u")
	if raw == "" {
		http.Error(w, "missing u", http.StatusBadRequest)
		return
	}
	target, err := url.Parse(raw)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		http.Error(w, "u must be an http(s) URL", http.StatusBadRequest)
		return
	}

	// Same SSRF guard as the stream relay: this box has a tunnel into the home
	// estate, so a fetcher that will retrieve anything is a route onto the LAN.
	if !resolvesPublic(target.Hostname()) {
		http.Error(w, "refusing non-public address", http.StatusForbidden)
		return
	}

	if s.iptvBudget.degraded() {
		http.Error(w, "relay budget exhausted", http.StatusServiceUnavailable)
		return
	}

	client := &http.Client{CheckRedirect: iptvCheckRedirect}
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusBadGateway)
		return
	}
	// Several torrent sites serve a challenge page to anything that does not
	// look like a browser.
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := client.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "could not reach that page")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeJSONError(w, http.StatusBadGateway,
			"that page answered "+resp.Status+
				" — it may be behind a bot check, so open it in a tab and paste the magnet instead")
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, pageMaxBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "could not read that page")
		return
	}
	s.iptvBudget.add(int64(len(body)))

	out := extractLinks(string(body), target)
	out.Source = target.String()
	if len(out.Links) == 0 {
		writeJSONError(w, http.StatusNotFound,
			"no magnet or .torrent link found on that page")
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// extractLinks pulls magnets and .torrent URLs out of page HTML.
//
// This deliberately reads the raw text rather than parsing a DOM. Torrent
// sites emit magnets in href attributes, in inline scripts, in data- fields
// and in plain text, and a link that is only assembled by JavaScript is not in
// the markup at all -- so matching the URI syntax wherever it appears finds
// strictly more than walking anchors would.
func extractLinks(html string, base *url.URL) pageResult {
	out := pageResult{Links: []pageLink{}}

	if m := reTitle.FindStringSubmatch(html); m != nil {
		out.Title = strings.TrimSpace(unescapeHTML(m[1]))
	}

	seen := make(map[string]bool)
	add := func(raw, kind string) {
		if len(out.Links) >= pageMaxLinks {
			return
		}
		link := unescapeHTML(raw)
		// A magnet's own trailing punctuation is legal, so only strip what
		// cannot be part of one.
		link = strings.TrimRight(link, `.,;)]}`)
		if link == "" || seen[link] {
			return
		}
		seen[link] = true
		out.Links = append(out.Links, pageLink{URL: link, Kind: kind, Name: nameFor(link, kind)})
	}

	for _, m := range reMagnet.FindAllString(html, -1) {
		add(m, "magnet")
	}
	for _, m := range reTorrentFile.FindAllString(html, -1) {
		add(m, "torrent")
	}

	// A .torrent file is more useful than a magnet -- its metadata is inline,
	// so the file list is known without first finding a peer -- so it leads.
	sort.SliceStable(out.Links, func(i, j int) bool {
		return out.Links[i].Kind == "torrent" && out.Links[j].Kind != "torrent"
	})
	return out
}

// nameFor recovers a human label: a magnet's dn= parameter, or a .torrent
// URL's filename.
func nameFor(link, kind string) string {
	if kind == "magnet" {
		if u, err := url.Parse(link); err == nil {
			if dn := u.Query().Get("dn"); dn != "" {
				return dn
			}
		}
		return ""
	}
	if u, err := url.Parse(link); err == nil {
		parts := strings.Split(u.Path, "/")
		name, err := url.PathUnescape(parts[len(parts)-1])
		if err == nil {
			return name
		}
	}
	return ""
}

// unescapeHTML handles the entities that actually appear inside URLs in
// markup. A magnet's parameters are separated by &, which pages write as
// &amp;, and a link left un-unescaped is a link that will not resolve.
var htmlEntities = strings.NewReplacer(
	"&amp;", "&", "&#38;", "&", "&#x26;", "&",
	"&quot;", `"`, "&#39;", "'", "&apos;", "'",
	"&lt;", "<", "&gt;", ">",
)

func unescapeHTML(s string) string { return htmlEntities.Replace(s) }

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
