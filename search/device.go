package main

import "strings"

// Device playback profiles.
//
// A browser will attempt almost anything and fall back to software decode. A
// set-top box will not: Roku either plays a container natively or shows a bare
// "cannot play" error with no explanation. Offering a source the device cannot
// handle makes the app look broken when the app is fine, so unplayable sources
// are filtered out before they ever reach the screen.
//
// Roku's supported set (models from ~2017 on):
//
//	containers  MP4/M4V, MKV, MOV, TS/M2TS, and HLS/DASH
//	video       H.264, HEVC/H.265, VP9 (4K models), AV1 (2023+ only)
//	audio       AAC, MP3, AC3/EAC3, FLAC, ALAC, PCM, Vorbis-in-MKV
//
// AVI, WMV, FLV, RMVB and OGV are not playable at all, whatever is inside them.

type deviceProfile struct {
	Name string
	// Containers the device can demux, lower-case with the leading dot.
	Containers []string
	// Containers it definitely cannot, checked first so a bad container beats
	// a good codec.
	BadContainers []string
	// Video codecs it can decode, matched against parsed release names.
	BadCodecs []string
	// Whether audio-only material is useful on this device.
	AllowAudio bool
	// Whether the device can run an emulator or an embedded player at all.
	// A set-top box has no WebAssembly and no third-party iframe, so a game
	// result there is a card that can never be opened.
	AllowInteractive bool
}

var rokuProfile = deviceProfile{
	Name:          "roku",
	Containers:    []string{".mp4", ".m4v", ".mkv", ".mov", ".ts", ".m2ts"},
	BadContainers: []string{".avi", ".wmv", ".flv", ".rmvb", ".ogv", ".ogm", ".divx", ".iso", ".img"},
	// AV1 only decodes on 2023+ hardware and XviD/DivX not at all, so both are
	// excluded rather than gambling on the specific model.
	BadCodecs:  []string{"xvid", "divx", "av1", "mpeg2", "vc1", "wmv"},
	AllowAudio: true,
}

// deviceProfileFor maps a client hint to a profile. An unknown or absent hint
// means no filtering, which keeps browsers unaffected.
func deviceProfileFor(name string) *deviceProfile {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "roku":
		return &rokuProfile
	default:
		return nil
	}
}

// playable reports whether the device can be expected to play this source.
func (p *deviceProfile) playable(s source) bool {
	name := strings.ToLower(s.Title)

	for _, bad := range p.BadContainers {
		if strings.Contains(name, bad) {
			return false
		}
	}
	if s.Codec != "" {
		codec := strings.ToLower(s.Codec)
		for _, bad := range p.BadCodecs {
			if codec == bad {
				return false
			}
		}
	}

	// Release names usually carry no extension at all, so a known-good
	// container is treated as a bonus rather than a requirement -- demanding
	// one would throw away most of the catalogue.
	for _, good := range p.Containers {
		if strings.Contains(name, good) {
			return true
		}
	}

	// No container in the name: fall back to the codec, which is the strongest
	// remaining signal. H.264/HEVC in an unnamed container is nearly always MP4
	// or MKV in practice.
	switch strings.ToLower(s.Codec) {
	case "x264", "h264", "avc", "x265", "h265", "hevc":
		return true
	}
	return false
}

// kindPlayable reports whether a whole card is worth showing on the device.
// A TV cannot do anything with software, games or ebooks.
func (p *deviceProfile) kindPlayable(c card) bool {
	// A game needs an emulator or somebody else's embedded player, and a
	// set-top box has neither. Showing one there is offering a card that
	// cannot open.
	if c.Kind == "game" {
		return p.AllowInteractive
	}
	for _, g := range c.Groups {
		switch g {
		case "movies", "tv", "anime":
			return true
		case "music":
			if p.AllowAudio {
				return true
			}
		case "books":
			// Audiobooks are worth keeping; ebooks are not. The distinction is
			// only visible in the release name.
			if p.AllowAudio && looksLikeAudiobook(c) {
				return true
			}
		}
	}
	// "other" is the biggest bucket because many indexers do not categorise at
	// all, so it is judged on whether any source looks playable rather than
	// discarded outright.
	for _, s := range c.Sources {
		if p.playable(s) {
			return true
		}
	}
	return false
}

var audiobookMarkers = []string{
	"audiobook", "audio book", "unabridged", "abridged",
	"narrated", "audible", "m4b",
}

func looksLikeAudiobook(c card) bool {
	hay := strings.ToLower(c.Title)
	for _, s := range c.Sources {
		hay += " " + strings.ToLower(s.Title)
	}
	for _, m := range audiobookMarkers {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

// applyDevice filters a result set down to what the device can actually play.
func (p *deviceProfile) applyDevice(cards []card) []card {
	if p == nil {
		return cards
	}
	out := make([]card, 0, len(cards))
	for _, c := range cards {
		kept := make([]source, 0, len(c.Sources))
		for _, s := range c.Sources {
			if p.playable(s) {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			continue
		}
		c.Sources = kept
		if !p.kindPlayable(c) {
			continue
		}
		c.Seeders = 0
		for _, s := range kept {
			if s.Seeders > c.Seeders {
				c.Seeders = s.Seeders
			}
		}
		rankSources(&c)
		out = append(out, c)
	}
	return out
}
