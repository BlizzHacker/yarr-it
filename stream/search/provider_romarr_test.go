package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	_ Provider         = (*romarrProvider)(nil)
	_ Searcher         = (*romarrProvider)(nil)
	_ LibraryProvider  = (*romarrProvider)(nil)
	_ Requester        = (*romarrProvider)(nil)
	_ ActivityProvider = (*romarrProvider)(nil)
	_ gameDetailer     = (*romarrProvider)(nil)
)

func TestRomarrIsNotAPlayer(t *testing.T) {
	var p Provider = newRomarrProvider(romarrConfig{BaseURL: "http://x", APIKey: "k"})
	if _, ok := p.(gamePlayer); ok {
		t.Error("Romarr acquires; it must not claim to launch anything")
	}
}

// The single most important behaviour in this file: a file that is verifying
// against its DAT is being imported, and is NOT available. This is the whole
// reason the mapping exists rather than a boolean.
func TestRomarrVerifyMapsToImportingNotAvailable(t *testing.T) {
	for _, stage := range []string{"verifying", "verify", "importing", "import"} {
		if got := romarrLibraryState(stage); got != StateImporting {
			t.Errorf("romarrLibraryState(%q) = %q, want importing", stage, got)
		}
		if romarrLibraryState(stage) == StateAvailable {
			t.Errorf("%q was reported available — a file that has not passed its DAT check is not held", stage)
		}
	}
	table := map[string]LibraryState{
		"imported":    StateAvailable,
		"grabbed":     StateDownloading,
		"queued":      StateDownloading,
		"downloading": StateDownloading,
		"wanted":      StateRequested,
		"searching":   StateRequested,
		"failed":      StateMissing,
		"":            StateUnknown,
	}
	for stage, want := range table {
		if got := romarrLibraryState(stage); got != want {
			t.Errorf("romarrLibraryState(%q) = %q, want %q", stage, got, want)
		}
	}
}

func TestRomarrGameIDRoundTrip(t *testing.T) {
	// Names that would fracture a naive id: a colon, a tilde, spaces.
	for _, name := range []string{"Chrono Trigger", "R:Racing Evolution", "weird~name", "スーパーメトロイド"} {
		id := romarrGameID("snes", name)
		if strings.Count(id, ":") != 2 {
			t.Errorf("id %q for %q has stray colons; scheme:type:id must hold", id, name)
		}
		slug, got, ok := parseRomarrGameID(id)
		if !ok || slug != "snes" || got != name {
			t.Errorf("round trip of %q failed: slug=%q name=%q ok=%v", name, slug, got, ok)
		}
	}
	if _, _, ok := parseRomarrGameID("romm:release:5"); ok {
		t.Error("a RomM id must not parse as a Romarr game id")
	}
}

// fakeRomarr is a configurable stand-in for the Romarr API.
type fakeRomarr struct {
	statusCode  int // for /api/v1/system/status; 0 => 200
	status      romarrSystemStatus
	release     romarrReleasePage
	queue       []romarrQueueItem
	wanted      []romarrWantedItem
	requestResp romarrRequestResult

	lastRequestPath string
	lastRequestBody map[string]string
}

