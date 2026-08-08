package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// --- the unit conversion ----------------------------------------------------

// The one that matters most. Jellyfin counts in ticks, ten million to the
// second, and every wrong answer here is plausible: seconds looks like the
// start of the file, milliseconds looks like the start of the file, and both
// produce a URL that a player accepts without complaint.
func TestJellyfinTicksAreTenMillionToTheSecond(t *testing.T) {
	cases := []struct {
		seconds float64
		want    int64
	}{
		{0, 0},
		{1, 10_000_000},
		// The offset from the engine's own worked example: a programme that
		// began 37 minutes ago.
		{2220, 22_200_000_000},
		{0.5, 5_000_000},
		{3600, 36_000_000_000},
		// Negatives cannot be seeked to and must not be handed on as negative
		// ticks, which Jellyfin silently reads as zero.
		{-5, 0},
	}
	for _, c := range cases {
		if got := jellyfinTicks(c.seconds); got != c.want {
			t.Errorf("jellyfinTicks(%v) = %d, want %d", c.seconds, got, c.want)
		}
	}

	// Guard the specific mistakes, so a future edit that introduces one fails
	// here rather than in front of a viewer.
	if got := jellyfinTicks(2220); got == 2220 {
		t.Error("2220 seconds came back as 2220 ticks: seconds were passed straight through")
	}
	if got := jellyfinTicks(2220); got == 2_220_000 {
		t.Error("2220 seconds came back as milliseconds")
	}
	if got := jellyfinSeconds(jellyfinTicks(2220)); got != 2220 {
		t.Errorf("round trip of 2220s gave %v", got)
	}
}

func TestJellyfinClampsOffsetInsideTheRuntime(t *testing.T) {
	runtime := int64(3600) * jellyfinTicksPerSecond

	if got := jellyfinClampTicks(1200*jellyfinTicksPerSecond, runtime); got != 1200*jellyfinTicksPerSecond {
		t.Errorf("an offset inside the file was altered: %d", got)
	}
	// Past the end: one second short, because a seek to the final tick yields a
	// stream with nothing in it, which looks exactly like missing media.
	got := jellyfinClampTicks(7200*jellyfinTicksPerSecond, runtime)
	if got != runtime-jellyfinTicksPerSecond {
		t.Errorf("an offset past the end gave %d, want %d", got, runtime-jellyfinTicksPerSecond)
	}
	if got := jellyfinClampTicks(-1, runtime); got != 0 {
		t.Errorf("a negative offset gave %d, want 0", got)
	}
	// A file with no known runtime must not have its offset thrown away.
	if got := jellyfinClampTicks(500*jellyfinTicksPerSecond, 0); got != 500*jellyfinTicksPerSecond {
		t.Errorf("an unknown runtime discarded the offset: %d", got)
	}
}

// --- fixture server ---------------------------------------------------------

// jellyfinFixture is a stand-in Jellyfin. Every field is a knob a test turns to
// describe one server condition; nothing here reaches the network.
type jellyfinFixture struct {
	version      string
	wizardDone   bool
	publicStatus int
	sysStatus    int
	sysBody      jellyfinSystemInfo

	// itemsByID answers /Items?ids=
	itemsByID map[string]jellyfinItem
	// pages answers /Items listings, keyed by parentId ("" for the whole
	// server). Paged by the fixture using startIndex/limit.
	pages map[string][]jellyfinItem
	// views answers /Library/VirtualFolders
	views []jellyfinVirtualFolder

	playback     *jellyfinPlaybackInfo
	playbackCode int
	// rejectDeviceProfile reproduces Jellyfin 10.11, which answers 400 to any
	// PlaybackInfo carrying a device profile it cannot evaluate a user for.
	rejectDeviceProfile bool

	// observed, for assertions
	lastAuth        string
	lastPlaybackReq map[string]any
	sawProfile      bool
	sawProfileless  bool
	pageRequests    []url.Values
}

