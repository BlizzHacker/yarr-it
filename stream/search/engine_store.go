package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The Yarr.It engine: the part that makes Yarr.It a whole media manager on its
// own rather than a front end for one.
//
// Standalone, it does what Radarr + Sonarr + Prowlarr + Overseerr do between
// them for films and series -- keeps a monitored library, searches indexers
// itself (any Torznab/Newznab feed, no Prowlarr required), scores releases,
// hands the winner to a download client, files the result and answers
// requests. Beside an existing *arr stack it steps back: a Radarr or Sonarr
// the owner already runs is preferred for the same job, and the engine is
// what answers whatever nobody else does.
//
// State is one JSON file, written atomically. A media library's control data
// is small -- thousands of rows, not millions -- and a file the owner can read,
// back up and diff beats a database they cannot open.

const (
	engineStateVersion = 1
	engineHistoryCap   = 500
)

type engineEpisode struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Title   string `json:"title,omitempty"`
	Aired   string `json:"aired,omitempty"`
	HasFile bool   `json:"hasFile,omitempty"`
	File    string `json:"file,omitempty"`
	Quality string `json:"quality,omitempty"`
}

type engineItem struct {
	ID        string          `json:"id"` // canonical: imdb:movie:tt0133093
	Kind      string          `json:"kind"`
	IMDb      string          `json:"imdb"`
	Title     string          `json:"title"`
	Year      int             `json:"year,omitempty"`
	Poster    string          `json:"poster,omitempty"`
	Overview  string          `json:"overview,omitempty"`
	Monitored bool            `json:"monitored"`
	Added     string          `json:"added"`
	HasFile   bool            `json:"hasFile,omitempty"` // films
	File      string          `json:"file,omitempty"`
	Quality   string          `json:"quality,omitempty"`
	Episodes  []engineEpisode `json:"episodes,omitempty"` // series
	// Search backoff: a title nobody seeds must not be asked for every tick.
	LastSearch  string `json:"lastSearch,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	RequestedBy string `json:"requestedBy,omitempty"`
	// MetaRefreshed is when a series' episode list was last re-read, so
	// episodes announced after it was added are still searched for.
	MetaRefreshed string `json:"metaRefreshed,omitempty"`
	// MetaRetry holds back another refresh after a failed one.
	MetaRetry string `json:"metaRetry,omitempty"`
}

type engineIndexer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // torznab | newznab | prowlarr
	URL     string `json:"url"`
	APIKey  string `json:"apiKey,omitempty"`
	Enabled bool   `json:"enabled"`
}

type engineClient struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // qbittorrent
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Category string `json:"category,omitempty"`
	Enabled  bool   `json:"enabled"`
}

type engineProfile struct {
	// Allowed qualities, most wanted first. A release outside the list is
	// rejected; within it, earlier wins.
	Qualities []string `json:"qualities"`
	// Cutoff: once a file this good is on disk, stop upgrading.
	Cutoff         string   `json:"cutoff"`
	MinSeeders     int      `json:"minSeeders"`
	MaxSizeGB      float64  `json:"maxSizeGB,omitempty"`
	RejectSources  []string `json:"rejectSources"`
	PreferredWords []string `json:"preferredWords,omitempty"`
	RejectWords    []string `json:"rejectWords,omitempty"`
}

type engineSettings struct {
	MovieRoot  string `json:"movieRoot,omitempty"`
	SeriesRoot string `json:"seriesRoot,omitempty"`
	// DownloadPath is where THIS process sees completed downloads;
	// RemoteDownloadPath is the same folder as the download client names it.
	// Equal when both run on one box; different across containers.
	DownloadPath        string        `json:"downloadPath,omitempty"`
	RemoteDownloadPath  string        `json:"remoteDownloadPath,omitempty"`
	SearchIntervalHours int           `json:"searchIntervalHours"`
	PreferEngine        bool          `json:"preferEngine,omitempty"`
	Profile             engineProfile `json:"profile"`
}

type engineGrab struct {
	ItemID   string  `json:"itemId"`
	Release  string  `json:"release"`
	Indexer  string  `json:"indexer,omitempty"`
	Hash     string  `json:"hash,omitempty"`
	ClientID string  `json:"clientId"`
	Season   int     `json:"season,omitempty"`
	Episode  int     `json:"episode,omitempty"`
	Quality  string  `json:"quality,omitempty"`
	Grabbed  string  `json:"grabbed"`
	State    string  `json:"state"` // downloading | imported | failed
	Detail   string  `json:"detail,omitempty"`
	Progress float64 `json:"progress,omitempty"`
	// Category is the qBittorrent category the grab was submitted under, so a
	// later change to the client's category never hides it from reconcile.
	Category string `json:"category,omitempty"`
	// Unplaced lists episodes of this download whose placement failed; they
	// stay owed until placed, or the grab fails so they are searched again.
	Unplaced []wantSlot `json:"unplaced,omitempty"`
	// ClientGone is when reconcile first found the grab's client removed or
	// disabled; past the grace period the grab fails and stops blocking.
	ClientGone string `json:"clientGone,omitempty"`
}

type engineEvent struct {
	At     string `json:"at"`
	ItemID string `json:"itemId,omitempty"`
	Kind   string `json:"kind"` // added | grabbed | imported | failed | requested | decided
	Detail string `json:"detail"`
}

type engineRequest struct {
	ID          int    `json:"id"`
	CanonicalID string `json:"canonicalId"`
	Title       string `json:"title"`
	Year        int    `json:"year,omitempty"`
	Kind        string `json:"kind"`
	Poster      string `json:"poster,omitempty"`
	By          string `json:"by"`
	At          string `json:"at"`
	Status      string `json:"status"` // pending | approved | declined
	Detail      string `json:"detail,omitempty"`
}

type engineState struct {
	Version  int             `json:"version"`
	Settings engineSettings  `json:"settings"`
	Indexers []engineIndexer `json:"indexers"`
	Clients  []engineClient  `json:"clients"`
	Items    []engineItem    `json:"items"`
	Grabs    []engineGrab    `json:"grabs"`
	History  []engineEvent   `json:"history"`
	Requests []engineRequest `json:"requests"`
	NextReq  int             `json:"nextRequestId"`
}

func defaultEngineSettings() engineSettings {
	return engineSettings{
		SearchIntervalHours: 12,
		Profile: engineProfile{
			Qualities:     []string{"1080p", "2160p", "720p"},
			Cutoff:        "1080p",
			MinSeeders:    1,
			RejectSources: []string{"CAM", "TS", "TC", "DVDScr"},
		},
	}
}

type engineStore struct {
	path string
	mu   sync.Mutex
	st   engineState
	now  func() time.Time
}

func openEngineStore(path string) (*engineStore, error) {
	s := &engineStore{path: path, now: time.Now}
	s.st = engineState{Version: engineStateVersion, Settings: defaultEngineSettings(), NextReq: 1}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		return nil, fmt.Errorf("engine state %s is not valid JSON: %w", path, err)
	}
	if s.st.Settings.SearchIntervalHours <= 0 {
		s.st.Settings.SearchIntervalHours = 12
	}
	if len(s.st.Settings.Profile.Qualities) == 0 {
		s.st.Settings.Profile = defaultEngineSettings().Profile
	}
	if s.st.NextReq < 1 {
		s.st.NextReq = 1
	}
	return s, nil
}

// update runs fn under the lock and persists the result. fn's error aborts
// without writing, so a half-applied change never reaches disk.
func (s *engineStore) update(fn func(st *engineState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneEngineState(s.st)
	if err := fn(&next); err != nil {
		if errors.Is(err, errNoChange) {
			return nil // nothing to write
		}
		return err
	}
	if n := len(next.History); n > engineHistoryCap {
		next.History = next.History[n-engineHistoryCap:]
	}
	if err := s.write(next); err != nil {
		return err
	}
	s.st = next
	return nil
}

// errNoChange, returned from an update func, skips the write: a tick that
// found nothing new must not rewrite the whole file every minute.
var errNoChange = errors.New("no change")

// settings reads just the settings, without copying the whole state -- it is
// asked on every routing decision.
func (s *engineStore) settings() engineSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.st.Settings
	p := &out.Profile
	p.Qualities = append([]string(nil), p.Qualities...)
	p.RejectSources = append([]string(nil), p.RejectSources...)
	p.PreferredWords = append([]string(nil), p.PreferredWords...)
	p.RejectWords = append([]string(nil), p.RejectWords...)
	return out
}

// upsertByID replaces the entry with in's id, or appends in with a new id.
// keep lets a blank secret on edit keep the stored one.
func upsertByID[T any](list []T, in T, id func(*T) *string, keep func(old T, in *T)) ([]T, T) {
	if *id(&in) != "" {
		for i := range list {
			if *id(&list[i]) == *id(&in) {
				keep(list[i], &in)
				list[i] = in
				return list, in
			}
		}
	}
	if *id(&in) == "" {
		*id(&in) = newEngineID()
	}
	return append(list, in), in
}

func removeByID[T any](list []T, want string, id func(*T) *string) []T {
	out := list[:0]
	for i := range list {
		if *id(&list[i]) != want {
			out = append(out, list[i])
		}
	}
	return out
}

// view hands fn a copy, so a reader can never mutate shared state.
func (s *engineStore) view() engineState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneEngineState(s.st)
}

func (s *engineStore) write(st engineState) error {
	if s.path == "" {
		return nil // in-memory, for tests
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	// 0600: indexer API keys and the download client's password live here.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func cloneEngineState(in engineState) engineState {
	b, _ := json.Marshal(in)
	var out engineState
	_ = json.Unmarshal(b, &out)
	return out
}

func (st *engineState) item(id string) *engineItem {
	for i := range st.Items {
		if st.Items[i].ID == id {
			return &st.Items[i]
		}
	}
	return nil
}

func (st *engineState) logEvent(now time.Time, itemID, kind, detail string) {
	st.History = append(st.History, engineEvent{
		At: now.UTC().Format(time.RFC3339), ItemID: itemID, Kind: kind, Detail: detail,
	})
}
