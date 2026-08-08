package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// linearHourlyLibrary is six programmes of exactly one hour each. Round
// numbers make the arithmetic in these tests checkable by eye, which is what
// you want from the tests that guard the one invariant the feature is about.
func linearHourlyLibrary() *linearSliceLibrary {
	var items []LinearItem
	for i := 1; i <= 6; i++ {
		items = append(items, LinearItem{
			MediaItem: MediaItem{
				CanonicalID: fmt.Sprintf("hour:%d", i),
				Domain:      "video",
				Type:        "movie",
				Title:       fmt.Sprintf("The %d O'Clock Film", i),
			},
			DurationSeconds: 3600,
			Genres:          []string{"Drama"},
		})
	}
	return &linearSliceLibrary{provider: "fixture", library: "hourly", items: items}
}

// linearHourlyEngine stands at exactly 20:00 UTC with an hourly channel whose
// first programme therefore begins at 20:00.
func linearHourlyEngine(t *testing.T) (*LinearEngine, *linearClock) {
	t.Helper()
	at := time.Date(2026, 6, 1, 20, 0, 0, 0, time.UTC)
	clk := &linearClock{t: at}
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL, e.retryBackoff = clk.now, time.Hour, 24*time.Hour, 0
	e.AddLibrary(linearHourlyLibrary())
	ch := linearTestChannel("hourly", StrategyCyclic)
	ch.EPGDays = 1
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}
	return e, clk
}

// --- the subtraction --------------------------------------------------------

func TestLinearLiveOffsetIsNowMinusStart(t *testing.T) {
	start := time.Date(2026, 6, 1, 20, 0, 0, 0, time.UTC)
	p := Program{Title: "Something", StartTime: start.Unix(), EndTime: start.Add(time.Hour).Unix()}

	at := start.Add(37 * time.Minute)
	got := linearLiveOffset(p, at.Unix())
	if got != 37*time.Minute {
		t.Fatalf("tuning in at 20:37 to a programme that began at 20:00 gave an offset of %s, want 00:37:00", got)
	}
	if got := linearLiveOffset(p, start.Unix()); got != 0 {
		t.Errorf("tuning in exactly on the hour gave %s, want 0", got)
	}
	if got := linearLiveOffset(p, start.Add(-time.Minute).Unix()); got != 0 {
		t.Errorf("an offset before the start must clamp to 0, got %s", got)
	}
	if got := linearLiveOffset(p, start.Add(2*time.Hour).Unix()); got >= time.Hour {
		t.Errorf("an offset past the end must clamp inside the programme, got %s", got)
	}
}

// The same thing end to end: the channel, the schedule and the clock, with no
// hand-built Program anywhere.
func TestLinearTuningInMidProgrammeStartsMidProgramme(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	clk.advance(37 * time.Minute) // it is now 20:37

	np := e.NowPlaying(context.Background(), "hourly", "")
	if np.State != LinearStateOnAir || np.Program == nil {
		t.Fatalf("nothing on air at 20:37: %+v", np)
	}
	startedAt := time.Unix(np.Program.StartTime, 0).UTC()
	if startedAt.Hour() != 20 || startedAt.Minute() != 0 {
		t.Fatalf("the programme on air began at %s, expected 20:00", startedAt.Format("15:04:05"))
	}
	if np.OffsetSeconds != 2220 {
		t.Fatalf("playback offset is %.0fs, want 2220 (00:37:00)", np.OffsetSeconds)
	}
	if np.RemainingSeconds != 1380 {
		t.Errorf("remaining is %.0fs, want 1380", np.RemainingSeconds)
	}
	if np.Next == nil || time.Unix(np.Next.StartTime, 0).UTC().Hour() != 21 {
		t.Error("the guide did not say what is on at 21:00")
	}
}

// --- the invariant: one schedule, not one per viewer ------------------------

