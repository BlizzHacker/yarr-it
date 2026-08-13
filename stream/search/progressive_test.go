package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmptyBrowseNeverFansOutToTorrentIndexers(t *testing.T) {
	j := newSearchJob("   ", "music")
	j.indexerStage(context.Background(), &server{})
	if got := j.snapshot().sources["indexers"]; got != stageNone {
		t.Fatalf("empty browse marked indexers %q, want %q", got, stageNone)
	}
}

// Progressive search.
//
// The property under test throughout is the one the old design got wrong: how
// long a person waits before they see anything. Every test here starts a search
// against a backend that is slow, dead or missing, and asserts that the
// response still arrives -- with whatever was genuinely available -- inside a
// budget somebody would call instant.

// A Prowlarr that accepts the connection and then says nothing. This is the
// failure that hung searches for 112 seconds; refusing the connection outright
// would not reproduce it.
func silentProwlarr(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An archive.org stub. `delay` models the real thing, which answers in roughly
// 120-250ms from the VPS on a warm connection.
func stubArchive(t *testing.T, delay time.Duration, titles ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		docs := make([]map[string]any, 0, len(titles))
		for i, title := range titles {
			docs = append(docs, map[string]any{
				"identifier": fmt.Sprintf("stub_%d_%s", i, strings.ReplaceAll(title, " ", "_")),
				"title":      title,
				"emulator":   "nes",
				"downloads":  1000 - i,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{"numFound": len(docs), "docs": docs},
		})
	}))
	t.Cleanup(srv.Close)

	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	t.Cleanup(func() { archiveSearchAPI = old })
	return srv
}

func newTestServer() *server {
	s := &server{
		cache:    map[string]cacheEntry{},
		inflight: map[string]chan struct{}{},
		ttl:      time.Minute,
	}
	s.tmdb = newTMDB("")
	s.igdb = newIGDB("", "")
	return s
}

func doSearch(t *testing.T, s *server, path string) (*httptest.ResponseRecorder, map[string]any, time.Duration) {
	t.Helper()
	rec := httptest.NewRecorder()
	start := time.Now()
	s.handleSearch(rec, httptest.NewRequest("GET", path, nil))
	took := time.Since(start)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response was not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return rec, body, took
}

func cardTitles(body map[string]any) []string {
	raw, _ := body["cards"].([]any)
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		if m, ok := c.(map[string]any); ok {
			out = append(out, fmt.Sprint(m["title"]))
		}
	}
	return out
}

// The whole point. A dead indexer must cost the response nothing at all: the
// answer goes out on the first-paint budget carrying whatever was ready.
//
// Measured before this: 26.7s cold and 112s while Prowlarr stalled, ending in a
// 504. The variance was the defect -- a search that is sometimes 1s and
// sometimes 27s reads as broken even when it works.
func TestAStalledIndexerCostsTheResponseNothing(t *testing.T) {
	stubArchive(t, 30*time.Millisecond, "Super Mario Bros")
	s := newTestServer()
	s.prowlarrURL = silentProwlarr(t).URL
	s.apiKey = "test"

	rec, body, took := doSearch(t, s, "/api/search?q=mario&kind=game&minSeeders=0")

	if rec.Code != 200 {
		t.Fatalf("status %d; a slow indexer must not become an error page", rec.Code)
	}
	if took > firstPaintBudget+400*time.Millisecond {
		t.Fatalf("first paint took %s; the response waited on the indexers", took)
	}
	if got := cardTitles(body); len(got) == 0 {
		t.Fatal("no cards; archive.org answered in 30ms and was not used")
	}
	if body["pending"] != true {
		t.Error("pending is not set; the client cannot tell more is coming")
	}
	if body["job"] == nil || body["job"] == "" {
		t.Error("no job id; there is no way to collect the rest")
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("progressive first paint has Cache-Control %q; a shared cache may pin an empty answer", cc)
	}
}

