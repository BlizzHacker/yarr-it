package main

// The scheduler: turning a pool of programmes into a run of time slots.
//
// Two properties drive every decision here.
//
// The first is determinism. Given the same seed, the same pool and the same
// cursor, this must produce the same television every time -- across restarts,
// across machines, and across Go releases. That is why the randomness is a
// splitmix64 written out longhand rather than math/rand: math/rand's stream is
// reproducible today but is explicitly not a compatibility promise, and a
// schedule that reshuffles itself on a toolchain upgrade would rewrite a
// channel's past.
//
// The second is that generation is resumable. A schedule is extended, never
// rebuilt: the fill starts from wherever the last programme ended and carries
// a small cursor forward. Rebuilding a multi-day schedule per request would be
// both slow and wrong -- wrong because the programme a viewer is halfway
// through would be replaced underneath them.

import (
	"fmt"
	"sort"
	"time"
)

// --- deterministic randomness ----------------------------------------------

// linearSplitMix64 is written out here rather than imported so the sequence is
// pinned by this file. See the file comment: a library that is "deterministic
// for now" is not deterministic enough for a schedule that gets persisted.
func linearSplitMix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	z := x
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

type linearRNG struct{ state uint64 }

func newLinearRNG(seed uint64) *linearRNG { return &linearRNG{state: seed} }

func (r *linearRNG) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// intn is unbiased by rejection, which matters less for aesthetics than for
// the property that two different seeds really do give different lineups.
func (r *linearRNG) intn(n int) int {
	if n <= 0 {
		return 0
	}
	limit := ^uint64(0) - (^uint64(0) % uint64(n))
	for {
		v := r.next()
		if v < limit {
			return int(v % uint64(n))
		}
	}
}

func linearHashString(s string) uint64 {
	// FNV-1a, then mixed, so ids that share a prefix do not share a lineup.
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return linearSplitMix64(h)
}

func linearMix(seed int64, n uint64) uint64 {
	return linearSplitMix64(uint64(seed) ^ linearSplitMix64(n+0x632BE59BD9B4E019))
}

// linearPermutation returns a deterministic permutation of [0,n) for a round.
// Addressable by round means the scheduler can answer "which show is 400
// slots from now" without replaying the 399 before it.
func linearPermutation(n int, seed int64, round int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	rng := newLinearRNG(linearMix(seed, uint64(round)))
	for i := n - 1; i > 0; i-- {
		j := rng.intn(i + 1)
		idx[i], idx[j] = idx[j], idx[i]
	}
	return idx
}

// --- the cursor -------------------------------------------------------------

// linearCursor is the small amount of state that must survive a restart for a
// channel to carry on where it left off rather than starting its rotation
// over. It is persisted next to the schedule.
type linearCursor struct {
	// Ordinal counts programmes placed by the channel's top-level strategy.
	Ordinal int `json:"ordinal"`
	// Partition counts them per daypart or weekday, for the strategies that
	// split the day. Kept separate so a quiet daypart does not have its
	// rotation dragged forward by a busy one.
	Partition map[string]int `json:"partition,omitempty"`
}

func (c *linearCursor) partitionNext(key string) int {
	if c.Partition == nil {
		c.Partition = map[string]int{}
	}
	n := c.Partition[key]
	c.Partition[key] = n + 1
	return n
}

func (c linearCursor) clone() linearCursor {
	out := linearCursor{Ordinal: c.Ordinal}
	if c.Partition != nil {
		out.Partition = make(map[string]int, len(c.Partition))
		for k, v := range c.Partition {
			out.Partition[k] = v
		}
	}
	return out
}

// --- sequences --------------------------------------------------------------

// linearSeq is an infinite, index-addressable ordering of a pool. Every
// strategy is one of these; the fill loop does not know which.
type linearSeq interface {
	at(n int) (LinearItem, bool)
	size() int
}

// linearGroup is one show's episodes, in order.
type linearGroup struct {
	key   string
	items []LinearItem
}

