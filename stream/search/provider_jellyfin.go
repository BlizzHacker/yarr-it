package main

// Jellyfin: the video library, and the thing that actually serves the bytes.
//
// This is the first adapter in the package that has to be right about *time*.
// A Radarr adapter that is a little sloppy shows a wrong year; a streaming
// adapter that is a little sloppy hands back a URL that plays from the
// beginning of the film while the guide swears it is 37 minutes in. Linear TV
// is one subtraction and one seek, and this file owns the seek.
//
// Three things are deliberate here:
//
//   - Time crosses into Jellyfin exactly once, through jellyfinTicks. Jellyfin
//     counts in .NET ticks -- 10,000,000 to the second -- and the classic bug
//     is to send seconds, or milliseconds, into a field that wants ticks. It
//     does not fail: 2,220 sent where 22,200,000,000 was meant is 0.0002
//     seconds in, which looks exactly like "it started from the beginning" and
//     sends you hunting the scheduler instead of the unit conversion. One
//     function, one test, one place to be wrong.
//
//   - DirectPlay and Seekable are measured, not assumed. Jellyfin's static
//     endpoint serves the file untouched and honours byte ranges, so it is both
//     direct play and seekable -- but it ignores startTimeTicks entirely, so it
//     cannot begin at an offset. The transcoding endpoints do honour
//     startTimeTicks. Those two facts decide everything below, and reporting
//     them honestly is the whole point: a caller told "seekable" that is not
//     will silently start at zero, which is the exact failure the linear engine
//     exists to prevent.
//
//   - The HTTP API is the product, not the database. A previous pass read
//     Jellyfin's SQLite directly for a fixture, which is fine for a fixture and
//     wrong for an adapter: it breaks the moment Jellyfin migrates a schema and
//     it cannot reach a Jellyfin that is not on this filesystem.
//
// Nothing here invents media words. Domain and Type resolve through
// schema.json, always.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// A .NET tick is 100 nanoseconds, so a second is ten million of them. This
	// is the only place the number appears.
	jellyfinTicksPerSecond = 10_000_000

	// The oldest Jellyfin whose API this adapter speaks. 10.8 is where the
	// PlaybackInfo/DeviceProfile shapes used below settled; below it the honest
	// answer is "incompatible", not a stream of confusing 404s.
	jellyfinMinMajor = 10
	jellyfinMinMinor = 8

	// How many items one page of a library listing asks for. Jellyfin will
	// happily return thousands in one response, but each row carries genre and
	// studio arrays and the server builds them all before writing a byte, so a
	// smaller page starts returning sooner and survives a busy server.
	jellyfinPageSize = 500

	// Default ceiling on how many items one library contributes to a channel's
	// pool. A real installation here holds ~35,650 items; pulling every one of
	// them every five minutes to schedule a channel that will play forty of
	// them is waste, and an unbounded fetch against a busy server is how a
	// guide request turns into a two-minute stall. Truncation is logged, never
	// silent.
	jellyfinDefaultPoolLimit = 10_000

	// A library listing against a large Jellyfin genuinely takes tens of
	// seconds. Callers painting UI pass their own shorter context and get the
	// shorter bound.
	jellyfinDefaultTimeout = 120 * time.Second
)

// jellyfinTicks converts seconds to Jellyfin's ticks. Negative offsets are
// clamped: a player cannot seek before the start, and passing a negative tick
// count to Jellyfin makes it start from zero without saying so.
func jellyfinTicks(seconds float64) int64 {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	return int64(math.Round(seconds * jellyfinTicksPerSecond))
}

// jellyfinSeconds is the inverse, used to report back what was actually asked
// for after clamping.
func jellyfinSeconds(ticks int64) float64 {
	return float64(ticks) / float64(jellyfinTicksPerSecond)
}

type jellyfinConfig struct {
	ID      string
	Name    string
	BaseURL string
	// APIKey is a Jellyfin API key (Dashboard -> API Keys). It is sent in the
	// Authorization header for API calls and, unavoidably, in the query string
	// of a stream URL -- a player cannot set headers, which is why Jellyfin's
	// own web client does the same thing.
	APIKey string

	// UserID scopes item queries to one Jellyfin user. Optional, but it buys
	// something concrete: Jellyfin 10.11 will only negotiate a device profile
	// in the context of a user, so with a UserID set the server returns its own
	// transcode plan, and without one this adapter builds the stream URL
	// itself. Both honour the offset; the former lets Jellyfin choose the
	// codecs. Setting it also filters the library to what that user may see.
	UserID string

	HTTPClient *http.Client

	// PoolLimit bounds how many items one library contributes. Zero means
	// jellyfinDefaultPoolLimit.
	PoolLimit int

	// newSession makes the per-playback session id that keeps Jellyfin from
	// handing back a cached transcode at the wrong position. Injectable so a
	// test can assert an exact URL; nil means a fresh random one per call.
	newSession func() string
}

// --- HTTP error taxonomy ----------------------------------------------------
//
// Deliberately local to the two media-server adapters rather than borrowed from
// a sibling: the distinction that health reporting turns on is "an answer we did
// not like" (a status) versus "no answer at all" (a transport failure), and
// owning it here means these files depend only on the frozen contract.

