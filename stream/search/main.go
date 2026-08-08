// mw-search fronts Prowlarr for the browser.
//
// It exists for three reasons the raw Prowlarr API cannot serve:
//
//  1. Prowlarr lives on the home LAN, reachable only across the WireGuard
//     tunnel. Only search metadata crosses it -- never media bytes -- so the
//     home IP is never in the streaming path.
//  2. A query fans out to ~28 indexers and takes 8 seconds. Caching makes the
//     site feel instant and keeps that fan-out rare.
//  3. Prowlarr returns flat scene filenames. Grouping them into title cards is
//     what makes this feel like a library instead of a directory listing.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type prowlarrResult struct {
	Title       string `json:"title"`
	Indexer     string `json:"indexer"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	Leechers    int    `json:"leechers"`
	Protocol    string `json:"protocol"`
	PublishDate string `json:"publishDate"`
	MagnetURL   string `json:"magnetUrl"`
	DownloadURL string `json:"downloadUrl"`
	InfoURL     string `json:"infoUrl"`
	InfoHash    string `json:"infoHash"`
	GUID        string `json:"guid"`
	Categories  []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"categories"`
}

// source is one torrent -- one row in a title card's quality picker.
type source struct {
	Title     string `json:"title"`
	Indexer   string `json:"indexer"`
	Size      int64  `json:"size"`
	SizeHuman string `json:"sizeHuman"`
	Seeders   int    `json:"seeders"`
	Leechers  int    `json:"leechers"`
	Magnet    string `json:"magnet"`
	Quality   string `json:"quality"`
	Source    string `json:"source"`
	Codec     string `json:"codec"`
	Audio     string `json:"audio"`
	Group     string `json:"group"`
	WebSafe   bool   `json:"webSafe"`
	Published string `json:"published"`
}

// card is a single work (a film, or one episode) with every torrent for it.
type card struct {
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Year     int      `json:"year"`
	IsSeries bool     `json:"isSeries"`
	Season   int      `json:"season,omitempty"`
	Episode  int      `json:"episode,omitempty"`
	Kind     string   `json:"kind"` // video | audio | image | game | other
	Sources  []source `json:"sources"`
	Best     int      `json:"best"`    // index into Sources
	Seeders  int      `json:"seeders"` // max across sources
	Art      artwork  `json:"art"`
	Groups   []string `json:"groups"`
	Adult    bool     `json:"adult"`

	// Instant marks a result that is served over HTTP by a host that is always
	// up, rather than by whoever happens to be seeding. It has no seeder count
	// because the question does not apply -- it will play.
	Instant bool `json:"instant,omitempty"`
	// Popular is the host's own demand signal (archive.org download count),
	// used where seeders would be for a torrent.
	Popular int `json:"popular,omitempty"`
	// Platform is the console or system, for game results.
	Platform string `json:"platform,omitempty"`

	// owner marks a card that came from the household's own media servers
	// rather than from a public source, and so must never be shown to anybody
	// but the owner.
	//
	// Unexported on purpose, twice over: encoding/json cannot serialise it, so
	// it can never reach a client even by accident; and it cannot be set by
	// decoding a body, so no caller can launder a public card into an owner one
	// or the reverse. Its only job is to be read by dropOwnerCards at the two
	// points a card becomes shared state. See owner.go.
	owner bool
}

type cacheEntry struct {
	cards   []card
	expires time.Time
}

