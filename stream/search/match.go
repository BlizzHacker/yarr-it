package main

import (
	"fmt"
	"sort"
	"strings"
)

// Title matching, and why it needs a file of its own.
//
// A catalogue knows a work by the name its publisher gave it. An archive knows
// it by the name somebody typed on the day they uploaded a dump of it. Those
// two names are almost never the same string, and the gap is not random -- it
// is a small, closed set of conventions that ROM, scan and film uploads have
// followed for thirty years:
//
//	The Legend of Zelda: A Link to the Past      <- what the catalogue calls it
//	Legend Of Zelda, The A Link To The Past ( USA) SNES ROM
//	                                             <- what archive.org calls it
//
// Four separate conventions in one line: the leading article moved to the end
// behind a comma, the colon dropped, a parenthesised region tag added, and the
// platform plus the word "ROM" tacked on. Compared as strings these titles
// share nothing; compared through this file they are the same work.
//
// WHAT THIS REPLACED, AND WHY IT HAD TO GO
//
// The search built one Solr clause: `title:("<the whole query>")`, a quoted
// phrase. A quoted phrase demands those words, in that order, with nothing
// between them -- so every one of the four conventions above is fatal on its
// own. Measured against live archive.org:
//
//	title:("The Legend of Zelda: A Link to the Past")   0 results
//	title:(legend AND zelda AND link AND past)          2 results, both the game
//
// The Archive demonstrably holds two playable copies. The search returned
// nothing, and the landing page tile that ran it looked like a working button.
// That is the whole defect, and it was not one bad row -- it was every tile
// whose title carries a colon, an article, an accent or a subtitle.
//
// The mirror image is just as wrong. `title:(super AND mario AND world)` sorted
// by download count puts "Super Mario World DX", an MS-DOS fan hack, above
// "Super Mario World", the game. More results is not the goal; the RIGHT result
// on top is. So this file does two jobs, and the second matters more than the
// first: build a query that can find the thing, then score what comes back so
// the exact title always outranks a hack, a sequel or a bundle that merely
// contains it.
//
// THE SHAPE OF THE ANSWER
//
//	canonicalTitle   one name for one work, with the conventions undone
//	matchScore       0..1, where 1 means "this is that work"
//	archiveTitleClause / archiveTitleBatch  the Solr side of the same knowledge
//
// Query building and scoring deliberately normalise to DIFFERENT depths, and
// that asymmetry is the thing most likely to look like a bug later. Scoring
// folds "III" to "3" and "Baldur's" to "baldurs" because it is comparing two
// strings we hold. A query cannot: it is handed to somebody else's tokeniser,
// which indexed "Baldur's Gate" as `baldur` + `s` and would never match the
// token `baldurs`. So queries stay shallow and emit alternatives -- `(iii OR
// 3)` -- while scoring goes deep.

// ---------------------------------------------------------------- vocabulary

// articles that get moved to the end behind a comma. "Legend Of Zelda, The"
// is the convention; every ROM set and half the library scans use it.
var trailingArticles = map[string]bool{
	"the": true, "a": true, "an": true,
	"le": true, "la": true, "les": true, "l": true,
	"el": true, "los": true, "las": true,
	"der": true, "die": true, "das": true,
	"il": true, "lo": true, "gli": true, "i": true,
	"de": true, "het": true, "en": true, "ett": true,
}

// There are TWO stopword lists, and the difference between them is the
// difference between finding a thing and confusing it with another thing.
//
// queryStopwords is for RECALL. These words are not required of a search index,
// because an uploader may have dropped them: "Legend of Zelda - Link to the
// Past" is a real filing of a title that has an "A" in it. Requiring every
// little word turns a miss into a certainty.
//
// scoreStopwords is for PRECISION, and it is deliberately tiny. Using the
// recall list here was a real defect, caught against live archive.org: with
// "of" and "from" both discarded, "The Last of Us" and an Amiga demo called
// "Last From Us" reduce to the same two words, and the landing page offered the
// demo as the game. Only the leading article is genuinely free -- "The Simpsons"
// and "Simpsons" are the same show; "of" and "from" are not the same word.
//
// "i" is deliberately in neither, even though it is an article in Italian: it
// is also the roman numeral one, and dropping it turns "Final Fantasy I" into
// "Final Fantasy".
var queryStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "and": true,
	"or": true, "to": true, "in": true, "on": true, "at": true,
	"for": true, "with": true, "from": true,
}

