package main

// Two-tier search: fast indexers answer the request, slow ones fill the cache.
//
// The single aggregate call to Prowlarr returns only once every indexer has
// answered or timed out, so one slow indexer set the response time for the
// whole search -- 8 to 29 seconds against a floor of about 3.
//
// That looked unavoidable because `indexerIds` was believed to be ignored. It
// is not: a query scoped to three indexers returns in 3.1s where the unscoped
// call takes 24.7s, with results from exactly those three.
//
// The obvious use of that -- query every indexer separately and answer on a
// deadline -- was measured and is worse. It turns one request into 42, which
// Prowlarr cannot absorb: five cold searches in a row degraded from 13 of 42
// indexers answering to 0 of 42, because each search queued behind the
// previous one's outstanding work. Load, not latency, became the limit.
//
// So the split is by tier, not by indexer. Two calls: the fast indexers, whose
// result is the response, and the slow ones, which land in the cache for the
// next request. Same request count as the original design, without the tail.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// An indexer slower than this goes in the background tier. Measured: most
	// indexers land under 3s and the tail runs past 30.
	slowIndexerThreshold = 3500 * time.Millisecond

	// Ceiling on the foreground call, in case a "fast" indexer has a bad day.
	fastTierDeadline = 8 * time.Second

	// How long the background tier may run before it is abandoned.
	slowTierBudget = 45 * time.Second

	// Tiers are recomputed occasionally; indexer performance drifts slowly.
	indexerListTTL = 10 * time.Minute
)

type indexerTiers struct {
	fast []int
	slow []int
}

// tiers splits the enabled indexers by measured response time, from Prowlarr's
// own statistics. Cached, because it changes slowly and costs two calls.
func (s *server) tiers(ctx context.Context) (indexerTiers, error) {
	s.ixMu.Lock()
	if time.Now().Before(s.ixExpires) && len(s.ixCache.fast)+len(s.ixCache.slow) > 0 {
		defer s.ixMu.Unlock()
		return s.ixCache, nil
	}
	s.ixMu.Unlock()

	var enabled []int
	var raw []struct {
		ID     int  `json:"id"`
		Enable bool `json:"enable"`
	}
	if err := s.prowlarrJSON(ctx, "/api/v1/indexer", &raw); err != nil {
		return indexerTiers{}, err
	}
	for _, r := range raw {
		if r.Enable {
			enabled = append(enabled, r.ID)
		}
	}
	if len(enabled) == 0 {
		return indexerTiers{}, fmt.Errorf("no enabled indexers")
	}

	var stats struct {
		Indexers []struct {
			IndexerID           int `json:"indexerId"`
			AverageResponseTime int `json:"averageResponseTime"`
		} `json:"indexers"`
	}
	slowSet := map[int]bool{}
	if err := s.prowlarrJSON(ctx, "/api/v1/indexerstats", &stats); err == nil {
		for _, st := range stats.Indexers {
			if time.Duration(st.AverageResponseTime)*time.Millisecond > slowIndexerThreshold {
				slowSet[st.IndexerID] = true
			}
		}
	}
	// If the statistics are unavailable every indexer is treated as fast, which
	// degrades to the original single-call behaviour rather than to an empty
	// result.

	var t indexerTiers
	for _, id := range enabled {
		if slowSet[id] {
			t.slow = append(t.slow, id)
		} else {
			t.fast = append(t.fast, id)
		}
	}
	sort.Ints(t.fast)
	sort.Ints(t.slow)

	s.ixMu.Lock()
	s.ixCache, s.ixExpires = t, time.Now().Add(indexerListTTL)
	s.ixMu.Unlock()
	return t, nil
}