// linearGroupBy splits a sorted pool into shows, preserving episode order
// inside each. Groups come back in a stable order because the pool was sorted
// by the same key.
func linearGroupBy(pool []LinearItem) []linearGroup {
	var out []linearGroup
	for _, it := range pool {
		k := it.seriesKey()
		if n := len(out); n > 0 && out[n-1].key == k {
			out[n-1].items = append(out[n-1].items, it)
			continue
		}
		out = append(out, linearGroup{key: k, items: []LinearItem{it}})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// randomSeq: no memory, repeats allowed. That is what the name promises, and
// the shuffle-bag behaviour people usually want is CYCLIC_SHUFFLE.
type linearRandomSeq struct {
	pool []LinearItem
	seed int64
}

func (s linearRandomSeq) size() int { return len(s.pool) }
func (s linearRandomSeq) at(n int) (LinearItem, bool) {
	if len(s.pool) == 0 {
		return LinearItem{}, false
	}
	return s.pool[int(linearMix(s.seed, uint64(n))%uint64(len(s.pool)))], true
}

// cyclicSeq walks the library in its sorted order, forever.
type linearCyclicSeq struct{ pool []LinearItem }

func (s linearCyclicSeq) size() int { return len(s.pool) }
func (s linearCyclicSeq) at(n int) (LinearItem, bool) {
	if len(s.pool) == 0 {
		return LinearItem{}, false
	}
	return s.pool[n%len(s.pool)], true
}

// cyclicShuffleSeq shuffles the *shows* and keeps each show's episodes in
// order. Round r plays every show exactly once, so the episode a show is up to
// after r rounds is simply its r'th -- which is what makes this addressable.
type linearCyclicShuffleSeq struct {
	groups []linearGroup
	seed   int64
	total  int
}

func (s linearCyclicShuffleSeq) size() int { return s.total }
func (s linearCyclicShuffleSeq) at(n int) (LinearItem, bool) {
	g := len(s.groups)
	if g == 0 {
		return LinearItem{}, false
	}
	round, pos := n/g, n%g
	order := linearPermutation(g, s.seed, round)
	grp := s.groups[order[pos]]
	return grp.items[round%len(grp.items)], true
}

// blockSeq plays BlockSize consecutive episodes of one show before moving on.
// Shuffled picks the next show at random each round; unshuffled rotates them,
// which is the BLOCK / BLOCK_CYCLIC distinction. Either way episodes inside a
// show never jump around: the block index does the advancing.
type linearBlockSeq struct {
	groups  []linearGroup
	seed    int64
	size_   int
	shuffle bool
	total   int
}

func (s linearBlockSeq) size() int { return s.total }
func (s linearBlockSeq) at(n int) (LinearItem, bool) {
	g := len(s.groups)
	if g == 0 || s.size_ <= 0 {
		return LinearItem{}, false
	}
	block, within := n/s.size_, n%s.size_
	round, pos := block/g, block%g
	gi := pos
	if s.shuffle {
		gi = linearPermutation(g, s.seed, round)[pos]
	}
	grp := s.groups[gi]
	return grp.items[(round*s.size_+within)%len(grp.items)], true
}

// linearBuildSeq turns a strategy plus a pool into a sequence. The pool is
// sorted here so callers cannot forget: an unsorted pool silently destroys
// every determinism guarantee in this file.
func linearBuildSeq(strategy LinearStrategy, pool []LinearItem, seed int64, blockSize int) (linearSeq, error) {
	sorted := make([]LinearItem, len(pool))
	copy(sorted, pool)
	linearSortItems(sorted)

	switch strategy {
	case StrategyRandom:
		return linearRandomSeq{pool: sorted, seed: seed}, nil
	case StrategyCyclic:
		return linearCyclicSeq{pool: sorted}, nil
	case StrategyCyclicShuffle:
		return linearCyclicShuffleSeq{groups: linearGroupBy(sorted), seed: seed, total: len(sorted)}, nil
	case StrategyBlock, StrategyBlockCyclic:
		if blockSize <= 0 {
			blockSize = linearDefaultBlock
		}
		return linearBlockSeq{
			groups:  linearGroupBy(sorted),
			seed:    seed,
			size_:   blockSize,
			shuffle: strategy == StrategyBlock,
			total:   len(sorted),
		}, nil
	}
	return nil, fmt.Errorf("no sequence for strategy %q", strategy)
}

// --- pickers ----------------------------------------------------------------

// linearPicker chooses what goes in the slot beginning at `slot`. Only the
// partitioned strategies care about the time; the rest ignore it.
type linearPicker interface {
	pick(cur *linearCursor, slot time.Time) (LinearItem, bool)
}

type linearFlatPicker struct{ seq linearSeq }

func (p linearFlatPicker) pick(cur *linearCursor, _ time.Time) (LinearItem, bool) {
	it, ok := p.seq.at(cur.Ordinal)
	if ok {
		cur.Ordinal++
	}
	return it, ok
}

// linearPartitionPicker backs TIME_BASED and DAY_BASED: work out which part of
// the schedule this slot falls in, then order within that part.
//
// The part is decided from the slot's *local* time, which is the whole point:
// a daypart that starts at 06:00 must start at 06:00 in the channel's zone on
// both sides of a DST change, not at a fixed offset from UTC.
type linearPartitionPicker struct {
	loc      *time.Location
	parts    []linearPart
	fallback linearSeq
}

type linearPart struct {
	key       string
	daypart   LinearDaypart
	seq       linearSeq
	byWeekday bool
}

func (p linearPartitionPicker) pick(cur *linearCursor, slot time.Time) (LinearItem, bool) {
	local := slot.In(p.loc)
	for _, part := range p.parts {
		if !part.covers(local) {
			continue
		}
		if part.seq.size() == 0 {
			// This daypart's rules matched nothing. Falling through to the
			// channel's whole pool keeps the channel on air; going dark for
			// six hours because one daypart was over-specified is the worse
			// answer, and the preview already reported the empty part.
			break
		}
		n := cur.partitionNext(part.key)
		return part.seq.at(n)
	}
	if p.fallback == nil {
		return LinearItem{}, false
	}
	n := cur.partitionNext("__fallback")
	return p.fallback.at(n)
}

func (d LinearDaypart) coversWeekday(local time.Time) bool {
	if len(d.Weekdays) == 0 {
		return true
	}
	wd := int(local.Weekday())
	for _, w := range d.Weekdays {
		if w == wd {
			return true
		}
	}
	return false
}

func (p linearPart) covers(local time.Time) bool {
	if !p.daypart.coversWeekday(local) {
		return false
	}
	if p.byWeekday {
		return true
	}
	start, end := p.daypart.StartMinute, p.daypart.EndMinute
	if start == 0 && (end == 0 || end == 1440) {
		return true // whole day
	}
	m := local.Hour()*60 + local.Minute()
	if start <= end {
		return m >= start && m < end
	}
	// Wraps midnight: 22:00 -> 06:00.
	return m >= start || m < end
}

// linearBuildPicker assembles the picker for a channel over an already
// rule-filtered pool.
func linearBuildPicker(ch *LinearChannel, pool []LinearItem) (linearPicker, error) {
	switch ch.ScheduleStrategy {
	case StrategyTimeBased, StrategyDayBased:
		byWeekday := ch.ScheduleStrategy == StrategyDayBased
		pp := linearPartitionPicker{loc: ch.location()}
		for i, d := range ch.Options.Dayparts {
			sub := linearApplyRules(pool, d.Rules)
			sub = linearSchedulable(sub)
			// Each daypart gets its own seed so two parts with the same rules
			// do not play the same thing in lockstep.
			seed := ch.Seed ^ int64(linearHashString(fmt.Sprintf("%s#%d", d.Name, i))&0x7fffffffffffffff)
			seq, err := linearBuildSeq(d.Strategy, sub, seed, ch.Options.BlockSize)
			if err != nil {
				return nil, err
			}
			key := d.Name
			if key == "" {
				key = fmt.Sprintf("part%d", i)
			}
			pp.parts = append(pp.parts, linearPart{key: key, daypart: d, seq: seq, byWeekday: byWeekday})
		}
		fb, err := linearBuildSeq(StrategyCyclicShuffle, linearSchedulable(pool), ch.Seed, ch.Options.BlockSize)
		if err != nil {
			return nil, err
		}
		pp.fallback = fb
		return pp, nil
	default:
		seq, err := linearBuildSeq(ch.ScheduleStrategy, linearSchedulable(pool), ch.Seed, ch.Options.BlockSize)
		if err != nil {
			return nil, err
		}
		return linearFlatPicker{seq: seq}, nil
	}
}

// linearSchedulable drops items with no usable duration. They are removed here
// rather than clamped so a channel's advertised hours stay honest, and so the
// fill loop can never be handed a zero-length slot.
func linearSchedulable(pool []LinearItem) []LinearItem {
	out := make([]LinearItem, 0, len(pool))
	for _, it := range pool {
		if it.schedulable() {
			out = append(out, it)
		}
	}
	return out
}

// --- filling ----------------------------------------------------------------

// linearMaxSlots bounds one fill. Fourteen days of five-minute programmes is
// about 4,000; this is far above any sane channel and exists only so a
// pathological pool cannot spin a request forever.
const linearMaxSlots = 20000

// linearFill lays programmes end to end from `from` until the schedule passes
// `until`, and returns the advanced cursor.
//
// The invariant it enforces is that programme[n].End <= programme[n+1].Start.
// With no slot grid those are equal -- the next thing starts exactly when the
// last one finishes, which is what a channel is. With a grid they are not
// equal and the difference is the gap before the next half hour, which is
// still a legal schedule and must not be mistaken for a bug.
func linearFill(ch *LinearChannel, pool []LinearItem, cur linearCursor, from, until time.Time) ([]Program, linearCursor, error) {
	out := []Program{}
	next := cur.clone()

	usable := linearSchedulable(pool)
	if len(usable) == 0 {
		// Not an error. An empty channel is a state, and a channel whose
		// library has gone missing must not take a request down with it.
		return out, next, nil
	}
	picker, err := linearBuildPicker(ch, pool)
	if err != nil {
		return nil, cur, err
	}

	grid := time.Duration(ch.Options.SlotMinutes) * time.Minute
	t := from.UTC().Truncate(time.Second)
	for i := 0; i < linearMaxSlots && t.Before(until); i++ {
		it, ok := picker.pick(&next, t)
		if !ok || !it.schedulable() {
			break
		}
		dur := it.Duration()
		if dur < time.Second {
			dur = linearMinProgramSecs * time.Second
		}
		end := t.Add(dur)
		out = append(out, linearProgramFor(ch.ID, it, t, end))

		advance := dur
		if grid > 0 {
			// Round the *slot* up to the grid, leaving the programme its true
			// length and the remainder as a gap.
			if rem := advance % grid; rem != 0 {
				advance += grid - rem
			}
		}
		t = t.Add(advance)
	}
	return out, next, nil
}

func linearProgramFor(channelID string, it LinearItem, start, end time.Time) Program {
	return Program{
		ChannelID:   channelID,
		StartTime:   start.UTC().Unix(),
		EndTime:     end.UTC().Unix(),
		CanonicalID: it.CanonicalID,
		Title:       it.Title,
		Subtitle:    it.Subtitle,
		Season:      it.Season,
		Episode:     it.Episode,
		Description: it.Overview,
		Artwork:     it.Artwork,
	}
}

// linearCheckOrdering is the invariant, written down once so both the engine
// and the tests assert exactly the same thing.
func linearCheckOrdering(programs []Program) error {
	for i := 1; i < len(programs); i++ {
		prev, cur := programs[i-1], programs[i]
		if prev.EndTime > cur.StartTime {
			return fmt.Errorf("programme %d (%s) ends at %d but %d (%s) starts at %d: they overlap by %ds",
				i-1, prev.Title, prev.EndTime, i, cur.Title, cur.StartTime, prev.EndTime-cur.StartTime)
		}
		if cur.EndTime <= cur.StartTime {
			return fmt.Errorf("programme %d (%s) ends at or before it starts", i, cur.Title)
		}
	}
	return nil
}
