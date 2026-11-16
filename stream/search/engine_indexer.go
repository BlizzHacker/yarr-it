package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Indexers, natively. Any Torznab or Newznab feed works -- a tracker's own
// Torznab endpoint, Jackett, or one of Prowlarr's per-indexer feeds -- so a
// standalone install needs no Prowlarr. Prowlarr's aggregate search is still
// accepted as an indexer kind, for an owner who already runs one.

type engineQuery struct {
	Kind    string // movie | series
	Text    string
	IMDb    string // tt-prefixed
	Season  int
	Episode int
}

const engineIndexerBodyCap = 16 << 20

func (ix engineIndexer) search(ctx context.Context, hc *http.Client, q engineQuery) ([]engineRelease, error) {
	switch ix.Kind {
	case "prowlarr":
		return ix.searchProwlarr(ctx, hc, q)
	case "torznab", "newznab", "":
		rs, err := ix.searchTorznab(ctx, hc, q, true)
		// Not every feed implements the typed searches; a plain text search is
		// the one thing every Torznab endpoint answers.
		if (err != nil || len(rs) == 0) && q.Text != "" {
			if rs2, err2 := ix.searchTorznab(ctx, hc, q, false); err2 == nil {
				return rs2, nil
			}
		}
		return rs, err
	}
	return nil, fmt.Errorf("unknown indexer kind %q", ix.Kind)
}

func (ix engineIndexer) torznabURL(q engineQuery, typed bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(ix.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("indexer address must be an http(s) URL")
	}
	v := u.Query()
	if ix.APIKey != "" {
		v.Set("apikey", ix.APIKey)
	}
	text := q.Text
	switch {
	case typed && q.Kind == "movie":
		v.Set("t", "movie")
		if q.IMDb != "" {
			v.Set("imdbid", strings.TrimPrefix(q.IMDb, "tt"))
			text = ""
		}
	case typed && q.Kind == "series":
		v.Set("t", "tvsearch")
		if q.IMDb != "" {
			v.Set("imdbid", strings.TrimPrefix(q.IMDb, "tt"))
		}
		if q.Season > 0 {
			v.Set("season", strconv.Itoa(q.Season))
		}
		if q.Episode > 0 {
			v.Set("ep", strconv.Itoa(q.Episode))
		}
	default:
		v.Set("t", "search")
		if q.Kind == "series" && q.Season > 0 {
			text += " S" + pad2(q.Season)
			if q.Episode > 0 {
				text += "E" + pad2(q.Episode)
			}
		}
	}
	if text != "" {
		v.Set("q", strings.TrimSpace(text))
	}
	v.Set("limit", "100")
	u.RawQuery = v.Encode()
	return u.String(), nil
}

