package main

// Spotify and Apple Music links: metadata in, search queries out.
//
// This is deliberately not a Spotify downloader, and it will not become one.
// Spotify's audio is DRM-protected; getting a file out of it means defeating
// Widevine, which is circumvention of an access control, not a feature. No
// amount of demand changes that, so the honest version of "paste a Spotify
// link" is:
//
//	read the public track list -> hand those titles to the search Yarr.It
//	already has -> the user finds them from sources they can legally obtain.
//
// The UI is written to match ("we looked these up for you", never
// "downloading from Spotify"), because a tool that implies it ripped Spotify
// while actually searching torrent indexers is lying about the one thing the
// user most needs to understand.
//
// Neither provider needs an API key here. Spotify's embed page and Apple
// Music's JSON-LD are both public, unauthenticated, and intended to be read by
// third parties -- so there is no credential to leak and no terms-of-service
// account to get banned.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type musicRef struct {
	Provider string // spotify | apple
	Kind     string // track | album | playlist | artist
	ID       string
	URL      string
}

type musicTrack struct {
	Title    string  `json:"title"`
	Artist   string  `json:"artist,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	// Query is what gets handed to Yarr.It's own search. It is built here so
	// the UI never has to invent one, and so the joining rule is in one place.
	Query string `json:"query"`
}

type musicResult struct {
	Provider string       `json:"provider"`
	Kind     string       `json:"kind"`
	Title    string       `json:"title"`
	Artist   string       `json:"artist,omitempty"`
	Artwork  string       `json:"artwork,omitempty"`
	Source   string       `json:"source"`
	Tracks   []musicTrack `json:"tracks"`
	Query    string       `json:"query"` // a whole-album search
	// Disclaimer is served with the data rather than left to the front end, so
	// there is no build of the UI in which this claim goes missing.
	Disclaimer string   `json:"disclaimer"`
	Notes      []string `json:"notes,omitempty"`
}

const musicDisclaimer = "Spotify and Apple Music streams are DRM-protected — nothing is downloaded from them. " +
	"This reads the public track list and searches Yarr.It's own indexers for each title."

var (
	reSpotify = regexp.MustCompile(`^/(?:intl-[a-z]{2}/)?(track|album|playlist|artist)/([A-Za-z0-9]{16,32})`)
	reApple   = regexp.MustCompile(`^/[a-z]{2}/(album|playlist|song|artist)/`)
)

// parseMusicLink recognises a music-service link. Matching is done on the
// parsed host, never on a substring of the raw text, so
// https://evil.example/open.spotify.com/track/x is not mistaken for Spotify.
func parseMusicLink(raw string) (*musicRef, bool) {
	u, err := parseTargetURL(raw)
	if err != nil {
		return nil, false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "open.spotify.com", "play.spotify.com":
		m := reSpotify.FindStringSubmatch(u.Path)
		if m == nil {
			return nil, false
		}
		return &musicRef{Provider: "spotify", Kind: m[1], ID: m[2], URL: raw}, true
	case "music.apple.com", "itunes.apple.com", "embed.music.apple.com":
		m := reApple.FindStringSubmatch(u.Path)
		if m == nil {
			return nil, false
		}
		kind := m[1]
		if kind == "song" {
			kind = "track"
		}
		// An Apple album URL with a ?i= parameter means "this one song on that
		// album", which is a track link wearing an album URL.
		if u.Query().Get("i") != "" {
			kind = "track"
		}
		return &musicRef{Provider: "apple", Kind: kind, URL: raw}, true
	}
	return nil, false
}

func (s *linkService) handleMusic(w http.ResponseWriter, r *http.Request) {
	setLinkCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	ref, ok := parseMusicLink(raw)
	if !ok {
		writeLinkError(w, http.StatusBadRequest, &resolveError{
			Code: codeUnsupported, Message: "that is not a Spotify or Apple Music link"})
		return
	}
	if !s.limiter.allow(linkClientIP(r)) {
		writeLinkError(w, http.StatusTooManyRequests, &resolveError{
			Code: codeRateLimited, Message: "too many links from this address — wait a moment"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var (
		res *musicResult
		err error
	)
	if ref.Provider == "spotify" {
		res, err = fetchSpotifyMeta(ctx, ref, s.egress.URL())
	} else {
		res, err = fetchAppleMeta(ctx, ref, s.egress.URL())
	}
	if err != nil {
		s.note(ref.Provider, codeExtractorBroke)
		writeLinkError(w, http.StatusBadGateway, &resolveError{
			Code: codeExtractorBroke, Extractor: ref.Provider,
			Message:  "could not read the public track list for that link",
			Upstream: truncate(err.Error(), 200)})
		return
	}
	s.note(ref.Provider, codeOK)
	writeJSON(w, 200, res)
}

// ---------------------------------------------------------------- spotify --

// Spotify's embed page carries a Next.js data blob with the entity and, for an
// album or playlist, its track list. This is the same document the official
// embed iframe renders from, so it is public by construction.
type spotifyEmbed struct {
	Props struct {
		PageProps struct {
			State struct {
				Data struct {
					Entity spotifyEntity `json:"entity"`
				} `json:"data"`
			} `json:"state"`
		} `json:"pageProps"`
	} `json:"props"`
}

type spotifyEntity struct {
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle"`
	Duration float64 `json:"duration"`
	// An album or playlist names its artist in `subtitle`; a single track
	// leaves that empty and uses `artists` instead. Reading only one of them
	// produced "Never Gonna Give You Up" with no artist, and a query with no
	// artist finds the wrong song on any indexer.
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	TrackList []spotifyEntity `json:"trackList"`
	Visual    struct {
		Image []struct {
			URL string `json:"url"`
		} `json:"image"`
	} `json:"visualIdentity"`
}