// Two clients that have never met, tuning to the same channel at the same
// wall-clock moment, must land on the same programme at the same offset. A
// channel that restarts from zero for each viewer is not television.
func TestTwoClientsSeeTheSameProgrammeAtTheSameOffset(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	clk.advance(2*time.Hour + 13*time.Minute) // 22:13, well past the first programme

	// Two independent readers. Neither has any state; each is arriving cold.
	clientA := e.NowPlaying(context.Background(), "hourly", "")
	clientB := e.NowPlaying(context.Background(), "hourly", "")

	if clientA.Program == nil || clientB.Program == nil {
		t.Fatalf("a client got nothing: A=%+v B=%+v", clientA, clientB)
	}
	if clientA.Program.CanonicalID != clientB.Program.CanonicalID {
		t.Fatalf("two clients are watching different programmes: %q and %q",
			clientA.Program.Title, clientB.Program.Title)
	}
	if clientA.OffsetSeconds != clientB.OffsetSeconds {
		t.Fatalf("two clients got different offsets: %.0fs and %.0fs", clientA.OffsetSeconds, clientB.OffsetSeconds)
	}
	// And crucially: neither started at zero. A per-viewer channel would give
	// both of them offset 0 and this test would pass while the feature failed.
	if clientA.OffsetSeconds != 780 {
		t.Fatalf("offset is %.0fs; at 22:13 on an hourly channel it must be 780 (00:13:00 into the 22:00 programme)",
			clientA.OffsetSeconds)
	}
	if time.Unix(clientA.Program.StartTime, 0).UTC().Hour() != 22 {
		t.Errorf("the programme on air began at %s, not 22:00", time.Unix(clientA.Program.StartTime, 0).UTC())
	}
}

// The sharpest version of the same failure: the *first* viewer. A channel
// whose schedule is generated when someone first asks starts its first
// programme at that instant, so the first person to tune in always gets offset
// zero -- per-viewer restart wearing a hat, and it passes a two-client test
// because both clients arrive after the damage is done.
func TestChannelWasAlreadyRunningBeforeAnyoneTunedIn(t *testing.T) {
	at := time.Date(2026, 6, 1, 20, 37, 0, 0, time.UTC)
	clk := &linearClock{t: at}
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL = clk.now, time.Hour, 24*time.Hour
	e.AddLibrary(linearHourlyLibrary())
	ch := linearTestChannel("cold", StrategyCyclic)
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}

	// The very first read this channel has ever had.
	np := e.NowPlaying(context.Background(), "cold", "")
	if np.Program == nil {
		t.Fatalf("nothing on air: %+v", np)
	}
	if np.OffsetSeconds == 0 {
		t.Fatal("the first viewer got offset 0: the channel began the moment they asked, instead of having been running")
	}
	if np.OffsetSeconds != 2220 {
		t.Fatalf("offset is %.0fs, want 2220: the 20:00 programme, 37 minutes in", np.OffsetSeconds)
	}
	if got := time.Unix(np.Program.StartTime, 0).UTC(); got.Hour() != 20 || got.Minute() != 0 {
		t.Errorf("the programme began at %s, want 20:00", got.Format("15:04:05"))
	}
	// And the schedule really does stretch back before the request.
	st := linearMustWindow(t, e, "cold")
	if st.Programs[0].StartTime >= at.Unix() {
		t.Error("the schedule has no past at all; the channel was born on request")
	}
}

// The same assertion over HTTP with two separate clients and no shared
// connection, because that is what "two clients" means in production.
func TestTwoHTTPClientsSeeTheSameLivePosition(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	clk.advance(3*time.Hour + 41*time.Minute) // 23:41
	srv := linearTestServer(t, e)

	fetch := func() LinearNowPlaying {
		t.Helper()
		// A fresh transport each time: no connection reuse, no cookies, no
		// shared anything between the two callers.
		c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := c.Get(srv.URL + linearRoutePrefix + "now?channel=hourly")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var np LinearNowPlaying
		if err := json.NewDecoder(resp.Body).Decode(&np); err != nil {
			t.Fatal(err)
		}
		return np
	}
	a, b := fetch(), fetch()
	if a.Program == nil || b.Program == nil {
		t.Fatalf("a client got nothing over HTTP: %+v / %+v", a, b)
	}
	if a.Program.CanonicalID != b.Program.CanonicalID || a.OffsetSeconds != b.OffsetSeconds {
		t.Fatalf("two HTTP clients disagree: %q@%.0fs vs %q@%.0fs",
			a.Program.Title, a.OffsetSeconds, b.Program.Title, b.OffsetSeconds)
	}
	if a.OffsetSeconds != 2460 {
		t.Fatalf("offset over HTTP is %.0fs, want 2460 (00:41:00 into the 23:00 programme)", a.OffsetSeconds)
	}
}

