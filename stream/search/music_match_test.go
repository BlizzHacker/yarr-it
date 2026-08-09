package main

import "testing"

func musicCard(title, artist string, downloads int) card {
	return card{
		Title: title, Popular: downloads, Kind: domainMusic, Instant: true,
		Music: &musicFacts{Artist: artist},
	}
}

// concertCard is what the search path actually builds for an etree item: the
// venue is a separate field, not something to be recovered from the title.
func concertCard(title, artist, venue string, downloads int) card {
	c := musicCard(title, artist, downloads)
	c.Music.Venue = venue
	c.Music.Form = "concert"
	return c
}

// The Live Music Archive's title convention, undone. Scored raw, a concert is
// thirteen words of which two are the answer, so match.go's per-extra-word
// penalty drives every one of them to its floor and download count decides the
// order -- the failure match.go exists to stop, arriving from the other end.
func TestAConcertTitleReducesToTheArtist(t *testing.T) {
	cases := map[string]string{
		"Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08": "Grateful Dead",
		"Umphrey's McGee Live at Fox Theatre on 2004-02-07":                   "Umphrey's McGee",
		"moe. Live at Dar Constitution Hall on 2004-10-29":                    "moe.",
		"Godspeed You! Black Emperor Live at So What on 1999-08-08":           "Godspeed You! Black Emperor",
		// No "Live at" at all, which is how a lot of the newer uploads are
		// titled. The trailing date still goes.
		"Phish 2024-07-20": "Phish",
		// Nothing to undo.
		"House Of The Rising Sun": "House Of The Rising Sun",
		// A title that IS a date keeps its name, for the same reason match.go
		// refuses to strip noise from the front of a title.
		"1977-05-08": "1977-05-08",
	}
	for in, want := range cases {
		if got := musicWorkTitle(in); got != want {
			t.Errorf("musicWorkTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// The venue is not the work. Measured on live archive.org: "so what" returned
// three concerts recorded at a Dallas venue called So What, above everything.
func TestTheVenueIsNotTheWork(t *testing.T) {
	cards := []card{
		concertCard("Ryan Adams Live at So What on 2001-04-27", "Ryan Adams", "So What", 40000),
		concertCard("Godspeed You! Black Emperor Live at So What on 1999-08-08",
			"Godspeed You! Black Emperor", "So What", 30000),
	}
	if got := rankMusic(cards, "so what"); len(got) != 0 {
		t.Fatalf("kept %d results whose only claim is the name of the room they "+
			"were recorded in: %v", len(got), got[0].Title)
	}
	// The same venue, asked for by somebody who names the band as well. Now it
	// is a refinement rather than the entire claim, and it must work.
	got := rankMusic(cards, "ryan adams so what")
	if len(got) == 0 || got[0].Music.Artist != "Ryan Adams" {
		t.Fatalf("naming the band and the venue found %v", titlesOf(got))
	}
}

// archive.org files "Davis, Miles" as readily as "Miles Davis" and neither
// spelling is wrong. match.go's restoreArticle undoes this for "The"; nobody
// had done it for people.
func TestACommaInvertedNameIsTheSameArtist(t *testing.T) {
	cases := map[string]string{
		"Davis, Miles":     "Miles Davis",
		"Armstrong, Louis": "Louis Armstrong",
		"Smith, Bessie":    "Bessie Smith",
		// A LIST rendered with commas is not an inversion, and inverting it
		// produces a string that is neither name.
		"Bessie SMITH - Gesang, Clara SMITH - Gesang": "",
		"Parker; Gillespie":                           "",
		"":                                            "",
		", Miles":                                     "",
	}
	for in, want := range cases {
		if got := musicUninvert(in); got != want {
			t.Errorf("musicUninvert(%q) = %q, want %q", in, got, want)
		}
	}

	inverted := musicCard("Miles Ahead", "Davis, Miles", 10)
	if s := musicScore("miles davis", inverted); s < matchAccept {
		t.Errorf("a filing-order name scored %.2f against the query it obviously "+
			"answers; the bar is %.2f", s, matchAccept)
	}
}

// A recording is filed under the name of the work; the artist is a different
// field. "Tears" by King Oliver's Jazz Band is the Louis Armstrong record, and
// scoring on the title alone finds nothing at all.
func TestTheArtistIsScoredAsWellAsTheTitle(t *testing.T) {
	// The artist as musicArtist would set it: archive.org's creator for this
	// item is ["King Oliver's Jazz Band","Hardin","Armstrong"] and the card
	// carries the first.
	tears := musicCard("Tears", "King Oliver's Jazz Band", 500)
	if s := musicScore("king oliver", tears); s < matchFloor {
		t.Errorf("scored %.2f on the artist; the title says nothing about who "+
			"played it", s)
	}
	// The possessive is the common spelling for bands of this era and must not
	// cost the match: match.go folds "Oliver's" to the single token `olivers`,
	// which no query will ever produce.
	if s := musicScore("king olivers jazz band", tears); s < matchAccept {
		t.Errorf("the band's own name scored %.2f against itself", s)
	}
	// And the work is still scored, for somebody who knows the record.
	if s := musicScore("tears", tears); s < matchAccept {
		t.Errorf("the work itself scored %.2f", s)
	}
}

// A query that names both, which is how anybody asks for one particular show.
func TestAQueryNamingArtistAndVenueFindsTheShow(t *testing.T) {
	cards := []card{
		concertCard("Grateful Dead Live at Boston Garden on 1977-05-07",
			"Grateful Dead", "Boston Garden", 794005),
		concertCard("Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08",
			"Grateful Dead", "Barton Hall, Cornell University", 1453564),
	}
	got := rankMusic(cards, "grateful dead barton hall")
	if len(got) == 0 {
		t.Fatal("no results for a query naming the band and the venue")
	}
	if got[0].Title != "Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08" {
		t.Errorf("top result %q, want the Barton Hall show", got[0].Title)
	}
}

// The whole point of ranking rather than trusting download order: an exact
// artist match must beat a bigger number.
func TestTheRightArtistBeatsTheBiggerNumber(t *testing.T) {
	cards := []card{
		// A hugely popular concert that merely mentions the name in its venue.
		musicCard("Smashing Pumpkins Live at Duke Ellington Ballroom on 1995-10-23",
			"Smashing Pumpkins", 900000),
		// The record itself, barely downloaded.
		musicCard("Jig Walk", "Duke Ellington; Ellington", 300),
	}
	got := rankMusic(cards, "duke ellington")
	if len(got) == 0 {
		t.Fatal("everything was dropped")
	}
	if got[0].Title != "Jig Walk" {
		t.Errorf("top result %q with %d downloads; a ballroom named after somebody "+
			"is not a record by them", got[0].Title, got[0].Popular)
	}
}

// Among results that are equally the thing asked for, popularity is the right
// tiebreak -- it separates the canonical upload from its near-duplicates, the
// same job seeders do for a torrent.
func TestPopularityStillBreaksTiesAmongEqualAnswers(t *testing.T) {
	cards := []card{
		musicCard("Grateful Dead Live at Boston Garden on 1977-05-07", "Grateful Dead", 794005),
		musicCard("Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08",
			"Grateful Dead", 1453564),
		musicCard("Grateful Dead Live at The Centrum on 1987-04-03", "Grateful Dead", 792636),
	}
	got := rankMusic(cards, "grateful dead")
	if len(got) != 3 {
		t.Fatalf("kept %d of 3 concerts by the band that was asked for", len(got))
	}
	if got[0].Popular != 1453564 {
		t.Errorf("top result has %d downloads; among equal answers the canonical "+
			"upload should lead", got[0].Popular)
	}
}

// A three-word query has stopped being a keyword and started naming something.
// Measured on live archive.org: "kind of blue" returned three Kind Country
// concerts recorded at Blue Ribbon Pines and Blue Ox Festival, each holding two
// of the three words in unrelated halves of a long title. The album is not free
// anywhere on the service, so nothing is the honest answer.
func TestANamedQueryRequiresEveryWord(t *testing.T) {
	noise := concertCard("Kind Country Live at Blue Ribbon Pines - Wu Stage on 2018-08-17",
		"Kind Country", "Blue Ribbon Pines - Wu Stage", 2270)
	if got := rankMusic([]card{noise}, "kind of blue"); len(got) != 0 {
		t.Errorf("kept %q for \"kind of blue\"; it holds two of three words and is "+
			"a bluegrass set from Minnesota", got[0].Title)
	}
	// A record actually called that survives -- this is the one live archive.org
	// returns for the same query once the noise is gone.
	real := musicCard("Fred Rich Hotel Astor Orch Feelin Kind of Blue",
		"Fred Rich Hotel Astor Orch", 100)
	if got := rankMusic([]card{real}, "kind of blue"); len(got) != 1 {
		t.Error("a record whose title contains every word was dropped too")
	}

	// One and two-word queries keep match.go's generous floor: "zelda" is a
	// keyword, not a title, and a two-word query missing a word already scores
	// below the floor without help.
	partial := musicCard("Blue Ribbon", "Somebody", 1)
	if got := rankMusic([]card{partial}, "blue"); len(got) != 1 {
		t.Error("a one-word query stopped returning things containing that word")
	}
}

// The venue counts towards "could this be the thing at all", even though it
// cannot answer "is this the thing". A query naming a place is asking about a
// place.
func TestANamedQueryCanNameAVenue(t *testing.T) {
	show := concertCard("Grateful Dead Live at Barton Hall on 1977-05-08",
		"Grateful Dead", "Barton Hall", 100)
	elsewhere := concertCard("Grateful Dead Live at Boston Garden on 1977-05-07",
		"Grateful Dead", "Boston Garden", 900000)
	got := rankMusic([]card{elsewhere, show}, "grateful dead barton hall")
	if len(got) != 1 || got[0].Music.Venue != "Barton Hall" {
		t.Fatalf("naming the venue returned %v", titlesOf(got))
	}
}

// A browse has no query and must keep the Archive's own popularity order --
// which is the right order for a shelf and is not something to re-derive.
func TestABrowseIsLeftInPopularityOrder(t *testing.T) {
	cards := []card{musicCard("b", "x", 1), musicCard("a", "y", 2)}
	got := rankMusic(cards, "  ")
	if len(got) != 2 || got[0].Title != "b" {
		t.Errorf("a browse was reordered: %v", got)
	}
}

// A card with no music facts must not crash the scorer -- the generic archive
// path can produce one for a music kind, and a nil dereference there takes out
// the whole search.
func TestScoringSurvivesACardWithNoMusicFacts(t *testing.T) {
	plain := card{Title: "Crazy Blues", Kind: domainMusic}
	if s := musicScore("crazy blues", plain); s < matchAccept {
		t.Errorf("a card with no facts scored %.2f on its own title", s)
	}
}

// The concert case that would otherwise invent two extra words for every live
// result in the catalogue: for a concert the work IS the artist, so scoring
// "Grateful Dead Grateful Dead" would penalise every one of them.
func TestAConcertIsNotScoredAgainstItsArtistTwice(t *testing.T) {
	c := musicCard("Grateful Dead Live at Barton Hall on 1977-05-08", "Grateful Dead", 10)
	if s := musicScore("grateful dead", c); s < matchAccept {
		t.Fatalf("the band's own concert scored %.2f against the band's name", s)
	}
}
