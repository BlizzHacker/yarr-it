package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Discover: the browsable landing page.
//
// Search alone requires you to already know what you want, which is the main
// thing a torrent index feels like and a streaming app does not. These rows
// come straight from TMDB -- they are catalogue metadata, not torrents, so
// nothing is indexed or hosted here. Clicking a title runs a normal search for
// it, which is where any actual sources come from.

type discoverRow struct {
	Title string        `json:"title"`
	Key   string        `json:"key"`
	Items []discoverItm `json:"items"`
}

type discoverItm struct {
	Title     string  `json:"title"`
	Year      int     `json:"year"`
	Poster    string  `json:"poster,omitempty"`
	Backdrop  string  `json:"backdrop,omitempty"`
	Overview  string  `json:"overview,omitempty"`
	Rating    float64 `json:"rating,omitempty"`
	MediaType string  `json:"mediaType"`
}

type discoverCache struct {
	mu      sync.RWMutex
	rows    []discoverRow
	expires time.Time
}

// TMDB's own lists. Trending changes daily, so a few hours of cache is ample
// and keeps us far inside the rate limit.
var discoverSources = []struct {
	key, title, path string
}{
	{"trending", "Trending this week", "/trending/all/week"},
	{"popular-movies", "Popular films", "/movie/popular"},
	{"top-movies", "Top rated films", "/movie/top_rated"},
	{"popular-tv", "Popular TV", "/tv/popular"},
	{"now-playing", "In cinemas now", "/movie/now_playing"},
}

func (s *server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if !s.tmdb.enabled() {
		writeJSON(w, 200, map[string]any{"rows": []discoverRow{}})
		return
	}

	s.discover.mu.RLock()
	if time.Now().Before(s.discover.expires) && len(s.discover.rows) > 0 {
		rows := s.discover.rows
		s.discover.mu.RUnlock()
		w.Header().Set("X-Cache", "HIT")
		writeJSON(w, 200, map[string]any{"rows": rows})
		return
	}
	s.discover.mu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	rows := make([]discoverRow, len(discoverSources))
	var wg sync.WaitGroup
	for i, src := range discoverSources {
		wg.Add(1)
		go func(i int, key, title, path string) {
			defer wg.Done()
			rows[i] = discoverRow{Title: title, Key: key, Items: s.tmdb.list(ctx, path)}
		}(i, src.key, src.title, src.path)
	}
	wg.Wait()

	// Drop rows TMDB failed to return rather than rendering empty shelves.
	live := make([]discoverRow, 0, len(rows))
	for _, row := range rows {
		if len(row.Items) > 0 {
			live = append(live, row)
		}
	}

	if len(live) > 0 {
		s.discover.mu.Lock()
		s.discover.rows = live
		s.discover.expires = time.Now().Add(3 * time.Hour)
		s.discover.mu.Unlock()
		if s.warm != nil {
			go s.warm.refill()
		}
	}

	w.Header().Set("X-Cache", "MISS")
	writeJSON(w, 200, map[string]any{"rows": live})
}

func (c *tmdbClient) list(ctx context.Context, path string) []discoverItm {
	u := fmt.Sprintf("%s%s?api_key=%s&language=en-US&page=1", tmdbBase, path, c.apiKey)
	var out struct {
		Results []struct {
			tmdbHit
			MediaType string `json:"media_type"`
		} `json:"results"`
	}
	if err := c.getJSON(ctx, u, &out); err != nil {
		return nil
	}

	items := make([]discoverItm, 0, 20)
	for _, h := range out.Results {
		title := h.Title
		if title == "" {
			title = h.Name
		}
		if title == "" {
			continue
		}
		date := h.ReleaseDate
		if date == "" {
			date = h.FirstAirDate
		}
		year := 0
		if len(date) >= 4 {
			fmt.Sscanf(date[:4], "%d", &year)
		}
		mt := h.MediaType
		if mt == "" {
			if h.Name != "" && h.Title == "" {
				mt = "tv"
			} else {
				mt = "movie"
			}
		}
		it := discoverItm{
			Title: title, Year: year, Overview: h.Overview,
			Rating: h.VoteAverage, MediaType: mt,
		}
		if h.PosterPath != "" {
			it.Poster = tmdbImgBase + "/w342" + h.PosterPath
		}
		if h.BackdropPath != "" {
			it.Backdrop = tmdbImgBase + "/w1280" + h.BackdropPath
		}
		items = append(items, it)
		if len(items) >= 20 {
			break
		}
	}
	return items
}

var _ = json.Marshal
