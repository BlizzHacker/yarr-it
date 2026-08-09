package main

// Joining a video-only track to its audio, on the fly.
//
// This started as optional polish and turned out to be load-bearing. Measured
// against real public links on 2026-08-08:
//
//	vimeo    22 formats, 0 with sound   (HLS renditions, audio served separately)
//	reddit   20 formats, 0 with sound   (DASH, same split)
//	youtube  33 formats, 1 with sound   (360p; everything above it is DASH)
//
// So "label the silent ones and let the user choose" -- which is what the
// brief allows and what the incumbents do -- means Vimeo and Reddit hand back
// nothing usable at all, and YouTube tops out at 360p. That is not a
// downloader, so the tracks get joined.
//
// The join is a REMUX, not a transcode: ffmpeg -c copy writes the existing
// compressed streams into a new container without re-encoding either one. It
// costs almost no CPU. It does cost bandwidth -- every byte crosses this box
// twice -- which is why it is capped, why it is only ever offered for formats
// that genuinely have no complete alternative, and why muxAvailable() turns
// the whole thing off when ffmpeg is missing rather than failing per-request.

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// muxMaxDuration bounds one remux. A stream copy of a normal clip finishes in
// about the time it takes to transfer it; anything still running after this is
// a stalled CDN or a live stream that will never end.
const muxMaxDuration = 30 * time.Minute

var (
	muxOnce  sync.Once
	muxFound bool
	muxBin   string
)

// muxAvailable reports whether ffmpeg is on the box. Checked once, at first
// use, so a server without ffmpeg simply never advertises a joined format
// instead of offering buttons that 500.
func muxAvailable() bool {
	muxOnce.Do(func() {
		for _, name := range []string{"ffmpeg", "ffmpeg.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				muxBin, muxFound = p, true
				return
			}
		}
	})
	return muxFound
}

// bestAudioFor picks the audio track to pair with a silent video track.
//
// Container agreement matters more than bitrate: pairing a VP9/webm video with
// an AAC/m4a audio stream produces a file that plays, but pairing within the
// same family avoids ffmpeg having to bitstream-filter anything and keeps the
// result playable on the widest set of devices. Language is matched where the
// video declares one, so a dubbed track does not get the wrong audio.
func bestAudioFor(video linkFormat, all []linkFormat) (linkFormat, bool) {
	var best linkFormat
	found := false
	score := func(a linkFormat) int {
		n := 0
		// Same delivery method: mixing a progressive file with an HLS manifest
		// works, but keeps two very different readers alive at once.
		if a.Streaming == video.Streaming {
			n += 400
		}
		if compatibleContainers(video.Ext, a.Ext) {
			n += 200
		}
		if video.Language != "" && a.Language == video.Language {
			n += 150
		}
		// Prefer a middling bitrate: the largest audio track on YouTube is
		// often a 256k Opus that doubles the size for no audible gain in a
		// clip somebody is about to watch once.
		switch {
		case a.Bytes > 0 && a.Bytes < 8<<20:
			n += 60
		case a.Bytes > 0:
			n += 20
		}
		return n
	}
	for _, a := range all {
		if formatClass(a) != classAudioOnly {
			continue
		}
		if !found || score(a) > score(best) {
			best, found = a, true
		}
	}
	return best, found
}

func compatibleContainers(video, audio string) bool {
	fam := func(e string) string {
		switch strings.ToLower(e) {
		case "mp4", "m4v", "m4a", "mov":
			return "mp4"
		case "webm", "weba", "opus", "ogg":
			return "webm"
		}
		return e
	}
	return fam(video) == fam(audio)
}

// attachPairing marks every silent video track that can be completed by
// joining it to an audio track, and gives it the combined size.
//
// It runs before bestFormat, because a silent track with a partner is no
// longer a trap -- it is the 1080p file the visitor came for.
func attachPairing(fs []linkFormat, enabled bool) {
	if !enabled {
		return
	}
	for i := range fs {
		if formatClass(fs[i]) != classSilent {
			continue
		}
		audio, ok := bestAudioFor(fs[i], fs)
		if !ok {
			continue
		}
		fs[i].Muxed = true
		fs[i].PairedWith = audio.ID
		fs[i].pairURL = audio.rawURL
		fs[i].pairHeaders = audio.headers
		// Produced as it goes, so there is no byte range to jump to.
		fs[i].Seekable = false
		// The joined file is both streams plus a little container overhead.
		// Reported as an estimate because the overhead is real and unknown.
		if fs[i].BytesKnown && audio.BytesKnown {
			fs[i].Bytes += audio.Bytes
			fs[i].BytesGuessed = true
			fs[i].SizeHuman = sizeLabel(fs[i])
		}
	}
	// Pairing changes what class a format belongs to, so the order has to be
	// recomputed -- a joined 1080p should now outrank a complete 360p.
	sortFormats(fs)
}

