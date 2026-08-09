package main

// The resolution engine behind /api/link/*.
//
// yt-dlp does the extraction. That is a deliberate choice over writing
// per-network scrapers: it covers ~1800 sites, it is maintained daily by
// people who do nothing else, and every network in the list breaks its own
// extractor several times a year. Re-implementing Instagram's signed-URL
// dance in Go would be a treadmill with one person on it.
//
// It runs as a *subprocess*, not through a Python API, for four reasons that
// are all about this being hostile input:
//
//   - This service is Go. Embedding CPython means cgo, a build that only works
//     where the exact interpreter is present, and a parser for adversarial
//     HTML sharing an address space with the thing holding the Prowlarr key.
//   - A subprocess has a hard kill. An extractor that wedges on a slow CDN is
//     a dead process, not a leaked goroutine.
//   - The version is one file. Pinning, rolling forward and rolling *back* are
//     all "swap the binary", which is what you want at 02:00 when a network
//     ships a change and the previous build worked.
//   - It can be sandboxed as a unit -- see ytdlpRunner.command for the env
//     stripping, and link_guard.go for the egress proxy it is confined to.
//
// The cost is process spawn, ~200ms. The network fetch it wraps is 1-8s, so
// this is not the part that is slow.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Failure codes. These exist so a broken extractor is a *diagnosable* event
// rather than an empty list: every one of them is reported to the caller,
// counted on /api/link/health, and logged with the extractor name. "It
// returned nothing" is the failure mode that wastes a weekend; "instagram
// login_required, 41 times in an hour" is a five-minute fix.
const (
	codeOK             = "ok"
	codeUnsupported    = "unsupported_site"
	codeLoginRequired  = "login_required"
	codeGeoBlocked     = "geo_blocked"
	codeNotFound       = "not_found"
	codePrivate        = "private"
	codeDRM            = "drm_protected"
	codeLive           = "live_not_supported"
	codeExtractorBroke = "extractor_broken"
	codeImpersonation  = "impersonation_unavailable"
	codeRateLimited    = "rate_limited"
	codeTimeout        = "timeout"
	codeNoFormats      = "no_formats"
	codeToolMissing    = "resolver_unavailable"
	codeBlocked        = "blocked_address"
	// codeNoVideoInPost is its own code rather than a flavour of not_found
	// because it is the single most common Instagram outcome and it means
	// something specific: the link is fine, the post is real, and it is
	// photos. Reporting "that link is gone" for a live photo post sends
	// people to check the wrong thing.
	codeNoVideoInPost = "no_video_in_post"
)

// resolveError carries both a sentence for the visitor and the machine-usable
// detail. Upstream is the raw yt-dlp stderr line, passed through rather than
// swallowed -- when an extractor breaks, that line is the entire diagnosis.
type resolveError struct {
	Code      string
	Message   string
	Extractor string
	Upstream  string
}

func (e *resolveError) Error() string {
	if e.Upstream != "" {
		return e.Code + ": " + e.Message + " (" + e.Upstream + ")"
	}
	return e.Code + ": " + e.Message
}

type ytdlpRunner struct {
	bin string // "yt-dlp", or an absolute path
	// Where this runner is allowed to reach the network: an HTTP proxy that
	// applies the SSRF guard, and (in production) a namespace with no route to
	// the LAN. Both come from the same object so they cannot drift apart.
	eg      *egress
	timeout time.Duration

	versionOnce sync.Once
	version     string
	versionErr  error
}