func (f *fakeRomarr) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/system/status", func(w http.ResponseWriter, r *http.Request) {
		if f.statusCode != 0 {
			w.WriteHeader(f.statusCode)
			return
		}
		writeJSON(w, 200, f.status)
	})
	mux.HandleFunc("/api/v1/release", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, f.release)
	})
	mux.HandleFunc("/api/v1/queue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, romarrQueuePage{Items: f.queue})
	})
	mux.HandleFunc("/api/v1/wanted/missing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, romarrWantedPage{Items: f.wanted})
	})
	record := func(w http.ResponseWriter, r *http.Request) {
		f.lastRequestPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		f.lastRequestBody = map[string]string{}
		_ = json.Unmarshal(body, &f.lastRequestBody)
		writeJSON(w, 200, f.requestResp)
	}
	mux.HandleFunc("/api/request", record)
	mux.HandleFunc("/api/v1/release/grab", record)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRomarrHealthStates(t *testing.T) {
	t.Run("not_configured without key", func(t *testing.T) {
		p := newRomarrProvider(romarrConfig{BaseURL: "http://romarr.example"})
		if got := p.Health(context.Background()).State; got != HealthNotConfigured {
			t.Fatalf("state = %q, want not_configured", got)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		p := newRomarrProvider(romarrConfig{BaseURL: url, APIKey: "k",
			HTTPClient: &http.Client{Timeout: 2 * time.Second}})
		if got := p.Health(context.Background()).State; got != HealthUnreachable {
			t.Fatalf("state = %q, want unreachable", got)
		}
	})

	t.Run("auth_failed", func(t *testing.T) {
		f := &fakeRomarr{statusCode: 401}
		srv := f.server(t)
		p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "bad"})
		if got := p.Health(context.Background()).State; got != HealthAuthFailed {
			t.Fatalf("state = %q, want auth_failed", got)
		}
	})

	t.Run("incompatible on wrong API", func(t *testing.T) {
		f := &fakeRomarr{statusCode: 404}
		srv := f.server(t)
		p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})
		if got := p.Health(context.Background()).State; got != HealthIncompatible {
			t.Fatalf("state = %q, want incompatible", got)
		}
	})

	t.Run("incompatible without a version", func(t *testing.T) {
		f := &fakeRomarr{status: romarrSystemStatus{Version: ""}}
		srv := f.server(t)
		p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})
		if got := p.Health(context.Background()).State; got != HealthIncompatible {
			t.Fatalf("state = %q, want incompatible", got)
		}
	})

	t.Run("degraded when a dependency is down", func(t *testing.T) {
		f := &fakeRomarr{status: romarrSystemStatus{Version: "0.7.0", Prowlarr: false, Qbittorrent: true, Romm: true}}
		srv := f.server(t)
		p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})
		h := p.Health(context.Background())
		if h.State != HealthDegraded {
			t.Fatalf("state = %q, want degraded", h.State)
		}
		if !strings.Contains(h.Detail, "Prowlarr") {
			t.Errorf("degraded detail should name the fault, got %q", h.Detail)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		f := &fakeRomarr{status: romarrSystemStatus{Version: "0.7.0", Prowlarr: true, Qbittorrent: true, Romm: true, Library: true}}
		srv := f.server(t)
		p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})
		h := p.Health(context.Background())
		if h.State != HealthOK {
			t.Fatalf("state = %q, want healthy", h.State)
		}
		if h.Version != "0.7.0" {
			t.Errorf("version = %q, want 0.7.0", h.Version)
		}
	})
}

func TestRomarrSearchReturnsReleases(t *testing.T) {
	f := &fakeRomarr{release: romarrReleasePage{
		Game: "Chrono Trigger", Found: 2, Accepted: 1,
		Items: []romarrCandidate{
			{ID: "abc00", Title: "Chrono Trigger - SNES", Indexer: "BlueRoms", Accepted: true, Grabbable: true, Reasons: []string{"+30 names Super Nintendo"}},
			{ID: "def01", Title: "Chrono Trigger junk", Indexer: "Other", Accepted: false},
		},
	}}
	srv := f.server(t)
	p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})

	items, err := p.Search(context.Background(), "Chrono Trigger", "game")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].CanonicalID != "romarr:release:abc00" || items[0].Type != "release" {
		t.Errorf("first item mapped wrong: %+v", items[0])
	}
	if items[0].State != StateMissing {
		t.Errorf("a search candidate is not held; state = %q, want missing", items[0].State)
	}
	// Foreign domain: stay silent, do not error.
	if got, err := p.Search(context.Background(), "x", "video"); err != nil || got != nil {
		t.Errorf("Search(video) = %v, %v; want nil, nil", got, err)
	}
}

func TestRomarrLibraryLedgerAndStatus(t *testing.T) {
	f := &fakeRomarr{
		queue: []romarrQueueItem{
			{Game: "Super Castlevania IV", Platform: "snes", State: "grabbed", Detail: ""},
			{Game: "Chrono Trigger", Platform: "snes", State: "verifying"},
		},
		wanted: []romarrWantedItem{
			{Game: "Super Metroid", Platform: "snes", LastError: "release offers no usable download link"},
		},
	}
	srv := f.server(t)
	p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})

	items, err := p.Library(context.Background(), "game")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("ledger has %d items, want 3 (2 queued + 1 wanted)", len(items))
	}
	byTitle := map[string]MediaItem{}
	for _, it := range items {
		byTitle[it.Title] = it
	}
	if byTitle["Super Castlevania IV"].State != StateDownloading {
		t.Errorf("grabbed -> %q, want downloading", byTitle["Super Castlevania IV"].State)
	}
	if byTitle["Chrono Trigger"].State != StateImporting {
		t.Errorf("verifying -> %q, want importing (never available)", byTitle["Chrono Trigger"].State)
	}
	if byTitle["Super Metroid"].State != StateRequested {
		t.Errorf("wanted -> %q, want requested", byTitle["Super Metroid"].State)
	}

	// LibraryStatus reads the same lists and maps the same way.
	st, err := p.LibraryStatus(context.Background(), romarrGameID("snes", "Chrono Trigger"))
	if err != nil {
		t.Fatal(err)
	}
	if st != StateImporting {
		t.Errorf("status of a verifying item = %q, want importing", st)
	}
	// Not-ours id: unknown, cheaply, without a heavy scan.
	st, err = p.LibraryStatus(context.Background(), "romm:release:99")
	if err != nil || st != StateUnknown {
		t.Errorf("status of a RomM id = %q,%v; want unknown,nil", st, err)
	}
}

