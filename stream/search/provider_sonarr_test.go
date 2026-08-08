package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func sonarrStub(t *testing.T) (*arrStub, *sonarrProvider) {
	t.Helper()
	stub := newArrStub(t)
	stub.status("v3", "Sonarr", "4.0.19.2979")
	stub.healthEntries("v3")
	stub.on("GET /api/v3/queue", 200, sonarrQueuePage{})
	stub.on("GET /api/v3/rootfolder", 200, []arrRootFolder{
		{ID: 10, Path: "/mnt/lvm_shared/TV_Show", Accessible: false},
		{ID: 3, Path: "/mnt/usb2/TV_Shows", Accessible: true},
	})
	stub.on("GET /api/v3/qualityprofile", 200, []arrProfile{
		{ID: 4, Name: "HD-1080p"}, {ID: 1, Name: "Perfect-Plex-TV"},
	})

	cfg := stub.config()
	cfg.ID, cfg.Name = "sonarr", "Sonarr"
	return stub, newSonarrProvider(cfg)
}

// A show with two seasons: one complete, one not started.
func testSeries() sonarrSeries {
	return sonarrSeries{
		ID: 17, Title: "The Cleveland Show", Year: 2009, TvdbID: 95015,
		Monitored: true, SeriesType: "standard", Network: "FOX",
		Statistics: &sonarrSeasonStats{EpisodeFileCount: 8, EpisodeCount: 16, TotalEpisodeCount: 16},
		Seasons: []sonarrSeason{
			{SeasonNumber: 1, Monitored: true,
				Statistics: &sonarrSeasonStats{EpisodeFileCount: 8, EpisodeCount: 8, TotalEpisodeCount: 8}},
			{SeasonNumber: 2, Monitored: false,
				Statistics: &sonarrSeasonStats{EpisodeFileCount: 0, EpisodeCount: 8, TotalEpisodeCount: 8}},
		},
		Images: []arrImage{{CoverType: "poster", RemoteURL: "https://img.test/cs.jpg"}},
	}
}

func TestSonarrIdentity(t *testing.T) {
	_, p := sonarrStub(t)
	if got := p.Domains(); len(got) != 1 || got[0] != "video" {
		t.Errorf("Domains() = %v, want [video]", got)
	}
}

func TestSonarrSearchReturnsSeriesInTheCanonicalVocabulary(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series/lookup", 200, []sonarrSeries{
		{ID: 294, Title: "Firefly", Year: 2002, TvdbID: 78874, SeriesType: "standard"},
		{ID: 0, Title: "Firefly Lane", Year: 2021, TvdbID: 384248, SeriesType: "standard"},
	})
	stub.on("GET /api/v3/series/294", 200, sonarrSeries{
		ID: 294, Title: "Firefly", Year: 2002, TvdbID: 78874, Monitored: true,
		Statistics: &sonarrSeasonStats{EpisodeFileCount: 14, EpisodeCount: 14, TotalEpisodeCount: 14},
	})

	items, err := p.Search(arrTestCtx(t), "firefly", "tv")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Type != "series" || items[0].Domain != "video" {
		t.Errorf("item = %+v", items[0])
	}
	if items[0].CanonicalID != "tvdb:series:78874" {
		t.Errorf("canonicalId = %q", items[0].CanonicalID)
	}
	// Sonarr's lookup zeroes the statistics even for shows it already has, so
	// without the second pass a fully-downloaded show reads as missing and the
	// user is offered a request for something they already own.
	if items[0].State != StateAvailable {
		t.Errorf("an in-library show reported %q", items[0].State)
	}
	if items[1].State != StateMissing {
		t.Errorf("a show we do not have reported %q", items[1].State)
	}
}

