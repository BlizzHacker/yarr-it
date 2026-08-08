package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- test scaffolding -------------------------------------------------------

type linearClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *linearClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *linearClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// linearCountingLibrary reports how often it was actually asked for items, so
// "the schedule is not rebuilt per request" can be asserted rather than hoped.
type linearCountingLibrary struct {
	mu    sync.Mutex
	calls int
	items []LinearItem
	err   error
}

func (l *linearCountingLibrary) LinearProviderID() string { return "fixture" }
func (l *linearCountingLibrary) LinearLibraryID() string  { return "main" }
func (l *linearCountingLibrary) LinearItems(context.Context) ([]LinearItem, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	out := make([]LinearItem, len(l.items))
	copy(out, l.items)
	return out, nil
}

func (l *linearCountingLibrary) fail(err error) {
	l.mu.Lock()
	l.err = err
	l.mu.Unlock()
}

func (l *linearCountingLibrary) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// linearEngineAt builds an engine standing at a chosen instant with one
// channel already configured.
func linearEngineAt(t *testing.T, dir string, at time.Time, ch LinearChannel) (*LinearEngine, *linearClock) {
	t.Helper()
	clk := &linearClock{t: at.UTC()}
	e := NewLinearEngine(dir)
	e.nowFn = clk.now
	e.pastBuffer = time.Hour
	e.poolTTL = 24 * time.Hour
	e.retryBackoff = 0
	e.AddLibrary(linearFixtureLibrary())
	if ch.ID != "" {
		if _, err := e.SaveChannel(ch); err != nil {
			t.Fatalf("save channel: %v", err)
		}
	}
	return e, clk
}

func linearMustWindow(t *testing.T, e *LinearEngine, id string) *linearScheduleState {
	t.Helper()
	st, err := e.ensureWindow(context.Background(), id)
	if err != nil {
		t.Fatalf("ensureWindow(%s): %v", id, err)
	}
	if st == nil {
		t.Fatalf("ensureWindow(%s) returned no schedule", id)
	}
	return st
}

// --- the window -------------------------------------------------------------

// The performance requirement, asserted where it can actually be checked: a
// hundred reads must generate one schedule. The cursor is the witness -- it
// only moves when programmes are produced.
func TestLinearScheduleIsNotRebuiltOnEveryRequest(t *testing.T) {
	lib := &linearCountingLibrary{items: linearFixtureItems()}
	clk := &linearClock{t: linearEpoch}
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL = clk.now, time.Hour, 24*time.Hour
	e.AddLibrary(lib)
	ch := linearTestChannel("perf", StrategyCyclicShuffle)
	ch.EPGDays = 3
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}

	first := linearMustWindow(t, e, "perf")
	if len(first.Programs) == 0 {
		t.Fatal("nothing was generated")
	}
	wantCursor := first.Cursor.Ordinal
	wantPrograms := append([]Program(nil), first.Programs...)
	libCalls := lib.count()

	for i := 0; i < 100; i++ {
		clk.advance(time.Second)
		st := linearMustWindow(t, e, "perf")
		if st.Cursor.Ordinal != wantCursor {
			t.Fatalf("read %d advanced the cursor from %d to %d: the schedule was regenerated", i, wantCursor, st.Cursor.Ordinal)
		}
		if len(st.Programs) != len(wantPrograms) {
			t.Fatalf("read %d changed the programme count from %d to %d", i, len(wantPrograms), len(st.Programs))
		}
		for k := range wantPrograms {
			if st.Programs[k] != wantPrograms[k] {
				t.Fatalf("read %d rewrote programme %d", i, k)
			}
		}
	}
	if lib.count() != libCalls {
		t.Errorf("the library was re-read %d extra times for reads that generated nothing", lib.count()-libCalls)
	}
}