func (f *jellyfinFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/System/Info/Public", func(w http.ResponseWriter, r *http.Request) {
		if f.publicStatus != 0 && f.publicStatus != 200 {
			w.WriteHeader(f.publicStatus)
			return
		}
		writeJSON(w, 200, jellyfinPublicInfo{
			Version: f.version, ServerName: "FIXTURE", ID: "fix",
			ProductName: "Jellyfin Server", StartupWizardCompleted: f.wizardDone,
		})
	})

	mux.HandleFunc("/System/Info", func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		if f.sysStatus != 0 && f.sysStatus != 200 {
			w.WriteHeader(f.sysStatus)
			return
		}
		body := f.sysBody
		if body.Version == "" {
			body.Version = f.version
		}
		writeJSON(w, 200, body)
	})

	mux.HandleFunc("/Library/VirtualFolders", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, f.views)
	})

	mux.HandleFunc("/Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.pageRequests = append(f.pageRequests, q)
		if ids := q.Get("ids"); ids != "" {
			it, ok := f.itemsByID[ids]
			if !ok {
				writeJSON(w, 200, jellyfinItemsPage{})
				return
			}
			writeJSON(w, 200, jellyfinItemsPage{Items: []jellyfinItem{it}, TotalRecordCount: 1})
			return
		}
		all := f.pages[q.Get("parentId")]
		start, _ := strconv.Atoi(q.Get("startIndex"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		if start > len(all) {
			start = len(all)
		}
		end := start + limit
		if limit <= 0 || end > len(all) {
			end = len(all)
		}
		writeJSON(w, 200, jellyfinItemsPage{
			Items: all[start:end], TotalRecordCount: len(all), StartIndex: start,
		})
	})

	mux.HandleFunc("/Items/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/PlaybackInfo") {
			w.WriteHeader(404)
			return
		}
		if f.playbackCode != 0 && f.playbackCode != 200 {
			w.WriteHeader(f.playbackCode)
			return
		}
		f.lastPlaybackReq = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&f.lastPlaybackReq)
		if _, hasProfile := f.lastPlaybackReq["DeviceProfile"]; hasProfile {
			f.sawProfile = true
			if f.rejectDeviceProfile {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else {
			f.sawProfileless = true
		}
		if f.playback == nil {
			writeJSON(w, 200, jellyfinPlaybackInfo{})
			return
		}
		writeJSON(w, 200, *f.playback)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func jellyfinHealthyFixture() *jellyfinFixture {
	return &jellyfinFixture{version: "10.11.11", wizardDone: true}
}

func newTestJellyfin(t *testing.T, f *jellyfinFixture) *jellyfinProvider {
	t.Helper()
	srv := f.server(t)
	return newJellyfinProvider(jellyfinConfig{
		BaseURL: srv.URL, APIKey: "test-key", HTTPClient: srv.Client(),
	})
}

// --- health -----------------------------------------------------------------

// All six states, each reachable and each distinct. They are not collapsed into
// "down" because each implies a different fix, and telling someone with a wrong
// key that the server is unreachable sends them hunting a network fault.
func TestJellyfinHealthDistinguishesAllSixStates(t *testing.T) {
	ctx := context.Background()

	t.Run("not_configured without an address", func(t *testing.T) {
		p := newJellyfinProvider(jellyfinConfig{APIKey: "k"})
		if got := p.Health(ctx).State; got != HealthNotConfigured {
			t.Fatalf("state %q, want %q", got, HealthNotConfigured)
		}
	})

	t.Run("not_configured without a key", func(t *testing.T) {
		p := newJellyfinProvider(jellyfinConfig{BaseURL: "http://example.invalid"})
		h := p.Health(ctx)
		if h.State != HealthNotConfigured {
			t.Fatalf("state %q, want %q", h.State, HealthNotConfigured)
		}
		if !strings.Contains(h.Detail, "API key") {
			t.Errorf("detail must say what to do, got %q", h.Detail)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		f := jellyfinHealthyFixture()
		srv := f.server(t)
		addr := srv.URL
		srv.Close() // nothing is listening now
		p := newJellyfinProvider(jellyfinConfig{BaseURL: addr, APIKey: "k"})
		if got := p.Health(ctx).State; got != HealthUnreachable {
			t.Fatalf("state %q, want %q", got, HealthUnreachable)
		}
	})

	t.Run("auth_failed", func(t *testing.T) {
		f := jellyfinHealthyFixture()
		f.sysStatus = 401
		h := newTestJellyfin(t, f).Health(ctx)
		if h.State != HealthAuthFailed {
			t.Fatalf("state %q, want %q", h.State, HealthAuthFailed)
		}
		if h.Version != "10.11.11" {
			t.Errorf("a rejected key still knows the version, got %q", h.Version)
		}
	})

	t.Run("incompatible by version", func(t *testing.T) {
		f := jellyfinHealthyFixture()
		f.version = "10.7.7"
		h := newTestJellyfin(t, f).Health(ctx)
		if h.State != HealthIncompatible {
			t.Fatalf("state %q, want %q", h.State, HealthIncompatible)
		}
	})

	t.Run("incompatible when the address is not Jellyfin", func(t *testing.T) {
		f := jellyfinHealthyFixture()
		f.publicStatus = 404
		if got := newTestJellyfin(t, f).Health(ctx).State; got != HealthIncompatible {
			t.Fatalf("state %q, want %q", got, HealthIncompatible)
		}
	})

	t.Run("degraded while first-run setup is unfinished", func(t *testing.T) {
		f := jellyfinHealthyFixture()
		f.wizardDone = false
		if got := newTestJellyfin(t, f).Health(ctx).State; got != HealthDegraded {
			t.Fatalf("state %q, want %q", got, HealthDegraded)
		}
	})

	t.Run("degraded when a restart is pending", func(t *testing.T) {
		f := jellyfinHealthyFixture()
		f.sysBody = jellyfinSystemInfo{HasPendingRestart: true}
		if got := newTestJellyfin(t, f).Health(ctx).State; got != HealthDegraded {
			t.Fatalf("state %q, want %q", got, HealthDegraded)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		h := newTestJellyfin(t, jellyfinHealthyFixture()).Health(ctx)
		if h.State != HealthOK {
			t.Fatalf("state %q (%s), want %q", h.State, h.Detail, HealthOK)
		}
		if h.Version != "10.11.11" {
			t.Errorf("version %q", h.Version)
		}
	})
}

// The token belongs in a header, not a query string, for every API call. The
// one exception is a URL handed to a player, which is asserted separately.
func TestJellyfinAuthenticatesWithAHeader(t *testing.T) {
	f := jellyfinHealthyFixture()
	p := newTestJellyfin(t, f)
	p.Health(context.Background())
	if !strings.HasPrefix(f.lastAuth, "MediaBrowser Token=") {
		t.Fatalf("Authorization header was %q", f.lastAuth)
	}
}

// --- streaming --------------------------------------------------------------

func jellyfinPlayableFixture() *jellyfinFixture {
	f := jellyfinHealthyFixture()
	f.playback = &jellyfinPlaybackInfo{
		PlaySessionID: "sess",
		MediaSources: []jellyfinMediaSource{{
			ID: "src1", Container: "mkv", RunTimeTicks: 3600 * jellyfinTicksPerSecond,
			SupportsDirectPlay: true, SupportsDirectStream: true, SupportsTranscoding: true,
			TranscodingURL:         "/videos/item1/master.m3u8?MediaSourceId=src1&StartTimeTicks=22200000000&api_key=test-key",
			TranscodingSubProtocol: "hls",
		}},
	}
	return f
}

// At offset zero the honest and best answer is the file itself: nothing to
// transcode, nothing to lose, and Jellyfin's static endpoint answers ranged
// requests so it really is seekable.
func TestJellyfinStreamPrefersDirectPlayAtOffsetZero(t *testing.T) {
	p := newTestJellyfin(t, jellyfinPlayableFixture())

	src, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !src.DirectPlay {
		t.Error("a directly playable source at offset zero must be reported as direct play")
	}
	if !src.Seekable {
		t.Error("the static endpoint honours byte ranges, so it must be reported seekable")
	}
	if !strings.Contains(src.URL, "static=true") {
		t.Errorf("expected the static endpoint, got %s", jellyfinRedact(src.URL))
	}
	if strings.Contains(src.URL, "startTimeTicks") {
		t.Errorf("a zero offset must not add a start position: %s", jellyfinRedact(src.URL))
	}
	if src.MimeType != "video/x-matroska" {
		t.Errorf("mime %q", src.MimeType)
	}
}

// The heart of it. The engine says "begin 2220 seconds in"; the URL that comes
// back must say so in the units Jellyfin reads.
func TestJellyfinStreamCarriesTheOffsetAsTicks(t *testing.T) {
	f := jellyfinPlayableFixture()
	p := newTestJellyfin(t, f)

	src, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 2220})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(src.URL)
	if err != nil {
		t.Fatal(err)
	}
	ticks := jellyfinFirstQuery(u, "startTimeTicks", "StartTimeTicks")
	if ticks != "22200000000" {
		t.Fatalf("the URL asks Jellyfin to start at %q ticks, want 22200000000 (2220s x 10,000,000)", ticks)
	}
	if src.DirectPlay {
		t.Error("an offset start goes through the transcode pipeline; claiming direct play would be a lie")
	}
	if !src.Seekable {
		t.Error("an HLS playlist covering the rest of the runtime is seekable")
	}

	// It must also have been sent in the PlaybackInfo body, which is what makes
	// Jellyfin's own plan come back pointed at the right place.
	if got, _ := f.lastPlaybackReq["StartTimeTicks"].(float64); int64(got) != 22_200_000_000 {
		t.Errorf("PlaybackInfo body carried StartTimeTicks=%v, want 22200000000", f.lastPlaybackReq["StartTimeTicks"])
	}
}

// Jellyfin normally embeds the start position in the plan it returns. When it
// does not -- an older build, a different negotiation -- the adapter must write
// it in rather than hand back a URL that plays from the beginning while looking
// perfectly healthy.
func TestJellyfinRepairsAPlanThatLostTheOffset(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playback.MediaSources[0].TranscodingURL = "/videos/item1/master.m3u8?MediaSourceId=src1"
	p := newTestJellyfin(t, f)

	src, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 2220})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(src.URL)
	if got := u.Query().Get("startTimeTicks"); got != "22200000000" {
		t.Fatalf("a plan with no start position was handed back unrepaired: %s", jellyfinRedact(src.URL))
	}

	// And a plan carrying the wrong position is corrected, not trusted.
	f.playback.MediaSources[0].TranscodingURL = "/videos/item1/master.m3u8?StartTimeTicks=1"
	src, err = p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 2220})
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(src.URL)
	if got := jellyfinFirstQuery(u, "startTimeTicks", "StartTimeTicks"); got != "22200000000" {
		t.Fatalf("a plan with the wrong start position was trusted: %q", got)
	}
}

