package main

// Romarr: the *arr for games -- the acquisition half of the games story.
//
// Where RomM serves what is held, Romarr goes and gets what is not: it searches
// indexers for a ROM, hands the winning release to a download client, verifies
// the finished file against a No-Intro or Redump DAT, and files it into the
// library. So this adapter is a Searcher (an indexer, in schema.json's role
// vocabulary -- it returns raw downloadable releases, not canonical works), a
// Requester, a LibraryProvider over its own acquisition ledger, and an
// ActivityProvider over what is in flight.
//
// The one subtlety the design calls out explicitly: Romarr's pipeline has a
// verify stage between "downloaded" and "in the library", and a file that is
// being verified is NOT yet available. So romarrLibraryState maps "verifying"
// (and "importing") onto StateImporting, never StateAvailable -- offering a
// user a play button for a ROM that has not passed its DAT check is exactly the
// dishonesty the state machine exists to prevent.
//
// The API is reached over HTTP only. Romarr's store is never touched directly:
// that is a product rule, and the /api/v1/* surface is the supported way in.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const romarrDefaultTimeout = 90 * time.Second

type romarrConfig struct {
	ID      string
	Name    string
	BaseURL string
	APIKey  string

	HTTPClient *http.Client
}

// --- API shapes -------------------------------------------------------------

type romarrSystemStatus struct {
	Version     string               `json:"version"`
	Prowlarr    bool                 `json:"prowlarr"`
	Qbittorrent bool                 `json:"qbittorrent"`
	Romm        bool                 `json:"romm"`
	Library     bool                 `json:"library"`
	Libraries   []romarrLibrary      `json:"libraries"`
	Clients     []romarrClientStatus `json:"clients"`
	Ggrequestz  *romarrDepStatus     `json:"ggrequestz"`
}

type romarrLibrary struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
}

type romarrClientStatus struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	OK         bool   `json:"ok"`
}

type romarrDepStatus struct {
	Configured bool `json:"configured"`
	OK         bool `json:"ok"`
}

type romarrQueueItem struct {
	Game     string `json:"game"`
	Platform string `json:"platform"`
	Release  string `json:"release"`
	Seeders  int    `json:"seeders"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
	At       string `json:"at"`
}

type romarrQueuePage struct {
	Items []romarrQueueItem `json:"items"`
}

type romarrWantedItem struct {
	Game      string `json:"game"`
	Platform  string `json:"platform"`
	Added     string `json:"added"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
}

type romarrWantedPage struct {
	Items []romarrWantedItem `json:"items"`
}

type romarrCandidate struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Size      int64    `json:"size"`
	Seeders   int      `json:"seeders"`
	Indexer   string   `json:"indexer"`
	Protocol  string   `json:"protocol"`
	Private   bool     `json:"private"`
	Score     int      `json:"score"`
	Accepted  bool     `json:"accepted"`
	Grabbable bool     `json:"grabbable"`
	Reasons   []string `json:"reasons"`
}

type romarrReleasePage struct {
	Game     string            `json:"game"`
	Platform string            `json:"platform"`
	Found    int               `json:"found"`
	Accepted int               `json:"accepted"`
	Items    []romarrCandidate `json:"items"`
	Error    string            `json:"error"`
}

type romarrRequestResult struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Release string `json:"release"`
	Seeders int    `json:"seeders"`
}

// --- client -----------------------------------------------------------------

type romarrClient struct {
	baseURL string // no trailing slash
	apiKey  string
	hc      *http.Client
}

func newRomarrClient(cfg romarrConfig) *romarrClient {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: romarrDefaultTimeout}
	}
	return &romarrClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		hc:      hc,
	}
}

func (c *romarrClient) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	return gameSend(c.hc, req, out)
}

func (c *romarrClient) postJSON(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	c.auth(req)
	req.Header.Set("Content-Type", "application/json")
	return gameSend(c.hc, req, out)
}

func (c *romarrClient) auth(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
}

// --- provider ---------------------------------------------------------------

type romarrProvider struct {
	c    *romarrClient
	id   string
	name string
}

func newRomarrProvider(cfg romarrConfig) *romarrProvider {
	id, name := cfg.ID, cfg.Name
	if id == "" {
		id = "romarr"
	}
	if name == "" {
		name = "Romarr"
	}
	return &romarrProvider{c: newRomarrClient(cfg), id: id, name: name}
}