type server struct {
	prowlarrURL string
	apiKey      string
	ttl         time.Duration

	mu    sync.RWMutex
	cache map[string]cacheEntry

	// inflight collapses concurrent identical calls to one upstream source into
	// a single request. Without it, a keystroke's type-ahead and the search it
	// precedes both ask archive.org the same question at the same moment.
	flightMu sync.Mutex
	inflight map[string]chan struct{}

	// Searches still running after their response was sent, collectable by id.
	// This is what lets a search answer in 300ms and still deliver everything a
	// 30-second indexer fan-out finds.
	jobs jobStore

	// Trips when Prowlarr is failing, so a stalled backend costs one search its
	// budget rather than every search for as long as it is down.
	indexerBreaker breaker

	// Fast/slow indexer split, so it is not recomputed per search.
	ixMu      sync.Mutex
	ixCache   indexerTiers
	ixExpires time.Time

	// Test hook, fired when a background slow-tier pass finishes.
	onSlowTierDone func()

	// Per-user watchlist and resume points.
	library *libraryStore

	tmdb     *tmdbClient
	igdb     *igdbClient
	discover discoverCache
	warm     *warmer

	// Configured backends -- Radarr, Sonarr, ROMarr, Jellyfin, a linear-TV
	// engine. Nil is a valid state and means none are configured yet, which is
	// how a fresh self-host starts; every reader must handle it rather than
	// assume at least one exists.
	providers *Registry

	// How this server answers "is the caller the owner". Nil is a valid state
	// and means nobody is -- see authConfig.isOwner, which answers false on a
	// nil receiver, so a server built by a test behaves like an instance with
	// no owner rather than one where everybody is.
	auth *authConfig
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8802", "listen address")
	prowlarr := flag.String("prowlarr", "http://192.168.0.115:9696", "Prowlarr base URL (over the tunnel)")
	ttl := flag.Duration("ttl", defaultTTL, "cache TTL for search results")
	flag.Parse()

	// Deliberately not fatal. Torrent search needs Prowlarr, but the catalogue,
	// archive.org, the watchlist and resume points do not -- and refusing to
	// start meant a self-hoster who had not reached the Prowlarr step yet got a
	// process that died instantly, with the reason only in a log they had no
	// reason to look at. Starting degraded is honest and inspectable: /api/health
	// reports it, and a search says what is missing instead of timing out.
	key := os.Getenv("PROWLARR_API_KEY")
	if key == "" {
		log.Printf("PROWLARR_API_KEY not set; torrent search is disabled " +
			"(catalogue, archive.org, watchlist and resume still work)")
	}

	s := &server{
		// Always non-nil. The endpoints tolerate a nil registry, but a nil one
		// here would silently mean no provider could ever be added.
		providers:   &Registry{},
		prowlarrURL: strings.TrimRight(*prowlarr, "/"),
		apiKey:      key,
		ttl:         *ttl,
		cache:       make(map[string]cacheEntry),
		inflight:    make(map[string]chan struct{}),
		tmdb:        newTMDB(os.Getenv("TMDB_API_KEY")),
		// Reuses the RomM installation's IGDB credentials. That is the part of
		// RomM that describes games in general; its own API is library-bound
		// and would only describe this installation's shelf.
		igdb: newIGDB(os.Getenv("IGDB_CLIENT_ID"), os.Getenv("IGDB_CLIENT_SECRET")),
	}
	lib, err := newLibraryStore(os.Getenv("LIBRARY_PATH"))
	if err != nil {
		// A library that cannot be read is a data problem, not a reason to
		// serve a broken one.
		log.Fatalf("library: %v", err)
	}
	s.library = lib
	if os.Getenv("LIBRARY_PATH") == "" {
		log.Printf("LIBRARY_PATH not set; watchlist and resume are in-memory only")
	}
	go lib.flushLoop()

	s.warm = newWarmer(s)
	// archive.org answers the first paint of every search, so its connection is
	// opened before the first person needs it rather than during their search.
	go warmArchiveConnection()
	go s.evictLoop()
	// Pre-search the titles on the landing rails so the common path --
	// browse trending, click a poster -- hits cache instead of a 14s fan-out.
	go s.warm.run()

	mux := http.NewServeMux()
	auth := loadAuthConfig()
	// Every owner check on this server reads this one config. Assigned before
	// any route is registered so no handler can be wired against a nil one.
	s.auth = auth
	logOwnerPolicy(auth)
	if auth.Enabled {
		switch auth.Scope {
		case scopeAll:
			log.Printf("sign-in required for everyone (client %s…)", auth.ClientID[:8])
		case scopeOff:
			log.Printf("sign-in configured but switched off (AUTH_SCOPE=off)")
		default:
			log.Printf("sign-in required for TV clients only (client %s…)", auth.ClientID[:8])
		}
	} else {
		log.Printf("sign-in NOT configured; the site is open")
	}

	// Sign-in endpoints are deliberately outside the gate: a person who cannot
	// reach the login page cannot ever get through it.
	mux.HandleFunc("/auth/login", auth.handleLogin)
	mux.HandleFunc("/auth/callback", auth.handleCallback)
	mux.HandleFunc("/auth/logout", auth.handleLogout)
	mux.HandleFunc("/auth/verify", auth.handleVerify)
	mux.HandleFunc("/auth/me", auth.handleMe)

	// The data is what actually needs protecting. Gating it here means the API
	// is safe even if the edge is ever misconfigured -- the check does not
	// depend on Caddy getting its forward_auth right.
	// Catalogue endpoints are open to any origin, so a client can point at any
	// instance. Personal endpoints below deliberately are not.
	mux.HandleFunc("/api/search", publicCORS(auth.requireAuth(s.handleSearch)))
	// Collecting the rest of a search that has already answered. A separate
	// path as well as the `job=` parameter above, because a television's HTTP
	// client is easier to point at a URL than to teach a query-string protocol
	// -- and both reach the same handler, so they cannot drift.
	mux.HandleFunc("/api/search/updates", publicCORS(auth.requireAuth(s.handleSearch)))
	// Type-ahead. Answers from memory and archive.org only -- never from an
	// indexer -- because nothing that asks an indexer can keep up with typing.
	mux.HandleFunc("/api/suggest", publicCORS(auth.requireAuth(s.handleSuggest)))
	mux.HandleFunc("/api/discover", publicCORS(auth.requireAuth(s.handleDiscover)))
	// Turns an archive.org item into displayable images. A television cannot
	// render a PDF or follow a details page, so this is what makes comics and
	// photo sets work there at all.
	mux.HandleFunc("/api/pages", publicCORS(auth.requireAuth(s.handlePages)))
	// Health stays open so a monitor does not need a session to see the
	// service is alive.
	// Health is how a client confirms an address is a Yarr.It server at all,
	// so it must answer before any origin is trusted.
	mux.HandleFunc("/api/health", publicCORS(s.handleHealth))
	// Public, and CORS-open on purpose: a Roku or a Tizen set cannot embed this
	// file at build time, and hand-copying it into each client is precisely how
	// the vocabulary drifted last time.
	mux.HandleFunc("/api/schema", publicCORS(s.handleSchema))
	// What is configured and whether it is well. Still open, and still the same
	// shape for everyone, but the list itself is now filtered by audience --
	// naming a household's Radarr, Plex and ROMarr to a stranger says what it
	// runs and, by implication, what it holds. A non-owner sees an empty list
	// whether or not anything is configured, so the emptiness carries no signal.
	mux.HandleFunc("/api/providers", publicCORS(s.handleProviders))
	// What every backend is currently doing, merged. A download queue is a list
	// of titles this household is acquiring, which is as personal as the
	// library itself.
	mux.HandleFunc("/api/activity", auth.requireOwnerUser(s.handleActivity))

	// Radarr / Sonarr / Lidarr, one entry per configured instance. A missing
	// instance is not an error: a self-hoster who runs none of these still gets
	// a working search, and one that will not register must not stop the rest.
	for _, p := range arrProvidersFromEnv() {
		if err := s.providers.Add(p); err != nil {
			log.Printf("provider %s: %v", p.ID(), err)
		}
	}

	// Registered individually rather than through registerArrRoutes, and now
	// all five behind the owner boundary rather than two of them open.
	//
	// Search and details used to be public on the grounds that they "describe
	// things that exist in the world". That was true of the words and false of
	// the response: every MediaItem carries a State, which is this household's
	// answer to "do I have this", plus the ProviderID that says which of its
	// instances answered. A stranger could walk a film list through
	// /api/arr/search and read the shelf a title at a time without ever
	// touching /api/arr/library. Filtering those fields per-provider was the
	// alternative and it is the kind of subtlety that survives exactly until
	// the next adapter is added, so the boundary is drawn at the route.
	//
	// What a non-owner loses is a catalogue lookup they can still get from
	// /api/search, which reaches archive.org and the indexers and knows nothing
	// about this household.
	arr := &arrAPI{reg: s.providers}
	mux.HandleFunc("/api/arr/search", auth.requireOwner(arr.handleSearch))
	mux.HandleFunc("/api/arr/details", auth.requireOwner(arr.handleDetails))
	mux.HandleFunc("/api/arr/library", auth.requireOwner(arr.handleLibrary))
	mux.HandleFunc("/api/arr/status", auth.requireOwner(arr.handleStatus))
	// Requesting also spends the owner's disk and bandwidth, which is a second
	// reason on top of the first.
	mux.HandleFunc("/api/arr/request", auth.requireOwner(arr.handleRequest))

	// Whether an archive.org item can actually be played here, and how. Public
	// and CORS-open: it describes a public item and holds nothing personal.
	registerPlayRoutes(mux, auth)

	// Stremio-protocol addons. Deliberately the ...With form rather than the
	// one-line registerAddonRoutes: that one calls loadAuthConfig() again, which
	// mints a SECOND signing key when SESSION_SECRET is unset, and it cannot put
	// addons into the registry -- so they would be invisible to /api/providers
	// and to every For(domain, role) lookup.
	registerAddonRoutesWith(mux, auth, s.providers)

	// ROMarr and RomM. Same split as the *arr routes and for the same reason:
	// the convenience helper registers all five bare.
	for _, p := range gameProvidersFromEnv() {
		if rp, ok := p.(*rommProvider); ok {
			// Play is earned, not assumed -- confirm the emulator loader really
			// serves before advertising that anything can be played.
			pctx, pcancel := context.WithTimeout(context.Background(), 4*time.Second)
			rp.verifyPlay(pctx)
			pcancel()
		}
		if err := s.providers.Add(p); err != nil {
			log.Printf("provider %s: %v", p.ID(), err)
		}
	}
	// Same split as the *arr routes and for the same reason, with one that is
	// worse: RomM's search IS this household's shelf rather than a catalogue
	// lookup that happens to mention it, and its details route hands back a
	// playUrl that launches the owner's own ROM.
	game := &gameAPI{reg: s.providers}
	mux.HandleFunc("/api/game/search", auth.requireOwner(game.handleSearch))
	mux.HandleFunc("/api/game/details", auth.requireOwner(game.handleDetails))
	mux.HandleFunc("/api/game/library", auth.requireOwner(game.handleLibrary))
	mux.HandleFunc("/api/game/status", auth.requireOwner(game.handleStatus))
	mux.HandleFunc("/api/game/request", auth.requireOwner(game.handleRequest))

	// Live TV. Gated by what each route actually is rather than by prefix: a
	// guide is catalogue data that a television reads cross-origin, while
	// creating or editing a channel is administration. Registering the whole
	// prefix one way would either lock TVs out of the guide or leave channel
	// editing open.
	linear := LinearDefaultEngine()
	// Media servers feed the linear engine its programmes and resolve them to
	// something playable. Constructed even when discovery fails, so an instance
	// that is merely restarting reports an honest health state instead of
	// disappearing from the settings screen.
	var resolvers []LinearResolver
	lctx, lcancel := context.WithTimeout(context.Background(), 20*time.Second)
	if u := os.Getenv("JELLYFIN_URL"); u != "" {
		p, libs, res, err := newJellyfinLinear(lctx, jellyfinConfig{
			ID: "jellyfin", Name: "Jellyfin", BaseURL: u,
			APIKey: os.Getenv("JELLYFIN_API_KEY"), UserID: os.Getenv("JELLYFIN_USER_ID"),
		})
		if err != nil {
			log.Printf("jellyfin: %v", err)
		}
		if p != nil {
			if e := s.providers.Add(p); e != nil {
				log.Printf("jellyfin: %v", e)
			}
		}
		for _, l := range libs {
			linear.AddLibrary(l)
		}
		if res != nil {
			resolvers = append(resolvers, res)
		}
	}
	if u := os.Getenv("PLEX_URL"); u != "" {
		p, libs, res, err := newPlexLinear(lctx, plexConfig{
			ID: "plex", Name: "Plex", BaseURL: u, Token: os.Getenv("PLEX_TOKEN"),
		})
		if err != nil {
			log.Printf("plex: %v", err)
		}
		if p != nil {
			if e := s.providers.Add(p); e != nil {
				log.Printf("plex: %v", e)
			}
		}
		for _, l := range libs {
			linear.AddLibrary(l)
		}
		if res != nil {
			resolvers = append(resolvers, res)
		}
	}
	lcancel()
	if len(resolvers) > 0 {
		// One engine, every configured server: a channel may schedule from
		// Jellyfin and Plex at once, and the chain finds whichever owns an item.
		linear.SetResolver(newLinearResolverChain(resolvers...))
	}
	// Every linear route is owner-only, including the read-only ones that were
	// public a moment ago.
	//
	// This is the leak the endpoint list would not have caught. A linear channel
	// is assembled BY SCHEDULING THE OWNER'S JELLYFIN AND PLEX LIBRARIES: the
	// guide is therefore a timetable of his files, /now names the one playing,
	// and /stream resolves to a URL that serves the bytes. Nothing about those
	// three routes reads as personal from its name, and all three were open to
	// every origin on the internet.
	//
	// The read/write split that used to be drawn on /channels is gone with it.
	// A GET there lists the channels and their definitions -- the library
	// sections, the filters, the titles matched -- so "reading is safe, writing
	// is administration" was the wrong axis: both halves are the owner's.
	//
	// The cost is that a television must present a credential for Live TV.
	// It already can: bearer.go exists precisely so a set-top box that finished
	// the device flow can prove who it is without a cookie jar.
	mux.HandleFunc("/api/v1/linear/guide", auth.requireOwner(linear.handleGuide))
	mux.HandleFunc("/api/v1/linear/now", auth.requireOwner(linear.handleNow))
	mux.HandleFunc("/api/v1/linear/stream", auth.requireOwner(linear.handleStream))
	mux.HandleFunc("/api/v1/linear/channels", auth.requireOwner(linear.handleChannels))
	mux.HandleFunc("/api/v1/linear/preview", auth.requireOwner(linear.handlePreview))
	if err := s.providers.Add(NewLinearProvider(linear)); err != nil {
		// Not fatal: an unregisterable linear engine means no Live TV, which is
		// a missing feature rather than a reason to refuse to serve search.
		log.Printf("linear TV: %v", err)
	}

	// The watchlist and resume points. Owner-only rather than per-signed-in-user.
	//
	// requireUser would still be defensible here -- this data is keyed on the
	// session, so each account only ever reads its own rows. It is owner-only
	// anyway for a reason that is about collection rather than access: keeping
	// it open means this service accumulates viewing history for every account
	// in the identity provider, and nobody asked for that. One owner means one
	// person's rows on disk, which is the smallest thing that satisfies what
	// was actually requested.
	//
	// Reversing this is one edit per line if a household ever wants shared
	// watchlists; the storage is already keyed per user and would need no
	// migration.
	mux.HandleFunc("/api/v1/library", auth.requireOwnerUser(s.handleLibrary))
	mux.HandleFunc("/api/v1/progress", auth.requireOwnerUser(s.handleProgress))
	mux.HandleFunc("/api/v1/continue", auth.requireOwnerUser(s.handleContinue))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      130 * time.Second,
	}
	log.Printf("mw-search listening on %s -> %s (ttl %s)", *addr, s.prowlarrURL, *ttl)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	n := len(s.cache)
	s.mu.RUnlock()

	// Whether this instance has been told who owns it. Reported here for the
	// same reason "not-configured" is reported for Prowlarr: an owner-less
	// instance serves none of its own media to anybody, which from the front
	// looks exactly like an empty library or a broken backend -- and an operator
	// who cannot tell those apart goes looking for the setting that turns the
	// gate off. Naming the state is what stops that.
	//
	// It discloses nothing worth having. In this state there is no owner
	// content being served to anyone, so "there is no owner here" is not a lead;
	// and the owner's identity is never in it, only whether one exists.
	owner := "not-configured"
	switch {
	case !s.auth.enabled():
		owner = "no-sign-in"
	case s.auth.ownerConfigured():
		owner = "configured"
	}

	// Distinguish "not configured" from "configured but unreachable". Both leave
	// search dead, but only one of them is something the operator can fix by
	// pasting in a key, and reporting an unconfigured instance as "down" sends
	// them looking for a network fault that does not exist.
	if s.apiKey == "" {
		writeJSON(w, 200, map[string]any{
			"prowlarr":      "not-configured",
			"cachedQueries": n,
			"owner":         owner,
			"detail":        "PROWLARR_API_KEY is not set; torrent search is disabled",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.prowlarrURL+"/api/v1/health", nil)
	req.Header.Set("X-Api-Key", s.apiKey)
	resp, err := http.DefaultClient.Do(req)
	status := "down"
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			status = "ok"
		}
	}
	body := map[string]any{"prowlarr": status, "cachedQueries": n, "owner": owner}
	// Whether searches are currently skipping the indexers on purpose. Without
	// this, a breaker that has tripped looks exactly like an index with nothing
	// in it -- results simply stop arriving and nothing says why.
	if s.indexerBreaker.open() {
		body["indexerBreaker"] = "open"
	}
	writeJSON(w, 200, body)
}

