package main

// The channel model for the native linear-TV engine.
//
// A channel here is *data*. Nothing in this file knows that a "90s Cartoons"
// channel or a "Horror" channel might exist: a channel is a row a user wrote,
// carrying a rule set that selects programmes out of a library and a strategy
// that orders them. The moment a channel list is hardcoded, the feature stops
// being "build your own television" and becomes a fixed lineup someone has to
// ship a release to change.
//
// The rule set is previewable on purpose. "247 programmes matched" before you
// press save is the difference between building a channel and guessing at one;
// a channel that turns out to be empty only once it is on air is the failure
// this preview exists to prevent.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	// The IANA zone database, embedded in the binary.
	//
	// A channel's timezone is data typed by a user, so the set of zones needed
	// cannot be known at build time. Without this import the answer depends on
	// whether the host happens to ship /usr/share/zoneinfo -- which a scratch
	// container and a Windows box both get wrong in different ways, and which
	// would turn "my guide is an hour out" into an environment bug nobody can
	// reproduce. ~450KB of binary buys a guide that is right everywhere.
	_ "time/tzdata"
)

// --- what a channel is made of ---------------------------------------------

// LinearItem is one schedulable thing. It embeds MediaItem rather than
// restating it: the canonical id, domain, type, title, season and episode are
// already defined once in provider.go and a second copy would drift.
//
// The extra fields are the ones scheduling and rules need and a search result
// does not: how long it runs, and the facets a channel is built out of.
type LinearItem struct {
	MediaItem

	// DurationSeconds is what makes an item schedulable at all. An item that
	// does not know its own length cannot be given a slot, and pretending it
	// is 30 minutes long puts the whole channel out of sync with reality by
	// however much it was wrong.
	DurationSeconds int `json:"durationSeconds"`

	Genres     []string `json:"genres,omitempty"`
	Network    string   `json:"network,omitempty"`
	Collection string   `json:"collection,omitempty"`
	Rating     string   `json:"rating,omitempty"`

	// SeriesID groups episodes that must play in order. Empty for a film.
	SeriesID string `json:"seriesId,omitempty"`
	// LibraryID is which source library this came from, so a channel can be
	// scoped to some of a provider's libraries and not all of them.
	LibraryID string `json:"libraryId,omitempty"`
}

func (it LinearItem) Duration() time.Duration {
	return time.Duration(it.DurationSeconds) * time.Second
}

// schedulable is the guard that stops a malformed library from hanging the
// scheduler. A zero-length item placed in a slot advances the clock by zero,
// and the fill loop then runs forever inside one instant.
func (it LinearItem) schedulable() bool { return it.DurationSeconds > 0 }

// seriesKey is the unit that "preserve episode order" applies to.
//
// A film gets a key of its own, which is deliberate: it makes CYCLIC_SHUFFLE
// on a film library degrade to a plain shuffle instead of needing a second
// code path for libraries with no episodes in them.
func (it LinearItem) seriesKey() string {
	if s := strings.TrimSpace(it.SeriesID); s != "" {
		return "s:" + s
	}
	if it.Season > 0 || it.Episode > 0 {
		if s := strings.TrimSpace(it.Subtitle); s != "" {
			return "n:" + strings.ToLower(s)
		}
	}
	return "one:" + it.CanonicalID
}

// linearSortItems puts a pool into a stable, meaningful order: series
// together, episodes in broadcast order inside each.
//
// This is not cosmetic. Every strategy indexes into this slice, so if the
// order depended on whatever order Jellyfin happened to return rows in, the
// same seed would produce a different channel on every restart and none of
// the determinism below would be worth anything.
func linearSortItems(items []LinearItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if ka, kb := a.seriesKey(), b.seriesKey(); ka != kb {
			return ka < kb
		}
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.CanonicalID < b.CanonicalID
	})
}

// --- scheduling strategies --------------------------------------------------

type LinearStrategy string

