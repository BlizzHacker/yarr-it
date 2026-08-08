package main

import "strings"

// Query-term matching, used to keep search results actually about the search.

// stopWords are ignored when comparing a title to a query. Without this, a
// query like "the matrix" would count "the" as a matched term and let anything
// with a leading "The" look half-relevant.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "and": true,
	"in": true, "on": true, "to": true, "for": true,
}

// queryTerms normalises a search string into comparable words.
func queryTerms(q string) []string {
	fields := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 || stopWords[f] {
			continue
		}
		out = append(out, f)
	}
	// A query of nothing but stop words ("the") should still match on them
	// rather than matching everything equally.
	if len(out) == 0 && len(fields) > 0 {
		return fields
	}
	return out
}

// adultMarkers demote porn rips that happen to share a word with the query.
//
// A parody titled "... Inception XXX ..." genuinely matches the term, so term
// scoring alone cannot separate it from the film. This is a ranking nudge, not
// a filter: the result stays reachable, it just stops outranking the thing the
// person almost certainly meant.
var adultMarkers = []string{
	"xxx", "porn", "brazzers", "onlyfans", "hentai", "jav ",
	"anal", "milf", "creampie", "blowjob", "s3xus", "sexart",
	// Added for the archive.org text catalogue, which carries no category to
	// key on: its erotica is titled plainly and reached a Comics browse
	// untagged. These are title words, so they are chosen to be words that do
	// not appear in the name of something innocent.
	"erotic", "nsfw", "playboy", "penthouse", "hustler",
	"savita bhabhi", "savita bhabi", "kamasutra", "kama sutra",
	"lustomic", "bdsm", "futanari", "doujin", "ecchi",
	// Added for the archive.org FILM catalogue. `feature_films` is a general
	// public-domain library and its most-downloaded titles include the 1960s
	// nudie-cutie and sexploitation runs, which reached the landing page
	// unasked. Same rule as above: words that do not appear in the name of
	// something innocent. "naked" is deliberately absent -- The Naked Gun.
	"nudist", "nudie", "sexploitation", "molester", "sex madness",
}

func looksAdult(title string) bool {
	t := strings.ToLower(title)
	for _, m := range adultMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// titleOverlap counts how many query terms appear in the title.
//
// Substring rather than whole-word: release titles run words together and
// carry punctuation, so "spiderman" should match "Spider-Man" once both are
// normalised.
func titleOverlap(title string, terms []string) int {
	if len(terms) == 0 {
		return 0
	}
	norm := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		}
		return ' '
	}, title)
	collapsed := strings.ReplaceAll(norm, " ", "")

	n := 0
	for _, t := range terms {
		if strings.Contains(norm, t) || strings.Contains(collapsed, t) {
			n++
		}
	}
	return n
}

// queryWantsAdult reports whether the search itself asked for this material,
// in which case demoting it would just be wrong.
func queryWantsAdult(terms []string) bool {
	for _, t := range terms {
		for _, m := range adultMarkers {
			if strings.Contains(strings.TrimSpace(m), t) || t == strings.TrimSpace(m) {
				return true
			}
		}
	}
	return false
}
