package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// withoutArchiveScope removes one domain's archive.org scope for the duration
// of a test and puts it back afterwards.
//
// It exists because there is no longer a domain without one. Two tests are
// about what happens when archive.org cannot answer for a kind at all, and both
// used to reach that state by naming `audio` -- which was true, and was the
// defect this work removes. Producing the state is the honest replacement for
// finding it lying around.
func withoutArchiveScope(t *testing.T, kind string) {
	t.Helper()
	old, had := archiveScopes[kind]
	delete(archiveScopes, kind)
	t.Cleanup(func() {
		if had {
			archiveScopes[kind] = old
		}
	})
}

// The whole reason this domain exists. Measured on live before any of this:
// kind=audio returned zero cards and `sources: {"archive":"none"}` for every
// query and for the browse, because there was no entry here to find.
func TestMusicHasAnArchiveScopeAtAll(t *testing.T) {
	for _, spelling := range []string{"music", "audio", "album", "song", "songs"} {
		kind := canonicalKind(spelling)
		scope, ok := scopeFor(kind)
		if !ok {
			t.Fatalf("kind=%s (canonical %q) has no archive.org scope; "+
				"the whole domain falls through to the indexers", spelling, kind)
		}
		if scope == "" {
			t.Fatalf("kind=%s resolved to an empty scope, which searches everything", spelling)
		}
	}
}

// A music scope that reaches the general audio dumping grounds is how somebody
// else's rip of a copyrighted album ends up on this site -- the exact failure
// archiveCuratedFilm was written to stop for video. Each of these was measured
// and rejected for a stated reason; this pins the rejection.
func TestMusicScopeExcludesTheBroadAudioCollections(t *testing.T) {
	for _, banned := range []string{"opensource_audio", "audio_music", "unlockedrecordings"} {
		if strings.Contains(archiveMusicScope, banned) {
			t.Errorf("the music scope names %q, which is broad and user-contributed; "+
				"see the measurements at the top of music.go", banned)
		}
	}
	// Every leg must carry the playability gate, or a shelf fills with items
	// whose only file is a ZIP.
	if !strings.Contains(archiveMusicScope, musicPlayable) {
		t.Error("the music scope does not require a browser-playable derivative")
	}
	// The copyright boundary is a stated fact, not a mood.
	if !strings.Contains(archiveMusicScope, "year:[1877 TO 1925]") {
		t.Error("the early-recordings leg has lost its year gate; see musicScopeEarly")
	}
}

// A music query has to ask about the performer, not only about the name of the
// record. Measured: title-only returns 4 items for "louis armstrong" and
// title-or-creator returns 399.
func TestMusicQueryAsksAboutTheArtist(t *testing.T) {
	got := musicClauseFor("louis armstrong")
	if !strings.Contains(got, "title:(louis AND armstrong)") {
		t.Errorf("no title clause in %q", got)
	}
	if !strings.Contains(got, "creator:(louis AND armstrong)") {
		t.Errorf("no creator clause in %q -- a recording is filed under the work, "+
			"and the artist lives in a different field", got)
	}
	if !strings.Contains(got, " OR ") {
		t.Errorf("the two clauses are not alternatives in %q", got)
	}
}

// An empty query is a browse, not a search for nothing. Without this, picking
// Music with no words typed is a Solr syntax error.
func TestMusicBrowseIsTheWholeScope(t *testing.T) {
	if got := musicQueryFor("   "); got != archiveMusicScope {
		t.Errorf("browse query = %q, want the bare scope", got)
	}
	if got := musicQueryFor("miles davis"); !strings.Contains(got, archiveMusicScope) {
		t.Errorf("a search dropped the scope: %q", got)
	}
}

// --- language ----------------------------------------------------------------

// The measurement this whole rule turns on: 95% of the Live Music Archive
// states no language. Treating silence as "not English" would take it from
// 293,163 items to 13,248, which from the front looks exactly like the empty
// domain this work started from.
func TestAnItemThatStatesNoLanguageIsKept(t *testing.T) {
	if !musicLanguageAllows("en", "") {
		t.Fatal("an item that says nothing about its language was dropped; " +
			"absence of a claim is not a claim of absence")
	}
	if !musicLanguageAllows("fr", "  ") {
		t.Fatal("whitespace is still silence")
	}
}