func (s *server) prowlarrJSON(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", s.prowlarrURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", s.apiKey)
	resp, err := prowlarrClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("prowlarr %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// searchTier runs one scoped Prowlarr search. An empty id list means every
// indexer, which is the unscoped call.
func (s *server) searchTier(ctx context.Context, ids []int, q, kind string) ([]card, error) {
	u := fmt.Sprintf("%s/api/v1/search?query=%s&limit=200", s.prowlarrURL, url.QueryEscape(q))
	for _, c := range categoriesFor(kind) {
		u += "&categories=" + strconv.Itoa(c)
	}
	for _, id := range ids {
		u += "&indexerIds=" + strconv.Itoa(id)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", s.apiKey)

	resp, err := prowlarrClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("prowlarr status %d", resp.StatusCode)
	}
	var raw []prowlarrResult
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return buildCards(raw), nil
}

// mergeCards concatenates two result sets, dropping repeats by cache key.
// The tiers are disjoint sets of indexers, but the same release is often
// listed by several of them.
func mergeCards(a, b []card) []card {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]card, 0, len(a)+len(b))
	for _, c := range append(append([]card{}, a...), b...) {
		if c.Key != "" && seen[c.Key] {
			continue
		}
		if c.Key != "" {
			seen[c.Key] = true
		}
		out = append(out, c)
	}
	return out
}

// searchTiered answers from the fast indexers and folds the slow ones into the
// cache behind the response.
// How long the indexer list may take before a search gives up on it.
//
// Deliberately short: this is a small local call that either answers at once or
// is not going to. Everything else in a search is bounded, so leaving this one
// open meant one sick backend could hold a request open indefinitely.
const indexerListDeadline = 12 * time.Second

// A client with a ceiling. http.DefaultClient has no timeout at all, which is
// fine for a script and wrong for a request path a person is waiting on: a peer
// that accepts the connection and then goes quiet holds the goroutine, the
// request and the user's patience for as long as it likes.
var prowlarrClient = &http.Client{Timeout: 110 * time.Second}

func (s *server) searchTiered(ctx context.Context, q, kind string,
	onFull func([]card)) ([]card, bool, error) {

	// Bounded, because this call sits before the fast-tier deadline below and
	// was therefore the only unbounded outbound request in the package. A
	// Prowlarr that accepts connections and never answers made every uncached
	// search hang forever: measured 280s with no response, and a browser fetch
	// still pending at 829s. Failing fast reaches the client's retry path,
	// which already exists and already says something useful.
	listCtx, listCancel := context.WithTimeout(ctx, indexerListDeadline)
	defer listCancel()
	t, err := s.tiers(listCtx)
	if err != nil {
		return nil, false, err
	}

	fastCtx, cancel := context.WithTimeout(ctx, fastTierDeadline)
	defer cancel()
	fast, err := s.searchTier(fastCtx, t.fast, q, kind)
	if err != nil {
		return nil, false, err
	}

	if len(t.slow) == 0 {
		return fast, false, nil
	}

	// The slow tier must outlive this request -- that is the whole point -- so
	// it runs on a context detached from the caller's.
	go func() {
		slowCtx, slowCancel := context.WithTimeout(
			context.WithoutCancel(ctx), slowTierBudget)
		defer slowCancel()

		slow, err := s.searchTier(slowCtx, t.slow, q, kind)
		if err != nil {
			// The slow tier failing costs the next request some coverage, not
			// this one an answer, so it is logged and dropped.
			log.Printf("slow tier %q: %v", q, err)
		}

		if len(slow) > 0 && onFull != nil {
			onFull(mergeCards(fast, slow))
		}
		// Per-server, not package-level: background passes outlive the call
		// that started them, so a shared hook lets one test's leftover
		// goroutine fire the next test's callback.
		if s.onSlowTierDone != nil {
			s.onSlowTierDone()
		}
	}()

	return fast, true, nil
}

// searchCacheKey is the one place the cache key is built. The warmer, the
// handler and the background tier's cache write must agree on it exactly, or a
// warmed entry is stored under a key nothing ever reads.
func searchCacheKey(q, kind string) string {
	return kind + "\x00" + strings.ToLower(q)
}
