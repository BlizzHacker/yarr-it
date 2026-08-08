package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func lidarrStub(t *testing.T) (*arrStub, *lidarrProvider) {
	t.Helper()
	stub := newArrStub(t)
	stub.status("v1", "Lidarr", "3.1.0.4875")
	stub.healthEntries("v1")
	stub.on("GET /api/v1/queue", 200, lidarrQueuePage{})
	stub.on("GET /api/v1/rootfolder", 200, []arrRootFolder{
		{ID: 2, Path: "/mnt/lvm_shared/Music", Accessible: false},
		{ID: 1, Path: "/mnt/usb3/Music", Accessible: true},
	})
	stub.on("GET /api/v1/qualityprofile", 200, []arrProfile{{ID: 3, Name: "Standard"}, {ID: 1, Name: "Any"}})
	stub.on("GET /api/v1/metadataprofile", 200, []arrProfile{{ID: 2, Name: "None"}, {ID: 1, Name: "Standard"}})

	cfg := stub.config()
	cfg.ID, cfg.Name = "lidarr", "Lidarr"
	return stub, newLidarrProvider(cfg)
}

const (
	radioheadMBID = "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	kidAMBID      = "e75c0549-ad55-39e3-8025-c72c5d4a3c5d"
)

func TestLidarrIdentity(t *testing.T) {
	_, p := lidarrStub(t)
	// "music", never "audio". Cached cards may carry the other spelling and
	// canonicalDomain resolves both, but what a provider claims is checked.
	if got := p.Domains(); len(got) != 1 || got[0] != "music" {
		t.Errorf("Domains() = %v, want [music]", got)
	}
}

// "kid a" is an album and "radiohead" is an artist, and the user does not
// announce which they meant.
func TestLidarrSearchAsksBothLookups(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/artist/lookup", 200, []lidarrArtist{
		{ArtistName: "Radiohead", ForeignArtistID: radioheadMBID, ArtistType: "Group",
			Images: []arrImage{{CoverType: "poster", RemoteURL: "https://img.test/rh.jpg"}}},
	})
	stub.on("GET /api/v1/album/lookup", 200, []lidarrAlbum{
		{Title: "Kid A", ForeignAlbumID: kidAMBID, AlbumType: "Album",
			ReleaseDate: "2000-08-03T00:00:00Z",
			Artist:      &lidarrArtist{ArtistName: "Radiohead", ForeignArtistID: radioheadMBID}},
	})
	stub.on("GET /api/v1/artist", 200, []lidarrArtist{})
	stub.on("GET /api/v1/album", 200, []lidarrAlbum{})

	items, err := p.Search(arrTestCtx(t), "radiohead", "audio")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want an artist and an album", len(items))
	}
	if items[0].Type != "artist" || items[0].CanonicalID != "mbid:artist:"+radioheadMBID {
		t.Errorf("artist item = %+v", items[0])
	}
	if items[1].Type != "album" || items[1].CanonicalID != "mbid:album:"+kidAMBID {
		t.Errorf("album item = %+v", items[1])
	}
	if items[1].Subtitle != "Radiohead" {
		t.Errorf("an album card with no artist name is unreadable: subtitle = %q", items[1].Subtitle)
	}
	if items[1].Year != 2000 {
		t.Errorf("year = %d", items[1].Year)
	}
	for _, it := range items {
		if it.Domain != "music" {
			t.Errorf("%q has domain %q", it.Title, it.Domain)
		}
	}
}

// Half an answer beats none: the two lookups fail independently.
func TestLidarrSearchSurvivesOneDeadLookup(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/artist/lookup", 500, map[string]string{"error": "boom"})
	stub.on("GET /api/v1/album/lookup", 200, []lidarrAlbum{
		{Title: "Kid A", ForeignAlbumID: kidAMBID,
			Artist: &lidarrArtist{ArtistName: "Radiohead", ForeignArtistID: radioheadMBID}},
	})
	stub.on("GET /api/v1/album", 200, []lidarrAlbum{})

	items, err := p.Search(arrTestCtx(t), "kid a", "")
	if err != nil {
		t.Fatalf("one dead lookup took the whole search down: %v", err)
	}
	if len(items) != 1 || items[0].Type != "album" {
		t.Errorf("items = %+v", items)
	}
}

