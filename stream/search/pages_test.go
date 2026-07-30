package main

import (
	"strings"
	"testing"
)

func TestArchiveIdentifierHandlesEveryUrlShape(t *testing.T) {
	cases := map[string]string{
		"https://archive.org/details/superman-1938-issue-1":  "superman-1938-issue-1",
		"https://archive.org/download/nasa_images_965/x.jpg": "nasa_images_965",
		"https://archive.org/details/foo?start=3":            "foo",
		"https://archive.org/details/bar#page":               "bar",
		"ia:baz":                                             "baz",
		"plain-identifier":                                   "plain-identifier",
		"":                                                   "",
		"https://example.com/not-archive":                    "",
	}
	for in, want := range cases {
		if got := archiveIdentifier(in); got != want {
			t.Errorf("archiveIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

// A PDF-only comic is unreadable on a television; the derived page images are
// the entire reason it can be shown at all.
func TestComicPagesUseTheDerivedImageEndpoint(t *testing.T) {
	pages := comicPages("superman-1938-issue-1", 3)
	if len(pages) != 3 {
		t.Fatalf("got %d pages, want 3", len(pages))
	}
	first := pages[0]
	for _, want := range []string{"/download/superman-1938-issue-1/page/", "n0_w", ".jpg"} {
		if !strings.Contains(first, want) {
			t.Errorf("page URL %q is missing %q", first, want)
		}
	}
	if !strings.Contains(pages[2], "n2_") {
		t.Errorf("third page is %q, want index 2", pages[2])
	}
}

func TestImageFilesSkipFurnitureAndSortStably(t *testing.T) {
	m := &archiveMetadata{}
	m.Files = []struct {
		Name   string `json:"name"`
		Format string `json:"format"`
		Size   string `json:"size"`
	}{
		{Name: "b.jpg"}, {Name: "a.jpg"},
		{Name: "__ia_thumb.jpg"},  // the item's own tile, not content
		{Name: "cover_thumb.jpg"}, // a thumbnail of a page
		{Name: "notes.xml"},       // not a picture
		{Name: "scan.PNG"},        // case must not matter
	}
	got := imageFiles(m, "item")

	if len(got) != 3 {
		t.Fatalf("got %d images, want 3: %v", len(got), got)
	}
	if !strings.HasSuffix(got[0], "/a.jpg") || !strings.HasSuffix(got[1], "/b.jpg") {
		t.Errorf("not in name order: %v", got)
	}
	for _, u := range got {
		if strings.Contains(u, "thumb") {
			t.Errorf("a thumbnail leaked into the pictures: %s", u)
		}
		if !strings.HasPrefix(u, "https://archive.org/download/item/") {
			t.Errorf("wrong base for %s", u)
		}
	}
}

// Filenames on archive.org routinely contain spaces; an unescaped one produces
// a URL the client silently fails to load.
func TestFilenamesAreEscapedButPathsSurvive(t *testing.T) {
	if got := urlPathEscape("Some File.jpg"); got != "Some%20File.jpg" {
		t.Errorf("space not escaped: %q", got)
	}
	if got := urlPathEscape("dir/sub/a b.jpg"); got != "dir/sub/a%20b.jpg" {
		t.Errorf("directory separators must survive: %q", got)
	}
	if got := urlPathEscape("q?x&y.jpg"); strings.ContainsAny(got, "?&") {
		t.Errorf("query characters not escaped: %q", got)
	}
}