func TestLanguageFilterKeepsWhatItSaysItKeeps(t *testing.T) {
	cases := []struct {
		want, stated string
		keep         bool
	}{
		{"en", "English", true},
		{"en", "eng", true},
		{"en", "en", true},
		{"en", "German", false},
		{"en", "fre", false},
		// Multi-valued, which archive.org genuinely sends: one Boston Public
		// Library item is catalogued ["eng","jpn"] and is both.
		{"en", "eng, jpn", true},
		{"ja", "eng, jpn", true},
		{"de", "eng, jpn", false},
		{"fr", "French", true},
		{"fr", "fra", true},
		// The filter switched off keeps everything, including what it cannot read.
		{"", "Klingon", true},
		// A language nothing knows about is not a reason to hide an item.
		{"en", "", true},
	}
	for _, c := range cases {
		if got := musicLanguageAllows(c.want, c.stated); got != c.keep {
			t.Errorf("musicLanguageAllows(%q, %q) = %v, want %v", c.want, c.stated, got, c.keep)
		}
	}
}

// "Turn it off" has to actually work, in every spelling somebody would reach
// for. A filter that cannot be switched off does not narrow a catalogue, it
// replaces it.
func TestTheLanguageFilterCanBeTurnedOff(t *testing.T) {
	for _, off := range []string{"any", "ANY", "all", "off", "*"} {
		if got := normaliseLanguage(off); got != "" {
			t.Errorf("lang=%q resolved to %q, want no constraint", off, got)
		}
	}
	if got := normaliseLanguage(""); got != musicLanguageDefault {
		t.Errorf("an absent lang resolved to %q, want the default %q", got, musicLanguageDefault)
	}
	for _, spelling := range []string{"en", "EN", "english", "English", "eng"} {
		if got := normaliseLanguage(spelling); got != "en" {
			t.Errorf("lang=%q resolved to %q, want en", spelling, got)
		}
	}
	// Somebody else's language, honoured rather than ignored.
	if got := normaliseLanguage("ja"); got != "ja" {
		t.Errorf("lang=ja resolved to %q", got)
	}
}

// The Solr form and the Go form are two expressions of one rule, and the whole
// risk is that they drift. This pins the property that matters: whatever the Go
// rule keeps, the clause must name.
func TestTheSolrLanguageClauseMatchesTheGoRule(t *testing.T) {
	clause := musicLanguageClause("en")
	if clause == "" {
		t.Fatal("no clause for English")
	}
	for _, spelling := range musicLanguages["en"] {
		if !strings.Contains(clause, spelling) {
			t.Errorf("the clause %q never mentions %q, which the Go rule accepts",
				clause, spelling)
		}
		if !musicLanguageAllows("en", spelling) {
			t.Errorf("the Go rule rejects %q, which the clause accepts", spelling)
		}
	}
	// "or the item said nothing", which is the half that saves the catalogue.
	if !strings.Contains(clause, "-language:[* TO *]") {
		t.Errorf("the clause %q does not keep items with no language field", clause)
	}
	if musicLanguageClause("") != "" {
		t.Error("a switched-off filter produced a clause")
	}
}

// The default is per-domain. Defaulting every domain to English would delete
// most of the games and comics catalogues to enforce a preference nobody
// expressed about them -- their language field is empty on almost every item.
func TestOnlyMusicDefaultsToEnglish(t *testing.T) {
	music := parseFilters(url.Values{"kind": {"audio"}})
	if music.Lang != "en" {
		t.Errorf("music defaulted to lang=%q, want en", music.Lang)
	}
	for _, kind := range []string{"game", "comic", "video", "literature", "image"} {
		f := parseFilters(url.Values{"kind": {kind}})
		if f.Lang != "" {
			t.Errorf("kind=%s defaulted to lang=%q; only music has a language default",
				kind, f.Lang)
		}
	}
	// An explicit request is honoured for any domain.
	if got := parseFilters(url.Values{"kind": {"video"}, "lang": {"fr"}}).Lang; got != "fr" {
		t.Errorf("an explicit lang=fr on video resolved to %q", got)
	}
	if got := parseFilters(url.Values{"kind": {"audio"}, "lang": {"any"}}).Lang; got != "" {
		t.Errorf("lang=any on music resolved to %q, want no constraint", got)
	}
}

