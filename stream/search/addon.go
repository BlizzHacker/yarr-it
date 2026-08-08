package main

// A client for the Stremio addon protocol.
//
// WHY THIS EXISTS
//
// Stremio's addon protocol is the reason Stremio has a catalogue at all: an
// addon is a plain HTTP server that returns JSON, hundreds already exist, and
// none of them know or care which client is asking. Speaking that protocol
// means every one of those addons works here on day one -- in a browser, with
// nothing installed, which is the one thing Stremio itself cannot do.
//
// PROVENANCE -- read this before changing anything here
//
// The wire format below was learned from Stremio's MIT-licensed addon SDK
// documentation (docs/protocol.md and docs/api/**) and then checked against
// live public addons. Nothing was read from, ported from, or transliterated
// out of stremio-web, which is GPL-2.0, nor from any of the Stremio
// repositories that carry no licence at all. That matters commercially: Yarr.It
// is permissive on purpose, and a single copied file would relicense it.
//
// WHERE THE DOCS AND THE WIRE DISAGREE
//
// Both were checked, and the wire wins, because the wire is what ships:
//
//   * protocol.md's own meta example shows `{"meta": [ {...} ]}` -- an array.
//     Cinemeta, the canonical public addon, answers `{"meta": {...}}` -- an
//     object. Both are accepted here.
//   * meta.md documents `genres` and `releaseInfo`. Cinemeta sends `genre` and
//     `year`. Both spellings are read.
//   * meta.md marks `poster` **required** on a catalog preview. Real catalogs
//     omit it. A missing poster drops an image, never an item.
//   * content.types.md lists four types "as of Apr 2016" and invites people to
//     ask for more. Torrentio's live manifest already declares `anime` and
//     `other`. So the type vocabulary is open in practice, and treating it as a
//     closed set of four is what makes the protocol look video-only.
//
// Being strict about any of those would reject working addons, which is the
// only failure mode that actually matters for a compatibility layer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- limits ----------------------------------------------------------------
//
// Every one of these is a bound on somebody else's server, which is the whole
// point: an addon is a URL a user pasted, so it is hostile until it has
// behaved.

const (
	// A manifest is a few hundred bytes to a few kilobytes. Cinemeta's, the
	// largest of the public ones, is under 16 KiB. 256 KiB is generous by two
	// orders of magnitude and still small enough that a hostile addon cannot
	// use it to exhaust memory.
	addonManifestMaxBytes = 256 << 10

	// A catalog page is 100 items of metadata. 4 MiB fits that with room for
	// verbose descriptions, and refuses an endless body.
	addonResourceMaxBytes = 4 << 20

	// Manifest fetches paint a settings screen and happen while somebody
	// watches, so they are held to the same bound as a provider health probe.
	addonManifestTimeout = 8 * time.Second

	// Resource fetches are allowed longer: a stream addon legitimately scrapes
	// several sources before answering.
	addonResourceTimeout = 15 * time.Second

	// A redirect chain longer than this is either a loop or an attempt to walk
	// somewhere the first check would have refused.
	addonMaxRedirects = 3

	// Identifies us to addon operators reading their logs, and carries no
	// information about the viewer.
	addonUserAgent = "Yarr.It/1.0 (+https://yarrit.com; Stremio addon protocol client)"
)

// --- the wire format -------------------------------------------------------

// AddonManifest is /manifest.json.
//
// Only the fields Yarr.It acts on are modelled. Unknown fields are dropped
// rather than rejected: addons in the wild carry vendor extensions
// (`extraSupported`, `popularities`, `dvdRelease`) and a client that refuses
// what it does not recognise is a client that breaks every time someone else
// ships a feature.
type AddonManifest struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	Resources  []AddonResource `json:"resources"`
	Types      []string        `json:"types"`
	IDPrefixes []string        `json:"idPrefixes,omitempty"`

	Catalogs      []AddonCatalog `json:"catalogs"`
	AddonCatalogs []AddonCatalog `json:"addonCatalogs,omitempty"`

	Logo          string `json:"logo,omitempty"`
	Background    string `json:"background,omitempty"`
	ContactEmail  string `json:"contactEmail,omitempty"`
	BehaviorHints struct {
		Adult                 bool `json:"adult,omitempty"`
		P2P                   bool `json:"p2p,omitempty"`
		Configurable          bool `json:"configurable,omitempty"`
		ConfigurationRequired bool `json:"configurationRequired,omitempty"`
	} `json:"behaviorHints,omitempty"`
}

// AddonResource is one entry of `resources`.
//
// The protocol allows a resource to be either a bare string ("meta") or an
// object ({name, types, idPrefixes}). Both forms are live right now: Cinemeta
// uses strings, Torrentio uses objects. A parser that handles only one of them
// silently loses half the ecosystem, so the shape is decided per element.
type AddonResource struct {
	Name       string   `json:"name"`
	Types      []string `json:"types,omitempty"`
	IDPrefixes []string `json:"idPrefixes,omitempty"`
}

func (a *AddonResource) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		a.Name = s
		return nil
	}
	// The alias breaks the recursion that would otherwise call this method
	// again on the object form.
	type plain AddonResource
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*a = AddonResource(p)
	return nil
}