// searchState is everything the response says about itself beyond the cards:
// where they came from, what is still coming, and what failed.
type searchState struct {
	cache   string
	job     string
	pending bool
	sources map[string]string
	stale   bool
	partial bool
}

// respondSearch writes one search response, in the one shape every caller of
// this endpoint gets -- the first paint, a poll, and a straight cache hit.
//
// Filters are applied here rather than upstream, so changing one costs no
// indexer traffic and a poll can re-narrow a growing result set for free.
//
// ownerView says whether the caller is the owner, and is the last gate any card
// passes before it is serialised. It is belt to the braces putCached and
// searchJob.add already provide: those keep owner-visible cards out of shared
// state, this catches anything merged into a single response afterwards. Both
// are needed, because they fail in different directions -- the invariant
// upstream cannot see a per-request merge, and a check here cannot see a card
// that was cached under someone else's search.
func respondSearch(w http.ResponseWriter, q string, f filters,
	dev *deviceProfile, cards []card, st searchState, ownerView bool) {

	if !ownerView {
		cards = dropOwnerCards(cards)
	}
	visible := dev.applyDevice(cards)
	body := map[string]any{
		"query":  q,
		"cards":  f.apply(visible),
		"facets": buildFacets(visible),
		"total":  len(visible),
	}
	if dev != nil {
		body["device"] = dev.Name
		body["filteredOut"] = len(cards) - len(visible)
	}
	if st.stale {
		body["stale"] = true
	}
	if st.partial {
		body["partial"] = true
	}
	if st.job != "" {
		body["job"] = st.job
	}
	// Always present, never inferred from its absence. A client that has to
	// guess whether more is coming will guess wrong in exactly the case that
	// matters -- an empty first paint, where "no results" and "not yet" look
	// identical.
	body["pending"] = st.pending
	body["complete"] = !st.pending
	if st.pending {
		// What the client should wait before collecting. Advisory: the client
		// backs off on its own, and a client that ignores it is merely rude
		// rather than wrong, because a poll is a map lookup.
		body["retryMs"] = 400
	}
	if len(st.sources) > 0 {
		body["sources"] = st.sources
	}
	w.Header().Set("X-Cache", st.cache)
	if ownerView {
		// This response may carry more than the public one for the same query,
		// so it must not be stored anywhere a later caller could be served from.
		// writeJSON stamps `public, max-age=60` on everything and publicCORS
		// varies only on Origin -- neither of which mentions the credential that
		// made this answer different, so a CDN or a corporate proxy would
		// happily hand the owner's search to the next person who typed the same
		// words.
		//
		// Wrapped rather than set here, because writeJSON would overwrite a
		// header set before it and headers set after it are already on the
		// wire. privateWriter stamps at WriteHeader time, which is the only
		// moment that wins. Non-owner responses keep the public caching, which
		// is what makes the site fast for the people who send most of the
		// traffic.
		w = &privateWriter{ResponseWriter: w}
	}
	writeJSON(w, 200, body)
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	// A poll for a job an earlier request started. Handled before anything
	// else, because everything below starts work and a poll must start none.
	if id := strings.TrimSpace(r.URL.Query().Get("job")); id != "" {
		s.handleSearchCollect(w, r, id)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 128 {
		q = q[:128]
	}
	f := parseFilters(r.URL.Query())
	kind := f.Kind

	// A category with no words is a browse: "show me games". Requiring a query
	// meant the category chips could only ever narrow results that a text
	// search had already produced -- so there was no way to simply ask for a
	// category, which is the first thing anyone tries.
	// A kind with no words is a browse for the same reason a group is --
	// "show me comics". Only groups were accepted, so picking Comics or Images
	// without typing anything returned "missing q", which on a TV remote is
	// the normal way to use it.
	if q == "" && len(f.Groups) == 0 && f.Kind == "" {
		writeJSON(w, 400, map[string]string{"error": "missing q"})
		return
	}
	// A set-top box cannot fall back to software decode the way a browser can,
	// so unplayable sources are removed rather than shown and failed.
	dev := deviceProfileFor(r.URL.Query().Get("device"))
	cacheKey := searchCacheKey(q, kind)
	// Read once per request rather than per response branch: the cookie or the
	// bearer token is checked here, and every exit below uses the same answer.
	ownerView := s.auth.isOwner(r)

	// A complete answer already in memory. Nothing else can beat this, and it
	// is the state the warmer and every previous search are working towards.
	if cards, ok := s.getCached(cacheKey); ok {
		respondSearch(w, q, f, dev, cards, searchState{cache: "HIT"}, ownerView)
		return
	}

	_, wantArchive := scopeFor(kind)

	// Nothing on this instance could ever answer this: no indexer is
	// configured and the kind has no archive.org scope. Waiting does not fix
	// that, so say which of the two problems it is rather than offering a
	// retry that can only fail again.
	if s.apiKey == "" && !wantArchive {
		if stale, ok := s.getAny(cacheKey); ok {
			respondSearch(w, q, f, dev, stale, searchState{cache: "STALE", stale: true}, ownerView)
			return
		}
		writeJSON(w, 503, map[string]string{
			"error": "no torrent indexer is configured on this server — " +
				"add a Prowlarr URL and API key to enable search",
		})
		return
	}

	// Start (or join) the job that will do the slow half, then answer with
	// whatever is ready inside the first-paint budget. The response never waits
	// on the indexers: that wait is the defect this replaces.
	job, _ := s.jobs.start(s, q, kind)

	select {
	case <-job.firstWave:
	case <-time.After(firstPaintBudget):
	case <-r.Context().Done():
		return
	}

	snap := job.snapshot()

	// Cards already in memory from adjacent queries and from the warmer. This
	// is what makes a first paint carry something real when archive.org has not
	// answered yet -- a search for "mario kart" shows the matching part of a
	// cached "mario" immediately.
	cards := mergeCards(snap.cards, s.localCards(q, kind, localPaintLimit))
	if len(cards) == 0 {
		// Last resort before an empty grid: anything we ever had for exactly
		// this query, however old.
		if old, ok := s.getAny(cacheKey); ok {
			cards = old
		}
	}

	st := searchState{
		cache:   "PARTIAL",
		job:     job.id,
		pending: !snap.complete,
		sources: snap.sources,
		partial: !snap.complete,
	}
	if snap.complete {
		st.cache = "MISS"
	}
	respondSearch(w, q, f, dev, cards, st, ownerView)
}

// handleSearchCollect answers a poll: everything the job has found so far.
//
// It does no work of its own -- a lock, a copy and a filter pass. That is the
// property that makes polling affordable where an open stream would not be:
// there is no goroutine and no connection held per waiting client.
func (s *server) handleSearchCollect(w http.ResponseWriter, r *http.Request, id string) {
	f := parseFilters(r.URL.Query())
	dev := deviceProfileFor(r.URL.Query().Get("device"))

	job, ok := s.jobs.get(id)
	if !ok {
		// The job was swept, or this client has been away long enough that it
		// was. 410 rather than 404: the id was real, the results are simply no
		// longer collectable, and the client should start a fresh search rather
		// than treat it as a bad request.
		writeJSON(w, 410, map[string]any{
			"error": "that search has expired — run it again",
			"gone":  true,
		})
		return
	}

	snap := job.snapshot()
	q := job.query
	cards := mergeCards(snap.cards, s.localCards(q, job.kind, localPaintLimit))

	// A job id is a bearer of nothing: it names a query, not a person, and the
	// person collecting is often not the one who started it. So the audience is
	// decided from THIS request's credentials, never carried on the job.
	respondSearch(w, q, f, dev, cards, searchState{
		cache:   "JOB",
		job:     job.id,
		pending: !snap.complete,
		sources: snap.sources,
		partial: !snap.complete,
	}, s.auth.isOwner(r))
}

// claim returns (wait, true) for the goroutine that should do the upstream
// call, or (wait, false) for one that should wait on the leader.
func (s *server) claim(k string) (<-chan struct{}, bool) {
	s.flightMu.Lock()
	defer s.flightMu.Unlock()
	if s.inflight == nil {
		s.inflight = map[string]chan struct{}{}
	}
	if ch, ok := s.inflight[k]; ok {
		return ch, false
	}
	ch := make(chan struct{})
	s.inflight[k] = ch
	return ch, true
}

func (s *server) release(k string) {
	s.flightMu.Lock()
	defer s.flightMu.Unlock()
	if ch, ok := s.inflight[k]; ok {
		close(ch)
		delete(s.inflight, k)
	}
}

func (s *server) getCached(k string) ([]card, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.cache[k]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.cards, true
}

// getAny returns cached cards regardless of freshness. Used only when the
// upstream has failed, where stale results beat an error page.
func (s *server) getAny(k string) ([]card, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.cache[k]
	if !ok || len(e.cards) == 0 {
		return nil, false
	}
	return e.cards, true
}

func (s *server) putCached(k string, cards []card) {
	// The cache is keyed on the query and nothing else, so whatever goes in
	// here is answered to whoever asks the same question next. Owner-visible
	// cards are dropped on the way in rather than filtered on the way out:
	// filtering on the way out has to be remembered at every read, and
	// localCards already reads this map from a path that has no idea who is
	// asking. See dropOwnerCards in owner.go.
	cards = dropOwnerCards(cards)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]cacheEntry{}
	}
	ttl := s.ttl
	if ttl <= 0 {
		ttl = defaultTTL
	}
	s.cache[k] = cacheEntry{cards: cards, expires: time.Now().Add(ttl)}
}

