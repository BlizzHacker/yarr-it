package main

// The HTTP surface for addons: install one, see what it provides, turn it off,
// reorder, and query the installed set.
//
// The shape follows providers_api.go rather than inventing a second style,
// because the rule there is the one that matters here too: one sick addon must
// cost a row, never the page. A settings screen that will not paint because an
// addon is hanging is a screen that cannot be used to remove the addon that is
// hanging.
//
// WHY THE GATES ARE WHERE THEY ARE
//
// The split is not "reads are public, writes are private". It is drawn on what
// each route actually exposes, and one of the lines is not obvious:
//
//   * Installing, removing, enabling and reordering need a session. Installing
//     in particular is the route that makes this server fetch a URL a person
//     supplied, and an anonymous caller must never be able to do that.
//
//   * LISTING installed addons needs a session too -- unlike /api/providers,
//     which is deliberately public. The reason is the addon URL itself. The
//     protocol has no auth, so addons that need a key put it in the path:
//     Torrentio's configured install URLs look like
//     https://torrentio.strem.fun/providers=yts%7Crealdebrid=<KEY>/manifest.json
//     and that was confirmed on the wire -- .../providers=yts/manifest.json
//     answers 200 with a manifest whose description shows only YTS enabled.
//     So an installed addon URL is a credential often enough that it must be
//     treated as one always.
//
//   * Searching and meta ARE public and CORS-open, like every other catalogue
//     route here: they describe things that exist in the world, and a
//     television is a different origin.
//
//   * Stream and subtitle lookups need a session. A configured debrid addon
//     answers those with links bound to the household's paid account, and
//     handing those to any origin would be handing out the account.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// How long one addon gets before the aggregate gives up on it. The same bound
// providers_api.go uses, and for the same reason.
const addonProbeTimeout = 6 * time.Second

// A ceiling on installed addons. Not a licensing limit -- a bound on how much
// work one page paint can fan out into. Stremio users with thirty addons are
// the ones who complain that it is slow.
const addonMaxInstalled = 64

// --- storage ---------------------------------------------------------------

// installedAddon is one addon as the household configured it.
//
// The manifest is stored alongside the URL rather than re-fetched on boot: an
// addon that is down must still appear in the list, with its name, so it can be
// removed. A list that only shows the addons that answer cannot be used to
// clean up the ones that do not.
type installedAddon struct {
	URL      string         `json:"url"`
	AddonID  string         `json:"addonId"`
	Manifest *AddonManifest `json:"manifest"`
	Enabled  bool           `json:"enabled"`
	AddedAt  int64          `json:"addedAt"`
}

// addonStore is the installed set, in priority order.
//
// Order is the slice order, not a field. A separate rank integer is a second
// source of truth that drifts the first time two clients reorder at once, and
// the recovery from that is worse than the feature.
type addonStore struct {
	mu    sync.RWMutex
	path  string
	items []installedAddon
}

func newAddonStore(path string) (*addonStore, error) {
	s := &addonStore{path: strings.TrimSpace(path)}
	if s.path == "" {
		// In-memory is a valid state, and the same one a fresh self-host starts
		// in. Addons then last until restart, which is worth a log line and not
		// worth refusing to start over.
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return s, fmt.Errorf("addon store directory: %w", err)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("reading addon store: %w", err)
	}
	var items []installedAddon
	if err := json.Unmarshal(raw, &items); err != nil {
		// A corrupt file must not stop the service. Starting empty loses the
		// list; refusing to start loses everything else too.
		return s, fmt.Errorf("addon store is not valid JSON, starting empty: %w", err)
	}
	for _, it := range items {
		if it.Manifest != nil && it.URL != "" {
			s.items = append(s.items, it)
		}
	}
	return s, nil
}

// save writes atomically -- beside the file, then renamed -- so a crash during
// a write loses the change rather than the list.
func (s *addonStore) save() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	// 0600: this file holds addon URLs, and an addon URL can carry a debrid
	// key. It is a credential store whether or not it looks like one.
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *addonStore) List() []installedAddon {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]installedAddon, len(s.items))
	copy(out, s.items)
	return out
}

