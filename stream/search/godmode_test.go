package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Cinephage -------------------------------------------------------------

func cinephageStub(t *testing.T) *arrStub {
	s := newArrStub(t)
	for _, persona := range []string{"radarr", "sonarr"} {
		app := map[string]string{"radarr": "Radarr", "sonarr": "Sonarr"}[persona]
		s.on("GET /api/"+persona+"/api/v3/system/status", 200, map[string]any{
			"appName": app, "instanceName": "Cinephage", "version": "0.18.1",
		})
		s.on("GET /api/"+persona+"/api/v3/health", 200, []arrHealthEntry{})
	}
	return s
}

func TestCinephageRegistersBothPersonasUnderOneAddress(t *testing.T) {
	s := cinephageStub(t)
	ps := newCinephageProviders(s.config())
	if len(ps) != 2 {
		t.Fatalf("want 2 personas, got %d", len(ps))
	}
	reg := &Registry{}
	for _, p := range ps {
		if err := reg.Add(p); err != nil {
			t.Fatalf("register %s: %v", p.ID(), err)
		}
	}
	if ps[0].ID() != "cinephage-movies" || ps[1].ID() != "cinephage-tv" {
		t.Fatalf("ids: %s, %s", ps[0].ID(), ps[1].ID())
	}
	for _, p := range ps {
		if _, ok := p.(Requester); !ok {
			t.Errorf("%s lost the Requester interface through embedding", p.ID())
		}
		if _, ok := p.(Searcher); !ok {
			t.Errorf("%s lost the Searcher interface through embedding", p.ID())
		}
		if _, ok := p.(bundledEngine); ok {
			t.Errorf("%s is a third-party service, not one of Yarr.It's engines", p.ID())
		}
	}
}

func TestCinephageVersionZeroIsHealthyNotTooOld(t *testing.T) {
	s := cinephageStub(t)
	for _, p := range newCinephageProviders(s.config()) {
		h := p.Health(arrTestCtx(t))
		if h.State != HealthOK {
			t.Errorf("%s: want healthy for Cinephage 0.18.1, got %s (%s)", p.ID(), h.State, h.Detail)
		}
	}
	if len(s.callsTo("GET", "/api/radarr/api/v3/system/status")) == 0 {
		t.Error("films persona did not use Cinephage's /api/radarr surface")
	}
	if len(s.callsTo("GET", "/api/sonarr/api/v3/system/status")) == 0 {
		t.Error("series persona did not use Cinephage's /api/sonarr surface")
	}
}

func TestCinephageCompatLayerOffSaysHowToTurnItOn(t *testing.T) {
	s := newArrStub(t) // no routes: every arr-compat path 404s, as when the toggle is off
	h := newCinephageProviders(s.config())[0].Health(arrTestCtx(t))
	if h.State != HealthIncompatible {
		t.Fatalf("want incompatible, got %s", h.State)
	}
	if !strings.Contains(h.Detail, "Settings → System → General") {
		t.Errorf("detail should say where the toggle is, got %q", h.Detail)
	}
}

func TestCinephageNoKeyPointsAtCinephageNotRadarr(t *testing.T) {
	h := newCinephageProviders(arrConfig{BaseURL: "http://x"})[1].Health(arrTestCtx(t))
	if h.State != HealthNotConfigured || !strings.Contains(h.Detail, "Cinephage") {
		t.Errorf("got %s %q", h.State, h.Detail)
	}
}

// --- request routing -------------------------------------------------------

func TestRequestRoutingSendsSeriesToASeriesProvider(t *testing.T) {
	all := []Provider{
		newRadarrProvider(arrConfig{BaseURL: "http://r"}),
		newSonarrProvider(arrConfig{BaseURL: "http://s"}),
	}
	ref, _ := parseArrRef("tvdb:series:81189")
	ts := requestTargets(all, ref)
	if len(ts) != 1 || ts[0].ID() != "sonarr" {
		t.Fatalf("a series must route to Sonarr, got %v", ids(ts))
	}
	ref, _ = parseArrRef("tmdb:movie:603")
	ts = requestTargets(all, ref)
	if len(ts) != 1 || ts[0].ID() != "radarr" {
		t.Fatalf("a film must route to Radarr, got %v", ids(ts))
	}
}

func ids(ps []Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID()
	}
	return out
}

// --- Seerr -----------------------------------------------------------------

