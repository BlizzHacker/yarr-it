package main

// Radarr: films.
//
// It is a discovery, acquisition and library provider for the "video" domain
// and nothing else. It has no guide, no channels and serves no bytes, so it
// implements none of those interfaces rather than stubbing them with errors
// nobody reads -- asking a Radarr for a TV schedule should be a compile-time
// no, not a runtime disappointment.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// The oldest Radarr whose /api/v3 looks like what this adapter sends. v3 is
// the API; Radarr 3.0 is the release that shipped it.
const radarrMinMajor = 3

type radarrMovie struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle,omitempty"`
	SortTitle     string `json:"sortTitle,omitempty"`
	Year          int    `json:"year"`
	Overview      string `json:"overview,omitempty"`
	Studio        string `json:"studio,omitempty"`

	TmdbID int    `json:"tmdbId"`
	ImdbID string `json:"imdbId,omitempty"`

	HasFile     bool   `json:"hasFile"`
	MovieFileID int    `json:"movieFileId"`
	SizeOnDisk  int64  `json:"sizeOnDisk,omitempty"`
	Monitored   bool   `json:"monitored"`
	IsAvailable bool   `json:"isAvailable,omitempty"`
	Status      string `json:"status,omitempty"`

	Images       []arrImage `json:"images,omitempty"`
	RemotePoster string     `json:"remotePoster,omitempty"`
	TitleSlug    string     `json:"titleSlug,omitempty"`

	QualityProfileID    int    `json:"qualityProfileId,omitempty"`
	RootFolderPath      string `json:"rootFolderPath,omitempty"`
	MinimumAvailability string `json:"minimumAvailability,omitempty"`

	AddOptions *radarrAddOptions `json:"addOptions,omitempty"`
}

type radarrAddOptions struct {
	SearchForMovie bool   `json:"searchForMovie"`
	Monitor        string `json:"monitor,omitempty"`
}

type radarrQueueRecord struct {
	arrQueueRecord
	Movie *radarrMovie `json:"movie,omitempty"`
}

type radarrQueuePage struct {
	TotalRecords int                 `json:"totalRecords"`
	Records      []radarrQueueRecord `json:"records"`
}

type radarrProvider struct {
	c    *arrClient
	id   string
	name string
}

func newRadarrProvider(cfg arrConfig) *radarrProvider {
	id, name := cfg.ID, cfg.Name
	if id == "" {
		id = "radarr"
	}
	if name == "" {
		name = "Radarr"
	}
	return &radarrProvider{
		c:    newArrClient(cfg, "v3", "Radarr", radarrMinMajor),
		id:   id,
		name: name,
	}
}

func (p *radarrProvider) ID() string   { return p.id }
func (p *radarrProvider) Name() string { return p.name }

// Canonical ids from schema.json, never "movies". The Registry refuses the
// other spelling on the way in, which is the last cheap place to catch it.
func (p *radarrProvider) Domains() []string { return []string{"video"} }

func (p *radarrProvider) Roles() []string {
	return []string{"discovery", "acquisition", "library"}
}

func (p *radarrProvider) Capabilities() []string {
	return []string{"health", "search", "details", "library", "libraryStatus", "request", "activity"}
}

func (p *radarrProvider) Health(ctx context.Context) Health { return p.c.health(ctx) }

// --- search ----------------------------------------------------------------

// Search answers "what films exist by this name", library or not.
//
// The queue is fetched alongside the lookup rather than after it: without it a
// film that is 60% downloaded looks identical to one nobody has ever asked
// for, and the user requests it again.
func (p *radarrProvider) Search(ctx context.Context, query, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "video") {
		return nil, nil // Not ours. Not an error.
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	var (
		found []radarrMovie
		queue map[int]arrQueueRecord
		qerr  error
		wg    sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		queue, qerr = p.queueByMovie(ctx)
	}()

	err := p.c.get(ctx, "movie/lookup", url.Values{"term": {query}}, &found)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	// A queue we could not read costs state detail, not the search. Downgrading
	// to "we know it is in the library but not what it is doing" beats failing
	// a search that otherwise worked.
	if qerr != nil {
		queue = nil
	}

	out := make([]MediaItem, 0, len(found))
	for _, m := range found {
		out = append(out, p.toItem(m, queue))
	}
	return out, nil
}

