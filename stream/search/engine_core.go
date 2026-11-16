package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type engine struct {
	store *engineStore
	meta  engineMeta
	tmdb  *tmdbClient
	hc    *http.Client
	now   func() time.Time

	mu    sync.Mutex
	qbits map[string]*qbitClient // keyed by client id + config, rebuilt on change
	// searching guards against two searches for one item at once (a request
	// arriving while the scheduler is already on it).
	searching map[string]bool
}

func newEngine(store *engineStore, meta engineMeta, tmdb *tmdbClient, hc *http.Client) *engine {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &engine{store: store, meta: meta, tmdb: tmdb, hc: hc, now: time.Now,
		qbits: map[string]*qbitClient{}, searching: map[string]bool{}}
}

// engineFromEnv opens the engine unless YARRIT_ENGINE=off. State lives at
// YARRIT_ENGINE_STATE, else next to LIBRARY_PATH; with neither, the engine is
// off -- a dev run without a data directory must not scatter state files.
func engineFromEnv(tmdb *tmdbClient) (*engine, error) {
	if strings.EqualFold(os.Getenv("YARRIT_ENGINE"), "off") {
		return nil, nil
	}
	path := strings.TrimSpace(os.Getenv("YARRIT_ENGINE_STATE"))
	if path == "" {
		if lib := strings.TrimSpace(os.Getenv("LIBRARY_PATH")); lib != "" {
			path = filepath.Join(filepath.Dir(lib), "engine.json")
		}
	}
	if path == "" {
		return nil, nil
	}
	_, statErr := os.Stat(path)
	st, err := openEngineStore(path)
	if err != nil {
		return nil, err
	}
	if os.IsNotExist(statErr) {
		if err := seedEngineFromEnv(st); err != nil {
			return nil, err
		}
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	return newEngine(st, newCinemeta(hc), tmdb, hc), nil
}

func (e *engine) stamp() string { return e.now().UTC().Format(time.RFC3339) }

// --- library ---------------------------------------------------------------

// resolveRef turns any canonical id the engine can serve into (kind, imdb).
func (e *engine) resolveRef(ctx context.Context, ref arrRef) (string, string, error) {
	kind := engineKindOf(ref.Type)
	if kind == "" {
		return "", "", fmt.Errorf("the engine handles films and series, not %q", ref.Type)
	}
	if ref.Scheme == "imdb" {
		if !reIMDb.MatchString(ref.ID) {
			return "", "", fmt.Errorf("%q is not an IMDb id", ref.ID)
		}
		return kind, ref.ID, nil
	}
	imdb, k, err := tmdbToIMDb(ctx, e.tmdb, ref)
	if err != nil {
		return "", "", err
	}
	if !reIMDb.MatchString(imdb) {
		return "", "", fmt.Errorf("no IMDb id is known for %s:%s", ref.Scheme, ref.ID)
	}
	return k, imdb, nil
}

func (e *engine) canResolve(ref arrRef) bool {
	if engineKindOf(ref.Type) == "" {
		return false
	}
	return ref.Scheme == "imdb" || ((ref.Scheme == "tmdb" || ref.Scheme == "tvdb") && e.tmdb.enabled())
}

// Add puts a title in the library (monitored), or returns it if present.
func (e *engine) Add(ctx context.Context, ref arrRef, by string) (engineItem, bool, error) {
	kind, imdb, err := e.resolveRef(ctx, ref)
	if err != nil {
		return engineItem{}, false, err
	}
	id := engineItemID(kind, imdb)
	if st := e.store.view(); st.item(id) != nil {
		it := st.item(id)
		return *it, false, nil
	}
	d, err := e.meta.Lookup(ctx, imdb, kind)
	if err != nil {
		return engineItem{}, false, err
	}
	it := engineItem{
		ID: id, Kind: kind, IMDb: imdb, Title: d.Title, Year: d.Year, Poster: d.Poster,
		Overview: d.Overview, Monitored: true, Added: e.stamp(), Episodes: d.Episodes, RequestedBy: by,
	}
	created := false
	err = e.store.update(func(st *engineState) error {
		if st.item(id) != nil {
			return nil
		}
		st.Items = append(st.Items, it)
		st.logEvent(e.now(), id, "added", it.Title)
		created = true
		return nil
	})
	return it, created, err
}

func (e *engine) Remove(id string) error {
	return e.store.update(func(st *engineState) error {
		for i := range st.Items {
			if st.Items[i].ID == id {
				st.Items = append(st.Items[:i], st.Items[i+1:]...)
				return nil
			}
		}
		return errors.New("not in the library")
	})
}

func (e *engine) SetMonitored(id string, on bool) error {
	return e.store.update(func(st *engineState) error {
		it := st.item(id)
		if it == nil {
			return errors.New("not in the library")
		}
		it.Monitored = on
		return nil
	})
}

// downloadingIDs: the items with a grab in flight, built once per request
// rather than rescanning every grab for every item.
func downloadingIDs(grabs []engineGrab) map[string]bool {
	out := map[string]bool{}
	for _, g := range grabs {
		if g.State == "downloading" {
			out[g.ItemID] = true
		}
	}
	return out
}

// itemState is the five-way LibraryState for one item.
func itemState(it engineItem, downloading map[string]bool) LibraryState {
	if downloading[it.ID] {
		return StateDownloading
	}
	if it.Kind == "movie" {
		if it.HasFile {
			return StateAvailable
		}
	} else if len(it.Episodes) > 0 {
		have := 0
		for _, ep := range it.Episodes {
			if ep.HasFile {
				have++
			}
		}
		if have == len(it.Episodes) {
			return StateAvailable
		}
	}
	if it.Monitored {
		return StateRequested
	}
	return StateMissing
}

// --- searching -------------------------------------------------------------

type candidatesResult struct {
	Releases []scoredRelease `json:"releases"`
	Failed   []string        `json:"failed,omitempty"`
}

// Candidates asks every enabled indexer at once and ranks what comes back.
func (e *engine) Candidates(ctx context.Context, id string, season, episode int) (candidatesResult, error) {
	st := e.store.view()
	it := st.item(id)
	if it == nil {
		return candidatesResult{}, errors.New("not in the library")
	}
	return e.candidates(ctx, st, *it, season, episode)
}

// candidates works on a state copy the caller already holds, so AutoSearch
// does not re-copy the whole state for every slot it searches.
func (e *engine) candidates(ctx context.Context, st engineState, it engineItem, season, episode int) (candidatesResult, error) {
	var ixs []engineIndexer
	for _, ix := range st.Indexers {
		if ix.Enabled {
			ixs = append(ixs, ix)
		}
	}
	if len(ixs) == 0 {
		return candidatesResult{}, errors.New("no indexers yet — add one under Settings → Stack")
	}
	q := engineQuery{Kind: it.Kind, Text: it.Title, IMDb: it.IMDb, Season: season, Episode: episode}
	if it.Kind == "movie" && it.Year > 0 {
		q.Text = fmt.Sprintf("%s %d", it.Title, it.Year)
	}
	var (
		mu     sync.Mutex
		all    []engineRelease
		failed []string
		wg     sync.WaitGroup
	)
	for _, ix := range ixs {
		wg.Add(1)
		go func(ix engineIndexer) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			rs, err := ix.search(cctx, e.hc, q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, err.Error())
				return
			}
			all = append(all, rs...)
		}(ix)
	}
	wg.Wait()
	t := engineTarget{Kind: it.Kind, Title: it.Title, Year: it.Year, Season: season, Episode: episode}
	return candidatesResult{Releases: rankReleases(all, t, st.Settings.Profile), Failed: failed}, nil
}