// Extending must be append-only. If a viewer is 20 minutes into something, the
// schedule growing at the far end must not touch what they are watching.
func TestLinearWindowExtendsWithoutRewritingWhatExists(t *testing.T) {
	ch := linearTestChannel("ext", StrategyCyclicShuffle)
	ch.EPGDays = 1
	e, clk := linearEngineAt(t, "", linearEpoch, ch)

	before := append([]Program(nil), linearMustWindow(t, e, "ext").Programs...)
	clk.advance(20 * time.Hour)
	after := linearMustWindow(t, e, "ext").Programs

	// Whatever survived pruning must be untouched and in the same order.
	cut := clk.now().Add(-e.pastBuffer).Unix()
	var kept []Program
	for _, p := range before {
		if p.EndTime > cut {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		t.Fatal("the test pruned everything; it proves nothing")
	}
	if len(after) <= len(kept) {
		t.Fatalf("the window did not extend: %d programmes survived pruning, %d exist now", len(kept), len(after))
	}
	for i, p := range kept {
		if after[i] != p {
			t.Fatalf("programme %d was rewritten when the window extended:\n was %+v\n now %+v", i, p, after[i])
		}
	}
	if err := linearCheckOrdering(after); err != nil {
		t.Fatal(err)
	}
}

func TestLinearPastBufferIsKeptThenPruned(t *testing.T) {
	ch := linearTestChannel("prune", StrategyCyclic)
	// Episodes only, so the one-hour buffer is guaranteed to contain more than
	// one finished programme and the assertion below is not at the mercy of a
	// two-hour film straddling the window.
	ch.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
		{Field: LinearFieldType, Op: LinearOpIs, Value: "episode"},
	}}
	e, clk := linearEngineAt(t, "", linearEpoch, ch)
	linearMustWindow(t, e, "prune")

	clk.advance(10 * time.Hour)
	st := linearMustWindow(t, e, "prune")
	now := clk.now().Unix()
	cut := now - int64(e.pastBuffer.Seconds())
	var recentPast int
	for _, p := range st.Programs {
		if p.EndTime <= cut {
			t.Fatalf("%q ended %ds before the buffer cut-off and is still stored", p.Title, cut-p.EndTime)
		}
		if p.EndTime <= now {
			recentPast++
		}
	}
	if recentPast == 0 {
		t.Error("nothing from the recent past survived; the buffer that lets a client rejoin mid-programme is gone")
	}
}

// A server that was off for a week must not invent a week of programmes that
// nobody watched -- but it must not rewind the rotation either.
func TestLinearLongDowntimeRestartsAtNowAndKeepsTheRotation(t *testing.T) {
	ch := linearTestChannel("down", StrategyCyclicShuffle)
	e, clk := linearEngineAt(t, "", linearEpoch, ch)
	first := linearMustWindow(t, e, "down")
	cursorBefore := first.Cursor.Ordinal

	clk.advance(7 * 24 * time.Hour)
	st := linearMustWindow(t, e, "down")

	now := clk.now().Unix()
	for _, p := range st.Programs {
		if p.EndTime < now-int64(e.pastBuffer.Seconds()) {
			t.Fatalf("a week of dead air was backfilled: %q ended %ds ago", p.Title, now-p.EndTime)
		}
	}
	if st.Cursor.Ordinal <= cursorBefore {
		t.Errorf("the rotation rewound: cursor went from %d to %d", cursorBefore, st.Cursor.Ordinal)
	}
	if err := linearCheckOrdering(st.Programs); err != nil {
		t.Fatal(err)
	}
}

// --- persistence ------------------------------------------------------------

// The schedule is state on the server, not a function of the request. Prove it
// by throwing the process away.
func TestLinearScheduleSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	ch := linearTestChannel("persist", StrategyCyclicShuffle)
	e1, _ := linearEngineAt(t, dir, linearEpoch, ch)
	want := append([]Program(nil), linearMustWindow(t, e1, "persist").Programs...)

	files, _ := os.ReadDir(dir)
	if len(files) < 2 {
		t.Fatalf("expected a channels file and a schedule file in %s, found %d entries", dir, len(files))
	}

	clk := &linearClock{t: linearEpoch}
	e2 := NewLinearEngine(dir)
	e2.nowFn, e2.pastBuffer, e2.poolTTL = clk.now, time.Hour, 24*time.Hour
	e2.AddLibrary(linearFixtureLibrary())
	if _, ok := e2.Channel("persist"); !ok {
		t.Fatal("the channel definition did not survive the restart")
	}
	got := linearMustWindow(t, e2, "persist").Programs
	if len(got) != len(want) {
		t.Fatalf("after a restart the schedule has %d programmes, it had %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("programme %d changed across the restart:\n was %+v\n now %+v", i, want[i], got[i])
		}
	}
}