const (
	// StrategyRandom picks with no memory: repeats are possible, which is what
	// "random" honestly means.
	StrategyRandom LinearStrategy = "RANDOM"
	// StrategyCyclic walks the library in order, forever. Episode order is
	// preserved because the order *is* the strategy.
	StrategyCyclic LinearStrategy = "CYCLIC"
	// StrategyCyclicShuffle shuffles which show plays next but never shuffles
	// the episodes inside a show. This is the one people mean when they say
	// "random", and getting it wrong shows S03E12 before S01E01.
	StrategyCyclicShuffle LinearStrategy = "CYCLIC_SHUFFLE"
	// StrategyBlock plays several consecutive episodes of one show, then moves
	// to another show chosen at random.
	StrategyBlock LinearStrategy = "BLOCK"
	// StrategyBlockCyclic is the same run-of-episodes idea with the shows
	// themselves in a fixed rotation.
	StrategyBlockCyclic LinearStrategy = "BLOCK_CYCLIC"
	// StrategyTimeBased splits the day into dayparts, each with its own rules:
	// cartoons in the morning, horror after midnight.
	StrategyTimeBased LinearStrategy = "TIME_BASED"
	// StrategyDayBased does the same per weekday.
	StrategyDayBased LinearStrategy = "DAY_BASED"
)

var linearStrategies = map[LinearStrategy]bool{
	StrategyRandom: true, StrategyCyclic: true, StrategyCyclicShuffle: true,
	StrategyBlock: true, StrategyBlockCyclic: true,
	StrategyTimeBased: true, StrategyDayBased: true,
}

// LinearDaypart is one slice of the local day, or one weekday, with its own
// selection rules and its own inner ordering.
//
// StartMinute and EndMinute are minutes past local midnight. A part that wraps
// midnight (start 1320, end 360) is normal and handled.
type LinearDaypart struct {
	Name        string `json:"name"`
	StartMinute int    `json:"startMinute,omitempty"`
	EndMinute   int    `json:"endMinute,omitempty"`
	// Weekdays, for DAY_BASED. Empty means every day.
	Weekdays []int `json:"weekdays,omitempty"`

	Rules LinearRuleGroup `json:"rules"`
	// Strategy inside the part. TIME_BASED and DAY_BASED say *what plays when*;
	// this says how it is ordered once chosen. Defaults to CYCLIC_SHUFFLE.
	Strategy LinearStrategy `json:"strategy,omitempty"`
}

// LinearOptions carries the knobs a strategy needs, kept off LinearChannel's
// top level so an unused knob is absent rather than zero and confusing.
type LinearOptions struct {
	// BlockSize is how many consecutive episodes of one show a BLOCK plays.
	BlockSize int `json:"blockSize,omitempty"`
	// Dayparts drive TIME_BASED and DAY_BASED.
	Dayparts []LinearDaypart `json:"dayparts,omitempty"`
	// SlotMinutes rounds each programme's slot up to a grid, the way a real
	// broadcaster starts things on the hour and half hour. Zero means
	// back-to-back, which is what a personal channel usually wants.
	SlotMinutes int `json:"slotMinutes,omitempty"`
}

// --- the channel ------------------------------------------------------------