// The filter has to survive the round trip through a real card list, because
// that is where it is actually applied.
func TestTheLanguageFilterDropsOnlyWhatDeclaresAnotherLanguage(t *testing.T) {
	cards := []card{
		{Title: "Grateful Dead Live at Barton Hall", Kind: "music", Instant: true,
			Sources: []source{{Magnet: "x"}}, Music: &musicFacts{Artist: "Grateful Dead"}},
		{Title: "House Of The Rising Sun", Kind: "music", Instant: true,
			Sources: []source{{Magnet: "y"}}, Music: &musicFacts{Language: "English"}},
		{Title: "Dubinushka", Kind: "music", Instant: true,
			Sources: []source{{Magnet: "z"}}, Music: &musicFacts{Language: "Russian"}},
	}
	f := parseFilters(url.Values{"kind": {"music"}, "minSeeders": {"0"}})
	got := titlesOf(f.apply(cards))
	if len(got) != 2 {
		t.Fatalf("kept %v, want the two that are English or silent", got)
	}
	for _, title := range got {
		if title == "Dubinushka" {
			t.Error("a Russian-language item survived lang=en")
		}
	}

	off := parseFilters(url.Values{"kind": {"music"}, "lang": {"any"}, "minSeeders": {"0"}})
	if n := len(off.apply(cards)); n != 3 {
		t.Fatalf("lang=any kept %d of 3; the filter is not reversible", n)
	}
}

// A default filter nobody can see is a catalogue quietly replaced. The facet is
// built from the unfiltered set, so the number says what turning it off buys.
func TestTheFacetsSayWhatTheLanguageFilterIsCosting(t *testing.T) {
	cards := []card{
		{Title: "a", Music: &musicFacts{Language: "English"}},
		{Title: "b", Music: &musicFacts{Language: "eng"}},
		{Title: "c", Music: &musicFacts{}},
		{Title: "d", Music: &musicFacts{Language: "German"}},
	}
	got := map[string]int{}
	for _, fc := range buildFacets(cards).Languages {
		got[fc.Value] = fc.Count
	}
	if got["en"] != 2 {
		t.Errorf("en counted %d, want 2 (both spellings)", got["en"])
	}
	if got["de"] != 1 {
		t.Errorf("de counted %d, want 1", got["de"])
	}
	if got[languageUnstated] != 1 {
		t.Errorf("unstated counted %d, want 1 -- it is a value, not a gap", got[languageUnstated])
	}
	// Nothing about a film or a ROM should grow a language facet.
	if fs := buildFacets([]card{{Title: "Zelda"}}).Languages; len(fs) != 0 {
		t.Errorf("a non-music result set produced language facets %v", fs)
	}
}

// --- cards --------------------------------------------------------------------

func TestAConcertCardCarriesWhatMakesAConcertMeanSomething(t *testing.T) {
	docs := []musicDoc{{
		Identifier: "gd77-05-08.sbd.hicks.4982.sbeok.shnf",
		Title:      "Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08",
		Creator:    []string{"Grateful Dead"},
		MediaType:  "etree",
		Venue:      "Barton Hall, Cornell University",
		Coverage:   "Ithaca, NY",
		Date:       "1977-05-08T00:00:00Z",
		Downloads:  1453564,
		Year:       json.RawMessage(`1977`),
		Collection: []string{"GratefulDead", "etree", "stream_only"},
	}}
	cards := musicCards(docs)
	if len(cards) != 1 {
		t.Fatalf("got %d cards", len(cards))
	}
	c := cards[0]
	if c.Music == nil {
		t.Fatal("no music facts at all; the card is a title and a picture")
	}
	if c.Music.Artist != "Grateful Dead" {
		t.Errorf("artist %q", c.Music.Artist)
	}
	if c.Music.Form != "concert" {
		t.Errorf("form %q, want concert -- a show is not a single", c.Music.Form)
	}
	if c.Music.Venue != "Barton Hall, Cornell University" {
		t.Errorf("venue %q", c.Music.Venue)
	}
	if c.Music.Place != "Ithaca, NY" {
		t.Errorf("place %q", c.Music.Place)
	}
	// The date identifies the show. Two Grateful Dead cards differ by nothing
	// else, so the time-of-day archive.org staples on has to go.
	if c.Music.Date != "1977-05-08" {
		t.Errorf("date %q, want 1977-05-08", c.Music.Date)
	}
	if c.Kind != domainMusic || !sameDomain(c.Kind, "audio") {
		t.Errorf("kind %q does not resolve to the music domain", c.Kind)
	}
	if len(c.Groups) != 1 || c.Groups[0] != "music" {
		t.Errorf("groups %v -- the Music chip filters on this", c.Groups)
	}
	if !c.Instant {
		t.Error("a hosted result was not marked instant")
	}
	if len(c.Sources) != 1 {
		t.Fatalf("%d sources; two identical archive.org sources have nothing to "+
			"order them by and Best would vary between requests", len(c.Sources))
	}
	if !strings.HasSuffix(c.Sources[0].Magnet, "#music") {
		t.Errorf("target %q does not ask for the track list", c.Sources[0].Magnet)
	}
	if c.Sources[0].Source != "Live Music Archive" {
		t.Errorf("source label %q", c.Sources[0].Source)
	}
}