// --- grabbing --------------------------------------------------------------

func (e *engine) client(st engineState) (*qbitClient, engineClient, error) {
	for _, c := range st.Clients {
		if !c.Enabled {
			continue
		}
		if c.Kind != "" && c.Kind != "qbittorrent" {
			continue
		}
		e.mu.Lock()
		// One cached client per configured client; an edit replaces it, so a
		// changed password or address never leaves a stale session behind.
		qc, ok := e.qbits[c.ID]
		if !ok || qc.cfg != c {
			qc = newQbitClient(c, e.hc)
			e.qbits[c.ID] = qc
		}
		e.mu.Unlock()
		return qc, c, nil
	}
	return nil, engineClient{}, errors.New("no download client yet — add qBittorrent under Settings → Stack")
}

// Grab hands one release to the download client and records it.
func (e *engine) Grab(ctx context.Context, id string, r scoredRelease) error {
	st := e.store.view()
	if st.item(id) == nil {
		return errors.New("not in the library")
	}
	return e.grab(ctx, st, id, r)
}

func (e *engine) grab(ctx context.Context, st engineState, id string, r scoredRelease) error {
	qc, cfg, err := e.client(st)
	if err != nil {
		return err
	}
	link := r.Magnet
	if link == "" {
		link = r.DownloadURL
	}
	if link == "" {
		return errors.New("that release has no magnet or download link")
	}
	if err := qc.add(ctx, link, ""); err != nil {
		return err
	}
	g := engineGrab{
		ItemID: id, Release: r.Title, Indexer: r.Indexer, Hash: strings.ToLower(r.InfoHash),
		ClientID: cfg.ID, Season: r.Season, Episode: r.Episode, Quality: r.Quality,
		Grabbed: e.stamp(), State: "downloading",
	}
	if r.Pack {
		g.Episode = 0
	}
	return e.store.update(func(st *engineState) error {
		st.Grabs = append(st.Grabs, g)
		st.logEvent(e.now(), id, "grabbed", r.Title+" ("+r.Indexer+")")
		return nil
	})
}

