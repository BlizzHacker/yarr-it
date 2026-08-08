package main

// Sonarr: television, at three depths.
//
// This is the adapter where the granularity actually matters. A film is one
// thing you either have or do not; a show is a series that contains seasons
// that contain episodes, and every one of those three is something a person
// asks for by name. "Add the whole show", "get me season 2", "I'm missing the
// finale" are three different requests, and an adapter that only understands
// the first turns the other two into a full-series grab nobody wanted.
//
// The anime case is handled by carrying Sonarr's own seriesType through onto
// schema.json's "anime" type rather than flattening it into "series". A second
// Sonarr instance dedicated to anime is a normal setup -- absolute numbering
// and a different root folder -- and the Registry allows it because the id
// differs, not because the domain does. Both instances serve "video".

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Sonarr 3.0 is the release that shipped /api/v3.
const sonarrMinMajor = 3

type sonarrSeasonStats struct {
	EpisodeFileCount  int     `json:"episodeFileCount"`
	EpisodeCount      int     `json:"episodeCount"`
	TotalEpisodeCount int     `json:"totalEpisodeCount"`
	SizeOnDisk        int64   `json:"sizeOnDisk"`
	PercentOfEpisodes float64 `json:"percentOfEpisodes"`
}

type sonarrSeason struct {
	SeasonNumber int                `json:"seasonNumber"`
	Monitored    bool               `json:"monitored"`
	Statistics   *sonarrSeasonStats `json:"statistics,omitempty"`
}

type sonarrSeries struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	SortTitle string `json:"sortTitle,omitempty"`
	Year      int    `json:"year"`
	Overview  string `json:"overview,omitempty"`
	Network   string `json:"network,omitempty"`
	Status    string `json:"status,omitempty"`
	Ended     bool   `json:"ended,omitempty"`

	TvdbID int    `json:"tvdbId"`
	ImdbID string `json:"imdbId,omitempty"`

	Monitored  bool   `json:"monitored"`
	SeriesType string `json:"seriesType,omitempty"`
	TitleSlug  string `json:"titleSlug,omitempty"`

	Seasons    []sonarrSeason     `json:"seasons,omitempty"`
	Statistics *sonarrSeasonStats `json:"statistics,omitempty"`

	Images       []arrImage `json:"images,omitempty"`
	RemotePoster string     `json:"remotePoster,omitempty"`

	QualityProfileID int    `json:"qualityProfileId,omitempty"`
	RootFolderPath   string `json:"rootFolderPath,omitempty"`
	Path             string `json:"path,omitempty"`
	SeasonFolder     bool   `json:"seasonFolder,omitempty"`

	AddOptions *sonarrAddOptions `json:"addOptions,omitempty"`
}

// sonarrAddOptions is Sonarr's monitor vocabulary, and it is the reason a
// season request does not become a full-series grab: the series is added with
// monitor "none" and exactly the wanted season is switched on afterwards.
type sonarrAddOptions struct {
	Monitor                      string `json:"monitor,omitempty"`
	SearchForMissingEpisodes     bool   `json:"searchForMissingEpisodes"`
	SearchForCutoffUnmetEpisodes bool   `json:"searchForCutoffUnmetEpisodes"`
	IgnoreEpisodesWithFiles      bool   `json:"ignoreEpisodesWithFiles"`
	IgnoreEpisodesWithoutFiles   bool   `json:"ignoreEpisodesWithoutFiles"`
}

