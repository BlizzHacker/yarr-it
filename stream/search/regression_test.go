package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Regression tests for defects that were live and silent.
//
// Each one had the same shape: the system kept answering, so nothing looked
// broken. That is why they are pinned here rather than left to integration
// testing -- an integration test that asserts "a response came back" would have
// passed throughout.

// A self-hoster who has not reached the Prowlarr step yet must get a running
// server, not a process that exits before it prints anything useful.
func TestMissingProwlarrKeyDoesNotKillSearch(t *testing.T) {
	s := &server{cache: map[string]cacheEntry{}} // apiKey deliberately empty

	_, err := s.searchProwlarr(t.Context(), "dune", "video")
	if !errors.Is(err, errNoIndexers) {
		t.Fatalf("got %v, want errNoIndexers", err)
	}
}

// "Not configured" and "unreachable" are different problems with different
// fixes. Reporting both as "down" sends someone hunting a network fault that
// does not exist.
func TestHealthSeparatesUnconfiguredFromUnreachable(t *testing.T) {
	s := &server{cache: map[string]cacheEntry{}} // no apiKey

	rec := httptest.NewRecorder()
	s.handleHealth(rec, httptest.NewRequest("GET", "/api/health", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("health did not return JSON: %v", err)
	}
	if got["prowlarr"] != "not-configured" {
		t.Errorf("prowlarr = %v, want not-configured", got["prowlarr"])
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status %d; health must answer even when unconfigured", rec.Code)
	}
}

// The advice matters. "Try again in a moment" is false for a server that has no
// indexer at all -- waiting never fixes it, so the user retries forever.
func TestUnconfiguredSearchSaysSoRatherThanBlamingTiming(t *testing.T) {
	// The state under test is "this kind has no archive.org scope, so nothing
	// can rescue the response". It used to be reached with kind=audio because
	// music genuinely had no source; it now has one, so the state is produced
	// deliberately. See the note on TestAnUnanswerableSearchSaysWhyRatherThanPending.
	withoutArchiveScope(t, "music")

	s := &server{
		cache:    map[string]cacheEntry{},
		inflight: map[string]chan struct{}{},
	}
	s.tmdb = newTMDB("")
	s.igdb = newIGDB("", "")

	rec := httptest.NewRecorder()
	s.handleSearch(rec, httptest.NewRequest("GET", "/api/search?q=dune&kind=audio", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	msg := got["error"]
	if msg == "" {
		t.Fatal("no error message at all")
	}
	low := strings.ToLower(msg)
	for _, wrong := range []string{"try again", "taking too long"} {
		if strings.Contains(low, wrong) {
			t.Errorf("message blames timing (%q); it should say no indexer is configured", msg)
		}
	}
	if !strings.Contains(low, "indexer") {
		t.Errorf("message %q never mentions the actual problem", msg)
	}
}

// A search must never hang forever when the indexer backend goes quiet.
//
// Measured before this was bounded: 280s with no response at all, and a browser
// fetch still pending at 829 seconds. The indexer-list call sat before the
// fast-tier deadline, and used http.DefaultClient, which has no timeout -- the
// one outbound request in the package with no ceiling, on the critical path of
// every uncached search.
func TestASearchGivesUpWhenTheIndexerGoesQuiet(t *testing.T) {
	// A server that accepts the connection and then says nothing, which is the
	// failure mode that hangs. Refusing the connection would not reproduce it.
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer quiet.Close()

	s := &server{
		prowlarrURL: quiet.URL,
		apiKey:      "test",
		cache:       map[string]cacheEntry{},
		inflight:    map[string]chan struct{}{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, _, err := s.searchTiered(ctx, "anything", "video", func([]card) {})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("a silent indexer produced no error; the caller cannot tell it failed")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("searchTiered did not return; a silent indexer still hangs the request")
	}
}

// Every outbound client on a request path a person waits on must have a
// ceiling. http.DefaultClient does not.
func TestTheProwlarrClientHasATimeout(t *testing.T) {
	if prowlarrClient.Timeout <= 0 {
		t.Fatal("prowlarrClient has no timeout; a quiet peer holds the request open indefinitely")
	}
	if indexerListDeadline <= 0 {
		t.Fatal("the indexer list call is unbounded")
	}
}