func newYtdlpRunner(bin string, eg *egress, timeout time.Duration) *ytdlpRunner {
	if bin == "" {
		bin = "yt-dlp"
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &ytdlpRunner{bin: bin, eg: eg, timeout: timeout}
}

// command builds the subprocess with the flags that matter for safety, and an
// environment stripped to almost nothing.
//
// --ignore-config is not cosmetic: yt-dlp reads config files from several
// locations by default and any of them could add --exec, --proxy or cookie
// flags. The env is rebuilt rather than inherited for the same reason -- an
// inherited http_proxy or YTDLP_* variable is a way to steer the fetcher
// around the guard, and PROWLARR_API_KEY has no business being visible to a
// process that parses attacker-controlled HTML.
func (y *ytdlpRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	base := []string{
		"--ignore-config",
		"--no-warnings",
		"--no-progress",
		"--no-playlist",
		"--socket-timeout", "15",
		"--retries", "2",
		"--extractor-retries", "1",
	}
	if y.eg != nil && y.eg.url != "" {
		base = append(base, "--proxy", y.eg.url)
	}
	cmd := exec.CommandContext(ctx, y.bin, append(base, args...)...)
	// A context cancel must actually end the process. WaitDelay bounds the
	// grace period so a child that ignores the kill cannot hold a goroutine
	// and a pipe open indefinitely.
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = []string{
		"PATH=" + safePath(),
		"LANG=C.UTF-8",
		"PYTHONIOENCODING=utf-8",
		// Explicitly empty so a proxy set in the unit file cannot override
		// --proxy, and so no_proxy cannot carve an exception out of it.
		"http_proxy=", "https_proxy=", "HTTP_PROXY=", "HTTPS_PROXY=", "no_proxy=", "NO_PROXY=",
	}
	if home := osTempHome(); home != "" {
		cmd.Env = append(cmd.Env, home)
	}
	return cmd
}

// Version is reported on /api/link/health. A resolver silently running a
// six-month-old yt-dlp looks exactly like a dozen simultaneously broken
// networks, so the version is surfaced where it can be checked at a glance.
func (y *ytdlpRunner) Version(ctx context.Context) (string, error) {
	y.versionOnce.Do(func() {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := y.command(c, "--version")
		out, err := cmd.Output()
		if err != nil {
			y.versionErr = err
			return
		}
		y.version = strings.TrimSpace(string(out))
	})
	return y.version, y.versionErr
}

// ---------------------------------------------------------- the info JSON --

type ytFormat struct {
	FormatID       string            `json:"format_id"`
	Ext            string            `json:"ext"`
	URL            string            `json:"url"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	FPS            float64           `json:"fps"`
	VCodec         string            `json:"vcodec"`
	ACodec         string            `json:"acodec"`
	Filesize       int64             `json:"filesize"`
	FilesizeApprox int64             `json:"filesize_approx"`
	TBR            float64           `json:"tbr"`
	ABR            float64           `json:"abr"`
	VBR            float64           `json:"vbr"`
	Protocol       string            `json:"protocol"`
	FormatNote     string            `json:"format_note"`
	Resolution     string            `json:"resolution"`
	DynamicRange   string            `json:"dynamic_range"`
	Language       string            `json:"language"`
	HasDRM         bool              `json:"has_drm"`
	HTTPHeaders    map[string]string `json:"http_headers"`
	AudioChannels  int               `json:"audio_channels"`
}

type ytInfo struct {
	Type       string     `json:"_type"`
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Uploader   string     `json:"uploader"`
	Channel    string     `json:"channel"`
	Extractor  string     `json:"extractor"`
	Duration   float64    `json:"duration"`
	Thumbnail  string     `json:"thumbnail"`
	WebpageURL string     `json:"webpage_url"`
	IsLive     bool       `json:"is_live"`
	LiveStatus string     `json:"live_status"`
	AgeLimit   int        `json:"age_limit"`
	Formats    []ytFormat `json:"formats"`
	Entries    []ytInfo   `json:"entries"`
	Ext        string     `json:"ext"`
	URL        string     `json:"url"`
	VCodec     string     `json:"vcodec"`
	ACodec     string     `json:"acodec"`
}

// probe runs the extractor and returns the parsed info document.
func (y *ytdlpRunner) probe(ctx context.Context, pageURL string) (*ytInfo, *resolveError) {
	ctx, cancel := context.WithTimeout(ctx, y.timeout)
	defer cancel()

	// "--" then the URL: a pasted string beginning with "-" must never be read
	// as a flag.
	cmd := y.command(ctx, "--dump-single-json", "--", pageURL)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return nil, &resolveError{Code: codeTimeout,
				Message: "that site took too long to answer"}
		}
		var ee *exec.Error
		if errors.As(err, &ee) {
			return nil, &resolveError{Code: codeToolMissing,
				Message:  "the link resolver is not installed on this server",
				Upstream: ee.Error()}
		}
		return nil, classifyYtdlp(stderr.String())
	}

	var info ytInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, &resolveError{Code: codeExtractorBroke,
			Message:  "the resolver returned something unreadable for that link",
			Upstream: truncate(err.Error(), 200)}
	}
	return &info, nil
}

// classifyYtdlp turns yt-dlp's stderr into a code plus a sentence a person can
// act on.
//
// The mapping is deliberately conservative: anything not recognised becomes
// extractor_broken with the raw line attached, because "we do not know" is a
// more useful thing to report than a guess that sends someone looking in the
// wrong place.
func classifyYtdlp(stderr string) *resolveError {
	line := firstErrorLine(stderr)
	low := strings.ToLower(line)
	extractor := extractorFrom(line)

	mk := func(code, msg string) *resolveError {
		return &resolveError{Code: code, Message: msg, Extractor: extractor,
			Upstream: truncate(line, 400)}
	}

	switch {
	case strings.Contains(low, "unsupported url"):
		return mk(codeUnsupported, "nothing here knows how to read that link")
	// Checked before the generic "not found" arm: the wording overlaps and
	// this is the more specific, more common answer.
	case strings.Contains(low, "no video in this post") ||
		strings.Contains(low, "there is no video"):
		return mk(codeNoVideoInPost,
			"that post is photos, not video — Instagram only serves photo posts to a "+
				"signed-in session, so an anonymous tool cannot fetch them")
	case strings.Contains(low, "not available in your country") ||
		strings.Contains(low, "available in your country") ||
		strings.Contains(low, "not available from your location") ||
		strings.Contains(low, "geo") && strings.Contains(low, "restrict"):
		return mk(codeGeoBlocked, "that video is blocked in this server's country")
	case strings.Contains(low, "sign in") || strings.Contains(low, "log in") ||
		strings.Contains(low, "login required") || strings.Contains(low, "cookies") ||
		strings.Contains(low, "authentication"):
		return mk(codeLoginRequired,
			"that network will only show this to a signed-in account, so it cannot be read anonymously")
	case strings.Contains(low, "private") || strings.Contains(low, "not authorized"):
		return mk(codePrivate, "that post is private")
	case strings.Contains(low, "does not exist") || strings.Contains(low, "not found") ||
		strings.Contains(low, "removed") || strings.Contains(low, "unavailable") ||
		strings.Contains(low, "404"):
		return mk(codeNotFound, "that link points at something that is gone")
	case strings.Contains(low, "drm"):
		return mk(codeDRM, "that stream is DRM-protected, so there is nothing to hand you")
	case strings.Contains(low, "impersonat"):
		return mk(codeImpersonation,
			"this server is missing the impersonation support that site needs — "+
				"install yt-dlp with the curl-cffi extra")
	case strings.Contains(low, "429") || strings.Contains(low, "rate") && strings.Contains(low, "limit") ||
		strings.Contains(low, "too many requests"):
		return mk(codeRateLimited, "that network is rate-limiting this server right now")
	case strings.Contains(low, "refusing non-public") ||
		strings.Contains(low, "proxy") && strings.Contains(low, "403"):
		return mk(codeBlocked, "that link resolves to an address this server will not fetch")
	case strings.Contains(low, "live event will begin") || strings.Contains(low, "is live"):
		return mk(codeLive, "that is a live stream — nothing to download until it ends")
	case line == "":
		return &resolveError{Code: codeExtractorBroke,
			Message: "the resolver failed without saying why"}
	default:
		return mk(codeExtractorBroke,
			"that site's reader is broken right now — this is a bug on our side, not yours")
	}
}

// firstErrorLine picks the ERROR: line out of stderr. yt-dlp prints debug and
// warning chatter around it and the last line is often just a URL.
func firstErrorLine(stderr string) string {
	var fallback string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "ERROR:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "ERROR:"))
		}
		if fallback == "" {
			fallback = l
		}
	}
	return fallback
}

// extractorFrom reads the "[instagram]" tag yt-dlp prefixes its errors with.
// Knowing which extractor failed is what makes a breakage greppable.
func extractorFrom(line string) string {
	if !strings.HasPrefix(line, "[") {
		return ""
	}
	if i := strings.Index(line, "]"); i > 1 {
		name := line[1:i]
		if j := strings.Index(name, ":"); j > 0 {
			name = name[:j]
		}
		return name
	}
	return ""
}

// ------------------------------------------------------------- normalise --

// linkFormat is one downloadable row. Every field the UI shows is here, and
// the honesty of the list rests on four of them being right: HasAudio,
// AudioKnown, BytesKnown and BytesEstimated.
//
// HasAudio and AudioKnown are separate on purpose, and that separation is the
// single most important thing in this file. yt-dlp reports three different
// states in two fields:
//
//	acodec: "mp4a.40.2"  -> there is an audio track
//	acodec: "none"       -> there is definitively no audio track (DASH video)
//	acodec: absent       -> nothing is known about audio
//
// Facebook and Twitch return the third. Collapsing it into "no audio" is how
// fdown.net ended up shipping "some Facebook videos being downloaded as an
// audio file" -- a complete, perfectly good progressive MP4 gets classified by
// what the metadata *lacks*. So: HasAudio=false means silent, and it is only
// ever set when AudioKnown is true.
type linkFormat struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Ext    string `json:"ext"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	FPS    int    `json:"fps,omitempty"`
	VCodec string `json:"vcodec,omitempty"`
	ACodec string `json:"acodec,omitempty"`

	HasVideo   bool `json:"hasVideo"`
	HasAudio   bool `json:"hasAudio"`
	AudioKnown bool `json:"audioKnown"` // false => "not reported", never "silent"

	Bytes        int64  `json:"bytes,omitempty"`
	BytesKnown   bool   `json:"bytesKnown"`
	BytesGuessed bool   `json:"bytesEstimated"`
	SizeHuman    string `json:"sizeHuman"`
	Protocol     string `json:"protocol"`
	Streaming    bool   `json:"streaming"` // HLS/DASH: no single file to save
	Note         string `json:"note,omitempty"`
	DynamicRange string `json:"dynamicRange,omitempty"`
	Language     string `json:"language,omitempty"`
	Channels     int    `json:"channels,omitempty"`
	Media        string `json:"media,omitempty"` // signed /api/link/media URL

	// Muxed means this track has no audio of its own but will be joined to one
	// on the way out, so what the visitor receives is complete. PairedWith
	// names the audio track, because "1080p (joined with the 129k audio)" is a
	// claim somebody should be able to check.
	Muxed      bool   `json:"muxed,omitempty"`
	PairedWith string `json:"pairedWith,omitempty"`
	// Seekable is false for anything produced on the fly: a remux is written
	// as it goes, so there is no byte range to jump to.
	Seekable bool `json:"seekable"`

	rawURL      string
	headers     map[string]string
	pairURL     string
	pairHeaders map[string]string
}

// tracks decides what a format actually contains.
//
// Precedence matters. The explicit codec fields win when present; after that
// the only trustworthy signals are the picture dimensions and the container,
// in that order. Guessing "audio" from missing metadata is the specific
// mistake this function exists to avoid, so the final fallback is video: a
// link pasted from a social network is a video far more often than not, and
// being wrong in that direction produces "we could not label this" rather than
// "here is your silent/soundless file".
func tracks(f ytFormat) (hasVideo, hasAudio, audioKnown bool) {
	vSet := f.VCodec != "" && f.VCodec != "none"
	aSet := f.ACodec != "" && f.ACodec != "none"
	vNone := f.VCodec == "none"
	aNone := f.ACodec == "none"

	switch {
	case vSet || aSet:
		// At least one codec is positively identified. Audio is "known" only
		// when the acodec field is actually present.
		return vSet, aSet, f.ACodec != ""

	// One field says "none" and the other says nothing at all. The explicit
	// "none" is still real information: it rules that track out, which leaves
	// the other one as what this format is.
	//
	// Vimeo's audio renditions are exactly this -- vcodec "none", acodec
	// absent. Reading the absence as "no audio either" dropped them from the
	// list entirely, which in turn left Vimeo's silent video tracks with
	// nothing to be paired with and no usable format on the whole page.
	case vNone && !aNone:
		return false, true, false // audio-only, codec unreported
	case aNone && !vNone:
		return true, false, true // video-only, and the silence is confirmed
	case vNone && aNone:
		return false, false, true // a storyboard or similar; dropped upstream

	case f.Height > 0 || f.Width > 0 || f.Resolution != "" || f.VBR > 0:
		return true, false, false // video, audio unreported
	case f.ABR > 0:
		return false, true, true
	}
	switch strings.ToLower(f.Ext) {
	case "m4a", "mp3", "opus", "ogg", "oga", "wav", "aac", "flac", "weba":
		return false, true, true
	case "jpg", "jpeg", "png", "webp", "gif":
		return false, false, true // handled as an image elsewhere
	}
	return true, false, false
}

// isStreamingProtocol reports whether a format is a manifest rather than a
// file. It matters twice over: there is no Content-Length to fetch for one,
// and "download" means "reassemble hundreds of segments", which is not
// something a browser <a download> can do.
func isStreamingProtocol(p string) bool {
	switch {
	case strings.HasPrefix(p, "m3u8"), strings.HasPrefix(p, "dash"),
		strings.HasPrefix(p, "http_dash"), p == "f4m", p == "ism", p == "rtmp":
		return true
	}
	return false
}

// normaliseFormats turns yt-dlp's format list into the rows the UI shows.
//
// Three things are dropped outright: storyboards (mhtml sheets of thumbnails,
// which are not media), DRM-protected entries (nothing usable can come of
// them) and formats with neither a video nor an audio track.
func normaliseFormats(info *ytInfo) []linkFormat {
	out := make([]linkFormat, 0, len(info.Formats))
	for _, f := range info.Formats {
		if f.Protocol == "mhtml" || f.Ext == "mhtml" {
			continue
		}
		if f.HasDRM {
			continue
		}
		hasV, hasA, aKnown := tracks(f)
		if !hasV && !hasA {
			continue
		}
		if f.URL == "" {
			continue
		}

		lf := linkFormat{
			ID: f.FormatID, Ext: f.Ext, Width: f.Width, Height: f.Height,
			FPS: int(math.Round(f.FPS)), VCodec: shortCodec(f.VCodec), ACodec: shortCodec(f.ACodec),
			HasVideo: hasV, HasAudio: hasA, AudioKnown: aKnown, Protocol: f.Protocol,
			Note: f.FormatNote, DynamicRange: f.DynamicRange, Language: f.Language,
			Channels:  f.AudioChannels,
			Streaming: isStreamingProtocol(f.Protocol),
			rawURL:    f.URL, headers: f.HTTPHeaders,
		}

		switch {
		case f.Filesize > 0:
			lf.Bytes, lf.BytesKnown = f.Filesize, true
		case f.FilesizeApprox > 0:
			lf.Bytes, lf.BytesKnown, lf.BytesGuessed = f.FilesizeApprox, true, true
		case f.TBR > 0 && info.Duration > 0:
			// Bitrate times duration. This is a real estimate and is labelled
			// as one -- the alternative these sites all pick is printing it
			// unqualified, which is how you get a "12 MB" button that saves
			// 400 MB.
			lf.Bytes = int64(f.TBR * 1000 / 8 * info.Duration)
			lf.BytesKnown, lf.BytesGuessed = true, true
		}
		lf.Label = formatLabel(lf, f)
		lf.SizeHuman = sizeLabel(lf)
		// A plain progressive file can be seeked with a Range request. A
		// manifest cannot, and neither can anything produced on the fly.
		lf.Seekable = !lf.Streaming
		out = append(out, lf)
	}
	sortFormats(out)
	return out
}

// shortCodec trims the long RFC 6381 strings ("avc1.64002a") to the family a
// person recognises, keeping the detail only where it distinguishes anything.
func shortCodec(c string) string {
	if c == "" || c == "none" {
		return ""
	}
	base := c
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	switch {
	case strings.HasPrefix(base, "avc1"), strings.HasPrefix(base, "h264"):
		return "H.264"
	case strings.HasPrefix(base, "hev1"), strings.HasPrefix(base, "hvc1"), strings.HasPrefix(base, "h265"):
		return "HEVC"
	case strings.HasPrefix(base, "av01"):
		return "AV1"
	case strings.HasPrefix(base, "vp9"), base == "vp09":
		return "VP9"
	case strings.HasPrefix(base, "vp8"):
		return "VP8"
	case strings.HasPrefix(base, "mp4a"):
		return "AAC"
	case strings.HasPrefix(base, "opus"):
		return "Opus"
	case strings.HasPrefix(base, "vorbis"):
		return "Vorbis"
	case strings.HasPrefix(base, "mp3"):
		return "MP3"
	case strings.HasPrefix(base, "ec-3"), strings.HasPrefix(base, "ac-3"):
		return "Dolby"
	case strings.HasPrefix(base, "flac"):
		return "FLAC"
	}
	return base
}

func formatLabel(lf linkFormat, f ytFormat) string {
	if lf.HasVideo {
		var res string
		switch {
		case lf.Height > 0:
			res = fmt.Sprintf("%dp", lf.Height)
		case f.Resolution != "":
			res = f.Resolution
		case f.FormatNote != "":
			res = f.FormatNote
		case f.FormatID != "":
			// Facebook labels its two progressive files "sd" and "hd" and
			// reports no dimensions at all. That is still more use to a person
			// than the word "video".
			res = strings.ToUpper(f.FormatID)
		default:
			res = "video"
		}
		if lf.FPS >= 50 {
			res += fmt.Sprintf("%d", lf.FPS)
		}
		return res
	}
	if f.ABR > 0 {
		return fmt.Sprintf("audio %.0fk", f.ABR)
	}
	if f.TBR > 0 {
		return fmt.Sprintf("audio %.0fk", f.TBR)
	}
	return "audio"
}

func sizeLabel(lf linkFormat) string {
	if !lf.BytesKnown || lf.Bytes <= 0 {
		if lf.Streaming {
			return "size unknown (stream)"
		}
		return "size unknown"
	}
	s := humanSize(lf.Bytes)
	if lf.BytesGuessed {
		return "~" + s
	}
	return s
}

// Format classes, in the order a visitor should meet them.
const (
	classComplete   = 0 // video + audio, both confirmed
	classProbablyOK = 1 // video, audio not reported -- almost always a muxed file
	classAudioOnly  = 2
	classSilent     = 3 // video with acodec explicitly "none"
)

// preferredHeight is the tallest format that may win the pre-selection.
// Bigger ones stay in the list, one click away.
const preferredHeight = 1080

func formatClass(f linkFormat) int {
	switch {
	case f.HasVideo && f.HasAudio:
		return classComplete
	// A silent track that will be joined to audio on the way out arrives
	// complete, so it is no longer the trap the class exists to keep away from
	// the top of the list. On Vimeo and Reddit this is the ONLY way any format
	// reaches classComplete at all.
	case f.HasVideo && f.Muxed:
		return classComplete
	case f.HasVideo && !f.AudioKnown:
		return classProbablyOK
	case !f.HasVideo && f.HasAudio:
		return classAudioOnly
	default:
		return classSilent
	}
}

// sortFormats orders rows so the first thing a visitor sees is the one that
// will actually work.
//
// Complete files lead and explicitly-silent video sinks to the bottom, because
// a video-only DASH track is the single most common way these tools hand
// somebody a file with no sound. Audio-only sits between them: it is a
// legitimate thing to want, but never what someone who pasted a video link
// meant by "download".
func sortFormats(fs []linkFormat) {
	sort.SliceStable(fs, func(i, j int) bool {
		ci, cj := formatClass(fs[i]), formatClass(fs[j])
		if ci != cj {
			return ci < cj
		}
		if fs[i].Streaming != fs[j].Streaming {
			return !fs[i].Streaming // a real file beats a manifest
		}
		if fs[i].Height != fs[j].Height {
			return fs[i].Height > fs[j].Height
		}
		if fs[i].FPS != fs[j].FPS {
			return fs[i].FPS > fs[j].FPS
		}
		return fs[i].Bytes > fs[j].Bytes
	})
}

// bestFormat picks the row that is pre-selected.
//
// The rule it enforces is the one the incumbents get wrong: someone who pasted
// a video link must never be handed audio, and must never be handed silent
// video. So the default is only ever drawn from the two classes that carry a
// picture and (as far as anything can tell) sound. If a page genuinely offers
// nothing else -- a music track, or YouTube DASH with no progressive format at
// all -- the caller is told via the notes rather than quietly given a surprise.
func bestFormat(fs []linkFormat, wantVideo bool) int {
	best, bestScore := -1, -1
	for i, f := range fs {
		c := formatClass(f)
		if wantVideo && (c == classAudioOnly || c == classSilent) {
			continue
		}
		if !wantVideo && c != classAudioOnly {
			continue
		}
		score := 0
		switch c {
		case classComplete:
			score += 4000
		case classProbablyOK:
			score += 3000
		case classAudioOnly:
			score += 2000
		}
		if !f.Streaming {
			score += 500 // a real file can be saved; a manifest cannot
		}
		// Cap the resolution that wins by default at 1080p. Defaulting a phone
		// on mobile data to a 700 MB 4K file is a hostile default, and the
		// bigger rows are one click away. Anything above the cap is ranked
		// below 1080p and in ascending order, so 1440p is preferred to 2160p
		// when 1080p is not on offer at all.
		h := f.Height
		if h > preferredHeight {
			h = preferredHeight - 1 - (h-preferredHeight)/1000
		}
		score += h
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	// No fallback to index 0. Returning "the first one" when nothing satisfies
	// the rule is exactly how a video page ends up defaulting to an audio file
	// or a silent one -- a live run caught this on Vimeo and Reddit, where
	// every single video track is silent. -1 means "nothing here is safe to
	// pre-select", and the caller says so out loud instead.
	return best
}

// ------------------------------------------------------------ size probes --

// probeSizes fills in the true byte count for progressive formats whose size
// the extractor did not report.
//
// The requirement is "fetch sizes rather than guessing", and most networks
// outside YouTube report nothing at all -- Vimeo, Dailymotion, Twitch and
// Instagram all came back with zero sized formats in testing. A one-byte
// ranged GET returns Content-Range: bytes 0-0/<total>, which is the real
// number for one round trip and no payload.
//
// Ranged GET rather than HEAD because signed CDN URLs frequently 403 a HEAD
// while serving the GET perfectly. HEAD is the fallback, not the first try.
func probeSizes(ctx context.Context, fs []linkFormat, budget time.Duration, maxProbes int, proxyURL string) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	type job struct{ i int }
	jobs := make(chan job)
	client := guardedClientVia(8*time.Second, proxyURL)
	// Redirects are followed by the guarded client, but a signed CDN URL that
	// redirects is normal, so this is not capped tighter than the default 5.

	var wg sync.WaitGroup
	var mu sync.Mutex
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				mu.Lock()
				f := fs[j.i]
				mu.Unlock()
				n, ok := contentLength(ctx, client, f.rawURL, f.headers)
				if !ok {
					continue
				}
				mu.Lock()
				fs[j.i].Bytes = n
				fs[j.i].BytesKnown = true
				fs[j.i].BytesGuessed = false
				fs[j.i].SizeHuman = sizeLabel(fs[j.i])
				mu.Unlock()
			}
		}()
	}

	sent := 0
	for i := range fs {
		if sent >= maxProbes {
			break
		}
		// A manifest has no single length to fetch; probing one would return
		// the size of the playlist text and be worse than saying "unknown".
		if fs[i].Streaming {
			continue
		}
		if fs[i].BytesKnown && !fs[i].BytesGuessed {
			continue
		}
		select {
		case jobs <- job{i}:
			sent++
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
}

func contentLength(ctx context.Context, client *http.Client, rawURL string, hdr map[string]string) (int64, bool) {
	try := func(method string, extra map[string]string) (int64, bool) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return 0, false
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, false
		}
		defer func() {
			_, _ = readAndDiscard(resp)
			resp.Body.Close()
		}()
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if i := strings.LastIndex(cr, "/"); i >= 0 && i+1 < len(cr) {
				var n int64
				if _, err := fmt.Sscanf(cr[i+1:], "%d", &n); err == nil && n > 0 {
					return n, true
				}
			}
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.ContentLength > 0 {
			return resp.ContentLength, true
		}
		return 0, false
	}
	if n, ok := try(http.MethodGet, map[string]string{"Range": "bytes=0-0"}); ok {
		return n, true
	}
	return try(http.MethodHead, nil)
}