// wanted lists what an item still needs: (0,0) for a film, else episodes
// that have aired and have no file, grouped so a season with two or more
// missing is tried as a pack first.
type wantSlot struct{ Season, Episode int }

func (e *engine) wanted(it engineItem, grabs []engineGrab, prof engineProfile) []wantSlot {
	inFlight := func(s, ep int) bool {
		for _, g := range grabs {
			if g.ItemID == it.ID && g.State == "downloading" &&
				(it.Kind == "movie" || (g.Season == s && (g.Episode == ep || g.Episode == 0))) {
				return true
			}
		}
		return false
	}
	if it.Kind == "movie" {
		if (it.HasFile && !isUpgrade(it.Quality, prof.Qualities[0], prof)) || inFlight(0, 0) {
			return nil
		}
		return []wantSlot{{}}
	}
	today := e.now().UTC().Format("2006-01-02")
	perSeason := map[int][]int{}
	var order []int
	for _, ep := range it.Episodes {
		if ep.HasFile || (ep.Aired != "" && ep.Aired[:min(10, len(ep.Aired))] > today) || inFlight(ep.Season, ep.Episode) {
			continue
		}
		if _, ok := perSeason[ep.Season]; !ok {
			order = append(order, ep.Season)
		}
		perSeason[ep.Season] = append(perSeason[ep.Season], ep.Episode)
	}
	var out []wantSlot
	for _, s := range order {
		if len(perSeason[s]) >= 2 {
			out = append(out, wantSlot{Season: s})
			continue
		}
		for _, ep := range perSeason[s] {
			out = append(out, wantSlot{Season: s, Episode: ep})
		}
	}
	return out
}

