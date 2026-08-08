package main

// RomM: the game library and player.
//
// RomM is the serving half of the games story. It knows what ROMs are on the
// shelf, and -- unlike a Radarr or a Romarr -- it can actually launch some of
// them: RomM bundles EmulatorJS and serves an in-browser player. So this
// adapter is a LibraryProvider, and advertises the "play" role, but only after
// it has verified that the launch path is really there. It is NOT an
// acquisition provider: it never grabs anything. Asking RomM to obtain a game
// is a compile-time no, because it implements no Requester.
//
// Two things are deliberately careful here:
//
//   - "play" is earned, not assumed. RomM ships the EmulatorJS player, but a
//     stripped or misconfigured install may not serve it, and EmulatorJS only
//     has cores for a subset of platforms. So the role is added only when the
//     player asset actually answers, and the launch URL is offered per-item
//     through the gamePlayer interface -- never as a blanket claim that every
//     console game in a 166k-ROM library runs in a browser tab.
//   - the id scheme is RomM's own ROM id ("romm:release:<n>"). A ROM is a
//     concrete regional dump, which is a "release" in schema.json's game
//     vocabulary, not the abstract "game" -- region and platform are first
//     class here and are not flattened into a video-style "quality".
//
// Nothing in this file invents media words: Domain and Type are resolved
// through canonicalDomain / the schema, always.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The oldest RomM whose /api this adapter was written against. RomM 3.x spoke a
// materially different API; below this the honest answer is incompatible, not a
// stream of confusing 404s.
const rommMinMajor = 3

// How many ROMs a library listing pulls by default. RomM's /api/roms is
// expensive on the server side regardless of limit (it recomputes filter facets
// over the whole library every call), and each ROM row carries its full merged
// metadata, so this is kept modest on purpose: a bounded page is the difference
// between a usable shelf and a multi-hundred-megabyte transfer.
const rommDefaultLibraryLimit = 72

// How long a RomM call may take when the caller did not bound it. Generous
// because /api/roms genuinely takes the better part of a minute on a large
// library; callers painting UI pass a short context and get the short bound.
const rommDefaultTimeout = 120 * time.Second

type rommConfig struct {
	ID       string
	Name     string
	BaseURL  string
	Username string
	Password string

	HTTPClient *http.Client

	// LibraryLimit bounds a Library() page. Zero means rommDefaultLibraryLimit.
	LibraryLimit int
}

// --- shared game HTTP error taxonomy ---------------------------------------
//
// Kept in this file (rather than borrowed from the *arr adapters) so the two
// game providers depend only on the frozen contract and each other, not on a
// sibling adapter that another author owns. The distinction that matters is the
// same one health reporting turns on: an answer we did not like (gameHTTPError,
// carries a status) versus no answer at all (gameTransportError).

type gameHTTPError struct {
	Status int
	Method string
	URL    string
	Body   string
}

func (e *gameHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: status %d", e.Method, e.URL, e.Status)
	}
	return fmt.Sprintf("%s %s: status %d: %s", e.Method, e.URL, e.Status, e.Body)
}

type gameTransportError struct{ Err error }

func (e *gameTransportError) Error() string { return e.Err.Error() }
func (e *gameTransportError) Unwrap() error { return e.Err }