func readAndDiscard(resp *http.Response) (int64, error) {
	// Bounded: the probe asked for one byte, but a server that ignores Range
	// will start sending the file. Reading a little keeps the connection
	// reusable; reading it all would download the video.
	var buf [512]byte
	n, err := resp.Body.Read(buf[:])
	return int64(n), err
}

// stream re-runs yt-dlp to fetch one format and writes it to w.
//
// This is the fallback for CDNs that will not serve a resolved URL to anybody
// but the client that resolved it. TikTok is the reason it exists: its media
// host answers 403 to a correct request carrying the exact headers and cookie
// yt-dlp reported, while yt-dlp itself downloads the same file without
// complaint. The difference is session state and TLS fingerprinting, and Go's
// net/http cannot reproduce either -- so when a fetch is refused, the tool
// that can do it is asked to.
//
// The trade is real: this re-extracts the page (a second round trip) and the
// output is a pipe, so there is no Range support and no seeking. It is
// therefore a fallback, never the first choice.
func (y *ytdlpRunner) stream(ctx context.Context, w io.Writer, pageURL, formatID string) error {
	args := []string{"--no-part", "--quiet", "-o", "-"}
	if formatID != "" {
		args = append(args, "-f", formatID)
	}
	args = append(args, "--", pageURL)

	cmd := y.command(ctx, args...)
	cmd.Stdout = w
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s", truncate(firstErrorLine(stderr.String()), 200))
	}
	return nil
}

// safePath keeps PATH (yt-dlp has to be findable) and nothing else that could
// matter. The env stripping in command() is about secrets and proxy steering,
// not about making the binary unlocatable.
func safePath() string {
	if p := os.Getenv("PATH"); p != "" {
		return p
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

// osTempHome hands the child somewhere to put a cache directory. Without it
// yt-dlp falls back to paths that may not exist under a hardened systemd unit,
// and on Windows the runtime needs SystemRoot to resolve at all.
func osTempHome() string {
	if r := os.Getenv("SystemRoot"); r != "" {
		return "SystemRoot=" + r
	}
	if h := os.Getenv("HOME"); h != "" {
		return "HOME=" + h
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
