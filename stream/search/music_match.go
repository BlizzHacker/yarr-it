package main

import (
	"sort"
	"strings"
)

// Matching a music query, and why match.go cannot do it alone.
//
// match.go is right about everything it was written for and none of it
// transfers unchanged, because a recording is not shaped like a film or a ROM.
//
// A film and a ROM are filed under their own name. Their title IS their
// identity, so scoring a query against the title is scoring it against the
// thing. A recording is filed under the name of the WORK, and the person who
// made it lives in a different field:
//
//	title    "Tears"                    <- what the catalogue calls it
//	creator  "King Oliver's Jazz Band; Hardin; Armstrong"
//
// Nobody searching for Louis Armstrong types "Tears". Measured against live
// archive.org, `title:(louis AND armstrong)` returns 4 items and the same query
// across title and creator returns 399 -- and the four are the ones with his
// name printed on the record sleeve, which is the least interesting subset.
//
// The second difference is the Live Music Archive's title convention, which is
// as regular as any ROM-set convention and just as fatal if it is not undone:
//
//	Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08
//	^^^^^^^^^^^^^ the work    ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ ^^^^^^^^^^ provenance
//
// Scored as a title, that is thirteen words of which two are the answer, so
// match.go's per-extra-word penalty drives every concert to its 0.3 floor and
// popularity decides the order -- which is precisely the failure match.go was
// written to stop, arriving through the other end. Measured: "so what" returned
// three Ryan Adams and Godspeed You! Black Emperor concerts, because they
// happened to be recorded at a venue in Dallas called So What.
//
// The third is comma inversion, and it is the same convention match.go's
// restoreArticle undoes for articles -- applied to people rather than to "The".
// archive.org files "Davis, Miles" and "Armstrong, Louis" as often as it files
// them the other way round, and neither spelling is wrong.
//
// WHAT THIS DOES NOT DO
//
// It does not re-implement scoring. matchScore stays the only definition of how
// well one string answers another, and everything here is about deciding WHICH
// STRINGS to hand it. That is deliberate: two scoring functions would drift,
// and the one in match.go has a decade of specific failures written into it.

// musicLiveMarker is the Live Music Archive's join between a work and where it
// was played. Every one of the 293,163 items in that collection that carries a
// venue at all uses this exact spelling.
const musicLiveMarker = " live at "

// musicWorkTitle reduces a title to the part that names the work.
//
// For a concert that is the artist, because a concert IS its artist plus a date
// -- there is no other name for it. For everything else the title is already
// the work and comes back unchanged.
//
// The date is trimmed separately from the venue, because the two conventions
// appear independently: "Phish 7/20/24 Xfinity Center - Mansfield, MA" carries
// a date and no "Live at", and dropping it leaves "Phish", which is right.
func musicWorkTitle(title string) string {
	t := strings.TrimSpace(title)
	if i := strings.Index(strings.ToLower(t), musicLiveMarker); i > 0 {
		return strings.TrimSpace(t[:i])
	}
	return musicTrimTrailingDate(t)
}

// musicTrimTrailingDate removes a trailing " on 1977-05-08" or a bare trailing
// ISO date. Only from the END, and only when something is left: a recording
// legitimately titled with a date -- "1977-05-08" as the whole title -- keeps
// its name, for the same reason match.go refuses to strip noise from the front
// of a title.
func musicTrimTrailingDate(t string) string {
	fields := strings.Fields(t)
	for len(fields) > 1 {
		last := fields[len(fields)-1]
		if !isISODate(last) {
			break
		}
		fields = fields[:len(fields)-1]
		if len(fields) > 1 && strings.EqualFold(fields[len(fields)-1], "on") {
			fields = fields[:len(fields)-1]
		}
	}
	return strings.Join(fields, " ")
}

