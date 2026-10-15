package main

// Reducing a Minerva snapshot to the catalogue the server reads.
//
// This process talks to NOTHING. It reads the directory -fetch-minerva left
// behind and writes one small file. That is what makes it safe to run against
// a live deployment, and what makes it re-runnable after a parser fix without
// going back to the site.
//
// THE THREE PROPERTIES, which are vimm_import.go's three:
//
//  1. RE-RUNNABLE. The merge is keyed on the info-hash, which is
//     content-addressed and therefore the most stable primary key any source in
//     this codebase has, and it is read from the FILE ON DISK rather than from
//     a running store. Nothing is ever appended blindly.
//  2. IMPROVING, NOT MERELY GROWING. A collection whose listing failed to parse
//     is CARRIED FORWARD from the existing catalogue rather than deleted, and
//     named in the report. A snapshot is a partial view of the site, and the
//     failure mode of treating it as complete is a category that silently
//     empties.
//  3. LOUD ABOUT WHAT IT DID NOT PUBLISH. Every input row is accounted for
//     against exactly one reason. A quiet importer that publishes 300 of 1,049
//     sets looks identical to one that is broken.
//
// AND FOUR GATES, in this order, each of which stops the write entirely:
//
//	1. every browse hash resolved to exactly one path
//	2. the two independent censuses agree to within 5%
//	3. the corpus did not shrink below 80% of what is already published
//	4. an unknown collection, or an unknown path inside a facet collection,
//	   fails and NAMES THEM ALL AT ONCE
//
// Gate 4 is a deploy hazard and is meant to be. Minerva adds a directory, the
// next import exits non-zero, and a person extends the map. Automation around
// this must never treat a non-zero exit as "retry": there is nothing to retry,
// and retrying is how a silent default gets added to make the noise stop.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// minervaImportReport accounts for every input row against exactly one reason.
type minervaImportReport struct {
	// Read is how many hashes the snapshot's browse census held.
	Read int
	// Published, and how it broke down against the previous catalogue.
	Published int
	Added     int
	Updated   int
	Unchanged int

	// Every skip, each of which is a DIFFERENT thing having gone wrong.
	SkipNoPath             int // in the dashboard, never linked under /browse/
	SkipNoMagnet           int // browsable, but no torrent covers it
	SkipNoSize             int // no size: never published, see below
	SkipExcludedCollection int // Miscellaneous; there is no way to switch it on

	// Diagnostics that are not skips.
	PathsResolved      int
	EntitiesDecoded    int
	CollectionsCarried []string
	Systems            map[string]int
	NoSlug             int
	// Unlisted names the hashes the dashboard holds and /browse/ does not, so
	// the deliberately-unlisted "Outliers" torrent is visible as a decision
	// rather than as an off-by-one.
	Unlisted []string
	// SpotPaths is a handful of resolved paths printed verbatim, so a
	// path-corrupting template change at the far end is visible to a person
	// reading the report instead of showing up weeks later as bad filenames.
	SpotPaths []string
}

