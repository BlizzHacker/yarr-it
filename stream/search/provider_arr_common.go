package main

// Shared plumbing for the three *arr adapters.
//
// Radarr, Sonarr and Lidarr are the same application wearing three coats: the
// same X-Api-Key header, the same /api/vN prefix, the same system/status and
// health documents, the same paged queue envelope. Triplicating that is how the
// three drift -- one of them learns to tell auth_failed from unreachable and
// the other two keep collapsing both into "down". So the transport, the health
// classifier and the queue vocabulary live here once, and each adapter is left
// holding only what is genuinely different: which nouns it deals in.
//
// Nothing in this file invents media words. Domains and types come from
// schema.json, resolved through canonicalDomain, always.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How long a single upstream call may take when the caller did not bound it.
// The *arr root-folder endpoint stats every mount and can genuinely take most
// of a minute on a box with sleeping USB disks, so this is deliberately long;
// callers that paint UI pass a short context and get the short bound they want.
const arrDefaultTimeout = 60 * time.Second

// How many search hits get their library state resolved. Lidarr cannot tell
// you from a lookup whether an artist is already yours, so each answer costs a
// round trip. Twelve covers everything above the fold; the rest are returned
// honestly as StateUnknown rather than guessed at.
const arrSearchStateLimit = 12

// Concurrency for those per-hit state probes. Enough to hide LAN latency,
// small enough that a search never looks like a denial of service.
const arrStateProbeConcurrency = 6

var errArrNoAPIKey = errors.New("no api key configured")

// arrHTTPError is a non-2xx answer that the instance was well enough to send.
// The status is kept because 401 and 404 mean completely different repairs.
type arrHTTPError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *arrHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: status %d", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("%s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// arrTransportError is "we never got an answer" -- refused, timed out, no
// route, DNS gone. Kept distinct from arrHTTPError because it is the whole
// difference between unreachable and auth_failed.
type arrTransportError struct{ Err error }

func (e *arrTransportError) Error() string { return e.Err.Error() }
func (e *arrTransportError) Unwrap() error { return e.Err }

// arrConfig is everything an adapter needs to exist. A configuration with a
// URL and no key is legal and expected: the provider still registers, still
// appears in the settings screen, and reports not_configured -- which is the
// only state a user can act on by typing a key in.
type arrConfig struct {
	ID      string
	Name    string
	BaseURL string
	APIKey  string

	HTTPClient *http.Client

	// Add defaults. Left zero, they are resolved from the instance itself on
	// first use: the first accessible root folder and the lowest quality
	// profile id. Resolving beats guessing "/movies" and failing at 400.
	RootFolder        string
	QualityProfileID  int
	MetadataProfileID int // Lidarr only.

	// SeriesType forces what a Sonarr instance files new shows as: "standard",
	// "anime" or "daily". Left empty, whatever Sonarr's own metadata says wins,
	// which is right almost always -- it already knows a show is anime. It is
	// here for the instance that exists precisely because its owner disagrees:
	// a second Sonarr, absolute-numbered, pointed at a different root.
	SeriesType string
}

// arrClient is the transport half of an adapter.
type arrClient struct {
	baseURL string // no trailing slash
	apiKey  string
	api     string // "v3" for Radarr/Sonarr, "v1" for Lidarr
	app     string // what system/status must report as appName
	// minMajor is the oldest major version whose API shape this adapter was
	// written against. Below it, the honest answer is incompatible, not a
	// stream of confusing 404s from endpoints that did not exist yet.
	minMajor int
	hc       *http.Client

	// Resolved add-defaults, cached: the root-folder endpoint is expensive and
	// a request path must not pay for it twice.
	defMu      sync.Mutex
	rootFolder string
	qualityID  int
	metadataID int
}

func newArrClient(cfg arrConfig, api, app string, minMajor int) *arrClient {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: arrDefaultTimeout}
	}
	return &arrClient{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		api:        api,
		app:        app,
		minMajor:   minMajor,
		hc:         hc,
		rootFolder: cfg.RootFolder,
		qualityID:  cfg.QualityProfileID,
		metadataID: cfg.MetadataProfileID,
	}
}

func (c *arrClient) path(p string) string {
	return c.baseURL + "/api/" + c.api + "/" + strings.TrimLeft(p, "/")
}

