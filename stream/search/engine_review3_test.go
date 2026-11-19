package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression tests for the second round of native-engine review findings.

func TestASeasonWithAnEpisodeInFlightIsNotGrabbedAsAPack(t *testing.T) {
	e := testEngine(t, nil, nil)
	it := engineItem{ID: "imdb:series:tt1", Kind: "series", Episodes: []engineEpisode{
		{Season: 1, Episode: 1, Aired: "2020-01-01"},
		{Season: 1, Episode: 2, Aired: "2020-01-01"},
		{Season: 1, Episode: 3, Aired: "2020-01-01"},
	}}
	grabs := []engineGrab{{ItemID: it.ID, Season: 1, Episode: 2, State: "downloading"}}
	got := e.wanted(it, grabs, e.store.settings().Profile)
	want := []wantSlot{{Season: 1, Episode: 1}, {Season: 1, Episode: 3}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("a season with a download running must be fetched per episode: %v", got)
	}
}

func TestReconcileFindsAHashedGrabAfterTheCategoryChanges(t *testing.T) {
	qb := newFakeQbit(t)
	qb.filterCategory = true
	e := testEngine(t, nil, qb)
	_ = e.store.update(func(s *engineState) error {
		s.Clients[0].Category = "renamed"
		s.Grabs = []engineGrab{{ItemID: "imdb:movie:tt9", Release: "r", Hash: "ha", ClientID: s.Clients[0].ID,
			Category: "yarrit", Grabbed: time.Now().Add(-2 * engineMissingGrace).UTC().Format(time.RFC3339),
			State: "downloading"}}
		return nil
	})
	qb.mu.Lock()
	qb.torrents = []qbitTorrent{{Hash: "ha", Category: "yarrit", Progress: 0.5, State: "downloading"}}
	qb.mu.Unlock()
	if err := e.Reconcile(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if g := e.store.view().Grabs[0]; g.State != "downloading" || g.Progress != 0.5 {
		t.Fatalf("a grab in the old category must still be found by hash: %+v", g)
	}
}

func TestARemovedClientsGrabsFailAfterTheGracePeriod(t *testing.T) {
	e := testEngine(t, nil, nil)
	now := time.Now()
	e.now = func() time.Time { return now }
	_ = e.store.update(func(s *engineState) error {
		s.Grabs = []engineGrab{{ItemID: "imdb:movie:tt9", Release: "r", Hash: "h", ClientID: "gone",
			Grabbed: now.UTC().Format(time.RFC3339), State: "downloading"}}
		return nil
	})
	_ = e.Reconcile(ctxT(t))
	if g := e.store.view().Grabs[0]; g.State != "downloading" || g.ClientGone == "" {
		t.Fatalf("a removed client gets a grace period first: %+v", g)
	}
	now = now.Add(engineMissingGrace + time.Minute)
	_ = e.Reconcile(ctxT(t))
	if g := e.store.view().Grabs[0]; g.State != "failed" {
		t.Fatalf("past the grace period the grab must stop blocking a replacement: %+v", g)
	}
}

func TestAnUnplacedEpisodeThatLeavesTheDownloadFailsTheGrab(t *testing.T) {
	it := engineItem{Kind: "series", Episodes: []engineEpisode{
		{Season: 1, Episode: 1, HasFile: true}, {Season: 1, Episode: 2}}}
	prev := []wantSlot{{Season: 1, Episode: 2}}
	// The retry replays E01 and E02 is no longer in the download.
	state, detail, unplaced := settleImport(prev, &it, importResult{Placed: []string{"e1.mkv"}})
	if state != "failed" || !strings.Contains(detail, "S01E02") || unplaced != nil {
		t.Fatalf("the owed episode must not be forgotten: %s %q %v", state, detail, unplaced)
	}
	// A retry that fails again keeps it owed and the grab downloading.
	state, _, unplaced = settleImport(prev, &it, importResult{Placed: []string{"e1.mkv"},
		Failed: []string{"e2: busy"}, FailedSlots: prev})
	if state != "downloading" || len(unplaced) != 1 {
		t.Fatalf("got %s %v", state, unplaced)
	}
	// Once placed, the grab is imported.
	it.Episodes[1].HasFile = true
	if state, _, _ = settleImport(prev, &it, importResult{Placed: []string{"e1", "e2"}}); state != "imported" {
		t.Fatalf("got %s", state)
	}
}

type failingMeta struct{ engineMeta }

func (failingMeta) Lookup(context.Context, string, string) (metaDetail, error) {
	return metaDetail{}, errors.New("metadata unavailable")
}

func TestAFailedMetadataRefreshBacksOff(t *testing.T) {
	e := testEngine(t, nil, nil)
	ref, _ := parseArrRef("imdb:series:tt11280740")
	it, _, err := e.Add(ctxT(t), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = e.store.update(func(s *engineState) error {
		s.item(it.ID).MetaRefreshed = time.Now().Add(-2 * engineMetaRefresh).UTC().Format(time.RFC3339)
		return nil
	})
	e.meta = failingMeta{e.meta}
	e.refreshDue(ctxT(t))
	cur := viewItem(e, it.ID)
	if cur.MetaRetry == "" || metaRefreshDue(*cur, time.Now()) {
		t.Fatalf("a failed refresh must wait before trying again: %+v", cur.MetaRetry)
	}
	if !metaRefreshDue(*cur, time.Now().Add(engineMetaRetry+time.Minute)) {
		t.Fatal("after the back-off the refresh is due again")
	}
}

func TestPartialCleanupOnlyTouchesEngineTempNames(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.mkv")
	old := time.Now().Add(-2 * staleCopyAge)
	for _, name := range []string{"dst.mkv.yarrit-partial-42", "dst.mkv.partial-backup", "dst.mkv.yarrit-partial-x"} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("x"), 0o644)
		_ = os.Chtimes(p, old, old)
	}
	removeStalePartials(dst)
	if _, err := os.Stat(filepath.Join(dir, "dst.mkv.yarrit-partial-42")); !os.IsNotExist(err) {
		t.Fatal("the engine's abandoned temp copy should be removed")
	}
	for _, keep := range []string{"dst.mkv.partial-backup", "dst.mkv.yarrit-partial-x"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("%s is not an engine temp name and must be kept", keep)
		}
	}
}

