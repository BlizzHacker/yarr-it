package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Seerr (Overseerr / Jellyseerr, now one project) as a request queue.
//
// Seerr's /api/v1 uses the same X-Api-Key header and the same /api/<ver>/
// layout as the *arrs, so the transport is the arr client with api "v1". Only
// health differs, because Seerr has no system/status.
//
// What Yarr.It does with it: list requests and approve or decline them. It does
// NOT create requests through Seerr -- Yarr.It already hands a request straight
// to Radarr, Sonarr or Cinephage, and routing the owner's own click through an
// approval queue they would then approve themselves is a step with no purpose.

type seerrProvider struct {
	c    *arrClient
	id   string
	name string

	// Seerr's request objects carry a TMDB id and no title. Titles never
	// change, so each is fetched once per process.
	titles sync.Map // "movie:603" -> seerrTitle
}

type seerrTitle struct {
	Title   string
	Year    int
	Artwork string
}

func newSeerrProvider(cfg arrConfig) *seerrProvider {
	id, name := cfg.ID, cfg.Name
	if id == "" {
		id = "seerr"
	}
	if name == "" {
		name = "Seerr"
	}
	return &seerrProvider{c: newArrClient(cfg, "v1", "Seerr", 0), id: id, name: name}
}

func (p *seerrProvider) ID() string        { return p.id }
func (p *seerrProvider) Name() string      { return p.name }
func (p *seerrProvider) Domains() []string { return []string{"video"} }
func (p *seerrProvider) Roles() []string   { return []string{"requests"} }
func (p *seerrProvider) Capabilities() []string {
	return []string{"health", "requestQueue", "requestDecide"}
}

func (p *seerrProvider) Health(ctx context.Context) Health {
	if p.c.apiKey == "" {
		return Health{State: HealthNotConfigured,
			Detail: "no API key set for Seerr — copy it from Seerr's Settings → General"}
	}
	if p.c.baseURL == "" {
		return Health{State: HealthNotConfigured, Detail: "no address set for Seerr"}
	}
	var st struct {
		Version string `json:"version"`
	}
	if err := p.c.get(ctx, "status", nil, &st); err != nil {
		return queueErrorHealth(err, "Seerr", p.c.baseURL, "check it against Settings → General")
	}
	if st.Version == "" {
		return Health{State: HealthIncompatible,
			Detail: "this address answers, but not like Seerr — check the URL"}
	}
	// /status is public, so it proves the address and not the key. auth/me is
	// the cheapest call that needs the key.
	var me struct {
		ID int `json:"id"`
	}
	if err := p.c.get(ctx, "auth/me", nil, &me); err != nil {
		h := queueErrorHealth(err, "Seerr", p.c.baseURL, "check it against Settings → General")
		h.Version = st.Version
		return h
	}
	return Health{State: HealthOK, Version: st.Version}
}

// --- the queue -------------------------------------------------------------

type seerrRequestPage struct {
	Results []seerrRequest `json:"results"`
}

type seerrRequest struct {
	ID        int    `json:"id"`
	Status    int    `json:"status"` // 1 pending, 2 approved, 3 declined, 4 failed, 5 completed
	Type      string `json:"type"`   // movie | tv
	Is4K      bool   `json:"is4k"`
	CreatedAt string `json:"createdAt"`
	Media     struct {
		TmdbID int `json:"tmdbId"`
		TvdbID int `json:"tvdbId"`
		Status int `json:"status"` // 3 processing, 4 partially available, 5 available
	} `json:"media"`
	RequestedBy struct {
		DisplayName string `json:"displayName"`
		Username    string `json:"username"`
	} `json:"requestedBy"`
	Seasons []struct {
		SeasonNumber int `json:"seasonNumber"`
	} `json:"seasons"`
}

// seerrFilter maps the shared vocabulary onto Seerr's filter words. Seerr has
// no "declined" filter, so declined and cancelled are fetched as "all" and cut
// here.
func seerrFilter(s RequestStatus) string {
	switch s {
	case RequestPending, RequestApproved, RequestProcessing, RequestAvailable, RequestFailed:
		return string(s)
	}
	return "all"
}

func seerrStatus(r seerrRequest) RequestStatus {
	switch r.Status {
	case 1:
		return RequestPending
	case 3:
		return RequestDeclined
	case 4:
		return RequestFailed
	case 5:
		return RequestAvailable
	}
	switch r.Media.Status {
	case 5:
		return RequestAvailable
	case 3, 4:
		return RequestProcessing
	}
	return RequestApproved
}