// do performs one call. body may be nil; out may be nil when the answer is not
// interesting. Errors are always one of errArrNoAPIKey, *arrTransportError or
// *arrHTTPError, so every caller can classify without string matching.
func (c *arrClient) do(ctx context.Context, method, p string, q url.Values, body, out any) error {
	if c.apiKey == "" {
		return errArrNoAPIKey
	}
	u := c.path(p)
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
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return &arrTransportError{Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Bounded: an *arr error page can be a stack trace, and none of it
		// belongs in a health Detail shown to a person.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &arrHTTPError{
			Status: resp.StatusCode, Method: method, Path: p,
			Body: strings.TrimSpace(string(snippet)),
		}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	// Streamed rather than buffered: Radarr's /movie is 28 MB on a real
	// library, and holding it twice is the difference between a 1 GB box
	// coping and not.
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *arrClient) get(ctx context.Context, p string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, p, q, nil, out)
}

func (c *arrClient) post(ctx context.Context, p string, body, out any) error {
	return c.do(ctx, http.MethodPost, p, nil, body, out)
}

func (c *arrClient) put(ctx context.Context, p string, body, out any) error {
	return c.do(ctx, http.MethodPut, p, nil, body, out)
}

func (c *arrClient) delete(ctx context.Context, p string, q url.Values) error {
	return c.do(ctx, http.MethodDelete, p, q, nil, nil)
}

// --- health ----------------------------------------------------------------

type arrSystemStatus struct {
	AppName      string `json:"appName"`
	InstanceName string `json:"instanceName"`
	Version      string `json:"version"`
}

type arrHealthEntry struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}

// health answers with one of the six schema.json states and never collapses
// them. Each branch below implies a different repair, which is the entire
// reason the states are not a boolean:
//
//	not_configured  someone must paste in an API key
//	unreachable     someone must fix a network or start the service
//	auth_failed     the key is present and wrong
//	incompatible    the address answers, but not with an API we speak
//	degraded        the instance is up and telling us it has a problem
//	healthy         nothing to do
func (c *arrClient) health(ctx context.Context) Health {
	if c.apiKey == "" {
		return Health{
			State:  HealthNotConfigured,
			Detail: "no API key set for " + c.app + " — copy it from its Settings → General page",
		}
	}
	if c.baseURL == "" {
		return Health{
			State:  HealthNotConfigured,
			Detail: "no address set for " + c.app,
		}
	}

	var st arrSystemStatus
	err := c.get(ctx, "system/status", nil, &st)
	if err != nil {
		var he *arrHTTPError
		if errors.As(err, &he) {
			switch {
			case he.Status == http.StatusUnauthorized, he.Status == http.StatusForbidden:
				return Health{
					State:  HealthAuthFailed,
					Detail: "the API key for " + c.app + " was rejected — check it against Settings → General",
				}
			case he.Status == http.StatusNotFound, he.Status == http.StatusMethodNotAllowed,
				he.Status == http.StatusGone:
				// The address answers HTTP but has no /api/vN. Either it is a
				// much older release or it is not an *arr at all.
				return Health{
					State: HealthIncompatible,
					Detail: "this address answers, but does not speak the " + c.app +
						" API " + c.api + " — check the URL and that it is a recent " + c.app,
				}
			}
			// Up enough to answer, sick enough to fail its own status call.
			return Health{
				State:  HealthDegraded,
				Detail: c.app + " answered its status check with HTTP " + strconv.Itoa(he.Status),
			}
		}
		// No answer at all.
		return Health{
			State:  HealthUnreachable,
			Detail: "could not reach " + c.app + " at " + c.baseURL,
		}
	}

	// A JSON document arrived. It still has to be the right application: a
	// Radarr entry pointed at Sonarr's port authenticates fine and then fails
	// on every noun, which reads like a broken adapter for as long as it takes
	// someone to notice the port.
	if st.AppName != "" && !strings.EqualFold(st.AppName, c.app) {
		return Health{
			State:   HealthIncompatible,
			Version: st.Version,
			Detail:  "this address is " + st.AppName + ", not " + c.app + " — check the port",
		}
	}
	major, ok := arrMajorVersion(st.Version)
	if !ok {
		return Health{
			State:  HealthIncompatible,
			Detail: c.app + " did not report a version we can read (" + st.Version + ")",
		}
	}
	if major < c.minMajor {
		return Health{
			State:   HealthIncompatible,
			Version: st.Version,
			Detail: fmt.Sprintf("%s %s is older than the %s v%d API this adapter speaks — upgrade %s",
				c.app, st.Version, c.app, c.minMajor, c.app),
		}
	}

	// It is up and it is the right thing. Now ask what it thinks of itself.
	var entries []arrHealthEntry
	if err := c.get(ctx, "health", nil, &entries); err != nil {
		// The instance answered system/status a moment ago, so this is not an
		// outage -- it is an instance that cannot complete a routine call.
		return Health{
			State:   HealthDegraded,
			Version: st.Version,
			Detail:  c.app + " is up but its health check failed: " + arrShortError(err),
		}
	}

	// *arr grades its own findings, and the grades mean different things.
	// "error" is a fault that stops work: a missing root folder, a download
	// client that cannot be reached. "warning" is mostly advisory -- an
	// available update, an indexer that has been sulking. Promoting warnings
	// to degraded would leave every instance permanently amber over a pending
	// update, and an alert that is always on is an alert nobody reads. So
	// errors set the state and warnings are reported in the detail.
	var problems, warnings []string
	for _, e := range entries {
		switch strings.ToLower(e.Type) {
		case "error":
			problems = append(problems, e.Message)
		case "warning":
			warnings = append(warnings, e.Message)
		}
	}
	if len(problems) > 0 {
		return Health{
			State:   HealthDegraded,
			Version: st.Version,
			Detail:  strings.Join(problems, "; "),
		}
	}
	h := Health{State: HealthOK, Version: st.Version}
	if len(warnings) > 0 {
		h.Detail = fmt.Sprintf("%d warning(s) from %s: %s", len(warnings), c.app, strings.Join(warnings, "; "))
	}
	return h
}

