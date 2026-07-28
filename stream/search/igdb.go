package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Game discovery rows, from IGDB.
//
// The landing page already had archive.org shelves, but those rank by download
// count within one archive -- good for "what can I play right now", useless as
// an answer to "what are the great games". This is the games equivalent of the
// TMDB rows: real ratings, real cover art, real popularity, for the medium as a
// whole.
//
// THIS DELIBERATELY DOES NOT READ THE OWNER'S ROM LIBRARY. RomM's API is
// library-bound -- /api/search/roms takes a rom_id, so it can only describe
// games already on the shelf -- and a public landing page built from a private
// library is a public index of what one person owns. RomM's *credentials* for
// IGDB are reused, which is the part that describes games in general rather
// than this installation in particular.
//
// A click behaves exactly like a TMDB row: `Play` stays empty, so it runs a
// search and turns up archive.org items and torrents. The row says what is
// worth playing; the existing pipeline decides what can actually be played.

// perGameRow is how many covers a shelf shows, matching the film rows.
const perGameRow = 24

const (
	igdbAPI      = "https://api.igdb.com/v4"
	igdbTokenURL = "https://id.twitch.tv/oauth2/token"
	igdbImage    = "https://images.igdb.com/igdb/image/upload"
)

type igdbClient struct {
	clientID string
	secret   string
	http     *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newIGDB(clientID, secret string) *igdbClient {
	return &igdbClient{
		clientID: clientID,
		secret:   secret,
		http:     &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *igdbClient) enabled() bool {
	return c != nil && c.clientID != "" && c.secret != ""
}

// accessToken fetches and caches a Twitch app token.
//
// It is refreshed a minute early: a token that expires mid-request produces a
// 401 that looks exactly like bad credentials, which is a genuinely confusing
// thing to debug.
func (c *igdbClient) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}

	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.secret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, igdbTokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("igdb token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Never echo the body: a rejected credential exchange can repeat the
		// submitted secret back at you.
		return "", fmt.Errorf("igdb token: status %d", resp.StatusCode)
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("igdb token: %w", err)
	}
	c.token = out.AccessToken
	c.expires = time.Now().Add(time.Duration(out.ExpiresIn)*time.Second - time.Minute)
	return c.token, nil
}

// query runs one APIcalypse query against an IGDB endpoint.
func (c *igdbClient) query(ctx context.Context, endpoint, body string, into any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		igdbAPI+"/"+endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Client-ID", c.clientID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("igdb %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("igdb %s: status %d", endpoint, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

type igdbGame struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	Summary          string  `json:"summary"`
	TotalRating      float64 `json:"total_rating"`
	TotalRatingCount int     `json:"total_rating_count"`
	FirstRelease     int64   `json:"first_release_date"`
	Cover            struct {
		ImageID string `json:"image_id"`
	} `json:"cover"`
	Platforms []struct {
		Abbreviation string `json:"abbreviation"`
		Name         string `json:"name"`
	} `json:"platforms"`
}

// gameFields is the field list every row asks for, so items render identically
// whichever row they came from.
const gameFields = "fields name,summary,total_rating,total_rating_count," +
	"first_release_date,cover.image_id,platforms.abbreviation,platforms.name;"

// igdbRows are the shelves, chosen to answer different questions.
//
// `game_type = 0` restricts to main games: without it the lists fill with DLC,
// expansions, bundles and ports, which are real IGDB entries but not what
// anybody means by "top rated games".
var igdbRows = []struct {
	key, title, query string
}{
	{
		key: "games-top", title: "Top rated games",
		// A high rating from nine people is noise, so a floor on the number of
		// ratings is what makes this list mean anything.
		query: gameFields + " where total_rating_count > 400 & game_type = 0;" +
			" sort total_rating desc; limit 80;",
	},
	{
		key: "games-popular", title: "Popular games right now",
		query: gameFields + " where total_rating_count > 80 & game_type = 0 &" +
			" first_release_date > %d; sort total_rating desc; limit 80;",
	},
	{
		key: "games-classics", title: "Retro classics",
		// Pre-2001, which is roughly the cartridge and early-disc era -- the
		// games this player can actually emulate in a browser.
		query: gameFields + " where total_rating_count > 120 & game_type = 0 &" +
			" first_release_date < 978307200; sort total_rating desc; limit 80;",
	},
}

// discoverGames builds the game shelves.
func (c *igdbClient) discoverGames(ctx context.Context) []discoverRow {
	if !c.enabled() {
		return nil
	}
	// "Right now" is relative, so the recent-releases window is computed rather
	// than baked into a constant that silently ages.
	recent := time.Now().AddDate(-3, 0, 0).Unix()

	out := make([]discoverRow, len(igdbRows))
	var wg sync.WaitGroup
	for i, row := range igdbRows {
		wg.Add(1)
		go func(i int, key, title, q string) {
			defer wg.Done()
			if strings.Contains(q, "%d") {
				q = fmt.Sprintf(q, recent)
			}
			var games []igdbGame
			if err := c.query(ctx, "games", q, &games); err != nil {
				log.Printf("discover %s: %v", key, err)
				return
			}
			out[i] = discoverRow{Title: title, Key: key, Items: gameItems(games)}
		}(i, row.key, row.title, row.query)
	}
	wg.Wait()

	return dedupeShelves(out)
}

// dedupeShelves keeps each title on the first shelf that claims it.
//
// The shelves overlap heavily by nature: the highest-rated games ever made are
// largely the retro ones, so "Top rated" and "Retro classics" came back as the
// same three games in the same order -- the same shelf printed twice, which
// reads as a bug in the page. Later shelves fill down their own ranking with
// whatever is left, which is why each query over-fetches.
func dedupeShelves(in []discoverRow) []discoverRow {
	seen := make(map[string]bool)
	rows := make([]discoverRow, 0, len(in))
	for _, r := range in {
		kept := make([]discoverItm, 0, len(r.Items))
		for _, it := range r.Items {
			key := strings.ToLower(it.Title)
			if seen[key] {
				continue
			}
			seen[key] = true
			kept = append(kept, it)
		}
		if len(kept) > perGameRow {
			kept = kept[:perGameRow]
		}
		if len(kept) > 0 {
			r.Items = kept
			rows = append(rows, r)
		}
	}
	return rows
}

func gameItems(games []igdbGame) []discoverItm {
	items := make([]discoverItm, 0, len(games))
	for _, g := range games {
		// A shelf is cover art. An entry with no cover renders as a grey box
		// with a title in it, which looks broken next to the film rows.
		if g.Cover.ImageID == "" {
			continue
		}
		items = append(items, discoverItm{
			Title: g.Name,
			Year:  igdbYear(g.FirstRelease),
			// t_cover_big is the same 264x374 aspect the film posters use, so
			// the rows line up. Served by IGDB directly -- an <img> is not
			// subject to CORS, so this costs the relay nothing.
			Poster:   fmt.Sprintf("%s/t_cover_big/%s.jpg", igdbImage, g.Cover.ImageID),
			Overview: g.Summary,
			// IGDB rates out of 100; the film rows show a 10-point score.
			Rating:    g.TotalRating / 10,
			MediaType: "game",
			// Left empty on purpose: a click runs a search, exactly like a film
			// row, and the existing pipeline finds an archive.org item or a
			// torrent. The shelf says what is worth playing; it is not a
			// pointer at anybody's library.
		})
	}
	return items
}

func igdbYear(unix int64) int {
	if unix <= 0 {
		return 0
	}
	return time.Unix(unix, 0).UTC().Year()
}
