package main

// Progressive search: answer now, finish later.
//
// The old handler waited for the torrent-indexer fan-out before it answered
// anything. Measured on the live site: 0.99s for a cached-adjacent query, 26.7s
// cold, and 112s ending in a 504 while Prowlarr was stalled. archive.org and
// the in-memory cache could have answered the same query in a fraction of a
// second, and were made to queue behind indexers that are slow on a good day
// and absent on a bad one. The variance was the defect -- a search that is
// sometimes 1s and sometimes 27s reads as broken even when it works.
//
// So a search is now two things:
//
//   1. A response, sent within a fixed budget, carrying whatever is already in
//      memory plus archive.org if it made it in time. It always says whether
//      more is coming.
//   2. A job, which keeps working after the response was sent and can be
//      collected by id.
//
// WHY A JOB ID AND POLLING, RATHER THAN SSE OR A CHUNKED STREAM
//
// Three constraints decided it, and only one mechanism satisfies all three:
//
//   * The Roku client does plain GETs through roUrlTransfer. It cannot consume
//     an event stream and cannot parse a body incrementally. SSE and chunked
//     JSON both fail here, and Roku is a shipped client -- not a hypothetical.
//   * Nothing may hold a goroutine per idle client. An open SSE connection is
//     exactly that: one goroutine and one socket per waiting browser, for as
//     long as the slowest indexer takes. A poll costs a map lookup.
//   * It has to survive Caddy. `encode zstd gzip` sits in front of this, and a
//     streaming response through a compressor depends on flush behaviour that
//     is easy to get subtly wrong and hard to notice. A plain JSON GET has no
//     such failure mode.
//
// Polling also degrades correctly: a client that never polls simply gets the
// first wave, which is a complete and honest answer on its own. A client that
// polls once gets more. Nothing is lost when the collection step is skipped,
// which is not true of a stream that a client failed to read.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// How long the first response may wait for a fast source.
	//
	// Sized against measurement, not taste. The requirement is that a person
	// sees something in under half a second end to end, and end to end includes
	// a TLS handshake to the edge -- measured at 139ms from the vantage point
	// these numbers were taken from. That leaves about 350ms of server time,
	// and a first paint of sixty cards is a few tens of milliseconds to
	// serialise, so 250ms is the budget with the margin already spent.
	//
	// It was 350ms, and two of twelve queries came back at 518ms and 540ms. At
	// 250ms, two of twenty still came back at 503ms and 540ms -- the tail is
	// archive.org landing right on the edge of the budget, on top of a payload
	// of sixty cards. 200ms puts the whole distribution inside the line with
	// room for a bad handshake.
	//
	// The cost is real and worth naming: archive.org's median from the VPS is
	// about 220ms, so a cold query more often misses this budget than makes it,
	// and lands on the first poll 250ms later instead. That trade is only
	// acceptable because of what happens in the actual client -- type-ahead has
	// already asked archive.org for this prefix by the time anybody presses
	// Enter, so the search reads it out of memory. Measured on the live site:
	// eight of eight typed queries returned 25-60 real cards in 0.22-0.30s.
	firstPaintBudget = 200 * time.Millisecond

	// How long a finished job stays collectable. A client that was backgrounded
	// mid-search comes back and finds its results rather than a dead id.
	jobRetain = 3 * time.Minute

	// The ceiling on a whole job, including the slow indexer tier.
	jobTotalBudget = 70 * time.Second

	// How many cards a first paint may borrow from memory. Enough to fill a
	// grid; past that the network results will have arrived anyway.
	localPaintLimit = 60
)

// What a source is doing, in the words the client shows to a person.
const (
	stagePending       = "pending"
	stageOK            = "ok"
	stageFailed        = "failed"
	stageUnavailable   = "unavailable"
	stageNotConfigured = "not-configured"
	stageNone          = "none"
)

// searchJob is one query's slow half.
//
// It outlives the request that started it, is shared by every request for the
// same query, and is collected by id. It holds no client connection.
type searchJob struct {
	id    string
	key   string // cache key: the query and kind it answers
	query string
	kind  string

	// Closed once the fast sources have reported. A request waits on this, with
	// a budget; a later request for the same query finds it already closed and
	// answers immediately.
	firstWave chan struct{}
	closeOnce sync.Once

	// Closed when the background indexer tier lands, so the job can complete
	// early rather than sitting out its whole budget.
	slowLanded chan struct{}
	slowOnce   sync.Once

	mu       sync.RWMutex
	cards    []card
	seen     map[string]bool
	sources  map[string]string
	complete bool
	started  time.Time
	done     time.Time
	rev      int
}