// arrMajorVersion reads the leading integer of "6.2.1.10461".
func arrMajorVersion(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	head := v
	if i := strings.IndexByte(v, '.'); i >= 0 {
		head = v[:i]
	}
	n, err := strconv.Atoi(head)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// arrShortError keeps a Detail readable. An operator needs to know which of
// the six things went wrong, not the inside of an HTTP client.
func arrShortError(err error) string {
	var he *arrHTTPError
	if errors.As(err, &he) {
		return "HTTP " + strconv.Itoa(he.Status)
	}
	var te *arrTransportError
	if errors.As(err, &te) {
		return "no answer"
	}
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// version is the bare version string, used by callers that want it without a
// full health verdict.
func (c *arrClient) version(ctx context.Context) (string, error) {
	var st arrSystemStatus
	if err := c.get(ctx, "system/status", nil, &st); err != nil {
		return "", err
	}
	return st.Version, nil
}

// --- canonical ids ---------------------------------------------------------

// The id scheme is deliberately built from the metadata source's id, not the
// *arr row id. A Radarr rebuild renumbers every movie; TMDB does not. It also
// means two instances of one application -- Radarr and Radarr-4K -- produce the
// same canonical id for the same film, which is what lets a UI say "already
// requested" about the other instance's copy.
//
//	tmdb:movie:78
//	tvdb:series:78874
//	tvdb:season:78874:1
//	tvdb:episode:78874:1:5
//	mbid:artist:a74b1b7f-71a5-4011-9441-d0b5e4122711
//	mbid:album:e75c0549-ad55-39e3-8025-c72c5d4a3c5d
//	mbid:track:a46aae44-3441-39fb-945a-cce752349138
//
// The type is spelled out rather than inferred because a MusicBrainz id gives
// no hint of whether it names an artist or an album, and guessing wrong is a
// lookup against the wrong endpoint that returns a confident empty list.
type arrRef struct {
	Scheme  string // tmdb | tvdb | mbid
	Type    string // a canonical type from schema.json
	ID      string
	Season  int
	Episode int
}

func arrMovieID(tmdbID int) string {
	return "tmdb:movie:" + strconv.Itoa(tmdbID)
}

func arrSeriesID(tvdbID int, anime bool) string {
	return "tvdb:" + arrSeriesType(anime) + ":" + strconv.Itoa(tvdbID)
}

// arrSeriesType maps Sonarr's own word onto a canonical type. "anime" is a
// video type in schema.json in its own right, so an anime instance's results
// are not squeezed into "series" and are not given a vocabulary of their own.
//
// The cost, stated plainly: reclassifying a show in Sonarr changes its
// canonical id, so a card cached under the old one no longer matches. That is
// rare and self-healing on the next search, and the alternative -- dropping
// the distinction from the id -- would mean a client could never tell an
// absolute-numbered show from a seasoned one without asking again.
func arrSeriesType(anime bool) string {
	if anime {
		return "anime"
	}
	return "series"
}

func arrSeasonID(tvdbID, season int) string {
	return "tvdb:season:" + strconv.Itoa(tvdbID) + ":" + strconv.Itoa(season)
}

func arrEpisodeID(tvdbID, season, episode int) string {
	return "tvdb:episode:" + strconv.Itoa(tvdbID) + ":" + strconv.Itoa(season) + ":" + strconv.Itoa(episode)
}

func arrMusicID(typ, mbid string) string { return "mbid:" + typ + ":" + mbid }

func parseArrRef(s string) (arrRef, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 3 {
		return arrRef{}, fmt.Errorf("%q is not a canonical id (want scheme:type:id)", s)
	}
	ref := arrRef{Scheme: parts[0], Type: parts[1], ID: parts[2]}
	if ref.Scheme == "" || ref.Type == "" || ref.ID == "" {
		return arrRef{}, fmt.Errorf("%q is not a canonical id (want scheme:type:id)", s)
	}
	// The type must be a word schema.json knows, or this id names something no
	// client can render.
	if canonicalDomain(ref.Type) == "" {
		return arrRef{}, fmt.Errorf("%q names type %q, which is not in the media schema", s, ref.Type)
	}
	switch ref.Type {
	case "season":
		if len(parts) < 4 {
			return arrRef{}, fmt.Errorf("%q is a season id with no season number", s)
		}
		n, err := strconv.Atoi(parts[3])
		if err != nil {
			return arrRef{}, fmt.Errorf("%q has a non-numeric season", s)
		}
		ref.Season = n
	case "episode":
		if len(parts) < 5 {
			return arrRef{}, fmt.Errorf("%q is an episode id with no season and episode", s)
		}
		sn, err1 := strconv.Atoi(parts[3])
		en, err2 := strconv.Atoi(parts[4])
		if err1 != nil || err2 != nil {
			return arrRef{}, fmt.Errorf("%q has a non-numeric season or episode", s)
		}
		ref.Season, ref.Episode = sn, en
	}
	return ref, nil
}

// --- shared value shapes ---------------------------------------------------

type arrImage struct {
	CoverType string `json:"coverType"`
	URL       string `json:"url"`
	RemoteURL string `json:"remoteUrl"`
}

// arrPoster prefers the remote URL. The local one is relative to the *arr
// instance, which lives on the LAN -- a phone on mobile data would render a
// broken image and no error.
func arrPoster(images []arrImage) string {
	var fallback string
	for _, im := range images {
		u := im.RemoteURL
		if u == "" {
			continue
		}
		if strings.EqualFold(im.CoverType, "poster") || strings.EqualFold(im.CoverType, "cover") {
			return u
		}
		if fallback == "" {
			fallback = u
		}
	}
	return fallback
}

// arrYear reads the year out of an ISO date, tolerating the empty and null
// forms every one of these APIs uses for "unknown".
func arrYear(date string) int {
	if len(date) < 4 {
		return 0
	}
	n, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return n
}

type arrQueueRecord struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	Status   string  `json:"status"`
	Size     float64 `json:"size"`
	SizeLeft float64 `json:"sizeleft"`

	TrackedDownloadState  string `json:"trackedDownloadState"`
	TrackedDownloadStatus string `json:"trackedDownloadStatus"`
	ErrorMessage          string `json:"errorMessage"`

	// Whichever of these the application in question uses.
	MovieID      int `json:"movieId"`
	SeriesID     int `json:"seriesId"`
	EpisodeID    int `json:"episodeId"`
	SeasonNumber int `json:"seasonNumber"`
	ArtistID     int `json:"artistId"`
	AlbumID      int `json:"albumId"`
}

type arrQueuePage struct {
	Page         int              `json:"page"`
	PageSize     int              `json:"pageSize"`
	TotalRecords int              `json:"totalRecords"`
	Records      []arrQueueRecord `json:"records"`
}

// arrQueueStage names what is happening, for an activity row. The words are
// the ones a person would use, not the ones the API uses: "importPending" is
// jargon for "downloaded, waiting to be filed".
func arrQueueStage(r arrQueueRecord) string {
	st := strings.ToLower(r.Status)
	tds := strings.ToLower(r.TrackedDownloadState)
	switch {
	case strings.EqualFold(r.TrackedDownloadStatus, "error"), st == "failed",
		tds == "failed", tds == "failedpending":
		return "failed"
	case tds == "importpending", tds == "importing", tds == "importblocked":
		return "importing"
	case st == "paused":
		return "paused"
	case st == "queued", st == "delay":
		return "queued"
	case st == "completed":
		return "importing"
	default:
		return "downloading"
	}
}

// arrQueueLibraryState is the same fact expressed as the thing a card must
// show. Note that a failed grab is still "requested": the item is monitored,
// the application will try again, and offering a fresh request would just
// duplicate work already queued.
func arrQueueLibraryState(r arrQueueRecord) LibraryState {
	switch arrQueueStage(r) {
	case "importing":
		return StateImporting
	case "failed":
		return StateRequested
	default:
		return StateDownloading
	}
}

func arrQueueProgress(r arrQueueRecord) float64 {
	if r.Size <= 0 {
		return 0
	}
	p := (r.Size - r.SizeLeft) / r.Size
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// arrContainerState folds the state of a thing that holds other things -- a
// series, a season, an artist, an album.
//
// The precedence is chosen for the question a card actually answers, which is
// "can I press play, and if not, is something already happening". Anything on
// disk wins: a series missing its last episode is still watchable tonight. Only
// when there is nothing at all does the queue speak, and only when the queue is
// silent too does "monitored" become the honest word "requested".
func arrContainerState(inLibrary bool, filesOnDisk int, monitored bool, queued *arrQueueRecord) LibraryState {
	if !inLibrary {
		return StateMissing
	}
	if filesOnDisk > 0 {
		return StateAvailable
	}
	if queued != nil {
		return arrQueueLibraryState(*queued)
	}
	if monitored {
		return StateRequested
	}
	return StateMissing
}

// arrLeafState is the same for a thing with exactly one file: a movie, an
// episode, a track.
func arrLeafState(inLibrary, hasFile, monitored bool, queued *arrQueueRecord) LibraryState {
	if !inLibrary {
		return StateMissing
	}
	if hasFile {
		return StateAvailable
	}
	if queued != nil {
		return arrQueueLibraryState(*queued)
	}
	if monitored {
		return StateRequested
	}
	return StateMissing
}

// --- add defaults ----------------------------------------------------------

type arrRootFolder struct {
	ID         int    `json:"id"`
	Path       string `json:"path"`
	Accessible bool   `json:"accessible"`
	Name       string `json:"name"`
}

type arrProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// resolveRootFolder picks where new items go. An inaccessible root is skipped
// rather than used: on this estate two of the configured roots point at an
// unmounted share, and adding into one of them produces an item the
// application immediately reports as broken.
func (c *arrClient) resolveRootFolder(ctx context.Context) (string, error) {
	c.defMu.Lock()
	if c.rootFolder != "" {
		defer c.defMu.Unlock()
		return c.rootFolder, nil
	}
	c.defMu.Unlock()

	var folders []arrRootFolder
	if err := c.get(ctx, "rootfolder", nil, &folders); err != nil {
		return "", err
	}
	var chosen string
	for _, f := range folders {
		if f.Accessible && f.Path != "" {
			chosen = f.Path
			break
		}
	}
	if chosen == "" {
		return "", fmt.Errorf("%s has no accessible root folder to add into", c.app)
	}
	c.defMu.Lock()
	c.rootFolder = chosen
	c.defMu.Unlock()
	return chosen, nil
}

func (c *arrClient) resolveQualityProfile(ctx context.Context) (int, error) {
	c.defMu.Lock()
	if c.qualityID != 0 {
		defer c.defMu.Unlock()
		return c.qualityID, nil
	}
	c.defMu.Unlock()

	id, err := c.lowestProfileID(ctx, "qualityprofile")
	if err != nil {
		return 0, err
	}
	c.defMu.Lock()
	c.qualityID = id
	c.defMu.Unlock()
	return id, nil
}

func (c *arrClient) resolveMetadataProfile(ctx context.Context) (int, error) {
	c.defMu.Lock()
	if c.metadataID != 0 {
		defer c.defMu.Unlock()
		return c.metadataID, nil
	}
	c.defMu.Unlock()

	id, err := c.lowestProfileID(ctx, "metadataprofile")
	if err != nil {
		return 0, err
	}
	c.defMu.Lock()
	c.metadataID = id
	c.defMu.Unlock()
	return id, nil
}

// lowestProfileID picks the first profile by id. Deterministic beats clever:
// an operator who wants a particular profile sets it in arrConfig, and one who
// has not should get the same answer every time rather than whatever the
// database happened to return first.
func (c *arrClient) lowestProfileID(ctx context.Context, endpoint string) (int, error) {
	var profiles []arrProfile
	if err := c.get(ctx, endpoint, nil, &profiles); err != nil {
		return 0, err
	}
	if len(profiles) == 0 {
		return 0, fmt.Errorf("%s has no %s configured", c.app, endpoint)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	return profiles[0].ID, nil
}

// arrCommand is the envelope every *arr uses for "go and do something".
type arrCommand struct {
	Name string `json:"name"`

	MovieIDs   []int `json:"movieIds,omitempty"`
	SeriesID   int   `json:"seriesId,omitempty"`
	SeasonNum  *int  `json:"seasonNumber,omitempty"`
	EpisodeIDs []int `json:"episodeIds,omitempty"`
	ArtistID   int   `json:"artistId,omitempty"`
	AlbumIDs   []int `json:"albumIds,omitempty"`
}

// --- construction from the environment -------------------------------------

// arrProvidersFromEnv builds adapters from RADARR_URL / RADARR_API_KEY and
// friends, so wiring is a config change rather than a code change.
//
// Extra instances are named: RADARR_INSTANCES=4k turns into RADARR_4K_URL and
// RADARR_4K_API_KEY, with the id "radarr-4k". That is how the anime case is
// configured -- SONARR_INSTANCES=anime -- and it is the reason the Registry
// allows several instances of one type at all.
//
// An instance with a URL but no key is still returned. Dropping it would make
// the settings screen show nothing, which is indistinguishable from "you never
// configured this" and leaves the user with nowhere to type the key.
func arrProvidersFromEnv() []Provider {
	var out []Provider
	for _, spec := range []struct {
		prefix string
		build  func(arrConfig) Provider
	}{
		{"RADARR", func(c arrConfig) Provider { return newRadarrProvider(c) }},
		{"SONARR", func(c arrConfig) Provider { return newSonarrProvider(c) }},
		{"LIDARR", func(c arrConfig) Provider { return newLidarrProvider(c) }},
	} {
		for _, cfg := range arrConfigsFor(spec.prefix) {
			out = append(out, spec.build(cfg))
		}
	}
	return out
}

func arrConfigsFor(prefix string) []arrConfig {
	var out []arrConfig
	base := strings.ToLower(prefix)

	if u := os.Getenv(prefix + "_URL"); u != "" {
		out = append(out, arrConfig{
			ID:               base,
			Name:             strings.ToUpper(base[:1]) + base[1:],
			BaseURL:          u,
			APIKey:           os.Getenv(prefix + "_API_KEY"),
			RootFolder:       os.Getenv(prefix + "_ROOT_FOLDER"),
			QualityProfileID: arrEnvInt(prefix + "_QUALITY_PROFILE_ID"),
			SeriesType:       os.Getenv(prefix + "_SERIES_TYPE"),
		})
	}
	for _, name := range strings.Split(os.Getenv(prefix+"_INSTANCES"), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := prefix + "_" + strings.ToUpper(strings.NewReplacer("-", "_", " ", "_").Replace(name))
		u := os.Getenv(key + "_URL")
		if u == "" {
			continue
		}
		slug := strings.ToLower(strings.NewReplacer("_", "-", " ", "-").Replace(name))
		out = append(out, arrConfig{
			ID:               base + "-" + slug,
			Name:             strings.ToUpper(base[:1]) + base[1:] + " " + name,
			BaseURL:          u,
			APIKey:           os.Getenv(key + "_API_KEY"),
			RootFolder:       os.Getenv(key + "_ROOT_FOLDER"),
			QualityProfileID: arrEnvInt(key + "_QUALITY_PROFILE_ID"),
			SeriesType:       os.Getenv(key + "_SERIES_TYPE"),
		})
	}
	return out
}

func arrEnvInt(k string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k)))
	if err != nil {
		return 0
	}
	return n
}

