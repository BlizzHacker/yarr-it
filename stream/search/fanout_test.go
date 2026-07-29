package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeProwlarr serves an indexer list and per-indexer searches, with one
// indexer deliberately slower than the deadline.
func fakeProwlarr(t *testing.T, slowID int, slowFor time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/indexer", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "name": "fast-one", "enable": true},
			{"id": 2, "name": "fast-two", "enable": true},
			{"id": slowID, "name": "slow", "enable": true},
			{"id": 9, "name": "disabled", "enable": false},
		})
	})

	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("indexerIds")
		if id == fmt.Sprint(slowID) {
			select {
			case <-time.After(slowFor):
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode([]prowlarrResult{{
			Title:     "result from indexer " + id,
			Indexer:   "ix" + id,
			Seeders:   10,
			Protocol:  "torrent",
			MagnetURL: "magnet:?xt=urn:btih:" + id,
			GUID:      "guid-" + id,
		}})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fanoutServer(t *testing.T, srv *httptest.Server) *server {
	t.Helper()
	return &server{
		prowlarrURL: srv.URL,
		apiKey:      "test",
		ttl:         time.Minute,
		cache:       map[string]cacheEntry{},
		inflight:    map[string]chan struct{}{},
	}
}

// The whole point: a slow indexer delays itself, not the response.
func TestFanoutAnswersOnDeadlineWithoutTheSlowIndexer(t *testing.T) {
	srv := fakeProwlarr(t, 3, 2*time.Second)
	s := fanoutServer(t, srv)

	start := time.Now()
	res, err := s.searchFanout(context.Background(), "matrix", "", 300*time.Millisecond, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > time.Second {
		t.Fatalf("waited %s for a 300ms deadline: the slow indexer held the response", elapsed)
	}
	if !res.partial {
		t.Error("result should be marked partial when the deadline cut it short")
	}
	if res.answered != 2 || res.total != 3 {
		t.Errorf("answered %d of %d, want 2 of 3 (the disabled one must not be asked)",
			res.answered, res.total)
	}
	if len(res.cards) != 2 {
		t.Errorf("got %d cards, want the 2 fast indexers", len(res.cards))
	}
}

// Answering early is only acceptable because the rest still arrive.
func TestFanoutDeliversStragglersToTheCallback(t *testing.T) {
	srv := fakeProwlarr(t, 3, 400*time.Millisecond)
	s := fanoutServer(t, srv)

	var (
		mu   sync.Mutex
		full []card
	)
	done := make(chan struct{})
	res, err := s.searchFanout(context.Background(), "matrix", "", 100*time.Millisecond,
		func(c []card) {
			mu.Lock()
			full = c
			mu.Unlock()
			close(done)
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.cards) != 2 {
		t.Fatalf("immediate answer had %d cards, want 2", len(res.cards))
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow indexer never reported; its results would be lost every time")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(full) != 3 {
		t.Errorf("callback got %d cards, want all 3 including the straggler", len(full))
	}
}

// When everything is quick there is no reason to report a partial answer.
func TestFanoutIsNotPartialWhenAllAnswer(t *testing.T) {
	srv := fakeProwlarr(t, 3, 0)
	s := fanoutServer(t, srv)

	res, err := s.searchFanout(context.Background(), "matrix", "", 3*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.partial {
		t.Error("marked partial even though every indexer answered")
	}
	if len(res.cards) != 3 {
		t.Errorf("got %d cards, want 3", len(res.cards))
	}
}

// The handler, the warmer and the deferred cache write must agree on the key.
func TestSearchCacheKeyMatchesTheWarmerFormat(t *testing.T) {
	if got, want := searchCacheKey("The Matrix", ""), "\x00the matrix"; got != want {
		t.Errorf("key %q, want %q -- the warmer writes this form", got, want)
	}
	if searchCacheKey("x", "game") == searchCacheKey("x", "video") {
		t.Error("different kinds must not share a cache entry")
	}
}

// The global budget is what stops one search's stragglers from starving the
// next one. Per-search limits let five cold searches degrade to zero indexers.
func TestFanoutSlotsAreGlobalAndAlwaysReturned(t *testing.T) {
	if cap(prowlarrSlots) != fanoutConcurrency {
		t.Fatalf("slot budget is %d, want %d", cap(prowlarrSlots), fanoutConcurrency)
	}

	srv := fakeProwlarr(t, 3, 300*time.Millisecond)
	s := fanoutServer(t, srv)

	// Run several searches back to back, the pattern that broke it.
	for i := 0; i < 4; i++ {
		if _, err := s.searchFanout(context.Background(), fmt.Sprintf("q%d", i), "",
			80*time.Millisecond, func([]card) {}); err != nil {
			t.Fatal(err)
		}
	}

	// Every slot must come back, or the next search blocks forever.
	deadline := time.Now().Add(5 * time.Second)
	for len(prowlarrSlots) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(prowlarrSlots); n != 0 {
		t.Errorf("%d slot(s) still held after every search finished: they leak", n)
	}
}