func (p *romarrProvider) ID() string   { return p.id }
func (p *romarrProvider) Name() string { return p.name }

func (p *romarrProvider) Domains() []string { return []string{"game"} }

// Roles: it obtains ("acquisition"), its search returns raw downloadable
// releases rather than canonical works ("indexer"), and it tracks an
// acquisition ledger of what has been asked for and where each thing is in the
// pipeline ("library"). It never launches anything, so no "play".
func (p *romarrProvider) Roles() []string {
	return []string{"acquisition", "indexer", "library"}
}

func (p *romarrProvider) Capabilities() []string {
	return []string{"health", "search", "details", "library", "libraryStatus", "request", "activity"}
}

// --- health -----------------------------------------------------------------

// Health uses the authenticated /api/v1/system/status rather than /api/health,
// on purpose: Romarr's /api/health answers {ok:true} to anyone, even with a
// wrong key, so it can say "reachable" but never "auth_failed". system/status
// is behind the gate and reports every dependency, which is what lets a healthy
// instance be told apart from one that is up but has lost Prowlarr.
func (p *romarrProvider) Health(ctx context.Context) Health {
	if p.c.apiKey == "" {
		return Health{State: HealthNotConfigured,
			Detail: "no API key set for " + p.name + " — copy it from its Settings page"}
	}
	if p.c.baseURL == "" {
		return Health{State: HealthNotConfigured, Detail: "no address set for " + p.name}
	}

	var st romarrSystemStatus
	if err := p.c.get(ctx, "/api/v1/system/status", nil, &st); err != nil {
		if status, ok := gameHTTPStatus(err); ok {
			switch {
			case gameIsAuthStatus(status):
				return Health{State: HealthAuthFailed,
					Detail: "the API key for " + p.name + " was rejected"}
			case gameIsIncompatibleStatus(status):
				return Health{State: HealthIncompatible,
					Detail: "this address answers, but does not speak the " + p.name + " API — check the URL"}
			}
			return Health{State: HealthDegraded,
				Detail: p.name + " answered its status check with HTTP " + fmt.Sprint(status)}
		}
		return Health{State: HealthUnreachable, Detail: "could not reach " + p.name + " at " + p.c.baseURL}
	}

	if strings.TrimSpace(st.Version) == "" {
		// A 200 with none of the shape we need is not something we can drive.
		return Health{State: HealthIncompatible,
			Detail: p.name + " did not report a version we can read"}
	}

	// Up and speaking the right API. Now ask what it thinks of itself: a
	// dependency it reports as down is a real, operator-fixable problem.
	var problems []string
	if !st.Prowlarr {
		problems = append(problems, "Prowlarr unreachable")
	}
	if !st.Qbittorrent {
		problems = append(problems, "qBittorrent unreachable")
	}
	if !st.Romm {
		problems = append(problems, "RomM unreachable")
	}
	for _, l := range st.Libraries {
		if !l.OK {
			problems = append(problems, "library "+l.Name+" unreachable")
		}
	}
	for _, cl := range st.Clients {
		if cl.Configured && !cl.OK {
			problems = append(problems, "download client "+cl.Name+" unreachable")
		}
	}
	if st.Ggrequestz != nil && st.Ggrequestz.Configured && !st.Ggrequestz.OK {
		problems = append(problems, "GG Requestz unreachable")
	}
	if len(problems) > 0 {
		return Health{State: HealthDegraded, Version: st.Version, Detail: strings.Join(problems, "; ")}
	}
	return Health{State: HealthOK, Version: st.Version}
}

// --- search -----------------------------------------------------------------

// Search answers "what releases exist for this game", by running Romarr's
// interactive search -- the read half of the request path. It grabs nothing and
// records nothing: it is the same scored-candidate view the UI shows before a
// human picks one, which is exactly what makes it safe to exercise.
func (p *romarrProvider) Search(ctx context.Context, query, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "game") {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	var page romarrReleasePage
	if err := p.c.get(ctx, "/api/v1/release", url.Values{"game": {query}}, &page); err != nil {
		return nil, err
	}
	out := make([]MediaItem, 0, len(page.Items))
	for _, cand := range page.Items {
		out = append(out, romarrCandidateItem(p.id, cand))
	}
	return out, nil
}