func seerrStub(t *testing.T) *arrStub {
	s := newArrStub(t)
	s.on("GET /api/v1/status", 200, map[string]any{"version": "2.7.3"})
	s.on("GET /api/v1/auth/me", 200, map[string]any{"id": 1})
	s.on("GET /api/v1/request", 200, map[string]any{"results": []map[string]any{
		{"id": 7, "status": 2, "type": "movie", "createdAt": "2026-11-10T00:00:00Z",
			"media":       map[string]any{"tmdbId": 603, "status": 3},
			"requestedBy": map[string]any{"displayName": "Sam"}},
		{"id": 9, "status": 1, "type": "tv", "createdAt": "2026-11-12T00:00:00Z", "is4k": true,
			"media":       map[string]any{"tmdbId": 1399, "tvdbId": 121361, "status": 2},
			"requestedBy": map[string]any{"username": "kid"},
			"seasons":     []map[string]any{{"seasonNumber": 1}, {"seasonNumber": 2}}},
	}})
	s.on("GET /api/v1/movie/603", 200, map[string]any{"title": "The Matrix", "releaseDate": "1999-03-31", "posterPath": "/m.jpg"})
	s.on("GET /api/v1/tv/1399", 200, map[string]any{"name": "Game of Thrones", "firstAirDate": "2011-04-17"})
	return s
}

func TestSeerrHealthy(t *testing.T) {
	s := seerrStub(t)
	h := newSeerrProvider(s.config()).Health(arrTestCtx(t))
	if h.State != HealthOK || h.Version != "2.7.3" {
		t.Fatalf("got %+v", h)
	}
}

func TestSeerrWrongKeyIsAuthFailedEvenThoughStatusIsPublic(t *testing.T) {
	s := seerrStub(t)
	cfg := s.config()
	cfg.APIKey = "wrong"
	// The stub rejects every call without the right key, including /status;
	// Seerr itself leaves /status open, which is why auth/me is probed.
	h := newSeerrProvider(cfg).Health(arrTestCtx(t))
	if h.State != HealthAuthFailed {
		t.Fatalf("want auth_failed, got %s", h.State)
	}
}

