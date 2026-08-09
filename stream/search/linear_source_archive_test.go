package main

// The archive.org source, against a stub.
//
// Everything here runs against a fake Archive. That is not a convenience: the
// real one reindexes collections, retires items and occasionally serves an
// item whose metadata it cannot read, so a suite that called it would fail for
// reasons that have nothing to do with this code and would pass while an
// assertion was wrong. The live service was measured separately; what those
// measurements found is written into the stub below and into the comments in
// linear_source_archive.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- a fake Archive ---------------------------------------------------------

type linearFakeIAItem struct {
	Identifier string
	Title      string
	License    string
	Year       any
	Files      []archiveMetaFile
	Collection []string
	// MetaError reproduces `{"error": "item metadata may be invalid"}`, which
	// the real service returns with HTTP 200 for a meaningful slice of the
	// classic-TV pool.
	MetaError string
	// StreamOnly reproduces the "play but do not download" marker.
	StreamOnly bool
}

type linearFakeIA struct {
	items []linearFakeIAItem
	// Ranges records whether the file server honours Range. Both settings are
	// real: the Archive answers 206, and a misconfigured mirror would answer
	// 200 and send the whole file.
	ignoreRange bool

	metadataHits int64
	rangeHits    int64
	srv          *httptest.Server
}

func newLinearFakeIA(t *testing.T, items []linearFakeIAItem) *linearFakeIA {
	t.Helper()
	f := &linearFakeIA{items: items}
	mux := http.NewServeMux()

	mux.HandleFunc("/advancedsearch.php", func(w http.ResponseWriter, r *http.Request) {
		type doc struct {
			Identifier string   `json:"identifier"`
			Title      string   `json:"title"`
			Year       any      `json:"year,omitempty"`
			Downloads  int      `json:"downloads"`
			LicenseURL string   `json:"licenseurl,omitempty"`
			Collection []string `json:"collection,omitempty"`
		}
		var docs []doc
		for i, it := range f.items {
			// The stub applies the same licence filter the real index does, so
			// the Go-side re-check is exercised against a list that already
			// passed once -- which is the arrangement in production.
			if !archiveLinearIsPD(it.License) {
				continue
			}
			docs = append(docs, doc{
				Identifier: it.Identifier, Title: it.Title, Year: it.Year,
				Downloads: len(f.items) - i, LicenseURL: it.License,
				Collection: it.Collection,
			})
		}
		writeJSON(w, 200, map[string]any{"response": map[string]any{"docs": docs}})
	})

	mux.HandleFunc("/metadata/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&f.metadataHits, 1)
		id := strings.TrimPrefix(r.URL.Path, "/metadata/")
		for _, it := range f.items {
			if it.Identifier != id {
				continue
			}
			if it.MetaError != "" {
				writeJSON(w, 200, map[string]string{"error": it.MetaError})
				return
			}
			meta := map[string]any{
				"identifier": it.Identifier,
				"title":      it.Title,
				"mediatype":  "movies",
			}
			if it.License != "" {
				meta["licenseurl"] = it.License
			}
			if it.Year != nil {
				meta["year"] = it.Year
			}
			coll := it.Collection
			if it.StreamOnly {
				coll = append(append([]string{}, coll...), "stream_only")
			}
			if len(coll) > 0 {
				meta["collection"] = coll
			}
			writeJSON(w, 200, map[string]any{"metadata": meta, "files": it.Files})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})

	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		body := strings.Repeat("v", 4096)
		if rng := r.Header.Get("Range"); rng != "" && !f.ignoreRange {
			atomic.AddInt64(&f.rangeHits, 1)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-1023/%d", len(body)))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(body[:1024]))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	// Point the source at the stub for the duration of the test.
	oldSearch, oldMeta, oldDL := archiveLinearSearchAPI, archiveLinearMetadataAPI, archiveLinearDownloadAPI
	archiveLinearSearchAPI = f.srv.URL + "/advancedsearch.php"
	archiveLinearMetadataAPI = f.srv.URL + "/metadata/"
	archiveLinearDownloadAPI = f.srv.URL + "/download/"
	t.Cleanup(func() {
		archiveLinearSearchAPI, archiveLinearMetadataAPI, archiveLinearDownloadAPI = oldSearch, oldMeta, oldDL
	})
	return f
}

const linearFakePDLicence = "http://creativecommons.org/publicdomain/mark/1.0/"

// A pool shaped like the real one: good items, an item the Archive cannot
// describe, an item that is not public domain, one with no playable derivative,
// and one marked stream-only.
func linearFakeIAPool() []linearFakeIAItem {
	return []linearFakeIAItem{
		{
			Identifier: "popeye_patriotic_popeye", Title: "Patriotic Popeye",
			License: linearFakePDLicence, Year: 1957,
			Files: []archiveMetaFile{
				{Name: "popeye.mpeg", Format: "MPEG2", Length: "362.98"},
				{Name: "popeye_512kb.mp4", Format: "512Kb MPEG4", Length: "363.16"},
				{Name: "popeye.thumbs/x_000001.jpg", Format: "Thumbnail"},
			},
		},
		{
			Identifier: "bb_snow_white", Title: "Betty Boop: Snow White",
			License: "http://creativecommons.org/licenses/publicdomain/", Year: "1933",
			Files: []archiveMetaFile{
				// Colon-form duration, which the newer derivatives use.
				{Name: "snow_white.mp4", Format: "h.264 IA", Length: "7:04"},
				{Name: "snow_white_512kb.mp4", Format: "512Kb MPEG4", Length: "424.1"},
			},
		},
		{
			Identifier: "Bonanza_pd", Title: "Bonanza public domain episodes",
			License: linearFakePDLicence, MetaError: "item metadata may be invalid",
		},
		{
			// Freely viewable but not public domain. The real
			// `BettyBoopCartoons` item carries exactly this.
			Identifier: "BettyBoopCartoons", Title: "Betty Boop Cartoons",
			License: "http://creativecommons.org/licenses/by-nc-nd/2.5/",
			Files: []archiveMetaFile{
				{Name: "bb.mp4", Format: "h.264", Length: "600"},
			},
		},
		{
			// A real programme whose only copy is an unplayable original.
			Identifier: "mpeg_only", Title: "Only An MPEG2",
			License: linearFakePDLicence,
			Files: []archiveMetaFile{
				{Name: "x.mpeg", Format: "MPEG2", Length: "500"},
			},
		},
		{
			// A playable file that does not say how long it is.
			Identifier: "no_duration", Title: "No Duration Declared",
			License: linearFakePDLicence,
			Files: []archiveMetaFile{
				{Name: "y_512kb.mp4", Format: "512Kb MPEG4", Length: ""},
			},
		},
		{
			Identifier: "locked_up", Title: "Stream Only",
			License: linearFakePDLicence, StreamOnly: true,
			Files: []archiveMetaFile{
				{Name: "z_512kb.mp4", Format: "512Kb MPEG4", Length: "300"},
			},
		},
	}
}

func archiveTestLibrary(t *testing.T) *archiveLinearLibrary {
	t.Helper()
	libs, _ := newArchiveLinear()
	lib, ok := libs[0].(*archiveLinearLibrary)
	if !ok {
		t.Fatalf("newArchiveLinear returned %T", libs[0])
	}
	return lib
}

// archiveWarmedItems is how a test gets a populated pool.
//
// LinearItems deliberately never waits for archive.org -- see the comment on
// archiveLinearLibrary -- so a test that wants the pool has to warm it first,
// exactly as main.go does at startup. The two steps are separate here for the
// same reason they are separate in production: reading is a request path and
// must be instant, warming is a background job and may take minutes.
func archiveWarmedItems(t *testing.T, lib *archiveLinearLibrary) []LinearItem {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := lib.Warm(ctx); err != nil {
		t.Fatalf("warming %s: %v", lib.LinearLibraryID(), err)
	}
	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatalf("reading %s after a warm: %v", lib.LinearLibraryID(), err)
	}
	return items
}

