package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Categories: the landing page, expanded.
//
// The landing page shows one section per domain, each a rail or two. Behind
// those there are hundreds of categories: fifty-odd game systems, TMDB's film
// and television genres, and every Internet Archive collection worth naming.
// Putting them on the page is not an option -- the paint budget is ~0.2s, and
// thirty shelves of games is a worse home page than one shelf of games.
//
// Growing the page in place is not the answer either. Expanding "Movies" into
// twenty genre rows is twenty requests, twenty rails of skeletons and a page
// visibly working for ten seconds to show things nobody asked for: strictly
// worse than the one row that was already there.
//
// So a category is its own PAGE, and the API is split to match:
//
//	GET /api/categories        the shape -- every domain, every category, and
//	                           how many items each holds. No upstream call: a
//	                           static table plus counts refreshed in the
//	                           background. A domain page is drawn entirely from
//	                           this, so choosing a category costs no network.
//	GET /api/rows?path=games/snes
//	                           one category's items, paged. A category page
//	                           fetches this and nothing else.
//
// /api/discover is untouched. The landing page makes exactly the calls it made
// before, so its paint cannot regress; everything here happens only after
// somebody navigates.
//
// Categories are addressable by path -- `games/snes`, `books/gutenberg`,
// `movies/science-fiction` -- because a page somebody can link to is worth
// having for its own sake. The path is RESOLVED against this registry, never
// interpolated into a query: a path that is not in the tree does not resolve,
// so no caller can reach through here into Solr.

// --------------------------------------------------------------- the tree --

type browseCategory struct {
	// Key is the row key: "ia:sys:snes", "tmdb:movie:28", "ia:col:gutenberg".
	Key string `json:"key"`
	// Path is the same category as a URL: "games/snes", "books/gutenberg". The
	// key is an internal handle; this is what a person can send to somebody.
	Path  string `json:"path"`
	Title string `json:"title"`
	// Badge is the compact label a tile shows -- "SNES" where the title is
	// "Super Nintendo".
	Badge string `json:"badge,omitempty"`
	// Find is every other word that should reach this category, lowercased and
	// space-joined: "megadrive", "speccy", "vcs", "famicom".
	//
	// Published rather than left to the server because the find box on a domain
	// page is a filter over a list already in the browser, and it has to answer
	// in the same frame as the keystroke. Without this it matched only the
	// title and the badge, so "mega drive" found the Genesis and "megadrive"
	// found nothing -- which is precisely the case that makes an alias worth
	// having. One flat string rather than an array: the whole tree is sent on
	// every visit, and this is the cheap shape.
	Find string `json:"find,omitempty"`
	// Count is how many items the source really holds. Absent until the
	// background refresh has been round once; a client must read 0 as "not
	// counted yet" rather than "empty".
	Count int `json:"count,omitempty"`
	// Plays marks a category this site's own player can run, as opposed to one
	// that opens in the host's player. Answered by play_archive.go.
	Plays bool `json:"plays,omitempty"`
}

type browseGroup struct {
	Key        string           `json:"key"`
	Title      string           `json:"title"`
	Categories []browseCategory `json:"categories"`
}

type browseDomain struct {
	Key    string        `json:"key"`
	Title  string        `json:"title"`
	Groups []browseGroup `json:"groups"`
}

// iaCollection is one Internet Archive collection published as a category.
//
// Every entry was probed against archive.org before it was written down.
// `film_noir`, `poetry` and `childrenslibrary` are not in this list because
// they return zero items -- plausible collection names that do not exist, which
// is exactly the dead category a browse must not contain.
//
// The domain keys are the site's own category vocabulary (see GROUP_LABELS in
// web/src/main.js and groupsFor in category.go), not a second set: a URL that
// says `movies` should mean the same thing as the Movies chip.
type iaCollection struct {
	Slug       string // the key segment, ours
	Title      string
	Collection string // theirs
	MediaType  string
	Domain     string
	Group      string
}

