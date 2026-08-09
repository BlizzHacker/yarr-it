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

// The music domain had exactly one audio row on the landing page and it was
// LibriVox audiobooks. schema.json files an audiobook under `literature` and
// says why, so there was no music shelf at all.
func TestTheLandingPageHasMusicRows(t *testing.T) {
	byKey := map[string]archiveRow{}
	for _, r := range archiveRows {
		byKey[r.key] = r
	}
	for _, key := range []string{"ia-music-live", "ia-music-early", "ia-music-netlabels"} {
		row, ok := byKey[key]
		if !ok {
			t.Fatalf("row %q is not registered, so it never reaches the page", key)
		}
		if row.mediaType != "audio" {
			t.Errorf("row %q has mediaType %q", key, row.mediaType)
		}
		if row.title == "" || row.query == "" || row.sort == "" {
			t.Errorf("row %q is incomplete: %+v", key, row)
		}
	}
	// The audiobook row is still there and is still not music. This is here so
	// that "add a music row" is never satisfied by relabelling that one.
	if _, ok := byKey["ia-audiobooks"]; !ok {
		t.Error("the audiobooks row disappeared")
	}
}

// Every shelf query has to carry the playability gate, or the shelf fills with
// items whose only file is a ZIP -- 32 of Musopen's 34 are exactly that.
func TestEveryMusicRowRequiresSomethingPlayable(t *testing.T) {
	for _, row := range musicRows {
		if !strings.Contains(row.query, musicPlayable) {
			t.Errorf("row %q does not require a browser-playable derivative: %s",
				row.key, row.query)
		}
	}
}

// A racial slur must not be on an unauthenticated front page. Measured: the
// Great 78 Project's most-downloaded pre-1926 item is a 1916 minstrel record
// whose title is a slur, and it took the number one slot the first time the
// early-recordings query was run.
//
// This is a SHELF rule and not a search rule. discover_archive.go draws that
// line -- a search returns what the Archive has because somebody asked; a shelf
// is an offer this page makes unprompted -- and the record stays findable.
func TestTheEarlyRecordingsShelfExcludesSlurs(t *testing.T) {
	var early archiveRow
	for _, r := range musicRows {
		if r.key == "ia-music-early" {
			early = r
		}
	}
	if early.query == "" {
		t.Fatal("no early-recordings row")
	}
	for _, word := range []string{"nigger", "coon", "darkies"} {
		if !strings.Contains(early.query, word) {
			t.Errorf("the shelf query does not exclude %q; it was measured at the "+
				"top of this shelf", word)
		}
	}
	if !strings.Contains(early.query, "-title:(") {
		t.Error("the exclusion is not expressed as a negation on the title")
	}
	// It must not have leaked into the search path. Somebody looking for a
	// historical record should still find it.
	if strings.Contains(archiveMusicScope, "-title:(") {
		t.Error("a shelf-only exclusion reached the search scope; a search is a " +
			"question somebody asked, not an offer this page made")
	}
	if strings.Contains(strings.Join(adultMarkers, " "), "coon") {
		t.Error("a racial slur was added to adultMarkers, which is about " +
			"pornography and would demote the item in every search on the wrong axis")
	}
}

// The language preference on a shelf is the Solr form of the same rule search
// applies in Go, and the "or the item said nothing" half is what saves the
// catalogue: without it the live shelf drops from 290,403 candidates to 13,248.
func TestTheLiveShelfKeepsItemsThatStateNoLanguage(t *testing.T) {
	var live archiveRow
	for _, r := range musicRows {
		if r.key == "ia-music-live" {
			live = r
		}
	}
	if !strings.Contains(live.query, "-language:[* TO *]") {
		t.Fatalf("the live shelf requires a language field: %s", live.query)
	}
	if !strings.Contains(live.query, "eng") {
		t.Errorf("the live shelf states no language preference at all: %s", live.query)
	}
	// A malformed clause here is a shelf that silently does not appear.
	if musicShelfEnglishQ == "" {
		t.Error("the language term is empty, which produces `A AND  AND B`")
	}
	if strings.Contains(live.query, "AND  AND") {
		t.Errorf("malformed query: %s", live.query)
	}
}