func romarrCandidateItem(providerID string, cand romarrCandidate) MediaItem {
	subtitle := cand.Indexer
	if !cand.Accepted {
		subtitle = strings.TrimSpace(subtitle + " · below the bar")
	}
	return MediaItem{
		CanonicalID:    "romarr:release:" + cand.ID,
		Domain:         "game",
		Type:           "release",
		Title:          cand.Title,
		Subtitle:       subtitle,
		Overview:       strings.Join(cand.Reasons, ", "),
		ProviderID:     providerID,
		ProviderItemID: cand.ID,
		// A search hit is not held; it is a candidate to acquire.
		State: StateMissing,
	}
}

// --- library (the acquisition ledger) ---------------------------------------
//
// Romarr's library view is deliberately its acquisition ledger -- what has been
// asked for and where each thing is in the pipeline -- not a second copy of
// RomM's finished shelf. That is the part Romarr uniquely knows, and it is where
// the verify stage becomes visible: a ledger entry can honestly read
// "importing" (verifying) without ever being reported as available.

func (p *romarrProvider) Library(ctx context.Context, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "game") {
		return nil, nil
	}
	queue, err := p.queue(ctx)
	if err != nil {
		return nil, err
	}
	wanted, err := p.wanted(ctx)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	out := make([]MediaItem, 0, len(queue)+len(wanted))
	for _, q := range queue {
		id := romarrGameID(q.Platform, q.Game)
		seen[id] = true
		out = append(out, MediaItem{
			CanonicalID:    id,
			Domain:         "game",
			Type:           "game",
			Title:          q.Game,
			Subtitle:       q.Platform,
			Overview:       q.Detail,
			ProviderID:     p.id,
			ProviderItemID: q.Game + "|" + q.Platform,
			State:          romarrLibraryState(q.State),
		})
	}
	for _, wi := range wanted {
		id := romarrGameID(wi.Platform, wi.Game)
		if seen[id] {
			continue
		}
		out = append(out, MediaItem{
			CanonicalID:    id,
			Domain:         "game",
			Type:           "game",
			Title:          wi.Game,
			Subtitle:       wi.Platform,
			Overview:       wi.LastError,
			ProviderID:     p.id,
			ProviderItemID: wi.Game + "|" + wi.Platform,
			State:          StateRequested,
		})
	}
	return out, nil
}

// LibraryStatus reports where one requested game is in the pipeline. It reads
// only the small queue and wanted lists, so it is cheap, and it opines only on
// ids Romarr itself issued -- a RomM release id gets StateUnknown, because
// whether that ROM is playable is RomM's question to answer, not Romarr's.
func (p *romarrProvider) LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error) {
	slug, name, ok := parseRomarrGameID(canonicalID)
	if !ok {
		return StateUnknown, nil
	}
	queue, err := p.queue(ctx)
	if err != nil {
		return StateUnknown, err
	}
	for _, q := range queue {
		if strings.EqualFold(q.Platform, slug) && strings.EqualFold(q.Game, name) {
			return romarrLibraryState(q.State), nil
		}
	}
	wanted, err := p.wanted(ctx)
	if err != nil {
		return StateUnknown, err
	}
	for _, wi := range wanted {
		if strings.EqualFold(wi.Platform, slug) && strings.EqualFold(wi.Game, name) {
			return StateRequested, nil
		}
	}
	return StateUnknown, nil
}

