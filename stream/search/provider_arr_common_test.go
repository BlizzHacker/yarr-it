package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fixture harness -------------------------------------------------------

// arrStub is a fake *arr. Every test in this package runs against one, so
// `go test` never touches the network: the real instances live on a home LAN
// that CI has no route to, and a suite that only passes at Wade's desk is not
// a suite.
type arrStub struct {
	t      *testing.T
	srv    *httptest.Server
	apiKey string

	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	calls  []arrCall
}

type arrCall struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
}

func newArrStub(t *testing.T) *arrStub {
	t.Helper()
	s := &arrStub{t: t, apiKey: "test-key", routes: map[string]http.HandlerFunc{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *arrStub) serve(w http.ResponseWriter, r *http.Request) {
	// Auth is checked here rather than per-route because every *arr checks it
	// the same way, and an adapter that forgot the header must fail loudly in
	// every test rather than in one.
	if s.apiKey != "" && r.Header.Get("X-Api-Key") != s.apiKey {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"Unauthorized"}`)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

	s.mu.Lock()
	s.calls = append(s.calls, arrCall{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body,
	})
	h, ok := s.routes[r.Method+" "+r.URL.Path]
	if !ok {
		h, ok = s.routes["* "+r.URL.Path]
	}
	s.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"no such route in the stub"}`)
		return
	}
	// Re-attach the body so handlers that want it can read it.
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	h(w, r)
}

func (s *arrStub) on(methodAndPath string, status int, body any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[methodAndPath] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}
}

func (s *arrStub) onFunc(methodAndPath string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[methodAndPath] = h
}

func (s *arrStub) callsTo(method, path string) []arrCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []arrCall
	for _, c := range s.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func (s *arrStub) config() arrConfig {
	return arrConfig{BaseURL: s.srv.URL, APIKey: s.apiKey, HTTPClient: s.srv.Client()}
}

// status wires the system/status document every health check starts from.
func (s *arrStub) status(api, app, version string) {
	s.on("GET /api/"+api+"/system/status", 200, map[string]any{
		"appName": app, "instanceName": app, "version": version,
	})
}

func (s *arrStub) healthEntries(api string, entries ...arrHealthEntry) {
	if entries == nil {
		entries = []arrHealthEntry{}
	}
	s.on("GET /api/"+api+"/health", 200, entries)
}

func arrTestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// --- health: the six states, one test each ---------------------------------

// The whole point of this file. Collapsing these into "down" is what sends
// someone hunting a network fault when the real answer was an empty API key
// field, and the schema keeps them apart specifically so the UI can too.

func TestArrHealthNotConfiguredWhenNoKey(t *testing.T) {
	p := newRadarrProvider(arrConfig{BaseURL: "http://192.0.2.1:7878"})
	h := p.Health(arrTestCtx(t))
	if h.State != HealthNotConfigured {
		t.Fatalf("state = %q, want not_configured", h.State)
	}
	if !strings.Contains(h.Detail, "API key") {
		t.Errorf("detail must say what to do, got %q", h.Detail)
	}
}

func TestArrHealthUnreachableWhenNothingAnswers(t *testing.T) {
	stub := newArrStub(t)
	cfg := stub.config()
	stub.srv.Close() // Refused from here on.

	p := newRadarrProvider(cfg)
	h := p.Health(arrTestCtx(t))
	if h.State != HealthUnreachable {
		t.Fatalf("state = %q, want unreachable (detail %q)", h.State, h.Detail)
	}
}

func TestArrHealthAuthFailedOn401And403(t *testing.T) {
	for _, code := range []int{401, 403} {
		stub := newArrStub(t)
		stub.on("GET /api/v3/system/status", code, map[string]string{"error": "no"})
		// The stub's own key check would answer 401 first, so disable it and
		// let the route decide.
		stub.apiKey = ""
		cfg := stub.config()
		cfg.APIKey = "wrong-key"

		p := newRadarrProvider(cfg)
		h := p.Health(arrTestCtx(t))
		if h.State != HealthAuthFailed {
			t.Errorf("HTTP %d gave %q, want auth_failed", code, h.State)
		}
	}
}

func TestArrHealthIncompatibleWhenApiVersionIsAbsent(t *testing.T) {
	stub := newArrStub(t)
	// Nothing registered at /api/v3/system/status, so the stub 404s -- exactly
	// what an *arr too old to have this API does.
	p := newRadarrProvider(stub.config())
	h := p.Health(arrTestCtx(t))
	if h.State != HealthIncompatible {
		t.Fatalf("state = %q, want incompatible", h.State)
	}
}

func TestArrHealthIncompatibleWhenItIsADifferentApplication(t *testing.T) {
	stub := newArrStub(t)
	// A very common misconfiguration: Radarr's entry pointed at Sonarr's port.
	// It authenticates perfectly and then fails on every noun.
	stub.status("v3", "Sonarr", "4.0.19.2979")

	p := newRadarrProvider(stub.config())
	h := p.Health(arrTestCtx(t))
	if h.State != HealthIncompatible {
		t.Fatalf("state = %q, want incompatible", h.State)
	}
	if !strings.Contains(h.Detail, "Sonarr") || !strings.Contains(h.Detail, "Radarr") {
		t.Errorf("detail should name both applications, got %q", h.Detail)
	}
}