func TestAnEarlyRecordingCardNamesItsArtistAndCollection(t *testing.T) {
	cards := musicCards([]musicDoc{{
		Identifier: "78_house-of-the-rising-sun_josh-white_gbia0001628b",
		Title:      "House Of The Rising Sun",
		Creator:    []string{"Josh White and his Guitar"},
		MediaType:  "audio",
		Language:   []string{"English"},
		Year:       json.RawMessage(`1942`),
		Collection: []string{"78rpm", "georgeblood", "audio_music"},
	}})
	if len(cards) != 1 {
		t.Fatalf("got %d cards", len(cards))
	}
	c := cards[0]
	if c.Music.Form != "release" {
		t.Errorf("form %q, want release", c.Music.Form)
	}
	if c.Music.Artist != "Josh White and his Guitar" {
		t.Errorf("artist %q", c.Music.Artist)
	}
	if c.Music.Language != "English" {
		t.Errorf("language %q", c.Music.Language)
	}
	if c.Sources[0].Source != "Great 78 Project" {
		t.Errorf("source label %q -- it should name what kind of recording this is",
			c.Sources[0].Source)
	}
	// The artist goes where a game card puts its console, so an existing client
	// shows it with no change.
	if c.Platform != "Josh White and his Guitar" {
		t.Errorf("platform %q", c.Platform)
	}
}

// archive.org's creator is a list far more often in this corpus than anywhere
// else: a 78 label routinely names the singer, the band, the composer and the
// lyricist. Declaring it a string fails the decode for the whole page.
func TestAMultiValuedCreatorDoesNotFailThePage(t *testing.T) {
	var docs struct {
		Response struct {
			Docs []musicDoc `json:"docs"`
		} `json:"response"`
	}
	body := `{"response":{"docs":[
	  {"identifier":"a","title":"Tears","creator":["King Oliver's Jazz Band","Hardin","Armstrong"]},
	  {"identifier":"b","title":["Two","Titles"],"creator":"Solo"},
	  {"identifier":"c","title":"Third","creator":null,"language":["eng","jpn"]}
	]}}`
	if err := json.Unmarshal([]byte(body), &docs); err != nil {
		t.Fatalf("one oddly-catalogued item took the whole page down: %v", err)
	}
	cards := musicCards(docs.Response.Docs)
	if len(cards) != 3 {
		t.Fatalf("got %d cards, want 3", len(cards))
	}
	byKey := map[string]card{}
	for _, c := range cards {
		byKey[c.Key] = c
	}
	if got := byKey["ia:a"].Music.Artist; got != "King Oliver's Jazz Band" {
		t.Errorf("artist %q, want the first value -- the one their own page leads with", got)
	}
	if got := byKey["ia:b"].Title; got != "Two" {
		t.Errorf("title %q, want the first of two", got)
	}
	if got := byKey["ia:c"].Music.Language; got != "eng, jpn" {
		t.Errorf("language %q, want both", got)
	}
}

