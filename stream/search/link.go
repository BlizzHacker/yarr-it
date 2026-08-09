package main

// /api/link/* -- paste a link from anywhere, get back an honest list of what
// can actually be downloaded or played.
//
// The shape is copied deliberately from fdown.net and snap-insta.to, because
// the shape is why people use those instead of a real tool: one box, one
// paste, no account, no options. The differences are all in what comes back --
// real sizes, real codecs, an explicit answer about whether a file has sound,
// and a named reason when it does not work.
//
// Everything here is anonymous by design. It touches nothing of the owner's:
// no Prowlarr call, no TMDB key, no library. The only reason it lives in
// mw-search is that mw-search is the Go service that already fronts this
// domain -- which is also exactly why link_guard.go exists, since mw-search is
// the process holding a tunnel to 192.168.0.115.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	linkCacheTTL   = 5 * time.Minute
	mediaTokenTTL  = 30 * time.Minute
	maxPlaylist    = 60
	sizeProbeCount = 14
	sizeProbeTime  = 9 * time.Second
)

type linkService struct {
	yt     *ytdlpRunner
	egress *egress
	key    []byte // HMAC key for media tokens; regenerated each start

	limiter *ipLimiter
	stream  *streamGate

	mu    sync.RWMutex
	cache map[string]linkCacheEntry

	statMu sync.Mutex
	stats  map[string]int // "<extractor>/<code>" -> count
}

type linkCacheEntry struct {
	body    []byte
	expires time.Time
}

func newLinkService(bin string, timeout time.Duration, maxConcurrent int, netns string) (*linkService, error) {
	proxy, err := newEgress(netns)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &linkService{
		yt:      newYtdlpRunner(bin, proxy, timeout),
		egress:  proxy,
		key:     key,
		limiter: newIPLimiter(8, 20*time.Second),
		stream:  newStreamGate(maxConcurrent, 3),
		cache:   make(map[string]linkCacheEntry),
		stats:   make(map[string]int),
	}, nil
}

func (s *linkService) note(extractor, code string) {
	if extractor == "" {
		extractor = "-"
	}
	s.statMu.Lock()
	s.stats[extractor+"/"+code]++
	s.statMu.Unlock()
}

// --------------------------------------------------------------- resolve --

type linkResponse struct {
	Kind      string       `json:"kind"` // video | audio | image | collection
	Source    string       `json:"source"`
	Extractor string       `json:"extractor,omitempty"`
	Title     string       `json:"title"`
	Uploader  string       `json:"uploader,omitempty"`
	Duration  float64      `json:"duration,omitempty"`
	Thumbnail string       `json:"thumbnail,omitempty"`
	IsLive    bool         `json:"isLive,omitempty"`
	AgeLimit  int          `json:"ageLimit,omitempty"`
	Formats   []linkFormat `json:"formats,omitempty"`
	Best      int          `json:"best"`
	Items     []linkItem   `json:"items,omitempty"` // collection entries
	Notes     []string     `json:"notes,omitempty"`
}

type linkItem struct {
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Thumbnail string  `json:"thumbnail,omitempty"`
	Duration  float64 `json:"duration,omitempty"`
}