func (r *minervaImportReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "read %d sets from the snapshot\n", r.Read)
	fmt.Fprintf(&b, "published %d sets (%d added, %d updated, %d unchanged)\n",
		r.Published, r.Added, r.Updated, r.Unchanged)
	fmt.Fprintf(&b, "  paths resolved from the browse tree: %d (%d needed entity decoding)\n",
		r.PathsResolved, r.EntitiesDecoded)
	fmt.Fprintf(&b, "  machines: %d sets on a system this site has a slug for, %d without one\n",
		r.Published-r.NoSlug, r.NoSlug)

	skipped := r.SkipNoPath + r.SkipNoMagnet + r.SkipNoSize + r.SkipExcludedCollection
	fmt.Fprintf(&b, "skipped %d:\n", skipped)
	fmt.Fprintf(&b, "  %6d  in the dashboard but never linked under /browse/\n", r.SkipNoPath)
	fmt.Fprintf(&b, "  %6d  browsable, but no torrent covers them\n", r.SkipNoMagnet)
	fmt.Fprintf(&b, "  %6d  no size published (a set is never offered without one)\n", r.SkipNoSize)
	fmt.Fprintf(&b, "  %6d  in a collection this deployment does not publish\n", r.SkipExcludedCollection)

	if len(r.Unlisted) > 0 {
		fmt.Fprintf(&b, "unlisted torrents (in the API, not in /browse/): %s\n",
			strings.Join(r.Unlisted, ", "))
	}
	if len(r.CollectionsCarried) > 0 {
		fmt.Fprintf(&b, "CARRIED FORWARD from the previous catalogue (their listing did not parse): %s\n",
			strings.Join(r.CollectionsCarried, ", "))
	}

	b.WriteString("collections published:\n")
	type kv struct {
		k string
		n int
	}
	all := make([]kv, 0, len(r.Systems))
	for k, n := range r.Systems {
		all = append(all, kv{k, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].k < all[j].k
	})
	for _, e := range all {
		fmt.Fprintf(&b, "  %6d  %s\n", e.n, e.k)
	}

	// Ten real paths, printed verbatim. This is the cheapest possible detector
	// for a template change that mangles names: a person reading the report
	// sees "No-Intro/Nintendo - Game Boy" or they see something wrong.
	if len(r.SpotPaths) > 0 {
		b.WriteString("a sample of resolved paths, to eyeball:\n")
		for _, p := range r.SpotPaths {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	return b.String()
}

// minervaCensusAgreement is how closely the two independent censuses -- the
// browse tree and the dashboard API -- must agree before anything is written.
//
// They will never agree exactly and should not be made to: the API holds one
// deliberately unlisted torrent ("Outliers", 24.74 GB, category "Uncategorized")
// that appears nowhere under /browse/. Today that is 1,049 against 1,050. A
// 5% tolerance absorbs a handful more of those; a larger gap means one of the
// two sources has changed shape and neither should be trusted.
const minervaCensusAgreement = 0.95

// minervaCorpusRatchet is the floor on shrinkage without -force.
//
// The failure it prevents is specific and has happened to other importers here:
// the far end serves a partial page, the fetch succeeds, the reduction
// succeeds, and a 1,049-set catalogue is replaced by a 40-set one with every
// step reporting success. Growth is never blocked, and a genuine shrink is a
// person's decision to confirm.
const minervaCorpusRatchet = 0.80

// importMinerva reduces a snapshot directory into the catalogue file.
func importMinerva(dir, catalogPath string, force bool) (*minervaImportReport, error) {
	rep := &minervaImportReport{Systems: map[string]int{}}

	raw, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return nil, fmt.Errorf("reading the snapshot: %w", err)
	}
	var snap minervaSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("the snapshot is unreadable: %w", err)
	}

	rep.Read = len(snap.BrowseHashes)
	rep.PathsResolved = len(snap.Paths)

	// ---- gate 1: every browse hash has exactly one path ------------------
	var noPath []string
	for _, h := range snap.BrowseHashes {
		if strings.TrimSpace(snap.Paths[h]) == "" {
			noPath = append(noPath, h)
		}
	}
	if len(noPath) > 0 {
		sort.Strings(noPath)
		show := noPath
		if len(show) > 8 {
			show = show[:8]
		}
		return rep, fmt.Errorf("hash accounting did not close: %d of %d browse hashes "+
			"have no path (%s). The browse walk is incomplete and nothing was written",
			len(noPath), len(snap.BrowseHashes), strings.Join(show, ", "))
	}

	// ---- gate 2: the two censuses agree ----------------------------------
	inBrowse := map[string]bool{}
	for _, h := range snap.BrowseHashes {
		inBrowse[h] = true
	}
	both := 0
	for _, h := range snap.DashHashes {
		if inBrowse[h] {
			both++
		}
		if !inBrowse[h] {
			rep.Unlisted = append(rep.Unlisted, minervaUnlistedLabel(h, snap))
		}
	}
	smaller := len(snap.BrowseHashes)
	if len(snap.DashHashes) < smaller {
		smaller = len(snap.DashHashes)
	}
	if smaller == 0 {
		return rep, fmt.Errorf("one of the two censuses is empty (browse %d, dashboard %d); "+
			"nothing written", len(snap.BrowseHashes), len(snap.DashHashes))
	}
	if agreement := float64(both) / float64(smaller); agreement < minervaCensusAgreement {
		return rep, fmt.Errorf("the browse tree and the dashboard API agree on only "+
			"%.1f%% of the smaller census (%d of %d); one of them has changed shape "+
			"and nothing was written", agreement*100, both, smaller)
	}
	sort.Strings(rep.Unlisted)

	// The dashboard's unlisted extras are skipped, counted, and named. That is
	// how "Outliers" -- 24.74 GB of unvetted, unclassified material nobody has
	// looked at -- gets quarantined for free rather than by a special case.
	rep.SkipNoPath = len(snap.DashHashes) - both

	// ---- build ------------------------------------------------------------
	var unknownCollections, unknownSystems []string
	entries := make([]minervaEntry, 0, len(snap.BrowseHashes))
	seenColl := map[string]bool{}

	for _, h := range snap.BrowseHashes {
		path := strings.TrimSpace(snap.Paths[h])
		collection, component := minervaSplitPath(path)
		if collection == "" {
			rep.SkipNoMagnet++
			continue
		}
		seenColl[collection] = true

		coll, known := minervaCollectionFor(collection)
		if !known {
			unknownCollections = append(unknownCollections, collection)
			continue
		}
		// Unconditional, and it is the SAME answer minervaCard and setsForSystem
		// give. It was not always: this line honoured MINERVA_COLLECTIONS while
		// both of those refused minervaExcluded outright, so opting in wrote
		// 6.62 TB of rows into the catalogue that no surface could render and
		// every search then scanned past. One rule, in three places that agree.
		if coll.Kind == minervaExcluded {
			rep.SkipExcludedCollection++
			continue
		}

		// A HARD SKIP, not a default. "Whole set" with no size beside it is
		// exactly the tile that gets somebody 6.75 TB by surprise, and a
		// plausible-looking zero would be worse than no row at all.
		size := snap.Sizes[h]
		if size <= 0 {
			rep.SkipNoSize++
			continue
		}

		e := minervaEntry{
			InfoHash:   h,
			Path:       path,
			Collection: collection,
			Name:       minervaDisplayName(path),
			SizeBytes:  size,
			MeasuredAt: snap.Measured[h],
		}
		if e.Name != path {
			rep.EntitiesDecoded++
		}

		// Only a facet collection may carry a machine, and inside one, an
		// unknown component is fatal rather than a quiet empty.
		if coll.Facet && component != "" {
			slug, ok := minervaSystemFor(collection, component)
			if !ok {
				unknownSystems = append(unknownSystems, collection+"/"+component)
				continue
			}
			e.System = slug
			e.Platform = component
		}
		if e.System == "" {
			rep.NoSlug++
		}
		rep.Systems[collection]++
		entries = append(entries, e)
	}

	// ---- gate 4: unknown vocabulary, named ALL AT ONCE --------------------
	//
	// All at once rather than one per run, because the alternative is somebody
	// extending the map, re-running, and being told about the next one -- for
	// as many runs as Minerva added directories.
	if len(unknownCollections) > 0 || len(unknownSystems) > 0 {
		var b strings.Builder
		b.WriteString("the snapshot holds names this build has never heard of, " +
			"so nothing was written.\n")
		if u := uniqueSorted(unknownCollections); len(u) > 0 {
			fmt.Fprintf(&b, "  %d unknown collection(s) -- add them to minervaCollections in minerva.go:\n", len(u))
			for _, n := range u {
				fmt.Fprintf(&b, "      %q: {Kind: ?, Domain: ?},\n", n)
			}
		}
		if u := uniqueSorted(unknownSystems); len(u) > 0 {
			fmt.Fprintf(&b, "  %d unknown path(s) in a per-machine collection -- "+
				"add them to minervaSystems in minerva_systems.go, mapping to a slug "+
				"or to \"\" where this site has no slug for the machine:\n", len(u))
			for _, n := range u {
				fmt.Fprintf(&b, "      %q: \"\",\n", n)
			}
		}
		b.WriteString("  This is deliberate and is not a retry-able failure: " +
			"it needs a person to decide what each of these is.")
		return rep, fmt.Errorf("%s", b.String())
	}

	// ---- merge against the file on disk ----------------------------------
	prev, err := readMinervaCatalogue(catalogPath)
	if err != nil {
		return rep, err
	}
	byHash := make(map[string]minervaEntry, len(prev.Entries))
	for _, e := range prev.Entries {
		byHash[e.InfoHash] = e
	}

	// Carry forward, per collection, anything the snapshot did not see.
	//
	// A collection missing from THIS snapshot is not evidence it is gone; it is
	// evidence this walk did not reach it. Deleting on that basis is how a
	// category silently empties, so the previous rows stay and the report says
	// so out loud.
	carried := map[string]bool{}
	for _, e := range prev.Entries {
		if seenColl[e.Collection] {
			continue
		}
		if _, known := minervaCollectionFor(e.Collection); !known {
			continue
		}
		entries = append(entries, e)
		carried[e.Collection] = true
	}
	rep.CollectionsCarried = uniqueSorted(minervaKeysOf(carried))

	for _, e := range entries {
		old, existed := byHash[e.InfoHash]
		switch {
		case !existed:
			rep.Added++
		case old == e:
			rep.Unchanged++
		default:
			rep.Updated++
		}
	}
	rep.Published = len(entries)

	// ---- gate 3: the corpus ratchet --------------------------------------
	if !force && len(prev.Entries) > 0 {
		if ratio := float64(len(entries)) / float64(len(prev.Entries)); ratio < minervaCorpusRatchet {
			return rep, fmt.Errorf("this snapshot would publish %d sets where the "+
				"current catalogue has %d (%.0f%%). That is more shrinkage than a "+
				"healthy re-import produces, so nothing was written. Re-run with "+
				"-force if the site really did shrink",
				len(entries), len(prev.Entries), ratio*100)
		}
	}

	// Deterministic, so re-importing an unchanged snapshot produces a
	// byte-identical file and a diff shows only what really moved.
	sort.Slice(entries, func(i, j int) bool { return entries[i].InfoHash < entries[j].InfoHash })
	rep.SpotPaths = minervaSpotPaths(entries)

	cat := minervaCatalogue{
		Version:   minervaCatalogueVersion,
		Source:    minervaSiteName,
		Trackers:  snap.Trackers,
		Generated: snap.FetchedAt,
		Imported:  time.Now().UTC().Format(time.RFC3339),
		Entries:   entries,
	}
	if err := writeMinervaCatalogue(catalogPath, cat); err != nil {
		return rep, err
	}
	return rep, nil
}