// Anime is a type inside the video domain, not a domain of its own. Flattening
// it into "series" loses the only word a client has for absolute numbering.
func TestSonarrCarriesAnimeThroughAsItsOwnType(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series/lookup", 200, []sonarrSeries{
		{ID: 0, Title: "Cowboy Bebop", Year: 1998, TvdbID: 76885, SeriesType: "anime"},
		{ID: 0, Title: "Neighbours", Year: 1985, TvdbID: 76028, SeriesType: "daily"},
	})

	items, err := p.Search(arrTestCtx(t), "anime", "anime")
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Type != "anime" {
		t.Errorf("anime series has type %q", items[0].Type)
	}
	if items[0].CanonicalID != "tvdb:anime:76885" {
		t.Errorf("canonicalId = %q", items[0].CanonicalID)
	}
	if canonicalDomain(items[0].Type) != "video" {
		t.Errorf("type %q does not resolve to the video domain", items[0].Type)
	}
	// "daily" is a scheduling hint about a standard show, not a kind of thing.
	// Inventing a type for it would put a word in the vocabulary no client can
	// render.
	if items[1].Type != "series" {
		t.Errorf("a daily show has type %q, want series", items[1].Type)
	}
}

// The reason this adapter exists in three depths.
func TestSonarrDetailsWalksSeriesToSeasonToEpisode(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{testSeries()})
	stub.on("GET /api/v3/episode", 200, []sonarrEpisode{
		{ID: 2116, SeriesID: 17, SeasonNumber: 2, EpisodeNumber: 1, Title: "Da Doggone Daddy-Daughter Dance", HasFile: false, Monitored: true},
		{ID: 2117, SeriesID: 17, SeasonNumber: 2, EpisodeNumber: 2, Title: "Cleveland Live!", HasFile: true, EpisodeFileID: 44},
	})

	// Series -> seasons.
	item, children, err := p.Details(arrTestCtx(t), "tvdb:series:95015")
	if err != nil {
		t.Fatal(err)
	}
	if item.Type != "series" {
		t.Errorf("item type = %q", item.Type)
	}
	if len(children) != 2 {
		t.Fatalf("got %d seasons", len(children))
	}
	if children[0].Type != "season" || children[0].CanonicalID != "tvdb:season:95015:1" {
		t.Errorf("season child = %+v", children[0])
	}
	if children[0].State != StateAvailable {
		t.Errorf("a complete season reported %q", children[0].State)
	}
	if children[1].State != StateMissing {
		t.Errorf("an unmonitored empty season reported %q, want missing", children[1].State)
	}

	// Season -> episodes.
	item, children, err = p.Details(arrTestCtx(t), "tvdb:season:95015:2")
	if err != nil {
		t.Fatal(err)
	}
	if item.Type != "season" || item.Season != 2 {
		t.Errorf("season item = %+v", item)
	}
	if len(children) != 2 {
		t.Fatalf("got %d episodes", len(children))
	}
	if children[0].Type != "episode" || children[0].CanonicalID != "tvdb:episode:95015:2:1" {
		t.Errorf("episode child = %+v", children[0])
	}
	if children[0].State != StateRequested {
		t.Errorf("a monitored missing episode reported %q, want requested", children[0].State)
	}
	if children[1].State != StateAvailable {
		t.Errorf("an episode on disk reported %q", children[1].State)
	}

	// Episode -> itself.
	item, children, err = p.Details(arrTestCtx(t), "tvdb:episode:95015:2:2")
	if err != nil {
		t.Fatal(err)
	}
	if item.Episode != 2 || item.Season != 2 || len(children) != 0 {
		t.Errorf("episode item = %+v children = %d", item, len(children))
	}
}

