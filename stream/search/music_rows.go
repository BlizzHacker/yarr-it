package main

// Music on the landing page.
//
// The standard these have to meet was set by discover_resolve.go and it is not
// negotiable: every tile carries a verified, openable target, or it is not
// published. A row of names that resolve to nothing is the defect that file
// exists to remove, and adding three more of them in a different medium would
// be a straight regression.
//
// These rows meet it the same way the games and film rows do, by construction
// rather than by checking afterwards: each is a Solr query that returns
// IDENTIFIERS, so the tile IS the item and the click opens it. Nothing here
// starts from a name and goes looking.
//
// The one audio row that existed before this was `ia-audiobooks`, which is
// LibriVox. An audiobook is not music -- schema.json files it under
// `literature` and explains why -- so the music domain had no shelf at all.
//
// EVERY ROW BELOW WAS RUN AGAINST THE LIVE SERVICE AND ITS FIRST THIRTY-SIX
// RESULTS READ. That is not diligence for its own sake; two of the three needed
// changing because of what came back, and both changes are recorded on the row
// they belong to.

// musicShelfMP3 is the playability gate, shared with the search scope. See
// musicPlayable in music.go for why an item with no derivative is a details
// page rather than a record.
const musicShelfMP3 = musicPlayable

// musicShelfEnglish is the language preference, in its Solr form.
//
// A shelf is the one place the filter belongs in the query rather than in Go:
// it is built once every three hours and there is no per-request setting to
// respect, so there is no cache to poison and nothing to switch off. Search
// does it the other way round, in Go, and music.go says why at length.
//
// "or the item said nothing" is carried here exactly as it is in the Go rule.
// Without it this clause would take the live-concert shelf from 290,403
// candidates to 13,248, because ninety-five per cent of the Live Music Archive
// simply never filled the field in.
var musicShelfEnglish = musicLanguageClause(musicLanguageDefault)

// musicShelfClean keeps racial slurs off an unauthenticated front page.
//
// THIS IS NOT A SEARCH FILTER AND MUST NEVER BECOME ONE. discover_archive.go
// draws the distinction and it is the right one: a search returns what the
// Archive has because somebody asked for it; a shelf is an offer this page
// makes, unprompted, to whoever arrives. The 1916 minstrel record this was
// written for is a real historical document and stays findable by anybody who
// goes looking for it.
//
// It exists because of a measurement, not a worry. The Great 78 Project's
// most-downloaded item published before 1926 is a Harry C. Browne record whose
// title is a racial slur, and it took the number one slot on the early-recordings
// shelf the first time that query was run; "All Coons Look Alike to Me" was
// nineteenth. Neither is caught by isAdultItem, which is about pornography and
// is a different axis entirely -- and widening `adultMarkers` to cover this
// would file a historical record as porn and demote it in every search, which
// is both wrong and invisible.
//
// So it is a clause on the shelf query and nowhere else. The words are the ones
// that actually appear in this corpus, and each is chosen to be a word that
// cannot be part of an innocent title -- the same rule adultMarkers follows.
// Verified against live archive.org: both items above return zero hits with
// this clause applied and are still reachable by search without it.
const musicShelfClean = `-title:(nigger OR niggers OR coon OR coons OR ` +
	`darkies OR darky OR darkie OR chink OR wop OR kike)`

// musicShelfEnglishQ is musicShelfEnglish as a term that can be concatenated
// into a query, or a clause that matches everything when the filter is off.
//
// A separate name because the default could in principle be "any", and pasting
// an empty string into `A AND  AND B` produces a query that fails rather than a
// query that matches everything. The failure mode of a malformed Solr query
// here is a shelf that silently does not appear, which is exactly the class of
// thing this file is about.
var musicShelfEnglishQ = func() string {
	if musicShelfEnglish == "" {
		return "*:*"
	}
	return musicShelfEnglish
}()

