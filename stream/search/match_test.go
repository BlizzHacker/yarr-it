package main

import (
	"strings"
	"testing"
)

// Every title in this file was taken from live archive.org on 2026-08-08, by
// asking it for the games the landing page offers. They are not invented
// examples of what ROM naming might look like -- they are what it does look
// like, including the double space in "( USA)" and the missing space in
// "Zelda, The A Link".

func TestCanonicalTitleUndoesArchiveNamingConventions(t *testing.T) {
	cases := []struct{ in, want string }{
		// The one that started this: four conventions in a single title.
		{"Legend Of Zelda, The A Link To The Past ( USA) SNES ROM",
			"the legend of zelda a link to the past"},
		{"The Legend of Zelda: A Link to the Past",
			"the legend of zelda a link to the past"},

		{"Chrono Trigger (SNES)", "chrono trigger"},
		{"Castlevania - Symphony of the Night (USA)", "castlevania symphony of the night"},
		{"Castlevania: Symphony of the Night", "castlevania symphony of the night"},
		{"Super Metroid for SNES ( Japan, USA) ( En, Ja)", "super metroid"},

		// Accents, ampersands and possessives all have to survive as the same
		// word on both sides or the catalogue and the archive never meet.
		{"Pokémon Red", "pokemon red"},
		{"God of War Ragnarök", "god of war ragnarok"},
		{"Baldur's Gate III", "baldurs gate 3"},
		{"Baldurs Gate 3", "baldurs gate 3"},
		{"Final Fantasy VII", "final fantasy 7"},
		{"Sonic & Knuckles", "sonic and knuckles"},

		// The trailing-noise strip must not eat a name. "World" is a region tag
		// in brackets and the last word of a real title outside them; "Sega" is
		// a machine and the first word of a real title.
		{"Super Mario World", "super mario world"},
		{"Super Mario World (World)", "super mario world"},
		{"Sega Rally Championship", "sega rally championship"},
		{"NES Remix", "nes remix"},

		// Real archive.org verbosity.
		{"Super Mario Advance 2 Super Mario World ( Europe) ( En, Fr, De, Es) " +
			"Compatible with retroachievements, playable in browser",
			"super mario advance 2 super mario world"},
	}
	for _, c := range cases {
		if got := canonicalTitle(c.in); got != c.want {
			t.Errorf("canonicalTitle(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// The defect Wade reported from the other side: archive.org HAS this game, and
// the search returned nothing, because a quoted phrase demands an exact
// substring and no ROM title is ever one.
func TestTheTitleThatReturnedNothingNowMatches(t *testing.T) {
	want := "The Legend of Zelda: A Link to the Past"
	got := "Legend Of Zelda, The A Link To The Past ( USA) SNES ROM"
	if s := matchScore(want, got); s < matchAccept {
		t.Fatalf("matchScore = %.2f, want >= %.2f -- this is the pair that "+
			"returned 0 results on live", s, matchAccept)
	}
	// Its sibling is a DIFFERENT game and must not satisfy the same request.
	sibling := "Legend Of Zelda, The A Link To The Past Four Swords ( U) [!]"
	if s := matchScore(want, sibling); s >= matchAccept {
		t.Fatalf("Four Swords scored %.2f; a different game must not be "+
			"offered as this one", s)
	}
}

// The same defect from the other side: a search that DID return something
// returned the wrong thing first. An exact title must outrank a romhack,
// always -- whatever the download counts say.
func TestAnExactTitleOutranksEverythingItIsContainedIn(t *testing.T) {
	const want = "Super Mario World"
	// Ordered as live archive.org returns them, most-downloaded first. The hack
	// has 157,392 downloads and the game has 28,882.
	got := []string{
		"Super Mario World DX",
		"Super Mario World",
		"Super Mario World 2: Yoshi's Island",
		"Super Mario Advance 2: Super Mario World",
		"Super Mario All Stars + Super Mario World ( USA)",
		"Super Mario World ( Unl)",
	}
	i, score := bestMatch(want, got, matchFloor)
	if i != 1 {
		t.Fatalf("best match is %q; want the exact title", got[i])
	}
	if score < matchAccept {
		t.Fatalf("exact title scored %.2f, below the acceptance bar", score)
	}
	exact := matchScore(want, "Super Mario World")
	for _, other := range []string{
		"Super Mario World DX",
		"Super Mario World 2: Yoshi's Island",
		"Super Mario Advance 2: Super Mario World",
		"Super Mario All Stars + Super Mario World ( USA)",
	} {
		if s := matchScore(want, other); s >= exact {
			t.Errorf("%q scored %.2f, at or above the exact title's %.2f",
				other, s, exact)
		}
		if s := matchScore(want, other); s >= matchAccept {
			t.Errorf("%q scored %.2f -- it would be offered AS Super Mario World",
				other, s)
		}
	}
}

// A dump tag says which copy; a derivative marker says it is not the work. The
// first must not cost anything, the second must be disqualifying.
func TestProvenanceIsFreeAndDerivativesAreNot(t *testing.T) {
	const want = "Maniac Mansion"
	// A cracked Commodore 64 copy IS Maniac Mansion. Its brackets describe how
	// it was distributed in 1987, and excluding it would throw away most of the
	// 8-bit computer catalogue.
	cracked := "Maniac Mansion (1987)(Lucasfilm)(de)(Side A)[cr TSK]"
	if s := matchScore(want, cracked); s < matchAccept {
		t.Errorf("a cracked copy scored %.2f; it is still the game", s)
	}

	for _, not := range []string{
		"Super Mario World ( Unl)",
		"Castlevania Symphony Of The Night (Prototype)",
		"Night of the living dead Trailer",
	} {
		base := strings.TrimSuffix(strings.SplitN(not, " (", 2)[0], " Trailer")
		if s := matchScore(base, not); s >= matchAccept {
			t.Errorf("%q scored %.2f against %q; it is not that work",
				not, s, base)
		}
	}
}

// The failure that looks like success: a search returns results, none of which
// are the thing. Anything missing a word that was asked for has to land well
// below the bar rather than merely below the best.
func TestAMissingWordIsAWrongAnswerNotAWeakOne(t *testing.T) {
	pairs := [][2]string{
		{"Super Metroid", "Super Mario World"},
		{"The Legend of Zelda: A Link to the Past", "Legend Of Zelda, The Ocarina Of Time"},
		{"Chrono Trigger", "Chrono Cross"},
	}
	for _, p := range pairs {
		if s := matchScore(p[0], p[1]); s >= 0.45 {
			t.Errorf("matchScore(%q, %q) = %.2f -- too close to a real answer",
				p[0], p[1], s)
		}
	}
	// But a one-word search must still reach the long titles containing it,
	// or narrowing a catalogue becomes impossible.
	if s := matchScore("zelda", "Legend Of Zelda, The A Link To The Past ( USA)"); s < matchFloor {
		t.Errorf("a one-word search lost its results: %.2f", s)
	}
}

func TestQueryClauseIsUnquotedAndRequiresEveryRealWord(t *testing.T) {
	q := archiveTitleClause("The Legend of Zelda: A Link to the Past")
	// The quoted phrase is the defect. Not a stylistic preference: measured
	// against live archive.org it returns 0 where this returns both copies.
	if strings.Contains(q, `"`) {
		t.Fatalf("clause is quoted again: %s", q)
	}
	for _, w := range []string{"legend", "zelda", "link", "past"} {
		if !strings.Contains(q, w) {
			t.Errorf("clause dropped %q: %s", w, q)
		}
	}
	for _, w := range []string{"the", "of", "to"} {
		if strings.Contains(q, " "+w+" AND") {
			t.Errorf("clause requires the stopword %q: %s", w, q)
		}
	}
	if !strings.Contains(q, " AND ") {
		t.Fatalf("clause does not require its terms: %s", q)
	}
}

// The year the client appends to every tile click ("Super Mario World 1990")
// was going into the phrase, and no archive.org ROM title carries it.
func TestTheAppendedYearIsNotRequired(t *testing.T) {
	q := archiveTitleClause("Super Mario World 1990")
	if strings.Contains(q, "1990") {
		t.Fatalf("the year is required of the index: %s", q)
	}
}

// A search index holds whichever numeral the uploader typed. Folding to one of
// them here would miss every item that used the other.
func TestNumeralsAreOfferedBothWays(t *testing.T) {
	for _, q := range []string{
		archiveTitleClause("Final Fantasy VII"),
		archiveTitleClause("Final Fantasy 7"),
	} {
		if !strings.Contains(q, "vii") || !strings.Contains(q, "7") {
			t.Errorf("clause offers only one numeral form: %s", q)
		}
	}
}

// A search box accepts anything, and this clause is interpolated into Solr.
func TestQueryClauseNeutralisesSolrSyntax(t *testing.T) {
	q := archiveTitleClause(`doom" OR collection:(nsfw`)
	if strings.Contains(q, `"`) {
		t.Fatalf("injected quote survived: %s", q)
	}
	// Exactly one colon, and it is the one this function wrote. A second one
	// means the caller's text reached Solr as syntax.
	if strings.Count(q, ":") != 1 || !strings.HasPrefix(q, "title:(") {
		t.Fatalf("injected syntax survived: %s", q)
	}
	if strings.Count(q, "title:(") != 1 {
		t.Fatalf("query structure was broken by input: %s", q)
	}
	// Unbalanced brackets in the input must not unbalance the clause.
	if strings.Count(q, "(") != strings.Count(q, ")") {
		t.Fatalf("clause is unbalanced: %s", q)
	}
}

func TestEmptyQueryIsABrowseNotASearchForNothing(t *testing.T) {
	if q := archiveTitleClause("   "); q != "" {
		t.Fatalf("empty term built a clause: %q", q)
	}
}

// A title that is nothing but stopwords and machine names still has to be
// searchable -- "It", "Us", "Up" are all real films.
func TestATitleOfNothingButStopwordsStillAsksSomething(t *testing.T) {
	for _, title := range []string{"It", "Us", "Up"} {
		if q := archiveTitleClause(title); q == "" {
			t.Errorf("%q produced no query at all", title)
		}
	}
}

func TestBatchAsksOneQuestionForManyTitles(t *testing.T) {
	q := archiveTitleBatch([]string{
		"Super Metroid",
		"The Legend of Zelda: A Link to the Past",
		"Super Mario World",
		// A duplicate and an unaskable title must not add clauses.
		"Super Mario World",
		"   ",
	})
	if n := strings.Count(q, "title:("); n != 3 {
		t.Fatalf("batch has %d clauses, want 3: %s", n, q)
	}
	if !strings.Contains(q, " OR ") {
		t.Fatalf("batch does not widen: %s", q)
	}
	if strings.Count(q, "(") != strings.Count(q, ")") {
		t.Fatalf("batch is unbalanced: %s", q)
	}
	if archiveTitleBatch(nil) != "" {
		t.Fatal("an empty batch must not ask for everything")
	}
}

// Ranking is where popularity stops deciding what the search was about.
func TestRankingPutsTheAnswerFirstAndDropsTheWrongOnes(t *testing.T) {
	cards := []card{
		{Title: "Super Mario World DX", Popular: 157392},
		{Title: "Super Mario World", Popular: 28882},
		{Title: "Super Mario World 2: Yoshi's Island", Popular: 19304},
		{Title: "Super Metroid: Ascent", Popular: 90000},
		{Title: "Super Mario World", Popular: 1867},
	}
	got := rankByMatch(cards, "Super Mario World")
	if len(got) == 0 {
		t.Fatal("ranking dropped everything")
	}
	if got[0].Title != "Super Mario World" || got[0].Popular != 28882 {
		t.Fatalf("first result is %q (%d downloads); want the most-downloaded "+
			"exact title", got[0].Title, got[0].Popular)
	}
	// The second copy of the exact title comes next: same score, fewer
	// downloads. Popularity is the tiebreak, not the ranking.
	if got[1].Title != "Super Mario World" || got[1].Popular != 1867 {
		t.Fatalf("second result is %q (%d); want the other exact copy",
			got[1].Title, got[1].Popular)
	}
	for _, c := range got {
		if strings.Contains(c.Title, "Metroid") {
			t.Fatalf("a result nobody asked for survived: %q", c.Title)
		}
	}
}

func TestRankingLeavesABrowseAlone(t *testing.T) {
	cards := []card{{Title: "b"}, {Title: "a"}}
	got := rankByMatch(cards, "")
	if len(got) != 2 || got[0].Title != "b" {
		t.Fatalf("a browse was reordered or filtered: %+v", got)
	}
}

// The trap in undoing region tags: every short region code is also a word.
// With "us" treated as one anywhere, "The Last of Us" reduced to "The Last" and
// matched an Amiga demo called "Last From Us (19xx)(Uzi)" -- which the landing
// page then offered as the game. Found on live archive.org while checking this
// file's own output.
func TestARegionCodeIsOnlyARegionCodeInsideBrackets(t *testing.T) {
	if got := canonicalTitle("The Last of Us"); got != "the last of us" {
		t.Errorf("canonicalTitle ate a real word: %q", got)
	}
	for _, title := range []string{"It", "Us", "Up", "No"} {
		if canonicalTitle(title) == "" {
			t.Errorf("%q canonicalised to nothing", title)
		}
	}
	// Inside brackets it is unambiguous and must still be removed.
	if got := canonicalTitle("Sonic The Hedgehog ( U) [!]"); got != "sonic the hedgehog" {
		t.Errorf("a bracketed region code survived: %q", got)
	}
	if s := matchScore("The Last of Us", "Last From Us (19xx)(Uzi)"); s >= matchAccept {
		t.Errorf("an Amiga demo scored %.2f as The Last of Us", s)
	}
}

// The other half of the same trap: the scoring stopword list has to be tiny.
// With "of" and "from" both discarded as stopwords, those two titles reduce to
// the same two words.
func TestOnlyTheLeadingArticleIsFree(t *testing.T) {
	// The article genuinely is free -- these are the same show.
	if s := matchScore("The Simpsons", "Simpsons"); s < matchAccept {
		t.Errorf("an article changed the show: %.2f", s)
	}
	// A preposition is not.
	for _, p := range [][2]string{
		{"The Last of Us", "Last From Us"},
		{"Journey to the Centre of the Earth", "Journey From the Centre of the Earth"},
	} {
		if s := matchScore(p[0], p[1]); s >= matchAccept {
			t.Errorf("matchScore(%q, %q) = %.2f", p[0], p[1], s)
		}
	}
	// An exact identity still beats a merely-article-different one.
	if matchScore("The Simpsons", "The Simpsons") <= matchScore("The Simpsons", "Simpsons") {
		t.Error("the exact title did not win its own tie")
	}
}

// Two works can share a name. The title alone says nothing about which, and
// pretending otherwise resolved Parasite (2019) onto Parasite (1982) and The
// Rookie (2018) onto a 1990 Clint Eastwood film.
func TestAYearInTheTitleIsKeptEvenThoughTheNameIsNot(t *testing.T) {
	if got := canonicalTitle("Parasite 1982"); got != "parasite" {
		t.Errorf("the name is not just the name: %q", got)
	}
	if got := titleYear("Parasite 1982"); got != 1982 {
		t.Errorf("titleYear = %d, want 1982", got)
	}
	// archive.org's own `year` field is often absent, so the brackets are
	// frequently the only place the year appears at all.
	if got := titleYear("The Rookie ( 1990)"); got != 1990 {
		t.Errorf("titleYear = %d, want 1990", got)
	}
	if got := titleYear("Super Mario World"); got != 0 {
		t.Errorf("invented a year: %d", got)
	}
	// Same name, different work: the titles are identical and must score so.
	// Telling them apart is the caller's job, with the year.
	if s := matchScore("Parasite", "Parasite 1982"); s < matchAccept {
		t.Errorf("the two Parasites do not even look alike: %.2f", s)
	}
}