func TestSonarrLibraryStatusAnswersAtEveryDepth(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{testSeries()})
	stub.on("GET /api/v3/episode", 200, []sonarrEpisode{
		{ID: 2116, SeriesID: 17, SeasonNumber: 2, EpisodeNumber: 1, Monitored: true},
		{ID: 2117, SeriesID: 17, SeasonNumber: 2, EpisodeNumber: 2, HasFile: true},
	})

	for _, c := range []struct {
		id   string
		want LibraryState
	}{
		{"tvdb:series:95015", StateAvailable},
		{"tvdb:season:95015:1", StateAvailable},
		{"tvdb:season:95015:2", StateMissing},
		{"tvdb:episode:95015:2:1", StateRequested},
		{"tvdb:episode:95015:2:2", StateAvailable},
		{"tvdb:season:95015:9", StateMissing},
		{"tvdb:episode:95015:2:99", StateMissing},
	} {
		got, err := p.LibraryStatus(arrTestCtx(t), c.id)
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestSonarrLibraryStatusOfAShowWeDoNotHave(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{})

	got, err := p.LibraryStatus(arrTestCtx(t), "tvdb:episode:999999:1:1")
	if err != nil {
		t.Fatal(err)
	}
	if got != StateMissing {
		t.Errorf("state = %q, want missing", got)
	}
}

// Adding a whole show is the only case where Sonarr's own add-time monitoring
// is what was asked for.
func TestSonarrRequestForAWholeShowAddsWithMonitorAll(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{})
	stub.on("GET /api/v3/series/lookup", 200, []sonarrSeries{
		{ID: 0, Title: "Firefly", TvdbID: 78874, SeriesType: "standard",
			Seasons: []sonarrSeason{{SeasonNumber: 1, Monitored: true}}},
	})
	stub.on("POST /api/v3/series", 201, sonarrSeries{
		ID: 700, Title: "Firefly", TvdbID: 78874, Monitored: true,
		Seasons: []sonarrSeason{{SeasonNumber: 1, Monitored: true}},
	})
	stub.on("POST /api/v3/command", 201, map[string]any{"id": 1})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:series:78874"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}

	var sent sonarrSeries
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v3/series")[0].Body, &sent)
	if sent.RootFolderPath != "/mnt/usb2/TV_Shows" {
		t.Errorf("rootFolderPath = %q, want the accessible one", sent.RootFolderPath)
	}
	if sent.QualityProfileID != 1 {
		t.Errorf("qualityProfileId = %d, want the lowest id", sent.QualityProfileID)
	}
	if sent.AddOptions == nil || sent.AddOptions.Monitor != "all" || !sent.AddOptions.SearchForMissingEpisodes {
		t.Errorf("addOptions = %+v, want monitor all with a search", sent.AddOptions)
	}
}

// The case this adapter is shaped around: asking for one season must not grab
// nine. Sonarr has no "add just this season" call, so the series is added with
// monitor "none" and exactly the wanted season is switched on afterwards.
func TestSonarrSeasonRequestDoesNotGrabTheWholeShow(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{})
	stub.on("GET /api/v3/series/lookup", 200, []sonarrSeries{
		{ID: 0, Title: "The Cleveland Show", TvdbID: 95015, SeriesType: "standard",
			Seasons: []sonarrSeason{
				{SeasonNumber: 1, Monitored: true},
				{SeasonNumber: 2, Monitored: true},
				{SeasonNumber: 3, Monitored: true},
			}},
	})
	stub.on("POST /api/v3/series", 201, sonarrSeries{
		ID: 700, Title: "The Cleveland Show", TvdbID: 95015,
		Seasons: []sonarrSeason{
			{SeasonNumber: 1, Monitored: false},
			{SeasonNumber: 2, Monitored: false},
			{SeasonNumber: 3, Monitored: false},
		},
	})
	stub.on("PUT /api/v3/series/700", 202, map[string]any{"id": 700})
	stub.on("POST /api/v3/command", 201, map[string]any{"id": 1})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:season:95015:2"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}

	var added sonarrSeries
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v3/series")[0].Body, &added)
	if added.AddOptions == nil || added.AddOptions.Monitor != "none" {
		t.Errorf("addOptions = %+v, want monitor none", added.AddOptions)
	}
	if added.AddOptions.SearchForMissingEpisodes {
		t.Error("the add fired a search for every missing episode of the show")
	}
	// The lookup copy arrives with most seasons already true; left alone, a
	// "season 2 only" request quietly monitors every season.
	for _, s := range added.Seasons {
		if s.Monitored {
			t.Errorf("season %d was monitored by the add payload", s.SeasonNumber)
		}
	}

	puts := stub.callsTo("PUT", "/api/v3/series/700")
	if len(puts) != 1 {
		t.Fatalf("expected one season-monitor update, got %d", len(puts))
	}
	var updated sonarrSeries
	_ = json.Unmarshal(puts[0].Body, &updated)
	for _, s := range updated.Seasons {
		if s.SeasonNumber == 2 && !s.Monitored {
			t.Error("season 2 was not monitored, so nothing will be grabbed")
		}
		if s.SeasonNumber != 2 && s.Monitored {
			t.Errorf("season %d was monitored by a season-2 request", s.SeasonNumber)
		}
	}

	cmds := stub.callsTo("POST", "/api/v3/command")
	if len(cmds) != 1 {
		t.Fatalf("expected one search command, got %d", len(cmds))
	}
	var cmd arrCommand
	_ = json.Unmarshal(cmds[0].Body, &cmd)
	if cmd.Name != "SeasonSearch" || cmd.SeriesID != 700 || cmd.SeasonNum == nil || *cmd.SeasonNum != 2 {
		t.Errorf("command = %+v, want a SeasonSearch for season 2", cmd)
	}
}

