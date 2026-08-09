package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubMusicItem serves one metadata document. The committed suite must never
// call the real archive.org.
func stubMusicItem(t *testing.T, body string) *musicReader {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	r := newMusicReader(nil)
	r.metadataBase = srv.URL + "/metadata/"
	return r
}

// The Barton Hall show, trimmed to what this decision turns on. Everything in
// here is verbatim from the live item: the format names, the two shapes of
// `length`, the fact that the Ogg derivative carries no title, and the
// `stream_only` collection.
const bartonHall = `{
  "metadata": {
    "identifier": "gd77-05-08.sbd.hicks.4982.sbeok.shnf",
    "title": "Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08",
    "creator": "Grateful Dead",
    "mediatype": "etree",
    "date": "1977-05-08",
    "year": "1977",
    "venue": "Barton Hall, Cornell University",
    "coverage": "Ithaca, NY",
    "collection": ["GratefulDead", "etree", "stream_only"],
    "access-restricted-item": "true"
  },
  "files": [
    {"name": "__ia_thumb.jpg", "format": "Item Tile", "size": "2601"},
    {"name": "gd1977-05-08-d1.md5", "format": "Checksums", "size": "549"},
    {"name": "gd77-05-08eaton-d1t01.mp3", "format": "VBR MP3", "title": "Minglewood Blues",
     "track": "1", "length": "06:21", "album": "1977-05-08 - Barton Hall, Cornell University",
     "creator": "Grateful Dead", "size": "8838656"},
    {"name": "gd77-05-08eaton-d1t01.ogg", "format": "Ogg Vorbis", "length": "381.1",
     "size": "4267995"},
    {"name": "gd77-05-08eaton-d1t02.mp3", "format": "VBR MP3", "title": "Loser",
     "track": "2", "length": "08:52", "album": "1977-05-08 - Barton Hall, Cornell University",
     "creator": "Grateful Dead", "size": "11903488"},
    {"name": "gd77-05-08eaton-d1t02.ogg", "format": "Ogg Vorbis", "length": "532.04"},
    {"name": "gd77-05-08eaton-d1t01.shn", "format": "Shorten", "size": "40000000"}
  ]
}`

// The whole point of this endpoint. A card that says "Grateful Dead Live at
// Barton Hall" with no way to see or play a track is a poster.
func TestAConcertOpensAsItsTracks(t *testing.T) {
	r := stubMusicItem(t, bartonHall)
	got := r.Resolve(context.Background(), "gd77-05-08.sbd.hicks.4982.sbeok.shnf")

	if got.Reason != "" {
		t.Fatalf("refused: %s", got.Reason)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("%d tracks, want 2 -- the derivatives of one recording are one "+
			"track, not three", len(got.Tracks))
	}
	if got.Tracks[0].Title != "Minglewood Blues" || got.Tracks[1].Title != "Loser" {
		t.Errorf("tracks %q, %q", got.Tracks[0].Title, got.Tracks[1].Title)
	}
	// mm:ss, which is what the newer derivatives carry.
	if got.Tracks[0].DurationSeconds != 381 {
		t.Errorf("duration %d, want 381 (06:21)", got.Tracks[0].DurationSeconds)
	}
	if got.TotalSeconds != 381+532 {
		t.Errorf("total %d, want %d", got.TotalSeconds, 381+532)
	}
	if got.Form != "concert" {
		t.Errorf("form %q", got.Form)
	}
	if got.Artist != "Grateful Dead" || got.Venue != "Barton Hall, Cornell University" {
		t.Errorf("artist %q venue %q", got.Artist, got.Venue)
	}
	if got.Date != "1977-05-08" || got.Year != 1977 {
		t.Errorf("date %q year %d", got.Date, got.Year)
	}
	// The album the FILES agree on, which is better than the item title for the
	// line above a track list.
	if got.Album != "1977-05-08 - Barton Hall, Cornell University" {
		t.Errorf("album %q", got.Album)
	}
	if got.Details == "" {
		t.Error("no link back to the Archive's own page")
	}
}

// MP3 is not the best encode in the item; it is the only one every target this
// service has agrees on. Ogg is absent from Safari and every iOS browser.
func TestTheMP3DerivativeIsWhatPlays(t *testing.T) {
	r := stubMusicItem(t, bartonHall)
	got := r.Resolve(context.Background(), "gd77-05-08.sbd.hicks.4982.sbeok.shnf")
	for _, tr := range got.Tracks {
		if !strings.HasSuffix(tr.URL, ".mp3") {
			t.Errorf("track %q plays %q, which is not the format every client agrees on",
				tr.Title, tr.URL)
		}
		if tr.MimeType != "audio/mpeg" {
			t.Errorf("track %q declared %q", tr.Title, tr.MimeType)
		}
		// A Shorten file is 40MB of lossless nothing a browser can do. Checked on
		// the file name and not the URL: this item's own identifier ends in
		// `.shnf`, so a substring test against the URL passes for every track.
		if strings.HasSuffix(strings.ToLower(tr.File), ".shn") {
			t.Errorf("a Shorten file reached a player: %q", tr.File)
		}
	}
}

