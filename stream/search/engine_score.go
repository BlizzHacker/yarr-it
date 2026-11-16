package main

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Release scoring. Every candidate gets a score and, if it is unfit, the
// reasons -- shown in interactive search, so "why didn't it grab that?" has a
// sentence for an answer instead of a shrug.

type engineRelease struct {
	Title       string `json:"title"`
	Indexer     string `json:"indexer"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	Magnet      string `json:"magnet,omitempty"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	InfoHash    string `json:"infoHash,omitempty"`
	Published   string `json:"published,omitempty"`
}

type scoredRelease struct {
	engineRelease
	Quality  string   `json:"quality"`
	Source   string   `json:"source"`
	Codec    string   `json:"codec"`
	Group    string   `json:"group"`
	Season   int      `json:"season,omitempty"`
	Episode  int      `json:"episode,omitempty"`
	Pack     bool     `json:"pack,omitempty"` // whole season
	Score    int      `json:"score"`
	Rejected []string `json:"rejected,omitempty"`
}

// engineTarget is what a search is for.
type engineTarget struct {
	Kind    string // movie | series
	Title   string
	Year    int
	Season  int // 0: any (film, or a whole-series search)
	Episode int // 0 with Season>0: season pack wanted or acceptable
}

var reSeasonPack = regexp.MustCompile(`(?i)\b(?:S(\d{1,2})|Season[ .](\d{1,2}))\b`)