func (s *linkService) handleResolve(w http.ResponseWriter, r *http.Request) {
	setLinkCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	if raw == "" {
		writeLinkError(w, http.StatusBadRequest, &resolveError{
			Code: "missing_url", Message: "paste a link first"})
		return
	}

	ip := linkClientIP(r)
	if !s.limiter.allow(ip) {
		w.Header().Set("Retry-After", "20")
		writeLinkError(w, http.StatusTooManyRequests, &resolveError{
			Code: codeRateLimited, Message: "too many links from this address — wait a moment"})
		return
	}

	// The address check runs here, before yt-dlp is started, so an obvious
	// probe never costs a subprocess. It is not the only check: yt-dlp itself
	// is confined to the egress proxy, which re-applies the same rules to
	// every hop it follows.
	if _, err := resolveTarget(r.Context(), nil, raw); err != nil {
		s.note("-", codeBlocked)
		writeLinkError(w, http.StatusForbidden, &resolveError{
			Code: codeBlocked, Message: err.Error()})
		return
	}

	if body, ok := s.cached(raw); ok {
		w.Header().Set("X-Cache", "HIT")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		return
	}

	// Snapshot the egress proxy's refusal count around the probe. When yt-dlp
	// follows a redirect onto a private address the proxy refuses it, and all
	// yt-dlp can report is "HTTP Error 403" -- which classifies as
	// extractor_broken and tells the visitor we are broken when in fact the
	// guard did its job. Comparing the counter is what turns that back into
	// the truthful answer.
	_, refusedBefore := s.egress.stats()
	info, rerr := s.yt.probe(r.Context(), raw)
	if rerr != nil {
		if _, refusedAfter := s.egress.stats(); refusedAfter > refusedBefore {
			rerr = &resolveError{
				Code:      codeBlocked,
				Message:   "that link redirects to an address this server will not fetch",
				Extractor: rerr.Extractor,
				Upstream:  rerr.Upstream,
			}
		}
		s.note(rerr.Extractor, rerr.Code)
		log.Printf("link resolve %q: %s", redactURL(raw), rerr.Error())
		writeLinkError(w, statusFor(rerr.Code), rerr)
		return
	}

	resp := s.buildResponse(r.Context(), raw, info)
	s.note(resp.Extractor, codeOK)

	body, err := json.Marshal(resp)
	if err != nil {
		writeLinkError(w, http.StatusInternalServerError, &resolveError{
			Code: codeExtractorBroke, Message: "could not encode that result"})
		return
	}
	s.putCache(raw, body)
	w.Header().Set("X-Cache", "MISS")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *linkService) buildResponse(ctx context.Context, raw string, info *ytInfo) linkResponse {
	// A playlist, a channel page, or an Instagram carousel: a list, not a
	// single file. Returned as items the caller resolves one at a time, which
	// is what keeps a 200-video channel from becoming a 200-subprocess fan-out.
	if info.Type == "playlist" || (len(info.Entries) > 0 && len(info.Formats) == 0) {
		items := make([]linkItem, 0, len(info.Entries))
		for i, e := range info.Entries {
			if i >= maxPlaylist {
				break
			}
			u := e.WebpageURL
			if u == "" {
				u = e.URL
			}
			if u == "" {
				continue
			}
			items = append(items, linkItem{
				Title: linkFirstNonEmpty(e.Title, e.ID, u), URL: u,
				Thumbnail: e.Thumbnail, Duration: e.Duration,
			})
		}
		notes := []string{}
		if len(info.Entries) > maxPlaylist {
			notes = append(notes, fmt.Sprintf(
				"that link holds %d items — showing the first %d", len(info.Entries), maxPlaylist))
		}
		return linkResponse{
			Kind: "collection", Source: raw, Extractor: info.Extractor,
			Title: linkFirstNonEmpty(info.Title, "Collection"), Items: items, Notes: notes,
		}
	}

	formats := normaliseFormats(info)
	// Sizes are fetched, not guessed. Most networks report nothing at all --
	// Vimeo, Dailymotion, Twitch and Instagram all came back with zero sized
	// formats -- so without this the list would be a column of "unknown" or,
	// worse, bitrate maths presented as fact.
	probeSizes(ctx, formats, sizeProbeTime, sizeProbeCount, s.egress.URL())

	// Before choosing a default: on Vimeo and Reddit every video track is
	// silent, so unless the silent ones can be paired with audio there is no
	// safe default to choose at all.
	attachPairing(formats, muxAvailable())

	kind := "video"
	anyVideo := false
	for _, f := range formats {
		if f.HasVideo {
			anyVideo = true
			break
		}
	}
	if !anyVideo {
		kind = "audio"
	}

	best := bestFormat(formats, anyVideo)
	for i := range formats {
		formats[i].Media = s.mediaURL(formats[i], info)
	}

	return linkResponse{
		Kind: kind, Source: raw, Extractor: info.Extractor,
		Title:    linkFirstNonEmpty(info.Title, info.ID, "Untitled"),
		Uploader: linkFirstNonEmpty(info.Uploader, info.Channel),
		Duration: info.Duration, Thumbnail: info.Thumbnail,
		IsLive:   info.IsLive || info.LiveStatus == "is_live",
		AgeLimit: info.AgeLimit,
		Formats:  formats, Best: best,
		Notes: formatNotes(formats, best),
	}
}

