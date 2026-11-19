package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// /api/engine/* -- configuring and driving the built-in engine.
//
// Owner only, except two routes a household member needs to make a request:
// looking a title up (catalogue data, no household state) and asking for it.
// Secrets go in and never come back out: every read replaces an API key or
// password with "keySet": true.

type engineAPI struct {
	e    *engine
	reqs *nativeRequests
}

type indexerView struct {
	engineIndexer
	APIKey string `json:"apiKey,omitempty"`
	KeySet bool   `json:"keySet"`
}

type clientView struct {
	engineClient
	Password string `json:"password,omitempty"`
	PassSet  bool   `json:"passwordSet"`
}

func redactIndexers(in []engineIndexer) []indexerView {
	out := make([]indexerView, 0, len(in))
	for _, ix := range in {
		out = append(out, indexerView{engineIndexer: ix, KeySet: ix.APIKey != ""})
	}
	return out
}

func redactClients(in []engineClient) []clientView {
	out := make([]clientView, 0, len(in))
	for _, c := range in {
		out = append(out, clientView{engineClient: c, PassSet: c.Password != ""})
	}
	return out
}

func indexerID(x *engineIndexer) *string { return &x.ID }
func clientID(x *engineClient) *string   { return &x.ID }

func newEngineID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func validHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (a *engineAPI) handleState(w http.ResponseWriter, r *http.Request) {
	st := a.e.store.view()
	h := a.e.health(r.Context(), st)
	writeJSON(w, 200, map[string]any{
		"health": h, "settings": st.Settings,
		"indexers": redactIndexers(st.Indexers), "clients": redactClients(st.Clients),
		"counts": map[string]int{"items": len(st.Items), "grabs": len(st.Grabs), "requests": len(st.Requests)},
	})
}

func (a *engineAPI) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSON(w, 405, map[string]string{"error": "PUT only"})
		return
	}
	var in engineSettings
	if err := decodeBody(r, &in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
		return
	}
	if len(in.Profile.Qualities) == 0 {
		writeJSON(w, 400, map[string]string{"error": "the profile needs at least one quality"})
		return
	}
	for _, q := range in.Profile.Qualities {
		if qualityRank(q) == 0 {
			writeJSON(w, 400, map[string]string{"error": "unknown quality " + q})
			return
		}
	}
	if in.SearchIntervalHours < 1 {
		in.SearchIntervalHours = 12
	}
	err := a.e.store.update(func(st *engineState) error { st.Settings = in; return nil })
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, in)
}

// handleIndexers: POST upserts (a blank key keeps the stored one), DELETE ?id=.
func (a *engineAPI) handleIndexers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var in engineIndexer
		if err := decodeBody(r, &in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Kind == "" {
			in.Kind = "torznab"
		}
		if in.Kind != "torznab" && in.Kind != "newznab" && in.Kind != "prowlarr" {
			writeJSON(w, 400, map[string]string{"error": "kind must be torznab, newznab or prowlarr"})
			return
		}
		if !validHTTPURL(in.URL) || in.Name == "" {
			writeJSON(w, 400, map[string]string{"error": "an indexer needs a name and an http(s) address"})
			return
		}
		err := a.e.store.update(func(st *engineState) error {
			st.Indexers, in = upsertByID(st.Indexers, in, indexerID, func(old engineIndexer, in *engineIndexer) {
				if in.APIKey == "" {
					in.APIKey = old.APIKey
				}
			})
			return nil
		})
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, redactIndexers([]engineIndexer{in})[0])
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if err := a.e.store.update(func(st *engineState) error {
			st.Indexers = removeByID(st.Indexers, id, indexerID)
			return nil
		}); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeJSON(w, 405, map[string]string{"error": "POST or DELETE"})
	}
}

func (a *engineAPI) handleClients(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var in engineClient
		if err := decodeBody(r, &in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
			return
		}
		if in.Kind == "" {
			in.Kind = "qbittorrent"
		}
		if in.Kind != "qbittorrent" {
			writeJSON(w, 400, map[string]string{"error": "qbittorrent is the supported client so far"})
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" {
			in.Name = "qBittorrent"
		}
		if !validHTTPURL(in.URL) {
			writeJSON(w, 400, map[string]string{"error": "the client needs an http(s) address"})
			return
		}
		err := a.e.store.update(func(st *engineState) error {
			st.Clients, in = upsertByID(st.Clients, in, clientID, func(old engineClient, in *engineClient) {
				if in.Password == "" {
					in.Password = old.Password
				}
			})
			return nil
		})
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, redactClients([]engineClient{in})[0])
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if err := a.e.store.update(func(st *engineState) error {
			st.Clients = removeByID(st.Clients, id, clientID)
			return nil
		}); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeJSON(w, 405, map[string]string{"error": "POST or DELETE"})
	}
}

