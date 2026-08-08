package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The adapter must satisfy exactly the interfaces it claims, and no more.
// Asking RomM to obtain something is meant to be a compile-time no.
var (
	_ Provider        = (*rommProvider)(nil)
	_ LibraryProvider = (*rommProvider)(nil)
	_ gameDetailer    = (*rommProvider)(nil)
	_ gamePlayer      = (*rommProvider)(nil)
)

func TestRommIsNotARequester(t *testing.T) {
	var p Provider = newRommProvider(rommConfig{BaseURL: "http://x", Username: "u", Password: "p"})
	if _, ok := p.(Requester); ok {
		t.Error("RomM must not satisfy Requester — it serves, it does not acquire")
	}
	if _, ok := p.(Searcher); ok {
		t.Error("RomM must not satisfy Searcher")
	}
}

// fakeRomM is a configurable stand-in for the RomM API. Each field, when set,
// overrides the default handler for that route.
type fakeRomM struct {
	version     string
	setupWizard bool

	heartbeatStatus int // 0 => 200
	tokenStatus     int // 0 => 200
	platformsStatus int // 0 => 200
	loaderStatus    int // 0 => 200 (the EmulatorJS player asset)

	roms    map[string]rommRom // by id, for /api/roms/{id}
	romList []rommRom          // for /api/roms
}

func (f *fakeRomM) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if f.heartbeatStatus != 0 {
			w.WriteHeader(f.heartbeatStatus)
			return
		}
		v := f.version
		if v == "" {
			v = "4.9.2"
		}
		writeJSON(w, 200, map[string]any{
			"SYSTEM": map[string]any{"VERSION": v, "SHOW_SETUP_WIZARD": f.setupWizard},
		})
	})
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, r *http.Request) {
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			return
		}
		writeJSON(w, 200, map[string]any{"access_token": "tok", "token_type": "bearer", "expires": 1800})
	})
	mux.HandleFunc("/api/platforms", func(w http.ResponseWriter, r *http.Request) {
		if f.platformsStatus != 0 {
			w.WriteHeader(f.platformsStatus)
			return
		}
		writeJSON(w, 200, []map[string]any{{"id": 1, "slug": "snes"}})
	})
	mux.HandleFunc("/assets/emulatorjs/data/loader.js", func(w http.ResponseWriter, r *http.Request) {
		if f.loaderStatus != 0 {
			w.WriteHeader(f.loaderStatus)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("// loader"))
	})
	mux.HandleFunc("/api/roms", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, rommRomsPage{Items: f.romList, Total: len(f.romList)})
	})
	mux.HandleFunc("/api/roms/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/roms/")
		rom, ok := f.roms[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, rom)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRommHealthStates(t *testing.T) {
	t.Run("not_configured without address", func(t *testing.T) {
		p := newRommProvider(rommConfig{Username: "u", Password: "p"})
		if got := p.Health(context.Background()).State; got != HealthNotConfigured {
			t.Fatalf("state = %q, want not_configured", got)
		}
	})

	t.Run("not_configured without credentials", func(t *testing.T) {
		p := newRommProvider(rommConfig{BaseURL: "http://romm.example"})
		if got := p.Health(context.Background()).State; got != HealthNotConfigured {
			t.Fatalf("state = %q, want not_configured", got)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		// A server that is closed the instant it exists: the address resolves,
		// the connection is refused. That is unreachable, not auth_failed.
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		p := newRommProvider(rommConfig{BaseURL: url, Username: "u", Password: "p",
			HTTPClient: &http.Client{Timeout: 2 * time.Second}})
		if got := p.Health(context.Background()).State; got != HealthUnreachable {
			t.Fatalf("state = %q, want unreachable", got)
		}
	})

	t.Run("auth_failed", func(t *testing.T) {
		f := &fakeRomM{platformsStatus: 401}
		srv := f.server(t)
		p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "bad"})
		if got := p.Health(context.Background()).State; got != HealthAuthFailed {
			t.Fatalf("state = %q, want auth_failed", got)
		}
	})

	t.Run("incompatible by version", func(t *testing.T) {
		f := &fakeRomM{version: "2.9.0"}
		srv := f.server(t)
		p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})
		h := p.Health(context.Background())
		if h.State != HealthIncompatible {
			t.Fatalf("state = %q, want incompatible", h.State)
		}
	})

	t.Run("incompatible when heartbeat is absent", func(t *testing.T) {
		f := &fakeRomM{heartbeatStatus: 404}
		srv := f.server(t)
		p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})
		if got := p.Health(context.Background()).State; got != HealthIncompatible {
			t.Fatalf("state = %q, want incompatible", got)
		}
	})

	t.Run("degraded when setup incomplete", func(t *testing.T) {
		f := &fakeRomM{setupWizard: true}
		srv := f.server(t)
		p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})
		if got := p.Health(context.Background()).State; got != HealthDegraded {
			t.Fatalf("state = %q, want degraded", got)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		f := &fakeRomM{version: "4.9.2"}
		srv := f.server(t)
		p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})
		h := p.Health(context.Background())
		if h.State != HealthOK {
			t.Fatalf("state = %q, want healthy", h.State)
		}
		if h.Version != "4.9.2" {
			t.Errorf("version = %q, want 4.9.2", h.Version)
		}
	})
}

