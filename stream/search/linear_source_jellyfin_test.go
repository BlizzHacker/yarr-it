package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// jellyfinEpisodes makes n half-hour episodes of one series.
func jellyfinEpisodes(n int) []jellyfinItem {
	out := make([]jellyfinItem, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, jellyfinItem{
			ID: fmt.Sprintf("ep%03d", i), Name: fmt.Sprintf("Episode %d", i), Type: "Episode",
			RunTimeTicks: 1800 * jellyfinTicksPerSecond,
			SeriesName:   "A Show", SeriesID: "series1",
			ParentIndexNumber: 1, IndexNumber: i,
			Genres:       []string{"Comedy"},
			Studios:      []jellyfinNameID{{Name: "NBC"}},
			LocationType: "FileSystem",
		})
	}
	return out
}

func jellyfinLibraryFixture(items []jellyfinItem) *jellyfinFixture {
	f := jellyfinPlayableFixture()
	f.views = []jellyfinVirtualFolder{
		{Name: "TV", ItemID: "view-tv", CollectionType: "tvshows"},
		{Name: "Movies", ItemID: "view-mov", CollectionType: "movies"},
		// Music must never be offered as a source for a video channel.
		{Name: "Music", ItemID: "view-mus", CollectionType: "music"},
	}
	f.pages = map[string][]jellyfinItem{"view-tv": items, "view-mov": nil}
	return f
}

// --- library ----------------------------------------------------------------

// A library larger than one page must come back whole. Getting this wrong
// silently gives a channel the first 500 items of 35,650 and no way to tell.
func TestJellyfinLinearLibraryPagesTheWholeLibrary(t *testing.T) {
	f := jellyfinLibraryFixture(jellyfinEpisodes(1250))
	p := newTestJellyfin(t, f)
	lib := &jellyfinLinearLibrary{p: p, view: f.views[0], itemTypes: []string{"Episode"}}

	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1250 {
		t.Fatalf("got %d items from a 1,250-item library", len(items))
	}
	if items[0].DurationSeconds != 1800 {
		t.Errorf("duration %d, want 1800", items[0].DurationSeconds)
	}
	if items[0].LibraryID != "TV" {
		t.Errorf("libraryId %q, want the library's name", items[0].LibraryID)
	}
	// It must have taken more than one request, or the paging is not being
	// exercised and the assertion above proves nothing.
	if len(f.pageRequests) < 3 {
		t.Errorf("expected several pages, saw %d requests", len(f.pageRequests))
	}
}

// The pool limit is a cap, not a suggestion: an unbounded fetch against a busy
// server is how a guide request turns into a two-minute stall.
func TestJellyfinLinearLibraryStopsAtThePoolLimit(t *testing.T) {
	f := jellyfinLibraryFixture(jellyfinEpisodes(1250))
	p := newTestJellyfin(t, f)
	p.poolLimit = 600
	lib := &jellyfinLinearLibrary{p: p, view: f.views[0], itemTypes: []string{"Episode"}}

	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 600 {
		t.Fatalf("got %d items, want the 600-item cap", len(items))
	}
}

// The name is what a person types into a channel and what the "library" rule
// field compares against. A GUID would be tidier and unusable by hand.
func TestJellyfinLinearLibraryIDIsTheLibraryName(t *testing.T) {
	lib := &jellyfinLinearLibrary{
		p:    newJellyfinProvider(jellyfinConfig{BaseURL: "http://jf.test", APIKey: "k"}),
		view: jellyfinVirtualFolder{Name: "90s Cartoons", ItemID: "abc-guid"},
	}
	if got := lib.LinearLibraryID(); got != "90s Cartoons" {
		t.Fatalf("LinearLibraryID = %q", got)
	}
	if got := lib.LinearProviderID(); got != "jellyfin" {
		t.Fatalf("LinearProviderID = %q", got)
	}
}

// A TV library contributes episodes and not series. A series has no runtime, so
// scheduling one is impossible; including them would fill the preview with
// hundreds of "matched but not schedulable" rows and explain nothing.
func TestJellyfinScheduleTypesExcludeThingsWithNoRuntime(t *testing.T) {
	if got := jellyfinScheduleTypes("tvshows"); len(got) != 1 || got[0] != "Episode" {
		t.Errorf("a TV library contributes %v, want [Episode]", got)
	}
	if got := jellyfinScheduleTypes("movies"); len(got) != 1 || got[0] != "Movie" {
		t.Errorf("a movie library contributes %v, want [Movie]", got)
	}
	for _, ty := range []string{"tvshows", "movies", "homevideos", "", "mixed"} {
		for _, w := range jellyfinScheduleTypes(ty) {
			if w == "Series" || w == "Season" {
				t.Errorf("library type %q contributes %q, which has no runtime", ty, w)
			}
		}
	}
}