// A damaged schedule file is discarded rather than served. Overlapping
// programmes in front of every viewer is worse than a regenerated guide.
func TestLinearCorruptScheduleIsDiscardedNotServed(t *testing.T) {
	dir := t.TempDir()
	ch := linearTestChannel("bad", StrategyCyclic)
	e1, _ := linearEngineAt(t, dir, linearEpoch, ch)
	linearMustWindow(t, e1, "bad")

	path := filepath.Join(dir, linearSchedFile("bad"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st linearScheduleState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	st.Programs[1].StartTime = st.Programs[0].StartTime // force an overlap
	b, _ := json.Marshal(st)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	clk := &linearClock{t: linearEpoch}
	e2 := NewLinearEngine(dir)
	e2.nowFn, e2.pastBuffer, e2.poolTTL = clk.now, time.Hour, 24*time.Hour
	e2.AddLibrary(linearFixtureLibrary())
	got := linearMustWindow(t, e2, "bad").Programs
	if err := linearCheckOrdering(got); err != nil {
		t.Fatalf("a corrupt schedule was loaded and served: %v", err)
	}
}

// --- editing ----------------------------------------------------------------

// Saving new rules must not yank someone out of the middle of a programme.
func TestLinearEditingRulesKeepsWhatIsOnAir(t *testing.T) {
	ch := linearTestChannel("edit", StrategyCyclic)
	e, _ := linearEngineAt(t, "", linearEpoch, ch)
	linearMustWindow(t, e, "edit")

	onAir := e.NowPlaying(context.Background(), "edit", "")
	if onAir.Program == nil {
		t.Fatal("nothing was on air to begin with")
	}
	before := *onAir.Program

	ch.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
		{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Horror"},
	}}
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}
	after := e.NowPlaying(context.Background(), "edit", "")
	if after.Program == nil {
		t.Fatal("the channel went dark when its rules were saved")
	}
	if *after.Program != before {
		t.Errorf("the programme on air changed when rules were saved:\n was %+v\n now %+v", before, *after.Program)
	}
	// The future did change, though: that is the point of saving.
	st := linearMustWindow(t, e, "edit")
	var future int
	for _, p := range st.Programs {
		if p.StartTime > after.ServerNow {
			future++
			if !strings.Contains(p.Title, "Cine") && !strings.Contains(p.Title, "Night of the Reels") && !strings.Contains(p.Title, "Fifth Season") {
				t.Errorf("%q is scheduled after the edit but is not Horror", p.Title)
			}
		}
	}
	if future == 0 {
		t.Error("the new rules produced no future programmes")
	}
	if err := linearCheckOrdering(st.Programs); err != nil {
		t.Fatal(err)
	}
}

// --- empty and failing sources ---------------------------------------------

// An empty channel is a state to draw, not a crash and not a 500.
func TestLinearEmptyChannelIsASafeState(t *testing.T) {
	ch := linearTestChannel("empty", StrategyCyclic)
	ch.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
		{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Polka Documentary"},
	}}
	e, _ := linearEngineAt(t, "", linearEpoch, ch)

	st := linearMustWindow(t, e, "empty")
	if len(st.Programs) != 0 {
		t.Fatalf("an impossible rule set produced %d programmes", len(st.Programs))
	}
	if !st.Empty || st.Detail == "" {
		t.Errorf("the empty state was not described: empty=%v detail=%q", st.Empty, st.Detail)
	}

	np := e.NowPlaying(context.Background(), "empty", "")
	if np.State != LinearStateEmpty {
		t.Errorf("NowPlaying state is %q, want %q", np.State, LinearStateEmpty)
	}
	if np.Detail == "" {
		t.Error("NowPlaying gave no reason for the empty channel")
	}
	// And the guide answers rather than failing.
	got, err := e.Guide(context.Background(), []string{"empty"}, 0, 0)
	if err != nil {
		t.Fatalf("the guide failed because one channel was empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("the empty channel contributed %d programmes", len(got))
	}
}