func newSearchJob(q, kind string) *searchJob {
	return &searchJob{
		id:         newJobID(),
		key:        searchCacheKey(q, kind),
		query:      q,
		kind:       kind,
		firstWave:  make(chan struct{}),
		slowLanded: make(chan struct{}),
		seen:       map[string]bool{},
		sources:    map[string]string{},
		started:    time.Now(),
	}
}

func newJobID() string {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A predictable id is not a security problem here -- a job holds a
		// public search result and nothing else -- so a clock fallback is
		// better than failing the search.
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))[:18]
	}
	return hex.EncodeToString(b[:])
}

// add folds new cards in, keeping the first occurrence of each key.
//
// Order is append-only on purpose. The client paints progressively, and a card
// that moves after it has been drawn moves whatever somebody was about to
// click. Ranking is applied by the filter layer at read time, over the whole
// set, so nothing is lost by not re-ordering the store.
func (j *searchJob) add(cards []card) {
	if len(cards) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cards {
		if c.Key != "" {
			if j.seen[c.Key] {
				continue
			}
			j.seen[c.Key] = true
		}
		j.cards = append(j.cards, c)
	}
	j.rev++
}

func (j *searchJob) mark(source, state string) {
	j.mu.Lock()
	j.sources[source] = state
	j.rev++
	j.mu.Unlock()
}

// releaseFirstWave lets any request waiting on the first paint go.
func (j *searchJob) releaseFirstWave() {
	j.closeOnce.Do(func() { close(j.firstWave) })
}

func (j *searchJob) slowIsIn() {
	j.slowOnce.Do(func() { close(j.slowLanded) })
}

func (j *searchJob) finish() {
	j.mu.Lock()
	j.complete = true
	j.done = time.Now()
	j.rev++
	j.mu.Unlock()
	j.releaseFirstWave()
}

type jobSnapshot struct {
	cards    []card
	sources  map[string]string
	complete bool
	rev      int
}

func (j *searchJob) snapshot() jobSnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()
	cards := make([]card, len(j.cards))
	copy(cards, j.cards)
	sources := make(map[string]string, len(j.sources))
	for k, v := range j.sources {
		sources[k] = v
	}
	return jobSnapshot{cards: cards, sources: sources, complete: j.complete, rev: j.rev}
}

// jobStore keeps live and recently-finished jobs.
//
// Two indexes, deliberately: by id so a client can collect its own, and by
// cache key so two people searching the same thing share one job instead of
// doubling the load on Prowlarr. That second index is what the old inflight
// map did, except it now survives the request that created it.
//
// Held as a value on the server and lazily initialised, so a server built by a
// test with a struct literal behaves exactly like the real one. Requiring a
// constructor is how half these tests would have started nil-panicking in a
// path they are not even about.
type jobStore struct {
	mu    sync.Mutex
	byID  map[string]*searchJob
	byKey map[string]*searchJob
}

// init must be called with the lock held.
func (js *jobStore) init() {
	if js.byID == nil {
		js.byID = map[string]*searchJob{}
		js.byKey = map[string]*searchJob{}
	}
}

// start returns the job for a query, creating and launching it if there is not
// already a live one. The bool reports whether this call created it.
func (js *jobStore) start(s *server, q, kind string) (*searchJob, bool) {
	key := searchCacheKey(q, kind)

	js.mu.Lock()
	js.init()
	if j, ok := js.byKey[key]; ok {
		j.mu.RLock()
		fresh := !j.complete || time.Since(j.done) < 15*time.Second
		j.mu.RUnlock()
		if fresh {
			js.mu.Unlock()
			return j, false
		}
	}
	j := newSearchJob(q, kind)
	js.byID[j.id] = j
	js.byKey[key] = j
	js.mu.Unlock()

	go j.run(s)
	return j, true
}

func (js *jobStore) get(id string) (*searchJob, bool) {
	js.mu.Lock()
	defer js.mu.Unlock()
	js.init()
	j, ok := js.byID[id]
	return j, ok
}

