package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
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

// episodeScoped: the request names one episode. Season 0 holds specials, so
// it is a real season; episode 0 means the whole title.
func episodeScoped(opts RequestOptions) bool { return opts.Episode > 0 && opts.Season >= 0 }

// Request adds the title and starts a search in the background: an indexer
// round can take half a minute, and the person who clicked must not wait.
func (p *engineProvider) Request(ctx context.Context, item MediaItem, opts RequestOptions) (RequestResult, error) {
	ref, err := parseArrRef(item.CanonicalID)
	if err != nil {
		return RequestResult{}, err
	}
	// An episode request fetches that episode only. Monitoring the whole
	// series would make the scheduler search every other episode too, so a
	// new title is created with the monitoring state it should have.
	scoped := episodeScoped(opts)
	it, created, err := p.e.AddWith(ctx, ref, "", opts.Monitor && !scoped)
	if err != nil {
		return RequestResult{}, err
	}
	if !created && opts.Monitor && !scoped && !it.Monitored {
		// As Radarr and Sonarr do: asking to monitor something already in
		// the library turns monitoring on, so a failed search is retried.
		if err := p.e.SetMonitored(it.ID, true); err != nil {
			return RequestResult{}, err
		}
	}
	go p.e.searchSoon(it.ID, opts.Season, opts.Episode, scoped)
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

	mu sync.Mutex
	// routing: approvals this process is routing now. A request stored as
	// "routing" that is not in here was left by a restart mid-approval.
	routing map[int]bool
	// unsaved: decisions whose final write failed, kept so the next decision
	// on that request saves them instead of calling the provider again.
	unsaved map[int]requestOutcome
}

type requestOutcome struct{ status, detail, canonicalID, title string }

func (n *nativeRequests) setRouting(id int, on bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.routing == nil {
		n.routing = map[int]bool{}
	}
	if on {
		n.routing[id] = true
	} else {
		delete(n.routing, id)
	}
}

func (n *nativeRequests) isRouting(id int) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.routing[id]
}

// finish writes a decision's outcome onto a request still marked routing,
// retrying briefly: a transient write failure must not strand the request.
//
// A flush (stale) outcome never overwrites a request this process is
// routing right now: that reservation belongs to a newer decision.
func (n *nativeRequests) finish(id int, o requestOutcome, stale bool) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		err = n.e.store.update(func(st *engineState) error {
			if stale && n.isRouting(id) {
				return errNoChange
			}
			for i := range st.Requests {
				if st.Requests[i].ID == id && st.Requests[i].Status == "routing" {
					st.Requests[i].Status, st.Requests[i].Detail = o.status, o.detail
					if o.status == "approved" {
						st.logEvent(n.e.now(), o.canonicalID, "decided", o.title+": approved")
					}
				}
			}
			return nil
		})
		if err == nil {
			return nil
		}
	}
	return err
}

// flushUnsaved saves an earlier outcome that could not be written. After
// it, the request is either approved (the provider is not asked again) or
// pending, and the new decision proceeds normally.
func (n *nativeRequests) flushUnsaved(id int) error {
	n.mu.Lock()
	o, ok := n.unsaved[id]
	delete(n.unsaved, id) // taken: a concurrent decision cannot flush it too
	n.mu.Unlock()
	if !ok {
		return nil
	}
	if err := n.finish(id, o, true); err != nil {
		n.keepUnsaved(id, o)
		return fmt.Errorf("an earlier decision on this request still cannot be saved: %w", err)
	}
	return nil
}

func (n *nativeRequests) keepUnsaved(id int, o requestOutcome) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.unsaved == nil {
		n.unsaved = map[int]requestOutcome{}
	}
	if _, newer := n.unsaved[id]; !newer {
		n.unsaved[id] = o
	}
}

// flushAll retries every held outcome; the request list calls it, so a held
// decision is saved as soon as the owner looks at the queue.
func (n *nativeRequests) flushAll() {
	n.mu.Lock()
	ids := make([]int, 0, len(n.unsaved))
	for id := range n.unsaved {
		ids = append(ids, id)
	}
	n.mu.Unlock()
	for _, id := range ids {
		_ = n.flushUnsaved(id)
	}
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
			if r.CanonicalID == canonicalID && (r.Status == "pending" || r.Status == "routing") {
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
	n.flushAll()
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
	if err := n.flushUnsaved(id); err != nil {
		return err
	}
	// Reserve the decision atomically: a decline commits here, and an
	// approval moves the request to "routing" before it is routed, so a
	// competing decision sees it already taken instead of overwriting it.
	var req engineRequest
	reserved := false
	err = n.e.store.update(func(st *engineState) error {
		for i := range st.Requests {
			r := &st.Requests[i]
			if r.ID != id {
				continue
			}
			if r.Status == "routing" && !n.isRouting(id) {
				r.Status = "pending" // left by a restart mid-approval: decide again
			}
			if r.Status != "pending" {
				return &arrHTTPError{Status: 409, Method: "POST", Path: "requests", Body: "already decided"}
			}
			req = *r
			if approve {
				r.Status, r.Detail = "routing", "being approved"
				n.setRouting(id, true)
				reserved = true
			} else {
				r.Status, r.Detail = "declined", ""
				st.logEvent(n.e.now(), r.CanonicalID, "decided", r.Title+": declined")
			}
			return nil
		}
		return &arrHTTPError{Status: 404, Method: "POST", Path: "requests", Body: "no such request"}
	})
	if reserved {
		defer n.setRouting(id, false)
	}
	if err != nil || !approve {
		return err
	}
	res, providerID, rerr := n.route(ctx, req.CanonicalID)
	if rerr == nil && !res.Accepted {
		// A provider that declines answers Accepted=false with no error.
		rerr = &arrHTTPError{Status: 409, Method: "POST", Path: "requests",
			Body: "not accepted: " + strings.TrimSpace(providerID+" "+res.Detail)}
	}
	status, detail := "approved", strings.TrimSpace(providerID+": "+res.Detail)
	if rerr != nil {
		// Back to pending, with the reason, so the owner can retry or decline.
		status, detail = "pending", rerr.Error()
	}
	o := requestOutcome{status: status, detail: detail, canonicalID: req.CanonicalID, title: req.Title}
	if uerr := n.finish(id, o, false); uerr != nil {
		n.keepUnsaved(id, o)
		uerr = fmt.Errorf("the decision could not be saved yet; it is saved on the next decision for this request: %w", uerr)
		return errors.Join(rerr, uerr)
	}
	return rerr
}
