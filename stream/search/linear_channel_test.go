package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- the fixture library ----------------------------------------------------
//
// A small, fully deterministic stand-in for a media server: four shows with
// their episodes in order, plus a shelf of films. Every assertion below is
// about behaviour this fixture makes checkable by hand.

func linearFixtureItems() []LinearItem {
	shows := []struct {
		id, name, genre, network, rating string
		year                             int
		mins                             int
	}{
		{"alpha", "Alpha Show", "Sci-Fi", "ABC", "TV-PG", 1994, 22},
		{"bravo", "Bravo Show", "Comedy", "NBC", "TV-14", 2001, 24},
		{"cine", "Cine Show", "Horror", "HBO", "TV-MA", 1988, 26},
		{"delta", "Delta Show", "Comedy", "ABC", "TV-G", 2015, 22},
	}
	var out []LinearItem
	for _, s := range shows {
		for ep := 1; ep <= 8; ep++ {
			season, num := 1, ep
			if ep > 4 {
				season, num = 2, ep-4
			}
			out = append(out, LinearItem{
				MediaItem: MediaItem{
					CanonicalID: fmt.Sprintf("fix:%s:s%02de%02d", s.id, season, num),
					Domain:      "video",
					Type:        "episode",
					Title:       fmt.Sprintf("%s S%02dE%02d", s.name, season, num),
					Subtitle:    s.name,
					Year:        s.year,
					Season:      season,
					Episode:     num,
				},
				DurationSeconds: s.mins * 60,
				Genres:          []string{s.genre},
				Network:         s.network,
				Rating:          s.rating,
				SeriesID:        s.id,
			})
		}
	}
	films := []struct {
		id, title string
		genres    []string
		year      int
		rating    string
		mins      int
	}{
		{"f1", "Night of the Reels", []string{"Horror"}, 1981, "R", 92},
		{"f2", "Second City", []string{"Comedy", "Drama"}, 1999, "PG-13", 104},
		{"f3", "Third Orbit", []string{"Sci-Fi"}, 2011, "PG", 118},
		{"f4", "Fourth Wall", []string{"Comedy"}, 1986, "PG", 96},
		{"f5", "Fifth Season", []string{"Drama", "Horror"}, 1994, "R", 110},
		{"f6", "Sixth Sense of Humour", []string{"Comedy"}, 2020, "PG-13", 88},
	}
	for _, f := range films {
		out = append(out, LinearItem{
			MediaItem: MediaItem{
				CanonicalID: "fix:film:" + f.id,
				Domain:      "video",
				Type:        "movie",
				Title:       f.title,
				Year:        f.year,
			},
			DurationSeconds: f.mins * 60,
			Genres:          f.genres,
			Network:         "Cinema",
			Collection:      "Fixture Films",
			Rating:          f.rating,
		})
	}
	return out
}

func linearFixtureLibrary() *linearSliceLibrary {
	return &linearSliceLibrary{provider: "fixture", library: "main", items: linearFixtureItems()}
}

func linearTestChannel(id string, strategy LinearStrategy) LinearChannel {
	c := LinearChannel{
		ID: id, Number: 101, Name: "Test " + id,
		Enabled: true, SourceProvider: "fixture",
		ScheduleStrategy: strategy,
		Timezone:         "UTC",
		EPGDays:          1,
		Seed:             424242,
	}
	c.normalise()
	return c
}

// --- rules ------------------------------------------------------------------