// Lidarr's lookup says nothing about ownership -- there is no id field at all
// -- so every leading hit is checked. Without it, an artist whose whole
// discography is on disk is offered as a fresh request.
func TestLidarrSearchResolvesOwnershipOfLookupHits(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/artist/lookup", 200, []lidarrArtist{
		{ArtistName: "System of a Down", ForeignArtistID: "cc0b7089-c08d-4c10-b6b0-873582c17fd6"},
		{ArtistName: "Nobody We Have", ForeignArtistID: "00000000-0000-0000-0000-000000000000"},
	})
	stub.on("GET /api/v1/album/lookup", 200, []lidarrAlbum{})
	stub.onFunc("GET /api/v1/artist", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("mbId") == "cc0b7089-c08d-4c10-b6b0-873582c17fd6" {
			_ = json.NewEncoder(w).Encode([]lidarrArtist{{
				ID: 1, ArtistName: "System of a Down",
				ForeignArtistID: "cc0b7089-c08d-4c10-b6b0-873582c17fd6", Monitored: true,
				Statistics: &lidarrStatistics{TrackFileCount: 69, TrackCount: 71, TotalTrackCount: 71},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode([]lidarrArtist{})
	})

	items, err := p.Search(arrTestCtx(t), "system", "music")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].State != StateAvailable {
		t.Errorf("an artist with 69 files on disk reported %q", items[0].State)
	}
	if items[1].State != StateMissing {
		t.Errorf("an artist nobody has reported %q", items[1].State)
	}
}

func TestLidarrIgnoresVideoQueries(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/artist/lookup", 200, []lidarrArtist{{ArtistName: "no", ForeignArtistID: "x"}})
	stub.on("GET /api/v1/album/lookup", 200, []lidarrAlbum{})

	items, err := p.Search(arrTestCtx(t), "blade runner", "movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("a film query got %d music results", len(items))
	}
	if n := len(stub.callsTo("GET", "/api/v1/artist/lookup")); n != 0 {
		t.Errorf("Lidarr was called %d times for a video domain", n)
	}
}

func TestLidarrDetailsWalksArtistToAlbumToTrack(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/artist", 200, []lidarrArtist{{
		ID: 1, ArtistName: "System of a Down", ForeignArtistID: "cc0b7089", Monitored: true,
		Statistics: &lidarrStatistics{TrackFileCount: 69, TotalTrackCount: 71},
	}})
	stub.onFunc("GET /api/v1/album", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("artistId") == "1" {
			_ = json.NewEncoder(w).Encode([]lidarrAlbum{
				{ID: 3, Title: "Toxicity", ForeignAlbumID: "f50fbcb4", ReleaseDate: "2001-08-27T00:00:00Z",
					Monitored: true, Statistics: &lidarrStatistics{TrackFileCount: 15, TotalTrackCount: 15}},
				{ID: 1, Title: "System of a Down", ForeignAlbumID: "97f37409", ReleaseDate: "1998-06-30T00:00:00Z",
					Monitored: true, Statistics: &lidarrStatistics{TrackFileCount: 0, TotalTrackCount: 15}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode([]lidarrAlbum{
			{ID: 3, Title: "Toxicity", ForeignAlbumID: "f50fbcb4", ReleaseDate: "2001-08-27T00:00:00Z",
				Monitored: true, Statistics: &lidarrStatistics{TrackFileCount: 15, TotalTrackCount: 15},
				Artist: &lidarrArtist{ArtistName: "System of a Down", ForeignArtistID: "cc0b7089"}},
		})
	})
	stub.on("GET /api/v1/track", 200, []lidarrTrack{
		{ID: 131, Title: "Know", AbsoluteTrackNumber: 2, MediumNumber: 1, HasFile: true, ForeignTrackID: "79861e3a"},
		{ID: 130, Title: "Prison Song", AbsoluteTrackNumber: 1, MediumNumber: 1, HasFile: false, ForeignTrackID: "a46aae44"},
	})

	item, children, err := p.Details(arrTestCtx(t), "mbid:artist:cc0b7089")
	if err != nil {
		t.Fatal(err)
	}
	if item.Type != "artist" || len(children) != 2 {
		t.Fatalf("artist item = %+v, %d children", item, len(children))
	}
	// Chronological, so a discography reads like one.
	if children[0].Title != "System of a Down" {
		t.Errorf("albums are not in release order: %q first", children[0].Title)
	}
	if children[0].State != StateRequested {
		t.Errorf("a monitored album with no files reported %q, want requested", children[0].State)
	}
	if children[1].State != StateAvailable {
		t.Errorf("a complete album reported %q", children[1].State)
	}

	item, children, err = p.Details(arrTestCtx(t), "mbid:album:f50fbcb4")
	if err != nil {
		t.Fatal(err)
	}
	if item.Type != "album" || len(children) != 2 {
		t.Fatalf("album item = %+v, %d children", item, len(children))
	}
	if children[0].Type != "track" || children[0].Title != "Prison Song" {
		t.Errorf("tracks are not in play order: %+v", children[0])
	}
	if children[0].CanonicalID != "mbid:track:a46aae44" {
		t.Errorf("track canonicalId = %q", children[0].CanonicalID)
	}
	// A track with no file on a monitored album is "requested", not "missing":
	// Lidarr is actively looking for that release, and a card that said
	// "missing" would offer a request for work already in hand.
	if children[0].State != StateRequested || children[1].State != StateAvailable {
		t.Errorf("track states = %q, %q", children[0].State, children[1].State)
	}
}

func TestLidarrLibraryStatus(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.onFunc("GET /api/v1/artist", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("mbId") == radioheadMBID {
			_ = json.NewEncoder(w).Encode([]lidarrArtist{{
				ID: 5, ArtistName: "Radiohead", ForeignArtistID: radioheadMBID, Monitored: true,
				Statistics: &lidarrStatistics{TrackFileCount: 0, TotalTrackCount: 100},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode([]lidarrArtist{})
	})
	stub.on("GET /api/v1/album", 200, []lidarrAlbum{})

	got, err := p.LibraryStatus(arrTestCtx(t), "mbid:artist:"+radioheadMBID)
	if err != nil {
		t.Fatal(err)
	}
	if got != StateRequested {
		t.Errorf("a monitored artist with no files = %q, want requested", got)
	}

	got, err = p.LibraryStatus(arrTestCtx(t), "mbid:artist:00000000")
	if err != nil {
		t.Fatal(err)
	}
	if got != StateMissing {
		t.Errorf("an artist nobody has = %q, want missing", got)
	}

	got, err = p.LibraryStatus(arrTestCtx(t), "mbid:album:"+kidAMBID)
	if err != nil {
		t.Fatal(err)
	}
	if got != StateMissing {
		t.Errorf("an album nobody has = %q, want missing", got)
	}
}

// Lidarr will not look a track up by MusicBrainz id -- there is no endpoint.
// "Missing" would be a guess dressed as a fact, and the UI would offer a
// request for a track already on disk.
func TestLidarrTrackStatusIsUnknownNotMissing(t *testing.T) {
	_, p := lidarrStub(t)
	got, err := p.LibraryStatus(arrTestCtx(t), "mbid:track:a46aae44-3441-39fb-945a-cce752349138")
	if err != nil {
		t.Fatal(err)
	}
	if got != StateUnknown {
		t.Errorf("state = %q, want unknown", got)
	}
}

func TestLidarrArtistRequestAddsWithResolvedProfiles(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/artist", 200, []lidarrArtist{})
	stub.on("GET /api/v1/artist/lookup", 200, []lidarrArtist{
		{ArtistName: "Radiohead", ForeignArtistID: radioheadMBID},
	})
	stub.on("POST /api/v1/artist", 201, lidarrArtist{ID: 900, ArtistName: "Radiohead", ForeignArtistID: radioheadMBID})
	stub.on("POST /api/v1/command", 201, map[string]any{"id": 1})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "mbid:artist:" + radioheadMBID}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}

	var sent lidarrArtist
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v1/artist")[0].Body, &sent)
	// Two of this instance's three roots point at an unmounted share; adding
	// into one produces an artist Lidarr immediately reports as broken.
	if sent.RootFolderPath != "/mnt/usb3/Music" {
		t.Errorf("rootFolderPath = %q, want the accessible one", sent.RootFolderPath)
	}
	if sent.QualityProfileID != 1 || sent.MetadataProfileID != 1 {
		t.Errorf("profiles = quality %d metadata %d, want the lowest ids",
			sent.QualityProfileID, sent.MetadataProfileID)
	}
	// Lidarr rejects an add with no metadata profile, which is the trap that
	// makes this the one adapter needing a third resolved default.
	if sent.MetadataProfileID == 0 {
		t.Error("no metadata profile was set; Lidarr rejects the add")
	}

	var cmd arrCommand
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v1/command")[0].Body, &cmd)
	if cmd.Name != "ArtistSearch" || cmd.ArtistID != 900 {
		t.Errorf("command = %+v", cmd)
	}
}

// Asking for one album must add its artist unmonitored. Adding the artist
// monitored would start a discography-wide grab nobody asked for.
func TestLidarrAlbumRequestAddsTheArtistWithoutGrabbingEverything(t *testing.T) {
	stub, p := lidarrStub(t)

	artistAdded := false
	stub.onFunc("GET /api/v1/album", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("foreignAlbumId") == kidAMBID && artistAdded {
			_ = json.NewEncoder(w).Encode([]lidarrAlbum{{
				ID: 44, Title: "Kid A", ForeignAlbumID: kidAMBID, ArtistID: 900, Monitored: false,
				Statistics: &lidarrStatistics{TrackFileCount: 0, TotalTrackCount: 10},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode([]lidarrAlbum{})
	})
	stub.on("GET /api/v1/album/lookup", 200, []lidarrAlbum{
		{Title: "Kid A", ForeignAlbumID: kidAMBID,
			Artist: &lidarrArtist{ArtistName: "Radiohead", ForeignArtistID: radioheadMBID}},
	})
	stub.on("GET /api/v1/artist", 200, []lidarrArtist{})
	stub.on("GET /api/v1/artist/lookup", 200, []lidarrArtist{
		{ArtistName: "Radiohead", ForeignArtistID: radioheadMBID},
	})
	stub.onFunc("POST /api/v1/artist", func(w http.ResponseWriter, r *http.Request) {
		artistAdded = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(lidarrArtist{ID: 900, ArtistName: "Radiohead", ForeignArtistID: radioheadMBID})
	})
	stub.on("PUT /api/v1/album/44", 202, map[string]any{"id": 44})
	stub.on("POST /api/v1/command", 201, map[string]any{"id": 1})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "mbid:album:" + kidAMBID}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}

	var sentArtist lidarrArtist
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v1/artist")[0].Body, &sentArtist)
	if sentArtist.Monitored {
		t.Error("the artist was added monitored; asking for one album would pull the discography")
	}
	if sentArtist.AddOptions == nil || sentArtist.AddOptions.SearchForMissingAlbums {
		t.Errorf("addOptions = %+v; the add started a search of its own", sentArtist.AddOptions)
	}

	var sentAlbum lidarrAlbum
	_ = json.Unmarshal(stub.callsTo("PUT", "/api/v1/album/44")[0].Body, &sentAlbum)
	if !sentAlbum.Monitored {
		t.Error("the album was not monitored, so nothing will be grabbed")
	}

	var cmd arrCommand
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v1/command")[0].Body, &cmd)
	if cmd.Name != "AlbumSearch" || len(cmd.AlbumIDs) != 1 || cmd.AlbumIDs[0] != 44 {
		t.Errorf("command = %+v, want an AlbumSearch for 44 alone", cmd)
	}
}

func TestLidarrArtistRequestForACompleteDiscographySearchesNothing(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/artist", 200, []lidarrArtist{{
		ID: 1, ArtistName: "System of a Down", ForeignArtistID: "cc0b7089", Monitored: true,
		Statistics: &lidarrStatistics{TrackFileCount: 71, TrackCount: 71, TotalTrackCount: 71},
	}})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "mbid:artist:cc0b7089"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || !strings.Contains(res.Detail, "already") {
		t.Errorf("result = %+v", res)
	}
	if n := len(stub.callsTo("POST", "/api/v1/command")); n != 0 {
		t.Errorf("it searched for a discography already on disk (%d commands)", n)
	}
}

func TestLidarrAlbumRequestForSomethingAlreadyHeldSearchesNothing(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/album", 200, []lidarrAlbum{{
		ID: 3, Title: "Toxicity", ForeignAlbumID: "f50fbcb4", Monitored: true,
		Statistics: &lidarrStatistics{TrackFileCount: 15, TotalTrackCount: 15},
	}})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "mbid:album:f50fbcb4"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || !strings.Contains(res.Detail, "already") {
		t.Errorf("result = %+v", res)
	}
	if n := len(stub.callsTo("POST", "/api/v1/command")); n != 0 {
		t.Errorf("it searched for an album already on disk (%d commands)", n)
	}
}