// AutoSearch searches for whatever the item is missing and grabs the best fit
// for each slot. Returns how many grabs it made.
func (e *engine) AutoSearch(ctx context.Context, id string) (int, error) {
	e.mu.Lock()
	if e.searching[id] {
		e.mu.Unlock()
		return 0, errors.New("already searching")
	}
	e.searching[id] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.searching, id); e.mu.Unlock() }()

	st := e.store.view()
	it := st.item(id)
	if it == nil {
		return 0, errors.New("not in the library")
	}
	grabbed := 0
	var lastErr error
	for _, slot := range e.wanted(*it, st.Grabs, st.Settings.Profile) {
		res, err := e.candidates(ctx, st, *it, slot.Season, slot.Episode)
		if err != nil {
			lastErr = err
			break
		}
		var pick *scoredRelease
		for i := range res.Releases {
			r := res.Releases[i]
			if len(r.Rejected) > 0 {
				break
			}
			if it.Kind == "movie" && it.HasFile && !isUpgrade(it.Quality, r.Quality, st.Settings.Profile) {
				continue
			}
			pick = &r
			break
		}
		if pick == nil && slot.Season > 0 && slot.Episode == 0 {
			// No pack: fall back to the season's episodes one by one.
			for _, ep := range it.Episodes {
				if ep.Season == slot.Season && !ep.HasFile {
					res, err := e.candidates(ctx, st, *it, ep.Season, ep.Episode)
					if err == nil && len(res.Releases) > 0 && len(res.Releases[0].Rejected) == 0 {
						if e.grab(ctx, st, id, res.Releases[0]) == nil {
							grabbed++
						}
					}
				}
			}
			continue
		}
		if pick != nil {
			if err := e.grab(ctx, st, id, *pick); err != nil {
				lastErr = err
				continue
			}
			grabbed++
		}
	}
	_ = e.store.update(func(st *engineState) error {
		if it := st.item(id); it != nil {
			it.LastSearch = e.stamp()
			if grabbed == 0 {
				it.Attempts++
			} else {
				it.Attempts = 0
			}
		}
		return nil
	})
	return grabbed, lastErr
}

// backoff: 1h, 2h, 4h ... capped at a week, on top of the search interval.
func searchDue(it engineItem, now time.Time, intervalHours int) bool {
	if !it.Monitored {
		return false
	}
	if it.LastSearch == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, it.LastSearch)
	if err != nil {
		return true
	}
	wait := time.Duration(intervalHours) * time.Hour
	if it.Attempts > 0 {
		extra := time.Hour << min(it.Attempts, 8)
		wait += min(extra, 7*24*time.Hour)
	}
	return now.Sub(last) >= wait
}

// --- the loop --------------------------------------------------------------