// The grouping is the point: a channel is "(Horror or Sci-Fi) and not the
// 2010s", and expressing that must not require a query language.
func TestLinearRulesSupportNestedAndOrGroups(t *testing.T) {
	pool := linearFixtureItems()

	g := LinearRuleGroup{
		Match: LinearMatchAll,
		Groups: []LinearRuleGroup{{
			Match: LinearMatchAny,
			Rules: []LinearRule{
				{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Horror"},
				{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Sci-Fi"},
			},
		}},
		Rules: []LinearRule{
			{Field: LinearFieldYear, Op: LinearOpBetween, Min: 1980, Max: 1999},
		},
	}
	got := linearApplyRules(pool, g)
	if len(got) == 0 {
		t.Fatal("the grouped rule matched nothing; it should match 80s/90s horror and sci-fi")
	}
	for _, it := range got {
		if it.Year < 1980 || it.Year > 1999 {
			t.Errorf("%q (%d) is outside the year range but matched", it.Title, it.Year)
		}
		if !contains(it.Genres, "Horror") && !contains(it.Genres, "Sci-Fi") {
			t.Errorf("%q has genres %v, neither branch of the OR", it.Title, it.Genres)
		}
	}
	// And the OR really is an OR: both branches contributed.
	var horror, scifi bool
	for _, it := range got {
		horror = horror || contains(it.Genres, "Horror")
		scifi = scifi || contains(it.Genres, "Sci-Fi")
	}
	if !horror || !scifi {
		t.Errorf("only one branch of the OR contributed (horror=%v scifi=%v)", horror, scifi)
	}
}

func TestLinearRulesCoverTheDocumentedFields(t *testing.T) {
	pool := linearFixtureItems()
	cases := []struct {
		name string
		rule LinearRule
		want func(LinearItem) bool
	}{
		{"genre", LinearRule{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Comedy"},
			func(it LinearItem) bool { return contains(it.Genres, "Comedy") }},
		{"year range", LinearRule{Field: LinearFieldYear, Op: LinearOpBetween, Min: 2000, Max: 2030},
			func(it LinearItem) bool { return it.Year >= 2000 }},
		{"network", LinearRule{Field: LinearFieldNetwork, Op: LinearOpIs, Value: "ABC"},
			func(it LinearItem) bool { return it.Network == "ABC" }},
		{"collection", LinearRule{Field: LinearFieldCollection, Op: LinearOpIs, Value: "Fixture Films"},
			func(it LinearItem) bool { return it.Collection == "Fixture Films" }},
		{"title contains", LinearRule{Field: LinearFieldTitle, Op: LinearOpContains, Value: "Show"},
			func(it LinearItem) bool { return strings.Contains(it.Title, "Show") }},
		{"rating", LinearRule{Field: LinearFieldRating, Op: LinearOpIn, Values: []string{"TV-G", "PG"}},
			func(it LinearItem) bool { return it.Rating == "TV-G" || it.Rating == "PG" }},
		{"media type", LinearRule{Field: LinearFieldType, Op: LinearOpIs, Value: "movie"},
			func(it LinearItem) bool { return it.Type == "movie" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.rule.Validate(); err != nil {
				t.Fatalf("rule is not valid: %v", err)
			}
			got := linearApplyRules(pool, LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{tc.rule}})
			if len(got) == 0 {
				t.Fatal("matched nothing")
			}
			var want int
			for _, it := range pool {
				if tc.want(it) {
					want++
				}
			}
			if len(got) != want {
				t.Errorf("matched %d, expected %d", len(got), want)
			}
		})
	}
}

// The preview is the feature: you find out a channel is empty in the editor,
// not on air.
func TestLinearRulePreviewCountsBeforeAnythingIsSaved(t *testing.T) {
	pool := linearFixtureItems()
	g := LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
		{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Comedy"},
	}}
	p := linearPreviewRules(pool, g)

	var want int
	for _, it := range pool {
		if contains(it.Genres, "Comedy") {
			want++
		}
	}
	if p.Matched != want {
		t.Errorf("preview says %d matched, the pool says %d", p.Matched, want)
	}
	if p.TotalPool != len(pool) {
		t.Errorf("preview lost the denominator: totalPool %d, pool %d", p.TotalPool, len(pool))
	}
	if !strings.Contains(p.Detail, fmt.Sprintf("%d programmes matched", want)) {
		t.Errorf("preview detail should read like %q, got %q", fmt.Sprintf("%d programmes matched", want), p.Detail)
	}
	if p.HoursOfContent <= 0 {
		t.Error("preview reported no hours of content for a non-empty match")
	}
}

func TestLinearPreviewSaysSoWhenNothingMatches(t *testing.T) {
	p := linearPreviewRules(linearFixtureItems(), LinearRuleGroup{
		Match: LinearMatchAll,
		Rules: []LinearRule{{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Polka Documentary"}},
	})
	if p.Matched != 0 {
		t.Fatalf("matched %d, want 0", p.Matched)
	}
	if !strings.Contains(p.Detail, "0 programmes matched") {
		t.Errorf("an empty match must say so plainly, got %q", p.Detail)
	}
}

// Items with no duration are counted separately, because a channel advertised
// as 300 programmes that can only schedule 260 is a lie the editor should not
// tell.
func TestLinearPreviewSeparatesMatchedFromSchedulable(t *testing.T) {
	pool := append(linearFixtureItems(), LinearItem{
		MediaItem:       MediaItem{CanonicalID: "fix:broken", Type: "movie", Title: "No Duration Recorded", Year: 1990},
		Genres:          []string{"Horror"},
		DurationSeconds: 0,
	})
	p := linearPreviewRules(pool, LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
		{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Horror"},
	}})
	if p.Skipped != 1 {
		t.Errorf("skipped %d, want 1 (the item with no duration)", p.Skipped)
	}
	if p.Matched != p.Schedulable+p.Skipped {
		t.Errorf("matched %d != schedulable %d + skipped %d", p.Matched, p.Schedulable, p.Skipped)
	}
	if !strings.Contains(p.Detail, "no duration") {
		t.Errorf("the detail must explain the gap, got %q", p.Detail)
	}
}

func TestLinearRuleValidationRejectsWhatItCannotRun(t *testing.T) {
	bad := []LinearRule{
		{Field: "vibes", Op: LinearOpIs, Value: "good"},
		{Field: LinearFieldGenre, Op: "sortaLike", Value: "Horror"},
		{Field: LinearFieldGenre, Op: LinearOpIn},
		{Field: LinearFieldGenre, Op: LinearOpBetween, Min: 1, Max: 2},
		{Field: LinearFieldYear, Op: LinearOpBetween, Min: 2000, Max: 1990},
		{Field: LinearFieldTitle, Op: LinearOpContains, Value: "  "},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("rule %d %+v was accepted; it cannot be evaluated", i, r)
		}
	}
}

// An operator this build does not know must match nothing, not everything.
// Failing open would turn a typo into a channel that plays the whole library.
func TestLinearUnknownOperatorFailsClosed(t *testing.T) {
	r := LinearRule{Field: LinearFieldGenre, Op: "approximately", Value: "Horror"}
	got := linearApplyRules(linearFixtureItems(), LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{r}})
	if len(got) != 0 {
		t.Errorf("an unknown operator matched %d items; it must match none", len(got))
	}
}