type mediaHTTPError struct {
	Status int
	Method string
	URL    string
	Body   string
}

func (e *mediaHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: status %d", e.Method, e.URL, e.Status)
	}
	return fmt.Sprintf("%s %s: status %d: %s", e.Method, e.URL, e.Status, e.Body)
}

type mediaTransportError struct{ Err error }

func (e *mediaTransportError) Error() string { return e.Err.Error() }
func (e *mediaTransportError) Unwrap() error { return e.Err }

// mediaSend performs one request, classifies the outcome, and decodes a 2xx body
// into out (which may be nil). Every error it returns is a *mediaHTTPError or a
// *mediaTransportError, so callers classify without string-matching.
func mediaSend(hc *http.Client, req *http.Request, out any) error {
	resp, err := hc.Do(req)
	if err != nil {
		return &mediaTransportError{Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &mediaHTTPError{
			Status: resp.StatusCode, Method: req.Method, URL: req.URL.Path,
			Body: mediaSnippet(resp.Body, 512),
		}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// mediaSnippet reads a bounded prefix of a body. An upstream error page can be a
// stack trace, and none of it belongs in a health Detail shown to a person.
func mediaSnippet(r io.Reader, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return strings.TrimSpace(string(b))
}

func mediaHTTPStatus(err error) (int, bool) {
	var he *mediaHTTPError
	if errors.As(err, &he) {
		return he.Status, true
	}
	return 0, false
}

func mediaIsAuthStatus(s int) bool {
	return s == http.StatusUnauthorized || s == http.StatusForbidden
}

// mediaIsIncompatibleStatus is the shape of "answers HTTP, but not with the API
// we speak": no such endpoint, wrong method, gone.
func mediaIsIncompatibleStatus(s int) bool {
	return s == http.StatusNotFound || s == http.StatusMethodNotAllowed || s == http.StatusGone
}

// errMediaItemGone is what an adapter returns when the server says it has never
// heard of the id. The linear resolver turns it into ErrLinearMissingMedia,
// which short-circuits the retry loop -- waiting will not bring a deleted file
// back, and three attempts only make the viewer stare at a spinner first.
var errMediaItemGone = errors.New("the server does not have this item")

// --- Jellyfin API shapes ----------------------------------------------------

type jellyfinPublicInfo struct {
	Version                string `json:"Version"`
	ServerName             string `json:"ServerName"`
	ID                     string `json:"Id"`
	ProductName            string `json:"ProductName"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

type jellyfinSystemInfo struct {
	Version           string `json:"Version"`
	ServerName        string `json:"ServerName"`
	HasPendingRestart bool   `json:"HasPendingRestart"`
	IsShuttingDown    bool   `json:"IsShuttingDown"`
}

type jellyfinNameID struct {
	Name string `json:"Name"`
	ID   string `json:"Id"`
}

type jellyfinItem struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Type         string `json:"Type"`
	Overview     string `json:"Overview"`
	RunTimeTicks int64  `json:"RunTimeTicks"`

	ProductionYear    int    `json:"ProductionYear"`
	OfficialRating    string `json:"OfficialRating"`
	SeriesName        string `json:"SeriesName"`
	SeriesID          string `json:"SeriesId"`
	SeasonName        string `json:"SeasonName"`
	IndexNumber       int    `json:"IndexNumber"`
	ParentIndexNumber int    `json:"ParentIndexNumber"`

	Genres  []string         `json:"Genres"`
	Studios []jellyfinNameID `json:"Studios"`

	// LocationType is "Virtual" for an episode Jellyfin knows about from the
	// metadata provider but has no file for. Scheduling one is how a channel
	// goes to a black screen at 20:00 with the guide insisting otherwise.
	LocationType string            `json:"LocationType"`
	IsFolder     bool              `json:"IsFolder"`
	ImageTags    map[string]string `json:"ImageTags"`
	MediaType    string            `json:"MediaType"`
}

type jellyfinItemsPage struct {
	Items            []jellyfinItem `json:"Items"`
	TotalRecordCount int            `json:"TotalRecordCount"`
	StartIndex       int            `json:"StartIndex"`
}

type jellyfinVirtualFolder struct {
	Name           string   `json:"Name"`
	ItemID         string   `json:"ItemId"`
	CollectionType string   `json:"CollectionType"`
	Locations      []string `json:"Locations"`
}

type jellyfinMediaSource struct {
	ID                   string `json:"Id"`
	Container            string `json:"Container"`
	Path                 string `json:"Path"`
	Protocol             string `json:"Protocol"`
	RunTimeTicks         int64  `json:"RunTimeTicks"`
	SupportsDirectPlay   bool   `json:"SupportsDirectPlay"`
	SupportsDirectStream bool   `json:"SupportsDirectStream"`
	SupportsTranscoding  bool   `json:"SupportsTranscoding"`
	// TranscodingUrl is Jellyfin's own answer to "how would you serve this",
	// already carrying the start position we asked for. Preferring it over a
	// hand-built URL means the server's own negotiation decides the container
	// and codecs, which is one fewer thing for this adapter to get wrong.
	TranscodingURL         string `json:"TranscodingUrl"`
	TranscodingSubProtocol string `json:"TranscodingSubProtocol"`
	IsRemote               bool   `json:"IsRemote"`
}

type jellyfinPlaybackInfo struct {
	MediaSources  []jellyfinMediaSource `json:"MediaSources"`
	PlaySessionID string                `json:"PlaySessionId"`
	ErrorCode     string                `json:"ErrorCode"`
}

// --- client -----------------------------------------------------------------

type jellyfinClient struct {
	baseURL string // no trailing slash
	apiKey  string
	userID  string
	hc      *http.Client
}

func newJellyfinClient(cfg jellyfinConfig) *jellyfinClient {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: jellyfinDefaultTimeout}
	}
	return &jellyfinClient{
		baseURL: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		apiKey:  strings.TrimSpace(cfg.APIKey),
		userID:  strings.TrimSpace(cfg.UserID),
		hc:      hc,
	}
}

// authHeader is Jellyfin's documented scheme. The token goes in a header rather
// than the query string for every API call; the one place it cannot is a stream
// URL handed to a player, and that is called out where it happens.
func (c *jellyfinClient) authHeader() string {
	return fmt.Sprintf(`MediaBrowser Token="%s", Client="Yarr.It", Device="Yarr.It", DeviceId="yarrit-linear", Version="1"`, c.apiKey)
}

func (c *jellyfinClient) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", c.authHeader())
	}
	return mediaSend(c.hc, req, out)
}

func (c *jellyfinClient) post(ctx context.Context, path string, q url.Values, body any, out any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", c.authHeader())
	}
	return mediaSend(c.hc, req, out)
}

// --- provider ---------------------------------------------------------------

type jellyfinProvider struct {
	c    *jellyfinClient
	id   string
	name string

	poolLimit  int
	newSession func() string
}

func newJellyfinProvider(cfg jellyfinConfig) *jellyfinProvider {
	id, name := strings.TrimSpace(cfg.ID), strings.TrimSpace(cfg.Name)
	if id == "" {
		id = "jellyfin"
	}
	if name == "" {
		name = "Jellyfin"
	}
	limit := cfg.PoolLimit
	if limit <= 0 {
		limit = jellyfinDefaultPoolLimit
	}
	sess := cfg.newSession
	if sess == nil {
		sess = mediaRandomSession
	}
	return &jellyfinProvider{
		c: newJellyfinClient(cfg), id: id, name: name, poolLimit: limit, newSession: sess,
	}
}

// mediaRandomSession makes a fresh playback session id. Shared by both media
// adapters because both servers key a running transcode on one, and both will
// hand back somebody else's stream if two playbacks share an id.
func mediaRandomSession() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "yarrit-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "yarrit-" + hex.EncodeToString(b[:])
}

func (p *jellyfinProvider) ID() string        { return p.id }
func (p *jellyfinProvider) Name() string      { return p.name }
func (p *jellyfinProvider) Domains() []string { return []string{"video"} }

// Roles: Jellyfin knows what is held and serves the bytes for it. It is not an
// acquisition provider and implements no Requester, so asking it to obtain
// something is a compile-time no.
func (p *jellyfinProvider) Roles() []string { return []string{"library", "stream"} }

func (p *jellyfinProvider) Capabilities() []string {
	return []string{"health", "library", "libraryStatus", "stream"}
}

// --- health -----------------------------------------------------------------

// Health answers with one of the six schema.json states and never collapses
// them. Jellyfin makes this straightforward because liveness and authorisation
// are separately observable: /System/Info/Public answers without a credential
// and carries the version, and /System/Info needs one -- so "the box is up but
// the key is wrong" and "the box is not there" are different answers rather than
// the same "down".
func (p *jellyfinProvider) Health(ctx context.Context) Health {
	if p.c.baseURL == "" {
		return Health{State: HealthNotConfigured, Detail: "no address set for " + p.name}
	}
	if p.c.apiKey == "" {
		return Health{State: HealthNotConfigured,
			Detail: "no API key set for " + p.name + " — create one in Jellyfin under Dashboard → API Keys"}
	}

	var pub jellyfinPublicInfo
	if err := p.c.get(ctx, "/System/Info/Public", nil, &pub); err != nil {
		if status, ok := mediaHTTPStatus(err); ok {
			if mediaIsIncompatibleStatus(status) {
				return Health{State: HealthIncompatible,
					Detail: "this address answers, but it is not a Jellyfin server — check the URL"}
			}
			return Health{State: HealthDegraded,
				Detail: p.name + " answered with HTTP " + strconv.Itoa(status)}
		}
		return Health{State: HealthUnreachable,
			Detail: "could not reach " + p.name + " at " + p.c.baseURL}
	}

	version := strings.TrimSpace(pub.Version)
	major, minor, ok := jellyfinVersion(version)
	if !ok {
		return Health{State: HealthIncompatible,
			Detail: p.name + " did not report a version this adapter can read (" + version + ")"}
	}
	if major < jellyfinMinMajor || (major == jellyfinMinMajor && minor < jellyfinMinMinor) {
		return Health{State: HealthIncompatible, Version: version,
			Detail: fmt.Sprintf("%s %s is older than the Jellyfin %d.%d API this adapter speaks — upgrade %s",
				p.name, version, jellyfinMinMajor, jellyfinMinMinor, p.name)}
	}
	if !pub.StartupWizardCompleted {
		return Health{State: HealthDegraded, Version: version,
			Detail: p.name + " has not finished its first-run setup yet"}
	}

	// Up and the right version. Now prove the credential by asking for
	// something only a credential can see.
	var sys jellyfinSystemInfo
	if err := p.c.get(ctx, "/System/Info", nil, &sys); err != nil {
		if status, ok := mediaHTTPStatus(err); ok {
			switch {
			case mediaIsAuthStatus(status):
				return Health{State: HealthAuthFailed, Version: version,
					Detail: "the API key for " + p.name + " was rejected — create a new one under Dashboard → API Keys"}
			case mediaIsIncompatibleStatus(status):
				return Health{State: HealthIncompatible, Version: version,
					Detail: p.name + " does not serve /System/Info — check the URL and version"}
			}
			return Health{State: HealthDegraded, Version: version,
				Detail: p.name + " could not answer /System/Info (HTTP " + strconv.Itoa(status) + ")"}
		}
		// Reachable a moment ago on the public endpoint but not now: the usual
		// cause is a server so busy it cannot answer an authenticated call,
		// which is degraded rather than absent.
		return Health{State: HealthDegraded, Version: version,
			Detail: p.name + " is reachable but did not answer an authenticated request in time — it may be busy scanning"}
	}
	if sys.IsShuttingDown {
		return Health{State: HealthDegraded, Version: version, Detail: p.name + " is shutting down"}
	}
	if sys.HasPendingRestart {
		return Health{State: HealthDegraded, Version: version,
			Detail: p.name + " needs a restart to finish applying an update"}
	}
	return Health{State: HealthOK, Version: version}
}

// jellyfinVersion reads the leading major.minor out of "10.11.11".
func jellyfinVersion(v string) (int, int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0, false
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

// --- libraries --------------------------------------------------------------

// views lists Jellyfin's top-level libraries. /Library/VirtualFolders is used
// rather than /UserViews because an API key is server-scoped and has no user:
// asking for a user's views with no user is how this returns an empty list on a
// perfectly healthy server.
func (p *jellyfinProvider) views(ctx context.Context) ([]jellyfinVirtualFolder, error) {
	var out []jellyfinVirtualFolder
	if err := p.c.get(ctx, "/Library/VirtualFolders", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// jellyfinVideoCollection reports whether a library holds the kind of thing a
// video channel can schedule. Music and books are excluded here rather than
// filtered later so a "Music" library never appears as a choice on a TV channel
// editor at all.
func jellyfinVideoCollection(collectionType string) bool {
	switch strings.ToLower(strings.TrimSpace(collectionType)) {
	case "movies", "tvshows", "homevideos", "musicvideos", "":
		// An empty collection type is a mixed library, which does hold video.
		return true
	}
	return false
}

// Library returns a bounded page of items, for the settings screen and the
// generic library view. It is not what a channel schedules from -- see
// LinearItems, which pages the whole library.
func (p *jellyfinProvider) Library(ctx context.Context, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "video") {
		return nil, nil // Not ours. Not an error.
	}
	items, _, err := p.page(ctx, "", []string{"Movie", "Series"}, 0, jellyfinPageSize)
	if err != nil {
		return nil, err
	}
	out := make([]MediaItem, 0, len(items))
	for _, it := range items {
		out = append(out, p.toMediaItem(it))
	}
	return out, nil
}

// LibraryStatus answers for one item without listing the shelf.
func (p *jellyfinProvider) LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error) {
	id, ok := parseJellyfinID(canonicalID)
	if !ok {
		// Not an id this provider issued. Unknown, not missing: claiming
		// "missing" about an id we never checked would offer a request for
		// something we have no opinion on.
		return StateUnknown, nil
	}
	it, err := p.item(ctx, id)
	if err != nil {
		if errors.Is(err, errMediaItemGone) {
			return StateMissing, nil
		}
		return StateUnknown, err
	}
	if strings.EqualFold(it.LocationType, "Virtual") {
		// Jellyfin knows the episode exists in the world; there is no file.
		return StateMissing, nil
	}
	return StateAvailable, nil
}

func (p *jellyfinProvider) item(ctx context.Context, id string) (jellyfinItem, error) {
	q := url.Values{"ids": {id}, "fields": {"Genres,Studios,Overview"}, "limit": {"1"}}
	if p.c.userID != "" {
		q.Set("userId", p.c.userID)
	}
	var page jellyfinItemsPage
	if err := p.c.get(ctx, "/Items", q, &page); err != nil {
		if status, ok := mediaHTTPStatus(err); ok && status == http.StatusNotFound {
			return jellyfinItem{}, errMediaItemGone
		}
		return jellyfinItem{}, err
	}
	if len(page.Items) == 0 {
		return jellyfinItem{}, errMediaItemGone
	}
	return page.Items[0], nil
}

// page fetches one page of items under a parent. parentID may be empty, which
// means the whole server.
func (p *jellyfinProvider) page(ctx context.Context, parentID string, types []string, start, limit int) ([]jellyfinItem, int, error) {
	q := url.Values{
		"recursive":              {"true"},
		"includeItemTypes":       {strings.Join(types, ",")},
		"fields":                 {"Genres,Studios,Overview"},
		"sortBy":                 {"SortName"},
		"sortOrder":              {"Ascending"},
		"enableImageTypes":       {"Primary"},
		"enableTotalRecordCount": {"true"},
		"enableUserData":         {"false"},
		// Episodes Jellyfin knows about but has no file for cannot be
		// scheduled, and are cheaper to exclude here than to filter later.
		"isMissing":  {"false"},
		"startIndex": {strconv.Itoa(start)},
		"limit":      {strconv.Itoa(limit)},
	}
	if parentID != "" {
		q.Set("parentId", parentID)
	}
	if p.c.userID != "" {
		q.Set("userId", p.c.userID)
	}
	var res jellyfinItemsPage
	if err := p.c.get(ctx, "/Items", q, &res); err != nil {
		return nil, 0, err
	}
	return res.Items, res.TotalRecordCount, nil
}

// --- streaming --------------------------------------------------------------

// Stream is the whole reason this file exists.
//
// The contract it holds: the URL that comes back begins at opts.OffsetSeconds.
// Not "the URL plus a note about where to seek to" -- StreamSource has nowhere
// to carry such a note, so a caller handed a URL has no way to know it starts in
// the wrong place. Everything else follows from that.
//
// Which endpoint gets used, and why:
//
//   - offset 0, and the source can be played untouched -> the static endpoint.
//     Jellyfin serves the file byte for byte and honours Range, so this is real
//     direct play and really seekable. Preferred whenever it is available,
//     because it costs the server nothing and loses no quality.
//
//   - offset > 0 -> a transcoding endpoint, because the static one ignores
//     startTimeTicks. This is reported as DirectPlay false, which is the honest
//     answer even when Jellyfin decides it can remux rather than re-encode:
//     the bytes went through its encoder pipeline either way.
//
// The offset is clamped inside the runtime. Asking for a frame past the end of
// a file gets a stream that ends instantly, which on a channel looks exactly
// like the media being missing.
func (p *jellyfinProvider) Stream(ctx context.Context, canonicalID string, opts StreamOptions) (StreamSource, error) {
	id, ok := parseJellyfinID(canonicalID)
	if !ok {
		return StreamSource{}, fmt.Errorf("%s cannot stream %q: not an id it issued", p.name, canonicalID)
	}

	ticks := jellyfinTicks(opts.OffsetSeconds)
	info, err := p.playbackInfo(ctx, id, ticks)
	if err != nil {
		return StreamSource{}, err
	}
	ms, ok := jellyfinPickSource(info.MediaSources)
	if !ok {
		// Jellyfin has the item and no playable source behind it: the file has
		// gone from disk. Missing rather than a transient failure, so the
		// caller does not retry three times to be told the same thing.
		return StreamSource{}, fmt.Errorf("%w: %s has no playable media source for %s",
			errMediaItemGone, p.name, canonicalID)
	}
	ticks = jellyfinClampTicks(ticks, ms.RunTimeTicks)

	if ticks == 0 && (ms.SupportsDirectPlay || ms.SupportsDirectStream) {
		return StreamSource{
			URL:        p.staticURL(id, ms),
			MimeType:   jellyfinMime(ms.Container),
			DirectPlay: true,
			// Verified against a live server: the static endpoint answers a
			// ranged request with 206 and Accept-Ranges: bytes.
			Seekable: true,
		}, nil
	}

	// Jellyfin's own transcode plan, which already carries the start position we
	// asked it for. Letting the server pick the container and codecs is one
	// fewer thing this adapter can get wrong, and it is what a real client uses.
	if u := strings.TrimSpace(ms.TranscodingURL); u != "" {
		full := p.absolute(u)
		full = jellyfinEnsureStartTicks(full, ticks)
		full = p.ensureAPIKey(full)
		hls := jellyfinIsHLS(full, ms.TranscodingSubProtocol)
		return StreamSource{
			URL:      full,
			MimeType: jellyfinTranscodeMime(full, ms.TranscodingSubProtocol),
			// The bytes went through the encoder pipeline, whether or not it
			// chose to copy the streams. Calling that direct play would be a
			// small lie that a client would act on.
			DirectPlay: false,
			// An HLS playlist from Jellyfin is a VOD playlist covering the rest
			// of the runtime, so a player can seek inside it. A progressive
			// transcode is produced as it is consumed and answers no ranges,
			// so it cannot be.
			Seekable: hls,
		}, nil
	}

	// No plan came back. That is the normal case for an API key with no user,
	// because Jellyfin 10.11 will not negotiate a device profile without one.
	//
	// The fallback is the progressive endpoint rather than a hand-built HLS
	// URL, and that was learned the hard way: a master.m3u8 assembled here
	// produces a playlist whose segments carry runtimeTicks=0 and will not
	// load, because the parameters a working playlist needs are the ones
	// PlaybackInfo would have computed. The progressive endpoint needs none of
	// that -- one URL, no segment negotiation -- and honours startTimeTicks.
	return StreamSource{
		URL:      p.progressiveURL(id, ms, ticks),
		MimeType: "video/mp4",
		// Re-muxed or re-encoded by Jellyfin either way, and produced as it is
		// consumed: verified against a live server to answer
		// "Accept-Ranges: none", so it cannot be seeked.
		DirectPlay: false,
		Seekable:   false,
	}, nil
}

// playbackInfo asks Jellyfin how it would serve this item from this position.
//
// StartTimeTicks is sent in the body, not merely used to build a URL: it is what
// gives Jellyfin the chance to return a plan already pointed at the right place.
// (On 10.11 it often does not -- see jellyfinEnsureStartTicks, which is what
// makes that survivable.)
//
// The device profile is sent only when a user is configured, and that is a
// compatibility fix rather than a preference. Jellyfin 10.11 evaluates the
// user's playback policy inside SetDeviceSpecificData whenever a profile is
// present, and an API key carries no user: the call then throws and comes back
// as a bare 400 on a perfectly healthy file. Without a profile the same call
// succeeds and returns the media sources, which is all the fallback URL needs.
func (p *jellyfinProvider) playbackInfo(ctx context.Context, id string, ticks int64) (jellyfinPlaybackInfo, error) {
	send := func(withProfile bool) (jellyfinPlaybackInfo, error) {
		q := url.Values{}
		if p.c.userID != "" {
			q.Set("userId", p.c.userID)
		}
		body := map[string]any{
			"StartTimeTicks":       ticks,
			"EnableDirectPlay":     true,
			"EnableDirectStream":   true,
			"EnableTranscoding":    true,
			"AllowVideoStreamCopy": true,
			"AllowAudioStreamCopy": true,
			// Explicitly off: opening a live stream here would start work on
			// the server for a plan the caller may never fetch, and this
			// adapter has no hook to close it again.
			"AutoOpenLiveStream": false,
		}
		if withProfile {
			body["DeviceProfile"] = jellyfinDeviceProfile()
		}
		var info jellyfinPlaybackInfo
		err := p.c.post(ctx, "/Items/"+url.PathEscape(id)+"/PlaybackInfo", q, body, &info)
		return info, err
	}

	withProfile := p.c.userID != ""
	info, err := send(withProfile)
	if err != nil && withProfile {
		if status, ok := mediaHTTPStatus(err); ok && status == http.StatusBadRequest {
			// The profile is what it objected to. Ask again without one rather
			// than reporting a healthy file as unplayable.
			log.Printf("jellyfin: %s rejected a device profile for %s; retrying without one", p.name, id)
			info, err = send(false)
		}
	}
	if err != nil {
		// Only 404 means the item is gone. A 400 means Jellyfin did not like
		// the request -- which is this adapter's problem, not a missing file,
		// and calling it missing media tells the operator their library is
		// broken when it is not, and stops the engine retrying.
		if status, ok := mediaHTTPStatus(err); ok && status == http.StatusNotFound {
			return jellyfinPlaybackInfo{}, fmt.Errorf("%w: %s has no item %s", errMediaItemGone, p.name, id)
		}
		return jellyfinPlaybackInfo{}, err
	}
	if info.ErrorCode != "" {
		return jellyfinPlaybackInfo{}, fmt.Errorf("%s refused to plan playback for %s: %s", p.name, id, info.ErrorCode)
	}
	return info, nil
}

// jellyfinDeviceProfile describes what this client can play.
//
// It is broad on purpose. A narrow profile makes Jellyfin re-encode material it
// could have remuxed, which costs CPU on the server for no gain; the containers
// and codecs here are the ones a modern player handles, and anything outside
// them still works because the transcoding profile catches it.
func jellyfinDeviceProfile() map[string]any {
	return map[string]any{
		"Name":                "Yarr.It Linear",
		"MaxStreamingBitrate": 120_000_000,
		"DirectPlayProfiles": []map[string]any{
			{"Container": "mp4,m4v,mkv,webm,mov,ts,m2ts,avi", "Type": "Video",
				"VideoCodec": "h264,hevc,vp8,vp9,av1,mpeg4,mpeg2video",
				"AudioCodec": "aac,mp3,ac3,eac3,opus,flac,vorbis,dts,truehd,pcm"},
		},
		"TranscodingProfiles": []map[string]any{
			// HLS first: it is the form that honours a start position and stays
			// seekable, which is what a linear channel needs.
			{"Container": "ts", "Type": "Video", "Protocol": "hls",
				"VideoCodec": "h264,hevc", "AudioCodec": "aac,mp3,ac3",
				"Context": "Streaming", "MaxAudioChannels": "6",
				"MinSegments": 1, "BreakOnNonKeyFrames": true},
			{"Container": "mp4", "Type": "Video", "Protocol": "http",
				"VideoCodec": "h264", "AudioCodec": "aac",
				"Context": "Streaming", "MaxAudioChannels": "2"},
		},
		"ContainerProfiles": []map[string]any{},
		"CodecProfiles":     []map[string]any{},
		"SubtitleProfiles": []map[string]any{
			{"Format": "vtt", "Method": "External"},
		},
	}
}

// jellyfinPickSource chooses among a file's media sources: the one that can be
// played untouched, else the first that exists at all.
func jellyfinPickSource(sources []jellyfinMediaSource) (jellyfinMediaSource, bool) {
	if len(sources) == 0 {
		return jellyfinMediaSource{}, false
	}
	for _, ms := range sources {
		if ms.SupportsDirectPlay {
			return ms, true
		}
	}
	for _, ms := range sources {
		if ms.SupportsDirectStream {
			return ms, true
		}
	}
	return sources[0], true
}

// jellyfinClampTicks keeps a requested position inside the file.
//
// One second short of the end rather than exactly the end: a seek to the final
// tick produces a stream with nothing in it, which on a channel is
// indistinguishable from the media having gone missing.
func jellyfinClampTicks(ticks, runtime int64) int64 {
	if ticks < 0 {
		return 0
	}
	if runtime > jellyfinTicksPerSecond && ticks >= runtime {
		return runtime - jellyfinTicksPerSecond
	}
	return ticks
}

// staticURL is direct play: the file, untouched, byte-range seekable.
//
// The API key rides in the query string here and only here. That is not a
// preference -- a URL handed to a <video> element or a set-top player cannot
// carry a header, and Jellyfin's own web client does exactly the same. It is
// why these URLs are treated as credentials themselves and never logged.
func (p *jellyfinProvider) staticURL(id string, ms jellyfinMediaSource) string {
	q := url.Values{
		"static":        {"true"},
		"mediaSourceId": {ms.ID},
		"api_key":       {p.c.apiKey},
	}
	container := strings.TrimSpace(ms.Container)
	if i := strings.IndexByte(container, ','); i >= 0 {
		container = container[:i]
	}
	path := "/Videos/" + url.PathEscape(id) + "/stream"
	if container != "" {
		path += "." + container
	}
	return p.c.baseURL + path + "?" + q.Encode()
}

// progressiveURL is the fallback when Jellyfin returned no plan of its own: a
// single remuxed or re-encoded stream that begins at the requested position.
//
// h264/aac are named rather than left to the server because Jellyfin copies the
// stream when the source already matches -- so a modern h264 file is remuxed
// at no CPU cost, and only genuinely incompatible material is re-encoded. Left
// unspecified, a set-top box can be handed something it cannot decode.
//
// PlaySessionId is the part that took a live server to find, and it is not
// cosmetic. Jellyfin caches a transcode and will hand the same one back to the
// same session for the same item -- the start position is not part of that
// key. Without a fresh session id per request, tuning in at 20:37 to a
// programme you last watched at 20:05 returns the 20:05 stream: the URL carries
// the right startTimeTicks, the server answers 200, and playback silently
// begins in the wrong place. That is precisely the failure the linear engine
// exists to prevent, and it is invisible from the URL.
func (p *jellyfinProvider) progressiveURL(id string, ms jellyfinMediaSource, ticks int64) string {
	q := url.Values{
		"mediaSourceId":  {ms.ID},
		"startTimeTicks": {strconv.FormatInt(ticks, 10)},
		"videoCodec":     {"h264"},
		"audioCodec":     {"aac"},
		"api_key":        {p.c.apiKey},
		"PlaySessionId":  {p.newSession()},
	}
	return p.c.baseURL + "/Videos/" + url.PathEscape(id) + "/stream.mp4?" + q.Encode()
}

func (p *jellyfinProvider) absolute(u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return p.c.baseURL + "/" + strings.TrimLeft(u, "/")
}

// ensureAPIKey adds the key to a URL that does not already carry one. Jellyfin
// usually embeds it in the TranscodingUrl; when it does not, a player would get
// a 401 and report it as "the file will not play".
func (p *jellyfinProvider) ensureAPIKey(raw string) string {
	if p.c.apiKey == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get("api_key") != "" || q.Get("ApiKey") != "" {
		return raw
	}
	q.Set("api_key", p.c.apiKey)
	u.RawQuery = q.Encode()
	return u.String()
}

// jellyfinEnsureStartTicks guarantees the offset is in the URL.
//
// Jellyfin normally puts it there itself, having been told in the PlaybackInfo
// body. "Normally" is not good enough for the one value this whole feature
// turns on, so it is checked and, if absent or zero while we asked for more,
// written in. A URL missing it plays from the beginning and looks perfectly
// healthy while doing so.
func jellyfinEnsureStartTicks(raw string, ticks int64) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	// Jellyfin has spelled this both ways across versions.
	for _, key := range []string{"startTimeTicks", "StartTimeTicks"} {
		if v := q.Get(key); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n == ticks {
				return raw
			}
			q.Del(key)
		}
	}
	if ticks > 0 {
		q.Set("startTimeTicks", strconv.FormatInt(ticks, 10))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func jellyfinIsHLS(raw, subProtocol string) bool {
	if strings.EqualFold(strings.TrimSpace(subProtocol), "hls") {
		return true
	}
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	return strings.HasSuffix(strings.ToLower(raw), ".m3u8")
}

func jellyfinTranscodeMime(raw, subProtocol string) string {
	if jellyfinIsHLS(raw, subProtocol) {
		return "application/x-mpegURL"
	}
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.LastIndexByte(raw, '.'); i >= 0 {
		return jellyfinMime(raw[i+1:])
	}
	return "video/mp4"
}

func jellyfinMime(container string) string {
	container = strings.ToLower(strings.TrimSpace(container))
	if i := strings.IndexByte(container, ','); i >= 0 {
		container = container[:i]
	}
	switch container {
	case "mp4", "m4v":
		return "video/mp4"
	case "mkv":
		return "video/x-matroska"
	case "webm":
		return "video/webm"
	case "ts", "m2ts", "mpegts":
		return "video/mp2t"
	case "mov":
		return "video/quicktime"
	case "avi":
		return "video/x-msvideo"
	case "m3u8":
		return "application/x-mpegURL"
	case "":
		return ""
	}
	return "video/" + container
}

// --- mapping ----------------------------------------------------------------

// toMediaItem maps one Jellyfin row onto the canonical shape.
func (p *jellyfinProvider) toMediaItem(it jellyfinItem) MediaItem {
	typ := jellyfinType(it.Type)
	m := MediaItem{
		CanonicalID:    jellyfinCanonicalID(typ, it.ID),
		Domain:         "video",
		Type:           typ,
		Title:          strings.TrimSpace(it.Name),
		Overview:       it.Overview,
		Year:           it.ProductionYear,
		Artwork:        p.artwork(it),
		ProviderID:     p.id,
		ProviderItemID: it.ID,
		State:          StateAvailable,
	}
	if typ == "episode" {
		// The series name goes in Subtitle because that is where the linear
		// rule engine looks for it (LinearFieldSeries) and where seriesKey
		// falls back to when an item carries no series id.
		m.Subtitle = strings.TrimSpace(it.SeriesName)
		m.Season = it.ParentIndexNumber
		m.Episode = it.IndexNumber
	}
	if strings.EqualFold(it.LocationType, "Virtual") {
		m.State = StateMissing
	}
	return m
}

// toLinearItem is toMediaItem plus the facets scheduling and rules need.
func (p *jellyfinProvider) toLinearItem(it jellyfinItem, libraryID string) LinearItem {
	li := LinearItem{
		MediaItem:       p.toMediaItem(it),
		DurationSeconds: int(it.RunTimeTicks / jellyfinTicksPerSecond),
		Genres:          it.Genres,
		Rating:          it.OfficialRating,
		SeriesID:        it.SeriesID,
		LibraryID:       libraryID,
	}
	if len(it.Studios) > 0 {
		li.Network = it.Studios[0].Name
	}
	return li
}

// jellyfinType maps Jellyfin's own words onto schema.json's. The mapping is
// explicit rather than a lowercase of whatever Jellyfin said, because a word
// this package does not know would flow into MediaItem.Type and match nothing
// -- which is the exact class of bug schema.json exists to stop.
func jellyfinType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "movie":
		return "movie"
	case "episode":
		return "episode"
	case "series":
		return "series"
	case "season":
		return "season"
	case "video", "musicvideo":
		// A home video or a music video is a standalone piece of video with a
		// runtime; "movie" is the schema type that means exactly that.
		return "movie"
	}
	return "movie"
}

func (p *jellyfinProvider) artwork(it jellyfinItem) string {
	tag := it.ImageTags["Primary"]
	if tag == "" {
		return ""
	}
	return p.c.baseURL + "/Items/" + url.PathEscape(it.ID) + "/Images/Primary?tag=" + url.QueryEscape(tag)
}

// --- canonical ids ----------------------------------------------------------

// The id scheme, bracketed in one place: "jellyfin:<schema type>:<Jellyfin id>".
//
// The type is carried so a card can be drawn from the id alone, and the
// Jellyfin id is last so parsing is "everything after the final colon" and
// stays correct if a future type ever contains one.
func jellyfinCanonicalID(typ, id string) string {
	if typ == "" {
		typ = "movie"
	}
	return "jellyfin:" + typ + ":" + id
}

func parseJellyfinID(canonicalID string) (string, bool) {
	const prefix = "jellyfin:"
	if !strings.HasPrefix(canonicalID, prefix) {
		return "", false
	}
	rest := canonicalID[len(prefix):]
	i := strings.LastIndexByte(rest, ':')
	if i < 0 {
		return "", false
	}
	id := rest[i+1:]
	if id == "" {
		return "", false
	}
	return id, true
}