// isISODate recognises `YYYY-MM-DD`, which is the only date form the Live Music
// Archive puts in a title.
func isISODate(w string) bool {
	if len(w) != 10 || w[4] != '-' || w[7] != '-' {
		return false
	}
	for i, r := range w {
		if i == 4 || i == 7 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// musicUninvert undoes "Davis, Miles".
//
// Bounded on both sides on purpose. A creator field in this corpus is often a
// LIST rendered with commas -- "Bessie SMITH - Gesang, Clara SMITH - Gesang" --
// and inverting that produces a string that is neither name and matches
// neither. A personal name inverted for filing has one comma, at most three
// words before it and at most two after; anything else is left exactly as it
// arrived, which costs nothing because the un-inverted form is scored anyway.
func musicUninvert(name string) string {
	head, tail, found := strings.Cut(name, ",")
	if !found {
		return ""
	}
	head, tail = strings.TrimSpace(head), strings.TrimSpace(tail)
	if head == "" || tail == "" {
		return ""
	}
	if strings.Contains(tail, ",") {
		return "" // a list, not an inversion
	}
	hf, tf := strings.Fields(head), strings.Fields(tail)
	if len(hf) == 0 || len(hf) > 3 || len(tf) == 0 || len(tf) > 2 {
		return ""
	}
	return tail + " " + head
}

// musicDepossess turns "King Oliver's Jazz Band" into "King Oliver Jazz Band".
//
// Bands of this era are named possessively and it is not a minority spelling:
// King Oliver's Jazz Band, Kid Ory's Sunshine Orchestra, Earl Fuller's Famous
// Jazz Band, Harry Raderman's Dance Orchestra, Lanin's Southern Serenaders --
// all in the first thirty results of one shelf query.
//
// match.go deletes apostrophes so that "Baldur's" is one token rather than two,
// which is right for a game and wrong here: it makes the token `olivers`, and
// somebody searching for King Oliver types `oliver`. Rather than change that --
// it is load-bearing elsewhere and the comment there explains why -- the
// possessive is removed as an ADDITIONAL reading of the name, so both spellings
// are tried and neither is lost.
func musicDepossess(s string) string {
	for _, apostrophe := range []string{"'s", "’s"} {
		s = strings.ReplaceAll(s, apostrophe+" ", " ")
		if strings.HasSuffix(s, apostrophe) {
			s = strings.TrimSuffix(s, apostrophe)
		}
	}
	return s
}

// musicSharesAToken reports whether two strings have any significant word in
// common. It is the gate on the venue; see musicScore.
func musicSharesAToken(want, got string) bool {
	wt := significantTokens(want)
	if len(wt) == 0 {
		return false
	}
	have := make(map[string]bool)
	for _, t := range significantTokens(got) {
		have[t] = true
	}
	for _, t := range wt {
		if have[t] {
			return true
		}
	}
	return false
}

// musicScore says how well a card answers a music query.
//
// IDENTITY FIRST, THEN THE VENUE, AND THE ORDER IS THE WHOLE DESIGN
//
// A result has an identity -- the work, and whoever made it -- and it has
// provenance: where it was recorded and when. match.go draws the same line for
// bracketed qualifiers and states the rule: an extra word in the base means a
// different thing, an extra word in brackets means the same thing described.
// A venue is provenance.
//
// So the identity is scored first, from every spelling a person might use:
//
//	the work            "crazy blues"
//	the artist          "louis armstrong"
//	the artist, filed   "armstrong, louis"
//	the artist, plainly "king oliver" for King Oliver's Jazz Band
//
// and the venue is allowed to IMPROVE that score but never to create one. That
// asymmetry is what separates the two cases that otherwise look identical:
//
//	"grateful dead barton hall"   the band matches, the venue completes it
//	"so what"                     nothing but a room in Dallas with that name
//
// Both were real. The second was measured against live archive.org, where "so
// what" -- a Miles Davis tune -- returned three concerts by Ryan Adams and
// Godspeed You! Black Emperor, all recorded at a venue called So What, above
// everything else.
//
// Taking the maximum rather than a weighted sum keeps the rest honest: a sum
// would let a card that is a weak answer twice outrank one that is the right
// answer once.
func musicScore(want string, c card) float64 {
	work := musicWorkTitle(c.Title)
	best := matchScore(want, work)

	if c.Music == nil {
		// A card from the generic path, which produces no music facts. Its title
		// is all there is, and it is scored raw as well as reduced -- there is no
		// artist to tell one from the other.
		if s := matchScore(want, c.Title); s > best {
			best = s
		}
		return best
	}

	artist := c.Music.Artist
	identity := work
	if artist != "" {
		identity = artist + " " + work
		for _, form := range []string{artist, musicUninvert(artist), musicDepossess(artist)} {
			if form == "" {
				continue
			}
			if s := matchScore(want, form); s > best {
				best = s
			}
		}
		// Artist and work together, for a query that names both. Only when they
		// are actually different strings -- for a concert the work IS the artist,
		// and scoring "Grateful Dead Grateful Dead" would invent two extra words
		// for every live result in the catalogue.
		if !strings.EqualFold(artist, work) {
			if s := matchScore(want, artist+" "+work); s > best {
				best = s
			}
		}
	}

	// The venue, and only once something in the identity has already answered
	// part of the question. One shared word is a low bar on purpose: "grateful
	// dead barton hall" matches the band on two words out of four, which scores
	// 0.22 on its own and would be dropped before the venue could rescue it.
	venue := strings.TrimSpace(c.Music.Venue)
	if venue == "" || !musicSharesAToken(want, identity) {
		return best
	}
	if s := matchScore(want, artist+" "+venue); s > best {
		best = s
	}
	return best
}

// musicNamedThreshold is how many significant words a query needs before every
// one of them is required.
//
// Below it, a partial match is what somebody wants: "zelda" is a keyword and
// not a title, and match.go's floor is deliberately generous for exactly that
// reason. At three words a query has stopped being a keyword and started naming
// something, and a result missing one of them is a different thing.
//
// Measured, and this is the case it was written for: "kind of blue" returned
// three Kind Country concerts recorded at Blue Ribbon Pines and Blue Ox
// Festival, each scoring 0.30 for holding two of the three words in unrelated
// halves of a long title. The album itself is not free anywhere on archive.org,
// so the honest answer to that query is nothing at all -- and nothing is a
// better answer than a bluegrass set from Minnesota.
//
// Two words needs no rule: a two-word query missing one word already scores
// 0.225, which is below matchFloor.
const musicNamedThreshold = 3

// musicHasEveryTerm reports whether every significant word of the query appears
// somewhere in what this result IS -- its title, its artist, its venue.
//
// The venue counts here where it does not count for scoring, and the difference
// is what each is for: scoring asks "is this the thing", and a room cannot
// answer that; this asks "could this be the thing at all", and a query naming
// a venue is asking about a place.
func musicHasEveryTerm(want string, c card) bool {
	whole := c.Title
	if c.Music != nil {
		whole += " " + c.Music.Artist + " " + c.Music.Venue
		if inv := musicUninvert(c.Music.Artist); inv != "" {
			whole += " " + inv
		}
		if dep := musicDepossess(c.Music.Artist); dep != c.Music.Artist {
			whole += " " + dep
		}
	}
	have := make(map[string]bool)
	for _, t := range significantTokens(whole) {
		have[t] = true
	}
	for _, t := range significantTokens(want) {
		if !have[t] {
			return false
		}
	}
	return true
}

// rankMusic orders music results by whether they are what was asked for, then
// by how popular they are.
//
// The same two-key sort rankByMatch uses, and for the same reason: the Archive
// returns these in download order, and download order alone put three Ryan
// Adams concerts at the top of a search for "so what". Anything below
// matchFloor is dropped rather than shown weakly -- a result nobody asked for
// is not a worse answer, it is a wrong one.
//
// A browse -- no query -- is left in the Archive's own order, which is
// popularity, and is the right order for a shelf.
func rankMusic(cards []card, want string) []card {
	if strings.TrimSpace(want) == "" {
		return cards
	}
	named := len(significantTokens(want)) >= musicNamedThreshold
	kept := make([]scored, 0, len(cards))
	for _, c := range cards {
		s := musicScore(want, c)
		if s < matchFloor {
			continue
		}
		if named && !musicHasEveryTerm(want, c) {
			continue
		}
		kept = append(kept, scored{card: c, score: s})
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].score != kept[j].score {
			return kept[i].score > kept[j].score
		}
		return kept[i].card.Popular > kept[j].card.Popular
	})
	out := make([]card, len(kept))
	for i, s := range kept {
		out[i] = s.card
	}
	return out
}
