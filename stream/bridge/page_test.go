package main

import (
	"net/url"
	"strings"
	"testing"
)

func base() *url.URL {
	u, _ := url.Parse("https://example.org/description.php?id=1")
	return u
}

// Magnet parameters are separated by &, which every page writes as &amp;. A
// link left un-unescaped has literal "amp;" before each tracker and resolves
// to a torrent with no trackers at all.
func TestMagnetEntitiesAreUnescaped(t *testing.T) {
	html := `<a href="magnet:?xt=urn:btih:EB7C1B76&amp;dn=N64+pack&amp;tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337">DL</a>`
	out := extractLinks(html, base())
	if len(out.Links) != 1 {
		t.Fatalf("want 1 link, got %d", len(out.Links))
	}
	got := out.Links[0].URL
	if strings.Contains(got, "amp;") {
		t.Errorf("entity survived: %s", got)
	}
	if !strings.Contains(got, "&dn=N64+pack") || !strings.Contains(got, "&tr=udp") {
		t.Errorf("parameters lost: %s", got)
	}
	if out.Links[0].Name != "N64 pack" {
		t.Errorf("name = %q, want the dn parameter", out.Links[0].Name)
	}
}

// Sites put magnets in scripts and data attributes as often as in hrefs, and a
// link assembled by JavaScript is not in the markup as an anchor at all.
func TestMagnetsAreFoundOutsideAnchors(t *testing.T) {
	html := `<script>var m = "magnet:?xt=urn:btih:AAAA&dn=one";</script>
	         <div data-magnet='magnet:?xt=urn:btih:BBBB&dn=two'></div>
	         plain text magnet:?xt=urn:btih:CCCC&dn=three here`
	out := extractLinks(html, base())
	if len(out.Links) != 3 {
		t.Fatalf("want 3 links, got %d: %+v", len(out.Links), out.Links)
	}
}

func TestDuplicateLinksAreCollapsed(t *testing.T) {
	html := `<a href="magnet:?xt=urn:btih:AAAA">a</a><a href="magnet:?xt=urn:btih:AAAA">b</a>`
	if out := extractLinks(html, base()); len(out.Links) != 1 {
		t.Errorf("duplicate not collapsed: %d links", len(out.Links))
	}
}

// A .torrent file carries its metadata inline, so the file list is known
// without first finding a peer -- which is why it should be offered first.
func TestTorrentFilesAreOfferedBeforeMagnets(t *testing.T) {
	html := `<a href="magnet:?xt=urn:btih:AAAA">m</a>
	         <a href="https://example.org/files/Best%20N64.torrent">t</a>`
	out := extractLinks(html, base())
	if len(out.Links) != 2 {
		t.Fatalf("want 2 links, got %d", len(out.Links))
	}
	if out.Links[0].Kind != "torrent" {
		t.Errorf("magnet led; want the .torrent first")
	}
	if out.Links[0].Name != "Best N64.torrent" {
		t.Errorf("name = %q", out.Links[0].Name)
	}
}

func TestTitleIsReadForLabelling(t *testing.T) {
	html := `<html><head><title>Nintendo 64 ROM pack &amp; more</title></head><body>
	         <a href="magnet:?xt=urn:btih:AAAA">x</a></body></html>`
	out := extractLinks(html, base())
	if out.Title != "Nintendo 64 ROM pack & more" {
		t.Errorf("title = %q", out.Title)
	}
}

// An index page can carry hundreds of links; past a point they are noise and a
// response nobody can use.
func TestLinkCountIsCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < pageMaxLinks*3; i++ {
		b.WriteString(`<a href="magnet:?xt=urn:btih:` + strings.Repeat("A", 30) + string(rune('a'+i%26)) + `">x</a>`)
	}
	if out := extractLinks(b.String(), base()); len(out.Links) > pageMaxLinks {
		t.Errorf("cap not applied: %d links", len(out.Links))
	}
}

func TestAPageWithNothingPlayableYieldsNoLinks(t *testing.T) {
	out := extractLinks(`<html><body><p>Nothing here.</p></body></html>`, base())
	if len(out.Links) != 0 {
		t.Errorf("invented %d links", len(out.Links))
	}
	// The zero value must still marshal as [] rather than null, so the client
	// can iterate it without a guard.
	if out.Links == nil {
		t.Error("Links should be an empty slice, not nil")
	}
}

// Trailing punctuation from surrounding prose is not part of the URI.
func TestTrailingProsePunctuationIsTrimmed(t *testing.T) {
	out := extractLinks(`Grab it here: magnet:?xt=urn:btih:AAAA.`, base())
	if got := out.Links[0].URL; strings.HasSuffix(got, ".") {
		t.Errorf("trailing period kept: %s", got)
	}
}

// The real link from the user's page, with the entity encoding a browser sees.
func TestTheRealN64PackMagnetSurvivesIntact(t *testing.T) {
	html := `<a href="magnet:?xt=urn:btih:EB7C1B7623104466A65034A5554DA59AE6C4517A&amp;` +
		`dn=Nintendo%2064%20ROM%20pack%3A%20Best%20N64%20games.&amp;` +
		`tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&amp;` +
		`tr=udp%3A%2F%2Fopen.stealth.si%3A80%2Fannounce">Get this torrent</a>`
	out := extractLinks(html, base())
	if len(out.Links) != 1 {
		t.Fatalf("want 1 link, got %d", len(out.Links))
	}
	link := out.Links[0]
	if !strings.HasPrefix(link.URL, "magnet:?xt=urn:btih:EB7C1B7623104466A65034A5554DA59AE6C4517A") {
		t.Errorf("infohash mangled: %s", link.URL)
	}
	if strings.Count(link.URL, "&tr=") != 2 {
		t.Errorf("trackers lost: %s", link.URL)
	}
	if link.Name != "Nintendo 64 ROM pack: Best N64 games." {
		t.Errorf("name = %q", link.Name)
	}
}
