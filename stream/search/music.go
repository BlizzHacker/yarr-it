package main

// The music domain.
//
// WHAT WAS THERE BEFORE: NOTHING.
//
// `archiveScopes` in archive.go had no `audio`/`music` entry, so scopeFor()
// answered false for the whole domain. Measured against the live service on
// 2026-08-08, every one of these returned zero cards with sources
// `{"archive":"none"}`:
//
//	/api/search?kind=audio&q=miles davis
//	/api/search?kind=audio&q=grateful dead
//	/api/search?kind=audio&q=beethoven
//	/api/search?kind=audio            (browse)
//
// With no scope, the domain fell through to the torrent indexers alone, which
// means a music search answered nothing whenever they were slow or down -- and
// the one audio row on the landing page was `ia-audiobooks`, which is LibriVox.
// Audiobooks are not music; schema.json files them under `literature` on
// purpose, and it says why.
//
// The Internet Archive holds an enormous amount of music that is free to hear
// and that nobody had pointed this at. This file is the scope, the query and
// the card. music_match.go ranks what comes back, music_item.go turns one item
// into playable tracks, music_rows.go puts it on the landing page.
//
// WHAT COUNTS AS MUSIC HERE, AND WHY IT IS NOT "mediatype:(audio)"
//
// `mediatype:(audio)` is 2.9 million items and is mostly not music: podcasts,
// lectures, radio, LibriVox, phone recordings, and a large quantity of ripped
// commercial albums somebody uploaded. Pointing a music domain at it would
// repeat exactly the mistake discover_resolve.go documents for film, where a
// plain `mediatype:(movies)` scope resolved the film shelf onto community
// uploads of Spider-Man and Interstellar.
//
// So the scope is four named, curated sources, and every one of them pairs a
// collection with a per-item assertion -- the discipline linear_source_archive.go
// arrived at when a "classic TV" collection turned out to contain a Twilight
// Zone rip. Neither half is trusted alone.
//
// WHAT WAS DELIBERATELY LEFT OUT, HAVING BEEN MEASURED
//
//	audio_music          504,134 items. Broad and user-contributed. Its answer
//	                     to "miles davis" is `davis-miles-1958-miles-ahead-side-a`,
//	                     an LP rip of a 1958 Columbia record.
//	opensource_audio     2,922,236 items, and its most-downloaded three are
//	                     "ENGLISH Questions", "6a 0ddd 605c 08221" and
//	                     `geometry_dash_1.9`. It is a dumping ground.
//	unlockedrecordings   23,391 items, a real Archive programme -- but its
//	                     most-downloaded include "Beethoven's Greatest Hits"
//	                     (Columbia, 1969), which carries no licence, no
//	                     restriction marker and is plainly still in copyright.
//	                     Excluded for the reason literature excludes
//	                     `internetarchivebooks`: it is a lending programme, and
//	                     this domain is what is free to hear now.
//	"anything asserting  402,183 items, and it was tried. The top twelve are
//	  a CC/PD licence"   LibriVox audiobooks, and further down sit `Bitches
//	                     Brew` (Miles Davis, Columbia 1970) and Edith Piaf's
//	                     1960 Columbia single, both riding an uploader's CC tag
//	                     on a recording that is not theirs to license. An
//	                     assertion by whoever uploaded the file is not an
//	                     assertion about the file.
//
// All counts measured against live archive.org on 2026-08-08.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// domainMusic is schema.json's id for this domain. Written once here rather
// than spelled inline, because the domain answers to "music", "audio", "song",
// "album" and five other aliases and every one of them must land on the same
// branch -- see canonicalDomain.
const domainMusic = "music"

// --- the scope ---------------------------------------------------------------

// musicPlayable is the honesty gate every leg shares.
//
// `format:("VBR MP3")` is the Archive's own browser-playable derivative, and
// requiring it is the same check archiveCuratedFilm makes with `format:(MPEG4)`
// for the same reason: an item with no derivative a browser can play is a
// details page, not a record. It is not theoretical -- `musopen` holds 34 items
// and 32 of them are ZIP bundles with no audio file at all, so without this the
// Musopen shelf would be almost entirely dead buttons.
const musicPlayable = `format:("VBR MP3")`