func (p *romarrProvider) queue(ctx context.Context) ([]romarrQueueItem, error) {
	var page romarrQueuePage
	if err := p.c.get(ctx, "/api/v1/queue", nil, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (p *romarrProvider) wanted(ctx context.Context) ([]romarrWantedItem, error) {
	var page romarrWantedPage
	if err := p.c.get(ctx, "/api/v1/wanted/missing", nil, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

// romarrLibraryState is the honesty-critical mapping. The verify stage is the
// reason it exists: a file being verified against its DAT has been downloaded
// but is NOT yet in the library, so "verifying" -- like "importing" -- is
// StateImporting, and only a finished, imported ROM is StateAvailable.
func romarrLibraryState(stage string) LibraryState {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "imported", "available":
		return StateAvailable
	case "verifying", "verify", "importing", "import":
		return StateImporting
	case "grabbed", "downloading", "queued", "delay":
		return StateDownloading
	case "searching", "wanted", "requested", "pending":
		return StateRequested
	case "failed", "missing", "error":
		return StateMissing
	default:
		return StateUnknown
	}
}

// --- details ----------------------------------------------------------------

// Details describes a requested game by running the interactive search for it
// and summarising what Romarr would find. Release ids are not describable on
// their own (they live in Romarr's per-search candidate cache), so they are
// declined rather than guessed at.
func (p *romarrProvider) Details(ctx context.Context, canonicalID string) (MediaItem, []MediaItem, error) {
	slug, name, ok := parseRomarrGameID(canonicalID)
	if !ok {
		return MediaItem{}, nil, fmt.Errorf("%s can only describe a game+platform, not %q", p.name, canonicalID)
	}
	q := url.Values{"game": {name}}
	if slug != "" {
		q.Set("platform", slug)
	}
	var page romarrReleasePage
	if err := p.c.get(ctx, "/api/v1/release", q, &page); err != nil {
		return MediaItem{}, nil, err
	}
	overview := fmt.Sprintf("%d release(s) found, %d acceptable", page.Found, page.Accepted)
	if len(page.Items) > 0 {
		overview += "; best: " + page.Items[0].Title
	}
	item := MediaItem{
		CanonicalID: canonicalID,
		Domain:      "game",
		Type:        "game",
		Title:       name,
		Subtitle:    slug,
		Overview:    overview,
		ProviderID:  p.id,
	}
	children := make([]MediaItem, 0, len(page.Items))
	for _, cand := range page.Items {
		children = append(children, romarrCandidateItem(p.id, cand))
	}
	return item, children, nil
}

// --- request ----------------------------------------------------------------

// Request obtains a game. Two id shapes are honoured: a "romarr:game:<plat>~
// <name>" id auto-picks and grabs the best release for that game and platform
// (POST /api/request), and a "romarr:release:<id>" id grabs one specific
// candidate from a prior interactive search (POST /api/v1/release/grab).
//
// Both genuinely hand a release to a download client, which is why the live
// verification exercises the read-only interactive search instead -- see the
// report. RequestOptions has no effect here: Romarr has no unmonitored
// watchlist mode, so there is no safe no-op grab to expose.
func (p *romarrProvider) Request(ctx context.Context, item MediaItem, _ RequestOptions) (RequestResult, error) {
	if id, ok := parseRomarrReleaseID(item.CanonicalID); ok {
		var res romarrRequestResult
		if err := p.c.postJSON(ctx, "/api/v1/release/grab", map[string]string{"id": id}, &res); err != nil {
			return RequestResult{}, err
		}
		return RequestResult{Accepted: res.OK, Detail: romarrRequestDetail(res)}, nil
	}

	slug, name, ok := parseRomarrGameID(item.CanonicalID)
	if !ok {
		// Fall back to the human title if no canonical id was given, so a caller
		// can request "Chrono Trigger" on "snes" by title alone.
		name = strings.TrimSpace(item.Title)
		slug = strings.TrimSpace(item.Subtitle)
	}
	if name == "" || slug == "" {
		return RequestResult{}, fmt.Errorf("%s needs a game and a platform to obtain %q", p.name, item.CanonicalID)
	}
	var res romarrRequestResult
	if err := p.c.postJSON(ctx, "/api/request",
		map[string]string{"game": name, "platform": slug}, &res); err != nil {
		return RequestResult{}, err
	}
	return RequestResult{Accepted: res.OK, Detail: romarrRequestDetail(res)}, nil
}

func romarrRequestDetail(res romarrRequestResult) string {
	if res.OK {
		if res.Release != "" {
			return "grabbed " + res.Release
		}
		return "accepted"
	}
	if res.Error != "" {
		return res.Error
	}
	return "Romarr did not accept the request"
}

// --- activity ---------------------------------------------------------------

func (p *romarrProvider) Activity(ctx context.Context) ([]ActivityItem, error) {
	queue, err := p.queue(ctx)
	if err != nil {
		return nil, err
	}
	wanted, err := p.wanted(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ActivityItem, 0, len(queue)+len(wanted))
	for _, q := range queue {
		out = append(out, ActivityItem{
			CanonicalID: romarrGameID(q.Platform, q.Game),
			Title:       q.Game,
			Domain:      "game",
			ProviderID:  p.id,
			Stage:       romarrStage(q.State),
			Detail:      q.Detail,
		})
	}
	for _, wi := range wanted {
		out = append(out, ActivityItem{
			CanonicalID: romarrGameID(wi.Platform, wi.Game),
			Title:       wi.Game,
			Domain:      "game",
			ProviderID:  p.id,
			Stage:       "searching",
			Detail:      wi.LastError,
		})
	}
	return out, nil
}

// romarrStage names what a person would call the queue state, and keeps the
// verify stage visible rather than folding it into "downloading".
func romarrStage(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "verifying", "verify", "importing", "import":
		return "importing"
	case "imported":
		return "imported"
	case "grabbed", "downloading":
		return "downloading"
	case "queued", "delay":
		return "queued"
	case "failed", "error":
		return "failed"
	default:
		if state == "" {
			return "queued"
		}
		return strings.ToLower(state)
	}
}

// --- canonical ids ----------------------------------------------------------

// A requested game is identified by its platform slug and name. The name is
// base64url-encoded so the id stays free of the ':' the scheme splits on and
// the '~' that separates the two parts -- a game called "R:Racing" or one with
// a stray tilde must not fracture its own id.
func romarrGameID(platform, name string) string {
	return "romarr:game:" + strings.ToLower(strings.TrimSpace(platform)) + "~" +
		base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(name)))
}