// archiveWarmAll warms every library a construction returned.
func archiveWarmAll(t *testing.T, libs []LinearLibrary) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, l := range libs {
		w, ok := l.(*archiveLinearLibrary)
		if !ok {
			continue
		}
		if err := w.Warm(ctx); err != nil {
			t.Fatalf("warming %s: %v", w.LinearLibraryID(), err)
		}
	}
}

// --- licences ---------------------------------------------------------------

func TestArchiveLinearPublicDomainLicences(t *testing.T) {
	pd := []string{
		"http://creativecommons.org/publicdomain/mark/1.0/",
		"https://creativecommons.org/publicdomain/mark/1.0/",
		"http://creativecommons.org/licenses/publicdomain/",
		"http://creativecommons.org/publicdomain/zero/1.0/",
		"HTTP://CreativeCommons.ORG/PublicDomain/Mark/1.0/",
		"http://www.creativecommons.org/publicdomain/mark/1.0/",
	}
	for _, l := range pd {
		if !archiveLinearIsPD(l) {
			t.Errorf("%q was not recognised as public domain", l)
		}
	}
	// The ones that matter: freely viewable is not the same as free.
	notPD := []string{
		"",
		"   ",
		"http://creativecommons.org/licenses/by-nc-nd/2.5/",
		"http://creativecommons.org/licenses/by/4.0/",
		"http://creativecommons.org/licenses/by-sa/3.0/",
		"http://example.com/publicdomain/",
		"all rights reserved",
	}
	for _, l := range notPD {
		if archiveLinearIsPD(l) {
			t.Errorf("%q was accepted as public domain", l)
		}
	}
}

// --- durations --------------------------------------------------------------