// A plan with no api_key would 401 in the player and be reported as "the file
// will not play", which is a long way from the truth.
func TestJellyfinAddsTheKeyToAPlanThatOmittedIt(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playback.MediaSources[0].TranscodingURL = "/videos/item1/master.m3u8?MediaSourceId=src1&StartTimeTicks=22200000000"
	p := newTestJellyfin(t, f)

	src, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 2220})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(src.URL)
	if u.Query().Get("api_key") == "" {
		t.Fatal("the stream URL carries no credential and would 401 in the player")
	}
}

// No plan at all -- the normal case for an API key with no user. Fall back to
// the progressive endpoint.
//
// Deliberately NOT a hand-built master.m3u8: one of those was tried against a
// live Jellyfin 10.11 and produced a playlist whose segments carried
// runtimeTicks=0 and would not load. The offset was perfectly correct in the
// URL and nothing played, which is the worst shape a bug can take.
func TestJellyfinFallsBackToAProgressiveStreamWhenNoPlanIsOffered(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playback.MediaSources[0].TranscodingURL = ""
	p := newTestJellyfin(t, f)

	src, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src.URL, ".m3u8") {
		t.Fatalf("a hand-built playlist came back; its segments do not load: %s", jellyfinRedact(src.URL))
	}
	if !strings.Contains(src.URL, "/stream.mp4") {
		t.Fatalf("expected the progressive endpoint, got %s", jellyfinRedact(src.URL))
	}
	u, _ := url.Parse(src.URL)
	if got := u.Query().Get("startTimeTicks"); got != "6000000000" {
		t.Fatalf("startTimeTicks=%q, want 6000000000", got)
	}
	if src.MimeType != "video/mp4" {
		t.Errorf("mime %q", src.MimeType)
	}
	// A progressive transcode is produced as it is consumed and answers no
	// ranges. Claiming otherwise would have a client draw a scrub bar that
	// silently restarts the stream from the top.
	if src.Seekable {
		t.Error("a progressive transcode was reported seekable")
	}
	if src.DirectPlay {
		t.Error("a progressive transcode was reported as direct play")
	}
}