// toItem maps one Radarr movie onto the canonical shape. Type is "movie" from
// schema.json's video types; nothing here invents a word.
func (p *radarrProvider) toItem(m radarrMovie, queue map[int]arrQueueRecord) MediaItem {
	inLibrary := m.ID > 0
	// A lookup result omits hasFile entirely but still carries movieFileId, so
	// the file question is answered by either.
	hasFile := m.HasFile || m.MovieFileID > 0

	var queued *arrQueueRecord
	if inLibrary && queue != nil {
		if r, ok := queue[m.ID]; ok {
			queued = &r
		}
	}

	art := arrPoster(m.Images)
	if art == "" {
		art = m.RemotePoster
	}

	return MediaItem{
		CanonicalID:    arrMovieID(m.TmdbID),
		Domain:         "video",
		Type:           "movie",
		Title:          m.Title,
		Subtitle:       m.Studio,
		Year:           m.Year,
		Overview:       m.Overview,
		Artwork:        art,
		ProviderID:     p.id,
		ProviderItemID: strconv.Itoa(m.ID),
		State:          arrLeafState(inLibrary, hasFile, m.Monitored, queued),
	}
}

// --- library ---------------------------------------------------------------

func (p *radarrProvider) Library(ctx context.Context, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "video") {
		return nil, nil
	}

	var (
		movies []radarrMovie
		queue  map[int]arrQueueRecord
		qerr   error
		wg     sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		queue, qerr = p.queueByMovie(ctx)
	}()

	// Radarr does not paginate /movie, so this is the whole shelf in one
	// document -- 28 MB on this estate. It is streamed rather than buffered
	// for that reason; see arrClient.do.
	err := p.c.get(ctx, "movie", nil, &movies)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	if qerr != nil {
		queue = nil
	}

	out := make([]MediaItem, 0, len(movies))
	for _, m := range movies {
		out = append(out, p.toItem(m, queue))
	}
	return out, nil
}

// LibraryStatus answers for one film without fetching the shelf. Radarr will
// filter /movie by tmdbId server-side, which turns a 28 MB answer into a
// two-line one.
func (p *radarrProvider) LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error) {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return StateUnknown, err
	}
	if ref.Scheme != "tmdb" || ref.Type != "movie" {
		// Not something this provider can have an opinion about. Unknown, not
		// missing: claiming "missing" about an id we cannot read would offer a
		// request for something we never checked.
		return StateUnknown, nil
	}

	var movies []radarrMovie
	if err := p.c.get(ctx, "movie", url.Values{"tmdbId": {ref.ID}}, &movies); err != nil {
		return StateUnknown, err
	}
	if len(movies) == 0 {
		return StateMissing, nil
	}
	m := movies[0]

	var queued *arrQueueRecord
	if !m.HasFile && m.MovieFileID == 0 {
		if queue, err := p.queueByMovie(ctx); err == nil {
			if r, ok := queue[m.ID]; ok {
				queued = &r
			}
		}
	}
	return arrLeafState(true, m.HasFile || m.MovieFileID > 0, m.Monitored, queued), nil
}

// --- details ---------------------------------------------------------------

// Details returns one film. A film contains nothing, so the children list is
// always empty -- the shape is shared with Sonarr and Lidarr, where it is not.
func (p *radarrProvider) Details(ctx context.Context, canonicalID string) (MediaItem, []MediaItem, error) {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return MediaItem{}, nil, err
	}
	if ref.Scheme != "tmdb" || ref.Type != "movie" {
		return MediaItem{}, nil, fmt.Errorf("%s cannot describe %q", p.name, canonicalID)
	}

	m, found, err := p.findMovie(ctx, ref.ID)
	if err != nil {
		return MediaItem{}, nil, err
	}
	if !found {
		return MediaItem{}, nil, fmt.Errorf("%s knows nothing about %s", p.name, canonicalID)
	}
	queue, _ := p.queueByMovie(ctx)
	return p.toItem(m, queue), nil, nil
}