var scoreStopwords = map[string]bool{"the": true, "a": true, "an": true}

// The noise vocabulary comes in three sets rather than one, and the split is
// not tidiness -- it is the difference between working and destroying titles.
//
// "World" is a region tag inside brackets and a word in the name of Super Mario
// World. "Sega" is a machine and the first word of Sega Rally Championship. A
// single list applied everywhere either keeps the tags (and every ROM title
// mismatches) or eats the names (and Super Mario World becomes Super Mario).
// So each set is applied exactly where its members cannot mean anything else:
//
//	platformNoise  machines and file words. Stripped from the END of a base
//	               title and never REQUIRED of a search index, because a
//	               catalogue never writes them and an archive often does.
//	regionWords    territories and languages. Stripped inside brackets, where
//	               "(World)" can only be a region, and from the end of a base
//	               title -- with the exception below.
//	bracketNoise   everything above plus dump and revision bookkeeping, used
//	               only inside brackets, where nothing names the work.

// platformNoise: the machine, and the words for "this is a file of a game".
// archive.org routinely ends a title with these ("... SNES ROM", "Chrono
// Trigger (SNES)", "... playable in browser") and a catalogue never does.
var platformNoise = map[string]bool{
	"snes": true, "sfc": true, "nes": true, "famicom": true,
	"gb": true, "gbc": true, "gba": true, "gameboy": true,
	"n64": true, "gamecube": true, "wii": true,
	"genesis": true, "megadrive": true, "sms": true,
	"gamegear": true, "saturn": true, "dreamcast": true, "32x": true,
	"psx": true, "ps1": true, "ps2": true, "psp": true,
	"lynx": true, "jaguar": true, "colecovision": true,
	"intellivision": true, "msx": true, "turbografx": true, "pce": true,
	"wonderswan": true, "neogeo": true, "amiga": true, "c64": true,
	"amstrad": true, "spectrum": true, "msdos": true,
	"mame": true, "swf": true,
	"rom": true, "roms": true, "iso": true, "cart": true, "cartridge": true,
	"browser": true, "playable": true, "compatible": true,
	"retroachievements": true, "unlicensed": true,
}

// regionNames: territories spelled out. Long enough to be unambiguous, so they
// can also be stripped from the end of a bare title ("Super Mario Kart USA").
//
// "world" is deliberately ABSENT even here. It is a region tag, but it is also
// the last word of Super Mario World, Wonder Boy in Monster World and a hundred
// others. Inside brackets it is handled by bracketNoise, where it cannot be
// part of a name.
var regionNames = map[string]bool{
	"usa": true, "america": true, "ntsc": true,
	"europe": true, "eur": true, "pal": true,
	"japan": true, "jpn": true,
	"korea": true, "kor": true, "china": true, "chn": true, "taiwan": true,
	"brazil": true, "australia": true, "canada": true,
	"asia": true, "international": true,
}

// regionCodes: the one- and two-letter forms, which are only ever safe INSIDE
// brackets. Every one of them is also a word.
//
// This distinction was not theoretical. With "us" treated as a region code
// anywhere, "The Last of Us" reduced to "The Last" and matched an Amiga demo
// called "Last From Us (19xx)(Uzi)", which the landing page then offered as the
// game -- found on live archive.org while checking this file's own output. "It",
// "Us", "Up" and "No" are all real titles; "(U)", "(E)" and "(J)" are not.
var regionCodes = map[string]bool{
	"us": true, "u": true, "e": true, "uk": true, "jp": true, "j": true,
	"en": true, "fr": true, "de": true, "es": true, "it": true, "pt": true,
	"nl": true, "sv": true, "da": true, "fi": true, "ja": true, "ko": true,
	"zh": true, "pl": true, "cs": true, "hu": true, "tr": true, "multi": true,
}