// Season 0 is Sonarr's specials, and it is a real season number. A request for
// it must not be mistaken for "no season given" and widened to the whole show.
func TestSonarrSeasonZeroIsARealRequest(t *testing.T) {
	stub, p := sonarrStub(t)
	s := testSeries()
	s.Seasons = append([]sonarrSeason{{SeasonNumber: 0, Monitored: false}}, s.Seasons...)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{s})
	stub.on("PUT /api/v3/series/17", 202, map[string]any{"id": 17})
	stub.on("POST /api/v3/command", 201, map[string]any{"id": 1})

	if _, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:season:95015:0"}, RequestOptions{Monitor: true}); err != nil {
		t.Fatal(err)
	}
	var cmd arrCommand
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v3/command")[0].Body, &cmd)
	if cmd.Name != "SeasonSearch" {
		t.Fatalf("command = %+v; a specials request became %q", cmd, cmd.Name)
	}
	if cmd.SeasonNum == nil || *cmd.SeasonNum != 0 {
		t.Errorf("seasonNumber = %v, want 0", cmd.SeasonNum)
	}
}

func TestSonarrEpisodeRequestMonitorsAndSearchesOnlyThatEpisode(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{testSeries()})
	stub.on("GET /api/v3/episode", 200, []sonarrEpisode{
		{ID: 2116, SeriesID: 17, SeasonNumber: 2, EpisodeNumber: 1, Monitored: false},
		{ID: 2117, SeriesID: 17, SeasonNumber: 2, EpisodeNumber: 2, Monitored: false},
	})
	stub.on("PUT /api/v3/episode/monitor", 202, map[string]any{})
	stub.on("POST /api/v3/command", 201, map[string]any{"id": 1})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:episode:95015:2:1"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}

	mons := stub.callsTo("PUT", "/api/v3/episode/monitor")
	if len(mons) != 1 {
		t.Fatalf("expected one episode-monitor call, got %d", len(mons))
	}
	var mon sonarrEpisodeMonitor
	_ = json.Unmarshal(mons[0].Body, &mon)
	if len(mon.EpisodeIDs) != 1 || mon.EpisodeIDs[0] != 2116 || !mon.Monitored {
		t.Errorf("monitor payload = %+v", mon)
	}

	var cmd arrCommand
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v3/command")[0].Body, &cmd)
	if cmd.Name != "EpisodeSearch" || len(cmd.EpisodeIDs) != 1 || cmd.EpisodeIDs[0] != 2116 {
		t.Errorf("command = %+v, want an EpisodeSearch for 2116 alone", cmd)
	}
	// The series row must not be touched: switching the show to monitored
	// would start pulling every future episode nobody asked for.
	if n := len(stub.callsTo("PUT", "/api/v3/series/17")); n != 0 {
		t.Errorf("an episode request rewrote the series row %d times", n)
	}
}

