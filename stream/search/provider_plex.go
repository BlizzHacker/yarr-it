package main

// Plex: the second video library, and the harder one to start mid-programme.
//
// Everything the Jellyfin adapter does, this one does too -- with one honest
// difference that is worth stating at the top rather than burying:
//
//	Plex can begin a stream at an arbitrary offset, and this adapter does it.
//	But the only endpoint that will is the universal transcoder, and what comes
//	back is a session-bound transcode that answers no byte ranges. So starting
//	at 20:37 into a film costs you both direct play and seeking, and this file
//	reports both as false. It is not a silent compromise: a caller told
//	Seekable=false knows not to offer a scrub bar, and a caller told
//	DirectPlay=false knows a CPU somewhere is doing work.
//
// The alternative -- hand back the direct file URL, which is genuinely direct
// play and genuinely range-seekable, and let the player seek -- was rejected.
// StreamSource has nowhere to carry "and by the way, start 2,220 seconds in", so
// a caller receiving that URL has no way to distinguish it from one that starts
// where it was asked to. Silently starting at zero is the exact failure the
// linear engine exists to prevent, so at offset zero we hand back direct play,
// and past zero we hand back something that really begins there.
//
// Two Plex quirks are load-bearing and were found the hard way against a live
// server; both are commented where they bite:
//
//   - The conversion profile is selected by X-Plex-Platform, not by the product
//     name. With Platform="Generic" every transcode decision comes back "no
//     conversion profile found for protocol http" and nothing plays -- a 200
//     response describing a total failure.
//   - copyts=1 is what makes the returned stream carry real timestamps starting
//     at the offset. Without it the content is still right, but the container
//     claims to start at zero, and anything downstream that trusts timestamps
//     is then quietly wrong.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Plex counts durations in milliseconds. Stated once, used once.
	plexMillisPerSecond = 1000

	// The oldest Plex Media Server this adapter speaks to. The universal
	// transcoder's offset parameter and the container pagination below have
	// both been stable since well before this; older than it and the honest
	// answer is "incompatible" rather than a confusing 400.
	plexMinMajor = 1
	plexMinMinor = 20

	plexPageSize         = 500
	plexDefaultPoolLimit = 10_000
	plexDefaultTimeout   = 120 * time.Second

	// What this client tells Plex it is.
	//
	// Product and device name are ours, because a server operator looking at
	// their sessions list deserves to see who is watching. Platform is NOT
	// ours, and that is not laziness: Plex picks the conversion profile from
	// the platform string, and an unknown one gets no profile at all -- the
	// transcode decision then comes back 2000 "Neither direct play nor
	// conversion is available" on a perfectly healthy file. "Chrome" selects
	// the HTML5 profile, which describes what any player Yarr.It hands a URL to
	// can actually decode.
	plexProduct  = "Yarr.It"
	plexVersion  = "1"
	plexPlatform = "Chrome"
)

type plexConfig struct {
	ID      string
	Name    string
	BaseURL string
	// Token is a Plex authentication token (X-Plex-Token). Sent as a header for
	// API calls and, unavoidably, in the query string of stream and artwork
	// URLs -- Plex will not serve either without one and a <video> element
	// cannot set a header.
	Token string

	// ClientID identifies this installation to Plex. Stable across restarts, so
	// one Yarr.It does not appear as a new device on every boot.
	ClientID string

	HTTPClient *http.Client

	// PoolLimit bounds how many items one section contributes. Zero means
	// plexDefaultPoolLimit.
	PoolLimit int

	// newSession makes the per-playback session id. Injectable so a test can
	// assert an exact URL; nil means a fresh random one per call.
	//
	// Fresh per call, never derived from the programme: Plex reads a session id
	// as one client's playback, so two viewers tuning into the same channel
	// with the same id would look like one viewer seeking back and forth, and
	// each would interrupt the other. The cost is that every tune-in starts its
	// own transcode, which is Plex's model and not something an adapter can fix.
	newSession func() string
}

// --- API shapes -------------------------------------------------------------

type plexTag struct {
	Tag string `json:"tag"`
}

type plexPart struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Duration  int64  `json:"duration"`
	File      string `json:"file"`
	Size      int64  `json:"size"`
	Container string `json:"container"`
}

type plexMedia struct {
	ID         int64      `json:"id"`
	Duration   int64      `json:"duration"`
	Container  string     `json:"container"`
	VideoCodec string     `json:"videoCodec"`
	AudioCodec string     `json:"audioCodec"`
	Part       []plexPart `json:"Part"`
}