// An offset past the end of the file must be pulled back inside it, or the
// viewer gets a stream that ends immediately -- which on a channel is
// indistinguishable from the media having gone.
func TestJellyfinStreamClampsAnOffsetPastTheEnd(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playback.MediaSources[0].TranscodingURL = ""
	p := newTestJellyfin(t, f)

	src, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 99999})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(src.URL)
	got, _ := strconv.ParseInt(u.Query().Get("startTimeTicks"), 10, 64)
	want := int64(3599) * jellyfinTicksPerSecond
	if got != want {
		t.Fatalf("startTimeTicks=%d, want %d (one second short of the runtime)", got, want)
	}
}

// Found against a live Jellyfin 10.11 and invisible from the URL alone.
//
// Jellyfin caches a running transcode and hands the same one back to the same
// session for the same item -- the start position is not part of that key. Two
// tune-ins to one channel at different offsets therefore returned byte-for-byte
// the same stream, each URL carrying its own correct startTimeTicks and each
// answering 200. A fresh session id per request is what keeps the second
// viewer from being served the first viewer's position.
func TestJellyfinGivesEveryStreamItsOwnSession(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playback.MediaSources[0].TranscodingURL = ""
	p := newTestJellyfin(t, f)
	ctx := context.Background()

	first, err := p.Stream(ctx, "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Stream(ctx, "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 1810})
	if err != nil {
		t.Fatal(err)
	}
	sessionOf := func(raw string) string {
		u, _ := url.Parse(raw)
		return u.Query().Get("PlaySessionId")
	}
	a, b := sessionOf(first.URL), sessionOf(second.URL)
	if a == "" || b == "" {
		t.Fatal("no PlaySessionId: Jellyfin will serve a cached transcode at whatever position it already had")
	}
	if a == b {
		t.Fatal("two playbacks shared a session id; the second would be served the first one's position")
	}
}

