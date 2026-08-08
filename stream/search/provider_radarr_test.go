package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A stub Radarr wired for the common case: it answers system/status, has a
// root folder and a quality profile, and an empty queue. Individual tests
// overwrite whichever route they are about.
func radarrStub(t *testing.T) (*arrStub, *radarrProvider) {
	t.Helper()
	stub := newArrStub(t)
	stub.status("v3", "Radarr", "6.2.1.10461")
	stub.healthEntries("v3")
	stub.on("GET /api/v3/queue", 200, radarrQueuePage{})
	stub.on("GET /api/v3/rootfolder", 200, []arrRootFolder{
		{ID: 10, Path: "/mnt/nothing", Accessible: false},
		{ID: 2, Path: "/mnt/usb2/Movie", Accessible: true},
	})
	stub.on("GET /api/v3/qualityprofile", 200, []arrProfile{
		{ID: 7, Name: "1080p-Tight"}, {ID: 4, Name: "HD-1080p"},
	})

	cfg := stub.config()
	cfg.ID, cfg.Name = "radarr", "Radarr"
	return stub, newRadarrProvider(cfg)
}

func TestRadarrIdentity(t *testing.T) {
	_, p := radarrStub(t)
	if got := p.Domains(); len(got) != 1 || got[0] != "video" {
		t.Errorf("Domains() = %v, want [video]", got)
	}
	if !contains(p.Roles(), "acquisition") || !contains(p.Roles(), "library") || !contains(p.Roles(), "discovery") {
		t.Errorf("Roles() = %v", p.Roles())
	}
}

// A search must return schema.json's words, not Radarr's. This is the exact
// place `kind=movies` was born.
func TestRadarrSearchSpeaksTheCanonicalVocabulary(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie/lookup", 200, []radarrMovie{
		{ID: 587, Title: "Blade Runner", Year: 1982, TmdbID: 78, MovieFileID: 4927,
			Studio: "Warner Bros.",
			Images: []arrImage{{CoverType: "poster", RemoteURL: "https://img.test/br.jpg"}}},
		{ID: 0, Title: "The Blade Runner Phenomenon", Year: 2021, TmdbID: 833898},
	})

	items, err := p.Search(arrTestCtx(t), "blade runner", "movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	for _, it := range items {
		if it.Domain != "video" {
			t.Errorf("%q has domain %q, want video", it.Title, it.Domain)
		}
		if it.Type != "movie" {
			t.Errorf("%q has type %q, want movie", it.Title, it.Type)
		}
		if it.ProviderID != "radarr" {
			t.Errorf("%q has providerId %q", it.Title, it.ProviderID)
		}
	}
	if items[0].CanonicalID != "tmdb:movie:78" {
		t.Errorf("canonicalId = %q", items[0].CanonicalID)
	}
	if items[0].Artwork != "https://img.test/br.jpg" {
		t.Errorf("artwork = %q", items[0].Artwork)
	}
}

// A music query must not be answered by a film service. Not an error -- there
// is simply nothing to say -- because an error would make a multi-provider
// fan-out report a failure that did not happen.
func TestRadarrIgnoresOtherDomains(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie/lookup", 200, []radarrMovie{{ID: 1, Title: "no", TmdbID: 1}})

	for _, domain := range []string{"music", "audio", "comics", "games"} {
		items, err := p.Search(arrTestCtx(t), "anything", domain)
		if err != nil {
			t.Fatalf("domain %q: %v", domain, err)
		}
		if len(items) != 0 {
			t.Errorf("domain %q returned %d film results", domain, len(items))
		}
	}
	if n := len(stub.callsTo("GET", "/api/v3/movie/lookup")); n != 0 {
		t.Errorf("Radarr was called %d times for a non-video domain", n)
	}
}