// minervaSplitPath returns the collection and the first component below it.
//
// "No-Intro/Nintendo - Game Boy"            -> ("No-Intro", "Nintendo - Game Boy")
// "Redump/IBM - PC compatible/Q"            -> ("Redump", "IBM - PC compatible")
// "bitsavers"                               -> ("bitsavers", "")
//
// The SECOND component is the one the four facet collections cut at, and it is
// deliberately the second and not the last: Redump's IBM subtree inserts a
// letter tier, and the letter "Q" is not a machine.
func minervaSplitPath(path string) (collection, component string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// minervaDisplayName is the text a person reads for a set.
//
// The LAST component, because that is what identifies it -- "Nintendo - Game
// Boy" rather than "No-Intro/Nintendo - Game Boy", since the collection is
// shown separately on the card. A single-component path is its own name.
func minervaDisplayName(path string) string {
	p := strings.Trim(path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// minervaUnlistedLabel names a dashboard-only hash as helpfully as it can.
func minervaUnlistedLabel(h string, snap minervaSnapshot) string {
	if n := strings.TrimSpace(snap.Names[h]); n != "" {
		return fmt.Sprintf("%s (%s)", n, h[:8])
	}
	return h
}

// minervaSpotPaths picks a spread of real paths for the report to print.
func minervaSpotPaths(entries []minervaEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	const want = 10
	step := len(entries) / want
	if step < 1 {
		step = 1
	}
	out := make([]string, 0, want)
	for i := 0; i < len(entries) && len(out) < want; i += step {
		out = append(out, entries[i].Path)
	}
	return out
}

func readMinervaCatalogue(path string) (minervaCatalogue, error) {
	var cat minervaCatalogue
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cat, nil
		}
		return cat, err
	}
	if len(raw) == 0 {
		return cat, nil
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		// The same judgement loadMinervaStore makes: a catalogue that cannot be
		// read is a data problem to look at. Overwriting it would destroy the
		// evidence.
		return cat, fmt.Errorf("the existing catalogue %s is unreadable, so this "+
			"import would replace something nobody has looked at: %w", path, err)
	}
	return cat, nil
}

func writeMinervaCatalogue(path string, cat minervaCatalogue) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(cat)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	// Atomic, so a server reading this file during an import sees the whole old
	// catalogue or the whole new one.
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func minervaKeysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
