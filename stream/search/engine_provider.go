package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The engine through the provider contract, so every screen that already
// talks to Radarr or Sonarr -- search, request, library state, activity --
// talks to the engine the same way, and request routing treats it like any
// other acquirer: a service the owner brought is preferred, unless the owner
// tells the engine to go first.

type engineProvider struct{ e *engine }

func (p *engineProvider) ID() string        { return "yarrit" }
func (p *engineProvider) Name() string      { return "Yarr.It engine" }
func (p *engineProvider) Domains() []string { return []string{"video"} }
func (p *engineProvider) Roles() []string   { return []string{"discovery", "acquisition", "library"} }
func (p *engineProvider) Capabilities() []string {
	return []string{"health", "search", "library", "libraryStatus", "request", "activity"}
}
func (p *engineProvider) Engine() string { return "yarrit" }

// Preferred: the owner asked the built-in engine to take requests even
// though a Radarr or Sonarr is configured.
func (p *engineProvider) Preferred() bool { return p.e.store.settings().PreferEngine }

func (p *engineProvider) Health(ctx context.Context) Health { return p.e.health(ctx, p.e.store.view()) }

// health judges a state copy the caller already holds.
func (e *engine) health(ctx context.Context, st engineState) Health {
	var ix, cl int
	for _, i := range st.Indexers {
		if i.Enabled {
			ix++
		}
	}
	for _, c := range st.Clients {
		if c.Enabled {
			cl++
		}
	}
	switch {
	case ix == 0 && cl == 0:
		return Health{State: HealthNotConfigured, Detail: "add an indexer and a download client under Settings → Stack"}
	case ix == 0:
		return Health{State: HealthNotConfigured, Detail: "add an indexer under Settings → Stack"}
	case cl == 0:
		return Health{State: HealthNotConfigured, Detail: "add a download client under Settings → Stack"}
	}
	if qc, _, err := e.client(st); err == nil {
		if _, err := qc.version(ctx); err != nil {
			return Health{State: HealthDegraded, Detail: "download client: " + err.Error()}
		}
	}
	if st.Settings.MovieRoot == "" || st.Settings.SeriesRoot == "" {
		return Health{State: HealthDegraded, Detail: "grabs work; set the Films and Series folders so finished downloads are filed"}
	}
	return Health{State: HealthOK, Version: "native"}
}

func (p *engineProvider) toItem(it engineItem, downloading map[string]bool) MediaItem {
	return MediaItem{
		CanonicalID: it.ID, Domain: "video", Type: it.Kind, Title: it.Title, Year: it.Year,
		Overview: it.Overview, Artwork: it.Poster, ProviderID: p.ID(), ProviderItemID: it.IMDb,
		State: itemState(it, downloading),
	}
}

func (p *engineProvider) Search(ctx context.Context, query, domain string) ([]MediaItem, error) {
	hits, err := p.e.meta.Search(ctx, query, "")
	if err != nil {
		return nil, err
	}
	st := p.e.store.view()
	dl := downloadingIDs(st.Grabs)
	out := make([]MediaItem, 0, len(hits))
	for _, h := range hits {
		m := MediaItem{
			CanonicalID: engineItemID(h.Kind, h.IMDb), Domain: "video", Type: h.Kind,
			Title: h.Title, Year: h.Year, Overview: h.Overview, Artwork: h.Poster,
			ProviderID: p.ID(), ProviderItemID: h.IMDb, State: StateMissing,
		}
		if it := st.item(m.CanonicalID); it != nil {
			m.State = itemState(*it, dl)
		}
		out = append(out, m)
	}
	return out, nil
}

func (p *engineProvider) Library(ctx context.Context, domain string) ([]MediaItem, error) {
	st := p.e.store.view()
	out := make([]MediaItem, 0, len(st.Items))
	dl := downloadingIDs(st.Grabs)
	for _, it := range st.Items {
		out = append(out, p.toItem(it, dl))
	}
	return out, nil
}

func (p *engineProvider) LibraryStatus(ctx context.Context, canonicalID string) (LibraryState, error) {
	st := p.e.store.view()
	if it := st.item(canonicalID); it != nil {
		return itemState(*it, downloadingIDs(st.Grabs)), nil
	}
	return StateMissing, nil
}

func (p *engineProvider) CanRequest(ref arrRef) bool { return p.e.canResolve(ref) }

// Request adds the title and starts a search in the background: an indexer
// round can take half a minute, and the person who clicked must not wait.
func (p *engineProvider) Request(ctx context.Context, item MediaItem, opts RequestOptions) (RequestResult, error) {
	ref, err := parseArrRef(item.CanonicalID)
	if err != nil {
		return RequestResult{}, err
	}
	it, created, err := p.e.Add(ctx, ref, "")
	if err != nil {
		return RequestResult{}, err
	}
	if !opts.Monitor && created {
		_ = p.e.SetMonitored(it.ID, false)
	}
	go func(id string) {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, _ = p.e.AutoSearch(sctx, id)
	}(it.ID)
	verb := "added"
	if !created {
		verb = "already in the library"
	}
	return RequestResult{Accepted: true, Detail: fmt.Sprintf("%q %s; searching your indexers", it.Title, verb)}, nil
}

