package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiskCacheSurvivesRestartAndMemoryEviction(t *testing.T) {
	dir := t.TempDir()
	first := &server{cacheDir: dir, ttl: time.Hour}
	key := searchCacheKey("Sintel", "video")
	first.putCached(key, []card{{Key: "sintel", Title: "Sintel"}})
	restarted := &server{cacheDir: dir}
	got, ok := restarted.getCached(key)
	if !ok || len(got) != 1 || got[0].Key != "sintel" {
		t.Fatalf("lost persistent result: %#v", got)
	}
	if _, ok := restarted.getCached(searchCacheKey("Sintel", "game")); ok {
		t.Fatal("cross-domain cache collision")
	}
}

func TestDiskCacheRetainsHistoryWithoutMakingItFresh(t *testing.T) {
	dir := t.TempDir()
	s := &server{cacheDir: dir, ttl: time.Hour}
	key := searchCacheKey("Sintel", "video")
	s.putCached(key, []card{{Key: "a", Sources: []source{{Magnet: "magnet:old"}}}, {Key: "b"}})
	s.putCached(key, []card{{Key: "a", Sources: []source{{Magnet: "magnet:new"}}}})
	restarted := &server{cacheDir: dir}
	fresh, _ := restarted.getCached(key)
	if len(fresh) != 1 || len(fresh[0].Sources) != 1 {
		t.Fatal("history contaminated fresh response")
	}
	old, _ := restarted.getAny(key)
	if len(old) != 2 || len(old[0].Sources) != 2 {
		t.Fatalf("history lost: %#v", old)
	}
	entry, err := readDiskEntry(s.diskCachePath(key))
	if err != nil {
		t.Fatal(err)
	}
	entry.Expires = time.Now().Add(-48 * time.Hour)
	// Exercise the normal writer with an already-expired entry in a new dir.
	expired := &server{cacheDir: t.TempDir()}
	expired.writeDiskCache(key, cacheEntry{cards: entry.History, expires: entry.Expires})
	if _, ok := expired.getCached(key); ok {
		t.Fatal("old result reported fresh")
	}
	if _, ok := expired.getAny(key); !ok {
		t.Fatal("expired history was discarded")
	}
}

func TestDiskCacheCorruptionAndUnwritableDirectoryDegradeToMemory(t *testing.T) {
	dir := t.TempDir()
	s := &server{cacheDir: dir}
	key := "../../outside"
	if err := os.WriteFile(s.diskCachePath(key), []byte("{invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.getAny(key); ok {
		t.Fatal("accepted corrupt cache")
	}
	if filepath.Dir(s.diskCachePath(key)) != dir {
		t.Fatal("query escaped cache directory")
	}
	blocked := filepath.Join(dir, "file")
	os.WriteFile(blocked, []byte("x"), 0600)
	s.cacheDir = blocked
	s.putCached(key, []card{{Key: "works"}})
	if _, ok := s.getCached(key); !ok {
		t.Fatal("disk error broke memory cache")
	}
}

func TestDiskCacheConcurrentWritesRemainReadable(t *testing.T) {
	s := &server{cacheDir: t.TempDir(), ttl: time.Hour}
	key := searchCacheKey("Sintel", "video")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.putCached(key, []card{{Key: "sintel"}}); s.getAny(key) }()
	}
	wg.Wait()
	if _, ok := (&server{cacheDir: s.cacheDir}).getCached(key); !ok {
		t.Fatal("concurrent write corrupted cache")
	}
}

func TestDiskCacheNeverPersistsOwnerMedia(t *testing.T) {
	s := &server{cacheDir: t.TempDir(), ttl: time.Hour}
	key := searchCacheKey("private", "")
	s.putCached(key, []card{{Key: "public"}, {Key: "secret-owner", owner: true}})
	raw, err := os.ReadFile(s.diskCachePath(key))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-owner") {
		t.Fatal("private media persisted")
	}
	got, ok := (&server{cacheDir: s.cacheDir}).getCached(key)
	if !ok || len(got) != 1 || got[0].Key != "public" {
		t.Fatal("owner boundary lost on restart")
	}
}