var iaCollections = []iaCollection{
	// Books. Gutenberg is what people mean by "free books"; the rest are the
	// Archive's own shelves.
	{"gutenberg", "Public domain classics", "gutenberg", "texts", "books", "books"}, // 56,051
	{"pulp", "Pulp magazines", "pulpmagazinearchive", "texts", "books", "books"},    // 27,953
	{"magazines", "Magazine rack", "magazine_rack", "texts", "books", "books"},      // 623,671
	{"scifi-books", "Science fiction", "sciencefiction", "texts", "books", "books"}, // 316
	{"cookbooks", "Cookbooks", "cookbooks", "texts", "books", "books"},              // 173
	{"childrens", "Children's books", "iacl", "texts", "books", "books"},            // 3,313
	{"manuals", "Computer manuals", "computermanuals", "texts", "books", "books"},   // 18,162
	{"biodiversity", "Natural history", "biodiversity", "texts", "books", "books"},  // 285,916

	// Comics. Deliberately the named golden-age publishers rather than the
	// general `comics` bucket, which is user-uploaded and mostly in copyright:
	// it puts a complete modern Batman run at the top. Filtering that bucket by
	// year does not help -- the year describes the issues, not the upload.
	{"fawcett", "Fawcett Comics", "fawcett-comics", "texts", "comics", "comics"},               // 811
	{"four-favorites", "Four Favorites", "four-favorites-comics", "texts", "comics", "comics"}, // 20
	{"ace-dotty", "Ace Comics", "comic-ace-dotty", "texts", "comics", "comics"},                // 6
	{"historietas", "Historietas", "historietas-collection", "texts", "comics", "comics"},      // 53

	// Film and television, from the Archive rather than TMDB: these are films
	// you can watch now, not titles to go looking for.
	{"feature-films", "Feature films", "feature_films", "movies", "movies", "archive"}, // 28,406
	{"silent", "Silent film", "silent_films", "movies", "movies", "archive"},           // 3,526
	{"scifi-horror", "Sci-fi & horror", "SciFi_Horror", "movies", "movies", "archive"}, // 954
	// Slug is `cartoons`, not `animation`: TMDB also has an Animation genre, and
	// two categories in one domain cannot share a URL.
	{"cartoons", "Animation & cartoons", "animationandcartoons", "movies", "movies", "archive"}, // 15,680
	{"prelinger", "Prelinger Archives", "prelinger", "movies", "movies", "archive"},             // 10,459
	{"classic-tv", "Classic television", "classic_tv", "movies", "tv", "archive"},               // 11,006

	// Music and spoken word. Not genres -- a taped 1977 concert is not a genre,
	// it is a recording of a particular kind, and the Archive already sorts them
	// that way. music.go holds the same view of this catalogue.
	{"librivox", "LibriVox audiobooks", "librivoxaudio", "audio", "music", "audio"}, // 21,708
	{"etree", "Live Music Archive", "etree", "etree", "music", "audio"},             // 293,091
	{"78rpm", "78 RPM & cylinders", "78rpm", "audio", "music", "audio"},             // 309,371
	{"netlabels", "Netlabels", "netlabels", "audio", "music", "audio"},              // 76,954
	{"oldtimeradio", "Old time radio", "oldtimeradio", "audio", "music", "audio"},   // 8,712
}

var iaCollectionBySlug = func() map[string]*iaCollection {
	m := map[string]*iaCollection{}
	for i := range iaCollections {
		m[iaCollections[i].Slug] = &iaCollections[i]
	}
	return m
}()

func (c *iaCollection) query() string {
	return fmt.Sprintf("collection:(%s) AND mediatype:(%s)", c.Collection, c.MediaType)
}

