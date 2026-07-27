package main

import (
	"io"
	"net/http"
	"net/url"
)

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

	n, _ := io.Copy(w, resp.Body)
	// Billed twice: in from upstream, out to the client.
	s.iptvBudget.add(n * 2)
}

// iptvCheckRedirect re-applies the SSRF guard on every redirect hop. An
// upstream that passes the check on the first request can still redirect to
// http://192.168.0.x/ or http://127.0.0.1/ and walk straight into the estate
// behind the tunnel, so this is not optional and there is no way to disable
// it. It is a named function (rather than an inline closure) purely so it can
// be exercised directly in tests; the logic and effect are unchanged.
func iptvCheckRedirect(req *http.Request, _ []*http.Request) error {
	if !resolvesPublic(req.URL.Hostname()) {
		return http.ErrUseLastResponse
	}
	return nil
}
