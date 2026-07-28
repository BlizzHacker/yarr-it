package main

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Search filters, in the spirit of a real torrent client rather than a web
// search box. Filtering happens server-side on the already-cached result set,
// so changing a filter is instant and costs no indexer traffic.

type filters struct {
	MinSeeders int
	MinSizeMB  int64
	MaxSizeMB  int64
	Qualities  []string // 2160p, 1080p, 720p, ...
	Codecs     []string // x264, x265, ...
	Indexers   []string
	WebSafe    bool   // only what the browser can play unaided
	Sort       string // relevance | seeders | size | quality | recent | title
	Query      string
	Groups     []string // movies, tv, music, games, apps, books, anime, adult
	ShowAdult  bool
	Kind       string // video | audio | image
}

func parseFilters(q url.Values) filters {
	f := filters{
		MinSeeders: atoiDefault(q.Get("minSeeders"), 0),
		MinSizeMB:  int64(atoiDefault(q.Get("minSizeMB"), 0)),
		MaxSizeMB:  int64(atoiDefault(q.Get("maxSizeMB"), 0)),
		Qualities:  splitCSV(q.Get("quality")),
		Codecs:     splitCSV(q.Get("codec")),
		Indexers:   splitCSV(q.Get("indexer")),
		WebSafe:    q.Get("webSafe") == "1" || q.Get("webSafe") == "true",
		Sort:       q.Get("sort"),
		Kind:       q.Get("kind"),
		Query:      q.Get("q"),
		Groups:     splitCSV(q.Get("groups")),
		// Adult results are hidden unless explicitly asked for.
		ShowAdult: q.Get("adult") == "1" || q.Get("adult") == "true",
	}
	if f.Sort == "" {
		f.Sort = "relevance"
	}
	return f
}