// bracketNoise is what may be discarded from inside brackets, where the
// uploader is describing the copy rather than naming the work.
var bracketNoise = map[string]bool{
	"world": true, "rev": true, "revision": true, "ver": true,
	"version": true, "alt": true, "alternate": true, "set": true,
	"dump": true, "disc": true, "disk": true, "side": true, "cd": true,
	"tape": true, "part": true, "vol": true, "volume": true,
	"russia": true, "rus": true, "russian": true, "france": true,
	"germany": true, "spain": true, "italy": true, "sweden": true,
	"netherlands": true, "nintendo": true, "sega": true, "atari": true,
	"commodore": true, "playstation": true, "coleco": true,
	"arcade": true, "dos": true, "pc": true, "flash": true, "game": true,
}

func isBracketNoise(w string) bool {
	return bracketNoise[w] || regionNames[w] || regionCodes[w] || platformNoise[w]
}

// isSuffixNoise says a word may be stripped from the END of a base title.
// Region CODES are excluded on purpose -- see regionCodes.
func isSuffixNoise(w string) bool {
	return platformNoise[w] || regionNames[w]
}

// derivativeMarkers name content that is NOT the work, however much of the work
// it contains. A prototype, a fan translation and an unlicensed clone are all
// legitimately on archive.org and all legitimately findable -- they are simply
// not the answer to "I want to play this game", and without this they win on
// download count. "Super Metroid: Ascent" has more downloads than most real
// SNES uploads.
//
// Cracker tags are NOT here on purpose. `[cr TSK]` on a Commodore 64 title
// describes how the copy was distributed in 1987, not what is in it; the game
// underneath is the game. Excluding those would throw away most of the
// 8-bit-computer catalogue for no gain.
var derivativeMarkers = map[string]bool{
	"unl": true, "unlicensed": true, "bootleg": true, "pirate": true,
	"hack": true, "romhack": true, "hacked": true,
	"beta": true, "proto": true, "prototype": true, "preview": true,
	"demo": true, "sample": true, "trailer": true, "teaser": true,
	"translation": true, "translated": true, "fanmade": true, "homebrew": true,
	"soundtrack": true, "ost": true, "manual": true, "scan": true,
	"walkthrough": true, "longplay": true, "speedrun": true, "playthrough": true,
	"review": true, "commercial": true, "advert": true, "advertisement": true,
	// A cheat disc boots, so nothing downstream notices it is not the game.
	"cheat": true, "cheats": true, "trainer": true, "savegame": true,
}

var romanValues = map[string]int{
	"i": 1, "ii": 2, "iii": 3, "iv": 4, "v": 5,
	"vi": 6, "vii": 7, "viii": 8, "ix": 9, "x": 10,
	"xi": 11, "xii": 12, "xiii": 13, "xiv": 14, "xv": 15,
}

// arabicToRoman is the reverse, used only when building a query: a search index
// has whichever the uploader typed, so both are offered.
var arabicToRoman = map[string]string{
	"1": "i", "2": "ii", "3": "iii", "4": "iv", "5": "v",
	"6": "vi", "7": "vii", "8": "viii", "9": "ix", "10": "x",
	"11": "xi", "12": "xii", "13": "xiii", "14": "xiv", "15": "xv",
}

// accentFolds covers the Latin-1 range these catalogues actually produce:
// "Pokémon", "God of War Ragnarök", "Ōkami", "Café". Folding rather than
// stripping, because deleting the character joins two words into one.
var accentFolds = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a", "ā", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e", "ē", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ī", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o", "ō", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u", "ū", "u",
	"ñ", "n", "ç", "c", "ý", "y", "ÿ", "y",
	"æ", "ae", "œ", "oe", "ß", "ss", "þ", "th", "ð", "d",
	"‘", "'", "’", "'", "“", "\"", "”", "\"", "–", "-", "—", "-", "…", " ",
)

// ------------------------------------------------------------- decomposition

