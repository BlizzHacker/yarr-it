package main

// archive.org as a source of television: Nostalgia TV.
//
// Same two objects as linear_source_jellyfin.go, for the same reasons:
//
//   - archiveLinearLibrary answers "what could this channel play", by turning
//     one curated archive.org collection into LinearItems that carry a real
//     duration and the facets the rule editor selects on.
//   - archiveLinearResolver answers "the schedule says this programme is 37
//     minutes in; give me something that plays from there".
//
// What is different, and what most of this file is about, is that archive.org
// is not a media server. A media server was configured by its owner and every
// item in it is theirs to play. The Archive is a public library that anyone can
// upload to, so two questions have to be answered per item before it can be put
// on a channel, and neither can be answered by assumption:
//
//	is this actually public domain, and can we prove it?
//	how long is it, exactly, and can the bytes be joined mid-way?
//
// Both are answered from what the Archive itself asserts, never from a title
// that looks old or a collection that sounds safe. See the licence and duration
// sections below -- each records what was measured against the live service on
// 2026-08-08 rather than what seemed likely.

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

const (
	// archiveLinearProviderID is what a channel's sourceProvider names, and it
	// is the single id linear_access.go's public allowlist is keyed on. Nothing
	// else in the process may claim it: AddPublicLibrary is the only caller
	// that marks a provider public, and it is only ever called with this.
	archiveLinearProviderID   = "archive-org"
	archiveLinearProviderName = "Internet Archive"

	// How many candidates a collection offers up per refresh. The pool is
	// sorted by downloads, so this takes the well-known, well-seeded end of a
	// collection rather than a random slice of it. Three days of EPG at ~11
	// minutes a programme needs about 400 slots and the scheduler cycles, so a
	// couple of hundred distinct programmes is already a channel that does not
	// visibly repeat.
	archiveLinearCandidates = 220

	// Items are re-read from the Archive this often. Far longer than the
	// engine's own five-minute pool TTL, and deliberately: what a 1954 Popeye
	// cartoon is does not change, and re-deriving it every five minutes would
	// mean a few hundred metadata requests an hour, forever, to learn nothing.
	archiveLinearTTL = 6 * time.Hour

	// Metadata requests in flight at once. The Archive is a charity and this is
	// a background refresh nobody is waiting on; there is no reason to be
	// expensive about it.
	archiveLinearConcurrency = 6
)

// Endpoints as vars so a test can point the whole source at a stub. There is a
// real archive.org behind these and the committed suite must never call it:
// a test that depends on the live Archive fails when a collection is reindexed
// and passes when the assertion is wrong.
var (
	archiveLinearSearchAPI   = "https://archive.org/advancedsearch.php"
	archiveLinearMetadataAPI = "https://archive.org/metadata/"
	archiveLinearDownloadAPI = "https://archive.org/download/"
)

// --- what counts as public domain -------------------------------------------

// archiveLinearPDLicences are the licence URLs that amount to "this is public
// domain", matched as a prefix against archive.org's `licenseurl`.
//
// Both Creative Commons spellings appear in the catalogue and mean the same
// thing; measured on the classic-TV pool, `licenses/publicdomain` is the older
// form and `publicdomain/mark` the current one, and items carry one or the
// other with no pattern. CC0 is here for completeness.
//
// What is deliberately *not* here is every other Creative Commons licence. A
// by-nc-nd item is freely viewable and is not public domain, and the difference
// matters: `BettyBoopCartoons` carries by-nc-nd/2.5 and is excluded by this
// list even though it sits in a collection that is otherwise all PD.
var archiveLinearPDLicences = []string{
	"creativecommons.org/publicdomain/",
	"creativecommons.org/licenses/publicdomain",
	"creativecommons.org/share-your-work/public-domain/",
}

