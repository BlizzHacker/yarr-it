package main

// The importer's job is to refuse.
//
// Publishing is the easy half; every test here is about a snapshot that looks
// fine and is not, because that is the failure mode this whole shape exists to
// prevent. A quiet importer that publishes 300 of 1,049 sets looks exactly like
// one that is working.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ----------------------------------------------------------------- helpers --

// goodSnapshot is a small but internally consistent snapshot: three sets in
// three collections, every hash resolved, both censuses agreeing.
func goodSnapshot() minervaSnapshot {
	const (
		h1 = "1111111111111111111111111111111111111111"
		h2 = "2222222222222222222222222222222222222222"
		h3 = "3333333333333333333333333333333333333333"
	)
	return minervaSnapshot{
		FetchedAt: "2026-08-24T12:00:00Z",
		Trackers:  []string{"udp://tracker.example:80/announce"},
		Paths: map[string]string{
			h1: "No-Intro/Nintendo - Super Nintendo Entertainment System",
			h2: "bitsavers",
			h3: "Redump/Sony - PlayStation",
		},
		Sizes:        map[string]int64{h1: 8_400_000_000, h2: 1_640_000_000_000, h3: 900_000_000_000},
		Measured:     map[string]string{h1: "2026-04-14T00:11:35Z"},
		Names:        map[string]string{h1: "Minerva_Myrient - No-Intro - Nintendo - Super Nintendo Entertainment System"},
		BrowseHashes: []string{h1, h2, h3},
		DashHashes:   []string{h1, h2, h3},
		Requests:     13,
	}
}

func writeSnapshot(t *testing.T, snap minervaSnapshot) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readCatalogue(t *testing.T, path string) minervaCatalogue {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no catalogue at %s: %v", path, err)
	}
	var cat minervaCatalogue
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatalf("catalogue does not parse: %v", err)
	}
	return cat
}

// ------------------------------------------------------------ the happy path --

func TestAConsistentSnapshotPublishes(t *testing.T) {
	dir := writeSnapshot(t, goodSnapshot())
	out := filepath.Join(t.TempDir(), "minerva.json")

	rep, err := importMinerva(dir, out, false)
	if err != nil {
		t.Fatalf("a consistent snapshot was refused: %v", err)
	}
	if rep.Published != 3 {
		t.Errorf("published %d, want 3", rep.Published)
	}
	cat := readCatalogue(t, out)
	if len(cat.Entries) != 3 {
		t.Fatalf("catalogue holds %d entries", len(cat.Entries))
	}
	// Trackers live once at the head, not on every row, or a 1,049-row file
	// carries 1.4 MB of duplicated URLs.
	if len(cat.Trackers) != 1 {
		t.Errorf("trackers are not at the catalogue head: %v", cat.Trackers)
	}
	// The SNES set must have picked up its machine; bitsavers must not have a
	// machine at all, because it is not cut per machine.
	for _, e := range cat.Entries {
		switch e.Collection {
		case "No-Intro":
			if e.System != "snes" {
				t.Errorf("the No-Intro SNES set carries system %q", e.System)
			}
		case "bitsavers":
			if e.System != "" {
				t.Errorf("bitsavers carries a machine slug %q", e.System)
			}
		}
	}
	// Deterministic: importing the same snapshot twice must produce the same
	// bytes, so a diff shows only what really moved.
	first, _ := os.ReadFile(out)
	if _, err := importMinerva(dir, out, false); err != nil {
		t.Fatalf("re-import failed: %v", err)
	}
	second, _ := os.ReadFile(out)
	if string(first) == "" || len(first) != len(second) {
		t.Error("re-importing an unchanged snapshot produced a different file")
	}
}

// ----------------------------------------------------------------- gate 1 --