func TestArrHealthIncompatibleBelowTheMinimumVersion(t *testing.T) {
	stub := newArrStub(t)
	stub.status("v3", "Radarr", "2.0.0.5000") // Before /api/v3 existed.

	p := newRadarrProvider(stub.config())
	h := p.Health(arrTestCtx(t))
	if h.State != HealthIncompatible {
		t.Fatalf("state = %q, want incompatible", h.State)
	}
	if h.Version != "2.0.0.5000" {
		t.Errorf("version = %q; the operator needs to see which version was rejected", h.Version)
	}
}

func TestArrHealthDegradedWhenTheInstanceReportsAnError(t *testing.T) {
	stub := newArrStub(t)
	stub.status("v3", "Sonarr", "4.0.19.2979")
	stub.healthEntries("v3",
		arrHealthEntry{Source: "RootFolderCheck", Type: "error", Message: "Missing root folder: /mnt/lvm_shared/TV_Show"},
	)

	p := newSonarrProvider(stub.config())
	h := p.Health(arrTestCtx(t))
	if h.State != HealthDegraded {
		t.Fatalf("state = %q, want degraded", h.State)
	}
	if !strings.Contains(h.Detail, "Missing root folder") {
		t.Errorf("detail must carry the instance's own words, got %q", h.Detail)
	}
}

func TestArrHealthHealthyWithWarnings(t *testing.T) {
	stub := newArrStub(t)
	stub.status("v3", "Radarr", "6.2.1.10461")
	stub.healthEntries("v3",
		arrHealthEntry{Source: "UpdateCheck", Type: "warning", Message: "New update is available: v6.3.0.10514"},
	)

	p := newRadarrProvider(stub.config())
	h := p.Health(arrTestCtx(t))
	// A pending update is not a fault. Promoting warnings to degraded leaves
	// every instance permanently amber, and an alert that is always on is an
	// alert nobody reads.
	if h.State != HealthOK {
		t.Fatalf("state = %q, want healthy", h.State)
	}
	if h.Version != "6.2.1.10461" {
		t.Errorf("version = %q", h.Version)
	}
	if !strings.Contains(h.Detail, "New update") {
		t.Errorf("the warning should still be reported, got detail %q", h.Detail)
	}
}

func TestArrHealthHealthyWithNothingToSay(t *testing.T) {
	stub := newArrStub(t)
	stub.status("v1", "Lidarr", "3.1.0.4875")
	stub.healthEntries("v1")

	p := newLidarrProvider(stub.config())
	h := p.Health(arrTestCtx(t))
	if h.State != HealthOK || h.Detail != "" {
		t.Fatalf("state = %q detail = %q, want a clean healthy", h.State, h.Detail)
	}
}

func TestArrHealthDegradedWhenTheHealthEndpointItselfFails(t *testing.T) {
	stub := newArrStub(t)
	stub.status("v3", "Radarr", "6.2.1.10461")
	stub.on("GET /api/v3/health", 500, map[string]string{"error": "boom"})

	p := newRadarrProvider(stub.config())
	h := p.Health(arrTestCtx(t))
	// It answered a moment ago, so this is not an outage.
	if h.State != HealthDegraded {
		t.Fatalf("state = %q, want degraded", h.State)
	}
}

// A health probe must not outlive the page it is painting.
func TestArrHealthRespectsAShortContext(t *testing.T) {
	stub := newArrStub(t)
	stub.onFunc("GET /api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	p := newRadarrProvider(stub.config())
	start := time.Now()
	h := p.Health(ctx)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("health took %v; it ignored the caller's deadline", elapsed)
	}
	if h.State != HealthUnreachable {
		t.Errorf("state = %q, want unreachable after a timeout", h.State)
	}
}

// --- registration ----------------------------------------------------------

// The bug this whole contract exists to prevent, checked against the real
// adapters rather than a stand-in: a provider claiming "movies" is refused, so
// these three had better claim the canonical words.
func TestArrAdaptersRegisterWithCanonicalDomains(t *testing.T) {
	r := &Registry{}
	for _, p := range []Provider{
		newRadarrProvider(arrConfig{ID: "radarr", Name: "Radarr", BaseURL: "http://x"}),
		newSonarrProvider(arrConfig{ID: "sonarr", Name: "Sonarr", BaseURL: "http://x"}),
		newLidarrProvider(arrConfig{ID: "lidarr", Name: "Lidarr", BaseURL: "http://x"}),
	} {
		if err := r.Add(p); err != nil {
			t.Fatalf("registering %s: %v", p.ID(), err)
		}
	}
	if got := r.Domains(); len(got) != 2 || got[0] != "music" || got[1] != "video" {
		t.Errorf("Domains() = %v, want [music video]", got)
	}
	// And they must be reachable by the words a caller would actually use.
	for _, spelling := range []string{"movies", "tv", "video", "anime"} {
		if len(r.For(spelling, "acquisition")) != 2 {
			t.Errorf("For(%q, acquisition) found %d video providers, want 2",
				spelling, len(r.For(spelling, "acquisition")))
		}
	}
	for _, spelling := range []string{"music", "audio", "album"} {
		if len(r.For(spelling, "acquisition")) != 1 {
			t.Errorf("For(%q, acquisition) found %d music providers, want 1",
				spelling, len(r.For(spelling, "acquisition")))
		}
	}
}

