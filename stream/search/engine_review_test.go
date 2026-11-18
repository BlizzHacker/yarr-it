package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression tests for the native-engine review findings (yarr-it PR #7).

func TestLinkOrCopyRecognisesAFinishedPlacementOnReplay(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	_ = os.WriteFile(src, []byte(strings.Repeat("x", 4096)), 0o644)
	dst := filepath.Join(dir, "lib", "dst.mkv")
	if err := linkOrCopy(src, dst); err != nil {
		t.Fatal(err)
	}
	// The process stopped before the state commit; the retry must succeed.
	if err := linkOrCopy(src, dst); err != nil {
		t.Fatalf("replay of a finished placement was refused: %v", err)
	}
	// An unrelated file at the destination is still refused.
	other := filepath.Join(dir, "other.mkv")
	_ = os.WriteFile(other, []byte("different"), 0o644)
	if err := linkOrCopy(other, dst); err == nil {
		t.Fatal("an unrelated existing destination must be refused")
	}
}

func TestLinkOrCopyIsNotBlockedByAnAbandonedPartial(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.mkv")
	stale := dst + ".partial"
	_ = os.WriteFile(stale, []byte("half"), 0o644)
	old := time.Now().Add(-2 * staleCopyAge)
	_ = os.Chtimes(stale, old, old)
	removeStalePartials(dst)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("an abandoned partial copy should be cleaned up")
	}
	fresh := dst + ".partial-123"
	_ = os.WriteFile(fresh, []byte("active"), 0o644)
	removeStalePartials(dst)
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("an active copy's temp file must not be removed")
	}
}

func TestPackFallbackSkipsUnairedAndInFlightEpisodes(t *testing.T) {
	e := testEngine(t, nil, nil)
	it := engineItem{ID: "imdb:series:tt1", Kind: "series", Episodes: []engineEpisode{
		{Season: 1, Episode: 1, Aired: "2020-01-01"},
		{Season: 1, Episode: 2, Aired: "2020-01-01"},
		{Season: 1, Episode: 3, Aired: "2999-01-01"},
	}}
	grabs := []engineGrab{{ItemID: it.ID, Season: 1, Episode: 2, State: "downloading"}}
	got := e.eligibleEpisodes(it, grabs)
	if len(got) != 1 || got[0] != (wantSlot{Season: 1, Episode: 1}) {
		t.Fatalf("only aired, missing, not-downloading episodes are eligible: %v", got)
	}
}

func TestReconcileChecksEachGrabAgainstItsOwnClient(t *testing.T) {
	qa, qb := newFakeQbit(t), newFakeQbit(t)
	e := testEngine(t, nil, nil)
	dl := t.TempDir()
	_ = e.store.update(func(s *engineState) error {
		s.Clients = []engineClient{
			{ID: "b", Name: "b", Kind: "qbittorrent", URL: qb.srv.URL, Username: "admin", Password: "pw", Enabled: true},
			{ID: "a", Name: "a", Kind: "qbittorrent", URL: qa.srv.URL, Username: "admin", Password: "pw", Enabled: true},
		}
		s.Settings.DownloadPath = dl
		s.Grabs = []engineGrab{{ItemID: "imdb:movie:tt9", Release: "r", Hash: "ha", ClientID: "a",
			Grabbed: time.Now().UTC().Format(time.RFC3339), State: "downloading"}}
		return nil
	})
	qa.mu.Lock()
	qa.torrents = []qbitTorrent{{Hash: "ha", Progress: 0.5, State: "downloading"}}
	qa.mu.Unlock()
	if err := e.Reconcile(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if g := e.store.view().Grabs[0]; g.Progress != 0.5 {
		t.Fatalf("client a's grab was not read from client a: %+v", g)
	}
}

func TestAGrabThatNeverAppearsFailsAfterTheGracePeriod(t *testing.T) {
	qb := newFakeQbit(t)
	e := testEngine(t, nil, qb)
	old := time.Now().Add(-2 * engineMissingGrace).UTC().Format(time.RFC3339)
	_ = e.store.update(func(s *engineState) error {
		s.Grabs = []engineGrab{{ItemID: "imdb:movie:tt9", Release: "gone", Hash: "nohash",
			ClientID: "c1", Grabbed: old, State: "downloading"}}
		return nil
	})
	if err := e.Reconcile(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if g := e.store.view().Grabs[0]; g.State != "failed" {
		t.Fatalf("a long-missing grab must fail so a replacement can be sought: %+v", g)
	}
}

func TestEpisodeMetadataRefreshAddsNewEpisodesAndKeepsFiles(t *testing.T) {
	e := testEngine(t, nil, nil)
	ref, _ := parseArrRef("imdb:series:tt11280740")
	it, _, err := e.Add(ctxT(t), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = e.store.update(func(s *engineState) error {
		cur := s.item(it.ID)
		cur.Episodes = cur.Episodes[:1] // as if added before S01E02 was announced
		cur.Episodes[0].HasFile, cur.Episodes[0].File = true, "/lib/e1.mkv"
		return nil
	})
	if err := e.refreshEpisodes(ctxT(t), it); err != nil {
		t.Fatal(err)
	}
	view := e.store.view()
	cur := view.item(it.ID)
	if findEpisode(cur, 1, 2) == nil {
		t.Fatal("a newly announced episode was not added")
	}
	if ep := findEpisode(cur, 1, 1); ep == nil || !ep.HasFile || ep.File != "/lib/e1.mkv" {
		t.Fatalf("refresh must keep file fields: %+v", ep)
	}
	if cur.MetaRefreshed == "" || metaRefreshDue(*cur, time.Now()) {
		t.Error("a just-refreshed series is not due again")
	}
}

func TestADeclinedProviderLeavesTheRequestPending(t *testing.T) {
	e := testEngine(t, nil, nil)
	n := &nativeRequests{e: e, route: func(ctx context.Context, id string) (RequestResult, string, error) {
		return RequestResult{Accepted: false, Detail: "quality profile missing"}, "sonarr", nil
	}}
	if _, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid"); err != nil {
		t.Fatal(err)
	}
	if err := n.Decide(ctxT(t), "1", true); err == nil || !strings.Contains(err.Error(), "quality profile missing") {
		t.Fatalf("a refusal must surface its reason: %v", err)
	}
	if r := e.store.view().Requests[0]; r.Status != "pending" {
		t.Fatalf("a refused approval stays pending so the owner can retry or decline: %+v", r)
	}
	if err := n.Decide(ctxT(t), "1", false); err != nil {
		t.Fatalf("the owner can still decline it: %v", err)
	}
}

func TestADeclineCannotBeOverwrittenByAnApprovalInFlight(t *testing.T) {
	e := testEngine(t, nil, nil)
	release := make(chan struct{})
	n := &nativeRequests{e: e}
	n.route = func(ctx context.Context, id string) (RequestResult, string, error) {
		<-release
		return RequestResult{Accepted: true}, "yarrit", nil
	}
	if _, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- n.Decide(context.Background(), "1", true) }()
	deadline := time.Now().Add(5 * time.Second)
	for e.store.view().Requests[0].Status != "routing" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := n.Decide(ctxT(t), "1", false); err == nil {
		t.Fatal("a decline while an approval is routing must be refused")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r := e.store.view().Requests[0]; r.Status != "approved" {
		t.Fatalf("got %+v", r)
	}
}
