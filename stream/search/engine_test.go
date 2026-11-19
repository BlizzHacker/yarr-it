package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fakes -----------------------------------------------------------------

type fakeMeta struct{ details map[string]metaDetail }

func (f fakeMeta) Search(ctx context.Context, q, kind string) ([]metaHit, error) {
	var out []metaHit
	for _, d := range f.details {
		if strings.Contains(strings.ToLower(d.Title), strings.ToLower(q)) && (kind == "" || kind == d.Kind) {
			out = append(out, d.metaHit)
		}
	}
	return out, nil
}

func (f fakeMeta) Lookup(ctx context.Context, imdb, kind string) (metaDetail, error) {
	d, ok := f.details[imdb]
	if !ok {
		return metaDetail{}, fmt.Errorf("no %s", imdb)
	}
	return d, nil
}

var testMeta = fakeMeta{details: map[string]metaDetail{
	"tt0133093": {metaHit: metaHit{IMDb: "tt0133093", Kind: "movie", Title: "The Matrix", Year: 1999}},
	"tt11280740": {metaHit: metaHit{IMDb: "tt11280740", Kind: "series", Title: "Severance", Year: 2022},
		Episodes: []engineEpisode{
			{Season: 1, Episode: 1, Aired: "2022-02-18"}, {Season: 1, Episode: 2, Aired: "2022-02-18"},
			{Season: 9, Episode: 1, Aired: "2999-01-01"}, // not aired: never wanted
		}},
}}

func torznabServer(t *testing.T, items string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "ix-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprintf(w, `<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>%s</channel></rss>`, items)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tzItem(title string, seeders int, hash string, size int64) string {
	return fmt.Sprintf(`<item><title>%s</title><size>%d</size><link>magnet:?xt=urn:btih:%s&amp;dn=x</link>`+
		`<torznab:attr name="seeders" value="%d"/><torznab:attr name="infohash" value="%s"/></item>`,
		title, size, hash, seeders, hash)
}

type fakeQbit struct {
	mu       sync.Mutex
	srv      *httptest.Server
	sid      string
	added    []string
	torrents []qbitTorrent
	addBody  string
	logins   int
	// filterCategory makes torrents/info honour ?category= as qBittorrent does.
	filterCategory bool
}