// Two Sonarrs is the anime case, and it must register cleanly.
func TestArrTwoSonarrInstancesCoexist(t *testing.T) {
	r := &Registry{}
	if err := r.Add(newSonarrProvider(arrConfig{ID: "sonarr", Name: "Sonarr", BaseURL: "http://a"})); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(newSonarrProvider(arrConfig{
		ID: "sonarr-anime", Name: "Sonarr Anime", BaseURL: "http://b", SeriesType: "anime",
	})); err != nil {
		t.Fatalf("a second Sonarr was refused: %v", err)
	}
	if got := len(r.For("video", "acquisition")); got != 2 {
		t.Errorf("For(video, acquisition) = %d, want 2", got)
	}
}

// The point of the optional-interface design. None of these three serve bytes
// or know a schedule, so none of them should satisfy those interfaces -- there
// must be no method to stub out with an error nobody reads.
func TestArrAdaptersDoNotClaimWhatTheyCannotDo(t *testing.T) {
	for _, p := range []Provider{
		newRadarrProvider(arrConfig{BaseURL: "http://x"}),
		newSonarrProvider(arrConfig{BaseURL: "http://x"}),
		newLidarrProvider(arrConfig{BaseURL: "http://x"}),
	} {
		if _, ok := p.(Streamer); ok {
			t.Errorf("%s satisfies Streamer; it serves no bytes", p.Name())
		}
		if _, ok := p.(ChannelProvider); ok {
			t.Errorf("%s satisfies ChannelProvider", p.Name())
		}
		if _, ok := p.(GuideProvider); ok {
			t.Errorf("%s satisfies GuideProvider", p.Name())
		}
		for _, want := range []any{(*Searcher)(nil), (*LibraryProvider)(nil), (*Requester)(nil), (*ActivityProvider)(nil)} {
			_ = want
		}
		if _, ok := p.(Searcher); !ok {
			t.Errorf("%s does not satisfy Searcher", p.Name())
		}
		if _, ok := p.(LibraryProvider); !ok {
			t.Errorf("%s does not satisfy LibraryProvider", p.Name())
		}
		if _, ok := p.(Requester); !ok {
			t.Errorf("%s does not satisfy Requester", p.Name())
		}
		if _, ok := p.(ActivityProvider); !ok {
			t.Errorf("%s does not satisfy ActivityProvider", p.Name())
		}
		if _, ok := p.(arrDetailer); !ok {
			t.Errorf("%s does not satisfy arrDetailer", p.Name())
		}
	}
}

// Every capability advertised has to be a word schema.json defines, or a
// client reading the list cannot act on it.
func TestArrCapabilitiesAreAllInTheSchema(t *testing.T) {
	known := map[string]bool{}
	for _, c := range schema.Capabilities {
		known[c] = true
	}
	for _, p := range []Provider{
		newRadarrProvider(arrConfig{BaseURL: "http://x"}),
		newSonarrProvider(arrConfig{BaseURL: "http://x"}),
		newLidarrProvider(arrConfig{BaseURL: "http://x"}),
	} {
		for _, c := range p.Capabilities() {
			if !known[c] {
				t.Errorf("%s claims capability %q, which is not in schema.json", p.Name(), c)
			}
		}
	}
}

// --- canonical ids ---------------------------------------------------------

func TestArrCanonicalIDsRoundTrip(t *testing.T) {
	cases := []struct {
		id      string
		typ     string
		season  int
		episode int
		domain  string
	}{
		{arrMovieID(78), "movie", 0, 0, "video"},
		{arrSeriesID(78874, false), "series", 0, 0, "video"},
		{arrSeriesID(78874, true), "anime", 0, 0, "video"},
		{arrSeasonID(78874, 2), "season", 2, 0, "video"},
		{arrEpisodeID(78874, 2, 5), "episode", 2, 5, "video"},
		{arrMusicID("artist", "a74b1b7f-71a5-4011-9441-d0b5e4122711"), "artist", 0, 0, "music"},
		{arrMusicID("album", "e75c0549-ad55-39e3-8025-c72c5d4a3c5d"), "album", 0, 0, "music"},
		{arrMusicID("track", "a46aae44-3441-39fb-945a-cce752349138"), "track", 0, 0, "music"},
	}
	for _, c := range cases {
		ref, err := parseArrRef(c.id)
		if err != nil {
			t.Errorf("parse(%q): %v", c.id, err)
			continue
		}
		if ref.Type != c.typ || ref.Season != c.season || ref.Episode != c.episode {
			t.Errorf("parse(%q) = %+v, want type %q season %d episode %d",
				c.id, ref, c.typ, c.season, c.episode)
		}
		// The type in an id must resolve to the domain the provider claims, or
		// routing sends it to the wrong instance.
		if got := canonicalDomain(ref.Type); got != c.domain {
			t.Errorf("type %q in %q resolves to domain %q, want %q", ref.Type, c.id, got, c.domain)
		}
	}
}

