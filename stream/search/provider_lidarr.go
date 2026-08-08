package main

// Lidarr: music, at three depths -- artist, album, track.
//
// The same shape as Sonarr with different nouns, and one real difference: a
// track is not separately obtainable. Lidarr acquires releases, and a release
// is an album; asking for a single track is asking for the album it is on.
// The adapter says so plainly rather than accepting a request it cannot honour
// and leaving the user waiting for a download that will never start.
//
// The other difference is that Lidarr's lookup tells you nothing about your
// own library -- no id field comes back at all -- so library state for search
// hits costs one bounded round trip each. That is why arrSearchStateLimit
// exists: the leading results get a real answer and the tail is honestly
// StateUnknown, rather than everything being confidently reported missing.

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

// Lidarr has been on /api/v1 since 0.7; 1.0 is the first release this adapter
// was written against.
const lidarrMinMajor = 1

type lidarrStatistics struct {
	AlbumCount      int     `json:"albumCount"`
	TrackFileCount  int     `json:"trackFileCount"`
	TrackCount      int     `json:"trackCount"`
	TotalTrackCount int     `json:"totalTrackCount"`
	SizeOnDisk      int64   `json:"sizeOnDisk"`
	PercentOfTracks float64 `json:"percentOfTracks"`
}

type lidarrArtist struct {
	ID         int    `json:"id"`
	ArtistName string `json:"artistName"`
	SortName   string `json:"sortName,omitempty"`
	Overview   string `json:"overview,omitempty"`
	ArtistType string `json:"artistType,omitempty"`
	Status     string `json:"status,omitempty"`
	Ended      bool   `json:"ended,omitempty"`

	ForeignArtistID string `json:"foreignArtistId"`

	Monitored  bool              `json:"monitored"`
	Statistics *lidarrStatistics `json:"statistics,omitempty"`
	Images     []arrImage        `json:"images,omitempty"`

	QualityProfileID  int    `json:"qualityProfileId,omitempty"`
	MetadataProfileID int    `json:"metadataProfileId,omitempty"`
	RootFolderPath    string `json:"rootFolderPath,omitempty"`
	Path              string `json:"path,omitempty"`
	MonitorNewItems   string `json:"monitorNewItems,omitempty"`

	AddOptions *lidarrArtistAddOptions `json:"addOptions,omitempty"`
}

type lidarrArtistAddOptions struct {
	Monitor                string `json:"monitor,omitempty"`
	Monitored              bool   `json:"monitored"`
	SearchForMissingAlbums bool   `json:"searchForMissingAlbums"`
}