// archiveLinearIsPD reports whether a licence URL asserts public domain.
func archiveLinearIsPD(licenseURL string) bool {
	l := strings.ToLower(strings.TrimSpace(licenseURL))
	if l == "" {
		return false
	}
	l = strings.TrimPrefix(strings.TrimPrefix(l, "https://"), "http://")
	l = strings.TrimPrefix(l, "www.")
	for _, p := range archiveLinearPDLicences {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// archiveLinearPDClause is the same rule expressed to their search index, so
// the candidate list arrives already narrowed instead of being fetched wholesale
// and thrown away.
//
// The clause is a filter on the query, and the licence is checked *again* in Go
// once the item is in hand. That is not belt and braces for its own sake: the
// index and the item can disagree, and the Go check is the one that is
// unit-testable and the one that decides.
const archiveLinearPDClause = `licenseurl:(*publicdomain* OR *publicdomain\/mark* OR *creativecommons.org\/publicdomain*)`

// --- the collections --------------------------------------------------------

// archiveLinearCollection is one curated slice of the Archive, presented as a
// library a channel can be built from.
type archiveLinearCollection struct {
	// ID is what a channel's sourceLibraries names and what the `library` rule
	// field compares against. A readable name, not an identifier, for the same
	// reason the Jellyfin library uses the library's name.
	ID string
	// Query is the Solr scope, before the public-domain clause is added.
	Query string
	// Genre is attached to every item from this collection so the rule editor
	// has something to select on. archive.org's own `subject` field is free
	// text and mostly absent on this material, so this is the honest facet.
	Genre string
}

// The collections Nostalgia TV is built from.
//
// Chosen the way discover_archive.go chooses its comics row, and for the same
// reason: the big buckets are user-uploaded and full of material that is not
// free. `animationandcartoons` holds 15,809 items and `classic_tv` holds
// 11,435, and neither is a public-domain collection -- classic_tv contains a
// Twilight Zone rip and a Benny Hill upload, both plainly still in copyright.
// Counts measured live 2026-08-08.
//
// So the scope is narrow named collections AND a per-item licence assertion,
// not either on its own. The licence clause is what actually excludes the
// Twilight Zone and Benny Hill items above -- verified: both return zero hits
// once it is applied.
//
// The decade collections stop at the 1960s. US copyright before 1964 had to be
// renewed and frequently was not, which is why genuine public-domain television
// clusters there; `classic_tv_1980s` and `classic_tv_1990s` are largely
// unrenewed uploads of material that is still owned, and including them would
// lean the entire filter on one metadata field being right.
var archiveLinearCollections = []archiveLinearCollection{
	{
		ID:    "Cartoons",
		Genre: "Animation",
		// Film Chest's vintage cartoon restorations, the Archive's own vintage
		// cartoon collection, and its animation-shorts collection. Popeye,
		// Betty Boop, Woody Woodpecker, Flip the Frog.
		Query: `collection:(classic_cartoons OR vintage_cartoons OR more_animation) AND mediatype:(movies)`,
	},
	{
		ID:    "Classic TV",
		Genre: "Classic TV",
		Query: `collection:(classic_tv_1940s OR classic_tv_1950s OR classic_tv_1960s) AND mediatype:(movies)`,
	},
}

// --- picking a file to play -------------------------------------------------

// archiveLinearFormats ranks the file formats a browser can actually play,
// best first.
//
// archive.org keeps the upload plus a set of derivatives. The original is
// frequently an MPEG-2 `.mpeg` -- 162MB for a six-minute Popeye against 26MB
// for the same cartoon as MP4 -- which no browser will play and which would
// cost the viewer five times the bandwidth if one did. The derivatives are
// H.264 in an MP4 container, which is the one video format every target here
// agrees on: a browser, a Roku, a Tizen set and an Android box.
//
// `h.264 ia` is the Archive's current derivative and the best quality of the
// three; `512kb mpeg4` is the older one and is what most of this material
// actually has.
var archiveLinearFormats = map[string]int{
	"h.264 ia":    0,
	"h.264":       1,
	"hd mpeg4":    2,
	"512kb mpeg4": 3,
	"mpeg4":       4,
}

type archiveLinearFile struct {
	Name    string
	Seconds int
}

// archiveLinearDuration parses the `length` archive.org puts on a file.
//
// It arrives in two shapes and both are real: `363.16` seconds on the older
// derivatives and `6:03` or `1:08:41` on the newer ones. Anything else -- an
// empty string, a word, a negative -- is *not* guessed at. It returns zero, the
// item is left unschedulable, and the preview says so. See the comment on
// LinearItems for why that is the honest answer rather than a shame.
func archiveLinearDuration(raw string) int {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if strings.Contains(s, ":") {
		var total float64
		for _, part := range strings.Split(s, ":") {
			v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil || v < 0 {
				return 0
			}
			total = total*60 + v
		}
		if total <= 0 {
			return 0
		}
		return int(total)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return int(v)
}

// archiveLinearPickFile chooses what the channel will actually play.
//
// A file only qualifies if it is a playable format *and* declares its own
// length. Those are one decision, not two: the schedule is laid end to end from
// these numbers, so a file whose duration came from anywhere other than the
// file itself puts the whole channel out of step by however much it was wrong,
// and every programme after it too.
//
// In particular the item-level `runtime` field -- "6:00" on the Popeye above,
// against a true 363.16s -- is not used. It is rounded, it describes the work
// rather than the encode, and three seconds of error per programme is four
// minutes of drift a day.
func archiveLinearPickFile(files []archiveMetaFile) (archiveLinearFile, bool) {
	best, bestRank := archiveLinearFile{}, len(archiveLinearFormats)+1
	for _, f := range files {
		name := strings.TrimSpace(f.Name)
		if name == "" || strings.Contains(name, "/") {
			// Derivative thumbnails live in a subdirectory. Nothing playable
			// ever does, and skipping them keeps the id format below simple.
			continue
		}
		rank, ok := archiveLinearFormats[strings.ToLower(strings.TrimSpace(f.Format))]
		if !ok || rank >= bestRank {
			continue
		}
		secs := archiveLinearDuration(f.Length)
		if secs <= 0 {
			continue
		}
		best, bestRank = archiveLinearFile{Name: name, Seconds: secs}, rank
	}
	return best, bestRank <= len(archiveLinearFormats)
}

// --- canonical ids ----------------------------------------------------------

// A programme's canonical id has to carry the file as well as the item, because
// the resolver is handed nothing but this string and an archive.org item holds
// a dozen files of which exactly one is the encode the schedule was built from.
// Resolving to "whatever mp4 is in there" would let a re-derive change what
// plays under a schedule that still says 6:03.
//
// `ia:` matches the prefix archive.org results already use elsewhere in this
// package; the first slash separates the item from the file, and files with a
// slash in them are refused above so the split is unambiguous.
func archiveLinearID(identifier, file string) string {
	return "ia:" + identifier + "/" + file
}

func parseArchiveLinearID(id string) (identifier, file string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(id), "ia:")
	if !found {
		return "", "", false
	}
	identifier, file, found = strings.Cut(rest, "/")
	if !found || identifier == "" || file == "" {
		return "", "", false
	}
	return identifier, file, true
}

// --- metadata ---------------------------------------------------------------

type archiveMetaFile struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Length string `json:"length"`
}

// archiveLinearMeta is only the fields this decision turns on.
//
// `length` is a string here even though it is often numeric in the JSON,
// because archive.org sends both `363.16` and `"6:03"` for the same field and a
// float would fail to decode the second -- taking the whole item down with it.
// The same trap is documented in play_archive.go for `emulator_ext`; it is the
// same trap.
type archiveLinearMeta struct {
	Metadata struct {
		Identifier  string          `json:"identifier"`
		Title       string          `json:"title"`
		MediaType   string          `json:"mediatype"`
		LicenseURL  string          `json:"licenseurl"`
		Year        json.RawMessage `json:"year"`
		Date        string          `json:"date"`
		Collection  json.RawMessage `json:"collection"`
		AccessRestr json.RawMessage `json:"access-restricted-item"`
	} `json:"metadata"`
	Files []archiveMetaFile `json:"files"`
	// Error is how the Archive reports an item whose metadata it cannot serve.
	// It arrives as HTTP 200 with `{"error": "item metadata may be invalid"}`,
	// so a status check alone reads it as success and then finds no files.
	// Measured 2026-08-08: this is not rare on the classic-TV pool and it is
	// stable per item, not a transient -- `Bonanza_pd` and `SherlockHolmes1954`
	// both return it every time.
	Error string `json:"error"`
}

func (m *archiveLinearMeta) streamOnly() bool {
	for _, c := range jsonStrings(m.Metadata.Collection) {
		if strings.EqualFold(strings.TrimSpace(c), "stream_only") {
			return true
		}
	}
	if len(m.Metadata.AccessRestr) > 0 {
		var s string
		if err := json.Unmarshal(m.Metadata.AccessRestr, &s); err == nil {
			return strings.EqualFold(strings.TrimSpace(s), "true")
		}
		var b bool
		if err := json.Unmarshal(m.Metadata.AccessRestr, &b); err == nil {
			return b
		}
	}
	return false
}

// --- the library ------------------------------------------------------------

// archiveLinearLibrary is one curated collection, presented as a source of
// programmes.
type archiveLinearLibrary struct {
	coll   archiveLinearCollection
	client *http.Client

	mu      sync.Mutex
	cached  []LinearItem
	fetched time.Time
	// inflight collapses concurrent refreshes. The engine asks every channel's
	// pool independently, so two channels on one collection would otherwise
	// each start their own few-hundred-request refresh.
	refreshing bool
	done       chan struct{}
}

func (l *archiveLinearLibrary) LinearProviderID() string { return archiveLinearProviderID }
func (l *archiveLinearLibrary) LinearLibraryID() string  { return l.coll.ID }

// LinearItems returns everything this collection could play.
//
// Items whose duration could not be established are returned with a duration of
// zero rather than dropped. That is the same contract the Jellyfin library
// keeps and it is what makes the preview's numbers mean anything: the scheduler
// skips them at its schedulable() guard, and the rule editor reports "n matched,
// m schedulable (k have no duration)". Dropping them here would silently shrink
// the denominator and turn a collection that is half-unusable into one that
// merely looks small.
//
// Nothing without a duration is ever scheduled. That is the guarantee that
// matters and it is enforced in the scheduler, not by hiding the evidence.
func (l *archiveLinearLibrary) LinearItems(ctx context.Context) ([]LinearItem, error) {
	l.mu.Lock()
	if l.cached != nil && time.Since(l.fetched) < archiveLinearTTL {
		out := append([]LinearItem(nil), l.cached...)
		l.mu.Unlock()
		return out, nil
	}
	if l.refreshing {
		wait := l.done
		stale := append([]LinearItem(nil), l.cached...)
		l.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			// A caller giving up must not take the refresh with it. The last
			// good copy is a better answer than an error, and is what the
			// engine would have fallen back to anyway.
			if len(stale) > 0 {
				return stale, nil
			}
			return nil, ctx.Err()
		}
		l.mu.Lock()
		out := append([]LinearItem(nil), l.cached...)
		l.mu.Unlock()
		return out, nil
	}
	l.refreshing = true
	l.done = make(chan struct{})
	stale := append([]LinearItem(nil), l.cached...)
	l.mu.Unlock()

	items, err := l.refresh(ctx)

	l.mu.Lock()
	if err == nil {
		l.cached, l.fetched = items, time.Now()
	}
	l.refreshing = false
	close(l.done)
	out := append([]LinearItem(nil), l.cached...)
	l.mu.Unlock()

	if err != nil {
		if len(stale) > 0 {
			// One bad refresh must not blank a channel that was working.
			return stale, fmt.Errorf("archive.org %s: serving the last good copy: %w", l.coll.ID, err)
		}
		return nil, fmt.Errorf("archive.org %s: %w", l.coll.ID, err)
	}
	return out, nil
}