// A library that starts failing must not blank a guide that was working. The
// last good copy carries the channel until the source comes back.
func TestLinearGuideSurvivesALibraryFailure(t *testing.T) {
	lib := &linearCountingLibrary{items: linearFixtureItems()}
	clk := &linearClock{t: linearEpoch}
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL = clk.now, time.Hour, time.Minute
	e.AddLibrary(lib)
	ch := linearTestChannel("flaky", StrategyCyclic)
	ch.EPGDays = 1
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}
	before := append([]Program(nil), linearMustWindow(t, e, "flaky").Programs...)

	lib.fail(errors.New("connection refused"))
	clk.advance(20 * time.Hour) // forces both a pool refresh and an extension

	got, err := e.Guide(context.Background(), []string{"flaky"}, clk.now().Unix(), clk.now().Add(2*time.Hour).Unix())
	if err != nil {
		t.Fatalf("the guide failed while the library was down: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the guide emptied when the library went down")
	}
	st := linearMustWindow(t, e, "flaky")
	if len(st.Programs) <= len(before)/2 {
		t.Errorf("the schedule collapsed from %d to %d programmes when the source failed", len(before), len(st.Programs))
	}
	if err := linearCheckOrdering(st.Programs); err != nil {
		t.Fatal(err)
	}
}

// A channel pointed at a provider nobody configured says so, and does not take
// the request down.
func TestLinearUnknownSourceProviderIsReported(t *testing.T) {
	ch := linearTestChannel("nosrc", StrategyCyclic)
	ch.SourceProvider = "not-installed"
	e, _ := linearEngineAt(t, "", linearEpoch, ch)
	st := linearMustWindow(t, e, "nosrc")
	if len(st.Programs) != 0 {
		t.Fatal("a channel with no source produced programmes")
	}
	if !strings.Contains(st.Detail, "not-installed") {
		t.Errorf("the reason should name the missing provider, got %q", st.Detail)
	}
}

// --- the guide --------------------------------------------------------------

func TestLinearGuideReturnsOnlyTheRequestedWindow(t *testing.T) {
	ch := linearTestChannel("guide", StrategyCyclic)
	ch.EPGDays = 2
	e, _ := linearEngineAt(t, "", linearEpoch, ch)
	linearMustWindow(t, e, "guide")

	from := linearEpoch.Add(3 * time.Hour).Unix()
	to := linearEpoch.Add(5 * time.Hour).Unix()
	got, err := e.Guide(context.Background(), []string{"guide"}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("the guide returned nothing for a window inside the schedule")
	}
	for _, p := range got {
		if p.EndTime <= from || p.StartTime >= to {
			t.Errorf("%q (%d-%d) is outside the requested %d-%d", p.Title, p.StartTime, p.EndTime, from, to)
		}
	}
	if err := linearCheckOrdering(got); err != nil {
		t.Fatal(err)
	}
}

func TestLinearGuideCoversEveryEnabledChannelByDefault(t *testing.T) {
	e, _ := linearEngineAt(t, "", linearEpoch, linearTestChannel("g1", StrategyCyclic))
	second := linearTestChannel("g2", StrategyCyclic)
	second.Number = 102
	if _, err := e.SaveChannel(second); err != nil {
		t.Fatal(err)
	}
	off := linearTestChannel("g3", StrategyCyclic)
	off.Number, off.Enabled = 103, false
	if _, err := e.SaveChannel(off); err != nil {
		t.Fatal(err)
	}

	got, err := e.Guide(context.Background(), nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.ChannelID] = true
	}
	if !seen["g1"] || !seen["g2"] {
		t.Errorf("the default guide missed a channel: %v", seen)
	}
	if seen["g3"] {
		t.Error("a disabled channel appeared in the guide")
	}
}

// --- timezone and DST -------------------------------------------------------

// A day is not 24 hours twice a year. A guide that assumes it is prints an
// hour of the wrong day, which is the drift this engine is required not to
// have.
func TestLinearLocalDayIsShortInSpringAndLongInAutumn(t *testing.T) {
	tz := "America/Chicago"
	if linearLocation(tz) == time.UTC {
		t.Fatalf("%s did not resolve; the embedded tzdata is not reaching this build", tz)
	}
	cases := []struct {
		name  string
		month time.Month
		day   int
		hours float64
	}{
		{"spring forward", time.March, 8, 23},
		{"fall back", time.November, 1, 25},
		{"ordinary day", time.June, 10, 24},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := linearLocalDayBounds(tz, 2026, tc.month, tc.day)
			if got := end.Sub(start).Hours(); got != tc.hours {
				t.Fatalf("%s 2026-%02d-%02d is %g hours long, want %g", tz, tc.month, tc.day, got, tc.hours)
			}
		})
	}
}