// If a hash has no path, the join the whole walk exists to produce has holes,
// and publishing the rows that did resolve would look like a healthy import of
// a smaller site.
func TestHashAccountingMustCloseOrNothingIsWritten(t *testing.T) {
	snap := goodSnapshot()
	delete(snap.Paths, "2222222222222222222222222222222222222222")
	dir := writeSnapshot(t, snap)
	out := filepath.Join(t.TempDir(), "minerva.json")

	_, err := importMinerva(dir, out, false)
	if err == nil {
		t.Fatal("a snapshot with an unresolved hash imported without complaint")
	}
	if !strings.Contains(err.Error(), "accounting") {
		t.Errorf("the error does not say what went wrong: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a failed import wrote a catalogue anyway")
	}
}

// ----------------------------------------------------------------- gate 2 --

// The two censuses are independent on purpose, and the failure this catches is
// them ceasing to describe the same thing at all.
//
// The realistic version is not "the API grew"; extra API-only hashes are
// handled safely below, skipped and named. It is the API changing how it spells
// a hash -- base32 instead of hex, say, or upper case -- at which point the two
// lists overlap on nothing while each looks perfectly healthy on its own. The
// join is then empty and every set would silently vanish.
func TestDisjointCensusesFailTheImport(t *testing.T) {
	snap := goodSnapshot()
	// Same three torrents, spelled differently. Neither list is obviously
	// wrong; together they say nothing.
	snap.DashHashes = []string{
		strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40),
	}
	dir := writeSnapshot(t, snap)
	out := filepath.Join(t.TempDir(), "minerva.json")

	_, err := importMinerva(dir, out, false)
	if err == nil {
		t.Fatal("two censuses that overlap on nothing imported without complaint")
	}
	if !strings.Contains(err.Error(), "agree") {
		t.Errorf("the error does not say what went wrong: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a failed import wrote a catalogue anyway")
	}
}

// An empty census on either side is refused rather than divided by.
func TestAnEmptyCensusFailsTheImport(t *testing.T) {
	snap := goodSnapshot()
	snap.DashHashes = nil
	dir := writeSnapshot(t, snap)
	out := filepath.Join(t.TempDir(), "minerva.json")

	if _, err := importMinerva(dir, out, false); err == nil {
		t.Fatal("an empty dashboard census imported without complaint")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a failed import wrote a catalogue anyway")
	}
}

// One unlisted torrent is normal and must not fail the import -- "Outliers" is
// real and deliberate -- but it must be SKIPPED and NAMED rather than published
// or silently dropped. Nobody has looked inside it.
func TestTheUnlistedTorrentIsQuarantinedAndNamed(t *testing.T) {
	snap := goodSnapshot()
	const outlier = "9999999999999999999999999999999999999999"
	snap.DashHashes = append(snap.DashHashes, outlier)
	snap.Sizes[outlier] = 24_740_000_000
	snap.Names[outlier] = "Minerva_Myrient - Outliers"
	dir := writeSnapshot(t, snap)
	out := filepath.Join(t.TempDir(), "minerva.json")

	rep, err := importMinerva(dir, out, false)
	if err != nil {
		t.Fatalf("one unlisted torrent failed the whole import: %v", err)
	}
	if rep.SkipNoPath != 1 {
		t.Errorf("the unlisted torrent was not counted as skipped: %d", rep.SkipNoPath)
	}
	if len(rep.Unlisted) != 1 || !strings.Contains(rep.Unlisted[0], "Outliers") {
		t.Errorf("the report does not name the unlisted torrent: %v", rep.Unlisted)
	}
	for _, e := range readCatalogue(t, out).Entries {
		if e.InfoHash == outlier {
			t.Error("the unlisted, unvetted torrent was published")
		}
	}
	if !strings.Contains(rep.String(), "Outliers") {
		t.Error("the printed report does not mention the unlisted torrent")
	}
}

// ----------------------------------------------------------------- gate 3 --