// artist reads whichever of the two shapes this entity uses.
func (e spotifyEntity) artist() string {
	if e.Subtitle != "" {
		return e.Subtitle
	}
	names := make([]string, 0, len(e.Artists))
	for _, a := range e.Artists {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	return strings.Join(names, ", ")
}

var reNextData = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)

func fetchSpotifyMeta(ctx context.Context, ref *musicRef, proxyURL string) (*musicResult, error) {
	body, err := fetchPublicPage(ctx,
		fmt.Sprintf("https://open.spotify.com/embed/%s/%s", ref.Kind, ref.ID), proxyURL)
	if err != nil {
		return nil, err
	}
	m := reNextData.FindSubmatch(body)
	if m == nil {
		return nil, errors.New("spotify embed page has no data block (their page shape changed)")
	}
	var doc spotifyEmbed
	if err := json.Unmarshal(m[1], &doc); err != nil {
		return nil, err
	}
	e := doc.Props.PageProps.State.Data.Entity
	title := linkFirstNonEmpty(e.Name, e.Title)
	if title == "" {
		return nil, errors.New("spotify returned no title for that link")
	}

	artist := e.artist()
	res := &musicResult{
		Provider: "spotify", Kind: ref.Kind, Title: title, Artist: artist,
		Source: ref.URL, Disclaimer: musicDisclaimer,
	}
	if len(e.Visual.Image) > 0 {
		res.Artwork = e.Visual.Image[0].URL
	}
	for _, t := range e.TrackList {
		name := linkFirstNonEmpty(t.Title, t.Name)
		if name == "" {
			continue
		}
		// A track's own credit wins; the album artist is the fallback, which
		// is what makes a compilation resolve to the right performer.
		a := linkFirstNonEmpty(t.artist(), artist)
		res.Tracks = append(res.Tracks, musicTrack{
			Title: name, Artist: a, Duration: t.Duration / 1000,
			Query: searchQueryFor(a, name),
		})
	}
	if len(res.Tracks) == 0 {
		res.Tracks = append(res.Tracks, musicTrack{
			Title: title, Artist: artist, Duration: e.Duration / 1000,
			Query: searchQueryFor(artist, title),
		})
	}
	res.Query = searchQueryFor(artist, title)
	// The embed blob is what the iframe shows, which for a long playlist is a
	// window rather than the whole thing. Saying so beats silently truncating.
	if ref.Kind == "playlist" && len(res.Tracks) >= 10 {
		res.Notes = append(res.Notes,
			"Spotify's public embed only exposes the first stretch of a long playlist, so this list may be partial.")
	}
	return res, nil
}

// ------------------------------------------------------------------ apple --