// Twenty simultaneous tune-ins, which is also the race the schedule generator
// has to survive: they arrive before anything has been generated at all.
func TestManySimultaneousClientsAgreeAndGenerateOneSchedule(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	clk.advance(90 * time.Minute) // 21:30

	const n = 20
	var wg sync.WaitGroup
	results := make([]LinearNowPlaying, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = e.NowPlaying(context.Background(), "hourly", "")
		}(i)
	}
	wg.Wait()

	first := results[0]
	if first.Program == nil {
		t.Fatalf("client 0 got nothing: %+v", first)
	}
	for i, r := range results {
		if r.Program == nil {
			t.Fatalf("client %d got nothing", i)
		}
		if r.Program.CanonicalID != first.Program.CanonicalID || r.OffsetSeconds != first.OffsetSeconds {
			t.Fatalf("client %d disagrees: %q@%.0fs vs %q@%.0fs",
				i, r.Program.Title, r.OffsetSeconds, first.Program.Title, first.OffsetSeconds)
		}
	}
	if first.OffsetSeconds != 1800 {
		t.Errorf("offset is %.0fs, want 1800", first.OffsetSeconds)
	}
	// One schedule, not twenty.
	st := linearMustWindow(t, e, "hourly")
	if err := linearCheckOrdering(st.Programs); err != nil {
		t.Fatalf("concurrent tune-ins corrupted the schedule: %v", err)
	}
}

// A second server process reading the same state must put a viewer in exactly
// the same place -- the schedule belongs to the channel, not to a process.
func TestASecondServerAgreesOnTheLivePosition(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 6, 1, 20, 0, 0, 0, time.UTC)
	clk := &linearClock{t: at}
	e1 := NewLinearEngine(dir)
	e1.nowFn, e1.pastBuffer, e1.poolTTL = clk.now, time.Hour, 24*time.Hour
	e1.AddLibrary(linearHourlyLibrary())
	ch := linearTestChannel("shared", StrategyCyclic)
	if _, err := e1.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}
	linearMustWindow(t, e1, "shared")

	clk.advance(4*time.Hour + 5*time.Minute) // 00:05 the next day
	a := e1.NowPlaying(context.Background(), "shared", "")

	e2 := NewLinearEngine(dir)
	e2.nowFn, e2.pastBuffer, e2.poolTTL = clk.now, time.Hour, 24*time.Hour
	e2.AddLibrary(linearHourlyLibrary())
	b := e2.NowPlaying(context.Background(), "shared", "")

	if a.Program == nil || b.Program == nil {
		t.Fatalf("nothing on air: %+v / %+v", a, b)
	}
	if a.Program.CanonicalID != b.Program.CanonicalID || a.OffsetSeconds != b.OffsetSeconds {
		t.Fatalf("a second server put the viewer somewhere else: %q@%.0fs vs %q@%.0fs",
			a.Program.Title, a.OffsetSeconds, b.Program.Title, b.OffsetSeconds)
	}
	if a.OffsetSeconds != 300 {
		t.Errorf("offset is %.0fs, want 300", a.OffsetSeconds)
	}
}

// --- rollover ---------------------------------------------------------------