// --- the details capability ------------------------------------------------

// arrDetailer is one item and what it contains: a series and its seasons, a
// season and its episodes, an artist and its albums, an album and its tracks.
//
// It is an optional interface discovered by assertion, exactly like Searcher
// and Requester, and it lives here rather than in provider.go because it is
// not part of the frozen contract -- callers that do not know about it are
// unaffected, and the ones that do ask first.
type arrDetailer interface {
	Details(ctx context.Context, canonicalID string) (MediaItem, []MediaItem, error)
}

// arrBoundedMap runs fn over the first limit items with bounded concurrency.
// Used to resolve library state for search hits: the alternative is either a
// serial round trip per result or an unbounded burst at the instance.
func arrBoundedMap(n, limit int, fn func(i int)) {
	if limit > 0 && n > limit {
		n = limit
	}
	sem := make(chan struct{}, arrStateProbeConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// --- HTTP surface ----------------------------------------------------------

// registerArrRoutes wires the acquisition surface onto a mux.
//
// It takes the Registry rather than a list of adapters because routing a
// request is exactly "find the instance that claims this domain and can act on
// it" -- and because a household may run two Radarrs, so the caller has to be
// able to name one.
//
// These handlers are deliberately unwrapped: the caller decides which of them
// need a session. /api/arr/search and /api/arr/details describe things that
// exist and are safe to leave open; /api/arr/library, /api/arr/status and
// /api/arr/request expose and change what this household has, and should be
// wrapped the way /api/v1/library already is.
func registerArrRoutes(mux *http.ServeMux, r *Registry) {
	a := &arrAPI{reg: r}
	mux.HandleFunc("/api/arr/search", a.handleSearch)
	mux.HandleFunc("/api/arr/details", a.handleDetails)
	mux.HandleFunc("/api/arr/library", a.handleLibrary)
	mux.HandleFunc("/api/arr/status", a.handleStatus)
	mux.HandleFunc("/api/arr/request", a.handleRequest)
}

type arrAPI struct{ reg *Registry }

// How long any one provider gets on these routes. Longer than a health probe
// because a library listing is genuinely large, short enough that a wedged
// instance costs a row rather than the page.
const arrRouteTimeout = 45 * time.Second

func (a *arrAPI) providers() []Provider {
	if a.reg == nil {
		return nil
	}
	return a.reg.All()
}

func (a *arrAPI) byID(id string) Provider {
	for _, p := range a.providers() {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// arrFanoutResult mirrors the shape providers_api.go settled on: what came
// back, and who could not be asked. A short list that is silently short is
// worse than an error, because it looks like an answer.
type arrFanoutResult struct {
	Items  []MediaItem `json:"items"`
	Failed []string    `json:"failed,omitempty"`
}

func (a *arrAPI) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 400, map[string]string{"error": "missing q"})
		return
	}
	domain := r.URL.Query().Get("domain")
	only := r.URL.Query().Get("provider")

	var (
		mu     sync.Mutex
		items  []MediaItem
		failed []string
		wg     sync.WaitGroup
	)
	for _, p := range a.providers() {
		if only != "" && p.ID() != only {
			continue
		}
		s, ok := p.(Searcher)
		if !ok {
			continue // Asked, not assumed.
		}
		wg.Add(1)
		go func(p Provider, s Searcher) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					mu.Lock()
					failed = append(failed, p.ID())
					mu.Unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(r.Context(), arrRouteTimeout)
			defer cancel()
			got, err := s.Search(ctx, q, domain)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, p.ID())
				return
			}
			items = append(items, got...)
		}(p, s)
	}
	wg.Wait()

	sort.Strings(failed)
	if items == nil {
		items = []MediaItem{}
	}
	writeJSON(w, 200, arrFanoutResult{Items: items, Failed: failed})
}

