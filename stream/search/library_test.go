package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func libServer(t *testing.T) (*server, *authConfig) {
	t.Helper()
	store, err := newLibraryStore(filepath.Join(t.TempDir(), "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &server{library: store, cache: map[string]cacheEntry{}}, testAuth()
}

func signedIn(t *testing.T, c *authConfig, method, url string, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, url, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, url, nil)
	}
	v, _ := c.encode(session{User: "wade", Exp: time.Now().Add(time.Hour).Unix()})
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
	return r
}

// Personal data has no anonymous form: the library must demand a session even
// when browsing is open.
func TestLibraryRequiresASessionEvenWhenScopeIsTV(t *testing.T) {
	s, c := libServer(t)
	c.Scope = scopeTV // browsing is open under this scope

	rec := httptest.NewRecorder()
	c.requireUser(s.handleLibrary)(rec, httptest.NewRequest("GET", "/api/v1/library", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous library read got %d, want 401", rec.Code)
	}
}

// Without accounts configured there is nothing to key the data on, and quietly
// sharing one bucket between everyone would be worse than refusing.
func TestLibraryRefusesWhenAuthIsUnconfigured(t *testing.T) {
	s, _ := libServer(t)
	c := &authConfig{Enabled: false}
	rec := httptest.NewRecorder()
	c.requireUser(s.handleLibrary)(rec, httptest.NewRequest("GET", "/api/v1/library", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 when accounts are not configured", rec.Code)
	}
}

func TestLibraryAddListRemove(t *testing.T) {
	s, c := libServer(t)
	h := c.requireUser(s.handleLibrary)

	rec := httptest.NewRecorder()
	h(rec, signedIn(t, c, "POST", "/api/v1/library",
		`{"key":"Tt-1375666","title":"Inception","year":2010,"kind":"video"}`))
	if rec.Code != 200 {
		t.Fatalf("add got %d: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h(rec, signedIn(t, c, "GET", "/api/v1/library", ""))
	var got struct {
		Items []struct {
			Key      string    `json:"key"`
			Title    string    `json:"title"`
			Progress *progress `json:"progress"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Title != "Inception" {
		t.Fatalf("library is %+v, want one Inception", got.Items)
	}
	// Keys are normalised so two clients cannot create two rows for one title.
	if got.Items[0].Key != "tt-1375666" {
		t.Errorf("key %q was not normalised", got.Items[0].Key)
	}

	rec = httptest.NewRecorder()
	h(rec, signedIn(t, c, "DELETE", "/api/v1/library?key=TT-1375666", ""))
	if rec.Code != 200 {
		t.Fatalf("delete got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h(rec, signedIn(t, c, "GET", "/api/v1/library", ""))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Items) != 0 {
		t.Errorf("library still holds %d item(s) after delete", len(got.Items))
	}
}

// One library across devices is the whole point: a position set on the TV must
// be readable by the browser.
func TestProgressIsSharedAcrossDevices(t *testing.T) {
	s, c := libServer(t)
	ph := c.requireUser(s.handleProgress)

	rec := httptest.NewRecorder()
	ph(rec, signedIn(t, c, "PUT", "/api/v1/progress",
		`{"key":"tt-1","title":"Dune","position":900,"duration":9000,"device":"roku"}`))
	if rec.Code != 200 {
		t.Fatalf("put got %d: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	ph(rec, signedIn(t, c, "GET", "/api/v1/progress?key=tt-1", ""))
	var got struct {
		Progress *progress `json:"progress"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Progress == nil || got.Progress.Position != 900 {
		t.Fatalf("progress came back as %+v, want position 900", got.Progress)
	}

	// A bare position update from another client must not wipe the metadata.
	rec = httptest.NewRecorder()
	ph(rec, signedIn(t, c, "PUT", "/api/v1/progress", `{"key":"tt-1","position":1200}`))
	rec = httptest.NewRecorder()
	ph(rec, signedIn(t, c, "GET", "/api/v1/progress?key=tt-1", ""))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Progress.Title != "Dune" || got.Progress.Duration != 9000 {
		t.Errorf("a bare update erased metadata: %+v", got.Progress)
	}
	if got.Progress.Position != 1200 {
		t.Errorf("position is %v, want the update to apply", got.Progress.Position)
	}
}

// Continue Watching must not offer the credits back, nor a title someone
// opened for ten seconds.
func TestContinueWatchingExcludesFinishedAndBarelyStarted(t *testing.T) {
	s, c := libServer(t)
	ph := c.requireUser(s.handleProgress)

	for _, body := range []string{
		`{"key":"midway","title":"Midway","position":1800,"duration":6000}`,
		`{"key":"done","title":"Finished","position":5900,"duration":6000}`,
		`{"key":"glance","title":"Glanced","position":8,"duration":6000}`,
	} {
		rec := httptest.NewRecorder()
		ph(rec, signedIn(t, c, "PUT", "/api/v1/progress", body))
		if rec.Code != 200 {
			t.Fatalf("put %s got %d", body, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	c.requireUser(s.handleContinue)(rec, signedIn(t, c, "GET", "/api/v1/continue", ""))
	var got struct {
		Items []progress `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Items) != 1 || got.Items[0].Key != "midway" {
		t.Fatalf("continue row is %+v, want only the part-watched title", got.Items)
	}
}

// Two users must never see each other's shelf.
func TestLibrariesAreIsolatedPerUser(t *testing.T) {
	s, _ := libServer(t)
	s.library.add("wade", libraryItem{Key: "a", Title: "Wade's"})
	s.library.add("someone", libraryItem{Key: "b", Title: "Theirs"})

	if got := s.library.list("wade"); len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("wade sees %+v", got)
	}
	if got := s.library.list("someone"); len(got) != 1 || got[0].Key != "b" {
		t.Fatalf("the other user sees %+v", got)
	}
}

// A restart must not lose the shelf.
func TestLibrarySurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.json")
	s1, err := newLibraryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.add("wade", libraryItem{Key: "k", Title: "Persisted"})
	s1.setProgress("wade", progress{Key: "k", Position: 42, Duration: 100})
	if err := s1.flush(); err != nil {
		t.Fatal(err)
	}

	s2, err := newLibraryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.list("wade"); len(got) != 1 || got[0].Title != "Persisted" {
		t.Fatalf("after reload the library is %+v", got)
	}
	p, ok := s2.progressFor("wade", "k")
	if !ok || p.Position != 42 {
		t.Fatalf("after reload progress is %+v ok=%v", p, ok)
	}
}

// A half-written file would cost every user their library, so the write is
// atomic and a corrupt file is reported rather than silently replaced.
func TestCorruptLibraryFileIsReportedNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newLibraryStore(path); err == nil {
		t.Fatal("a corrupt library file was accepted; the data would be replaced")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{not json" {
		t.Error("the corrupt file was modified instead of left for inspection")
	}
}

func TestFlushIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "library.json")
	s, _ := newLibraryStore(path)
	s.add("wade", libraryItem{Key: "k"})
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %s was left behind", e.Name())
		}
	}
}