func TestAReplayIsRefusedWhenOnlyTheMiddleDiffers(t *testing.T) {
	dir := t.TempDir()
	a := make([]byte, 3<<20)
	b := make([]byte, 3<<20)
	b[len(b)/2] = 1 // same size, same first and last MiB
	src, dst := filepath.Join(dir, "src.mkv"), filepath.Join(dir, "dst.mkv")
	_ = os.WriteFile(src, a, 0o644)
	_ = os.WriteFile(dst, b, 0o644)
	if err := linkOrCopy(src, dst); err == nil {
		t.Fatal("a different file at the destination must not count as placed")
	}
	_ = os.WriteFile(dst, a, 0o644)
	if err := linkOrCopy(src, dst); err != nil {
		t.Fatalf("an identical copy is a finished placement: %v", err)
	}
}

func TestASeasonZeroEpisodeRequestIsEpisodeScoped(t *testing.T) {
	if !episodeScoped(RequestOptions{Season: 0, Episode: 1}) {
		t.Fatal("S00E01 is a special: an episode request")
	}
	if episodeScoped(RequestOptions{Season: 0, Episode: 0}) || episodeScoped(RequestOptions{Season: 2}) {
		t.Fatal("episode 0 means the whole title")
	}
	e := testEngine(t, nil, nil)
	p := &engineProvider{e: e}
	item := MediaItem{CanonicalID: "imdb:series:tt11280740"}
	if _, err := p.Request(ctxT(t), item, RequestOptions{Season: 0, Episode: 1, Monitor: true}); err != nil {
		t.Fatal(err)
	}
	if viewItem(e, "imdb:series:tt11280740").Monitored {
		t.Fatal("an episode request must not monitor the whole series")
	}
}

func TestAnEpisodeRequestWaitsForASearchAlreadyRunning(t *testing.T) {
	old := searchBusyRetry
	searchBusyRetry = 20 * time.Millisecond
	t.Cleanup(func() { searchBusyRetry = old })
	ix := torznabServer(t, tzItem("Severance.S01E02.1080p.WEB-DL.x264-GRP", 20, "e2hash", 2<<30))
	qb := newFakeQbit(t)
	e := testEngine(t, ix, qb)
	ref, _ := parseArrRef("imdb:series:tt11280740")
	it, _, err := e.AddWith(ctxT(t), ref, "", false)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.searching[it.ID] = true // an S01E01 search is running
	e.mu.Unlock()
	p := &engineProvider{e: e}
	if _, err := p.Request(ctxT(t), MediaItem{CanonicalID: it.ID}, RequestOptions{Season: 1, Episode: 2}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	e.mu.Lock()
	delete(e.searching, it.ID)
	e.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		qb.mu.Lock()
		n := len(qb.added)
		qb.mu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the episode request was dropped behind the running search")
}

func TestARequestBeingRoutedStillBlocksADuplicate(t *testing.T) {
	e := testEngine(t, nil, nil)
	n := &nativeRequests{e: e}
	if _, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid"); err != nil {
		t.Fatal(err)
	}
	_ = e.store.update(func(s *engineState) error { s.Requests[0].Status = "routing"; return nil })
	if _, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid"); err == nil {
		t.Fatal("a request being approved is still active")
	}
}

func TestAStrandedApprovalIsRecoveredWithoutAskingTheProviderAgain(t *testing.T) {
	e := testEngine(t, nil, nil)
	routed := 0
	n := &nativeRequests{e: e, route: func(ctx context.Context, id string) (RequestResult, string, error) {
		routed++
		return RequestResult{Accepted: true}, "radarr", nil
	}}
	if _, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid"); err != nil {
		t.Fatal(err)
	}
	// The provider accepted, but the final write failed: the outcome is held.
	_ = e.store.update(func(s *engineState) error { s.Requests[0].Status = "routing"; return nil })
	n.unsaved = map[int]requestOutcome{1: {status: "approved", detail: "radarr:", canonicalID: "imdb:movie:tt0133093", title: "The Matrix"}}
	if err := n.Decide(ctxT(t), "1", true); err == nil {
		t.Fatal("the saved approval means the request is already decided")
	}
	if r := e.store.view().Requests[0]; r.Status != "approved" || routed != 0 {
		t.Fatalf("recovery must save the outcome without routing again: %+v routed=%d", r, routed)
	}
}

func TestAnApprovalLeftRoutingByARestartCanBeDecidedAgain(t *testing.T) {
	e := testEngine(t, nil, nil)
	n := &nativeRequests{e: e}
	if _, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid"); err != nil {
		t.Fatal(err)
	}
	_ = e.store.update(func(s *engineState) error { s.Requests[0].Status = "routing"; return nil })
	if err := n.Decide(ctxT(t), "1", false); err != nil {
		t.Fatalf("nothing is routing it any more, so the owner can decide: %v", err)
	}
}

// viewItem reads one item from a state snapshot.
func viewItem(e *engine, id string) *engineItem {
	v := e.store.view()
	return v.item(id)
}