func (s *addonStore) Get(addonID string) (installedAddon, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, it := range s.items {
		if it.AddonID == addonID {
			return it, true
		}
	}
	return installedAddon{}, false
}

// Add installs an addon, or updates one already installed at the same id.
//
// Re-adding is an update rather than a duplicate on purpose: the common reason
// to paste a URL again is to change the configuration encoded in it, and
// ending up with two rows called "Torrentio" that behave differently is how
// someone spends an evening confused.
func (s *addonStore) Add(it installedAddon) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.items {
		if existing.AddonID == it.AddonID {
			it.AddedAt = existing.AddedAt
			it.Enabled = it.Enabled || existing.Enabled
			s.items[i] = it
			return s.save()
		}
	}
	if len(s.items) >= addonMaxInstalled {
		return fmt.Errorf("this instance already has the maximum of %d addons installed", addonMaxInstalled)
	}
	s.items = append(s.items, it)
	return s.save()
}

func (s *addonStore) Remove(addonID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, it := range s.items {
		if it.AddonID == addonID {
			s.items = append(s.items[:i], s.items[i+1:]...)
			if err := s.save(); err != nil {
				log.Printf("addon store: %v", err)
			}
			return true
		}
	}
	return false
}

func (s *addonStore) SetEnabled(addonID string, enabled bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, it := range s.items {
		if it.AddonID == addonID {
			s.items[i].Enabled = enabled
			if err := s.save(); err != nil {
				log.Printf("addon store: %v", err)
			}
			return true
		}
	}
	return false
}

// Reorder moves the named addons to the front, in the order given.
//
// Anything not named keeps its relative position behind them. That is what
// makes a drag-and-drop list survive a stale client: a browser that reorders
// three rows while a phone installs a fourth does not delete the fourth.
func (s *addonStore) Reorder(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rank := make(map[string]int, len(ids))
	for i, id := range ids {
		if _, seen := rank[id]; !seen {
			rank[id] = i
		}
	}
	sort.SliceStable(s.items, func(a, b int) bool {
		ra, oka := rank[s.items[a].AddonID]
		rb, okb := rank[s.items[b].AddonID]
		switch {
		case oka && okb:
			return ra < rb
		case oka:
			return true
		case okb:
			return false
		default:
			return false // stable: unnamed addons keep their order
		}
	})
	if err := s.save(); err != nil {
		log.Printf("addon store: %v", err)
	}
}

// --- redaction -------------------------------------------------------------

// redactAddonURL makes an addon URL safe to log.
//
// Everything between the host and /manifest.json is configuration, and
// configuration is where the keys are. Keeping the host means a log line is
// still useful for "which addon is timing out"; dropping the middle means the
// answer to "who has my Real-Debrid key" is not "anyone with the log".
func redactAddonURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "(addon)"
	}
	p := strings.Trim(u.Path, "/")
	p = strings.TrimSuffix(p, "manifest.json")
	p = strings.Trim(p, "/")
	if p == "" {
		return u.Scheme + "://" + u.Host + "/manifest.json"
	}
	return u.Scheme + "://" + u.Host + "/…/manifest.json"
}

// addonIsConfigured reports whether the URL carries a configuration segment,
// which is what a UI needs to warn "this link contains your key" before
// somebody pastes it into a forum.
func addonIsConfigured(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	p := strings.Trim(u.Path, "/")
	p = strings.TrimSuffix(p, "manifest.json")
	return strings.Trim(p, "/") != ""
}

// --- views -----------------------------------------------------------------