type torznabFeed struct {
	Channel struct {
		Items []struct {
			Title     string `xml:"title"`
			GUID      string `xml:"guid"`
			Link      string `xml:"link"`
			PubDate   string `xml:"pubDate"`
			Size      int64  `xml:"size"`
			Enclosure struct {
				URL    string `xml:"url,attr"`
				Length int64  `xml:"length,attr"`
			} `xml:"enclosure"`
			Attrs []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"attr"`
		} `xml:"item"`
	} `xml:"channel"`
}

type torznabError struct {
	Code        string `xml:"code,attr"`
	Description string `xml:"description,attr"`
}

func (ix engineIndexer) searchTorznab(ctx context.Context, hc *http.Client, q engineQuery, typed bool) ([]engineRelease, error) {
	u, err := ix.torznabURL(q, typed)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: could not connect", ix.Name)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, engineIndexerBodyCap))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("%s rejected the API key", ix.Name)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered HTTP %d", ix.Name, resp.StatusCode)
	}
	var te torznabError
	if xml.Unmarshal(body, &te) == nil && te.Code != "" {
		return nil, fmt.Errorf("%s: %s", ix.Name, te.Description)
	}
	var feed torznabFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("%s did not answer with a Torznab feed", ix.Name)
	}
	out := make([]engineRelease, 0, len(feed.Channel.Items))
	for _, it := range feed.Channel.Items {
		r := engineRelease{Title: it.Title, Indexer: ix.Name, Size: it.Size, Published: it.PubDate}
		if r.Size == 0 {
			r.Size = it.Enclosure.Length
		}
		link := it.Enclosure.URL
		if link == "" {
			link = it.Link
		}
		for _, a := range it.Attrs {
			switch strings.ToLower(a.Name) {
			case "seeders":
				r.Seeders, _ = strconv.Atoi(a.Value)
			case "size":
				if n, err := strconv.ParseInt(a.Value, 10, 64); err == nil && r.Size == 0 {
					r.Size = n
				}
			case "infohash":
				r.InfoHash = strings.ToLower(a.Value)
			case "magneturl":
				r.Magnet = a.Value
			}
		}
		if strings.HasPrefix(link, "magnet:") {
			r.Magnet = link
		} else if link != "" {
			r.DownloadURL = link
		}
		if r.InfoHash == "" && r.Magnet != "" {
			r.InfoHash = magnetHash(r.Magnet)
		}
		out = append(out, r)
	}
	return out, nil
}

func (ix engineIndexer) searchProwlarr(ctx context.Context, hc *http.Client, q engineQuery) ([]engineRelease, error) {
	base := strings.TrimRight(strings.TrimSpace(ix.URL), "/")
	text := q.Text
	if q.Kind == "series" && q.Season > 0 {
		text += " S" + pad2(q.Season)
		if q.Episode > 0 {
			text += "E" + pad2(q.Episode)
		}
	}
	cat := "2000"
	if q.Kind == "series" {
		cat = "5000"
	}
	u := base + "/api/v1/search?query=" + url.QueryEscape(strings.TrimSpace(text)) +
		"&categories=" + cat + "&limit=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", ix.APIKey)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: could not connect", ix.Name)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("%s rejected the API key", ix.Name)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered HTTP %d", ix.Name, resp.StatusCode)
	}
	var raw []prowlarrResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, engineIndexerBodyCap)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s did not answer like Prowlarr", ix.Name)
	}
	out := make([]engineRelease, 0, len(raw))
	for _, r := range raw {
		if r.Protocol != "" && r.Protocol != "torrent" {
			continue // usenet needs a usenet client; none is wired yet
		}
		er := engineRelease{
			Title: r.Title, Indexer: r.Indexer, Size: r.Size, Seeders: r.Seeders,
			Magnet: r.MagnetURL, DownloadURL: r.DownloadURL, InfoHash: strings.ToLower(r.InfoHash),
			Published: r.PublishDate,
		}
		if er.InfoHash == "" && er.Magnet != "" {
			er.InfoHash = magnetHash(er.Magnet)
		}
		out = append(out, er)
	}
	return out, nil
}

// magnetHash pulls the btih out of a magnet link, lowercased hex or base32 as
// given. Used to find a grab in the client later.
func magnetHash(m string) string {
	u, err := url.Parse(m)
	if err != nil {
		return ""
	}
	for _, xt := range u.Query()["xt"] {
		if h, ok := strings.CutPrefix(strings.ToLower(xt), "urn:btih:"); ok {
			return h
		}
	}
	return ""
}

// test proves an indexer answers and accepts its key, for the settings
// screen. Deliberately not a search: Prowlarr fans a search out to every
// tracker it knows, which can outlast any reasonable wait on a button.
func (ix engineIndexer) test(ctx context.Context, hc *http.Client) error {
	var u string
	switch ix.Kind {
	case "prowlarr":
		u = strings.TrimRight(strings.TrimSpace(ix.URL), "/") + "/api/v1/system/status"
	default:
		pu, err := url.Parse(strings.TrimSpace(ix.URL))
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			return fmt.Errorf("indexer address must be an http(s) URL")
		}
		v := pu.Query()
		v.Set("t", "caps")
		if ix.APIKey != "" {
			v.Set("apikey", ix.APIKey)
		}
		pu.RawQuery = v.Encode()
		u = pu.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if ix.Kind == "prowlarr" {
		req.Header.Set("X-Api-Key", ix.APIKey)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s: could not connect", ix.Name)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return fmt.Errorf("%s rejected the API key", ix.Name)
	case resp.StatusCode != 200:
		return fmt.Errorf("%s answered HTTP %d", ix.Name, resp.StatusCode)
	}
	var te torznabError
	if xml.Unmarshal(body, &te) == nil && te.Code != "" {
		return fmt.Errorf("%s: %s", ix.Name, te.Description)
	}
	if ix.Kind == "prowlarr" && !strings.Contains(string(body), "\"version\"") {
		return fmt.Errorf("%s did not answer like Prowlarr", ix.Name)
	}
	if ix.Kind != "prowlarr" && !strings.Contains(string(body), "<caps") {
		return fmt.Errorf("%s did not answer like a Torznab/Newznab feed", ix.Name)
	}
	return nil
}