// A channel with no rules is the whole library. This is the one case where
// "matched nothing" would be the surprising answer.
func TestLinearEmptyRuleSetMatchesEverything(t *testing.T) {
	pool := linearFixtureItems()
	if got := linearApplyRules(pool, LinearRuleGroup{Match: LinearMatchAll}); len(got) != len(pool) {
		t.Errorf("an empty rule set matched %d of %d", len(got), len(pool))
	}
}

// --- the channel is data ----------------------------------------------------

// Nothing here knows what channels exist. An engine with no configuration has
// no channels, and the two below exist only because a test wrote them down --
// which is exactly how a user creates one.
func TestLinearChannelsAreConfigurationNotCode(t *testing.T) {
	e := NewLinearEngine("")
	if got, _ := e.Channels(context.Background()); len(got) != 0 {
		t.Fatalf("a fresh engine already had %d channels; the lineup is hardcoded somewhere", len(got))
	}
	e.AddLibrary(linearFixtureLibrary())

	for i, spec := range []struct {
		id, name string
		num      int
		genre    string
	}{
		{"ch-horror", "All Horror", 66, "Horror"},
		{"ch-comedy", "Nothing But Comedy", 67, "Comedy"},
	} {
		c := linearTestChannel(spec.id, StrategyCyclicShuffle)
		c.Name, c.Number = spec.name, spec.num
		c.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
			{Field: LinearFieldGenre, Op: LinearOpIs, Value: spec.genre},
		}}
		if _, err := e.SaveChannel(c); err != nil {
			t.Fatalf("channel %d: %v", i, err)
		}
	}
	got, _ := e.Channels(context.Background())
	if len(got) != 2 {
		t.Fatalf("got %d channels, want the 2 that were configured", len(got))
	}
	if got[0].Number != 66 || got[1].Number != 67 {
		t.Errorf("channels came back out of number order: %d, %d", got[0].Number, got[1].Number)
	}
	// The wire shape is the shared one, and its source must be a value the
	// schema knows, or a client cannot render the badge.
	known := map[string]bool{}
	for _, s := range schema.ChannelSources {
		known[s] = true
	}
	for _, c := range got {
		if !known[c.Source] {
			t.Errorf("channel %q reports source %q, which is not in schema.json", c.ID, c.Source)
		}
		if c.ProviderID != linearProviderID {
			t.Errorf("channel %q reports provider %q", c.ID, c.ProviderID)
		}
	}
}

