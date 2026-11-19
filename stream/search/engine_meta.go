package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Where the engine learns what a film or series is.
//
// Default: Cinemeta, Stremio's public catalogue, keyed by IMDb id and needing
// no account or API key. That is the difference between a standalone install
// that works on first boot and one that starts with "go and register for a
// TMDB key". With TMDB_API_KEY set, TMDB ids (what Radarr, Seerr and most
// lists speak) are translated to IMDb ids so those requests reach the engine.

type metaHit struct {
	IMDb     string `json:"imdb"`
	Kind     string `json:"kind"` // movie | series
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	Poster   string `json:"poster,omitempty"`
	Overview string `json:"overview,omitempty"`
}

type metaDetail struct {
	metaHit
	Episodes []engineEpisode
}

type engineMeta interface {
	Search(ctx context.Context, q, kind string) ([]metaHit, error)
	Lookup(ctx context.Context, imdb, kind string) (metaDetail, error)
}

var reIMDb = regexp.MustCompile(`^tt\d{5,10}$`)

type cinemeta struct {
	base string
	hc   *http.Client
}

func newCinemeta(hc *http.Client) *cinemeta {
	base := strings.TrimRight(os.Getenv("ENGINE_CINEMETA_URL"), "/")
	if base == "" {
		base = "https://v3-cinemeta.strem.io"
	}
	return &cinemeta{base: base, hc: hc}
}

type cinemetaMeta struct {
	ID          string `json:"id"`
	IMDb        string `json:"imdb_id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Year        any    `json:"year"`
	ReleaseInfo string `json:"releaseInfo"`
	Poster      string `json:"poster"`
	Description string `json:"description"`
	Videos      []struct {
		Season   int    `json:"season"`
		Episode  int    `json:"episode"`
		Number   int    `json:"number"`
		Name     string `json:"name"`
		Title    string `json:"title"`
		Released string `json:"released"`
	} `json:"videos"`
}

func (m cinemetaMeta) hit(kind string) metaHit {
	id := m.IMDb
	if id == "" {
		id = m.ID
	}
	return metaHit{IMDb: id, Kind: kind, Title: m.Name, Year: metaYear(m.Year, m.ReleaseInfo),
		Poster: m.Poster, Overview: m.Description}
}

func metaYear(y any, info string) int {
	switch v := y.(type) {
	case float64:
		return int(v)
	case string:
		info = v + " " + info
	}
	if m := reYear.FindString(info); m != "" {
		n, _ := strconv.Atoi(m)
		return n
	}
	return 0
}

func (c *cinemeta) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the catalogue")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("catalogue answered HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func cinemetaType(kind string) string {
	if kind == "series" {
		return "series"
	}
	return "movie"
}

func (c *cinemeta) Search(ctx context.Context, q, kind string) ([]metaHit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	var out []metaHit
	kinds := []string{"movie", "series"}
	if kind == "movie" || kind == "series" {
		kinds = []string{kind}
	}
	for _, k := range kinds {
		var res struct {
			Metas []cinemetaMeta `json:"metas"`
		}
		path := "/catalog/" + cinemetaType(k) + "/top/search=" + url.PathEscape(q) + ".json"
		if err := c.get(ctx, path, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Metas {
			if h := m.hit(k); reIMDb.MatchString(h.IMDb) {
				out = append(out, h)
			}
		}
	}
	return out, nil
}

func (c *cinemeta) Lookup(ctx context.Context, imdb, kind string) (metaDetail, error) {
	if !reIMDb.MatchString(imdb) {
		return metaDetail{}, fmt.Errorf("%q is not an IMDb id", imdb)
	}
	var res struct {
		Meta cinemetaMeta `json:"meta"`
	}
	if err := c.get(ctx, "/meta/"+cinemetaType(kind)+"/"+imdb+".json", &res); err != nil {
		return metaDetail{}, err
	}
	if res.Meta.Name == "" {
		return metaDetail{}, fmt.Errorf("the catalogue has no %s %s", kind, imdb)
	}
	d := metaDetail{metaHit: res.Meta.hit(kind)}
	d.IMDb = imdb
	if kind == "series" {
		for _, v := range res.Meta.Videos {
			ep := v.Episode
			if ep == 0 {
				ep = v.Number
			}
			if v.Season <= 0 || ep <= 0 {
				continue // specials stay out of the monitored set
			}
			name := v.Name
			if name == "" {
				name = v.Title
			}
			d.Episodes = append(d.Episodes, engineEpisode{Season: v.Season, Episode: ep, Title: name, Aired: v.Released})
		}
	}
	return d, nil
}

// tmdbToIMDb translates a TMDB or TVDB id through TMDB, when a key is set.
func tmdbToIMDb(ctx context.Context, t *tmdbClient, ref arrRef) (string, string, error) {
	if !t.enabled() {
		return "", "", fmt.Errorf("a TMDB key is needed to add by %s id", ref.Scheme)
	}
	key := url.QueryEscape(t.apiKey)
	switch {
	case ref.Scheme == "tmdb" && ref.Type == "movie":
		var r struct {
			IMDb string `json:"imdb_id"`
		}
		err := t.getJSON(ctx, "https://api.themoviedb.org/3/movie/"+url.PathEscape(ref.ID)+"/external_ids?api_key="+key, &r)
		return r.IMDb, "movie", err
	case ref.Scheme == "tmdb":
		var r struct {
			IMDb string `json:"imdb_id"`
		}
		err := t.getJSON(ctx, "https://api.themoviedb.org/3/tv/"+url.PathEscape(ref.ID)+"/external_ids?api_key="+key, &r)
		return r.IMDb, "series", err
	case ref.Scheme == "tvdb":
		var r struct {
			TV []struct {
				ID int `json:"id"`
			} `json:"tv_results"`
		}
		if err := t.getJSON(ctx, "https://api.themoviedb.org/3/find/"+url.PathEscape(ref.ID)+"?external_source=tvdb_id&api_key="+key, &r); err != nil {
			return "", "", err
		}
		if len(r.TV) == 0 {
			return "", "", fmt.Errorf("TMDB knows no series with TVDB id %s", ref.ID)
		}
		return tmdbToIMDb(ctx, t, arrRef{Scheme: "tmdb", Type: "series", ID: strconv.Itoa(r.TV[0].ID)})
	}
	return "", "", fmt.Errorf("cannot translate %s:%s", ref.Scheme, ref.Type)
}

// engineKindOf maps a canonical type to the engine's two kinds.
func engineKindOf(typ string) string {
	switch typ {
	case "movie", "film":
		return "movie"
	case "series", "anime", "season", "episode", "show":
		return "series"
	}
	return ""
}

func engineItemID(kind, imdb string) string { return "imdb:" + kind + ":" + imdb }