// The channel moves on by itself. End is exclusive, so at the exact second one
// programme finishes the next one is already on at offset zero -- no timer,
// no client-side handoff, just the schedule and the clock.
func TestLinearRollsOverToTheNextProgrammeOnItsOwn(t *testing.T) {
	e, clk := linearHourlyEngine(t)

	clk.advance(59*time.Minute + 59*time.Second) // 20:59:59
	before := e.NowPlaying(context.Background(), "hourly", "")
	if before.Program == nil || before.OffsetSeconds != 3599 {
		t.Fatalf("at 20:59:59 the offset is %+v", before)
	}

	clk.advance(time.Second) // 21:00:00 exactly
	after := e.NowPlaying(context.Background(), "hourly", "")
	if after.Program == nil {
		t.Fatalf("the channel went dark at the rollover: %+v", after)
	}
	if after.Program.CanonicalID == before.Program.CanonicalID {
		t.Fatalf("at 21:00:00 the channel is still showing %q; it should have rolled over", after.Program.Title)
	}
	if after.Program.StartTime != before.Program.EndTime {
		t.Errorf("the next programme starts at %d but the last ended at %d", after.Program.StartTime, before.Program.EndTime)
	}
	if after.OffsetSeconds != 0 {
		t.Errorf("the new programme starts at offset %.0fs, want 0", after.OffsetSeconds)
	}

	clk.advance(90 * time.Second)
	later := e.NowPlaying(context.Background(), "hourly", "")
	if later.Program.CanonicalID != after.Program.CanonicalID || later.OffsetSeconds != 90 {
		t.Errorf("90 seconds after the rollover: %q@%.0fs", later.Program.Title, later.OffsetSeconds)
	}
}

func TestLinearIndexAtHandlesTheEdges(t *testing.T) {
	programs := []Program{
		{Title: "A", StartTime: 100, EndTime: 200},
		{Title: "B", StartTime: 200, EndTime: 300},
	}
	cases := []struct {
		now   int64
		want  string
		onAir bool
	}{
		{50, "", false},  // before the schedule begins
		{100, "A", true}, // exactly at the start
		{199, "A", true}, // the last second of A
		{200, "B", true}, // the boundary belongs to B
		{299, "B", true},
		{300, "", false}, // past the end of what exists
		{9999, "", false},
	}
	for _, tc := range cases {
		i, ok := linearIndexAt(programs, tc.now)
		if ok != tc.onAir {
			t.Errorf("at %d on-air=%v, want %v", tc.now, ok, tc.onAir)
			continue
		}
		if ok && programs[i].Title != tc.want {
			t.Errorf("at %d the programme is %q, want %q", tc.now, programs[i].Title, tc.want)
		}
	}
	if _, ok := linearIndexAt(nil, 100); ok {
		t.Error("an empty schedule reported something on air")
	}
}

// A gap left by a slot grid is a state a client can draw, not an error.
func TestLinearGapIsOffAirNotAFailure(t *testing.T) {
	clk := &linearClock{t: time.Date(2026, 6, 1, 20, 0, 0, 0, time.UTC)}
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL = clk.now, time.Hour, 24*time.Hour
	e.AddLibrary(linearFixtureLibrary())
	ch := linearTestChannel("gapped", StrategyCyclic)
	ch.Options.SlotMinutes = 60
	ch.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
		{Field: LinearFieldType, Op: LinearOpIs, Value: "episode"},
	}}
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}
	clk.advance(50 * time.Minute) // past a 22-26 minute episode in a 60 minute slot

	np := e.NowPlaying(context.Background(), "gapped", "")
	if np.State != LinearStateOffAir {
		t.Fatalf("in a scheduled gap the state is %q, want %q", np.State, LinearStateOffAir)
	}
	if np.Next == nil {
		t.Fatal("off air with no indication of when the channel comes back")
	}
	if !strings.Contains(np.Detail, "starts in") {
		t.Errorf("the off-air detail should say when the next programme starts, got %q", np.Detail)
	}
}

// --- source failures --------------------------------------------------------

type linearFakeResolver struct {
	mu         sync.Mutex
	calls      int
	failFor    map[string]error // by channel id
	lastOffset float64
}

