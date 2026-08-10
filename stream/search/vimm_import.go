package main

// Importing the Vimm's Lair export.
//
// The input is whatever the browser extension has scraped so far. That is the
// governing fact about this file: it is not a finished dataset, it is a scan in
// progress, and it will be handed to this importer again and again as it fills
// in. At the time of writing it holds 22,970 rows of which 5,586 have been
// visited and 17,384 are still nothing but a vault id and a name.
//
// Three properties follow from that, and they are the whole design:
//
//  1. RE-RUNNABLE. Import is a merge keyed on the vault id, which is Vimm's own
//     stable page id. Importing the same export twice changes nothing;
//     importing a longer one adds the new rows and updates the ones that grew.
//     Nothing is ever appended blindly, so there is no duplicate to clean up.
//
//  2. IMPROVING, NOT MERELY GROWING. A row's title can get BETTER on a
//     re-import. The extension build that produced the first export could not
//     find a game name on a vault page at all -- there is no h1, no
//     .game-title, no itemprop -- and returned page furniture for all 4,470
//     visited rows, so those titles had to be recovered from the filename. The
//     fixed build reads the document title. One export will contain rows from
//     both, so the merge takes the better of what it has rather than treating a
//     known id as finished. It is also one-way: a good title is never replaced
//     by furniture, so re-importing an OLDER export cannot undo a repair.
//
//  3. LOUD ABOUT WHAT IT DID NOT PUBLISH. Every skip is counted against a
//     stated reason and printed. A quiet importer that publishes 5,000 of
//     23,000 rows looks identical to one that is broken.
//
// WHERE THE RESULT LIVES, AND WHY THERE
//
// /var/lib/mw-search/vimm.json, beside library.json, under the unit's
// StateDirectory. Three reasons, in order:
//
//   - It has to survive a restart. This is a catalogue Wade spent hours
//     scraping through a browser; losing it to a deploy and asking him to run
//     the scan again is not a real option, so it cannot live only in memory.
//   - It has to be small enough to parse at startup. The export is 9.2 MB and
//     three quarters of it is unusable rows; what is written here is the
//     publishable remainder with the title already derived and the machine
//     already resolved. Nothing re-derives anything on a request path, and the
//     9 MB file is never opened by the server at all.
//   - ProtectSystem=strict makes every other path read-only inside the unit's
//     sandbox. StateDirectory is the one directory systemd creates, chowns and
//     mounts writable, which is why library.json already lives there.
//
// The write is atomic -- temp file, rename -- so a server starting during an
// import reads either the old catalogue or the new one and never half of one.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// vimmExport is the extension's file, as far as this cares about it.
type vimmExport struct {
	Version     int    `json:"version"`
	GeneratedAt string `json:"generated_at"`
	Source      string `json:"source"`
	Items       []struct {
		VaultID      string `json:"vault_id"`
		Title        string `json:"title"`
		Filename     string `json:"filename"`
		Platform     string `json:"platform"`
		PageURL      string `json:"page_url"`
		PlayURL      string `json:"play_url"`
		DownloadURL  string `json:"download_url"`
		Playable     bool   `json:"playable"`
		Downloadable bool   `json:"downloadable"`
		SizeBytes    int64  `json:"size_bytes"`
	} `json:"items"`
}

// vimmImportReport is what the import says it did. Every row of the input is
// accounted for in exactly one of these.
type vimmImportReport struct {
	Read int

	Published int // in the file after the merge
	Added     int // ids that were not there before
	Updated   int // ids that were, and changed
	Unchanged int

	// Skips, by reason.
	SkipNoVaultID  int
	SkipNoPlatform int
	SkipNoTitle    int
	SkipNoTarget   int

	// Not skips: things worth knowing about the input.
	RejectedURLs   int            // a URL that was not https on vimm.net
	UnknownExt     int            // a filename ending in a dotted suffix we do not strip
	TitlesFromPage int            // published rows named by the page
	TitlesFromFile int            // published rows named by their filename
	NoSlug         int            // published rows on a machine this site has no slug for
	Systems        map[string]int // published rows per Vimm platform

	// UnknownPlatforms is the loud failure. A platform absent from vimmSystems
	// stops the import rather than being dropped.
	UnknownPlatforms map[string]int
}

func newVimmImportReport() *vimmImportReport {
	return &vimmImportReport{
		Systems:          map[string]int{},
		UnknownPlatforms: map[string]int{},
	}
}