// addonView is what a client renders.
//
// Domains, roles and capabilities are the same vocabulary /api/providers uses,
// so a settings screen can show an addon and a Radarr in one list without a
// second renderer.
type addonView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Logo        string `json:"logo,omitempty"`

	// URL is only ever sent to a session; see the gate note at the top.
	URL        string `json:"url,omitempty"`
	SafeURL    string `json:"safeUrl"`
	Configured bool   `json:"configured"`

	Enabled bool  `json:"enabled"`
	Order   int   `json:"order"`
	AddedAt int64 `json:"addedAt,omitempty"`

	Domains      []string `json:"domains"`
	Roles        []string `json:"roles"`
	Capabilities []string `json:"capabilities"`
	Resources    []string `json:"resources"`
	Catalogs     int      `json:"catalogs"`
	Searchable   bool     `json:"searchable"`

	// UnsupportedTypes names content types this addon serves that Yarr.It has
	// nowhere to put. Reported rather than dropped silently: an addon whose
	// items simply never appear looks identical to a broken one.
	UnsupportedTypes []string `json:"unsupportedTypes,omitempty"`

	// AdultContent and P2P are the protocol's own warnings and are carried
	// through rather than discarded. p2p in particular means a viewer's IP is
	// exposed to a swarm, which is a thing to be told before playing, not
	// after.
	AdultContent bool `json:"adultContent,omitempty"`
	P2P          bool `json:"p2p,omitempty"`

	Health Health `json:"health"`
}

func viewOf(it installedAddon, order int) addonView {
	m := it.Manifest
	v := addonView{
		ID:         it.AddonID,
		Enabled:    it.Enabled,
		Order:      order,
		AddedAt:    it.AddedAt,
		SafeURL:    redactAddonURL(it.URL),
		Configured: addonIsConfigured(it.URL),
	}
	if m == nil {
		v.Name = it.AddonID
		v.Health = Health{State: HealthIncompatible, Detail: "This addon's manifest was never read."}
		return v
	}
	v.Name = m.Name
	v.Version = m.Version
	v.Description = m.Description
	v.Logo = m.Logo
	v.Domains = addonDomains(m)
	v.Roles = addonRoles(m)
	v.Capabilities = addonCapabilities(m)
	v.Catalogs = len(m.Catalogs)
	v.Searchable = addonSupportsSearch(m)
	v.UnsupportedTypes = unmappedTypes(m)
	v.AdultContent = m.BehaviorHints.Adult
	v.P2P = m.BehaviorHints.P2P
	for _, r := range m.Resources {
		if r.Name != "" {
			v.Resources = append(v.Resources, r.Name)
		}
	}
	sort.Strings(v.Resources)
	if v.Domains == nil {
		v.Domains = []string{}
	}
	if v.Roles == nil {
		v.Roles = []string{}
	}
	return v
}

// --- the API ---------------------------------------------------------------

type addonAPI struct {
	store  *addonStore
	client *AddonClient
	// reg is optional. When present, installed addons are registered as
	// Providers so they appear in /api/providers beside Radarr and Jellyfin
	// with no further wiring.
	reg *Registry
}

func newAddonAPI(store *addonStore, client *AddonClient, reg *Registry) *addonAPI {
	return &addonAPI{store: store, client: client, reg: reg}
}

// handleList reports every installed addon and its health.
//
// Health is probed concurrently and each probe is bounded, exactly as
// /api/providers does it. Serially, five dead addons would take five timeouts
// to paint the one page from which they could be deleted.
func (a *addonAPI) handleList(w http.ResponseWriter, r *http.Request, _ string) {
	items := a.store.List()
	views := make([]addonView, len(items))

	var wg sync.WaitGroup
	for i, it := range items {
		views[i] = viewOf(it, i)
		views[i].URL = it.URL // session-gated route; the owner may copy it back
		wg.Add(1)
		go func(i int, it installedAddon) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					// A panic inside one addon's probe must not take down the
					// endpoint that reports on all the others.
					views[i].Health = Health{
						State:  HealthDegraded,
						Detail: "Checking this addon failed unexpectedly.",
					}
				}
			}()
			if !it.Enabled {
				// A disabled addon is not probed at all. Probing it would
				// spend a timeout on something the user has already said they
				// do not want, and report it unhealthy for not answering a
				// question nobody asked.
				views[i].Health = Health{State: HealthNotConfigured, Detail: "Turned off."}
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), addonProbeTimeout)
			defer cancel()
			p := newAddonProvider(a.client, it.URL, it.Manifest)
			views[i].Health = p.Health(ctx)
		}(i, it)
	}
	wg.Wait()

	writeJSON(w, 200, map[string]any{
		"addons": views,
		// Which domains the enabled addons can actually answer for, so a client
		// shows only the tabs that will return something.
		"domains": a.enabledDomains(),
		"limits": map[string]any{
			"maxInstalled":       addonMaxInstalled,
			"manifestMaxBytes":   addonManifestMaxBytes,
			"responseMaxBytes":   addonResourceMaxBytes,
			"manifestTimeoutSec": int(addonManifestTimeout / time.Second),
		},
	})
}