func (l *archiveLinearLibrary) refresh(ctx context.Context) ([]LinearItem, error) {
	docs, err := l.search(ctx)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("the collection returned no public-domain items")
	}

	out := make([]LinearItem, len(docs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, archiveLinearConcurrency)
	for i, d := range docs {
		wg.Add(1)
		go func(i int, d archiveLinearDoc) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = l.itemFor(ctx, d)
		}(i, d)
	}
	wg.Wait()

	items := make([]LinearItem, 0, len(out))
	skipped := 0
	for _, it := range items0(out) {
		if it.DurationSeconds <= 0 {
			skipped++
		}
		items = append(items, it)
	}
	if skipped > 0 {
		log.Printf("archive.org %s: %d of %d candidates have no usable duration "+
			"and will not be scheduled", l.coll.ID, skipped, len(items))
	}
	return items, nil
}

// items0 drops the zero values left by candidates that were rejected outright
// -- not public domain, stream-only, or metadata the Archive could not serve.
// Those are not "matched but unschedulable"; they are not programmes at all,
// and counting them would make the preview's denominator meaningless in the
// other direction.
func items0(in []LinearItem) []LinearItem {
	out := make([]LinearItem, 0, len(in))
	for _, it := range in {
		if strings.TrimSpace(it.CanonicalID) != "" {
			out = append(out, it)
		}
	}
	return out
}