func TestSonarrEpisodeRequestForSomethingAlreadyHeldSearchesNothing(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{testSeries()})
	stub.on("GET /api/v3/episode", 200, []sonarrEpisode{
		{ID: 2117, SeriesID: 17, SeasonNumber: 2, EpisodeNumber: 2, HasFile: true},
	})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:episode:95015:2:2"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || !strings.Contains(res.Detail, "already") {
		t.Errorf("result = %+v", res)
	}
	if n := len(stub.callsTo("POST", "/api/v3/command")); n != 0 {
		t.Errorf("it searched for an episode already on disk (%d commands)", n)
	}
}

// A fully-downloaded show must not trigger a SeriesSearch. That is a sweep
// across every configured indexer for no benefit, and telling the user their
// own show has been "requested" is worse than saying nothing.
func TestSonarrRequestForACompleteShowSearchesNothing(t *testing.T) {
	stub, p := sonarrStub(t)
	s := testSeries()
	s.Statistics = &sonarrSeasonStats{EpisodeFileCount: 16, EpisodeCount: 16, TotalEpisodeCount: 16}
	stub.on("GET /api/v3/series", 200, []sonarrSeries{s})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:series:95015"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || !strings.Contains(res.Detail, "already") {
		t.Errorf("result = %+v", res)
	}
	if n := len(stub.callsTo("POST", "/api/v3/command")); n != 0 {
		t.Errorf("it swept the indexers for a show already on disk (%d commands)", n)
	}

	// A complete season is the same answer one level down...
	res, err = p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:season:95015:1"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || !strings.Contains(res.Detail, "already") {
		t.Errorf("complete season: result = %+v", res)
	}

	// ...but a season with nothing in it is still a real request.
	stub.on("PUT /api/v3/series/17", 202, map[string]any{"id": 17})
	stub.on("POST /api/v3/command", 201, map[string]any{"id": 1})
	res, err = p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:season:95015:2"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || strings.Contains(res.Detail, "already") {
		t.Errorf("empty season: result = %+v", res)
	}
	if n := len(stub.callsTo("POST", "/api/v3/command")); n != 1 {
		t.Errorf("an empty season fired %d searches, want 1", n)
	}
}

func TestSonarrRequestWithoutMonitorDownloadsNothing(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{})
	stub.on("GET /api/v3/series/lookup", 200, []sonarrSeries{
		{ID: 0, Title: "Firefly", TvdbID: 78874,
			Seasons: []sonarrSeason{{SeasonNumber: 1, Monitored: true}}},
	})
	stub.on("POST /api/v3/series", 201, sonarrSeries{ID: 700, Title: "Firefly", TvdbID: 78874})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:series:78874"}, RequestOptions{Monitor: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}
	var sent sonarrSeries
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v3/series")[0].Body, &sent)
	if sent.Monitored || sent.AddOptions.Monitor != "none" || sent.AddOptions.SearchForMissingEpisodes {
		t.Errorf("a watchlist add was monitored: %+v %+v", sent.Monitored, sent.AddOptions)
	}
	if n := len(stub.callsTo("POST", "/api/v3/command")); n != 0 {
		t.Errorf("a watchlist add fired %d commands", n)
	}
}