func TestRommLibraryMapping(t *testing.T) {
	f := &fakeRomM{
		romList: []rommRom{
			{ID: 10, Name: "Chrono Trigger", PlatformSlug: "snes",
				PlatformDisplayName: "Super Nintendo", Regions: []string{"USA"},
				URLCover: "https://img/ct.jpg"},
			{ID: 11, Name: "", FsNameNoExt: "Missing Dump", PlatformSlug: "nes",
				PlatformDisplayName: "NES", MissingFromFS: true},
		},
	}
	srv := f.server(t)
	p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})

	items, err := p.Library(context.Background(), "game")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	a := items[0]
	if a.CanonicalID != "romm:release:10" {
		t.Errorf("canonical id = %q, want romm:release:10", a.CanonicalID)
	}
	if a.Domain != "game" || a.Type != "release" {
		t.Errorf("domain/type = %q/%q, want game/release — a ROM is a release, not a flattened quality", a.Domain, a.Type)
	}
	if a.Subtitle != "Super Nintendo · USA" {
		t.Errorf("subtitle = %q, want platform and region kept first-class", a.Subtitle)
	}
	if a.State != StateAvailable {
		t.Errorf("state = %q, want available", a.State)
	}

	// missing_from_fs: catalogued but not on disk, so not available.
	b := items[1]
	if b.Title != "Missing Dump" {
		t.Errorf("title fell back wrong: %q", b.Title)
	}
	if b.State != StateMissing {
		t.Errorf("state = %q, want missing (file gone from disk)", b.State)
	}
}

func TestRommLibraryIgnoresForeignDomain(t *testing.T) {
	f := &fakeRomM{romList: []rommRom{{ID: 1, Name: "x", PlatformSlug: "snes"}}}
	srv := f.server(t)
	p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})
	items, err := p.Library(context.Background(), "video")
	if err != nil {
		t.Fatal(err)
	}
	if items != nil {
		t.Errorf("Library(video) returned %d items; a games provider must stay silent on video", len(items))
	}
}

func TestRommLibraryStatus(t *testing.T) {
	f := &fakeRomM{
		roms: map[string]rommRom{
			"10": {ID: 10, Name: "Present", MissingFromFS: false},
			"11": {ID: 11, Name: "Gone", MissingFromFS: true},
		},
	}
	srv := f.server(t)
	p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})

	cases := []struct {
		id   string
		want LibraryState
	}{
		{"romm:release:10", StateAvailable},
		{"romm:release:11", StateMissing},      // present in DB, missing on disk
		{"romm:release:999", StateMissing},     // 404 from RomM
		{"romarr:game:snes~abc", StateUnknown}, // not ours: no opinion, not "missing"
		{"tmdb:movie:5", StateUnknown},
	}
	for _, c := range cases {
		got, err := p.LibraryStatus(context.Background(), c.id)
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if got != c.want {
			t.Errorf("LibraryStatus(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestRommDetails(t *testing.T) {
	f := &fakeRomM{roms: map[string]rommRom{
		"10": {ID: 10, Name: "Chrono Trigger", PlatformSlug: "snes",
			PlatformDisplayName: "Super Nintendo", Summary: "A JRPG."},
	}}
	srv := f.server(t)
	p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})

	item, children, err := p.Details(context.Background(), "romm:release:10")
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Chrono Trigger" || item.Overview != "A JRPG." {
		t.Errorf("details mapped wrong: %+v", item)
	}
	if len(children) != 0 {
		t.Errorf("a ROM has no children, got %d", len(children))
	}

	if _, _, err := p.Details(context.Background(), "romarr:game:snes~x"); err == nil {
		t.Error("Details on a foreign id should error, not guess")
	}
}

func TestRommPlayIsEarnedAndGated(t *testing.T) {
	f := &fakeRomM{}
	srv := f.server(t)
	p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})

	// Before verification, play is not advertised and no URL is offered — the
	// whole point of not implying arbitrary browser play.
	if contains(p.Roles(), "play") {
		t.Fatal("play advertised before the launch path was verified")
	}
	if _, ok := p.PlayURL(context.Background(), "romm:release:10"); ok {
		t.Fatal("PlayURL offered a launch before verification")
	}

	// After the player asset answers, play is earned.
	if !p.verifyPlay(context.Background()) {
		t.Fatal("verifyPlay should succeed when the loader asset serves 200")
	}
	if !contains(p.Roles(), "play") {
		t.Error("play should be advertised once verified")
	}
	u, ok := p.PlayURL(context.Background(), "romm:release:10")
	if !ok {
		t.Fatal("PlayURL should offer a launch for a real ROM id once verified")
	}
	if u != srv.URL+"/console/rom/10/play" {
		t.Errorf("play url = %q, want the RomM console route", u)
	}
	// A foreign id is still declined even when play is enabled.
	if _, ok := p.PlayURL(context.Background(), "romarr:release:abc"); ok {
		t.Error("PlayURL must decline a non-RomM id")
	}
}

func TestRommPlayNotAdvertisedWhenAssetMissing(t *testing.T) {
	f := &fakeRomM{loaderStatus: 404}
	srv := f.server(t)
	p := newRommProvider(rommConfig{BaseURL: srv.URL, Username: "u", Password: "p"})
	if p.verifyPlay(context.Background()) {
		t.Fatal("verifyPlay must fail when the player asset is absent")
	}
	if contains(p.Roles(), "play") {
		t.Error("play must not be advertised on an install that does not serve the player")
	}
}

func TestRommYearFromEpoch(t *testing.T) {
	if got := rommYear(json.Number("")); got != 0 {
		t.Errorf("empty date = %d, want 0", got)
	}
	// 1995-03-11 in unix seconds -> year 1995.
	if got := rommYear(json.Number("794880000")); got != 1995 {
		t.Errorf("epoch year = %d, want 1995", got)
	}
}