// formatNotes says out loud the things a quality list normally hides.
func formatNotes(fs []linkFormat, best int) []string {
	var notes []string
	silent, streaming, estimated, muxed := 0, 0, 0, 0
	bestWithSound := 0
	for _, f := range fs {
		switch formatClass(f) {
		case classSilent:
			silent++
		case classComplete, classProbablyOK:
			if f.Height > bestWithSound {
				bestWithSound = f.Height
			}
		}
		if f.Muxed {
			muxed++
		}
		if f.Streaming {
			streaming++
		}
		if f.BytesGuessed {
			estimated++
		}
	}
	if muxed > 0 {
		notes = append(notes, fmt.Sprintf(
			"This network serves picture and sound as separate tracks. %d of these are joined "+
				"back together as they download, so what you save has both.", muxed))
	}
	if silent > 0 {
		n := fmt.Sprintf("%d carry no audio track and could not be paired with one — "+
			"they are marked, and none of them is the default.", silent)
		if bestWithSound > 0 {
			n += fmt.Sprintf(" The highest quality with sound is %dp.", bestWithSound)
		}
		notes = append(notes, n)
	}
	if streaming > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d are segmented streams (HLS/DASH) — they play here, but they are not a single file you can save.", streaming))
	}
	if estimated > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d sizes are marked ~ because that network would not tell us and the file would not answer a size request.", estimated))
	}
	// best < 0 means nothing here is safe to pre-select. That is a real state
	// -- it is what Vimeo and Reddit look like on a server with no ffmpeg --
	// and the visitor is told rather than handed a silent file by default.
	if best < 0 {
		notes = append(notes, "Nothing here has both picture and sound in one file, and this server "+
			"cannot join them. Pick a video track and an audio track and join them yourself.")
	} else if best < len(fs) && !fs[best].AudioKnown && fs[best].HasVideo {
		notes = append(notes, "This network does not report codecs, so we cannot promise the audio track is there — it almost always is.")
	}
	return notes
}

// ----------------------------------------------------------------- media --

// mediaToken is what the media endpoint accepts instead of a raw URL.
//
// Without it, /api/link/media?u=<anything> would be a second open proxy sitting
// next to the first, and one that bypasses the resolve rate limit. The token
// is signed with a key generated at startup, so only a URL this process
// resolved -- within the last half hour -- can be streamed.
type mediaToken struct {
	URL     string            `json:"u"`
	Headers map[string]string `json:"h,omitempty"`
	Name    string            `json:"n,omitempty"`
	Mime    string            `json:"m,omitempty"`
	Expires int64             `json:"e"`
	// Audio, when set, means this token is a request to join two tracks. Both
	// halves live inside the signature so neither can be swapped for another
	// URL after the fact.
	Audio        string            `json:"a,omitempty"`
	AudioHeaders map[string]string `json:"ah,omitempty"`
	// Page and FormatID are the fallback route: when a CDN refuses this
	// server directly, yt-dlp is asked to fetch the same format itself. Both
	// are inside the signature, so a caller cannot point the fallback at an
	// arbitrary page and use it as an unmetered extraction API.
	Page     string `json:"p,omitempty"`
	FormatID string `json:"f,omitempty"`
	// Remux means run this through ffmpeg on the way out -- either to join two
	// tracks, or to repack a segmented stream into a single MP4.
	Remux bool `json:"rx,omitempty"`
}

