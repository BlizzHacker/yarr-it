package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// Background cache warming.
//
// A cold search takes ~14 seconds, because Prowlarr fans out to 48 indexers and
// there is no way to split that call -- indexerIds is ignored, so the query is
// all-or-nothing. A warm search takes ~70ms.
//
// The gap is closable because the most common path is predictable: people open
// the site, look at the trending rails, and click one. Those titles are known
// hours in advance, so they can be searched while nobody is waiting. Clicking a
// trending poster then hits cache and feels instant.
//
// Warming is deliberately unhurried -- one query at a time with a gap between,
// so it never competes with a real user's search for Prowlarr's attention.

const (
	warmInterval  = 45 * time.Second
	warmBatchSize = 24
	warmStagger   = 20 * time.Second

	// The cache must outlive a full warm cycle, or warming cannot work at all:
	// at one title every 45s, 24 titles take 18 minutes, so a 15 minute TTL
	// expired the earliest entries before the warmer had finished the batch and
	// could return to them. Every rail click stayed a cold 8s search while the
	// warmer looked busy.
	//
	// 45 minutes leaves the whole batch warm with room for a cycle to run long.
	// Torrent results are stable over that window -- a release that exists now
	// still exists in half an hour -- so the staleness costs nothing real.
	defaultTTL = 45 * time.Minute
)

type warmer struct {
	srv *server

	mu    sync.Mutex
	queue []string
	seen  map[string]time.Time
}

func newWarmer(s *server) *warmer {
	return &warmer{srv: s, seen: make(map[string]time.Time)}
}

// enqueue adds titles worth pre-searching, most-promising first.
func (w *warmer) enqueue(titles []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range titles {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		// Do not re-warm something searched recently; the cache still holds it.
		if last, ok := w.seen[strings.ToLower(t)]; ok && time.Since(last) < time.Hour {
			continue
		}
		w.queue = append(w.queue, t)
	}
	if len(w.queue) > 200 {
		w.queue = w.queue[:200]
	}
}

func (w *warmer) next() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return "", false
	}
	t := w.queue[0]
	w.queue = w.queue[1:]
	w.seen[strings.ToLower(t)] = time.Now()
	return t, true
}

// run pulls titles off the queue slowly and forever.
func (w *warmer) run() {
	// Let the service settle before adding load.
	time.Sleep(warmStagger)
	ticker := time.NewTicker(warmInterval)
	defer ticker.Stop()

	for range ticker.C {
		title, ok := w.next()
		if !ok {
			w.refill()
			continue
		}
		key := "\x00" + strings.ToLower(title)
		if _, fresh := w.srv.getCached(key); fresh {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		cards, err := w.srv.searchProwlarr(ctx, title, "")
		if err == nil && len(cards) > 0 {
			w.srv.tmdb.enrich(ctx, cards, 24)
			w.srv.putCached(key, cards)
			log.Printf("warmed %q (%d cards)", title, len(cards))
		}
		cancel()
	}
}

// refill tops the queue up from the current discover rows, so warming follows
// whatever is actually being shown on the landing page.
func (w *warmer) refill() {
	w.srv.discover.mu.RLock()
	rows := w.srv.discover.rows
	w.srv.discover.mu.RUnlock()

	titles := make([]string, 0, warmBatchSize)
	for _, row := range rows {
		for i, item := range row.Items {
			if i >= 6 { // a few from each rail rather than all of one
				break
			}
			if item.Year > 0 {
				titles = append(titles, item.Title)
			}
		}
	}
	if len(titles) > warmBatchSize {
		titles = titles[:warmBatchSize]
	}
	w.enqueue(titles)
}
