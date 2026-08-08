package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Making a tile tell the truth.
//
// THE PROBLEM, MEASURED
//
// On 2026-08-08 the live landing page carried 340 tiles across 15 rows. Every
// one of them looked identical: cover art, a title, a verb, a hover state. 168
// of them worked. The other 172 -- every tile on the five TMDB rows and the
// three IGDB rows -- reached nothing at all, and 171 of those returned literally
// zero results rather than a poor one.
//
// The cause is one line of design, stated in a comment above the renderer:
// "Clicking one runs an ordinary search for its title". A tile arrives holding a
// real identity -- an IGDB id, a TMDB id -- and throws it away in favour of a
// fuzzy text search on the display name. So the tile can never be more certain
// than the search is, and the search is run AFTER the person has already
// committed to the click.
//
// Two different failures were hiding behind the same empty grid, and they need
// different answers:
//
//	(a) The search could not find things that ARE there. That is match.go: the
//	    query was a quoted phrase, and archive.org's copy of A Link to the Past
//	    is titled "Legend Of Zelda, The A Link To The Past ( USA) SNES ROM".
//
//	(b) Some tiles could never work at all. Astro Bot, Kingdom Come:
//	    Deliverance II and Donkey Kong Bananza are current console games with no
//	    free legal source. A row called "Top rated games" filled with those is a
//	    row of dead buttons by construction, and no amount of matching fixes it.
//
// THIS FILE IS THE ANSWER TO (b)
//
// Every catalogue tile is resolved BEFORE the page is built, and comes out in
// one of three states rather than two. The third state is the whole reason this
// file is not a one-liner.
//
//	direct     something was found and the tile carries its address. The click
//	           opens that item. The identity is carried through instead of
//	           discarded, which is the fix for (a) as well.
//	unchecked  nothing was found HERE, but a source that could hold it exists
//	           and was not asked. Nothing is known, so nothing is claimed: the
//	           tile is published offering to go and look, and says so.
//	dead       every source that could hold it was asked and none does. Removed.
//
// COLLAPSING `unchecked` INTO `dead` IS A BUG, AND IT IS ONE I SHIPPED
//
// The first version of this file had two states and treated "archive.org does
// not have it" as "nothing has it". That is only true on an instance with no
// torrent indexer. On Wade's, it meant the five TMDB rows were dropped at
// runtime -- measured 0/20 each -- during a Prowlarr outage I had been told
// about in advance. Those tiles resolve perfectly well through torrents; the
// backend was simply down for an afternoon.
//
// A page that permanently deletes five shelves because a backend blinked is a
// worse version of the defect this task started from. The original bug was a
// tile that claimed something it could not deliver; deleting on an outage is a
// tile that delivers something it refuses to claim. Both are the page lying
// about what it knows, and the fix for both is the same: publish what is known,
// state what is not, and never let a transient fact become a permanent one.
//
// So `dead` requires a COMPLETED NEGATIVE from every configured source. An
// unreachable source produces no answer, and no answer is not "no".
//
// WHY THE INDEXER IS ASKED ONCE AND NOT ONCE PER TILE
//
// archive.org answers about twenty-four titles in a single Solr query, so it is
// asked about every tile -- that is what archiveTitleBatch is for.
//
// A torrent indexer cannot be used that way, and the reason is measured in this
// repository rather than assumed: fanout.go records that five cold searches in
// a row degraded Prowlarr from 13 of 42 indexers answering to 0 of 42, because
// each fan-out queues behind the last. One search is one fan-out across every
// indexer. A landing page of 172 catalogue tiles would be 172 fan-outs every
// time the three-hour cache expired, which does not resolve a page -- it takes
// the indexer down, and then reports every tile as dead because it did.
//
// So the indexer is asked the one question that scales and that actually
// decides something: are you there. That is enough, because the only decision
// it is needed for is whether "archive.org does not have this" means "nothing
// has this". Beyond that, results the server ALREADY holds are consulted --
// the search cache and whatever the warmer has filled -- which costs nothing
// upstream and can only ever promote a tile, never condemn one.

// resolveBatch is how many titles go into one Solr query. Chosen so the query
// string stays comfortably inside anything that might proxy it, and so a single
// upstream failure costs one part of one row rather than the row.
const resolveBatch = 12

// resolveRows is how many docs to ask for per batch. Each title can legitimately
// have several copies -- regions, revisions, a Genesis bootleg alongside the
// SNES original -- and the right one is not always the most downloaded, so this
// has to be generous enough that the winner is present to be ranked.
const resolveRowsPerBatch = 300