// LinearChannel is a user-authored channel definition. Everything about what
// this channel is lives in these fields; nothing about it lives in code.
type LinearChannel struct {
	ID          string `json:"id"`
	Number      int    `json:"number"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Logo        string `json:"logo,omitempty"`
	Enabled     bool   `json:"enabled"`

	// SourceProvider is a provider id from the registry -- "jellyfin",
	// "plex-4k". SourceLibraries narrows it to some of that provider's
	// libraries; empty means all of them.
	SourceProvider  string   `json:"sourceProvider"`
	SourceLibraries []string `json:"sourceLibraries,omitempty"`

	Rules            LinearRuleGroup `json:"rules"`
	ScheduleStrategy LinearStrategy  `json:"scheduleStrategy"`
	Options          LinearOptions   `json:"options,omitempty"`

	// Timezone is an IANA name. It decides when "8pm" is and where a day
	// boundary falls, which is why it is per channel and not per viewer: a
	// daypart is a property of the broadcast, not of who is watching.
	Timezone string `json:"timezone"`
	// EPGDays is how far ahead the guide is generated.
	EPGDays int `json:"epgDays"`

	// Seed makes a schedule reproducible. Same seed, same pool, same cursor,
	// same television -- which is the only way any of this is testable.
	Seed int64 `json:"seed,omitempty"`
}

const (
	linearDefaultEPGDays = 3
	linearMaxEPGDays     = 14
	linearDefaultBlock   = 3
	linearDefaultPast    = 6 * time.Hour
	linearMinProgramSecs = 1
)

// toChannel projects onto the wire type every client already understands.
// Clients must not learn a second channel shape just because this one is
// generated locally.
func (c *LinearChannel) toChannel(providerID string) Channel {
	return Channel{
		ID:         c.ID,
		Number:     c.Number,
		Name:       c.Name,
		Logo:       c.Logo,
		Source:     "NATIVE_YARR",
		ProviderID: providerID,
	}
}

// location resolves the channel's zone, falling back to UTC rather than
// failing. A guide an hour out is bad; a channel that will not load because
// someone typed the zone wrong is worse, and the fallback is visible in the
// rendered times.
func (c *LinearChannel) location() *time.Location {
	return linearLocation(c.Timezone)
}

var (
	linearLocMu    sync.Mutex
	linearLocCache = map[string]*time.Location{}
)

func linearLocation(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "UTC") {
		return time.UTC
	}
	linearLocMu.Lock()
	defer linearLocMu.Unlock()
	if loc, ok := linearLocCache[name]; ok {
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	linearLocCache[name] = loc
	return loc
}

// normalise fills defaults in place, so every consumer downstream can assume a
// sane channel and none of them has to repeat the same three fallbacks.
func (c *LinearChannel) normalise() {
	c.ID = strings.TrimSpace(c.ID)
	c.Name = strings.TrimSpace(c.Name)
	if c.ScheduleStrategy == "" {
		c.ScheduleStrategy = StrategyCyclicShuffle
	}
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	if c.EPGDays <= 0 {
		c.EPGDays = linearDefaultEPGDays
	}
	if c.EPGDays > linearMaxEPGDays {
		c.EPGDays = linearMaxEPGDays
	}
	if c.Options.BlockSize <= 0 {
		c.Options.BlockSize = linearDefaultBlock
	}
	if c.Seed == 0 {
		// Derived, not random: a channel that reseeds itself on every restart
		// would reshuffle its own past.
		c.Seed = int64(linearHashString(c.ID+"|"+c.Name) & 0x7fffffffffffffff)
	}
	if c.Rules.Match == "" {
		c.Rules.Match = LinearMatchAll
	}
	for i := range c.Options.Dayparts {
		if c.Options.Dayparts[i].Strategy == "" {
			c.Options.Dayparts[i].Strategy = StrategyCyclicShuffle
		}
		if c.Options.Dayparts[i].Rules.Match == "" {
			c.Options.Dayparts[i].Rules.Match = LinearMatchAll
		}
	}
}

// Validate refuses a channel that cannot work, and says which field and why.
// Rejecting at save time is the only place these are cheap to catch: a bad
// strategy discovered during a fill is a channel that silently goes dark.
func (c *LinearChannel) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("channel id is required")
	}
	if c.Name == "" {
		return fmt.Errorf("channel %q needs a name", c.ID)
	}
	if c.Number <= 0 {
		return fmt.Errorf("channel %q needs a positive channel number", c.ID)
	}
	if !linearStrategies[c.ScheduleStrategy] {
		return fmt.Errorf("channel %q has unknown scheduleStrategy %q", c.ID, c.ScheduleStrategy)
	}
	if c.SourceProvider == "" {
		return fmt.Errorf("channel %q needs a sourceProvider", c.ID)
	}
	// An unknown zone is reported rather than silently swallowed, because the
	// symptom otherwise is a guide quietly an hour or six out.
	if c.Timezone != "" && !strings.EqualFold(c.Timezone, "UTC") {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return fmt.Errorf("channel %q has unknown timezone %q", c.ID, c.Timezone)
		}
	}
	if err := c.Rules.Validate(); err != nil {
		return fmt.Errorf("channel %q rules: %w", c.ID, err)
	}
	partitioned := c.ScheduleStrategy == StrategyTimeBased || c.ScheduleStrategy == StrategyDayBased
	if partitioned && len(c.Options.Dayparts) == 0 {
		return fmt.Errorf("channel %q uses %s but defines no dayparts", c.ID, c.ScheduleStrategy)
	}
	for i, d := range c.Options.Dayparts {
		if !linearStrategies[d.Strategy] {
			return fmt.Errorf("channel %q daypart %d has unknown strategy %q", c.ID, i, d.Strategy)
		}
		if d.Strategy == StrategyTimeBased || d.Strategy == StrategyDayBased {
			return fmt.Errorf("channel %q daypart %d cannot itself be %s", c.ID, i, d.Strategy)
		}
		if err := d.Rules.Validate(); err != nil {
			return fmt.Errorf("channel %q daypart %d rules: %w", c.ID, i, err)
		}
		if d.StartMinute < 0 || d.StartMinute >= 1440 || d.EndMinute < 0 || d.EndMinute > 1440 {
			return fmt.Errorf("channel %q daypart %d has minutes outside a day", c.ID, i)
		}
	}
	return nil
}

// --- rules ------------------------------------------------------------------

const (
	LinearMatchAll = "all" // AND
	LinearMatchAny = "any" // OR
)

// Rule fields. These are facets of a library item, named the way a person
// building a channel would name them.
const (
	LinearFieldGenre      = "genre"
	LinearFieldYear       = "year"
	LinearFieldNetwork    = "network"
	LinearFieldCollection = "collection"
	LinearFieldTitle      = "title"
	LinearFieldRating     = "rating"
	LinearFieldType       = "type"   // movie, episode, ... from schema.json
	LinearFieldSeries     = "series" // series name
	LinearFieldLibrary    = "library"
)

// Rule operators.
const (
	LinearOpIs          = "is"
	LinearOpIsNot       = "isNot"
	LinearOpContains    = "contains"
	LinearOpNotContains = "notContains"
	LinearOpIn          = "in"
	LinearOpNotIn       = "notIn"
	LinearOpBetween     = "between"
	LinearOpGTE         = "gte"
	LinearOpLTE         = "lte"
)

var linearRuleFields = map[string]bool{
	LinearFieldGenre: true, LinearFieldYear: true, LinearFieldNetwork: true,
	LinearFieldCollection: true, LinearFieldTitle: true, LinearFieldRating: true,
	LinearFieldType: true, LinearFieldSeries: true, LinearFieldLibrary: true,
}

var linearRuleOps = map[string]bool{
	LinearOpIs: true, LinearOpIsNot: true, LinearOpContains: true,
	LinearOpNotContains: true, LinearOpIn: true, LinearOpNotIn: true,
	LinearOpBetween: true, LinearOpGTE: true, LinearOpLTE: true,
}

// LinearRule is one condition. Multi-valued fields (genre) are satisfied when
// any of their values satisfies the rule, which is what "genre is Horror"
// means to the person who typed it.
type LinearRule struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
	Min    int      `json:"min,omitempty"`
	Max    int      `json:"max,omitempty"`
}

// LinearRuleGroup is the AND/OR grouping. Groups nest, so
// "(genre is Horror OR genre is Thriller) AND year between 1980 and 1999"
// is expressible without a query language.
//
// An empty group matches everything. That is the useful default -- a channel
// with no rules is the whole library -- and it is the one case where "matched
// nothing" would be the surprising answer.
type LinearRuleGroup struct {
	Match  string            `json:"match"`
	Negate bool              `json:"not,omitempty"`
	Rules  []LinearRule      `json:"rules,omitempty"`
	Groups []LinearRuleGroup `json:"groups,omitempty"`
}

func (r LinearRule) Validate() error {
	f := strings.TrimSpace(strings.ToLower(r.Field))
	if !linearRuleFields[f] {
		return fmt.Errorf("unknown rule field %q", r.Field)
	}
	if !linearRuleOps[r.Op] {
		return fmt.Errorf("unknown rule operator %q on field %q", r.Op, r.Field)
	}
	switch r.Op {
	case LinearOpIn, LinearOpNotIn:
		if len(r.Values) == 0 {
			return fmt.Errorf("rule %q %s needs values", r.Field, r.Op)
		}
	case LinearOpBetween:
		if f != LinearFieldYear {
			return fmt.Errorf("operator between only applies to year, not %q", r.Field)
		}
		if r.Max != 0 && r.Min > r.Max {
			return fmt.Errorf("rule %q between %d and %d is inverted", r.Field, r.Min, r.Max)
		}
	case LinearOpGTE, LinearOpLTE:
		if f != LinearFieldYear {
			return fmt.Errorf("operator %s only applies to year, not %q", r.Op, r.Field)
		}
	default:
		if strings.TrimSpace(r.Value) == "" {
			return fmt.Errorf("rule %q %s needs a value", r.Field, r.Op)
		}
	}
	return nil
}

func (g LinearRuleGroup) Validate() error {
	switch strings.ToLower(strings.TrimSpace(g.Match)) {
	case "", LinearMatchAll, LinearMatchAny:
	default:
		return fmt.Errorf("group match must be %q or %q, got %q", LinearMatchAll, LinearMatchAny, g.Match)
	}
	for _, r := range g.Rules {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	for _, sub := range g.Groups {
		if err := sub.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ruleValues returns the item's values for a field. Multi-valued by design:
// genre is a list, and flattening it to "the first genre" is how a Horror
// channel loses every film that is also a Thriller.
func (it LinearItem) ruleValues(field string) []string {
	switch strings.TrimSpace(strings.ToLower(field)) {
	case LinearFieldGenre:
		return it.Genres
	case LinearFieldNetwork:
		return []string{it.Network}
	case LinearFieldCollection:
		return []string{it.Collection}
	case LinearFieldTitle:
		return []string{it.Title}
	case LinearFieldRating:
		return []string{it.Rating}
	case LinearFieldType:
		return []string{it.Type}
	case LinearFieldSeries:
		return []string{it.Subtitle, it.SeriesID}
	case LinearFieldLibrary:
		return []string{it.LibraryID}
	}
	return nil
}

func (r LinearRule) matches(it LinearItem) bool {
	if strings.TrimSpace(strings.ToLower(r.Field)) == LinearFieldYear {
		return r.matchNumber(it.Year)
	}
	return r.matchStrings(it.ruleValues(r.Field))
}

func (r LinearRule) matchNumber(v int) bool {
	switch r.Op {
	case LinearOpBetween:
		if r.Min != 0 && v < r.Min {
			return false
		}
		if r.Max != 0 && v > r.Max {
			return false
		}
		return true
	case LinearOpGTE:
		return v >= r.Min || (r.Min == 0 && v >= linearAtoi(r.Value))
	case LinearOpLTE:
		if r.Max != 0 {
			return v <= r.Max
		}
		return v <= linearAtoi(r.Value)
	case LinearOpIs:
		return v == linearAtoi(r.Value)
	case LinearOpIsNot:
		return v != linearAtoi(r.Value)
	case LinearOpIn, LinearOpNotIn:
		hit := false
		for _, s := range r.Values {
			if v == linearAtoi(s) {
				hit = true
				break
			}
		}
		if r.Op == LinearOpNotIn {
			return !hit
		}
		return hit
	}
	return false
}

func (r LinearRule) matchStrings(vals []string) bool {
	eqAny := func(want string) bool {
		for _, v := range vals {
			if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
				return true
			}
		}
		return false
	}
	subAny := func(want string) bool {
		w := strings.ToLower(strings.TrimSpace(want))
		if w == "" {
			return false
		}
		for _, v := range vals {
			if strings.Contains(strings.ToLower(v), w) {
				return true
			}
		}
		return false
	}
	switch r.Op {
	case LinearOpIs:
		return eqAny(r.Value)
	case LinearOpIsNot:
		return !eqAny(r.Value)
	case LinearOpContains:
		return subAny(r.Value)
	case LinearOpNotContains:
		return !subAny(r.Value)
	case LinearOpIn:
		for _, want := range r.Values {
			if eqAny(want) {
				return true
			}
		}
		return false
	case LinearOpNotIn:
		for _, want := range r.Values {
			if eqAny(want) {
				return false
			}
		}
		return true
	}
	// An operator this build does not understand fails closed. The alternative
	// -- treating it as "true" -- turns a typo into a channel that quietly
	// plays the entire library, which is much harder to notice than an empty
	// one that the preview already warned about.
	return false
}

func (g LinearRuleGroup) matches(it LinearItem) bool {
	res := g.evaluate(it)
	if g.Negate {
		return !res
	}
	return res
}

func (g LinearRuleGroup) evaluate(it LinearItem) bool {
	if len(g.Rules) == 0 && len(g.Groups) == 0 {
		return true // no rules means the whole library
	}
	any := strings.EqualFold(strings.TrimSpace(g.Match), LinearMatchAny)
	if any {
		for _, r := range g.Rules {
			if r.matches(it) {
				return true
			}
		}
		for _, sub := range g.Groups {
			if sub.matches(it) {
				return true
			}
		}
		return false
	}
	for _, r := range g.Rules {
		if !r.matches(it) {
			return false
		}
	}
	for _, sub := range g.Groups {
		if !sub.matches(it) {
			return false
		}
	}
	return true
}

// linearApplyRules is the selection step. It returns a new slice; the caller's
// pool is shared between channels and must not be reordered under them.
func linearApplyRules(pool []LinearItem, g LinearRuleGroup) []LinearItem {
	out := make([]LinearItem, 0, len(pool))
	for _, it := range pool {
		if g.matches(it) {
			out = append(out, it)
		}
	}
	return out
}

// --- preview ----------------------------------------------------------------

// LinearPreview is the answer to "what will this channel actually be", asked
// before anything is saved.
//
// Matched and Schedulable are reported separately on purpose. A rule set can
// match 300 things of which 40 have no duration recorded, and reporting only
// the 300 would promise a channel that turns out to be a third shorter than
// advertised, for reasons invisible from the rule editor.
type LinearPreview struct {
	TotalPool      int          `json:"totalPool"`
	Matched        int          `json:"matched"`
	Schedulable    int          `json:"schedulable"`
	Skipped        int          `json:"skipped"`
	HoursOfContent float64      `json:"hoursOfContent"`
	Sample         []LinearItem `json:"sample,omitempty"`
	Errors         []string     `json:"errors,omitempty"`
	Detail         string       `json:"detail"`
}

const linearPreviewSample = 10

func linearPreviewRules(pool []LinearItem, g LinearRuleGroup) LinearPreview {
	p := LinearPreview{TotalPool: len(pool)}
	if err := g.Validate(); err != nil {
		p.Errors = append(p.Errors, err.Error())
		p.Detail = "the rule set is not valid, so nothing was matched: " + err.Error()
		return p
	}
	matched := linearApplyRules(pool, g)
	p.Matched = len(matched)
	var secs int
	for _, it := range matched {
		if it.schedulable() {
			p.Schedulable++
			secs += it.DurationSeconds
			if len(p.Sample) < linearPreviewSample {
				p.Sample = append(p.Sample, it)
			}
		} else {
			p.Skipped++
		}
	}
	p.HoursOfContent = float64(secs) / 3600.0
	switch {
	case p.Matched == 0:
		p.Detail = "0 programmes matched; this channel would have nothing to play"
	case p.Schedulable == 0:
		p.Detail = fmt.Sprintf("%d programmes matched but none has a known duration, so none can be scheduled", p.Matched)
	case p.Skipped > 0:
		p.Detail = fmt.Sprintf("%d programmes matched, %d schedulable (%d have no duration), %.1f hours of content",
			p.Matched, p.Schedulable, p.Skipped, p.HoursOfContent)
	default:
		p.Detail = fmt.Sprintf("%d programmes matched, %.1f hours of content", p.Matched, p.HoursOfContent)
	}
	return p
}

// --- source libraries -------------------------------------------------------

// LinearLibrary is where programmes come from. Anything that can list items
// with durations can back a channel: a Jellyfin library, a Plex section, a
// folder, or a fixture in a test.
type LinearLibrary interface {
	// LinearProviderID matches LinearChannel.SourceProvider.
	LinearProviderID() string
	// LinearLibraryID matches an entry in LinearChannel.SourceLibraries.
	LinearLibraryID() string
	LinearItems(ctx context.Context) ([]LinearItem, error)
}

// linearSliceLibrary is the in-memory library used by tests and by anything
// that already has the items in hand.
type linearSliceLibrary struct {
	provider string
	library  string
	items    []LinearItem
	// err lets a test make a source fail, which is the case that decides
	// whether one broken library takes the whole guide down.
	err error
}

func (l *linearSliceLibrary) LinearProviderID() string { return l.provider }
func (l *linearSliceLibrary) LinearLibraryID() string  { return l.library }
func (l *linearSliceLibrary) LinearItems(context.Context) ([]LinearItem, error) {
	if l.err != nil {
		return nil, l.err
	}
	out := make([]LinearItem, len(l.items))
	copy(out, l.items)
	for i := range out {
		out[i].LibraryID = l.library
		out[i].ProviderID = l.provider
	}
	return out, nil
}

// LinearJSONLibrary reads items from a JSON file.
//
// It accepts both the tidy shape (genres as a list) and the shape that falls
// out of a media server's own database (genres as a pipe-joined string),
// because that is what a real export looks like and normalising on read beats
// asking whoever produced it to reshape it first.
type LinearJSONLibrary struct {
	Provider string
	Library  string
	Path     string

	mu     sync.Mutex
	loaded []LinearItem
}

type linearJSONRow struct {
	CanonicalID string `json:"canonicalId"`
	Domain      string `json:"domain"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	Overview    string `json:"overview"`
	Artwork     string `json:"artwork"`
	Year        int    `json:"year"`

	DurationSeconds int `json:"durationSeconds"`

	Genres     []string `json:"genres"`
	GenresRaw  string   `json:"genresRaw"`
	Network    string   `json:"network"`
	StudiosRaw string   `json:"studiosRaw"`
	Collection string   `json:"collection"`
	Rating     string   `json:"rating"`

	SeriesID  string `json:"seriesId"`
	SeriesRaw string `json:"seriesRaw"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
}

const linearZeroGUID = "00000000-0000-0000-0000-000000000000"

func (l *LinearJSONLibrary) LinearProviderID() string { return l.Provider }
func (l *LinearJSONLibrary) LinearLibraryID() string  { return l.Library }

func (l *LinearJSONLibrary) LinearItems(context.Context) ([]LinearItem, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loaded != nil {
		out := make([]LinearItem, len(l.loaded))
		copy(out, l.loaded)
		return out, nil
	}
	raw, err := os.ReadFile(l.Path)
	if err != nil {
		return nil, fmt.Errorf("linear library %s/%s: %w", l.Provider, l.Library, err)
	}
	var rows []linearJSONRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("linear library %s/%s: %w", l.Provider, l.Library, err)
	}
	items := make([]LinearItem, 0, len(rows))
	for _, r := range rows {
		it := LinearItem{
			MediaItem: MediaItem{
				CanonicalID:    r.CanonicalID,
				Domain:         canonicalDomainOr(r.Domain, "video"),
				Type:           r.Type,
				Title:          r.Title,
				Subtitle:       r.Subtitle,
				Overview:       r.Overview,
				Artwork:        r.Artwork,
				Year:           r.Year,
				Season:         r.Season,
				Episode:        r.Episode,
				ProviderID:     l.Provider,
				ProviderItemID: r.CanonicalID,
			},
			DurationSeconds: r.DurationSeconds,
			Genres:          r.Genres,
			Network:         r.Network,
			Collection:      r.Collection,
			Rating:          r.Rating,
			SeriesID:        r.SeriesID,
			LibraryID:       l.Library,
		}
		if len(it.Genres) == 0 && r.GenresRaw != "" {
			it.Genres = linearSplitList(r.GenresRaw)
		}
		if it.Network == "" && r.StudiosRaw != "" {
			if s := linearSplitList(r.StudiosRaw); len(s) > 0 {
				it.Network = s[0]
			}
		}
		if it.SeriesID == "" && r.SeriesRaw != "" && r.SeriesRaw != linearZeroGUID {
			it.SeriesID = r.SeriesRaw
		}
		items = append(items, it)
	}
	l.loaded = items
	out := make([]LinearItem, len(items))
	copy(out, items)
	return out, nil
}

func linearSplitList(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == '|' || r == ';' })
	out := make([]string, 0, len(f))
	for _, v := range f {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// canonicalDomainOr keeps a library row honest against schema.json without
// dropping it: an unrecognised domain becomes the sensible default rather than
// a card nothing will ever match, which is the bug schema.json exists to stop.
func canonicalDomainOr(s, fallback string) string {
	if d := canonicalDomain(s); d != "" {
		return d
	}
	return fallback
}

func linearAtoi(s string) int {
	n := 0
	neg := false
	s = strings.TrimSpace(s)
	for i, r := range s {
		if i == 0 && r == '-' {
			neg = true
			continue
		}
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	if neg {
		return -n
	}
	return n
}
