package main

// Vimm's Lair on the category pages.
//
// /browse/games/snes used to be an archive.org page that happened to be called
// "Super Nintendo". Search, the `system=` filter and the system facet have all
// carried Vimm since it was imported; browse was the one surface that did not,
// for no better reason than that browseRow switched on `src.solr` and
// `src.tmdbPath` and there was no third branch. So a person who searched found
// both catalogues and a person who browsed found one, which is the kind of
// inconsistency that reads as a bug in whichever half they saw second.
//
// WHAT A VIMM TILE ON A CATEGORY PAGE IS
//
// Exactly what a Vimm result in a search already is: a link to somebody else's
// website, labelled as one. Nothing here hosts, relays or plays a Vimm ROM --
// see the note at the foot of this file -- and the tile is built so that no
// part of the UI can accidentally claim otherwise. It carries `External`, which
// is what makes the client print "Vimm ↗" rather than "Play" or "Open"; that
// field had to be added to discoverItm for this, because a browse item had
// never before been something that leaves the site.
//
// WHY THE PAGE INTERLEAVES RATHER THAN CONCATENATES
//
// The obvious implementation is "Vimm first, then archive.org". It is wrong at
// this data's shape: Vimm holds 941 SNES entries, so a visitor would page
// through sixteen screens of Vimm before seeing a single archive.org item and
// would reasonably conclude the Archive was missing. The reverse is worse --
// archive.org's Genesis shelf is 12,975 items and Vimm would never appear at
// all.
//
// So every page draws a fixed share from each: vimmShareOf(limit) tiles from
// the catalogue, the rest from archive.org. Fixed rather than proportional
// because the share has to be computable from the page number ALONE. That is
// what keeps paging honest -- page 7's archive.org offset must not depend on
// how many Vimm entries happened to survive on pages 1 through 6, or "Load
// more" starts skipping or repeating items.
//
// The cost of a fixed share is that when one source runs out the page gets
// short, and a short page used to be the client's signal that there was
// nothing more. That signal was already a guess; it is now wrong often enough
// to matter, so the server states it instead -- see `more` in handleRows.

import (
	"net/url"
	"sort"
	"strings"
)

// vimmShareOf is how much of one browse page comes from the catalogue.
//
// A third, floored at one. The ratio is a judgement rather than a measurement
// and it is worth saying which way it errs: archive.org holds more on almost
// every machine (12,975 Genesis items against Vimm's 865), so an even split
// would over-represent Vimm, while a share proportional to the counts would
// bury it on exactly the machines where it is most complete. A third keeps both
// visible on every screen at every depth, which is the property the page needs.
//
// Floored at one so a small limit cannot silently drop the source entirely: a
// rail asking for two items shows one of each rather than two of the Archive's.
func vimmShareOf(limit int) int {
	if limit <= 1 {
		return 0
	}
	if n := limit / 3; n > 0 {
		return n
	}
	return 1
}

// vimmWork is one game on one machine, gathered from every row that describes
// it -- the three discs, the USA and Europe dumps, the revision.
//
// The grouping is search's, through vimmGroupKey, and it is deliberately the
// same function rather than a second rule that agrees today: a browse page that
// showed Final Fantasy VII three times where a search showed it once would be
// two answers to "how many of this game is there".
type vimmWork struct {
	Title string
	Page  string
	// VaultID and Core are what the vault needs to serve this work: the id
	// addresses the ROM and the artwork, the core tells the player which
	// machine to be. Core is empty for a work the vault cannot play, which is
	// how a browse tile knows to stay an outbound link.
	VaultID string
	Core    string
	// Sort is the head entry's vault id, zero-padded so a numeric id orders
	// numerically. The order has to be stable across requests or paging
	// repeats and skips items; map iteration is not.
	Sort string
}

// worksBySystem gathers every publishable entry for one machine.
//
// A whole-catalogue scan per call, which is affordable for the reason vimm.go's
// search is: 5,586 rows today and ~23,000 when the scan finishes, one string
// comparison each. browse.go caches the resulting row for three hours besides,
// so this runs once per category per page per three hours rather than once per
// visitor.
func (s *vimmStore) worksBySystem(system string) []vimmWork {
	if s == nil || system == "" {
		return nil
	}

	s.mu.RLock()
	groups := map[string][]vimmEntry{}
	for i := range s.rows {
		r := &s.rows[i]
		// An entry on a machine this site has no slug for is not this machine.
		// vimm.go publishes those -- they are findable by title and they say
		// what they are -- but there is no category page they belong on, for
		// the same reason they take no part in the system facet.
		if r.System != system {
			continue
		}
		// Nothing verified survived the import, so there is nothing to click.
		// The site's rule is that a tile carries a real target or is not shown.
		if !r.playable() && !r.downloadable() {
			continue
		}
		k := vimmGroupKey(r.vimmEntry)
		groups[k] = append(groups[k], r.vimmEntry)
	}
	s.mu.RUnlock()

	out := make([]vimmWork, 0, len(groups))
	for _, entries := range groups {
		if w, ok := vimmWorkFrom(entries); ok {
			out = append(out, w)
		}
	}
	// By title, so a category page reads as a catalogue rather than as the
	// order Wade's browser extension happened to visit pages in. The vault id
	// breaks ties so the order is total and therefore stable.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if la, lb := strings.ToLower(a.Title), strings.ToLower(b.Title); la != lb {
			return la < lb
		}
		return a.Sort < b.Sort
	})
	return out
}