func TestJellyfinStreamRejectsAnIDItDidNotIssue(t *testing.T) {
	p := newTestJellyfin(t, jellyfinPlayableFixture())
	if _, err := p.Stream(context.Background(), "plex:movie:99", StreamOptions{}); err == nil {
		t.Fatal("streaming a foreign id must fail rather than guess")
	}
}

// Found against a live Jellyfin 10.11, and worth a test of its own because the
// symptom was a lie: 10.11 evaluates the user's playback policy whenever a
// device profile is supplied, an API key carries no user, and the call comes
// back as a bare 400. Classified as "gone", that told the operator their file
// was missing when it was sitting on disk -- and stopped the engine retrying.
func TestJellyfinDoesNotCallARejectedRequestAMissingFile(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playbackCode = 400
	p := newTestJellyfin(t, f)

	_, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 60})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), errMediaItemGone.Error()) {
		t.Fatal("a 400 was reported as missing media; that blames the library for this adapter's bad request")
	}
}

// The other half of that fix: when a profile is what the server objected to,
// ask again without one rather than declaring a healthy file unplayable.
func TestJellyfinRetriesWithoutADeviceProfileWhenOneIsRejected(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.rejectDeviceProfile = true
	// A user id is what makes the adapter send a profile in the first place.
	srv := f.server(t)
	p := newJellyfinProvider(jellyfinConfig{
		BaseURL: srv.URL, APIKey: "test-key", UserID: "user1", HTTPClient: srv.Client(),
	})

	src, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 2220})
	if err != nil {
		t.Fatalf("the retry without a profile did not happen: %v", err)
	}
	u, _ := url.Parse(src.URL)
	if got := jellyfinFirstQuery(u, "startTimeTicks", "StartTimeTicks"); got != "22200000000" {
		t.Fatalf("the offset was lost on the retry: startTimeTicks=%q", got)
	}
	if !f.sawProfile {
		t.Error("the first attempt should have carried a profile")
	}
	if !f.sawProfileless {
		t.Error("the retry should have carried no profile")
	}
}