// The netlabel shelf requires an artist. Without it the row fills with tiles
// titled `badpanda074` -- real, openable, and saying nothing at all to the
// person looking at them.
func TestTheNetlabelShelfRequiresAnArtist(t *testing.T) {
	for _, r := range musicRows {
		if r.key != "ia-music-netlabels" {
			continue
		}
		if !strings.Contains(r.query, "creator:[* TO *]") {
			t.Errorf("the netlabel shelf does not require an artist: %s", r.query)
		}
		if !strings.Contains(r.query, "licenseurl:(*creativecommons.org*)") {
			t.Error("the netlabel shelf does not check the licence per item")
		}
	}
}

// The standard discover_resolve.go set: every tile carries a verified, openable
// target or it is not published. These rows meet it by construction because
// they start from identifiers -- this pins that they still do.
func TestEveryMusicTileCarriesATarget(t *testing.T) {
	docs := []iaSearchDoc{
		{Identifier: "gd77-05-08.sbd.hicks.4982.sbeok.shnf",
			Title:     "Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08",
			Downloads: 1453564, Collection: []string{"GratefulDead", "etree"}},
		{Identifier: "78_crazy-blues_mamie-smith", Title: "Crazy Blues", Downloads: 1000},
		// Marked adult, and a shelf is an unprompted offer.
		{Identifier: "csr049", Title: "Wakka Chikka: Porn Music For The Masses", Downloads: 99},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Response struct {
				Docs []iaSearchDoc `json:"docs"`
			} `json:"response"`
		}
		body.Response.Docs = docs
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	old := archiveSearchAPI
	archiveSearchAPI = srv.URL
	defer func() { archiveSearchAPI = old }()

	for _, row := range musicRows {
		got, err := fetchArchiveRow(context.Background(), row, 24)
		if err != nil {
			t.Fatalf("row %q: %v", row.key, err)
		}
		if len(got.Items) != 2 {
			t.Fatalf("row %q returned %d tiles; the adult one should be gone",
				row.key, len(got.Items))
		}
		for _, it := range got.Items {
			if it.Play == "" {
				t.Errorf("row %q tile %q has no target, so the click goes looking "+
					"instead of opening", row.key, it.Title)
			}
			if !strings.Contains(it.Play, "archive.org/details/") {
				t.Errorf("row %q tile %q opens %q", row.key, it.Title, it.Play)
			}
			if it.MediaType != "audio" {
				t.Errorf("row %q tile %q is a %q", row.key, it.Title, it.MediaType)
			}
			if it.Poster == "" {
				t.Errorf("row %q tile %q has no artwork", row.key, it.Title)
			}
		}
		// A row every item of which already points somewhere is passed through
		// the resolver untouched. If that ever stops being true these tiles get
		// re-resolved against a scope that does not cover music and vanish.
		if !rowIsAlreadyTargeted(got) {
			t.Errorf("row %q is not self-targeted and will be re-resolved", row.key)
		}
	}
}

// resolveScope has no music branch, which is correct -- these rows never need
// resolving -- but it must not silently answer with somebody else's catalogue,
// which is how "comics" once returned emulators.
func TestMusicIsNotResolvedAgainstAnotherCatalogue(t *testing.T) {
	if scope, ok := resolveScope("audio"); ok {
		t.Errorf("a music tile would be resolved against %q", scope)
	}
}

// The shelves are built from one language table and the search filter reads the
// same one. A test that only checked the Go side would not notice the clause
// going stale.
func TestTheShelfLanguageClauseComesFromTheSharedTable(t *testing.T) {
	if musicShelfEnglish != musicLanguageClause(musicLanguageDefault) {
		t.Error("the shelf clause and the shared rule have drifted apart")
	}
	for _, spelling := range musicLanguages[musicLanguageDefault] {
		if !strings.Contains(musicShelfEnglish, spelling) {
			t.Errorf("the shelf clause omits %q", spelling)
		}
	}
}

// A music search asked for by kind must reach the music path end to end, with
// the cards a client can actually render.
func TestAMusicSearchAnswersWithMusicCards(t *testing.T) {
	stubMusicArchive(t, nil, musicDoc{
		Identifier: "gd77-05-08.sbd.hicks.4982.sbeok.shnf",
		Title:      "Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08",
		Creator:    []string{"Grateful Dead"}, MediaType: "etree",
		Venue: "Barton Hall, Cornell University", Coverage: "Ithaca, NY",
		Date: "1977-05-08T00:00:00Z", Downloads: 1453564,
		Year: json.RawMessage(`1977`), Collection: []string{"etree"},
	})
	s := newTestServer()

	rec, body, _ := doSearch(t, s,
		"/api/search?q=grateful+dead&kind=audio&minSeeders=0")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	cards, _ := body["cards"].([]any)
	if len(cards) != 1 {
		t.Fatalf("%d cards: %v", len(cards), body["cards"])
	}
	first, _ := cards[0].(map[string]any)
	music, _ := first["music"].(map[string]any)
	if music == nil {
		t.Fatal("the card carries no music object, so a client has no artist to show")
	}
	if music["artist"] != "Grateful Dead" {
		t.Errorf("artist %v", music["artist"])
	}
	if music["venue"] != "Barton Hall, Cornell University" {
		t.Errorf("venue %v", music["venue"])
	}
	if music["form"] != "concert" {
		t.Errorf("form %v", music["form"])
	}
	src, _ := body["sources"].(map[string]any)
	if src["archive"] == stageNone {
		t.Error("the archive stage reported `none`, which is the state this whole " +
			"change exists to remove")
	}
}