func (s *linkService) mediaURL(f linkFormat, info *ytInfo) string {
	if f.rawURL == "" {
		return ""
	}
	tok := mediaToken{
		URL: f.rawURL, Headers: f.headers, Expires: time.Now().Add(mediaTokenTTL).Unix(),
		Name: downloadName(info.Title, f), Mime: mimeFor(f),
		Page: info.WebpageURL, FormatID: f.ID,
	}
	switch {
	case f.Muxed && f.pairURL != "":
		tok.Audio, tok.AudioHeaders = f.pairURL, f.pairHeaders
		tok.Remux = true
	case f.Streaming && muxAvailable():
		// A complete HLS/DASH rendition plays, but it is a playlist of
		// segments -- there is no file to save and no browser outside Safari
		// can play it natively. Repacking the same segments into a fragmented
		// MP4 makes it both saveable and playable, and still re-encodes
		// nothing.
		tok.Remux = true
	}
	if tok.Remux {
		// Whatever the source containers were, what comes out is MP4.
		tok.Mime = "video/mp4"
		tok.Name = strings.TrimSuffix(tok.Name, "."+f.Ext) + ".mp4"
	}
	signed := s.signToken(tok)
	if signed == "" {
		return ""
	}
	return "/api/link/media?t=" + signed
}

func (s *linkService) signToken(tok mediaToken) string {
	payload, err := json.Marshal(tok)
	if err != nil {
		return ""
	}
	b := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(b))
	return b + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *linkService) openToken(raw string) (*mediaToken, error) {
	i := strings.LastIndexByte(raw, '.')
	if i <= 0 {
		return nil, fmt.Errorf("malformed token")
	}
	body, sig := raw[:i], raw[i+1:]
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	// Constant time: a byte-at-a-time comparison here is a signature oracle.
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return nil, fmt.Errorf("bad signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, err
	}
	var t mediaToken
	if err := json.Unmarshal(payload, &t); err != nil {
		return nil, err
	}
	if time.Now().Unix() > t.Expires {
		return nil, fmt.Errorf("expired")
	}
	return &t, nil
}

// handleMedia streams a resolved format back to the browser.
//
// It exists because a resolved CDN URL usually cannot be used directly from a
// page: the CDN sends no Access-Control-Allow-Origin, and several networks
// require a Referer the browser will not let a <video> element set. Proxying
// fixes both, and is the same reason /bridge/iptv exists next door.
//
// Range headers are passed through in both directions so the <video> element
// can seek, which is the difference between "playable" and "downloads for two
// minutes then starts".
func (s *linkService) handleMedia(w http.ResponseWriter, r *http.Request) {
	setLinkCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	tok, err := s.openToken(r.URL.Query().Get("t"))
	if err != nil {
		http.Error(w, "this download link is no longer valid — resolve the page again", http.StatusForbidden)
		return
	}

	ip := linkClientIP(r)
	release, ok := s.stream.acquire(ip)
	if !ok {
		http.Error(w, "too many downloads at once from this address", http.StatusTooManyRequests)
		return
	}
	defer release()

	// Joining two tracks, or repacking a segmented stream into one file.
	if tok.Remux {
		s.streamMuxed(w, r, tok)
		return
	}

	// Re-checked even though we minted the token: the CDN URL was validated at
	// resolve time, up to half an hour ago, and DNS can have moved since.
	if _, err := resolveTarget(r.Context(), nil, tok.URL); err != nil {
		http.Error(w, errBlocked.Error(), http.StatusForbidden)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, tok.URL, nil)
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusBadGateway)
		return
	}
	for k, v := range tok.Headers {
		req.Header.Set(k, v)
	}
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}

	client := guardedClientVia(0, s.egress.URL()) // long-lived: this is a media stream
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "that file's host did not answer", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Some CDNs will not serve a resolved URL to anyone but the client that
	// resolved it -- TikTok answers 403 to a request carrying its own reported
	// headers and cookie. Nothing can be fixed by retrying the same way, so
	// the fetch is handed to yt-dlp, which holds the session that works.
	// Nothing has been written to w yet, so this is still a clean swap.
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		if tok.Page != "" {
			resp.Body.Close()
			s.streamViaResolver(w, r, tok)
			return
		}
	}

	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if w.Header().Get("Content-Type") == "" && tok.Mime != "" {
		w.Header().Set("Content-Type", tok.Mime)
	}
	if r.URL.Query().Get("dl") == "1" && tok.Name != "" {
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+tok.Name+`"; filename*=UTF-8''`+urlEscapeFilename(tok.Name))
	}
	// Never let a proxy or the browser cache somebody else's resolved media.
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// streamViaResolver is the fallback for a CDN that refused us directly. It
// costs a second extraction and gives up Range support, so the response says
// so rather than letting a browser think it can resume.
func (s *linkService) streamViaResolver(w http.ResponseWriter, r *http.Request, tok *mediaToken) {
	ctx, cancel := context.WithTimeout(r.Context(), muxMaxDuration)
	defer cancel()

	w.Header().Set("Accept-Ranges", "none")
	if tok.Mime != "" {
		w.Header().Set("Content-Type", tok.Mime)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("dl") == "1" && tok.Name != "" {
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+tok.Name+`"; filename*=UTF-8''`+urlEscapeFilename(tok.Name))
	}

	cw := &countingWriter{w: w, flusher: asFlusher(w)}
	if err := s.yt.stream(ctx, cw, tok.Page, tok.FormatID); err != nil && cw.n == 0 {
		// Nothing written yet, so this can still be an honest error response.
		http.Error(w, "that network would not release the file to this server: "+err.Error(),
			http.StatusBadGateway)
		return
	}
	if cw.n == 0 {
		log.Printf("link media: resolver fallback produced no bytes for %s", redactURL(tok.Page))
	}
}

