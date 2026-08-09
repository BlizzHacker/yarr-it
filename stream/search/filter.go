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
	Kind       string // video | audio | image | game | comic
	// Source separates the two fundamentally different ways a result arrives:
	// "instant" is hosted by somebody who is always up (archive.org), "swarm"
	// is a torrent that depends on whoever is seeding. They behave so
	// differently -- one always plays, one might not -- that mixing them with
	// no way to choose makes the result list harder to reason about.
	Source string // "" (both) | instant | swarm

	// Lang is the language a result must be in, as a two-letter code, or "" for
	// no constraint. Music defaults it to English; see music.go, which holds the
	// rule, the table and the measurements it turns on.
	//
	// It is applied HERE rather than in the upstream query on purpose, and that
	// is the whole reason the filter is switchable at all: the result cache is
	// keyed on the query and the kind, so narrowing upstream would mean the
	// first person to search with the filter on decides what everybody else
	// gets. Filtering cards means one fetch serves every setting and changing
	// it costs no upstream traffic -- which is what this file already does for
	// every other filter, and says so at the top.
	Lang string

	// Systems narrows game results to particular machines -- snes, genesis,
	// c64. Accepts a slug, a full name, a short name or any alias, so
	// "megadrive" and "genesis" reach the same shelf; see systems.go.
	//
	// Unlike every other filter here this one ALSO deepens the result set,
	// because narrowing 60 rows out of a 272,000-item catalogue to one machine
	// leaves almost nothing. The cached set still stays unnarrowed -- see
	// deepenBySystem in main.go, which is careful about exactly the trap the
	// `Lang` comment above describes.
	Systems []string
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
		Kind:       kindFor(q),
		Source:     strings.ToLower(strings.TrimSpace(q.Get("source"))),
		Query:      q.Get("q"),
		Groups:     splitCSV(q.Get("groups")),
		Systems:    splitCSV(q.Get("system")),
		// Adult results are hidden unless explicitly asked for.
		ShowAdult: q.Get("adult") == "1" || q.Get("adult") == "true",
	}
	if f.Sort == "" {
		f.Sort = "relevance"
	}
	f.Lang = languageFor(q, f.Kind)
	return f
}

// systems resolves the requested machines -- slug, full name, short name or any
// alias -- to catalogue entries. Anything unrecognised is dropped, so nothing a
// caller supplies ever reaches a query.
func (f filters) systems() []*gameSystem { return resolveSystems(f.Systems) }

// systemIDs is the resolved set as slugs, for matching against a card.
func (f filters) systemIDs() map[string]bool {
	out := map[string]bool{}
	for _, s := range f.systems() {
		out[s.ID] = true
	}
	return out
}

