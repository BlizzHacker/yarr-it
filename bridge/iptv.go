package main

import (
	"io"
	"net/http"
	"net/url"
)

// iptvCopyBufSize bounds each read/write/charge cycle of the streaming copy
// in copyIPTV. 32 KiB is a modest chunk for video -- large enough to keep
// syscall overhead low, small enough that the sub-budget is charged and
// checked often enough to cut a stream off promptly once it trips.
const iptvCopyBufSize = 32 << 10

// handleIPTV re-serves an upstream stream over HTTPS with a CORS header.
//
// It exists because two browser rules block most real IPTV and neither can be
// worked around client-side: an HTTPS page cannot load HTTP media, and most
// IPTV endpoints send no Access-Control-Allow-Origin. Fetching server-side
// fixes both.
//
// This is the last tier of the ladder precisely because it is the expensive
// one: every byte is billed twice and the allowance is shared with the mail
// edge, so it is guarded by its own sub-budget.
func (s *server) handleIPTV(w http.ResponseWriter, r *http.Request) {
	// Same concurrency gate handleSocket uses. Without it this endpoint is an
	// open, anonymising HTTP proxy for anyone on the internet, paid for out of
	// the allowance shared with the mail edge on this box.
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

	// SSRF guard. This box has a WireGuard tunnel into the home estate, so a
	// proxy that will fetch anything is a route into the LAN.
	if !resolvesPublic(target.Hostname()) {
		http.Error(w, "refusing non-public address", http.StatusForbidden)
		return
	}

	if s.iptvBudget.degraded() {
		http.Error(w, "iptv relay budget exhausted", http.StatusServiceUnavailable)
		return
	}

	client := &http.Client{
		Timeout:       0, // streams are long-lived; the request context governs lifetime
		CheckRedirect: iptvCheckRedirect,
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "Yarr.It/1.0")

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)

	s.copyIPTV(w, r, resp.Body)
}

// copyIPTV streams body to w in bounded chunks instead of a single io.Copy.
//
// io.Copy does not return until the reader is exhausted -- for a continuous
// IPTV stream that means until the viewer disconnects, hours later. Charging
// the budget from its return value therefore reads zero for the entire life
// of the stream and degraded() never fires, which is exactly the workload the
// sub-cap exists for. Copying in a loop charges both budgets as bytes
// actually flow and lets the sub-cap cut a stream off mid-copy the moment it
// trips, rather than only ever refusing the next stream to start.
//
// Bytes are charged to both s.budget and s.iptvBudget: IPTV traffic is not on
// top of the monthly allowance, it is carved out of it, capped additionally
// at its own lower ceiling.
func (s *server) copyIPTV(w http.ResponseWriter, r *http.Request, body io.Reader) {
	buf := make([]byte, iptvCopyBufSize)
	flusher, _ := w.(http.Flusher)
	for {
		if r.Context().Err() != nil {
			return // client disconnected
		}

		nr, er := body.Read(buf)
		if nr > 0 {
			if _, ew := w.Write(buf[:nr]); ew != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			// Billed twice: in from upstream, out to the client. Charged to
			// both budgets -- see the carve-out note above.
			n := int64(nr) * 2
			s.budget.add(n)
			s.iptvBudget.add(n)
			if s.iptvBudget.degraded() {
				return
			}
		}
		if er != nil {
			return
		}
	}
}

// iptvCheckRedirect re-applies the SSRF guard on every redirect hop. An
// upstream that passes the check on the first request can still redirect to
// http://192.168.0.x/ or http://127.0.0.1/ and walk straight into the estate
// behind the tunnel, so this is not optional and there is no way to disable
// it. It is a named function (rather than an inline closure) purely so it can
// be exercised directly in tests; the logic and effect are unchanged.
//
// It also caps the number of redirect hops, matching tracker.go's
// announceClient. CheckRedirect entirely replaces net/http's default
// 10-redirect limit, and the client here has Timeout: 0, so an
// attacker-controlled upstream that redirects to itself would otherwise loop
// forever -- uncharged, since io.Copy/copyIPTV never runs while the client is
// still following redirects.
func iptvCheckRedirect(req *http.Request, via []*http.Request) error {
	if !resolvesPublic(req.URL.Hostname()) {
		return http.ErrUseLastResponse
	}
	if len(via) > 3 {
		return http.ErrUseLastResponse
	}
	return nil
}