// musicScopeLive is the Live Music Archive: 293,163 concerts, and the single
// best thing on archive.org that a music section can offer.
//
// `mediatype:(etree)` rather than `collection:(etree)`, and the difference is
// not cosmetic. Sorted by downloads, `collection:(etree)` returns the ARTIST
// PAGES first -- `GratefulDead`, `PhilLeshandFriends`, `moe` -- which are
// mediatype:collection items with no audio in them at all, and whose own
// `collection` field carries every user who has favourited them (the Grateful
// Dead's runs to several thousand `fav-*` entries). The concerts are
// mediatype:etree; measured, 293,091 of the 302,524 hits.
//
// The licence question for this collection is answered by admission rather than
// per item, and that is not a shortcut: the Live Music Archive only accepts
// bands that have granted permission for their live recordings to be traded
// freely, which is what the collection IS. Measured on the Barton Hall show:
// there is no `licenseurl` field on an etree item at all, so a licence clause
// here would return nothing rather than filter anything.
//
// What IS asserted per item is what may be DONE with it: many etree items carry
// `stream_only` / `access-restricted-item`, the band's own "you may listen, you
// may not take a copy". That is read on the item in music_item.go and honoured
// there, exactly as play_archive.go honours the same marker.
const musicScopeLive = `mediatype:(etree)`

// musicScopeEarly is shellac whose US copyright has run out.
//
// The year is the per-item assertion, and the boundary is a legal fact rather
// than a feel: under the Music Modernization Act, sound recordings first
// published before 1923 are public domain outright, and those published 1923
// to 1946 enter the public domain 100 years after publication. In 2026 that
// makes 1925 the last safe year, and the boundary moves forward by one on every
// 1 January. It is written as a literal rather than computed from time.Now()
// because a scope that silently widens itself at midnight on New Year's Eve is
// not something anybody reviewed.
//
// This is stricter than "the Great 78 Project", and deliberately. That
// programme's collection holds "Mr. Sandman" (1954) and "La Vie En Rose"
// (1960); both are digitised shellac and neither is out of copyright. The
// collection is the Archive's, the copyright call is ours, and 1925 is the one
// we can defend.
//
// 46,823 items, of which 46,336 have a playable derivative.
const musicScopeEarly = `mediatype:(audio) AND ` +
	`collection:(georgeblood OR 78rpm) AND year:[1877 TO 1925]`

// musicScopeFree is freely-licensed modern music: the netlabel scene.
//
// Here the per-item assertion is the licence, and unlike the "anything with a
// CC tag" experiment recorded at the top of this file it is trustworthy,
// because a netlabel release is published BY its label ON these terms. The
// uploader and the rights holder are the same party, which is the whole
// difference between this and somebody's rip of Bitches Brew wearing a CC mark.
//
// Every Creative Commons licence is accepted, not only the public-domain ones.
// That is a different judgement from linear_source_archive.go, which takes only
// public domain, and the reason is what is being done: a linear channel
// BROADCASTS a work on a schedule of ours, while this hands somebody a link to
// the Archive's own file and they press play. Verbatim redistribution is
// permitted by every CC licence including by-nc-nd, and no byte crosses this
// relay.
//
// 61,115 items, 53,543 with a playable derivative.
const musicScopeFree = `mediatype:(audio) AND collection:(netlabels) AND ` +
	`licenseurl:(*creativecommons.org*)`

// musicScopeOpen is Musopen: modern recordings of classical works, released
// CC0 or public-domain-marked by the people who made them.
//
// Two items survive the playability gate out of 34, so this leg is small enough
// to look pointless. It is here because one of the two is "Musopen - The
// Complete Chopin Collection", which is the only free, legally clean, complete
// recording of a major composer on the whole service -- and because free
// classical is the thinnest part of this domain, so what exists of it is worth
// naming.
const musicScopeOpen = `mediatype:(audio) AND collection:(musopen)`

// archiveMusicScope is the four legs together.
//
// 390,343 items, every one of which has a derivative a browser will play.
var archiveMusicScope = "(" +
	"(" + musicScopeLive + ")" +
	" OR (" + musicScopeEarly + ")" +
	" OR (" + musicScopeFree + ")" +
	" OR (" + musicScopeOpen + ")" +
	") AND " + musicPlayable