// Nothing about being a music result exempts an item from the check every other
// shelf and search applies. The netlabel scene includes a release titled, in
// full, "Porn Music For The Masses".
func TestAdultMusicIsStillMarkedAdult(t *testing.T) {
	cards := musicCards([]musicDoc{{
		Identifier: "csr049",
		Title:      "Various Artists - Wakka Chikka: Porn Music For The Masses Volume 1",
		MediaType:  "audio",
	}})
	if len(cards) != 1 || !cards[0].Adult {
		t.Fatal("an adult-titled release was not marked adult, so it reaches an " +
			"unauthenticated browse untagged")
	}
}

// --- the fetch ----------------------------------------------------------------

// stubMusicArchive points the music search at a local server. The committed
// suite must never call the real archive.org: a test that depends on it fails
// when a collection is reindexed and passes when the assertion is wrong.
func stubMusicArchive(t *testing.T, seen *url.Values, docs ...musicDoc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.URL.Query()
		}
		var out musicResponse
		out.Response.NumFound = len(docs)
		out.Response.Docs = docs
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	t.Cleanup(func() { archiveSearchAPI = old })
}

func TestTheMusicSearchAsksForWhatACardNeeds(t *testing.T) {
	var seen url.Values
	stubMusicArchive(t, &seen, musicDoc{Identifier: "x", Title: "Crazy Blues"})

	s := newTestServer()
	if _, err := s.searchArchiveMusic(context.Background(), "crazy blues"); err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, want := range []string{"creator", "venue", "coverage", "date", "language", "licenseurl"} {
		found := false
		for _, f := range seen["fl[]"] {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the query never asks for %q, so no card can carry it", want)
		}
	}
	if !strings.Contains(seen.Get("q"), "creator:(") {
		t.Errorf("query %q asks only about titles", seen.Get("q"))
	}
	// The language is deliberately NOT in the upstream query: one fetch has to
	// serve every setting, or the first person to search decides for everybody.
	if strings.Contains(seen.Get("q"), "language:") {
		t.Errorf("the language filter reached the upstream query (%q); it belongs "+
			"in the card filter, where it costs no traffic and cannot poison the cache",
			seen.Get("q"))
	}
}

// Music results must not be served out of the generic archive.org cache or the
// other way round: the two paths ask different questions of the same catalogue,
// and one answer served as the other silently halves a music search.
func TestMusicAndGenericArchiveResultsDoNotShareACacheKey(t *testing.T) {
	if musicCacheKey("miles davis") == archiveCacheKey("miles davis", "music") {
		t.Fatal("music and generic archive.org results collide in the cache")
	}
	if got := archiveSuggestKey("miles davis", "audio"); got != musicCacheKey("miles davis") {
		t.Errorf("type-ahead reads %q for a music query; it would miss every warm "+
			"answer and re-ask the Archive on every keystroke", got)
	}
	if got := archiveSuggestKey("zelda", "game"); got != archiveCacheKey("zelda", "game") {
		t.Errorf("a game query was routed to the music cache (%q)", got)
	}
}

// The one entry point both callers use. If these ever disagree, a search and
// the keystroke that preceded it fill the same cache with different answers.
func TestTheDomainDispatcherSendsMusicToTheMusicSearch(t *testing.T) {
	var seen url.Values
	stubMusicArchive(t, &seen, musicDoc{Identifier: "x", Title: "Crazy Blues",
		Creator: []string{"Mamie Smith"}})

	// A fresh server per spelling. Sharing one means the second call is answered
	// from cache without a request going out, which would make a dispatcher that
	// routes only the first spelling correctly look like one that routes them all.
	for _, spelling := range []string{"music", "audio", "songs"} {
		seen = nil
		s := newTestServer()
		cards, err := s.searchArchiveDomain(context.Background(), "crazy blues", spelling)
		if err != nil {
			t.Fatalf("kind=%s: %v", spelling, err)
		}
		if len(cards) == 0 || cards[0].Music == nil {
			t.Fatalf("kind=%s produced no music-shaped card", spelling)
		}
		if !strings.Contains(seen.Get("q"), "creator:(") {
			t.Errorf("kind=%s took the generic path: %q", spelling, seen.Get("q"))
		}
	}
}