// seasonPack reports a whole-season release: "Show.S02.1080p" or "Season 2",
// with no episode marker.
func seasonPack(name string, p parsed) (int, bool) {
	if p.Episode > 0 {
		return 0, false
	}
	m := reSeasonPack.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	s := m[1]
	if s == "" {
		s = m[2]
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

var reNameSep = regexp.MustCompile(`[._\-\[\]\(\){}]+`)

// titleBeforeYear: everything before the first plausible year, and the year.
func titleBeforeYear(name string) (string, int) {
	flat := strings.Join(strings.Fields(reNameSep.ReplaceAllString(name, " ")), " ")
	loc := reYear.FindStringIndex(flat)
	if loc == nil || loc[0] == 0 {
		return "", 0
	}
	y, _ := strconv.Atoi(flat[loc[0]:loc[1]])
	return strings.TrimSpace(flat[:loc[0]]), y
}

func sourceRank(src string) int {
	switch strings.ToUpper(src) {
	case "REMUX":
		return 6
	case "BLURAY", "UHD":
		return 5
	case "WEB-DL", "WEBDL":
		return 4
	case "WEB", "WEBRIP":
		return 3
	case "BDRIP", "BRRIP", "HDRIP":
		return 2
	case "HDTV", "DVDRIP":
		return 1
	}
	return 0
}

func scoreRelease(r engineRelease, t engineTarget, prof engineProfile) scoredRelease {
	p := parseRelease(r.Title)
	// The same bracket-dropping hides "(1080p BluRay x265)"; look again in the
	// raw name with brackets flattened to spaces.
	if p.Quality == "" || p.Source == "" || p.Codec == "" {
		flat := reNameSep.ReplaceAllString(r.Title, " ")
		if p.Quality == "" {
			p.Quality = findToken(flat, qualityTokens)
		}
		if p.Source == "" {
			p.Source = findToken(flat, sourceTokens)
		}
		if p.Codec == "" {
			p.Codec = findToken(flat, codecTokens)
		}
	}
	s := scoredRelease{
		engineRelease: r,
		Quality:       p.Quality, Source: p.Source, Codec: p.Codec, Group: p.Group,
		Season: p.Season, Episode: p.Episode,
	}
	if n, ok := seasonPack(r.Title, p); ok {
		s.Pack, s.Season = true, n
	}
	reject := func(why string) { s.Rejected = append(s.Rejected, why) }
	upper := strings.ToUpper(r.Title)

	// Is it even the right thing?
	title := p.Title
	if title == "" {
		title = r.Title
	}
	if s.Pack {
		// parseRelease cuts the title only at SxxEyy; a pack keeps "S01".
		if loc := reSeasonPack.FindStringIndex(title); loc != nil && loc[0] > 0 {
			title = strings.TrimSpace(title[:loc[0]])
		}
	}
	// parseRelease drops bracketed text before it looks for the year, so
	// "Title (1968) Criterion (1080p ...)" leaves the edition in the title and
	// no year at all. The raw name, cut at its first year, is the second
	// opinion.
	rawTitle, rawYear := titleBeforeYear(r.Title)
	if p.Year == 0 {
		p.Year = rawYear
	}
	if t.Title != "" && matchScore(t.Title, title) < 0.8 && (rawTitle == "" || matchScore(t.Title, rawTitle) < 0.8) {
		reject("title does not match " + strconv.Quote(t.Title))
	}
	switch t.Kind {
	case "movie":
		if p.IsSeries || s.Pack {
			reject("is a series release")
		}
		if t.Year > 0 && p.Year > 0 && absInt(p.Year-t.Year) > 1 {
			reject("year " + strconv.Itoa(p.Year) + " is not " + strconv.Itoa(t.Year))
		}
	case "series":
		switch {
		case t.Season > 0 && t.Episode > 0:
			if s.Pack {
				if s.Season != t.Season {
					reject("season pack for the wrong season")
				}
			} else if p.Season != t.Season || p.Episode != t.Episode {
				reject("not S" + pad2(t.Season) + "E" + pad2(t.Episode))
			}
		case t.Season > 0:
			if !s.Pack || s.Season != t.Season {
				reject("not a season " + strconv.Itoa(t.Season) + " pack")
			}
		}
		if !p.IsSeries && !s.Pack {
			reject("not a series release")
		}
	}

	// Is it good enough?
	qi := indexFold(prof.Qualities, p.Quality)
	if p.Quality == "" {
		reject("quality unknown")
	} else if qi < 0 {
		reject(p.Quality + " is not in the profile")
	}
	for _, bad := range prof.RejectSources {
		if strings.EqualFold(p.Source, bad) {
			reject(p.Source + " releases are rejected")
		}
	}
	if r.Seeders < prof.MinSeeders {
		reject(strconv.Itoa(r.Seeders) + " seeders, fewer than " + strconv.Itoa(prof.MinSeeders))
	}
	if prof.MaxSizeGB > 0 && float64(r.Size) > prof.MaxSizeGB*(1<<30) {
		reject("larger than " + strconv.FormatFloat(prof.MaxSizeGB, 'f', -1, 64) + " GB")
	}
	for _, w := range prof.RejectWords {
		if w != "" && strings.Contains(upper, strings.ToUpper(w)) {
			reject("contains " + strconv.Quote(w))
		}
	}
	if r.Magnet == "" && r.DownloadURL == "" {
		reject("no magnet or download link")
	}

	// How good?
	if qi >= 0 {
		s.Score += (len(prof.Qualities) - qi) * 1000
	}
	s.Score += sourceRank(p.Source) * 60
	switch strings.ToUpper(p.Codec) {
	case "X265", "H265", "HEVC", "AV1":
		s.Score += 25
	}
	if strings.Contains(upper, "PROPER") || strings.Contains(upper, "REPACK") {
		s.Score += 20
	}
	for _, w := range prof.PreferredWords {
		if w != "" && strings.Contains(upper, strings.ToUpper(w)) {
			s.Score += 50
		}
	}
	if r.Seeders > 0 {
		s.Score += int(math.Min(100, 20*math.Log2(float64(r.Seeders)+1)))
	}
	return s
}

// rankReleases scores and sorts: fit releases first, best score first.
func rankReleases(rs []engineRelease, t engineTarget, prof engineProfile) []scoredRelease {
	out := make([]scoredRelease, 0, len(rs))
	seen := map[string]bool{}
	for _, r := range rs {
		key := strings.ToLower(r.InfoHash)
		if key == "" {
			key = strings.ToLower(r.Title) + "|" + strconv.FormatInt(r.Size, 10)
		}
		if seen[key] {
			continue // the same torrent from two indexers
		}
		seen[key] = true
		out = append(out, scoreRelease(r, t, prof))
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := len(out[i].Rejected) == 0, len(out[j].Rejected) == 0
		if fi != fj {
			return fi
		}
		return out[i].Score > out[j].Score
	})
	return out
}

// meetsCutoff: is a file at quality `have` good enough to stop upgrading?
func meetsCutoff(have string, prof engineProfile) bool {
	if have == "" {
		return false
	}
	hi, ci := indexFold(prof.Qualities, have), indexFold(prof.Qualities, prof.Cutoff)
	if ci < 0 {
		return hi >= 0
	}
	return hi >= 0 && hi <= ci
}

// isUpgrade: would `next` replace `have` under this profile?
func isUpgrade(have, next string, prof engineProfile) bool {
	if have == "" {
		return true
	}
	if meetsCutoff(have, prof) {
		return false
	}
	hi, ni := indexFold(prof.Qualities, have), indexFold(prof.Qualities, next)
	return ni >= 0 && (hi < 0 || ni < hi)
}

func indexFold(list []string, v string) int {
	for i, s := range list {
		if strings.EqualFold(s, v) {
			return i
		}
	}
	return -1
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
