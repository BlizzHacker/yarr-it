package main

// The memory cache is a bounded hot tier. This disk tier retains public search
// metadata across eviction and restarts, including discoveries missing from a
// later refresh. Fresh answers still use only the latest successful response;
// history is offered only as explicitly stale fallback. No media is downloaded.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

type diskSearchEntry struct {
	Version int       `json:"version"`
	Key     string    `json:"key"`
	Expires time.Time `json:"expires"`
	Cards   []card    `json:"cards"`
	History []card    `json:"history"`
}

func (s *server) diskCachePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.cacheDir, hex.EncodeToString(sum[:])+".json")
}

func readDiskEntry(path string) (diskSearchEntry, error) {
	var entry diskSearchEntry
	f, err := os.Open(path)
	if err != nil {
		return entry, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 64<<20)).Decode(&entry)
	return entry, err
}

func (s *server) readDiskCache(key string, history bool) (cacheEntry, bool) {
	if s.cacheDir == "" {
		return cacheEntry{}, false
	}
	entry, err := readDiskEntry(s.diskCachePath(key))
	if err != nil || entry.Version != 1 || entry.Key != key {
		return cacheEntry{}, false
	}
	cards := entry.Cards
	if history {
		cards = entry.History
	}
	cards = dropOwnerCards(cards)
	return cacheEntry{cards: cards, expires: entry.Expires}, len(cards) > 0
}

// Retain every distinct source, including new torrents for an existing title.
func archiveSearchCards(latest, prior []card) []card {
	out := append([]card(nil), latest...)
	positions := map[string]int{}
	for i, c := range out {
		positions[c.Key] = i
	}
	for _, c := range prior {
		i, found := positions[c.Key]
		if !found || c.Key == "" {
			positions[c.Key] = len(out)
			out = append(out, c)
			continue
		}
		seen := map[string]bool{}
		identity := func(v source) string { return v.Magnet + "\x00" + v.Title + "\x00" + v.Indexer }
		// Clone the slice: cached cards are shared with concurrent readers.
		out[i].Sources = append([]source(nil), out[i].Sources...)
		for _, v := range out[i].Sources {
			seen[identity(v)] = true
		}
		for _, v := range c.Sources {
			if !seen[identity(v)] {
				out[i].Sources = append(out[i].Sources, v)
				seen[identity(v)] = true
			}
		}
	}
	return out
}

func (s *server) writeDiskCache(key string, current cacheEntry) {
	if s.cacheDir == "" {
		return
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if err := os.MkdirAll(s.cacheDir, 0700); err != nil {
		log.Printf("search cache: create directory: %v", err)
		return
	}
	entry := diskSearchEntry{Version: 1, Key: key, Expires: current.expires,
		Cards: dropOwnerCards(current.cards)}
	if old, err := readDiskEntry(s.diskCachePath(key)); err == nil && old.Version == 1 && old.Key == key {
		// A slower writer cannot replace a more recent successful response.
		if old.Expires.After(entry.Expires) {
			entry.Cards, entry.Expires = old.Cards, old.Expires
		}
		entry.History = archiveSearchCards(current.cards, old.History)
	} else {
		entry.History = append([]card(nil), entry.Cards...)
	}
	entry.History = dropOwnerCards(entry.History)
	f, err := os.CreateTemp(s.cacheDir, ".search-*.tmp")
	if err != nil {
		log.Printf("search cache: create file: %v", err)
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if err = json.NewEncoder(f).Encode(entry); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.diskCachePath(key))
	}
	if err != nil {
		log.Printf("search cache: persist: %v", err)
	}
}