// findMovie prefers the library copy, then falls back to the metadata lookup.
// The order matters: the library copy is the one that knows whether the file
// is on disk, and it is the row a request has to act on.
func (p *radarrProvider) findMovie(ctx context.Context, tmdbID string) (radarrMovie, bool, error) {
	var mine []radarrMovie
	if err := p.c.get(ctx, "movie", url.Values{"tmdbId": {tmdbID}}, &mine); err != nil {
		return radarrMovie{}, false, err
	}
	if len(mine) > 0 {
		return mine[0], true, nil
	}
	var found []radarrMovie
	if err := p.c.get(ctx, "movie/lookup", url.Values{"term": {"tmdb:" + tmdbID}}, &found); err != nil {
		return radarrMovie{}, false, err
	}
	if len(found) == 0 {
		return radarrMovie{}, false, nil
	}
	return found[0], true, nil
}

// --- activity --------------------------------------------------------------

func (p *radarrProvider) Activity(ctx context.Context) ([]ActivityItem, error) {
	var page radarrQueuePage
	err := p.c.get(ctx, "queue", url.Values{
		"pageSize":                 {"200"},
		"includeMovie":             {"true"},
		"includeUnknownMovieItems": {"false"},
	}, &page)
	if err != nil {
		return nil, err
	}

	out := make([]ActivityItem, 0, len(page.Records))
	for _, r := range page.Records {
		title := r.Title
		canonical := ""
		if r.Movie != nil {
			if r.Movie.Title != "" {
				title = r.Movie.Title
			}
			canonical = arrMovieID(r.Movie.TmdbID)
		}
		out = append(out, ActivityItem{
			CanonicalID: canonical,
			Title:       title,
			Domain:      "video",
			ProviderID:  p.id,
			Stage:       arrQueueStage(r.arrQueueRecord),
			Progress:    arrQueueProgress(r.arrQueueRecord),
			Detail:      arrActivityDetail(r.arrQueueRecord),
		})
	}
	return out, nil
}

// arrActivityDetail says what a person needs to act on, which is only ever the
// bad news. A healthy download explains itself with a progress bar.
func arrActivityDetail(r arrQueueRecord) string {
	if r.ErrorMessage != "" {
		return r.ErrorMessage
	}
	if strings.EqualFold(r.TrackedDownloadStatus, "warning") {
		return "the download client reported a warning"
	}
	return ""
}

func (p *radarrProvider) queueByMovie(ctx context.Context) (map[int]arrQueueRecord, error) {
	var page radarrQueuePage
	err := p.c.get(ctx, "queue", url.Values{
		"pageSize":                 {"200"},
		"includeUnknownMovieItems": {"false"},
	}, &page)
	if err != nil {
		return nil, err
	}
	out := make(map[int]arrQueueRecord, len(page.Records))
	for _, r := range page.Records {
		if r.MovieID == 0 {
			continue
		}
		// First record wins: a film with two grabs in flight is still one film
		// downloading, and the first is the one furthest along in the queue's
		// own ordering.
		if _, seen := out[r.MovieID]; !seen {
			out[r.MovieID] = r.arrQueueRecord
		}
	}
	return out, nil
}

// --- request ---------------------------------------------------------------

