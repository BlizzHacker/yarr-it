package main

import (
	"strings"
	"testing"
)

// Every row must be scoped to something, or a landing shelf becomes whatever
// archive.org happens to sort first across 40 million items.
func TestEveryRowIsScopedAndLabelled(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range archiveRows {
		if r.key == "" || r.title == "" || r.query == "" || r.sort == "" {
			t.Errorf("row %q is missing a field", r.key)
		}
		if seen[r.key] {
			t.Errorf("duplicate row key %q", r.key)
		}
		seen[r.key] = true
		if !strings.Contains(r.query, "mediatype:") {
			t.Errorf("row %q is not scoped by mediatype: %s", r.key, r.query)
		}
	}
}

// The comics row once led with a complete Batman run, because the general
// `comics` collection is user-uploaded and mostly still in copyright -- and
// filtering it by year does not help, since the year describes the issues
// rather than the upload.
func TestTheComicsRowDoesNotUseTheGeneralUploadBucket(t *testing.T) {
	var row archiveRow
	for _, r := range archiveRows {
		if r.key == "ia-comics" {
			row = r
		}
	}
	if row.key == "" {
		t.Fatal("no comics row")
	}
	if strings.Contains(row.query, "collection:(comics)") {
		t.Error("comics row is back on the general upload bucket")
	}
	if !strings.Contains(row.query, "fawcett-comics") {
		t.Error("comics row should name curated golden-age publishers")
	}
}

// A game the touch player can run must open in it; anything else opens in
// theirs, which costs no bandwidth here.
func TestPlayTargetPicksTheTouchPlayerOnlyWhenACoreExists(t *testing.T) {
	nes := iaSearchDoc{Identifier: "smb", Emulator: "nes"}
	if got := playTargetFor(nes, "game"); !strings.HasSuffix(got, "#ejs") {
		t.Errorf("nes game = %q, want the touch player", got)
	}
	dos := iaSearchDoc{Identifier: "oregon", Emulator: "dosbox"}
	if got := playTargetFor(dos, "game"); strings.HasSuffix(got, "#ejs") {
		t.Errorf("dos game = %q, but EmulatorJS has no dosbox core", got)
	}
	book := iaSearchDoc{Identifier: "alice"}
	if got := playTargetFor(book, "text"); got != "https://archive.org/details/alice" {
		t.Errorf("book = %q", got)
	}
}