// Registration.
//
// scopeFor() reads archiveScopes, and three things downstream ask it whether a
// domain has an archive.org source at all: handleSearch's "nothing here could
// ever answer this" gate, searchJob.run's decision to run the archive leg, and
// archiveSuggestions. Without an entry, kind=music answers 503 on an instance
// with no torrent indexer -- which is precisely the instance where the Archive
// is supposed to be the whole catalogue.
//
// It is registered here rather than written into the map literal in archive.go
// because that file is being edited elsewhere at the time of writing. The two
// are equivalent and the literal is the better home; moving this line into
// archiveScopes and deleting this init changes nothing.
//
// The value is the same scope archiveMusicScope holds, so a caller that reaches
// searchArchive directly (the generic path) gets the right catalogue with
// generic ranking, and one that reaches searchArchiveMusic gets the same
// catalogue with music-shaped queries and music-shaped ranking. They can differ
// in quality; they can never differ in what they are allowed to return.
func init() {
	archiveScopes[domainMusic] = archiveMusicScope
}

// --- what a music card carries beyond a title --------------------------------

// musicFacts is the part of a result that only makes sense for music.
//
// It hangs off `card` as one optional object rather than as six optional
// fields, so a card for a film or a ROM is unchanged on the wire and a client
// that knows nothing about music sees exactly what it saw before.
//
// TRACKS ARE NOT IN HERE, AND THAT IS THE POINT OF music_item.go. A track list
// costs one metadata request per item, and a search returns sixty items; doing
// that here would add a minute of somebody else's rate limit to every search
// for a list most people never open. It is the same trade play_archive.go makes
// for the ROM inside a game item, and it is made in the same place: at open
// time, once, for the one item somebody actually chose.
type musicFacts struct {
	// Artist is who made it: archive.org's `creator`. For a concert this is the
	// band; for a 78 it is whoever is on the label, which is frequently several
	// people and occasionally the composer as well.
	Artist string `json:"artist,omitempty"`
	// Form says what shape the thing is, because "a concert" and "a single" are
	// not the same object even though both are audio. `concert` | `release`.
	Form string `json:"form,omitempty"`
	// Venue and Place are what makes a concert card mean anything: "Barton Hall,
	// Cornell University" and "Ithaca, NY". Empty for everything else.
	Venue string `json:"venue,omitempty"`
	Place string `json:"place,omitempty"`
	// Date is the recording date, `YYYY-MM-DD`, when the item states one. For
	// live music this is the identity of the show -- two Grateful Dead cards
	// differ by nothing else.
	Date string `json:"date,omitempty"`
	// Language is what the item SAYS it is in, verbatim and un-normalised, or
	// empty when it says nothing. Empty is a first-class value and is not
	// "unknown means foreign": see musicLanguageAllows.
	Language string `json:"language,omitempty"`
	// Licence is the licence URL the item carries, when it carries one. Present
	// so a person can check the claim this domain is making on their behalf
	// rather than take it.
	Licence string `json:"licence,omitempty"`
}

// --- the query ---------------------------------------------------------------

// musicClauseFor turns a query into the half of a Solr query that names a work.
//
// It asks about the CREATOR as well as the title, which the generic path in
// archive.go does not, and that single difference is most of what makes a music
// search work. Measured against live archive.org:
//
//	title:(louis AND armstrong)                          4 hits
//	title:(...) OR creator:(...)                       399 hits
//
// The reason is structural rather than incidental. A film or a ROM is filed
// under its own name and nothing else, so its title is its identity. A
// recording is filed under the name of the WORK -- "Tears", "Crazy Blues",
// "When The Saints Go Marching In" -- and the artist lives in a separate field.
// Searching only titles for "louis armstrong" asks for a record CALLED Louis
// Armstrong, which is not what anybody means.
//
// The terms themselves come from archiveQueryTokens in match.go, unchanged, so
// the escaping guarantee and the roman-numeral handling are the same ones the
// rest of the service relies on and there is no second opinion about what a
// query says.
func musicClauseFor(q string) string {
	toks := archiveQueryTokens(q)
	if len(toks) == 0 {
		return ""
	}
	joined := strings.Join(toks, " AND ")
	return "(title:(" + joined + ") OR creator:(" + joined + "))"
}

// musicQueryFor is the whole Solr query for a music search, or "" for a browse.
//
// Note what is NOT here: the language filter. It is applied in Go, on the cards,
// and musicLanguageAllows says why.
func musicQueryFor(q string) string {
	clause := musicClauseFor(q)
	if clause == "" {
		// A browse: everything in the scope, ordered by the Archive's own
		// popularity signal. Same rule archiveQueryFor follows -- an empty term
		// is a category, not a search for nothing.
		return archiveMusicScope
	}
	return clause + " AND " + archiveMusicScope
}

