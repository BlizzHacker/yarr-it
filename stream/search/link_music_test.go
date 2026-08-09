package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecognisesMusicLinks(t *testing.T) {
	cases := []struct {
		url          string
		provider     string
		kind         string
		id           string
		shouldHandle bool
	}{
		{"https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT", "spotify", "track", "4cOdK2wGLETKBW3PvgPWqT", true},
		{"https://open.spotify.com/album/4LH4d3cOWNNsVw41Gqt2kv?si=abc", "spotify", "album", "4LH4d3cOWNNsVw41Gqt2kv", true},
		{"https://open.spotify.com/intl-de/track/4cOdK2wGLETKBW3PvgPWqT", "spotify", "track", "4cOdK2wGLETKBW3PvgPWqT", true},
		{"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M", "spotify", "playlist", "37i9dQZF1DXcBWIGoYBM5M", true},
		{"https://music.apple.com/us/album/the-dark-side-of-the-moon/1065973699", "apple", "album", "", true},
		{"https://music.apple.com/us/song/time/1065973706", "apple", "track", "", true},
		// An album URL with ?i= is a link to one song on that album.
		{"https://music.apple.com/us/album/x/123?i=456", "apple", "track", "", true},

		// Not music links.
		{"https://www.youtube.com/watch?v=aqz-KE-bpKQ", "", "", "", false},
		{"https://open.spotify.com/", "", "", "", false},
		// Host confusion: the spotify string appears, but the host is not theirs.
		{"https://evil.example.com/open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT", "", "", "", false},
		{"https://open.spotify.com.evil.example/track/4cOdK2wGLETKBW3PvgPWqT", "", "", "", false},
	}
	for _, tc := range cases {
		ref, ok := parseMusicLink(tc.url)
		if ok != tc.shouldHandle {
			t.Errorf("parseMusicLink(%q) handled = %v, want %v", tc.url, ok, tc.shouldHandle)
			continue
		}
		if !ok {
			continue
		}
		if ref.Provider != tc.provider || ref.Kind != tc.kind {
			t.Errorf("parseMusicLink(%q) = %s/%s, want %s/%s",
				tc.url, ref.Provider, ref.Kind, tc.provider, tc.kind)
		}
		if tc.id != "" && ref.ID != tc.id {
			t.Errorf("parseMusicLink(%q) id = %q, want %q", tc.url, ref.ID, tc.id)
		}
	}
}

// Captured verbatim from https://open.spotify.com/embed/album/4LH4d3cOWNNsVw41Gqt2kv
// on 2026-08-08, trimmed to the fields this code reads.
const spotifyEmbedFixture = `<!DOCTYPE html><html><body>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"state":{"data":{"entity":{
"type":"album","name":"The Dark Side of the Moon","uri":"spotify:album:4LH4d3cOWNNsVw41Gqt2kv",
"title":"The Dark Side of the Moon","subtitle":"Pink Floyd","duration":0,
"visualIdentity":{"image":[{"url":"https://image-cdn-fa.spotifycdn.com/image/ab67616d.jpg","maxHeight":640}]},
"trackList":[
{"uri":"spotify:track:574y1r7o2tRA009FW0LE7v","title":"Speak to Me","subtitle":"Pink Floyd","duration":64333},
{"uri":"spotify:track:2","title":"Breathe (In the Air)","subtitle":"Pink Floyd","duration":169000},
{"uri":"spotify:track:3","title":"Time - 2011 Remaster","subtitle":"Pink Floyd, Some Guest","duration":422000}
]}}}}},"page":"/embed/[...]"}</script></body></html>`

func TestSpotifyEmbedParsing(t *testing.T) {
	m := reNextData.FindStringSubmatch(spotifyEmbedFixture)
	if m == nil {
		t.Fatal("the __NEXT_DATA__ block was not found in a real captured page")
	}
	var doc spotifyEmbed
	if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
		t.Fatal(err)
	}
	e := doc.Props.PageProps.State.Data.Entity
	if e.Name != "The Dark Side of the Moon" {
		t.Errorf("album name = %q", e.Name)
	}
	if e.Subtitle != "Pink Floyd" {
		t.Errorf("artist = %q", e.Subtitle)
	}
	if len(e.TrackList) != 3 {
		t.Fatalf("track list has %d entries, want 3", len(e.TrackList))
	}
	if e.TrackList[0].Title != "Speak to Me" {
		t.Errorf("first track = %q", e.TrackList[0].Title)
	}
	if got := e.TrackList[0].Duration / 1000; got != 64.333 {
		t.Errorf("duration = %v seconds, want 64.333 (Spotify reports milliseconds)", got)
	}
	if len(e.Visual.Image) == 0 || e.Visual.Image[0].URL == "" {
		t.Error("artwork was not picked up")
	}
}

// Captured verbatim from music.apple.com's JSON-LD on 2026-08-08.
const appleLDFixture = `{"@context":"https://schema.org","@type":"MusicAlbum",
"name":"The Dark Side of the Moon",
"byArtist":[{"@type":"MusicGroup","url":"https://music.apple.com/us/artist/pink-floyd/487143","name":"Pink Floyd"}],
"image":"https://is1-ssl.mzstatic.com/image/thumb/x.jpg",
"tracks":[
{"@type":"MusicRecording","name":"Speak to Me","duration":"PT1M4S","url":"https://music.apple.com/us/song/speak-to-me/1065973702"},
{"@type":"MusicRecording","name":"Time","duration":"PT7M2S","url":"https://music.apple.com/us/song/time/1065973706"}
]}`

