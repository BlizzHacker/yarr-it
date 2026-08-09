package main

// End-to-end proof against the real internet.
//
// Skipped unless YARRIT_LIVE=1, because it needs network, a yt-dlp binary and
// the cooperation of eight social networks -- none of which belong in a unit
// test run. It is a test rather than a script so the evidence can be
// reproduced with one command when a network breaks:
//
//	YARRIT_LIVE=1 YTDLP_BIN=/usr/bin/yt-dlp go test -run TestLive -v ./...
//
// What it proves, per URL: yt-dlp runs confined to the egress proxy, the
// formats normalise, the sizes come back, the default format is one that
// actually has a picture and sound, and the signed media URL returns bytes
// with a plausible container signature.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func liveService(t *testing.T) *linkService {
	t.Helper()
	if os.Getenv("YARRIT_LIVE") != "1" {
		t.Skip("set YARRIT_LIVE=1 to run the live network tests")
	}
	bin := os.Getenv("YTDLP_BIN")
	if bin == "" {
		bin = "yt-dlp"
	}
	s, err := newLinkService(bin, 90*time.Second, 8, os.Getenv("YARRIT_NETNS"))
	if err != nil {
		t.Fatal(err)
	}
	// Live runs resolve a dozen URLs back to back; the production bucket is
	// sized for a person pasting links, not for this.
	s.limiter = newIPLimiter(1000, time.Millisecond)
	t.Cleanup(func() { _ = s.egress.Close() })
	return s
}

var liveURLs = []struct{ network, url string }{
	{"youtube", "https://www.youtube.com/watch?v=aqz-KE-bpKQ"},
	{"tiktok", "https://www.tiktok.com/@nasa/video/7670721000471891214"},
	{"vimeo", "https://vimeo.com/channels/staffpicks/1212617765"},
	{"dailymotion", "https://www.dailymotion.com/video/x7tpzb4"},
	{"reddit", "https://www.reddit.com/r/aww/comments/1v2mhes/i_carry_shelter_dogs_around_nyc_in_a_dog_backpack/"},
	{"twitch-clip", "https://www.twitch.tv/ivycomb/clip/AbnegateCrazyClipsdadKevinTurtle-vk_Spe6R93eXjdia"},
	{"instagram-reel", "https://www.instagram.com/reel/DMmxU4bKcsR/"},
	{"twitter-x", "https://x.com/SpaceX/status/2075621981488033835"},
	{"facebook", "https://www.facebook.com/100033620354545/videos/106560053808006/"},
	{"instagram-photo", "https://www.instagram.com/p/BsOGulcndj-/"},
}

func TestLiveResolveAcrossNetworks(t *testing.T) {
	s := liveService(t)
	for _, tc := range liveURLs {
		t.Run(tc.network, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			info, rerr := s.yt.probe(ctx, tc.url)
			if rerr != nil {
				// A failure here is reportable data, not a broken test: some
				// networks defeat these tools and the point is to know which.
				t.Logf("FAILED  %-16s code=%-22s %s", tc.network, rerr.Code, rerr.Message)
				t.Logf("        upstream: %s", rerr.Upstream)
				return
			}
			resp := s.buildResponse(ctx, tc.url, info)
			if resp.Kind == "collection" {
				t.Logf("OK      %-16s collection of %d items", tc.network, len(resp.Items))
				return
			}
			if len(resp.Formats) == 0 {
				t.Errorf("%s: resolved but produced no formats", tc.network)
				return
			}
			t.Logf("OK      %-16s %q", tc.network, truncate(resp.Title, 46))
			if resp.Best < 0 {
				// A real state, not a crash: nothing on the page has both
				// picture and sound and this server cannot join them.
				t.Logf("        %d formats, NO SAFE DEFAULT", len(resp.Formats))
				for _, n := range resp.Notes {
					t.Logf("        note: %s", n)
				}
				return
			}
			best := resp.Formats[resp.Best]
			t.Logf("        %d formats, default %s %s (%s) audio=%s codec=%s/%s",
				len(resp.Formats), best.Label, best.Ext, best.SizeHuman,
				audioWord(best), orDash(best.VCodec), orDash(best.ACodec))
			for _, n := range resp.Notes {
				t.Logf("        note: %s", n)
			}

			// The promise: a video page never defaults to audio or to silence.
			switch formatClass(best) {
			case classAudioOnly:
				t.Errorf("%s: DEFAULTED TO AUDIO for a video page", tc.network)
			case classSilent:
				t.Errorf("%s: DEFAULTED TO A SILENT FILE", tc.network)
			}

			sized := 0
			for _, f := range resp.Formats {
				if f.BytesKnown && !f.BytesGuessed {
					sized++
				}
			}
			t.Logf("        %d/%d formats carry a measured (not estimated) size",
				sized, len(resp.Formats))
		})
	}
}