// mediaKind maps an Archive mediatype onto what the client should do with a
// click, in the site's own vocabulary.
func (c *iaCollection) mediaKind() string {
	switch c.MediaType {
	case "texts":
		return "text"
	case "audio", "etree":
		return "audio"
	default:
		return "video"
	}
}

// ------------------------------------------------------------ row fetching --

type browseCache struct {
	mu     sync.RWMutex
	rows   map[string]discoverRow
	expiry map[string]time.Time
	// counts is filled by a background refresh and read by every request.
	// Serving a tree with no counts yet is fine -- the shape is what people
	// navigate by -- so nothing ever waits on this.
	counts map[string]int
}

func newBrowseCache() *browseCache {
	return &browseCache{
		rows:   map[string]discoverRow{},
		expiry: map[string]time.Time{},
		counts: map[string]int{},
	}
}

func (b *browseCache) row(key string) (discoverRow, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.rows[key]
	if !ok || time.Now().After(b.expiry[key]) {
		return discoverRow{}, false
	}
	return r, true
}

func (b *browseCache) putRow(key string, r discoverRow, ttl time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rows[key] = r
	b.expiry[key] = time.Now().Add(ttl)
}

func (b *browseCache) allCounts() map[string]int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]int, len(b.counts))
	for k, v := range b.counts {
		out[k] = v
	}
	return out
}

func (b *browseCache) putCounts(counts map[string]int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range counts {
		b.counts[k] = v
	}
}

// ------------------------------------------------------------ key handling --

type rowSource struct {
	title     string
	mediaType string
	// One of these two. An archive.org row is a Solr query; a TMDB row is a
	// discover path.
	solr     string
	solrSort string
	tmdbPath string
}

// slugify turns a name into the URL segment a person would guess. TMDB says
// "Science Fiction"; the page is at movies/science-fiction.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// keyForPath resolves "games/snes" to a row key.
//
// Archive.org collections are resolved before TMDB genres, because a
// collection's slug is fixed in this file and a genre's comes from somebody
// else's API: a genre TMDB adds tomorrow must not be able to take over a URL
// that already works. iaCollections is written to avoid the one collision that
// exists today (Animation).
func (s *server) keyForPath(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	domain, slug := parts[0], parts[1]

	if domain == "games" {
		if sys := systemByID[slug]; sys != nil {
			return "ia:sys:" + sys.ID, true
		}
		return "", false
	}
	if col := iaCollectionBySlug[slug]; col != nil && col.Domain == domain {
		return "ia:col:" + col.Slug, true
	}
	if domain == "movies" || domain == "tv" {
		kind := "movie"
		if domain == "tv" {
			kind = "tv"
		}
		for _, id := range s.tmdb.genreIDs() {
			if name, ok := s.tmdb.genreName(id); ok && slugify(name) == slug {
				return fmt.Sprintf("tmdb:%s:%d", kind, id), true
			}
		}
	}
	return "", false
}

// sourceFor resolves a row key. It returns false for anything not in the
// registry, which is what keeps a request from reaching Solr with text the
// caller chose.
func (s *server) sourceFor(key string) (rowSource, bool) {
	parts := strings.SplitN(key, ":", 3)
	if len(parts) != 3 {
		return rowSource{}, false
	}
	switch parts[0] + ":" + parts[1] {
	case "ia:sys":
		sys := systemByID[parts[2]]
		if sys == nil {
			return rowSource{}, false
		}
		return rowSource{
			title: sys.Name, mediaType: "game",
			solr: systemQuery(sys), solrSort: "downloads desc",
		}, true

	case "ia:col":
		col := iaCollectionBySlug[parts[2]]
		if col == nil {
			return rowSource{}, false
		}
		return rowSource{
			title: col.Title, mediaType: col.mediaKind(),
			solr: col.query(), solrSort: "downloads desc",
		}, true

	case "tmdb:movie", "tmdb:tv":
		id, err := strconv.Atoi(parts[2])
		if err != nil || id <= 0 {
			return rowSource{}, false
		}
		name, ok := s.tmdb.genreName(id)
		if !ok {
			return rowSource{}, false
		}
		kind := parts[1]
		return rowSource{
			title: name, mediaType: kind,
			tmdbPath: fmt.Sprintf("/discover/%s?with_genres=%d&sort_by=popularity.desc&vote_count.gte=%d",
				kind, id, genreVoteFloor),
		}, true
	}
	return rowSource{}, false
}

