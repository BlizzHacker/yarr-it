package main

// One archive.org item, opened as music.
//
// A concert is not a film, and a card that says "Grateful Dead Live at Barton
// Hall" with no way to see or play a track is a poster for a record shop. The
// Barton Hall item is one show, twenty tracks, two and a half hours; the thing
// somebody wants from it is "Morning Dew", and until this file existed there
// was no way to reach it -- the card's only target was the Archive's own
// details page, which is HTML, and the browser's archive resolver turned that
// into an iframe.
//
// So this is the music domain's equivalent of pages.go. That file exists
// because a Roku cannot render a PDF and a comic therefore has to arrive as a
// list of images; this one exists because no client can be handed a details
// page and be expected to find the fourth track on it. Both take an
// identifier and return the item as the list of things it actually contains.
//
// WHY IT IS A SEPARATE REQUEST AND NOT PART OF THE SEARCH
//
// The file list costs one metadata request per item and a search returns sixty
// of them. play_archive.go makes the same trade for the ROM inside a game item
// and states the reason plainly: doing sixty lookups per search would make
// every search slower for a link most people never click. So the card carries
// the identity and this is called once, for the one item somebody chose.
//
// WHAT IS HONOURED HERE
//
// `stream_only` -- the band's own "listen, do not take a copy", which a large
// part of the Live Music Archive carries including the Barton Hall show. It is
// read on the item and it decides ONE thing: whether a download link is
// published. It does not decide whether the track plays, and it must not, for
// the reason play_archive.go arrived at when it made the same mistake in the
// other direction: the Archive's own player fetches these bytes to play them,
// so refusing to play here honours nothing -- it just moves the same act onto
// their page. Playing is the play; publishing a file to save is the download.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// musicMetadataTimeout matches pages.go and play_archive.go: this endpoint
// paints a track list, so it must fail fast. A slow Archive costs one open,
// not a page.
const musicMetadataTimeout = 12 * time.Second

// musicFormats ranks the derivatives a browser will play, best first.
//
// MP3 first, always, and not because it is the best encode -- it is the worst
// of the three. It is the only one every target this service has agrees on: a
// browser, a Roku, a Tizen set and an Android box. Ogg Vorbis is absent from
// Safari and every iOS browser, and FLAC is absent from older WebKit and is ten
// times the bytes for material that was taped off a soundboard in 1977.
//
// The same reasoning linear_source_archive.go applies to H.264, and the same
// conclusion: pick the format that plays everywhere, not the one that measures
// best.
var musicFormats = map[string]int{
	"vbr mp3":     0,
	"128kbps mp3": 1,
	"64kbps mp3":  2,
	"mp3":         3,
	"ogg vorbis":  4,
	"24bit flac":  5,
	"flac":        6,
}

// musicMimes is what each format actually is, so a client is told rather than
// left to sniff an extension.
var musicMimes = map[string]string{
	"vbr mp3": "audio/mpeg", "128kbps mp3": "audio/mpeg",
	"64kbps mp3": "audio/mpeg", "mp3": "audio/mpeg",
	"ogg vorbis": "audio/ogg",
	"24bit flac": "audio/flac", "flac": "audio/flac",
}