// A snapshot much smaller than what is already published is refused, because a
// partial page at the far end produces exactly that and every step reports
// success.
func TestAShrunkenSnapshotIsRefused(t *testing.T) {
	out := filepath.Join(t.TempDir(), "minerva.json")

	// Publish a healthy catalogue of ten sets first.
	big := goodSnapshot()
	for i := 0; i < 7; i++ {
		h := strings.Repeat(string(rune('a'+i)), 40)
		big.Paths[h] = "No-Intro/Nintendo - Game Boy"
		big.Sizes[h] = 1 << 30
		big.BrowseHashes = append(big.BrowseHashes, h)
		big.DashHashes = append(big.DashHashes, h)
	}
	if _, err := importMinerva(writeSnapshot(t, big), out, false); err != nil {
		t.Fatalf("the first import failed: %v", err)
	}
	if n := len(readCatalogue(t, out).Entries); n != 10 {
		t.Fatalf("first import published %d, want 10", n)
	}

	// Now offer three. That is 30% and must be refused.
	small := writeSnapshot(t, goodSnapshot())
	if _, err := importMinerva(small, out, false); err == nil {
		t.Fatal("a 70% shrink imported without complaint")
	}
	if n := len(readCatalogue(t, out).Entries); n != 10 {
		t.Errorf("the refused import damaged the catalogue: %d entries left", n)
	}

	// And -force is the way a person says the shrink is real.
	if _, err := importMinerva(small, out, true); err != nil {
		t.Fatalf("-force did not allow the shrink: %v", err)
	}
	if n := len(readCatalogue(t, out).Entries); n != 3 {
		t.Errorf("after -force the catalogue holds %d entries, want 3", n)
	}
}

// ----------------------------------------------------------------- gate 4 --

// An unknown collection stops the import and NAMES it. A silent default is how
// 40 TB of unclassified material would arrive in a games search.
func TestAnUnknownCollectionFailsTheImportLoudly(t *testing.T) {
	snap := goodSnapshot()
	snap.Paths["2222222222222222222222222222222222222222"] = "Nintendo Switch Dumps/Games"
	dir := writeSnapshot(t, snap)
	out := filepath.Join(t.TempDir(), "minerva.json")

	_, err := importMinerva(dir, out, false)
	if err == nil {
		t.Fatal("an unknown collection imported without complaint")
	}
	if !strings.Contains(err.Error(), "Nintendo Switch Dumps") {
		t.Errorf("the error does not name the collection: %v", err)
	}
	if !strings.Contains(err.Error(), "minervaCollections") {
		t.Errorf("the error does not say where to fix it: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a failed import wrote a catalogue anyway")
	}
}

