package main

// The EPG store and the engine that keeps it filled.
//
// The rule that shapes this file: **a channel's schedule is global state.**
// It is generated once, written down, and read by everyone. It is not derived
// per request and it is certainly not derived per viewer. A channel that
// starts from programme one when you tune in is a video playlist wearing a
// channel's clothes -- two people cannot talk about what is on, nothing can be
// recorded, and the guide is fiction. Everything here exists to make one
// schedule that every reader agrees about.
//
// The second rule follows from the first: extend, never rebuild. A fill starts
// where the last programme ended and appends. Regenerating the window on each
// request would be slow, and worse, would move the programme a viewer is
// halfway through.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	linearProviderID   = "yarr-linear"
	linearProviderName = "Yarr.It Linear TV"
	linearRoutePrefix  = "/api/v1/linear/"
	linearPoolTTL      = 5 * time.Minute
)

// linearScheduleState is one channel's persisted schedule.
type linearScheduleState struct {
	ChannelID string       `json:"channelId"`
	Revision  string       `json:"revision"`
	Anchor    int64        `json:"anchor"`
	Cursor    linearCursor `json:"cursor"`
	// Programs are kept sorted by StartTime and are append-only within a
	// revision. Existing entries are never rewritten; that promise is what
	// makes the schedule the same for every reader over time as well as
	// across readers at one instant.
	Programs []Program `json:"programs"`

	// Empty records that the channel has nothing to play, and why. It is a
	// state rather than an error so the guide can say so instead of a request
	// failing and the whole grid disappearing.
	Empty  bool   `json:"empty,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type linearLibEntry struct {
	items   []LinearItem
	fetched time.Time
	// stale is the last good copy, kept so one failing library cannot blank a
	// channel that was working a minute ago.
	stale []LinearItem
}

// LinearEngine owns the channels, their schedules and the libraries behind
// them. One engine per server; it is the thing that makes the schedule shared.
type LinearEngine struct {
	mu        sync.RWMutex
	dir       string
	channels  map[string]*LinearChannel
	schedules map[string]*linearScheduleState
	libs      []LinearLibrary
	pools     map[string]*linearLibEntry

	resolver LinearResolver

	// Injectable so tests can stand at a chosen instant, run a DST weekend, or
	// prove two readers agree without racing a real clock.
	nowFn func() time.Time

	pastBuffer    time.Duration
	poolTTL       time.Duration
	retryAttempts int
	retryBackoff  time.Duration
}

func NewLinearEngine(dir string) *LinearEngine {
	e := &LinearEngine{
		dir:           dir,
		channels:      map[string]*LinearChannel{},
		schedules:     map[string]*linearScheduleState{},
		pools:         map[string]*linearLibEntry{},
		nowFn:         time.Now,
		pastBuffer:    linearDefaultPast,
		poolTTL:       linearPoolTTL,
		retryAttempts: 3,
		retryBackoff:  250 * time.Millisecond,
	}
	if dir != "" {
		if err := e.load(); err != nil {
			// Not fatal: a corrupt or partial state directory must not stop the
			// server. Channels can be re-added; a server that will not boot
			// cannot be fixed through its own UI.
			log.Printf("linear: could not load state from %s: %v", dir, err)
		}
	}
	return e
}

func (e *LinearEngine) now() time.Time { return e.nowFn().UTC() }

// AddLibrary registers a source of programmes.
func (e *LinearEngine) AddLibrary(l LinearLibrary) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.libs = append(e.libs, l)
}

// SetResolver installs what turns a programme into playable bytes.
func (e *LinearEngine) SetResolver(r LinearResolver) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resolver = r
}

// --- channel CRUD -----------------------------------------------------------

// SaveChannel validates and stores a channel definition.
//
// When the definition changes materially -- different rules, strategy or
// source -- the schedule is regenerated *from the end of whatever is on air
// now*, not from now. Someone watching does not get yanked out of the middle
// of a programme because an editor was saved in another tab.
func (e *LinearEngine) SaveChannel(ch LinearChannel) (*LinearChannel, error) {
	ch.normalise()
	if err := ch.Validate(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	for id, other := range e.channels {
		if id != ch.ID && other.Number == ch.Number {
			e.mu.Unlock()
			return nil, fmt.Errorf("channel number %d is already used by %q", ch.Number, other.Name)
		}
	}
	stored := ch
	e.channels[ch.ID] = &stored
	st := e.schedules[ch.ID]
	rev := stored.revision()
	if st != nil && st.Revision != rev {
		now := e.now().Unix()
		kept := st.Programs[:0]
		for _, p := range st.Programs {
			// Keep the past and whatever is airing right now; drop the future,
			// which the new rules will refill.
			if p.StartTime <= now {
				kept = append(kept, p)
			}
		}
		st.Programs = kept
		st.Revision = rev
		// The pool changed, so the old rotation position means nothing.
		st.Cursor = linearCursor{}
		st.Empty = false
		st.Detail = ""
	}
	e.mu.Unlock()
	if err := e.persist(); err != nil {
		return nil, err
	}
	return &stored, nil
}

func (e *LinearEngine) DeleteChannel(id string) {
	e.mu.Lock()
	delete(e.channels, id)
	delete(e.schedules, id)
	e.mu.Unlock()
	_ = e.persist()
	if e.dir != "" {
		_ = os.Remove(filepath.Join(e.dir, linearSchedFile(id)))
	}
}

func (e *LinearEngine) Channel(id string) (*LinearChannel, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ch, ok := e.channels[id]
	if !ok {
		return nil, false
	}
	c := *ch
	return &c, true
}

// ChannelDefs returns the definitions in channel-number order, which is the
// order a viewer expects to find them in.
func (e *LinearEngine) ChannelDefs() []LinearChannel {
	e.mu.RLock()
	out := make([]LinearChannel, 0, len(e.channels))
	for _, c := range e.channels {
		out = append(out, *c)
	}
	e.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Number != out[j].Number {
			return out[i].Number < out[j].Number
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// revision fingerprints the fields that change what plays. Cosmetic edits --
// a new logo, a fixed typo in the description -- deliberately do not appear
// here, because reshuffling a channel to correct its spelling would be absurd.
func (c *LinearChannel) revision() string {
	b, _ := json.Marshal(struct {
		R LinearRuleGroup
		S LinearStrategy
		O LinearOptions
		P string
		L []string
		Z string
		D int64
	}{c.Rules, c.ScheduleStrategy, c.Options, c.SourceProvider, c.SourceLibraries, c.Timezone, c.Seed})
	return fmt.Sprintf("%016x", linearHashString(string(b)))
}

// --- pools ------------------------------------------------------------------

// pool gathers the items a channel can draw on, after its rules.
//
// A library that fails is served from its last good copy. That is the
// difference between "Jellyfin restarted" being invisible and it emptying
// every channel that pointed at it.
func (e *LinearEngine) pool(ctx context.Context, ch *LinearChannel) ([]LinearItem, error) {
	e.mu.RLock()
	libs := make([]LinearLibrary, len(e.libs))
	copy(libs, e.libs)
	ttl := e.poolTTL
	e.mu.RUnlock()

	want := map[string]bool{}
	for _, l := range ch.SourceLibraries {
		want[strings.ToLower(strings.TrimSpace(l))] = true
	}

	var all []LinearItem
	var failures []string
	matchedAny := false
	for _, l := range libs {
		if !strings.EqualFold(l.LinearProviderID(), ch.SourceProvider) {
			continue
		}
		if len(want) > 0 && !want[strings.ToLower(l.LinearLibraryID())] {
			continue
		}
		matchedAny = true
		key := l.LinearProviderID() + "/" + l.LinearLibraryID()

		e.mu.Lock()
		entry := e.pools[key]
		if entry == nil {
			entry = &linearLibEntry{}
			e.pools[key] = entry
		}
		fresh := entry.items != nil && e.now().Sub(entry.fetched) < ttl
		cached := entry.items
		stale := entry.stale
		e.mu.Unlock()

		if fresh {
			all = append(all, cached...)
			continue
		}
		items, err := l.LinearItems(ctx)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", key, err))
			if stale != nil {
				all = append(all, stale...)
			}
			continue
		}
		for i := range items {
			if items[i].LibraryID == "" {
				items[i].LibraryID = l.LinearLibraryID()
			}
		}
		e.mu.Lock()
		entry.items = items
		entry.stale = items
		entry.fetched = e.now()
		e.mu.Unlock()
		all = append(all, items...)
	}

	if !matchedAny {
		return nil, fmt.Errorf("no configured library matches provider %q", ch.SourceProvider)
	}
	filtered := linearApplyRules(all, ch.Rules)
	if len(failures) > 0 {
		return filtered, fmt.Errorf("source degraded: %s", strings.Join(failures, "; "))
	}
	return filtered, nil
}

// Preview answers the rule editor without saving anything.
func (e *LinearEngine) Preview(ctx context.Context, ch LinearChannel) LinearPreview {
	ch.normalise()
	if err := ch.Rules.Validate(); err != nil {
		return LinearPreview{Errors: []string{err.Error()}, Detail: "the rule set is not valid: " + err.Error()}
	}
	// Preview must see the whole pool, then apply the rules itself, so it can
	// report the denominator: "247 of 3,900" is a useful answer and "247" on
	// its own is not.
	unruled := ch
	unruled.Rules = LinearRuleGroup{Match: LinearMatchAll}
	all, err := e.pool(ctx, &unruled)
	p := linearPreviewRules(all, ch.Rules)
	if err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
	return p
}

// --- the window -------------------------------------------------------------

var errLinearNoChannel = errors.New("no such channel")

// ensureWindow makes sure the channel's schedule covers a small past buffer,
// now, and EPGDays ahead -- extending it if it does not, and doing nothing at
// all if it already does.
//
// "Doing nothing at all" is the important branch. It is what makes reading the
// guide cheap, and it is what guarantees that two readers a millisecond apart
// see identical programmes: neither of them regenerated anything.
func (e *LinearEngine) ensureWindow(ctx context.Context, id string) (*linearScheduleState, error) {
	e.mu.RLock()
	ch := e.channels[id]
	e.mu.RUnlock()
	if ch == nil {
		return nil, errLinearNoChannel
	}
	def := *ch

	now := e.now()
	horizon := now.Add(time.Duration(def.EPGDays) * 24 * time.Hour)

	e.mu.Lock()
	st := e.schedules[id]
	if st == nil {
		st = &linearScheduleState{ChannelID: id, Revision: def.revision(), Anchor: now.Unix()}
		e.schedules[id] = st
	}
	if st.Revision != def.revision() {
		st.Revision = def.revision()
	}
	covered := len(st.Programs) > 0 && st.Programs[len(st.Programs)-1].EndTime >= horizon.Unix()
	e.mu.Unlock()

	if covered {
		if e.prune(id, now) {
			_ = e.persistSchedule(id)
		}
		return e.snapshot(id), nil
	}

	if !def.Enabled {
		return e.snapshot(id), nil
	}

	pool, poolErr := e.pool(ctx, &def)
	if poolErr != nil {
		log.Printf("linear: channel %s pool: %v", id, poolErr)
	}
	usable := linearSchedulable(pool)

	e.mu.Lock()
	st = e.schedules[id]
	if len(usable) == 0 {
		// Nothing to play. If there is already a schedule, leave it exactly as
		// it is -- a library blip must not erase a guide people are reading.
		st.Empty = len(st.Programs) == 0
		if st.Empty {
			st.Detail = linearEmptyDetail(len(pool), poolErr)
		}
		e.mu.Unlock()
		return e.snapshot(id), nil
	}
	st.Empty = false
	st.Detail = ""

	// Where a schedule with no history begins. Never `now`: a channel that
	// starts its first programme at the instant somebody first asks is a
	// channel that begins at zero for its first viewer, which is the same
	// failure as restarting per viewer wearing a hat. Anchoring at the local
	// midnight makes the first tune-in land mid-programme like any other, and
	// makes two servers that were started at different times of day agree.
	from := linearGenesis(&def, now)
	if n := len(st.Programs); n > 0 {
		// Continue from the end of what exists -- unless the server was off
		// long enough that doing so would invent days of programmes nobody
		// watched, in which case the genesis wins. The cursor is kept either
		// way: the channel picks its rotation up where it stopped rather than
		// rewinding to episode one.
		if last := time.Unix(st.Programs[n-1].EndTime, 0).UTC(); last.After(from) {
			from = last
		}
	}
	cur := st.Cursor
	e.mu.Unlock()

	added, nextCur, err := linearFill(&def, pool, cur, from, horizon)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	st = e.schedules[id]
	// Re-check under the lock: another request may have extended the same
	// channel while this fill ran. Appending blindly would interleave two
	// schedules and break the ordering invariant for everyone.
	if n := len(st.Programs); n > 0 && len(added) > 0 && added[0].StartTime < st.Programs[n-1].EndTime {
		e.mu.Unlock()
		return e.snapshot(id), nil
	}
	st.Programs = append(st.Programs, added...)
	st.Cursor = nextCur
	if st.Anchor == 0 && len(st.Programs) > 0 {
		st.Anchor = st.Programs[0].StartTime
	}
	e.mu.Unlock()

	e.prune(id, now)
	// The past beyond the buffer has just been thrown away, which is fine: it
	// was generated to establish the channel's *phase*, not to be read. What
	// matters is that the programme on air began when the schedule says, not
	// when the request arrived.
	if err := e.persistSchedule(id); err != nil {
		log.Printf("linear: could not persist schedule for %s: %v", id, err)
	}
	return e.snapshot(id), nil
}

// linearGenesis is where a channel with no history starts: the most recent
// midnight in its own zone.
//
// Midnight rather than "now" is the whole point -- a channel is supposed to
// have been running whether or not anyone was watching, and a viewer who
// arrives at 20:37 should find something already 37 minutes in. It is also
// what makes the phase reproducible: two servers with the same seed and the
// same library, started at different times on the same day, put a viewer in
// the same place.
//
// Beyond that day, continuity comes from the persisted schedule rather than
// from recomputation, because backfilling months of a channel nobody watched
// would cost real time to produce and would be thrown away by the next prune.
func linearGenesis(ch *LinearChannel, now time.Time) time.Time {
	loc := ch.location()
	l := now.In(loc)
	midnight := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	if midnight.After(l) {
		// Defensive: a zone whose day begins with a jump forward.
		midnight = midnight.AddDate(0, 0, -1)
	}
	return midnight.UTC()
}

func linearEmptyDetail(matched int, err error) string {
	switch {
	case err != nil && matched == 0:
		return "this channel has nothing to play: " + err.Error()
	case matched == 0:
		return "this channel's rules match nothing in its source libraries"
	default:
		return fmt.Sprintf("%d programmes match this channel's rules but none has a known duration, so none can be scheduled", matched)
	}
}

// prune drops programmes that ended before the past buffer. Reports whether
// anything changed, so a read-only pass does not rewrite the state file.
func (e *LinearEngine) prune(id string, now time.Time) bool {
	cutoff := now.Add(-e.pastBuffer).Unix()
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.schedules[id]
	if st == nil {
		return false
	}
	i := 0
	for i < len(st.Programs) && st.Programs[i].EndTime <= cutoff {
		i++
	}
	if i == 0 {
		return false
	}
	st.Programs = append([]Program(nil), st.Programs[i:]...)
	return true
}

func (e *LinearEngine) snapshot(id string) *linearScheduleState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	st := e.schedules[id]
	if st == nil {
		return nil
	}
	out := *st
	out.Programs = append([]Program(nil), st.Programs...)
	out.Cursor = st.Cursor.clone()
	return &out
}

// Guide answers the grid. It never touches the resolver: a source that cannot
// serve bytes must not be able to take the listings down with it.
func (e *LinearEngine) Guide(ctx context.Context, channelIDs []string, from, to int64) ([]Program, error) {
	if len(channelIDs) == 0 {
		for _, c := range e.ChannelDefs() {
			if c.Enabled {
				channelIDs = append(channelIDs, c.ID)
			}
		}
	}
	now := e.now().Unix()
	if from <= 0 {
		from = now
	}
	if to <= from {
		to = from + 6*3600
	}
	var out []Program
	for _, id := range channelIDs {
		st, err := e.ensureWindow(ctx, id)
		if err != nil {
			if errors.Is(err, errLinearNoChannel) {
				continue
			}
			// One bad channel does not empty the grid.
			log.Printf("linear: guide for %s: %v", id, err)
			continue
		}
		if st == nil {
			continue
		}
		for _, p := range st.Programs {
			if p.EndTime > from && p.StartTime < to {
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ChannelID != out[j].ChannelID {
			return out[i].ChannelID < out[j].ChannelID
		}
		return out[i].StartTime < out[j].StartTime
	})
	return out, nil
}

// Channels projects onto the wire type. Disabled channels are omitted rather
// than flagged: a client should not have to know how to hide something.
func (e *LinearEngine) Channels(ctx context.Context) ([]Channel, error) {
	defs := e.ChannelDefs()
	out := make([]Channel, 0, len(defs))
	for i := range defs {
		if !defs[i].Enabled {
			continue
		}
		out = append(out, defs[i].toChannel(linearProviderID))
	}
	return out, nil
}

// --- local-time rendering ---------------------------------------------------

// LinearGuideEntry is a Program with the viewer's local rendering attached.
// It embeds Program rather than restating it, so the wire shape stays the one
// every client already parses and the local strings are additive.
type LinearGuideEntry struct {
	Program
	StartLocal      string `json:"startLocal,omitempty"`
	EndLocal        string `json:"endLocal,omitempty"`
	DurationSeconds int64  `json:"durationSeconds"`
}

func linearRender(programs []Program, tz string) []LinearGuideEntry {
	loc := linearLocation(tz)
	out := make([]LinearGuideEntry, 0, len(programs))
	for _, p := range programs {
		out = append(out, LinearGuideEntry{
			Program:         p,
			StartLocal:      time.Unix(p.StartTime, 0).In(loc).Format(time.RFC3339),
			EndLocal:        time.Unix(p.EndTime, 0).In(loc).Format(time.RFC3339),
			DurationSeconds: p.EndTime - p.StartTime,
		})
	}
	return out
}

// linearLocalDayBounds returns the UTC instants that bracket one local day.
//
// It exists because "a day" is not 24 hours. On the spring transition the
// local day is 23 hours long and on the autumn one it is 25, and a guide that
// asks for `start + 24h` prints an hour of the wrong day twice a year --
// which is exactly the drift this engine is required not to have.
func linearLocalDayBounds(tz string, year int, month time.Month, day int) (time.Time, time.Time) {
	loc := linearLocation(tz)
	start := time.Date(year, month, day, 0, 0, 0, 0, loc)
	end := time.Date(year, month, day+1, 0, 0, 0, 0, loc)
	return start.UTC(), end.UTC()
}

// --- persistence ------------------------------------------------------------
//
// Plain JSON files, written atomically. Deliberately not SQLite: this module
// adds no dependency to a module that currently has none, the whole dataset is
// a few thousand rows that are read wholly and appended to, and a cgo-free
// build that cross-compiles to the edge box unchanged is worth more here than
// query support nothing asks for.

func linearSchedFile(id string) string {
	safe := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			safe = append(safe, r)
		default:
			safe = append(safe, '_')
		}
	}
	return fmt.Sprintf("sched-%s-%016x.json", string(safe), linearHashString(id))
}

func linearWriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	// Rename over the live file: a reader either sees the whole old state or
	// the whole new one, never a half-written schedule.
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (e *LinearEngine) persist() error {
	if e.dir == "" {
		return nil
	}
	defs := e.ChannelDefs()
	b, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		return err
	}
	return linearWriteFileAtomic(filepath.Join(e.dir, "channels.json"), b)
}

func (e *LinearEngine) persistSchedule(id string) error {
	if e.dir == "" {
		return nil
	}
	st := e.snapshot(id)
	if st == nil {
		return nil
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return linearWriteFileAtomic(filepath.Join(e.dir, linearSchedFile(id)), b)
}

func (e *LinearEngine) load() error {
	b, err := os.ReadFile(filepath.Join(e.dir, "channels.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var defs []LinearChannel
	if err := json.Unmarshal(b, &defs); err != nil {
		return err
	}
	for i := range defs {
		defs[i].normalise()
		c := defs[i]
		e.channels[c.ID] = &c

		sb, err := os.ReadFile(filepath.Join(e.dir, linearSchedFile(c.ID)))
		if err != nil {
			continue
		}
		var st linearScheduleState
		if err := json.Unmarshal(sb, &st); err != nil {
			log.Printf("linear: schedule for %s is unreadable, it will be regenerated: %v", c.ID, err)
			continue
		}
		// A schedule that came back damaged is worse than none: it would put
		// overlapping programmes in front of every viewer.
		if err := linearCheckOrdering(st.Programs); err != nil {
			log.Printf("linear: schedule for %s failed its ordering check, discarding: %v", c.ID, err)
			continue
		}
		e.schedules[c.ID] = &st
	}
	return nil
}

// --- provider ---------------------------------------------------------------

// linearProvider is how the engine reaches the rest of Yarr.It. It implements
// exactly the optional interfaces it can honour and no more, so nothing has to
// ask it for a guide it does not have.
type linearProvider struct{ e *LinearEngine }

func NewLinearProvider(e *LinearEngine) Provider { return linearProvider{e: e} }

func (p linearProvider) ID() string        { return linearProviderID }
func (p linearProvider) Name() string      { return linearProviderName }
func (p linearProvider) Domains() []string { return []string{"video"} }
func (p linearProvider) Roles() []string   { return []string{"linear-tv", "epg", "stream"} }
func (p linearProvider) Capabilities() []string {
	return []string{"health", "channels", "guide", "stream"}
}

func (p linearProvider) Health(ctx context.Context) Health {
	defs := p.e.ChannelDefs()
	if len(defs) == 0 {
		return Health{State: HealthNotConfigured, Detail: "no channels defined yet; create one from a library and a rule set"}
	}
	enabled, playable := 0, 0
	for _, c := range defs {
		if !c.Enabled {
			continue
		}
		enabled++
		if st, err := p.e.ensureWindow(ctx, c.ID); err == nil && st != nil && len(st.Programs) > 0 {
			playable++
		}
	}
	switch {
	case enabled == 0:
		return Health{State: HealthNotConfigured, Detail: "every channel is switched off"}
	case playable == 0:
		return Health{State: HealthDegraded, Detail: "no channel has anything to play; check the source libraries and rule sets"}
	case playable < enabled:
		return Health{State: HealthDegraded, Detail: fmt.Sprintf("%d of %d channels have nothing to play", enabled-playable, enabled)}
	}
	return Health{State: HealthOK, Detail: fmt.Sprintf("%d channels on air", enabled)}
}

func (p linearProvider) Channels(ctx context.Context) ([]Channel, error) {
	return p.e.Channels(ctx)
}

func (p linearProvider) Guide(ctx context.Context, ids []string, from, to int64) ([]Program, error) {
	return p.e.Guide(ctx, ids, from, to)
}

// --- HTTP -------------------------------------------------------------------

var (
	linearOnce    sync.Once
	linearDefault *LinearEngine
)

// LinearDefaultEngine is the process-wide engine. One engine means one
// schedule, which is the invariant this whole package exists to hold.
func LinearDefaultEngine() *LinearEngine {
	linearOnce.Do(func() {
		linearDefault = NewLinearEngine(strings.TrimSpace(os.Getenv("LINEAR_STATE_DIR")))
	})
	return linearDefault
}

// registerLinearRoutes wires the linear-TV endpoints onto a mux.
//
// It takes only the mux so main.go can call it in one line. The handlers are
// registered unwrapped; whether they sit behind publicCORS or a session gate
// is main.go's decision, and the guide is catalogue data while channel
// editing is not.
func registerLinearRoutes(mux *http.ServeMux) {
	LinearDefaultEngine().RegisterRoutes(mux)
}

// RegisterRoutes is the same thing against a specific engine, which is how the
// tests drive a real mux without touching process-wide state.
func (e *LinearEngine) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(linearRoutePrefix+"channels", e.handleChannels)
	mux.HandleFunc(linearRoutePrefix+"preview", e.handlePreview)
	mux.HandleFunc(linearRoutePrefix+"guide", e.handleGuide)
	mux.HandleFunc(linearRoutePrefix+"now", e.handleNow)
	mux.HandleFunc(linearRoutePrefix+"stream", e.handleStream)
}

// linearChannelRow is what the channel list returns: the wire Channel every
// client knows, the definition an editor needs, and the honest state of the
// schedule behind it.
type linearChannelRow struct {
	Channel
	Definition LinearChannel `json:"definition"`
	Status     string        `json:"status"`
	Detail     string        `json:"detail,omitempty"`
	Programs   int           `json:"scheduledPrograms"`
	ThroughUTC int64         `json:"scheduledThrough,omitempty"`
}

func (e *LinearEngine) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		defs := e.ChannelDefs()
		rows := make([]linearChannelRow, 0, len(defs))
		for i := range defs {
			row := linearChannelRow{
				Channel:    defs[i].toChannel(linearProviderID),
				Definition: defs[i],
			}
			switch {
			case !defs[i].Enabled:
				row.Status = "disabled"
			default:
				st, err := e.ensureWindow(r.Context(), defs[i].ID)
				switch {
				case err != nil:
					row.Status = "error"
					row.Detail = err.Error()
				case st == nil || len(st.Programs) == 0:
					row.Status = "empty"
					if st != nil {
						row.Detail = st.Detail
					}
				default:
					row.Status = "on-air"
					row.Programs = len(st.Programs)
					row.ThroughUTC = st.Programs[len(st.Programs)-1].EndTime
				}
			}
			rows = append(rows, row)
		}
		writeJSON(w, 200, map[string]any{"channels": rows})

	case http.MethodPost:
		var ch LinearChannel
		if err := decodeBody(r, &ch); err != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed body"})
			return
		}
		saved, err := e.SaveChannel(ch)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"channel": saved})

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeJSON(w, 400, map[string]string{"error": "id is required"})
			return
		}
		e.DeleteChannel(id)
		writeJSON(w, 200, map[string]any{"ok": true})

	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (e *LinearEngine) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var ch LinearChannel
	if err := decodeBody(r, &ch); err != nil {
		writeJSON(w, 400, map[string]string{"error": "malformed body"})
		return
	}
	if strings.TrimSpace(ch.SourceProvider) == "" {
		writeJSON(w, 400, map[string]string{"error": "sourceProvider is required to preview a rule set"})
		return
	}
	writeJSON(w, 200, e.Preview(r.Context(), ch))
}

func (e *LinearEngine) handleGuide(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var ids []string
	for _, v := range strings.Split(q.Get("channels"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			ids = append(ids, v)
		}
	}
	from := linearQueryInt(q.Get("from"), 0)
	to := linearQueryInt(q.Get("to"), 0)
	programs, err := e.Guide(r.Context(), ids, from, to)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	tz := strings.TrimSpace(q.Get("tz"))
	writeJSON(w, 200, map[string]any{
		"programs":  linearRender(programs, tz),
		"tz":        tz,
		"serverNow": e.now().Unix(),
	})
}

func linearQueryInt(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}