// The real assertion: a daypart that starts at 06:00 local starts at 06:00
// local on both sides of a transition. An implementation that froze the UTC
// offset would pass on one side and fail on the other.
func TestLinearDaypartsHoldTheirLocalHourAcrossDST(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start time.Time
	}{
		{"fall back", time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)},
		{"spring forward", time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := linearDaypartChannel("dst", StrategyTimeBased)
			ch.Timezone = "America/Chicago"
			ch.normalise()
			loc := ch.location()

			got, _, err := linearFill(&ch, linearFixtureItems(), linearCursor{}, tc.start, tc.start.Add(48*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if err := linearCheckOrdering(got); err != nil {
				t.Fatal(err)
			}
			var offsets = map[int]bool{}
			var morning, late int
			for _, p := range got {
				local := time.Unix(p.StartTime, 0).In(loc)
				_, off := local.Zone()
				offsets[off] = true
				switch h := local.Hour(); {
				case h >= 6 && h < 12:
					morning++
					if !linearTitleHasGenre(t, p, "Comedy") {
						t.Errorf("%q played at %s local, inside the Comedy morning", p.Title, local.Format("Mon 15:04"))
					}
				case h >= 22 || h < 6:
					late++
					if !linearTitleHasGenre(t, p, "Horror") {
						t.Errorf("%q played at %s local, inside the Horror late slot", p.Title, local.Format("Mon 15:04"))
					}
				}
			}
			if len(offsets) < 2 {
				t.Fatalf("the run did not actually cross the transition (offsets seen: %v)", offsets)
			}
			if morning == 0 || late == 0 {
				t.Fatalf("dayparts never fired (morning=%d late=%d)", morning, late)
			}
			// UTC must stay strictly increasing even where local time repeats.
			for i := 1; i < len(got); i++ {
				if got[i].StartTime <= got[i-1].StartTime {
					t.Fatalf("UTC went backwards at %d: %d then %d", i, got[i-1].StartTime, got[i].StartTime)
				}
			}
		})
	}
}

// DAY_BASED must switch at local midnight, which moves relative to UTC when
// the clocks change.
func TestLinearDayBasedSwitchesAtLocalMidnightAcrossDST(t *testing.T) {
	ch := linearDaypartChannel("dstday", StrategyDayBased)
	ch.Timezone = "America/Chicago"
	ch.normalise()
	loc := ch.location()

	start := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC) // Friday, before the change
	got, _, err := linearFill(&ch, linearFixtureItems(), linearCursor{}, start, start.Add(96*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var weekday, weekend int
	for _, p := range got {
		local := time.Unix(p.StartTime, 0).In(loc)
		if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
			weekend++
			if !linearTitleHasGenre(t, p, "Horror") {
				t.Errorf("%q played on %s local, which is the Horror day", p.Title, local.Format("Mon 15:04"))
			}
		} else {
			weekday++
			if !linearTitleHasGenre(t, p, "Comedy") {
				t.Errorf("%q played on %s local, which is the Comedy day", p.Title, local.Format("Mon 15:04"))
			}
		}
	}
	if weekday == 0 || weekend == 0 {
		t.Fatalf("the run did not cover both kinds of day (weekday=%d weekend=%d)", weekday, weekend)
	}
	// A UTC-based implementation would put the Sunday->Monday switch six hours
	// early; assert the boundary against local midnight directly.
	for i := 1; i < len(got); i++ {
		prev := time.Unix(got[i-1].StartTime, 0).In(loc)
		cur := time.Unix(got[i].StartTime, 0).In(loc)
		if prev.Day() == cur.Day() {
			continue
		}
		midnight := time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, loc)
		if cur.Before(midnight) || prev.After(midnight) {
			t.Fatalf("the day boundary at index %d does not bracket local midnight: %s -> %s", i, prev, cur)
		}
	}
}

// Times are stored UTC and rendered in the viewer's zone. Two viewers in two
// zones must see the same instant with different clock faces on it.
func TestLinearGuideRendersInTheViewersZone(t *testing.T) {
	p := Program{ChannelID: "x", Title: "T",
		StartTime: time.Date(2026, 6, 1, 20, 0, 0, 0, time.UTC).Unix(),
		EndTime:   time.Date(2026, 6, 1, 21, 0, 0, 0, time.UTC).Unix()}

	chicago := linearRender([]Program{p}, "America/Chicago")
	tokyo := linearRender([]Program{p}, "Asia/Tokyo")

	if chicago[0].StartTime != tokyo[0].StartTime {
		t.Fatal("rendering changed the stored UTC instant")
	}
	if !strings.HasPrefix(chicago[0].StartLocal, "2026-06-01T15:00:00-05:00") {
		t.Errorf("Chicago rendering is %q, want 15:00-05:00", chicago[0].StartLocal)
	}
	if !strings.HasPrefix(tokyo[0].StartLocal, "2026-06-02T05:00:00+09:00") {
		t.Errorf("Tokyo rendering is %q, want the next day at 05:00+09:00", tokyo[0].StartLocal)
	}
	if chicago[0].DurationSeconds != 3600 {
		t.Errorf("duration rendered as %ds", chicago[0].DurationSeconds)
	}
}