type appleLD struct {
	Type     string `json:"@type"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	ByArtist []struct {
		Name string `json:"name"`
	} `json:"byArtist"`
	Image  string `json:"image"`
	Tracks []struct {
		Name     string `json:"name"`
		Duration string `json:"duration"`
		ByArtist []struct {
			Name string `json:"name"`
		} `json:"byArtist"`
	} `json:"tracks"`
}

var reLDJSON = regexp.MustCompile(`(?s)<script[^>]*type="application/ld\+json"[^>]*>(.*?)</script>`)

func fetchAppleMeta(ctx context.Context, ref *musicRef, proxyURL string) (*musicResult, error) {
	body, err := fetchPublicPage(ctx, ref.URL, proxyURL)
	if err != nil {
		return nil, err
	}
	m := reLDJSON.FindSubmatch(body)
	if m == nil {
		return nil, errors.New("apple music page carries no structured data (their page shape changed)")
	}
	var doc appleLD
	if err := json.Unmarshal(m[1], &doc); err != nil {
		return nil, err
	}
	if doc.Name == "" {
		return nil, errors.New("apple music returned no title for that link")
	}
	artist := ""
	if len(doc.ByArtist) > 0 {
		artist = doc.ByArtist[0].Name
	}

	res := &musicResult{
		Provider: "apple", Kind: ref.Kind, Title: doc.Name, Artist: artist,
		Artwork: doc.Image, Source: ref.URL, Disclaimer: musicDisclaimer,
		Query: searchQueryFor(artist, doc.Name),
	}
	for _, t := range doc.Tracks {
		a := artist
		if len(t.ByArtist) > 0 && t.ByArtist[0].Name != "" {
			a = t.ByArtist[0].Name
		}
		res.Tracks = append(res.Tracks, musicTrack{
			Title: t.Name, Artist: a, Duration: parseISODuration(t.Duration),
			Query: searchQueryFor(a, t.Name),
		})
	}
	if len(res.Tracks) == 0 {
		res.Tracks = append(res.Tracks, musicTrack{
			Title: doc.Name, Artist: artist, Query: searchQueryFor(artist, doc.Name)})
	}
	return res, nil
}

// ------------------------------------------------------------------ utils --

const musicPageMaxBytes = 3 << 20

// fetchPublicPage reads a public metadata page through the SSRF-guarded
// client, bounded in size. These hosts are fixed and public, but the guard is
// applied anyway: it costs nothing and means no future edit can turn this into
// an unchecked fetcher by changing one URL.
func fetchPublicPage(ctx context.Context, target, proxyURL string) ([]byte, error) {
	if _, err := resolveTarget(ctx, nil, target); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := guardedClientVia(15*time.Second, proxyURL).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("that page answered %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, musicPageMaxBytes))
}

// searchQueryFor joins artist and title the way an indexer expects, and drops
// the decorations that turn a good query into a zero-result one: "(feat. X)",
// "- Remastered 2011", bracketed edition notes.
func searchQueryFor(artist, title string) string {
	title = stripTrackDecorations(title)
	artist = strings.TrimSpace(artist)
	// A "subtitle" on Spotify can be a comma-joined list of every credited
	// artist; the first is the one an indexer will have filed it under.
	if i := strings.IndexAny(artist, ",;"); i > 0 {
		artist = strings.TrimSpace(artist[:i])
	}
	if artist == "" {
		return strings.TrimSpace(title)
	}
	if strings.Contains(strings.ToLower(title), strings.ToLower(artist)) {
		return strings.TrimSpace(title)
	}
	return strings.TrimSpace(artist + " " + title)
}

var reDecorations = regexp.MustCompile(`(?i)\s*[\(\[](feat\.?|ft\.?|with|remaster(ed)?|deluxe|bonus|mono|stereo|live|explicit|radio edit|single version)[^)\]]*[\)\]]`)
var reTrailingRemaster = regexp.MustCompile(`(?i)\s*-\s*(remaster(ed)?|\d{4} remaster(ed)?|mono|stereo|single version|radio edit)[^-]*$`)

func stripTrackDecorations(s string) string {
	s = reDecorations.ReplaceAllString(s, "")
	s = reTrailingRemaster.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

var reISODur = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?$`)

// parseISODuration reads Apple's "PT1M4S". Returns 0 for anything it does not
// recognise, which the UI renders as "no duration" rather than "0 seconds".
func parseISODuration(s string) float64 {
	m := reISODur.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	var total float64
	if m[1] != "" {
		h, _ := strconv.ParseFloat(m[1], 64)
		total += h * 3600
	}
	if m[2] != "" {
		mm, _ := strconv.ParseFloat(m[2], 64)
		total += mm * 60
	}
	if m[3] != "" {
		ss, _ := strconv.ParseFloat(m[3], 64)
		total += ss
	}
	return total
}