// Without a user configured, no profile is sent at all -- because on 10.11 one
// would simply fail.
func TestJellyfinSendsNoDeviceProfileWithoutAUser(t *testing.T) {
	f := jellyfinPlayableFixture()
	p := newTestJellyfin(t, f)

	if _, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{OffsetSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if f.sawProfile {
		t.Error("a device profile was sent with no user context, which Jellyfin 10.11 rejects")
	}
}

func TestJellyfinStreamReportsAGoneItemAsMissing(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playbackCode = 404
	p := newTestJellyfin(t, f)

	_, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), errMediaItemGone.Error()) {
		t.Fatalf("a 404 must be classified as gone so the caller stops retrying, got %v", err)
	}
}

// An item Jellyfin knows about with no media behind it is a deleted file, not a
// transient failure.
func TestJellyfinStreamReportsAnItemWithNoSourcesAsMissing(t *testing.T) {
	f := jellyfinPlayableFixture()
	f.playback = &jellyfinPlaybackInfo{}
	p := newTestJellyfin(t, f)

	if _, err := p.Stream(context.Background(), "jellyfin:movie:item1", StreamOptions{}); err == nil ||
		!strings.Contains(err.Error(), errMediaItemGone.Error()) {
		t.Fatalf("want a gone error, got %v", err)
	}
}

// --- library ----------------------------------------------------------------

func TestJellyfinLibraryStatus(t *testing.T) {
	f := jellyfinHealthyFixture()
	f.itemsByID = map[string]jellyfinItem{
		"have": {ID: "have", Name: "Held", Type: "Movie", LocationType: "FileSystem"},
		"ghost": {ID: "ghost", Name: "Announced", Type: "Episode",
			LocationType: "Virtual"},
	}
	p := newTestJellyfin(t, f)
	ctx := context.Background()

	for _, c := range []struct {
		id   string
		want LibraryState
	}{
		{"jellyfin:movie:have", StateAvailable},
		// Jellyfin knows the episode exists in the world and has no file for it.
		// Scheduling one is how a channel goes to black at 20:00.
		{"jellyfin:episode:ghost", StateMissing},
		{"jellyfin:movie:nope", StateMissing},
		// Not an id this provider issued: no opinion, rather than a wrong one.
		{"romm:release:5", StateUnknown},
	} {
		got, err := p.LibraryStatus(ctx, c.id)
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if got != c.want {
			t.Errorf("LibraryStatus(%s) = %q, want %q", c.id, got, c.want)
		}
	}
}