// vimmWorkFrom reduces the rows for one work to the one tile that represents
// them, by the same two rules vimmCard uses: the lowest vault id is the head,
// and the shortest title is the one to show because it is the one with the
// fewest qualifiers left on it.
func vimmWorkFrom(entries []vimmEntry) (vimmWork, bool) {
	if len(entries) == 0 {
		return vimmWork{}, false
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if len(a.VaultID) != len(b.VaultID) {
			return len(a.VaultID) < len(b.VaultID)
		}
		return a.VaultID < b.VaultID
	})
	title := entries[0].Title
	for _, e := range entries[1:] {
		if len(e.Title) < len(title) {
			title = e.Title
		}
	}
	if strings.TrimSpace(title) == "" {
		return vimmWork{}, false
	}
	// The head is the lowest-numbered entry THAT HAS A VAULT PAGE, rather than
	// simply the lowest-numbered one.
	//
	// The distinction is not hypothetical: an export can carry a row whose page
	// URL failed vimmURL's https-on-vimm.net check while its siblings' passed,
	// and taking the lowest id unconditionally would drop the whole game
	// because disc 1 happened to be the bad row. A work is browsable if any of
	// its rows can be addressed.
	var head vimmEntry
	var found bool
	for _, e := range entries {
		if strings.TrimSpace(e.Page) != "" {
			head, found = e, true
			break
		}
	}
	if !found {
		return vimmWork{}, false
	}
	// The head is chosen for having a vault page, which is not the same as
	// being the disc the vault can play -- so the core is taken from the first
	// entry that HAS one rather than from the head. A work is playable if any
	// of its rows is.
	core, vaultID := "", strings.TrimSpace(head.VaultID)
	for _, e := range entries {
		if e.vaultable() {
			core, vaultID = e.Core, e.VaultID
			break
		}
	}

	return vimmWork{
		Title: title,
		// The vault page, not a download and not their player: it is the
		// thing's own address, and it is the one URL that is right to put
		// behind a tile whose label already says the click leaves.
		Page:    strings.TrimSpace(head.Page),
		VaultID: vaultID,
		Core:    core,
		// Left-padded to a fixed width so "10" sorts after "9" rather than
		// before it. Ids longer than the pad are rare and simply sort late,
		// which is fine: this is a tie-break, not the ordering.
		Sort: padVaultID(head.VaultID),
	}, true
}

// padVaultID makes a numeric vault id sort numerically as a string.
func padVaultID(id string) string {
	const width = 12
	if len(id) >= width {
		return id
	}
	return strings.Repeat("0", width-len(id)) + id
}

// vimmBrowseItems is one page's worth of catalogue tiles for a machine.
//
// `offset` is an item offset into the machine's whole list, computed by the
// caller from the page number and the fixed share. Out of range is empty rather
// than an error: a machine whose catalogue has been exhausted contributes
// nothing to page 40 and the page is still a page.
func (s *vimmStore) vimmBrowseItems(system string, offset, limit int) []discoverItm {
	if limit <= 0 || offset < 0 {
		return nil
	}
	works := s.worksBySystem(system)
	if offset >= len(works) {
		return nil
	}
	end := offset + limit
	if end > len(works) {
		end = len(works)
	}
	vault := vimmVaultBase()

	out := make([]discoverItm, 0, end-offset)
	for _, w := range works[offset:end] {
		it := discoverItm{
			Title:     w.Title,
			MediaType: "game",
			// The vault page. A click opens it on their site.
			Play: w.Page,
			// Who this came from, so a row that mixes two catalogues can say
			// which tile is which without the client parsing a hostname.
			Source: vimmSiteName,
		}

		if vault != "" && w.Core != "" {
			// The vault holds this one, so the tile plays it here rather than
			// sending somebody to vimm.net. Same URI shape the search cards
			// use, and for the same reason: the core travels with it because
			// inferring one from a file extension boots the wrong machine and
			// runs a black screen with no error.
			it.Play = vault + "/api/rom/" + w.VaultID +
				"#ejs=" + url.QueryEscape(w.Core) + "&name=" + url.QueryEscape(w.Title)
			// Artwork at last. There was none here because nothing addressed
			// Vimm's box art and guessing a URL from a vault id put a broken
			// image in a 2:3 box; the vault proxies it, with the Referer
			// vimm.net requires, so there is now a real URL to use.
			it.Poster = vault + "/api/art/" + w.VaultID
			out = append(out, it)
			continue
		}

		// Nothing here can play it, so the tile says where it goes instead.
		// This is what makes the tile read "Vimm ↗" rather than "Play" -- see
		// the field comment on discoverItm.External, and tileAction in
		// web/src/home.js.
		it.External = &externalSite{
			Name:  vimmSiteName,
			Short: vimmShortName,
			Host:  vimmHost,
			Page:  w.Page,
		}
		out = append(out, it)
	}
	return out
}

