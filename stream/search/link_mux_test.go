package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The shape of a real Vimeo answer: every video track silent, audio served
// separately. Captured from https://vimeo.com/channels/staffpicks/1212617765
// on 2026-08-08 -- 22 formats, not one of them with sound.
func vimeoShape() []ytFormat {
	return []ytFormat{
		{FormatID: "hls-audio-high", Ext: "mp4", VCodec: "none", ACodec: "mp4a.40.2",
			Protocol: "m3u8_native", ABR: 128, URL: "https://vimeocdn/a.m3u8"},
		{FormatID: "hls-5006", Ext: "mp4", Height: 858, VCodec: "avc1.64002A", ACodec: "none",
			Protocol: "m3u8_native", URL: "https://vimeocdn/858.m3u8"},
		{FormatID: "hls-2334", Ext: "mp4", Height: 572, VCodec: "avc1.640020", ACodec: "none",
			Protocol: "m3u8_native", URL: "https://vimeocdn/572.m3u8"},
	}
}

// The bug a live run caught and the unit tests had missed: with every video
// track silent, bestFormat fell through to "return 0" and handed back exactly
// what it was supposed to refuse.
func TestNoSafeDefaultIsReportedNotFaked(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Extractor: "vimeo", Formats: vimeoShape()})
	attachPairing(fs, false) // a server with no ffmpeg

	best := bestFormat(fs, true)
	if best >= 0 {
		t.Fatalf("bestFormat = %d (%s, class %d); with no complete format and no muxing "+
			"there is no safe default and it must say so with -1",
			best, fs[best].Label, formatClass(fs[best]))
	}
	notes := strings.Join(formatNotes(fs, best), " ")
	if !strings.Contains(notes, "join them yourself") {
		t.Errorf("the visitor was not told why there is no default: %q", notes)
	}
}

// Reddit's split is the same story with a DASH accent.
func TestRedditSplitTracksAreNotDefaultedToAudio(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Extractor: "Reddit", Formats: []ytFormat{
		{FormatID: "dash-12", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2",
			Filesize: 1 << 20, URL: "https://v.redd.it/a"},
		{FormatID: "dash-10", Ext: "mp4", Height: 1280, VCodec: "avc1.4d401f", ACodec: "none",
			Filesize: 20 << 20, URL: "https://v.redd.it/1280"},
		{FormatID: "dash-6", Ext: "mp4", Height: 392, VCodec: "avc1.4d401e", ACodec: "none",
			Filesize: 4 << 20, URL: "https://v.redd.it/392"},
	}})
	attachPairing(fs, false)
	if best := bestFormat(fs, true); best >= 0 && formatClass(fs[best]) == classAudioOnly {
		t.Fatal("a Reddit video defaulted to its audio track -- the exact failure the live run caught")
	}
}

// With pairing on, the same Vimeo answer becomes usable: the 858p track is
// joined to the audio and is now a legitimate default.
func TestPairingMakesSplitTracksComplete(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Extractor: "vimeo", Formats: vimeoShape()})
	attachPairing(fs, true)

	best := bestFormat(fs, true)
	if best < 0 {
		t.Fatal("with pairing enabled there should be a safe default")
	}
	got := fs[best]
	if !got.Muxed {
		t.Errorf("default %s is not marked as joined", got.Label)
	}
	if got.PairedWith == "" {
		t.Error("the joined format does not name the audio track it was paired with")
	}
	if got.Height != 858 {
		t.Errorf("default height = %d, want the best available (858)", got.Height)
	}
	if formatClass(got) != classComplete {
		t.Errorf("a joined track should count as complete, got class %d", formatClass(got))
	}
	if got.Seekable {
		t.Error("a joined stream is produced as it goes and cannot be seeked; it must not claim otherwise")
	}
	notes := strings.Join(formatNotes(fs, best), " ")
	if !strings.Contains(notes, "joined back together") {
		t.Errorf("the joining was not disclosed: %q", notes)
	}
}

func TestPairingPrefersMatchingContainer(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Formats: []ytFormat{
		{FormatID: "v-webm", Ext: "webm", Height: 1080, VCodec: "vp9", ACodec: "none", URL: "https://x/v"},
		{FormatID: "a-m4a", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", ABR: 128, URL: "https://x/a1"},
		{FormatID: "a-opus", Ext: "webm", VCodec: "none", ACodec: "opus", ABR: 128, URL: "https://x/a2"},
	}})
	attachPairing(fs, true)
	for _, f := range fs {
		if f.ID == "v-webm" {
			if f.PairedWith != "a-opus" {
				t.Errorf("a VP9/webm video was paired with %q; the same-family audio avoids "+
					"a container mismatch", f.PairedWith)
			}
		}
	}
}