func parseRomarrGameID(canonicalID string) (slug, name string, ok bool) {
	const prefix = "romarr:game:"
	if !strings.HasPrefix(canonicalID, prefix) {
		return "", "", false
	}
	rest := canonicalID[len(prefix):]
	sep := strings.IndexByte(rest, '~')
	if sep < 0 {
		return "", "", false
	}
	slug = rest[:sep]
	dec, err := base64.RawURLEncoding.DecodeString(rest[sep+1:])
	if err != nil {
		return "", "", false
	}
	name = string(dec)
	if name == "" {
		return "", "", false
	}
	return slug, name, true
}

func parseRomarrReleaseID(canonicalID string) (string, bool) {
	const prefix = "romarr:release:"
	if !strings.HasPrefix(canonicalID, prefix) {
		return "", false
	}
	id := canonicalID[len(prefix):]
	if id == "" {
		return "", false
	}
	return id, true
}

// ---------------------------------------------------------------------------
//   Wiring
// ---------------------------------------------------------------------------

// gameProvidersFromEnv builds the game adapters from the environment, so wiring
// is a config change rather than a code change. An instance with a URL but no
// credential is still returned: dropping it would make the settings screen show
// nothing, which is indistinguishable from "never configured" and leaves the
// user with nowhere to type the key.
//
//	ROMARR_URL, ROMARR_API_KEY            -> the "romarr" provider
//	ROMM_URL, ROMM_USERNAME, ROMM_PASSWORD -> the "romm" provider
//
// ROMM_USER / ROMM_PASS are accepted as aliases for the RomM credential, so an
// operator who already set them for another tool does not have to rename them.
func gameProvidersFromEnv() []Provider {
	var out []Provider

	if u := os.Getenv("ROMARR_URL"); u != "" {
		out = append(out, newRomarrProvider(romarrConfig{
			ID:      "romarr",
			Name:    "Romarr",
			BaseURL: u,
			APIKey:  os.Getenv("ROMARR_API_KEY"),
		}))
	}

	if u := os.Getenv("ROMM_URL"); u != "" {
		out = append(out, newRommProvider(rommConfig{
			ID:       "romm",
			Name:     "RomM",
			BaseURL:  u,
			Username: firstNonEmptyGame(os.Getenv("ROMM_USERNAME"), os.Getenv("ROMM_USER")),
			Password: firstNonEmptyGame(os.Getenv("ROMM_PASSWORD"), os.Getenv("ROMM_PASS")),
		}))
	}
	return out
}