func TestArchiveLinearDurationParsesBothShapes(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"363.16", 363},
		{"600", 600},
		{"7:04", 424},
		{"1:08:41", 4121},
		{"0:30", 30},
		// Everything below is unusable, and unusable must mean zero rather than
		// a guess -- a wrong duration puts the whole channel out of step.
		{"", 0},
		{"   ", 0},
		{"unknown", 0},
		{"-5", 0},
		{"0", 0},
		{"1:xx", 0},
		{"1:-2", 0},
	}
	for _, tc := range cases {
		if got := archiveLinearDuration(tc.in); got != tc.want {
			t.Errorf("archiveLinearDuration(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The MP4 derivative is chosen over the much larger original, and quality order
// is respected among the MP4s.
func TestArchiveLinearPicksThePlayableDerivative(t *testing.T) {
	f, ok := archiveLinearPickFile([]archiveMetaFile{
		{Name: "x.mpeg", Format: "MPEG2", Length: "500"},
		{Name: "x_512kb.mp4", Format: "512Kb MPEG4", Length: "501"},
		{Name: "x.mp4", Format: "h.264 IA", Length: "502"},
		{Name: "x.thumbs/a.jpg", Format: "Thumbnail"},
	})
	if !ok {
		t.Fatal("no file was picked from a set containing three playable ones")
	}
	if f.Name != "x.mp4" {
		t.Errorf("picked %q, want the h.264 IA derivative", f.Name)
	}
	if f.Seconds != 502 {
		t.Errorf("duration is %d, want the picked file's own length", f.Seconds)
	}
}

func TestArchiveLinearRefusesFilesWithoutADuration(t *testing.T) {
	if _, ok := archiveLinearPickFile([]archiveMetaFile{
		{Name: "x_512kb.mp4", Format: "512Kb MPEG4", Length: ""},
	}); ok {
		t.Fatal("a playable file with no declared length was accepted; " +
			"the schedule is laid out from these numbers and a guess desynchronises the channel")
	}
	if _, ok := archiveLinearPickFile([]archiveMetaFile{
		{Name: "x.mpeg", Format: "MPEG2", Length: "500"},
	}); ok {
		t.Fatal("an MPEG2 original was accepted; no browser will play it")
	}
}

// --- canonical ids ----------------------------------------------------------

func TestArchiveLinearIDCarriesTheFile(t *testing.T) {
	id := archiveLinearID("popeye_patriotic_popeye", "popeye_512kb.mp4")
	ident, file, ok := parseArchiveLinearID(id)
	if !ok || ident != "popeye_patriotic_popeye" || file != "popeye_512kb.mp4" {
		t.Fatalf("round trip gave (%q, %q, %v)", ident, file, ok)
	}
	for _, bad := range []string{
		"", "popeye", "ia:", "ia:popeye", "ia:popeye/",
		"ia:/file.mp4", "jf:123", "tt0111161",
	} {
		if _, _, ok := parseArchiveLinearID(bad); ok {
			t.Errorf("%q was parsed as an archive.org programme id", bad)
		}
	}
}

// --- the library ------------------------------------------------------------

func TestArchiveLinearLibraryAdmitsOnlyWhatItCanProve(t *testing.T) {
	newLinearFakeIA(t, linearFakeIAPool())
	lib := archiveTestLibrary(t)

	items := archiveWarmedItems(t, lib)

	byID := map[string]LinearItem{}
	for _, it := range items {
		byID[it.ProviderItemID] = it
	}

	// Admitted and schedulable.
	for _, want := range []struct {
		id   string
		secs int
	}{
		{"popeye_patriotic_popeye", 363},
		{"bb_snow_white", 424},
	} {
		it, ok := byID[want.id]
		if !ok {
			t.Errorf("%s was not admitted", want.id)
			continue
		}
		if it.DurationSeconds != want.secs {
			t.Errorf("%s runs %ds, want %ds", want.id, it.DurationSeconds, want.secs)
		}
		if !it.schedulable() {
			t.Errorf("%s is not schedulable", want.id)
		}
	}

	// Refused outright: these are not programmes at all.
	for _, id := range []string{
		"BettyBoopCartoons", // by-nc-nd is not public domain
		"Bonanza_pd",        // the Archive cannot describe it
		"locked_up",         // stream-only
	} {
		if _, ok := byID[id]; ok {
			t.Errorf("%s was admitted to the pool", id)
		}
	}

	// Kept but unschedulable: real public-domain programmes whose duration
	// could not be established. They stay in the pool so the preview can report
	// them honestly, and the scheduler skips them.
	for _, id := range []string{"mpeg_only", "no_duration"} {
		it, ok := byID[id]
		if !ok {
			t.Errorf("%s was dropped; it should be counted as matched-but-unschedulable", id)
			continue
		}
		if it.DurationSeconds != 0 {
			t.Errorf("%s was given a duration of %d out of nowhere", id, it.DurationSeconds)
		}
		if it.schedulable() {
			t.Errorf("%s is schedulable despite having no known duration", id)
		}
	}
}

// The guarantee that matters: nothing without a proven duration ever reaches a
// schedule, however many of them are in the pool.
func TestArchiveLinearNothingWithoutADurationIsScheduled(t *testing.T) {
	newLinearFakeIA(t, linearFakeIAPool())
	lib := archiveTestLibrary(t)
	items := archiveWarmedItems(t, lib)

	ch := LinearChannel{
		ID: "cartoons", Number: 901, Name: "Cartoons", Enabled: true,
		SourceProvider: archiveLinearProviderID, ScheduleStrategy: StrategyCyclic,
		Timezone: "UTC", EPGDays: 1, Seed: 7,
	}
	ch.normalise()

	programs, _, err := linearFill(&ch, items, linearCursor{}, linearEpoch, linearEpoch.Add(6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(programs) == 0 {
		t.Fatal("nothing was scheduled at all")
	}
	unschedulable := map[string]bool{}
	for _, it := range items {
		if !it.schedulable() {
			unschedulable[it.CanonicalID] = true
		}
	}
	if len(unschedulable) == 0 {
		t.Fatal("the fixture no longer contains an item without a duration, so this proves nothing")
	}
	for _, p := range programs {
		if unschedulable[p.CanonicalID] {
			t.Errorf("%q has no known duration but was given the slot at %d", p.Title, p.StartTime)
		}
		if p.EndTime <= p.StartTime {
			t.Errorf("%q was given a zero-length slot", p.Title)
		}
	}
	if err := linearCheckOrdering(programs); err != nil {
		t.Error(err)
	}
}

// A preview built on this library reports the unschedulable items rather than
// hiding them, which is the reason they are kept.
func TestArchiveLinearPreviewCountsWhatCannotBeScheduled(t *testing.T) {
	newLinearFakeIA(t, linearFakeIAPool())
	lib := archiveTestLibrary(t)
	items := archiveWarmedItems(t, lib)
	p := linearPreviewRules(items, LinearRuleGroup{Match: LinearMatchAll})
	if p.Skipped == 0 {
		t.Fatal("the preview reported nothing skipped despite two items having no duration")
	}
	if p.Schedulable == 0 {
		t.Fatal("the preview reported nothing schedulable")
	}
	if !strings.Contains(p.Detail, "no duration") {
		t.Errorf("the preview detail does not explain the shortfall: %q", p.Detail)
	}
}

// The pool is cached. Without this the engine's five-minute refresh would mean
// a few hundred metadata requests an hour, forever.
func TestArchiveLinearLibraryCachesItsPool(t *testing.T) {
	f := newLinearFakeIA(t, linearFakeIAPool())
	lib := archiveTestLibrary(t)

	archiveWarmedItems(t, lib)
	first := atomic.LoadInt64(&f.metadataHits)
	if first == 0 {
		t.Fatal("the first load asked for no metadata at all")
	}
	// Many reads, and a second warm on top, must all be served from memory.
	for i := 0; i < 20; i++ {
		if _, err := lib.LinearItems(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	archiveWarmedItems(t, lib)
	if got := atomic.LoadInt64(&f.metadataHits); got != first {
		t.Errorf("re-reading a warm pool cost %d more metadata requests, want none", got-first)
	}
}

// Reading the pool must never wait on archive.org, and must never be cancelled
// by whoever happened to be reading.
//
// This is the production failure written down. LinearItems used to run the
// refresh on its caller's context and block every other caller on it, so a
// client that gave up at ninety seconds killed a seven-minute crawl and the
// cache never populated -- forever, once the startup warm was out of the
// picture. Both halves are asserted here because fixing either one alone still
// leaves the channels dark.
func TestArchiveLinearItemsNeverBlocksOrInheritsACallerDeadline(t *testing.T) {
	// A stub that never answers, so any waiting at all shows up as a hang.
	stalled := make(chan struct{})
	t.Cleanup(func() { close(stalled) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-stalled
	}))
	t.Cleanup(srv.Close)

	oldSearch, oldMeta := archiveLinearSearchAPI, archiveLinearMetadataAPI
	archiveLinearSearchAPI = srv.URL + "/advancedsearch.php"
	archiveLinearMetadataAPI = srv.URL + "/metadata/"
	t.Cleanup(func() { archiveLinearSearchAPI, archiveLinearMetadataAPI = oldSearch, oldMeta })

	lib := archiveTestLibrary(t)

	// An already-cancelled context, which is what a request whose client has
	// hung up looks like. It must neither block nor be reported as the reason
	// the pool is empty.
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan time.Duration, 1)
	go func() {
		started := time.Now()
		_, _ = lib.LinearItems(dead)
		done <- time.Since(started)
	}()

	select {
	case took := <-done:
		if took > 2*time.Second {
			t.Fatalf("reading the pool took %s; it must not wait on archive.org", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading the pool blocked on a stalled archive.org; " +
			"this is the production hang, where /api/v1/linear/channels never answered")
	}

	// And the refresh the read kicked off is still running, rather than having
	// been cancelled along with the caller.
	lib.mu.Lock()
	building := lib.building
	lib.mu.Unlock()
	if !building {
		t.Error("the background refresh did not survive its caller; " +
			"a cancelled request must not destroy the crawl it started")
	}
}

// A failing collection backs off instead of retrying on every read.
func TestArchiveLinearBacksOffAfterAFailure(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	oldSearch := archiveLinearSearchAPI
	archiveLinearSearchAPI = srv.URL + "/advancedsearch.php"
	t.Cleanup(func() { archiveLinearSearchAPI = oldSearch })

	lib := archiveTestLibrary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lib.Warm(ctx); err == nil {
		t.Fatal("a collection whose search returns 500 reported success")
	}
	after := atomic.LoadInt64(&hits)

	// Fifty reads must not produce fifty more attempts. This is the log line
	// that repeated every thirty seconds in production.
	for i := 0; i < 50; i++ {
		if _, err := lib.LinearItems(context.Background()); err == nil {
			t.Fatal("an empty, failed pool reported no error")
		}
	}
	if got := atomic.LoadInt64(&hits); got != after {
		t.Errorf("%d reads triggered %d extra upstream attempts, want 0 while backing off", 50, got-after)
	}

	lib.mu.Lock()
	failures, next := lib.failures, lib.nextTry
	lib.mu.Unlock()
	if failures == 0 || next.IsZero() {
		t.Errorf("no backoff was recorded (failures=%d nextTry=%v)", failures, next)
	}
}

// A source that is still loading must be reported as such, and must be
// distinguishable from one whose rules match nothing.
//
// The engine keys off ErrLinearSourceWarming for both the wording it shows an
// operator and the decision not to log per request, so the archive library's
// own error has to carry it. A private sentinel here would compile, pass every
// test in this file, and quietly restore both defects.
func TestArchiveLinearWarmingIsRecognisedByTheEngine(t *testing.T) {
	if !errors.Is(archiveLinearBuilding, ErrLinearSourceWarming) {
		t.Fatal("the archive library's building state does not wrap ErrLinearSourceWarming, " +
			"so the engine cannot tell it from a genuine failure")
	}

	warming := linearEmptyDetail(0, fmt.Errorf("archive-org/Cartoons: %w", archiveLinearBuilding))
	if !strings.Contains(warming, "still loading") {
		t.Errorf("a warming source is described as %q", warming)
	}
	if strings.Contains(warming, "match") {
		t.Errorf("a warming source was described as a rule-matching problem: %q", warming)
	}

	// A real emptiness must still read as one.
	empty := linearEmptyDetail(0, nil)
	if !strings.Contains(empty, "match") {
		t.Errorf("an genuinely empty channel is described as %q", empty)
	}
}

// While a source is warming, reading it reports the warming state rather than
// an error that reads like a fault.
func TestArchiveLinearReportsWarmingWhileTheFirstPoolBuilds(t *testing.T) {
	stalled := make(chan struct{})
	t.Cleanup(func() { close(stalled) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-stalled
	}))
	t.Cleanup(srv.Close)
	oldSearch := archiveLinearSearchAPI
	archiveLinearSearchAPI = srv.URL + "/advancedsearch.php"
	t.Cleanup(func() { archiveLinearSearchAPI = oldSearch })

	lib := archiveTestLibrary(t)
	items, err := lib.LinearItems(context.Background())
	if len(items) != 0 {
		t.Fatalf("a cold pool returned %d items", len(items))
	}
	if !errors.Is(err, ErrLinearSourceWarming) {
		t.Fatalf("a cold pool reported %v, want a warming state", err)
	}
}

func TestArchiveLinearBackoffGrowsAndIsCapped(t *testing.T) {
	if got := archiveLinearBackoff(1); got != archiveLinearRetryBase {
		t.Errorf("first retry waits %s, want %s", got, archiveLinearRetryBase)
	}
	if got := archiveLinearBackoff(2); got != 2*archiveLinearRetryBase {
		t.Errorf("second retry waits %s", got)
	}
	if got := archiveLinearBackoff(50); got != archiveLinearRetryMax {
		t.Errorf("a permanently failing collection waits %s, want the %s cap", got, archiveLinearRetryMax)
	}
	if got := archiveLinearBackoff(0); got != archiveLinearRetryBase {
		t.Errorf("a zero failure count waits %s", got)
	}
}

// A refresh that fails must not blank a pool that was working.
func TestArchiveLinearKeepsTheLastGoodPoolWhenARefreshFails(t *testing.T) {
	f := newLinearFakeIA(t, linearFakeIAPool())
	lib := archiveTestLibrary(t)
	good := archiveWarmedItems(t, lib)
	if len(good) == 0 {
		t.Fatal("the first warm produced nothing")
	}
	_ = f

	// Break the upstream, expire the cache, and read again.
	oldSearch := archiveLinearSearchAPI
	archiveLinearSearchAPI = "http://127.0.0.1:1/advancedsearch.php"
	t.Cleanup(func() { archiveLinearSearchAPI = oldSearch })

	lib.mu.Lock()
	lib.fetched = time.Now().Add(-2 * archiveLinearTTL)
	lib.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = lib.Warm(ctx)

	after, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatalf("a failed refresh reported an error over a good pool: %v", err)
	}
	if len(after) != len(good) {
		t.Errorf("a failed refresh changed the pool from %d to %d programmes; "+
			"the last good copy must survive", len(good), len(after))
	}
}

// The facets a channel is built out of have to be there, or the rule editor has
// nothing to select on.
func TestArchiveLinearItemsCarryTheirFacets(t *testing.T) {
	newLinearFakeIA(t, linearFakeIAPool())
	lib := archiveTestLibrary(t)
	items := archiveWarmedItems(t, lib)
	var it LinearItem
	for _, candidate := range items {
		if candidate.ProviderItemID == "popeye_patriotic_popeye" {
			it = candidate
			break
		}
	}
	if it.CanonicalID == "" {
		t.Fatal("the fixture item was not admitted")
	}
	if it.ProviderID != archiveLinearProviderID {
		t.Errorf("providerId is %q", it.ProviderID)
	}
	if it.LibraryID != "Cartoons" || it.Collection != "Cartoons" {
		t.Errorf("library is %q / collection %q", it.LibraryID, it.Collection)
	}
	if len(it.Genres) == 0 || it.Genres[0] != "Animation" {
		t.Errorf("genres are %v", it.Genres)
	}
	if it.Year != 1957 {
		t.Errorf("year is %d, want 1957", it.Year)
	}
	if it.Domain != "video" || it.Type != "movie" {
		t.Errorf("domain/type is %q/%q", it.Domain, it.Type)
	}
	if it.Rating != "Public Domain" {
		t.Errorf("rating is %q", it.Rating)
	}
	if !strings.Contains(it.Artwork, "popeye_patriotic_popeye") {
		t.Errorf("artwork is %q", it.Artwork)
	}
	// A rule set written against these facets must actually select.
	matched := linearApplyRules(items, LinearRuleGroup{
		Match: LinearMatchAll,
		Rules: []LinearRule{{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Animation"}},
	})
	if len(matched) == 0 {
		t.Error("a genre rule matched none of the library's own items")
	}
}

// --- the resolver -----------------------------------------------------------

func TestArchiveLinearResolverSeeksAndReportsItHonestly(t *testing.T) {
	f := newLinearFakeIA(t, linearFakeIAPool())
	_, res := newArchiveLinear()

	p := Program{
		CanonicalID: archiveLinearID("popeye_patriotic_popeye", "popeye_512kb.mp4"),
		Title:       "Patriotic Popeye",
	}
	src, err := res.LinearResolve(context.Background(), p, StreamOptions{OffsetSeconds: 37 * 60})
	if err != nil {
		t.Fatal(err)
	}
	if !src.Seekable {
		t.Error("a source that answered 206 was reported as not seekable")
	}
	if !src.DirectPlay || src.MimeType != "video/mp4" {
		t.Errorf("source is %+v", src)
	}
	if !strings.HasSuffix(src.URL, "#t=2220") {
		t.Errorf("the schedule's offset did not reach the URL: %q", src.URL)
	}
	if atomic.LoadInt64(&f.rangeHits) == 0 {
		t.Error("the resolver reported seekability without asking for a range")
	}

	// At the top of a programme there is nothing to seek to, so no fragment.
	src, err = res.LinearResolve(context.Background(), p, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src.URL, "#t=") {
		t.Errorf("a zero offset still produced a fragment: %q", src.URL)
	}
}

// A server that ignores Range answers 200 and sends the whole file. The request
// "succeeded", and treating that as seekable is how a channel starts every
// programme from zero while insisting it is 37 minutes in.
func TestArchiveLinearResolverRefusesToClaimSeekWhenRangeIsIgnored(t *testing.T) {
	f := newLinearFakeIA(t, linearFakeIAPool())
	f.ignoreRange = true
	_, res := newArchiveLinear()

	src, err := res.LinearResolve(context.Background(), Program{
		CanonicalID: archiveLinearID("popeye_patriotic_popeye", "popeye_512kb.mp4"),
	}, StreamOptions{OffsetSeconds: 120})
	if err != nil {
		t.Fatal(err)
	}
	if src.Seekable {
		t.Fatal("a 200 answer to a Range request was reported as seekable")
	}
	if strings.Contains(src.URL, "#t=") {
		t.Error("a position was put on a URL that cannot honour it")
	}
}

// The resolver must reject ids it did not issue, so a chain moves on to the
// next media server instead of stopping at this one.
func TestArchiveLinearResolverRejectsForeignIDs(t *testing.T) {
	newLinearFakeIA(t, linearFakeIAPool())
	_, res := newArchiveLinear()
	_, err := res.LinearResolve(context.Background(),
		Program{CanonicalID: "jellyfin:abc123", Title: "Somebody's Film"}, StreamOptions{})
	if err == nil {
		t.Fatal("the archive.org resolver accepted a Jellyfin id")
	}
	if !strings.Contains(err.Error(), ErrLinearMissingMedia.Error()) {
		t.Errorf("error is %v, want a missing-media error so a resolver chain continues", err)
	}
}

// One probe per file, however many times the channel is tuned into.
func TestArchiveLinearResolverProbesEachFileOnce(t *testing.T) {
	f := newLinearFakeIA(t, linearFakeIAPool())
	_, res := newArchiveLinear()
	p := Program{CanonicalID: archiveLinearID("popeye_patriotic_popeye", "popeye_512kb.mp4")}
	for i := 0; i < 5; i++ {
		if _, err := res.LinearResolve(context.Background(), p, StreamOptions{OffsetSeconds: float64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt64(&f.rangeHits); got != 1 {
		t.Errorf("five tune-ins cost %d range probes, want 1", got)
	}
}

// --- the channels ------------------------------------------------------------

func TestNostalgiaChannelsAreValidAndPublic(t *testing.T) {
	e := NewLinearEngine("")
	libs, _ := newArchiveLinear()
	for _, l := range libs {
		e.AddPublicLibrary(l)
	}

	names := map[string]bool{}
	for _, l := range libs {
		names[l.LinearLibraryID()] = true
	}

	for _, ch := range nostalgiaChannels() {
		c := ch
		c.normalise()
		if err := c.Validate(); err != nil {
			t.Errorf("%s is not a valid channel: %v", ch.ID, err)
		}
		if !e.channelIsPublic(&c) {
			t.Errorf("%s is not public; Nostalgia TV has to be watchable without an account", ch.ID)
		}
		if !c.Enabled {
			t.Errorf("%s is switched off", ch.ID)
		}
		// A channel pointing at a library that does not exist would be an
		// empty channel with no error anywhere.
		for _, want := range c.SourceLibraries {
			if !names[want] {
				t.Errorf("%s names library %q, which no archive.org collection provides (have %v)",
					ch.ID, want, names)
			}
		}
	}
}

func TestNostalgiaChannelsCoverCartoonsAndClassicTV(t *testing.T) {
	chans := nostalgiaChannels()
	if len(chans) < 2 {
		t.Fatalf("Nostalgia TV ships %d channels, want at least a cartoons one and a classic-TV one", len(chans))
	}
	numbers := map[int]string{}
	for _, c := range chans {
		if prev, clash := numbers[c.Number]; clash {
			t.Errorf("channels %s and %s share number %d", prev, c.ID, c.Number)
		}
		numbers[c.Number] = c.ID
	}
}

func TestSeedNostalgiaChannelsIsIdempotentAndRespectsDeletion(t *testing.T) {
	dir := t.TempDir()
	e := NewLinearEngine(dir)
	libs, _ := newArchiveLinear()
	for _, l := range libs {
		e.AddPublicLibrary(l)
	}

	added := SeedNostalgiaChannels(e)
	if len(added) != len(nostalgiaChannels()) {
		t.Fatalf("the first seed created %v, want every built-in channel", added)
	}
	if again := SeedNostalgiaChannels(e); len(again) != 0 {
		t.Errorf("seeding twice created %v; it must only fill in what is absent", again)
	}

	// An edit survives.
	ch, ok := e.Channel("nostalgia-cartoons")
	if !ok {
		t.Fatal("the cartoons channel was not created")
	}
	ch.Name = "Wade's Cartoons"
	ch.Number = 42
	if _, err := e.SaveChannel(*ch); err != nil {
		t.Fatal(err)
	}
	SeedNostalgiaChannels(e)
	after, _ := e.Channel("nostalgia-cartoons")
	if after.Name != "Wade's Cartoons" || after.Number != 42 {
		t.Errorf("seeding overwrote an edited channel: %+v", after)
	}

	// A deletion sticks, including across a restart.
	e.DeleteChannel("nostalgia-cartoons")
	if added := SeedNostalgiaChannels(e); len(added) != 0 {
		t.Errorf("a deleted channel was re-created: %v", added)
	}
	restarted := NewLinearEngine(dir)
	for _, l := range libs {
		restarted.AddPublicLibrary(l)
	}
	if added := SeedNostalgiaChannels(restarted); len(added) != 0 {
		t.Errorf("a deleted channel came back after a restart: %v", added)
	}
	if _, exists := restarted.Channel("nostalgia-classic-tv"); !exists {
		t.Error("the channel that was never deleted did not survive the restart")
	}
}

// --- end to end --------------------------------------------------------------

// The whole path a viewer takes, against the stub: list the channels, read the
// guide, ask what is on, tune in, and land at the offset the schedule dictates
// -- all with no account.
func TestNostalgiaChannelPlaysForAStranger(t *testing.T) {
	newLinearFakeIA(t, linearFakeIAPool())

	clk := &linearClock{t: linearEpoch}
	e := NewLinearEngine("")
	e.nowFn = clk.now
	e.pastBuffer = time.Hour
	e.retryBackoff = 0
	libs, res := newArchiveLinear()
	for _, l := range libs {
		e.AddPublicLibrary(l)
	}
	e.SetResolver(res)
	// Warm before asserting, exactly as main.go does at startup. Reading the
	// pool never waits for archive.org, so without this the channels would
	// correctly report themselves as still loading.
	archiveWarmAll(t, libs)
	if added := SeedNostalgiaChannels(e); len(added) == 0 {
		t.Fatal("no channels were created")
	}
	// Nobody is the owner: this is a stranger's session throughout.
	mux := http.NewServeMux()
	e.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var list struct {
		Channels []linearChannelRow `json:"channels"`
	}
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"channels", &list); code != 200 {
		t.Fatalf("channel list returned %d", code)
	}
	if len(list.Channels) == 0 {
		t.Fatal("a stranger saw no channels at all; Nostalgia TV must be watchable without an account")
	}
	id := list.Channels[0].ID

	var guide struct {
		Programs []LinearGuideEntry `json:"programs"`
	}
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"guide?channels="+id, &guide); code != 200 {
		t.Fatalf("guide returned %d", code)
	}
	if len(guide.Programs) == 0 {
		t.Fatal("the guide was empty")
	}

	var np LinearNowPlaying
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"now?channel="+id, &np); code != 200 {
		t.Fatalf("now returned %d", code)
	}
	if np.State != LinearStateOnAir || np.Program == nil {
		t.Fatalf("nothing is on air: %+v", np)
	}

	// Move the clock into the middle of what is on, so the offset is real
	// rather than zero.
	mid := np.Program.StartTime + (np.Program.EndTime-np.Program.StartTime)/2
	clk.advance(time.Duration(mid-np.ServerNow) * time.Second)

	var tune LinearTuneIn
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"stream?channel="+id, &tune); code != 200 {
		t.Fatalf("stream returned %d: %+v", code, tune)
	}
	if !tune.Available || tune.Source == nil {
		t.Fatalf("nothing playable: %+v", tune)
	}
	if !tune.Source.Seekable {
		t.Error("the source was not reported seekable, so a mid-programme join is a lie")
	}
	if tune.OffsetSeconds <= 0 {
		t.Fatalf("tuning in mid-programme produced an offset of %v", tune.OffsetSeconds)
	}
	if !strings.Contains(tune.Source.URL, "#t=") {
		t.Errorf("the offset did not reach the playable URL: %q", tune.Source.URL)
	}

	// And the bytes really are there, at a non-zero offset.
	base, _, _ := strings.Cut(tune.Source.URL, "#")
	req, _ := http.NewRequest(http.MethodGet, base, nil)
	req.Header.Set("Range", "bytes=1024-2047")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("asking for bytes 1024-2047 returned %d, want 206", resp.StatusCode)
	}
	if resp.Header.Get("Content-Range") == "" {
		t.Error("a 206 arrived with no Content-Range")
	}
}

// --- against the real archive.org --------------------------------------------

// The whole thing against the live service.
//
// Skipped by default, like TestLinearAgainstARealLibrary above it and for the
// same reason: the committed suite has to be hermetic, and a test that calls
// archive.org fails when a collection is reindexed rather than when this code
// breaks. Set YARRIT_LINEAR_LIVE_ARCHIVE=1 to run it.
//
// What it is for is the question a stub cannot answer -- whether the durations
// are real, whether the bytes are there, and whether a mid-programme join
// actually gets a 206 from their storage nodes. Run it whenever the collections
// or the file-format preferences change.
func TestNostalgiaAgainstLiveArchive(t *testing.T) {
	if strings.TrimSpace(os.Getenv("YARRIT_LINEAR_LIVE_ARCHIVE")) == "" {
		t.Skip("set YARRIT_LINEAR_LIVE_ARCHIVE=1 to run this against archive.org")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	clk := &linearClock{t: time.Now().UTC()}
	e := NewLinearEngine("")
	e.nowFn = clk.now
	e.retryBackoff = 0
	libs, res := newArchiveLinear()
	for _, l := range libs {
		e.AddPublicLibrary(l)
	}
	e.SetResolver(res)

	for _, l := range libs {
		// A budget each. Sharing one across both libraries only measures how
		// long the first took.
		lctx, lcancel := context.WithTimeout(ctx, 10*time.Minute)
		started := time.Now()
		items, err := l.LinearItems(lctx)
		lcancel()
		if err != nil {
			t.Fatalf("%s: %v", l.LinearLibraryID(), err)
		}
		t.Logf("LIVE %-12s refreshed in %s", l.LinearLibraryID(), time.Since(started).Round(time.Second))
		schedulable := linearSchedulable(items)
		var secs int
		for _, it := range schedulable {
			secs += it.DurationSeconds
		}
		t.Logf("LIVE %-12s %3d candidates, %3d schedulable, %5.1f hours",
			l.LinearLibraryID(), len(items), len(schedulable), float64(secs)/3600)
		if len(schedulable) < 20 {
			t.Errorf("%s yielded only %d schedulable programmes; a channel needs a pool",
				l.LinearLibraryID(), len(schedulable))
		}
		for _, it := range schedulable {
			if it.DurationSeconds <= 0 {
				t.Fatalf("%s: %q is in the schedulable set with no duration", l.LinearLibraryID(), it.Title)
			}
			if _, _, ok := parseArchiveLinearID(it.CanonicalID); !ok {
				t.Fatalf("%s: %q has an unresolvable id %q", l.LinearLibraryID(), it.Title, it.CanonicalID)
			}
		}
	}

	if added := SeedNostalgiaChannels(e); len(added) == 0 {
		t.Fatal("no channels were created")
	}
	mux := http.NewServeMux()
	e.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// A stranger throughout: no owner func is installed.
	var list struct {
		Channels []linearChannelRow `json:"channels"`
	}
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"channels", &list); code != 200 {
		t.Fatalf("channel list returned %d", code)
	}
	if len(list.Channels) < 2 {
		t.Fatalf("a stranger saw %d channels", len(list.Channels))
	}

	for _, row := range list.Channels {
		t.Run(row.ID, func(t *testing.T) {
			var np LinearNowPlaying
			if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"now?channel="+row.ID, &np); code != 200 {
				t.Fatalf("now returned %d", code)
			}
			if np.State != LinearStateOnAir || np.Program == nil {
				t.Fatalf("nothing on air: %+v", np)
			}
			t.Logf("LIVE %s #%d %q is on: %q, %.0fs in of %ds",
				row.ID, row.Number, row.Name, np.Program.Title,
				np.OffsetSeconds, np.Program.EndTime-np.Program.StartTime)

			// Stand in the middle of the programme so the offset is real.
			mid := np.Program.StartTime + (np.Program.EndTime-np.Program.StartTime)/2
			clk.advance(time.Duration(mid-np.ServerNow) * time.Second)

			var tune LinearTuneIn
			if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"stream?channel="+row.ID, &tune); code != 200 {
				t.Fatalf("stream returned %d: %+v", code, tune)
			}
			if !tune.Available || tune.Source == nil {
				t.Fatalf("nothing playable: %s", tune.Detail)
			}
			if !tune.Source.Seekable {
				t.Fatalf("archive.org would not serve %q by range", tune.Source.URL)
			}
			if tune.OffsetSeconds <= 0 {
				t.Fatalf("offset is %v", tune.OffsetSeconds)
			}
			t.Logf("LIVE %s -> %s (offset %.0fs, seekable=%v)",
				row.ID, tune.Source.URL, tune.OffsetSeconds, tune.Source.Seekable)

			// Real media bytes, at a non-zero offset, from the live service.
			base, _, _ := strings.Cut(tune.Source.URL, "#")
			off := int64(tune.OffsetSeconds) * 20000 // roughly the 512kbps bitrate
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+2047))
			req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("fetching bytes at offset %d: %v", off, err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("range request at byte %d returned %d, want 206", off, resp.StatusCode)
			}
			if len(body) == 0 {
				t.Fatal("a 206 arrived with no bytes in it")
			}
			t.Logf("LIVE %s <- %d bytes, %s, Content-Range: %s",
				row.ID, len(body), resp.Header.Get("Content-Type"),
				resp.Header.Get("Content-Range"))
		})
	}
}