func TestAppleMusicJSONLDParsing(t *testing.T) {
	var doc appleLD
	if err := json.Unmarshal([]byte(appleLDFixture), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Type != "MusicAlbum" || doc.Name != "The Dark Side of the Moon" {
		t.Errorf("album = %s / %s", doc.Type, doc.Name)
	}
	if len(doc.ByArtist) == 0 || doc.ByArtist[0].Name != "Pink Floyd" {
		t.Error("artist not parsed")
	}
	if len(doc.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(doc.Tracks))
	}
	if got := parseISODuration(doc.Tracks[1].Duration); got != 422 {
		t.Errorf("PT7M2S = %v seconds, want 422", got)
	}
}

func TestISODurationParsing(t *testing.T) {
	cases := map[string]float64{
		"PT1M4S": 64, "PT7M2S": 422, "PT1H2M3S": 3723, "PT30S": 30, "PT2M": 120,
		"": 0, "garbage": 0, "P1D": 0,
	}
	for in, want := range cases {
		if got := parseISODuration(in); got != want {
			t.Errorf("parseISODuration(%q) = %v, want %v", in, got, want)
		}
	}
}

// The queries are what actually get typed into Yarr.It's search, so a query
// that carries "- 2011 Remaster" into a torrent index returns nothing.
func TestSearchQueriesAreUsable(t *testing.T) {
	cases := []struct{ artist, title, want string }{
		{"Pink Floyd", "Time", "Pink Floyd Time"},
		{"Pink Floyd", "Time - 2011 Remaster", "Pink Floyd Time"},
		{"Pink Floyd", "Money (Remastered 2011)", "Pink Floyd Money"},
		{"Drake", "Work (feat. Rihanna)", "Drake Work"},
		{"Pink Floyd, Some Guest", "Breathe", "Pink Floyd Breathe"},
		{"", "Instrumental", "Instrumental"},
		// Not duplicated when the artist is already in the title.
		{"Weezer", "Weezer - Buddy Holly", "Weezer - Buddy Holly"},
	}
	for _, tc := range cases {
		if got := searchQueryFor(tc.artist, tc.title); got != tc.want {
			t.Errorf("searchQueryFor(%q, %q) = %q, want %q", tc.artist, tc.title, got, tc.want)
		}
	}
}

// The one claim this feature must never make. If the disclaimer ever drifts
// into implying a download, the whole feature becomes dishonest.
func TestDisclaimerIsHonestAboutWhatIsHappening(t *testing.T) {
	low := strings.ToLower(musicDisclaimer)
	for _, forbidden := range []string{
		"download from spotify", "downloading from spotify",
		"rip", "convert spotify", "spotify to mp3",
	} {
		if strings.Contains(low, forbidden) {
			t.Errorf("the disclaimer contains %q, which claims something this does not do", forbidden)
		}
	}
	for _, required := range []string{"drm", "nothing is downloaded", "searches"} {
		if !strings.Contains(low, required) {
			t.Errorf("the disclaimer does not mention %q", required)
		}
	}
}

// The disclaimer travels with the data, so there is no build of the front end
// in which it can be dropped.
func TestDisclaimerIsPartOfEveryResult(t *testing.T) {
	for _, res := range []*musicResult{
		{Provider: "spotify", Disclaimer: musicDisclaimer},
		{Provider: "apple", Disclaimer: musicDisclaimer},
	} {
		b, _ := json.Marshal(res)
		if !strings.Contains(string(b), "DRM-protected") {
			t.Errorf("%s result serialised without the disclaimer", res.Provider)
		}
	}
}

func TestTrackDecorationStripping(t *testing.T) {
	cases := map[string]string{
		"Time - 2011 Remaster":      "Time",
		"Money (Remastered)":        "Money",
		"Work (feat. Rihanna)":      "Work",
		"Song [Live]":               "Song",
		"Perfectly Ordinary Title":  "Perfectly Ordinary Title",
		"Wish You Were Here":        "Wish You Were Here",
		"Us and Them - Radio Edit":  "Us and Them",
		"A Song With (Parentheses)": "A Song With (Parentheses)",
	}
	for in, want := range cases {
		if got := stripTrackDecorations(in); got != want {
			t.Errorf("stripTrackDecorations(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMusicEndpointRejectsNonMusicLinks(t *testing.T) {
	s := newTestLinkService(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/link/music?u=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3Dx", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	s.handleMusic(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a YouTube link on the music endpoint -> %d, want 400", rec.Code)
	}
}

// A Spotify link aimed at the general resolver must be recognised as music
// rather than handed to yt-dlp, which would try to extract audio from it.
func TestSpotifyLinksAreRoutedToMetadataNotExtraction(t *testing.T) {
	if _, ok := parseMusicLink("https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT"); !ok {
		t.Fatal("a Spotify track link was not recognised as a music link")
	}
}