// --- the provider -----------------------------------------------------------

func TestLinearProviderJoinsTheRegistry(t *testing.T) {
	e, _ := linearEngineAt(t, "", linearEpoch, linearTestChannel("reg", StrategyCyclic))
	p := NewLinearProvider(e)

	r := &Registry{}
	if err := r.Add(p); err != nil {
		t.Fatalf("the linear provider was refused by the registry: %v", err)
	}
	if got := len(r.For("video", "linear-tv")); got != 1 {
		t.Errorf("For(video, linear-tv) returned %d, want 1", got)
	}
	if got := len(r.For("tv", "epg")); got != 1 {
		t.Errorf("For(tv, epg) returned %d, want 1", got)
	}
	cp, ok := p.(ChannelProvider)
	if !ok {
		t.Fatal("the linear provider does not satisfy ChannelProvider")
	}
	gp, ok := p.(GuideProvider)
	if !ok {
		t.Fatal("the linear provider does not satisfy GuideProvider")
	}
	chans, err := cp.Channels(context.Background())
	if err != nil || len(chans) != 1 {
		t.Fatalf("Channels() = %d, %v", len(chans), err)
	}
	programs, err := gp.Guide(context.Background(), []string{"reg"}, 0, 0)
	if err != nil || len(programs) == 0 {
		t.Fatalf("Guide() = %d programmes, %v", len(programs), err)
	}
	// Capabilities must be words schema.json knows, or a settings screen
	// cannot render them.
	known := map[string]bool{}
	for _, c := range schema.Capabilities {
		known[c] = true
	}
	for _, c := range p.Capabilities() {
		if !known[c] {
			t.Errorf("capability %q is not in schema.json", c)
		}
	}
	if h := p.Health(context.Background()); h.State != HealthOK {
		t.Errorf("health is %q (%s), want healthy", h.State, h.Detail)
	}
}

func TestLinearProviderHealthDistinguishesUnconfiguredFromBroken(t *testing.T) {
	e := NewLinearEngine("")
	e.nowFn = (&linearClock{t: linearEpoch}).now
	p := NewLinearProvider(e)
	if h := p.Health(context.Background()); h.State != HealthNotConfigured {
		t.Errorf("an engine with no channels reports %q, want not_configured", h.State)
	}

	e.AddLibrary(linearFixtureLibrary())
	ch := linearTestChannel("broken", StrategyCyclic)
	ch.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
		{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Polka Documentary"},
	}}
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}
	h := p.Health(context.Background())
	if h.State != HealthDegraded {
		t.Errorf("a channel with nothing to play reports %q, want degraded", h.State)
	}
	if h.Detail == "" {
		t.Error("degraded health said nothing about what to fix")
	}
}

// --- HTTP -------------------------------------------------------------------