func (a *addonAPI) enabledDomains() []string {
	seen := map[string]bool{}
	for _, it := range a.store.List() {
		if !it.Enabled || it.Manifest == nil {
			continue
		}
		for _, d := range addonDomains(it.Manifest) {
			seen[d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// handleAdd installs an addon from a pasted URL.
//
// This is the route that turns user input into an outbound request from this
// server, so it is the one the whole policy in addon.go exists for. It needs a
// session, the URL is checked before a packet leaves, and the address that is
// finally dialled is checked again at dial time.
func (a *addonAPI) handleAdd(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "use POST"})
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "Send a JSON body with a url."})
		return
	}
	raw := strings.TrimSpace(body.URL)
	if raw == "" {
		writeJSON(w, 400, map[string]string{"error": "No address given."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), addonManifestTimeout+2*time.Second)
	defer cancel()

	m, err := a.client.FetchManifest(ctx, raw)
	if err != nil {
		// The reason is shown, because the person who pasted the URL is the
		// person who can fix it, and "could not add addon" tells them nothing.
		// The URL itself is never echoed back into a log.
		log.Printf("addon install refused for %s: %v", redactAddonURL(raw), err)
		status := 400
		var ue *addonURLError
		if errors.As(err, &ue) {
			status = 400
		} else if errors.Is(err, ErrAddonInvalid) {
			status = 422
		} else {
			var he *addonHTTPError
			if errors.As(err, &he) {
				status = 502
			}
		}
		writeJSON(w, status, map[string]string{"error": addonUserMessage(err)})
		return
	}

	it := installedAddon{
		URL:      addonManifestURL(raw),
		AddonID:  m.ID,
		Manifest: m,
		Enabled:  true,
		AddedAt:  time.Now().Unix(),
	}
	if err := a.store.Add(it); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	a.syncRegistry()

	v := viewOf(it, len(a.store.List())-1)
	v.URL = it.URL
	v.Health = Health{State: HealthOK, Version: m.Version}
	if un := unmappedTypes(m); len(un) > 0 {
		v.Health = Health{
			State:   HealthDegraded,
			Version: m.Version,
			Detail:  "Installed, but these content types are not supported here: " + strings.Join(un, ", ") + ".",
		}
	}
	writeJSON(w, 200, map[string]any{"addon": v})
}

// addonUserMessage turns any failure into a sentence for the person reading it,
// following Health.Detail's rule: say what to do, not what went wrong.
func addonUserMessage(err error) string {
	var ue *addonURLError
	if errors.As(err, &ue) {
		return ue.Detail
	}
	var he *addonHTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusNotFound:
			return "There is no addon manifest at that address. Check the URL ends in /manifest.json."
		case http.StatusUnauthorized, http.StatusForbidden:
			return "That addon refused the request. If it needs configuring, install it from its own site first and paste the configured link."
		default:
			return fmt.Sprintf("That address answered %d.", he.Status)
		}
	}
	if errors.Is(err, ErrAddonInvalid) {
		return "That address answered, but not with an addon manifest."
	}
	return "Could not add that addon."
}

func (a *addonAPI) handleRemove(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeJSON(w, 405, map[string]string{"error": "use POST or DELETE"})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body)
		id = strings.TrimSpace(body.ID)
	}
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "No addon id given."})
		return
	}
	if !a.store.Remove(id) {
		writeJSON(w, 404, map[string]string{"error": "No such addon."})
		return
	}
	a.syncRegistry()
	writeJSON(w, 200, map[string]any{"removed": id})
}

