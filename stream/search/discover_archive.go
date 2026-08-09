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

// Browsable rows built from things that exist.
//
// The landing page was five rows of TMDB, so the site looked like a movie site
// no matter what else it could play. These rows come from the Internet
// Archive: games, films, television, books, audiobooks and comics that are
// free, legal, and open in the browser immediately.
//
// The distinction from the catalogue rows is the whole point, and it is worth
// stating plainly because the page used to blur it. A catalogue row is a list
// of names, and a name is not a promise anybody can keep -- measured on
// 2026-08-08, all 172 catalogue tiles on the live page reached nothing. Every
// row here starts from an identifier, so the tile IS the thing and the click
// opens it.
//
// Why not the RomM and Komga servers on the home estate, which hold far more:
// this site is public and needs no login, so a row sourced from a private
// library publishes a catalogue of what somebody owns, and the files behind it
// are not servable to anonymous visitors anyway. Those belong behind a login,
// which is why romm.go/komga.go take a credential and stay switched off until
// one is configured.

type archiveRow struct {
	key, title, query, sort string
	// mediaType decides what the client does with a click: "game" opens the
	// player, "text" opens the reader.
	mediaType string
}

// Each row is one Solr query. `emulator:[* TO *]` is the same definition the
// game search uses: an item that declares an emulator is one archive.org will
// run in a browser.
var archiveRows = []archiveRow{
	{
		key: "ia-games", title: "Games you can play right now", mediaType: "game",
		query: `emulator:[* TO *] AND mediatype:(software)`,
		sort:  "downloads desc",
	},
	{
		key: "ia-arcade", title: "Arcade cabinets", mediaType: "game",
		query: `collection:(internetarcade) AND mediatype:(software)`,
		sort:  "downloads desc",
	},
	{
		key: "ia-dos", title: "MS-DOS classics", mediaType: "game",
		query: `collection:(softwarelibrary_msdos_games) AND mediatype:(software)`,
		sort:  "downloads desc",
	},
	{
		key: "ia-flash", title: "Flash, still playable", mediaType: "game",
		query: `emulator:(ruffle-swf) AND mediatype:(software)`,
		sort:  "downloads desc",
	},
	// Film and television.
	//
	// These are new, and they exist because of what the TMDB rows turned out to
	// be. TMDB answers "what is popular", which is a fine question and the wrong
	// one for a shelf: measured on 2026-08-08, all 100 tiles across the five
	// TMDB rows reached nothing, because a film in cinemas this week has no free
	// legal source and never will. The rows below answer the question a shelf is
	// actually making a promise about -- what can somebody watch right now --
	// and every tile on them carries the identifier of the thing itself.
	//
	// The collections are the Archive's own curated free-film libraries, chosen
	// the same way the books row chose Gutenberg over controlled digital
	// lending: what is free to watch now, rather than what is merely present.
	// `format:(MPEG4)` is the same honesty check the film resolver uses --
	// mediatype:(movies) also holds posters, stills and audio-only lectures, and
	// an item with no browser-playable derivative is a details page, not a film.
	{
		key: "ia-films", title: "Films you can watch now", mediaType: "video",
		query: `collection:(feature_films) AND mediatype:(movies) AND format:(MPEG4)`,
		sort:  "downloads desc",
	},
	{
		key: "ia-tv", title: "Classic television", mediaType: "video",
		query: `collection:(classic_tv) AND mediatype:(movies) AND format:(MPEG4)`,
		sort:  "downloads desc",
	},
	{
		key: "ia-cartoons", title: "Cartoons", mediaType: "video",
		query: `collection:(animationandcartoons) AND mediatype:(movies) AND format:(MPEG4)`,
		sort:  "downloads desc",
	},
	{
		key: "ia-books", title: "Books", mediaType: "text",
		query: `collection:(gutenberg) AND mediatype:(texts)`,
		sort:  "downloads desc",
	},
	{
		// Named golden-age publisher collections rather than the general
		// `comics` bucket, which is user-uploaded and mostly commercial runs
		// still in copyright -- it put a complete Batman run at the top of this
		// row. Filtering that bucket by year does not help either: the year
		// field describes the issues, not the upload, so a scan of a full
		// modern run still matches 1940.
		key: "ia-comics", title: "Golden age comics", mediaType: "text",
		query: `collection:(fawcett-comics OR four-favorites-comics OR ` +
			`comic-ace-dotty OR historietas-collection) AND mediatype:(texts)`,
		sort: "downloads desc",
	},
	{
		key: "ia-audiobooks", title: "Audiobooks", mediaType: "audio",
		query: `collection:(librivoxaudio) AND mediatype:(audio)`,
		sort:  "downloads desc",
	},
}

// flexString and flexStrings for the same reason archive.go uses them: every
// Solr field is multi-valued in the schema, and one item catalogued with two
// titles used to fail the decode for the entire shelf.
type iaSearchDoc struct {
	Identifier string          `json:"identifier"`
	Title      flexString      `json:"title"`
	Downloads  int             `json:"downloads"`
	Year       json.RawMessage `json:"year"`
	Emulator   flexString      `json:"emulator"`
	Collection flexStrings     `json:"collection"`
}