func TestArrCanonicalIDsRejectNonsense(t *testing.T) {
	for _, bad := range []string{
		"", "78", "tmdb:78", "tmdb::78", ":movie:78",
		"tmdb:widget:78",             // a type no client can render
		"tvdb:season:78874",          // season id with no season
		"tvdb:episode:78874:2",       // episode id with no episode
		"tvdb:season:78874:notanint", // unparseable
	} {
		if _, err := parseArrRef(bad); err == nil {
			t.Errorf("parse(%q) was accepted; it names nothing", bad)
		}
	}
}

// --- queue and state vocabulary --------------------------------------------

func TestArrQueueStageAndState(t *testing.T) {
	cases := []struct {
		name  string
		rec   arrQueueRecord
		stage string
		state LibraryState
	}{
		{"plain download", arrQueueRecord{Status: "downloading", TrackedDownloadState: "downloading", TrackedDownloadStatus: "ok"}, "downloading", StateDownloading},
		{"waiting to import", arrQueueRecord{Status: "completed", TrackedDownloadState: "importPending", TrackedDownloadStatus: "ok"}, "importing", StateImporting},
		{"importing", arrQueueRecord{Status: "completed", TrackedDownloadState: "importing", TrackedDownloadStatus: "ok"}, "importing", StateImporting},
		{"blocked import", arrQueueRecord{Status: "completed", TrackedDownloadState: "importBlocked", TrackedDownloadStatus: "warning"}, "importing", StateImporting},
		{"queued", arrQueueRecord{Status: "queued", TrackedDownloadState: "downloading"}, "queued", StateDownloading},
		{"paused", arrQueueRecord{Status: "paused", TrackedDownloadState: "downloading"}, "paused", StateDownloading},
		// A failed grab is still an outstanding request: the item is monitored
		// and will be retried, so offering a fresh request duplicates work.
		{"failed", arrQueueRecord{Status: "failed", TrackedDownloadState: "failed", TrackedDownloadStatus: "error"}, "failed", StateRequested},
	}
	for _, c := range cases {
		if got := arrQueueStage(c.rec); got != c.stage {
			t.Errorf("%s: stage = %q, want %q", c.name, got, c.stage)
		}
		if got := arrQueueLibraryState(c.rec); got != c.state {
			t.Errorf("%s: state = %q, want %q", c.name, got, c.state)
		}
	}
}

func TestArrQueueProgressIsClamped(t *testing.T) {
	cases := []struct {
		rec  arrQueueRecord
		want float64
	}{
		{arrQueueRecord{Size: 100, SizeLeft: 25}, 0.75},
		{arrQueueRecord{Size: 100, SizeLeft: 0}, 1},
		{arrQueueRecord{Size: 0, SizeLeft: 0}, 0},     // no size reported yet
		{arrQueueRecord{Size: 100, SizeLeft: 200}, 0}, // nonsense from the client
		{arrQueueRecord{Size: 100, SizeLeft: -50}, 1}, // ditto
	}
	for _, c := range cases {
		if got := arrQueueProgress(c.rec); got != c.want {
			t.Errorf("progress(size %v left %v) = %v, want %v", c.rec.Size, c.rec.SizeLeft, got, c.want)
		}
	}
}