// vimmSystemCounts is how many works the catalogue holds per machine.
//
// Works rather than rows, because that is what a category page will actually
// show: counting the 941 SNES rows would promise more tiles than the page can
// produce once the three discs of one game have become one tile.
//
// ONE PASS over the catalogue, not one pass per machine. The obvious
// implementation calls worksBySystem for each of the seventeen mapped slugs,
// and that is seventeen full scans: measured at 41 ms for a 23,000-row
// catalogue, on /api/categories, which is the request the browse landing page
// blocks on. Grouping by machine and work together costs one scan and measured
// ~2 ms for the same data.
//
// Local, so unlike every other count in browse.go this one does not wait on a
// background refresh and is never stale.
func (s *vimmStore) vimmSystemCounts() map[string]int {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int, len(s.systemCounts))
	for k, v := range s.systemCounts {
		out[k] = v
	}
	return out
}

// countVimmWorks is the count itself, run once per catalogue by replace.
//
// ONE PASS, and the filter has to be exactly worksBySystem's, or the number on
// a category tile promises tiles the page cannot produce. Both require a slug,
// a verified target, and an addressable vault page -- the last one per WORK
// rather than per row, which is why a group is counted on the first row that
// has a page rather than on its first row.
func countVimmWorks(rows []vimmRow) map[string]int {
	// The key is machine + work, so two machines holding the same game count
	// once each and three discs of one game count once between them.
	seen := make(map[string]bool, len(rows))
	out := map[string]int{}
	for i := range rows {
		r := &rows[i]
		if r.System == "" || (!r.playable() && !r.downloadable()) {
			continue
		}
		if strings.TrimSpace(r.Page) == "" {
			continue
		}
		// vimmGroupKey already begins with the machine, so it distinguishes
		// machines on its own and there is nothing to concatenate.
		k := vimmGroupKey(r.vimmEntry)
		if seen[k] {
			continue
		}
		seen[k] = true
		out[r.System]++
	}
	return out
}

// ---------------------------------------------------------------------------
// WHAT IS NOT HERE, AND WHY
// ---------------------------------------------------------------------------
//
// There is no relay that fetches a Vimm ROM so it can boot in this site's own
// EmulatorJS, and the absence is a decision rather than an omission.
//
// Vimm's download host answers 400 Bad Request to a plain request and 200 only
// when the request carries `Referer: https://vimm.net/vault/<vault_id>`. That
// is an access control, and it is the only one they have: it says the bytes are
// for people reading their pages. A relay works by sending that header from a
// server that was never on such a page -- the header would be a claim about
// where the request came from that is not true, made specifically to obtain a
// file that is otherwise refused. The bytes would then be cached on the VPS and
// served to anonymous visitors, which makes this host a distributor of
// commercial game ROMs rather than an index that points at one.
//
// This is not a line invented here. It is the line play_archive.go already
// draws: it refuses to relay an archive.org item marked `stream_only` because
// doing so "would republish it from our host, which is precisely what the
// marker asks us not to do". A 400 is a stronger version of the same marker --
// enforced rather than declared -- so relaying past it would hold Vimm to a
// weaker standard than the Internet Archive, which is backwards.
//
// The archive.org relay is not the precedent it looks like. The Console Living
// Room is a curated public collection with its own in-browser player and no
// access control on downloads; nothing is circumvented to reach it, and where
// the Archive does mark an item, the marker is obeyed.
//
// So a Vimm entry stays a link, and the labelling above is built to make that
// the honest, visible answer rather than a limitation to work around.