// --- language ----------------------------------------------------------------

// "english please".
//
// THE MEASUREMENT THIS TURNS ON
//
// A hard language filter would destroy this domain, and the numbers are not
// close. Measured against live archive.org on 2026-08-08:
//
//	                 items     state a language    say English
//	etree          293,163           13,309 (5%)       13,248
//	Great 78       187,034          186,229 (99%)     132,089
//	netlabels       76,954            5,762 (7%)        3,731
//
// Ninety-five per cent of the Live Music Archive does not fill in the language
// field. It is not foreign-language material; it is the Grateful Dead, and
// nobody typed "English" into a form in 2004. Requiring the field would take
// the live-music catalogue from 293,163 to 13,248 and would look, from the
// front, exactly like the empty domain this work started from.
//
// So the rule is: AN ITEM THAT STATES A LANGUAGE MUST MATCH; AN ITEM THAT
// STATES NOTHING IS KEPT. Absence of a claim is not a claim of absence. What
// this actually removes from a live-music browse is 61 items out of 293,163 --
// the ones that really are in Spanish or German -- and what it removes from an
// early-recordings browse is 2,854 of 28,176, which is the whole point, because
// that collection is a third non-English and a person who cannot read the label
// has no way to tell before pressing play.
//
// IT IS ALSO REVERSIBLE, WHICH IS HALF THE REQUIREMENT
//
// `lang=any` turns it off completely and `lang=fr` asks for something else.
// This matters more than the default: a filter that cannot be switched off does
// not narrow a catalogue, it replaces it, and the person on the other side has
// no way to discover what they are no longer being shown. The facet count is
// published alongside the results so the number is visible rather than implied.
//
// AND IT IS APPLIED IN GO, NOT IN SOLR
//
// The Archive can express this rule -- `(language:(eng OR english) OR (*:* AND
// -language:[* TO *]))` was measured and works -- and it is still the wrong
// place for a SEARCH to do it. The result cache is keyed on the query and the
// kind, so narrowing upstream would mean the first person to search with the
// filter on poisons the cache for everybody who searches with it off. Filtering
// the cards instead means one fetch serves every setting, switching the filter
// costs no upstream traffic at all, and the numbers in the facet are real. That
// is filter.go's whole design and this follows it.
//
// The Solr form is still used, in exactly one place where it is right: the
// landing-page shelves in music_rows.go, which are built once every three hours
// and have no per-request setting to respect.

// musicLanguages maps the codes a caller sends onto every spelling archive.org
// actually stores. Both halves are real: the Great 78 Project writes "English"
// and "German" in full, while LibriVox and the netlabels write "eng" and "fre".
// Measured -- `language:("en")` matches zero items in the Great 78 corpus and
// `language:("English")` matches 131,828 -- so a table that knew only ISO codes
// would silently keep nothing.
var musicLanguages = map[string][]string{
	"en": {"eng", "english", "en"},
	"fr": {"fre", "fra", "french", "fr"},
	"de": {"ger", "deu", "german", "de"},
	"es": {"spa", "spanish", "es"},
	"it": {"ita", "italian", "it"},
	"pt": {"por", "portuguese", "pt"},
	"nl": {"dut", "nld", "dutch", "nl"},
	"ru": {"rus", "russian", "ru"},
	"ja": {"jpn", "japanese", "ja"},
	"zh": {"chi", "zho", "chinese", "zh"},
	"ko": {"kor", "korean", "ko"},
	"hi": {"hin", "hindi", "hi"},
	"ar": {"ara", "arabic", "ar"},
	"sv": {"swe", "swedish", "sv"},
	"pl": {"pol", "polish", "pl"},
	"yi": {"yid", "yiddish", "yi"},
	"la": {"lat", "latin", "la"},
	"he": {"heb", "hebrew", "he"},
	"el": {"gre", "ell", "greek", "el"},
	"tr": {"tur", "turkish", "tr"},
}

// musicLanguageDefault is what a caller who says nothing gets.
//
// Wade asked for "english please" and this is the sensible reading of it: the
// results should be things he can understand without him having to configure
// anything. It is a default and not a policy -- `lang=any` is one parameter
// away and the response says how many results it is costing.
const musicLanguageDefault = "en"

