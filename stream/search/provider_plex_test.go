package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// --- fixture server ---------------------------------------------------------

// plexFixture is a stand-in Plex Media Server. Each field describes one server
// condition a test wants to talk about; nothing reaches the network.
type plexFixture struct {
	version      string
	machineID    string
	identStatus  int
	sectionsCode int
	sections     []plexDirectory

	// items answers /library/metadata/<ratingKey>
	items map[string]plexMetadata
	// pages answers /library/sections/<key>/all, keyed by "<key>/<type>"
	pages map[string][]plexMetadata

	// observed
	pageRequests []url.Values
	lastToken    string
}

func (f *plexFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/identity", func(w http.ResponseWriter, r *http.Request) {
		if f.identStatus != 0 && f.identStatus != 200 {
			w.WriteHeader(f.identStatus)
			return
		}
		var c plexContainer
		c.MediaContainer.Version = f.version
		c.MediaContainer.MachineIdentifier = f.machineID
		writeJSON(w, 200, c)
	})

	mux.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		f.lastToken = r.Header.Get("X-Plex-Token")
		if f.sectionsCode != 0 && f.sectionsCode != 200 {
			w.WriteHeader(f.sectionsCode)
			return
		}
		var c plexContainer
		c.MediaContainer.Directory = f.sections
		c.MediaContainer.Size = len(f.sections)
		writeJSON(w, 200, c)
	})

	mux.HandleFunc("/library/sections/", func(w http.ResponseWriter, r *http.Request) {
		// /library/sections/<key>/all
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 || parts[3] != "all" {
			w.WriteHeader(404)
			return
		}
		q := r.URL.Query()
		f.pageRequests = append(f.pageRequests, q)
		all := f.pages[parts[2]+"/"+q.Get("type")]
		start, _ := strconv.Atoi(q.Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(q.Get("X-Plex-Container-Size"))
		if start > len(all) {
			start = len(all)
		}
		end := start + size
		if size <= 0 || end > len(all) {
			end = len(all)
		}
		var c plexContainer
		c.MediaContainer.Metadata = all[start:end]
		c.MediaContainer.Size = end - start
		c.MediaContainer.TotalSize = len(all)
		writeJSON(w, 200, c)
	})

	mux.HandleFunc("/library/metadata/", func(w http.ResponseWriter, r *http.Request) {
		rk := strings.TrimPrefix(r.URL.Path, "/library/metadata/")
		m, ok := f.items[rk]
		if !ok {
			w.WriteHeader(404)
			return
		}
		var c plexContainer
		c.MediaContainer.Metadata = []plexMetadata{m}
		c.MediaContainer.Size = 1
		writeJSON(w, 200, c)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func plexHealthyFixture() *plexFixture {
	return &plexFixture{
		version:   "1.43.3.10861-07dfddaeb",
		machineID: "e8512748115b1a1b",
		sections: []plexDirectory{
			{Key: "1", Type: "movie", Title: "Movies"},
			{Key: "6", Type: "show", Title: "Cartoons"},
			{Key: "4", Type: "artist", Title: "Music"},
		},
	}
}

// plexMovie is a film with a real file behind it.
func plexMovie(rk string, durationMillis int64) plexMetadata {
	return plexMetadata{
		RatingKey: rk, Type: "movie", Title: "The 'Burbs", Year: 1989,
		Duration: durationMillis, Studio: "Universal Pictures", ContentRating: "PG",
		Thumb: "/library/metadata/" + rk + "/thumb/1",
		Genre: []plexTag{{Tag: "Comedy"}, {Tag: "Thriller"}},
		Media: []plexMedia{{
			ID: 1, Duration: durationMillis, Container: "mkv",
			VideoCodec: "hevc", AudioCodec: "aac",
			Part: []plexPart{{
				ID: 273305, Key: "/library/parts/273305/1769966165/file.mkv",
				Duration: durationMillis, Container: "mkv", File: "/mnt/x.mkv",
			}},
		}},
	}
}

func newTestPlex(t *testing.T, f *plexFixture) *plexProvider {
	t.Helper()
	srv := f.server(t)
	return newPlexProvider(plexConfig{
		BaseURL: srv.URL, Token: "test-token", HTTPClient: srv.Client(),
		newSession: func() string { return "fixed-session" },
	})
}

// --- health -----------------------------------------------------------------

func TestPlexHealthDistinguishesAllSixStates(t *testing.T) {
	ctx := context.Background()

	t.Run("not_configured without an address", func(t *testing.T) {
		p := newPlexProvider(plexConfig{Token: "t"})
		if got := p.Health(ctx).State; got != HealthNotConfigured {
			t.Fatalf("state %q", got)
		}
	})

	t.Run("not_configured without a token", func(t *testing.T) {
		h := newPlexProvider(plexConfig{BaseURL: "http://example.invalid"}).Health(ctx)
		if h.State != HealthNotConfigured {
			t.Fatalf("state %q", h.State)
		}
		if !strings.Contains(h.Detail, "token") {
			t.Errorf("detail must say what to do, got %q", h.Detail)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		f := plexHealthyFixture()
		srv := f.server(t)
		addr := srv.URL
		srv.Close()
		if got := newPlexProvider(plexConfig{BaseURL: addr, Token: "t"}).Health(ctx).State; got != HealthUnreachable {
			t.Fatalf("state %q", got)
		}
	})

	t.Run("auth_failed", func(t *testing.T) {
		f := plexHealthyFixture()
		f.sectionsCode = 401
		h := newTestPlex(t, f).Health(ctx)
		if h.State != HealthAuthFailed {
			t.Fatalf("state %q", h.State)
		}
		if h.Version == "" {
			t.Error("a rejected token still knows the version")
		}
	})

	t.Run("incompatible when the address is not Plex", func(t *testing.T) {
		f := plexHealthyFixture()
		f.identStatus = 404
		if got := newTestPlex(t, f).Health(ctx).State; got != HealthIncompatible {
			t.Fatalf("state %q", got)
		}
	})

	t.Run("incompatible when it answers but does not identify as Plex", func(t *testing.T) {
		f := plexHealthyFixture()
		f.machineID = ""
		if got := newTestPlex(t, f).Health(ctx).State; got != HealthIncompatible {
			t.Fatalf("state %q", got)
		}
	})

	t.Run("incompatible by version", func(t *testing.T) {
		f := plexHealthyFixture()
		f.version = "1.9.0.100-abc"
		if got := newTestPlex(t, f).Health(ctx).State; got != HealthIncompatible {
			t.Fatalf("state %q", got)
		}
	})

	t.Run("degraded with no libraries", func(t *testing.T) {
		f := plexHealthyFixture()
		f.sections = nil
		h := newTestPlex(t, f).Health(ctx)
		if h.State != HealthDegraded {
			t.Fatalf("state %q", h.State)
		}
	})

	t.Run("degraded on an unexpected status", func(t *testing.T) {
		f := plexHealthyFixture()
		f.sectionsCode = 500
		if got := newTestPlex(t, f).Health(ctx).State; got != HealthDegraded {
			t.Fatalf("state %q", got)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		h := newTestPlex(t, plexHealthyFixture()).Health(ctx)
		if h.State != HealthOK {
			t.Fatalf("state %q (%s)", h.State, h.Detail)
		}
		if h.Version != "1.43.3.10861-07dfddaeb" {
			t.Errorf("version %q", h.Version)
		}
	})
}

func TestPlexVersionParsing(t *testing.T) {
	cases := []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"1.43.3.10861-07dfddaeb", 1, 43, true},
		{"1.20.0", 1, 20, true},
		{"2.0", 2, 0, true},
		{"", 0, 0, false},
		{"not-a-version", 0, 0, false},
	}
	for _, c := range cases {
		ma, mi, ok := plexVersionParts(c.in)
		if ok != c.ok || (ok && (ma != c.major || mi != c.minor)) {
			t.Errorf("plexVersionParts(%q) = (%d,%d,%v), want (%d,%d,%v)", c.in, ma, mi, ok, c.major, c.minor, c.ok)
		}
	}
}

func TestPlexAuthenticatesWithAHeader(t *testing.T) {
	f := plexHealthyFixture()
	newTestPlex(t, f).Health(context.Background())
	if f.lastToken != "test-token" {
		t.Fatalf("X-Plex-Token header was %q", f.lastToken)
	}
}

// --- streaming --------------------------------------------------------------

// At offset zero, the file itself: nothing transcoded, and Plex answers ranged
// requests on a part URL, so this really is direct play and really is seekable.
func TestPlexStreamPrefersTheFileAtOffsetZero(t *testing.T) {
	f := plexHealthyFixture()
	f.items = map[string]plexMetadata{"174572": plexMovie("174572", 6_102_954)}
	p := newTestPlex(t, f)

	src, err := p.Stream(context.Background(), "plex:movie:174572", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !src.DirectPlay || !src.Seekable {
		t.Errorf("directPlay=%v seekable=%v, want both true at offset zero", src.DirectPlay, src.Seekable)
	}
	if !strings.Contains(src.URL, "/library/parts/273305/") {
		t.Errorf("expected the part file, got %s", jellyfinRedact(src.URL))
	}
	if strings.Contains(src.URL, "transcode") {
		t.Errorf("offset zero must not transcode: %s", jellyfinRedact(src.URL))
	}
	if src.MimeType != "video/x-matroska" {
		t.Errorf("mime %q", src.MimeType)
	}
}

// The heart of the Plex side. An offset means the universal transcoder, with
// the offset in seconds -- and it must be honest that the result is neither
// direct play nor seekable.
func TestPlexStreamStartsAtTheOffsetAndSaysWhatThatCost(t *testing.T) {
	f := plexHealthyFixture()
	f.items = map[string]plexMetadata{"174572": plexMovie("174572", 6_102_954)}
	p := newTestPlex(t, f)

	src, err := p.Stream(context.Background(), "plex:movie:174572", StreamOptions{OffsetSeconds: 2220})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(src.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(u.Path, "/video/:/transcode/universal/start") {
		t.Fatalf("path %q, want the universal transcoder", u.Path)
	}
	q := u.Query()
	if q.Get("offset") != "2220" {
		t.Fatalf("offset=%q, want 2220 seconds", q.Get("offset"))
	}
	// Plex takes seconds here. Milliseconds would be a plausible-looking
	// 2,220,000 that Plex clamps to the end of the film.
	if q.Get("offset") == "2220000" {
		t.Fatal("the offset was sent in milliseconds")
	}
	if q.Get("copyts") != "1" {
		t.Error("copyts=1 is what makes the returned stream's own timestamps start at the offset")
	}
	if q.Get("path") != "/library/metadata/174572" {
		t.Errorf("path param %q", q.Get("path"))
	}
	if q.Get("X-Plex-Platform") != "Chrome" {
		t.Errorf("X-Plex-Platform=%q — Plex picks the conversion profile from this, and an unknown one "+
			"makes every transcode decision come back as 'no conversion profile found'", q.Get("X-Plex-Platform"))
	}
	if q.Get("X-Plex-Token") == "" {
		t.Error("a player cannot set headers, so the stream URL must carry the token")
	}
	if src.DirectPlay {
		t.Error("a transcode must not claim direct play")
	}
	if src.Seekable {
		t.Error("Plex's universal transcode answers Accept-Ranges: none, so Seekable must be false")
	}
}

func TestPlexClampsTheOffsetInsideTheFile(t *testing.T) {
	// 6,102,954 ms is 6,102 whole seconds.
	if got := plexClampOffset(2220, 6_102_954); got != 2220 {
		t.Errorf("an offset inside the file was altered: %d", got)
	}
	if got := plexClampOffset(99999, 6_102_954); got != 6101 {
		t.Errorf("an offset past the end gave %d, want 6101 (one second short)", got)
	}
	if got := plexClampOffset(-5, 6_102_954); got != 0 {
		t.Errorf("a negative offset gave %d", got)
	}
	if got := plexClampOffset(0, 6_102_954); got != 0 {
		t.Errorf("zero gave %d", got)
	}
	// A file with no known duration must not have its offset discarded.
	if got := plexClampOffset(500, 0); got != 500 {
		t.Errorf("an unknown duration discarded the offset: %d", got)
	}
}

func TestPlexStreamClampsAnOffsetPastTheEnd(t *testing.T) {
	f := plexHealthyFixture()
	f.items = map[string]plexMetadata{"1": plexMovie("1", 60_000)} // one minute
	p := newTestPlex(t, f)

	src, err := p.Stream(context.Background(), "plex:movie:1", StreamOptions{OffsetSeconds: 9999})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(src.URL)
	if got := u.Query().Get("offset"); got != "59" {
		t.Fatalf("offset=%q, want 59", got)
	}
}

func TestPlexStreamRejectsAnIDItDidNotIssue(t *testing.T) {
	p := newTestPlex(t, plexHealthyFixture())
	if _, err := p.Stream(context.Background(), "jellyfin:movie:x", StreamOptions{}); err == nil {
		t.Fatal("streaming a foreign id must fail rather than guess")
	}
}

func TestPlexStreamReportsAGoneItemAsMissing(t *testing.T) {
	p := newTestPlex(t, plexHealthyFixture())
	_, err := p.Stream(context.Background(), "plex:movie:nope", StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), errMediaItemGone.Error()) {
		t.Fatalf("want a gone error, got %v", err)
	}
}

// A catalogue row with no file behind it is a deleted file, not a transient
// failure -- and scheduling it is how a channel goes to black.
func TestPlexStreamReportsAnItemWithNoFileAsMissing(t *testing.T) {
	f := plexHealthyFixture()
	ghost := plexMovie("2", 1000)
	ghost.Media = nil
	f.items = map[string]plexMetadata{"2": ghost}
	p := newTestPlex(t, f)

	if _, err := p.Stream(context.Background(), "plex:movie:2", StreamOptions{}); err == nil ||
		!strings.Contains(err.Error(), errMediaItemGone.Error()) {
		t.Fatalf("want a gone error, got %v", err)
	}
}

// --- library ----------------------------------------------------------------

func TestPlexLibraryStatus(t *testing.T) {
	f := plexHealthyFixture()
	ghost := plexMovie("2", 1000)
	ghost.Media = nil
	f.items = map[string]plexMetadata{"1": plexMovie("1", 1000), "2": ghost}
	p := newTestPlex(t, f)
	ctx := context.Background()

	for _, c := range []struct {
		id   string
		want LibraryState
	}{
		{"plex:movie:1", StateAvailable},
		{"plex:movie:2", StateMissing},
		{"plex:movie:404", StateMissing},
		{"jellyfin:movie:x", StateUnknown},
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

func TestPlexCanonicalIDsRoundTrip(t *testing.T) {
	id := plexCanonicalID("episode", "128732")
	if id != "plex:episode:128732" {
		t.Fatalf("id %q", id)
	}
	got, ok := parsePlexID(id)
	if !ok || got != "128732" {
		t.Fatalf("parse gave (%q,%v)", got, ok)
	}
	for _, bad := range []string{"", "128732", "jellyfin:movie:1", "plex:", "plex:movie:"} {
		if _, ok := parsePlexID(bad); ok {
			t.Errorf("%q was accepted as a Plex id", bad)
		}
	}
}

func TestPlexTypesResolveThroughTheSchema(t *testing.T) {
	for _, pt := range []string{"movie", "episode", "show", "season", "clip", "whatever"} {
		typ := plexType(pt)
		if canonicalDomain(typ) != "video" {
			t.Errorf("Plex type %q mapped to %q, which schema.json does not file under video", pt, typ)
		}
	}
}

func TestPlexMapsAMovieOntoTheCanonicalShape(t *testing.T) {
	p := newPlexProvider(plexConfig{BaseURL: "http://plex.test", Token: "tok"})
	li := p.toLinearItem(plexMovie("174572", 6_102_954), plexMetadata{}, "Movies")

	if li.CanonicalID != "plex:movie:174572" {
		t.Errorf("canonicalId %q", li.CanonicalID)
	}
	if li.DurationSeconds != 6102 {
		t.Errorf("duration %d seconds, want 6102 (milliseconds divided by a thousand)", li.DurationSeconds)
	}
	if len(li.Genres) != 2 || li.Genres[0] != "Comedy" {
		t.Errorf("genres %v", li.Genres)
	}
	if li.Network != "Universal Pictures" || li.Rating != "PG" || li.LibraryID != "Movies" {
		t.Errorf("facets: network=%q rating=%q library=%q", li.Network, li.Rating, li.LibraryID)
	}
	if !strings.Contains(li.Artwork, "X-Plex-Token=") {
		t.Error("Plex serves no image without a token, so the artwork URL must carry one")
	}
	if !li.schedulable() {
		t.Error("a film with a duration must be schedulable")
	}
}

// Plex hangs genres off the show, never the episode. Without folding them down,
// a rule saying "genre is Comedy" on a TV library matches nothing -- and looks
// correct while doing it.
func TestPlexFoldsShowFacetsOntoAnEpisode(t *testing.T) {
	p := newPlexProvider(plexConfig{BaseURL: "http://plex.test", Token: "tok"})
	episode := plexMetadata{
		RatingKey: "128732", Type: "episode", Title: "Early Reel",
		Duration: 88_003, Index: 1, ParentIndex: 0,
		GrandparentTitle: "The Amazing World of Gumball", GrandparentRatingKey: "128730",
	}
	show := plexMetadata{
		RatingKey: "128730", Type: "show", Title: "The Amazing World of Gumball",
		Genre:  []plexTag{{Tag: "Comedy"}, {Tag: "Family"}},
		Studio: "Boulder Media", ContentRating: "TV-Y7", Year: 2011,
	}

	li := p.toLinearItem(episode, show, "Cartoons")
	if len(li.Genres) != 2 || li.Genres[0] != "Comedy" {
		t.Fatalf("genres %v — the show's genres were not folded onto the episode", li.Genres)
	}
	if li.Network != "Boulder Media" {
		t.Errorf("network %q", li.Network)
	}
	if li.Rating != "TV-Y7" {
		t.Errorf("rating %q", li.Rating)
	}
	if li.Subtitle != "The Amazing World of Gumball" {
		t.Errorf("subtitle %q, want the series name where the rule engine looks for it", li.Subtitle)
	}
	if li.SeriesID != "128730" {
		t.Errorf("seriesId %q", li.SeriesID)
	}
	if li.Episode != 1 {
		t.Errorf("episode %d", li.Episode)
	}
	// The episode's own facets must win where it has them.
	episode.Genre = []plexTag{{Tag: "Action"}}
	if got := p.toLinearItem(episode, show, "Cartoons").Genres; len(got) != 1 || got[0] != "Action" {
		t.Errorf("the episode's own genres must not be overwritten by the show's, got %v", got)
	}
}

func TestPlexVideoSectionsExcludeMusic(t *testing.T) {
	for _, ty := range []string{"movie", "show"} {
		if !plexVideoSection(ty) {
			t.Errorf("%q should be usable by a video channel", ty)
		}
	}
	for _, ty := range []string{"artist", "photo", "", "podcast"} {
		if plexVideoSection(ty) {
			t.Errorf("%q must not be offered as a source for a video channel", ty)
		}
	}
	// A show section contributes episodes, which have a runtime; a show does
	// not and could never be given a slot.
	if plexItemTypeCode("show") != "4" {
		t.Error("a show section must be paged for episodes")
	}
	if plexItemTypeCode("movie") != "1" {
		t.Error("a movie section must be paged for movies")
	}
}
