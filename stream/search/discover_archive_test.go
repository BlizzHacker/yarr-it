package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The live outage this pins was caused by treating Archive's search API as if
// fourteen simultaneous shelf queries were free. Every request then crossed
// the shared page deadline and Games, Books, Comics, Audiobooks and Music all
// vanished together. A small gate retains parallelism without recreating that
// all-or-nothing failure.
func TestArchiveDiscoverBoundsUpstreamConcurrency(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"docs":[{"identifier":"item","title":"Item"}]}}`))
	}))
	defer srv.Close()

	oldAPI := archiveSearchAPI
	archiveSearchAPI = srv.URL
	defer func() { archiveSearchAPI = oldAPI }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows := (&server{}).archiveDiscover(ctx, 1)
	if got := int(maximum.Load()); got > archiveDiscoverConcurrency {
		t.Fatalf("Archive saw %d simultaneous shelf queries, want at most %d", got, archiveDiscoverConcurrency)
	}
	if len(rows) != len(archiveRows) {
		t.Fatalf("got %d rows, want %d", len(rows), len(archiveRows))
	}
}

// Every row must be scoped to something, or a landing shelf becomes whatever
// archive.org happens to sort first across 40 million items.
func TestEveryRowIsScopedAndLabelled(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range archiveRows {
		if r.key == "" || r.title == "" || r.query == "" || r.sort == "" {
			t.Errorf("row %q is missing a field", r.key)
		}
		if seen[r.key] {
			t.Errorf("duplicate row key %q", r.key)
		}
		seen[r.key] = true
		if !strings.Contains(r.query, "mediatype:") {
			t.Errorf("row %q is not scoped by mediatype: %s", r.key, r.query)
		}
	}
}

// The comics row once led with a complete Batman run, because the general
// `comics` collection is user-uploaded and mostly still in copyright -- and
// filtering it by year does not help, since the year describes the issues
// rather than the upload.
func TestTheComicsRowDoesNotUseTheGeneralUploadBucket(t *testing.T) {
	var row archiveRow
	for _, r := range archiveRows {
		if r.key == "ia-comics" {
			row = r
		}
	}
	if row.key == "" {
		t.Fatal("no comics row")
	}
	if strings.Contains(row.query, "collection:(comics)") {
		t.Error("comics row is back on the general upload bucket")
	}
	if !strings.Contains(row.query, "fawcett-comics") {
		t.Error("comics row should name curated golden-age publishers")
	}
}

// A game the touch player can run must open in it; anything else opens in
// theirs, which costs no bandwidth here.
func TestPlayTargetPicksTheTouchPlayerOnlyWhenACoreExists(t *testing.T) {
	nes := iaSearchDoc{Identifier: "smb", Emulator: "nes"}
	if got := playTargetFor(nes, "game"); !strings.HasSuffix(got, "#ejs") {
		t.Errorf("nes game = %q, want the touch player", got)
	}
	dos := iaSearchDoc{Identifier: "oregon", Emulator: "dosbox"}
	if got := playTargetFor(dos, "game"); strings.HasSuffix(got, "#ejs") {
		t.Errorf("dos game = %q, but EmulatorJS has no dosbox core", got)
	}
	book := iaSearchDoc{Identifier: "alice"}
	if got := playTargetFor(book, "text"); got != "https://archive.org/details/alice" {
		t.Errorf("book = %q", got)
	}
}

// A shelf is not a search.
//
// A search returns what the Archive has because somebody asked for it. A shelf
// is an offer this page makes unprompted, on a landing page with no sign-in and
// no age gate. searchArchive has always drawn that line and these rows never
// did, which did not matter while they held games and Gutenberg -- and stopped
// mattering the moment they held feature films, whose most-downloaded twenty-
// four on live archive.org include "Diary of a Nudist" and "The Naked Witch".
func TestAShelfDoesNotOfferAdultItemsUnasked(t *testing.T) {
	docs := []iaSearchDoc{
		{Identifier: "his_girl_friday", Title: "His Girl Friday", Downloads: 90},
		{Identifier: "nudist", Title: "Diary of a Nudist", Downloads: 100},
		{Identifier: "molester", Title: "The Child Molester (1964)", Downloads: 95},
		{Identifier: "clean", Title: "McLintock!", Downloads: 80},
		{Identifier: "eb", Title: "Some Scan", Collection: []string{"eroticabooks"}, Downloads: 70},
	}
	kept := []string{}
	for _, d := range docs {
		if isAdultItem(d.Title.String(), d.Identifier, d.Collection) {
			continue
		}
		kept = append(kept, d.Identifier)
	}
	if len(kept) != 2 || kept[0] != "his_girl_friday" || kept[1] != "clean" {
		t.Fatalf("shelf kept %v", kept)
	}
}

// Every Solr field is multi-valued in the schema. Typed as a plain string, one
// item catalogued with two titles failed the decode for the whole shelf.
func TestAShelfSurvivesAMultiValuedField(t *testing.T) {
	var body struct {
		Response struct {
			Docs []iaSearchDoc `json:"docs"`
		} `json:"response"`
	}
	raw := `{"response":{"docs":[
	  {"identifier":"two","title":["A Film","A Film (restored)"],
	   "emulator":["nes","nes"],"collection":"feature_films"},
	  {"identifier":"one","title":"B Film","collection":["feature_films"]}
	]}}`
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("one list-valued field failed the shelf: %v", err)
	}
	if len(body.Response.Docs) != 2 {
		t.Fatalf("got %d docs", len(body.Response.Docs))
	}
	if got := body.Response.Docs[0].Title.String(); got != "A Film" {
		t.Errorf("title = %q", got)
	}
	if got := body.Response.Docs[0].Emulator.String(); got != "nes" {
		t.Errorf("emulator = %q", got)
	}
	if len(body.Response.Docs[0].Collection) != 1 {
		t.Errorf("bare-string collection = %v", body.Response.Docs[0].Collection)
	}
}

// The film and television rows are the replacement for five TMDB rows whose
// every tile reached nothing. They must be scoped to what is free to watch AND
// to what has something to play, or they are the same mistake with better
// provenance.
func TestTheFilmRowsAreScopedToWhatCanActuallyBeWatched(t *testing.T) {
	found := 0
	for _, r := range archiveRows {
		if r.mediaType != "video" {
			continue
		}
		found++
		if !strings.Contains(r.query, "collection:(") {
			t.Errorf("row %q draws from the whole of mediatype:(movies)", r.key)
		}
		// An item with no browser-playable derivative is a details page, not a
		// film, and a tile pointing at one is a dead button with a poster.
		if !strings.Contains(r.query, "format:(MPEG4)") {
			t.Errorf("row %q does not require something playable: %s", r.key, r.query)
		}
	}
	if found == 0 {
		t.Fatal("no film row at all; the video domain has nothing behind it")
	}
}