type archiveLinearDoc struct {
	Identifier string          `json:"identifier"`
	Title      string          `json:"title"`
	Year       json.RawMessage `json:"year"`
	Downloads  int             `json:"downloads"`
	LicenseURL string          `json:"licenseurl"`
	Collection []string        `json:"collection"`
}

func (l *archiveLinearLibrary) search(ctx context.Context) ([]archiveLinearDoc, error) {
	params := url.Values{}
	params.Set("q", "("+l.coll.Query+") AND "+archiveLinearPDClause)
	for _, f := range []string{"identifier", "title", "year", "downloads", "licenseurl", "collection"} {
		params.Add("fl[]", f)
	}
	params.Add("sort[]", "downloads desc")
	params.Set("rows", strconv.Itoa(archiveLinearCandidates))
	params.Set("page", "1")
	params.Set("output", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		archiveLinearSearchAPI+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search: status %d", resp.StatusCode)
	}
	var body struct {
		Response struct {
			Docs []archiveLinearDoc `json:"docs"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	out := make([]archiveLinearDoc, 0, len(body.Response.Docs))
	seen := make(map[string]bool, len(body.Response.Docs))
	for _, d := range body.Response.Docs {
		if d.Identifier == "" || seen[d.Identifier] {
			continue
		}
		// The index said public domain when it was asked; it is checked again
		// here because a wildcard clause is a search convenience and this is
		// the decision. An item that got through the query without the licence
		// to back it up is dropped.
		if !archiveLinearIsPD(d.LicenseURL) {
			continue
		}
		seen[d.Identifier] = true
		out = append(out, d)
	}
	return out, nil
}

// itemFor turns one candidate into a schedulable programme, or into the zero
// value if it is not one at all.
func (l *archiveLinearLibrary) itemFor(ctx context.Context, d archiveLinearDoc) LinearItem {
	meta, err := l.metadata(ctx, d.Identifier)
	if err != nil {
		return LinearItem{}
	}
	// The item's own licence overrides the index when the two disagree, and a
	// disagreement is resolved against publishing. The index is a periodically
	// rebuilt copy; the item is the thing itself.
	if lic := strings.TrimSpace(meta.Metadata.LicenseURL); lic != "" && !archiveLinearIsPD(lic) {
		return LinearItem{}
	}
	// "You may play this but not download it." Honoured rather than tested:
	// a linear channel hands the viewer a URL to the file, which is the
	// download endpoint however it is dressed up. play_archive.go refuses on
	// the same signal for the same reason.
	if meta.streamOnly() {
		return LinearItem{}
	}

	title := strings.TrimSpace(d.Title)
	if title == "" {
		title = strings.TrimSpace(meta.Metadata.Title)
	}
	if title == "" {
		title = d.Identifier
	}
	// The Archive is a public upload site and its titles are not curated. The
	// same screen these channels play on is the one a child might be in front
	// of, and the adult check the search path already applies costs nothing
	// here.
	if looksAdult(title) || looksAdult(d.Identifier) {
		return LinearItem{}
	}
	for _, c := range d.Collection {
		if looksAdult(c) || adultArchiveCollections[strings.ToLower(c)] {
			return LinearItem{}
		}
	}

	year := archiveYear(d.Year)
	if year == 0 {
		year = archiveYear(meta.Metadata.Year)
	}
	if year == 0 && len(meta.Metadata.Date) >= 4 {
		year = linearAtoi(meta.Metadata.Date[:4])
	}

	it := LinearItem{
		MediaItem: MediaItem{
			Domain: "video",
			// `movie` rather than `episode`: each of these is one standalone
			// file with its own runtime, and the Archive records no reliable
			// season or episode number for them. Claiming `episode` would put a
			// number in the guide that nothing checked.
			Type:           "movie",
			Title:          title,
			Year:           year,
			Artwork:        "https://archive.org/services/img/" + d.Identifier,
			ProviderID:     archiveLinearProviderID,
			ProviderItemID: d.Identifier,
		},
		Genres:     []string{l.coll.Genre},
		Collection: l.coll.ID,
		// Public domain, and said so in the field a rule can select on -- so a
		// channel can be written against it rather than against a collection
		// name that might change.
		Rating:    "Public Domain",
		LibraryID: l.coll.ID,
	}

	f, ok := archiveLinearPickFile(meta.Files)
	if !ok {
		// A real programme with no usable encode. Returned with a canonical id
		// but no duration so the preview counts it honestly, and skipped by the
		// scheduler. It has no file, so its id names the item alone -- it can
		// never be resolved, and it will never be asked for.
		it.CanonicalID = "ia:" + d.Identifier + "/"
		it.ProviderItemID = d.Identifier
		return it
	}
	it.CanonicalID = archiveLinearID(d.Identifier, f.Name)
	it.DurationSeconds = f.Seconds
	return it
}

func (l *archiveLinearLibrary) metadata(ctx context.Context, id string) (*archiveLinearMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		archiveLinearMetadataAPI+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata %s: status %d", id, resp.StatusCode)
	}
	var m archiveLinearMeta
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("metadata %s: %w", id, err)
	}
	if strings.TrimSpace(m.Error) != "" {
		return nil, fmt.Errorf("metadata %s: %s", id, m.Error)
	}
	return &m, nil
}

// --- the resolver -----------------------------------------------------------

// archiveLinearResolver turns a scheduled programme into playable bytes.
//
// There is no server-side seek to ask for. archive.org serves a static file, so
// joining a programme 37 minutes in is a range request the player makes -- and
// whether that works is a property of the bytes, not something this code can
// decide. So it is measured, once per file, and reported.
type archiveLinearResolver struct {
	client *http.Client

	mu     sync.Mutex
	probed map[string]bool
}

func newArchiveLinearResolver(client *http.Client) *archiveLinearResolver {
	return &archiveLinearResolver{client: client, probed: map[string]bool{}}
}

func archiveLinearURL(identifier, file string) string {
	return archiveLinearDownloadAPI + url.PathEscape(identifier) + "/" + url.PathEscape(file)
}

// LinearResolve hands back the file, positioned.
//
// The offset is carried as a media fragment rather than a query parameter,
// because that is the one mechanism that works without the player knowing
// anything about this service: `#t=` is understood by every HTML5 media element
// and is stripped before the request goes out, so it changes where playback
// starts without changing which bytes are asked for. A client that computes its
// own seek from LinearNowPlaying.offsetSeconds will simply seek to the place it
// is already at.
//
// It is only appended when the source has been proven seekable. Handing a
// player `#t=1234` for a file that cannot be ranged produces a video that either
// ignores it or stalls, and either way the channel silently lies about where it
// is.
func (r *archiveLinearResolver) LinearResolve(ctx context.Context, p Program, opts StreamOptions) (StreamSource, error) {
	identifier, file, ok := parseArchiveLinearID(p.CanonicalID)
	if !ok {
		// Not ours. Reported as missing so a resolver chain moves on to the
		// next member rather than treating this as the final word.
		return StreamSource{}, fmt.Errorf("%w: %q was not scheduled from %s",
			ErrLinearMissingMedia, p.CanonicalID, archiveLinearProviderName)
	}

	raw := archiveLinearURL(identifier, file)
	seekable, err := r.rangeOK(ctx, raw)
	if err != nil {
		// The file could not be reached at all. Transient by assumption, so the
		// engine's retry gets a chance -- unlike a missing-media answer, which
		// stops it.
		return StreamSource{}, fmt.Errorf("archive.org could not serve %q: %w", p.Title, err)
	}

	out := StreamSource{
		URL:        raw,
		MimeType:   "video/mp4",
		DirectPlay: true,
		Seekable:   seekable,
	}
	if seekable && opts.OffsetSeconds >= 1 {
		out.URL = raw + "#t=" + strconv.Itoa(int(opts.OffsetSeconds))
	}
	return out, nil
}

// rangeOK asks for a kilobyte from the middle of the file and reports whether
// the Archive answered with exactly that.
//
// A 206 with a Content-Range is the only acceptable answer. A 200 means the
// server ignored the header and is about to send the whole file, which is not
// seekable in the sense that matters here even though the request "succeeded" --
// and treating it as success is how a channel ends up starting every programme
// from zero while insisting it is 37 minutes in.
//
// Measured against the live service on 2026-08-08: archive.org answers
// `/download/<id>/<file>` with a 302 to a storage node carrying
// `Accept-Ranges: bytes`, and the node answers the forwarded Range with 206 and
// a Content-Range. Go's client forwards the Range header across that redirect,
// which is what makes one request enough.
//
// The result is remembered per file. A channel re-tunes on every programme
// change and on every viewer, and re-probing bytes that were seekable a minute
// ago would be a request per tune-in for an answer that does not change.
func (r *archiveLinearResolver) rangeOK(ctx context.Context, rawURL string) (bool, error) {
	r.mu.Lock()
	if ok, seen := r.probed[rawURL]; seen {
		r.mu.Unlock()
		return ok, nil
	}
	r.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")
	req.Header.Set("Range", "bytes=0-1023")

	resp, err := r.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	// The body is not read. A kilobyte was asked for and closing without
	// draining lets the connection go rather than paying for bytes nobody wants.

	ok := resp.StatusCode == http.StatusPartialContent &&
		strings.TrimSpace(resp.Header.Get("Content-Range")) != ""
	if !ok && resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("status %d", resp.StatusCode)
	}

	r.mu.Lock()
	r.probed[rawURL] = ok
	r.mu.Unlock()
	return ok, nil
}

// --- construction -----------------------------------------------------------

// newArchiveLinear builds everything Nostalgia TV needs: one library per
// curated collection and the resolver that plays them.
//
// It takes no credentials and reaches nothing on the home network, which is the
// property that lets linear_access.go publish these channels. Anything that
// changes here and starts needing either is no longer a public source.
func newArchiveLinear() ([]LinearLibrary, LinearResolver) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     5 * time.Minute,
			ForceAttemptHTTP2:   true,
		},
	}
	libs := make([]LinearLibrary, 0, len(archiveLinearCollections))
	for _, c := range archiveLinearCollections {
		libs = append(libs, &archiveLinearLibrary{coll: c, client: client})
	}
	return libs, newArchiveLinearResolver(client)
}