func linearTestServer(t *testing.T, e *LinearEngine) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	e.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func linearGetJSON(t *testing.T, url string, into any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func TestLinearHTTPSurface(t *testing.T) {
	e, _ := linearEngineAt(t, "", linearEpoch, LinearChannel{})
	srv := linearTestServer(t, e)

	// Preview before saving anything.
	body := `{"sourceProvider":"fixture","rules":{"match":"all","rules":[{"field":"genre","op":"is","value":"Comedy"}]}}`
	resp, err := http.Post(srv.URL+linearRoutePrefix+"preview", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var prev LinearPreview
	if err := json.NewDecoder(resp.Body).Decode(&prev); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if prev.Matched == 0 || prev.TotalPool == 0 {
		t.Fatalf("preview returned %+v", prev)
	}
	if !strings.Contains(prev.Detail, "programmes matched") {
		t.Errorf("preview detail is %q", prev.Detail)
	}

	// Create the channel.
	chBody, _ := json.Marshal(linearTestChannel("http", StrategyCyclic))
	resp, err = http.Post(srv.URL+linearRoutePrefix+"channels", "application/json", strings.NewReader(string(chBody)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("creating a channel returned %d", resp.StatusCode)
	}

	// List it.
	var list struct {
		Channels []linearChannelRow `json:"channels"`
	}
	if code := linearGetJSON(t, srv.URL+linearRoutePrefix+"channels", &list); code != 200 {
		t.Fatalf("channel list returned %d", code)
	}
	if len(list.Channels) != 1 || list.Channels[0].Status != "on-air" {
		t.Fatalf("channel list is %+v", list.Channels)
	}
	if list.Channels[0].Source != "NATIVE_YARR" {
		t.Errorf("channel source is %q", list.Channels[0].Source)
	}

	// Guide.
	var guide struct {
		Programs  []LinearGuideEntry `json:"programs"`
		ServerNow int64              `json:"serverNow"`
	}
	if code := linearGetJSON(t, srv.URL+linearRoutePrefix+"guide?channels=http&tz=America/Chicago", &guide); code != 200 {
		t.Fatalf("guide returned %d", code)
	}
	if len(guide.Programs) == 0 {
		t.Fatal("the guide was empty")
	}
	if guide.Programs[0].StartLocal == "" {
		t.Error("the guide did not render local times when asked for a zone")
	}
	if guide.ServerNow != linearEpoch.Unix() {
		t.Errorf("serverNow is %d, want %d: clients need the server's clock to compute offsets", guide.ServerNow, linearEpoch.Unix())
	}

	// Now playing.
	var np LinearNowPlaying
	if code := linearGetJSON(t, srv.URL+linearRoutePrefix+"now?channel=http", &np); code != 200 {
		t.Fatalf("now returned %d", code)
	}
	if np.State != LinearStateOnAir || np.Program == nil {
		t.Fatalf("now returned %+v", np)
	}

	// An unknown channel is a 404, not a 500 and not an empty 200.
	if code := linearGetJSON(t, srv.URL+linearRoutePrefix+"now?channel=nope", nil); code != 404 {
		t.Errorf("an unknown channel returned %d, want 404", code)
	}
	if code := linearGetJSON(t, srv.URL+linearRoutePrefix+"now", nil); code != 400 {
		t.Errorf("a missing channel parameter returned %d, want 400", code)
	}

	// Delete.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+linearRoutePrefix+"channels?id=http", nil)
	dresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	dresp.Body.Close()
	if dresp.StatusCode != 200 {
		t.Fatalf("delete returned %d", dresp.StatusCode)
	}
	if _, ok := e.Channel("http"); ok {
		t.Error("the channel survived its own deletion")
	}
}

func TestLinearHTTPRejectsInvalidChannels(t *testing.T) {
	e, _ := linearEngineAt(t, "", linearEpoch, LinearChannel{})
	srv := linearTestServer(t, e)
	bad := `{"id":"x","name":"X","number":0,"sourceProvider":"fixture","enabled":true}`
	resp, err := http.Post(srv.URL+linearRoutePrefix+"channels", "application/json", strings.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("an invalid channel returned %d, want 400", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "number") {
		t.Errorf("the error should say which field is wrong, got %q", body["error"])
	}
}

func TestLinearRegisterRoutesTakesOnlyAMux(t *testing.T) {
	// The signature main.go will call. It must not need anything else.
	var register func(*http.ServeMux) = registerLinearRoutes
	mux := http.NewServeMux()
	register(mux)
	for _, path := range []string{"channels", "preview", "guide", "now", "stream"} {
		req := httptest.NewRequest(http.MethodGet, linearRoutePrefix+path, nil)
		if h, pattern := mux.Handler(req); h == nil || pattern == "" {
			t.Errorf("%s%s is not registered", linearRoutePrefix, path)
		}
	}
}

// --- against a real library -------------------------------------------------

// The whole engine against a real media library, when one is pointed at.
//
// Skipped by default and on purpose: the committed suite has to be hermetic,
// and a 40,000-row export of somebody's media is not something to check in.
// Point YARRIT_LINEAR_REAL_LIBRARY at a JSON export to run it.
func TestLinearAgainstARealLibrary(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("YARRIT_LINEAR_REAL_LIBRARY"))
	if path == "" {
		t.Skip("set YARRIT_LINEAR_REAL_LIBRARY to a library export to run this")
	}
	lib := &LinearJSONLibrary{Provider: "jellyfin", Library: "main", Path: path}
	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real library: %d items", len(items))

	at := time.Date(2026, 8, 7, 20, 37, 0, 0, time.UTC)
	clk := &linearClock{t: at}
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL, e.retryBackoff = clk.now, 6*time.Hour, 24*time.Hour, 0
	e.AddLibrary(lib)

	ch := LinearChannel{
		ID: "real-comedy", Number: 501, Name: "Real Comedy",
		Enabled: true, SourceProvider: "jellyfin",
		ScheduleStrategy: StrategyCyclicShuffle,
		Timezone:         "America/Chicago",
		EPGDays:          2,
		Seed:             20260807,
		Rules: LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
			{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Comedy"},
			{Field: LinearFieldType, Op: LinearOpIs, Value: "episode"},
		}},
	}

	prev := e.Preview(context.Background(), ch)
	t.Logf("rule preview: %s", prev.Detail)
	if prev.Matched == 0 {
		t.Fatalf("the rule set matched nothing in the real library (pool %d)", prev.TotalPool)
	}
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}

	st := linearMustWindow(t, e, ch.ID)
	if len(st.Programs) == 0 {
		t.Fatal("no schedule was generated from the real library")
	}
	if err := linearCheckOrdering(st.Programs); err != nil {
		t.Fatalf("the real schedule broke the ordering invariant: %v", err)
	}
	t.Logf("schedule: %d programmes, through %s",
		len(st.Programs), time.Unix(st.Programs[len(st.Programs)-1].EndTime, 0).UTC())

	// Two independent readers, on real programmes.
	a := e.NowPlaying(context.Background(), ch.ID, "America/Chicago")
	b := e.NowPlaying(context.Background(), ch.ID, "America/Chicago")
	if a.Program == nil {
		t.Fatalf("nothing on air: %+v", a)
	}
	if a.Program.CanonicalID != b.Program.CanonicalID || a.OffsetSeconds != b.OffsetSeconds {
		t.Fatalf("two clients disagree on a real channel: %q@%.0f vs %q@%.0f",
			a.Program.Title, a.OffsetSeconds, b.Program.Title, b.OffsetSeconds)
	}
	if a.OffsetSeconds <= 0 {
		t.Fatal("the first viewer of a real channel got offset 0")
	}
	t.Logf("on air: %q (%s) at offset %.0fs, started %s local",
		a.Program.Title, a.Program.Subtitle, a.OffsetSeconds, a.StartLocal)

	// Real rollover: step to the exact second this programme ends.
	end := a.Program.EndTime
	clk.advance(time.Duration(end-at.Unix()) * time.Second)
	next := e.NowPlaying(context.Background(), ch.ID, "America/Chicago")
	if next.Program == nil || next.Program.CanonicalID == a.Program.CanonicalID {
		t.Fatalf("the real channel did not roll over at %d: %+v", end, next)
	}
	if next.OffsetSeconds != 0 {
		t.Errorf("the programme after a real rollover started at offset %.0f", next.OffsetSeconds)
	}
	t.Logf("rolled over to %q at offset %.0fs", next.Program.Title, next.OffsetSeconds)

	// Episode order is preserved across a real library too.
	seen := map[string][]int{}
	for _, p := range st.Programs {
		if p.Subtitle == "" || p.Season == 0 {
			continue
		}
		seen[p.Subtitle] = append(seen[p.Subtitle], p.Season*1000+p.Episode)
	}
	var checked int
	for show, eps := range seen {
		if len(eps) < 2 {
			continue
		}
		checked++
		for i := 1; i < len(eps); i++ {
			if eps[i] < eps[i-1] && eps[i] != eps[0] {
				t.Errorf("%s went backwards: %d then %d", show, eps[i-1], eps[i])
			}
		}
	}
	t.Logf("episode order checked across %d shows that appeared more than once", checked)
}

func TestLinearScheduleFilenameIsSafe(t *testing.T) {
	for _, id := range []string{"../../etc/passwd", "a b/c", "ok-id_1"} {
		got := linearSchedFile(id)
		if strings.ContainsAny(got, `/\:`) || strings.Contains(got, "..") {
			t.Errorf("id %q produced filename %q", id, got)
		}
	}
	if linearSchedFile("a-b") == linearSchedFile("a_b") {
		t.Error("two different ids collapsed to one filename")
	}
}