// registerGameRoutes constructs the game providers from the environment, adds
// them to the shared Registry, and wires the game acquisition surface onto a
// mux. It is the games equivalent of registerArrRoutes, kept in this package's
// own file so the caller can wire it without editing main.
//
// Providers go into the same Registry the rest of the app reads, so once added
// they also appear in /api/providers and /api/activity without any further
// wiring. RomM's "play" role is probed here, once, with a short bound: the
// launch path is confirmed before the provider is added, so Roles() tells the
// truth from the first paint.
func registerGameRoutes(mux *http.ServeMux, r *Registry) {
	if r == nil {
		return
	}
	for _, p := range gameProvidersFromEnv() {
		if rp, ok := p.(*rommProvider); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			rp.verifyPlay(ctx)
			cancel()
		}
		if err := r.Add(p); err != nil {
			log.Printf("game provider %q not registered: %v", p.ID(), err)
		}
	}

	a := &gameAPI{reg: r}
	mux.HandleFunc("/api/game/search", a.handleSearch)
	mux.HandleFunc("/api/game/details", a.handleDetails)
	mux.HandleFunc("/api/game/library", a.handleLibrary)
	mux.HandleFunc("/api/game/status", a.handleStatus)
	mux.HandleFunc("/api/game/request", a.handleRequest)
}

// ---------------------------------------------------------------------------
//   HTTP surface
// ---------------------------------------------------------------------------

// gameAPI fans a request out across the game-domain providers. It is a small
// twin of arrAPI, self-contained so the two game adapters depend only on the
// frozen contract. The rule it shares with every other fan-out here: one sick
// provider costs a row, never the page, and a short list that is silently short
// is worse than an error because it looks like an answer.
type gameAPI struct{ reg *Registry }

const gameRouteTimeout = 60 * time.Second

func (a *gameAPI) providers() []Provider {
	if a.reg == nil {
		return nil
	}
	return a.reg.For("game", "")
}

type gameFanoutResult struct {
	Items  []MediaItem `json:"items"`
	Failed []string    `json:"failed,omitempty"`
}

func (a *gameAPI) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 400, map[string]string{"error": "missing q"})
		return
	}
	only := r.URL.Query().Get("provider")

	var (
		mu     sync.Mutex
		items  []MediaItem
		failed []string
		wg     sync.WaitGroup
	)
	for _, p := range a.providers() {
		if only != "" && p.ID() != only {
			continue
		}
		s, ok := p.(Searcher)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(p Provider, s Searcher) {
			defer wg.Done()
			defer gameRecover(&mu, &failed, p.ID())
			ctx, cancel := context.WithTimeout(r.Context(), gameRouteTimeout)
			defer cancel()
			got, err := s.Search(ctx, q, "game")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, p.ID())
				return
			}
			items = append(items, got...)
		}(p, s)
	}
	wg.Wait()

	sort.Strings(failed)
	if items == nil {
		items = []MediaItem{}
	}
	writeJSON(w, 200, gameFanoutResult{Items: items, Failed: failed})
}

func (a *gameAPI) handleLibrary(w http.ResponseWriter, r *http.Request) {
	only := r.URL.Query().Get("provider")

	var (
		mu     sync.Mutex
		items  []MediaItem
		failed []string
		wg     sync.WaitGroup
	)
	for _, p := range a.providers() {
		if only != "" && p.ID() != only {
			continue
		}
		lp, ok := p.(LibraryProvider)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(p Provider, lp LibraryProvider) {
			defer wg.Done()
			defer gameRecover(&mu, &failed, p.ID())
			ctx, cancel := context.WithTimeout(r.Context(), gameRouteTimeout)
			defer cancel()
			got, err := lp.Library(ctx, "game")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, p.ID())
				return
			}
			items = append(items, got...)
		}(p, lp)
	}
	wg.Wait()

	sort.Strings(failed)
	if items == nil {
		items = []MediaItem{}
	}
	writeJSON(w, 200, gameFanoutResult{Items: items, Failed: failed})
}

