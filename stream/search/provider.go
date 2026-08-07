package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The provider contract.
//
// Every backend -- Radarr, Sonarr, Lidarr, ROMarr, Comicarr, Jellyfin, Plex,
// Komga, a linear-TV engine -- reaches Yarr.It through this. The design rule
// that matters: a provider advertises what it can do, and callers ask. The
// alternative is a growing `if type ==` tree in every screen, which is how a
// universal front door turns back into six special cases.
//
// Only Provider is mandatory. Everything else is a small optional interface,
// discovered by type assertion. That is deliberate: Radarr has no guide and
// never will, so there must be no Guide() method for it to stub out with an
// error nobody reads.

// Health states are defined in schema.json so the client renders the same set.
// They are deliberately not collapsed into "down" -- each implies a different
// fix, and "unreachable" sends someone hunting a network fault that a missing
// API key would have explained in one line.
type HealthState string

const (
	HealthOK            HealthState = "healthy"
	HealthNotConfigured HealthState = "not_configured"
	HealthUnreachable   HealthState = "unreachable"
	HealthAuthFailed    HealthState = "auth_failed"
	HealthIncompatible  HealthState = "incompatible"
	HealthDegraded      HealthState = "degraded"
)

type Health struct {
	State   HealthState `json:"state"`
	Version string      `json:"version,omitempty"`
	// Detail is shown to the operator, so it must say what to do, not what
	// went wrong internally. "Set an API key" beats "401 Unauthorized".
	Detail string `json:"detail,omitempty"`
}

// Provider is the minimum every backend implements.
type Provider interface {
	// ID is stable across restarts and unique across instances: a user may run
	// Radarr and Radarr-4K, and requests must route to the right one.
	ID() string
	Name() string

	// Domains this instance handles, as canonical ids from schema.json.
	Domains() []string
	// Roles it performs: discovery, acquisition, library, stream, reader,
	// play, linear-tv, epg, indexer.
	Roles() []string
	// Capabilities it actually implements, from schema.json's list.
	Capabilities() []string

	// Health must never block for long. It is called to paint a settings
	// screen, and one unreachable instance must not stall the page that would
	// let the user fix it.
	Health(ctx context.Context) Health
}

// --- optional interfaces, discovered by assertion --------------------------

// Searcher answers "what exists", whether or not the user has it.
type Searcher interface {
	Search(ctx context.Context, query string, domain string) ([]MediaItem, error)
}

// LibraryProvider answers "what do I already have".
type LibraryProvider interface {
	Library(ctx context.Context, domain string) ([]MediaItem, error)
	// LibraryStatus answers for one item, which is the common case when
	// painting a search result and far cheaper than fetching everything.
	LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error)
}

// Requester can be asked to obtain something.
type Requester interface {
	Request(ctx context.Context, item MediaItem, opts RequestOptions) (RequestResult, error)
}

// ActivityProvider reports what is in flight: searching, downloading,
// importing, verifying.
type ActivityProvider interface {
	Activity(ctx context.Context) ([]ActivityItem, error)
}

// Streamer serves bytes for playback.
type Streamer interface {
	Stream(ctx context.Context, canonicalID string, opts StreamOptions) (StreamSource, error)
}

// ChannelProvider presents continuously running channels. Implemented by the
// native linear-TV engine, an M3U source, a tuner, or an adapter to a separate
// NostalgiaVision installation.
type ChannelProvider interface {
	Channels(ctx context.Context) ([]Channel, error)
}

// GuideProvider supplies schedule data. Separate from ChannelProvider because
// a tuner may list channels while its guide comes from an XMLTV file.
type GuideProvider interface {
	Guide(ctx context.Context, channelIDs []string, from, to int64) ([]Program, error)
}

// --- shared value types ----------------------------------------------------

// LibraryState is what the user already has, and is deliberately richer than a
// boolean: "requested" and "downloading" are the states a UI must distinguish
// to avoid offering a request that is already in flight.
type LibraryState string

const (
	StateUnknown     LibraryState = "unknown"
	StateMissing     LibraryState = "missing"
	StateRequested   LibraryState = "requested"
	StateDownloading LibraryState = "downloading"
	StateImporting   LibraryState = "importing"
	StateAvailable   LibraryState = "available"
)

// MediaItem is the canonical shape every provider returns, whatever it holds.
// Domain and Type are values from schema.json, never provider-specific words.
type MediaItem struct {
	CanonicalID string `json:"canonicalId"`
	Domain      string `json:"domain"`
	Type        string `json:"type"`

	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Year     int    `json:"year,omitempty"`
	Overview string `json:"overview,omitempty"`
	Artwork  string `json:"artwork,omitempty"`

	// Which provider said so, and its own id for the thing -- needed to route
	// a request back to the instance that can act on it.
	ProviderID     string `json:"providerId"`
	ProviderItemID string `json:"providerItemId,omitempty"`

	State LibraryState `json:"state,omitempty"`

	Season  int `json:"season,omitempty"`
	Episode int `json:"episode,omitempty"`
}