// genreVoteFloor keeps a genre row from filling with films nobody has heard of.
// TMDB's popularity sort alone surfaces recent uploads with four votes;
// requiring a real audience is what makes "Action" look like Action.
const genreVoteFloor = 200

func (c *tmdbClient) genreName(id int) (string, bool) {
	if !c.enabled() {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.genres[id]
	return n, ok
}

// genreIDs returns the loaded genre ids in a stable order.
func (c *tmdbClient) genreIDs() []int {
	if !c.enabled() {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]int, 0, len(c.genres))
	for id := range c.genres {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// ---------------------------------------------------------------- handlers --

const (
	browseRowTTL   = 3 * time.Hour
	countRefreshIn = 12 * time.Hour
	// One screen of a rail. More is wasted bytes on a phone; fewer makes the
	// rail look thin.
	browseRowItems = 24
	// The cap on one /api/rows call. Hundreds of rows exist, but a client that
	// asks for hundreds at once is either broken or hostile, and the honest
	// answer on a slow connection is to fetch a few at a time.
	maxRowsPerCall = 8
	// Past this, walking a 300,000-item collection one page at a time through
	// this relay is not browsing and search is the right tool.
	maxBrowsePage = 60
)

func (s *server) handleCategories(w http.ResponseWriter, r *http.Request) {
	counts := s.browse.allCounts()
	writeJSON(w, 200, map[string]any{"domains": s.categoryTree(counts)})
}

// categoryTree assembles the whole shape. Pure apart from reading the count
// map, so it costs nothing to build per request and needs no cache of its own.
func (s *server) categoryTree(counts map[string]int) []browseDomain {
	var out []browseDomain
	unknown := countsUnmeasured(counts)

	// Games, by machine and not by genre -- which is the obvious question,
	// because Movies below gets genres and igdb.go could supply them here too.
	//
	// It would be the wrong axis. archive.org's catalogue carries no genre
	// field at all, so a genre shelf could only be built by matching 271,000
	// archive items against IGDB titles by name -- the same fuzzy title match
	// that discover_resolve.go exists to stop trusting. The machine, meanwhile,
	// is already in the data, exact, free, and is what people actually browse
	// retro games by: somebody wants SNES games, not platformers.
	//
	// Ordered by how much is actually there, so the first thing anybody sees
	// under Commodore is the C64's 99,993 items and not the VIC-20's one.
	games := browseDomain{Key: "games", Title: "Games"}
	for _, fam := range systemFamilies {
		g := browseGroup{Key: fam.Key, Title: fam.Title}
		for _, sys := range systemsInFamily(fam.Key, counts) {
			n := counts[sys.ID]
			// A machine with nothing in it is not a category. Nothing here is
			// removed for being small -- an 18-item Mega Duck shelf is a real
			// shelf -- only for being empty.
			if n == 0 && !unknown {
				continue
			}
			g.Categories = append(g.Categories, browseCategory{
				Key: "ia:sys:" + sys.ID, Path: "games/" + sys.ID,
				Title: sys.Name, Badge: sys.Short, Find: strings.Join(sys.Aliases, " "),
				Count: n, Plays: sys.playsHere(),
			})
		}
		if len(g.Categories) > 0 {
			games.Groups = append(games.Groups, g)
		}
	}
	if len(games.Groups) > 0 {
		out = append(out, games)
	}

	// Film and television, from TMDB's own genre list.
	for _, d := range []struct{ key, title, kind string }{
		{"movies", "Movies", "movie"},
		{"tv", "TV", "tv"},
	} {
		dom := browseDomain{Key: d.key, Title: d.title}
		g := browseGroup{Key: "genre", Title: "Genres"}
		for _, id := range s.tmdb.genreIDs() {
			name, ok := s.tmdb.genreName(id)
			if !ok {
				continue
			}
			slug := slugify(name)
			// A genre whose slug an archive.org collection already owns is not
			// published: the collection's URL was there first, and two
			// categories cannot share one page.
			if c := iaCollectionBySlug[slug]; c != nil && c.Domain == d.key {
				continue
			}
			key := fmt.Sprintf("tmdb:%s:%d", d.kind, id)
			g.Categories = append(g.Categories, browseCategory{
				Key: key, Path: d.key + "/" + slug, Title: name, Count: counts[key],
			})
		}
		if len(g.Categories) > 0 {
			dom.Groups = append(dom.Groups, g)
		}
		dom.Groups = append(dom.Groups, s.archiveGroupsFor(d.key, counts)...)
		if len(dom.Groups) > 0 {
			out = append(out, dom)
		}
	}

	// Everything the Internet Archive contributes that is not a game.
	for _, d := range []struct{ key, title string }{
		{"books", "Books"},
		{"comics", "Comics"},
		{"music", "Music"},
	} {
		dom := browseDomain{Key: d.key, Title: d.title}
		dom.Groups = s.archiveGroupsFor(d.key, counts)
		if len(dom.Groups) > 0 {
			out = append(out, dom)
		}
	}
	return out
}

// archiveGroupsFor collects the Archive collections belonging to one domain,
// grouped and ordered by size.
func (s *server) archiveGroupsFor(domain string, counts map[string]int) []browseGroup {
	byGroup := map[string]*browseGroup{}
	unknown := countsUnmeasured(counts)
	var order []string
	for i := range iaCollections {
		c := &iaCollections[i]
		if c.Domain != domain {
			continue
		}
		key := "ia:col:" + c.Slug
		n := counts[key]
		if n == 0 && !unknown {
			continue
		}
		g, ok := byGroup[c.Group]
		if !ok {
			g = &browseGroup{Key: c.Group, Title: groupTitles[c.Group]}
			byGroup[c.Group] = g
			order = append(order, c.Group)
		}
		g.Categories = append(g.Categories, browseCategory{
			Key: key, Path: c.Domain + "/" + c.Slug, Title: c.Title, Count: n,
		})
	}
	out := make([]browseGroup, 0, len(order))
	for _, k := range order {
		g := byGroup[k]
		sort.SliceStable(g.Categories, func(i, j int) bool {
			return g.Categories[i].Count > g.Categories[j].Count
		})
		out = append(out, *g)
	}
	return out
}

// countsUnmeasured distinguishes "the background refresh has not run yet" from
// "this really is empty". Before the first refresh every count is zero, and
// hiding every category then would serve an empty tree for the first minute of
// a process's life.
func countsUnmeasured(counts map[string]int) bool { return len(counts) == 0 }

var groupTitles = map[string]string{
	"books":   "Shelves",
	"comics":  "Golden age",
	"archive": "From the Internet Archive",
	"audio":   "Recordings",
}

// handleRows fills rows. Two shapes, because two things ask:
//
//	?path=games/snes[&page=2]  one category page. Answers with that category's
//	                           items, its name, and how many there are in total
//	                           so the page can say so and offer more.
//	?key=…&key=…               a handful of rails at once, for anywhere showing
//	                           several categories side by side.
func (s *server) handleRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), browseRowItems)
	if limit < 1 || limit > 100 {
		limit = browseRowItems
	}
	page := atoiDefault(q.Get("page"), 1)
	if page < 1 || page > maxBrowsePage {
		page = 1
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	if path := q.Get("path"); path != "" {
		key, ok := s.keyForPath(path)
		if !ok {
			// A URL somebody typed, or an old link. Say so plainly rather than
			// serving an empty page that reads as a dead category.
			writeJSON(w, 404, map[string]string{"error": "no such category"})
			return
		}
		row, err := s.browseRow(ctx, key, limit, page)
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": "could not load that category"})
			return
		}
		writeJSON(w, 200, map[string]any{
			"row":   row,
			"path":  path,
			"page":  page,
			"total": s.browse.allCounts()[countKeyFor(key)],
		})
		return
	}

	keys := q["key"]
	if len(keys) == 0 {
		writeJSON(w, 400, map[string]string{"error": "missing path or key"})
		return
	}
	if len(keys) > maxRowsPerCall {
		keys = keys[:maxRowsPerCall]
	}

	rows := make([]discoverRow, len(keys))
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func(i int, k string) {
			defer wg.Done()
			row, err := s.browseRow(ctx, k, limit, 1)
			if err != nil {
				log.Printf("browse %s: %v", k, err)
				return
			}
			rows[i] = row
		}(i, k)
	}
	wg.Wait()

	// A row that failed or resolved to nothing is dropped rather than sent as
	// an empty shelf, for the same reason discover drops them: an empty rail
	// reads as a broken page.
	live := make([]discoverRow, 0, len(rows))
	for _, row := range rows {
		if len(row.Items) > 0 {
			live = append(live, row)
		}
	}
	writeJSON(w, 200, map[string]any{"rows": live})
}