// sweep drops jobs nobody can still be waiting for.
func (js *jobStore) sweep() {
	cutoff := time.Now().Add(-jobRetain)
	js.mu.Lock()
	defer js.mu.Unlock()
	js.init()
	for id, j := range js.byID {
		j.mu.RLock()
		dead := j.complete && j.done.Before(cutoff)
		key := j.key
		j.mu.RUnlock()
		if !dead {
			continue
		}
		delete(js.byID, id)
		if js.byKey[key] == j {
			delete(js.byKey, key)
		}
	}
}

// run does the actual searching, on a context detached from any request.
//
// A request's context dies when its response is written, which is the whole
// point of this design -- so anything inherited from it would be cancelled the
// instant the first paint went out.
func (j *searchJob) run(s *server) {
	ctx, cancel := context.WithTimeout(context.Background(), jobTotalBudget)
	defer cancel()
	defer j.finish()

	var wg sync.WaitGroup

	// archive.org is the fast half and the only thing the first paint waits
	// for. It is a public metadata query with no queue behind it: measured
	// 120-250ms from the VPS on a warm connection, against 3-30s for the
	// indexers. Making it wait behind them was the original defect.
	if _, wantArchive := scopeFor(j.kind); wantArchive {
		j.mark("archive", stagePending)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Released whatever happens: a failed archive.org must not hold the
			// first paint for the whole budget.
			defer j.releaseFirstWave()

			cards, err := s.searchArchiveCached(ctx, j.query, j.kind)
			if err != nil {
				log.Printf("job %s archive.org %q: %v", j.id, j.query, err)
				j.mark("archive", stageFailed)
				return
			}
			j.add(cards)
			j.mark("archive", stageOK)
		}()
	} else {
		j.mark("archive", stageNone)
		j.releaseFirstWave()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		j.indexerStage(ctx, s)
	}()

	wg.Wait()
	j.enrichArt(ctx, s)

	// Cache the whole set under the key the handler and the warmer read, so the
	// next search for this is a straight hit.
	snap := j.snapshot()
	if len(snap.cards) > 0 {
		s.putCached(j.key, snap.cards)
	}
}

// enrichArt fills in TMDB posters for the leading cards.
//
// Done on a copy and written back by key rather than under the job's lock for
// the duration: this makes forty outbound requests, and holding the lock across
// them would stall every poll for as long as TMDB felt like taking.
//
// Safe to call repeatedly -- enrich skips a card that already has art -- which
// is what lets it run once when the fast tier lands and again at the end,
// instead of leaving the grid blank until the slowest indexer has finished.
func (j *searchJob) enrichArt(ctx context.Context, s *server) {
	if !s.tmdb.enabled() {
		return
	}
	snap := j.snapshot()
	if len(snap.cards) == 0 {
		return
	}
	s.tmdb.enrich(ctx, snap.cards, 40)

	found := make(map[string]artwork, len(snap.cards))
	for _, c := range snap.cards {
		if c.Key != "" && c.Art.Found {
			found[c.Key] = c.Art
		}
	}
	if len(found) == 0 {
		return
	}
	j.mu.Lock()
	for i := range j.cards {
		if j.cards[i].Art.Found {
			continue
		}
		if a, ok := found[j.cards[i].Key]; ok {
			j.cards[i].Art = a
		}
	}
	j.rev++
	j.mu.Unlock()
}