func TestSeerrRequestsMapStatusTitlesAndCanonicalIDs(t *testing.T) {
	s := seerrStub(t)
	got, err := newSeerrProvider(s.config()).Requests(arrTestCtx(t), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	m, tv := got[0], got[1]
	if m.Title != "The Matrix" || m.Year != 1999 || m.Status != RequestProcessing || m.Decidable {
		t.Errorf("film: %+v", m)
	}
	if m.CanonicalID != "tmdb:movie:603" || m.ID != "seerr:7" || m.RequestedBy != "Sam" {
		t.Errorf("film ids: %+v", m)
	}
	if !strings.HasPrefix(m.Artwork, "https://image.tmdb.org/") {
		t.Errorf("artwork should be a public TMDB url, got %q", m.Artwork)
	}
	if tv.Title != "Game of Thrones" || tv.Status != RequestPending || !tv.Decidable {
		t.Errorf("series: %+v", tv)
	}
	if tv.CanonicalID != "tvdb:series:121361" || !strings.Contains(tv.Detail, "4K") || !strings.Contains(tv.Detail, "seasons 1, 2") {
		t.Errorf("series detail: %+v", tv)
	}
}

func TestSeerrDeclinedIsFilteredLocally(t *testing.T) {
	s := seerrStub(t)
	got, err := newSeerrProvider(s.config()).Requests(arrTestCtx(t), RequestDeclined, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("no declined requests in the fixture, got %d", len(got))
	}
	if c := s.callsTo("GET", "/api/v1/request"); len(c) != 1 || c[0].Query.Get("filter") != "all" {
		t.Errorf("declined has no Seerr filter word and must fetch all, got %+v", c)
	}
}

func TestSeerrDecidePostsTheRightVerb(t *testing.T) {
	s := seerrStub(t)
	s.on("POST /api/v1/request/9/approve", 200, map[string]any{"id": 9})
	s.on("POST /api/v1/request/9/decline", 200, map[string]any{"id": 9})
	p := newSeerrProvider(s.config())
	if err := p.Decide(arrTestCtx(t), "9", true); err != nil {
		t.Fatal(err)
	}
	if err := p.Decide(arrTestCtx(t), "9", false); err != nil {
		t.Fatal(err)
	}
	if len(s.callsTo("POST", "/api/v1/request/9/approve")) != 1 || len(s.callsTo("POST", "/api/v1/request/9/decline")) != 1 {
		t.Error("approve/decline did not reach Seerr")
	}
	if err := p.Decide(arrTestCtx(t), "../settings", true); err == nil {
		t.Error("a non-numeric id must be refused before it becomes a path")
	}
}

// --- GG Requestz -----------------------------------------------------------

func ggrStub(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"1.4.0"}`))
		case "/api/request":
			if r.Header.Get("Authorization") != "Bearer ggr-key" {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"success":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"requests":[
				{"id":3,"title":"Chrono Trigger","status":"rejected","request_type":"game","platforms":["SNES"],"created_at":"2026-11-01T00:00:00Z"},
				{"id":4,"title":"Okami","status":"pending","request_type":"game","platforms":[{"name":"PS2"},{"name":"Wii"}],"created_at":"2026-11-02T00:00:00Z"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGGRequestzHealthAndBearerAuth(t *testing.T) {
	srv := ggrStub(t)
	if h := newGGRProvider(srv.URL, "ggr-key", srv.Client()).Health(arrTestCtx(t)); h.State != HealthOK || h.Version != "1.4.0" {
		t.Fatalf("got %+v", h)
	}
	if h := newGGRProvider(srv.URL, "nope", srv.Client()).Health(arrTestCtx(t)); h.State != HealthAuthFailed {
		t.Fatalf("want auth_failed, got %+v", h)
	}
	if h := newGGRProvider(srv.URL, "", srv.Client()).Health(arrTestCtx(t)); h.State != HealthNotConfigured {
		t.Fatalf("want not_configured, got %+v", h)
	}
}

func TestGGRequestzRequestsSpeakTheSharedVocabulary(t *testing.T) {
	srv := ggrStub(t)
	got, err := newGGRProvider(srv.URL, "ggr-key", srv.Client()).Requests(arrTestCtx(t), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Status != RequestDeclined || got[1].Status != RequestPending {
		t.Fatalf("got %+v", got)
	}
	if got[1].Detail != "PS2, Wii" || got[0].Detail != "SNES" || got[1].Domain != "game" {
		t.Errorf("platforms/domain: %+v", got)
	}
	for _, r := range got {
		if r.Decidable {
			t.Errorf("GG Requestz keeps approval behind a session; %s must not offer a decision", r.ID)
		}
	}
}

func TestGGRequestzIsNotADecider(t *testing.T) {
	var p Provider = newGGRProvider("http://x", "k", nil)
	if _, ok := p.(RequestDecider); ok {
		t.Error("GG Requestz must not claim RequestDecider until its API allows it")
	}
}

// --- the unified queue -----------------------------------------------------

func TestUnifiedQueueMergesPendingFirstAndNamesFailures(t *testing.T) {
	s := seerrStub(t)
	g := ggrStub(t)
	reg := &Registry{}
	_ = reg.Add(newSeerrProvider(s.config()))
	_ = reg.Add(newGGRProvider(g.URL, "ggr-key", g.Client()))
	dead := newGGRProvider("http://127.0.0.1:1", "k", nil)
	dead.id = "ggrequestz-dead"
	_ = reg.Add(dead)

	v := (&requestsAPI{reg: reg}).collect(arrTestCtx(t), "", 50)
	if len(v.Items) != 4 {
		t.Fatalf("want 4 merged, got %d: %+v", len(v.Items), v.Items)
	}
	if v.Items[0].Status != RequestPending || v.Items[1].Status != RequestPending {
		t.Errorf("pending must come first: %+v", v.Items)
	}
	if len(v.Failed) != 1 || v.Failed[0] != "ggrequestz-dead" {
		t.Errorf("failed should name the dead backend, got %v", v.Failed)
	}
}

func TestDecideRoutesByIDAndRefusesWhatABackendCannotDo(t *testing.T) {
	s := seerrStub(t)
	s.on("POST /api/v1/request/9/approve", 200, nil)
	reg := &Registry{}
	_ = reg.Add(newSeerrProvider(s.config()))
	_ = reg.Add(newGGRProvider("http://x", "k", nil))
	api := &requestsAPI{reg: reg}

	cases := []struct {
		body string
		want int
	}{
		{`{"id":"seerr:9","action":"approve"}`, 200},
		{`{"id":"ggrequestz:4","action":"approve"}`, 409},
		{`{"id":"nobody:1","action":"approve"}`, 404},
		{`{"id":"seerr:9","action":"maybe"}`, 400},
		{`{"id":"seerr","action":"approve"}`, 400},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		api.handleDecide(rec, httptest.NewRequest("POST", "/api/requests/decide", strings.NewReader(c.body)))
		if rec.Code != c.want {
			t.Errorf("%s: want %d, got %d (%s)", c.body, c.want, rec.Code, rec.Body.String())
		}
	}
}

func TestListRejectsUnknownStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	(&requestsAPI{reg: &Registry{}}).handleList(rec, httptest.NewRequest("GET", "/api/requests?status=bogus", nil))
	if rec.Code != 400 {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

// --- stack topology --------------------------------------------------------


func TestNewRolesAndCapabilitiesAreInTheSchema(t *testing.T) {
	known := map[string]bool{}
	for _, c := range schema.Capabilities {
		known[c] = true
	}
	for _, p := range []Provider{
		newSeerrProvider(arrConfig{BaseURL: "http://x"}),
		newGGRProvider("http://x", "", nil),
	} {
		for _, c := range p.Capabilities() {
			if !known[c] {
				t.Errorf("%s claims capability %q, not in schema.json", p.ID(), c)
			}
		}
		if err := (&Registry{}).Add(p); err != nil {
			t.Errorf("%s: %v", p.ID(), err)
		}
	}
}