// AddonCatalog is one entry of `catalogs` or `addonCatalogs`.
type AddonCatalog struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`

	Extra []AddonExtra `json:"extra,omitempty"`
	// extraSupported is the older spelling and is still what Cinemeta emits
	// alongside the new one. Read so a search-capable catalog is not reported
	// as feed-only.
	ExtraSupported []string `json:"extraSupported,omitempty"`
	ExtraRequired  []string `json:"extraRequired,omitempty"`
}

type AddonExtra struct {
	Name         string   `json:"name"`
	IsRequired   bool     `json:"isRequired,omitempty"`
	Options      []string `json:"options,omitempty"`
	OptionsLimit int      `json:"optionsLimit,omitempty"`
}

// supportsExtra reports whether this catalog accepts an extra argument, under
// either spelling.
func (c AddonCatalog) supportsExtra(name string) bool {
	for _, e := range c.Extra {
		if strings.EqualFold(e.Name, name) {
			return true
		}
	}
	for _, e := range c.ExtraSupported {
		if strings.EqualFold(e, name) {
			return true
		}
	}
	return false
}

// requiresExtra reports whether the catalog refuses to answer without an
// argument. A search-only catalog declares `search` required, and asking it for
// a plain feed returns nothing at all -- so those must be skipped when building
// a browse row rather than shown as empty.
func (c AddonCatalog) requiresExtra(name string) bool {
	for _, e := range c.Extra {
		if strings.EqualFold(e.Name, name) && e.IsRequired {
			return true
		}
	}
	for _, e := range c.ExtraRequired {
		if strings.EqualFold(e, name) {
			return true
		}
	}
	return false
}

// AddonMeta covers both the full Meta object and the shorter Meta Preview.
// They differ only in which fields are populated, and a single struct means a
// catalog row and a details page render from the same code.
type AddonMeta struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Poster      string `json:"poster,omitempty"`
	PosterShape string `json:"posterShape,omitempty"`
	Background  string `json:"background,omitempty"`
	Logo        string `json:"logo,omitempty"`
	Description string `json:"description,omitempty"`

	// The protocol documents releaseInfo; Cinemeta sends year. Both are read,
	// and both may be a string ("2000-2014") or a number (2008), so both are
	// decoded leniently.
	ReleaseInfo looseString `json:"releaseInfo,omitempty"`
	Year        looseString `json:"year,omitempty"`

	IMDBRating looseString `json:"imdbRating,omitempty"`
	Runtime    string      `json:"runtime,omitempty"`
	Released   string      `json:"released,omitempty"`

	// genres is documented; genre is what ships. Both are collected, and
	// AllGenres is the only thing that should ever read them -- see there.
	Genres []string `json:"genres,omitempty"`
	Genre  []string `json:"genre,omitempty"`

	Videos []AddonVideo `json:"videos,omitempty"`
}

type AddonVideo struct {
	ID        string        `json:"id"`
	Title     string        `json:"title,omitempty"`
	Season    int           `json:"season,omitempty"`
	Episode   int           `json:"episode,omitempty"`
	Released  string        `json:"released,omitempty"`
	Thumbnail string        `json:"thumbnail,omitempty"`
	Overview  string        `json:"overview,omitempty"`
	Streams   []AddonStream `json:"streams,omitempty"`
}

// AddonStream is one playable option.
//
// Exactly one of the source fields identifies the stream. They are not
// collapsed into a single URL here because they mean genuinely different things
// downstream: a magnet needs the torrent engine, an externalUrl must open a
// browser, and a ytId needs a YouTube player. Flattening them would force the
// player to guess from the string.
type AddonStream struct {
	URL         string `json:"url,omitempty"`
	YTID        string `json:"ytId,omitempty"`
	InfoHash    string `json:"infoHash,omitempty"`
	FileIdx     *int   `json:"fileIdx,omitempty"`
	ExternalURL string `json:"externalUrl,omitempty"`

	Name string `json:"name,omitempty"`
	// title is the older spelling of description and is still the one
	// Torrentio populates. Kept separate so neither is lost.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`

	Sources   []string        `json:"sources,omitempty"`
	Subtitles []AddonSubtitle `json:"subtitles,omitempty"`

	BehaviorHints struct {
		NotWebReady      bool     `json:"notWebReady,omitempty"`
		BingeGroup       string   `json:"bingeGroup,omitempty"`
		CountryWhitelist []string `json:"countryWhitelist,omitempty"`
		Filename         string   `json:"filename,omitempty"`
		VideoSize        int64    `json:"videoSize,omitempty"`
	} `json:"behaviorHints,omitempty"`
}

type AddonSubtitle struct {
	ID   string `json:"id,omitempty"`
	URL  string `json:"url"`
	Lang string `json:"lang"`
}

// looseString decodes a JSON value that is documented as a string but arrives
// as a number often enough to matter: imdbRating is "6.8" from Cinemeta and 6.8
// from several community addons, and a hard type error there loses the whole
// catalog page over a rating.
type looseString string

func (s *looseString) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = looseString(v)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*s = looseString(n.String())
		return nil
	}
	// Anything else (an object, an array) is not usable as a label; drop it
	// rather than fail the surrounding item.
	*s = ""
	return nil
}

func (s looseString) String() string { return string(s) }

// --- response envelopes ----------------------------------------------------

type addonCatalogResponse struct {
	Metas []AddonMeta `json:"metas"`
	Err   string      `json:"err,omitempty"`
}

// addonMetaResponse tolerates both shapes seen in the wild. protocol.md's
// example wraps the meta in an array; every real addon returns a bare object.
type addonMetaResponse struct {
	Meta *AddonMeta
	Err  string
}