// streamMuxed pipes both tracks through ffmpeg and out to the client.
//
// ffmpeg does its own fetching, which is the same SSRF exposure yt-dlp has, so
// it gets the same answer: it is forced through the loopback egress proxy and
// its protocol list is nailed shut. -protocol_whitelist without file: is what
// stops a crafted URL turning "-i" into a local file read.
func (s *linkService) streamMuxed(w http.ResponseWriter, r *http.Request, tok *mediaToken) {
	if !muxAvailable() {
		http.Error(w, "this server cannot join audio and video (ffmpeg is not installed)",
			http.StatusServiceUnavailable)
		return
	}
	// Both halves get the address check, not just the one that happens to be
	// first in the command line.
	for _, u := range []string{tok.URL, tok.Audio} {
		if u == "" {
			continue
		}
		if _, err := resolveTarget(r.Context(), nil, u); err != nil {
			http.Error(w, errBlocked.Error(), http.StatusForbidden)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), muxMaxDuration)
	defer cancel()

	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		// file: is deliberately absent. With it, a crafted input turns "-i"
		// into a local file read.
		"-protocol_whitelist", "http,https,tcp,tls,crypto,hls,httpproxy",
	}
	if h := ffmpegHeaders(tok.Headers); h != "" {
		args = append(args, "-headers", h)
	}
	args = append(args, "-i", tok.URL)
	if tok.Audio != "" {
		if h := ffmpegHeaders(tok.AudioHeaders); h != "" {
			args = append(args, "-headers", h)
		}
		args = append(args, "-i", tok.Audio, "-map", "0:v:0", "-map", "1:a:0")
	}
	args = append(args,
		// The whole point: copy the existing streams, re-encode nothing.
		"-c", "copy",
		// Fragmented MP4, because a normal MP4 puts its index at the end and
		// therefore cannot be written to a pipe -- ffmpeg would fail with
		// "muxer does not support non seekable output".
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1",
	)

	// ffmpeg fetches both tracks itself, so it gets the same confined proxy as
	// yt-dlp -- see the http_proxy entries in cmd.Env below.
	cmd := exec.CommandContext(ctx, muxBin, args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = []string{
		"PATH=" + safePath(),
		// ffmpeg reads these for its http/https protocol handlers, which is
		// how it lands behind the same guard as everything else here.
		"http_proxy=" + s.egress.URL(),
		"https_proxy=" + s.egress.URL(),
		"no_proxy=",
	}
	if home := osTempHome(); home != "" {
		cmd.Env = append(cmd.Env, home)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "could not start the joiner", http.StatusInternalServerError)
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4 << 10}

	if err := cmd.Start(); err != nil {
		http.Error(w, "could not start the joiner", http.StatusInternalServerError)
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// A remux is produced as it goes, so there is no length to announce and no
	// way to satisfy a Range request. Saying so is better than accepting a
	// Range and silently ignoring it, which makes a browser's resume look like
	// corruption.
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("dl") == "1" && tok.Name != "" {
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+tok.Name+`"; filename*=UTF-8''`+urlEscapeFilename(tok.Name))
	}
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64<<10)
	written := int64(0)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // client went away
			}
			written += int64(n)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	if written == 0 {
		// Nothing came out: the headers are already sent, so this cannot become
		// an error response. Log it with ffmpeg's own words so the cause is
		// recoverable rather than a mystery empty file.
		log.Printf("link mux produced no output: %s", truncate(stderr.String(), 300))
	}
}

// ffmpegHeaders renders the per-format headers yt-dlp reported into the CRLF
// blob ffmpeg's -headers option expects.
//
// Newlines are stripped from values, not escaped: a header value arriving from
// a social network with a CRLF in it would otherwise inject arbitrary extra
// headers into ffmpeg's request.
func ffmpegHeaders(h map[string]string) string {
	if len(h) == 0 {
		return ""
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic, so the same input builds the same command
	var b strings.Builder
	for _, k := range keys {
		if strings.EqualFold(k, "Range") || strings.EqualFold(k, "Host") {
			continue
		}
		ck, cv := stripCTL(k), stripCTL(h[k])
		if ck == "" || cv == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\r\n", ck, cv)
	}
	return b.String()
}

func stripCTL(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// limitedWriter keeps a bounded tail of ffmpeg's stderr without letting a
// chatty failure grow without limit.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	return l.w.Write(p)
}