func (p *seerrProvider) Requests(ctx context.Context, status RequestStatus, limit int) ([]QueuedRequest, error) {
	if limit <= 0 {
		limit = requestsDefaultLimit
	}
	q := url.Values{
		"take":   {strconv.Itoa(limit)},
		"skip":   {"0"},
		"sort":   {"added"},
		"filter": {seerrFilter(status)},
	}
	var page seerrRequestPage
	if err := p.c.get(ctx, "request", q, &page); err != nil {
		return nil, err
	}

	out := make([]QueuedRequest, 0, len(page.Results))
	tmdb := make([]int, 0, len(page.Results))
	for _, r := range page.Results {
		st := seerrStatus(r)
		if status != "" && st != status {
			continue
		}
		out = append(out, p.queued(r, st))
		tmdb = append(tmdb, r.Media.TmdbID)
	}

	// Titles, bounded: a first paint of fifty requests must not be fifty
	// serial round trips, nor fifty at once against a small box.
	arrBoundedMap(len(out), len(out), func(i int) {
		if t := p.title(ctx, out[i].Type, tmdb[i]); t.Title != "" {
			out[i].Title, out[i].Year, out[i].Artwork = t.Title, t.Year, t.Artwork
		}
	})
	return out, nil
}

func (p *seerrProvider) queued(r seerrRequest, st RequestStatus) QueuedRequest {
	typ := "movie"
	canonical := ""
	if r.Type == "tv" {
		typ = "series"
		if r.Media.TvdbID > 0 {
			canonical = arrSeriesID(r.Media.TvdbID, false)
		}
	} else if r.Media.TmdbID > 0 {
		canonical = arrMovieID(r.Media.TmdbID)
	}
	who := r.RequestedBy.DisplayName
	if who == "" {
		who = r.RequestedBy.Username
	}
	var detail []string
	if r.Is4K {
		detail = append(detail, "4K")
	}
	if len(r.Seasons) > 0 {
		nums := make([]string, 0, len(r.Seasons))
		for _, s := range r.Seasons {
			nums = append(nums, strconv.Itoa(s.SeasonNumber))
		}
		detail = append(detail, "seasons "+strings.Join(nums, ", "))
	}
	backend := strconv.Itoa(r.ID)
	return QueuedRequest{
		ID:             requestID(p.id, backend),
		ProviderID:     p.id,
		ProviderItemID: backend,
		Domain:         "video",
		Type:           typ,
		Title:          fmt.Sprintf("TMDB %d", r.Media.TmdbID),
		Status:         st,
		RequestedBy:    who,
		RequestedAt:    r.CreatedAt,
		Detail:         strings.Join(detail, " · "),
		CanonicalID:    canonical,
		Decidable:      st == RequestPending,
	}
}

func (p *seerrProvider) title(ctx context.Context, typ string, tmdbID int) seerrTitle {
	if tmdbID <= 0 {
		return seerrTitle{}
	}
	kind := "movie"
	if typ == "series" {
		kind = "tv"
	}
	key := kind + ":" + strconv.Itoa(tmdbID)
	if v, ok := p.titles.Load(key); ok {
		return v.(seerrTitle)
	}
	var d struct {
		Title        string `json:"title"`
		Name         string `json:"name"`
		ReleaseDate  string `json:"releaseDate"`
		FirstAirDate string `json:"firstAirDate"`
		PosterPath   string `json:"posterPath"`
	}
	if err := p.c.get(ctx, kind+"/"+strconv.Itoa(tmdbID), nil, &d); err != nil {
		return seerrTitle{}
	}
	t := seerrTitle{Title: d.Title, Year: arrYear(d.ReleaseDate)}
	if t.Title == "" {
		t.Title, t.Year = d.Name, arrYear(d.FirstAirDate)
	}
	if d.PosterPath != "" {
		t.Artwork = "https://image.tmdb.org/t/p/w342" + d.PosterPath
	}
	p.titles.Store(key, t)
	return t
}

func (p *seerrProvider) Decide(ctx context.Context, backendID string, approve bool) error {
	if _, err := strconv.Atoi(backendID); err != nil {
		return &arrHTTPError{Status: http.StatusBadRequest, Method: "POST", Path: "request",
			Body: "Seerr request ids are numbers"}
	}
	verb := "decline"
	if approve {
		verb = "approve"
	}
	return p.c.post(ctx, "request/"+backendID+"/"+verb, nil, nil)
}

// seerrProvidersFromEnv reads SEERR_URL / SEERR_API_KEY. The two names Seerr
// had before the projects merged are honoured, so an existing env file works.
func seerrProvidersFromEnv() []Provider {
	for _, prefix := range []string{"SEERR", "JELLYSEERR", "OVERSEERR"} {
		if u := strings.TrimSpace(os.Getenv(prefix + "_URL")); u != "" {
			return []Provider{newSeerrProvider(arrConfig{
				BaseURL: u, APIKey: os.Getenv(prefix + "_API_KEY"),
			})}
		}
	}
	return nil
}