// An anime instance files new shows its own way even when the metadata says
// standard. That is the whole reason a second instance exists.
func TestSonarrAnimeInstanceForcesItsSeriesType(t *testing.T) {
	stub := newArrStub(t)
	stub.status("v3", "Sonarr", "4.0.19.2979")
	stub.on("GET /api/v3/series", 200, []sonarrSeries{})
	stub.on("GET /api/v3/series/lookup", 200, []sonarrSeries{
		{ID: 0, Title: "Frieren", TvdbID: 424536, SeriesType: "standard"},
	})
	stub.on("GET /api/v3/rootfolder", 200, []arrRootFolder{{ID: 4, Path: "/mnt/usb2/Anime", Accessible: true}})
	stub.on("GET /api/v3/qualityprofile", 200, []arrProfile{{ID: 3, Name: "HD-720p"}})
	stub.on("POST /api/v3/series", 201, sonarrSeries{ID: 800, Title: "Frieren", TvdbID: 424536})
	stub.on("POST /api/v3/command", 201, map[string]any{"id": 1})

	cfg := stub.config()
	cfg.ID, cfg.Name, cfg.SeriesType = "sonarr-anime", "Sonarr Anime", "anime"
	p := newSonarrProvider(cfg)

	if _, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:series:424536"}, RequestOptions{Monitor: true}); err != nil {
		t.Fatal(err)
	}
	var sent sonarrSeries
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v3/series")[0].Body, &sent)
	if sent.SeriesType != "anime" {
		t.Errorf("seriesType = %q; the anime instance filed it as standard", sent.SeriesType)
	}
	if sent.RootFolderPath != "/mnt/usb2/Anime" {
		t.Errorf("rootFolderPath = %q", sent.RootFolderPath)
	}
}

func TestSonarrActivityNamesTheEpisode(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/queue", 200, sonarrQueuePage{Records: []sonarrQueueRecord{
		{
			arrQueueRecord: arrQueueRecord{
				SeriesID: 17, EpisodeID: 2165, SeasonNumber: 3,
				Title:  "The Cleveland Show S03E10 720p DSNP WEB-DL",
				Status: "downloading", TrackedDownloadState: "downloading",
				Size: 1000, SizeLeft: 400,
			},
			Series:  &sonarrSeries{Title: "The Cleveland Show", TvdbID: 95015},
			Episode: &sonarrEpisode{SeasonNumber: 3, EpisodeNumber: 10, Title: "Dancing with the Stools"},
		},
	}})

	items, err := p.Activity(arrTestCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d rows", len(items))
	}
	a := items[0]
	if !strings.Contains(a.Title, "S03E10") || !strings.Contains(a.Title, "Dancing with the Stools") {
		t.Errorf("title = %q; a scene filename is not what a person recognises", a.Title)
	}
	if a.CanonicalID != "tvdb:episode:95015:3:10" {
		t.Errorf("canonicalId = %q", a.CanonicalID)
	}
	if a.Progress != 0.6 {
		t.Errorf("progress = %v", a.Progress)
	}
}

func TestSonarrIgnoresMusicQueries(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series/lookup", 200, []sonarrSeries{{ID: 1, Title: "no", TvdbID: 1}})

	items, err := p.Search(arrTestCtx(t), "radiohead", "music")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("a music query got %d television results", len(items))
	}
}

func TestSonarrRefusesAnIDItCannotAct(t *testing.T) {
	_, p := sonarrStub(t)
	if _, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tmdb:movie:78"}, RequestOptions{Monitor: true}); err == nil {
		t.Error("Sonarr accepted a request for a film")
	}
}

// A season the show does not have is a rejection with a reason, not a silent
// success that never downloads anything.
func TestSonarrRejectsASeasonThatDoesNotExist(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{testSeries()})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tvdb:season:95015:99"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted {
		t.Error("a request for a nonexistent season was accepted")
	}
	if !strings.Contains(res.Detail, "no season 99") {
		t.Errorf("detail = %q", res.Detail)
	}
}

func TestSonarrDetailsRefusesAnEpisodeOfAShowItDoesNotHave(t *testing.T) {
	stub, p := sonarrStub(t)
	stub.on("GET /api/v3/series", 200, []sonarrSeries{})
	stub.onFunc("GET /api/v3/series/lookup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]sonarrSeries{{ID: 0, Title: "Firefly", TvdbID: 78874}})
	})

	// A series is describable without owning it -- that is the screen you would
	// request it from.
	if _, _, err := p.Details(arrTestCtx(t), "tvdb:series:78874"); err != nil {
		t.Errorf("describing an unowned series failed: %v", err)
	}
	// An episode is not: no episode rows exist until the show is added.
	if _, _, err := p.Details(arrTestCtx(t), "tvdb:episode:78874:1:1"); err == nil {
		t.Error("it described an episode of a show Sonarr has never seen")
	}
}