// titleParts is a title split into the two things a title actually contains.
//
// Base is the name of the work. Qualifiers are everything the uploader put in
// brackets, which on archive.org is provenance -- region, dump quality,
// revision, publisher, cracker -- and almost never identity. They are kept
// apart because they must be weighed differently: an extra word in the base
// ("Super Mario World DX") means a different thing; an extra word in brackets
// ("Maniac Mansion (1987)(Lucasfilm)(Side A)") means the same thing, described.
type titleParts struct {
	Base       string   // canonical, article restored, noise removed
	Qualifiers []string // canonical tokens that were inside brackets
	Derivative bool     // a qualifier or base word says "this is not the work"
	// Year is a four-digit year found in the title itself, in brackets or
	// trailing. It is removed from Base -- "Parasite 1982" and "Parasite" are
	// the same NAME -- and kept here, because it is the only thing that
	// distinguishes Bong Joon-ho's film from the 1982 one of the same name.
	// archive.org's own `year` field is frequently absent or nonsense (an
	// upload of The Rookie carries year 1065), so the title is often the better
	// source of it.
	Year int
}

// splitBrackets pulls out every (...), [...] and {...} group.
func splitBrackets(s string) (string, []string) {
	var base strings.Builder
	var groups []string
	depth := 0
	var cur strings.Builder
	for _, r := range s {
		switch r {
		case '(', '[', '{':
			if depth == 0 {
				cur.Reset()
			} else {
				cur.WriteRune(r)
			}
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
				if depth == 0 {
					groups = append(groups, cur.String())
					// A space in place of the group, so "Zelda(USA)Gold" does
					// not become one word.
					base.WriteRune(' ')
					continue
				}
			}
			if depth > 0 {
				cur.WriteRune(r)
			}
		default:
			if depth > 0 {
				cur.WriteRune(r)
			} else {
				base.WriteRune(r)
			}
		}
	}
	// An unclosed bracket -- "( USA" -- is common enough in scraped titles that
	// dropping the tail silently would lose real words. Treat it as a group.
	if depth > 0 && cur.Len() > 0 {
		groups = append(groups, cur.String())
	}
	return base.String(), groups
}

// words lowercases, folds accents, removes punctuation and returns the tokens.
//
// Apostrophes are DELETED rather than replaced with a space, so "Baldur's"
// becomes one token `baldurs` and not `baldur` + `s`. A one-letter token would
// otherwise survive into the comparison and count as a word the other side is
// missing.
func words(s string) []string {
	s = accentFolds.Replace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "+", " and ")

	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// isYear recognises the four digits a catalogue appends and an archive puts in
