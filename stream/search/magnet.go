package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Public trackers attached to reconstructed magnets. Prowlarr's `infoHash`
// field gives us the swarm identity but no announce list, and a magnet with no
// trackers finds no peers. These are the large open trackers most public
// torrents already use.
var defaultTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
	"udp://open.stealth.si:80/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.torrent.eu.org:451/announce",
}

var reInfoHash = regexp.MustCompile(`(?i)^[a-f0-9]{40}$`)

// withDefaultTrackers guarantees a magnet carries udp:// announce targets.
//
// A browser finds peers by announcing to trackers through the relay, so a
// magnet with no udp trackers -- or only http/wss ones -- yields no peers and
// simply never starts, however many seeders the indexer claims. Indexers often
// hand back a bare `magnet:?xt=...&dn=...`, so the announce list is merged in
// rather than assumed.
func withDefaultTrackers(magnet string) string {
	existing := map[string]bool{}
	for _, part := range strings.Split(magnet, "&") {
		if v, ok := strings.CutPrefix(part, "tr="); ok {
			if dec, err := url.QueryUnescape(v); err == nil {
				existing[dec] = true
			}
		}
	}
	var b strings.Builder
	b.WriteString(magnet)
	for _, tr := range defaultTrackers {
		if !existing[tr] {
			fmt.Fprintf(&b, "&tr=%s", url.QueryEscape(tr))
		}
	}
	return b.String()
}

// usableMagnet returns a magnet URI a browser can actually act on, or "".
//
// This function exists because of a real leak: Prowlarr populates `magnetUrl`
// and `downloadUrl` with links to *itself* --
//
//	http://192.168.0.115:9696/2/download?apikey=<PROWLARR_API_KEY>&link=...
//
// Emitting those to a public web page would publish the Prowlarr API key and
// the LAN address of the indexer host to every visitor, and the URL would be
// useless to them anyway since it points at private space. So anything that is
// not a literal "magnet:" URI is refused, and we rebuild from `infoHash`
// instead.
func usableMagnet(r prowlarrResult) string {
	// A real magnet, wherever it happens to live.
	for _, cand := range []string{r.MagnetURL, r.GUID} {
		if strings.HasPrefix(strings.ToLower(cand), "magnet:?") {
			return withDefaultTrackers(cand)
		}
	}

	// Otherwise rebuild one from the infohash, which is all a swarm needs.
	h := strings.TrimSpace(r.InfoHash)
	if !reInfoHash.MatchString(h) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "magnet:?xt=urn:btih:%s", strings.ToLower(h))
	if r.Title != "" {
		fmt.Fprintf(&b, "&dn=%s", url.QueryEscape(r.Title))
	}
	for _, tr := range defaultTrackers {
		fmt.Fprintf(&b, "&tr=%s", url.QueryEscape(tr))
	}
	return b.String()
}