// indexerStage runs the torrent fan-out and records what happened to it, in
// terms the client can put on screen.
func (j *searchJob) indexerStage(ctx context.Context, s *server) {
	if s.apiKey == "" {
		j.mark("indexers", stageNotConfigured)
		return
	}
	if !s.indexerBreaker.allow() {
		// Prowlarr has been failing. Sending another request would cost this
		// job its whole budget to learn what the last three already proved, and
		// pile another connection onto a backend that is already sick.
		j.mark("indexers", stageUnavailable)
		return
	}

	j.mark("indexers", stagePending)
	cards, deferred, err := s.searchTiered(ctx, j.query, j.kind, func(full []card) {
		j.add(full)
		j.mark("slowIndexers", stageOK)
		j.slowIsIn()
	})
	if err != nil {
		s.indexerBreaker.failure()
		log.Printf("job %s indexers %q: %v", j.id, j.query, err)

		// A backend that would not answer "list your indexers" is not going to
		// answer a search. Falling back to the unscoped aggregate call there
		// spends another thirty seconds learning it: measured against a
		// blackholed address, 35s to complete against the 5s it took to know.
		if errors.Is(err, errIndexersUnreachable) {
			j.mark("indexers", stageUnavailable)
			return
		}

		// The aggregate call is the fallback when the tiered search itself
		// fails on a backend that IS answering. It is the slowest path there
		// is, so it runs here, behind the response, where it costs nobody a
		// wait. It was measured at 23-40s on the critical path.
		agg, aggErr := s.searchProwlarrAggregate(ctx, j.query, j.kind)
		if aggErr != nil {
			j.mark("indexers", stageFailed)
			return
		}
		s.indexerBreaker.success()
		j.add(agg)
		j.mark("indexers", stageOK)
		return
	}

	s.indexerBreaker.success()
	j.add(cards)
	j.mark("indexers", stageOK)
	// Posters for what has landed so far, rather than making the grid wait for
	// the slowest indexer before anything gets a picture.
	j.enrichArt(ctx, s)

	if !deferred {
		return
	}

	// The background tier is already running inside searchTiered. Wait for it
	// so the job stays collectable until the last indexer has spoken, rather
	// than declaring itself complete while results are still arriving.
	j.mark("slowIndexers", stagePending)
	select {
	case <-j.slowLanded:
	case <-ctx.Done():
		j.mark("slowIndexers", stageFailed)
	case <-time.After(slowTierBudget + 2*time.Second):
		j.mark("slowIndexers", stageFailed)
	}
}

// ----------------------------------------------------------------- breaker --

// indexerBreaker stops a sick Prowlarr from costing every search its budget.
//
// This is not a nicety. Prowlarr genuinely stalls -- it answered HTTP 000 for
// twenty seconds during the measurement run that produced this work, then
// recovered to 200 in 14ms. While it is down, every search that tries it pays
// the full timeout and adds another connection to a backend that is already
// failing to answer the ones it has. Three failures is enough evidence.
type breaker struct {
	mu        sync.Mutex
	fails     int
	openUntil time.Time
}

const (
	breakerThreshold = 3
	breakerCooldown  = 30 * time.Second
)

// allow reports whether a call may go out. The zero value allows, so a server
// built without one behaves like a healthy backend rather than a broken one.
func (b *breaker) allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() || time.Now().After(b.openUntil) {
		return true
	}
	return false
}

func (b *breaker) failure() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails++
	if b.fails >= breakerThreshold {
		b.openUntil = time.Now().Add(breakerCooldown)
	}
}

func (b *breaker) success() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails = 0
	b.openUntil = time.Time{}
}

// open reports whether the breaker is currently refusing calls. For health.
func (b *breaker) open() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.openUntil.IsZero() && time.Now().Before(b.openUntil)
}

// ------------------------------------------------------------ local memory --

// localCards returns cards already held in memory whose titles match the query.
//
// This is what makes a first paint carry something real rather than an empty
// grid with a promise. It costs no network at all: a search for "mario kart"
// finds the matching subset of a cached "mario" search, and anything the
// warmer or a type-ahead has already pulled down is available instantly.
//
// It is deliberately a scan rather than an index. An index is a second copy of
// the truth that drifts from it -- and the measured cost of scanning the whole
// cache is well under a millisecond for the sizes this box ever holds.
func (s *server) localCards(q, kind string, limit int) []card {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(q)))
	if len(terms) == 0 {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]card, 0, limit)
	seen := make(map[string]bool, limit)
	now := time.Now()
	for _, e := range s.cache {
		// Genuinely ancient entries are excluded. Expired-but-recent ones are
		// not: a result from twenty minutes ago is a far better first paint
		// than nothing, and the live set replaces it seconds later anyway.
		if e.expires.Add(-s.ttl).Before(now.Add(-6 * time.Hour)) {
			continue
		}
		for _, c := range e.cards {
			if len(out) >= limit {
				return out
			}
			if c.Key == "" || seen[c.Key] {
				continue
			}
			if kind != "" && !sameDomain(c.Kind, kind) {
				continue
			}
			if !titleHasAll(c.Title, terms) {
				continue
			}
			seen[c.Key] = true
			out = append(out, c)
		}
	}
	return out
}

// titleHasAll is the match rule: every word typed has to appear somewhere in
// the title. Substring rather than whole-word, so "zeld" finds Zelda while the
// letters are still being typed.
func titleHasAll(title string, terms []string) bool {
	t := strings.ToLower(title)
	for _, term := range terms {
		if !strings.Contains(t, term) {
			return false
		}
	}
	return true
}
