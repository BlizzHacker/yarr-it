package main

import "strings"

// Newznab/Torznab category mapping, and the adult filter.
//
// Indexers report categories as numeric Newznab ids. Grouping them into the
// handful of buckets people actually think in — the way a torrent site's
// category bar works — lets the UI offer checkboxes instead of asking anyone to
// remember that 2000 means Movies.

type categoryGroup struct {
	Key   string
	Label string
	// Newznab ranges, inclusive lower / exclusive upper.
	Ranges [][2]int
}

var categoryGroups = []categoryGroup{
	{"movies", "Movies", [][2]int{{2000, 3000}}},
	{"tv", "TV", [][2]int{{5000, 6000}}},
	{"music", "Music", [][2]int{{3000, 4000}}},
	{"games", "Games", [][2]int{{1000, 2000}, {4050, 4060}}},
	{"apps", "Apps", [][2]int{{4000, 4050}, {4060, 5000}}},
	{"books", "Books", [][2]int{{7000, 8000}}},
	{"anime", "Anime", [][2]int{{5070, 5081}}},
	{"adult", "Adult", [][2]int{{6000, 7000}}},
}

func groupForCategory(id int) string {
	// Anime overlaps the TV range, so it is checked first.
	for _, g := range categoryGroups {
		if g.Key != "anime" {
			continue
		}
		for _, r := range g.Ranges {
			if id >= r[0] && id < r[1] {
				return "anime"
			}
		}
	}
	for _, g := range categoryGroups {
		if g.Key == "anime" {
			continue
		}
		for _, r := range g.Ranges {
			if id >= r[0] && id < r[1] {
				return g.Key
			}
		}
	}
	return "other"
}

// groupsFor returns every bucket a result belongs to.
func groupsFor(r prowlarrResult) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range r.Categories {
		g := groupForCategory(c.ID)
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	if len(out) == 0 {
		out = append(out, "other")
	}
	return out
}

// adultIndexers are dedicated porn trackers. Everything they return is adult
// regardless of how it is categorised, and several of them do not category-tag
// at all.
var adultIndexers = []string{
	"jav", "porn", "xxx", "sexy", "hentai", "erotic", "sukebei", "onejav",
	"u3c3", "sosulki", "ehentai", "e-hentai", "18+",
}

// isAdult decides whether a result should be hidden when the adult filter is on.
//
// Three signals, because no single one is reliable: the Newznab category (which
// many indexers omit), the indexer itself (a dedicated porn tracker tags
// nothing), and the release title (parodies and scene names leak through the
// other two).
func isAdult(r prowlarrResult, title string) bool {
	for _, c := range r.Categories {
		if c.ID >= 6000 && c.ID < 7000 {
			return true
		}
	}
	idx := strings.ToLower(r.Indexer)
	for _, marker := range adultIndexers {
		if strings.Contains(idx, marker) {
			return true
		}
	}
	return looksAdult(title)
}

func hasString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// anyGroupMatches reports whether a card falls into any selected bucket.
// Selecting nothing means no constraint, the way an unticked filter bar should
// behave.
func anyGroupMatches(cardGroups, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, g := range cardGroups {
		for _, w := range want {
			if strings.EqualFold(g, w) {
				return true
			}
		}
	}
	return false
}