func TestRomarrRequestPaths(t *testing.T) {
	t.Run("by game and platform", func(t *testing.T) {
		f := &fakeRomarr{requestResp: romarrRequestResult{OK: true, Release: "Chrono Trigger - SNES"}}
		srv := f.server(t)
		p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})

		res, err := p.Request(context.Background(),
			MediaItem{CanonicalID: romarrGameID("snes", "Chrono Trigger")}, RequestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Accepted {
			t.Errorf("request not accepted: %+v", res)
		}
		if f.lastRequestPath != "/api/request" {
			t.Errorf("hit %q, want /api/request", f.lastRequestPath)
		}
		if f.lastRequestBody["game"] != "Chrono Trigger" || f.lastRequestBody["platform"] != "snes" {
			t.Errorf("request body wrong: %v", f.lastRequestBody)
		}
	})

	t.Run("by release id grabs the specific candidate", func(t *testing.T) {
		f := &fakeRomarr{requestResp: romarrRequestResult{OK: true, Release: "picked"}}
		srv := f.server(t)
		p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})

		res, err := p.Request(context.Background(),
			MediaItem{CanonicalID: "romarr:release:abc00"}, RequestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Accepted || f.lastRequestPath != "/api/v1/release/grab" {
			t.Errorf("release grab path wrong: accepted=%v path=%q", res.Accepted, f.lastRequestPath)
		}
		if f.lastRequestBody["id"] != "abc00" {
			t.Errorf("grab id = %q, want abc00", f.lastRequestBody["id"])
		}
	})

	t.Run("refusal is reported honestly", func(t *testing.T) {
		f := &fakeRomarr{requestResp: romarrRequestResult{OK: false, Error: "no usable release among 0 result(s)"}}
		srv := f.server(t)
		p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})
		res, err := p.Request(context.Background(),
			MediaItem{CanonicalID: romarrGameID("snes", "Nonesuch")}, RequestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Accepted || !strings.Contains(res.Detail, "no usable release") {
			t.Errorf("a refusal should be Accepted=false with the reason, got %+v", res)
		}
	})
}

func TestRomarrActivityKeepsVerifyVisible(t *testing.T) {
	f := &fakeRomarr{
		queue: []romarrQueueItem{
			{Game: "Chrono Trigger", Platform: "snes", State: "verifying"},
			{Game: "Zelda", Platform: "snes", State: "grabbed"},
		},
		wanted: []romarrWantedItem{{Game: "Super Metroid", Platform: "snes", LastError: "no link"}},
	}
	srv := f.server(t)
	p := newRomarrProvider(romarrConfig{BaseURL: srv.URL, APIKey: "k"})

	acts, err := p.Activity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 3 {
		t.Fatalf("got %d activity items, want 3", len(acts))
	}
	stage := map[string]string{}
	for _, a := range acts {
		stage[a.Title] = a.Stage
		if a.Domain != "game" {
			t.Errorf("activity domain = %q, want game", a.Domain)
		}
	}
	if stage["Chrono Trigger"] != "importing" {
		t.Errorf("verifying surfaced as %q, want importing", stage["Chrono Trigger"])
	}
	if stage["Super Metroid"] != "searching" {
		t.Errorf("wanted surfaced as %q, want searching", stage["Super Metroid"])
	}
}

