package main

// Resolving an archive.org item into pictures a television can display.
//
// A card for a comic or a photo set carries an archive.org *details page* URL --
// HTML, not media. The browser copes because its resolver follows the item into
// its files. A Roku does not: startPlayback hands whatever URL it is given to a
// Video node, so a JPEG or a PDF reaches a video decoder and the channel reports
// that it cannot play the file.
//
// Two shapes have to be handled, and they are genuinely different:
//
//   * an image item is a bag of JPEGs, each its own picture
//   * a comic is one PDF, which no Roku can render -- but archive.org derives
//     page images for `texts` items and serves them as ordinary JPEGs at
//     /download/<id>/page/n<N>_w<width>.jpg
//
// So both end up as a list of image URLs, which is the one thing every client
// can already draw.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// Width of derived comic pages. 1280 is a TV-sized page without making a
	// slow connection wait for a print-resolution scan.
	comicPageWidth = 1280

	// How many pages to offer for a comic. The derive has no page count in its
	// metadata, and probing every page costs a request each, so this is a
	// ceiling -- the client stops when a page fails to load.
	comicPageLimit = 60

	metadataTimeout = 12 * time.Second
)

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

type archiveMetadata struct {
	Metadata struct {
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		MediaType  string `json:"mediatype"`
	} `json:"metadata"`
	Files []struct {
		Name   string `json:"name"`
		Format string `json:"format"`
		Size   string `json:"size"`
	} `json:"files"`
}

// archiveIdentifier pulls the item id out of any archive.org URL shape.
func archiveIdentifier(raw string) string {
	raw = strings.TrimSpace(raw)
	for _, marker := range []string{"archive.org/details/", "archive.org/download/", "ia:"} {
		if i := strings.Index(raw, marker); i >= 0 {
			id := raw[i+len(marker):]
			if j := strings.IndexAny(id, "/?#"); j >= 0 {
				id = id[:j]
			}
			return id
		}
	}
	// A bare identifier is also accepted, so a caller need not know the URL form.
	if !strings.Contains(raw, "/") && raw != "" {
		return raw
	}
	return ""
}

func fetchArchiveMetadata(ctx context.Context, id string) (*archiveMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://archive.org/metadata/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("archive.org metadata %s: %d", id, resp.StatusCode)
	}

	var m archiveMetadata
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// imageFiles returns the item's own pictures, largest-looking first is not
// worth guessing at -- name order is what a photo set actually means.
func imageFiles(m *archiveMetadata, id string) []string {
	var names []string
	for _, f := range m.Files {
		lower := strings.ToLower(f.Name)
		// Thumbnails are the item's own furniture, not its content.
		if strings.HasPrefix(f.Name, "__ia_thumb") || strings.Contains(lower, "_thumb.") {
			continue
		}
		if i := strings.LastIndex(lower, "."); i >= 0 && imageExts[lower[i:]] {
			names = append(names, f.Name)
		}
	}
	sort.Strings(names)

	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "https://archive.org/download/"+id+"/"+urlPathEscape(n))
	}
	return out
}

// comicPages returns derived page images. archive.org generates these for any
// `texts` item, which is what makes a PDF-only comic viewable at all.
func comicPages(id string, limit int) []string {
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, fmt.Sprintf(
			"https://archive.org/download/%s/page/n%d_w%d.jpg",
			id, i, comicPageWidth))
	}
	return out
}

// urlPathEscape encodes a filename for a URL path without escaping the slashes
// that separate directories inside an item.
func urlPathEscape(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(
			strings.ReplaceAll(p, "%", "%25"), " ", "%20")
		parts[i] = strings.NewReplacer("?", "%3F", "#", "%23", "&", "%26").Replace(parts[i])
	}
	return strings.Join(parts, "/")
}

// handlePages turns a card into something displayable.
func (s *server) handlePages(w http.ResponseWriter, r *http.Request) {
	id := archiveIdentifier(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "id is required"})
		return
	}

	limit := comicPageLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	kind := r.URL.Query().Get("kind")

	// A comic needs no metadata call: the derived page URLs are predictable,
	// and asking first would add a round trip to every open.
	if kind == "comic" {
		writeJSON(w, 200, map[string]any{
			"id": id, "kind": "comic", "pages": comicPages(id, limit),
			// The client stops at the first page that fails, because the
			// derive does not publish a page count.
			"probe": true,
		})
		return
	}

	m, err := fetchArchiveMetadata(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "could not read the item"})
		return
	}

	pages := imageFiles(m, id)
	if len(pages) == 0 && m.Metadata.MediaType == "texts" {
		// An item catalogued as an image but actually scanned as a book.
		writeJSON(w, 200, map[string]any{
			"id": id, "kind": "comic", "pages": comicPages(id, limit), "probe": true})
		return
	}

	writeJSON(w, 200, map[string]any{
		"id": id, "kind": "image", "title": m.Metadata.Title,
		"pages": pages, "probe": false,
	})
}