// Request obtains a film.
//
// Two paths, and the difference is worth keeping: a film already in the
// library is monitored and searched, never re-added -- a second POST would be
// a 400 that reads like a broken adapter. A film that is not there is added,
// and only searched for if the caller actually asked for it.
//
// RequestOptions.Monitor is the whole switch. True means "go and get it":
// monitor it and start a search. False means "put it on the shelf": the film
// is added unmonitored and nothing is downloaded, which is what a watchlist
// add should do and what makes this call safe to exercise against a real
// library.
func (p *radarrProvider) Request(ctx context.Context, item MediaItem, opts RequestOptions) (RequestResult, error) {
	ref, err := parseArrRef(item.CanonicalID)
	if err != nil {
		return RequestResult{}, err
	}
	if ref.Scheme != "tmdb" || ref.Type != "movie" {
		return RequestResult{}, fmt.Errorf("%s can only obtain films, not %q", p.name, item.CanonicalID)
	}

	var mine []radarrMovie
	if err := p.c.get(ctx, "movie", url.Values{"tmdbId": {ref.ID}}, &mine); err != nil {
		return RequestResult{}, err
	}

	if len(mine) > 0 {
		m := mine[0]
		if m.HasFile || m.MovieFileID > 0 {
			return RequestResult{
				Accepted: true,
				Detail:   fmt.Sprintf("%q is already in %s", m.Title, p.name),
			}, nil
		}
		if opts.Monitor && !m.Monitored {
			m.Monitored = true
			if err := p.c.put(ctx, "movie/"+strconv.Itoa(m.ID), m, nil); err != nil {
				return RequestResult{}, fmt.Errorf("monitoring %q: %w", m.Title, err)
			}
		}
		if opts.Monitor {
			if err := p.c.post(ctx, "command", arrCommand{
				Name: "MoviesSearch", MovieIDs: []int{m.ID},
			}, nil); err != nil {
				return RequestResult{}, fmt.Errorf("searching for %q: %w", m.Title, err)
			}
			return RequestResult{
				Accepted: true,
				Detail:   fmt.Sprintf("%s is already tracking %q; a search has been started", p.name, m.Title),
			}, nil
		}
		return RequestResult{
			Accepted: true,
			Detail:   fmt.Sprintf("%s is already tracking %q", p.name, m.Title),
		}, nil
	}

	// Not in the library: add it.
	var found []radarrMovie
	if err := p.c.get(ctx, "movie/lookup", url.Values{"term": {"tmdb:" + ref.ID}}, &found); err != nil {
		return RequestResult{}, err
	}
	if len(found) == 0 {
		return RequestResult{}, fmt.Errorf("%s could not find tmdb id %s", p.name, ref.ID)
	}
	add := found[0]

	root, err := p.c.resolveRootFolder(ctx)
	if err != nil {
		return RequestResult{}, err
	}
	profile, err := p.c.resolveQualityProfile(ctx)
	if err != nil {
		return RequestResult{}, err
	}

	add.ID = 0
	add.RootFolderPath = root
	add.QualityProfileID = profile
	add.Monitored = opts.Monitor
	if add.MinimumAvailability == "" {
		add.MinimumAvailability = "released"
	}
	add.AddOptions = &radarrAddOptions{SearchForMovie: opts.Monitor}

	var created radarrMovie
	if err := p.c.post(ctx, "movie", add, &created); err != nil {
		var he *arrHTTPError
		if errors.As(err, &he) && he.Status == 400 {
			// Radarr's 400 body says which field it disliked, and that is the
			// only useful thing to pass on.
			return RequestResult{Accepted: false, Detail: he.Body}, nil
		}
		return RequestResult{}, err
	}

	if opts.Monitor {
		return RequestResult{
			Accepted: true,
			Detail:   fmt.Sprintf("added %q to %s and started a search", created.Title, p.name),
		}, nil
	}
	return RequestResult{
		Accepted: true,
		Detail:   fmt.Sprintf("added %q to %s, unmonitored — nothing will be downloaded", created.Title, p.name),
	}, nil
}

// deleteMovie removes a row this adapter added. It exists so the request path
// can be exercised against a real instance and then put back exactly as it
// was: files are never touched and no import-list exclusion is written, so a
// film someone genuinely wants later is not silently blocked.
func (p *radarrProvider) deleteMovie(ctx context.Context, radarrID int) error {
	return p.c.delete(ctx, "movie/"+strconv.Itoa(radarrID), url.Values{
		"deleteFiles":            {"false"},
		"addImportListExclusion": {"false"},
	})
}