// fetchArchiveRow runs one row's query.
func fetchArchiveRow(ctx context.Context, row archiveRow, limit int) (discoverRow, error) {
	params := url.Values{}
	params.Set("q", row.query)
	for _, f := range []string{"identifier", "title", "downloads", "year", "emulator", "collection"} {
		params.Add("fl[]", f)
	}
	params.Add("sort[]", row.sort)
	// Over-fetch. Duplicates and adult items are dropped below, and asking for
	// exactly a shelf's width means a shelf ends up narrower than the ones
	// beside it for reasons a visitor cannot see.
	params.Set("rows", fmt.Sprint(limit+archiveRowSlack))
	params.Set("output", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		archiveSearchAPI+"?"+params.Encode(), nil)
	if err != nil {
		return discoverRow{}, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return discoverRow{}, fmt.Errorf("%s: %w", row.key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return discoverRow{}, fmt.Errorf("%s: status %d", row.key, resp.StatusCode)
	}

	var body struct {
		Response struct {
			Docs []iaSearchDoc `json:"docs"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return discoverRow{}, fmt.Errorf("%s: %w", row.key, err)
	}

	out := discoverRow{Title: row.title, Key: row.key, Items: []discoverItm{}}
	seen := make(map[string]bool)
	for _, d := range body.Response.Docs {
		if d.Identifier == "" || seen[d.Identifier] {
			continue
		}
		seen[d.Identifier] = true
		title := strings.TrimSpace(d.Title.String())
		if title == "" {
			title = d.Identifier
		}
		// A shelf is not a search. A search returns what the Archive has,
		// because somebody asked for it; a shelf is an offer this page makes
		// unprompted, on a landing page with no sign-in and no age gate.
		//
		// searchArchive has always applied this and these rows never did, which
		// did not matter while they were games and Gutenberg. It matters now
		// that they include feature films: the Archive's own `feature_films`
		// collection put "Diary of a Nudist", "The Naked Witch" and "The Child
		// Molester (1964)" on the front page, unasked, in the first
		// twenty-four.
		if isAdultItem(title, d.Identifier, d.Collection) {
			continue
		}
		out.Items = append(out.Items, discoverItm{
			Title: title,
			Year:  archiveYear(d.Year),
			// Their thumbnail service. An <img> is not subject to CORS, so this
			// loads straight from them and costs this relay nothing.
			Poster:    "https://archive.org/services/img/" + d.Identifier,
			MediaType: row.mediaType,
			// Clicking plays the item itself rather than running a search for
			// its name, which is the whole difference between this and the
			// TMDB rows: these are the thing, not a pointer to look for it.
			Play: playTargetFor(d, row.mediaType),
		})
		if len(out.Items) >= limit {
			break
		}
	}
	return out, nil
}

// archiveRowSlack is how much more than a shelf's width to ask for, so the
// filtering below cannot leave a short shelf.
const archiveRowSlack = 12

// playTargetFor picks how an item opens.
//
// A game archive.org can run AND EmulatorJS has a core for opens in ours,
// because theirs has no on-screen controls and is unusable on a phone.
// Everything else opens in theirs, which costs no bandwidth here.
func playTargetFor(d iaSearchDoc, mediaType string) string {
	details := "https://archive.org/details/" + d.Identifier
	if mediaType == "game" && ejsCoreFor(d.Emulator.String()) != "" {
		return details + "#ejs"
	}
	// Music opens as its own track list rather than as an iframe of somebody
	// else's player. Without this a shelf tile and a search result for the same
	// concert behave differently -- the search result lists 21 playable tracks,
	// the tile hands the whole show to the Archive's audio player. Same
	// convention as `#ejs`; see music_item.go.
	if canonicalDomain(mediaType) == domainMusic {
		return details + "#music"
	}
	return details
}

// archiveDiscover returns every row, fetched concurrently.
//
// A row that fails is dropped rather than failing the page: seven rows where
// one collection is briefly unreachable is still a landing page, and an error
// there would replace the whole thing with nothing.
func (s *server) archiveDiscover(ctx context.Context, perRow int) []discoverRow {
	out := make([]discoverRow, len(archiveRows))
	var wg sync.WaitGroup
	for i, r := range archiveRows {
		wg.Add(1)
		go func(i int, r archiveRow) {
			defer wg.Done()
			got, err := fetchArchiveRow(ctx, r, perRow)
			if err != nil {
				log.Printf("discover %s: %v", r.key, err)
				return
			}
			out[i] = got
		}(i, r)
	}
	wg.Wait()

	rows := make([]discoverRow, 0, len(out))
	for _, r := range out {
		if len(r.Items) > 0 {
			rows = append(rows, r)
		}
	}
	return rows
}