// handleStatus merges every provider's opinion, strongest wins. With RomM and
// Romarr both answering, a ROM that RomM reports available and Romarr has no
// opinion on is available; a game Romarr is still verifying is "importing", not
// "missing", because importing outranks unknown.
func (a *gameAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing id"})
		return
	}
	only := r.URL.Query().Get("provider")

	best := StateUnknown
	var (
		mu     sync.Mutex
		failed []string
		wg     sync.WaitGroup
	)
	for _, p := range a.providers() {
		if only != "" && p.ID() != only {
			continue
		}
		lp, ok := p.(LibraryProvider)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(p Provider, lp LibraryProvider) {
			defer wg.Done()
			defer gameRecover(&mu, &failed, p.ID())
			ctx, cancel := context.WithTimeout(r.Context(), gameRouteTimeout)
			defer cancel()
			st, err := lp.LibraryStatus(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, p.ID())
				return
			}
			if gameStateRank(st) > gameStateRank(best) {
				best = st
			}
		}(p, lp)
	}
	wg.Wait()
	sort.Strings(failed)

	writeJSON(w, 200, map[string]any{"canonicalId": id, "state": best, "failed": failed})
}

// handleDetails routes by trying each detailer in turn and taking the first
// that owns the id. RomM declines a Romarr id and vice versa, so the right
// provider answers without the handler having to parse the scheme itself. When
// the owning provider is a player, the launch URL rides along -- the only place
// "play" is surfaced, and only for an id a verified player accepts.
func (a *gameAPI) handleDetails(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing id"})
		return
	}
	only := r.URL.Query().Get("provider")

	ctx, cancel := context.WithTimeout(r.Context(), gameRouteTimeout)
	defer cancel()

	var lastErr error
	for _, p := range a.providers() {
		if only != "" && p.ID() != only {
			continue
		}
		d, ok := p.(gameDetailer)
		if !ok {
			continue
		}
		item, children, err := d.Details(ctx, id)
		if err != nil {
			lastErr = err
			continue
		}
		if children == nil {
			children = []MediaItem{}
		}
		resp := map[string]any{"item": item, "children": children, "providerId": p.ID()}
		if pl, ok := p.(gamePlayer); ok {
			if u, ok := pl.PlayURL(ctx, id); ok {
				resp["playUrl"] = u
			}
		}
		writeJSON(w, 200, resp)
		return
	}
	if lastErr != nil {
		writeJSON(w, 502, map[string]string{"error": lastErr.Error()})
		return
	}
	writeJSON(w, 404, map[string]string{"error": "no configured game provider can describe " + id})
}

type gameRequestBody struct {
	ProviderID  string `json:"providerId"`
	CanonicalID string `json:"canonicalId"`
}

// handleRequest hands one game to an acquisition provider. It picks the named
// provider, or the first game Requester when none is named -- which, since RomM
// is not a Requester, is Romarr.
func (a *gameAPI) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	var body gameRequestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
		return
	}
	body.CanonicalID = strings.TrimSpace(body.CanonicalID)
	if body.CanonicalID == "" {
		writeJSON(w, 400, map[string]string{"error": "missing canonicalId"})
		return
	}

	var target Requester
	var targetID string
	for _, p := range a.providers() {
		if body.ProviderID != "" && p.ID() != body.ProviderID {
			continue
		}
		rq, ok := p.(Requester)
		if !ok {
			continue
		}
		target, targetID = rq, p.ID()
		break
	}
	if target == nil {
		writeJSON(w, 404, map[string]string{"error": "no configured provider can obtain " + body.CanonicalID})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), gameRouteTimeout)
	defer cancel()
	res, err := target.Request(ctx,
		MediaItem{CanonicalID: body.CanonicalID, Domain: "game", ProviderID: targetID},
		RequestOptions{})
	if err != nil {
		writeJSON(w, 502, map[string]any{"accepted": false, "providerId": targetID, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"accepted": res.Accepted, "detail": res.Detail, "providerId": targetID})
}

func gameRecover(mu *sync.Mutex, failed *[]string, id string) {
	if rec := recover(); rec != nil {
		mu.Lock()
		*failed = append(*failed, id)
		mu.Unlock()
	}
}

// gameStateRank orders states by how much has already happened, so merging two
// providers' answers is a max rather than a coin flip.
func gameStateRank(s LibraryState) int {
	switch s {
	case StateAvailable:
		return 5
	case StateImporting:
		return 4
	case StateDownloading:
		return 3
	case StateRequested:
		return 2
	case StateMissing:
		return 1
	default:
		return 0
	}
}
