package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProwlarr serves an indexer list, stats marking id 3 as slow, and scoped
// searches where the slow indexer takes longer than the fast ones.
func fakeProwlarr(t *testing.T, slowFor time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/indexer", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "enable": true},
			{"id": 2, "enable": true},
			{"id": 3, "enable": true},
			{"id": 9, "enable": false},
		})
	})

	mux.HandleFunc("/api/v1/indexerstats", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"indexers": []map[string]any{
			{"indexerId": 1, "averageResponseTime": 200},
			{"indexerId": 2, "averageResponseTime": 400},
			{"indexerId": 3, "averageResponseTime": 30000},
		}})
	})

	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["indexerIds"]
		out := []prowlarrResult{}
		for _, id := range ids {
			if id == "3" {
				select {
				case <-time.After(slowFor):
				case <-r.Context().Done():
					return
				}
			}
			out = append(out, prowlarrResult{
				Title: "from " + id, Indexer: "ix" + id, Seeders: 5,
				Protocol: "torrent", MagnetURL: "magnet:?xt=urn:btih:" + id,
				GUID: "guid-" + id,
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func tierServer(t *testing.T, srv *httptest.Server) *server {
	t.Helper()
	return &server{
		prowlarrURL: srv.URL,
		apiKey:      "test",
		ttl:         time.Minute,
		cache:       map[string]cacheEntry{},
		inflight:    map[string]chan struct{}{},
	}
}

// Prowlarr's own statistics decide the split, so it adapts as indexers change.
func TestTiersSplitOnMeasuredResponseTime(t *testing.T) {
	s := tierServer(t, fakeProwlarr(t, 0))
	got, err := s.tiers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.fast) != 2 || len(got.slow) != 1 {
		t.Fatalf("split was fast=%v slow=%v, want 2 fast and 1 slow", got.fast, got.slow)
	}
	if got.slow[0] != 3 {
		t.Errorf("slow tier is %v, want the 30s indexer (id 3)", got.slow)
	}
	for _, id := range append(got.fast, got.slow...) {
		if id == 9 {
			t.Error("a disabled indexer was included")
		}
	}
}

// The response must not wait on the slow tier.
func TestTieredSearchDoesNotWaitForTheSlowTier(t *testing.T) {
	s := tierServer(t, fakeProwlarr(t, 2*time.Second))

	start := time.Now()
	cards, deferred, err := s.searchTiered(context.Background(), "matrix", "", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > time.Second {
		t.Fatalf("took %s: the slow tier held the response", elapsed)
	}
	if !deferred {
		t.Error("should report that a slow tier is still running")
	}
	if len(cards) != 2 {
		t.Errorf("got %d cards, want the 2 fast indexers", len(cards))
	}
}

// Answering early is only acceptable because the slow tier still lands.
func TestSlowTierReachesTheCache(t *testing.T) {
	s := tierServer(t, fakeProwlarr(t, 150*time.Millisecond))

	var (
		mu   sync.Mutex
		full []card
	)
	done := make(chan struct{})
	s.onSlowTierDone = func() { close(done) }

	if _, _, err := s.searchTiered(context.Background(), "matrix", "", func(c []card) {
		mu.Lock()
		full = c
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow tier never completed; its results would be lost every time")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(full) != 3 {
		t.Errorf("cache got %d cards, want all 3 tiers merged", len(full))
	}
}

// The tiers overlap in content even though they do not overlap in indexers.
func TestMergeCardsDropsDuplicatesByKey(t *testing.T) {
	a := []card{{Key: "x"}, {Key: "y"}}
	b := []card{{Key: "y"}, {Key: "z"}}
	if got := mergeCards(a, b); len(got) != 3 {
		t.Errorf("merged to %d cards, want 3 with the repeat dropped", len(got))
	}
	// A card with no key must not collapse into other keyless cards.
	if got := mergeCards([]card{{}, {}}, nil); len(got) != 2 {
		t.Errorf("keyless cards collapsed to %d, want 2", len(got))
	}
}

// Without stats every indexer is treated as fast, which is the original
// single-call behaviour -- never an empty result.
func TestMissingStatsDegradesToOneCall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/indexer", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "enable": true}, {"id": 2, "enable": true},
		})
	})
	mux.HandleFunc("/api/v1/indexerstats", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]prowlarrResult{
			{Title: "a", GUID: "g1", MagnetURL: "magnet:?xt=urn:btih:1", Protocol: "torrent"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s := tierServer(t, srv)
	got, err := s.tiers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.slow) != 0 || len(got.fast) != 2 {
		t.Fatalf("fast=%v slow=%v, want everything fast when stats are missing",
			got.fast, got.slow)
	}
	_, deferred, err := s.searchTiered(context.Background(), "q", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if deferred {
		t.Error("nothing should be deferred when there is no slow tier")
	}
}

// The handler, the warmer and the cache write must agree on the key.
func TestSearchCacheKeyMatchesTheWarmerFormat(t *testing.T) {
	if got, want := searchCacheKey("The Matrix", ""), "\x00the matrix"; got != want {
		t.Errorf("key %q, want %q -- the warmer writes this form", got, want)
	}
	if searchCacheKey("x", "game") == searchCacheKey("x", "video") {
		t.Error("different kinds must not share a cache entry")
	}
	if !strings.HasPrefix(searchCacheKey("A", "game"), "game\x00") {
		t.Error("kind must prefix the key")
	}
}