// The five states a card must tell apart. Without this, "already requested"
// and "missing" look the same and the user requests it twice.
func TestRadarrSearchDistinguishesEveryLibraryState(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie/lookup", 200, []radarrMovie{
		{ID: 0, Title: "Never Heard Of It", TmdbID: 1},
		{ID: 10, Title: "Tracked, Unmonitored", TmdbID: 2, Monitored: false},
		{ID: 11, Title: "Requested", TmdbID: 3, Monitored: true},
		{ID: 12, Title: "Downloading", TmdbID: 4, Monitored: true},
		{ID: 13, Title: "Importing", TmdbID: 5, Monitored: true},
		{ID: 14, Title: "Available", TmdbID: 6, Monitored: true, MovieFileID: 99},
	})
	stub.on("GET /api/v3/queue", 200, radarrQueuePage{Records: []radarrQueueRecord{
		{arrQueueRecord: arrQueueRecord{MovieID: 12, Status: "downloading",
			TrackedDownloadState: "downloading", Size: 100, SizeLeft: 40}},
		{arrQueueRecord: arrQueueRecord{MovieID: 13, Status: "completed",
			TrackedDownloadState: "importing", Size: 100, SizeLeft: 0}},
	}})

	items, err := p.Search(arrTestCtx(t), "state", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []LibraryState{
		StateMissing, StateMissing, StateRequested,
		StateDownloading, StateImporting, StateAvailable,
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d", len(items), len(want))
	}
	for i, it := range items {
		if it.State != want[i] {
			t.Errorf("%q state = %q, want %q", it.Title, it.State, want[i])
		}
	}
}

// A dead queue endpoint must cost state detail, not the whole search.
func TestRadarrSearchSurvivesADeadQueue(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/queue", 500, map[string]string{"error": "boom"})
	stub.on("GET /api/v3/movie/lookup", 200, []radarrMovie{{ID: 5, Title: "Dune", TmdbID: 438631, MovieFileID: 7}})

	items, err := p.Search(arrTestCtx(t), "dune", "")
	if err != nil {
		t.Fatalf("a broken queue took the search down: %v", err)
	}
	if len(items) != 1 || items[0].State != StateAvailable {
		t.Errorf("items = %+v", items)
	}
}

func TestRadarrLibraryStatusWithoutFetchingTheWholeShelf(t *testing.T) {
	stub, p := radarrStub(t)
	stub.onFunc("GET /api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		// The filter is the entire point: unfiltered, this endpoint is 28 MB
		// on a real library.
		if r.URL.Query().Get("tmdbId") != "78" {
			t.Errorf("LibraryStatus fetched the shelf instead of filtering (query %v)", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]radarrMovie{
			{ID: 587, Title: "Blade Runner", TmdbID: 78, HasFile: true, MovieFileID: 4927},
		})
	})

	got, err := p.LibraryStatus(arrTestCtx(t), "tmdb:movie:78")
	if err != nil {
		t.Fatal(err)
	}
	if got != StateAvailable {
		t.Errorf("state = %q, want available", got)
	}
}

func TestRadarrLibraryStatusReportsMissing(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie", 200, []radarrMovie{})

	got, err := p.LibraryStatus(arrTestCtx(t), "tmdb:movie:999999")
	if err != nil {
		t.Fatal(err)
	}
	if got != StateMissing {
		t.Errorf("state = %q, want missing", got)
	}
}

// An id this provider cannot read is "unknown", never "missing": claiming
// missing about something we never checked offers a request for a thing that
// may well be sitting on the shelf next door.
func TestRadarrLibraryStatusOfSomeoneElsesIDIsUnknown(t *testing.T) {
	_, p := radarrStub(t)
	got, err := p.LibraryStatus(arrTestCtx(t), "mbid:album:e75c0549-ad55-39e3-8025-c72c5d4a3c5d")
	if err != nil {
		t.Fatal(err)
	}
	if got != StateUnknown {
		t.Errorf("state = %q, want unknown", got)
	}
}

func TestRadarrLibraryListsEverything(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie", 200, []radarrMovie{
		{ID: 1, Title: "Total Recall", Year: 1990, TmdbID: 861, HasFile: false, Monitored: false},
		{ID: 2, Title: "Ong Bak 2", Year: 2008, TmdbID: 16353, HasFile: true},
	})

	items, err := p.Library(arrTestCtx(t), "video")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].State != StateMissing || items[1].State != StateAvailable {
		t.Errorf("states = %q, %q", items[0].State, items[1].State)
	}
}