type countingWriter struct {
	w       io.Writer
	flusher http.Flusher
	n       int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.flusher != nil {
		c.flusher.Flush()
	}
	return n, err
}

func asFlusher(w http.ResponseWriter) http.Flusher {
	if f, ok := w.(http.Flusher); ok {
		return f
	}
	return nil
}

// ---------------------------------------------------------------- health --

// handleHealth is what makes a broken extractor visible instead of silent.
//
// A resolver that has answered "no formats" 200 times for one extractor since
// lunch is a network that changed, and this is where you see it without
// reading a log. The yt-dlp version is here for the same reason: the first
// question when several networks break at once is "how old is the binary".
func (s *linkService) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	version, verr := s.yt.Version(ctx)

	s.statMu.Lock()
	counts := make(map[string]int, len(s.stats))
	for k, v := range s.stats {
		counts[k] = v
	}
	s.statMu.Unlock()

	allowed, refused := s.egress.stats()
	status := "ok"
	if verr != nil {
		status = "resolver missing"
	}
	writeJSON(w, 200, map[string]any{
		"status":        status,
		"ytdlp":         version,
		"ytdlpError":    errString(verr),
		"egressAllowed": allowed,
		"egressRefused": refused,
		"outcomes":      counts,
	})
}

// ----------------------------------------------------------- rate limits --

// ipLimiter is a token bucket per client address. It is the difference between
// a paste box and a free extraction API for whoever finds it first.
type ipLimiter struct {
	capacity int
	refill   time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens int
	last   time.Time
}

func newIPLimiter(capacity int, refill time.Duration) *ipLimiter {
	l := &ipLimiter{capacity: capacity, refill: refill, buckets: make(map[string]*bucket)}
	go l.evict()
	return l
}

func (l *ipLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		l.buckets[ip] = &bucket{tokens: l.capacity - 1, last: now}
		return true
	}
	gained := int(now.Sub(b.last) / l.refill)
	if gained > 0 {
		b.tokens = min(l.capacity, b.tokens+gained)
		b.last = now
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

func (l *ipLimiter) evict() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-30 * time.Minute)
		l.mu.Lock()
		for k, b := range l.buckets {
			if b.last.Before(cutoff) {
				delete(l.buckets, k)
			}
		}
		l.mu.Unlock()
	}
}