func (a *addonAPI) handleEnabled(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "use POST"})
		return
	}
	var body struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "Send a JSON body with an id and enabled."})
		return
	}
	if strings.TrimSpace(body.ID) == "" || body.Enabled == nil {
		writeJSON(w, 400, map[string]string{"error": "Send both id and enabled."})
		return
	}
	if !a.store.SetEnabled(body.ID, *body.Enabled) {
		writeJSON(w, 404, map[string]string{"error": "No such addon."})
		return
	}
	a.syncRegistry()
	writeJSON(w, 200, map[string]any{"id": body.ID, "enabled": *body.Enabled})
}

func (a *addonAPI) handleOrder(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "use POST"})
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "Send a JSON body with ids."})
		return
	}
	a.store.Reorder(body.IDs)
	a.syncRegistry()

	items := a.store.List()
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.AddonID
	}
	writeJSON(w, 200, map[string]any{"order": out})
}

// --- querying the installed set --------------------------------------------

// addonSearchResult carries the items and, just as importantly, who did not
// answer.
//
// Failed is not decoration. Without it a short list and a broken addon look
// identical, and "there are no results" is the single most misleading thing a
// search can say when the truth is "we could not ask".
type addonSearchResult struct {
	Items  []MediaItem `json:"items"`
	Failed []addonFail `json:"failed,omitempty"`
}

type addonFail struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// handleSearch fans a query across every enabled addon that declares it can
// search, bounded per addon.
func (a *addonAPI) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 400, map[string]string{"error": "No query given."})
		return
	}
	domain := canonicalDomain(r.URL.Query().Get("domain"))

	var (
		mu     sync.Mutex
		items  []MediaItem
		failed []addonFail
		wg     sync.WaitGroup
	)

	for _, it := range a.store.List() {
		if !it.Enabled || it.Manifest == nil {
			continue
		}
		if !addonSupportsSearch(it.Manifest) {
			continue // Asked, not assumed.
		}
		if domain != "" && len(searchableCatalogs(it.Manifest, domain)) == 0 {
			continue
		}
		wg.Add(1)
		go func(it installedAddon) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					mu.Lock()
					failed = append(failed, addonFail{ID: it.AddonID, Name: it.Manifest.Name, Detail: "This addon failed unexpectedly."})
					mu.Unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(r.Context(), addonResourceTimeout)
			defer cancel()

			p := newAddonProvider(a.client, it.URL, it.Manifest)
			got, err := p.Search(ctx, q, domain)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, addonFail{ID: it.AddonID, Name: it.Manifest.Name, Detail: addonUserMessage(err)})
				return
			}
			items = append(items, got...)
		}(it)
	}
	wg.Wait()

	// Installed order is the tiebreaker, because it is the only preference the
	// user has actually expressed. Sorting by relevance would need a score no
	// addon supplies, and inventing one silently reranks somebody's choices.
	rank := map[string]int{}
	for i, it := range a.store.List() {
		rank["addon:"+it.AddonID] = i
	}
	sort.SliceStable(items, func(x, y int) bool {
		return rank[items[x].ProviderID] < rank[items[y].ProviderID]
	})
	sort.Slice(failed, func(x, y int) bool { return failed[x].ID < failed[y].ID })

	if items == nil {
		items = []MediaItem{}
	}
	writeJSON(w, 200, addonSearchResult{Items: items, Failed: failed})
}

// handleMeta returns the details for one canonical addon id.
func (a *addonAPI) handleMeta(w http.ResponseWriter, r *http.Request) {
	ref, it, ok := a.resolve(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), addonResourceTimeout)
	defer cancel()

	m, err := a.client.Meta(ctx, it.URL, ref.Type, ref.ID)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": addonUserMessage(err)})
		return
	}
	if m.Type == "" {
		m.Type = ref.Type
	}
	writeJSON(w, 200, map[string]any{
		"item": m.toMediaItem("addon:"+it.AddonID, it.AddonID),
		"meta": m,
	})
}