func TestNewJellyfinLinearSkipsNonVideoLibraries(t *testing.T) {
	f := jellyfinLibraryFixture(jellyfinEpisodes(4))
	srv := f.server(t)

	_, libs, resolver, err := newJellyfinLinear(context.Background(), jellyfinConfig{
		BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolver == nil {
		t.Fatal("no resolver")
	}
	var names []string
	for _, l := range libs {
		names = append(names, l.LinearLibraryID())
	}
	if len(names) != 2 {
		t.Fatalf("libraries %v, want the two video ones", names)
	}
	for _, n := range names {
		if n == "Music" {
			t.Error("a music library was offered as a source for a video channel")
		}
	}
}

// A Jellyfin that is mid-restart cannot list its libraries but is still a
// configured instance. It must appear in settings with an honest health state
// rather than vanish.
func TestNewJellyfinLinearStillReturnsAProviderWhenDiscoveryFails(t *testing.T) {
	f := jellyfinHealthyFixture()
	srv := f.server(t)
	addr := srv.URL
	srv.Close()

	p, libs, resolver, err := newJellyfinLinear(context.Background(), jellyfinConfig{BaseURL: addr, APIKey: "k"})
	if err == nil {
		t.Fatal("discovery against a dead server must report an error")
	}
	if p == nil || resolver == nil {
		t.Fatal("the provider and resolver must come back anyway, so the instance is still visible")
	}
	if len(libs) != 0 {
		t.Errorf("libraries %v, want none", libs)
	}
	if got := p.Health(context.Background()).State; got != HealthUnreachable {
		t.Errorf("health %q, want %q", got, HealthUnreachable)
	}
}

// --- resolver ---------------------------------------------------------------

func TestJellyfinResolverRejectsAnIDItDidNotIssue(t *testing.T) {
	r := jellyfinLinearResolver{p: newTestJellyfin(t, jellyfinPlayableFixture())}
	_, err := r.LinearResolve(context.Background(), Program{CanonicalID: "plex:movie:1"}, StreamOptions{})
	if !errors.Is(err, ErrLinearMissingMedia) {
		t.Fatalf("err = %v, want ErrLinearMissingMedia so the engine stops retrying", err)
	}
}

func TestJellyfinResolverReportsADeletedFileAsMissingMedia(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playbackCode = 404
	r := jellyfinLinearResolver{p: newTestJellyfin(t, f)}

	_, err := r.LinearResolve(context.Background(),
		Program{CanonicalID: "jellyfin:movie:gone", Title: "A Film"}, StreamOptions{})
	if !errors.Is(err, ErrLinearMissingMedia) {
		t.Fatalf("err = %v, want ErrLinearMissingMedia", err)
	}
	if !strings.Contains(err.Error(), "A Film") {
		t.Errorf("the message must name the programme, got %q", err)
	}
}

// A transient failure must NOT be reported as missing, or the engine gives up
// on a server that was only briefly busy.
func TestJellyfinResolverKeepsTransientFailuresRetryable(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playbackCode = 503
	r := jellyfinLinearResolver{p: newTestJellyfin(t, f)}

	_, err := r.LinearResolve(context.Background(),
		Program{CanonicalID: "jellyfin:movie:item1"}, StreamOptions{})
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrLinearMissingMedia) {
		t.Fatal("a 503 was reported as missing media; the engine would stop retrying a server that is merely busy")
	}
}

func TestJellyfinResolverPassesTheOffsetThrough(t *testing.T) {
	r := jellyfinLinearResolver{p: newTestJellyfin(t, jellyfinPlayableFixture())}

	src, err := r.LinearResolve(context.Background(),
		Program{CanonicalID: "jellyfin:movie:item1"}, StreamOptions{OffsetSeconds: 900})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(src.URL)
	if got := jellyfinFirstQuery(u, "startTimeTicks", "StartTimeTicks"); got != "9000000000" {
		t.Fatalf("startTimeTicks=%q, want 9000000000 (900s)", got)
	}
}

// --- the whole path ---------------------------------------------------------

// The test this whole exercise exists for.
//
// A real engine, a real channel, a Jellyfin library behind an HTTP server, and
// a clock standing 37 minutes into a programme. What comes out must be a URL
// that asks Jellyfin to start 2,220 seconds in -- not a URL plus a note, and
// not a URL that starts at zero.
func TestLinearTuneInHandsBackAJellyfinURLAtTheLiveOffset(t *testing.T) {
	f := jellyfinLibraryFixture(jellyfinEpisodes(12))
	// Half-hour episodes, so a channel anchored at local midnight is exactly 37
	// minutes into its 41st programme at 20:37 -- and 7 minutes into the one
	// that began at 20:30.
	p := newTestJellyfin(t, f)

	at := time.Date(2026, 6, 1, 20, 37, 0, 0, time.UTC)
	clk := &linearClock{t: at}
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL, e.retryBackoff = clk.now, time.Hour, 24*time.Hour, 0
	e.AddLibrary(&jellyfinLinearLibrary{p: p, view: f.views[0], itemTypes: []string{"Episode"}})
	e.SetResolver(jellyfinLinearResolver{p: p})

	ch := LinearChannel{
		ID: "jf-tv", Number: 7, Name: "Jellyfin TV", Enabled: true,
		SourceProvider: "jellyfin", SourceLibraries: []string{"TV"},
		ScheduleStrategy: StrategyCyclic, Timezone: "UTC", EPGDays: 1, Seed: 99,
	}
	if _, err := e.SaveChannel(ch); err != nil {
		t.Fatal(err)
	}

	tune := e.TuneIn(context.Background(), "jf-tv", "UTC")
	if tune.State != LinearStateOnAir {
		t.Fatalf("state %q (%s), want on-air", tune.State, tune.Detail)
	}
	if !tune.Available || tune.Source == nil {
		t.Fatalf("nothing playable came back: %s", tune.Detail)
	}
	// Half-hour programmes anchored at midnight: at 20:37 the live one began at
	// 20:30, so the channel is 7 minutes in.
	if tune.OffsetSeconds != 420 {
		t.Fatalf("live offset %v, want 420 seconds", tune.OffsetSeconds)
	}

	u, err := url.Parse(tune.Source.URL)
	if err != nil {
		t.Fatal(err)
	}
	ticks, _ := strconv.ParseInt(jellyfinFirstQuery(u, "startTimeTicks", "StartTimeTicks"), 10, 64)
	want := int64(tune.OffsetSeconds) * jellyfinTicksPerSecond
	if ticks != want {
		t.Fatalf("the schedule says %v seconds in, but the stream URL asks for %d ticks (%v seconds); want %d",
			tune.OffsetSeconds, ticks, jellyfinSeconds(ticks), want)
	}
	if tune.Source.DirectPlay {
		t.Error("an offset start went through the transcode pipeline and must not claim direct play")
	}

	// And it must move. Advance past the programme boundary and the channel
	// rolls over on its own, to a new programme at a new offset.
	before := tune.Program.CanonicalID
	clk.advance(25 * time.Minute) // 21:02 -- 2 minutes into the 21:00 programme
	next := e.TuneIn(context.Background(), "jf-tv", "UTC")
	if next.Program == nil || next.Program.CanonicalID == before {
		t.Fatal("the channel did not roll over to the next programme")
	}
	if next.OffsetSeconds != 120 {
		t.Fatalf("after rollover the offset is %v, want 120 seconds", next.OffsetSeconds)
	}
	u2, _ := url.Parse(next.Source.URL)
	ticks2, _ := strconv.ParseInt(jellyfinFirstQuery(u2, "startTimeTicks", "StartTimeTicks"), 10, 64)
	if ticks2 != 120*jellyfinTicksPerSecond {
		t.Fatalf("after rollover the URL asks for %d ticks, want %d", ticks2, 120*jellyfinTicksPerSecond)
	}
}

// Two viewers tuning in at the same instant must land on the same frame. That
// is what makes it a channel rather than a playlist.
func TestTwoViewersOfAJellyfinChannelGetTheSameOffset(t *testing.T) {
	f := jellyfinLibraryFixture(jellyfinEpisodes(12))
	p := newTestJellyfin(t, f)

	at := time.Date(2026, 6, 1, 20, 37, 0, 0, time.UTC)
	e := NewLinearEngine("")
	e.nowFn, e.pastBuffer, e.poolTTL, e.retryBackoff = (&linearClock{t: at}).now, time.Hour, 24*time.Hour, 0
	e.AddLibrary(&jellyfinLinearLibrary{p: p, view: f.views[0], itemTypes: []string{"Episode"}})
	e.SetResolver(jellyfinLinearResolver{p: p})
	if _, err := e.SaveChannel(LinearChannel{
		ID: "jf-tv", Number: 7, Name: "Jellyfin TV", Enabled: true,
		SourceProvider: "jellyfin", ScheduleStrategy: StrategyCyclic,
		Timezone: "UTC", EPGDays: 1, Seed: 99,
	}); err != nil {
		t.Fatal(err)
	}

	a := e.TuneIn(context.Background(), "jf-tv", "")
	b := e.TuneIn(context.Background(), "jf-tv", "")
	if a.Program == nil || b.Program == nil {
		t.Fatal("nothing on air")
	}
	if a.Program.CanonicalID != b.Program.CanonicalID || a.OffsetSeconds != b.OffsetSeconds {
		t.Fatalf("two viewers landed differently: %s@%v vs %s@%v",
			a.Program.CanonicalID, a.OffsetSeconds, b.Program.CanonicalID, b.OffsetSeconds)
	}
}

// --- the chain --------------------------------------------------------------

// linearStubResolver answers for exactly one id prefix, like a real one.
type linearStubResolver struct {
	prefix string
	url    string
	fail   error
}

func (s linearStubResolver) LinearResolve(_ context.Context, p Program, opts StreamOptions) (StreamSource, error) {
	if s.fail != nil {
		return StreamSource{}, s.fail
	}
	if !strings.HasPrefix(p.CanonicalID, s.prefix) {
		return StreamSource{}, fmt.Errorf("%w: not mine", ErrLinearMissingMedia)
	}
	return StreamSource{URL: fmt.Sprintf("%s?offset=%v", s.url, opts.OffsetSeconds)}, nil
}

func TestLinearResolverChainRoutesByID(t *testing.T) {
	chain := newLinearResolverChain(
		linearStubResolver{prefix: "jellyfin:", url: "http://jf/stream"},
		linearStubResolver{prefix: "plex:", url: "http://plex/stream"},
	)
	ctx := context.Background()

	got, err := chain.LinearResolve(ctx, Program{CanonicalID: "plex:movie:9"}, StreamOptions{OffsetSeconds: 12})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.URL, "http://plex/stream") {
		t.Fatalf("routed to %q", got.URL)
	}
	// The offset must survive the extra hop.
	if !strings.Contains(got.URL, "offset=12") {
		t.Fatalf("the chain dropped the offset: %q", got.URL)
	}
}