// The five states a UI must be able to tell apart, exercised as a table so a
// future change that collapses two of them fails here first.
func TestArrLibraryStateCoversAllFiveDistinctions(t *testing.T) {
	dl := &arrQueueRecord{Status: "downloading", TrackedDownloadState: "downloading"}
	imp := &arrQueueRecord{Status: "completed", TrackedDownloadState: "importing"}

	leaf := []struct {
		name      string
		inLibrary bool
		hasFile   bool
		monitored bool
		queued    *arrQueueRecord
		want      LibraryState
	}{
		{"never heard of it", false, false, false, nil, StateMissing},
		{"tracked, unmonitored, nothing happening", true, false, false, nil, StateMissing},
		{"monitored and searching", true, false, true, nil, StateRequested},
		{"downloading", true, false, true, dl, StateDownloading},
		{"importing", true, false, true, imp, StateImporting},
		{"on disk", true, true, true, nil, StateAvailable},
	}
	for _, c := range leaf {
		if got := arrLeafState(c.inLibrary, c.hasFile, c.monitored, c.queued); got != c.want {
			t.Errorf("leaf %s = %q, want %q", c.name, got, c.want)
		}
	}

	container := []struct {
		name      string
		inLibrary bool
		files     int
		monitored bool
		queued    *arrQueueRecord
		want      LibraryState
	}{
		{"never heard of it", false, 0, true, nil, StateMissing},
		{"added, nothing yet, unmonitored", true, 0, false, nil, StateMissing},
		{"added and monitored", true, 0, true, nil, StateRequested},
		{"first episode downloading", true, 0, true, dl, StateDownloading},
		{"first episode importing", true, 0, true, imp, StateImporting},
		// Partial content is still watchable tonight, and that is the question
		// a card answers.
		{"half the season on disk", true, 4, true, dl, StateAvailable},
		{"complete", true, 8, true, nil, StateAvailable},
	}
	for _, c := range container {
		if got := arrContainerState(c.inLibrary, c.files, c.monitored, c.queued); got != c.want {
			t.Errorf("container %s = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestArrPosterPrefersTheRemoteURL(t *testing.T) {
	// The local URL is relative to an instance on the home LAN. A phone on
	// mobile data renders it as a broken image and reports no error at all.
	got := arrPoster([]arrImage{
		{CoverType: "fanart", URL: "/MediaCover/1/fanart.jpg", RemoteURL: "https://example.test/fanart.jpg"},
		{CoverType: "poster", URL: "/MediaCover/1/poster.jpg", RemoteURL: "https://example.test/poster.jpg"},
	})
	if got != "https://example.test/poster.jpg" {
		t.Errorf("arrPoster = %q, want the remote poster", got)
	}
	if got := arrPoster([]arrImage{{CoverType: "poster", URL: "/local.jpg"}}); got != "" {
		t.Errorf("a local-only image gave %q; it would render as a broken image off-LAN", got)
	}
}

func TestArrMajorVersion(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"6.2.1.10461", 6, true},
		{"4.0.19.2979", 4, true},
		{"3", 3, true},
		{"", 0, false},
		{"unknown", 0, false},
		{"v6.2", 0, false},
	} {
		got, ok := arrMajorVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("arrMajorVersion(%q) = %d,%v want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// --- configuration ---------------------------------------------------------

func TestArrConfigsFromEnvironment(t *testing.T) {
	t.Setenv("SONARR_URL", "http://192.0.2.10:8989")
	t.Setenv("SONARR_API_KEY", "aaa")
	t.Setenv("SONARR_INSTANCES", "anime, 4k")
	t.Setenv("SONARR_ANIME_URL", "http://192.0.2.11:8989")
	t.Setenv("SONARR_ANIME_API_KEY", "bbb")
	t.Setenv("SONARR_ANIME_SERIES_TYPE", "anime")
	// 4k has no URL, so it is not a configured instance at all.

	got := arrConfigsFor("SONARR")
	if len(got) != 2 {
		t.Fatalf("got %d configs, want 2 (the third has no URL)", len(got))
	}
	if got[0].ID != "sonarr" || got[1].ID != "sonarr-anime" {
		t.Errorf("ids = %q, %q", got[0].ID, got[1].ID)
	}
	if got[1].SeriesType != "anime" {
		t.Errorf("the anime instance did not pick up its series type: %q", got[1].SeriesType)
	}
}

// A URL with no key still yields a provider. Dropping it would make the
// settings screen show nothing, which looks like "never configured" and leaves
// the user nowhere to type the key that would fix it.
func TestArrProviderIsBuiltEvenWithoutAKey(t *testing.T) {
	t.Setenv("RADARR_URL", "http://192.0.2.10:7878")
	t.Setenv("RADARR_API_KEY", "")
	t.Setenv("SONARR_URL", "")
	t.Setenv("LIDARR_URL", "")
	t.Setenv("RADARR_INSTANCES", "")

	ps := arrProvidersFromEnv()
	if len(ps) != 1 {
		t.Fatalf("got %d providers, want the unconfigured Radarr", len(ps))
	}
	if h := ps[0].Health(arrTestCtx(t)); h.State != HealthNotConfigured {
		t.Errorf("state = %q, want not_configured", h.State)
	}
}

// --- routes ----------------------------------------------------------------

func arrRouteRegistry(t *testing.T, ps ...Provider) *Registry {
	t.Helper()
	r := &Registry{}
	for _, p := range ps {
		if err := r.Add(p); err != nil {
			t.Fatalf("registering %s: %v", p.ID(), err)
		}
	}
	return r
}

func TestRegisterArrRoutesServesSearch(t *testing.T) {
	stub := newArrStub(t)
	stub.status("v3", "Radarr", "6.2.1.10461")
	stub.on("GET /api/v3/movie/lookup", 200, []radarrMovie{
		{ID: 0, Title: "The Blade Runner Phenomenon", Year: 2021, TmdbID: 833898},
	})
	stub.on("GET /api/v3/queue", 200, radarrQueuePage{})

	cfg := stub.config()
	cfg.ID, cfg.Name = "radarr", "Radarr"
	reg := arrRouteRegistry(t, newRadarrProvider(cfg))

	mux := http.NewServeMux()
	registerArrRoutes(mux, reg)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/arr/search?q=blade+runner&domain=movies", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got arrFanoutResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Domain != "video" || got.Items[0].Type != "movie" {
		t.Fatalf("items = %+v", got.Items)
	}
	// "movies" on the way in must reach a provider that claims "video".
	if got.Items[0].CanonicalID != "tmdb:movie:833898" {
		t.Errorf("canonicalId = %q", got.Items[0].CanonicalID)
	}
}

func TestRegisterArrRoutesNamesProvidersItCouldNotAsk(t *testing.T) {
	stub := newArrStub(t)
	cfg := stub.config()
	cfg.ID, cfg.Name = "radarr", "Radarr"
	stub.srv.Close() // Every call now refused.

	reg := arrRouteRegistry(t, newRadarrProvider(cfg))
	mux := http.NewServeMux()
	registerArrRoutes(mux, reg)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/arr/search?q=dune", nil))

	var got arrFanoutResult
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Failed) != 1 || got.Failed[0] != "radarr" {
		t.Errorf("failed = %v; an empty list from a dead provider must not look like an answer", got.Failed)
	}
	if got.Items == nil {
		t.Error("items must be an empty list, not null -- clients iterate it")
	}
}

// With a Radarr and a Radarr-4K, the film is available if either has it.
// Answering from whichever instance was asked first is how a household ends up
// with two copies.
func TestArrStatusRouteTakesTheStrongestAnswer(t *testing.T) {
	have := newArrStub(t)
	have.status("v3", "Radarr", "6.2.1.10461")
	have.on("GET /api/v3/movie", 200, []radarrMovie{{ID: 587, Title: "Blade Runner", TmdbID: 78, HasFile: true, MovieFileID: 4927}})
	have.on("GET /api/v3/queue", 200, radarrQueuePage{})

	havent := newArrStub(t)
	havent.status("v3", "Radarr", "6.2.1.10461")
	havent.on("GET /api/v3/movie", 200, []radarrMovie{})
	havent.on("GET /api/v3/queue", 200, radarrQueuePage{})

	a := have.config()
	a.ID, a.Name = "radarr", "Radarr"
	b := havent.config()
	b.ID, b.Name = "radarr-4k", "Radarr 4K"

	reg := arrRouteRegistry(t, newRadarrProvider(a), newRadarrProvider(b))
	mux := http.NewServeMux()
	registerArrRoutes(mux, reg)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/arr/status?id=tmdb:movie:78", nil))

	var got struct {
		State LibraryState `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got.State != StateAvailable {
		t.Errorf("state = %q, want available -- one instance has it", got.State)
	}
}

func TestArrRequestRouteRejectsAnUnroutableID(t *testing.T) {
	reg := arrRouteRegistry(t)
	mux := http.NewServeMux()
	registerArrRoutes(mux, reg)

	req := httptest.NewRequest("POST", "/api/arr/request",
		strings.NewReader(`{"canonicalId":"tmdb:movie:78"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status %d, want 404 when nothing can obtain it", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/arr/request", strings.NewReader(`{"canonicalId":"nonsense"}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status %d, want 400 for an id that names nothing", rec.Code)
	}
}

// An absent "monitor" is a request; an explicit false is a watchlist add. If
// those collapsed, either every request would silently download or none would.
func TestArrRequestRouteDefaultsToMonitoring(t *testing.T) {
	seen := make(chan bool, 4)
	reg := arrRouteRegistry(t, &arrRecordingRequester{
		fakeProvider: fakeProvider{
			id: "radarr", name: "Radarr", domains: []string{"video"},
			roles: []string{"acquisition"}, caps: []string{"request"},
		},
		monitor: seen,
	})
	mux := http.NewServeMux()
	registerArrRoutes(mux, reg)

	for _, body := range []string{
		`{"canonicalId":"tmdb:movie:78"}`,
		`{"canonicalId":"tmdb:movie:78","monitor":false}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/arr/request", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d for %s", rec.Code, body)
		}
	}
	if got := <-seen; got != true {
		t.Error("a request with no opinion did not monitor; nothing would ever download")
	}
	if got := <-seen; got != false {
		t.Error("an explicit monitor:false still monitored; a watchlist add would start a download")
	}
}

type arrRecordingRequester struct {
	fakeProvider
	monitor chan bool
}

func (p *arrRecordingRequester) Request(_ context.Context, _ MediaItem, opts RequestOptions) (RequestResult, error) {
	p.monitor <- opts.Monitor
	return RequestResult{Accepted: true, Detail: fmt.Sprintf("monitor=%v", opts.Monitor)}, nil
}

// --- live estate check -----------------------------------------------------

// Everything above runs against httptest and never touches a network. This one
// deliberately does, and is skipped unless asked for:
//
//	YARRIT_ARR_LIVE=1 RADARR_URL=… RADARR_API_KEY=… go test -run TestArrLiveEstate -v
//
// It exists because a fixture proves the adapter matches what I *believed* the
// API returns. Only a real Radarr proves it matches what Radarr actually sends
// -- and the two have differed here already: a lookup result carries
// movieFileId but no hasFile at all, and Sonarr zeroes a show's statistics in
// lookup responses even for shows it owns.
//
// Set YARRIT_ARR_LIVE_WRITE=1 as well to prove the request path end to end. It
// adds one item unmonitored with no search -- so nothing is ever downloaded --
// confirms the row exists, deletes it with deleteFiles=false and no import-list
// exclusion, and confirms it is gone. The deletion is deferred, so a failure
// part way through still cleans up.
func TestArrLiveEstate(t *testing.T) {
	if os.Getenv("YARRIT_ARR_LIVE") != "1" {
		t.Skip("set YARRIT_ARR_LIVE=1 to check against real instances")
	}
	providers := arrProvidersFromEnv()
	if len(providers) == 0 {
		t.Fatal("no *arr configured; set RADARR_URL/RADARR_API_KEY and friends")
	}
	write := os.Getenv("YARRIT_ARR_LIVE_WRITE") == "1"

	for _, p := range providers {
		p := p
		t.Run(p.ID(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()

			h := p.Health(ctx)
			t.Logf("PROVIDER  %s (%s)", p.Name(), p.ID())
			t.Logf("VERSION   %s", h.Version)
			t.Logf("HEALTH    %s — %s", h.State, h.Detail)
			if h.State == HealthUnreachable || h.State == HealthNotConfigured ||
				h.State == HealthAuthFailed || h.State == HealthIncompatible {
				t.Fatalf("cannot exercise %s: %s", p.ID(), h.State)
			}

			term := os.Getenv(strings.ToUpper(strings.ReplaceAll(p.ID(), "-", "_")) + "_LIVE_TERM")
			if term == "" {
				if containsDomain(p.Domains(), "music") {
					term = "radiohead"
				} else {
					term = "blade runner"
				}
			}

			hits, err := p.(Searcher).Search(ctx, term, p.Domains()[0])
			if err != nil {
				t.Fatalf("SEARCH    failed: %v", err)
			}
			t.Logf("SEARCH    %q -> %d results", term, len(hits))
			for i, it := range hits {
				if i == 3 {
					break
				}
				t.Logf("            %-10s %-28s %-12s %s", it.Type, arrTrunc(it.Title, 28), it.State, it.CanonicalID)
			}
			if len(hits) == 0 {
				t.Fatal("SEARCH    returned nothing; nothing else can be checked")
			}

			lib, err := p.(LibraryProvider).Library(ctx, "")
			if err != nil {
				t.Fatalf("LIBRARY   failed: %v", err)
			}
			t.Logf("LIBRARY   %d items", len(lib))

			// Details of something actually owned, so the child level is real.
			detailID := hits[0].CanonicalID
			for _, it := range lib {
				if it.State == StateAvailable {
					detailID = it.CanonicalID
					break
				}
			}
			item, children, err := p.(arrDetailer).Details(ctx, detailID)
			if err != nil {
				t.Fatalf("DETAILS   %s failed: %v", detailID, err)
			}
			t.Logf("DETAILS   %s -> %q (%s, %s) with %d children",
				detailID, arrTrunc(item.Title, 40), item.Type, item.State, len(children))
			if len(children) > 0 {
				c := children[0]
				t.Logf("            first child: %-9s %-30s %s", c.Type, arrTrunc(c.Title, 30), c.State)
				// One level deeper, which is where season and track live.
				if sub, grand, err := p.(arrDetailer).Details(ctx, c.CanonicalID); err == nil {
					t.Logf("            %s -> %q with %d children",
						c.CanonicalID, arrTrunc(sub.Title, 30), len(grand))
					if len(grand) > 0 {
						t.Logf("            first grandchild: %-9s %-30s %s",
							grand[0].Type, arrTrunc(grand[0].Title, 30), grand[0].State)
					}
				}
			}

			// LibraryStatus must agree with what Library said, or a card and a
			// details screen would contradict each other.
			if len(lib) > 0 {
				st, err := p.(LibraryProvider).LibraryStatus(ctx, lib[0].CanonicalID)
				if err != nil {
					t.Errorf("STATUS    %s failed: %v", lib[0].CanonicalID, err)
				} else {
					t.Logf("STATUS    %s -> %s (Library said %s)", lib[0].CanonicalID, st, lib[0].State)
					if st != lib[0].State {
						t.Errorf("STATUS    disagrees with LIBRARY for %s: %q vs %q",
							lib[0].CanonicalID, st, lib[0].State)
					}
				}
			}

			act, err := p.(ActivityProvider).Activity(ctx)
			if err != nil {
				t.Fatalf("ACTIVITY  failed: %v", err)
			}
			t.Logf("ACTIVITY  %d in flight", len(act))
			for i, a := range act {
				if i == 3 {
					break
				}
				t.Logf("            %-11s %3.0f%%  %s", a.Stage, a.Progress*100, arrTrunc(a.Title, 44))
			}

			// REQUEST, part one: something already held. Proves the
			// already-present branch without writing anything at all.
			var held MediaItem
			for _, it := range lib {
				if it.State == StateAvailable {
					held = it
					break
				}
			}
			if held.CanonicalID != "" {
				res, err := p.(Requester).Request(ctx, held, RequestOptions{Monitor: false})
				if err != nil {
					t.Errorf("REQUEST   already-held %s failed: %v", held.CanonicalID, err)
				} else {
					t.Logf("REQUEST   already-held %s -> accepted=%v: %s",
						held.CanonicalID, res.Accepted, res.Detail)
				}
			}

			if !write {
				t.Log("REQUEST   write path skipped (set YARRIT_ARR_LIVE_WRITE=1)")
				return
			}
			arrLiveWriteProbe(ctx, t, p, hits)
		})
	}
}

// arrLiveWriteProbe adds one thing that is definitely not in the library,
// unmonitored and with no search, then removes it again.
//
// Unmonitored with no search is what makes this safe against a real library:
// the application creates a database row and does nothing else. No indexer is
// queried, no download client is touched, no file is written.
func arrLiveWriteProbe(ctx context.Context, t *testing.T, p Provider, hits []MediaItem) {
	t.Helper()

	// A search hit the instance reported as missing is, by definition, not in
	// the library -- so adding it cannot collide with anything real.
	var victim MediaItem
	for _, it := range hits {
		if it.State != StateMissing {
			continue
		}
		switch it.Type {
		case "movie", "series", "anime", "artist":
			victim = it
		}
		if victim.CanonicalID != "" {
			break
		}
	}
	if victim.CanonicalID == "" {
		t.Log("REQUEST   no missing item among the search hits; write path not exercised")
		return
	}

	res, err := p.(Requester).Request(ctx, victim, RequestOptions{Monitor: false})
	if err != nil {
		t.Fatalf("REQUEST   add %s failed: %v", victim.CanonicalID, err)
	}
	t.Logf("REQUEST   add %s (%s) -> accepted=%v: %s",
		victim.CanonicalID, arrTrunc(victim.Title, 30), res.Accepted, res.Detail)

	// Remove it whatever happens next.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := arrLiveRemove(cleanupCtx, p, victim.CanonicalID); err != nil {
			t.Errorf("REQUEST   CLEANUP FAILED for %s: %v — remove it by hand",
				victim.CanonicalID, err)
			return
		}
		// Confirmed by row id, not by state: an unmonitored item with no file
		// reads as "missing" whether or not the row is still there, so state
		// cannot tell "deleted" from "shelved and idle".
		id, err := arrLiveRowID(cleanupCtx, p, victim.CanonicalID)
		if err != nil {
			t.Errorf("REQUEST   could not confirm removal of %s: %v", victim.CanonicalID, err)
			return
		}
		if id != 0 {
			t.Errorf("REQUEST   %s is still row %d — a test entry was left behind",
				victim.CanonicalID, id)
			return
		}
		t.Logf("REQUEST   removed %s; the row is gone and no file was touched", victim.CanonicalID)
	}()

	if !res.Accepted {
		t.Errorf("REQUEST   the add was refused: %s", res.Detail)
		return
	}
	id, err := arrLiveRowID(ctx, p, victim.CanonicalID)
	if err != nil {
		t.Fatalf("REQUEST   lookup after add failed: %v", err)
	}
	if id == 0 {
		t.Errorf("REQUEST   the add reported success but no row exists")
		return
	}
	during, _ := p.(LibraryProvider).LibraryStatus(ctx, victim.CanonicalID)
	// "missing" is the correct answer here and not a bug: the row exists but
	// is unmonitored with nothing on disk, so nothing is going to happen and a
	// request is still the right thing to offer.
	t.Logf("REQUEST   after add, %s is row %d, state %q", victim.CanonicalID, id, during)
}