// handleTest: {"indexer":"id"} or {"client":"id"}.
func (a *engineAPI) handleTest(w http.ResponseWriter, r *http.Request) {
	var in struct{ Indexer, Client string }
	if err := decodeBody(r, &in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	st := a.e.store.view()
	for _, ix := range st.Indexers {
		if ix.ID == in.Indexer {
			if err := ix.test(ctx, a.e.hc); err != nil {
				writeJSON(w, 200, map[string]any{"ok": false, "detail": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "detail": ix.Name + " answered and accepted its key"})
			return
		}
	}
	for _, c := range st.Clients {
		if c.ID == in.Client {
			v, err := newQbitClient(c, a.e.hc).version(ctx)
			if err != nil {
				writeJSON(w, 200, map[string]any{"ok": false, "detail": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "detail": "qBittorrent " + v})
			return
		}
	}
	writeJSON(w, 404, map[string]string{"error": "no such indexer or client"})
}

type libraryRow struct {
	engineItem
	State    LibraryState    `json:"state"`
	Have     int             `json:"have,omitempty"`
	Total    int             `json:"total,omitempty"`
	Episodes []engineEpisode `json:"episodes,omitempty"`
}

func (a *engineAPI) handleLibrary(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st := a.e.store.view()
		rows := make([]libraryRow, 0, len(st.Items))
		dl := downloadingIDs(st.Grabs)
		for _, it := range st.Items {
			row := libraryRow{engineItem: it, State: itemState(it, dl), Total: len(it.Episodes)}
			for _, ep := range it.Episodes {
				if ep.HasFile {
					row.Have++
				}
			}
			row.Episodes = nil // the list view does not need every episode
			rows = append(rows, row)
		}
		writeJSON(w, 200, map[string]any{"items": rows})
	case http.MethodPost:
		var in struct {
			CanonicalID string `json:"canonicalId"`
			Search      bool   `json:"search"`
		}
		if err := decodeBody(r, &in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
			return
		}
		ref, err := parseArrRef(in.CanonicalID)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		it, created, err := a.e.Add(ctx, ref, "")
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		if in.Search {
			go func() {
				sctx, c2 := context.WithTimeout(context.Background(), 3*time.Minute)
				defer c2()
				_, _ = a.e.AutoSearch(sctx, it.ID)
			}()
		}
		writeJSON(w, 200, map[string]any{"item": it, "created": created})
	case http.MethodPatch:
		var in struct {
			ID        string `json:"id"`
			Monitored bool   `json:"monitored"`
		}
		if err := decodeBody(r, &in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
			return
		}
		if err := a.e.SetMonitored(in.ID, in.Monitored); err != nil {
			writeJSON(w, 404, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodDelete:
		if err := a.e.Remove(r.URL.Query().Get("id")); err != nil {
			writeJSON(w, 404, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeJSON(w, 405, map[string]string{"error": "GET, POST, PATCH or DELETE"})
	}
}

// handleSearch is interactive search: every candidate, with scores and the
// reasons any were rejected.
func (a *engineAPI) handleSearch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string `json:"id"`
		Season  int    `json:"season"`
		Episode int    `json:"episode"`
		Auto    bool   `json:"auto"`
	}
	if err := decodeBody(r, &in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if in.Auto {
		n, err := a.e.AutoSearch(ctx, in.ID)
		resp := map[string]any{"grabbed": n}
		if err != nil {
			resp["detail"] = err.Error()
		}
		writeJSON(w, 200, resp)
		return
	}
	res, err := a.e.Candidates(ctx, in.ID, in.Season, in.Episode)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if len(res.Releases) > 100 {
		res.Releases = res.Releases[:100]
	}
	writeJSON(w, 200, res)
}

func (a *engineAPI) handleGrab(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string        `json:"id"`
		Release scoredRelease `json:"release"`
	}
	if err := decodeBody(r, &in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
		return
	}
	if in.Release.Magnet != "" && !strings.HasPrefix(in.Release.Magnet, "magnet:") {
		writeJSON(w, 400, map[string]string{"error": "not a magnet link"})
		return
	}
	if in.Release.DownloadURL != "" && !validHTTPURL(in.Release.DownloadURL) {
		writeJSON(w, 400, map[string]string{"error": "download link must be http(s)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.e.Grab(ctx, in.ID, in.Release); err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *engineAPI) handleQueue(w http.ResponseWriter, r *http.Request) {
	st := a.e.store.view()
	grabs := st.Grabs
	if len(grabs) > 200 {
		grabs = grabs[len(grabs)-200:]
	}
	hist := st.History
	if len(hist) > 200 {
		hist = hist[len(hist)-200:]
	}
	writeJSON(w, 200, map[string]any{"grabs": grabs, "history": hist})
}

// handleLookup: catalogue search, for anyone signed in. No household state.
func (a *engineAPI) handleLookup(w http.ResponseWriter, r *http.Request, _ string) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 || len(q) > 200 {
		writeJSON(w, 400, map[string]string{"error": "search needs 2–200 characters"})
		return
	}
	kind := r.URL.Query().Get("kind")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	hits, err := a.e.meta.Search(ctx, q, kind)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	type hit struct {
		metaHit
		CanonicalID string `json:"canonicalId"`
	}
	out := make([]hit, 0, len(hits))
	for _, h := range hits {
		out = append(out, hit{metaHit: h, CanonicalID: engineItemID(h.Kind, h.IMDb)})
	}
	writeJSON(w, 200, map[string]any{"results": out})
}

// handleAsk: anyone signed in asks for a title; the owner decides later.
func (a *engineAPI) handleAsk(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	var in struct {
		CanonicalID string `json:"canonicalId"`
		Title       string `json:"title"`
		Year        int    `json:"year"`
		Poster      string `json:"poster"`
	}
	if err := decodeBody(r, &in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
		return
	}
	if in.Poster != "" && !validHTTPURL(in.Poster) {
		in.Poster = ""
	}
	req, err := a.reqs.Ask(r.Context(), in.CanonicalID, in.Title, in.Year, in.Poster, user)
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, req)
}

// routeRequest is how an approved native request reaches an acquirer.
func routeRequest(reg *Registry) func(ctx context.Context, canonicalID string) (RequestResult, string, error) {
	return func(ctx context.Context, canonicalID string) (RequestResult, string, error) {
		ref, err := parseArrRef(canonicalID)
		if err != nil {
			return RequestResult{}, "", err
		}
		ts := requestTargets(reg.All(), ref)
		if len(ts) == 0 {
			return RequestResult{}, "", errors.New("nothing configured here can obtain " + canonicalID)
		}
		rq := ts[0].(Requester)
		res, err := rq.Request(ctx, MediaItem{CanonicalID: canonicalID, Domain: canonicalDomain(ref.Type), Type: ref.Type},
			RequestOptions{Season: ref.Season, Episode: ref.Episode, Monitor: true})
		return res, ts[0].ID(), err
	}
}

func registerEngineRoutes(mux *http.ServeMux, auth *authConfig, reg *Registry, e *engine) {
	reqs := &nativeRequests{e: e, route: routeRequest(reg)}
	if err := reg.Add(&engineProvider{e}); err != nil {
		panic(err)
	}
	if err := reg.Add(reqs); err != nil {
		panic(err)
	}
	a := &engineAPI{e: e, reqs: reqs}
	mux.HandleFunc("/api/engine", auth.requireOwner(a.handleState))
	mux.HandleFunc("/api/engine/settings", auth.requireOwner(a.handleSettings))
	mux.HandleFunc("/api/engine/indexers", auth.requireOwner(a.handleIndexers))
	mux.HandleFunc("/api/engine/clients", auth.requireOwner(a.handleClients))
	mux.HandleFunc("/api/engine/test", auth.requireOwner(a.handleTest))
	mux.HandleFunc("/api/engine/library", auth.requireOwner(a.handleLibrary))
	mux.HandleFunc("/api/engine/search", auth.requireOwner(a.handleSearch))
	mux.HandleFunc("/api/engine/grab", auth.requireOwner(a.handleGrab))
	mux.HandleFunc("/api/engine/queue", auth.requireOwner(a.handleQueue))
	mux.HandleFunc("/api/engine/lookup", auth.requireUser(a.handleLookup))
	mux.HandleFunc("/api/requests/ask", auth.requireUser(a.handleAsk))
}