type RequestOptions struct {
	// Season 0 with Episode 0 means the whole thing.
	Season  int  `json:"season,omitempty"`
	Episode int  `json:"episode,omitempty"`
	Monitor bool `json:"monitor,omitempty"`
}

type RequestResult struct {
	Accepted bool   `json:"accepted"`
	Detail   string `json:"detail,omitempty"`
}

type ActivityItem struct {
	CanonicalID string  `json:"canonicalId"`
	Title       string  `json:"title"`
	Domain      string  `json:"domain"`
	ProviderID  string  `json:"providerId"`
	Stage       string  `json:"stage"`
	Progress    float64 `json:"progress,omitempty"`
	Detail      string  `json:"detail,omitempty"`
}

type StreamOptions struct {
	// Offset is where playback should begin. For a linear channel this is
	// computed from the schedule, not from where this viewer left off.
	OffsetSeconds float64 `json:"offsetSeconds,omitempty"`
}

type StreamSource struct {
	URL        string `json:"url"`
	MimeType   string `json:"mimeType,omitempty"`
	DirectPlay bool   `json:"directPlay"`
	// Seekable is honest capability reporting: a live source that cannot seek
	// must say so, because tuning into a scheduled programme mid-way depends
	// on it and silently starting from zero is the wrong answer.
	Seekable bool `json:"seekable"`
}

type Channel struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Name   string `json:"name"`
	Logo   string `json:"logo,omitempty"`
	// Source is a value from schema.json's channelSources. Clients must not
	// branch on it; it exists for diagnostics and settings.
	Source     string `json:"source"`
	ProviderID string `json:"providerId"`
}

type Program struct {
	ChannelID string `json:"channelId"`
	// Unix seconds, UTC. Stored as UTC everywhere and rendered in the viewer's
	// zone; a schedule that drifts an hour twice a year is not a TV guide.
	StartTime int64 `json:"startTime"`
	EndTime   int64 `json:"endTime"`

	CanonicalID string `json:"canonicalId,omitempty"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle,omitempty"`
	Season      int    `json:"season,omitempty"`
	Episode     int    `json:"episode,omitempty"`
	Description string `json:"description,omitempty"`
	Artwork     string `json:"artwork,omitempty"`
}

// --- registry --------------------------------------------------------------

// Registry holds configured provider instances and answers "who can do this".
// Multiple instances of one type are normal: Radarr and Radarr-4K, Sonarr-TV
// and Sonarr-Anime.
type Registry struct {
	mu        sync.RWMutex
	providers []Provider
}

func (r *Registry) Add(p Provider) error {
	if p.ID() == "" {
		return fmt.Errorf("provider %q has no id", p.Name())
	}
	// Domains are validated on the way in. A provider claiming "movies"
	// instead of "video" would route nothing and report nothing wrong -- the
	// original bug, one layer up.
	for _, d := range p.Domains() {
		if canonicalDomain(d) != d {
			return fmt.Errorf("provider %q claims domain %q, which is not canonical (did you mean %q?)",
				p.ID(), d, canonicalDomain(d))
		}
	}
	for _, role := range p.Roles() {
		if _, ok := schema.Roles[role]; !ok {
			return fmt.Errorf("provider %q claims unknown role %q", p.ID(), role)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.providers {
		if existing.ID() == p.ID() {
			return fmt.Errorf("provider id %q is already registered", p.ID())
		}
	}
	r.providers = append(r.providers, p)
	return nil
}

func (r *Registry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

// For returns providers that handle a domain in a role, in registration order.
// Both arguments are canonicalised, so a caller may say "movies".
func (r *Registry) For(domain, role string) []Provider {
	want := canonicalDomain(domain)
	var out []Provider
	for _, p := range r.All() {
		if role != "" && !contains(p.Roles(), role) {
			continue
		}
		if want != "" && !containsDomain(p.Domains(), want) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Domains lists every domain any configured provider can serve, so the UI can
// show only the tabs that will actually answer.
func (r *Registry) Domains() []string {
	seen := map[string]bool{}
	for _, p := range r.All() {
		for _, d := range p.Domains() {
			seen[d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func containsDomain(list []string, canonical string) bool {
	for _, s := range list {
		if canonicalDomain(s) == canonical {
			return true
		}
	}
	return false
}
