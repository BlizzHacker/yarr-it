package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// GG Requestz (github.com/XTREEMMAK/ggrequestz) -- the game-request front end
// that hands approved requests to ROMarr.
//
// What its API genuinely allows, stated rather than papered over:
//
//   - an API key (Admin -> API Keys, scope requests:read) can READ the
//     requests made under that key's user, via GET /api/request;
//   - approving and declining live behind a browser session
//     (/admin/api/requests/update reads a cookie, never a key).
//
// So this provider is a RequestLister and deliberately not a RequestDecider.
// The unified queue shows GG Requestz requests with their real status, and the
// decide endpoint answers 409 with "decide it in GG Requestz" rather than
// pretending. When GG Requestz accepts admin-scoped keys on its update route,
// Decide is a dozen lines here.
//
// Game requests made from Yarr.It itself go straight to ROMarr (the game
// Requester), the same way film requests go straight to Radarr or Cinephage.

type ggrProvider struct {
	id, name string
	base     string
	key      string
	hc       *http.Client
}

func newGGRProvider(base, key string, hc *http.Client) *ggrProvider {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &ggrProvider{
		id: "ggrequestz", name: "GG Requestz",
		base: strings.TrimRight(base, "/"), key: key, hc: hc,
	}
}

func (p *ggrProvider) ID() string             { return p.id }
func (p *ggrProvider) Name() string           { return p.name }
func (p *ggrProvider) Domains() []string      { return []string{"game"} }
func (p *ggrProvider) Roles() []string        { return []string{"requests"} }
func (p *ggrProvider) Capabilities() []string { return []string{"health", "requestQueue"} }

// get performs one call. GG Requestz rejects X-Api-Key; it wants a bearer.
func (p *ggrProvider) get(ctx context.Context, path string, q url.Values, auth bool, out any) error {
	u := p.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+p.key)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return &arrTransportError{Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &arrHTTPError{Status: resp.StatusCode, Method: "GET", Path: path,
			Body: strings.TrimSpace(string(snippet))}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func (p *ggrProvider) Health(ctx context.Context) Health {
	if p.base == "" {
		return Health{State: HealthNotConfigured, Detail: "no address set for GG Requestz"}
	}
	if p.key == "" {
		return Health{State: HealthNotConfigured,
			Detail: "no API key set for GG Requestz — create one under Admin → API Keys with the requests:read scope"}
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := p.get(ctx, "/api/version", nil, false, &v); err != nil {
		return queueErrorHealth(err, "GG Requestz", p.base, "check it and that it has the requests:read scope")
	}
	if v.Version == "" {
		return Health{State: HealthIncompatible,
			Detail: "this address answers, but not like GG Requestz — check the URL"}
	}
	var probe ggrRequestList
	if err := p.get(ctx, "/api/request", url.Values{"limit": {"1"}}, true, &probe); err != nil {
		h := queueErrorHealth(err, "GG Requestz", p.base, "check it and that it has the requests:read scope")
		h.Version = v.Version
		return h
	}
	return Health{State: HealthOK, Version: v.Version,
		Detail: "lists requests made under this key's user; approve or decline them in GG Requestz"}
}

type ggrRequestList struct {
	Success  bool         `json:"success"`
	Requests []ggrRequest `json:"requests"`
}

type ggrRequest struct {
	ID          int             `json:"id"`
	RequestType string          `json:"request_type"`
	Title       string          `json:"title"`
	Priority    string          `json:"priority"`
	Status      string          `json:"status"`
	Description string          `json:"description"`
	Platforms   json.RawMessage `json:"platforms"`
	CreatedAt   string          `json:"created_at"`
}

// GG Requestz's own words: pending, approved, rejected, fulfilled, cancelled.
func ggrStatus(s string) RequestStatus {
	switch strings.ToLower(s) {
	case "pending":
		return RequestPending
	case "approved":
		return RequestApproved
	case "rejected":
		return RequestDeclined
	case "fulfilled":
		return RequestAvailable
	case "cancelled":
		return RequestCancelled
	}
	return RequestPending
}

func ggrFilter(s RequestStatus) (string, bool) {
	switch s {
	case "":
		return "", true
	case RequestPending:
		return "pending", true
	case RequestApproved:
		return "approved", true
	case RequestDeclined:
		return "rejected", true
	case RequestAvailable:
		return "fulfilled", true
	case RequestCancelled:
		return "cancelled", true
	}
	// processing and failed have no GG Requestz equivalent.
	return "", false
}

func (p *ggrProvider) Requests(ctx context.Context, status RequestStatus, limit int) ([]QueuedRequest, error) {
	filter, ok := ggrFilter(status)
	if !ok {
		return []QueuedRequest{}, nil
	}
	if limit <= 0 {
		limit = requestsDefaultLimit
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if filter != "" {
		q.Set("status", filter)
	}
	var list ggrRequestList
	if err := p.get(ctx, "/api/request", q, true, &list); err != nil {
		return nil, err
	}
	out := make([]QueuedRequest, 0, len(list.Requests))
	for _, r := range list.Requests {
		backend := strconv.Itoa(r.ID)
		var detail []string
		if plats := ggrPlatforms(r.Platforms); plats != "" {
			detail = append(detail, plats)
		}
		if r.RequestType != "" && r.RequestType != "game" {
			detail = append(detail, r.RequestType)
		}
		out = append(out, QueuedRequest{
			ID:             requestID(p.id, backend),
			ProviderID:     p.id,
			ProviderItemID: backend,
			Domain:         "game",
			Type:           "game",
			Title:          r.Title,
			Status:         ggrStatus(r.Status),
			RequestedAt:    r.CreatedAt,
			Detail:         strings.Join(detail, " · "),
		})
	}
	return out, nil
}

// ggrPlatforms accepts either a JSON array of strings or of {name} objects --
// GG Requestz has stored both over its history.
func ggrPlatforms(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var names []string
	if json.Unmarshal(raw, &names) == nil {
		return strings.Join(names, ", ")
	}
	var objs []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &objs) == nil {
		// A failed decode into []string can leave zero values behind.
		names = names[:0]
		for _, o := range objs {
			if o.Name != "" {
				names = append(names, o.Name)
			}
		}
		return strings.Join(names, ", ")
	}
	return ""
}

func ggrProvidersFromEnv() []Provider {
	u := strings.TrimSpace(firstNonEmptyGame(os.Getenv("GGREQUESTZ_URL"), os.Getenv("GG_REQUESTZ_URL")))
	if u == "" {
		return nil
	}
	key := firstNonEmptyGame(os.Getenv("GGREQUESTZ_API_KEY"), os.Getenv("GG_REQUESTZ_API_KEY"))
	return []Provider{newGGRProvider(u, key, nil)}
}