// brackets. Both describe the same work, so neither should decide a match.
func isYear(w string) bool {
	if len(w) != 4 {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return (w[0] == '1' && w[1] == '9') || (w[0] == '2' && (w[1] == '0' || w[1] == '1'))
}

// isNumeric covers "1", "01", "1.1", "v2" -- version and disc numbering that
// appears inside brackets and never names a different work.
func isNumeric(w string) bool {
	if w == "" {
		return false
	}
	if w[0] == 'v' && len(w) > 1 {
		w = w[1:]
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// restoreArticle undoes "Legend Of Zelda, The A Link To The Past".
//
// Applied to the raw string before tokenising, because the comma is the signal
// and tokenising throws it away. Only the FIRST comma is considered: a title
// with several is a list, not an inversion.
func restoreArticle(s string) string {
	i := strings.Index(s, ",")
	if i < 0 {
		return s
	}
	head, tail := s[:i], strings.TrimSpace(s[i+1:])
	if head == "" || tail == "" {
		return s
	}
	f := strings.Fields(tail)
	if len(f) == 0 {
		return s
	}
	first := strings.ToLower(strings.Trim(f[0], ".!?"))
	if !trailingArticles[first] {
		return s
	}
	return f[0] + " " + head + " " + strings.Join(f[1:], " ")
}

// decompose is the whole of the normalisation, in one place so the query
// builder and the scorer can never disagree about what a title says.
func decompose(s string) titleParts {
	base, groups := splitBrackets(s)
	base = restoreArticle(base)

	out := titleParts{}

	baseWords := words(base)
	// Trailing noise only. "Chrono Trigger SNES ROM" loses two words; "NES
	// Remix" keeps both, because stripping from the front would eat the name.
	for len(baseWords) > 1 {
		last := baseWords[len(baseWords)-1]
		if isYear(last) {
			out.Year = atoiYear(last)
			baseWords = baseWords[:len(baseWords)-1]
			continue
		}
		if isSuffixNoise(last) || queryStopwords[last] {
			baseWords = baseWords[:len(baseWords)-1]
			continue
		}
		break
	}
	kept := make([]string, 0, len(baseWords))
	for _, w := range baseWords {
		if derivativeMarkers[w] {
			out.Derivative = true
		}
		if n, ok := romanValues[w]; ok && len(kept) > 0 {
			// Only after something else: a title that IS a roman numeral
			// ("X", "V") keeps its name.
			kept = append(kept, fmt.Sprint(n))
			continue
		}
		kept = append(kept, w)
	}
	out.Base = strings.Join(kept, " ")

	for _, g := range groups {
		for _, w := range words(g) {
			if isYear(w) {
				// "The Rookie ( 1990)" -- the brackets are where an uploader
				// most often puts the one fact that separates two works with
				// the same name.
				if out.Year == 0 {
					out.Year = atoiYear(w)
				}
				continue
			}
			if isBracketNoise(w) || isNumeric(w) {
				continue
			}
			if derivativeMarkers[w] {
				out.Derivative = true
			}
			out.Qualifiers = append(out.Qualifiers, w)
		}
	}
	return out
}

// canonicalTitle is one name for one work.
//
// Two titles with the same canonical form are the same thing as far as anything
// here is concerned, and that is the only promise it makes.
func canonicalTitle(s string) string { return decompose(s).Base }

// atoiYear parses a string isYear has already accepted.
func atoiYear(w string) int {
	n := 0
	for _, r := range w {
		n = n*10 + int(r-'0')
	}
	return n
}

// titleYear is the year a title states about itself, or 0.
func titleYear(s string) int { return decompose(s).Year }

// significantTokens is the canonical title reduced to the words that carry its
// identity: the leading article dropped, nothing else.
//
// Order is preserved and matters -- see matchScore, where an equal ORDERED
// sequence is what makes "The Simpsons" and "Simpsons" the same show without
// also making "The Last of Us" and "Last From Us" the same thing.
//
// Falls back to the full token list rather than returning nothing, because a
// title CAN be entirely articles -- "The The", "A".
func significantTokens(s string) []string {
	all := strings.Fields(canonicalTitle(s))
	out := make([]string, 0, len(all))
	for _, w := range all {
		if scoreStopwords[w] {
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return all
	}
	return out
}

func sameSequence(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------- scoring

// matchAccept is the bar for "this IS the thing asked for", used wherever a
// tile or a link is about to promise a result. It sits above every score a
// title with an extra word of its own can reach, so a sequel, a bundle or a
// hack can never satisfy it -- only the work itself, however it was labelled.
const matchAccept = 0.9

// matchFloor is the bar for "this is worth showing at all", used for ordering
// search results rather than for making promises. A one-word search still wants
// everything containing that word.
const matchFloor = 0.25

// matchScore says how well `got` answers a request for `want`, from 0 to 1.
//
// The tiers, and what each is protecting against:
//
//	1.00  the same work, however labelled          the answer
//	0.95  the same words, only the article differs "Simpsons" for "The Simpsons"
//	0.92  the same work, with bracketed provenance a dump, a print, a release
//	0.62  same, but the brackets say it is a hack  findable, never the answer
//	<0.9  the base title carries extra words       a sequel, a bundle, a hack
//	<0.4  some wanted words are missing            a different thing entirely
//
// The gap between the fourth and the first row is the one Wade actually hit:
// "Super Mario World DX" outranking "Super Mario World" because it had six
// times the downloads. Ordering by this score first and popularity second is
// what makes an exact title win, always.
//
// This says nothing about WHEN. Two works can share a name -- Parasite (2019)
// and Parasite (1982) are both on archive.org and score 1.00 against each
// other. Distinguishing them is the caller's job, with the year; see
// yearsAgree in discover_resolve.go.
func matchScore(want, got string) float64 {
	w := decompose(want)
	g := decompose(got)
	if w.Base == "" || g.Base == "" {
		return 0
	}

	wt := significantTokens(want)
	gt := significantTokens(got)
	if len(wt) == 0 {
		return 0
	}

	if w.Base == g.Base || sameSequence(wt, gt) {
		score := 1.0
		if w.Base != g.Base {
			// The same words in the same order, differing only by an article.
			// Not quite an identity, so it loses a tie to one.
			score = 0.95
		}
		if len(g.Qualifiers) > 0 {
			// Provenance, not identity. A small, flat cost so that a bare
			// title still wins a tie, and nothing more.
			score -= 0.08
		}
		if g.Derivative && !w.Derivative {
			score -= 0.3
		}
		return score
	}

	have := make(map[string]bool, len(gt))
	for _, t := range gt {
		have[t] = true
	}
	hit := 0
	for _, t := range wt {
		if have[t] {
			hit++
		}
	}
	if hit < len(wt) {
		// Something asked for is not there. This is the branch that has to stay
		// well clear of matchAccept: it is how "Super Metroid" used to come
		// back holding Super Mario World.
		return 0.45 * float64(hit) / float64(len(wt))
	}

	// Everything asked for is present, plus words of its own. Each extra word
	// is a step further from the thing -- "World" -> "World 2" -> "World 2
	// Yoshi's Island" -- so the penalty is per word rather than a flat one.
	extra := 0
	wantHas := make(map[string]bool, len(wt))
	for _, t := range wt {
		wantHas[t] = true
	}
	for _, t := range gt {
		if !wantHas[t] {
			extra++
		}
	}
	score := 0.9 - 0.12*float64(extra)
	if score < 0.3 {
		// A floor, not a rounding: a search for one word must still return the
		// long titles that contain it, which is most of a catalogue.
		score = 0.3
	}
	if g.Derivative && !w.Derivative {
		score -= 0.2
		if score < 0.05 {
			score = 0.05
		}
	}
	return score
}

// bestMatch picks the index of the best answer to `want` from `titles`, or -1
// when nothing clears `min`. Ties go to the earlier entry, so a caller that has
// already sorted by popularity keeps that order among equals -- which is how
// the real SNES upload beats the Genesis bootleg of the same name.
func bestMatch(want string, titles []string, min float64) (int, float64) {
	best, bestScore := -1, min
	for i, t := range titles {
		if s := matchScore(want, t); s > bestScore {
			best, bestScore = i, s
		}
	}
	if best < 0 {
		return -1, 0
	}
	return best, bestScore
}

// -------------------------------------------------------------- query syntax

// archiveTitleClause turns a query into the title half of a Solr query.
//
// Every significant word is REQUIRED (`AND`), which is what makes the result
// set small enough to rank honestly, and nothing is quoted, which is what makes
// it possible to match at all. Word order, punctuation, articles and region
// tags all stop mattering, because none of them survives into the clause.
//
// Roman numerals are emitted as alternatives rather than folded, because this
// string is handed to somebody else's index: it holds whichever form the
// uploader typed, and demanding one of them would miss half the catalogue.
//
// Returns "" for a query with nothing in it, which the caller reads as a browse
// rather than as a search for nothing.
func archiveTitleClause(q string) string {
	toks := archiveQueryTokens(q)
	if len(toks) == 0 {
		return ""
	}
	return "title:(" + strings.Join(toks, " AND ") + ")"
}

// archiveQueryTokens is the term list, with each term already expanded into the
// alternatives an index might hold it under.
//
// Deliberately SHALLOWER than significantTokens: the apostrophe is dropped
// (matching an index that split "Baldur's" into two terms is hopeless either
// way, but `baldur` matches both spellings while `baldurs` matches one), and
// the accent is folded, and that is all. Anything more aggressive here is a
// guess about a tokeniser we do not own.
func archiveQueryTokens(q string) []string {
	raw := words(q)
	out := make([]string, 0, len(raw))
	for _, w := range raw {
		if queryStopwords[w] || isYear(w) {
			continue
		}
		// A single letter is a fragment of a possessive or an initial and is
		// not worth requiring. A single DIGIT is the whole difference between
		// Final Fantasy 7 and Final Fantasy, so it stays.
		if len(w) < 2 && arabicToRoman[w] == "" {
			continue
		}
		if platformNoise[w] {
			// Never REQUIRED. A machine name is the one thing the two sides
			// reliably DISAGREE about: the archive writes "Chrono Trigger
			// (SNES)" and the catalogue writes "Chrono Trigger", so demanding
			// it excludes every upload that left it out, and demanding its
			// absence is not something a query can express. Region words are
			// not dropped here -- "Australia" is a machine to nobody, and a
			// film called Australia would lose its only word.
			continue
		}
		if alt, ok := arabicToRoman[w]; ok {
			out = append(out, "("+w+" OR "+alt+")")
			continue
		}
		if n, ok := romanValues[w]; ok && len(out) > 0 {
			out = append(out, "("+w+" OR "+fmt.Sprint(n)+")")
			continue
		}
		out = append(out, w)
		// Eight required terms is already a very long title, and past that the
		// clause is more likely to exclude the right item than to narrow
		// usefully.
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		// Everything was a stopword or a machine name -- "It", "The Thing",
		// "SNES". Fall back to the words themselves rather than to nothing.
		for _, w := range raw {
			if len(w) >= 2 {
				out = append(out, w)
			}
			if len(out) >= 8 {
				break
			}
		}
	}
	return out
}

// archiveTitleBatch asks one question on behalf of many titles.
//
// A shelf is twenty-four tiles, and deciding whether each has anything behind
// it is twenty-four questions -- which, done one at a time, is most of a minute
// of somebody else's rate limit for a page that is rebuilt every three hours.
// Solr will take them all at once: the clauses are OR'd, the caller asks for
// enough rows to hold every answer, and the matching back up is this file's job
// anyway.
//
// Titles that reduce to no terms are skipped rather than emitted as an empty
// clause, which would OR in "everything".
func archiveTitleBatch(titles []string) string {
	seen := make(map[string]bool, len(titles))
	clauses := make([]string, 0, len(titles))
	for _, t := range titles {
		c := archiveTitleClause(t)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		clauses = append(clauses, c)
	}
	if len(clauses) == 0 {
		return ""
	}
	if len(clauses) == 1 {
		return clauses[0]
	}
	return "(" + strings.Join(clauses, " OR ") + ")"
}

// ------------------------------------------------------------------ ordering

// scored pairs a card with how well it answered, so the sort can use both it
// and the source's own popularity signal without either being lost.
type scored struct {
	card  card
	score float64
}

// rankByMatch orders results by whether they are the thing asked for, and only
// then by how popular they are.
//
// Popularity alone is what put an MS-DOS fan hack above the game it hacks. It
// is still the right tiebreak -- between two items with the same title it picks
// the canonical upload over the five near-duplicates -- but it cannot be
// allowed to decide what the search was about.
//
// Anything below matchFloor is dropped. A result nobody asked for is not a
// weaker answer, it is a wrong one, and showing it is exactly the "appearance
// of working" this exists to stop.
func rankByMatch(cards []card, want string) []card {
	if strings.TrimSpace(want) == "" {
		return cards
	}
	out := make([]scored, 0, len(cards))
	for _, c := range cards {
		s := matchScore(want, c.Title)
		if s < matchFloor {
			continue
		}
		out = append(out, scored{card: c, score: s})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].card.Popular > out[j].card.Popular
	})
	ranked := make([]card, len(out))
	for i, s := range out {
		ranked[i] = s.card
	}
	return ranked
}