func (a *arrAPI) handleDetails(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing id"})
		return
	}
	p := a.pickDetailer(r.URL.Query().Get("provider"), id)
	if p == nil {
		writeJSON(w, 404, map[string]string{
			"error": "no configured provider can describe " + id,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), arrRouteTimeout)
	defer cancel()

	item, children, err := p.(arrDetailer).Details(ctx, id)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	if children == nil {
		children = []MediaItem{}
	}
	writeJSON(w, 200, map[string]any{"item": item, "children": children})
}

// pickDetailer finds the instance that can speak about an id. With no provider
// named it takes the first whose domain matches the id's type, which is right
// for the common single-instance case and explicit for the rest.
func (a *arrAPI) pickDetailer(only, id string) Provider {
	ref, err := parseArrRef(id)
	if err != nil {
		return nil
	}
	want := canonicalDomain(ref.Type)
	for _, p := range a.providers() {
		if only != "" && p.ID() != only {
			continue
		}
		if _, ok := p.(arrDetailer); !ok {
			continue
		}
		if only == "" && !containsDomain(p.Domains(), want) {
			continue
		}
		return p
	}
	return nil
}

func (a *arrAPI) handleLibrary(w http.ResponseWriter, r *http.Request) {
	only := r.URL.Query().Get("provider")
	domain := r.URL.Query().Get("domain")

	var (
		mu     sync.Mutex
		items  []MediaItem
		failed []string
		wg     sync.WaitGroup
	)
	for _, p := range a.providers() {
		if only != "" && p.ID() != only {
			continue
		}
		lp, ok := p.(LibraryProvider)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(p Provider, lp LibraryProvider) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					mu.Lock()
					failed = append(failed, p.ID())
					mu.Unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(r.Context(), arrRouteTimeout)
			defer cancel()
			got, err := lp.Library(ctx, domain)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, p.ID())
				return
			}
			items = append(items, got...)
		}(p, lp)
	}
	wg.Wait()

	sort.Strings(failed)
	if items == nil {
		items = []MediaItem{}
	}
	writeJSON(w, 200, arrFanoutResult{Items: items, Failed: failed})
}

