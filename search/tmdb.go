package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TMDB enrichment. Torrent indexers return filenames; TMDB turns those into
// something that looks like a library — poster, backdrop, synopsis, rating,
// genres, runtime. This is the whole difference between a directory listing
// and a product people want to browse.
//
// Lookups are cached hard and indefinitely: artwork for a 2010 film does not
// change, and TMDB rate-limits.

const (
	tmdbBase     = "https://api.themoviedb.org/3"
	tmdbImgBase  = "https://image.tmdb.org/t/p"
	tmdbTimeout  = 8 * time.Second
	tmdbCacheCap = 4000
)

type artwork struct {
	TMDBID    int      `json:"tmdbId,omitempty"`
	Poster    string   `json:"poster,omitempty"`
	Backdrop  string   `json:"backdrop,omitempty"`
	Overview  string   `json:"overview,omitempty"`
	Rating    float64  `json:"rating,omitempty"`
	Genres    []string `json:"genres,omitempty"`
	Released  string   `json:"released,omitempty"`
	MediaType string   `json:"mediaType,omitempty"` // movie | tv
	Found     bool     `json:"found"`
}

type tmdbClient struct {
	apiKey string
	http   *http.Client

	mu     sync.RWMutex
	cache  map[string]artwork
	genres map[int]string
}

func newTMDB(apiKey string) *tmdbClient {
	c := &tmdbClient{
		apiKey: apiKey,
		http:   &http.Client{Timeout: tmdbTimeout},
		cache:  make(map[string]artwork),
		genres: make(map[int]string),
	}
	if apiKey != "" {
		go c.loadGenres()
	}
	return c
}

func (c *tmdbClient) enabled() bool { return c != nil && c.apiKey != "" }

// loadGenres fetches the id->name map once so cards can show real genre labels
// instead of opaque numbers.
func (c *tmdbClient) loadGenres() {
	for _, kind := range []string{"movie", "tv"} {
		u := fmt.Sprintf("%s/genre/%s/list?api_key=%s", tmdbBase, kind, c.apiKey)
		var out struct {
			Genres []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"genres"`
		}
		if err := c.getJSON(context.Background(), u, &out); err != nil {
			continue
		}
		c.mu.Lock()
		for _, g := range out.Genres {
			c.genres[g.ID] = g.Name
		}
		c.mu.Unlock()
	}
}

func (c *tmdbClient) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("tmdb status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type tmdbHit struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
	GenreIDs     []int   `json:"genre_ids"`
}

// lookup resolves one title to artwork. Series are searched against /search/tv
// and films against /search/movie, because searching the wrong endpoint returns
// confident nonsense (a film named after a show, and vice versa).
func (c *tmdbClient) lookup(ctx context.Context, title string, year int, isSeries bool) artwork {
	if !c.enabled() || title == "" {
		return artwork{}
	}
	key := fmt.Sprintf("%s|%d|%t", strings.ToLower(title), year, isSeries)

	c.mu.RLock()
	if a, ok := c.cache[key]; ok {
		c.mu.RUnlock()
		return a
	}
	c.mu.RUnlock()

	kind := "movie"
	if isSeries {
		kind = "tv"
	}
	u := fmt.Sprintf("%s/search/%s?api_key=%s&query=%s&include_adult=false",
		tmdbBase, kind, c.apiKey, url.QueryEscape(title))
	if year > 0 {
		if isSeries {
			u += fmt.Sprintf("&first_air_date_year=%d", year)
		} else {
			u += fmt.Sprintf("&year=%d", year)
		}
	}

	var out struct {
		Results []tmdbHit `json:"results"`
	}
	a := artwork{MediaType: kind}
	if err := c.getJSON(ctx, u, &out); err == nil && len(out.Results) > 0 {
		h := out.Results[0]
		a.TMDBID = h.ID
		a.Overview = h.Overview
		a.Rating = h.VoteAverage
		a.Found = true
		if h.PosterPath != "" {
			a.Poster = tmdbImgBase + "/w342" + h.PosterPath
		}
		if h.BackdropPath != "" {
			a.Backdrop = tmdbImgBase + "/w1280" + h.BackdropPath
		}
		if h.ReleaseDate != "" {
			a.Released = h.ReleaseDate
		} else {
			a.Released = h.FirstAirDate
		}
		c.mu.RLock()
		for _, id := range h.GenreIDs {
			if n, ok := c.genres[id]; ok {
				a.Genres = append(a.Genres, n)
			}
		}
		c.mu.RUnlock()
	}

	c.mu.Lock()
	if len(c.cache) < tmdbCacheCap {
		c.cache[key] = a
	}
	c.mu.Unlock()
	return a
}

// enrich fills artwork for a page of cards concurrently. Bounded so a wide
// result set cannot open a hundred sockets on a 1 GB box or trip TMDB's
// rate limit.
func (c *tmdbClient) enrich(ctx context.Context, cards []card, limit int) {
	if !c.enabled() {
		return
	}
	if limit > len(cards) {
		limit = len(cards)
	}
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cards[i].Art = c.lookup(ctx, cards[i].Title, cards[i].Year, cards[i].IsSeries)
		}(i)
	}
	wg.Wait()
}