// countKeyFor maps a row key onto the key its count is stored under. Game
// systems are counted by slug because that is also what the search facet and
// the `system` filter use.
func countKeyFor(key string) string {
	if id, ok := strings.CutPrefix(key, "ia:sys:"); ok {
		return id
	}
	return key
}

func (s *server) browseRow(ctx context.Context, key string, limit, page int) (discoverRow, error) {
	cacheKey := fmt.Sprintf("%s\x00%d\x00%d", key, limit, page)
	if row, ok := s.browse.row(cacheKey); ok {
		return row, nil
	}
	src, ok := s.sourceFor(key)
	if !ok {
		return discoverRow{}, fmt.Errorf("unknown row key")
	}

	var (
		row discoverRow
		err error
	)
	switch {
	case src.solr != "":
		row, err = fetchArchivePage(ctx, archiveRow{
			key: key, title: src.title, query: src.solr,
			sort: src.solrSort, mediaType: src.mediaType,
		}, limit, page)
	case src.tmdbPath != "":
		row, err = s.tmdbRow(ctx, key, src, limit, page)
	default:
		err = fmt.Errorf("row has no source")
	}
	if err != nil {
		return discoverRow{}, err
	}
	s.browse.putRow(cacheKey, row, browseRowTTL)
	return row, nil
}