// A browse with no query at all. Picking Music on a TV remote sends exactly
// this, and it used to answer nothing.
func TestBrowsingMusicWithNoQueryReturnsSomething(t *testing.T) {
	stubMusicArchive(t, nil,
		musicDoc{Identifier: "a", Title: "Crazy Blues", Creator: []string{"Mamie Smith"},
			MediaType: "audio", Downloads: 10},
		musicDoc{Identifier: "b", Title: "St. Louis Blues",
			Creator: []string{"Original Dixieland Jazz Band"}, MediaType: "audio", Downloads: 20},
	)
	s := newTestServer()

	rec, body, _ := doSearch(t, s, "/api/search?kind=audio&minSeeders=0")
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200 -- a category with no words is a browse", rec.Code)
	}
	if cards, _ := body["cards"].([]any); len(cards) != 2 {
		t.Fatalf("%d cards from a music browse", len(cards))
	}
}

// The language default has to survive the whole request, not just the filter
// unit test -- and it has to be switchable from the URL.
func TestAMusicSearchDefaultsToEnglishAndCanBeTurnedOff(t *testing.T) {
	docs := []musicDoc{
		{Identifier: "a", Title: "Crazy Blues", MediaType: "audio",
			Language: []string{"English"}, Downloads: 10},
		{Identifier: "b", Title: "Dubinushka Russian Laborers Song", MediaType: "audio",
			Language: []string{"Russian"}, Downloads: 20},
	}
	stubMusicArchive(t, nil, docs...)
	s := newTestServer()

	_, body, _ := doSearch(t, s, "/api/search?kind=audio&minSeeders=0")
	if cards, _ := body["cards"].([]any); len(cards) != 1 {
		t.Fatalf("%d cards with the default filter, want the English one only", len(cards))
	}

	// Same server, same cache entry, different setting -- which is the property
	// that makes filtering in Go rather than in the query worth it.
	_, off, _ := doSearch(t, s, "/api/search?kind=audio&lang=any&minSeeders=0")
	if cards, _ := off["cards"].([]any); len(cards) != 2 {
		t.Fatalf("%d cards with lang=any; the filter is not reversible", len(cards))
	}
	_, ru, _ := doSearch(t, s, "/api/search?kind=audio&lang=ru&minSeeders=0")
	if cards, _ := ru["cards"].([]any); len(cards) != 1 {
		t.Fatalf("%d cards with lang=ru", len(cards))
	}
}

// A recording is filed under the name of the work and the artist is a separate
// field, so relevance has to see both -- otherwise "Tears" by King Oliver's
// Jazz Band lands in the -2000 band for a query naming the band.
func TestRelevanceSeesTheArtistNotOnlyTheTitle(t *testing.T) {
	terms := queryTerms("king oliver")
	withArtist := card{Title: "Tears", Instant: true,
		Music: &musicFacts{Artist: "King Oliver's Jazz Band"}}
	unrelated := card{Title: "Tears In Heaven", Instant: true, Music: &musicFacts{}}
	if relevance(withArtist, terms) <= relevance(unrelated, terms) {
		t.Errorf("the record by the band that was asked for scored %d, no better "+
			"than an unrelated title at %d",
			relevance(withArtist, terms), relevance(unrelated, terms))
	}
}

// parseFilters must not blow up on a lang it has never heard of. An
// unsatisfiable filter is indistinguishable from a broken server.
func TestAnUnknownLanguageIsNoConstraint(t *testing.T) {
	f := parseFilters(url.Values{"kind": {"audio"}, "lang": {"klingon"}})
	if f.Lang != "" {
		t.Errorf("lang=klingon resolved to %q, want no constraint", f.Lang)
	}
}