// musicRows are the shelves.
//
// They are appended to archiveRows rather than written into it, because that
// file is being edited elsewhere at the time of writing; the two are equivalent
// and the literal is the better home. See the init below.
var musicRows = []archiveRow{
	{
		// The Live Music Archive: 290,403 concerts that pass the playability
		// gate, from bands that permit taping. This is the single best thing the
		// Internet Archive has for a music section and nothing here was pointed
		// at it before.
		//
		// The row is measurably dominated by the Grateful Dead -- eleven of the
		// first thirty-six -- and that is left alone. It is not a bug in the
		// query; it is what the collection is, because they are the band that
		// made this collection exist. Suppressing them would be this page having
		// an opinion about what somebody wants to hear.
		key: "ia-music-live", title: "Live concerts, taped and free", mediaType: "audio",
		query: `mediatype:(etree) AND ` + musicShelfMP3 + ` AND ` + musicShelfEnglishQ,
		sort:  "downloads desc",
	},
	{
		// Shellac whose US copyright has expired: Rhapsody in Blue, Crazy Blues,
		// St. Louis Blues, Sousa, Caruso. See musicScopeEarly for why the year
		// boundary is 1925 and not "pre-1930".
		//
		// This started as `collection:(georgeblood)` alone and had to be widened.
		// The Great 78 Project on its own is a digitisation programme rather than
		// a curated shelf, and its most-downloaded pre-1926 items are the ones
		// somebody linked to from an article -- the first thirty were dominated
		// by obscure dance sides. Adding the general `78rpm` collection brings in
		// the material people actually recognise while the year gate keeps the
		// modern uploads out.
		key: "ia-music-early", title: "Early recordings", mediaType: "audio",
		query: `collection:(georgeblood OR 78rpm) AND mediatype:(audio) ` +
			`AND year:[1877 TO 1925] AND ` + musicShelfMP3 +
			` AND ` + musicShelfEnglishQ + ` AND ` + musicShelfClean,
		sort: "downloads desc",
	},
	{
		// The netlabel scene: modern music published by its own labels under
		// Creative Commons. 46,119 releases that name an artist.
		//
		// `creator:[* TO *]` is not tidiness. Without it the shelf fills with
		// tiles titled `badpanda074`, `badpanda089`, `badpanda098` -- real,
		// openable releases whose uploader left the title as the catalogue
		// number and named nobody, so the tile says nothing at all to the person
		// looking at it. Requiring an artist costs 7,424 of 53,543 candidates
		// and every one of them is a tile that would have been a shrug.
		//
		// No language clause. Seven per cent of this collection states a
		// language, so the clause would decide almost nothing -- and unlike the
		// 78s, most of this is instrumental, where the question does not apply.
		key: "ia-music-netlabels", title: "Freely licensed music", mediaType: "audio",
		query: `collection:(netlabels) AND mediatype:(audio) ` +
			`AND licenseurl:(*creativecommons.org*) AND ` + musicShelfMP3 +
			` AND creator:[* TO *]`,
		sort: "downloads desc",
	},
}

// Registration.
//
// archiveRows is discover_archive.go's list and s.archiveDiscover walks it, so
// appending here is what puts these on the landing page. Additive: nothing
// existing changes, the rows land after the games, film and books shelves, and
// deleting this init removes them cleanly.
//
// It is done in an init rather than by editing the literal for the same reason
// the scope registration in music.go is: that file is contended. Both are one
// line to inline later and neither changes behaviour when it is.
//
// WHAT THESE TILES OPEN, AND THE ONE THING THAT IS STILL WORTH IMPROVING
//
// fetchArchiveRow builds each tile's target with playTargetFor, which appends
// `#ejs` for a game it can run in our own player and otherwise returns the
// Archive's own details page. So a music tile opens archive.org's audio player:
// a real, working, verified target that plays the item and lists its tracks --
// which is why these rows meet the standard as they stand.
//
// A SEARCH result for the same item does better: it carries `#music`, which the
// browser turns into our own track list with per-track play. Making a shelf tile
// do the same is two lines in playTargetFor and is described in this change's
// notes rather than done here, because that function lives in the contended
// file.
func init() {
	archiveRows = append(archiveRows, musicRows...)
}