type lidarrAlbum struct {
	ID             int    `json:"id"`
	Title          string `json:"title"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Overview       string `json:"overview,omitempty"`
	AlbumType      string `json:"albumType,omitempty"`
	ReleaseDate    string `json:"releaseDate,omitempty"`

	ForeignAlbumID string `json:"foreignAlbumId"`
	ArtistID       int    `json:"artistId,omitempty"`

	Monitored    bool              `json:"monitored"`
	AnyReleaseOk bool              `json:"anyReleaseOk,omitempty"`
	Statistics   *lidarrStatistics `json:"statistics,omitempty"`
	Images       []arrImage        `json:"images,omitempty"`
	RemoteCover  string            `json:"remoteCover,omitempty"`

	Artist *lidarrArtist `json:"artist,omitempty"`
}

type lidarrTrack struct {
	ID                  int    `json:"id"`
	Title               string `json:"title"`
	TrackNumber         string `json:"trackNumber,omitempty"`
	AbsoluteTrackNumber int    `json:"absoluteTrackNumber,omitempty"`
	MediumNumber        int    `json:"mediumNumber,omitempty"`
	Duration            int    `json:"duration,omitempty"`
	HasFile             bool   `json:"hasFile"`
	ForeignTrackID      string `json:"foreignTrackId"`
	AlbumID             int    `json:"albumId,omitempty"`
	ArtistID            int    `json:"artistId,omitempty"`
}

type lidarrQueueRecord struct {
	arrQueueRecord
	Artist *lidarrArtist `json:"artist,omitempty"`
	Album  *lidarrAlbum  `json:"album,omitempty"`
}

type lidarrQueuePage struct {
	TotalRecords int                 `json:"totalRecords"`
	Records      []lidarrQueueRecord `json:"records"`
}

type lidarrProvider struct {
	c    *arrClient
	id   string
	name string
}

func newLidarrProvider(cfg arrConfig) *lidarrProvider {
	id, name := cfg.ID, cfg.Name
	if id == "" {
		id = "lidarr"
	}
	if name == "" {
		name = "Lidarr"
	}
	return &lidarrProvider{
		c:    newArrClient(cfg, "v1", "Lidarr", lidarrMinMajor),
		id:   id,
		name: name,
	}
}

func (p *lidarrProvider) ID() string   { return p.id }
func (p *lidarrProvider) Name() string { return p.name }

// "music" is the canonical id. Cards cached before this vocabulary carry
// "audio", and canonicalDomain resolves both -- but what a provider *claims*
// must be the canonical spelling, and the Registry enforces it.
func (p *lidarrProvider) Domains() []string { return []string{"music"} }

func (p *lidarrProvider) Roles() []string {
	return []string{"discovery", "acquisition", "library"}
}

func (p *lidarrProvider) Capabilities() []string {
	return []string{"health", "search", "details", "library", "libraryStatus", "request", "activity"}
}

func (p *lidarrProvider) Health(ctx context.Context) Health { return p.c.health(ctx) }

// --- search ----------------------------------------------------------------

// Search asks both of Lidarr's lookups, because "kid a" is an album and
// "radiohead" is an artist and the user does not announce which they meant.
// One failing does not take the other down: half an answer beats none, and the
// two endpoints fail independently.
func (p *lidarrProvider) Search(ctx context.Context, query, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "music") {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	var (
		artists []lidarrArtist
		albums  []lidarrAlbum
		aErr    error
		bErr    error
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		aErr = p.c.get(ctx, "artist/lookup", url.Values{"term": {query}}, &artists)
	}()
	go func() {
		defer wg.Done()
		bErr = p.c.get(ctx, "album/lookup", url.Values{"term": {query}}, &albums)
	}()
	wg.Wait()

	if aErr != nil && bErr != nil {
		return nil, aErr
	}

	queue, qerr := p.queueIndex(ctx)
	if qerr != nil {
		queue = lidarrQueueIndex{}
	}

	out := make([]MediaItem, 0, len(artists)+len(albums))

	// Lidarr's lookups return nothing about ownership, so each leading hit is
	// checked against the library. Bounded on both count and concurrency:
	// unbounded, a search for a common word would be dozens of simultaneous
	// calls at an instance that is also trying to import.
	resolvedArtists := make([]*lidarrArtist, len(artists))
	arrBoundedMap(len(artists), arrSearchStateLimit, func(i int) {
		mine, ok, err := p.libraryArtist(ctx, artists[i].ForeignArtistID)
		if err != nil || !ok {
			return
		}
		resolvedArtists[i] = &mine
	})
	for i, a := range artists {
		if resolvedArtists[i] != nil {
			a = *resolvedArtists[i]
		}
		out = append(out, p.artistItem(a, queue))
	}

	resolvedAlbums := make([]*lidarrAlbum, len(albums))
	arrBoundedMap(len(albums), arrSearchStateLimit, func(i int) {
		mine, ok, err := p.libraryAlbum(ctx, albums[i].ForeignAlbumID)
		if err != nil || !ok {
			return
		}
		resolvedAlbums[i] = &mine
	})
	for i, al := range albums {
		artistName := ""
		if al.Artist != nil {
			artistName = al.Artist.ArtistName
		}
		if resolvedAlbums[i] != nil {
			mine := *resolvedAlbums[i]
			// The library copy has the statistics but the lookup copy has the
			// artist attached, and a card with no artist name is unreadable.
			if mine.Artist == nil {
				mine.Artist = al.Artist
			}
			al = mine
		}
		out = append(out, p.albumItem(al, artistName, queue))
	}
	return out, nil
}

func (p *lidarrProvider) artistItem(a lidarrArtist, queue lidarrQueueIndex) MediaItem {
	inLibrary := a.ID > 0
	files := 0
	if a.Statistics != nil {
		files = a.Statistics.TrackFileCount
	}
	var queued *arrQueueRecord
	if inLibrary {
		if recs := queue.byArtist[a.ID]; len(recs) > 0 {
			queued = &recs[0]
		}
	}
	subtitle := a.ArtistType
	if inLibrary && a.Statistics != nil && a.Statistics.TotalTrackCount > 0 {
		subtitle = fmt.Sprintf("%d of %d tracks", a.Statistics.TrackFileCount, a.Statistics.TotalTrackCount)
	}
	return MediaItem{
		CanonicalID:    arrMusicID("artist", a.ForeignArtistID),
		Domain:         "music",
		Type:           "artist",
		Title:          a.ArtistName,
		Subtitle:       subtitle,
		Overview:       a.Overview,
		Artwork:        arrPoster(a.Images),
		ProviderID:     p.id,
		ProviderItemID: strconv.Itoa(a.ID),
		State:          arrContainerState(inLibrary, files, a.Monitored, queued),
	}
}

func (p *lidarrProvider) albumItem(al lidarrAlbum, artistName string, queue lidarrQueueIndex) MediaItem {
	inLibrary := al.ID > 0
	files := 0
	if al.Statistics != nil {
		files = al.Statistics.TrackFileCount
	}
	var queued *arrQueueRecord
	if inLibrary {
		if recs := queue.byAlbum[al.ID]; len(recs) > 0 {
			queued = &recs[0]
		}
	}
	if artistName == "" && al.Artist != nil {
		artistName = al.Artist.ArtistName
	}
	art := arrPoster(al.Images)
	if art == "" {
		art = al.RemoteCover
	}
	return MediaItem{
		CanonicalID:    arrMusicID("album", al.ForeignAlbumID),
		Domain:         "music",
		Type:           "album",
		Title:          al.Title,
		Subtitle:       artistName,
		Year:           arrYear(al.ReleaseDate),
		Overview:       al.Overview,
		Artwork:        art,
		ProviderID:     p.id,
		ProviderItemID: strconv.Itoa(al.ID),
		State:          arrContainerState(inLibrary, files, al.Monitored, queued),
	}
}

func (p *lidarrProvider) trackItem(t lidarrTrack, al lidarrAlbum, artistName string) MediaItem {
	return MediaItem{
		CanonicalID:    arrMusicID("track", t.ForeignTrackID),
		Domain:         "music",
		Type:           "track",
		Title:          t.Title,
		Subtitle:       strings.TrimSpace(artistName + " — " + al.Title),
		Year:           arrYear(al.ReleaseDate),
		Artwork:        arrPoster(al.Images),
		ProviderID:     p.id,
		ProviderItemID: strconv.Itoa(t.ID),
		// A track is on disk or it is not; there is no queue row for one.
		State: arrLeafState(t.ID > 0, t.HasFile, al.Monitored, nil),
	}
}

// --- library ---------------------------------------------------------------

// Library lists artists. Albums and tracks hang off Details, for the same
// reason Sonarr lists series: 825 artists is a shelf, and the 8,000 albums
// underneath them are not.
func (p *lidarrProvider) Library(ctx context.Context, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "music") {
		return nil, nil
	}
	var artists []lidarrArtist
	if err := p.c.get(ctx, "artist", nil, &artists); err != nil {
		return nil, err
	}
	queue, qerr := p.queueIndex(ctx)
	if qerr != nil {
		queue = lidarrQueueIndex{}
	}
	out := make([]MediaItem, 0, len(artists))
	for _, a := range artists {
		out = append(out, p.artistItem(a, queue))
	}
	return out, nil
}

func (p *lidarrProvider) LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error) {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return StateUnknown, err
	}
	if ref.Scheme != "mbid" {
		return StateUnknown, nil
	}
	queue, qerr := p.queueIndex(ctx)
	if qerr != nil {
		queue = lidarrQueueIndex{}
	}

	switch ref.Type {
	case "artist":
		a, ok, err := p.libraryArtist(ctx, ref.ID)
		if err != nil {
			return StateUnknown, err
		}
		if !ok {
			return StateMissing, nil
		}
		return p.artistItem(a, queue).State, nil

	case "album":
		al, ok, err := p.libraryAlbum(ctx, ref.ID)
		if err != nil {
			return StateUnknown, err
		}
		if !ok {
			return StateMissing, nil
		}
		return p.albumItem(al, "", queue).State, nil

	case "track":
		// Lidarr will not look a track up by its MusicBrainz id -- there is no
		// endpoint for it -- so the only honest answer is that we do not know.
		// Returning "missing" here would be a guess dressed as a fact, and the
		// UI would offer a request for a track already on disk. A caller that
		// needs the real answer asks Details for the album and reads the track
		// rows out of it, which is where they actually live.
		return StateUnknown, nil
	}
	return StateUnknown, nil
}

func (p *lidarrProvider) libraryArtist(ctx context.Context, mbid string) (lidarrArtist, bool, error) {
	var mine []lidarrArtist
	if err := p.c.get(ctx, "artist", url.Values{"mbId": {mbid}}, &mine); err != nil {
		return lidarrArtist{}, false, err
	}
	if len(mine) == 0 {
		return lidarrArtist{}, false, nil
	}
	return mine[0], true, nil
}

func (p *lidarrProvider) libraryAlbum(ctx context.Context, mbid string) (lidarrAlbum, bool, error) {
	var mine []lidarrAlbum
	if err := p.c.get(ctx, "album", url.Values{"foreignAlbumId": {mbid}}, &mine); err != nil {
		return lidarrAlbum{}, false, err
	}
	if len(mine) == 0 {
		return lidarrAlbum{}, false, nil
	}
	return mine[0], true, nil
}

// --- details ---------------------------------------------------------------

func (p *lidarrProvider) Details(ctx context.Context, canonicalID string) (MediaItem, []MediaItem, error) {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return MediaItem{}, nil, err
	}
	if ref.Scheme != "mbid" {
		return MediaItem{}, nil, fmt.Errorf("%s cannot describe %q", p.name, canonicalID)
	}
	queue, qerr := p.queueIndex(ctx)
	if qerr != nil {
		queue = lidarrQueueIndex{}
	}

	switch ref.Type {
	case "artist":
		a, inLibrary, err := p.libraryArtist(ctx, ref.ID)
		if err != nil {
			return MediaItem{}, nil, err
		}
		if !inLibrary {
			var found []lidarrArtist
			if err := p.c.get(ctx, "artist/lookup", url.Values{"term": {"lidarr:" + ref.ID}}, &found); err != nil {
				return MediaItem{}, nil, err
			}
			if len(found) == 0 {
				return MediaItem{}, nil, fmt.Errorf("%s knows nothing about %s", p.name, canonicalID)
			}
			// No album rows exist for an artist that is not in the library.
			return p.artistItem(found[0], queue), nil, nil
		}
		var albums []lidarrAlbum
		if err := p.c.get(ctx, "album", url.Values{"artistId": {strconv.Itoa(a.ID)}}, &albums); err != nil {
			return MediaItem{}, nil, err
		}
		sort.Slice(albums, func(i, j int) bool { return albums[i].ReleaseDate < albums[j].ReleaseDate })
		children := make([]MediaItem, 0, len(albums))
		for _, al := range albums {
			children = append(children, p.albumItem(al, a.ArtistName, queue))
		}
		return p.artistItem(a, queue), children, nil

	case "album":
		al, inLibrary, err := p.libraryAlbum(ctx, ref.ID)
		if err != nil {
			return MediaItem{}, nil, err
		}
		if !inLibrary {
			var found []lidarrAlbum
			if err := p.c.get(ctx, "album/lookup", url.Values{"term": {"lidarr:" + ref.ID}}, &found); err != nil {
				return MediaItem{}, nil, err
			}
			if len(found) == 0 {
				return MediaItem{}, nil, fmt.Errorf("%s knows nothing about %s", p.name, canonicalID)
			}
			return p.albumItem(found[0], "", queue), nil, nil
		}
		artistName := ""
		if al.Artist != nil {
			artistName = al.Artist.ArtistName
		}
		var tracks []lidarrTrack
		if err := p.c.get(ctx, "track", url.Values{"albumId": {strconv.Itoa(al.ID)}}, &tracks); err != nil {
			return MediaItem{}, nil, err
		}
		sort.Slice(tracks, func(i, j int) bool {
			if tracks[i].MediumNumber != tracks[j].MediumNumber {
				return tracks[i].MediumNumber < tracks[j].MediumNumber
			}
			return tracks[i].AbsoluteTrackNumber < tracks[j].AbsoluteTrackNumber
		})
		children := make([]MediaItem, 0, len(tracks))
		for _, t := range tracks {
			children = append(children, p.trackItem(t, al, artistName))
		}
		return p.albumItem(al, artistName, queue), children, nil
	}
	return MediaItem{}, nil, fmt.Errorf("%s cannot describe a %s on its own", p.name, ref.Type)
}

// --- activity --------------------------------------------------------------

type lidarrQueueIndex struct {
	byArtist map[int][]arrQueueRecord
	byAlbum  map[int][]arrQueueRecord
}

func (p *lidarrProvider) queueIndex(ctx context.Context) (lidarrQueueIndex, error) {
	var page lidarrQueuePage
	err := p.c.get(ctx, "queue", url.Values{
		"pageSize":                  {"200"},
		"includeUnknownArtistItems": {"false"},
	}, &page)
	idx := lidarrQueueIndex{
		byArtist: map[int][]arrQueueRecord{},
		byAlbum:  map[int][]arrQueueRecord{},
	}
	if err != nil {
		return idx, err
	}
	for _, r := range page.Records {
		if r.ArtistID != 0 {
			idx.byArtist[r.ArtistID] = append(idx.byArtist[r.ArtistID], r.arrQueueRecord)
		}
		if r.AlbumID != 0 {
			idx.byAlbum[r.AlbumID] = append(idx.byAlbum[r.AlbumID], r.arrQueueRecord)
		}
	}
	return idx, nil
}

func (p *lidarrProvider) Activity(ctx context.Context) ([]ActivityItem, error) {
	var page lidarrQueuePage
	err := p.c.get(ctx, "queue", url.Values{
		"pageSize":                  {"200"},
		"includeArtist":             {"true"},
		"includeAlbum":              {"true"},
		"includeUnknownArtistItems": {"false"},
	}, &page)
	if err != nil {
		return nil, err
	}
	out := make([]ActivityItem, 0, len(page.Records))
	for _, r := range page.Records {
		title := r.Title
		canonical := ""
		switch {
		case r.Album != nil:
			title = r.Album.Title
			if r.Artist != nil {
				title = r.Artist.ArtistName + " — " + r.Album.Title
			}
			canonical = arrMusicID("album", r.Album.ForeignAlbumID)
		case r.Artist != nil:
			title = r.Artist.ArtistName
			canonical = arrMusicID("artist", r.Artist.ForeignArtistID)
		}
		out = append(out, ActivityItem{
			CanonicalID: canonical,
			Title:       title,
			Domain:      "music",
			ProviderID:  p.id,
			Stage:       arrQueueStage(r.arrQueueRecord),
			Progress:    arrQueueProgress(r.arrQueueRecord),
			Detail:      arrActivityDetail(r.arrQueueRecord),
		})
	}
	return out, nil
}

// --- request ---------------------------------------------------------------

// Request obtains an artist's discography or one album.
//
// A track request is refused with an explanation rather than quietly widened
// to its album: silently downloading eleven tracks somebody did not ask for is
// worse than saying no, and the caller has the album's id in hand from
// Details.
func (p *lidarrProvider) Request(ctx context.Context, item MediaItem, opts RequestOptions) (RequestResult, error) {
	ref, err := parseArrRef(item.CanonicalID)
	if err != nil {
		return RequestResult{}, err
	}
	if ref.Scheme != "mbid" {
		return RequestResult{}, fmt.Errorf("%s can only obtain music, not %q", p.name, item.CanonicalID)
	}

	switch ref.Type {
	case "artist":
		return p.requestArtist(ctx, ref.ID, opts)
	case "album":
		return p.requestAlbum(ctx, ref.ID, opts)
	case "track":
		return RequestResult{
			Accepted: false,
			Detail:   "Lidarr acquires albums, not single tracks — request the album this track is on",
		}, nil
	}
	return RequestResult{}, fmt.Errorf("%s cannot obtain a %s", p.name, ref.Type)
}

func (p *lidarrProvider) requestArtist(ctx context.Context, mbid string, opts RequestOptions) (RequestResult, error) {
	a, inLibrary, err := p.ensureArtist(ctx, mbid, opts.Monitor)
	if err != nil {
		var he *arrHTTPError
		if errors.As(err, &he) && he.Status == 400 {
			return RequestResult{Accepted: false, Detail: he.Body}, nil
		}
		return RequestResult{}, err
	}
	// A discography already fully on disk needs no search, and calling it
	// "requested" when the user already owns it is worse than saying nothing.
	if inLibrary && a.Statistics != nil && a.Statistics.TrackFileCount > 0 &&
		a.Statistics.TrackFileCount >= a.Statistics.TotalTrackCount {
		return RequestResult{
			Accepted: true,
			Detail:   fmt.Sprintf("everything %s has of %q is already there", p.name, a.ArtistName),
		}, nil
	}
	if !opts.Monitor {
		return RequestResult{
			Accepted: true,
			Detail: fmt.Sprintf("%q is on the shelf in %s, unmonitored — nothing will be downloaded",
				a.ArtistName, p.name),
		}, nil
	}
	if inLibrary && !a.Monitored {
		a.Monitored = true
		if err := p.c.put(ctx, "artist/"+strconv.Itoa(a.ID), a, nil); err != nil {
			return RequestResult{}, fmt.Errorf("monitoring %q: %w", a.ArtistName, err)
		}
	}
	if err := p.c.post(ctx, "command", arrCommand{Name: "ArtistSearch", ArtistID: a.ID}, nil); err != nil {
		return RequestResult{}, fmt.Errorf("searching for %q: %w", a.ArtistName, err)
	}
	return RequestResult{
		Accepted: true,
		Detail:   fmt.Sprintf("%s is looking for %q", p.name, a.ArtistName),
	}, nil
}

func (p *lidarrProvider) requestAlbum(ctx context.Context, mbid string, opts RequestOptions) (RequestResult, error) {
	al, inLibrary, err := p.libraryAlbum(ctx, mbid)
	if err != nil {
		return RequestResult{}, err
	}

	if !inLibrary {
		// The album row only exists once its artist does, so the artist is
		// added first -- unmonitored, so adding an artist for one album does
		// not start a discography-wide grab.
		var found []lidarrAlbum
		if err := p.c.get(ctx, "album/lookup", url.Values{"term": {"lidarr:" + mbid}}, &found); err != nil {
			return RequestResult{}, err
		}
		if len(found) == 0 || found[0].Artist == nil {
			return RequestResult{}, fmt.Errorf("%s could not find album %s", p.name, mbid)
		}
		if _, _, err := p.ensureArtist(ctx, found[0].Artist.ForeignArtistID, false); err != nil {
			var he *arrHTTPError
			if errors.As(err, &he) && he.Status == 400 {
				return RequestResult{Accepted: false, Detail: he.Body}, nil
			}
			return RequestResult{}, err
		}
		al, inLibrary, err = p.libraryAlbum(ctx, mbid)
		if err != nil {
			return RequestResult{}, err
		}
		if !inLibrary {
			return RequestResult{
				Accepted: false,
				Detail: fmt.Sprintf("%s added the artist but has no row for album %s — "+
					"its metadata profile may be filtering this release out", p.name, mbid),
			}, nil
		}
	}

	if al.Statistics != nil && al.Statistics.TrackFileCount > 0 &&
		al.Statistics.TrackFileCount >= al.Statistics.TotalTrackCount {
		return RequestResult{
			Accepted: true,
			Detail:   fmt.Sprintf("%q is already in %s", al.Title, p.name),
		}, nil
	}
	if !opts.Monitor {
		return RequestResult{
			Accepted: true,
			Detail: fmt.Sprintf("%q is on the shelf in %s, unmonitored — nothing will be downloaded",
				al.Title, p.name),
		}, nil
	}
	if !al.Monitored {
		al.Monitored = true
		if err := p.c.put(ctx, "album/"+strconv.Itoa(al.ID), al, nil); err != nil {
			return RequestResult{}, fmt.Errorf("monitoring %q: %w", al.Title, err)
		}
	}
	if err := p.c.post(ctx, "command", arrCommand{
		Name: "AlbumSearch", AlbumIDs: []int{al.ID},
	}, nil); err != nil {
		return RequestResult{}, fmt.Errorf("searching for %q: %w", al.Title, err)
	}
	return RequestResult{
		Accepted: true,
		Detail:   fmt.Sprintf("%s is looking for %q", p.name, al.Title),
	}, nil
}

// ensureArtist returns the library row for an artist, adding it if needed. The
// bool reports whether it was already there, which is the difference between
// "we just created this" and "leave their settings alone".
func (p *lidarrProvider) ensureArtist(ctx context.Context, mbid string, monitor bool) (lidarrArtist, bool, error) {
	if a, ok, err := p.libraryArtist(ctx, mbid); err != nil || ok {
		return a, ok, err
	}

	var found []lidarrArtist
	if err := p.c.get(ctx, "artist/lookup", url.Values{"term": {"lidarr:" + mbid}}, &found); err != nil {
		return lidarrArtist{}, false, err
	}
	if len(found) == 0 {
		return lidarrArtist{}, false, fmt.Errorf("%s could not find artist %s", p.name, mbid)
	}
	add := found[0]

	root, err := p.c.resolveRootFolder(ctx)
	if err != nil {
		return lidarrArtist{}, false, err
	}
	quality, err := p.c.resolveQualityProfile(ctx)
	if err != nil {
		return lidarrArtist{}, false, err
	}
	metadata, err := p.c.resolveMetadataProfile(ctx)
	if err != nil {
		return lidarrArtist{}, false, err
	}

	add.ID = 0
	add.RootFolderPath = root
	add.QualityProfileID = quality
	add.MetadataProfileID = metadata
	add.Monitored = monitor
	add.AddOptions = &lidarrArtistAddOptions{
		Monitor:                monitorOptionFor(monitor),
		Monitored:              monitor,
		SearchForMissingAlbums: false, // The search is fired explicitly, if at all.
	}

	var created lidarrArtist
	if err := p.c.post(ctx, "artist", add, &created); err != nil {
		return lidarrArtist{}, false, err
	}
	return created, false, nil
}

func monitorOptionFor(monitor bool) string {
	if monitor {
		return "all"
	}
	return "none"
}

// deleteArtist removes a row this adapter added, leaving files and
// import-list exclusions untouched, so the request path can be proven against
// a real instance and put back exactly as it was.
func (p *lidarrProvider) deleteArtist(ctx context.Context, lidarrID int) error {
	return p.c.delete(ctx, "artist/"+strconv.Itoa(lidarrID), url.Values{
		"deleteFiles":            {"false"},
		"addImportListExclusion": {"false"},
	})
}
