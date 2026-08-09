package main

import (
	"strings"
	"testing"
)

// The regression this whole file exists for.
//
// Facebook and Twitch return formats with NO vcodec and NO acodec at all.
// yt-dlp then derives audio_ext:"none" from that absence, which reads exactly
// like "this file is silent" -- and it is not, these are ordinary progressive
// MP4s with sound. Classifying on the missing field is how fdown.net shipped
// "some Facebook videos being downloaded as an audio file".
func TestFacebookProgressiveIsNotMistakenForAudio(t *testing.T) {
	// Verbatim shape from https://www.facebook.com/100033620354545/videos/106560053808006/
	fb := ytFormat{FormatID: "sd", Ext: "mp4", URL: "https://video.example/f.mp4", DynamicRange: "SDR"}

	hasV, hasA, known := tracks(fb)
	if !hasV {
		t.Error("a Facebook progressive mp4 was classified as having no video")
	}
	if known {
		t.Error("audio was reported as KNOWN when Facebook told us nothing about it")
	}
	if hasA {
		t.Error("audio was asserted present when nothing is known -- claim less, not more")
	}

	info := &ytInfo{Extractor: "facebook", Title: "Josef Novak on Reels", Formats: []ytFormat{fb}}
	fs := normaliseFormats(info)
	if len(fs) != 1 {
		t.Fatalf("expected the one Facebook format to survive, got %d", len(fs))
	}
	if formatClass(fs[0]) != classProbablyOK {
		t.Errorf("class = %d, want classProbablyOK; anything else sorts a good file "+
			"into the silent or audio-only bucket", formatClass(fs[0]))
	}
	if fs[0].Label != "SD" {
		t.Errorf("label = %q, want %q -- Facebook reports no dimensions, so the "+
			"format id is the only thing left that means anything", fs[0].Label, "SD")
	}
	// The default must be this format: it is the only one, and it is fine.
	if got := bestFormat(fs, true); got != 0 {
		t.Errorf("bestFormat = %d, want 0", got)
	}
}

func TestTwitchClipIsNotMistakenForAudio(t *testing.T) {
	// Verbatim shape from a real twitch:clips resolve: height present, codecs absent.
	fs := normaliseFormats(&ytInfo{
		Extractor: "twitch:clips",
		Formats: []ytFormat{
			{FormatID: "portrait-360", Ext: "mp4", Height: 360, Resolution: "360p", URL: "https://c/1.mp4"},
			{FormatID: "portrait-480", Ext: "mp4", Height: 480, Resolution: "480p", URL: "https://c/2.mp4"},
		},
	})
	if len(fs) != 2 {
		t.Fatalf("got %d formats, want 2", len(fs))
	}
	for _, f := range fs {
		if !f.HasVideo {
			t.Errorf("%s classified as having no video", f.ID)
		}
		if f.AudioKnown {
			t.Errorf("%s claims to know about audio; twitch reported nothing", f.ID)
		}
	}
	if fs[0].Height != 480 {
		t.Errorf("higher resolution should sort first, got %dp", fs[0].Height)
	}
}

