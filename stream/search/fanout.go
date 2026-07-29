package main

// Per-indexer search fan-out.
//
// The single aggregate call to Prowlarr returns only once every indexer has
// answered or timed out, so one slow indexer sets the response time for the
// whole search -- 8 to 29 seconds, against a floor of about 3.
//
// That was believed to be unavoidable because `indexerIds` was thought to be
// ignored. It is not: scoping a query to three indexers returns in 3.1s where
// the unscoped call takes 24.7s, and returns results from exactly those three.
// So the work can be split, and a straggler no longer has to hold the page.
//
// Nothing is dropped to achieve it. Every indexer is still queried; the
// response is simply sent once the deadline passes, and indexers still running
// finish into the cache. The next request for the same query -- the refresh, or
// the next visitor -- gets the complete set.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// How long a search may take before it answers with what it has. Chosen
	// from measured behaviour: the bulk of indexers land inside 3s, and the
	// tail runs to 30.
	fanoutDeadline = 5 * time.Second

	// Concurrent requests to Prowlarr. The total work is unchanged -- Prowlarr
	// queries these same indexers itself -- but this bounds how much of it is
	// in flight at once.
	fanoutConcurrency = 12

	// Enabled indexers change rarely, so the list is re-read occasionally
	// rather than on every search.
	indexerListTTL = 10 * time.Minute
)

type indexerRef struct {
	ID   int
	Name string
}

// indexers returns the enabled indexer ids, cached.
func (s *server) indexers(ctx context.Context) ([]indexerRef, error) {
	s.ixMu.Lock()
	if time.Now().Before(s.ixExpires) && len(s.ixCache) > 0 {
		defer s.ixMu.Unlock()
		return s.ixCache, nil
	}
	s.ixMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, "GET", s.prowlarrURL+"/api/v1/indexer", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", s.apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("prowlarr indexer list status %d", resp.StatusCode)
	}

	var raw []struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Enable bool   `json:"enable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	out := make([]indexerRef, 0, len(raw))
	for _, r := range raw {
		if r.Enable {
			out = append(out, indexerRef{ID: r.ID, Name: r.Name})
		}
	}
	// Stable order keeps logs and tests comparable between runs.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	s.ixMu.Lock()
	s.ixCache, s.ixExpires = out, time.Now().Add(indexerListTTL)
	s.ixMu.Unlock()
	return out, nil
}

// searchOne queries a single indexer.
func (s *server) searchOne(ctx context.Context, id int, q, kind string) ([]card, error) {
	u := fmt.Sprintf("%s/api/v1/search?query=%s&limit=200&indexerIds=%d",
		s.prowlarrURL, url.QueryEscape(q), id)
	for _, c := range categoriesFor(kind) {
		u += "&categories=" + strconv.Itoa(c)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", s.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("indexer %d: status %d", id, resp.StatusCode)
	}
	var raw []prowlarrResult
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return buildCards(raw), nil
}

// fanoutResult is what a search knows once it stops waiting.
type fanoutResult struct {
	cards    []card
	answered int  // indexers that returned in time
	total    int  // indexers asked
	partial  bool // true when the deadline cut it short
}

// searchFanout queries every enabled indexer concurrently and returns once the
// deadline passes or all have answered, whichever comes first.
//
// The context governs only this call. Stragglers are deliberately given a
// detached context by the caller so that returning early does not cancel the
// work whose results the cache still wants.
func (s *server) searchFanout(ctx context.Context, q, kind string,
	deadline time.Duration, onComplete func([]card)) (fanoutResult, error) {

	list, err := s.indexers(ctx)
	if err != nil {
		return fanoutResult{}, err
	}
	if len(list) == 0 {
		return fanoutResult{}, fmt.Errorf("no enabled indexers")
	}

	var (
		mu       sync.Mutex
		cards    []card
		answered int
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, fanoutConcurrency)
	done := make(chan struct{})

	for _, ix := range list {
		wg.Add(1)
		go func(ix indexerRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			got, err := s.searchOne(ctx, ix.ID, q, kind)
			if err != nil {
				// A single failing indexer is normal and must not fail the
				// search; it simply contributes nothing.
				return
			}
			mu.Lock()
			cards = append(cards, got...)
			answered++
			mu.Unlock()
		}(ix)
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(deadline)
	defer timer.Stop()

	partial := false
	select {
	case <-done:
	case <-timer.C:
		partial = true
	case <-ctx.Done():
		partial = true
	}

	mu.Lock()
	out := make([]card, len(cards))
	copy(out, cards)
	n := answered
	mu.Unlock()

	if partial && onComplete != nil {
		// Let the rest finish and hand the full set to the caller, which puts
		// it in the cache. Without this the slow indexers would be queried on
		// every request and never actually contribute.
		go func() {
			<-done
			mu.Lock()
			full := make([]card, len(cards))
			copy(full, cards)
			mu.Unlock()
			onComplete(full)
		}()
	}

	return fanoutResult{cards: out, answered: n, total: len(list), partial: partial}, nil
}

// searchCacheKey is the one place the cache key is built. The warmer, the
// handler and the fan-out's deferred write must agree on it exactly, or a
// warmed entry is stored under a key nothing ever reads.
func searchCacheKey(q, kind string) string {
	return kind + "\x00" + strings.ToLower(q)
}