func (a *arrAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing id"})
		return
	}
	ref, err := parseArrRef(id)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	only := r.URL.Query().Get("provider")
	want := canonicalDomain(ref.Type)

	// The strongest answer across instances wins. With a Radarr and a
	// Radarr-4K, the film is "available" if either of them has it -- reporting
	// "missing" because the instance we happened to ask first does not have it
	// is how a household ends up with two copies.
	best := StateUnknown
	var failed []string
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, p := range a.providers() {
		if only != "" && p.ID() != only {
			continue
		}
		lp, ok := p.(LibraryProvider)
		if !ok {
			continue
		}
		if only == "" && !containsDomain(p.Domains(), want) {
			continue
		}
		wg.Add(1)
		go func(p Provider, lp LibraryProvider) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					mu.Lock()
					failed = append(failed, p.ID())
					mu.Unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(r.Context(), arrRouteTimeout)
			defer cancel()
			st, err := lp.LibraryStatus(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, p.ID())
				return
			}
			if arrStateRank(st) > arrStateRank(best) {
				best = st
			}
		}(p, lp)
	}
	wg.Wait()
	sort.Strings(failed)

	writeJSON(w, 200, map[string]any{
		"canonicalId": id,
		"state":       best,
		"failed":      failed,
	})
}

// arrStateRank orders the states by how much has already happened, so merging
// two instances' answers is a max rather than a coin flip.
func arrStateRank(s LibraryState) int {
	switch s {
	case StateAvailable:
		return 5
	case StateImporting:
		return 4
	case StateDownloading:
		return 3
	case StateRequested:
		return 2
	case StateMissing:
		return 1
	default:
		return 0
	}
}