// Missing is only final when everyone agrees. Otherwise the first server's "I
// have never heard of this" would cancel the second server's good answer.
func TestLinearResolverChainOnlyReportsMissingWhenAllAgree(t *testing.T) {
	ctx := context.Background()

	all := newLinearResolverChain(
		linearStubResolver{prefix: "jellyfin:"},
		linearStubResolver{prefix: "plex:"},
	)
	if _, err := all.LinearResolve(ctx, Program{CanonicalID: "romm:release:1"}, StreamOptions{}); !errors.Is(err, ErrLinearMissingMedia) {
		t.Fatalf("err = %v, want ErrLinearMissingMedia", err)
	}

	// One member merely broken: the answer must stay retryable, not become
	// "this file is gone".
	mixed := newLinearResolverChain(
		linearStubResolver{prefix: "jellyfin:"},
		linearStubResolver{prefix: "plex:", fail: errors.New("connection refused")},
	)
	_, err := mixed.LinearResolve(ctx, Program{CanonicalID: "romm:release:1"}, StreamOptions{})
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrLinearMissingMedia) {
		t.Fatal("a broken server made the chain report missing media, which stops the engine retrying")
	}
}

func TestLinearResolverChainOfOneIsThatOne(t *testing.T) {
	one := linearStubResolver{prefix: "jellyfin:", url: "http://jf/stream"}
	if got := newLinearResolverChain(nil, one, nil); got != LinearResolver(one) {
		t.Fatalf("a chain of one wrapped it unnecessarily: %T", got)
	}
	if _, err := newLinearResolverChain().LinearResolve(
		context.Background(), Program{}, StreamOptions{}); !errors.Is(err, ErrLinearNoResolver) {
		t.Fatalf("an empty chain gave %v, want ErrLinearNoResolver", err)
	}
}
