package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTP tracker announce proxy.
//
// A large share of public trackers are http:// only. A browser on an https://
// page cannot reach them at all -- mixed content blocks the request before CORS
// even applies -- so those trackers are invisible to an in-browser client no
// matter how many peers they hold.
//
// This forwards one announce and returns the peer list as JSON. Like the socket
// relay it is deliberately incurious: it takes an infohash it cannot interpret,
// asks a tracker the client chose, and hands back addresses. Nothing is stored.

const announceTimeout = 12 * time.Second

type announceResp struct {
	Peers    []peerAddr `json:"peers"`
	Interval int        `json:"interval,omitempty"`
	Seeders  int        `json:"seeders,omitempty"`
	Leechers int        `json:"leechers,omitempty"`
	Error    string     `json:"error,omitempty"`
}

type peerAddr struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (s *server) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	q := r.URL.Query()
	rawTracker := q.Get("tr")
	infoHash := strings.ToLower(strings.TrimSpace(q.Get("ih")))

	if len(infoHash) != 40 {
		writeAnnounce(w, announceResp{Error: "bad infohash"})
		return
	}
	raw, err := hex.DecodeString(infoHash)
	if err != nil {
		writeAnnounce(w, announceResp{Error: "bad infohash"})
		return
	}

	u, err := url.Parse(rawTracker)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		writeAnnounce(w, announceResp{Error: "tracker must be http(s)"})
		return
	}
	// The tracker host is attacker-supplied, so resolve it and refuse anything
	// that is not publicly routable. Without this the proxy would happily fetch
	// http://10.10.10.1/ and report back what it found.
	if !resolvesPublic(u.Hostname()) {
		writeAnnounce(w, announceResp{Error: "tracker not permitted"})
		return
	}

	peerID := "-MW0001-" + randomID(12)
	vals := url.Values{}
	vals.Set("info_hash", string(raw))
	vals.Set("peer_id", peerID)
	vals.Set("port", "6881")
	vals.Set("uploaded", "0")
	vals.Set("downloaded", "0")
	vals.Set("left", "0")
	vals.Set("compact", "1")
	vals.Set("numwant", "100")
	vals.Set("event", "started")

	sep := "?"
	if u.RawQuery != "" {
		sep = "&"
	}
	target := u.String() + sep + vals.Encode()

	ctx, cancel := context.WithTimeout(r.Context(), announceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		writeAnnounce(w, announceResp{Error: "bad request"})
		return
	}
	req.Header.Set("User-Agent", "mw-bridge/1.0")

	resp, err := announceClient.Do(req)
	if err != nil {
		writeAnnounce(w, announceResp{Error: "tracker unreachable"})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		writeAnnounce(w, announceResp{Error: "tracker read failed"})
		return
	}

	out := parseBencodeAnnounce(body)
	writeAnnounce(w, out)
}

var announceClient = &http.Client{
	Timeout: announceTimeout,
	CheckRedirect: func(r *http.Request, via []*http.Request) error {
		// A redirect could point back at private space; re-check every hop.
		if !resolvesPublic(r.URL.Hostname()) {
			return fmt.Errorf("redirect to non-public host")
		}
		if len(via) > 3 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

func writeAnnounce(w http.ResponseWriter, v announceResp) {
	_ = json.NewEncoder(w).Encode(v)
}

// resolvesPublic reports whether every address a host resolves to is publicly
// routable, so the proxy cannot be aimed at the estate behind the tunnel.
func resolvesPublic(host string) bool {
	if host == "" {
		return false
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !publicUnicast(ip) {
			return false
		}
	}
	return true
}

func randomID(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[time.Now().UnixNano()%int64(len(alphabet))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}

// parseBencodeAnnounce pulls the compact peer list out of a tracker response.
//
// A full bencode parser is unnecessary here: the only fields that matter are
// "peers" (a binary string of 6-byte entries) and the counts, so this scans for
// them directly and ignores everything else.
func parseBencodeAnnounce(b []byte) announceResp {
	out := announceResp{Peers: []peerAddr{}}

	if msg := bencodeString(b, "failure reason"); msg != "" {
		out.Error = msg
		return out
	}
	out.Interval = bencodeInt(b, "interval")
	out.Seeders = bencodeInt(b, "complete")
	out.Leechers = bencodeInt(b, "incomplete")

	peers := bencodeBytes(b, "peers")
	for i := 0; i+6 <= len(peers); i += 6 {
		port := int(peers[i+4])<<8 | int(peers[i+5])
		if port == 0 {
			continue
		}
		ip := net.IPv4(peers[i], peers[i+1], peers[i+2], peers[i+3])
		if !publicUnicast(ip) {
			continue
		}
		out.Peers = append(out.Peers, peerAddr{Host: ip.String(), Port: port})
	}
	return out
}

func keyIndex(b []byte, key string) int {
	needle := fmt.Sprintf("%d:%s", len(key), key)
	return strings.Index(string(b), needle)
}

func bencodeBytes(b []byte, key string) []byte {
	i := keyIndex(b, key)
	if i < 0 {
		return nil
	}
	j := i + len(fmt.Sprintf("%d:%s", len(key), key))
	if j >= len(b) {
		return nil
	}
	// Expect a byte-string: <len>:<data>
	colon := -1
	for k := j; k < len(b) && k < j+12; k++ {
		if b[k] == ':' {
			colon = k
			break
		}
		if b[k] < '0' || b[k] > '9' {
			return nil
		}
	}
	if colon < 0 {
		return nil
	}
	n, err := strconv.Atoi(string(b[j:colon]))
	if err != nil || n < 0 || colon+1+n > len(b) {
		return nil
	}
	return b[colon+1 : colon+1+n]
}

func bencodeInt(b []byte, key string) int {
	i := keyIndex(b, key)
	if i < 0 {
		return 0
	}
	j := i + len(fmt.Sprintf("%d:%s", len(key), key))
	if j >= len(b) || b[j] != 'i' {
		return 0
	}
	end := j + 1
	for end < len(b) && b[end] != 'e' {
		end++
	}
	n, _ := strconv.Atoi(string(b[j+1 : end]))
	return n
}

func bencodeString(b []byte, key string) string {
	return string(bencodeBytes(b, key))
}