// Lidarr acquires releases, and a release is an album. Silently widening a
// track request to its album downloads eleven things nobody asked for.
func TestLidarrRefusesATrackRequestWithAnExplanation(t *testing.T) {
	_, p := lidarrStub(t)
	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "mbid:track:a46aae44"}, RequestOptions{Monitor: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted {
		t.Error("a track request was accepted; nothing would ever download")
	}
	if !strings.Contains(res.Detail, "album") {
		t.Errorf("detail = %q; it must say what to ask for instead", res.Detail)
	}
}

func TestLidarrRequestWithoutMonitorDownloadsNothing(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/artist", 200, []lidarrArtist{})
	stub.on("GET /api/v1/artist/lookup", 200, []lidarrArtist{
		{ArtistName: "Radiohead", ForeignArtistID: radioheadMBID},
	})
	stub.on("POST /api/v1/artist", 201, lidarrArtist{ID: 900, ArtistName: "Radiohead", ForeignArtistID: radioheadMBID})

	res, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "mbid:artist:" + radioheadMBID}, RequestOptions{Monitor: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}
	var sent lidarrArtist
	_ = json.Unmarshal(stub.callsTo("POST", "/api/v1/artist")[0].Body, &sent)
	if sent.Monitored || sent.AddOptions.Monitor != "none" {
		t.Errorf("a watchlist add was monitored: %+v", sent.AddOptions)
	}
	if n := len(stub.callsTo("POST", "/api/v1/command")); n != 0 {
		t.Errorf("a watchlist add fired %d commands", n)
	}
}