func (s *server) evictLoop() {
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	n := 0
	for range t.C {
		// Finished searches nobody can still be collecting. Swept every minute
		// rather than every five: a job holds its whole result set, and on a
		// 1 GB box a busy hour of unswept jobs is real memory.
		s.jobs.sweep()

		n++
		if n%5 != 0 {
			continue
		}
		// Expired means "re-query", not "discard": expired entries are the
		// fallback when indexers time out. Only drop genuinely ancient ones so
		// memory stays bounded on a 1 GB box.
		cutoff := time.Now().Add(-24 * time.Hour)
		s.mu.Lock()
		for k, e := range s.cache {
			if e.expires.Before(cutoff) {
				delete(s.cache, k)
			}
		}
		s.mu.Unlock()
	}
}

// searchProwlarr answers from a per-indexer fan-out, so a slow indexer delays
// only itself. It falls back to the single aggregate call if the fan-out cannot
// start -- that path still works, it is just as slow as its slowest indexer.
// errNoIndexers means nobody configured an indexer, as distinct from an indexer
// that is configured and failing. The difference decides what the user is told:
// one is fixed by waiting, the other never is.
var errNoIndexers = errors.New("no indexer configured")

func (s *server) searchProwlarr(ctx context.Context, q, kind string) ([]card, error) {
	// Short-circuit rather than sending a request we know will be rejected. It
	// costs a round-trip per search and comes back as a bare 401, which reads
	// like a credential problem instead of an absent one.
	if s.apiKey == "" {
		return nil, errNoIndexers
	}

	cards, deferred, err := s.searchTiered(ctx, q, kind, func(full []card) {
		s.putCached(searchCacheKey(q, kind), full)
	})
	if err == nil {
		if deferred {
			log.Printf("search %q: %d cards from the fast tier, slow tier landing in cache",
				q, len(cards))
		}
		return cards, nil
	}
	log.Printf("search %q: tiering unavailable (%v), using the aggregate call", q, err)

	return s.searchProwlarrAggregate(ctx, q, kind)
}