func newFakeQbit(t *testing.T) *fakeQbit {
	f := &fakeQbit{sid: "s1", addBody: "Ok."}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/api/v2/auth/login" {
			_ = r.ParseForm()
			if r.PostForm.Get("username") != "admin" || r.PostForm.Get("password") != "pw" {
				_, _ = w.Write([]byte("Fails."))
				return
			}
			f.logins++
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: f.sid, Path: "/"})
			_, _ = w.Write([]byte("Ok."))
			return
		}
		if c, err := r.Cookie("SID"); err != nil || c.Value != f.sid {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v5.0.1"))
		case "/api/v2/torrents/createCategory":
			w.WriteHeader(409)
		case "/api/v2/torrents/add":
			_ = r.ParseForm()
			f.added = append(f.added, r.PostForm.Get("urls"))
			_, _ = w.Write([]byte(f.addBody))
		case "/api/v2/torrents/info":
			out := f.torrents
			if q := r.URL.Query(); f.filterCategory && q.Has("category") {
				out = nil
				for _, t := range f.torrents {
					if t.Category == q.Get("category") {
						out = append(out, t)
					}
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func testEngine(t *testing.T, ix *httptest.Server, qb *fakeQbit) *engine {
	st, err := openEngineStore("")
	if err != nil {
		t.Fatal(err)
	}
	e := newEngine(st, testMeta, newTMDB(""), &http.Client{Timeout: 5 * time.Second})
	_ = st.update(func(s *engineState) error {
		if ix != nil {
			s.Indexers = []engineIndexer{{ID: "i1", Name: "Test", Kind: "torznab", URL: ix.URL + "/api", APIKey: "ix-key", Enabled: true}}
		}
		if qb != nil {
			s.Clients = []engineClient{{ID: "c1", Name: "qb", Kind: "qbittorrent", URL: qb.srv.URL, Username: "admin", Password: "pw", Enabled: true}}
		}
		return nil
	})
	return e
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// --- scoring ---------------------------------------------------------------

func TestScoringPrefersTheProfileOrderAndRejectsWithReasons(t *testing.T) {
	prof := defaultEngineSettings().Profile // 1080p, 2160p, 720p; no CAM
	tg := engineTarget{Kind: "movie", Title: "The Matrix", Year: 1999}
	ranked := rankReleases([]engineRelease{
		{Title: "The.Matrix.1999.720p.BluRay.x264-GRP", Seeders: 50, Magnet: "magnet:?xt=urn:btih:a"},
		{Title: "The.Matrix.1999.1080p.WEB-DL.x265-GRP", Seeders: 10, Magnet: "magnet:?xt=urn:btih:b"},
		{Title: "The.Matrix.1999.2160p.UHD.BluRay.x265-GRP", Seeders: 80, Magnet: "magnet:?xt=urn:btih:c"},
		{Title: "The.Matrix.1999.CAM.x264-BAD", Seeders: 900, Magnet: "magnet:?xt=urn:btih:d"},
		{Title: "The.Matrix.Reloaded.2003.1080p.BluRay-GRP", Seeders: 99, Magnet: "magnet:?xt=urn:btih:e"},
		{Title: "The.Matrix.1999.1080p.BluRay-GRP", Seeders: 0, Magnet: "magnet:?xt=urn:btih:f"},
	}, tg, prof)
	if ranked[0].Quality != "1080p" || len(ranked[0].Rejected) != 0 {
		t.Fatalf("1080p is first in the profile and should win: %+v", ranked[0])
	}
	reasons := map[string]string{}
	for _, r := range ranked {
		reasons[r.Title] = strings.Join(r.Rejected, "; ")
	}
	for title, want := range map[string]string{
		"The.Matrix.1999.CAM.x264-BAD":              "CAM releases are rejected",
		"The.Matrix.Reloaded.2003.1080p.BluRay-GRP": "year 2003",
		"The.Matrix.1999.1080p.BluRay-GRP":          "0 seeders",
	} {
		if !strings.Contains(reasons[title], want) {
			t.Errorf("%s: want reason containing %q, got %q", title, want, reasons[title])
		}
	}
}

func TestScoringSeriesEpisodesAndSeasonPacks(t *testing.T) {
	prof := defaultEngineSettings().Profile
	ep := engineTarget{Kind: "series", Title: "Severance", Season: 1, Episode: 2}
	cases := map[string]bool{
		"Severance.S01E02.1080p.WEB-DL.x264-GRP": true,
		"Severance.S01.1080p.WEB-DL.x264-GRP":    true, // pack containing it
		"Severance.S01E03.1080p.WEB-DL.x264-GRP": false,
		"Severance.S02.1080p.WEB-DL.x264-GRP":    false,
	}
	for title, fit := range cases {
		s := scoreRelease(engineRelease{Title: title, Seeders: 5, Magnet: "magnet:?xt=urn:btih:x"}, ep, prof)
		if (len(s.Rejected) == 0) != fit {
			t.Errorf("%s: fit=%v, rejected=%v", title, fit, s.Rejected)
		}
	}
	pack := scoreRelease(engineRelease{Title: "Severance.S01.1080p.WEB-DL-GRP", Seeders: 5, Magnet: "magnet:?x"},
		engineTarget{Kind: "series", Title: "Severance", Season: 1}, prof)
	if !pack.Pack || pack.Season != 1 || len(pack.Rejected) != 0 {
		t.Errorf("season pack: %+v", pack)
	}
}

func TestCutoffStopsUpgrades(t *testing.T) {
	prof := engineProfile{Qualities: []string{"2160p", "1080p", "720p"}, Cutoff: "1080p"}
	if !isUpgrade("720p", "1080p", prof) || isUpgrade("1080p", "2160p", prof) || isUpgrade("720p", "480p", prof) {
		t.Error("cutoff/upgrade rules are wrong")
	}
}

// --- indexers --------------------------------------------------------------

func TestTorznabParsesSeedersHashAndMagnet(t *testing.T) {
	srv := torznabServer(t, tzItem("The.Matrix.1999.1080p.BluRay-GRP", 42, "abc123", 8<<30))
	ix := engineIndexer{Name: "T", Kind: "torznab", URL: srv.URL + "/api", APIKey: "ix-key"}
	rs, err := ix.search(ctxT(t), srv.Client(), engineQuery{Kind: "movie", Text: "The Matrix", IMDb: "tt0133093"})
	if err != nil || len(rs) != 1 {
		t.Fatalf("got %v %v", rs, err)
	}
	r := rs[0]
	if r.Seeders != 42 || r.InfoHash != "abc123" || !strings.HasPrefix(r.Magnet, "magnet:") || r.Size != 8<<30 {
		t.Errorf("parsed %+v", r)
	}
	bad := ix
	bad.APIKey = "nope"
	if _, err := bad.search(ctxT(t), srv.Client(), engineQuery{Kind: "movie", Text: "x"}); err == nil ||
		!strings.Contains(err.Error(), "rejected the API key") {
		t.Errorf("want key rejection, got %v", err)
	}
}

func TestTorznabRefusesNonHTTPAddresses(t *testing.T) {
	ix := engineIndexer{Name: "T", URL: "file:///etc/passwd"}
	if _, err := ix.torznabURL(engineQuery{}, true); err == nil {
		t.Error("a file: URL must be refused")
	}
}

// --- qBittorrent -----------------------------------------------------------

func TestQbitTreatsFailsAsFailureAndRelogsInAfterRestart(t *testing.T) {
	qb := newFakeQbit(t)
	c := newQbitClient(engineClient{URL: qb.srv.URL, Username: "admin", Password: "pw"}, nil)
	if err := c.add(ctxT(t), "magnet:?xt=urn:btih:a", ""); err != nil {
		t.Fatal(err)
	}
	qb.mu.Lock()
	qb.addBody, qb.sid = "Fails.", "s2" // qBittorrent restarted: old cookie is dead
	qb.mu.Unlock()
	err := c.add(ctxT(t), "magnet:?xt=urn:btih:b", "")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a 200 with Fails. must be an error, got %v", err)
	}
	if qb.logins != 2 {
		t.Errorf("a 403 should trigger exactly one fresh login, logins=%d", qb.logins)
	}
	wrong := newQbitClient(engineClient{URL: qb.srv.URL, Username: "admin", Password: "x"}, nil)
	if _, err := wrong.version(ctxT(t)); err != errQbitAuth {
		t.Errorf("want auth error, got %v", err)
	}
}

// --- the whole loop --------------------------------------------------------

func TestEngineAddSearchGrabImportFilm(t *testing.T) {
	ix := torznabServer(t,
		tzItem("The.Matrix.1999.720p.BluRay.x264-GRP", 50, "hash720", 4<<30)+
			tzItem("The.Matrix.1999.1080p.BluRay.x264-GRP", 30, "hash1080", 9<<30))
	qb := newFakeQbit(t)
	e := testEngine(t, ix, qb)

	dl, lib := t.TempDir(), t.TempDir()
	_ = e.store.update(func(s *engineState) error {
		s.Settings.DownloadPath, s.Settings.RemoteDownloadPath = dl, "/downloads"
		s.Settings.MovieRoot = filepath.Join(lib, "Films")
		return nil
	})

	ref, _ := parseArrRef("imdb:movie:tt0133093")
	it, created, err := e.Add(ctxT(t), ref, "")
	if err != nil || !created || it.Title != "The Matrix" {
		t.Fatalf("add: %+v %v %v", it, created, err)
	}
	n, err := e.AutoSearch(ctxT(t), it.ID)
	if err != nil || n != 1 {
		t.Fatalf("autosearch grabbed %d, err %v", n, err)
	}
	if len(qb.added) != 1 || !strings.Contains(qb.added[0], "hash1080") {
		t.Fatalf("should have grabbed the 1080p: %v", qb.added)
	}
	if st := func() LibraryState { v := e.store.view(); return itemState(*v.item(it.ID), downloadingIDs(v.Grabs)) }(); st != StateDownloading {
		t.Errorf("state after grab: %s", st)
	}

	// The download finishes; qBittorrent names its path as the client sees it.
	folder := filepath.Join(dl, "The.Matrix.1999.1080p.BluRay.x264-GRP")
	_ = os.MkdirAll(folder, 0o755)
	_ = os.WriteFile(filepath.Join(folder, "the.matrix.1999.mkv"), make([]byte, 1024), 0o644)
	_ = os.WriteFile(filepath.Join(folder, "sample.mkv"), make([]byte, 10), 0o644)
	qb.mu.Lock()
	qb.torrents = []qbitTorrent{{Hash: "HASH1080", Name: "x", Progress: 1, State: "uploading",
		ContentPath: "/downloads/The.Matrix.1999.1080p.BluRay.x264-GRP", Category: "yarrit"}}
	qb.mu.Unlock()

	if err := e.Reconcile(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	got := e.store.view()
	want := filepath.Join(lib, "Films", "The Matrix (1999)", "The Matrix (1999) [1080p].mkv")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("not filed at %s: %v (grab: %+v)", want, err, got.Grabs)
	}
	if !got.item(it.ID).HasFile || got.Grabs[0].State != "imported" {
		t.Errorf("item/grab not updated: %+v %+v", got.item(it.ID), got.Grabs[0])
	}
	if itemState(*got.item(it.ID), downloadingIDs(got.Grabs)) != StateAvailable {
		t.Error("an imported film is available")
	}
	// Nothing left to want: a second search grabs nothing.
	if w := e.wanted(*got.item(it.ID), got.Grabs, got.Settings.Profile); len(w) != 0 {
		t.Errorf("at the cutoff, nothing is wanted: %v", w)
	}
}

func TestEngineSeriesPrefersAPackAndFilesEachEpisode(t *testing.T) {
	ix := torznabServer(t, tzItem("Severance.S01.1080p.WEB-DL.x264-GRP", 20, "packhash", 20<<30))
	qb := newFakeQbit(t)
	e := testEngine(t, ix, qb)
	dl, lib := t.TempDir(), t.TempDir()
	_ = e.store.update(func(s *engineState) error {
		s.Settings.DownloadPath, s.Settings.SeriesRoot = dl, lib
		return nil
	})
	ref, _ := parseArrRef("imdb:series:tt11280740")
	it, _, err := e.Add(ctxT(t), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	slots := e.wanted(it, nil, e.store.view().Settings.Profile)
	if len(slots) != 1 || slots[0] != (wantSlot{Season: 1}) {
		t.Fatalf("two missing aired episodes should ask for one S01 pack, got %v (unaired S09 excluded)", slots)
	}
	if n, err := e.AutoSearch(ctxT(t), it.ID); n != 1 || err != nil {
		t.Fatalf("grabbed %d err %v", n, err)
	}
	pack := filepath.Join(dl, "Severance.S01")
	_ = os.MkdirAll(pack, 0o755)
	for _, f := range []string{"Severance.S01E01.1080p.mkv", "Severance.S01E02.1080p.mkv"} {
		_ = os.WriteFile(filepath.Join(pack, f), make([]byte, 64), 0o644)
	}
	qb.torrents = []qbitTorrent{{Hash: "packhash", Progress: 1, ContentPath: pack}}
	if err := e.Reconcile(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"S01E01", "S01E02"} {
		p := filepath.Join(lib, "Severance (2022)", "Season 01", "Severance - "+ep+" [1080p].mkv")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s: %v (%+v)", p, err, e.store.view().Grabs)
		}
	}
}

func TestImportRefusesPathsOutsideTheDownloadFolder(t *testing.T) {
	dl := t.TempDir()
	set := engineSettings{DownloadPath: dl}
	if _, err := mapDownloadPath("/etc", set); err == nil {
		t.Error("a content path outside the download folder must be refused")
	}
	if got := safeName(`../../etc/passwd: "x"`); strings.ContainsAny(got, `/\:"`) {
		t.Errorf("unsafe name survived: %q", got)
	}
}

func TestSearchBackoffGrows(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	it := engineItem{Monitored: true, LastSearch: now.Add(-13 * time.Hour).Format(time.RFC3339)}
	if !searchDue(it, now, 12) {
		t.Error("due after the interval")
	}
	it.Attempts = 4 // +16h
	if searchDue(it, now, 12) {
		t.Error("four misses should push the next search out")
	}
	it.Monitored = false
	it.LastSearch = ""
	if searchDue(it, now, 12) {
		t.Error("unmonitored items are never searched")
	}
}

// --- provider, routing, requests -------------------------------------------

func TestEngineIsTheFallbackUnlessPreferred(t *testing.T) {
	e := testEngine(t, nil, nil)
	ep := &engineProvider{e}
	radarr := newRadarrProvider(arrConfig{BaseURL: "http://r"})
	ref, _ := parseArrRef("tmdb:movie:603")
	// No TMDB key: the engine cannot translate a TMDB id, so only Radarr can.
	if ts := requestTargets([]Provider{ep, radarr}, ref); len(ts) != 1 || ts[0].ID() != "radarr" {
		t.Fatalf("got %v", ids(ts))
	}
	ref, _ = parseArrRef("imdb:movie:tt0133093")
	if ts := requestTargets([]Provider{ep, radarr}, ref); len(ts) != 1 || ts[0].ID() != "yarrit" {
		t.Fatalf("an IMDb id is the engine's: got %v", ids(ts))
	}
	sonarr := newSonarrProvider(arrConfig{BaseURL: "http://s"})
	ref, _ = parseArrRef("imdb:series:tt11280740")
	_ = sonarr
	if ts := requestTargets([]Provider{ep}, ref); len(ts) != 1 {
		t.Fatalf("standalone: the engine takes series too, got %v", ids(ts))
	}
	// Preferred moves the engine ahead of a brought service for a shared ref.
	_ = e.store.update(func(s *engineState) error { s.Settings.PreferEngine = true; return nil })
	both := []Provider{newRadarrProvider(arrConfig{BaseURL: "http://r"}), ep}
	ref, _ = parseArrRef("imdb:movie:tt0133093")
	if ts := requestTargets(both, ref); ts[0].ID() != "yarrit" {
		t.Fatalf("preferred engine should go first: %v", ids(ts))
	}
}

func TestEngineHealthSaysWhatIsMissing(t *testing.T) {
	e := testEngine(t, nil, nil)
	h := (&engineProvider{e}).Health(ctxT(t))
	if h.State != HealthNotConfigured || !strings.Contains(h.Detail, "indexer") || !strings.Contains(h.Detail, "download client") {
		t.Errorf("got %+v", h)
	}
}

func TestNativeRequestsAskDecideAndRoute(t *testing.T) {
	e := testEngine(t, nil, nil)
	reg := &Registry{}
	routed := ""
	n := &nativeRequests{e: e, route: func(ctx context.Context, id string) (RequestResult, string, error) {
		routed = id
		return RequestResult{Accepted: true, Detail: "added"}, "yarrit", nil
	}}
	_ = reg.Add(n)
	r, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid")
	if err != nil || r.ID != 1 || r.Status != "pending" {
		t.Fatalf("ask: %+v %v", r, err)
	}
	if _, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid"); err == nil {
		t.Error("a duplicate pending request must be refused")
	}
	view := (&requestsAPI{reg: reg}).collect(ctxT(t), RequestPending, 50)
	if len(view.Items) != 1 || !view.Items[0].Decidable || view.Items[0].RequestedBy != "kid" {
		t.Fatalf("queue: %+v", view)
	}
	if err := n.Decide(ctxT(t), "1", true); err != nil {
		t.Fatal(err)
	}
	if routed != "imdb:movie:tt0133093" {
		t.Errorf("approval should route the request, routed=%q", routed)
	}
	if err := n.Decide(ctxT(t), "1", false); err == nil {
		t.Error("a decided request cannot be decided again")
	}
}

func TestEngineAPINeverReturnsSecrets(t *testing.T) {
	e := testEngine(t, torznabServer(t, ""), newFakeQbit(t))
	a := &engineAPI{e: e}
	rec := httptest.NewRecorder()
	a.handleState(rec, httptest.NewRequest("GET", "/api/engine", nil))
	body := rec.Body.String()
	if strings.Contains(body, "ix-key") || strings.Contains(body, `"pw"`) {
		t.Fatalf("secrets leaked: %s", body)
	}
	if !strings.Contains(body, `"keySet":true`) || !strings.Contains(body, `"passwordSet":true`) {
		t.Errorf("should say a secret is set: %s", body)
	}
	// Updating without re-typing the key keeps it.
	rec = httptest.NewRecorder()
	a.handleIndexers(rec, httptest.NewRequest("POST", "/api/engine/indexers",
		strings.NewReader(`{"id":"i1","name":"Renamed","kind":"torznab","url":"http://x/api","enabled":true}`)))
	if rec.Code != 200 || e.store.view().Indexers[0].APIKey != "ix-key" {
		t.Errorf("blank key should keep the stored one: %d %+v", rec.Code, e.store.view().Indexers[0])
	}
	rec = httptest.NewRecorder()
	a.handleIndexers(rec, httptest.NewRequest("POST", "/api/engine/indexers",
		strings.NewReader(`{"name":"Bad","url":"gopher://x"}`)))
	if rec.Code != 400 {
		t.Errorf("non-http indexer should be refused, got %d", rec.Code)
	}
}

func TestEngineStorePersistsAtomically(t *testing.T) {
	p := filepath.Join(t.TempDir(), "engine.json")
	s, err := openEngineStore(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.update(func(st *engineState) error {
		st.Indexers = append(st.Indexers, engineIndexer{ID: "a", Name: "A", APIKey: "k"})
		return nil
	})
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state should be 0600, got %v %v", info, err)
	}
	s2, err := openEngineStore(p)
	if err != nil || len(s2.view().Indexers) != 1 {
		t.Fatalf("reload: %v %+v", err, s2.view())
	}
	if err := s.update(func(st *engineState) error { st.Items = nil; return fmt.Errorf("boom") }); err == nil {
		t.Fatal("expected error")
	}
	if len(s.view().Indexers) != 1 {
		t.Error("a failed update must not change state")
	}
}

func TestStackModesWithTheEngine(t *testing.T) {
	reg := &Registry{}
	_ = reg.Add(&engineProvider{testEngine(t, nil, nil)})
	if got := buildStack(reg); got.Mode != stackUnified {
		t.Errorf("engine only should be unified: %+v", got)
	}
	_ = reg.Add(newRadarrProvider(arrConfig{BaseURL: "http://r"}))
	if got := buildStack(reg); got.Mode != stackMixed {
		t.Errorf("engine + Radarr should be mixed: %+v", got)
	}
}

func TestFreshEngineIsSeededFromEnvOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YARRIT_ENGINE_STATE", filepath.Join(dir, "engine.json"))
	t.Setenv("ENGINE_MOVIE_ROOT", "/media/movies")
	t.Setenv("ENGINE_QBIT_URL", "http://qbittorrent:8080")
	t.Setenv("ENGINE_QBIT_PASSWORD", "pw")
	t.Setenv("ENGINE_INDEXER_URL", "http://prowlarr:9696")
	t.Setenv("ENGINE_INDEXER_KIND", "prowlarr")
	e, err := engineFromEnv(newTMDB(""))
	if err != nil || e == nil {
		t.Fatalf("engine: %v %v", e, err)
	}
	st := e.store.view()
	if st.Settings.MovieRoot != "/media/movies" || len(st.Clients) != 1 || len(st.Indexers) != 1 || st.Indexers[0].Kind != "prowlarr" {
		t.Fatalf("not seeded: %+v", st)
	}
	_ = e.store.update(func(s *engineState) error { s.Settings.MovieRoot = "/changed"; return nil })
	t.Setenv("ENGINE_MOVIE_ROOT", "/media/other")
	e2, _ := engineFromEnv(newTMDB(""))
	if got := e2.store.view(); got.Settings.MovieRoot != "/changed" || len(got.Clients) != 1 {
		t.Errorf("env must not override an existing state: %+v", got.Settings)
	}
}

func TestBracketedYearStillMatchesTheTitle(t *testing.T) {
	prof := defaultEngineSettings().Profile
	tg := engineTarget{Kind: "movie", Title: "Night of the Living Dead", Year: 1968}
	for _, name := range []string{
		"Night of the Living Dead (1968) Criterion (1080p BluRay x265 10bit)",
		"Night-of-the-Living-Dead-(1968)-Criterion-(1080p-BluRay)",
	} {
		s := scoreRelease(engineRelease{Title: name, Seeders: 9, Magnet: "magnet:?x"}, tg, prof)
		if len(s.Rejected) != 0 {
			t.Errorf("%s: %v", name, s.Rejected)
		}
	}
	wrong := scoreRelease(engineRelease{Title: "Night of the Living Dead (1990) (1080p BluRay)", Seeders: 9, Magnet: "magnet:?x"}, tg, prof)
	if len(wrong.Rejected) == 0 {
		t.Error("the 1990 remake must not pass for the 1968 film")
	}
}

func TestIndexerTestIsACapsCheckNotASearch(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("t"))
		if r.URL.Query().Get("apikey") != "k" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><caps><searching/></caps>`))
	}))
	t.Cleanup(srv.Close)
	ix := engineIndexer{Name: "T", Kind: "torznab", URL: srv.URL + "/api", APIKey: "k"}
	if err := ix.test(ctxT(t), srv.Client()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "caps" {
		t.Errorf("test should ask for caps only, asked %v", asked)
	}
	ix.APIKey = "bad"
	if err := ix.test(ctxT(t), srv.Client()); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("want key error, got %v", err)
	}
}