func TestLidarrActivityNamesTheAlbum(t *testing.T) {
	stub, p := lidarrStub(t)
	stub.on("GET /api/v1/queue", 200, lidarrQueuePage{Records: []lidarrQueueRecord{
		{
			arrQueueRecord: arrQueueRecord{
				ArtistID: 1, AlbumID: 3, Title: "System.Of.A.Down-Toxicity-2001-FLAC",
				Status: "completed", TrackedDownloadState: "importPending",
				Size: 400, SizeLeft: 0,
			},
			Artist: &lidarrArtist{ArtistName: "System of a Down", ForeignArtistID: "cc0b7089"},
			Album:  &lidarrAlbum{Title: "Toxicity", ForeignAlbumID: "f50fbcb4"},
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
	if a.Title != "System of a Down — Toxicity" {
		t.Errorf("title = %q", a.Title)
	}
	if a.CanonicalID != "mbid:album:f50fbcb4" || a.Domain != "music" {
		t.Errorf("row = %+v", a)
	}
	if a.Stage != "importing" {
		t.Errorf("stage = %q; importPending is jargon for 'downloaded, waiting to be filed'", a.Stage)
	}
}

func TestLidarrRefusesAnIDItCannotAct(t *testing.T) {
	_, p := lidarrStub(t)
	if _, err := p.Request(arrTestCtx(t),
		MediaItem{CanonicalID: "tmdb:movie:78"}, RequestOptions{Monitor: true}); err == nil {
		t.Error("Lidarr accepted a request for a film")
	}
}