type sonarrEpisode struct {
	ID            int    `json:"id"`
	SeriesID      int    `json:"seriesId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	Title         string `json:"title"`
	Overview      string `json:"overview,omitempty"`
	AirDateUtc    string `json:"airDateUtc,omitempty"`
	HasFile       bool   `json:"hasFile"`
	Monitored     bool   `json:"monitored"`
	EpisodeFileID int    `json:"episodeFileId,omitempty"`
}

type sonarrQueueRecord struct {
	arrQueueRecord
	Series  *sonarrSeries  `json:"series,omitempty"`
	Episode *sonarrEpisode `json:"episode,omitempty"`
}

type sonarrQueuePage struct {
	TotalRecords int                 `json:"totalRecords"`
	Records      []sonarrQueueRecord `json:"records"`
}

type sonarrProvider struct {
	c          *arrClient
	id         string
	name       string
	seriesType string // forced type for new shows; empty means "trust Sonarr"
}

func newSonarrProvider(cfg arrConfig) *sonarrProvider {
	id, name := cfg.ID, cfg.Name
	if id == "" {
		id = "sonarr"
	}
	if name == "" {
		name = "Sonarr"
	}
	return &sonarrProvider{
		c:          newArrClient(cfg, "v3", "Sonarr", sonarrMinMajor),
		id:         id,
		name:       name,
		seriesType: strings.ToLower(strings.TrimSpace(cfg.SeriesType)),
	}
}

func (p *sonarrProvider) ID() string   { return p.id }
func (p *sonarrProvider) Name() string { return p.name }

// One domain, whatever the instance is for. An anime Sonarr is still video;
// "anime" is a type inside that domain, not a domain of its own -- which is
// exactly what schema.json says and why there is no second vocabulary here.
func (p *sonarrProvider) Domains() []string { return []string{"video"} }

func (p *sonarrProvider) Roles() []string {
	return []string{"discovery", "acquisition", "library"}
}

func (p *sonarrProvider) Capabilities() []string {
	return []string{"health", "search", "details", "library", "libraryStatus", "request", "activity"}
}

func (p *sonarrProvider) Health(ctx context.Context) Health { return p.c.health(ctx) }

// --- search ----------------------------------------------------------------

// Search returns series. Seasons and episodes are reached through Details:
// a search for "firefly" that returned fourteen episodes and one series would
// bury the thing the user actually meant.
//
// Sonarr's lookup answers with zeroed statistics even for shows already in the
// library, so the state of in-library hits is resolved with a second, bounded
// pass. Reporting every hit as missing would offer a request for a show that
// is already fully downloaded.
func (p *sonarrProvider) Search(ctx context.Context, query, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "video") {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	var found []sonarrSeries
	if err := p.c.get(ctx, "series/lookup", url.Values{"term": {query}}, &found); err != nil {
		return nil, err
	}

	queue, qerr := p.queueBySeries(ctx)
	if qerr != nil {
		queue = nil
	}

	// Fill in the real statistics for the hits Sonarr already has. Only those:
	// a show that is not in the library has nothing to look up.
	resolved := make([]sonarrSeries, len(found))
	copy(resolved, found)
	var mu sync.Mutex
	arrBoundedMap(len(resolved), arrSearchStateLimit, func(i int) {
		if resolved[i].ID == 0 {
			return
		}
		var full sonarrSeries
		if err := p.c.get(ctx, "series/"+strconv.Itoa(resolved[i].ID), nil, &full); err != nil {
			return // Keep the lookup copy; state stays conservative.
		}
		mu.Lock()
		resolved[i] = full
		mu.Unlock()
	})

	out := make([]MediaItem, 0, len(resolved))
	for _, s := range resolved {
		out = append(out, p.seriesItem(s, queue))
	}
	return out, nil
}

// seriesType maps Sonarr's word onto a canonical one. Sonarr's third value,
// "daily", is a scheduling hint about a standard show, not a different kind of
// thing, so it lands on "series" -- inventing a type for it would put a word
// in the vocabulary that no client knows how to render.
func (p *sonarrProvider) canonicalType(s sonarrSeries) string {
	return arrSeriesType(strings.EqualFold(s.SeriesType, "anime"))
}

func (p *sonarrProvider) seriesItem(s sonarrSeries, queue map[int][]arrQueueRecord) MediaItem {
	inLibrary := s.ID > 0
	files := 0
	if s.Statistics != nil {
		files = s.Statistics.EpisodeFileCount
	}
	var queued *arrQueueRecord
	if inLibrary && queue != nil {
		if recs := queue[s.ID]; len(recs) > 0 {
			queued = &recs[0]
		}
	}

	art := arrPoster(s.Images)
	if art == "" {
		art = s.RemotePoster
	}
	subtitle := s.Network
	if s.Statistics != nil && s.Statistics.TotalEpisodeCount > 0 && inLibrary {
		subtitle = fmt.Sprintf("%d of %d episodes", s.Statistics.EpisodeFileCount, s.Statistics.TotalEpisodeCount)
	}

	return MediaItem{
		CanonicalID:    arrSeriesID(s.TvdbID, strings.EqualFold(s.SeriesType, "anime")),
		Domain:         "video",
		Type:           p.canonicalType(s),
		Title:          s.Title,
		Subtitle:       subtitle,
		Year:           s.Year,
		Overview:       s.Overview,
		Artwork:        art,
		ProviderID:     p.id,
		ProviderItemID: strconv.Itoa(s.ID),
		State:          arrContainerState(inLibrary, files, s.Monitored, queued),
	}
}

func (p *sonarrProvider) seasonItem(s sonarrSeries, se sonarrSeason, queue map[int][]arrQueueRecord) MediaItem {
	files := 0
	total := 0
	if se.Statistics != nil {
		files = se.Statistics.EpisodeFileCount
		total = se.Statistics.TotalEpisodeCount
	}
	var queued *arrQueueRecord
	for i := range queue[s.ID] {
		if queue[s.ID][i].SeasonNumber == se.SeasonNumber {
			queued = &queue[s.ID][i]
			break
		}
	}

	title := fmt.Sprintf("Season %d", se.SeasonNumber)
	if se.SeasonNumber == 0 {
		// Sonarr's season 0 is specials, and calling it "Season 0" on a card is
		// how it gets mistaken for a data error.
		title = "Specials"
	}
	subtitle := s.Title
	if total > 0 {
		subtitle = fmt.Sprintf("%s — %d of %d episodes", s.Title, files, total)
	}

	return MediaItem{
		CanonicalID:    arrSeasonID(s.TvdbID, se.SeasonNumber),
		Domain:         "video",
		Type:           "season",
		Title:          title,
		Subtitle:       subtitle,
		Overview:       "",
		Artwork:        arrPoster(s.Images),
		ProviderID:     p.id,
		ProviderItemID: fmt.Sprintf("%d:%d", s.ID, se.SeasonNumber),
		Season:         se.SeasonNumber,
		State:          arrContainerState(s.ID > 0, files, se.Monitored, queued),
	}
}

func (p *sonarrProvider) episodeItem(s sonarrSeries, e sonarrEpisode, queue map[int][]arrQueueRecord) MediaItem {
	var queued *arrQueueRecord
	for i := range queue[s.ID] {
		if queue[s.ID][i].EpisodeID == e.ID && e.ID != 0 {
			queued = &queue[s.ID][i]
			break
		}
	}
	return MediaItem{
		CanonicalID:    arrEpisodeID(s.TvdbID, e.SeasonNumber, e.EpisodeNumber),
		Domain:         "video",
		Type:           "episode",
		Title:          e.Title,
		Subtitle:       fmt.Sprintf("%s — S%02dE%02d", s.Title, e.SeasonNumber, e.EpisodeNumber),
		Year:           arrYear(e.AirDateUtc),
		Overview:       e.Overview,
		Artwork:        arrPoster(s.Images),
		ProviderID:     p.id,
		ProviderItemID: strconv.Itoa(e.ID),
		Season:         e.SeasonNumber,
		Episode:        e.EpisodeNumber,
		State:          arrLeafState(e.ID > 0, e.HasFile || e.EpisodeFileID > 0, e.Monitored, queued),
	}
}

// --- library ---------------------------------------------------------------

func (p *sonarrProvider) Library(ctx context.Context, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "video") {
		return nil, nil
	}
	var series []sonarrSeries
	if err := p.c.get(ctx, "series", nil, &series); err != nil {
		return nil, err
	}
	queue, qerr := p.queueBySeries(ctx)
	if qerr != nil {
		queue = nil
	}
	out := make([]MediaItem, 0, len(series))
	for _, s := range series {
		out = append(out, p.seriesItem(s, queue))
	}
	return out, nil
}

// LibraryStatus answers at whichever depth the id names. This is the method
// that stops a UI offering "request" for an episode that is already on disk
// inside a series it has only ever seen as a whole.
func (p *sonarrProvider) LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error) {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return StateUnknown, err
	}
	if ref.Scheme != "tvdb" {
		return StateUnknown, nil
	}
	switch ref.Type {
	case "series", "anime", "season", "episode":
	default:
		return StateUnknown, nil
	}

	s, ok, err := p.librarySeries(ctx, ref.ID)
	if err != nil {
		return StateUnknown, err
	}
	if !ok {
		return StateMissing, nil
	}
	queue, qerr := p.queueBySeries(ctx)
	if qerr != nil {
		queue = nil
	}

	switch ref.Type {
	case "series", "anime":
		return p.seriesItem(s, queue).State, nil
	case "season":
		for _, se := range s.Seasons {
			if se.SeasonNumber == ref.Season {
				return p.seasonItem(s, se, queue).State, nil
			}
		}
		return StateMissing, nil
	default: // episode
		eps, err := p.episodes(ctx, s.ID, ref.Season)
		if err != nil {
			return StateUnknown, err
		}
		for _, e := range eps {
			if e.SeasonNumber == ref.Season && e.EpisodeNumber == ref.Episode {
				return p.episodeItem(s, e, queue).State, nil
			}
		}
		return StateMissing, nil
	}
}

// librarySeries fetches this instance's copy of a show by TVDB id.
func (p *sonarrProvider) librarySeries(ctx context.Context, tvdbID string) (sonarrSeries, bool, error) {
	var mine []sonarrSeries
	if err := p.c.get(ctx, "series", url.Values{"tvdbId": {tvdbID}}, &mine); err != nil {
		return sonarrSeries{}, false, err
	}
	if len(mine) == 0 {
		return sonarrSeries{}, false, nil
	}
	return mine[0], true, nil
}

func (p *sonarrProvider) episodes(ctx context.Context, seriesID, season int) ([]sonarrEpisode, error) {
	q := url.Values{"seriesId": {strconv.Itoa(seriesID)}}
	if season >= 0 {
		q.Set("seasonNumber", strconv.Itoa(season))
	}
	var eps []sonarrEpisode
	if err := p.c.get(ctx, "episode", q, &eps); err != nil {
		return nil, err
	}
	return eps, nil
}

// --- details ---------------------------------------------------------------

// Details walks one level down, which is what a screen actually needs: a
// series yields its seasons, a season yields its episodes, an episode yields
// itself. Returning the whole tree at once would mean a 91-episode document
// for a card that shows five rows.
func (p *sonarrProvider) Details(ctx context.Context, canonicalID string) (MediaItem, []MediaItem, error) {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return MediaItem{}, nil, err
	}
	if ref.Scheme != "tvdb" {
		return MediaItem{}, nil, fmt.Errorf("%s cannot describe %q", p.name, canonicalID)
	}

	s, inLibrary, err := p.librarySeries(ctx, ref.ID)
	if err != nil {
		return MediaItem{}, nil, err
	}
	if !inLibrary {
		// Not ours yet, so describe it from the metadata Sonarr can still
		// reach. A details screen for something you do not have is exactly the
		// screen from which you would request it.
		var found []sonarrSeries
		if err := p.c.get(ctx, "series/lookup", url.Values{"term": {"tvdb:" + ref.ID}}, &found); err != nil {
			return MediaItem{}, nil, err
		}
		if len(found) == 0 {
			return MediaItem{}, nil, fmt.Errorf("%s knows nothing about %s", p.name, canonicalID)
		}
		s = found[0]
	}

	queue, qerr := p.queueBySeries(ctx)
	if qerr != nil {
		queue = nil
	}

	switch ref.Type {
	case "series", "anime":
		seasons := make([]sonarrSeason, len(s.Seasons))
		copy(seasons, s.Seasons)
		sort.Slice(seasons, func(i, j int) bool { return seasons[i].SeasonNumber < seasons[j].SeasonNumber })
		children := make([]MediaItem, 0, len(seasons))
		for _, se := range seasons {
			children = append(children, p.seasonItem(s, se, queue))
		}
		return p.seriesItem(s, queue), children, nil

	case "season":
		var self MediaItem
		for _, se := range s.Seasons {
			if se.SeasonNumber == ref.Season {
				self = p.seasonItem(s, se, queue)
			}
		}
		if self.CanonicalID == "" {
			self = p.seasonItem(s, sonarrSeason{SeasonNumber: ref.Season}, queue)
		}
		if !inLibrary {
			// No episode rows exist until the show is added; the season itself
			// is still describable and requestable.
			return self, nil, nil
		}
		eps, err := p.episodes(ctx, s.ID, ref.Season)
		if err != nil {
			return MediaItem{}, nil, err
		}
		sort.Slice(eps, func(i, j int) bool { return eps[i].EpisodeNumber < eps[j].EpisodeNumber })
		children := make([]MediaItem, 0, len(eps))
		for _, e := range eps {
			children = append(children, p.episodeItem(s, e, queue))
		}
		return self, children, nil

	default: // episode
		if !inLibrary {
			return MediaItem{}, nil, fmt.Errorf("%s does not have %s yet, so its episodes are unknown", p.name, s.Title)
		}
		eps, err := p.episodes(ctx, s.ID, ref.Season)
		if err != nil {
			return MediaItem{}, nil, err
		}
		for _, e := range eps {
			if e.EpisodeNumber == ref.Episode {
				return p.episodeItem(s, e, queue), nil, nil
			}
		}
		return MediaItem{}, nil, fmt.Errorf("%s has no S%02dE%02d of %s", p.name, ref.Season, ref.Episode, s.Title)
	}
}

// --- activity --------------------------------------------------------------

func (p *sonarrProvider) Activity(ctx context.Context) ([]ActivityItem, error) {
	var page sonarrQueuePage
	err := p.c.get(ctx, "queue", url.Values{
		"pageSize":                  {"200"},
		"includeSeries":             {"true"},
		"includeEpisode":            {"true"},
		"includeUnknownSeriesItems": {"false"},
	}, &page)
	if err != nil {
		return nil, err
	}

	out := make([]ActivityItem, 0, len(page.Records))
	for _, r := range page.Records {
		title := r.Title
		canonical := ""
		if r.Series != nil {
			if r.Episode != nil {
				title = fmt.Sprintf("%s — S%02dE%02d %s",
					r.Series.Title, r.Episode.SeasonNumber, r.Episode.EpisodeNumber, r.Episode.Title)
				canonical = arrEpisodeID(r.Series.TvdbID, r.Episode.SeasonNumber, r.Episode.EpisodeNumber)
			} else {
				title = r.Series.Title
				canonical = arrSeriesID(r.Series.TvdbID, strings.EqualFold(r.Series.SeriesType, "anime"))
			}
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

// queueBySeries keeps every record for a series rather than the first, because
// season and episode state both have to be answered out of it.
func (p *sonarrProvider) queueBySeries(ctx context.Context) (map[int][]arrQueueRecord, error) {
	var page sonarrQueuePage
	err := p.c.get(ctx, "queue", url.Values{
		"pageSize":                  {"200"},
		"includeUnknownSeriesItems": {"false"},
	}, &page)
	if err != nil {
		return nil, err
	}
	out := make(map[int][]arrQueueRecord, len(page.Records))
	for _, r := range page.Records {
		if r.SeriesID == 0 {
			continue
		}
		out[r.SeriesID] = append(out[r.SeriesID], r.arrQueueRecord)
	}
	return out, nil
}

// --- request ---------------------------------------------------------------

// Request obtains a show, a season or a single episode.
//
// The shape is always the same three steps -- make sure the series row exists,
// monitor exactly what was asked for, then search for exactly that -- because
// Sonarr has no "add just this season" call. Adding with monitor "none" and
// switching on the wanted part afterwards is what keeps a request for one
// episode from becoming a nine-season grab.
//
// RequestOptions.Monitor false means "put it on the shelf": the row is created
// or left alone, nothing is monitored, and no search runs.
func (p *sonarrProvider) Request(ctx context.Context, item MediaItem, opts RequestOptions) (RequestResult, error) {
	ref, err := parseArrRef(item.CanonicalID)
	if err != nil {
		return RequestResult{}, err
	}
	if ref.Scheme != "tvdb" {
		return RequestResult{}, fmt.Errorf("%s can only obtain television, not %q", p.name, item.CanonicalID)
	}
	// The id is the authority on depth; opts fills in only what the id left
	// unsaid, so a "whole series" id cannot be silently narrowed by a stale
	// season number in the options.
	season, episode := ref.Season, ref.Episode
	if ref.Type == "series" || ref.Type == "anime" {
		season, episode = opts.Season, opts.Episode
	}

	s, inLibrary, err := p.librarySeries(ctx, ref.ID)
	if err != nil {
		return RequestResult{}, err
	}

	if !inLibrary {
		created, wholeShowHandled, err := p.addSeries(ctx, ref, opts, season, episode)
		if err != nil {
			var he *arrHTTPError
			if errors.As(err, &he) && he.Status == 400 {
				return RequestResult{Accepted: false, Detail: he.Body}, nil
			}
			return RequestResult{}, err
		}
		s = created
		if wholeShowHandled {
			// The add itself monitored everything and started the search, so
			// firing SeriesSearch again here would queue a second identical
			// sweep across every indexer for no benefit.
			return RequestResult{
				Accepted: true,
				Detail:   fmt.Sprintf("added %q to %s and started looking for every episode", s.Title, p.name),
			}, nil
		}
	}

	// Nothing to do for something already complete, at whichever depth was
	// asked for. Firing SeriesSearch at a show that is fully downloaded is a
	// sweep across every indexer for no benefit, and telling the user it has
	// been "requested" when they already own it is worse than saying nothing.
	if complete, what := p.alreadyComplete(s, ref, season, episode); complete {
		return RequestResult{
			Accepted: true,
			Detail:   fmt.Sprintf("%s of %q is already in %s", what, s.Title, p.name),
		}, nil
	}

	if !opts.Monitor {
		return RequestResult{
			Accepted: true,
			Detail: fmt.Sprintf("%q is on the shelf in %s, unmonitored — nothing will be downloaded",
				s.Title, p.name),
		}, nil
	}

	switch {
	case episode > 0:
		return p.requestEpisode(ctx, s, season, episode)
	case season > 0 || (season == 0 && ref.Type == "season"):
		return p.requestSeason(ctx, s, season)
	default:
		return p.requestSeries(ctx, s)
	}
}

// addSeries creates the row. The monitor option is chosen from the depth being
// requested, so the add itself does the right thing for the whole-series case
// and stays out of the way for the narrower ones.
// alreadyComplete reports whether the exact thing being requested is already
// fully on disk, and what to call it. Partial content is deliberately not
// complete: a show missing its finale is exactly what someone would request.
//
// Episodes are left to requestEpisode, which has the per-episode file flag and
// answers the same question more precisely.
func (p *sonarrProvider) alreadyComplete(s sonarrSeries, ref arrRef, season, episode int) (bool, string) {
	if s.ID == 0 || episode > 0 {
		return false, ""
	}
	full := func(st *sonarrSeasonStats) bool {
		return st != nil && st.EpisodeFileCount > 0 && st.EpisodeFileCount >= st.TotalEpisodeCount
	}
	if ref.Type == "season" {
		for _, se := range s.Seasons {
			if se.SeasonNumber == season {
				return full(se.Statistics), fmt.Sprintf("Season %d", season)
			}
		}
		return false, ""
	}
	if season > 0 {
		return false, "" // A season named through the options, handled above.
	}
	return full(s.Statistics), "every episode"
}

// The second return value reports whether the add already did everything the
// request asked for -- true only for "the whole show, monitored", where
// Sonarr's own add-time options are exactly right.
func (p *sonarrProvider) addSeries(ctx context.Context, ref arrRef, opts RequestOptions, season, episode int) (sonarrSeries, bool, error) {
	var found []sonarrSeries
	if err := p.c.get(ctx, "series/lookup", url.Values{"term": {"tvdb:" + ref.ID}}, &found); err != nil {
		return sonarrSeries{}, false, err
	}
	if len(found) == 0 {
		return sonarrSeries{}, false, fmt.Errorf("%s could not find tvdb id %s", p.name, ref.ID)
	}
	add := found[0]

	root, err := p.c.resolveRootFolder(ctx)
	if err != nil {
		return sonarrSeries{}, false, err
	}
	profile, err := p.c.resolveQualityProfile(ctx)
	if err != nil {
		return sonarrSeries{}, false, err
	}

	monitorOption := "none"
	searchMissing := false
	if opts.Monitor && season == 0 && episode == 0 {
		// The only case where Sonarr's own add-time monitoring is what was
		// asked for.
		monitorOption = "all"
		searchMissing = true
	}

	add.ID = 0
	add.RootFolderPath = root
	add.QualityProfileID = profile
	add.SeasonFolder = true
	add.Monitored = opts.Monitor
	if p.seriesType != "" {
		add.SeriesType = p.seriesType
	}
	add.AddOptions = &sonarrAddOptions{
		Monitor:                  monitorOption,
		SearchForMissingEpisodes: searchMissing,
	}
	if monitorOption == "none" {
		// Sonarr writes seasons[].monitored from the add payload, and the
		// lookup copy arrives with most of them already true. Left alone, a
		// "season 2 only" request quietly monitors every season.
		for i := range add.Seasons {
			add.Seasons[i].Monitored = false
		}
	}

	var created sonarrSeries
	if err := p.c.post(ctx, "series", add, &created); err != nil {
		return sonarrSeries{}, false, err
	}
	return created, monitorOption == "all", nil
}

func (p *sonarrProvider) requestSeries(ctx context.Context, s sonarrSeries) (RequestResult, error) {
	if !s.Monitored {
		s.Monitored = true
		for i := range s.Seasons {
			s.Seasons[i].Monitored = true
		}
		if err := p.c.put(ctx, "series/"+strconv.Itoa(s.ID), s, nil); err != nil {
			return RequestResult{}, fmt.Errorf("monitoring %q: %w", s.Title, err)
		}
	}
	if err := p.c.post(ctx, "command", arrCommand{Name: "SeriesSearch", SeriesID: s.ID}, nil); err != nil {
		return RequestResult{}, fmt.Errorf("searching for %q: %w", s.Title, err)
	}
	return RequestResult{
		Accepted: true,
		Detail:   fmt.Sprintf("%s is looking for every episode of %q", p.name, s.Title),
	}, nil
}

func (p *sonarrProvider) requestSeason(ctx context.Context, s sonarrSeries, season int) (RequestResult, error) {
	known := false
	changed := false
	for i := range s.Seasons {
		if s.Seasons[i].SeasonNumber == season {
			known = true
			if !s.Seasons[i].Monitored {
				s.Seasons[i].Monitored = true
				changed = true
			}
		}
	}
	if !known {
		return RequestResult{
			Accepted: false,
			Detail:   fmt.Sprintf("%q has no season %d", s.Title, season),
		}, nil
	}
	if changed || !s.Monitored {
		s.Monitored = true
		if err := p.c.put(ctx, "series/"+strconv.Itoa(s.ID), s, nil); err != nil {
			return RequestResult{}, fmt.Errorf("monitoring season %d of %q: %w", season, s.Title, err)
		}
	}
	n := season
	if err := p.c.post(ctx, "command", arrCommand{
		Name: "SeasonSearch", SeriesID: s.ID, SeasonNum: &n,
	}, nil); err != nil {
		return RequestResult{}, fmt.Errorf("searching season %d of %q: %w", season, s.Title, err)
	}
	return RequestResult{
		Accepted: true,
		Detail:   fmt.Sprintf("%s is looking for season %d of %q", p.name, season, s.Title),
	}, nil
}

type sonarrEpisodeMonitor struct {
	EpisodeIDs []int `json:"episodeIds"`
	Monitored  bool  `json:"monitored"`
}

func (p *sonarrProvider) requestEpisode(ctx context.Context, s sonarrSeries, season, episode int) (RequestResult, error) {
	eps, err := p.episodes(ctx, s.ID, season)
	if err != nil {
		return RequestResult{}, err
	}
	var target *sonarrEpisode
	for i := range eps {
		if eps[i].EpisodeNumber == episode {
			target = &eps[i]
			break
		}
	}
	if target == nil {
		return RequestResult{
			Accepted: false,
			Detail:   fmt.Sprintf("%q has no S%02dE%02d", s.Title, season, episode),
		}, nil
	}
	if target.HasFile {
		return RequestResult{
			Accepted: true,
			Detail:   fmt.Sprintf("S%02dE%02d of %q is already in %s", season, episode, s.Title, p.name),
		}, nil
	}
	if !target.Monitored {
		if err := p.c.put(ctx, "episode/monitor", sonarrEpisodeMonitor{
			EpisodeIDs: []int{target.ID}, Monitored: true,
		}, nil); err != nil {
			return RequestResult{}, fmt.Errorf("monitoring S%02dE%02d: %w", season, episode, err)
		}
	}
	if err := p.c.post(ctx, "command", arrCommand{
		Name: "EpisodeSearch", EpisodeIDs: []int{target.ID},
	}, nil); err != nil {
		return RequestResult{}, fmt.Errorf("searching S%02dE%02d: %w", season, episode, err)
	}
	return RequestResult{
		Accepted: true,
		Detail:   fmt.Sprintf("%s is looking for S%02dE%02d of %q", p.name, season, episode, s.Title),
	}, nil
}

// deleteSeries removes a row this adapter added, leaving files and import-list
// exclusions untouched. It exists so the request path can be proven against a
// real instance without leaving anything behind.
func (p *sonarrProvider) deleteSeries(ctx context.Context, sonarrID int) error {
	return p.c.delete(ctx, "series/"+strconv.Itoa(sonarrID), url.Values{
		"deleteFiles":            {"false"},
		"addImportListExclusion": {"false"},
	})
}