func TestRadarrDetailsFallsBackToMetadataForSomethingWeDoNotHave(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie", 200, []radarrMovie{})
	stub.onFunc("GET /api/v3/movie/lookup", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("term") != "tmdb:335984" {
			t.Errorf("lookup term = %q, want the id form", r.URL.Query().Get("term"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]radarrMovie{
			{ID: 0, Title: "Blade Runner 2049", Year: 2017, TmdbID: 335984},
		})
	})

	item, children, err := p.Details(arrTestCtx(t), "tmdb:movie:335984")
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Blade Runner 2049" || item.State != StateMissing {
		t.Errorf("item = %+v", item)
	}
	// A film contains nothing. The shape is shared with Sonarr, where it does.
	if len(children) != 0 {
		t.Errorf("a film reported %d children", len(children))
	}
}

func TestRadarrActivityReportsProgressAndStage(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/queue", 200, radarrQueuePage{Records: []radarrQueueRecord{
		{arrQueueRecord: arrQueueRecord{
			MovieID: 84, Title: "A Black Veil For Lisa (1968) [BluRay] [1080p]",
			Status: "downloading", TrackedDownloadState: "downloading",
			Size: 1000, SizeLeft: 250,
		}, Movie: &radarrMovie{Title: "A Black Veil for Lisa", TmdbID: 104935}},
	}})

	items, err := p.Activity(arrTestCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d activity rows", len(items))
	}
	a := items[0]
	if a.Title != "A Black Veil for Lisa" {
		t.Errorf("title = %q; the release name is not what a person recognises", a.Title)
	}
	if a.CanonicalID != "tmdb:movie:104935" {
		t.Errorf("canonicalId = %q; without it a row cannot be linked to its card", a.CanonicalID)
	}
	if a.Domain != "video" || a.Stage != "downloading" || a.Progress != 0.75 {
		t.Errorf("row = %+v", a)
	}
}

// Requesting something already on disk must not POST a duplicate. Radarr
// answers a second add with a 400 that reads like a broken adapter.
func TestRadarrRequestForSomethingAlreadyHeldAddsNothing(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie", 200, []radarrMovie{
		{ID: 587, Title: "Blade Runner", TmdbID: 78, HasFile: true, MovieFileID: 4927, Monitored: true},
	})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tmdb:movie:78"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || !strings.Contains(res.Detail, "already") {
		t.Errorf("result = %+v", res)
	}
	if n := len(stub.callsTo("POST", "/api/v3/movie")); n != 0 {
		t.Errorf("it added the film again (%d POSTs)", n)
	}
	if n := len(stub.callsTo("POST", "/api/v3/command")); n != 0 {
		t.Errorf("it searched for a film already on disk (%d commands)", n)
	}
}

// The tracked-but-missing case: monitor it and search, do not re-add.
func TestRadarrRequestForATrackedFilmMonitorsAndSearches(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie", 200, []radarrMovie{
		{ID: 1, Title: "Total Recall", TmdbID: 861, Monitored: false},
	})
	stub.on("PUT /api/v3/movie/1", 202, map[string]any{"id": 1})
	stub.on("POST /api/v3/command", 201, map[string]any{"id": 1})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tmdb:movie:861"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}
	puts := stub.callsTo("PUT", "/api/v3/movie/1")
	if len(puts) != 1 {
		t.Fatalf("expected one monitor update, got %d", len(puts))
	}
	var updated radarrMovie
	if err := json.Unmarshal(puts[0].Body, &updated); err != nil {
		t.Fatal(err)
	}
	if !updated.Monitored {
		t.Error("the film was not switched to monitored, so nothing will ever be grabbed")
	}
	cmds := stub.callsTo("POST", "/api/v3/command")
	if len(cmds) != 1 {
		t.Fatalf("expected one search command, got %d", len(cmds))
	}
	var cmd arrCommand
	if err := json.Unmarshal(cmds[0].Body, &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "MoviesSearch" || len(cmd.MovieIDs) != 1 || cmd.MovieIDs[0] != 1 {
		t.Errorf("command = %+v", cmd)
	}
	if n := len(stub.callsTo("POST", "/api/v3/movie")); n != 0 {
		t.Errorf("it re-added a film Radarr already had (%d POSTs)", n)
	}
}