// languageFor decides what language constraint a request carries.
//
// The default is per-domain and not global, because "English please" is a
// sensible default for music and a nonsense one everywhere else: an archive.org
// GAME has no language, a comic scan's language field is empty on almost every
// item, and defaulting those to English would delete most of two catalogues to
// enforce a preference nobody expressed about them. So a `lang=` given
// explicitly is honoured for any domain, and only music supplies one when the
// caller says nothing.
func languageFor(q url.Values, kind string) string {
	if raw := strings.TrimSpace(q.Get("lang")); raw != "" {
		return normaliseLanguage(raw)
	}
	if canonicalDomain(kind) == domainMusic {
		return musicLanguageDefault
	}
	return ""
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
	wantSystems := f.systemIDs()

	for _, c := range cards {
		if c.Adult && !f.ShowAdult {
			continue
		}
		if len(f.Groups) > 0 && !anyGroupMatches(c.Groups, f.Groups) {
			continue
		}
		// Narrowing to a machine is narrowing to games, so a torrent with no
		// machine is excluded rather than passed through as "unconstrained".
		// Keeping everything without a system would make the SNES filter return
		// every film that happens to share the query.
		if len(wantSystems) > 0 && !wantSystems[c.System] {
			continue
		}
		// Kind was parsed, used for the cache key, and then never applied --
		// so `kind=image` returned whatever the query found, video included.
		// A filter that changes nothing is worse than a missing one: it reads
		// as an answer.
		// Compared through the canonicaliser, never as raw strings. A card
		// cached before this vocabulary existed still says "audio" where a new
		// client says "music"; a string compare would drop every one of them
		// and look exactly like the bug this replaced.
		if f.Kind != "" && !sameDomain(c.Kind, f.Kind) {
			continue
		}
		if f.Source == "instant" && !c.Instant {
			continue
		}
		if f.Source == "swarm" && c.Instant {
			continue
		}
		// The language the item states about itself. A card that states nothing
		// is kept -- see musicLanguageAllows, where the measurement that forces
		// that is written down: ninety-five per cent of the Live Music Archive
		// never filled the field in, so treating silence as "not English" would
		// delete the catalogue rather than narrow it.
		if !f.languageAllows(c) {
			continue
		}
		kept := make([]source, 0, len(c.Sources))
		for _, s := range c.Sources {
			// Seeders and size are swarm properties. A hosted result has
			// neither, so the default "at least 1 seeder" would silently drop
			// every game -- the most reliable results on the page -- and the
			// search would look like it found nothing.
			if !c.Instant {
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

// languageAllows reports whether a card survives the language filter.
//
// A card with nothing to say about its language is always kept, and a card
// carrying no music facts at all has nothing to say -- so this is a no-op for
// every domain that does not populate them, which is all of them but one.
func (f filters) languageAllows(c card) bool {
	if f.Lang == "" {
		return true
	}
	stated := ""
	if c.Music != nil {
		stated = c.Music.Language
	}
	return musicLanguageAllows(f.Lang, stated)
}

// health puts hosted results and torrents on one scale.
//
// "Most seeders" is really "healthiest first": the count is a proxy for whether
// a thing will actually play. A hosted result is at the top of that scale, not
// the bottom, so sorting it by its literal zero buries the only guaranteed
// results beneath a two-seeder torrent.
//
// It is scored as a solidly-healthy torrent rather than as infinity on purpose.
// A search whose word appears in both a game and a film -- "batman", "sonic" --
// should not bury a 500-seeder film under every game that shares the word.
const instantHealth = 250

func health(c card) int {
	if c.Instant {
		return instantHealth
	}
	return c.Seeders
}

// exactFirst puts results that ARE what was asked for above results that merely
// contain the words, whatever ordering was requested underneath.
//
// This is Wade's rule -- "an exact match must outrank a romhack, always" -- and
// it has to live here rather than in the source that produced the cards,
// because every source's own ordering is re-done by sortCards afterwards. The
// archive.org search already ranked its results correctly (see rankByMatch);
// the default `seeders` sort then re-sorted hosted results by download count
// and put "Super Mario World DX", an MS-DOS fan hack with 157,392 downloads,
// back above "Super Mario World" with 28,882.
//
// It is a BAND, not a score, and it runs LAST: sortCards has already applied
// whatever ordering was asked for, and this is a stable partition on top, so
// the requested order survives untouched inside the exact matches and inside
// the rest. Sorting is how somebody says what they want ordered by; being the
// thing they asked for is not one of the options, it is the question.
func (f filters) exactFirst(cards []card) {
	// One word is a keyword, not a title. Somebody typing "zelda" wants the
	// Zelda games; they are not asking for a work called exactly Zelda, and
	// promoting one puts a 151-download Amstrad fan game above A Link to the
	// Past. Two words is where a query starts naming something.
	if len(significantTokens(f.Query)) < 2 {
		return
	}
	sort.SliceStable(cards, func(i, j int) bool {
		ei := matchScore(f.Query, cards[i].Title) >= matchAccept
		ej := matchScore(f.Query, cards[j].Title) >= matchAccept
		return ei && !ej
	})
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
			hi, hj := health(cards[i]), health(cards[j])
			if hi != hj {
				return hi > hj
			}
			if cards[i].Instant && cards[j].Instant {
				return cards[i].Popular > cards[j].Popular
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
	// Last, and stable, so the ordering chosen above survives inside each band.
	f.exactFirst(cards)
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
	// A recording is filed under the name of the WORK, and the person who made
	// it lives in a different field. "Tears" by King Oliver's Jazz Band is the
	// Louis Armstrong record somebody was looking for, and scoring it on its
	// title alone gives an overlap of zero for "louis armstrong" -- which is the
	// -2000 band below, so the correct answer sinks beneath everything that
	// merely shares a word. The artist is part of what the title MEANS here, so
	// it is part of what the query is compared against. See music_match.go,
	// which makes the same move for the same reason at the archive.org end.
	if c.Music != nil && c.Music.Artist != "" {
		if m := titleOverlap(c.Title+" "+c.Music.Artist, queryTerms); m > matched {
			matched = m
		}
	}
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
	//
	// For a host that is always up the question does not apply: it will play,
	// every time, which is what the seeder score is trying to estimate. That is
	// scored as the top health band rather than as a fake seeder count -- the
	// dimension being ranked is "will this actually start", and here the answer
	// is yes. Their own download count then separates the canonical upload from
	// its near-duplicates, the same job seeders do for a torrent.
	if c.Instant {
		n += 900
		switch {
		case c.Popular >= 100_000:
			n += 120
		case c.Popular >= 10_000:
			n += 80
		case c.Popular >= 1_000:
			n += 40
		}
		return n
	}
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
	Qualities []facetCount `json:"qualities"`
	Codecs    []facetCount `json:"codecs"`
	Indexers  []facetCount `json:"indexers"`
	Groups    []facetCount `json:"groups"`
	// Languages is what the results say they are in, counted BEFORE the language
	// filter runs -- these are built from the unfiltered set, which is what makes
	// the number mean anything.
	//
	// It is here so that a default filter is visible rather than implied. A
	// filter somebody cannot see does not narrow a catalogue, it replaces it,
	// and the person on the other side has no way to find out what they are no
	// longer being shown. `unstated` is a value like any other and is the
	// largest one on most music searches; see musicLanguageAllows.
	Languages []facetCount `json:"languages,omitempty"`
	// Systems is which machines this result set actually contains, counted the
	// same way and for the same reason as Languages: publishing the whole
	// catalogue instead would offer fifty-odd chips of which two do anything,
	// and the other forty-eight would each empty the page. Omitted entirely
	// when nothing in the results is a game -- and an empty array means exactly
	// that, never "show them all".
	//
	// One asymmetry worth knowing about, because it looks like a bug and is
	// not: a chip's number GROWS when you press it. Unlike every other filter
	// here, choosing a machine also fetches more of that machine (see
	// deepenBySystem), and these counts are built after that -- so "Master
	// System 1" becomes "Master System 13" once selected, and delivers 13. The
	// number always describes the set the chip would give you, which is the
	// property worth keeping; counting before the deepening would promise 1 and
	// hand over 13, which is the same asymmetry pointing the wrong way.
	Systems    []facetCount `json:"systems,omitempty"`
	AdultCount int          `json:"adultCount"`
	// How many results arrive each way, so the source toggle can say so
	// rather than making you click to find out one of them is empty.
	InstantCount int   `json:"instantCount"`
	SwarmCount   int   `json:"swarmCount"`
	MaxSeed      int   `json:"maxSeeders"`
	MaxSizeMB    int64 `json:"maxSizeMB"`
}

type facetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
	// Label is the name a person reads, where the value is a slug they would
	// not. archive.org's field says "wonderswan-color"; the chip has to say
	// "WonderSwan Color". Only the system facet carries one -- a quality is
	// already "1080p" and a language already has a menu of its own.
	Label string `json:"label,omitempty"`
}

func buildFacets(cards []card) facets {
	q, cd, ix := map[string]int{}, map[string]int{}, map[string]int{}
	grp := map[string]int{}
	lang := map[string]int{}
	sys := map[string]int{}
	f := facets{}
	for _, c := range cards {
		for _, g := range c.Groups {
			grp[g]++
		}
		if c.System != "" {
			sys[c.System]++
		}
		if c.Music != nil {
			lang[languageFacet(c.Music.Language)]++
		}
		if c.Adult {
			f.AdultCount++
		}
		if c.Instant {
			f.InstantCount++
		} else {
			f.SwarmCount++
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
	if len(lang) > 0 {
		f.Languages = sortedFacets(lang, false)
	}
	if len(sys) > 0 {
		f.Systems = namedSystems(sortedFacets(sys, false))
	}
	return f
}

// namedSystems attaches the machine's name to each slug, so a chip can read
// "Super Nintendo" while the request it sends says `system=snes`.
func namedSystems(in []facetCount) []facetCount {
	for i := range in {
		if s := systemByID[in[i].Value]; s != nil {
			in[i].Label = s.Name
		}
	}
	return in
}

// languageFacet buckets what an item said about itself.
//
// The two-letter code where the spelling is one we know, the item's own words
// where it is not -- so an unrecognised language is reported as itself rather
// than swept into "unstated", which would be this service claiming the item
// said nothing when it did.
const languageUnstated = "unstated"

func languageFacet(stated string) string {
	stated = strings.TrimSpace(stated)
	if stated == "" {
		return languageUnstated
	}
	if code := normaliseLanguage(stated); code != "" {
		return code
	}
	return strings.ToLower(stated)
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

// kindFor decides which Newznab categories to ask the indexers for.
//
// An explicit ?kind= wins. Otherwise a single chosen category implies one:
// picking "Games" should make the indexer query itself narrower, not just hide
// rows after the fact -- which is the difference between a narrow search and a
// broad search with most of it filtered away.
//
// Several categories at once imply nothing, since there is no single Newznab
// bucket covering them.
func kindFor(q url.Values) string {
	// Both spellings go through the same translation. They did not, and the
	// result was silent: `groups=movies` was mapped to the internal "video",
	// while `kind=movies` was passed through untouched and then compared against
	// card kinds that are never spelled that way -- so every card was filtered
	// out and the response was a perfectly valid, perfectly empty page. The
	// facet counts were computed before the filter, so the answer even claimed
	// hundreds of results while listing none. The browser never hit it because
	// it sends `groups`; the TV apps send `kind`, which is why category browsing
	// looked broken only there.
	if k := q.Get("kind"); k != "" {
		return canonicalKind(k)
	}
	groups := splitCSV(q.Get("groups"))
	if len(groups) != 1 {
		return ""
	}
	return canonicalKind(groups[0])
}

// canonicalKind resolves any name a caller might use onto a canonical domain.
//
// The mapping lives in schema.json, not here. A switch in this file is what the
// web client would then have to mirror by hand, and mirroring by hand is the
// entire cause of the defect this replaced -- so the table is data, read by
// both languages and served to TV clients at /api/schema.
func canonicalKind(k string) string {
	return canonicalDomain(k)
}