func (r *linearFakeResolver) LinearResolve(_ context.Context, p Program, opts StreamOptions) (StreamSource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastOffset = opts.OffsetSeconds
	if err, ok := r.failFor[p.ChannelID]; ok {
		return StreamSource{}, err
	}
	return StreamSource{URL: "http://media/" + p.CanonicalID, MimeType: "video/mp4", DirectPlay: true, Seekable: true}, nil
}

func (r *linearFakeResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestLinearTuneInPassesTheScheduledOffsetToTheSource(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	res := &linearFakeResolver{}
	e.SetResolver(res)
	clk.advance(37 * time.Minute)

	got := e.TuneIn(context.Background(), "hourly", "")
	if !got.Available || got.Source == nil {
		t.Fatalf("tune-in failed: %+v", got)
	}
	if got.Source.URL == "" || !got.Source.Seekable {
		t.Errorf("source is %+v", *got.Source)
	}
	if res.lastOffset != 2220 {
		t.Fatalf("the source was asked for offset %.0fs, want 2220: a live source that is handed 0 restarts the programme", res.lastOffset)
	}
}

// A transient failure is retried, because the usual cause is a media server a
// moment behind a restart.
func TestLinearTransientSourceFailureIsRetriedThenReported(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	res := &linearFakeResolver{failFor: map[string]error{"hourly": errors.New("connection refused")}}
	e.SetResolver(res)
	e.retryAttempts, e.retryBackoff = 3, 0
	clk.advance(10 * time.Minute)

	got := e.TuneIn(context.Background(), "hourly", "")
	if got.Available {
		t.Fatal("a source that always fails reported itself available")
	}
	if res.count() != 3 {
		t.Errorf("the source was called %d times, want 3 attempts", res.count())
	}
	// The channel is still described. A client can draw the row, see what is
	// next, and change channel.
	if got.State != LinearStateOnAir || got.Program == nil {
		t.Fatalf("the failure erased what should be on air: %+v", got)
	}
	if got.Next == nil {
		t.Error("no next programme was offered as a way out")
	}
	if !strings.Contains(got.Detail, "cannot be played") {
		t.Errorf("the detail should explain the failure, got %q", got.Detail)
	}
}

// Missing media is not transient. Retrying it three times only makes the
// viewer wait longer for the same answer.
func TestLinearMissingMediaFailsFastAndSafely(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	res := &linearFakeResolver{failFor: map[string]error{"hourly": fmt.Errorf("no file at /mnt/x: %w", ErrLinearMissingMedia)}}
	e.SetResolver(res)
	e.retryAttempts, e.retryBackoff = 3, time.Second
	clk.advance(10 * time.Minute)

	start := time.Now()
	got := e.TuneIn(context.Background(), "hourly", "")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("missing media took %s; it should not have been retried at all", elapsed)
	}
	if res.count() != 1 {
		t.Errorf("missing media was tried %d times, want 1", res.count())
	}
	if got.Available {
		t.Fatal("missing media reported itself available")
	}
	if got.Program == nil || got.State != LinearStateOnAir {
		t.Errorf("missing media took the channel down instead of reporting itself: %+v", got)
	}
}

// The guide must never depend on the resolver. A failing source takes out one
// programme, not the listings.
func TestLinearGuideIsUnaffectedByAFailingSource(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	e.SetResolver(&linearFakeResolver{failFor: map[string]error{"hourly": errors.New("dead")}})
	e.retryAttempts, e.retryBackoff = 2, 0
	clk.advance(10 * time.Minute)

	if got := e.TuneIn(context.Background(), "hourly", ""); got.Available {
		t.Fatal("the source was supposed to be failing")
	}
	programs, err := e.Guide(context.Background(), []string{"hourly"}, 0, 0)
	if err != nil {
		t.Fatalf("the guide failed because a source did: %v", err)
	}
	if len(programs) == 0 {
		t.Fatal("the guide emptied because a source failed")
	}
	np := e.NowPlaying(context.Background(), "hourly", "")
	if np.State != LinearStateOnAir {
		t.Errorf("NowPlaying reports %q while the guide is fine", np.State)
	}
}