func TestPairedSizeIsTheSumAndMarkedApproximate(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Formats: []ytFormat{
		{FormatID: "v", Ext: "mp4", Height: 1080, VCodec: "avc1", ACodec: "none",
			Filesize: 100 << 20, URL: "https://x/v"},
		{FormatID: "a", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2",
			ABR: 128, Filesize: 5 << 20, URL: "https://x/a"},
	}})
	attachPairing(fs, true)
	var video linkFormat
	for _, f := range fs {
		if f.ID == "v" {
			video = f
		}
	}
	if video.Bytes != 105<<20 {
		t.Errorf("joined size = %d, want the sum of both tracks (%d)", video.Bytes, 105<<20)
	}
	if !video.BytesGuessed || !strings.HasPrefix(video.SizeHuman, "~") {
		t.Errorf("a joined size carries container overhead nobody can predict, so it must "+
			"be shown as an estimate; got %q", video.SizeHuman)
	}
}

// Audio-only tracks are left alone: they are a legitimate thing to want and
// joining one to a picture would be inventing a file nobody asked for.
func TestPairingLeavesCompleteAndAudioFormatsAlone(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Formats: []ytFormat{
		{FormatID: "full", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2", URL: "https://x/f"},
		{FormatID: "a", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", ABR: 128, URL: "https://x/a"},
	}})
	attachPairing(fs, true)
	for _, f := range fs {
		if f.Muxed {
			t.Errorf("%s was marked for joining although it needs nothing", f.ID)
		}
	}
}

// ffmpeg's -headers takes a CRLF-delimited blob. A header value arriving from
// a social network with a newline in it would otherwise inject extra headers
// into ffmpeg's own request.
func TestFFmpegHeadersCannotBeInjected(t *testing.T) {
	got := ffmpegHeaders(map[string]string{
		"Referer":    "https://vimeo.com/\r\nX-Injected: yes",
		"User-Agent": "Mozilla/5.0",
	})
	if strings.Contains(got, "X-Injected: yes\r\n") && strings.Count(got, "\r\n") > 2 {
		t.Errorf("a newline in a header value survived into the ffmpeg command: %q", got)
	}
	if !strings.Contains(got, "X-Injected") {
		return // the value was dropped entirely, which is also fine
	}
	// If it survived, it must be flattened into the Referer value, not a line
	// of its own.
	for _, line := range strings.Split(strings.TrimSuffix(got, "\r\n"), "\r\n") {
		if strings.HasPrefix(line, "X-Injected") {
			t.Errorf("injected header became its own line: %q", line)
		}
	}
}

func TestFFmpegHeadersAreDeterministicAndSkipHopByHop(t *testing.T) {
	in := map[string]string{"User-Agent": "UA", "Referer": "R", "Range": "bytes=0-", "Host": "h"}
	a, b := ffmpegHeaders(in), ffmpegHeaders(in)
	if a != b {
		t.Error("header rendering is not deterministic")
	}
	if strings.Contains(a, "Range") || strings.Contains(a, "Host") {
		t.Errorf("a hop-by-hop header was forwarded to ffmpeg: %q", a)
	}
}

// A muxed token carries both URLs inside the signature, so neither half can
// be swapped after the fact.
func TestMuxTokenSignsBothTracks(t *testing.T) {
	s := newTestLinkService(t)
	f := linkFormat{
		rawURL: "https://cdn.example.com/v.mp4", Ext: "mp4", Label: "1080p",
		Muxed: true, pairURL: "https://cdn.example.com/a.m4a",
	}
	raw := s.mediaURL(f, &ytInfo{Title: "Clip"})
	tok, err := s.openToken(strings.TrimPrefix(raw, "/api/link/media?t="))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Audio != "https://cdn.example.com/a.m4a" {
		t.Errorf("audio track missing from the token: %q", tok.Audio)
	}
	if tok.Mime != "video/mp4" || !strings.HasSuffix(tok.Name, ".mp4") {
		t.Errorf("a joined file is always mp4; got mime %q name %q", tok.Mime, tok.Name)
	}
}

// Both halves get the address check, not just whichever one is first in the
// ffmpeg command line. A token whose *audio* half points at the LAN must be
// refused just as firmly as one whose video half does.
func TestMuxRefusesWhenEitherTrackIsPrivate(t *testing.T) {
	s := newTestLinkService(t)
	for _, tok := range []mediaToken{
		{URL: "http://192.168.0.115:9696/v.mp4", Audio: "https://cdn.example.com/a.m4a",
			Expires: farFuture()},
		{URL: "https://cdn.example.com/v.mp4", Audio: "http://127.0.0.1:8802/a.m4a",
			Expires: farFuture()},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/link/media?t="+s.signToken(tok), nil)
		req.RemoteAddr = "203.0.113.9:1234"
		s.handleMedia(rec, req)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("a join with a private half answered %d; want a refusal (video=%s audio=%s)",
				rec.Code, tok.URL, tok.Audio)
		}
	}
}

func farFuture() int64 { return time.Now().Add(time.Hour).Unix() }