func (p *engineProvider) Activity(ctx context.Context) ([]ActivityItem, error) {
	st := p.e.store.view()
	var out []ActivityItem
	for _, g := range st.Grabs {
		if g.State != "downloading" {
			continue
		}
		title := g.Release
		if it := st.item(g.ItemID); it != nil {
			title = it.Title
			if g.Season > 0 {
				title += " S" + pad2(g.Season)
				if g.Episode > 0 {
					title += "E" + pad2(g.Episode)
				}
			}
		}
		out = append(out, ActivityItem{
			CanonicalID: g.ItemID, Title: title, Domain: "video", ProviderID: p.ID(),
			Stage: "downloading", Progress: g.Progress, Detail: g.Detail,
		})
	}
	return out, nil
}

// --- native requests -------------------------------------------------------

// Anyone signed in can ask; the owner decides; an approval goes to whichever
// acquirer routing picks -- the owner's Radarr, ROMarr, or the engine. This is
// Yarr.It's own Seerr, living beside the real one rather than instead of it.

type nativeRequests struct {
	e     *engine
	route func(ctx context.Context, canonicalID string) (RequestResult, string, error)
}

func (n *nativeRequests) ID() string        { return "yarrit-requests" }
func (n *nativeRequests) Name() string      { return "Yarr.It requests" }
func (n *nativeRequests) Domains() []string { return []string{"video", "game", "music"} }
func (n *nativeRequests) Roles() []string   { return []string{"requests"} }
func (n *nativeRequests) Capabilities() []string {
	return []string{"health", "requestQueue", "requestDecide"}
}
func (n *nativeRequests) Health(context.Context) Health {
	return Health{State: HealthOK, Version: "native"}
}

func (n *nativeRequests) Ask(ctx context.Context, canonicalID, title string, year int, poster, by string) (engineRequest, error) {
	ref, err := parseArrRef(canonicalID)
	if err != nil {
		return engineRequest{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 300 {
		return engineRequest{}, errors.New("a request needs a title")
	}
	var out engineRequest
	err = n.e.store.update(func(st *engineState) error {
		for _, r := range st.Requests {
			if r.CanonicalID == canonicalID && r.Status == "pending" {
				return fmt.Errorf("%q is already waiting for approval", r.Title)
			}
		}
		out = engineRequest{
			ID: st.NextReq, CanonicalID: canonicalID, Title: title, Year: year,
			Kind: ref.Type, Poster: poster, By: by, At: n.e.stamp(), Status: "pending",
		}
		st.NextReq++
		st.Requests = append(st.Requests, out)
		st.logEvent(n.e.now(), canonicalID, "requested", title+" by "+by)
		return nil
	})
	return out, err
}

func (n *nativeRequests) Requests(ctx context.Context, status RequestStatus, limit int) ([]QueuedRequest, error) {
	st := n.e.store.view()
	out := []QueuedRequest{}
	for i := len(st.Requests) - 1; i >= 0 && len(out) < limit; i-- {
		r := st.Requests[i]
		s := RequestStatus(r.Status)
		if status != "" && s != status {
			continue
		}
		id := strconv.Itoa(r.ID)
		out = append(out, QueuedRequest{
			ID: requestID(n.ID(), id), ProviderID: n.ID(), ProviderItemID: id,
			Domain: canonicalDomain(r.Kind), Type: r.Kind, Title: r.Title, Year: r.Year,
			Artwork: r.Poster, Status: s, RequestedBy: r.By, RequestedAt: r.At,
			Detail: r.Detail, CanonicalID: r.CanonicalID, Decidable: s == RequestPending,
		})
	}
	return out, nil
}

func (n *nativeRequests) Decide(ctx context.Context, backendID string, approve bool) error {
	id, err := strconv.Atoi(backendID)
	if err != nil {
		return &arrHTTPError{Status: 400, Method: "POST", Path: "requests", Body: "request ids are numbers"}
	}
	var req engineRequest
	err = n.e.store.update(func(st *engineState) error {
		for i := range st.Requests {
			if st.Requests[i].ID == id {
				if st.Requests[i].Status != "pending" {
					return &arrHTTPError{Status: 409, Method: "POST", Path: "requests", Body: "already decided"}
				}
				req = st.Requests[i]
				return nil
			}
		}
		return &arrHTTPError{Status: 404, Method: "POST", Path: "requests", Body: "no such request"}
	})
	if err != nil {
		return err
	}
	status, detail := "declined", ""
	if approve {
		res, providerID, err := n.route(ctx, req.CanonicalID)
		if err != nil {
			return err // stays pending; the owner can retry or decline
		}
		status, detail = "approved", strings.TrimSpace(providerID+": "+res.Detail)
	}
	return n.e.store.update(func(st *engineState) error {
		for i := range st.Requests {
			if st.Requests[i].ID == id {
				st.Requests[i].Status, st.Requests[i].Detail = status, detail
			}
		}
		st.logEvent(n.e.now(), req.CanonicalID, "decided", req.Title+": "+status)
		return nil
	})
}