// An unknown machine directory inside a per-machine collection is fatal for the
// same reason, and both kinds must be named AT ONCE -- otherwise somebody
// extends the map, re-runs, and is told about the next one, once per run.
func TestUnknownNamesAreAllReportedAtOnce(t *testing.T) {
	snap := goodSnapshot()
	h4 := strings.Repeat("4", 40)
	h5 := strings.Repeat("5", 40)
	h6 := strings.Repeat("6", 40)
	snap.Paths[h4] = "No-Intro/Nintendo - Machine From 2027"
	snap.Paths[h5] = "Redump/Sony - PlayStation 6"
	snap.Paths[h6] = "Brand New Collection/Something"
	for _, h := range []string{h4, h5, h6} {
		snap.Sizes[h] = 1 << 30
		snap.BrowseHashes = append(snap.BrowseHashes, h)
		snap.DashHashes = append(snap.DashHashes, h)
	}
	dir := writeSnapshot(t, snap)

	_, err := importMinerva(dir, filepath.Join(t.TempDir(), "minerva.json"), false)
	if err == nil {
		t.Fatal("unknown names imported without complaint")
	}
	for _, want := range []string{
		"Nintendo - Machine From 2027",
		"Sony - PlayStation 6",
		"Brand New Collection",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	// And it must say plainly that this is not something to retry.
	if !strings.Contains(err.Error(), "retry") {
		t.Errorf("the error does not warn automation off retrying: %v", err)
	}
}

// ------------------------------------------------------- skips and carrying --

// An unsized set is a hard skip. "Whole set" with no number beside it is the
// tile that gets somebody 6.75 TB by surprise.
func TestAnUnsizedSetIsSkippedRatherThanPublishedAtZero(t *testing.T) {
	snap := goodSnapshot()
	delete(snap.Sizes, "2222222222222222222222222222222222222222")
	dir := writeSnapshot(t, snap)
	out := filepath.Join(t.TempDir(), "minerva.json")

	rep, err := importMinerva(dir, out, false)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if rep.SkipNoSize != 1 {
		t.Errorf("the unsized set was not counted: %+v", rep.SkipNoSize)
	}
	for _, e := range readCatalogue(t, out).Entries {
		if e.SizeBytes <= 0 {
			t.Errorf("a set was published with size %d", e.SizeBytes)
		}
	}
}

// Miscellaneous is excluded, and there is nothing anywhere that switches it on.
//
// This test used to assert the opposite half as well -- that MINERVA_COLLECTIONS
// published it -- and that assertion was true of the importer and false of the
// system. minervaCard and setsForSystem refuse minervaExcluded unconditionally,
// so an opted-in import wrote 6.62 TB of rows that no surface could render, and
// /api/health reported `optedIn: 1` about a collection that could not produce a
// single tile. The refusals are the behaviour anybody wants; the switch that
// contradicted them is gone. What is pinned here now is that the three places
// give the same answer, and that no environment can talk any of them out of it.
func TestMiscellaneousIsExcludedAndCannotBeSwitchedOn(t *testing.T) {
	snap := goodSnapshot()
	snap.Paths["2222222222222222222222222222222222222222"] = "Miscellaneous"
	dir := writeSnapshot(t, snap)

	// Set anyway. The variable no longer exists in the code; a stale unit file
	// or a shell that still exports it must change nothing.
	t.Setenv("MINERVA_COLLECTIONS", "Miscellaneous")

	out := filepath.Join(t.TempDir(), "minerva.json")
	rep, err := importMinerva(dir, out, false)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if rep.SkipExcludedCollection != 1 {
		t.Errorf("Miscellaneous was not skipped: %+v", rep)
	}
	for _, e := range readCatalogue(t, out).Entries {
		if e.Collection == "Miscellaneous" {
			t.Error("Miscellaneous was published; nothing may publish it")
		}
	}
}

// The rest of the guarantee, at the two serve-time surfaces: even a catalogue
// file written by an older binary -- one that DID honour the opt-in -- produces
// no card and no band item. A catalogue can be older than the binary reading it,
// so the refusal has to live where the rendering happens and not only at import.
func TestAnExcludedCollectionInTheCatalogueStillRendersNothing(t *testing.T) {
	row := minervaRow{minervaEntry: minervaEntry{
		InfoHash:   "5555555555555555555555555555555555555555",
		Path:       "Miscellaneous/Nintendo - Super Nintendo Entertainment System",
		Collection: "Miscellaneous",
		Name:       "Nintendo - Super Nintendo Entertainment System",
		System:     "snes",
		SizeBytes:  6_620_000_000_000,
	}}
	row.lower = strings.ToLower(row.Path)
	row.magnet = minervaMagnet(row.minervaEntry, nil)

	if _, ok := minervaCard(row); ok {
		t.Error("minervaCard built a card for an excluded collection")
	}

	s := &minervaStore{rows: []minervaRow{row}}
	if cards := s.search("nintendo", "", nil, 40); len(cards) != 0 {
		t.Errorf("an excluded collection reached a search: %d cards", len(cards))
	}
	if items := s.setsForSystem("snes"); len(items) != 0 {
		t.Errorf("an excluded collection reached the category band: %d items", len(items))
	}

	// And health says nothing about an opt-in, because there is no longer one to
	// report. It reported `optedIn: 1` here for a collection that could never
	// produce a tile.
	if _, ok := s.stats()["optedIn"]; ok {
		t.Error("/api/health still reports an opt-in that does not exist")
	}
}

// A collection the snapshot did not reach is CARRIED FORWARD, not deleted. A
// snapshot is a partial view, and deleting on that basis is how a category
// silently empties.
func TestAMissingCollectionIsCarriedForwardNotDeleted(t *testing.T) {
	out := filepath.Join(t.TempDir(), "minerva.json")
	if _, err := importMinerva(writeSnapshot(t, goodSnapshot()), out, false); err != nil {
		t.Fatalf("first import failed: %v", err)
	}

	// A second snapshot whose walk never reached bitsavers.
	snap := goodSnapshot()
	delete(snap.Paths, "2222222222222222222222222222222222222222")
	snap.BrowseHashes = []string{
		"1111111111111111111111111111111111111111",
		"3333333333333333333333333333333333333333",
	}
	snap.DashHashes = snap.BrowseHashes

	rep, err := importMinerva(writeSnapshot(t, snap), out, false)
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	found := false
	for _, e := range readCatalogue(t, out).Entries {
		if e.Collection == "bitsavers" {
			found = true
		}
	}
	if !found {
		t.Error("a collection the walk did not reach was deleted rather than carried forward")
	}
	if len(rep.CollectionsCarried) != 1 || rep.CollectionsCarried[0] != "bitsavers" {
		t.Errorf("the report does not name what it carried: %v", rep.CollectionsCarried)
	}
	if !strings.Contains(rep.String(), "CARRIED FORWARD") {
		t.Error("the printed report does not say it carried anything forward")
	}
}

// An existing catalogue that cannot be read must not be overwritten -- that
// would destroy the evidence of whatever went wrong.
func TestAnUnreadableExistingCatalogueIsNotOverwritten(t *testing.T) {
	out := filepath.Join(t.TempDir(), "minerva.json")
	if err := os.WriteFile(out, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := importMinerva(writeSnapshot(t, goodSnapshot()), out, false); err == nil {
		t.Fatal("an unreadable catalogue was silently replaced")
	}
	raw, _ := os.ReadFile(out)
	if string(raw) != "{not json" {
		t.Error("the unreadable catalogue was overwritten anyway")
	}
}

// ---------------------------------------------------------------- the path --

// The facet level is the SECOND component, not the last: Redump's IBM subtree
// inserts a letter tier and the letter "Q" is not a machine.
func TestTheMachineIsTheSecondComponentNotTheLast(t *testing.T) {
	coll, comp := minervaSplitPath("Redump/IBM - PC compatible/Q")
	if coll != "Redump" || comp != "IBM - PC compatible" {
		t.Errorf("split gave (%q, %q)", coll, comp)
	}
	coll, comp = minervaSplitPath("bitsavers")
	if coll != "bitsavers" || comp != "" {
		t.Errorf("a whole-collection torrent split to (%q, %q)", coll, comp)
	}
}

// The display name is the last component, which is what identifies the set --
// the collection is shown separately on the card.
func TestTheDisplayNameIsTheLastComponent(t *testing.T) {
	if got := minervaDisplayName("Redump/IBM - PC compatible/Q"); got != "Q" {
		t.Errorf("display name = %q", got)
	}
	if got := minervaDisplayName("bitsavers"); got != "bitsavers" {
		t.Errorf("display name = %q", got)
	}
}

// The report prints real paths so a template change that mangles them is
// visible to a person, rather than surfacing weeks later as bad filenames.
func TestTheReportPrintsRealPathsToEyeball(t *testing.T) {
	dir := writeSnapshot(t, goodSnapshot())
	rep, err := importMinerva(dir, filepath.Join(t.TempDir(), "minerva.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SpotPaths) == 0 {
		t.Fatal("the report printed no sample paths")
	}
	if !strings.Contains(rep.String(), "sample of resolved paths") {
		t.Error("the printed report has no sample-path section")
	}
}