// importVimm reads an export, merges it into the catalogue at path and writes
// it back.
//
// The merge reads the EXISTING file rather than taking it from a running store,
// so this is safe to run against a deployment: the server is not consulted and
// keeps serving its loaded copy until it is restarted or told to reload.
func importVimm(exportPath, catalogPath string) (*vimmImportReport, error) {
	raw, err := os.ReadFile(exportPath)
	if err != nil {
		return nil, fmt.Errorf("read export: %w", err)
	}
	var exp vimmExport
	if err := json.Unmarshal(raw, &exp); err != nil {
		return nil, fmt.Errorf("parse export: %w", err)
	}

	existing := vimmCatalogue{}
	if catalogPath != "" {
		if old, err := os.ReadFile(catalogPath); err == nil && len(old) > 0 {
			if err := json.Unmarshal(old, &existing); err != nil {
				// Refuse rather than overwrite. A catalogue that cannot be read
				// is a thing to look at, not a thing to silently replace with
				// whatever this export happens to hold.
				return nil, fmt.Errorf("existing catalogue %s is unreadable: %w", catalogPath, err)
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read catalogue: %w", err)
		}
	}

	rep := newVimmImportReport()
	rep.Read = len(exp.Items)

	by := make(map[string]vimmEntry, len(existing.Entries)+len(exp.Items))
	for _, e := range existing.Entries {
		by[e.VaultID] = e
	}
	before := len(by)

	// One pass to find every unknown platform, so the error names all of them
	// rather than the first. Somebody fixing this wants the whole list.
	for _, it := range exp.Items {
		p := strings.TrimSpace(it.Platform)
		if p == "" {
			continue
		}
		if _, known := vimmSystemFor(p); !known {
			rep.UnknownPlatforms[p]++
		}
	}
	if len(rep.UnknownPlatforms) > 0 {
		return rep, fmt.Errorf("unknown Vimm platforms: %s -- add them to vimmSystems in vimm.go, "+
			"mapping to a slug from systems.go or to \"\" where this site has none",
			describeCounts(rep.UnknownPlatforms))
	}

	changed := map[string]bool{}
	for _, it := range exp.Items {
		id := strings.TrimSpace(it.VaultID)
		if id == "" {
			rep.SkipNoVaultID++
			continue
		}
		platform := strings.TrimSpace(it.Platform)
		if platform == "" {
			// The un-visited three quarters of the scan. These carry a real
			// name -- the listing page has one even where the vault page does
			// not -- and nothing else: no machine, no file, no download URL and
			// no play URL. A name alone is not something to publish. Under this
			// site's rule a tile carries a verified target or is not shown, and
			// the only address these have is a vault page which is neither a
			// download nor a player. They fill in when the scan reaches them.
			rep.SkipNoPlatform++
			continue
		}
		system, _ := vimmSystemFor(platform)

		file := strings.TrimSpace(it.Filename)
		if _, unknownExt := stripVimmExtension(file); unknownExt {
			rep.UnknownExt++
		}

		page := vimmURL(it.PageURL)
		play := vimmURL(it.PlayURL)
		download := vimmURL(it.DownloadURL)
		for _, pair := range [][2]string{
			{it.PageURL, page}, {it.PlayURL, play}, {it.DownloadURL, download},
		} {
			if strings.TrimSpace(pair[0]) != "" && pair[1] == "" {
				rep.RejectedURLs++
			}
		}

		next := by[id] // zero value when new
		next.VaultID = id
		next.Platform = platform
		next.System = system
		if page != "" {
			next.Page = page
		}
		if play != "" {
			next.Play = play
		}
		if download != "" {
			next.Download = download
		}
		if it.SizeBytes > 0 {
			next.Size = it.SizeBytes
		}
		if file != "" {
			next.File = file
		}
		// A real page title is kept; furniture is not, and never displaces one
		// already held. This is the one-way ratchet described at the top.
		if t := strings.TrimSpace(it.Title); t != "" && !isVimmNoiseTitle(t) {
			next.PageTitle = t
		}
		next.Title, next.TitleFrom = vimmTitle(next.PageTitle, next.File)

		// A row that cannot be published is skipped, and an entry already in
		// the catalogue under this id is LEFT ALONE rather than removed. An
		// export is a partial view -- a re-scan can regress a row -- and
		// deleting something that worked yesterday on the strength of a worse
		// answer today is the same mistake as never improving it.
		if next.Title == "" {
			rep.SkipNoTitle++
			continue
		}
		if next.Play == "" && next.Download == "" {
			rep.SkipNoTarget++
			continue
		}

		if old, had := by[id]; !had || old != next {
			changed[id] = true
		}
		by[id] = next
	}

	entries := make([]vimmEntry, 0, len(by))
	for _, e := range by {
		entries = append(entries, e)
		rep.Systems[e.Platform]++
		if e.System == "" {
			rep.NoSlug++
		}
		switch e.TitleFrom {
		case "page":
			rep.TitlesFromPage++
		case "file":
			rep.TitlesFromFile++
		}
	}
	sortVimmEntries(entries)

	rep.Published = len(entries)
	rep.Added = len(entries) - before
	if rep.Added < 0 {
		rep.Added = 0
	}
	rep.Updated = len(changed) - rep.Added
	if rep.Updated < 0 {
		rep.Updated = 0
	}
	rep.Unchanged = rep.Published - rep.Added - rep.Updated

	out := vimmCatalogue{
		Version:   vimmCatalogueVersion,
		Source:    firstNonEmptyString(exp.Source, existing.Source, "Vimm.net catalogue extension"),
		Generated: firstNonEmptyString(exp.GeneratedAt, existing.Generated),
		Imported:  time.Now().UTC().Format(time.RFC3339),
		Entries:   entries,
	}
	if catalogPath != "" {
		if err := writeVimmCatalogue(catalogPath, out); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// sortVimmEntries orders by vault id, numerically where it is a number. A
// stable order is what makes re-importing an unchanged export produce an
// unchanged file, so a diff shows only what really moved.
func sortVimmEntries(entries []vimmEntry) {
	sort.Slice(entries, func(i, j int) bool {
		a, errA := strconv.Atoi(entries[i].VaultID)
		b, errB := strconv.Atoi(entries[j].VaultID)
		if errA == nil && errB == nil {
			return a < b
		}
		return entries[i].VaultID < entries[j].VaultID
	})
}

func writeVimmCatalogue(path string, cat vimmCatalogue) error {
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
	// catalogue or the whole new one. os.Rename replaces on POSIX and on
	// Windows since Go 1.5.
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// describeCounts renders a count map as "a (3), b (1)", biggest first.
func describeCounts(m map[string]int) string {
	type kv struct {
		k string
		n int
	}
	all := make([]kv, 0, len(m))
	for k, n := range m {
		all = append(all, kv{k, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].k < all[j].k
	})
	parts := make([]string, 0, len(all))
	for _, e := range all {
		parts = append(parts, fmt.Sprintf("%s (%d)", e.k, e.n))
	}
	return strings.Join(parts, ", ")
}

// String is the import report a person reads. Every input row is accounted for.
func (r *vimmImportReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "read %d rows from the export\n", r.Read)
	fmt.Fprintf(&b, "published %d entries (%d added, %d updated, %d unchanged)\n",
		r.Published, r.Added, r.Updated, r.Unchanged)
	fmt.Fprintf(&b, "  titles: %d from the page, %d recovered from a filename\n",
		r.TitlesFromPage, r.TitlesFromFile)
	fmt.Fprintf(&b, "  machines: %d entries on a system this site has a slug for, %d without one\n",
		r.Published-r.NoSlug, r.NoSlug)
	fmt.Fprintf(&b, "skipped %d rows:\n", r.SkipNoVaultID+r.SkipNoPlatform+r.SkipNoTitle+r.SkipNoTarget)
	fmt.Fprintf(&b, "  %6d  not yet scanned (no platform, no file, no download or play URL)\n", r.SkipNoPlatform)
	fmt.Fprintf(&b, "  %6d  no usable title from either the page or a filename\n", r.SkipNoTitle)
	fmt.Fprintf(&b, "  %6d  no verified target to offer\n", r.SkipNoTarget)
	fmt.Fprintf(&b, "  %6d  no vault id\n", r.SkipNoVaultID)
	if r.RejectedURLs > 0 {
		fmt.Fprintf(&b, "  %6d  URLs rejected for not being https on %s\n", r.RejectedURLs, vimmHost)
	}
	if r.UnknownExt > 0 {
		fmt.Fprintf(&b, "  %6d  filenames ending in a suffix that was left alone\n", r.UnknownExt)
	}
	b.WriteString("platforms published:\n")
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
		slug, _ := vimmSystemFor(e.k)
		if slug == "" {
			slug = "— no slug on this site —"
		}
		fmt.Fprintf(&b, "  %6d  %-20s %s\n", e.n, e.k, slug)
	}
	return b.String()
}