// handleStream returns the playable options for one canonical addon id.
//
// Session-gated: a configured debrid addon answers with links bound to the
// household's paid account.
func (a *addonAPI) handleStream(w http.ResponseWriter, r *http.Request, _ string) {
	ref, it, ok := a.resolve(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), addonResourceTimeout)
	defer cancel()

	streams, err := a.client.Streams(ctx, it.URL, ref.Type, ref.ID)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": addonUserMessage(err)})
		return
	}
	if streams == nil {
		streams = []AddonStream{}
	}
	writeJSON(w, 200, map[string]any{
		"streams": streams,
		// The protocol's own P2P warning, carried to the point of decision
		// rather than left on a settings screen nobody rereads.
		"p2p": it.Manifest != nil && it.Manifest.BehaviorHints.P2P,
	})
}

func (a *addonAPI) handleSubtitles(w http.ResponseWriter, r *http.Request, _ string) {
	ref, it, ok := a.resolve(w, r)
	if !ok {
		return
	}
	extra := url.Values{}
	for _, k := range []string{"videoHash", "videoSize", "filename"} {
		if v := strings.TrimSpace(r.URL.Query().Get(k)); v != "" {
			extra.Set(k, v)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), addonResourceTimeout)
	defer cancel()

	subs, err := a.client.Subtitles(ctx, it.URL, ref.Type, ref.ID, extra)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": addonUserMessage(err)})
		return
	}
	if subs == nil {
		subs = []AddonSubtitle{}
	}
	writeJSON(w, 200, map[string]any{"subtitles": subs})
}

// resolve turns ?id=addon:<addon>:<type>:<id> into the installed addon that
// can answer for it.
func (a *addonAPI) resolve(w http.ResponseWriter, r *http.Request) (addonRef, installedAddon, bool) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "No id given."})
		return addonRef{}, installedAddon{}, false
	}
	ref, err := parseAddonRef(id)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "That is not an addon item id."})
		return addonRef{}, installedAddon{}, false
	}
	it, ok := a.store.Get(ref.AddonID)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "That addon is not installed here."})
		return addonRef{}, installedAddon{}, false
	}
	if !it.Enabled {
		writeJSON(w, 409, map[string]string{"error": "That addon is turned off."})
		return addonRef{}, installedAddon{}, false
	}
	return ref, it, true
}

// syncRegistry keeps the shared provider Registry matching the enabled set, so
// an addon shows up in /api/providers and in every domain/role query with no
// code in those places knowing addons exist.
func (a *addonAPI) syncRegistry() {
	if a.reg == nil {
		return
	}
	a.reg.ReplaceAddons(a.addonProviders())
}