// The escape hatch has to work: when one channel's source is broken, changing
// channel must land you somewhere that plays.
func TestLinearChannelChangeWorksWhileAnotherChannelIsBroken(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	good := linearTestChannel("good", StrategyCyclic)
	good.Number = 102
	if _, err := e.SaveChannel(good); err != nil {
		t.Fatal(err)
	}
	e.SetResolver(&linearFakeResolver{failFor: map[string]error{"hourly": errors.New("dead")}})
	e.retryAttempts, e.retryBackoff = 2, 0
	clk.advance(20 * time.Minute)

	broken := e.TuneIn(context.Background(), "hourly", "")
	if broken.Available {
		t.Fatal("the broken channel played")
	}
	changed := e.TuneIn(context.Background(), "good", "")
	if !changed.Available || changed.Source == nil {
		t.Fatalf("changing channel away from a broken one failed: %+v", changed)
	}
	if changed.OffsetSeconds <= 0 {
		t.Errorf("the channel changed to started from zero (offset %.0fs); it is live, not a playlist", changed.OffsetSeconds)
	}
	// Both channels are still listed.
	chans, err := e.Channels(context.Background())
	if err != nil || len(chans) != 2 {
		t.Fatalf("the channel list is %d channels, %v", len(chans), err)
	}
}

func TestLinearStreamEndpointReportsUnavailableWithoutHidingTheSchedule(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	e.SetResolver(&linearFakeResolver{failFor: map[string]error{"hourly": errors.New("dead")}})
	e.retryAttempts, e.retryBackoff = 1, 0
	clk.advance(15 * time.Minute)
	srv := linearTestServer(t, e)

	resp, err := http.Get(srv.URL + linearRoutePrefix + "stream?channel=hourly")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a failing source returned %d, want 503", resp.StatusCode)
	}
	var body LinearTuneIn
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Program == nil || body.Next == nil {
		t.Fatalf("the 503 carried no schedule, so a client has nothing to do with it: %+v", body)
	}
	if body.OffsetSeconds != 900 {
		t.Errorf("the 503 reported offset %.0f, want 900", body.OffsetSeconds)
	}
}

func TestLinearTuneInWithoutAResolverSaysSo(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	clk.advance(time.Minute)
	got := e.TuneIn(context.Background(), "hourly", "")
	if got.Available {
		t.Fatal("a channel with no resolver reported itself playable")
	}
	if !strings.Contains(got.Detail, "resolver") {
		t.Errorf("detail is %q; it should name what is missing", got.Detail)
	}
}

func TestLinearNowPlayingOnAnUnknownChannel(t *testing.T) {
	e, _ := linearHourlyEngine(t)
	np := e.NowPlaying(context.Background(), "does-not-exist", "")
	if np.State != LinearStateUnknown {
		t.Errorf("state is %q, want %q", np.State, LinearStateUnknown)
	}
	if np.Detail == "" {
		t.Error("no explanation for an unknown channel")
	}
}

func TestLinearDisabledChannelIsAState(t *testing.T) {
	e, _ := linearHourlyEngine(t)
	ch, _ := e.Channel("hourly")
	ch.Enabled = false
	if _, err := e.SaveChannel(*ch); err != nil {
		t.Fatal(err)
	}
	np := e.NowPlaying(context.Background(), "hourly", "")
	if np.State != LinearStateDisabled {
		t.Errorf("state is %q, want %q", np.State, LinearStateDisabled)
	}
}

func TestLinearNowPlayingRendersLocalTimesWhenAsked(t *testing.T) {
	e, clk := linearHourlyEngine(t)
	clk.advance(10 * time.Minute)
	np := e.NowPlaying(context.Background(), "hourly", "America/Chicago")
	if !strings.HasPrefix(np.StartLocal, "2026-06-01T15:00:00-05:00") {
		t.Errorf("startLocal is %q, want 15:00-05:00 for a 20:00 UTC start", np.StartLocal)
	}
	plain := e.NowPlaying(context.Background(), "hourly", "")
	if plain.StartLocal != "" {
		t.Errorf("a client that asked for no zone got %q", plain.StartLocal)
	}
}