// The Archive's own marker means "listen, do not take a copy". It decides
// whether a download may be OFFERED and nothing else -- refusing to play would
// honour nothing, because their own player fetches the same bytes.
func TestStreamOnlyStopsTheDownloadAndNotTheMusic(t *testing.T) {
	r := stubMusicItem(t, bartonHall)
	got := r.Resolve(context.Background(), "gd77-05-08.sbd.hicks.4982.sbeok.shnf")
	if got.Downloadable {
		t.Error("a stream-only item was published as downloadable")
	}
	if len(got.Tracks) == 0 {
		t.Fatal("a stream-only item was refused outright; that honours nothing " +
			"and just moves the same act onto their page")
	}
	for _, tr := range got.Tracks {
		if tr.URL == "" {
			t.Errorf("track %q has no URL, so it cannot play at all", tr.Title)
		}
	}
}

// The netlabel shape: `track` is "1/15", the artist is per track because a
// compilation is a different artist every track, and the item is not restricted.
const netlabelRelease = `{
  "metadata": {
    "identifier": "NS050", "title": "Another Day, Another Way",
    "creator": "No-Source Netlabel", "mediatype": "audio", "year": "2012",
    "licenseurl": "http://creativecommons.org/licenses/by-nc-sa/4.0/",
    "collection": ["no-source", "netlabels"]
  },
  "files": [
    {"name": "01-NS050-Multi-Panel_Christmas-With-Mr-Rice.mp3", "format": "VBR MP3",
     "title": "Christmas with Mr. Rice", "track": "1/15", "length": "240.9",
     "album": "Another Day, Another Way", "creator": "Multi-Panel", "size": "3854504"},
    {"name": "02-NS050-Full-Source_Prior-To-alt-ver.mp3", "format": "VBR MP3",
     "title": "Prior To (alt. ver.)", "track": "2/15", "length": "167.97",
     "album": "Another Day, Another Way", "creator": "Full-Source", "size": "4031288"}
  ]
}`

func TestACompilationKeepsItsPerTrackArtists(t *testing.T) {
	r := stubMusicItem(t, netlabelRelease)
	got := r.Resolve(context.Background(), "NS050")

	if got.Artist != "No-Source Netlabel" {
		t.Errorf("item artist %q", got.Artist)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("%d tracks", len(got.Tracks))
	}
	if got.Tracks[0].Artist != "Multi-Panel" || got.Tracks[1].Artist != "Full-Source" {
		t.Errorf("track artists %q, %q -- putting the label's name on all fifteen "+
			"would be wrong", got.Tracks[0].Artist, got.Tracks[1].Artist)
	}
	// "1/15" is a real value archive.org sends and an int field would fail the
	// decode for the whole item.
	if got.Tracks[0].Number != 1 || got.Tracks[1].Number != 2 {
		t.Errorf("track numbers %d, %d", got.Tracks[0].Number, got.Tracks[1].Number)
	}
	// Seconds as a bare float, the other shape of the same field.
	if got.Tracks[0].DurationSeconds != 240 {
		t.Errorf("duration %d, want 240", got.Tracks[0].DurationSeconds)
	}
	if !got.Downloadable {
		t.Error("an unrestricted CC release was marked non-downloadable")
	}
	if got.Licence == "" {
		t.Error("the licence claim this domain makes on somebody's behalf is not " +
			"published, so nobody can check it")
	}
	if got.Source != "Netlabels" {
		t.Errorf("source %q", got.Source)
	}
}

// An item that exists and holds nothing playable is a different answer from an
// item that does not exist, and a client that cannot tell them apart shows
// "loading" forever. Measured: 32 of Musopen's 34 items are ZIP bundles.
func TestAnItemWithNoAudioSaysSoRatherThanReturningNothing(t *testing.T) {
	r := stubMusicItem(t, `{"metadata":{"identifier":"musopen-x","title":"MUSOPEN: Coriolan",
	  "mediatype":"audio"},"files":[{"name":"x.zip","format":"ZIP","size":"1"}]}`)
	got := r.Resolve(context.Background(), "musopen-x")
	if len(got.Tracks) != 0 {
		t.Fatalf("%d tracks from a ZIP", len(got.Tracks))
	}
	if !strings.Contains(got.Reason, "nothing a browser can play") {
		t.Errorf("reason %q does not say the item is empty rather than absent", got.Reason)
	}
	if got.Details == "" {
		t.Error("even a refusal should name where the item actually is")
	}
}