// resolveTimeout bounds the whole pass. Discover already has its own budget;
// this exists so a slow Archive costs the landing page its catalogue rows
// rather than the landing page.
const resolveTimeout = 25 * time.Second

// resolveLookups is how many questions archive.org is asked at once.
//
// It is a limit rather than "as many as there are rows", and it is not about
// politeness. Every row resolving concurrently, each in two batches, put
// sixteen simultaneous queries on advancedsearch.php -- and under that it stops
// erroring and starts answering 200 with numFound 0. A batch that comes back
// empty is indistinguishable from a batch whose titles are genuinely not there,
// so the shelf simply loses those tiles, and the SAME page rebuilt a minute
// later had a different number of them: measured 2026-08-08, "Retro classics"
// came out with 2, 4 and 6 tiles on three consecutive builds of identical
// input. Asked four at a time it is stable, and the whole pass still finishes
// in about six seconds.
//
// A channel rather than a library: this is one counter with one use.
var resolveLookups = make(chan struct{}, 4)

func acquireLookup(ctx context.Context) bool {
	select {
	case resolveLookups <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func releaseLookup() { <-resolveLookups }

// resolveScope maps a tile's declared media type onto the archive.org scope
// that could actually satisfy it.
//
// A film is NOT resolved against the games scope, which is what the client did
// by omission: a click sent no `kind`, and an absent kind means the emulator
// scope, so every film tile searched a catalogue of ROMs. That is why a film
// tile returned zero rather than a wrong answer -- it was asking the wrong
// catalogue a question it could not answer.
//
// A mediaType this does not recognise gets NO scope and therefore no tiles,
// which is the same rule scopeFor already follows and for the same reason:
// silently searching the wrong catalogue is how "comics" returned emulators.
func resolveScope(mediaType string) (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(mediaType))
	if raw == "" {
		return "", false
	}
	// The archive.org shelves label their items "text", which the shared
	// vocabulary has no alias for -- deliberately, since a book and a comic are
	// both text and the distinction matters everywhere else. Those shelves
	// never reach here (they already carry targets), but a scope that quietly
	// answered "video" for them would be waiting for the day one does.
	if raw == "text" {
		return archiveScopes["literature"], true
	}
	switch canonicalDomain(raw) {
	case "game":
		return archiveScope, true
	case "video":
		return archiveCuratedFilm, true
	case "literature":
		return archiveScopes["literature"], true
	case "comic":
		return archiveScopes["comic"], true
	}
	return "", false
}

// archiveCuratedFilm is the film catalogue a SHELF may draw from, and it is
// deliberately much narrower than the one a SEARCH returns.
//
// The difference is the difference between "what does the Archive have" and
// "what does this site offer", and the plain `mediatype:(movies)` scope answers
// only the first. Resolved against it, the film rows filled with three kinds of
// thing that all score a perfect title match and none of which belong on a
// shelf -- every one of these was a real result while checking this file:
//
//	spider-man-no-way-home-2021_202202   opensource_movies, community upload
//	interstellar-2014_202409             the same
//	parasiteASL2                         an ASL dictionary entry for the WORD
//
// The community uploads are somebody's rip of a film still in copyright. Aside
// from what that makes this page, they are removed constantly, so a tile
// pointing at one is a working button that stops working -- the delayed version
// of exactly the failure this whole change exists to remove.
//
// So a shelf draws only from the Archive's own curated free-film collections,
// the same way the books shelf draws from Gutenberg rather than from controlled
// digital lending. Measured 2026-08-08: 21,346 feature films, 11,910 cartoons,
// 7,880 Prelinger, 7,651 classic TV, 2,715 shorts, 1,105 silents -- all of them
// free to watch now and none of them going anywhere.
//
// The format requirement stays: mediatype:(movies) also holds posters and
// stills, and an item with no browser-playable derivative is a details page,
// not a film.
const archiveCuratedFilm = `collection:(feature_films OR silent_films OR ` +
	`animationandcartoons OR short_films OR classic_tv OR prelinger) ` +
	`AND mediatype:(movies) AND format:(MPEG4)`

// The three states a tile can be published in. `direct` is the empty string so
// that the common, best case adds nothing to the response.
const (
	tileDirect    = ""
	tileFound     = "found"
	tileUnchecked = "unchecked"
)

// indexerState is what the torrent backend is doing, asked once per build.
//
// The three values are not collapsible into "working / not working", for the
// same reason handleHealth refuses to collapse them: `absent` is permanent and
// makes archive.org the entire catalogue, so a tile it cannot serve really is
// dead. `unreachable` is temporary and makes the same tile merely unknown. A
// page that cannot tell those apart deletes shelves during an outage.
type indexerState struct {
	Configured bool   `json:"configured"`
	Reachable  bool   `json:"reachable"`
	Detail     string `json:"detail,omitempty"`
}

// canAnswer reports whether this backend might hold something not on
// archive.org -- which is true whenever it is configured, whether or not it is
// answering right now. An unreachable indexer has not said no.
func (i indexerState) canAnswer() bool { return i.Configured }

// indexerHealth asks the backend the one question a landing page can afford:
// are you there.
//
// One request, bounded, and never fatal. This is the same probe /api/health
// makes, deliberately -- two different answers to "is Prowlarr up" on the same
// page would be worse than none.
func (s *server) indexerHealth(ctx context.Context) indexerState {
	if s.apiKey == "" {
		return indexerState{
			Detail: "no torrent indexer is configured, so the Internet Archive " +
				"is the whole catalogue on this instance",
		}
	}
	st := indexerState{Configured: true}

	ctx, cancel := context.WithTimeout(ctx, indexerProbeDeadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.prowlarrURL+"/api/v1/health", nil)
	if err != nil {
		st.Detail = "the torrent indexer could not be asked"
		return st
	}
	req.Header.Set("X-Api-Key", s.apiKey)
	resp, err := prowlarrClient.Do(req)
	if err != nil {
		st.Detail = "the torrent indexer is not answering, so some titles " +
			"cannot be checked until it is back"
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		st.Detail = fmt.Sprintf(
			"the torrent indexer answered %d, so some titles cannot be checked",
			resp.StatusCode)
		return st
	}
	st.Reachable = true
	return st
}

// indexerProbeDeadline matches fanout.go's reasoning about the same backend:
// this is a small local call over the tunnel that answers in milliseconds or is
// not going to answer, and a landing page must not wait on it.
const indexerProbeDeadline = 5 * time.Second

// resolvedTarget is one answer: the archive.org item a tile will open.
type resolvedTarget struct {
	identifier string
	title      string
	emulator   string
	downloads  int
	score      float64
}

// resolveDiscoverRows fills in a real target for every tile that has none, and
// removes the tiles and rows that have nothing behind them.
//
// Rows whose items already carry a target -- the archive.org shelves, which are
// built from real identifiers in the first place -- are passed through
// untouched. They were never the problem.
func (s *server) resolveDiscoverRows(ctx context.Context, rows []discoverRow,
	idx indexerState) []discoverRow {

	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	out := make([]discoverRow, len(rows))
	var wg sync.WaitGroup
	for i, row := range rows {
		if rowIsAlreadyTargeted(row) {
			out[i] = row
			continue
		}
		wg.Add(1)
		go func(i int, row discoverRow) {
			defer wg.Done()
			out[i] = s.resolveRow(ctx, row, idx)
		}(i, row)
	}
	wg.Wait()

	live := make([]discoverRow, 0, len(out))
	for _, r := range out {
		if len(r.Items) > 0 {
			live = append(live, r)
		}
	}
	return live
}

// rowIsAlreadyTargeted reports whether every item in a row already points at
// something real.
func rowIsAlreadyTargeted(row discoverRow) bool {
	if len(row.Items) == 0 {
		return false
	}
	for _, it := range row.Items {
		if it.Play == "" {
			return false
		}
	}
	return true
}

// resolveRow resolves one shelf and returns it holding what it can honestly
// offer -- which is not the same as what it can deliver, and the difference is
// the point. A tile nothing here can deliver is still published when something
// ELSE could, because this side has not established otherwise.
func (s *server) resolveRow(ctx context.Context, row discoverRow,
	idx indexerState) discoverRow {

	kind := rowMediaType(row)
	scope, scoped := resolveScope(kind)

	found := map[string]resolvedTarget{}
	if scoped {
		wanted := make([]wantedWork, 0, len(row.Items))
		for _, it := range row.Items {
			wanted = append(wanted, wantedWork{title: it.Title, year: it.Year})
		}
		found = s.resolveTitles(ctx, wanted, scope)
	}

	kept := make([]discoverItm, 0, len(row.Items))
	for _, it := range row.Items {
		switch {
		case len(found) > 0 && hasTarget(found, it.Title):
			t := found[canonicalTitle(it.Title)]
			it.State = tileDirect
			it.Play = archiveTargetFor(t)
			it.Source = "archive.org"

		case s.alreadyHeld(it.Title, kind):
			// Something already in this server's memory answers for it -- a
			// previous search, or the warmer. No target is published: a torrent
			// result is a set of releases to choose between, not one address,
			// and choosing on somebody's behalf at build time is how you hand
			// them a CAM rip. What IS published is the fact that the search
			// behind this tile finds something, and it is warm, so it is fast.
			it.State = tileFound
			it.Source = "indexer"

		case idx.canAnswer():
			// Not here, and the one place that could still hold it has not been
			// asked. This is the state that must not be collapsed into `dead`:
			// every TMDB row is made of these, and deleting them is how five
			// shelves disappeared over an afternoon's outage.
			it.State = tileUnchecked

		default:
			// Asked everything there is to ask. Astro Bot on an instance with
			// no indexer really is a dead button, and it goes.
			continue
		}
		kept = append(kept, it)
	}
	row.Items = kept
	return row
}

func hasTarget(found map[string]resolvedTarget, title string) bool {
	_, ok := found[canonicalTitle(title)]
	return ok
}

// alreadyHeld reports whether this server already holds a result that answers
// for a title -- from an earlier search or from the warmer.
//
// Free, in the sense that matters: it touches no upstream at all. It can only
// promote a tile from `unchecked` to `found`, never condemn one, so a cold
// cache costs accuracy in the safe direction.
// The year is deliberately NOT part of the lookup. localCards requires every
// word of the query to appear in a card's title, and a release is titled
// "Interstellar", not "Interstellar 2014" -- so including the year finds
// nothing at all. Precision comes from matchScore below, where it belongs.
func (s *server) alreadyHeld(title string, kind string) bool {
	for _, c := range s.localCards(title, canonicalDomain(kind), localHeldLimit) {
		if len(c.Sources) == 0 {
			continue
		}
		if matchScore(title, c.Title) >= matchAccept {
			return true
		}
	}
	return false
}

// localHeldLimit is small on purpose: this is asking "is there anything", not
// "give me everything", and it runs once per tile under a read lock the search
// path also wants.
const localHeldLimit = 12

// wantedWork is a tile's whole identity as far as this can use it: what it is
// called and when it came out. The year is not decoration -- it is the only
// thing that separates two works with the same name, and without it the film
// row resolved Parasite (2019) onto Parasite (1982) and The Rookie (2018, a
// police series) onto The Rookie (1990, a Clint Eastwood film).
type wantedWork struct {
	title string
	year  int
}

// freeCatalogueHorizon is the newest release year an item that declares NO year
// of its own may answer for.
//
// The reasoning is about the catalogue rather than about matching. Everything
// browser-playable on archive.org is old: emulated console and computer
// software, public-domain film, Gutenberg texts. Nothing published in the last
// twenty years is free there, so when an item names no year and the work is
// recent, the shared title is a coincidence and not a copy -- which is exactly
// what "Cocoon (19xx)(Hotline)", an Amiga demo, was against Cocoon (2023), and
// what "dispatch" on the Commodore 64 was against Dispatch (2025).
//
// A tile with no year of its own is not held to this: the catalogue simply did
// not say, and refusing on that basis would punish the tile for the catalogue's
// gap rather than for anything about the item.
const freeCatalogueHorizon = 2006

// nonFeatureCollections hold things ABOUT a work rather than the work.
//
// A trailer is titled with the film's name and nothing else, so no amount of
// title matching can tell it apart -- checked against live archive.org, a
// perfect match for "The Shawshank Redemption" turned out to be an item in
// `movie_trailers`, and one for "The Lord of the Rings: The Return of the King"
// was a games-press preview reel in `videogameprev`. The collection is the only
// place either of them says so.
var nonFeatureCollections = map[string]bool{
	"movie_trailers":          true,
	"movie_trailers_unsorted": true,
	"trailers":                true,
	"videogameprev":           true,
	"gamevideos":              true,
	"movieposters":            true,
	"coverart":                true,
	"albumart":                true,
}

// machineEraEnd is the last year a machine received commercial releases, or 0
// where the question does not apply.
//
// This exists because of one wrong answer that nothing else could catch: an
// archive.org item titled exactly "Paper Mario", declaring `emulator: nes`, was
// offered as Paper Mario -- which is an N64 game from 2000. The item is an NES
// homebrew demake. The title is identical, neither side declares a year, and
// the only thing that says they are different works is that the machine was
// discontinued five years before the game came out.
//
// Machines whose catalogue genuinely spans decades -- MS-DOS, Flash, arcade --
// are absent on purpose rather than given a wide range: an absent entry means
// "this tells us nothing", which is the truth for them.
var machineEraEnds = map[string]int{
	"nes": 1995, "snes": 1999, "gb": 1998, "gbc": 2002, "gba": 2008,
	"n64": 2002, "gamecube": 2007,
	"genesis": 1997, "sega32": 1996, "sms": 1996, "gamegear": 1997,
	"saturn": 2000, "dreamcast": 2002,
	"psx": 2005, "ps2": 2013, "psp": 2014,
	"atari2600": 1992, "atari5200": 1984, "atari7800": 1992, "lynx": 1995,
	"tg16": 1995, "supergrafx": 1991,
	"wonderswan": 2003, "wonderswan-color": 2003,
	"neo-geo-pocket": 2001, "neo-geo-pocket-color": 2001,
	"c64": 1994, "vic-20": 1985, "cpet": 1982,
	"colecovision": 1985, "intellivision": 1990, "msx": 1995,
	"amiga": 1996,
}

// machineEraSlack is how far past a machine's commercial life a title may still
// be claimed for it. Homebrew and late unlicensed releases are real, and a
// couple of years of slack costs nothing -- the case this defends against is
// off by five.
const machineEraSlack = 2

// machineCouldHold reports whether a machine was still receiving releases when
// a work came out.
func machineCouldHold(emulator string, year int) bool {
	if year <= 0 {
		return true
	}
	plat, ok := archivePlaySystems[strings.ToLower(strings.TrimSpace(emulator))]
	if !ok {
		return true
	}
	end, ok := machineEraEnds[plat.Platform]
	if !ok {
		return true
	}
	return year <= end+machineEraSlack
}

// aboutRatherThanTheThing reports whether an item describes a work instead of
// being it -- a trailer, a preview, a cheat disc, a soundtrack rip.
//
// The identifier is read as well as the title, and that is the point: an
// uploader routinely writes a clean title and puts the qualifier in the
// identifier. "the-godfather-trailer-hd" is titled, in full, "The Godfather".
func aboutRatherThanTheThing(d archiveDoc) bool {
	if decompose(d.Title.String()).Derivative {
		return true
	}
	for _, w := range words(d.Identifier) {
		if derivativeMarkers[w] {
			return true
		}
	}
	for _, c := range d.Collection {
		if nonFeatureCollections[strings.ToLower(strings.TrimSpace(c))] {
			return true
		}
	}
	return false
}

// yearsAgree decides whether two dates can describe the same work.
//
// One year of slack, because a catalogue's date is the first release anywhere
// and an upload's is usually the release in its own territory -- Chrono Trigger
// is 1995 everywhere and 1996 in Europe, and neither is wrong.
func yearsAgree(want, got int, recentAllowed bool) bool {
	if want <= 0 {
		// The catalogue did not say. Nothing to contradict.
		return true
	}
	if got > 0 {
		d := want - got
		if d < 0 {
			d = -d
		}
		return d <= 1
	}
	return recentAllowed || want <= freeCatalogueHorizon
}

// rowMediaType is what the row holds, taken from its items rather than from its
// name. A row's own key says how it was chosen ("games-top", "trending"), not
// what kind of thing is on it.
func rowMediaType(row discoverRow) string {
	for _, it := range row.Items {
		if it.MediaType != "" {
			return it.MediaType
		}
	}
	return ""
}

// archiveTargetFor is the URL a resolved tile opens.
//
// Identical to the one the archive.org shelves already use, deliberately: a
// resolved catalogue tile and a native archive tile must behave the same, or
// the two halves of the page drift apart. `#ejs` asks for our own player, which
// is the only one with touch controls.
func archiveTargetFor(t resolvedTarget) string {
	details := "https://archive.org/details/" + t.identifier
	if ejsCoreFor(t.emulator) != "" {
		return details + "#ejs"
	}
	return details
}

// resolveTitles asks archive.org about many titles at once and returns the best
// answer for each, keyed by canonical title.
//
// Only answers at or above matchAccept are returned. That bar is the whole
// point: a tile may not be drawn on the strength of "something with some of
// those words exists".
func (s *server) resolveTitles(ctx context.Context, wanted []wantedWork, scope string) map[string]resolvedTarget {
	found := make(map[string]resolvedTarget, len(wanted))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for start := 0; start < len(wanted); start += resolveBatch {
		end := start + resolveBatch
		if end > len(wanted) {
			end = len(wanted)
		}
		batch := wanted[start:end]
		wg.Add(1)
		go func(batch []wantedWork) {
			defer wg.Done()
			titles := make([]string, 0, len(batch))
			for _, w := range batch {
				titles = append(titles, w.title)
			}
			docs, err := archiveLookup(ctx, titles, scope)
			if err != nil {
				// An Archive that did not answer is not an Archive with
				// nothing. But a tile is a promise, and a promise cannot be
				// made on an unanswered question, so these titles simply do
				// not appear this time round. The next rebuild will ask again.
				log.Printf("discover resolve: %v", err)
				return
			}
			for _, want := range batch {
				best, ok := pickBest(want, docs)
				if !ok {
					continue
				}
				mu.Lock()
				if cur, seen := found[canonicalTitle(want.title)]; !seen || best.score > cur.score {
					found[canonicalTitle(want.title)] = best
				}
				mu.Unlock()
			}
		}(batch)
	}
	wg.Wait()
	return found
}

// pickBest chooses the copy of `want` that a click should open.
//
// Score first, downloads second, and in that order for the reason the whole of
// match.go exists: sorting by downloads alone is what offered an MS-DOS fan
// hack as Super Mario World. Among items that ARE the game, download count is
// the right tiebreak -- it separates the canonical upload from the five
// near-duplicates.
func pickBest(want wantedWork, docs []archiveDoc) (resolvedTarget, bool) {
	best := resolvedTarget{}
	ok := false
	for _, d := range docs {
		title := strings.TrimSpace(d.Title.String())
		if title == "" || d.Identifier == "" {
			continue
		}
		score := matchScore(want.title, title)
		if score < matchAccept {
			continue
		}
		// A trailer, a preview reel or a cheat disc carries the work's exact
		// title and is not the work. Nothing in the title says so; the
		// identifier and the collection do.
		if aboutRatherThanTheThing(d) {
			continue
		}
		// An NES item cannot be an N64 game, whatever it is called.
		if !machineCouldHold(d.Emulator.String(), want.year) {
			continue
		}
		// The name is not enough, and this is where the film row was getting
		// it wrong: Parasite (2019) resolved onto Parasite (1982), and The
		// Rookie (2018) onto a 1990 film, both on a perfect title match.
		//
		// The item's own year is taken from whichever source has one. Their
		// `year` field is frequently absent and occasionally nonsense -- an
		// upload of The Rookie declares 1065 -- while the title very often
		// carries the year the uploader cared about, in brackets.
		got := archiveYear(d.Year)
		if got <= 0 || got > time.Now().Year()+1 {
			got = titleYear(title)
		}
		if !yearsAgree(want.year, got, false) {
			continue
		}
		if ok && (score < best.score || (score == best.score && d.Downloads <= best.downloads)) {
			continue
		}
		best = resolvedTarget{
			identifier: d.Identifier, title: title, emulator: d.Emulator.String(),
			downloads: d.Downloads, score: score,
		}
		ok = true
	}
	return best, ok
}

// archiveLookup runs one batched Solr query.
func archiveLookup(ctx context.Context, titles []string, scope string) ([]archiveDoc, error) {
	clause := archiveTitleBatch(titles)
	if clause == "" {
		return nil, nil
	}
	if !acquireLookup(ctx) {
		return nil, ctx.Err()
	}
	defer releaseLookup()

	params := url.Values{}
	params.Set("q", clause+" AND "+scope)
	for _, f := range []string{"identifier", "title", "emulator", "downloads", "year", "collection"} {
		params.Add("fl[]", f)
	}
	params.Set("rows", fmt.Sprint(resolveRowsPerBatch))
	params.Set("page", "1")
	params.Set("output", "json")
	params.Add("sort[]", "downloads desc")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		archiveSearchAPI+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")

	resp, err := archiveClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("archive.org lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive.org lookup: status %d", resp.StatusCode)
	}

	var out archiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("archive.org lookup: %w", err)
	}
	return out.Response.Docs, nil
}