func TestLinearChannelValidationRefusesWhatCannotWork(t *testing.T) {
	base := linearTestChannel("v", StrategyCyclic)
	cases := []struct {
		name   string
		mutate func(*LinearChannel)
	}{
		{"no id", func(c *LinearChannel) { c.ID = "" }},
		{"no name", func(c *LinearChannel) { c.Name = "" }},
		{"no number", func(c *LinearChannel) { c.Number = 0 }},
		{"no source", func(c *LinearChannel) { c.SourceProvider = "" }},
		{"unknown strategy", func(c *LinearChannel) { c.ScheduleStrategy = "VIBES" }},
		{"unknown timezone", func(c *LinearChannel) { c.Timezone = "Mars/Olympus" }},
		{"dayparts missing", func(c *LinearChannel) { c.ScheduleStrategy = StrategyTimeBased }},
		{"bad rule", func(c *LinearChannel) {
			c.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{{Field: "mood", Op: LinearOpIs, Value: "x"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("accepted a channel that cannot work")
			}
		})
	}
}

func TestLinearChannelNumbersAreUnique(t *testing.T) {
	e := NewLinearEngine("")
	e.AddLibrary(linearFixtureLibrary())
	a := linearTestChannel("a", StrategyCyclic)
	b := linearTestChannel("b", StrategyCyclic)
	if _, err := e.SaveChannel(a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SaveChannel(b); err == nil {
		t.Fatal("two channels were allowed to share channel 101")
	}
}

// --- the JSON library adapter ----------------------------------------------

// The shape a real media server's database actually produces: genres joined
// with pipes, studios likewise, and an all-zero GUID where a series id would
// be. Normalising on read is what lets one adapter serve both a hand-written
// fixture and a live export.
func TestLinearJSONLibraryNormalisesRealExportColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lib.json")
	body := `[
	  {"canonicalId":"jf:1","domain":"video","type":"episode","title":"The Ship","subtitle":"1899",
	   "year":2022,"durationSeconds":3602,"genresRaw":"Drama|Horror|Mystery","studiosRaw":"Netflix|Dark Ways",
	   "rating":"TV-MA","seriesRaw":"00000000-0000-0000-0000-000000000000","season":1,"episode":1},
	  {"canonicalId":"jf:2","domain":"movies","type":"movie","title":"A Film",
	   "year":1999,"durationSeconds":5400,"genres":["Comedy"],"seriesRaw":"abc-123"}
	]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := &LinearJSONLibrary{Provider: "jellyfin", Library: "TV", Path: path}
	items, err := lib.LinearItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("loaded %d items, want 2", len(items))
	}
	if got := items[0].Genres; len(got) != 3 || got[0] != "Drama" {
		t.Errorf("pipe-joined genres were not split: %v", got)
	}
	if items[0].Network != "Netflix" {
		t.Errorf("network came from studios as %q, want Netflix", items[0].Network)
	}
	if items[0].SeriesID != "" {
		t.Errorf("the all-zero GUID became series id %q; it means 'no series'", items[0].SeriesID)
	}
	if items[1].SeriesID != "abc-123" {
		t.Errorf("a real series id was dropped: %q", items[1].SeriesID)
	}
	// "movies" is not canonical; it must resolve, not be passed through.
	if items[1].Domain != "video" {
		t.Errorf("domain %q was not canonicalised to video", items[1].Domain)
	}
}

func TestLinearChannelRoundTripsThroughJSON(t *testing.T) {
	c := linearTestChannel("json", StrategyTimeBased)
	c.Options.Dayparts = []LinearDaypart{{
		Name: "Morning", StartMinute: 360, EndMinute: 720,
		Rules:    LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Comedy"}}},
		Strategy: StrategyCyclic,
	}}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back LinearChannel
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.revision() != c.revision() {
		t.Error("a channel that went through JSON came back with a different revision; the schedule would be thrown away on every restart")
	}
}
