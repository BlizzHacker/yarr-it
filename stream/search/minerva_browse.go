package main

// The sets band on a category page.
//
// A machine's page is a grid of games. Minerva's contribution to it is not a
// game and must never be laid into that grid: interleaving an 8.4 GB
// whole-collection torrent among single titles -- which is what vimm_browse.go
// does for Vimm's entries, correctly, because those ARE single titles -- would
// put a container in a row of contents and give it a tile that looks exactly
// like the ones beside it.
//
// So it is a BAND BELOW THE GRID, in its own field, with its own sentence
// above it. Three consequences, each of which is a test:
//
//   - It is `Sets`, a sibling of `Items`, never more Items. A client that has
//     never heard of the field renders nothing, which is right; a new row
//     inside the existing list would have rendered on the Xbox, Roku and
//     Cartridge builds as a playable game.
//   - It DOES NOT PAGE, and therefore cannot come back short. hasMoreAfter is
//     untouched: it reasons about the grid, the band is not in the grid, and a
//     band that shows everything it has can never be the reason "Load more"
//     appears or fails to.
//   - It DOES NOT COUNT. allCounts and categoryTotal stay ignorant of it,
//     because a category total promises tiles the grid can produce and adding
//     sets to it would promise four more SNES games that do not exist.

import (
	"fmt"
	"sort"
	"strings"
)

// minervaBandLimit is how many sets one category page may show.
//
// Small on purpose. The band answers "there is also a complete set of this
// machine" -- one, or a handful where the collections disagree about how to cut
// it. It is not a directory listing, and a machine with forty matching
// directories has a naming problem rather than forty useful answers.
const minervaBandLimit = 6

// setsForSystem returns the whole-collection torrents for one machine.
//
// Keyed on the SLUG, which only the four per-machine collections carry, so a
// bitsavers or TOSEC torrent can never appear on a machine's page -- there is
// no slug on it to match.
func (s *minervaStore) setsForSystem(slug string) []discoverItm {
	if s == nil || strings.TrimSpace(slug) == "" {
		return nil
	}
	s.mu.RLock()
	matched := make([]minervaRow, 0, 8)
	for i := range s.rows {
		r := &s.rows[i]
		if r.System != slug {
			continue
		}
		coll, known := minervaCollectionFor(r.Collection)
		if !known || coll.Kind == minervaExcluded || coll.Domain != domainGame {
			continue
		}
		matched = append(matched, *r)
	}
	s.mu.RUnlock()

	// Smallest first. On a page whose whole job is to warn somebody what they
	// are about to fetch, the cheapest real option leading is the useful order
	// -- and it is deterministic, which map iteration is not.
	sort.SliceStable(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if a.SizeBytes != b.SizeBytes {
			return a.SizeBytes < b.SizeBytes
		}
		return a.Path < b.Path
	})
	if len(matched) > minervaBandLimit {
		matched = matched[:minervaBandLimit]
	}

	out := make([]discoverItm, 0, len(matched))
	for _, r := range matched {
		c, ok := minervaCard(r)
		if !ok {
			continue
		}
		out = append(out, discoverItm{
			Title:     r.Name,
			MediaType: domainGame,
			// The magnet itself. A click hands it to a torrent client; there is
			// no page to open and nothing to search for, so State stays empty --
			// this IS the verified target.
			Play:   c.Sources[0].Magnet,
			Source: minervaSiteName,
			// The same setInfo the search card carries, so tileAction reads one
			// property and does not have to know which endpoint built the item.
			// This is the field b71f7ad's bug was about, armed from the start
			// this time.
			Set: c.Set,
		})
	}
	return out
}

// minervaSetsNote is the sentence above the band.
//
// The server writes it because only the server knows the range in it, and the
// range is the entire point: "Complete SNES sets" alone is an invitation, while
// "8.4 GB - 41 GB" is the same invitation with the price on it. Somebody who
// reads this and decides not to click has been served correctly.
func minervaSetsNote(slug string, items []discoverItm) string {
	if len(items) == 0 {
		return ""
	}
	machine := slug
	if g := lookupSystem(slug); g != nil && g.Short != "" {
		machine = g.Short
	}

	lo, hi := items[0].Set.SizeBytes, items[0].Set.SizeBytes
	for _, it := range items[1:] {
		if it.Set == nil {
			continue
		}
		if it.Set.SizeBytes < lo {
			lo = it.Set.SizeBytes
		}
		if it.Set.SizeBytes > hi {
			hi = it.Set.SizeBytes
		}
	}

	// "one torrent each, every game at once" is the whole of what a set is,
	// said in the words somebody would use. It is deliberately not "download
	// the full set", which sounds like a button.
	size := humanSize(lo)
	if hi != lo {
		size = fmt.Sprintf("%s - %s", humanSize(lo), humanSize(hi))
	}
	return fmt.Sprintf("Complete %s sets - one torrent each, every game at once. %s.",
		machine, size)
}

// attachMinervaSets hangs the band off a finished category row.
//
// Called at the very end of building the row, after the grid and after the
// interleave, so nothing above it can see the sets and accidentally treat them
// as items. It is the only place discoverRow.Sets is ever assigned.
func (s *server) attachMinervaSets(key string, row discoverRow) discoverRow {
	slug, ok := strings.CutPrefix(key, "ia:sys:")
	if !ok || s == nil || s.minerva == nil {
		return row
	}
	items := s.minerva.setsForSystem(slug)
	if len(items) == 0 {
		return row
	}
	row.Sets = items
	row.SetsNote = minervaSetsNote(slug, items)
	return row
}