func (s *server) tmdbRow(ctx context.Context, key string, src rowSource, limit, page int) (discoverRow, error) {
	if !s.tmdb.enabled() {
		return discoverRow{}, fmt.Errorf("tmdb not configured")
	}
	items := s.tmdb.list(ctx, fmt.Sprintf("%s&page=%d", src.tmdbPath, page))
	if len(items) > limit {
		items = items[:limit]
	}
	// A genre row is catalogue metadata like every other TMDB row, so a click
	// searches for the title rather than opening something. mediaType carries
	// which endpoint it came from so the search knows film from television.
	for i := range items {
		items[i].MediaType = src.mediaType
	}
	return discoverRow{Title: src.title, Key: key, Items: items}, nil
}

// ------------------------------------------------------------------ counts --

// refreshCounts asks every source how much it holds.
//
// ~80 requests, so it runs in the background on a slow drip and never on a
// request path. What it buys is the thing that makes a picker usable: somebody
// deciding whether to open "WonderSwan" can see it holds 171 games and "Mega
// Duck" holds 18, rather than finding out by opening both.
func (s *server) refreshCounts(ctx context.Context) {
	type job struct {
		key  string
		solr string
		tmdb string
	}
	var jobs []job
	for i := range gameSystems {
		sys := &gameSystems[i]
		jobs = append(jobs, job{key: sys.ID, solr: systemQuery(sys)})
	}
	for i := range iaCollections {
		c := &iaCollections[i]
		jobs = append(jobs, job{key: "ia:col:" + c.Slug, solr: c.query()})
	}
	for _, kind := range []string{"movie", "tv"} {
		for _, id := range s.tmdb.genreIDs() {
			jobs = append(jobs, job{
				key:  fmt.Sprintf("tmdb:%s:%d", kind, id),
				tmdb: fmt.Sprintf("/discover/%s?with_genres=%d&vote_count.gte=%d", kind, id, genreVoteFloor),
			})
		}
	}

	counts := make(map[string]int, len(jobs))
	var mu sync.Mutex
	// Six at a time. Gentle -- this is somebody else's API and there is no
	// hurry -- but four was not enough to finish eighty counts inside the
	// budget when the Archive was answering in sixteen seconds.
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var (
				n   int
				err error
			)
			if j.solr != "" {
				n, err = archiveCount(ctx, j.solr)
			} else {
				n, err = s.tmdbCount(ctx, j.tmdb)
			}
			if err != nil {
				return
			}
			mu.Lock()
			counts[j.key] = n
			mu.Unlock()
		}(j)
	}
	wg.Wait()
	if len(counts) > 0 {
		s.browse.putCounts(counts)
		log.Printf("browse: counted %d of %d categories", len(counts), len(jobs))
	}
}

