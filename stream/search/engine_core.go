package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// engineMissingGrace is how long a grab may be absent from its download
	// client before it is failed. qBittorrent answers "Ok." to a .torrent URL
	// before fetching it, so a short absence is normal; a long one means the
	// fetch failed or the torrent was removed, and a replacement may be sought.
	engineMissingGrace = 30 * time.Minute
	// engineMetaRefresh is how often a monitored series' episode list is re-read.
	engineMetaRefresh = 24 * time.Hour
	// engineMetaRetry holds back a series whose refresh failed.
	engineMetaRetry = time.Hour
	// engineRequestWait bounds how long a requested search waits for a search
	// already running on the same title.
	engineRequestWait = 10 * time.Minute
)

// errSearchBusy: a search for this title is already running.
var errSearchBusy = errors.New("already searching")

// searchBusyRetry is how often a waiting requested search tries again.
var searchBusyRetry = 2 * time.Second

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
	return e.AddWith(ctx, ref, by, true)
}

// AddWith is Add with the initial monitoring state set in the same commit,
// so the scheduler never sees a title monitored that was not meant to be.
// An existing title is returned unchanged.
func (e *engine) AddWith(ctx context.Context, ref arrRef, by string, monitored bool) (engineItem, bool, error) {
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
		Overview: d.Overview, Monitored: monitored, Added: e.stamp(), Episodes: d.Episodes, RequestedBy: by,
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
		if c.Enabled && (c.Kind == "" || c.Kind == "qbittorrent") {
			return e.qbitFor(c), c, nil
		}
	}
	return nil, engineClient{}, errors.New("no download client yet — add qBittorrent under Settings → Stack")
}

// clientByID is the download client a grab was handed to. A grab recorded
// before client ids existed (empty id) falls back to the first enabled one.
func (e *engine) clientByID(st engineState, id string) (*qbitClient, engineClient, error) {
	if id == "" {
		return e.client(st)
	}
	for _, c := range st.Clients {
		if c.ID == id && c.Enabled && (c.Kind == "" || c.Kind == "qbittorrent") {
			return e.qbitFor(c), c, nil
		}
	}
	return nil, engineClient{}, fmt.Errorf("download client %s is unavailable (removed or disabled)", id)
}