func TestAMissingItemIsDistinguishedFromABrokenOne(t *testing.T) {
	// A 200 carrying `{}` is how archive.org answers for an identifier that
	// does not exist.
	absent := stubMusicItem(t, `{}`).Resolve(context.Background(), "nope")
	if !strings.Contains(absent.Reason, "no item with this identifier") {
		t.Errorf("reason %q", absent.Reason)
	}
	// A 200 carrying an error is how it answers for an item whose metadata it
	// cannot serve. That is not "no such item".
	broken := stubMusicItem(t, `{"error":"item metadata may be invalid"}`).
		Resolve(context.Background(), "Bonanza_pd")
	if !strings.Contains(broken.Reason, "did not answer") {
		t.Errorf("reason %q -- an unserviceable item is not a missing one", broken.Reason)
	}
}

// Track order is the running order. For a taped concert that is the only thing
// that makes the item make sense.
func TestTracksComeBackInOrder(t *testing.T) {
	r := stubMusicItem(t, `{"metadata":{"identifier":"x","mediatype":"audio"},"files":[
	  {"name":"c.mp3","format":"VBR MP3","title":"Third","track":"3","length":"10"},
	  {"name":"a.mp3","format":"VBR MP3","title":"First","track":"1","length":"10"},
	  {"name":"b.mp3","format":"VBR MP3","title":"Second","track":"2","length":"10"},
	  {"name":"z.mp3","format":"VBR MP3","title":"Unnumbered","length":"10"}
	]}`)
	got := r.Resolve(context.Background(), "x")
	want := []string{"First", "Second", "Third", "Unnumbered"}
	if len(got.Tracks) != len(want) {
		t.Fatalf("%d tracks", len(got.Tracks))
	}
	for i, w := range want {
		if got.Tracks[i].Title != w {
			t.Errorf("track %d is %q, want %q -- an unnumbered file is an extra, "+
				"not the opener", i, got.Tracks[i].Title, w)
		}
	}
}

// The derivatives of one recording do not carry the same metadata: on the
// Barton Hall show the MP3 has a title and the Ogg has none. Whichever file
// wins on format, the description should be the best one available.
func TestMetadataIsMergedAcrossDerivatives(t *testing.T) {
	r := stubMusicItem(t, `{"metadata":{"identifier":"x","mediatype":"audio"},"files":[
	  {"name":"t1.ogg","format":"Ogg Vorbis","title":"Morning Dew","track":"4","length":"600"},
	  {"name":"t1.mp3","format":"VBR MP3","length":"600"}
	]}`)
	got := r.Resolve(context.Background(), "x")
	if len(got.Tracks) != 1 {
		t.Fatalf("%d tracks, want 1", len(got.Tracks))
	}
	if !strings.HasSuffix(got.Tracks[0].URL, ".mp3") {
		t.Errorf("plays %q, want the MP3", got.Tracks[0].URL)
	}
	if got.Tracks[0].Title != "Morning Dew" {
		t.Errorf("title %q, want the one the Ogg carried", got.Tracks[0].Title)
	}
	if got.Tracks[0].Number != 4 {
		t.Errorf("number %d, want 4", got.Tracks[0].Number)
	}
}

// A file with no title at all still has to say something a person can read.
func TestAnUntitledFileGetsAReadableName(t *testing.T) {
	cases := map[string]string{
		"01 Maple Leaf Rag.mp3":      "Maple Leaf Rag",
		"gd77-05-08eaton-d1t01.mp3":  "gd77-05-08eaton-d1t01",
		"disc1/03. Fur Elise.mp3":    "Fur Elise",
		"02_Prior_To_alt_ver.mp3":    "Prior To alt ver",
		"Some Track Without Numbers": "Some Track Without Numbers",
	}
	for in, want := range cases {
		if got := musicTitleFromName(in); got != want {
			t.Errorf("musicTitleFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A compilation where the tracks disagree about the album has no album, rather
// than one contributor's record name over everybody else's work.
func TestAnAlbumNameNeedsUnanimity(t *testing.T) {
	if got := musicAlbumOf([]musicTrack{{Album: "A"}, {Album: "A"}, {Album: ""}}); got != "A" {
		t.Errorf("album %q -- a track that says nothing does not disagree", got)
	}
	if got := musicAlbumOf([]musicTrack{{Album: "A"}, {Album: "B"}}); got != "" {
		t.Errorf("album %q, want none", got)
	}
}

// The endpoint has to take back exactly what a card handed out, in any of the
// shapes a card might carry it.
func TestTheEndpointAcceptsEveryShapeOfIdentifier(t *testing.T) {
	r := stubMusicItem(t, netlabelRelease)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/music/item", r.handleMusicItem)

	for _, id := range []string{
		"NS050",
		"ia:NS050",
		"https://archive.org/details/NS050",
		"https://archive.org/details/NS050#music",
		"https://archive.org/download/NS050/01.mp3",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/music/item?id="+id, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("id=%s: status %d", id, rec.Code)
			continue
		}
		var got musicItem
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Errorf("id=%s: %v", id, err)
			continue
		}
		if len(got.Tracks) != 2 {
			t.Errorf("id=%s: %d tracks", id, len(got.Tracks))
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/music/item", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a missing id answered %d, want 400", rec.Code)
	}
}