// The stub's search response has to decode through the same struct production
// uses, or these tests pass against a shape archive.org never sends.
func TestArchiveLinearSearchDecodesTheRealShape(t *testing.T) {
	// Captured from archive.org's advancedsearch.php on 2026-08-08.
	const body = `{"responseHeader":{"status":0},"response":{"numFound":523,"start":0,"docs":[
	  {"identifier":"Popeye_forPresident","title":"Popeye for President","downloads":123,
	   "licenseurl":"http://creativecommons.org/publicdomain/mark/1.0/",
	   "collection":["classic_cartoons","animationandcartoons"],"year":"1956"},
	  {"identifier":"pdcartooncollection","title":"PD Cartoon Collection","downloads":99,
	   "licenseurl":"http://creativecommons.org/licenses/publicdomain/","year":1940}
	]}}`
	var out struct {
		Response struct {
			Docs []archiveLinearDoc `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("a real search response did not decode: %v", err)
	}
	if len(out.Response.Docs) != 2 {
		t.Fatalf("decoded %d docs", len(out.Response.Docs))
	}
	// `year` arrives as a string on one item and a number on the other, which
	// is why it is a RawMessage.
	if got := archiveYear(out.Response.Docs[0].Year); got != 1956 {
		t.Errorf("string year decoded to %d", got)
	}
	if got := archiveYear(out.Response.Docs[1].Year); got != 1940 {
		t.Errorf("numeric year decoded to %d", got)
	}
	if !archiveLinearIsPD(out.Response.Docs[0].LicenseURL) {
		t.Error("a real public-domain mark was not recognised")
	}
}

// The same for metadata, including the two shapes `length` arrives in and the
// error the Archive returns with HTTP 200.
func TestArchiveLinearMetadataDecodesTheRealShape(t *testing.T) {
	// Captured from archive.org/metadata/popeye_patriotic_popeye, 2026-08-08.
	const body = `{"metadata":{"identifier":"popeye_patriotic_popeye","mediatype":"movies",
	  "collection":["classic_cartoons","animationandcartoons"],"title":"Patriotic Popeye",
	  "runtime":"6:00"},
	  "files":[
	    {"name":"__ia_thumb.jpg","format":"Item Tile","size":"14248"},
	    {"name":"popeye_patriotic_popeye.mpeg","format":"MPEG2","length":"362.98","size":"162568192"},
	    {"name":"popeye_patriotic_popeye_512kb.mp4","format":"512Kb MPEG4","length":"363.16","size":"26402792"}
	  ]}`
	var m archiveLinearMeta
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("real metadata did not decode: %v", err)
	}
	f, ok := archiveLinearPickFile(m.Files)
	if !ok {
		t.Fatal("no playable file was found in real metadata")
	}
	if f.Name != "popeye_patriotic_popeye_512kb.mp4" || f.Seconds != 363 {
		t.Errorf("picked %+v", f)
	}
	// `collection` is a bare string when an item is in exactly one.
	var single archiveLinearMeta
	if err := json.Unmarshal([]byte(`{"metadata":{"collection":"stream_only"},"files":[]}`), &single); err != nil {
		t.Fatalf("a single-collection item did not decode: %v", err)
	}
	if !single.streamOnly() {
		t.Error("a stream-only marker sent as a bare string was missed")
	}

	// The error shape, which arrives with HTTP 200 and no files.
	var bad archiveLinearMeta
	if err := json.Unmarshal([]byte(`{"error":"item metadata may be invalid"}`), &bad); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(bad.Error) == "" {
		t.Error("the Archive's own error field was not decoded, so it would read as an empty item")
	}
}
