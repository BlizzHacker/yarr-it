package main

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

// The universal provider surface: what is configured, whether it is well, and
// what it is currently doing.
//
// The rule that shapes both handlers below: one sick provider must never take
// the response with it. A settings screen that cannot paint because an
// instance is unreachable is a screen that cannot be used to fix the
// unreachable instance, and an Activity view that fails whole because Lidarr
// timed out hides the five downloads that are fine.

// How long any single provider gets before the aggregate gives up on it.
// Deliberately short: these endpoints paint UI, and a slow backend must cost a
// row, not the page.
const providerProbeTimeout = 6 * time.Second

type providerView struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Domains      []string `json:"domains"`
	Roles        []string `json:"roles"`
	Capabilities []string `json:"capabilities"`
	Health       Health   `json:"health"`
}

// handleProviders reports every configured provider and its health.
//
// Health is probed concurrently and each probe is bounded. Serially, ten
// providers behind a dead VPN would take ten timeouts to paint one page.
func (s *server) handleProviders(w http.ResponseWriter, r *http.Request) {
	if s.providers == nil {
		writeJSON(w, 200, map[string]any{"providers": []providerView{}, "domains": []string{}})
		return
	}

	all := s.providers.All()
	views := make([]providerView, len(all))

	var wg sync.WaitGroup
	for i, p := range all {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			// Recover per provider. A panic in one adapter's health check must
			// not take down the endpoint that reports on all the others.
			defer func() {
				if rec := recover(); rec != nil {
					views[i].Health = Health{
						State:  HealthDegraded,
						Detail: "this provider's health check failed unexpectedly",
					}
				}
			}()
			ctx, cancel := context.WithTimeout(r.Context(), providerProbeTimeout)
			defer cancel()
			views[i] = providerView{
				ID:           p.ID(),
				Name:         p.Name(),
				Domains:      p.Domains(),
				Roles:        p.Roles(),
				Capabilities: p.Capabilities(),
				Health:       p.Health(ctx),
			}
		}(i, p)
	}
	wg.Wait()

	sort.Slice(views, func(a, b int) bool { return views[a].ID < views[b].ID })

	writeJSON(w, 200, map[string]any{
		"providers": views,
		// Which domains are actually answerable here, so a client shows only
		// the tabs that will return something rather than empty sections for
		// services the user does not run.
		"domains": s.providers.Domains(),
	})
}

type activityView struct {
	Items []ActivityItem `json:"items"`
	// Failed names the providers that could not be reached, so a short list is
	// visibly short rather than quietly wrong. Without this, "nothing is
	// downloading" and "we could not ask" look identical.
	Failed []string `json:"failed,omitempty"`
}

// handleActivity merges what every provider says is in flight into one view --
// downloading, searching, importing, verifying, across every domain.
//
// The user is taken but not filtered on: a download queue belongs to the
// household, not to whoever asked for it, and hiding a neighbour's download
// would make the same title look un-requested and get requested twice. It still
// needs a session, because what a household is downloading is nobody else's
// business.
func (s *server) handleActivity(w http.ResponseWriter, r *http.Request, _ string) {
	if s.providers == nil {
		writeJSON(w, 200, activityView{Items: []ActivityItem{}})
		return
	}

	var (
		mu     sync.Mutex
		items  []ActivityItem
		failed []string
		wg     sync.WaitGroup
	)

	for _, p := range s.providers.All() {
		ap, ok := p.(ActivityProvider)
		if !ok {
			continue // Asked, not assumed. Komga has no activity to report.
		}
		wg.Add(1)
		go func(p Provider, ap ActivityProvider) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					mu.Lock()
					failed = append(failed, p.ID())
					mu.Unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(r.Context(), providerProbeTimeout)
			defer cancel()

			got, err := ap.Activity(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, p.ID())
				return
			}
			items = append(items, got...)
		}(p, ap)
	}
	wg.Wait()

	// Newest-looking work first is the wrong sort here: someone opening this
	// screen wants the thing furthest along, because that is what is about to
	// become watchable.
	sort.SliceStable(items, func(a, b int) bool { return items[a].Progress > items[b].Progress })
	sort.Strings(failed)

	if items == nil {
		items = []ActivityItem{}
	}
	writeJSON(w, 200, activityView{Items: items, Failed: failed})
}
