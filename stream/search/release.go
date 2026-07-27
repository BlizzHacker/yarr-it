package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Release-name parsing. Prowlarr hands back scene-style filenames like
//
//	The.Matrix.1999.2160p.UHD.BluRay.x265.10bit.HDR.DTS-HD.MA.5.1-SWTYBLZ
//
// Every torrent site shows these raw, which is why they all feel like a
// directory listing instead of a library. Parsing them into (title, year,
// quality, codec) is what lets many torrents collapse into one title card with
// a quality picker underneath -- the single biggest UX difference here.

type parsed struct {
	Title     string
	Year      int
	Quality   string
	Source    string
	Codec     string
	Audio     string
	Group     string
	Season    int
	Episode   int
	IsSeries  bool
	WebSafe   bool // browser can play without transcoding
	Extension string
}

var (
	reYear    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reSeason  = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,3})\b`)
	reSeason2 = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2})\b`)
	reGroup   = regexp.MustCompile(`-([A-Za-z0-9]+)$`)
	reSpace   = regexp.MustCompile(`[.\_]+`)
	reMulti   = regexp.MustCompile(`\s{2,}`)
	reBracket = regexp.MustCompile(`[\[\(][^\]\)]*[\]\)]`)
)

// Ordered longest-first so "2160p" wins before "60p"-style partial matches.
var qualityTokens = []string{
	"2160p", "4320p", "1440p", "1080p", "1080i", "720p", "576p", "480p", "360p", "240p",
}

var sourceTokens = []string{
	"BluRay", "BDRip", "BRRip", "WEB-DL", "WEBDL", "WEBRip", "WEB", "HDTV", "DVDRip",
	"DVDScr", "HDRip", "CAM", "TS", "TC", "REMUX", "UHD",
}

var codecTokens = []string{
	"x265", "H265", "HEVC", "x264", "H264", "AVC", "XviD", "DivX", "AV1", "VP9", "MPEG2",
}

var audioTokens = []string{
	"DTS-HD", "TrueHD", "Atmos", "DDP5.1", "DDP", "EAC3", "AC3", "DTS", "AAC", "FLAC",
	"MP3", "Opus", "PCM",
}

// webSafeCodecs are what a browser can decode via MediaSource without help.
// Anything else needs the WebCodecs path (hardware decode on the viewer's GPU)
// or software fallback -- never a server-side transcode.
var webSafeCodecs = map[string]bool{
	"x264": true, "H264": true, "AVC": true, "VP9": true, "AV1": true,
}

func findToken(name string, tokens []string) string {
	upper := strings.ToUpper(name)
	for _, t := range tokens {
		if strings.Contains(upper, strings.ToUpper(t)) {
			return t
		}
	}
	return ""
}

func parseRelease(name string) parsed {
	p := parsed{}
	clean := reBracket.ReplaceAllString(name, " ")
	clean = reSpace.ReplaceAllString(clean, " ")
	clean = reMulti.ReplaceAllString(clean, " ")
	clean = strings.TrimSpace(clean)

	if m := reGroup.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		p.Group = m[1]
	}

	p.Quality = findToken(clean, qualityTokens)
	p.Source = findToken(clean, sourceTokens)
	p.Codec = findToken(clean, codecTokens)
	p.Audio = findToken(clean, audioTokens)
	p.WebSafe = webSafeCodecs[p.Codec]

	// Series detection first: SxxEyy or 1x02.
	cut := len(clean)
	if m := reSeason.FindStringSubmatchIndex(clean); m != nil {
		p.IsSeries = true
		p.Season, _ = strconv.Atoi(clean[m[2]:m[3]])
		p.Episode, _ = strconv.Atoi(clean[m[4]:m[5]])
		cut = m[0]
	} else if m := reSeason2.FindStringSubmatchIndex(clean); m != nil {
		p.IsSeries = true
		p.Season, _ = strconv.Atoi(clean[m[2]:m[3]])
		p.Episode, _ = strconv.Atoi(clean[m[4]:m[5]])
		cut = m[0]
	}

	// The title is whatever precedes the year (for films) or the SxxEyy marker.
	if loc := reYear.FindStringIndex(clean); loc != nil && loc[0] < cut {
		p.Year, _ = strconv.Atoi(clean[loc[0]:loc[1]])
		cut = loc[0]
	}

	title := clean
	if cut < len(clean) {
		title = clean[:cut]
	}
	// Trim any quality/source/codec tokens that leaked into the title.
	for _, tokens := range [][]string{qualityTokens, sourceTokens, codecTokens, audioTokens} {
		for _, t := range tokens {
			if i := strings.Index(strings.ToUpper(title), strings.ToUpper(t)); i > 0 {
				title = title[:i]
			}
		}
	}
	p.Title = strings.TrimSpace(strings.Trim(strings.TrimSpace(title), "-–—:|"))
	return p
}

// groupKey is what decides whether two torrents are the same thing. Films group
// by title+year; series group by title+season+episode so each episode is its
// own card rather than one giant blob for the show.
func (p parsed) groupKey() string {
	t := strings.ToLower(p.Title)
	t = strings.Join(strings.Fields(t), " ")
	if p.IsSeries {
		return t + "|s" + strconv.Itoa(p.Season) + "e" + strconv.Itoa(p.Episode)
	}
	if p.Year > 0 {
		return t + "|" + strconv.Itoa(p.Year)
	}
	return t
}

// qualityRank orders the picker so the best playable option is offered first.
func qualityRank(q string) int {
	switch q {
	case "4320p":
		return 7
	case "2160p":
		return 6
	case "1440p":
		return 5
	case "1080p", "1080i":
		return 4
	case "720p":
		return 3
	case "576p":
		return 2
	case "480p":
		return 1
	default:
		return 0
	}
}