// --- mapping ----------------------------------------------------------------

func TestJellyfinCanonicalIDsRoundTrip(t *testing.T) {
	id := jellyfinCanonicalID("episode", "abc123")
	if id != "jellyfin:episode:abc123" {
		t.Fatalf("id %q", id)
	}
	got, ok := parseJellyfinID(id)
	if !ok || got != "abc123" {
		t.Fatalf("parse gave (%q,%v)", got, ok)
	}
	for _, bad := range []string{"", "abc123", "plex:movie:1", "jellyfin:", "jellyfin:movie:"} {
		if _, ok := parseJellyfinID(bad); ok {
			t.Errorf("%q was accepted as a Jellyfin id", bad)
		}
	}
}

// Types must be words schema.json knows. A word invented here would flow into
// MediaItem.Type and match nothing, which is the exact bug schema.json exists
// to stop.
func TestJellyfinTypesResolveThroughTheSchema(t *testing.T) {
	for _, jf := range []string{"Movie", "Episode", "Series", "Season", "Video", "MusicVideo", "Whatever"} {
		typ := jellyfinType(jf)
		if canonicalDomain(typ) != "video" {
			t.Errorf("Jellyfin type %q mapped to %q, which schema.json does not file under video", jf, typ)
		}
	}
}

func TestJellyfinMapsAnEpisodeOntoTheCanonicalShape(t *testing.T) {
	p := newJellyfinProvider(jellyfinConfig{BaseURL: "http://jf.test", APIKey: "k"})
	li := p.toLinearItem(jellyfinItem{
		ID: "e1", Name: "The One With The Thing", Type: "Episode",
		RunTimeTicks: 1320 * jellyfinTicksPerSecond,
		SeriesName:   "A Show", SeriesID: "s1",
		ParentIndexNumber: 3, IndexNumber: 7,
		ProductionYear: 1998,
		Genres:         []string{"Comedy", "Family"},
		Studios:        []jellyfinNameID{{Name: "NBC"}},
		OfficialRating: "TV-PG",
		ImageTags:      map[string]string{"Primary": "tag1"},
	}, "TV")

	if li.CanonicalID != "jellyfin:episode:e1" {
		t.Errorf("canonicalId %q", li.CanonicalID)
	}
	if li.DurationSeconds != 1320 {
		t.Errorf("duration %d seconds, want 1320", li.DurationSeconds)
	}
	// The series name must land in Subtitle: that is where the rule engine
	// looks for it, and where seriesKey falls back to when grouping episodes.
	if li.Subtitle != "A Show" {
		t.Errorf("subtitle %q, want the series name", li.Subtitle)
	}
	if li.Season != 3 || li.Episode != 7 {
		t.Errorf("season/episode %d/%d", li.Season, li.Episode)
	}
	if li.Network != "NBC" || li.Rating != "TV-PG" || li.LibraryID != "TV" {
		t.Errorf("facets: network=%q rating=%q library=%q", li.Network, li.Rating, li.LibraryID)
	}
	if !strings.HasPrefix(li.Artwork, "http://jf.test/Items/e1/Images/Primary") {
		t.Errorf("artwork %q", li.Artwork)
	}
	if !li.schedulable() {
		t.Error("an episode with a runtime must be schedulable")
	}
}

// --- helpers ----------------------------------------------------------------

func jellyfinFirstQuery(u *url.URL, keys ...string) string {
	q := u.Query()
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// jellyfinRedact keeps a credential out of a test failure message. A stream URL
// carries the API key by necessity, and a CI log is not the place for it.
func jellyfinRedact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable url>"
	}
	q := u.Query()
	for _, k := range []string{"api_key", "ApiKey", "X-Plex-Token"} {
		if q.Get(k) != "" {
			q.Set(k, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