type plexMetadata struct {
	RatingKey     string `json:"ratingKey"`
	Key           string `json:"key"`
	Type          string `json:"type"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	Year          int    `json:"year"`
	Duration      int64  `json:"duration"`
	ContentRating string `json:"contentRating"`
	Studio        string `json:"studio"`
	Thumb         string `json:"thumb"`

	Index       int `json:"index"`       // episode number
	ParentIndex int `json:"parentIndex"` // season number

	GrandparentTitle     string `json:"grandparentTitle"`
	GrandparentRatingKey string `json:"grandparentRatingKey"`

	Genre      []plexTag `json:"Genre"`
	Collection []plexTag `json:"Collection"`

	Media []plexMedia `json:"Media"`
}

type plexDirectory struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Agent string `json:"agent"`
}

type plexContainer struct {
	MediaContainer struct {
		Size              int             `json:"size"`
		TotalSize         int             `json:"totalSize"`
		MachineIdentifier string          `json:"machineIdentifier"`
		Version           string          `json:"version"`
		Directory         []plexDirectory `json:"Directory"`
		Metadata          []plexMetadata  `json:"Metadata"`
	} `json:"MediaContainer"`
}

// --- client -----------------------------------------------------------------

type plexClient struct {
	baseURL  string // no trailing slash
	token    string
	clientID string
	hc       *http.Client
}

func newPlexClient(cfg plexConfig) *plexClient {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: plexDefaultTimeout}
	}
	id := strings.TrimSpace(cfg.ClientID)
	if id == "" {
		id = "yarrit-linear"
	}
	return &plexClient{
		baseURL:  strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		token:    strings.TrimSpace(cfg.Token),
		clientID: id,
		hc:       hc,
	}
}

// identityParams are the X-Plex-* fields Plex expects on every request. They go
// in as headers for API calls and as query parameters for URLs a player will
// fetch, which cannot carry headers.
func (c *plexClient) identityParams(session string) url.Values {
	v := url.Values{
		"X-Plex-Product":           {plexProduct},
		"X-Plex-Version":           {plexVersion},
		"X-Plex-Platform":          {plexPlatform},
		"X-Plex-Platform-Version":  {plexVersion},
		"X-Plex-Device":            {plexPlatform},
		"X-Plex-Device-Name":       {plexProduct},
		"X-Plex-Model":             {"hosted"},
		"X-Plex-Client-Identifier": {c.clientID},
	}
	if session != "" {
		v.Set("session", session)
		v.Set("X-Plex-Session-Identifier", session)
	}
	return v
}

func (c *plexClient) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	for k, vals := range c.identityParams("") {
		req.Header.Set(k, vals[0])
	}
	if c.token != "" {
		req.Header.Set("X-Plex-Token", c.token)
	}
	return mediaSend(c.hc, req, out)
}

// --- provider ---------------------------------------------------------------

type plexProvider struct {
	c    *plexClient
	id   string
	name string

	poolLimit  int
	newSession func() string
}

func newPlexProvider(cfg plexConfig) *plexProvider {
	id, name := strings.TrimSpace(cfg.ID), strings.TrimSpace(cfg.Name)
	if id == "" {
		id = "plex"
	}
	if name == "" {
		name = "Plex"
	}
	limit := cfg.PoolLimit
	if limit <= 0 {
		limit = plexDefaultPoolLimit
	}
	sess := cfg.newSession
	if sess == nil {
		sess = mediaRandomSession
	}
	return &plexProvider{c: newPlexClient(cfg), id: id, name: name, poolLimit: limit, newSession: sess}
}

func (p *plexProvider) ID() string        { return p.id }
func (p *plexProvider) Name() string      { return p.name }
func (p *plexProvider) Domains() []string { return []string{"video"} }
func (p *plexProvider) Roles() []string   { return []string{"library", "stream"} }
func (p *plexProvider) Capabilities() []string {
	return []string{"health", "library", "libraryStatus", "stream"}
}

// --- health -----------------------------------------------------------------

// Health answers with one of the six schema.json states and never collapses
// them. Plex separates liveness from authorisation the same way Jellyfin does:
// /identity answers without a token and carries the version, while
// /library/sections needs one -- so a wrong token and an absent server are
// different answers rather than the same "down".
func (p *plexProvider) Health(ctx context.Context) Health {
	if p.c.baseURL == "" {
		return Health{State: HealthNotConfigured, Detail: "no address set for " + p.name}
	}
	if p.c.token == "" {
		return Health{State: HealthNotConfigured,
			Detail: "no token set for " + p.name + " — copy an X-Plex-Token from a Plex client"}
	}

	var ident plexContainer
	if err := p.c.get(ctx, "/identity", nil, &ident); err != nil {
		if status, ok := mediaHTTPStatus(err); ok {
			if mediaIsIncompatibleStatus(status) {
				return Health{State: HealthIncompatible,
					Detail: "this address answers, but it is not a Plex server — check the URL"}
			}
			return Health{State: HealthDegraded,
				Detail: p.name + " answered with HTTP " + strconv.Itoa(status)}
		}
		return Health{State: HealthUnreachable,
			Detail: "could not reach " + p.name + " at " + p.c.baseURL}
	}

	version := strings.TrimSpace(ident.MediaContainer.Version)
	if ident.MediaContainer.MachineIdentifier == "" {
		return Health{State: HealthIncompatible, Version: version,
			Detail: "this address answers, but did not identify itself as a Plex server — check the URL"}
	}
	major, minor, ok := plexVersionParts(version)
	if !ok {
		return Health{State: HealthIncompatible,
			Detail: p.name + " did not report a version this adapter can read (" + version + ")"}
	}
	if major < plexMinMajor || (major == plexMinMajor && minor < plexMinMinor) {
		return Health{State: HealthIncompatible, Version: version,
			Detail: fmt.Sprintf("%s %s is older than the Plex %d.%d API this adapter speaks — upgrade %s",
				p.name, version, plexMinMajor, plexMinMinor, p.name)}
	}

	// Up and the right version. Now prove the token by asking for something
	// only a token can see.
	var secs plexContainer
	if err := p.c.get(ctx, "/library/sections", nil, &secs); err != nil {
		if status, ok := mediaHTTPStatus(err); ok {
			switch {
			case mediaIsAuthStatus(status):
				return Health{State: HealthAuthFailed, Version: version,
					Detail: "the token for " + p.name + " was rejected — copy a fresh X-Plex-Token from a Plex client"}
			case mediaIsIncompatibleStatus(status):
				return Health{State: HealthIncompatible, Version: version,
					Detail: p.name + " does not serve /library/sections — check the URL and version"}
			}
			return Health{State: HealthDegraded, Version: version,
				Detail: p.name + " could not list its libraries (HTTP " + strconv.Itoa(status) + ")"}
		}
		return Health{State: HealthDegraded, Version: version,
			Detail: p.name + " is reachable but did not answer an authenticated request in time — it may be busy scanning"}
	}
	if len(secs.MediaContainer.Directory) == 0 {
		return Health{State: HealthDegraded, Version: version,
			Detail: p.name + " has no libraries yet — add one in Plex before building a channel from it"}
	}
	return Health{State: HealthOK, Version: version}
}

func plexVersionParts(v string) (int, int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0, false
	}
	// "1.43.3.10861-07dfddaeb"
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor := 0
	if len(parts) > 1 {
		if n, err := strconv.Atoi(parts[1]); err == nil {
			minor = n
		}
	}
	return major, minor, true
}

// --- sections ---------------------------------------------------------------

func (p *plexProvider) sections(ctx context.Context) ([]plexDirectory, error) {
	var out plexContainer
	if err := p.c.get(ctx, "/library/sections", nil, &out); err != nil {
		return nil, err
	}
	return out.MediaContainer.Directory, nil
}

// plexVideoSection reports whether a section holds something a video channel
// can schedule. An "artist" section is music and an audiobook shelf is filed as
// one here, so both are excluded rather than filtered later.
func plexVideoSection(sectionType string) bool {
	switch strings.ToLower(strings.TrimSpace(sectionType)) {
	case "movie", "show":
		return true
	}
	return false
}

// plexItemTypeCode is Plex's numeric type filter: 1 is a movie, 2 a show, 4 an
// episode. A show section is asked for episodes because a show has no runtime
// and cannot be given a slot.
func plexItemTypeCode(sectionType string) string {
	if strings.EqualFold(strings.TrimSpace(sectionType), "show") {
		return "4"
	}
	return "1"
}

// page fetches one page of a section.
func (p *plexProvider) page(ctx context.Context, sectionKey, typeCode string, start, size int) ([]plexMetadata, int, error) {
	q := url.Values{
		"type":                   {typeCode},
		"X-Plex-Container-Start": {strconv.Itoa(start)},
		"X-Plex-Container-Size":  {strconv.Itoa(size)},
		// Plex will happily attach every actor and review to each row. None of
		// it is used here and all of it is paid for in bytes.
		"includeGuids":     {"0"},
		"excludeAllLeaves": {"1"},
	}
	var out plexContainer
	if err := p.c.get(ctx, "/library/sections/"+url.PathEscape(sectionKey)+"/all", q, &out); err != nil {
		return nil, 0, err
	}
	total := out.MediaContainer.TotalSize
	if total == 0 {
		total = out.MediaContainer.Size
	}
	return out.MediaContainer.Metadata, total, nil
}

// showFacets builds series id -> genres and studio for a show section.
//
// It exists because Plex hangs genres off the show and not off the episode, so
// without this an episode arrives with no genres at all and a "genre is Horror"
// channel built on a TV library matches nothing -- a rule set that looks
// correct, previews as zero, and gives no clue why.
func (p *plexProvider) showFacets(ctx context.Context, sectionKey string) (map[string]plexMetadata, error) {
	facets := map[string]plexMetadata{}
	for start := 0; ; start += plexPageSize {
		rows, total, err := p.page(ctx, sectionKey, "2", start, plexPageSize)
		if err != nil {
			return facets, err
		}
		for _, r := range rows {
			facets[r.RatingKey] = r
		}
		if len(rows) < plexPageSize || (total > 0 && start+len(rows) >= total) {
			break
		}
	}
	return facets, nil
}

// --- library ----------------------------------------------------------------

func (p *plexProvider) Library(ctx context.Context, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "video") {
		return nil, nil // Not ours. Not an error.
	}
	secs, err := p.sections(ctx)
	if err != nil {
		return nil, err
	}
	var out []MediaItem
	for _, s := range secs {
		if !plexVideoSection(s.Type) {
			continue
		}
		// A page per section, not the whole shelf: this answers a settings
		// screen, and the linear pool has its own paging.
		rows, _, err := p.page(ctx, s.Key, plexItemTypeCode(s.Type), 0, plexPageSize)
		if err != nil {
			return out, err
		}
		for _, r := range rows {
			out = append(out, p.toMediaItem(r))
		}
	}
	return out, nil
}

func (p *plexProvider) LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error) {
	rk, ok := parsePlexID(canonicalID)
	if !ok {
		return StateUnknown, nil
	}
	m, err := p.metadata(ctx, rk)
	if err != nil {
		if errors.Is(err, errMediaItemGone) {
			return StateMissing, nil
		}
		return StateUnknown, err
	}
	if _, ok := plexPickPart(m); !ok {
		// Plex has a catalogue row and no file behind it.
		return StateMissing, nil
	}
	return StateAvailable, nil
}

func (p *plexProvider) metadata(ctx context.Context, ratingKey string) (plexMetadata, error) {
	var out plexContainer
	if err := p.c.get(ctx, "/library/metadata/"+url.PathEscape(ratingKey), nil, &out); err != nil {
		if status, ok := mediaHTTPStatus(err); ok && (status == http.StatusNotFound || status == http.StatusBadRequest) {
			return plexMetadata{}, fmt.Errorf("%w: %s has no item %s", errMediaItemGone, p.name, ratingKey)
		}
		return plexMetadata{}, err
	}
	if len(out.MediaContainer.Metadata) == 0 {
		return plexMetadata{}, fmt.Errorf("%w: %s has no item %s", errMediaItemGone, p.name, ratingKey)
	}
	return out.MediaContainer.Metadata[0], nil
}

// plexPickPart finds the file behind an item: the first part of the first media
// version that actually names one.
func plexPickPart(m plexMetadata) (plexPart, bool) {
	for _, med := range m.Media {
		for _, part := range med.Part {
			if strings.TrimSpace(part.Key) != "" {
				return part, true
			}
		}
	}
	return plexPart{}, false
}

// --- streaming --------------------------------------------------------------

// Stream honours the schedule's offset, at a cost it reports rather than hides.
//
//	offset 0 -> the part file itself. Plex serves it byte for byte and answers
//	            ranged requests with 206 and Accept-Ranges: bytes, so this is
//	            real direct play and really seekable. Preferred always.
//
//	offset > 0 -> the universal transcoder, which is the only Plex endpoint that
//	            takes a start position. Verified against a live server: the
//	            returned container's start_time is the requested offset and its
//	            first frame matches the source file at that timestamp. It
//	            answers "Accept-Ranges: none", so Seekable is false, and the
//	            bytes are re-muxed or re-encoded, so DirectPlay is false.
//
// The honesty is the feature. A client told Seekable=false will not draw a
// scrub bar it cannot honour, and one told DirectPlay=false knows the server is
// doing work per viewer -- which for Plex it genuinely is, one session each.
func (p *plexProvider) Stream(ctx context.Context, canonicalID string, opts StreamOptions) (StreamSource, error) {
	rk, ok := parsePlexID(canonicalID)
	if !ok {
		return StreamSource{}, fmt.Errorf("%s cannot stream %q: not an id it issued", p.name, canonicalID)
	}
	m, err := p.metadata(ctx, rk)
	if err != nil {
		return StreamSource{}, err
	}
	part, ok := plexPickPart(m)
	if !ok {
		return StreamSource{}, fmt.Errorf("%w: %s has no playable file for %s",
			errMediaItemGone, p.name, canonicalID)
	}

	offset := plexClampOffset(opts.OffsetSeconds, plexDurationMillis(m, part))
	if offset == 0 {
		q := url.Values{"X-Plex-Token": {p.c.token}}
		return StreamSource{
			URL:        p.c.baseURL + part.Key + "?" + q.Encode(),
			MimeType:   plexMime(part.Container, m),
			DirectPlay: true,
			Seekable:   true,
		}, nil
	}

	return StreamSource{
		URL: p.transcodeURL(rk, offset),
		// Deliberately blank. Plex negotiates the container per item at the
		// moment the stream is opened -- the same film came back as Matroska
		// here -- and naming one now would be a guess a player might act on.
		MimeType:   "",
		DirectPlay: false,
		Seekable:   false,
	}, nil
}

// transcodeURL builds the universal-transcoder URL that begins at offset.
//
// Every parameter here earns its place:
//
//	offset       the whole point; seconds, not milliseconds.
//	copyts=1     keeps the source timestamps, so the stream's own clock says
//	             2220 rather than 0. Without it the pictures are still right and
//	             everything downstream that reads timestamps is wrong.
//	fastSeek=1   seeks by keyframe rather than decoding from the start of the
//	             file, which is the difference between a channel that tunes in
//	             within a second and one that thinks about it for a minute.
//	directPlay=0 direct play cannot start at an offset, so asking for it here
//	             only invites Plex to hand back a stream that starts at zero.
//	directStream=1 lets Plex re-mux instead of re-encoding when the codecs
//	             already suit, which is the cheap path and still honours offset.
func (p *plexProvider) transcodeURL(ratingKey string, offset int) string {
	session := p.newSession()
	q := p.c.identityParams(session)
	q.Set("path", "/library/metadata/"+ratingKey)
	q.Set("mediaIndex", "0")
	q.Set("partIndex", "0")
	q.Set("protocol", "http")
	q.Set("offset", strconv.Itoa(offset))
	q.Set("copyts", "1")
	q.Set("fastSeek", "1")
	q.Set("directPlay", "0")
	q.Set("directStream", "1")
	q.Set("location", "lan")
	q.Set("maxVideoBitrate", "20000")
	q.Set("videoQuality", "100")
	q.Set("X-Plex-Token", p.c.token)
	return p.c.baseURL + "/video/:/transcode/universal/start?" + q.Encode()
}

// plexDurationMillis prefers the part's own length over the item's. They agree
// on a single-file item and differ on a split one, where the part is the truth
// about what an offset is being measured into.
func plexDurationMillis(m plexMetadata, part plexPart) int64 {
	if part.Duration > 0 {
		return part.Duration
	}
	for _, med := range m.Media {
		if med.Duration > 0 {
			return med.Duration
		}
	}
	return m.Duration
}

// plexClampOffset turns the schedule's seconds into a whole second inside the
// file. One second short of the end, for the same reason Jellyfin's clamp is: a
// seek to the last frame yields a stream with nothing in it, which on a channel
// is indistinguishable from the file having gone.
func plexClampOffset(seconds float64, durationMillis int64) int {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	off := int(math.Round(seconds))
	if durationMillis > plexMillisPerSecond {
		if last := int(durationMillis/plexMillisPerSecond) - 1; off > last {
			off = last
		}
	}
	if off < 0 {
		return 0
	}
	return off
}

func plexMime(container string, m plexMetadata) string {
	if c := strings.TrimSpace(container); c != "" {
		return jellyfinMime(c)
	}
	for _, med := range m.Media {
		if c := strings.TrimSpace(med.Container); c != "" {
			return jellyfinMime(c)
		}
	}
	return ""
}

// --- mapping ----------------------------------------------------------------

func (p *plexProvider) toMediaItem(m plexMetadata) MediaItem {
	typ := plexType(m.Type)
	out := MediaItem{
		CanonicalID:    plexCanonicalID(typ, m.RatingKey),
		Domain:         "video",
		Type:           typ,
		Title:          strings.TrimSpace(m.Title),
		Overview:       m.Summary,
		Year:           m.Year,
		Artwork:        p.artwork(m),
		ProviderID:     p.id,
		ProviderItemID: m.RatingKey,
		State:          StateAvailable,
	}
	if typ == "episode" {
		// The series name goes in Subtitle because that is where the linear
		// rule engine looks for it and where seriesKey falls back to.
		out.Subtitle = strings.TrimSpace(m.GrandparentTitle)
		out.Season = m.ParentIndex
		out.Episode = m.Index
	}
	return out
}

// toLinearItem is toMediaItem plus the facets scheduling and rules need. show
// is the parent series row for an episode, and carries the genres and studio
// Plex does not repeat on the episode itself; it is the zero value for a film.
func (p *plexProvider) toLinearItem(m plexMetadata, show plexMetadata, libraryID string) LinearItem {
	li := LinearItem{
		MediaItem:       p.toMediaItem(m),
		DurationSeconds: int(m.Duration / plexMillisPerSecond),
		Genres:          plexTags(m.Genre),
		Network:         m.Studio,
		Collection:      firstPlexTag(m.Collection),
		Rating:          m.ContentRating,
		SeriesID:        m.GrandparentRatingKey,
		LibraryID:       libraryID,
	}
	if len(li.Genres) == 0 {
		li.Genres = plexTags(show.Genre)
	}
	if li.Network == "" {
		li.Network = show.Studio
	}
	if li.Rating == "" {
		li.Rating = show.ContentRating
	}
	if li.MediaItem.Year == 0 {
		li.MediaItem.Year = show.Year
	}
	return li
}

func plexTags(tags []plexTag) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if s := strings.TrimSpace(t.Tag); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstPlexTag(tags []plexTag) string {
	for _, t := range tags {
		if s := strings.TrimSpace(t.Tag); s != "" {
			return s
		}
	}
	return ""
}

// plexType maps Plex's own words onto schema.json's, explicitly rather than by
// lowercasing whatever Plex said -- a word this package does not know would
// flow into MediaItem.Type and match nothing.
func plexType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "movie":
		return "movie"
	case "episode":
		return "episode"
	case "show":
		return "series"
	case "season":
		return "season"
	case "clip":
		return "movie"
	}
	return "movie"
}

// artwork carries the token, because Plex serves no image without one. That
// makes these URLs credentials in their own right, exactly like the stream URL,
// and they are never logged.
func (p *plexProvider) artwork(m plexMetadata) string {
	thumb := strings.TrimSpace(m.Thumb)
	if thumb == "" {
		return ""
	}
	sep := "?"
	if strings.Contains(thumb, "?") {
		sep = "&"
	}
	return p.c.baseURL + thumb + sep + "X-Plex-Token=" + url.QueryEscape(p.c.token)
}

// --- canonical ids ----------------------------------------------------------

// "plex:<schema type>:<ratingKey>". Same shape as the Jellyfin scheme, parsed
// the same way, so the two are told apart by their prefix alone.
func plexCanonicalID(typ, ratingKey string) string {
	if typ == "" {
		typ = "movie"
	}
	return "plex:" + typ + ":" + ratingKey
}

func parsePlexID(canonicalID string) (string, bool) {
	const prefix = "plex:"
	if !strings.HasPrefix(canonicalID, prefix) {
		return "", false
	}
	rest := canonicalID[len(prefix):]
	i := strings.LastIndexByte(rest, ':')
	if i < 0 {
		return "", false
	}
	rk := rest[i+1:]
	if rk == "" {
		return "", false
	}
	return rk, true
}