// ReplaceAddons swaps every addon-backed provider in the registry for a new
// set, leaving Radarr, Jellyfin and the rest exactly where they were.
//
// Defined here rather than in provider.go because it is an addon concern:
// nothing else in the registry is ever replaced wholesale, and giving the
// registry a general Remove would invite it. The "addon:" prefix is the marker,
// and addonProvider.ID() is the only thing that mints it.
func (r *Registry) ReplaceAddons(ps []Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	kept := r.providers[:0]
	for _, p := range r.providers {
		if !strings.HasPrefix(p.ID(), "addon:") {
			kept = append(kept, p)
		}
	}
	r.providers = kept

	seen := map[string]bool{}
	for _, p := range ps {
		if p.ID() == "" || seen[p.ID()] {
			continue
		}
		// Domains are validated the same way Add does it. An addon serving a
		// type this build cannot place must not enter the registry claiming a
		// domain nothing will ever query.
		ok := true
		for _, d := range p.Domains() {
			if canonicalDomain(d) != d {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		seen[p.ID()] = true
		r.providers = append(r.providers, p)
	}
}

func (a *addonAPI) addonProviders() []Provider {
	var out []Provider
	for _, it := range a.store.List() {
		if !it.Enabled || it.Manifest == nil {
			continue
		}
		out = append(out, newAddonProvider(a.client, it.URL, it.Manifest))
	}
	return out
}

// --- wiring ----------------------------------------------------------------

var (
	addonOnce       sync.Once
	addonDefaultAPI *addonAPI
)

// DefaultAddonAPI is the process-wide addon surface, built from the
// environment. ADDON_STATE_PATH names the file the installed list lives in;
// unset means addons last until restart.
func DefaultAddonAPI() *addonAPI {
	addonOnce.Do(func() {
		store, err := newAddonStore(strings.TrimSpace(os.Getenv("ADDON_STATE_PATH")))
		if err != nil {
			log.Printf("addons: %v", err)
		}
		if os.Getenv("ADDON_STATE_PATH") == "" {
			log.Printf("ADDON_STATE_PATH not set; installed addons are in-memory only")
		}
		addonDefaultAPI = newAddonAPI(store, NewAddonClient(), nil)
	})
	return addonDefaultAPI
}

// registerAddonRoutes wires the addon endpoints onto a mux.
//
// It takes only the mux so main.go can call it in one line, and unlike the
// other one-line registrars in this package it applies its own gates rather
// than registering bare. That is deliberate: routes_test.go documents what
// happened the last time a convenience helper registered five handlers
// unwrapped, and the add route here is the one that makes this server fetch a
// URL a stranger could otherwise supply. A helper that leaves it open is a
// helper that hands out an SSRF probe.
//
// PREFER registerAddonRoutesWith when the caller already has an authConfig and
// a Registry. This function calls loadAuthConfig() itself, and when
// SESSION_SECRET is unset that mints a second random signing key -- cookies
// issued under main.go's config would then be rejected here. In production
// SESSION_SECRET is always set, so this is a development footgun rather than a
// deployment one, but the two-argument form has neither problem and also puts
// addons into /api/providers.
func registerAddonRoutes(mux *http.ServeMux) {
	auth := loadAuthConfig()
	if auth.Enabled && os.Getenv("SESSION_SECRET") == "" {
		log.Printf("addons: SESSION_SECRET is unset, so addon routes sign sessions with a different key than the rest of the server; wire registerAddonRoutesWith(mux, auth, registry) instead")
	}
	registerAddonRoutesWith(mux, auth, nil)
}

// registerAddonRoutesWith is the same wiring against a caller's auth config and
// provider registry.
func registerAddonRoutesWith(mux *http.ServeMux, auth *authConfig, reg *Registry) {
	api := DefaultAddonAPI()
	api.reg = reg
	api.syncRegistry()

	// Household configuration. Every one of these needs a session: three of
	// them change what this instance runs, and the fourth returns URLs that
	// can carry a debrid key.
	mux.HandleFunc("/api/addons", auth.requireUser(func(w http.ResponseWriter, r *http.Request, u string) {
		switch r.Method {
		case http.MethodGet:
			api.handleList(w, r, u)
		case http.MethodPost:
			api.handleAdd(w, r, u)
		case http.MethodDelete:
			api.handleRemove(w, r, u)
		default:
			writeJSON(w, 405, map[string]string{"error": "use GET, POST or DELETE"})
		}
	}))
	mux.HandleFunc("/api/addons/remove", auth.requireUser(api.handleRemove))
	mux.HandleFunc("/api/addons/enabled", auth.requireUser(api.handleEnabled))
	mux.HandleFunc("/api/addons/order", auth.requireUser(api.handleOrder))

	// Catalogue data. Open to any origin for the same reason /api/search is: a
	// television is a different origin, and these describe things that exist in
	// the world rather than anything this household owns.
	mux.HandleFunc("/api/addons/search", publicCORS(api.handleSearch))
	mux.HandleFunc("/api/addons/meta", publicCORS(api.handleMeta))

	// Playback links. Session-gated, and deliberately NOT CORS-open: a
	// configured debrid addon answers these with links bound to a paid account.
	mux.HandleFunc("/api/addons/stream", auth.requireUser(api.handleStream))
	mux.HandleFunc("/api/addons/subtitles", auth.requireUser(api.handleSubtitles))
}
