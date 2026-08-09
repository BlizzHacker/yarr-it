package main

// The final answer to "does the file actually have sound".
//
// Everything else in the live suite checks metadata and byte counts, which is
// exactly what a tool that hands out silent files would also pass. This one
// pulls a real chunk through the real endpoint, writes it to disk and asks
// ffprobe what streams are in it. If a joined 1080p arrives with no audio
// stream, this is the test that says so.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type probeStreams struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

func TestLiveDownloadedFileHasBothStreams(t *testing.T) {
	s := liveService(t)
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed; cannot inspect the produced file")
	}
	srv := httptest.NewServer(http.HandlerFunc(s.handleMedia))
	defer srv.Close()

	// Deliberately the networks that serve split or segmented tracks -- the
	// ones where a naive implementation produces a silent file.
	for _, tc := range []struct{ network, url string }{
		{"youtube", "https://www.youtube.com/watch?v=aqz-KE-bpKQ"},
		{"vimeo", "https://vimeo.com/channels/staffpicks/1212617765"},
		{"reddit", "https://www.reddit.com/r/aww/comments/1v2mhes/i_carry_shelter_dogs_around_nyc_in_a_dog_backpack/"},
		{"dailymotion", "https://www.dailymotion.com/video/x7tpzb4"},
		{"tiktok", "https://www.tiktok.com/@nasa/video/7670721000471891214"},
		{"facebook", "https://www.facebook.com/100033620354545/videos/106560053808006/"},
	} {
		t.Run(tc.network, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			info, rerr := s.yt.probe(ctx, tc.url)
			if rerr != nil {
				t.Skipf("%s did not resolve (%s)", tc.network, rerr.Code)
			}
			resp := s.buildResponse(ctx, tc.url, info)
			if resp.Best < 0 {
				t.Fatalf("%s: no safe default", tc.network)
			}
			best := resp.Formats[resp.Best]

			req, _ := http.NewRequestWithContext(ctx, "GET",
				srv.URL+strings.TrimPrefix(best.Media, "/api/link/media")+"&dl=1", nil)
			r, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tc.network, err)
			}
			defer r.Body.Close()

			path := filepath.Join(t.TempDir(), "clip.mp4")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			// 6 MiB is plenty for ffprobe to read both stream headers without
			// pulling a 250 MB file across for a test.
			n, _ := io.Copy(f, io.LimitReader(r.Body, 6<<20))
			f.Close()
			if n == 0 {
				t.Fatalf("%s: nothing downloaded", tc.network)
			}

			out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
				"-show_streams", "-print_format", "json", path).Output()
			if err != nil {
				t.Fatalf("%s: ffprobe could not read the produced file: %v", tc.network, err)
			}
			var ps probeStreams
			if err := json.Unmarshal(out, &ps); err != nil {
				t.Fatal(err)
			}

			var video, audio []string
			for _, st := range ps.Streams {
				switch st.CodecType {
				case "video":
					video = append(video, st.CodecName)
				case "audio":
					audio = append(audio, st.CodecName)
				}
			}
			t.Logf("%-14s %s %s -> %d bytes | video=%v audio=%v",
				tc.network, best.Label, best.Ext, n, video, audio)

			if len(video) == 0 {
				t.Errorf("%s: the default format produced a file with NO VIDEO STREAM", tc.network)
			}
			if len(audio) == 0 {
				t.Errorf("%s: the default format produced a SILENT FILE -- this is the exact "+
					"bug the format classifier exists to prevent", tc.network)
			}
		})
	}
}