// With no indexer configured at all, a search still has to work -- archive.org
// is a complete answer for the kinds it covers, and this is how a fresh
// self-host behaves before anybody has pasted in a Prowlarr key.
func TestNoIndexerConfiguredStillAnswersFromArchive(t *testing.T) {
	stubArchive(t, 0, "Zelda", "Zelda II")
	s := newTestServer() // apiKey deliberately empty

	rec, body, _ := doSearch(t, s, "/api/search?q=zelda&kind=game&minSeeders=0")

	if rec.Code != 200 {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if len(cardTitles(body)) != 2 {
		t.Fatalf("got %v, want both archive.org results", cardTitles(body))
	}
	src, _ := body["sources"].(map[string]any)
	if src["indexers"] != stageNotConfigured {
		t.Errorf("indexers reported as %v, want %q -- the client must be able to say why",
			src["indexers"], stageNotConfigured)
	}
}

// A kind archive.org has no scope for, with no indexer configured, genuinely
// cannot be answered. Saying "try again in a moment" there is false advice: the
// user retries forever and waiting never fixes it.
//
// This used to be written with `kind=audio`, which had no scope because the
// music domain had no archive.org source at all -- the defect music.go exists
// to fix. Every domain in schema.json now has one, so the state under test has
// to be produced rather than found. That is not a weaker test: the branch is
// live the moment a seventh domain is added without a scope, which is exactly
// when somebody needs it to behave.
func TestAnUnanswerableSearchSaysWhyRatherThanPending(t *testing.T) {
	withoutArchiveScope(t, "music")
	s := newTestServer()

	rec := httptest.NewRecorder()
	s.handleSearch(rec, httptest.NewRequest("GET", "/api/search?q=dune&kind=audio", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !strings.Contains(strings.ToLower(got["error"]), "indexer") {
		t.Errorf("message %q never names the problem", got["error"])
	}
}

// The rest of the search has to be collectable, or the first paint is just a
// truncated answer wearing a promise.
func TestTheRestOfASearchIsCollectableByJobID(t *testing.T) {
	stubArchive(t, 0, "Metroid")

	// A Prowlarr that answers, slowly, with one torrent.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/indexer"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "enable": true}})
		case strings.Contains(r.URL.Path, "/search"):
			time.Sleep(150 * time.Millisecond)
			_ = json.NewEncoder(w).Encode([]prowlarrResult{{
				Title: "Metroid.Prime.2002.1080p.BluRay.x264-GRP", Indexer: "stub",
				Protocol: "torrent", Seeders: 42, Size: 1 << 30,
				MagnetURL: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Categories: []struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}{{ID: 2000, Name: "Movies"}},
			}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer slow.Close()

	s := newTestServer()
	s.prowlarrURL = slow.URL
	s.apiKey = "test"

	_, first, _ := doSearch(t, s, "/api/search?q=metroid&minSeeders=0")
	jobID, _ := first["job"].(string)
	if jobID == "" {
		t.Fatal("no job id on the first paint")
	}
	firstCount := len(cardTitles(first))

	// Collect until it says it is finished.
	var last map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, body, _ := doSearch(t, s,
			"/api/search/updates?job="+jobID+"&minSeeders=0")
		last = body
		if body["complete"] == true {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last == nil || last["complete"] != true {
		t.Fatal("the job never completed")
	}
	if got := len(cardTitles(last)); got <= firstCount {
		t.Fatalf("collected %d cards against %d at first paint; nothing arrived after the response",
			got, firstCount)
	}
}

// A poll must do no work. That is the property that makes polling affordable
// where an open stream would not be -- and a poll that started a search would
// turn a backgrounded browser tab into a load generator.
func TestCollectingAnUnknownJobStartsNothing(t *testing.T) {
	var hits int32
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer prowlarr.Close()

	s := newTestServer()
	s.prowlarrURL = prowlarr.URL
	s.apiKey = "test"

	rec := httptest.NewRecorder()
	s.handleSearch(rec, httptest.NewRequest("GET", "/api/search/updates?job=nosuchjob", nil))

	if rec.Code != http.StatusGone {
		t.Errorf("status %d, want 410 -- the id was real once, the request is not malformed", rec.Code)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("a poll for an unknown job made %d upstream calls; it must make none", n)
	}
}

// Two people searching the same thing at the same moment must share one job.
// Otherwise the fix for slow searches doubles the load that made them slow.
func TestConcurrentSearchesForTheSameQueryShareOneJob(t *testing.T) {
	stubArchive(t, 40*time.Millisecond, "Sonic")
	s := newTestServer()
	s.prowlarrURL = silentProwlarr(t).URL
	s.apiKey = "test"

	_, a, _ := doSearch(t, s, "/api/search?q=sonic&kind=game&minSeeders=0")
	_, b, _ := doSearch(t, s, "/api/search?q=sonic&kind=game&minSeeders=0")

	if a["job"] != b["job"] {
		t.Errorf("two searches for the same query got jobs %v and %v", a["job"], b["job"])
	}
}

// A search must paint something real before any network call returns, when
// memory already holds a matching answer. This is what makes "mario kart"
// instant once "mario" has been searched.
func TestAFirstPaintBorrowsMatchingCardsFromMemory(t *testing.T) {
	stubArchive(t, 5*time.Second) // far too slow to make the first paint
	s := newTestServer()
	s.prowlarrURL = silentProwlarr(t).URL
	s.apiKey = "test"

	s.putCached(searchCacheKey("mario", "game"), []card{{
		Key: "mario-kart-64", Title: "Mario Kart 64", Kind: "game", Instant: true,
		Sources: []source{{Title: "Mario Kart 64", Indexer: "Archive.org", WebSafe: true}},
	}})

	_, body, took := doSearch(t, s, "/api/search?q=mario+kart&kind=game&minSeeders=0")

	if took > firstPaintBudget+400*time.Millisecond {
		t.Fatalf("took %s; memory is not being read before the network", took)
	}
	if got := cardTitles(body); len(got) != 1 || got[0] != "Mario Kart 64" {
		t.Fatalf("got %v, want the cached Mario Kart card", got)
	}
}

// Every word typed has to appear. Matching on any word instead of all of them
// makes a longer query return more results than a shorter one, which is the
// opposite of what typing more is for.
func TestLocalMatchingRequiresEveryWord(t *testing.T) {
	s := newTestServer()
	s.putCached(searchCacheKey("x", ""), []card{
		{Key: "a", Title: "Super Mario Kart", Kind: "game"},
		{Key: "b", Title: "Mario Party", Kind: "game"},
		{Key: "c", Title: "Kart Racer", Kind: "game"},
	})

	got := s.localCards("mario kart", "", 10)
	if len(got) != 1 || got[0].Title != "Super Mario Kart" {
		t.Fatalf("got %v, want only Super Mario Kart", titlesOf(got))
	}
}

func titlesOf(cards []card) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.Title)
	}
	return out
}

// A cache hit is a complete answer and must say so, or the client polls a job
// that does not exist.
func TestACacheHitIsCompleteAndCarriesNoJob(t *testing.T) {
	s := newTestServer()
	s.putCached(searchCacheKey("dune", "video"), []card{{
		Key: "dune-2021", Title: "Dune", Kind: "video", Seeders: 90,
		Sources: []source{{Title: "Dune.2021", Seeders: 90, Size: 1 << 30}},
	}})

	rec, body, _ := doSearch(t, s, "/api/search?q=dune&kind=video")

	if rec.Header().Get("X-Cache") != "HIT" {
		t.Errorf("X-Cache %q, want HIT", rec.Header().Get("X-Cache"))
	}
	if body["complete"] != true || body["pending"] != false {
		t.Errorf("a cache hit reported pending=%v complete=%v", body["pending"], body["complete"])
	}
	if body["job"] != nil {
		t.Errorf("a cache hit carried job %v; there is nothing to collect", body["job"])
	}
}

// ----------------------------------------------------------------- breaker --

// A backend that has failed three times in a row is down, not unlucky. Asking
// it again costs the next search its whole budget to learn what the last three
// already proved, and adds another connection to something already drowning.
func TestTheBreakerStopsAskingADeadIndexer(t *testing.T) {
	var b breaker

	if !b.allow() {
		t.Fatal("a fresh breaker refuses calls; a healthy backend would never be asked")
	}
	b.failure()
	b.failure()
	if !b.allow() {
		t.Error("two failures tripped it; that is not enough evidence")
	}
	b.failure()
	if b.allow() {
		t.Error("three consecutive failures did not trip it")
	}
	if !b.open() {
		t.Error("open() disagrees with allow()")
	}
	b.success()
	if !b.allow() || b.open() {
		t.Error("a success did not close it; recovery would never be noticed")
	}
}

// With the breaker open, a search must still answer from archive.org and must
// say the indexers are unavailable -- not silently return a short list that
// looks like a working search finding little.
func TestAnOpenBreakerIsReportedRatherThanHidden(t *testing.T) {
	stubArchive(t, 0, "Castlevania")
	s := newTestServer()
	s.prowlarrURL = silentProwlarr(t).URL
	s.apiKey = "test"
	s.indexerBreaker.failure()
	s.indexerBreaker.failure()
	s.indexerBreaker.failure()

	rec, body, took := doSearch(t, s, "/api/search?q=castlevania&kind=game&minSeeders=0")

	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if took > firstPaintBudget+400*time.Millisecond {
		t.Fatalf("took %s with the breaker open; nothing should have been asked", took)
	}
	if len(cardTitles(body)) == 0 {
		t.Fatal("no cards; archive.org was available and was not used")
	}
	// Collect once -- the job has nothing to wait for, so it finishes at once.
	jobID, _ := body["job"].(string)
	deadline := time.Now().Add(3 * time.Second)
	var src map[string]any
	for time.Now().Before(deadline) {
		_, upd, _ := doSearch(t, s, "/api/search/updates?job="+jobID)
		src, _ = upd["sources"].(map[string]any)
		if upd["complete"] == true {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if src["indexers"] != stageUnavailable {
		t.Errorf("indexers reported as %v, want %q", src["indexers"], stageUnavailable)
	}
}

// A backend that will not answer the indexer list -- a call that returns in
// milliseconds when it is well -- is unreachable, not merely slow. Retrying
// with the unscoped aggregate call there costs thirty more seconds to prove
// what five already established, and it is thirty seconds of another connection
// piled onto something already failing.
func TestAnUnreachableBackendIsNotRetriedWithTheSlowestCallThereIs(t *testing.T) {
	stubArchive(t, 0, "Total Recall")
	s := newTestServer()
	s.prowlarrURL = silentProwlarr(t).URL
	s.apiKey = "test"

	_, body, _ := doSearch(t, s, "/api/search?q=total+recall&kind=game&minSeeders=0")
	jobID, _ := body["job"].(string)

	// The list call gives up at indexerListDeadline. Anything much past that is
	// the aggregate fallback running against a host that cannot answer.
	deadline := time.Now().Add(indexerListDeadline + 5*time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		_, upd, _ := doSearch(t, s, "/api/search/updates?job="+jobID)
		last = upd
		if upd["complete"] == true {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last["complete"] != true {
		t.Fatalf("the job was still running %s after the list deadline; it fell through to the aggregate call",
			indexerListDeadline)
	}
	src, _ := last["sources"].(map[string]any)
	if src["indexers"] != stageUnavailable {
		t.Errorf("indexers reported as %v, want %q", src["indexers"], stageUnavailable)
	}
}

// ----------------------------------------------------------------- suggest --

// Type-ahead has to answer from memory. Anything that asks an indexer cannot
// keep up with typing, and anything that has to be told about new results
// separately will drift from them.
func TestSuggestionsComeFromMemoryWithoutTouchingTheNetwork(t *testing.T) {
	s := newTestServer()
	s.prowlarrURL = silentProwlarr(t).URL
	s.apiKey = "test"
	// Enough local matches that archive.org is not worth waiting for, which is
	// the path under test: the answer comes entirely out of memory.
	s.putCached(searchCacheKey("zelda", "game"), []card{
		{Key: "z1", Title: "The Legend of Zelda", Kind: "game", Instant: true},
		{Key: "z2", Title: "Zelda II: The Adventure of Link", Kind: "game", Instant: true},
		{Key: "z3", Title: "Zelda: Ocarina of Time", Kind: "game", Instant: true},
		{Key: "z4", Title: "Zelda: Majora's Mask", Kind: "game", Instant: true},
		{Key: "z5", Title: "Zelda: Wind Waker", Kind: "game", Instant: true},
	})
	s.discover.rows = []discoverRow{{
		Title: "Trending", Key: "trending",
		Items: []discoverItm{{Title: "Zelda: A Link to the Past", Year: 1991, MediaType: "movie"}},
	}}
	s.discover.expires = time.Now().Add(time.Hour)

	start := time.Now()
	rec := httptest.NewRecorder()
	s.handleSuggest(rec, httptest.NewRequest("GET", "/api/suggest?q=zelda&kind=game", nil))
	took := time.Since(start)

	if took > 250*time.Millisecond {
		t.Fatalf("suggestions took %s; that is not type-ahead", took)
	}
	var body struct {
		Suggestions []suggestion `json:"suggestions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(body.Suggestions) < 5 {
		t.Fatalf("got %d suggestions from a cache holding five matches", len(body.Suggestions))
	}
}

// The second job type-ahead does, and the one that makes "instant search" true
// for a query nobody has ever run: a keystroke pulls archive.org into the
// cache, so the search that follows a moment later reads it out of memory.
func TestTypingWarmsTheArchiveCacheForTheSearchThatFollows(t *testing.T) {
	stubArchive(t, 0, "Excitebike")
	s := newTestServer()

	rec := httptest.NewRecorder()
	s.handleSuggest(rec, httptest.NewRequest("GET", "/api/suggest?q=excitebike&kind=game", nil))

	// The fetch is deliberately detached from the request, so it may land just
	// after the response. Wait for it rather than racing it.
	warmed := false
	for i := 0; i < 100; i++ {
		if _, ok := s.getCached(archiveCacheKey("excitebike", "game")); ok {
			warmed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !warmed {
		t.Fatal("typing did not warm the archive cache; the search after it pays full price")
	}

	// And now the search itself finds it without asking anybody.
	_, body, took := doSearch(t, s, "/api/search?q=excitebike&kind=game&minSeeders=0")
	if took > 100*time.Millisecond {
		t.Errorf("the search after typing took %s; the warm cache was not used", took)
	}
	if len(cardTitles(body)) == 0 {
		t.Error("the search found nothing despite a warm archive cache")
	}
}

// With little or nothing in memory, archive.org is the only thing that can
// fill the list -- so that is the case worth waiting for. A dropdown showing
// one entry while the archive holds dozens is the failure this prevents.
func TestANovelQuerySuggestsFromArchiveWhenMemoryHasNothing(t *testing.T) {
	stubArchive(t, 20*time.Millisecond, "Punch-Out!!", "Super Punch-Out!!")
	s := newTestServer()

	rec := httptest.NewRecorder()
	s.handleSuggest(rec, httptest.NewRequest("GET", "/api/suggest?q=punch&kind=game", nil))

	var body struct {
		Suggestions []suggestion `json:"suggestions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Suggestions) != 2 {
		t.Fatalf("got %v, want both archive.org suggestions", titlesOfSuggestions(body.Suggestions))
	}
	if body.Suggestions[0].Source != "archive" {
		t.Errorf("source %q, want archive -- the client cannot say where it came from",
			body.Suggestions[0].Source)
	}
}

// One character matches nearly everything, which is noise rather than a
// suggestion. Answered rather than refused so the client needs no special case.
func TestOneCharacterSuggestsNothing(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	s.handleSuggest(rec, httptest.NewRequest("GET", "/api/suggest?q=z", nil))

	if rec.Code != 200 {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var body struct {
		Suggestions []suggestion `json:"suggestions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Suggestions) != 0 {
		t.Errorf("got %d suggestions for one letter", len(body.Suggestions))
	}
}

// "mario" should offer "Mario Bros" before "Dr. Mario". A list ordered by
// anything else makes the first entry -- the one Enter selects -- wrong.
func TestSuggestionsPutPrefixMatchesFirst(t *testing.T) {
	got := rankSuggestions([]suggestion{
		{Title: "Dr. Mario", Source: "results"},
		{Title: "Super Mario World", Source: "results"},
		{Title: "Mario Bros", Source: "archive"},
	}, []string{"mario"}, 5)

	if len(got) == 0 || got[0].Title != "Mario Bros" {
		t.Fatalf("first suggestion is %v, want the prefix match", titlesOfSuggestions(got))
	}
}

// The same title from two sources is one suggestion, not two.
func TestSuggestionsAreDedupedByTitle(t *testing.T) {
	got := rankSuggestions([]suggestion{
		{Title: "Sonic the Hedgehog", Source: "archive"},
		{Title: "sonic the hedgehog", Source: "results"},
	}, []string{"sonic"}, 5)

	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1: %v", len(got), titlesOfSuggestions(got))
	}
	if got[0].Source != "results" {
		t.Errorf("kept the %q copy; the one already on this server is the better answer", got[0].Source)
	}
}

func titlesOfSuggestions(in []suggestion) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.Title)
	}
	return out
}

// ------------------------------------------------------------------ budget --

// A guard on the numbers themselves. These are the whole contract: a first
// paint that waits longer than a person will tolerate is the defect returning,
// and it would return silently.
func TestTheFirstPaintBudgetStaysUnderHalfASecond(t *testing.T) {
	if firstPaintBudget > 500*time.Millisecond {
		t.Fatalf("firstPaintBudget is %s; the requirement is under half a second", firstPaintBudget)
	}
	if indexerListDeadline > 6*time.Second {
		t.Errorf("indexerListDeadline is %s; it is a local call that answers in milliseconds",
			indexerListDeadline)
	}
	if prowlarrClient.Timeout > time.Minute {
		t.Errorf("prowlarrClient timeout is %s; nothing legitimate runs that long",
			prowlarrClient.Timeout)
	}
}

// archive.org is the source a person waits on now, so its connection has to be
// pooled. A fresh client per call is a fresh TLS handshake per call: measured,
// 187ms of a 310ms round trip, paid on every search for nothing.
func TestTheArchiveClientReusesConnections(t *testing.T) {
	if archiveClient.Transport == nil {
		t.Fatal("archiveClient has the default transport; give it a pool it owns")
	}
	tr, ok := archiveClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("archiveClient transport is %T", archiveClient.Transport)
	}
	if tr.MaxIdleConnsPerHost < 2 {
		t.Errorf("MaxIdleConnsPerHost is %d; connections will not be reused", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout < time.Minute {
		t.Errorf("IdleConnTimeout is %s; the pool goes cold between searches", tr.IdleConnTimeout)
	}
}

// archive.org results are cached under their own key. Storing them under the
// merged search key would let one source's contribution be served as the whole
// answer, silently losing every torrent.
func TestArchiveResultsAreCachedSeparatelyFromMergedResults(t *testing.T) {
	if archiveCacheKey("mario", "game") == searchCacheKey("mario", "game") {
		t.Fatal("archive.org results share the merged search key; the two would overwrite each other")
	}
}

// The second call must not go out. Type-ahead asks this question a few hundred
// milliseconds before the search does, and paying for it twice is exactly the
// latency this design exists to remove.
func TestTheArchiveCacheIsUsedOnTheSecondAsk(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{"numFound": 1, "docs": []map[string]any{
				{"identifier": "tetris", "title": "Tetris", "emulator": "nes"},
			}},
		})
	}))
	defer srv.Close()
	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	defer func() { archiveSearchAPI = old }()

	s := newTestServer()
	if _, err := s.searchArchiveCached(t.Context(), "tetris", "game"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := s.searchArchiveCached(t.Context(), "tetris", "game"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("archive.org was asked %d times for the same query", n)
	}
}