// The other half of the same promise: when a codec IS explicitly "none", that
// is a real statement and must be believed and shown.
func TestExplicitlySilentVideoIsMarkedAndNeverDefault(t *testing.T) {
	info := &ytInfo{
		Extractor: "youtube", Duration: 635,
		Formats: []ytFormat{
			// YouTube DASH: the big ones are video-only.
			{FormatID: "401", Ext: "mp4", Width: 3840, Height: 2160, FPS: 60,
				VCodec: "av01.0.13M.08", ACodec: "none", Filesize: 712445280, URL: "https://y/2160"},
			{FormatID: "299", Ext: "mp4", Width: 1920, Height: 1080, FPS: 60,
				VCodec: "avc1.64002a", ACodec: "none", Filesize: 257619653, URL: "https://y/1080"},
			// The only muxed one, as is typical.
			{FormatID: "18", Ext: "mp4", Width: 640, Height: 360, FPS: 30,
				VCodec: "avc1.42001E", ACodec: "mp4a.40.2", Filesize: 40000000, URL: "https://y/360"},
			{FormatID: "140", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2",
				ABR: 129, Filesize: 10000000, URL: "https://y/audio"},
		},
	}
	fs := normaliseFormats(info)

	byID := map[string]linkFormat{}
	for _, f := range fs {
		byID[f.ID] = f
	}
	for _, id := range []string{"401", "299"} {
		f := byID[id]
		if !f.AudioKnown {
			t.Errorf("format %s: acodec was explicitly \"none\", so audio IS known", id)
		}
		if f.HasAudio {
			t.Errorf("format %s: reported as having audio when it has none", id)
		}
		if formatClass(f) != classSilent {
			t.Errorf("format %s: class %d, want classSilent", id, formatClass(f))
		}
	}

	// Silent tracks must sort last, whatever their resolution.
	if formatClass(fs[len(fs)-1]) != classSilent {
		t.Error("a silent video track did not sort to the bottom")
	}

	// The default must be the 360p muxed file, NOT the 4K silent one and NOT
	// the audio-only track.
	best := bestFormat(fs, true)
	if fs[best].ID != "18" {
		t.Fatalf("default format = %s (%s); want 18, the only one with both picture and sound",
			fs[best].ID, fs[best].Label)
	}
}

// "Nobody can click a video and receive audio." Asserted directly.
func TestDefaultForAVideoPageIsNeverAudioOnlyOrSilent(t *testing.T) {
	cases := [][]ytFormat{
		{ // audio-only listed first and largest
			{FormatID: "a", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", ABR: 320, Filesize: 99 << 20, URL: "https://x/a"},
			{FormatID: "v", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2", Filesize: 10 << 20, URL: "https://x/v"},
		},
		{ // silent 4K vs muxed 480p
			{FormatID: "big", Ext: "mp4", Height: 2160, VCodec: "av01", ACodec: "none", Filesize: 900 << 20, URL: "https://x/b"},
			{FormatID: "ok", Ext: "mp4", Height: 480, VCodec: "avc1", ACodec: "mp4a.40.2", Filesize: 20 << 20, URL: "https://x/o"},
		},
		{ // unknown-codec progressive vs silent DASH
			{FormatID: "hd", Ext: "mp4", URL: "https://x/hd"},
			{FormatID: "dash", Ext: "mp4", Height: 1080, VCodec: "avc1", ACodec: "none", URL: "https://x/d"},
		},
	}
	for i, raw := range cases {
		fs := normaliseFormats(&ytInfo{Formats: raw})
		best := bestFormat(fs, true)
		if best < 0 || best >= len(fs) {
			t.Fatalf("case %d: bestFormat returned %d", i, best)
		}
		switch formatClass(fs[best]) {
		case classAudioOnly:
			t.Errorf("case %d: a video page defaulted to an audio-only file (%s)", i, fs[best].ID)
		case classSilent:
			t.Errorf("case %d: a video page defaulted to a silent file (%s)", i, fs[best].ID)
		}
	}
}

// The reverse trap: an audio page must not default to something with a picture.
func TestAudioRequestPicksAudio(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Formats: []ytFormat{
		{FormatID: "v", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2", URL: "https://x/v"},
		{FormatID: "a", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", ABR: 128, URL: "https://x/a"},
	}})
	best := bestFormat(fs, false)
	if fs[best].ID != "a" {
		t.Errorf("audio request chose %s, want the audio-only format", fs[best].ID)
	}
}

func TestDefaultDoesNotJumpTo4KOnAPhone(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Formats: []ytFormat{
		{FormatID: "4k", Ext: "mp4", Height: 2160, VCodec: "avc1", ACodec: "mp4a.40.2", Filesize: 900 << 20, URL: "https://x/4"},
		{FormatID: "1080", Ext: "mp4", Height: 1080, VCodec: "avc1", ACodec: "mp4a.40.2", Filesize: 200 << 20, URL: "https://x/1"},
	}})
	if fs[bestFormat(fs, true)].ID != "1080" {
		t.Error("default jumped straight to 4K; 1080p is the sane ceiling for a pre-selection")
	}
}

