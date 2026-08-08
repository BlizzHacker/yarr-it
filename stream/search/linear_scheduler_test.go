package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func linearAllStrategies() []LinearStrategy {
	return []LinearStrategy{
		StrategyRandom, StrategyCyclic, StrategyCyclicShuffle,
		StrategyBlock, StrategyBlockCyclic, StrategyTimeBased, StrategyDayBased,
	}
}

// linearDaypartChannel builds a channel for the two partitioned strategies, so
// the shared assertions below can run over all seven.
func linearDaypartChannel(id string, strategy LinearStrategy) LinearChannel {
	c := linearTestChannel(id, strategy)
	switch strategy {
	case StrategyTimeBased:
		c.Options.Dayparts = []LinearDaypart{
			{Name: "Morning", StartMinute: 6 * 60, EndMinute: 12 * 60, Strategy: StrategyCyclic,
				Rules: LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Comedy"}}}},
			{Name: "Late", StartMinute: 22 * 60, EndMinute: 6 * 60, Strategy: StrategyCyclic,
				Rules: LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Horror"}}}},
			{Name: "Day", StartMinute: 12 * 60, EndMinute: 22 * 60, Strategy: StrategyCyclicShuffle,
				Rules: LinearRuleGroup{Match: LinearMatchAll}},
		}
	case StrategyDayBased:
		c.Options.Dayparts = []LinearDaypart{
			{Name: "Weekend", Weekdays: []int{0, 6}, Strategy: StrategyCyclic,
				Rules: LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Horror"}}}},
			{Name: "Weekday", Weekdays: []int{1, 2, 3, 4, 5}, Strategy: StrategyCyclic,
				Rules: LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Comedy"}}}},
		}
	}
	c.normalise()
	return c
}

func linearFillFor(t *testing.T, ch LinearChannel, from time.Time, hours int) []Program {
	t.Helper()
	got, _, err := linearFill(&ch, linearFixtureItems(), linearCursor{}, from, from.Add(time.Duration(hours)*time.Hour))
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("fill produced no programmes")
	}
	return got
}

var linearEpoch = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// --- the ordering invariant -------------------------------------------------

// programme[n].end <= programme[n+1].start, for every strategy. An overlap is
// two things claiming the same second of air time, and the guide and the
// player would disagree about which one won.
func TestLinearScheduleNeverOverlaps(t *testing.T) {
	for _, s := range linearAllStrategies() {
		t.Run(string(s), func(t *testing.T) {
			got := linearFillFor(t, linearDaypartChannel("ov", s), linearEpoch, 48)
			if err := linearCheckOrdering(got); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// With no slot grid the schedule is gapless too: each programme starts exactly
// when the last one ended. That is the difference between a channel and a
// playlist with pauses in it.
func TestLinearScheduleIsGaplessWithoutASlotGrid(t *testing.T) {
	got := linearFillFor(t, linearTestChannel("gap", StrategyCyclic), linearEpoch, 24)
	for i := 1; i < len(got); i++ {
		if got[i].StartTime != got[i-1].EndTime {
			t.Fatalf("gap of %ds between %q and %q", got[i].StartTime-got[i-1].EndTime, got[i-1].Title, got[i].Title)
		}
	}
}

// With a grid the gaps are deliberate -- the wait for the next half hour --
// and they must still never become overlaps.
func TestLinearSlotGridProducesGapsNotOverlaps(t *testing.T) {
	ch := linearTestChannel("grid", StrategyCyclic)
	ch.Options.SlotMinutes = 30
	got := linearFillFor(t, ch, linearEpoch, 24)
	if err := linearCheckOrdering(got); err != nil {
		t.Fatal(err)
	}
	var gaps int
	for i := 1; i < len(got); i++ {
		if got[i].StartTime > got[i-1].EndTime {
			gaps++
		}
		// Every start must land on the grid.
		if (got[i].StartTime-got[0].StartTime)%(30*60) != 0 {
			t.Fatalf("programme %d starts off the 30-minute grid", i)
		}
	}
	if gaps == 0 {
		t.Error("a 30-minute grid over 22-minute episodes should leave gaps; none appeared")
	}
}

// --- determinism ------------------------------------------------------------

// The seed is the whole testability story: same seed, same pool, same cursor,
// same television. Without it nothing else in this file can be asserted.
func TestLinearScheduleIsDeterministicForASeed(t *testing.T) {
	for _, s := range linearAllStrategies() {
		t.Run(string(s), func(t *testing.T) {
			a := linearFillFor(t, linearDaypartChannel("det", s), linearEpoch, 24)
			b := linearFillFor(t, linearDaypartChannel("det", s), linearEpoch, 24)
			if len(a) != len(b) {
				t.Fatalf("two identical fills produced %d and %d programmes", len(a), len(b))
			}
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("programme %d differed between two identical fills:\n %+v\n %+v", i, a[i], b[i])
				}
			}
		})
	}
}

func TestLinearDifferentSeedsGiveDifferentSchedules(t *testing.T) {
	for _, s := range []LinearStrategy{StrategyRandom, StrategyCyclicShuffle, StrategyBlock} {
		t.Run(string(s), func(t *testing.T) {
			a := linearDaypartChannel("seed", s)
			b := linearDaypartChannel("seed", s)
			b.Seed = a.Seed + 7
			pa := linearFillFor(t, a, linearEpoch, 24)
			pb := linearFillFor(t, b, linearEpoch, 24)
			same := true
			for i := range pa {
				if i >= len(pb) || pa[i].CanonicalID != pb[i].CanonicalID {
					same = false
					break
				}
			}
			if same {
				t.Error("two different seeds produced the same lineup; the seed is not reaching the ordering")
			}
		})
	}
}

// Generation must be resumable, because that is what lets the engine extend a
// schedule instead of rebuilding it. Filling A->B then B->C must give exactly
// what filling A->C gives.
func TestLinearFillIsResumableFromItsCursor(t *testing.T) {
	for _, s := range linearAllStrategies() {
		t.Run(string(s), func(t *testing.T) {
			ch := linearDaypartChannel("res", s)
			pool := linearFixtureItems()
			mid := linearEpoch.Add(9 * time.Hour)
			end := linearEpoch.Add(24 * time.Hour)

			whole, _, err := linearFill(&ch, pool, linearCursor{}, linearEpoch, end)
			if err != nil {
				t.Fatal(err)
			}
			first, cur, err := linearFill(&ch, pool, linearCursor{}, linearEpoch, mid)
			if err != nil {
				t.Fatal(err)
			}
			resume := time.Unix(first[len(first)-1].EndTime, 0).UTC()
			second, _, err := linearFill(&ch, pool, cur, resume, end)
			if err != nil {
				t.Fatal(err)
			}
			joined := append(append([]Program{}, first...), second...)
			if len(joined) != len(whole) {
				t.Fatalf("resumed fill produced %d programmes, one-shot produced %d", len(joined), len(whole))
			}
			for i := range whole {
				if joined[i] != whole[i] {
					t.Fatalf("programme %d differs after resuming:\n resumed %+v\n whole   %+v", i, joined[i], whole[i])
				}
			}
		})
	}
}

// --- episode order ----------------------------------------------------------

// linearShowOrder is broadcast order per show, taken from the fixture itself
// rather than restated, so "in order" means the same thing here as it does in
// the scheduler.
func linearShowOrder() map[string][]string {
	items := linearFixtureItems()
	linearSortItems(items)
	out := map[string][]string{}
	for _, it := range items {
		if it.Subtitle == "" {
			continue
		}
		out[it.Subtitle] = append(out[it.Subtitle], it.CanonicalID)
	}
	return out
}

// linearAssertEpisodeOrder checks that each show's successive appearances step
// exactly one place through its own episode list, wrapping at the end. That is
// what "episode order preserved" has to mean: not merely ascending, but never
// skipping one either.
func linearAssertEpisodeOrder(t *testing.T, programs []Program) {
	t.Helper()
	order := linearShowOrder()
	index := map[string]map[string]int{}
	for show, ids := range order {
		m := map[string]int{}
		for i, id := range ids {
			m[id] = i
		}
		index[show] = m
	}
	prev := map[string]int{}
	for _, p := range programs {
		if p.Subtitle == "" {
			continue
		}
		idx, ok := index[p.Subtitle][p.CanonicalID]
		if !ok {
			t.Fatalf("%q is not an episode of %q", p.Title, p.Subtitle)
		}
		if last, seen := prev[p.Subtitle]; seen {
			want := (last + 1) % len(order[p.Subtitle])
			if idx != want {
				t.Fatalf("%s went from episode index %d to %d, skipping %d",
					p.Subtitle, last, idx, want)
			}
		}
		prev[p.Subtitle] = idx
	}
	if len(prev) == 0 {
		t.Fatal("no episodes appeared at all")
	}
}

func TestLinearCyclicPreservesEpisodeOrder(t *testing.T) {
	linearAssertEpisodeOrder(t, linearFillFor(t, linearTestChannel("cyc", StrategyCyclic), linearEpoch, 12))
}

// The one people mean when they say "shuffle": which show is next is random,
// but S03E12 never comes before S01E01.
func TestLinearCyclicShufflePreservesEpisodeOrderWithinEachShow(t *testing.T) {
	got := linearFillFor(t, linearTestChannel("cs", StrategyCyclicShuffle), linearEpoch, 72)
	linearAssertEpisodeOrder(t, got)

	seen := map[string]bool{}
	for _, p := range got {
		if p.Subtitle != "" {
			seen[p.Subtitle] = true
		}
	}
	if len(seen) < 4 {
		t.Fatalf("only %d shows appeared; the shuffle is not reaching every show", len(seen))
	}
	// And it really did shuffle: the show order is not the sorted order.
	var order []string
	for _, p := range got[:8] {
		order = append(order, p.Subtitle)
	}
	if strings.Join(order[:4], ",") == "Alpha Show,Bravo Show,Cine Show,Delta Show" {
		t.Error("CYCLIC_SHUFFLE produced the plain sorted order; it did not shuffle")
	}
}

func TestLinearBlockPlaysRunsOfOneShow(t *testing.T) {
	for _, s := range []LinearStrategy{StrategyBlock, StrategyBlockCyclic} {
		t.Run(string(s), func(t *testing.T) {
			ch := linearTestChannel("blk", s)
			ch.Options.BlockSize = 3
			ch.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
				{Field: LinearFieldType, Op: LinearOpIs, Value: "episode"},
			}}
			pool := linearApplyRules(linearFixtureItems(), ch.Rules)
			got, _, err := linearFill(&ch, pool, linearCursor{}, linearEpoch, linearEpoch.Add(12*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			// The first three programmes are the same show, in episode order.
			if len(got) < 6 {
				t.Fatalf("only %d programmes", len(got))
			}
			for i := 1; i < 3; i++ {
				if got[i].Subtitle != got[0].Subtitle {
					t.Fatalf("block broken at %d: %q then %q", i, got[0].Subtitle, got[i].Subtitle)
				}
				if got[i].Episode != got[i-1].Episode+1 {
					t.Fatalf("episodes inside a block are out of order: E%02d then E%02d", got[i-1].Episode, got[i].Episode)
				}
			}
			if got[3].Subtitle == got[0].Subtitle {
				t.Error("the block never ended; it should move to another show after 3")
			}
			// And across blocks, a show still picks up exactly where it left
			// off -- blocks reorder shows, never episodes.
			linearAssertEpisodeOrder(t, got)
		})
	}
}

func TestLinearBlockCyclicRotatesShowsInOrder(t *testing.T) {
	ch := linearTestChannel("bc", StrategyBlockCyclic)
	ch.Options.BlockSize = 2
	ch.Rules = LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
		{Field: LinearFieldType, Op: LinearOpIs, Value: "episode"},
	}}
	pool := linearApplyRules(linearFixtureItems(), ch.Rules)
	got, _, err := linearFill(&ch, pool, linearCursor{}, linearEpoch, linearEpoch.Add(8*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Alpha Show", "Alpha Show", "Bravo Show", "Bravo Show", "Cine Show", "Cine Show", "Delta Show", "Delta Show"}
	for i, w := range want {
		if i >= len(got) {
			t.Fatalf("only %d programmes, want at least %d", len(got), len(want))
		}
		if got[i].Subtitle != w {
			t.Fatalf("BLOCK_CYCLIC slot %d is %q, want %q (the rotation is not in order)", i, got[i].Subtitle, w)
		}
	}
}

// --- dayparts ---------------------------------------------------------------

func TestLinearTimeBasedHonoursDaypartRules(t *testing.T) {
	ch := linearDaypartChannel("tb", StrategyTimeBased)
	got := linearFillFor(t, ch, linearEpoch, 48)
	var morning, late int
	for _, p := range got {
		h := time.Unix(p.StartTime, 0).UTC().Hour()
		switch {
		case h >= 6 && h < 12:
			morning++
			if !linearTitleHasGenre(t, p, "Comedy") {
				t.Errorf("%q played in the Comedy morning daypart at %02d:00", p.Title, h)
			}
		case h >= 22 || h < 6:
			late++
			if !linearTitleHasGenre(t, p, "Horror") {
				t.Errorf("%q played in the Horror late daypart at %02d:00", p.Title, h)
			}
		}
	}
	if morning == 0 || late == 0 {
		t.Fatalf("dayparts never fired (morning=%d late=%d)", morning, late)
	}
}

func TestLinearDayBasedHonoursWeekdayRules(t *testing.T) {
	ch := linearDaypartChannel("db", StrategyDayBased)
	// A Monday, so both a weekday and a weekend appear inside a week.
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got := linearFillFor(t, ch, start, 24*8)
	var weekday, weekend int
	for _, p := range got {
		wd := time.Unix(p.StartTime, 0).UTC().Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			weekend++
			if !linearTitleHasGenre(t, p, "Horror") {
				t.Errorf("%q played on %s, which is the Horror day", p.Title, wd)
			}
		} else {
			weekday++
			if !linearTitleHasGenre(t, p, "Comedy") {
				t.Errorf("%q played on %s, which is the Comedy day", p.Title, wd)
			}
		}
	}
	if weekday == 0 || weekend == 0 {
		t.Fatalf("day parts never fired (weekday=%d weekend=%d)", weekday, weekend)
	}
}

func linearTitleHasGenre(t *testing.T, p Program, genre string) bool {
	t.Helper()
	for _, it := range linearFixtureItems() {
		if it.CanonicalID == p.CanonicalID {
			return contains(it.Genres, genre)
		}
	}
	t.Fatalf("programme %q is not in the fixture at all", p.Title)
	return false
}

// A daypart whose rules match nothing must not black the channel out for six
// hours. Falling back keeps it on air, and the preview already said the part
// was empty.
func TestLinearEmptyDaypartFallsBackInsteadOfGoingDark(t *testing.T) {
	ch := linearTestChannel("fb", StrategyTimeBased)
	ch.Options.Dayparts = []LinearDaypart{{
		Name: "Impossible", StartMinute: 0, EndMinute: 1440, Strategy: StrategyCyclic,
		Rules: LinearRuleGroup{Match: LinearMatchAll, Rules: []LinearRule{
			{Field: LinearFieldGenre, Op: LinearOpIs, Value: "Polka Documentary"},
		}},
	}}
	ch.normalise()
	got := linearFillFor(t, ch, linearEpoch, 6)
	if len(got) == 0 {
		t.Fatal("the channel went dark because one daypart matched nothing")
	}
}

// --- the hang guard ---------------------------------------------------------

// An item with no duration advances the clock by nothing, and a naive fill
// loop then runs forever inside one instant. This is the check that the guard
// is real, not the comment that says it is.
func TestLinearZeroDurationItemsCannotHangTheFill(t *testing.T) {
	pool := []LinearItem{
		{MediaItem: MediaItem{CanonicalID: "z1", Title: "Zero A", Type: "movie"}, DurationSeconds: 0},
		{MediaItem: MediaItem{CanonicalID: "z2", Title: "Zero B", Type: "movie"}, DurationSeconds: -60},
	}
	ch := linearTestChannel("zero", StrategyCyclic)

	done := make(chan int, 1)
	go func() {
		got, _, err := linearFill(&ch, pool, linearCursor{}, linearEpoch, linearEpoch.Add(24*time.Hour))
		if err != nil {
			done <- -1
			return
		}
		done <- len(got)
	}()
	select {
	case n := <-done:
		if n != 0 {
			t.Fatalf("a pool of zero-length items produced %d programmes; they are not schedulable", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fill did not return: a zero-length item span the loop")
	}
}

func TestLinearFillIsBounded(t *testing.T) {
	pool := []LinearItem{{
		MediaItem:       MediaItem{CanonicalID: "tiny", Title: "One Second", Type: "movie"},
		DurationSeconds: 1,
	}}
	ch := linearTestChannel("bound", StrategyCyclic)
	got, _, err := linearFill(&ch, pool, linearCursor{}, linearEpoch, linearEpoch.Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != linearMaxSlots {
		t.Fatalf("fill produced %d programmes; it must stop at the %d bound rather than run a year of one-second slots", len(got), linearMaxSlots)
	}
}

// --- randomness -------------------------------------------------------------

// The permutation must be a permutation: every show exactly once per round, or
// CYCLIC_SHUFFLE would drop shows and repeat others.
func TestLinearPermutationIsAPermutation(t *testing.T) {
	for _, n := range []int{1, 2, 5, 17} {
		for round := 0; round < 20; round++ {
			p := linearPermutation(n, 99, round)
			seen := map[int]bool{}
			for _, v := range p {
				if v < 0 || v >= n {
					t.Fatalf("n=%d round=%d produced out-of-range %d", n, round, v)
				}
				if seen[v] {
					t.Fatalf("n=%d round=%d repeated %d", n, round, v)
				}
				seen[v] = true
			}
			if len(seen) != n {
				t.Fatalf("n=%d round=%d covered %d values", n, round, len(seen))
			}
		}
	}
}

// Pinned values. If a Go upgrade or a refactor changes the stream, this fails
// here rather than silently rewriting every persisted channel's future.
func TestLinearRandomStreamIsPinned(t *testing.T) {
	want := []uint64{
		linearSplitMix64(0), linearSplitMix64(1), linearSplitMix64(2),
	}
	got := []uint64{
		0xE220A8397B1DCDAF, 0x910A2DEC89025CC1, 0x975835DE1C9756CE,
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("splitmix64(%d) = %#016x, the pinned value is %#016x; the schedule stream has moved", i, want[i], got[i])
		}
	}
}

func TestLinearSeqSizesReportTheirPool(t *testing.T) {
	pool := linearFixtureItems()
	for _, s := range []LinearStrategy{StrategyRandom, StrategyCyclic, StrategyCyclicShuffle, StrategyBlock} {
		seq, err := linearBuildSeq(s, pool, 1, 3)
		if err != nil {
			t.Fatal(err)
		}
		if seq.size() != len(pool) {
			t.Errorf("%s reports size %d for a pool of %d", s, seq.size(), len(pool))
		}
		if _, ok := seq.at(0); !ok {
			t.Errorf("%s could not produce its first item", s)
		}
	}
	if _, err := linearBuildSeq("NOPE", pool, 1, 3); err == nil {
		t.Error("an unknown strategy built a sequence")
	}
}

func TestLinearOrderingCheckCatchesAnOverlap(t *testing.T) {
	bad := []Program{
		{Title: "A", StartTime: 0, EndTime: 100},
		{Title: "B", StartTime: 50, EndTime: 150},
	}
	err := linearCheckOrdering(bad)
	if err == nil {
		t.Fatal("the invariant check passed an overlap")
	}
	if !strings.Contains(err.Error(), "overlap") {
		t.Errorf("the error should name the problem, got %q", err)
	}
	if !strings.Contains(fmt.Sprint(err), "50s") {
		t.Errorf("the error should quantify the overlap, got %q", err)
	}
}