// streamGate caps concurrent media streams, globally and per address. Streams
// are the expensive endpoint -- every byte is paid for twice on this box.
type streamGate struct {
	global, perIP int
	mu            sync.Mutex
	active        map[string]int
	total         int
}

func newStreamGate(global, perIP int) *streamGate {
	if global <= 0 {
		global = 24
	}
	if perIP <= 0 {
		perIP = 3
	}
	return &streamGate{global: global, perIP: perIP, active: make(map[string]int)}
}

func (g *streamGate) acquire(ip string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total >= g.global || g.active[ip] >= g.perIP {
		return nil, false
	}
	g.total++
	g.active[ip]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.total--
		if g.active[ip] <= 1 {
			delete(g.active, ip)
		} else {
			g.active[ip]--
		}
	}, true
}

// ----------------------------------------------------------------- cache --

func (s *linkService) cached(u string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.cache[u]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.body, true
}

func (s *linkService) putCache(u string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) > 500 {
		for k, e := range s.cache {
			if time.Now().After(e.expires) {
				delete(s.cache, k)
			}
		}
	}
	s.cache[u] = linkCacheEntry{body: body, expires: time.Now().Add(linkCacheTTL)}
}

// ----------------------------------------------------------------- utils --

func statusFor(code string) int {
	switch code {
	case codeUnsupported, codeNotFound:
		return http.StatusNotFound
	case codeNoVideoInPost, codeNoFormats, codeLive:
		// The link was read fine; there is just nothing here to hand over.
		// That is not a 404 and pretending otherwise sends people to check
		// whether they copied the URL correctly.
		return http.StatusUnprocessableEntity
	case codeLoginRequired, codePrivate, codeGeoBlocked, codeDRM, codeBlocked:
		return http.StatusForbidden
	case codeRateLimited:
		return http.StatusTooManyRequests
	case codeTimeout:
		return http.StatusGatewayTimeout
	case codeToolMissing:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func writeLinkError(w http.ResponseWriter, status int, e *resolveError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": e.Message, "code": e.Code,
		"extractor": e.Extractor, "upstream": e.Upstream,
	})
}

func setLinkCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Range")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
}

func linkClientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		if i := strings.IndexByte(f, ','); i > 0 {
			return strings.TrimSpace(f[:i])
		}
		return strings.TrimSpace(f)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// redactURL keeps query strings out of the log. A pasted link can carry a
// share token that identifies the person who sent it.
func redactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i > 0 {
		return raw[:i] + "?…"
	}
	return raw
}

func linkFirstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func mimeFor(f linkFormat) string {
	switch strings.ToLower(f.Ext) {
	case "mp4", "m4v":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mkv":
		return "video/x-matroska"
	case "mov":
		return "video/quicktime"
	case "m4a":
		return "audio/mp4"
	case "mp3":
		return "audio/mpeg"
	case "opus", "ogg", "oga":
		return "audio/ogg"
	case "wav":
		return "audio/wav"
	case "flac":
		return "audio/flac"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	}
	if f.Streaming {
		return "application/vnd.apple.mpegurl"
	}
	return ""
}

// downloadName builds the saved filename. Anything that could steer a path or
// break a Content-Disposition header is stripped rather than escaped -- a
// title is attacker-controlled text arriving from a social network.
func downloadName(title string, f linkFormat) string {
	cleaned := make([]rune, 0, len(title))
	for _, r := range title {
		switch {
		case r < 0x20, r == 0x7f:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			cleaned = append(cleaned, '-')
		default:
			cleaned = append(cleaned, r)
		}
	}
	name := strings.TrimSpace(strings.Trim(string(cleaned), "."))
	if name == "" {
		name = "download"
	}
	if len(name) > 120 {
		name = strings.TrimSpace(name[:120])
	}
	suffix := f.Label
	if suffix != "" {
		name += " [" + suffix + "]"
	}
	ext := f.Ext
	if ext == "" {
		ext = "bin"
	}
	return name + "." + ext
}

func urlEscapeFilename(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(c), 16)))
	}
	return b.String()
}
