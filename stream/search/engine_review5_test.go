package main

import (
	"errors"
	"fmt"
	"io/fs"
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

func TestAnUnreadableSubfolderIsNotProofOfAnEmptyDownload(t *testing.T) {
	dl := t.TempDir()
	sub := filepath.Join(dl, "Season 1")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "Severance.S01E02.mkv"), []byte("x"), 0o644)
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if p == sub {
				// As an unreadable folder reports: an error, and nothing inside it.
				if err := fn(p, d, os.ErrPermission); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			return fn(p, d, err)
		})
	}
	t.Cleanup(func() { walkDir = filepath.WalkDir })
	checkPartialScan(t, dl)
}

func checkPartialScan(t *testing.T, dl string) {
	t.Helper()
	it := engineItem{Kind: "series", Title: "Severance", Year: 2022,
		Episodes: []engineEpisode{{Season: 1, Episode: 2}}}
	_, err := importDownload(dl, &it, engineGrab{Season: 1}, engineSettings{SeriesRoot: t.TempDir()})
	if err == nil || errors.Is(err, errNoVideo) || errors.Is(err, errNoEpisodeMatch) {
		t.Fatalf("an incomplete scan must stay retryable, not prove absence: %v", err)
	}
	prev := []wantSlot{{Season: 1, Episode: 2}}
	if state, _, _ := settleFailedImport(prev, importResult{}, err); state != "downloading" {
		t.Fatalf("the owed episode may be in the unreadable part: %s", state)
	}
}