func atoiDefault(s string, d int) int {
	if s == "" {
		return d
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return d
	}
	return n
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

func containsFold(list []string, v string) bool {
	if len(list) == 0 {
		return true // no constraint
	}
	v = strings.ToLower(v)
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// apply filters a card list, dropping sources that fail and then cards left
// with nothing. A card whose only remaining sources are dead is not useful.
func (f filters) apply(cards []card) []card {
	out := make([]card, 0, len(cards))

	for _, c := range cards {
		if c.Adult && !f.ShowAdult {
			continue
		}
		if len(f.Groups) > 0 && !anyGroupMatches(c.Groups, f.Groups) {
			continue
		}
		kept := make([]source, 0, len(c.Sources))
		for _, s := range c.Sources {
			if s.Seeders < f.MinSeeders {
				continue
			}
			sizeMB := s.Size / (1024 * 1024)
			if f.MinSizeMB > 0 && sizeMB < f.MinSizeMB {
				continue
			}
			if f.MaxSizeMB > 0 && sizeMB > f.MaxSizeMB {
				continue
			}
			if f.WebSafe && !s.WebSafe {
				continue
			}
			if len(f.Qualities) > 0 && !containsFold(f.Qualities, s.Quality) {
				continue
			}
			if len(f.Codecs) > 0 && !containsFold(f.Codecs, s.Codec) {
				continue
			}
			if len(f.Indexers) > 0 && !containsFold(f.Indexers, s.Indexer) {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) == 0 {
			continue
		}
		c.Sources = kept
		c.Seeders = 0
		for _, s := range kept {
			if s.Seeders > c.Seeders {
				c.Seeders = s.Seeders
			}
		}
		rankSources(&c)
		out = append(out, c)
	}

	f.sortCards(out)
	return out
}

func (f filters) sortCards(cards []card) {
	terms := queryTerms(f.Query)
	switch f.Sort {
	case "size":
		sort.SliceStable(cards, func(i, j int) bool {
			return biggest(cards[i]) > biggest(cards[j])
		})
	case "quality":
		sort.SliceStable(cards, func(i, j int) bool {
			qi, qj := bestQuality(cards[i]), bestQuality(cards[j])
			if qi != qj {
				return qi > qj
			}
			return cards[i].Seeders > cards[j].Seeders
		})
	case "recent":
		sort.SliceStable(cards, func(i, j int) bool {
			return newest(cards[i]) > newest(cards[j])
		})
	case "title":
		sort.SliceStable(cards, func(i, j int) bool {
			return strings.ToLower(cards[i].Title) < strings.ToLower(cards[j].Title)
		})
	case "seeders":
		sort.SliceStable(cards, func(i, j int) bool {
			if cards[i].Seeders != cards[j].Seeders {
				return cards[i].Seeders > cards[j].Seeders
			}
			return cards[i].Title < cards[j].Title
		})
	default: // relevance
		sort.SliceStable(cards, func(i, j int) bool {
			ri, rj := relevance(cards[i], terms), relevance(cards[j], terms)
			if ri != rj {
				return ri > rj
			}
			return cards[i].Seeders > cards[j].Seeders
		})
	}
}

// relevance decides what a person most likely meant.
//
// Raw seeder count alone is a poor ranking: a search for a film puts language
// packs, course recordings and porn rips above the film itself, because those
// happen to be well seeded. A confirmed TMDB match is strong evidence that a
// result *is* a real work, so it counts heavily.
//
// But "is a real work" is not "is the work you asked for". Indexers return
// loosely related junk, and a correctly-identified *different* film would
// otherwise outrank the target purely for having artwork -- a search for
// "inception" once put American Pie third. So the title is scored against the
// query first, and anything sharing no query terms is pushed below everything
// that does.
func relevance(c card, queryTerms []string) int {
	n := 0

	matched := titleOverlap(c.Title, queryTerms)
	switch {
	case len(queryTerms) == 0:
		// No query (discover/browse): nothing to match against.
	case matched == 0:
		n -= 2000
	default:
		n += 700 * matched / len(queryTerms)
	}

	// Only demote when the query did not itself ask for it.
	if looksAdult(c.Title) && !queryWantsAdult(queryTerms) {
		n -= 900
	}

	// Artwork is weighted differently for browsing than for searching, because
	// the two want opposite things.
	//
	// Browsing (no query) is a poster wall: a card with art is the whole point,
	// so art leads. Searching is a question with a right answer, and there the
	// only thing that decides whether a result actually plays is how many
	// people are seeding it.
	//
	// Artwork used to be worth 1000 in BOTH cases while the seeder bonus topped
	// out at 60, so one mis-matched poster outranked a 500-seeder release --
	// which is precisely how "random stuff with the wrong image" reached the
	// top of a search.
	if c.Art.Found {
		if len(queryTerms) == 0 {
			n += 1000
			if c.Art.Poster != "" {
				n += 200
			}
			if c.Year > 0 {
				n += 100
			}
			n += int(c.Art.Rating * 5)
		} else {
			n += 80
			if c.Art.Poster != "" {
				n += 20
			}
			n += int(c.Art.Rating)
		}
	}
	// Health dominates. A release nobody is seeding cannot be streamed at all,
	// so this is the single most useful thing to rank on.
	switch {
	case c.Seeders >= 500:
		n += 900
	case c.Seeders >= 100:
		n += 700
	case c.Seeders >= 50:
		n += 550
	case c.Seeders >= 20:
		n += 400
	case c.Seeders >= 5:
		n += 220
	case c.Seeders >= 1:
		n += 10
	}
	// Several sources for one title means the release is real and widely
	// mirrored rather than a one-off upload.
	if len(c.Sources) >= 5 {
		n += 30
	} else if len(c.Sources) >= 2 {
		n += 15
	}
	return n
}

func biggest(c card) int64 {
	var n int64
	for _, s := range c.Sources {
		if s.Size > n {
			n = s.Size
		}
	}
	return n
}

func bestQuality(c card) int {
	n := 0
	for _, s := range c.Sources {
		if r := qualityRank(s.Quality); r > n {
			n = r
		}
	}
	return n
}

func newest(c card) string {
	var n string
	for _, s := range c.Sources {
		if s.Published > n {
			n = s.Published
		}
	}
	return n
}

// facets reports what values actually exist in a result set, so the UI can show
// only filters that would do something rather than a fixed list of dead options.
type facets struct {
	Qualities  []facetCount `json:"qualities"`
	Codecs     []facetCount `json:"codecs"`
	Indexers   []facetCount `json:"indexers"`
	Groups     []facetCount `json:"groups"`
	AdultCount int          `json:"adultCount"`
	MaxSeed    int          `json:"maxSeeders"`
	MaxSizeMB  int64        `json:"maxSizeMB"`
}

type facetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

func buildFacets(cards []card) facets {
	q, cd, ix := map[string]int{}, map[string]int{}, map[string]int{}
	grp := map[string]int{}
	f := facets{}
	for _, c := range cards {
		for _, g := range c.Groups {
			grp[g]++
		}
		if c.Adult {
			f.AdultCount++
		}
		for _, s := range c.Sources {
			if s.Quality != "" {
				q[s.Quality]++
			}
			if s.Codec != "" {
				cd[s.Codec]++
			}
			if s.Indexer != "" {
				ix[s.Indexer]++
			}
			if s.Seeders > f.MaxSeed {
				f.MaxSeed = s.Seeders
			}
			if mb := s.Size / (1024 * 1024); mb > f.MaxSizeMB {
				f.MaxSizeMB = mb
			}
		}
	}
	f.Qualities = sortedFacets(q, true)
	f.Codecs = sortedFacets(cd, false)
	f.Indexers = sortedFacets(ix, false)
	f.Groups = sortedFacets(grp, false)
	return f
}

func sortedFacets(m map[string]int, byQuality bool) []facetCount {
	out := make([]facetCount, 0, len(m))
	for k, v := range m {
		out = append(out, facetCount{Value: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if byQuality {
			return qualityRank(out[i].Value) > qualityRank(out[j].Value)
		}
		return out[i].Count > out[j].Count
	})
	return out
}