func TestStoryboardsAndDRMAreDropped(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Formats: []ytFormat{
		{FormatID: "sb0", Ext: "mhtml", Protocol: "mhtml", VCodec: "none", ACodec: "none", URL: "https://y/sb"},
		{FormatID: "drm", Ext: "mp4", Height: 1080, VCodec: "avc1", ACodec: "mp4a.40.2", HasDRM: true, URL: "https://y/drm"},
		{FormatID: "keep", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2", URL: "https://y/k"},
		{FormatID: "nourl", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2"},
	}})
	if len(fs) != 1 || fs[0].ID != "keep" {
		ids := []string{}
		for _, f := range fs {
			ids = append(ids, f.ID)
		}
		t.Fatalf("kept %v; want only [keep] -- storyboards are thumbnails, DRM is unusable, "+
			"and a format with no URL is not a download", ids)
	}
}

// ------------------------------------------------------------- sizes ----

func TestReportedSizeIsExactAndEstimatesAreMarked(t *testing.T) {
	fs := normaliseFormats(&ytInfo{
		Duration: 600,
		Formats: []ytFormat{
			{FormatID: "exact", Ext: "mp4", Height: 1080, VCodec: "avc1", ACodec: "mp4a.40.2",
				Filesize: 257619653, URL: "https://x/1"},
			{FormatID: "approx", Ext: "mp4", Height: 720, VCodec: "avc1", ACodec: "mp4a.40.2",
				FilesizeApprox: 100000000, URL: "https://x/2"},
			{FormatID: "bitrate", Ext: "mp4", Height: 480, VCodec: "avc1", ACodec: "mp4a.40.2",
				TBR: 1000, URL: "https://x/3"},
			{FormatID: "hls", Ext: "mp4", Height: 360, VCodec: "avc1", ACodec: "mp4a.40.2",
				Protocol: "m3u8_native", URL: "https://x/4.m3u8"},
		},
	})
	got := map[string]linkFormat{}
	for _, f := range fs {
		got[f.ID] = f
	}
	if s := got["exact"].SizeHuman; strings.HasPrefix(s, "~") {
		t.Errorf("an exact filesize was rendered as an estimate: %q", s)
	}
	for _, id := range []string{"approx", "bitrate"} {
		if !got[id].BytesGuessed {
			t.Errorf("%s: a derived size was not flagged as an estimate", id)
		}
		if !strings.HasPrefix(got[id].SizeHuman, "~") {
			t.Errorf("%s: estimate rendered as %q, want a leading ~", id, got[id].SizeHuman)
		}
	}
	if !got["hls"].Streaming {
		t.Error("an m3u8 format was not marked as a stream")
	}
	if got["hls"].SizeHuman != "size unknown (stream)" {
		t.Errorf("hls size = %q; a manifest has no single size and saying one would be a lie",
			got["hls"].SizeHuman)
	}
}

func TestStreamingProtocolDetection(t *testing.T) {
	for _, p := range []string{"m3u8", "m3u8_native", "http_dash_segments", "dash", "ism", "rtmp"} {
		if !isStreamingProtocol(p) {
			t.Errorf("%s should be treated as a stream", p)
		}
	}
	for _, p := range []string{"https", "http", ""} {
		if isStreamingProtocol(p) {
			t.Errorf("%s should be treated as a plain file", p)
		}
	}
}

// ------------------------------------------------------------ labels ----