func (s *server) searchProwlarrAggregate(ctx context.Context, q, kind string) ([]card, error) {
	// Thirty seconds, not a hundred. This is the unscoped fan-out to every
	// indexer at once and it is the slowest thing in the system; it now runs
	// behind a response that has already been sent, so a longer ceiling buys
	// nothing but a socket held open past the point anyone is collecting.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	u := fmt.Sprintf("%s/api/v1/search?query=%s&limit=200", s.prowlarrURL, url.QueryEscape(q))
	for _, c := range categoriesFor(kind) {
		u += "&categories=" + strconv.Itoa(c)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", s.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("prowlarr status %d", resp.StatusCode)
	}

	var raw []prowlarrResult
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return buildCards(raw), nil
}

// categoriesFor maps the UI's media filter onto Newznab category ids.
func categoriesFor(kind string) []int {
	// Canonicalised first so every spelling a caller might use -- "movies",
	// "audio", "music" -- lands on the same branch.
	switch canonicalDomain(kind) {
	case "video":
		return []int{2000, 5000}
	case "music":
		return []int{3000}
	case "image":
		return []int{}
	case "game":
		// 1000-1999 is console, 4050-4069 is PC games.
		return []int{1000, 4050}
	case "comic":
		return []int{7030}
	case "literature":
		// 7000-7999 minus 7030, which is comics and has its own branch.
		return []int{7000}
	default:
		return nil
	}
}

