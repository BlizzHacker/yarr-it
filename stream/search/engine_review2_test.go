package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnEpisodeSearchGrabsOnlyThatEpisode(t *testing.T) {
	ix := torznabServer(t,
		tzItem("Severance.S01E01.1080p.WEB-DL.x264-GRP", 20, "e1hash", 2<<30)+
			tzItem("Severance.S01E02.1080p.WEB-DL.x264-GRP", 20, "e2hash", 2<<30))
	qb := newFakeQbit(t)
	e := testEngine(t, ix, qb)
	ref, _ := parseArrRef("imdb:series:tt11280740")
	it, _, err := e.Add(ctxT(t), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	n, err := e.AutoSearchEpisode(ctxT(t), it.ID, 1, 1)
	if err != nil || n != 1 {
		t.Fatalf("grabbed %d, err %v", n, err)
	}
	if len(qb.added) != 1 || !strings.Contains(qb.added[0], "e1hash") {
		t.Fatalf("an episode request must grab that episode only: %v", qb.added)
	}
}

func TestRequestingMonitoringOfAnExistingItemTurnsItOn(t *testing.T) {
	e := testEngine(t, nil, nil)
	ref, _ := parseArrRef("imdb:movie:tt0133093")
	it, _, err := e.Add(ctxT(t), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = e.SetMonitored(it.ID, false)
	p := &engineProvider{e: e}
	if _, err := p.Request(ctxT(t), MediaItem{CanonicalID: it.ID}, RequestOptions{Monitor: true}); err != nil {
		t.Fatal(err)
	}
	view := e.store.view()
	if !view.item(it.ID).Monitored {
		t.Fatal("a request asking for monitoring must monitor the existing item")
	}
}

func TestAFailedEpisodePlacementKeepsTheGrabRetryable(t *testing.T) {
	dl, lib := t.TempDir(), t.TempDir()
	pack := filepath.Join(dl, "Severance.S01")
	_ = os.MkdirAll(pack, 0o755)
	for _, f := range []string{"Severance.S01E01.1080p.mkv", "Severance.S01E02.1080p.mkv"} {
		_ = os.WriteFile(filepath.Join(pack, f), []byte(f), 0o644)
	}
	it := engineItem{ID: "imdb:series:tt11280740", Kind: "series", Title: "Severance", Year: 2022,
		Episodes: []engineEpisode{{Season: 1, Episode: 1}, {Season: 1, Episode: 2}}}
	// An unrelated file already sits where E02 would go: that placement fails.
	blocked := episodeDest(lib, it, 1, 2, "1080p", ".mkv")
	_ = os.MkdirAll(filepath.Dir(blocked), 0o755)
	_ = os.WriteFile(blocked, []byte("someone else's file"), 0o644)

	res, err := importDownload(pack, &it, engineGrab{Season: 1, Quality: "1080p"},
		engineSettings{SeriesRoot: lib})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Placed) != 1 || len(res.Failed) != 1 {
		t.Fatalf("one placed, one retryable failure expected: %+v", res)
	}
	if !findEpisode(&it, 1, 1).HasFile || findEpisode(&it, 1, 2).HasFile {
		t.Fatalf("only the placed episode has a file: %+v", it.Episodes)
	}
}