// musicLanguageOff are the spellings that mean "do not filter". Three of them
// because three different clients would each reach for a different one, and a
// person who types `lang=off` and gets an English-only result set has been
// ignored rather than answered.
var musicLanguageOff = map[string]bool{
	"any": true, "all": true, "off": true, "*": true,
}

// normaliseLanguage resolves what a caller asked for. It returns "" for "do not
// filter", which includes anything unrecognised -- the same rule
// canonicalDomain follows, and for the same reason: a filter nothing can
// satisfy is indistinguishable from a broken server, and showing everything is
// at least an arguable answer.
func normaliseLanguage(want string) string {
	w := strings.ToLower(strings.TrimSpace(want))
	if w == "" {
		return musicLanguageDefault
	}
	if musicLanguageOff[w] {
		return ""
	}
	if _, ok := musicLanguages[w]; ok {
		return w
	}
	// A spelling rather than a code: "english", "eng", "French".
	for code, names := range musicLanguages {
		for _, n := range names {
			if n == w {
				return code
			}
		}
	}
	return ""
}

// musicLanguageAllows is the whole rule, in one place, so the Solr clause below
// and the card filter can never disagree about what a language means.
//
// `stated` is what the item said about itself, verbatim, and may be several
// languages separated by anything -- an item genuinely catalogued as both
// English and Japanese is both.
func musicLanguageAllows(want, stated string) bool {
	if want == "" {
		return true // filter off
	}
	stated = strings.TrimSpace(stated)
	if stated == "" {
		// The item did not say. Ninety-five per cent of the Live Music Archive
		// is in this state and it is not evidence of anything.
		return true
	}
	names, ok := musicLanguages[want]
	if !ok {
		return true
	}
	for _, field := range strings.FieldsFunc(stated, func(r rune) bool {
		return r == ',' || r == ';' || r == '/' || r == '|' || r == ' '
	}) {
		f := strings.ToLower(strings.TrimSpace(field))
		for _, n := range names {
			if f == n {
				return true
			}
		}
	}
	return false
}

// musicLanguageClause is the same rule expressed to Solr, for the shelves.
//
// The `*:*` in the second half is not decoration: a bare leading negation is
// not a query Solr can start from, so the "field is absent" half has to be
// anchored to something. Measured with and without -- both forms return
// 293,102 of etree's 293,163 -- but the anchored one is the portable spelling.
func musicLanguageClause(want string) string {
	if want == "" {
		return ""
	}
	names, ok := musicLanguages[want]
	if !ok {
		return ""
	}
	return "(language:(" + strings.Join(names, " OR ") +
		") OR (*:* AND -language:[* TO *]))"
}

// --- fetching ----------------------------------------------------------------

// musicRows is how many results one music search asks for. The same 60 the
// generic archive search uses; there is no reason for music to be different and
// a good reason for it not to be -- two numbers drift.
const musicSearchRows = 60

// musicDoc is a music result as the search index returns it.
//
// Every string field is flexString or flexStrings for the reason archive.go
// documents at length: every field in this schema is multi-valued, and one item
// catalogued with two titles used to fail the decode for a whole page of
// results rather than for itself. `creator` is a list far more often here than
// anywhere else in the service -- a 78 label routinely names the singer, the
// band, the composer and the lyricist -- so this is not a theoretical concern
// in this domain, it is the common case.
type musicDoc struct {
	Identifier string          `json:"identifier"`
	Title      flexString      `json:"title"`
	Creator    flexStrings     `json:"creator"`
	MediaType  flexString      `json:"mediatype"`
	Venue      flexString      `json:"venue"`
	Coverage   flexString      `json:"coverage"`
	Date       flexString      `json:"date"`
	Language   flexStrings     `json:"language"`
	LicenseURL flexString      `json:"licenseurl"`
	Downloads  int             `json:"downloads"`
	Year       json.RawMessage `json:"year"`
	Collection flexStrings     `json:"collection"`
}

type musicResponse struct {
	Response struct {
		NumFound int        `json:"numFound"`
		Docs     []musicDoc `json:"docs"`
	} `json:"response"`
}

// musicFields is what is asked for. Deliberately does NOT include `subject`:
// it is free text, absent on most of the corpus, and asking for it on sixty
// results costs bytes for a field nothing reads.
var musicFields = []string{
	"identifier", "title", "creator", "mediatype", "venue", "coverage",
	"date", "language", "licenseurl", "downloads", "year", "collection",
}