func (m *addonMetaResponse) UnmarshalJSON(b []byte) error {
	var raw struct {
		Meta json.RawMessage `json:"meta"`
		Err  string          `json:"err"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Err = raw.Err
	if len(raw.Meta) == 0 {
		return nil
	}
	var one AddonMeta
	if err := json.Unmarshal(raw.Meta, &one); err == nil {
		m.Meta = &one
		return nil
	}
	var many []AddonMeta
	if err := json.Unmarshal(raw.Meta, &many); err == nil && len(many) > 0 {
		m.Meta = &many[0]
		return nil
	}
	return nil
}

type addonStreamResponse struct {
	Streams []AddonStream `json:"streams"`
	Err     string        `json:"err,omitempty"`
}

type addonSubtitlesResponse struct {
	Subtitles []AddonSubtitle `json:"subtitles"`
	Err       string          `json:"err,omitempty"`
}

// --- manifest validation ---------------------------------------------------

// ErrAddonInvalid marks a body that parsed as JSON but is not an addon. It is
// kept distinct from a transport failure because the two need opposite fixes:
// one is a wrong URL, the other is a network.
var ErrAddonInvalid = errors.New("not a valid addon manifest")

// validateManifest applies the protocol's own required-field list.
//
// Deliberately no stricter than the specification. A linter may complain that a
// version is not semver or that a description is missing; refusing to install
// over that would reject working addons for a cosmetic reason, and the user
// pasted the URL because they wanted the addon, not a lecture.
func validateManifest(m *AddonManifest) error {
	if m == nil {
		return fmt.Errorf("%w: empty response", ErrAddonInvalid)
	}
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("%w: no id", ErrAddonInvalid)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w: no name", ErrAddonInvalid)
	}
	named := 0
	for _, r := range m.Resources {
		if strings.TrimSpace(r.Name) != "" {
			named++
		}
	}
	if named == 0 {
		// The protocol's own words: an addon "must provide at least 1 resource
		// and a manifest". A manifest with none is a landing page.
		return fmt.Errorf("%w: declares no resources", ErrAddonInvalid)
	}
	if len(m.Types) == 0 && len(m.Catalogs) == 0 {
		allTyped := true
		for _, r := range m.Resources {
			if len(r.Types) == 0 {
				allTyped = false
				break
			}
		}
		if !allTyped {
			return fmt.Errorf("%w: declares no types", ErrAddonInvalid)
		}
	}
	return nil
}

// hasResource reports whether the addon serves a resource by name.
func (m *AddonManifest) hasResource(name string) bool {
	for _, r := range m.Resources {
		if strings.EqualFold(r.Name, name) {
			return true
		}
	}
	return false
}

func (m *AddonManifest) resource(name string) (AddonResource, bool) {
	for _, r := range m.Resources {
		if strings.EqualFold(r.Name, name) {
			return r, true
		}
	}
	return AddonResource{}, false
}

// Handles reports whether it is worth asking this addon for a resource.
//
// This is the protocol's own routing rule and it is worth honouring rather than
// asking everyone everything: a stream addon that declares idPrefixes ["tt"]
// answers an empty list for a `kitsu:` id, and firing that request anyway
// spends a timeout to learn what the manifest already said.
func (m *AddonManifest) Handles(resource, typ, id string) bool {
	r, ok := m.resource(resource)
	if !ok {
		return false
	}
	types := r.Types
	if len(types) == 0 {
		types = m.Types
	}
	if typ != "" && len(types) > 0 && !addonListHas(types, typ) {
		return false
	}
	// The protocol is explicit that idPrefixes does not apply to catalogs,
	// which are addressed by catalog id rather than by content id.
	if strings.EqualFold(resource, "catalog") {
		return true
	}
	prefixes := r.IDPrefixes
	if len(prefixes) == 0 {
		prefixes = m.IDPrefixes
	}
	if id == "" || len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// addonListHas is a plain case-insensitive membership test.
//
// filter.go has a containsFold that looks similar and is not: it answers true
// for an empty list, because there it means "no constraint". Here an empty list
// has already been substituted for the manifest-level one, so a miss is a miss
// and reusing that function would route every request to every addon.
func addonListHas(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// --- the type vocabulary ---------------------------------------------------
//
// THE FIRST IMPROVEMENT, AND IT COSTS ALMOST NOTHING.
//
// Stremio's own documentation lists four content types -- movie, series,
// channel, tv -- and dates that list "as of Apr 2016". Everything downstream in
// Stremio assumes video, which is why a books or games addon has nowhere to go.
//
// Yarr.It already has six canonical domains, and schema.json already carries
// aliases for the words an addon would naturally use: `book`, `audiobook`,
// `comic`, `manga`, `game`, `rom`, `album`, `track`, `image`. So supporting a
// comics addon needs no new vocabulary at all. It needs only that the type on
// the wire is resolved through canonicalDomain instead of compared against a
// hard-coded list of four.
//
// That is the entire change, and it is why stock movie/series addons keep
// working unchanged: `movie` and `series` resolve through exactly the same
// path.

// stremioDialect covers the handful of words that belong to Stremio's protocol
// rather than to Yarr.It's vocabulary, and which schema.json therefore does not
// and should not carry.
//
// Kept here, not in schema.json, on purpose: schema.json is the canonical
// vocabulary for this product, and another protocol's spellings are a
// translation layer, not part of it. It is also frozen.
var stremioDialect = map[string]struct{ domain, typ string }{
	// A Stremio `channel` is a YouTube channel: a named container with an
	// ordered list of videos inside it. That is a series in everything but
	// name.
	"channel": {"video", "series"},
	// `tv` is live output with no duration, which is what a linear channel is.
	// (`tv` already resolves to the video domain through schema.json's
	// aliases; this line is what gives it a usable *type* as well.)
	"tv": {"video", "live_channel"},
}

// Types that are deliberately left unmapped. `other` and `all` are wildcards
// meaning "unspecified", and guessing a domain for them would file a random
// item under a random tab -- which is worse than not showing it, because it is
// wrong somewhere the user cannot see why.
var stremioUnmapped = map[string]bool{"other": true, "all": true}

// canonicalTypeByToken maps a normalised type word onto its canonical spelling
// in schema.json. Built from the same embedded file, so it cannot drift.
//
// Built lazily rather than in an init(), and that is not a style choice. Go
// runs a package's init functions in filename order, and "addon.go" sorts
// before "schema.go" -- so an init here reads schema.Domains while it is still
// empty, and every type resolves to "". The symptom was silent: domains still
// worked, so cards appeared, filed correctly, with a blank type. Deferring to
// first use removes the ordering question entirely.
var canonicalTypeByToken = sync.OnceValue(func() map[string]string {
	m := make(map[string]string)
	for _, d := range schema.Domains {
		for _, t := range d.Types {
			m[normaliseToken(t)] = t
		}
	}
	return m
})

// addonDomain resolves an addon's `type` onto a canonical Yarr.It domain.
//
// Returns "" when the type carries no domain meaning, and callers must read
// that as "do not file this anywhere" rather than "file it under video".
func addonDomain(t string) string {
	tok := normaliseToken(t)
	if tok == "" || stremioUnmapped[tok] {
		return ""
	}
	if d := canonicalDomain(t); d != "" {
		return d
	}
	if m, ok := stremioDialect[tok]; ok {
		return m.domain
	}
	return ""
}

// addonType resolves an addon's `type` onto a canonical Yarr.It media type.
//
// May return "" while addonDomain returns a domain: an addon may serve a
// literature item typed simply `book` when the canonical types are author,
// book, edition, audiobook -- there the type is known -- but an addon serving
// `podcast` lands in no domain at all, which is reported rather than guessed.
func addonType(t string) string {
	tok := normaliseToken(t)
	if tok == "" || stremioUnmapped[tok] {
		return ""
	}
	if ct, ok := canonicalTypeByToken()[tok]; ok {
		return ct
	}
	if m, ok := stremioDialect[tok]; ok {
		return m.typ
	}
	return ""
}

// addonDomains lists the canonical domains a manifest can serve, deduplicated
// and sorted so a provider row is stable between paints.
func addonDomains(m *AddonManifest) []string {
	seen := map[string]bool{}
	collect := func(list []string) {
		for _, t := range list {
			if d := addonDomain(t); d != "" {
				seen[d] = true
			}
		}
	}
	collect(m.Types)
	for _, r := range m.Resources {
		collect(r.Types)
	}
	for _, c := range m.Catalogs {
		collect([]string{c.Type})
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// unmappedTypes reports the type words this manifest declares that Yarr.It
// could not place.
//
// Surfaced rather than swallowed. An addon that serves podcasts is not broken
// and neither are we -- but if its items simply vanished, the only visible
// symptom would be an empty row, which looks exactly like a dead addon.
func unmappedTypes(m *AddonManifest) []string {
	seen := map[string]bool{}
	var out []string
	check := func(list []string) {
		for _, t := range list {
			tok := normaliseToken(t)
			if tok == "" || seen[tok] || stremioUnmapped[tok] {
				continue
			}
			seen[tok] = true
			if addonDomain(t) == "" {
				out = append(out, t)
			}
		}
	}
	check(m.Types)
	for _, r := range m.Resources {
		check(r.Types)
	}
	sort.Strings(out)
	return out
}

// --- the capability bridge -------------------------------------------------
//
// THE SECOND IMPROVEMENT.
//
// A Stremio manifest says what resources it serves and nothing about what it is
// for. `resources: ["stream"]` covers WatchHub, which returns links to Netflix,
// and Torrentio, which returns torrent info-hashes -- from the manifest they
// are indistinguishable, so the UI has to offer both identically and let the
// user find out.
//
// Yarr.It already separates what a provider HOLDS from what it DOES, and every
// other backend is described that way. Translating a manifest into the same
// roles and capabilities means an addon appears in /api/providers next to
// Radarr, is filtered by the same `For(domain, role)` query, and is asked only
// for what it advertises.
//
// The rule this follows, from provider.go: advertise only what is implemented.
// It is why `search` is claimed only when a catalog actually declares the
// `search` extra, rather than for every addon that has a catalog. A feed-only
// catalog answers a search request with its unfiltered feed, which looks like
// a working search returning wrong results -- the worst available outcome.

func addonRoles(m *AddonManifest) []string {
	var out []string
	if m.hasResource("catalog") || m.hasResource("meta") {
		out = append(out, "discovery")
	}
	if m.hasResource("stream") {
		out = append(out, "stream")
		// An addon that warns it serves P2P content is, by the schema's own
		// definition, returning raw downloadable releases rather than
		// canonical works. That is the indexer role, and naming it is what
		// lets a client warn before it exposes an IP to a swarm.
		if m.BehaviorHints.P2P {
			out = append(out, "indexer")
		}
	}
	return out
}

func addonCapabilities(m *AddonManifest) []string {
	// Always health: this client probes the manifest, so the answer is real
	// regardless of what the addon offers.
	out := []string{"health"}
	if addonSupportsSearch(m) {
		out = append(out, "search")
	}
	if m.hasResource("meta") {
		out = append(out, "details")
	}
	if m.hasResource("stream") {
		out = append(out, "stream")
	}
	return out
}

// addonSupportsSearch reports whether any catalog will actually answer a query.
func addonSupportsSearch(m *AddonManifest) bool {
	for _, c := range m.Catalogs {
		if c.supportsExtra("search") {
			return true
		}
	}
	return false
}

// searchableCatalogs returns the catalogs that accept a search argument and
// serve a given domain. Passing "" for domain means every domain.
func searchableCatalogs(m *AddonManifest, domain string) []AddonCatalog {
	var out []AddonCatalog
	for _, c := range m.Catalogs {
		if !c.supportsExtra("search") {
			continue
		}
		if domain != "" && addonDomain(c.Type) != domain {
			continue
		}
		out = append(out, c)
	}
	return out
}

// browsableCatalogs returns the catalogs that will answer without arguments.
func browsableCatalogs(m *AddonManifest, domain string) []AddonCatalog {
	var out []AddonCatalog
	for _, c := range m.Catalogs {
		if c.requiresExtra("search") || c.requiresExtra("genre") {
			continue // Would answer empty; a row that is always empty is noise.
		}
		if domain != "" && addonDomain(c.Type) != domain {
			continue
		}
		out = append(out, c)
	}
	return out
}

// --- where an addon may point ----------------------------------------------
//
// SECURITY. An addon URL is a URL a user pasted that this server will then
// fetch, so this is SSRF by construction and the only question is where the
// line goes.
//
// The line is NOT "refuse RFC1918". Yarr.It is a LAN gateway on purpose --
// transport.js exists entirely to reach a self-hoster's 192.168 box, and
// provider.go's whole population of Radarr, Jellyfin and Plex instances lives
// on private addresses. Refusing private targets here would make the one
// deployment this project is built for the one that cannot use addons.
//
// What must never happen is the server becoming a network scanner for whoever
// is asking. Four things stop that, and they are separable on purpose:
//
//  1. Adding an addon needs a session. An anonymous request cannot make this
//     server fetch anything, so a stranger has no scanner at all.
//  2. The server's OWN surface is refused: loopback, link-local (which is where
//     169.254.169.254 cloud metadata lives), the unspecified address and
//     multicast. A user's LAN is theirs; the machine Yarr.It runs on is not,
//     and on a hosted instance loopback is the single most valuable target.
//     Opt back in with YARRIT_ADDON_ALLOW_LOOPBACK=1 when the whole stack is
//     one box, which is the self-hoster's normal case.
//  3. Ports that are dangerous to speak HTTP at are refused outright. Redis,
//     memcached and Elasticsearch all accept an HTTP request as a sequence of
//     commands; there is no addon on port 6379.
//  4. Probes at private destinations are rate limited. A person adds one or two
//     addons; a scan needs thousands. The bound is what makes the difference
//     visible without getting in a real user's way.
//
// And the knob for the deployment shape this project does not default to:
// YARRIT_ADDON_PRIVATE=deny turns off private destinations entirely, which is
// what a public multi-tenant instance should set, because there "the user's
// LAN" is really the operator's.

// addonPolicy is the decision, held as data so tests can state it.
type addonPolicy struct {
	AllowLoopback bool
	AllowPrivate  bool
}

func loadAddonPolicy() addonPolicy {
	p := addonPolicy{AllowLoopback: false, AllowPrivate: true}
	if v := strings.TrimSpace(os.Getenv("YARRIT_ADDON_ALLOW_LOOPBACK")); v == "1" || strings.EqualFold(v, "true") {
		p.AllowLoopback = true
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("YARRIT_ADDON_PRIVATE")), "deny") {
		p.AllowPrivate = false
	}
	return p
}

// Ports that speak something other than HTTP and can be driven by a request
// that merely looks like one. Refused regardless of address, because a hostile
// addon URL aimed at the LAN is the same attack as one aimed at localhost.
var addonDeniedPorts = map[int]string{
	22: "SSH", 23: "Telnet", 25: "SMTP", 110: "POP3", 135: "RPC",
	137: "NetBIOS", 138: "NetBIOS", 139: "NetBIOS", 143: "IMAP",
	445: "SMB", 465: "SMTPS", 587: "SMTP", 993: "IMAPS", 995: "POP3S",
	1433: "MSSQL", 3306: "MySQL", 3389: "RDP", 5432: "PostgreSQL",
	5900: "VNC", 6379: "Redis", 9200: "Elasticsearch", 11211: "Memcached",
	27017: "MongoDB",
}

// addonURLError is a refusal that can be shown to the person who pasted the
// URL. Like Health.Detail, it says what to do rather than what failed.
type addonURLError struct{ Detail string }

func (e *addonURLError) Error() string { return e.Detail }

// checkAddonURL validates the shape of a URL before any DNS or dial happens.
func (p addonPolicy) checkAddonURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, &addonURLError{Detail: "That is not a URL."}
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "":
		return nil, &addonURLError{Detail: "That address needs to start with http:// or https://."}
	default:
		// ipfs:// and ipns:// are real transports in the protocol and are
		// genuinely not implemented here, so say so rather than pretending the
		// URL is malformed.
		if s := strings.ToLower(u.Scheme); s == "ipfs" || s == "ipns" {
			return nil, &addonURLError{Detail: "IPFS addons are not supported here; use an https:// address."}
		}
		return nil, &addonURLError{Detail: "Only http:// and https:// addresses can be used."}
	}
	if u.User != nil {
		// A username and password in the URL would be sent to whatever the
		// host resolves to, which is a credential handed to a stranger.
		return nil, &addonURLError{Detail: "Remove the username and password from the address; addons are not authenticated that way."}
	}
	if u.Hostname() == "" {
		return nil, &addonURLError{Detail: "That address has no host."}
	}
	if ps := u.Port(); ps != "" {
		n, err := strconv.Atoi(ps)
		if err != nil || n <= 0 || n > 65535 {
			return nil, &addonURLError{Detail: "That address has an invalid port."}
		}
		if name, bad := addonDeniedPorts[n]; bad {
			return nil, &addonURLError{Detail: fmt.Sprintf("Port %d is %s, not a web server. No addon is served there.", n, name)}
		}
	}
	return u, nil
}

// checkAddonAddr is the check that actually matters, because it runs against a
// resolved IP rather than a hostname.
//
// Hostname checks are decorative on their own: a name resolves to whatever its
// owner wants, and can resolve to one address when validated and another when
// dialled. This runs at dial time, on the address about to be connected to.
func (p addonPolicy) checkAddonAddr(ip netip.Addr) error {
	if !ip.IsValid() {
		return &addonURLError{Detail: "That address could not be resolved."}
	}
	ip = ip.Unmap() // ::ffff:127.0.0.1 is 127.0.0.1 and must be judged as one.

	switch {
	case ip.IsUnspecified():
		return &addonURLError{Detail: "That address is not a real destination."}
	case ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsLinkLocalMulticast():
		return &addonURLError{Detail: "That address is a multicast group, not a server."}
	case ip.IsLinkLocalUnicast():
		// 169.254.0.0/16 and fe80::/10. This is where cloud instance metadata
		// lives (169.254.169.254), which hands out credentials to anything that
		// asks. There is no legitimate addon here.
		return &addonURLError{Detail: "Link-local addresses cannot be used for addons."}
	case ip.IsLoopback():
		if !p.AllowLoopback {
			return &addonURLError{Detail: "That address points back at the Yarr.It server itself. Set YARRIT_ADDON_ALLOW_LOOPBACK=1 if the addon really does run on the same machine."}
		}
	case isPrivateAddr(ip):
		if !p.AllowPrivate {
			return &addonURLError{Detail: "This instance does not allow addons on private addresses."}
		}
	}
	return nil
}

// isPrivateAddr covers the ranges a self-hoster actually uses.
//
// Carrier-grade NAT (100.64.0.0/10) is in here deliberately and is not an
// oversight: that is the range Tailscale hands out, and a large share of
// self-hosters reach their own boxes through it.
func isPrivateAddr(ip netip.Addr) bool {
	if ip.IsPrivate() { // 10/8, 172.16/12, 192.168/16, fc00::/7
		return true
	}
	if ip.Is4() {
		b := ip.As4()
		if b[0] == 100 && b[1] >= 64 && b[1] <= 127 {
			return true // 100.64.0.0/10, CGNAT / Tailscale
		}
	}
	return false
}

// --- rate limiting private probes ------------------------------------------

// addonRateLimiter bounds how often this server will reach at a private
// address on someone's say-so.
//
// A person adding an addon does it once, maybe twice. A scan needs one probe
// per host per port. Nothing here stops a determined single lookup, and that is
// the point -- the bound is set where a human never notices it and an
// enumeration cannot finish.
type addonRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	events []time.Time
	now    func() time.Time // test seam
}

func newAddonRateLimiter(limit int, window time.Duration) *addonRateLimiter {
	return &addonRateLimiter{limit: limit, window: window, now: time.Now}
}

func (l *addonRateLimiter) allow() bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cut := now.Add(-l.window)
	keep := l.events[:0]
	for _, t := range l.events {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.events = keep
	if len(l.events) >= l.limit {
		return false
	}
	l.events = append(l.events, now)
	return true
}

// --- the client ------------------------------------------------------------

// AddonClient fetches from addons under the policy above.
//
// It owns its own http.Client with no cookie jar, and every request it builds
// starts empty. That is how the rule "never forward the viewer's cookies or a
// provider API key to an addon" is kept: there is no path for a header to
// arrive here from an inbound request, because inbound requests are never
// passed in -- only a URL and a context are.
type AddonClient struct {
	policy  addonPolicy
	hc      *http.Client
	limiter *addonRateLimiter
}

type addonDialKey struct{}

// addonDialNote is where the dialer reports back what it actually connected to.
type addonDialNote struct {
	mu      sync.Mutex
	private bool
	ip      string
}

func NewAddonClient() *AddonClient {
	return NewAddonClientWith(loadAddonPolicy(), newAddonRateLimiter(12, time.Minute))
}

func NewAddonClientWith(p addonPolicy, limiter *addonRateLimiter) *AddonClient {
	c := &AddonClient{policy: p, limiter: limiter}

	base := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// The whole SSRF check happens here, on the address about to be
		// connected to, because a check anywhere earlier is a check on a name
		// that can resolve differently a millisecond later.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, &addonURLError{Detail: "That address could not be understood."}
			}
			ips, err := base.Resolver.LookupNetIP(ctx, "ip", host)
			if err != nil || len(ips) == 0 {
				return nil, &addonURLError{Detail: "That host name could not be resolved."}
			}
			note, _ := ctx.Value(addonDialKey{}).(*addonDialNote)

			var firstErr error
			for _, ip := range ips {
				ip = ip.Unmap()
				if err := c.policy.checkAddonAddr(ip); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				if note != nil {
					note.mu.Lock()
					note.private = isPrivateAddr(ip) || ip.IsLoopback()
					note.ip = ip.String()
					note.mu.Unlock()
				}
				// Dialled by literal address, so the connection lands on the
				// address that was checked and not on a second answer to the
				// same name.
				conn, derr := base.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if derr == nil {
					return conn, nil
				}
				if firstErr == nil {
					firstErr = derr
				}
			}
			if firstErr == nil {
				firstErr = &addonURLError{Detail: "That address could not be reached."}
			}
			return nil, firstErr
		},
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	c.hc = &http.Client{
		Transport: transport,
		// No cookie jar, stated rather than implied. A jar would let one addon
		// set a cookie that a later request to a different addon carried.
		Jar: nil,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= addonMaxRedirects {
				return &addonURLError{Detail: "That address redirects too many times."}
			}
			// Every hop is re-checked, not just cross-host ones: a redirect to
			// the same host on port 6379, or from a public host to its own
			// 127.0.0.1, is exactly the move this is here to stop.
			if _, err := c.policy.checkAddonURL(req.URL.String()); err != nil {
				return err
			}
			// Nothing is carried across a hop. Go copies most headers on a
			// same-host redirect; starting clean means an Authorization header
			// could not survive one even if something upstream added it.
			req.Header = addonHeaders()
			return nil
		},
	}
	return c
}

func addonHeaders() http.Header {
	h := make(http.Header, 2)
	h.Set("Accept", "application/json")
	h.Set("User-Agent", addonUserAgent)
	return h
}

// get performs one bounded request and decodes into out.
func (c *AddonClient) get(ctx context.Context, raw string, maxBytes int64, out any) error {
	u, err := c.policy.checkAddonURL(raw)
	if err != nil {
		return err
	}

	note := &addonDialNote{}
	ctx = context.WithValue(ctx, addonDialKey{}, note)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return &addonURLError{Detail: "That address could not be requested."}
	}
	// Built from nothing. No inbound request is in scope here, so there is
	// nothing to accidentally copy.
	req.Header = addonHeaders()

	resp, err := c.hc.Do(req)
	if err != nil {
		// Charge the limiter for reaching at a private address whether or not
		// the connection succeeded -- a refused connection is exactly the
		// answer a scanner is looking for.
		c.chargeIfPrivate(note)
		var ue *addonURLError
		if errors.As(err, &ue) {
			return ue
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return &addonURLError{Detail: "That addon did not answer in time."}
		}
		return &addonURLError{Detail: "Could not reach that addon."}
	}
	defer resp.Body.Close()
	c.chargeIfPrivate(note)

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &addonHTTPError{Status: resp.StatusCode}
	case resp.StatusCode >= 400:
		return &addonHTTPError{Status: resp.StatusCode}
	}

	// Read one byte past the cap so an oversized body is detected rather than
	// silently truncated into a parse error that blames the addon's JSON.
	lr := io.LimitReader(resp.Body, maxBytes+1)
	body, err := io.ReadAll(lr)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &addonURLError{Detail: "That addon stopped responding partway through."}
		}
		return &addonURLError{Detail: "That addon's response could not be read."}
	}
	if int64(len(body)) > maxBytes {
		return &addonURLError{Detail: fmt.Sprintf("That addon's response is larger than %s and was refused.", humanSize(maxBytes))}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: the response was not addon JSON", ErrAddonInvalid)
	}
	return nil
}

func (c *AddonClient) chargeIfPrivate(note *addonDialNote) {
	if note == nil || c.limiter == nil {
		return
	}
	note.mu.Lock()
	private := note.private
	note.mu.Unlock()
	if private {
		c.limiter.allow()
	}
}

// budgetForPrivate takes a token before a fetch that may land on a private
// address, so an exhausted budget refuses before any packet leaves.
func (c *AddonClient) budgetForPrivate(host string) error {
	if c.limiter == nil {
		return nil
	}
	// Only a literal address can be judged before resolution. A hostname is
	// charged after the dial instead, by chargeIfPrivate, once the address it
	// actually landed on is known -- charging it here would spend the LAN
	// budget on every ordinary install from the internet.
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	ip = ip.Unmap()
	if !isPrivateAddr(ip) && !ip.IsLoopback() {
		return nil
	}
	if !c.limiter.allow() {
		return &addonURLError{Detail: "Too many addon addresses have been tried recently. Wait a minute and try again."}
	}
	return nil
}

// addonHTTPError is an answer that was not a 2xx. Kept apart from a transport
// failure because 401 means "bring a key" and a refused connection means "wrong
// address", and telling someone the wrong one costs an hour.
type addonHTTPError struct{ Status int }

func (e *addonHTTPError) Error() string { return fmt.Sprintf("addon answered %d", e.Status) }

// --- protocol calls --------------------------------------------------------

// addonBase strips a trailing /manifest.json to get the resource root.
//
// Users paste the manifest URL because that is what every addon's install
// button gives them, and resource paths hang off its parent.
func addonBase(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	if i := strings.LastIndex(strings.ToLower(s), "/manifest.json"); i >= 0 && i == len(s)-len("/manifest.json") {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "/")
}

func addonManifestURL(raw string) string {
	return addonBase(raw) + "/manifest.json"
}

// FetchManifest retrieves and validates an addon's manifest.
func (c *AddonClient) FetchManifest(ctx context.Context, raw string) (*AddonManifest, error) {
	u, err := c.policy.checkAddonURL(raw)
	if err != nil {
		return nil, err
	}
	if err := c.budgetForPrivate(u.Hostname()); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, addonManifestTimeout)
	defer cancel()

	var m AddonManifest
	if err := c.get(ctx, addonManifestURL(raw), addonManifestMaxBytes, &m); err != nil {
		return nil, err
	}
	if err := validateManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// resourceURL builds /{resource}/{type}/{id}.json, with extra arguments in the
// path segment the protocol specifies rather than as a query string.
func addonResourceURL(base, resource, typ, id string, extra url.Values) string {
	p := addonBase(base) + "/" + url.PathEscape(resource) + "/" + url.PathEscape(typ) + "/" + url.PathEscape(id)
	if seg := addonExtraSegment(extra); seg != "" {
		// The protocol encodes extras as a query-string-shaped PATH segment:
		// /catalog/movie/top/search=dune.json -- not as ?search=dune.
		p += "/" + seg
	}
	return p + ".json"
}

// addonExtraSegment builds that segment.
//
// url.Values.Encode is deliberately not used. It encodes a space as "+", which
// means a space only inside a query string; this is a PATH segment, where "+"
// is a literal plus. The protocol's own example spells it
// "search=game%20of%20thrones", and %20 is unambiguous in both readings.
//
// Measured, so the reason is not overstated: Cinemeta accepts either form --
// "search=big+buck+bunny.json" and "search=big%20buck%20bunny.json" both return
// the same 7 results -- because it re-parses the segment as a query string. An
// addon that reads the segment as what it actually is would see the pluses.
// %20 is correct for both kinds of addon; "+" is correct only for one.
//
// Keys are sorted so the same request produces the same URL, which is what
// makes an addon's own caching work.
func addonExtraSegment(extra url.Values) string {
	if len(extra) == 0 {
		return ""
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		for _, v := range extra[k] {
			parts = append(parts, escapeExtra(k)+"="+escapeExtra(v))
		}
	}
	return strings.Join(parts, "&")
}

func escapeExtra(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Catalog fetches one catalog page, optionally searched or filtered.
func (c *AddonClient) Catalog(ctx context.Context, base, typ, id string, extra url.Values) ([]AddonMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, addonResourceTimeout)
	defer cancel()
	var resp addonCatalogResponse
	if err := c.get(ctx, addonResourceURL(base, "catalog", typ, id, extra), addonResourceMaxBytes, &resp); err != nil {
		return nil, err
	}
	if resp.Err != "" {
		return nil, fmt.Errorf("addon reported: %s", resp.Err)
	}
	return resp.Metas, nil
}

// Meta fetches the details for one item.
func (c *AddonClient) Meta(ctx context.Context, base, typ, id string) (*AddonMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, addonResourceTimeout)
	defer cancel()
	var resp addonMetaResponse
	if err := c.get(ctx, addonResourceURL(base, "meta", typ, id, nil), addonResourceMaxBytes, &resp); err != nil {
		return nil, err
	}
	if resp.Err != "" {
		return nil, fmt.Errorf("addon reported: %s", resp.Err)
	}
	if resp.Meta == nil {
		return nil, fmt.Errorf("%w: no meta in response", ErrAddonInvalid)
	}
	return resp.Meta, nil
}

// Streams fetches the playable options for one video id.
func (c *AddonClient) Streams(ctx context.Context, base, typ, id string) ([]AddonStream, error) {
	ctx, cancel := context.WithTimeout(ctx, addonResourceTimeout)
	defer cancel()
	var resp addonStreamResponse
	if err := c.get(ctx, addonResourceURL(base, "stream", typ, id, nil), addonResourceMaxBytes, &resp); err != nil {
		return nil, err
	}
	if resp.Err != "" {
		return nil, fmt.Errorf("addon reported: %s", resp.Err)
	}
	return resp.Streams, nil
}

// Subtitles fetches subtitle tracks for one video id.
func (c *AddonClient) Subtitles(ctx context.Context, base, typ, id string, extra url.Values) ([]AddonSubtitle, error) {
	ctx, cancel := context.WithTimeout(ctx, addonResourceTimeout)
	defer cancel()
	var resp addonSubtitlesResponse
	if err := c.get(ctx, addonResourceURL(base, "subtitles", typ, id, extra), addonResourceMaxBytes, &resp); err != nil {
		return nil, err
	}
	if resp.Err != "" {
		return nil, fmt.Errorf("addon reported: %s", resp.Err)
	}
	return resp.Subtitles, nil
}

// --- translation into Yarr.It's shapes -------------------------------------

// addonCanonicalID routes an item back to the addon that knows about it.
//
// The addon id is embedded rather than looked up, because two installed addons
// may both serve `tt0068646` and a request has to reach the one whose row was
// clicked.
func addonCanonicalID(addonID, typ, id string) string {
	return "addon:" + addonID + ":" + typ + ":" + id
}

type addonRef struct {
	AddonID string
	Type    string
	ID      string
}

func parseAddonRef(s string) (addonRef, error) {
	if !strings.HasPrefix(s, "addon:") {
		return addonRef{}, fmt.Errorf("not an addon id: %q", s)
	}
	// Split from the left for addon id and type, then keep the whole remainder
	// as the item id: Stremio episode ids contain colons ("tt0898266:9:17"),
	// so splitting on every colon would truncate them to the series.
	rest := strings.TrimPrefix(s, "addon:")
	first := strings.Index(rest, ":")
	if first < 0 {
		return addonRef{}, fmt.Errorf("malformed addon id: %q", s)
	}
	addonID := rest[:first]
	rest = rest[first+1:]
	second := strings.Index(rest, ":")
	if second < 0 {
		return addonRef{}, fmt.Errorf("malformed addon id: %q", s)
	}
	ref := addonRef{AddonID: addonID, Type: rest[:second], ID: rest[second+1:]}
	if ref.AddonID == "" || ref.Type == "" || ref.ID == "" {
		return addonRef{}, fmt.Errorf("malformed addon id: %q", s)
	}
	return ref, nil
}

// toMediaItem converts an addon's meta object into the canonical shape every
// other provider returns.
func (m AddonMeta) toMediaItem(providerID, addonID string) MediaItem {
	typ := m.Type
	item := MediaItem{
		CanonicalID:    addonCanonicalID(addonID, typ, m.ID),
		Domain:         addonDomain(typ),
		Type:           addonType(typ),
		Title:          strings.TrimSpace(m.Name),
		Overview:       m.Description,
		Artwork:        m.Poster,
		ProviderID:     providerID,
		ProviderItemID: m.ID,
		Year:           addonYear(m),
	}
	if item.Artwork == "" {
		item.Artwork = m.Background
	}
	return item
}

// AllGenres merges the two spellings, deduplicated.
//
// Measured against the live Cinemeta rather than assumed: it sends BOTH
// `genre` and `genres`, with the same values in each. Concatenating them --
// the obvious thing, and what a reader of the documentation alone would do --
// renders "Animation, Short, Comedy, Animation, Short, Comedy" on the details
// page. Case is folded for the comparison but the first spelling seen is what
// is kept, so "Sci-Fi" is not turned into "sci-fi" on screen.
func (m AddonMeta) AllGenres() []string {
	seen := make(map[string]bool, len(m.Genres)+len(m.Genre))
	out := make([]string, 0, len(m.Genres)+len(m.Genre))
	for _, list := range [][]string{m.Genres, m.Genre} {
		for _, g := range list {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			k := strings.ToLower(g)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, g)
		}
	}
	return out
}

// addonYear pulls a four-digit year out of whichever field carries it.
//
// releaseInfo is documented; year is what Cinemeta sends; released is an ISO
// timestamp. A range like "2000-2014" yields its first year, which is what a
// card shows.
func addonYear(m AddonMeta) int {
	for _, s := range []string{m.Year.String(), m.ReleaseInfo.String(), m.Released} {
		if y := firstYear(s); y > 0 {
			return y
		}
	}
	return 0
}

func firstYear(s string) int {
	run := 0
	start := -1
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] >= '0' && s[i] <= '9' {
			if run == 0 {
				start = i
			}
			run++
			continue
		}
		if run == 4 {
			n, err := strconv.Atoi(s[start : start+4])
			// Sanity bound: an id fragment or a runtime should not become a
			// release year.
			if err == nil && n >= 1870 && n <= 2200 {
				return n
			}
		}
		run = 0
	}
	return 0
}

// --- the Provider bridge ---------------------------------------------------
//
// THE THIRD IMPROVEMENT.
//
// An installed addon becomes a Provider, so it lands in the same Registry as
// Radarr and Jellyfin. Everything already built on that contract then applies
// with no further wiring: /api/providers probes it concurrently and bounded,
// reports one of six health states rather than "down", and a slow addon costs
// its own row instead of the settings page it would be fixed from.
//
// This is the difference that shows up on a bad day. In a client that treats
// addons as an opaque list, one addon that hangs makes the whole screen hang;
// here it is a row that says "did not answer in time" beside eight that work.

// Deliberately stateless beyond its configuration. An earlier draft cached the
// last Health here; nothing ever read it, and a cached health is a health that
// can be stale at exactly the moment somebody opens the screen to find out why
// something is broken. Every probe is live and bounded instead.
type addonProvider struct {
	client   *AddonClient
	url      string // the manifest URL as installed
	manifest *AddonManifest
}

func newAddonProvider(c *AddonClient, manifestURL string, m *AddonManifest) *addonProvider {
	return &addonProvider{client: c, url: manifestURL, manifest: m}
}

func (p *addonProvider) ID() string   { return "addon:" + p.manifest.ID }
func (p *addonProvider) Name() string { return p.manifest.Name }

func (p *addonProvider) Domains() []string      { return addonDomains(p.manifest) }
func (p *addonProvider) Roles() []string        { return addonRoles(p.manifest) }
func (p *addonProvider) Capabilities() []string { return addonCapabilities(p.manifest) }

// Health re-fetches the manifest, which is the only thing every addon must
// serve. It is also the cheapest possible probe and the one whose failure
// modes map exactly onto the schema's states.
func (p *addonProvider) Health(ctx context.Context) Health {
	// An addon that says it cannot work without configuration is not broken and
	// must not be reported as such -- that is precisely the distinction
	// not_configured exists to draw, and Stremio's own manifest already carries
	// the flag that answers it.
	if p.manifest.BehaviorHints.ConfigurationRequired {
		return Health{
			State:   HealthNotConfigured,
			Version: p.manifest.Version,
			Detail:  "This addon needs to be configured on its own site before it will answer.",
		}
	}

	m, err := p.client.FetchManifest(ctx, p.url)
	if err != nil {
		return addonHealthFromError(err, p.manifest.Version)
	}

	h := Health{State: HealthOK, Version: m.Version}
	if len(addonDomains(m)) == 0 {
		// It answers, it is valid, and nothing it serves can be filed anywhere
		// in this product. Degraded is the honest word: usable in part, not
		// usable as advertised. The types are named even here -- especially
		// here, because "nothing works" with no noun is the least actionable
		// message a settings screen can carry.
		detail := "This addon answers, but none of the content types it serves map onto anything Yarr.It can show."
		if un := unmappedTypes(m); len(un) > 0 {
			detail = "This addon only serves content types Yarr.It cannot show: " + strings.Join(un, ", ") + "."
		}
		h = Health{State: HealthDegraded, Version: m.Version, Detail: detail}
	} else if un := unmappedTypes(m); len(un) > 0 {
		h = Health{
			State:   HealthDegraded,
			Version: m.Version,
			Detail:  "Some of this addon's content types are not supported here: " + strings.Join(un, ", ") + ".",
		}
	}
	return h
}

// addonHealthFromError maps a failure onto the schema's states. The mapping is
// the point: each state implies a different fix, and collapsing them into
// "down" sends someone hunting a network fault that a wrong URL explains.
func addonHealthFromError(err error, version string) Health {
	var he *addonHTTPError
	if errors.As(err, &he) {
		switch {
		case he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden:
			return Health{State: HealthAuthFailed, Version: version,
				Detail: "This addon refused the request. It probably needs a configured URL of its own."}
		case he.Status == http.StatusNotFound:
			return Health{State: HealthIncompatible, Version: version,
				Detail: "No manifest at that address. Check the URL ends in /manifest.json."}
		default:
			return Health{State: HealthUnreachable, Version: version,
				Detail: fmt.Sprintf("This addon answered %d.", he.Status)}
		}
	}
	if errors.Is(err, ErrAddonInvalid) {
		return Health{State: HealthIncompatible, Version: version,
			Detail: "That address answers, but not with an addon manifest."}
	}
	var ue *addonURLError
	if errors.As(err, &ue) {
		return Health{State: HealthUnreachable, Version: version, Detail: ue.Detail}
	}
	return Health{State: HealthUnreachable, Version: version, Detail: "Could not reach this addon."}
}

// Search makes an addon answer the same question every other Searcher does.
//
// Only catalogs that declare the search extra are asked. A feed-only catalog
// would answer a search with its unfiltered feed, and results that ignore the
// query are worse than none: they look like an answer.
func (p *addonProvider) Search(ctx context.Context, query, domain string) ([]MediaItem, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	want := canonicalDomain(domain)
	cats := searchableCatalogs(p.manifest, want)
	if len(cats) == 0 {
		return nil, nil
	}

	var (
		mu    sync.Mutex
		out   []MediaItem
		wg    sync.WaitGroup
		first error
	)
	for _, cat := range cats {
		wg.Add(1)
		go func(cat AddonCatalog) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					mu.Lock()
					if first == nil {
						first = fmt.Errorf("catalog %q failed", cat.ID)
					}
					mu.Unlock()
				}
			}()
			extra := url.Values{}
			extra.Set("search", query)
			metas, err := p.client.Catalog(ctx, p.url, cat.Type, cat.ID, extra)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			for _, m := range metas {
				if m.Type == "" {
					m.Type = cat.Type
				}
				item := m.toMediaItem(p.ID(), p.manifest.ID)
				if item.Domain == "" || item.Title == "" {
					continue // Nowhere to file it, or nothing to label it with.
				}
				out = append(out, item)
			}
		}(cat)
	}
	wg.Wait()

	if len(out) == 0 && first != nil {
		return nil, first
	}
	return out, nil
}

// Stream resolves a canonical addon id to something playable.
//
// The first stream with a directly usable URL wins. Torrent and YouTube streams
// are legitimate protocol answers but are not a URL the player can open, so
// they are reported as not directly playable rather than handed over as a
// broken link.
func (p *addonProvider) Stream(ctx context.Context, canonicalID string, _ StreamOptions) (StreamSource, error) {
	ref, err := parseAddonRef(canonicalID)
	if err != nil {
		return StreamSource{}, err
	}
	if !p.manifest.Handles("stream", ref.Type, ref.ID) {
		return StreamSource{}, fmt.Errorf("this addon does not serve streams for %q", ref.ID)
	}
	streams, err := p.client.Streams(ctx, p.url, ref.Type, ref.ID)
	if err != nil {
		return StreamSource{}, err
	}
	for _, s := range streams {
		if s.URL != "" {
			return StreamSource{
				URL:      s.URL,
				MimeType: "",
				// notWebReady is the addon telling us the URL is not something
				// a browser can play directly. Honest capability reporting: a
				// client that trusts it can transcode instead of failing at
				// the video element.
				DirectPlay: !s.BehaviorHints.NotWebReady,
				Seekable:   !s.BehaviorHints.NotWebReady,
			}, nil
		}
	}
	if len(streams) == 0 {
		return StreamSource{}, fmt.Errorf("this addon has no streams for %q", ref.ID)
	}
	return StreamSource{}, fmt.Errorf("this addon's streams for %q need a torrent or external player", ref.ID)
}

// compile-time proof the bridge really satisfies the contracts it claims.
var (
	_ Provider = (*addonProvider)(nil)
	_ Searcher = (*addonProvider)(nil)
	_ Streamer = (*addonProvider)(nil)
)