func kindOf(r prowlarrResult) string {
	for _, c := range r.Categories {
		switch {
		case c.ID >= 1000 && c.ID < 2000, c.ID >= 4050 && c.ID < 4070:
			return "game"
		case c.ID >= 2000 && c.ID < 3000, c.ID >= 5000 && c.ID < 6000:
			return "video"
		case c.ID >= 3000 && c.ID < 4000:
			return "audio"
		// 7030 is comics, which sits inside the 7000-7999 book range -- so a
		// bare "is it 7000-8000" test swallows it and every comic is filed as
		// a book. Checked first for that reason.
		case c.ID == 7030:
			return "comic"
		case c.ID >= 7000 && c.ID < 8000:
			return "book"
		}
	}
	return "other"
}

func buildCards(raw []prowlarrResult) []card {
	byKey := map[string]*card{}

	for _, r := range raw {
		// Streaming needs a swarm to join. Usenet has no peers.
		if r.Protocol != "torrent" {
			continue
		}
		magnet := usableMagnet(r)
		if magnet == "" {
			continue
		}
		p := parseRelease(r.Title)
		if p.Title == "" {
			continue
		}
		k := p.groupKey()
		c, ok := byKey[k]
		if !ok {
			c = &card{
				Key: k, Title: p.Title, Year: p.Year, IsSeries: p.IsSeries,
				Season: p.Season, Episode: p.Episode, Kind: kindOf(r),
				Groups: groupsFor(r), Adult: isAdult(r, r.Title),
			}
			byKey[k] = c
		}
		c.Sources = append(c.Sources, source{
			Title: r.Title, Indexer: r.Indexer, Size: r.Size, SizeHuman: humanSize(r.Size),
			Seeders: r.Seeders, Leechers: r.Leechers, Magnet: magnet,
			Quality: p.Quality, Source: p.Source, Codec: p.Codec, Audio: p.Audio,
			Group: p.Group, WebSafe: p.WebSafe, Published: r.PublishDate,
		})
		if r.Seeders > c.Seeders {
			c.Seeders = r.Seeders
		}
		if isAdult(r, r.Title) {
			c.Adult = true
		}
		for _, g := range groupsFor(r) {
			if !containsFold(c.Groups, g) || len(c.Groups) == 0 {
				if !hasString(c.Groups, g) {
					c.Groups = append(c.Groups, g)
				}
			}
		}
	}

	out := make([]card, 0, len(byKey))
	for _, c := range byKey {
		rankSources(c)
		out = append(out, *c)
	}
	// Most-seeded first: seeders are the best available proxy for "will this
	// actually start playing".
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seeders != out[j].Seeders {
			return out[i].Seeders > out[j].Seeders
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// rankSources orders a card's torrents and picks the default. A well-seeded
// web-safe 1080p beats a dead 4K HEVC every time, because the first one plays.
func rankSources(c *card) {
	score := func(s source) int {
		n := 0
		switch {
		case s.Seeders >= 50:
			n += 60
		case s.Seeders >= 10:
			n += 45
		case s.Seeders >= 3:
			n += 25
		case s.Seeders >= 1:
			n += 10
		}
		if s.WebSafe {
			n += 25
		}
		// A player with a touch pad outranks one without, because the archive's
		// own emulator expects a keyboard and offers no on-screen controls --
		// on a phone that is a game you can watch but not play.
		if s.Indexer == "EmulatorJS" || s.Indexer == "Ruffle" {
			n += 200
		}
		n += qualityRank(s.Quality) * 3
		return n
	}
	sort.Slice(c.Sources, func(i, j int) bool {
		si, sj := score(c.Sources[i]), score(c.Sources[j])
		if si != sj {
			return si > sj
		}
		return c.Sources[i].Seeders > c.Sources[j].Seeders
	})
	c.Best = 0
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
