package main

// Per-user library and playback position.
//
// This is the thing a catalogue is not: somewhere to put what you mean to watch,
// and the memory of where you stopped. It is also the reason accounts exist at
// all -- the sign-in gate already identifies TV viewers, so the same session
// makes a phone, a browser and a television the same library.
//
// Storage is a JSON file rather than a database. The whole dataset is a few
// hundred rows per user with no queries beyond "everything for this user", and
// the service otherwise has no dependencies at all -- adding a database engine
// and a driver to store a watchlist would be the larger cost. The seam is
// narrow enough to move later if that stops being true.
//
// Writes are the part worth care. A player reports its position every few
// seconds, so persisting on every update would rewrite the file constantly for
// a value that only matters when someone comes back. Updates land in memory and
// are flushed on a timer, and the flush is atomic -- written beside the file and
// renamed -- so a crash mid-write loses the last few seconds of progress rather
// than the entire library.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// How often dirty state reaches disk.
	libraryFlushInterval = 5 * time.Second

	// Below this fraction a title is "still watching"; above it, finished. The
	// last few minutes of a film are credits, and offering to resume them is
	// worse than offering nothing.
	finishedFraction = 0.92

	// Ignore the first moments: someone who opened a title and closed it does
	// not want it at the top of Continue Watching forever.
	minResumeSeconds = 30
)

// libraryItem is a saved title.
type libraryItem struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Poster   string `json:"poster,omitempty"`
	IsSeries bool   `json:"isSeries,omitempty"`
	AddedAt  int64  `json:"addedAt"`
}

// progress is where a viewer stopped.
type progress struct {
	Key       string  `json:"key"`
	Title     string  `json:"title,omitempty"`
	Poster    string  `json:"poster,omitempty"`
	Position  float64 `json:"position"` // seconds
	Duration  float64 `json:"duration"` // seconds, 0 when unknown
	UpdatedAt int64   `json:"updatedAt"`
	Device    string  `json:"device,omitempty"`
}

// finished reports whether this counts as watched through.
func (p progress) finished() bool {
	return p.Duration > 0 && p.Position/p.Duration >= finishedFraction
}

// resumable reports whether it belongs in Continue Watching.
func (p progress) resumable() bool {
	return p.Position >= minResumeSeconds && !p.finished()
}

type userData struct {
	Items    map[string]libraryItem `json:"items"`
	Progress map[string]progress    `json:"progress"`
}

type libraryStore struct {
	path string

	mu    sync.RWMutex
	users map[string]*userData
	dirty bool
}

func newLibraryStore(path string) (*libraryStore, error) {
	s := &libraryStore{path: path, users: map[string]*userData{}}
	if path == "" {
		// No path configured: everything works, nothing survives a restart.
		// Better than refusing to start, and the log says which it is.
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.users); err != nil {
			// A corrupt file must not take the service down, but it must not be
			// silently overwritten either.
			return nil, fmt.Errorf("library file %s is unreadable: %w", path, err)
		}
	}
	for _, u := range s.users {
		if u.Items == nil {
			u.Items = map[string]libraryItem{}
		}
		if u.Progress == nil {
			u.Progress = map[string]progress{}
		}
	}
	return s, nil
}

// user returns the record for a user, creating it. Caller holds the lock.
func (s *libraryStore) user(id string) *userData {
	u, ok := s.users[id]
	if !ok {
		u = &userData{Items: map[string]libraryItem{}, Progress: map[string]progress{}}
		s.users[id] = u
	}
	return u
}

func (s *libraryStore) add(userID string, it libraryItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it.AddedAt == 0 {
		it.AddedAt = time.Now().Unix()
	}
	s.user(userID).Items[it.Key] = it
	s.dirty = true
}

func (s *libraryStore) remove(userID, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.user(userID).Items, key)
	s.dirty = true
}

func (s *libraryStore) list(userID string) []libraryItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[userID]
	if !ok {
		return []libraryItem{}
	}
	out := make([]libraryItem, 0, len(u.Items))
	for _, it := range u.Items {
		out = append(out, it)
	}
	// Most recently added first: a library is a stack, not an archive.
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt > out[j].AddedAt })
	return out
}

func (s *libraryStore) setProgress(userID string, p progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.UpdatedAt = time.Now().Unix()
	u := s.user(userID)

	// Keep whatever the client did not resend, so a bare position update from a
	// TV does not erase the artwork the browser stored.
	if prev, ok := u.Progress[p.Key]; ok {
		if p.Title == "" {
			p.Title = prev.Title
		}
		if p.Poster == "" {
			p.Poster = prev.Poster
		}
		if p.Duration == 0 {
			p.Duration = prev.Duration
		}
	}
	u.Progress[p.Key] = p
	s.dirty = true
}

func (s *libraryStore) progressFor(userID, key string) (progress, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[userID]
	if !ok {
		return progress{}, false
	}
	p, ok := u.Progress[key]
	return p, ok
}

// continueWatching is the resume row: started, not finished, newest first.
func (s *libraryStore) continueWatching(userID string, limit int) []progress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[userID]
	if !ok {
		return []progress{}
	}
	out := make([]progress, 0, len(u.Progress))
	for _, p := range u.Progress {
		if !p.resumable() {
			continue
		}
		// A client that only reports a position -- which is all a TV player
		// naturally has -- would otherwise put a raw key on the shelf. If the
		// title is in the library, use what it knows.
		if it, ok := u.Items[p.Key]; ok {
			if p.Title == "" {
				p.Title = it.Title
			}
			if p.Poster == "" {
				p.Poster = it.Poster
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// flush writes the file if anything changed. Atomic: a torn write would cost
// every user their library, which is far worse than losing one update.
func (s *libraryStore) flush() error {
	s.mu.Lock()
	if !s.dirty || s.path == "" {
		s.mu.Unlock()
		return nil
	}
	raw, err := json.Marshal(s.users)
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *libraryStore) flushLoop() {
	for range time.Tick(libraryFlushInterval) {
		if err := s.flush(); err != nil {
			logLibraryError(err)
		}
	}
}

// normaliseKey keeps one title from splitting into several library entries
// because two clients disagreed about case or spacing.
func normaliseKey(k string) string {
	return strings.ToLower(strings.TrimSpace(k))
}
