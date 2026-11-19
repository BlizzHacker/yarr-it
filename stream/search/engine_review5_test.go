package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Regression tests for the third CodeRabbit round.

func TestOwedEpisodesThatLeaveAnEmptyDownloadFailTheGrab(t *testing.T) {
	prev := []wantSlot{{Season: 1, Episode: 2}}
	state, _, unplaced := settleFailedImport(prev, importResult{}, errNoVideo)
	if state != "failed" || unplaced != nil {
		t.Fatalf("a read, empty download proves the owed episode is gone: %s %v", state, unplaced)
	}
	state, _, _ = settleFailedImport(prev, importResult{}, fmt.Errorf("%w (2 skipped)", errNoEpisodeMatch))
	if state != "failed" {
		t.Fatalf("no matching episode also proves it: %s", state)
	}
	state, _, unplaced = settleFailedImport(prev, importResult{}, os.ErrPermission)
	if state != "downloading" || len(unplaced) != 1 {
		t.Fatalf("a read failure stays retryable and keeps what is owed: %s %v", state, unplaced)
	}
	if state, _, _ = settleFailedImport(nil, importResult{}, errNoVideo); state != "downloading" {
		t.Fatalf("with nothing owed the grab is left as before: %s", state)
	}
}

func TestAnUnreadableDownloadIsNotTreatedAsEmpty(t *testing.T) {
	if _, err := videoFiles(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a download that cannot be read must be an error, not an empty list")
	}
}

func TestAnExplicitlyNamedSpecialKeepsItsOwnNumber(t *testing.T) {
	dl, lib := t.TempDir(), t.TempDir()
	f := filepath.Join(dl, "Severance.S00E05.1080p.mkv")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	it := engineItem{Kind: "series", Title: "Severance", Year: 2022,
		Episodes: []engineEpisode{{Season: 0, Episode: 3}, {Season: 0, Episode: 5}}}
	if _, err := importDownload(f, &it, engineGrab{Season: 0, Episode: 3}, engineSettings{SeriesRoot: lib}); err != nil {
		t.Fatal(err)
	}
	if it.Episodes[0].HasFile || !it.Episodes[1].HasFile {
		t.Fatalf("S00E05 must be filed as S00E05: %+v", it.Episodes)
	}
}