// musicCacheKey namespaces music results away from both the merged search
// results and the generic archive.org results for the same query.
//
// It has to be distinct from archiveCacheKey even though the kind is in both,
// because the two paths ask DIFFERENT QUESTIONS of the same catalogue -- one
// searches titles, the other titles and creators -- and serving one as the
// other would quietly halve the results for a music search that happened to
// follow a type-ahead through the generic path.
//
// The language is NOT part of the key, and that is the point of filtering in
// Go: one cached fetch answers every language setting.
func musicCacheKey(q string) string {
	return "ia-music\x00" + strings.ToLower(strings.TrimSpace(q))
}

// archiveSuggestKey is the cache key type-ahead should read for a given domain.
//
// It exists so that "which key holds the answer" is decided in exactly one
// place, next to the branch that decides which fetcher writes it. Reading the
// wrong one is silent: the suggestion path finds nothing, asks the Archive
// again on every keystroke, and the only symptom is that type-ahead is slower
// than it should be for one domain.
func archiveSuggestKey(q, kind string) string {
	if canonicalDomain(kind) == domainMusic {
		return musicCacheKey(q)
	}
	return archiveCacheKey(q, kind)
}

// searchArchiveDomain is the one entry point for "ask archive.org about this
// search", and it exists so that the two callers who need it -- the search job
// and type-ahead -- cannot end up on different sides of the music branch.
//
// Everything that is not music goes to searchArchiveCached exactly as before.
func (s *server) searchArchiveDomain(ctx context.Context, q, kind string) ([]card, error) {
	if canonicalDomain(kind) == domainMusic {
		return s.searchArchiveMusicCached(ctx, q)
	}
	return s.searchArchiveCached(ctx, q, kind)
}

// searchArchiveMusicCached is searchArchiveMusic with the result kept, and with
// concurrent callers for the same query collapsed onto one request.
//
// Same shape as searchArchiveCached and for the same reasons; see the comment
// there. It is a separate function rather than a parameter because the cache
// key differs, and a shared function with two key schemes is how the two
// namespaces would eventually collide.
func (s *server) searchArchiveMusicCached(ctx context.Context, q string) ([]card, error) {
	key := musicCacheKey(q)
	if cards, ok := s.getCached(key); ok {
		return cards, nil
	}

	wait, leader := s.claim(key)
	if !leader {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if cards, ok := s.getAny(key); ok {
			return cards, nil
		}
		// The leader failed. Asking again is right: two failures are cheaper
		// than a silent empty result.
	} else {
		defer s.release(key)
	}

	cards, err := s.searchArchiveMusic(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(cards) > 0 {
		s.putCached(key, cards)
	}
	return cards, nil
}

// searchArchiveMusic queries archive.org and returns one card per recording.
func (s *server) searchArchiveMusic(ctx context.Context, q string) ([]card, error) {
	query := musicQueryFor(q)
	if query == "" {
		return nil, nil
	}

	params := url.Values{}
	params.Set("q", query)
	for _, f := range musicFields {
		params.Add("fl[]", f)
	}
	params.Set("rows", fmt.Sprint(musicSearchRows))
	params.Set("page", "1")
	params.Set("output", "json")
	params.Add("sort[]", "downloads desc")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		archiveSearchAPI+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")

	resp, err := archiveClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("archive.org music search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive.org music search: status %d", resp.StatusCode)
	}

	var out musicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("archive.org music search: %w", err)
	}
	return rankMusic(musicCards(out.Response.Docs), q), nil
}

// --- cards --------------------------------------------------------------------

// musicArtist picks the name to show from archive.org's `creator`.
//
// The first value, which is the one their own item page leads with, and which
// on a 78 label is the performer rather than the composer -- "Enrico Caruso;
// Buzzi; Pecia" leads with Caruso. Every value is still searched (the Solr
// field is multi-valued), so filing the card under the first costs nothing in
// findability.
func musicArtist(creator []string) string {
	for _, c := range creator {
		if c = strings.TrimSpace(c); c != "" {
			return c
		}
	}
	return ""
}