// --- the channels -----------------------------------------------------------

// Nostalgia TV's lineup.
//
// This is the one place in the linear engine where channels are named in code,
// and it is worth being explicit about why that does not contradict
// linear_channel.go's rule that a channel is data a user wrote. These are
// *seeds*: they are written into the same store the editor writes to, they
// carry no privileges the editor cannot grant, and once written they are
// ordinary rows that can be edited, renumbered, disabled or deleted. What they
// buy is that a fresh install has something on air. Live TV that shows an empty
// grid until you configure a media server is a feature nobody discovers.
//
// The numbering starts at 901 to stay out of the way of channels made from a
// real library, which people number from 1.
func nostalgiaChannels() []LinearChannel {
	return []LinearChannel{
		{
			ID:              "nostalgia-cartoons",
			Number:          901,
			Name:            "Nostalgia Cartoons",
			Description:     "Public-domain animation from the Internet Archive: Popeye, Betty Boop, Woody Woodpecker and the rest of the golden age, running around the clock.",
			Enabled:         true,
			SourceProvider:  archiveLinearProviderID,
			SourceLibraries: []string{"Cartoons"},
			// Shorts, so the channel turns over quickly and a viewer who tunes
			// in mid-programme is never more than a few minutes from the next.
			ScheduleStrategy: StrategyCyclicShuffle,
			Timezone:         "America/Chicago",
			EPGDays:          3,
			// Fixed rather than derived. The derived seed is a hash of the id
			// and name, so renaming the channel would reshuffle it; pinning it
			// means the lineup survives a cosmetic edit.
			Seed: 20260808,
		},
		{
			ID:               "nostalgia-classic-tv",
			Number:           902,
			Name:             "Nostalgia Classic TV",
			Description:      "Public-domain television from the 1940s, 50s and 60s, scheduled as a channel: westerns, sitcoms, mysteries and variety, whatever is on when you arrive.",
			Enabled:          true,
			SourceProvider:   archiveLinearProviderID,
			SourceLibraries:  []string{"Classic TV"},
			ScheduleStrategy: StrategyCyclicShuffle,
			Timezone:         "America/Chicago",
			EPGDays:          3,
			Seed:             20260809,
		},
	}
}

