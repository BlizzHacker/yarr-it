package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression tests for the independent review of the second round.

func TestSpecialsAreNeverSearchedAsAPack(t *testing.T) {
	e := testEngine(t, nil, nil)
	it := engineItem{ID: "imdb:series:tt1", Kind: "series", Episodes: []engineEpisode{
		{Season: 0, Episode: 1, Aired: "2020-01-01"},
		{Season: 0, Episode: 2, Aired: "2020-01-01"},
	}}
	for _, s := range e.wanted(it, nil, e.store.settings().Profile) {
		if s.Episode == 0 {
			t.Fatalf("{0,0} is the whole-title slot, never a specials pack: %v", s)
		}
	}
}

func TestASingleFileSpecialImportsUnderItsRequestedNumber(t *testing.T) {
	dl, lib := t.TempDir(), t.TempDir()
	f := filepath.Join(dl, "Severance Christmas Special 1080p.mkv")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	it := engineItem{Kind: "series", Title: "Severance", Year: 2022,
		Episodes: []engineEpisode{{Season: 0, Episode: 3}}}
	if _, err := importDownload(f, &it, engineGrab{Season: 0, Episode: 3}, engineSettings{SeriesRoot: lib}); err != nil {
		t.Fatalf("a requested special must import by the grab's numbers: %v", err)
	}
	if !it.Episodes[0].HasFile {
		t.Fatal("S00E03 was not placed")
	}
}

func TestAnEpisodePlacedEarlierIsNotReplacedOrReread(t *testing.T) {
	dl, lib := t.TempDir(), t.TempDir()
	pack := filepath.Join(dl, "Severance.S01")
	_ = os.MkdirAll(pack, 0o755)
	_ = os.WriteFile(filepath.Join(pack, "Severance.S01E01.1080p.mkv"), []byte("new bytes"), 0o644)
	it := engineItem{Kind: "series", Title: "Severance", Year: 2022,
		Episodes: []engineEpisode{{Season: 1, Episode: 1}}}
	dst := episodeDest(lib, it, 1, 1, "1080p", ".mkv")
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	_ = os.WriteFile(dst, []byte("placed earlier"), 0o644)
	it.Episodes[0].HasFile, it.Episodes[0].File = true, dst
	res, err := importDownload(pack, &it, engineGrab{Season: 1, Quality: "1080p"}, engineSettings{SeriesRoot: lib})
	if err != nil || len(res.Placed) != 1 || len(res.Failed) != 0 {
		t.Fatalf("a recorded placement counts as placed: %+v %v", res, err)
	}
}

func TestAHeldOutcomeWaitsWhileItsRequestIsBeingDecided(t *testing.T) {
	e := testEngine(t, nil, nil)
	n := &nativeRequests{e: e}
	if _, err := n.Ask(ctxT(t), "imdb:movie:tt0133093", "The Matrix", 1999, "", "kid"); err != nil {
		t.Fatal(err)
	}
	_ = e.store.update(func(s *engineState) error { s.Requests[0].Status = "routing"; return nil })
	n.keepUnsaved(1, requestOutcome{status: "approved", detail: "radarr:"})
	unlock, ok := n.lockReq(1) // a decision on request 1 is in progress
	if !ok {
		t.Fatal("lock")
	}
	n.flushAll()
	if r := e.store.view().Requests[0]; r.Status != "routing" {
		t.Fatalf("a flush must not interleave with a decision: %+v", r)
	}
	if err := n.Decide(ctxT(t), "1", true); err == nil {
		t.Fatal("a second decision while one is in progress must be refused")
	}
	unlock()
	n.flushAll()
	if r := e.store.view().Requests[0]; r.Status != "approved" {
		t.Fatalf("once free, the held approval is saved: %+v", r)
	}
	if _, held := n.unsaved[1]; held {
		t.Fatal("a saved outcome is no longer held")
	}
}

func TestAClientThatReturnsClearsItsRemovedStamp(t *testing.T) {
	qb := newFakeQbit(t)
	e := testEngine(t, nil, qb)
	_ = e.store.update(func(s *engineState) error {
		s.Grabs = []engineGrab{{ItemID: "imdb:movie:tt9", Release: "r", Hash: "h", ClientID: s.Clients[0].ID,
			Grabbed: time.Now().UTC().Format(time.RFC3339), State: "downloading",
			ClientGone: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Detail: "download client x is unavailable"}}
		return nil
	})
	_ = e.Reconcile(ctxT(t))
	if g := e.store.view().Grabs[0]; g.ClientGone != "" || g.Detail != "" || g.State != "downloading" {
		t.Fatalf("a configured client clears the removed stamp: %+v", g)
	}
}