func TestRadarrRequestAddsWithResolvedDefaults(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie", 200, []radarrMovie{})
	stub.on("GET /api/v3/movie/lookup", 200, []radarrMovie{
		{ID: 0, Title: "Blade Runner 2049", Year: 2017, TmdbID: 335984},
	})
	stub.on("POST /api/v3/movie", 201, radarrMovie{ID: 900, Title: "Blade Runner 2049", TmdbID: 335984})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tmdb:movie:335984"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}
	posts := stub.callsTo("POST", "/api/v3/movie")
	if len(posts) != 1 {
		t.Fatalf("expected one add, got %d", len(posts))
	}
	var sent radarrMovie
	if err := json.Unmarshal(posts[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	// The inaccessible root must be skipped: adding into an unmounted share
	// produces an item Radarr immediately reports as broken.
	if sent.RootFolderPath != "/mnt/usb2/Movie" {
		t.Errorf("rootFolderPath = %q, want the accessible one", sent.RootFolderPath)
	}
	if sent.QualityProfileID != 4 {
		t.Errorf("qualityProfileId = %d, want the lowest id (4)", sent.QualityProfileID)
	}
	if !sent.Monitored {
		t.Error("monitored = false on a request that asked for it")
	}
	if sent.AddOptions == nil || !sent.AddOptions.SearchForMovie {
		t.Errorf("addOptions = %+v; nothing would be downloaded", sent.AddOptions)
	}
	if sent.ID != 0 {
		t.Errorf("id = %d on an add payload; Radarr rejects a pre-set id", sent.ID)
	}
}

// Monitor false is a watchlist add. If it started a search, "save for later"
// would silently fill the disk.
func TestRadarrRequestWithoutMonitorDownloadsNothing(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie", 200, []radarrMovie{})
	stub.on("GET /api/v3/movie/lookup", 200, []radarrMovie{
		{ID: 0, Title: "Blade Runner 2049", TmdbID: 335984},
	})
	stub.on("POST /api/v3/movie", 201, radarrMovie{ID: 900, Title: "Blade Runner 2049", TmdbID: 335984})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tmdb:movie:335984"}, RequestOptions{Monitor: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}
	var sent radarrMovie
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v3/movie")[0].Body, &sent)
	if sent.Monitored {
		t.Error("an unmonitored add was monitored")
	}
	if sent.AddOptions != nil && sent.AddOptions.SearchForMovie {
		t.Error("a watchlist add started a search")
	}
	if n := len(stub.callsTo("POST", "/api/v3/command")); n != 0 {
		t.Errorf("a watchlist add fired %d commands", n)
	}
}

// Radarr's 400 body names the field it disliked, and that is the only useful
// thing to hand back. Returning a bare error would lose it.
func TestRadarrRequestSurfacesARejection(t *testing.T) {
	stub, p := radarrStub(t)
	stub.on("GET /api/v3/movie", 200, []radarrMovie{})
	stub.on("GET /api/v3/movie/lookup", 200, []radarrMovie{{ID: 0, Title: "X", TmdbID: 5}})
	stub.onFunc("POST /api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`[{"propertyName":"Path","errorMessage":"already configured for an existing movie"}]`))
	})

	res, err := p.Request(arrTestCtx(t), MediaItem{CanonicalID: "tmdb:movie:5"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatalf("a rejection is an answer, not a transport failure: %v", err)
	}
	if res.Accepted {
		t.Error("a rejected add was reported as accepted")
	}
	if !strings.Contains(res.Detail, "already configured") {
		t.Errorf("detail = %q; Radarr's reason was lost", res.Detail)
	}
}

func TestRadarrRefusesAnIDItCannotAct(t *testing.T) {
	_, p := radarrStub(t)
	if _, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:series:78874"}, RequestOptions{Monitor: true}); err == nil {
		t.Error("Radarr accepted a request for a television series")
	}
}