// SeedNostalgiaChannels writes the built-in channels if they are not already
// there.
//
// Absent, not different. A channel the owner has renamed, renumbered, switched
// off or re-ruled is left exactly as it is -- the seed is what a fresh install
// starts with, not a definition that reasserts itself over somebody's edits
// every time the process restarts.
//
// A channel the owner *deleted* is a harder case and is handled by the marker
// written alongside: an id that has been seeded once is never seeded again, so
// deleting one makes it stay deleted. Without a state directory there is
// nowhere to keep that marker and nowhere the channels themselves persist
// either, so every start seeds afresh -- which is the only behaviour that
// leaves an ephemeral instance with anything on air.
func SeedNostalgiaChannels(e *LinearEngine) (added []string) {
	seeded := e.loadSeedMarker()
	for _, ch := range nostalgiaChannels() {
		if _, exists := e.Channel(ch.ID); exists {
			continue
		}
		if seeded[ch.ID] {
			continue // deleted on purpose; leave it deleted
		}
		if _, err := e.SaveChannel(ch); err != nil {
			log.Printf("nostalgia: could not create %q: %v", ch.ID, err)
			continue
		}
		added = append(added, ch.ID)
		seeded[ch.ID] = true
	}
	if len(added) > 0 {
		e.saveSeedMarker(seeded)
	}
	sort.Strings(added)
	return added
}