func (e *engine) qbitFor(c engineClient) *qbitClient {
	e.mu.Lock()
	defer e.mu.Unlock()
	// One cached client per configured client; an edit replaces it, so a
	// changed password or address never leaves a stale session behind.
	qc, ok := e.qbits[c.ID]
	if !ok || qc.cfg != c {
		qc = newQbitClient(c, e.hc)
		e.qbits[c.ID] = qc
	}
	return qc
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
		ClientID: cfg.ID, Category: qc.category(), Season: r.Season, Episode: r.Episode, Quality: r.Quality,
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
type wantSlot struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

func (e *engine) wanted(it engineItem, grabs []engineGrab, prof engineProfile) []wantSlot {
	if it.Kind == "movie" {
		if (it.HasFile && !isUpgrade(it.Quality, prof.Qualities[0], prof)) || inFlight(it, grabs, 0, 0) {
			return nil
		}
		return []wantSlot{{}}
	}
	// A pack would download again any episode already in flight, so a season
	// with a download running is fetched episode by episode instead.
	busy := map[int]bool{}
	for _, g := range grabs {
		if g.ItemID == it.ID && g.State == "downloading" {
			busy[g.Season] = true
		}
	}
	perSeason := map[int][]int{}
	var order []int
	for _, ep := range e.eligibleEpisodes(it, grabs) {
		if _, ok := perSeason[ep.Season]; !ok {
			order = append(order, ep.Season)
		}
		perSeason[ep.Season] = append(perSeason[ep.Season], ep.Episode)
	}
	var out []wantSlot
	for _, s := range order {
		// Specials are never packed: {Season: 0} would be the whole-title slot.
		if len(perSeason[s]) >= 2 && !busy[s] && s > 0 {
			out = append(out, wantSlot{Season: s})
			continue
		}
		for _, ep := range perSeason[s] {
			out = append(out, wantSlot{Season: s, Episode: ep})
		}
	}
	return out
}

// inFlight reports whether a grab already downloading covers this slot.
func inFlight(it engineItem, grabs []engineGrab, s, ep int) bool {
	for _, g := range grabs {
		if g.ItemID == it.ID && g.State == "downloading" &&
			(it.Kind == "movie" || (g.Season == s && (g.Episode == ep || g.Episode == 0))) {
			return true
		}
	}
	return false
}

// eligibleEpisodes is every episode that has aired, has no file and is not
// already downloading -- the only episodes any search may grab.
func (e *engine) eligibleEpisodes(it engineItem, grabs []engineGrab) []wantSlot {
	today := e.now().UTC().Format("2006-01-02")
	var out []wantSlot
	for _, ep := range it.Episodes {
		if ep.HasFile || (ep.Aired != "" && ep.Aired[:min(10, len(ep.Aired))] > today) || inFlight(it, grabs, ep.Season, ep.Episode) {
			continue
		}
		out = append(out, wantSlot{Season: ep.Season, Episode: ep.Episode})
	}
	return out
}

// AutoSearch searches for whatever the item is missing and grabs the best fit
// for each slot. Returns how many grabs it made.
func (e *engine) AutoSearch(ctx context.Context, id string) (int, error) {
	return e.autoSearch(ctx, id, nil)
}

// AutoSearchEpisode searches for one episode only: an episode request must
// never start downloads for the rest of the series.
func (e *engine) AutoSearchEpisode(ctx context.Context, id string, season, episode int) (int, error) {
	return e.autoSearch(ctx, id, &wantSlot{Season: season, Episode: episode})
}

func (e *engine) autoSearch(ctx context.Context, id string, only *wantSlot) (int, error) {
	e.mu.Lock()
	if e.searching[id] {
		e.mu.Unlock()
		return 0, errSearchBusy
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
	slots := e.wanted(*it, st.Grabs, st.Settings.Profile)
	if only != nil {
		slots = nil
		for _, ep := range e.eligibleEpisodes(*it, st.Grabs) {
			if ep == *only {
				slots = []wantSlot{ep}
			}
		}
	}
	for _, slot := range slots {
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
			// No pack: fall back to the season's episodes one by one -- only
			// those a search may grab (aired, missing, not already downloading).
			for _, ep := range e.eligibleEpisodes(*it, st.Grabs) {
				if ep.Season == slot.Season {
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

// searchSoon runs a requested search in the background. A search already
// running on the same title is waited out, not dropped: it may cover other
// episodes, and an unmonitored title gets no scheduler pass to catch up.
func (e *engine) searchSoon(id string, season, episode int, scoped bool) {
	ctx, cancel := context.WithTimeout(context.Background(), engineRequestWait)
	defer cancel()
	for {
		sctx, scancel := context.WithTimeout(ctx, 3*time.Minute)
		var err error
		if scoped {
			_, err = e.AutoSearchEpisode(sctx, id, season, episode)
		} else {
			_, err = e.AutoSearch(sctx, id)
		}
		scancel()
		if !errors.Is(err, errSearchBusy) {
			return
		}
		select {
		case <-ctx.Done():
			log.Printf("engine: requested search for %s gave up waiting for a running search", id)
			return
		case <-time.After(searchBusyRetry):
		}
	}
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
	open := map[string]bool{}
	for _, g := range st.Grabs {
		if g.State == "downloading" {
			open[g.ClientID] = true
		}
	}
	if len(open) == 0 {
		return nil
	}
	// Each grab is checked against the client it was handed to, so changing
	// which client is first never strands another client's downloads.
	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var firstErr error
	for _, cid := range ids {
		if err := e.reconcileClient(ctx, st, cid); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (e *engine) reconcileClient(ctx context.Context, st engineState, clientID string) error {
	qc, _, err := e.clientByID(st, clientID)
	if err != nil {
		e.clientGone(clientID, err.Error())
		return err
	}
	e.clientBack(clientID)
	// Known hashes are looked up across every category, so changing the
	// client's category never hides a running grab. A grab without a hash is
	// matched by name only within the category it was submitted under.
	all, err := qc.torrentsIn(ctx, "")
	if err != nil {
		return err
	}
	byHash := map[string]qbitTorrent{}
	for _, t := range all {
		byHash[strings.ToLower(t.Hash)] = t
	}
	byCategory := map[string][]qbitTorrent{}
	for _, g := range st.Grabs {
		if g.State != "downloading" || g.ClientID != clientID || g.Hash != "" {
			continue
		}
		cat := grabCategory(g, qc)
		if _, ok := byCategory[cat]; ok {
			continue
		}
		ts, err := qc.torrentsIn(ctx, cat)
		if err != nil {
			return err
		}
		byCategory[cat] = ts
	}

	// Phase 1, under the lock and quick: progress, failures, and which grabs
	// have finished.
	type ready struct {
		grab    engineGrab
		content string
	}
	var done []ready
	now := e.now()
	err = e.store.update(func(st *engineState) error {
		changed := false
		for i := range st.Grabs {
			g := &st.Grabs[i]
			if g.State != "downloading" || g.ClientID != clientID {
				continue
			}
			t, ok := byHash[g.Hash]
			if !ok && g.Hash == "" {
				// A .torrent URL grab has no hash until the client fetches it;
				// match it by name once, then remember the hash.
				for _, cand := range byCategory[grabCategory(*g, qc)] {
					if strings.EqualFold(cand.Name, g.Release) {
						t, ok, g.Hash = cand, true, strings.ToLower(cand.Hash)
						changed = true
						break
					}
				}
			}
			if !ok {
				// Absent past the grace period: the client never fetched it, or
				// it was removed. Fail it so a replacement can be searched.
				if grabbed, perr := time.Parse(time.RFC3339, g.Grabbed); perr == nil && now.Sub(grabbed) > engineMissingGrace {
					g.State = "failed"
					g.Detail = "never appeared in the download client (the torrent fetch failed or it was removed)"
					st.logEvent(now, g.ItemID, "failed", g.Release+": "+g.Detail)
					changed = true
				}
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
		unplaced := d.grab.Unplaced
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
				state, detail, unplaced = settleFailedImport(d.grab.Unplaced, res, err)
				break
			}
			placed = &copyIt
			state, detail, unplaced = settleImport(d.grab.Unplaced, &copyIt, res)
		}
		_ = e.store.update(func(st *engineState) error {
			for i := range st.Grabs {
				g := &st.Grabs[i]
				if g.Hash == d.grab.Hash && g.ItemID == d.grab.ItemID && g.State == "downloading" {
					g.State, g.Detail, g.Unplaced = state, detail, unplaced
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

// grabCategory is the category a grab was submitted under; grabs recorded
// before categories were kept use the client's current one.
func grabCategory(g engineGrab, qc *qbitClient) string {
	if g.Category != "" {
		return g.Category
	}
	return qc.category()
}

// settleImport decides what a successful import attempt means for its grab.
// Episodes that failed placement stay owed (prev) until they are placed: if
// one is no longer in the download, the grab fails so it is searched again
// instead of being marked imported and forgotten.
func settleImport(prev []wantSlot, it *engineItem, res importResult) (string, string, []wantSlot) {
	var gone []wantSlot
	for _, s := range prev {
		if ep := findEpisode(it, s.Season, s.Episode); ep != nil && !ep.HasFile && !hasSlot(res.FailedSlots, s) {
			gone = append(gone, s)
		}
	}
	placed := strings.Join(res.Placed, "; ")
	switch {
	case len(res.Failed) > 0:
		// Keep what landed; stay downloading so the failed episodes are
		// retried from this download once storage is fixed.
		return "downloading", "placed " + placed + "; will retry: " + strings.Join(res.Failed, "; "),
			mergeSlots(res.FailedSlots, gone)
	case len(gone) > 0:
		return "failed", "placed " + placed + "; " + slotNames(gone) +
			" no longer in the download, searching again", nil
	}
	return "imported", placed, nil
}

// settleFailedImport handles an import attempt that placed nothing. If the
// download was read and holds nothing to place while episodes are still
// owed, those episodes are gone: fail the grab so they are searched again.
// Anything else (an unreadable folder, a full disk) stays retryable.
func settleFailedImport(prev []wantSlot, res importResult, err error) (string, string, []wantSlot) {
	if len(prev) > 0 && (errors.Is(err, errNoVideo) || errors.Is(err, errNoEpisodeMatch)) {
		return "failed", slotNames(prev) + " no longer in the download, searching again", nil
	}
	return "downloading", err.Error(), mergeSlots(prev, res.FailedSlots)
}

func hasSlot(l []wantSlot, s wantSlot) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func mergeSlots(a, b []wantSlot) []wantSlot {
	out := append([]wantSlot(nil), a...)
	for _, s := range b {
		if !hasSlot(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func slotNames(l []wantSlot) string {
	names := make([]string, len(l))
	for i, s := range l {
		names[i] = "S" + pad2(s.Season) + "E" + pad2(s.Episode)
	}
	return strings.Join(names, ", ")
}

// clientBack clears the removed-client stamp once the client is configured
// again, so a later removal gets a fresh grace period.
func (e *engine) clientBack(clientID string) {
	_ = e.store.update(func(st *engineState) error {
		changed := false
		for i := range st.Grabs {
			g := &st.Grabs[i]
			if g.State == "downloading" && g.ClientID == clientID && g.ClientGone != "" {
				g.ClientGone, g.Detail, changed = "", "", true
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
}

// clientGone handles grabs whose download client was removed or disabled.
// They keep their slot for a grace period -- the owner may be re-adding the
// client -- then fail, so they stop blocking a replacement.
func (e *engine) clientGone(clientID, why string) {
	now := e.now()
	_ = e.store.update(func(st *engineState) error {
		changed := false
		for i := range st.Grabs {
			g := &st.Grabs[i]
			if g.State != "downloading" || g.ClientID != clientID {
				continue
			}
			if g.ClientGone == "" {
				g.ClientGone, g.Detail, changed = now.UTC().Format(time.RFC3339), why, true
				continue
			}
			since, err := time.Parse(time.RFC3339, g.ClientGone)
			if err != nil || now.Sub(since) > engineMissingGrace {
				g.State, g.Detail, changed = "failed", why+"; searching again", true
				st.logEvent(now, g.ItemID, "failed", g.Release+": "+why)
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
}

// metaRefreshDue: monitored series re-read their episode list daily.
func metaRefreshDue(it engineItem, now time.Time) bool {
	if it.Kind != "series" || !it.Monitored {
		return false
	}
	stamp := it.MetaRefreshed
	if stamp == "" {
		stamp = it.Added
	}
	if retry, err := time.Parse(time.RFC3339, it.MetaRetry); err == nil && now.Before(retry) {
		return false
	}
	last, err := time.Parse(time.RFC3339, stamp)
	return err != nil || now.Sub(last) >= engineMetaRefresh
}

// refreshEpisodes merges the current episode list into a series, keeping the
// file fields of episodes already on disk.
func (e *engine) refreshEpisodes(ctx context.Context, it engineItem) error {
	d, err := e.meta.Lookup(ctx, it.IMDb, it.Kind)
	if err != nil {
		return err
	}
	return e.store.update(func(st *engineState) error {
		cur := st.item(it.ID)
		if cur == nil {
			return errNoChange
		}
		for _, ne := range d.Episodes {
			if ep := findEpisode(cur, ne.Season, ne.Episode); ep != nil {
				if ne.Title != "" {
					ep.Title = ne.Title
				}
				if ne.Aired != "" {
					ep.Aired = ne.Aired
				}
				continue
			}
			cur.Episodes = append(cur.Episodes, engineEpisode{
				Season: ne.Season, Episode: ne.Episode, Title: ne.Title, Aired: ne.Aired})
		}
		sort.SliceStable(cur.Episodes, func(i, j int) bool {
			a, b := cur.Episodes[i], cur.Episodes[j]
			return a.Season < b.Season || (a.Season == b.Season && a.Episode < b.Episode)
		})
		cur.MetaRefreshed, cur.MetaRetry = e.stamp(), ""
		return nil
	})
}

// refreshDue refreshes every series that is due, one at a time. It runs on
// its own loop, so slow metadata lookups never hold up reconciliation, and a
// failure holds that series back for engineMetaRetry.
func (e *engine) refreshDue(ctx context.Context) {
	for _, it := range e.store.view().Items {
		if ctx.Err() != nil {
			return
		}
		if !metaRefreshDue(it, e.now()) {
			continue
		}
		mctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := e.refreshEpisodes(mctx, it)
		cancel()
		if err == nil {
			continue
		}
		log.Printf("engine: refresh episodes %s: %v", it.Title, err)
		retry := e.now().Add(engineMetaRetry).UTC().Format(time.RFC3339)
		_ = e.store.update(func(st *engineState) error {
			if cur := st.item(it.ID); cur != nil {
				cur.MetaRetry = retry
			}
			return nil
		})
	}
}

func (e *engine) metaLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		e.refreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Run is the scheduler: reconcile every minute, search what is due.
func (e *engine) Run(ctx context.Context) {
	go e.metaLoop(ctx)
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