func TestCodecNamesAreHumanReadable(t *testing.T) {
	cases := map[string]string{
		"avc1.64002a": "H.264", "av01.0.09M.08": "AV1", "vp9": "VP9",
		"mp4a.40.2": "AAC", "opus": "Opus", "hvc1.1.6.L93.B0": "HEVC",
		"none": "", "": "",
	}
	for in, want := range cases {
		if got := shortCodec(in); got != want {
			t.Errorf("shortCodec(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHighFrameRateIsInTheLabel(t *testing.T) {
	fs := normaliseFormats(&ytInfo{Formats: []ytFormat{
		{FormatID: "a", Ext: "mp4", Height: 1080, FPS: 60, VCodec: "avc1", ACodec: "mp4a.40.2", URL: "https://x/1"},
		{FormatID: "b", Ext: "mp4", Height: 1080, FPS: 30, VCodec: "avc1", ACodec: "mp4a.40.2", URL: "https://x/2"},
	}})
	labels := map[string]string{}
	for _, f := range fs {
		labels[f.ID] = f.Label
	}
	if labels["a"] != "1080p60" {
		t.Errorf("60fps label = %q, want 1080p60", labels["a"])
	}
	if labels["b"] != "1080p" {
		t.Errorf("30fps label = %q, want 1080p (the common case needs no decoration)", labels["b"])
	}
}

// ---------------------------------------------------------- failures ----

// A broken extractor has to be legible. "Could not download" is what the
// competitors say and it tells nobody anything.
func TestErrorsAreClassifiedNotSwallowed(t *testing.T) {
	cases := []struct {
		stderr    string
		wantCode  string
		wantExtra string
	}{
		{"ERROR: Unsupported URL: https://example.com/x", codeUnsupported, ""},
		{"ERROR: [facebook] 107: This video is only available for registered users. Use --cookies-from-browser",
			codeLoginRequired, "facebook"},
		{"ERROR: [Instagram] ABC: There is no video in this post", codeNoVideoInPost, "Instagram"},
		{"ERROR: [youtube] xyz: Video unavailable", codeNotFound, "youtube"},
		{"ERROR: [vimeo] 1: The uploader has not made this video available in your country",
			codeGeoBlocked, "vimeo"},
		{"ERROR: [dailymotion] x: The extractor is attempting impersonation, but none of these impersonate targets are available: firefox",
			codeImpersonation, "dailymotion"},
		{"ERROR: [generic] x: HTTP Error 429: Too Many Requests", codeRateLimited, "generic"},
		{"ERROR: [facebook] 359649331226507: Cannot parse data; please report this issue",
			codeExtractorBroke, "facebook"},
	}
	for _, tc := range cases {
		got := classifyYtdlp(tc.stderr)
		if got.Code != tc.wantCode {
			t.Errorf("classify(%.50s…) = %s, want %s", tc.stderr, got.Code, tc.wantCode)
		}
		if tc.wantExtra != "" && got.Extractor != tc.wantExtra {
			t.Errorf("extractor for %.40s… = %q, want %q", tc.stderr, got.Extractor, tc.wantExtra)
		}
		if got.Message == "" {
			t.Errorf("no human-readable message for %.40s…", tc.stderr)
		}
		// The raw upstream line is always carried through. When an extractor
		// breaks, that line IS the diagnosis.
		if got.Upstream == "" {
			t.Errorf("upstream detail was dropped for %.40s…", tc.stderr)
		}
	}
}

func TestUnknownFailureIsReportedAsOurBugNotTheUsers(t *testing.T) {
	got := classifyYtdlp("ERROR: [tiktok] 123: something nobody has seen before")
	if got.Code != codeExtractorBroke {
		t.Errorf("code = %s, want %s", got.Code, codeExtractorBroke)
	}
	if !strings.Contains(got.Message, "our side") {
		t.Errorf("message %q should not imply the visitor did something wrong", got.Message)
	}
}

func TestErrorLineSurvivesSurroundingChatter(t *testing.T) {
	stderr := "WARNING: something\n[debug] Loading cookies\nERROR: [twitter] 1: No video could be found in this tweet\n"
	if got := firstErrorLine(stderr); !strings.HasPrefix(got, "[twitter]") {
		t.Errorf("firstErrorLine = %q; the ERROR: line must win over surrounding noise", got)
	}
}

func TestExtractorNameParsing(t *testing.T) {
	cases := map[string]string{
		"[youtube] abc: boom":     "youtube",
		"[twitch:clips] x: boom":  "twitch",
		"[Instagram] y: boom":     "Instagram",
		"Unsupported URL: http:x": "",
	}
	for in, want := range cases {
		if got := extractorFrom(in); got != want {
			t.Errorf("extractorFrom(%q) = %q, want %q", in, got, want)
		}
	}
}