// musicDate trims archive.org's date to the day.
//
// They send `1977-05-08T00:00:00Z` from the search index and `1977-05-08` from
// the metadata API, for the same field on the same item. The time is always
// midnight UTC and means nothing; a show has a date, not a moment.
func musicDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, 'T'); i > 0 {
		raw = raw[:i]
	}
	if len(raw) > 10 {
		raw = raw[:10]
	}
	return raw
}

// musicForm says what shape an item is.
//
// `concert` and `release` are both in schema.json's music vocabulary in spirit
// -- the domain lists artist/album/track/playlist -- and the distinction is
// carried because a client renders them differently: a concert wants its venue
// and date on the card, a single wants its year.
func musicForm(mediaType string) string {
	if strings.EqualFold(strings.TrimSpace(mediaType), "etree") {
		return "concert"
	}
	return "release"
}

// musicSourceFor builds the one way to play a music item.
//
// ONE source, not two, and the reason is ordering rather than taste. rankSources
// scores a source on seeders, web-safety, quality and whether it is an emulator
// -- and two archive.org music sources are identical on every one of those, so
// the sort that decides `Best` has nothing to separate them and sort.Slice is
// not stable. Which player a card defaulted to would then vary between
// identical requests. A client that wants the Archive's own page has it: the
// item response carries `details`, which is exactly that.
//
// The `#music` fragment is read by the browser's archive resolver, which turns
// it into a track list rather than an iframe -- the same mechanism `#ejs` and
// `#swf` already use for games, so there is one convention here and not three.
func musicSourceFor(d musicDoc, title string) []source {
	return []source{{
		Title:   title,
		Indexer: "Archive.org",
		Magnet:  "https://archive.org/details/" + d.Identifier + "#music",
		Source:  musicSourceLabel(d),
		WebSafe: true,
	}}
}

// musicSourceLabel is what the card says this came from, in the place a film
// card names its release group and a game card names its console.
//
// It names the collection rather than the format, because "the Live Music
// Archive" tells somebody what kind of recording they are about to hear -- a
// soundboard tape of a show -- and "MP3" tells them nothing they cannot guess.
func musicSourceLabel(d musicDoc) string {
	if musicForm(d.MediaType.String()) == "concert" {
		return "Live Music Archive"
	}
	for _, c := range d.Collection {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "georgeblood":
			return "Great 78 Project"
		case "netlabels":
			return "Netlabels"
		case "musopen":
			return "Musopen"
		}
	}
	for _, c := range d.Collection {
		if strings.EqualFold(strings.TrimSpace(c), "78rpm") {
			return "78rpm"
		}
	}
	return "Archive.org"
}

// musicCards turns search documents into cards.
func musicCards(docs []musicDoc) []card {
	cards := make([]card, 0, len(docs))
	seen := make(map[string]bool, len(docs))
	for _, d := range docs {
		if d.Identifier == "" || seen[d.Identifier] {
			continue
		}
		seen[d.Identifier] = true

		title := strings.TrimSpace(d.Title.String())
		if title == "" {
			title = d.Identifier
		}
		artist := musicArtist(d.Creator)

		facts := &musicFacts{
			Artist:   artist,
			Form:     musicForm(d.MediaType.String()),
			Venue:    strings.TrimSpace(d.Venue.String()),
			Place:    strings.TrimSpace(d.Coverage.String()),
			Date:     musicDate(d.Date.String()),
			Language: strings.Join(d.Language, ", "),
			Licence:  strings.TrimSpace(d.LicenseURL.String()),
		}

		cards = append(cards, card{
			// The same check the shelves and the generic search apply. The
			// netlabel scene includes releases titled, in full, "Porn Music For
			// The Masses"; nothing about being a music result exempts it.
			Adult:    isAdultItem(title, d.Identifier, d.Collection),
			Key:      "ia:" + d.Identifier,
			Title:    title,
			Year:     archiveYear(d.Year),
			Kind:     domainMusic,
			Instant:  true,
			Popular:  d.Downloads,
			Platform: facts.Artist,
			// "music" is what categoryGroups calls this bucket, so the Music
			// chip in the filter bar selects these. A card without it is hidden
			// the moment somebody narrows to exactly what they wanted.
			Groups:  []string{"music"},
			Music:   facts,
			Sources: musicSourceFor(d, title),
			Art: artwork{
				Poster: "https://archive.org/services/img/" + d.Identifier,
				Found:  true,
			},
		})
	}
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].Popular > cards[j].Popular })
	return cards
}