type arrRequestBody struct {
	ProviderID  string `json:"providerId"`
	CanonicalID string `json:"canonicalId"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	// Monitor is a pointer so "absent" and "false" are different things. A
	// request with no opinion should behave like a request -- monitor it and
	// go looking -- while an explicit false is a watchlist add that must not
	// start a download.
	Monitor *bool `json:"monitor"`
}

func (a *arrAPI) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	var body arrRequestBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
		return
	}
	body.CanonicalID = strings.TrimSpace(body.CanonicalID)
	ref, err := parseArrRef(body.CanonicalID)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}

	var target Requester
	var targetID string
	want := canonicalDomain(ref.Type)
	for _, p := range a.providers() {
		if body.ProviderID != "" && p.ID() != body.ProviderID {
			continue
		}
		rq, ok := p.(Requester)
		if !ok {
			continue
		}
		if body.ProviderID == "" && !containsDomain(p.Domains(), want) {
			continue
		}
		target, targetID = rq, p.ID()
		break
	}
	if target == nil {
		writeJSON(w, 404, map[string]string{
			"error": "no configured provider can obtain " + body.CanonicalID,
		})
		return
	}

	monitor := true
	if body.Monitor != nil {
		monitor = *body.Monitor
	}
	// Season and episode may come from the id or from the body; the id wins,
	// because it is the thing the user clicked.
	season, episode := ref.Season, ref.Episode
	if season == 0 && episode == 0 {
		season, episode = body.Season, body.Episode
	}

	ctx, cancel := context.WithTimeout(r.Context(), arrRouteTimeout)
	defer cancel()

	res, err := target.Request(ctx,
		MediaItem{CanonicalID: body.CanonicalID, Domain: want, Type: ref.Type, ProviderID: targetID},
		RequestOptions{Season: season, Episode: episode, Monitor: monitor})
	if err != nil {
		writeJSON(w, 502, map[string]any{
			"accepted": false, "providerId": targetID, "error": err.Error(),
		})
		return
	}
	writeJSON(w, 200, map[string]any{
		"accepted": res.Accepted, "detail": res.Detail, "providerId": targetID,
	})
}
