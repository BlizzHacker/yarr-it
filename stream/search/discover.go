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
// come from catalogues -- TMDB and IGDB -- which are metadata, not torrents, so
// nothing is indexed or hosted here.
//
// A CLICK NO LONGER RUNS A SEARCH.
//
// It used to, and that was the defect: "clicking a title runs a normal search
// for it, which is where any actual sources come from". A tile arrived holding
// a real identity and threw it away to go looking for its own display name, so
// the tile could never be more certain than a search that had not run yet --
// and the person only found out after clicking. Measured on 2026-08-08, 171 of
// the 172 catalogue tiles on this page reached zero results.
//
// Every catalogue tile is now resolved to a real, openable target before the
// page is built, and a tile that does not resolve is not published. See
// discover_resolve.go for what that costs and why removal is the honest
// outcome rather than a disabled tile.

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
	// Play is the address of the thing itself, set whenever one is known. A
	// click on an item that has one opens that item and runs no search at all,
	// which is the difference between carrying an identity through and throwing
	// it away for a fuzzy match on a display name.
	Play string `json:"play,omitempty"`
	// State says what is actually KNOWN about getting hold of this, and exists
	// because there are three answers and only two used to be representable.
	//
	//	""           Play is set. Something was found and this is where it is.
	//	"found"      a source confirmed it holds this; the click searches for it
	//	"unchecked"  a source that could hold this exists and was not asked
	//
	// The last one is the one that matters. Treating it as "nothing has this"
	// is what deleted five shelves during a Prowlarr outage -- see
	// discover_resolve.go. A client must not render it as a promise, and must
	// not refuse to render it either.
	State string `json:"state,omitempty"`
	// Source names who will actually serve it, so a client can say where a
	// thing comes from without parsing the URL, and so a row that turns out to
	// be entirely one source is visible as that rather than as a mystery.
	Source string `json:"source,omitempty"`
}

type discoverCache struct {
	mu      sync.RWMutex
	rows    []discoverRow
	indexer indexerState
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
	s.discover.mu.RLock()
	if time.Now().Before(s.discover.expires) && len(s.discover.rows) > 0 {
		rows, idx := s.discover.rows, s.discover.indexer
		s.discover.mu.RUnlock()
		w.Header().Set("X-Cache", "HIT")
		writeJSON(w, 200, map[string]any{"rows": rows, "indexer": idx})
		return
	}
	s.discover.mu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	rows := make([]discoverRow, len(discoverSources))
	var archive, games []discoverRow
	var idx indexerState
	var wg sync.WaitGroup

	// One question to the torrent backend, alongside everything else. Its answer
	// does not fill a shelf; it decides what "archive.org does not have this"
	// is allowed to mean. See indexerHealth.
	wg.Add(1)
	go func() {
		defer wg.Done()
		idx = s.indexerHealth(ctx)
	}()

	// IGDB answers in well under a second while TMDB's five lists and the
	// archive queries take longer, so this runs alongside rather than after.
	wg.Add(1)
	go func() {
		defer wg.Done()
		games = s.igdb.discoverGames(ctx)
	}()

	// The archive rows are what make this more than a movie site, and they
	// need no TMDB key -- so they are fetched alongside rather than after, and
	// a deployment with no key still gets a landing page.
	wg.Add(1)
	go func() {
		defer wg.Done()
		archive = s.archiveDiscover(ctx, 24)
	}()

	if s.tmdb.enabled() {
		for i, src := range discoverSources {
			wg.Add(1)
			go func(i int, key, title, path string) {
				defer wg.Done()
				rows[i] = discoverRow{Title: title, Key: key, Items: s.tmdb.list(ctx, path)}
			}(i, src.key, src.title, src.path)
		}
	}
	wg.Wait()

	// Drop rows a source failed to return rather than rendering empty shelves.
	live := make([]discoverRow, 0, len(rows)+len(archive))
	for _, row := range rows {
		if len(row.Items) > 0 {
			live = append(live, row)
		}
	}
	// Films first -- they are still what most people arrive for. Then the
	// rated game shelves, which answer "what is worth playing", and only then
	// the archive.org rows, which answer "what can I start this second".
	live = append(live, games...)
	live = append(live, archive...)

	// Nothing above this line knows whether any of it can be delivered. TMDB
	// and IGDB answer "what is worth watching or playing", which is a different
	// question from "what will open when somebody presses this", and publishing
	// the first as though it were the second is the whole of the defect this
	// page had. This is where each tile gets told which of those it is.
	live = s.resolveDiscoverRows(ctx, live, idx)

	if len(live) > 0 {
		s.discover.mu.Lock()
		s.discover.rows = live
		s.discover.indexer = idx
		// A page built while the indexer was down is a page holding unchecked
		// tiles, and three hours is a long time to keep saying so after it
		// recovers. Held briefly instead, so the shelves settle on their own
		// rather than waiting out a cache that outlived the outage.
		ttl := 3 * time.Hour
		if idx.Configured && !idx.Reachable {
			ttl = 10 * time.Minute
		}
		s.discover.expires = time.Now().Add(ttl)
		s.discover.mu.Unlock()
		if s.warm != nil {
			go s.warm.refill()
		}
	}

	w.Header().Set("X-Cache", "MISS")
	writeJSON(w, 200, map[string]any{"rows": live, "indexer": idx})
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