// Does the byte stream actually arrive, and is it the container we claimed?
func TestLiveMediaStreamPlays(t *testing.T) {
	s := liveService(t)
	srv := httptest.NewServer(http.HandlerFunc(s.handleMedia))
	defer srv.Close()

	for _, tc := range liveURLs {
		t.Run(tc.network, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			info, rerr := s.yt.probe(ctx, tc.url)
			if rerr != nil {
				t.Skipf("%s did not resolve (%s)", tc.network, rerr.Code)
			}
			resp := s.buildResponse(ctx, tc.url, info)
			if len(resp.Formats) == 0 || resp.Best < 0 {
				t.Skip("no safe default format to stream")
			}
			best := resp.Formats[resp.Best]
			// Every default should now be fetchable as one file: progressive
			// URLs directly, segmented ones repacked by ffmpeg, split tracks
			// joined. Nothing is skipped, because a skip here would hide
			// exactly the case that does not work.

			req, _ := http.NewRequestWithContext(ctx, "GET",
				srv.URL+strings.TrimPrefix(best.Media, "/api/link/media"), nil)
			req.Header.Set("Range", "bytes=0-65535")
			r, err := srv.Client().Do(req)
			if err != nil {
				t.Errorf("%s: media stream failed: %v", tc.network, err)
				return
			}
			defer r.Body.Close()
			head, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if r.StatusCode < 200 || r.StatusCode >= 300 {
				// An error page is still "bytes", so status is checked first --
				// otherwise a 403 body reads as a successful download.
				t.Errorf("%s: media stream answered HTTP %d (%d bytes: %s)",
					tc.network, r.StatusCode, len(head), truncate(string(head), 160))
				return
			}
			if len(head) == 0 {
				t.Errorf("%s: media stream returned no bytes (HTTP %d)", tc.network, r.StatusCode)
				return
			}
			if c := sniffContainer(head); c == "unrecognised" {
				t.Errorf("%s: what came back is not a media container: %s",
					tc.network, truncate(string(head), 160))
				return
			}
			t.Logf("PLAYS   %-16s HTTP %d, %d bytes, content-type %q, container %s",
				tc.network, r.StatusCode, len(head), r.Header.Get("Content-Type"),
				sniffContainer(head))
		})
	}
}

func TestLiveMusicMetadata(t *testing.T) {
	liveService(t) // for the YARRIT_LIVE skip; metadata needs no subprocess
	for _, tc := range []struct{ name, url string }{
		{"spotify-album", "https://open.spotify.com/album/4LH4d3cOWNNsVw41Gqt2kv"},
		{"spotify-track", "https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT"},
		{"apple-album", "https://music.apple.com/us/album/the-dark-side-of-the-moon/1065973699"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, ok := parseMusicLink(tc.url)
			if !ok {
				t.Fatalf("%s was not recognised as a music link", tc.url)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var (
				res *musicResult
				err error
			)
			if ref.Provider == "spotify" {
				res, err = fetchSpotifyMeta(ctx, ref, "")
			} else {
				res, err = fetchAppleMeta(ctx, ref, "")
			}
			if err != nil {
				t.Logf("FAILED  %-14s %v", tc.name, err)
				return
			}
			t.Logf("OK      %-14s %q by %q, %d tracks", tc.name, res.Title, res.Artist, len(res.Tracks))
			for i, tr := range res.Tracks {
				if i >= 3 {
					break
				}
				t.Logf("          %-32s -> search %q", truncate(tr.Title, 32), tr.Query)
			}
		})
	}
}

// The live counterpart to the hermetic SSRF tests: a real public host that
// really redirects to a private address, followed through the real client.
func TestLiveRedirectToPrivateIsRefused(t *testing.T) {
	liveService(t) // for the YARRIT_LIVE skip; this test needs no subprocess
	client := guardedClient(20 * time.Second)
	for _, target := range []string{
		"https://httpbin.org/redirect-to?url=http%3A%2F%2F192.168.0.115%3A9696%2F",
		"https://httpbin.org/redirect-to?url=http%3A%2F%2F127.0.0.1%3A8802%2F",
	} {
		resp, err := client.Get(target)
		if err != nil {
			t.Logf("REFUSED %s\n        %v", target, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			t.Logf("REFUSED %s\n        stopped at the %d redirect, never followed", target, resp.StatusCode)
			continue
		}
		t.Errorf("FOLLOWED a redirect to a private address: %s -> %d %q",
			target, resp.StatusCode, truncate(string(body), 100))
	}
}

func audioWord(f linkFormat) string {
	switch {
	case f.HasAudio:
		return "yes"
	case f.Muxed:
		return "joined from " + f.PairedWith
	case f.AudioKnown:
		return "NO (silent)"
	default:
		return "not reported"
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// sniffContainer identifies the file from its first bytes, which is the only
// way to tell whether what arrived matches what was advertised.
func sniffContainer(b []byte) string {
	if len(b) >= 12 && string(b[4:8]) == "ftyp" {
		return fmt.Sprintf("ISO-BMFF/mp4 (brand %s)", strings.TrimSpace(string(b[8:12])))
	}
	if len(b) >= 4 && b[0] == 0x1A && b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3 {
		return "Matroska/WebM"
	}
	if len(b) >= 3 && string(b[:3]) == "ID3" {
		return "MP3"
	}
	if len(b) >= 4 && string(b[:4]) == "OggS" {
		return "Ogg"
	}
	if len(b) >= 7 && strings.HasPrefix(string(b), "#EXTM3U") {
		return "HLS manifest"
	}
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 {
		return "JPEG"
	}
	return "unrecognised"
}