// musicTrack is one playable recording.
type musicTrack struct {
	// Number is the track's place on the record or in the set, when the item
	// states one. Zero means it did not, which is common on a taped show and is
	// not worth inventing a number for -- the file order is then the set order.
	Number int    `json:"number,omitempty"`
	Title  string `json:"title"`
	// Artist is per track, because a netlabel compilation is a different artist
	// every track and putting the label's name on all fifteen would be wrong.
	// Empty when the file says nothing, in which case the item's artist stands.
	Artist string `json:"artist,omitempty"`
	Album  string `json:"album,omitempty"`
	// DurationSeconds is what the file itself declares, never the item's
	// `runtime`. linear_source_archive.go measured the difference and it is
	// real: the item said 6:00 for a file that is 363.16 seconds.
	DurationSeconds int `json:"durationSeconds,omitempty"`
	// URL is the file at archive.org. A media element is not subject to CORS,
	// so this plays directly from them and costs this relay nothing -- which is
	// the whole reason there is no bridge in this path, unlike a ROM.
	URL       string `json:"url"`
	MimeType  string `json:"mimeType"`
	Format    string `json:"format,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	// File is the name inside the item, carried so a client can address one
	// track by name rather than by index into a list that may grow.
	File string `json:"file"`
}

// musicItem is the whole answer.
type musicItem struct {
	ID string `json:"id"`
	// Domain and Type come from schema.json's music vocabulary. `album` is what
	// a set of tracks with one cover is called there; a concert is the same
	// object with a venue instead of a sleeve, so it is typed `album` and told
	// apart by Form, exactly as the card is.
	Domain string `json:"domain"`
	Type   string `json:"type"`
	Form   string `json:"form"`

	Title  string `json:"title"`
	Artist string `json:"artist,omitempty"`
	Album  string `json:"album,omitempty"`
	Year   int    `json:"year,omitempty"`
	Date   string `json:"date,omitempty"`
	Venue  string `json:"venue,omitempty"`
	Place  string `json:"place,omitempty"`

	Artwork string `json:"artwork,omitempty"`
	// Details is the Archive's own page. Always present, because it is the one
	// thing that is certainly there whatever this endpoint managed to work out,
	// and because a client that wants to offer "open at archive.org" should not
	// have to build the URL itself.
	Details  string `json:"details"`
	Licence  string `json:"licence,omitempty"`
	Language string `json:"language,omitempty"`
	Source   string `json:"source,omitempty"`

	// Downloadable is false for an item the Archive marks stream-only. It is
	// about what a client may OFFER, not about what plays: every track below
	// still carries a URL and still plays.
	Downloadable bool `json:"downloadable"`

	Tracks []musicTrack `json:"tracks"`
	// TotalSeconds is the sum of the tracks that declared a length. Present so
	// a client can say "2h 27m" without adding up a list it may have truncated.
	TotalSeconds int `json:"totalSeconds,omitempty"`

	// Reason is set only when there are no tracks, and says which of the two
	// things went wrong -- the item does not exist, or it exists and holds
	// nothing a browser can play. A client that cannot tell those apart shows
	// "loading" forever.
	Reason string `json:"reason,omitempty"`
}

// musicMetaFile is a file as the metadata API describes it.
//
// Track is a string and not an int on purpose: archive.org sends "1", "01" and
// "1/15" for the same field, and the last of those is what a netlabel release
// looks like. Declaring it an int fails the decode for the whole item, which
// would be reported as "the Archive did not answer" -- the trap play_archive.go
// paid for once with `emulator_ext` and again with `emulator`.
type musicMetaFile struct {
	Name    string     `json:"name"`
	Format  string     `json:"format"`
	Title   flexString `json:"title"`
	Track   flexString `json:"track"`
	Length  flexString `json:"length"`
	Album   flexString `json:"album"`
	Artist  flexString `json:"artist"`
	Creator flexString `json:"creator"`
	Size    string     `json:"size"`
}

type musicMetadata struct {
	Metadata struct {
		Identifier  string          `json:"identifier"`
		Title       flexString      `json:"title"`
		Creator     json.RawMessage `json:"creator"`
		MediaType   flexString      `json:"mediatype"`
		Date        flexString      `json:"date"`
		Year        json.RawMessage `json:"year"`
		Venue       flexString      `json:"venue"`
		Coverage    flexString      `json:"coverage"`
		LicenseURL  flexString      `json:"licenseurl"`
		Language    json.RawMessage `json:"language"`
		Collection  json.RawMessage `json:"collection"`
		AccessRestr json.RawMessage `json:"access-restricted-item"`
	} `json:"metadata"`
	Files []musicMetaFile `json:"files"`
	// Error is how the Archive reports an item whose metadata it cannot serve:
	// HTTP 200 carrying `{"error": ...}`. A status check alone reads that as
	// success and then finds no files. Same trap, same handling, as
	// linear_source_archive.go.
	Error string `json:"error"`
}

func (m *musicMetadata) streamOnly() bool {
	for _, c := range jsonStrings(m.Metadata.Collection) {
		if strings.EqualFold(strings.TrimSpace(c), "stream_only") {
			return true
		}
	}
	if len(m.Metadata.AccessRestr) > 0 {
		var s string
		if err := json.Unmarshal(m.Metadata.AccessRestr, &s); err == nil {
			return strings.EqualFold(strings.TrimSpace(s), "true")
		}
		var b bool
		if err := json.Unmarshal(m.Metadata.AccessRestr, &b); err == nil {
			return b
		}
	}
	return false
}

// musicReader turns an identifier into tracks.
//
// A struct rather than a bare function so a test can point it at a stub, which
// is the same reason playArchive is one. There is a real archive.org behind
// this and the committed suite must never call it.
type musicReader struct {
	client       *http.Client
	metadataBase string
	downloadBase string
	detailsBase  string
}

func newMusicReader(client *http.Client) *musicReader {
	if client == nil {
		client = &http.Client{Timeout: musicMetadataTimeout}
	}
	return &musicReader{
		client:       client,
		metadataBase: "https://archive.org/metadata/",
		downloadBase: "https://archive.org/download/",
		detailsBase:  "https://archive.org/details/",
	}
}

// musicTrackKey groups the derivatives of one recording.
//
// archive.org keeps several encodes of the same track side by side --
// `d1t01.mp3`, `d1t01.ogg`, `d1t01.flac` -- and they are one track, not three.
// The name without its extension is what they share, and it is what the Archive
// itself uses to relate them.
func musicTrackKey(name string) string {
	ext := path.Ext(name)
	return strings.ToLower(strings.TrimSuffix(name, ext))
}

// musicTitleFromName is the fallback when a file declares no title.
//
// It undoes the two things an uploader does to a filename and nothing more:
// the extension, and the leading track number. Anything cleverer would be
// guessing at somebody's naming scheme, and a filename is a worse title than a
// filename with a number on the front.
func musicTitleFromName(name string) string {
	base := path.Base(name)
	base = strings.TrimSuffix(base, path.Ext(base))
	base = strings.ReplaceAll(base, "_", " ")
	fields := strings.Fields(base)
	if len(fields) > 1 {
		if n := strings.Trim(fields[0], ".-"); n != "" && isAllDigits(n) {
			return strings.Join(fields[1:], " ")
		}
	}
	return strings.TrimSpace(base)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// musicTrackNumber reads archive.org's `track`, which is "1", "01" or "1/15".
// Anything it cannot read is zero, which means "the item did not say" and is a
// first-class answer -- a taped concert usually has no track numbers at all.
func musicTrackNumber(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if i := strings.IndexByte(raw, '/'); i > 0 {
		raw = raw[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// musicTracks picks one playable file per recording and puts them in order.
func (r *musicReader) musicTracks(id string, files []musicMetaFile) []musicTrack {
	type candidate struct {
		file musicMetaFile
		rank int
	}
	best := map[string]candidate{}
	order := []string{}

	for _, f := range files {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			continue
		}
		rank, ok := musicFormats[strings.ToLower(strings.TrimSpace(f.Format))]
		if !ok {
			continue
		}
		key := musicTrackKey(name)
		cur, seen := best[key]
		if !seen {
			order = append(order, key)
			best[key] = candidate{file: f, rank: rank}
			continue
		}
		if rank < cur.rank {
			// A better format for a track already seen. The metadata carried by
			// the derivatives differs -- the Ogg of a Grateful Dead track has no
			// title and the MP3 does -- so the two are merged rather than
			// replaced, and the winning FILE decides only which bytes play.
			f = musicMergeMeta(f, cur.file)
			best[key] = candidate{file: f, rank: rank}
			continue
		}
		best[key] = candidate{file: musicMergeMeta(cur.file, f), rank: cur.rank}
	}

	out := make([]musicTrack, 0, len(order))
	for _, key := range order {
		c := best[key]
		f := c.file
		format := strings.ToLower(strings.TrimSpace(f.Format))
		title := strings.TrimSpace(f.Title.String())
		if title == "" {
			title = musicTitleFromName(f.Name)
		}
		artist := strings.TrimSpace(f.Artist.String())
		if artist == "" {
			artist = strings.TrimSpace(f.Creator.String())
		}
		out = append(out, musicTrack{
			Number: musicTrackNumber(f.Track.String()),
			Title:  title,
			Artist: artist,
			Album:  strings.TrimSpace(f.Album.String()),
			// Reusing linear_source_archive.go's parser rather than writing a
			// second one. archive.org sends `363.16` on the older derivatives
			// and `6:03` on the newer ones -- for the SAME field on the SAME
			// item -- and that file already handles both and refuses to guess at
			// anything else. Two parsers for one field is how they disagree.
			DurationSeconds: archiveLinearDuration(f.Length.String()),
			URL:             r.downloadBase + url.PathEscape(id) + "/" + archiveFilePath(f.Name),
			MimeType:        musicMimes[format],
			Format:          strings.TrimSpace(f.Format),
			SizeBytes:       parseArchiveSize(f.Size),
			File:            f.Name,
		})
	}

	// Track number where the item states one, file order where it does not.
	// Stable, so a taped show whose files are `d1t01, d1t02, ...` keeps the
	// order the taper put them in -- which for a concert is the running order
	// and is the only thing that makes the item make sense.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Number != out[j].Number {
			// Zero sorts LAST, not first: an item where only some files are
			// numbered is one where the numbered ones are the record and the
			// rest are extras.
			if out[i].Number == 0 {
				return false
			}
			if out[j].Number == 0 {
				return true
			}
			return out[i].Number < out[j].Number
		}
		return false
	})
	return out
}

// musicMergeMeta fills the gaps in `keep` from `other`.
//
// Needed because archive.org's derivatives do not carry the same metadata: on
// the Barton Hall show the MP3 has `title: "Minglewood Blues"` and the Ogg of
// the same track has no title at all. Whichever file wins on format, the
// description of the track should be the best one available for it.
func musicMergeMeta(keep, other musicMetaFile) musicMetaFile {
	if keep.Title.String() == "" {
		keep.Title = other.Title
	}
	if keep.Track.String() == "" {
		keep.Track = other.Track
	}
	if keep.Album.String() == "" {
		keep.Album = other.Album
	}
	if keep.Artist.String() == "" {
		keep.Artist = other.Artist
	}
	if keep.Creator.String() == "" {
		keep.Creator = other.Creator
	}
	if keep.Length.String() == "" {
		keep.Length = other.Length
	}
	return keep
}

// Resolve reads one item and returns it as music.
func (r *musicReader) Resolve(ctx context.Context, id string) musicItem {
	out := musicItem{
		ID: id, Domain: domainMusic, Type: "album", Form: "release",
		Details: r.detailsBase + url.PathEscape(id),
		Tracks:  []musicTrack{},
	}

	meta, err := r.metadata(ctx, id)
	if err != nil {
		out.Reason = "the Internet Archive did not answer for this item, so what " +
			"is in it is unknown."
		return out
	}
	if meta.Metadata.Identifier == "" && meta.Metadata.Title.String() == "" && len(meta.Files) == 0 {
		// A 200 carrying `{}` is how archive.org answers for an identifier that
		// does not exist.
		out.Reason = "the Internet Archive has no item with this identifier."
		return out
	}

	out.Title = strings.TrimSpace(meta.Metadata.Title.String())
	if out.Title == "" {
		out.Title = id
	}
	out.Artist = musicArtist(jsonStrings(meta.Metadata.Creator))
	out.Form = musicForm(meta.Metadata.MediaType.String())
	out.Date = musicDate(meta.Metadata.Date.String())
	out.Year = archiveYear(meta.Metadata.Year)
	if out.Year == 0 && len(out.Date) >= 4 {
		out.Year = atoiYear(out.Date[:4])
	}
	out.Venue = strings.TrimSpace(meta.Metadata.Venue.String())
	out.Place = strings.TrimSpace(meta.Metadata.Coverage.String())
	out.Licence = strings.TrimSpace(meta.Metadata.LicenseURL.String())
	out.Language = strings.Join(jsonStrings(meta.Metadata.Language), ", ")
	out.Artwork = "https://archive.org/services/img/" + url.PathEscape(id)
	out.Source = musicSourceLabel(musicDoc{
		MediaType:  meta.Metadata.MediaType,
		Collection: jsonStrings(meta.Metadata.Collection),
	})
	// The Archive's "listen, do not take a copy". It decides whether a client
	// may offer a download and nothing else; see the file comment.
	out.Downloadable = !meta.streamOnly()

	out.Tracks = r.musicTracks(id, meta.Files)
	if len(out.Tracks) == 0 {
		out.Reason = "this item holds nothing a browser can play -- the Internet " +
			"Archive has derived no audio for it."
		return out
	}

	// The album name the FILES agree on, which is frequently better than the
	// item title: the Barton Hall item is titled "Grateful Dead Live at Barton
	// Hall, Cornell University on 1977-05-08" and its files all say
	// "1977-05-08 - Barton Hall, Cornell University", which is what belongs
	// above a track list.
	out.Album = musicAlbumOf(out.Tracks)
	for _, t := range out.Tracks {
		out.TotalSeconds += t.DurationSeconds
	}
	return out
}

// musicAlbumOf returns the album every track agrees on, or "".
//
// Unanimity rather than a majority: a compilation where two of fifteen tracks
// name a different album is a compilation, and picking the commonest would put
// one contributor's record name over somebody else's work.
func musicAlbumOf(tracks []musicTrack) string {
	album := ""
	for _, t := range tracks {
		if t.Album == "" {
			continue
		}
		if album == "" {
			album = t.Album
			continue
		}
		if !strings.EqualFold(album, t.Album) {
			return ""
		}
	}
	return album
}

func (r *musicReader) metadata(ctx context.Context, id string) (*musicMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, musicMetadataTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		r.metadataBase+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://yarrit.com)")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive.org metadata %s: status %d", id, resp.StatusCode)
	}
	var m musicMetadata
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		// Rate limiting and maintenance pages both arrive as 200 + HTML, so a
		// decode failure is an upstream problem rather than a missing item.
		return nil, fmt.Errorf("archive.org metadata %s: %w", id, err)
	}
	if strings.TrimSpace(m.Error) != "" {
		return nil, fmt.Errorf("archive.org metadata %s: %s", id, m.Error)
	}
	return &m, nil
}

// handleMusicItem serves one item as a track list.
func (r *musicReader) handleMusicItem(w http.ResponseWriter, req *http.Request) {
	// Any archive.org URL shape, or a bare identifier -- archiveIdentifier
	// already understands `details/`, `download/` and the `ia:` prefix the
	// cards use, so a client can hand back exactly what a card gave it.
	id := archiveIdentifier(req.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "id is required: an archive.org identifier, or any archive.org item URL",
		})
		return
	}
	writeJSON(w, http.StatusOK, r.Resolve(req.Context(), id))
}
