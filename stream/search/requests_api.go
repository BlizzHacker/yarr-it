package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Requests other people made, in one queue.
//
// Seerr holds the household's film and series requests; GG Requestz holds its
// game requests. Each has its own approval screen, and running both means
// checking two places to answer one question -- "what is everyone waiting
// for?". This is the one place, and approving here is approving there: the
// request stays in the app it was made in, with its own history and
// notifications, rather than being copied into a third store that drifts.
//
// Two optional interfaces, not one, because the backends genuinely differ:
// GG Requestz lets an API key read requests but keeps approval behind a
// browser session, so it lists and does not decide. A single interface would
// force it to stub Decide with an error nobody reads -- the exact thing the
// provider contract exists to prevent.

// RequestStatus is the shared vocabulary. Backends map onto it; clients never
// see "rejected" from one app and "declined" from another.
type RequestStatus string

const (
	RequestPending    RequestStatus = "pending"
	RequestApproved   RequestStatus = "approved"
	RequestProcessing RequestStatus = "processing"
	RequestAvailable  RequestStatus = "available"
	RequestDeclined   RequestStatus = "declined"
	RequestFailed     RequestStatus = "failed"
	RequestCancelled  RequestStatus = "cancelled"
)

func validRequestStatus(s string) bool {
	switch RequestStatus(s) {
	case RequestPending, RequestApproved, RequestProcessing, RequestAvailable,
		RequestDeclined, RequestFailed, RequestCancelled:
		return true
	}
	return false
}

// QueuedRequest is one person's ask, wherever it was made.
type QueuedRequest struct {
	// ID is "<providerId>:<backend id>" so a decision routes back to the app
	// that holds the request without the client knowing which app that is.
	ID             string        `json:"id"`
	ProviderID     string        `json:"providerId"`
	ProviderItemID string        `json:"providerItemId"`
	Domain         string        `json:"domain"`
	Type           string        `json:"type,omitempty"`
	Title          string        `json:"title"`
	Year           int           `json:"year,omitempty"`
	Artwork        string        `json:"artwork,omitempty"`
	Status         RequestStatus `json:"status"`
	RequestedBy    string        `json:"requestedBy,omitempty"`
	RequestedAt    string        `json:"requestedAt,omitempty"`
	Detail         string        `json:"detail,omitempty"`
	// CanonicalID, when the backend knows one, lets a client match the request
	// to a search card and show that it is already asked for.
	CanonicalID string `json:"canonicalId,omitempty"`
	// Decidable is per item, not per provider: Seerr will not approve a request
	// that is already approved, and the button should not be offered.
	Decidable bool `json:"decidable"`
}

// RequestLister reads a backend's request queue. status "" means all.
type RequestLister interface {
	Requests(ctx context.Context, status RequestStatus, limit int) ([]QueuedRequest, error)
}

// RequestDecider approves or declines one request in place.
type RequestDecider interface {
	Decide(ctx context.Context, backendID string, approve bool) error
}

const (
	requestsDefaultLimit = 50
	requestsMaxLimit     = 200
)

type requestsAPI struct {
	reg *Registry
}

type requestsView struct {
	Items []QueuedRequest `json:"items"`
	// Failed names the backends that could not be asked, so a short queue is
	// visibly short rather than quietly wrong.
	Failed []string `json:"failed,omitempty"`
}

// handleList merges every backend's queue. GET /api/requests?status=pending
func (a *requestsAPI) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "GET only"})
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "all" {
		status = ""
	}
	if status != "" && !validRequestStatus(status) {
		writeJSON(w, 400, map[string]string{"error": "unknown status " + status})
		return
	}
	limit := requestsDefaultLimit
	if n := atoiDefault(r.URL.Query().Get("limit"), 0); n > 0 {
		limit = min(n, requestsMaxLimit)
	}
	writeJSON(w, 200, a.collect(r.Context(), RequestStatus(status), limit))
}

func (a *requestsAPI) collect(ctx context.Context, status RequestStatus, limit int) requestsView {
	var (
		mu     sync.Mutex
		items  = []QueuedRequest{}
		failed []string
		wg     sync.WaitGroup
	)
	for _, p := range a.reg.All() {
		rl, ok := p.(RequestLister)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(p Provider, rl RequestLister) {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					mu.Lock()
					failed = append(failed, p.ID())
					mu.Unlock()
				}
			}()
			cctx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
			defer cancel()
			got, err := rl.Requests(cctx, status, limit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, p.ID())
				return
			}
			items = append(items, got...)
		}(p, rl)
	}
	wg.Wait()

	// Pending first -- that is what a person opening this screen came to act
	// on -- then newest first within each status.
	sort.SliceStable(items, func(i, j int) bool {
		pi, pj := items[i].Status == RequestPending, items[j].Status == RequestPending
		if pi != pj {
			return pi
		}
		return items[i].RequestedAt > items[j].RequestedAt
	})
	if len(items) > limit {
		items = items[:limit]
	}
	sort.Strings(failed)
	return requestsView{Items: items, Failed: failed}
}

type decideBody struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// handleDecide approves or declines. POST /api/requests/decide
// {"id":"seerr:42","action":"approve"}
func (a *requestsAPI) handleDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	var body decideBody
	if err := decodeBody(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body is not JSON"})
		return
	}
	var approve bool
	switch body.Action {
	case "approve":
		approve = true
	case "decline":
	default:
		writeJSON(w, 400, map[string]string{"error": "action must be approve or decline"})
		return
	}
	providerID, backendID, ok := splitRequestID(body.ID)
	if !ok {
		writeJSON(w, 400, map[string]string{"error": "id must be <providerId>:<requestId>"})
		return
	}
	target := a.reg.Get(providerID)
	if target == nil {
		writeJSON(w, 404, map[string]string{"error": "no request backend called " + providerID})
		return
	}
	d, ok := target.(RequestDecider)
	if !ok {
		writeJSON(w, 409, map[string]string{
			"error": target.Name() + " does not let Yarr.It approve or decline requests — decide it in " + target.Name(),
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := d.Decide(ctx, backendID, approve); err != nil {
		status := 502
		var he *arrHTTPError
		if errors.As(err, &he) && (he.Status == 404 || he.Status == 409 || he.Status == 400) {
			status = he.Status
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": body.ID, "action": body.Action})
}

// splitRequestID cuts at the LAST colon: provider ids may carry a dash-joined
// instance name but never a colon, and backend ids are plain integers.
func splitRequestID(id string) (provider, backend string, ok bool) {
	i := strings.LastIndex(id, ":")
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}

func requestID(providerID, backendID string) string { return providerID + ":" + backendID }

// queueErrorHealth maps a request-queue backend's error onto the six health
// states. Seerr and GG Requestz differ only in the words.
func queueErrorHealth(err error, app, base, keyHint string) Health {
	var he *arrHTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return Health{State: HealthAuthFailed, Detail: app + " rejected the API key — " + keyHint}
		case http.StatusNotFound:
			return Health{State: HealthIncompatible,
				Detail: "this address answers, but has no " + app + " API — check the URL"}
		}
		return Health{State: HealthDegraded, Detail: app + " answered with HTTP " + strconv.Itoa(he.Status)}
	}
	return Health{State: HealthUnreachable, Detail: "could not reach " + app + " at " + base}
}