// gameSend performs one request, classifies the outcome, and decodes a 2xx body
// into out (which may be nil). Every error it returns is a *gameHTTPError or a
// *gameTransportError, so callers classify without string-matching.
func gameSend(hc *http.Client, req *http.Request, out any) error {
	resp, err := hc.Do(req)
	if err != nil {
		return &gameTransportError{Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := readSnippet(resp.Body, 512)
		return &gameHTTPError{
			Status: resp.StatusCode, Method: req.Method, URL: req.URL.String(),
			Body: snippet,
		}
	}
	if out == nil {
		drain(resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// readSnippet reads a bounded, trimmed prefix of a body. An upstream error page
// can be a stack trace, and none of it belongs in a health Detail shown to a
// person.
func readSnippet(r io.Reader, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return strings.TrimSpace(string(b))
}

func drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }

// gameHTTPStatus reports the status code of an error that was an HTTP answer,
// and whether the error was one at all. Health classification is the caller.
func gameHTTPStatus(err error) (int, bool) {
	var he *gameHTTPError
	if errors.As(err, &he) {
		return he.Status, true
	}
	return 0, false
}

func gameIsAuthStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// gameIsIncompatibleStatus is the shape of "answers HTTP, but not with the API
// we speak": no such endpoint, wrong method, gone.
func gameIsIncompatibleStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed ||
		status == http.StatusGone
}

// --- optional interfaces this package's game providers may satisfy ---------
//
// Discovered by type assertion, exactly like Searcher and Requester in the
// frozen contract. They live here because they are not part of that contract --
// a caller that does not know about them is unaffected, and one that does asks
// first.

// gameDetailer is one item and, where the medium has them, its children. Games
// are leaves (a ROM contains nothing), so children is always empty -- the shape
// is shared with the *arr Details for consistency, and satisfies the same
// structural interface those handlers assert.
type gameDetailer interface {
	Details(ctx context.Context, canonicalID string) (MediaItem, []MediaItem, error)
}

// gamePlayer offers a launch URL for one item, and only when a real launch path
// exists for it. The bool is load-bearing: "no launch for this id" is a
// first-class answer, not an empty string a caller might paste into an <a>.
type gamePlayer interface {
	PlayURL(ctx context.Context, canonicalID string) (string, bool)
}

// --- RomM API shapes --------------------------------------------------------

type rommHeartbeat struct {
	System struct {
		Version         string `json:"VERSION"`
		ShowSetupWizard bool   `json:"SHOW_SETUP_WIZARD"`
	} `json:"SYSTEM"`
}

type rommRom struct {
	ID                  int           `json:"id"`
	Name                string        `json:"name"`
	FsNameNoExt         string        `json:"fs_name_no_ext"`
	FsName              string        `json:"fs_name"`
	FsExtension         string        `json:"fs_extension"`
	PlatformSlug        string        `json:"platform_slug"`
	PlatformDisplayName string        `json:"platform_display_name"`
	Summary             string        `json:"summary"`
	Regions             []string      `json:"regions"`
	Revision            string        `json:"revision"`
	URLCover            string        `json:"url_cover"`
	PathCoverLarge      string        `json:"path_cover_large"`
	PathCoverSmall      string        `json:"path_cover_small"`
	MissingFromFS       bool          `json:"missing_from_fs"`
	Metadatum           rommMetadatum `json:"metadatum"`
}

type rommMetadatum struct {
	FirstReleaseDate json.Number `json:"first_release_date"`
}

type rommRomsPage struct {
	Items  []rommRom `json:"items"`
	Total  int       `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

type rommTokenResp struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Expires     int    `json:"expires"`
}

// --- client -----------------------------------------------------------------

type rommClient struct {
	baseURL string // no trailing slash
	user    string
	pass    string
	hc      *http.Client
	// scope is what the token is asked for. A field rather than a constant
	// because RomM scopes its token per area and refuses one it cannot grant:
	// the firmware source (play_bios.go) needs firmware.read and this one does
	// not, and asking for a scope the account lacks would fail the token request
	// outright. Keeping them separate means a library that cannot read firmware
	// still serves its games.
	scope string

	mu      sync.Mutex
	token   string
	tokenTo time.Time
}

// The scopes the library adapter needs, and the default when none is set.
const rommLibraryScope = "roms.read platforms.read"

func newRommClient(cfg rommConfig) *rommClient {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: rommDefaultTimeout}
	}
	return &rommClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		user:    cfg.Username,
		pass:    cfg.Password,
		hc:      hc,
		scope:   rommLibraryScope,
	}
}

// accessToken fetches and caches a bearer token from RomM's OAuth2 password
// grant. Refreshed a minute early, because a token that expires mid-request
// produces a 401 indistinguishable from bad credentials -- a genuinely
// confusing thing to debug.
func (c *rommClient) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.tokenTo) {
		t := c.token
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	scope := c.scope
	if scope == "" {
		scope = rommLibraryScope
	}
	form := url.Values{
		"grant_type": {"password"},
		"username":   {c.user},
		"password":   {c.pass},
		"scope":      {scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	var tr rommTokenResp
	if err := gameSend(c.hc, req, &tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", &gameHTTPError{Status: 500, Method: "POST", URL: c.baseURL + "/api/token",
			Body: "RomM returned no access_token"}
	}
	ttl := time.Duration(tr.Expires) * time.Second
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	c.mu.Lock()
	c.token = tr.AccessToken
	c.tokenTo = time.Now().Add(ttl - time.Minute)
	c.mu.Unlock()
	return tr.AccessToken, nil
}

// getAuthed performs an authenticated GET and decodes out.
func (c *rommClient) getAuthed(ctx context.Context, path string, q url.Values, out any) error {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	return gameSend(c.hc, req, out)
}

// getPublic performs an unauthenticated GET (heartbeat, player assets).
func (c *rommClient) getPublic(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	return gameSend(c.hc, req, out)
}

// --- provider ---------------------------------------------------------------

type rommProvider struct {
	c    *rommClient
	id   string
	name string

	libLimit int

	// playChecked/playOK record whether the EmulatorJS launch path was verified.
	// Roles() reads playOK, so it never blocks and never guesses: play is
	// advertised only after the player asset actually answered.
	playMu      sync.Mutex
	playChecked bool
	playOK      bool
}

func newRommProvider(cfg rommConfig) *rommProvider {
	id, name := cfg.ID, cfg.Name
	if id == "" {
		id = "romm"
	}
	if name == "" {
		name = "RomM"
	}
	limit := cfg.LibraryLimit
	if limit <= 0 {
		limit = rommDefaultLibraryLimit
	}
	return &rommProvider{
		c:        newRommClient(cfg),
		id:       id,
		name:     name,
		libLimit: limit,
	}
}

func (p *rommProvider) ID() string   { return p.id }
func (p *rommProvider) Name() string { return p.name }

// Domains: games, and only games. Canonical, or the Registry refuses it.
func (p *rommProvider) Domains() []string { return []string{"game"} }

// Roles: RomM knows what is held ("library") and can launch some of it in a
// browser ("play") -- but the second only once verifyPlay has confirmed the
// player is actually served. Advertising play on an install that cannot honour
// it is exactly the "arbitrary console games run in a browser" claim the design
// forbids, so it is gated on a real probe.
func (p *rommProvider) Roles() []string {
	p.playMu.Lock()
	ok := p.playOK
	p.playMu.Unlock()
	if ok {
		return []string{"library", "play"}
	}
	return []string{"library"}
}

func (p *rommProvider) Capabilities() []string {
	return []string{"health", "library", "libraryStatus", "details"}
}

// verifyPlay probes the EmulatorJS player asset. It sets the play flag as a
// side effect and reports it. Called once at registration with a short context;
// tests set playOK directly and never touch the network.
func (p *rommProvider) verifyPlay(ctx context.Context) bool {
	err := p.c.getPublic(ctx, "/assets/emulatorjs/data/loader.js", nil)
	ok := err == nil
	p.playMu.Lock()
	p.playChecked = true
	p.playOK = ok
	p.playMu.Unlock()
	return ok
}

// --- health -----------------------------------------------------------------

// Health answers with one of the six schema.json states and never collapses
// them. RomM is unusual in that its liveness (/api/heartbeat) is open while its
// data needs a credential, so both are probed: the heartbeat says reachable and
// which version, and an authenticated /api/platforms says whether the
// credential actually works.
func (p *rommProvider) Health(ctx context.Context) Health {
	if p.c.baseURL == "" {
		return Health{State: HealthNotConfigured, Detail: "no address set for " + p.name}
	}
	if p.c.user == "" || p.c.pass == "" {
		return Health{State: HealthNotConfigured,
			Detail: "no credentials set for " + p.name + " — set a username and password"}
	}

	var hb rommHeartbeat
	if err := p.c.getPublic(ctx, "/api/heartbeat", &hb); err != nil {
		if status, ok := gameHTTPStatus(err); ok {
			if gameIsIncompatibleStatus(status) {
				return Health{State: HealthIncompatible,
					Detail: "this address answers, but has no RomM heartbeat — check the URL"}
			}
			return Health{State: HealthDegraded,
				Detail: p.name + " answered its heartbeat with HTTP " + strconv.Itoa(status)}
		}
		return Health{State: HealthUnreachable, Detail: "could not reach " + p.name + " at " + p.c.baseURL}
	}

	version := hb.System.Version
	major, ok := rommMajorVersion(version)
	if !ok {
		return Health{State: HealthIncompatible,
			Detail: p.name + " did not report a version we can read (" + version + ")"}
	}
	if major < rommMinMajor {
		return Health{State: HealthIncompatible, Version: version,
			Detail: fmt.Sprintf("%s %s is older than the RomM %d API this adapter speaks — upgrade %s",
				p.name, version, rommMinMajor, p.name)}
	}
	if hb.System.ShowSetupWizard {
		// Up, but telling us it is not finished being set up: a real, operator
		// -fixable problem, which is what degraded means.
		return Health{State: HealthDegraded, Version: version,
			Detail: p.name + " has not completed first-run setup"}
	}

	// Reachable and the right version. Now prove the credential by asking for
	// something only a credential can see.
	var platforms []json.RawMessage
	if err := p.c.getAuthed(ctx, "/api/platforms", nil, &platforms); err != nil {
		if status, ok := gameHTTPStatus(err); ok {
			switch {
			case gameIsAuthStatus(status):
				return Health{State: HealthAuthFailed, Version: version,
					Detail: "the credentials for " + p.name + " were rejected"}
			case gameIsIncompatibleStatus(status):
				return Health{State: HealthIncompatible, Version: version,
					Detail: p.name + " does not serve /api/platforms — check the URL and version"}
			}
			return Health{State: HealthDegraded, Version: version,
				Detail: p.name + " could not list platforms (HTTP " + strconv.Itoa(status) + ")"}
		}
		return Health{State: HealthUnreachable, Version: version,
			Detail: "could not reach " + p.name + " at " + p.c.baseURL}
	}
	return Health{State: HealthOK, Version: version}
}

func rommMajorVersion(v string) (int, bool) {
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

// --- library ----------------------------------------------------------------

func (p *rommProvider) Library(ctx context.Context, domain string) ([]MediaItem, error) {
	if domain != "" && !sameDomain(domain, "game") {
		return nil, nil // Not ours. Not an error.
	}
	var page rommRomsPage
	err := p.c.getAuthed(ctx, "/api/roms", url.Values{
		"limit":     {strconv.Itoa(p.libLimit)},
		"order_by":  {"name"},
		"order_dir": {"asc"},
	}, &page)
	if err != nil {
		return nil, err
	}
	out := make([]MediaItem, 0, len(page.Items))
	for _, r := range page.Items {
		out = append(out, p.toItem(r))
	}
	return out, nil
}

// LibraryStatus answers for one ROM without listing the shelf. The single-ROM
// endpoint is cheap where the list is not.
func (p *rommProvider) LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error) {
	id, ok := parseRommReleaseID(canonicalID)
	if !ok {
		// Not an id this provider issued. Unknown, not missing: claiming
		// "missing" about an id we never checked would offer a request for
		// something we have no opinion on.
		return StateUnknown, nil
	}
	var r rommRom
	if err := p.c.getAuthed(ctx, "/api/roms/"+id, nil, &r); err != nil {
		if status, ok := gameHTTPStatus(err); ok && status == http.StatusNotFound {
			return StateMissing, nil
		}
		return StateUnknown, err
	}
	if r.MissingFromFS {
		// RomM has a database row but the file is gone from disk -- present in
		// the catalogue, not actually playable.
		return StateMissing, nil
	}
	return StateAvailable, nil
}

// --- details ----------------------------------------------------------------

func (p *rommProvider) Details(ctx context.Context, canonicalID string) (MediaItem, []MediaItem, error) {
	id, ok := parseRommReleaseID(canonicalID)
	if !ok {
		return MediaItem{}, nil, fmt.Errorf("%s cannot describe %q", p.name, canonicalID)
	}
	var r rommRom
	if err := p.c.getAuthed(ctx, "/api/roms/"+id, nil, &r); err != nil {
		if status, ok := gameHTTPStatus(err); ok && status == http.StatusNotFound {
			return MediaItem{}, nil, fmt.Errorf("%s knows nothing about %s", p.name, canonicalID)
		}
		return MediaItem{}, nil, err
	}
	return p.toItem(r), nil, nil
}

// --- play -------------------------------------------------------------------

// PlayURL returns RomM's own in-browser launch URL for a ROM, and only when the
// player was verified. The per-platform question -- does EmulatorJS have a core
// for this system -- is RomM's own to answer, and RomM's player page answers it;
// this adapter deliberately does not reproduce (and so cannot drift from) that
// 80-platform map. What it guarantees is narrower and honest: a launch URL is
// offered only for a real RomM ROM id, on an install whose player actually
// serves, and never as a blanket claim over the whole library.
func (p *rommProvider) PlayURL(_ context.Context, canonicalID string) (string, bool) {
	p.playMu.Lock()
	ok := p.playOK
	p.playMu.Unlock()
	if !ok {
		return "", false
	}
	id, valid := parseRommReleaseID(canonicalID)
	if !valid {
		return "", false
	}
	return p.c.baseURL + "/console/rom/" + id + "/play", true
}

// --- mapping ----------------------------------------------------------------

// toItem maps one RomM ROM onto the canonical shape. Type is "release": a ROM is
// a specific regional dump, and region and platform are kept as first-class
// context (in Subtitle) rather than flattened into a video-style quality.
func (p *rommProvider) toItem(r rommRom) MediaItem {
	title := firstNonEmptyGame(r.Name, r.FsNameNoExt, r.FsName)

	subtitle := r.PlatformDisplayName
	if subtitle == "" {
		subtitle = r.PlatformSlug
	}
	if len(r.Regions) > 0 && r.Regions[0] != "" {
		subtitle = strings.TrimSpace(subtitle + " · " + r.Regions[0])
	}
	if r.Revision != "" {
		subtitle = strings.TrimSpace(subtitle + " · rev " + r.Revision)
	}

	state := StateAvailable
	if r.MissingFromFS {
		state = StateMissing
	}

	return MediaItem{
		CanonicalID:    rommReleaseID(r.ID),
		Domain:         "game",
		Type:           "release",
		Title:          title,
		Subtitle:       subtitle,
		Year:           rommYear(r.Metadatum.FirstReleaseDate),
		Overview:       r.Summary,
		Artwork:        p.artwork(r),
		ProviderID:     p.id,
		ProviderItemID: strconv.Itoa(r.ID),
		State:          state,
	}
}

// artwork prefers RomM's absolute cover URL (an IGDB image, reachable from
// anywhere) over the path-relative one, which is served off the LAN instance
// and would render broken on a phone on mobile data.
func (p *rommProvider) artwork(r rommRom) string {
	if r.URLCover != "" {
		return r.URLCover
	}
	for _, rel := range []string{r.PathCoverLarge, r.PathCoverSmall} {
		if rel == "" {
			continue
		}
		if strings.HasPrefix(rel, "http://") || strings.HasPrefix(rel, "https://") {
			return rel
		}
		return p.c.baseURL + "/" + strings.TrimLeft(rel, "/")
	}
	return ""
}

// rommYear reads a year out of RomM's first_release_date, which is a unix epoch
// (seconds) when known and null otherwise.
func rommYear(n json.Number) int {
	s := n.String()
	if s == "" {
		return 0
	}
	sec, err := n.Int64()
	if err != nil || sec <= 0 {
		return 0
	}
	return time.Unix(sec, 0).UTC().Year()
}

// --- canonical ids ----------------------------------------------------------

// rommReleaseID / parseRommReleaseID bracket the id scheme in one place. A ROM
// is identified by RomM's own numeric id: it is stable for the life of the file
// and is what both the library view and the player URL are keyed on.
func rommReleaseID(id int) string { return "romm:release:" + strconv.Itoa(id) }

func parseRommReleaseID(canonicalID string) (string, bool) {
	const prefix = "romm:release:"
	if !strings.HasPrefix(canonicalID, prefix) {
		return "", false
	}
	id := canonicalID[len(prefix):]
	if id == "" {
		return "", false
	}
	if _, err := strconv.Atoi(id); err != nil {
		return "", false
	}
	return id, true
}

// firstNonEmptyGame returns the first non-blank string. Named to avoid
// colliding with any sibling helper in this package.
func firstNonEmptyGame(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