// arrLiveRowID is the instance's own primary key for a canonical id, or 0 when
// it has no such row.
func arrLiveRowID(ctx context.Context, p Provider, canonicalID string) (int, error) {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return 0, err
	}
	switch prov := p.(type) {
	case *radarrProvider:
		var mine []radarrMovie
		if err := prov.c.get(ctx, "movie", url.Values{"tmdbId": {ref.ID}}, &mine); err != nil {
			return 0, err
		}
		if len(mine) == 0 {
			return 0, nil
		}
		return mine[0].ID, nil
	case *sonarrProvider:
		s, ok, err := prov.librarySeries(ctx, ref.ID)
		if err != nil || !ok {
			return 0, err
		}
		return s.ID, nil
	case *lidarrProvider:
		a, ok, err := prov.libraryArtist(ctx, ref.ID)
		if err != nil || !ok {
			return 0, err
		}
		return a.ID, nil
	}
	return 0, fmt.Errorf("no lookup path for %T", p)
}

// arrLiveRemove undoes the probe. Files are never deleted and no import-list
// exclusion is written, so nothing anyone genuinely wants is affected.
func arrLiveRemove(ctx context.Context, p Provider, canonicalID string) error {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return err
	}
	switch prov := p.(type) {
	case *radarrProvider:
		var mine []radarrMovie
		if err := prov.c.get(ctx, "movie", url.Values{"tmdbId": {ref.ID}}, &mine); err != nil {
			return err
		}
		if len(mine) == 0 {
			return nil
		}
		return prov.deleteMovie(ctx, mine[0].ID)
	case *sonarrProvider:
		s, ok, err := prov.librarySeries(ctx, ref.ID)
		if err != nil || !ok {
			return err
		}
		return prov.deleteSeries(ctx, s.ID)
	case *lidarrProvider:
		a, ok, err := prov.libraryArtist(ctx, ref.ID)
		if err != nil || !ok {
			return err
		}
		return prov.deleteArtist(ctx, a.ID)
	}
	return fmt.Errorf("no removal path for %T", p)
}

func arrTrunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