// Reconcile matches grabs against the client and imports what has finished.
func (e *engine) Reconcile(ctx context.Context) error {
	st := e.store.view()
	open := 0
	for _, g := range st.Grabs {
		if g.State == "downloading" {
			open++
		}
	}
	if open == 0 {
		return nil
	}
	qc, _, err := e.client(st)
	if err != nil {
		return err
	}
	ts, err := qc.torrents(ctx)
	if err != nil {
		return err
	}
	byHash := map[string]qbitTorrent{}
	for _, t := range ts {
		byHash[strings.ToLower(t.Hash)] = t
	}

	// Phase 1, under the lock and quick: progress, failures, and which grabs
	// have finished.
	type ready struct {
		grab    engineGrab
		content string
	}
	var done []ready
	err = e.store.update(func(st *engineState) error {
		changed := false
		for i := range st.Grabs {
			g := &st.Grabs[i]
			if g.State != "downloading" {
				continue
			}
			t, ok := byHash[g.Hash]
			if !ok {
				// A .torrent URL grab has no hash until the client fetches it;
				// match it by name once, then remember the hash.
				for _, cand := range ts {
					if strings.EqualFold(cand.Name, g.Release) {
						t, ok, g.Hash = cand, true, strings.ToLower(cand.Hash)
						changed = true
						break
					}
				}
			}
			if !ok {
				continue
			}
			if g.Progress != t.Progress {
				g.Progress, changed = t.Progress, true
			}
			switch {
			case t.failed():
				changed = true
				g.State, g.Detail = "failed", "qBittorrent reports "+t.State
				st.logEvent(e.now(), g.ItemID, "failed", g.Release+": "+t.State)
			case t.done():
				done = append(done, ready{grab: *g, content: t.ContentPath})
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Phase 2, outside the lock: filing can be a cross-disk copy of tens of
	// gigabytes, and the API must keep answering meanwhile.
	for _, d := range done {
		st := e.store.view()
		it := st.item(d.grab.ItemID)
		state, detail := "downloading", ""
		var placed *engineItem
		switch {
		case it == nil:
			state, detail = "failed", "the title was removed from the library"
		default:
			local, err := mapDownloadPath(d.content, st.Settings)
			if err != nil {
				detail = err.Error() // stays downloading: fixing the mapping retries it
				break
			}
			copyIt := *it
			res, err := importDownload(local, &copyIt, d.grab, st.Settings)
			if err != nil {
				detail = err.Error()
				break
			}
			state, detail, placed = "imported", strings.Join(res.Placed, "; "), &copyIt
		}
		_ = e.store.update(func(st *engineState) error {
			for i := range st.Grabs {
				g := &st.Grabs[i]
				if g.Hash == d.grab.Hash && g.ItemID == d.grab.ItemID && g.State == "downloading" {
					g.State, g.Detail = state, detail
				}
			}
			if placed != nil {
				if it := st.item(placed.ID); it != nil {
					// Only the file fields: monitoring may have changed meanwhile.
					it.HasFile, it.File, it.Quality = placed.HasFile, placed.File, placed.Quality
					for _, pe := range placed.Episodes {
						if ep := findEpisode(it, pe.Season, pe.Episode); ep != nil && pe.HasFile {
							ep.HasFile, ep.File, ep.Quality = true, pe.File, pe.Quality
						}
					}
				}
				st.logEvent(e.now(), placed.ID, "imported", fmt.Sprintf("%s: %s", placed.Title, detail))
			}
			return nil
		})
	}
	return nil
}

// Run is the scheduler: reconcile every minute, search what is due.
func (e *engine) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := e.Reconcile(ctx); err != nil && !strings.HasPrefix(err.Error(), "no download client") {
			log.Printf("engine: reconcile: %v", err)
		}
		st := e.store.view()
		for _, it := range st.Items {
			if ctx.Err() != nil {
				return
			}
			if searchDue(it, e.now(), st.Settings.SearchIntervalHours) && len(e.wanted(it, st.Grabs, st.Settings.Profile)) > 0 {
				sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				if _, err := e.AutoSearch(sctx, it.ID); err != nil && !strings.HasPrefix(err.Error(), "no indexers") {
					log.Printf("engine: search %s: %v", it.Title, err)
				}
				cancel()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// seedEngineFromEnv pre-fills a brand-new engine so a compose install works
// on first boot: folders, path mapping and the download client. Only ever on
// a state file that did not exist -- after that, the UI owns these settings.
func seedEngineFromEnv(st *engineStore) error {
	env := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	return st.update(func(s *engineState) error {
		s.Settings.MovieRoot = env("ENGINE_MOVIE_ROOT")
		s.Settings.SeriesRoot = env("ENGINE_SERIES_ROOT")
		s.Settings.DownloadPath = env("ENGINE_DOWNLOAD_PATH")
		s.Settings.RemoteDownloadPath = env("ENGINE_REMOTE_DOWNLOAD_PATH")
		if u := env("ENGINE_INDEXER_URL"); validHTTPURL(u) {
			kind := env("ENGINE_INDEXER_KIND")
			if kind == "" {
				kind = "torznab"
			}
			name := env("ENGINE_INDEXER_NAME")
			if name == "" {
				name = kind
			}
			s.Indexers = append(s.Indexers, engineIndexer{
				ID: newEngineID(), Name: name, Kind: kind, URL: u,
				APIKey: os.Getenv("ENGINE_INDEXER_API_KEY"), Enabled: true,
			})
		}
		if u := env("ENGINE_QBIT_URL"); validHTTPURL(u) {
			s.Clients = append(s.Clients, engineClient{
				ID: newEngineID(), Name: "qBittorrent", Kind: "qbittorrent", URL: u,
				Username: env("ENGINE_QBIT_USERNAME"), Password: os.Getenv("ENGINE_QBIT_PASSWORD"),
				Category: "yarrit", Enabled: true,
			})
		}
		return nil
	})
}