// registerGameRoutes must put both adapters into the shared Registry and wire
// the game surface, so the rest of the app sees them with no further work.
func TestRegisterGameRoutesWiresBothProviders(t *testing.T) {
	romarrFake := (&fakeRomarr{status: romarrSystemStatus{Version: "0.7.0", Prowlarr: true, Qbittorrent: true, Romm: true, Library: true},
		queue: []romarrQueueItem{{Game: "Zelda", Platform: "snes", State: "grabbed"}}}).server(t)
	rommFake := (&fakeRomM{version: "4.9.2",
		romList: []rommRom{{ID: 10, Name: "Chrono Trigger", PlatformSlug: "snes", PlatformDisplayName: "Super Nintendo"}},
		roms:    map[string]rommRom{"10": {ID: 10, Name: "Chrono Trigger", PlatformDisplayName: "Super Nintendo"}}}).server(t)

	t.Setenv("ROMARR_URL", romarrFake.URL)
	t.Setenv("ROMARR_API_KEY", "k")
	t.Setenv("ROMM_URL", rommFake.URL)
	t.Setenv("ROMM_USERNAME", "u")
	t.Setenv("ROMM_PASSWORD", "p")

	reg := &Registry{}
	mux := http.NewServeMux()
	registerGameRoutes(mux, reg)

	ids := map[string]Provider{}
	for _, p := range reg.All() {
		ids[p.ID()] = p
	}
	if _, ok := ids["romarr"]; !ok {
		t.Error("romarr not registered")
	}
	romm, ok := ids["romm"]
	if !ok {
		t.Fatal("romm not registered")
	}
	// The play role was probed against the fake's loader asset during
	// registration, so it should be earned by now.
	if !contains(romm.Roles(), "play") {
		t.Error("RomM play role not verified during registration")
	}

	// Details routes by scheme: a RomM id gets a play url, a Romarr id does not.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/game/details?id=romm:release:10", nil)
	mux.ServeHTTP(rec, req)
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["playUrl"] == nil || !strings.Contains(resp["playUrl"].(string), "/console/rom/10/play") {
		t.Errorf("RomM details should carry a play url, got %v", resp["playUrl"])
	}

	// Library fans out over both providers.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/game/library", nil))
	var lib gameFanoutResult
	if err := json.Unmarshal(rec.Body.Bytes(), &lib); err != nil {
		t.Fatal(err)
	}
	if len(lib.Items) < 2 {
		t.Errorf("library fan-out returned %d items, want at least RomM shelf + Romarr ledger", len(lib.Items))
	}
}

// --- live verification against real hardware --------------------------------
//
// Skipped by default so `go test` never touches the network. Run explicitly
// against the estate with YARRIT_LIVE=1 and the connection env vars set. It is
// deliberately read-only: it never grabs, so it can be run against Wade's real
// instances without leaving anything behind.
func TestLiveGameProviders(t *testing.T) {
	if os.Getenv("YARRIT_LIVE") != "1" {
		t.Skip("live test: set YARRIT_LIVE=1 (and the YARRIT_ROM* env vars) to run against real hardware")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if u := os.Getenv("YARRIT_ROMARR_URL"); u != "" {
		p := newRomarrProvider(romarrConfig{BaseURL: u, APIKey: os.Getenv("YARRIT_ROMARR_KEY")})
		h := p.Health(ctx)
		t.Logf("ROMARR  health=%s version=%s detail=%s", h.State, h.Version, h.Detail)

		items, err := p.Search(ctx, os.Getenv("YARRIT_ROMARR_QUERY"), "game")
		t.Logf("ROMARR  search(dry-run) -> %d releases, err=%v", len(items), err)

		lib, err := p.Library(ctx, "game")
		t.Logf("ROMARR  library(ledger) -> %d entries, err=%v", len(lib), err)

		acts, err := p.Activity(ctx)
		t.Logf("ROMARR  activity -> %d in flight, err=%v", len(acts), err)
	}

	if u := os.Getenv("YARRIT_ROMM_URL"); u != "" {
		p := newRommProvider(rommConfig{BaseURL: u,
			Username: os.Getenv("YARRIT_ROMM_USER"), Password: os.Getenv("YARRIT_ROMM_PASS"),
			LibraryLimit: 3})
		h := p.Health(ctx)
		t.Logf("ROMM    health=%s version=%s detail=%s", h.State, h.Version, h.Detail)

		played := p.verifyPlay(ctx)
		t.Logf("ROMM    play launch path verified=%v roles=%v", played, p.Roles())

		lib, err := p.Library(ctx, "game")
		t.Logf("ROMM    library -> %d roms, err=%v", len(lib), err)
		if len(lib) > 0 {
			id := lib[0].CanonicalID
			st, err := p.LibraryStatus(ctx, id)
			t.Logf("ROMM    status(%s) -> %s, err=%v", id, st, err)
			if u, ok := p.PlayURL(ctx, id); ok {
				t.Logf("ROMM    playURL(%s) -> %s", id, u)
			}
		}
	}
}