// countLoop keeps the numbers roughly current. Catalogue sizes move by a few
// items a day, so twelve hours is far more often than the data needs and still
// costs one burst of ~80 requests.
func (s *server) countLoop() {
	for {
		// Generous, because nothing waits on this and a partial answer means a
		// category page that says "browse" where it should say how much is in
		// there. Whatever did arrive is kept either way; see refreshCounts.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		s.refreshCounts(ctx)
		cancel()
		time.Sleep(countRefreshIn)
	}
}

// countClient is deliberately NOT archiveClient.
//
// That one is tuned for the paint path and gives archive.org 20 seconds, which
// is right when somebody is waiting: past that a search has already failed them
// and a slow Archive should cost one result set, not the page. A count is the
// opposite -- nobody is waiting, it runs on a twelve-hour timer, and a number
// that arrives late is worth strictly more than no number at all.
//
// Measured while writing this: on a bad afternoon a single rows=0 query to
// archive.org took 16.4 seconds, so the shared client's 20 was close enough to
// the edge that most of the eighty counts came back empty and every category
// page said "browse" instead of "12,975 items".
var countClient = &http.Client{Timeout: 90 * time.Second}

// archiveCount asks only how many, with rows=0 -- no documents come back.
func archiveCount(ctx context.Context, q string) (int, error) {
	params := url.Values{}
	params.Set("q", q)
	params.Set("rows", "0")
	params.Set("output", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		archiveSearchAPI+"?"+params.Encode(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")

	resp, err := countClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	var body struct {
		Response struct {
			NumFound int `json:"numFound"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	return body.Response.NumFound, nil
}

func (s *server) tmdbCount(ctx context.Context, path string) (int, error) {
	if !s.tmdb.enabled() {
		return 0, fmt.Errorf("tmdb not configured")
	}
	sep := "&"
	if !strings.Contains(path, "?") {
		sep = "?"
	}
	u := fmt.Sprintf("%s%s%sapi_key=%s", tmdbBase, path, sep, s.tmdb.apiKey)
	var out struct {
		TotalResults int `json:"total_results"`
	}
	if err := s.tmdb.getJSON(ctx, u, &out); err != nil {
		return 0, err
	}
	return out.TotalResults, nil
}
