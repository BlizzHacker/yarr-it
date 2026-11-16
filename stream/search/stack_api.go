package main

import (
	"net/http"
	"os"
	"strings"
)

// Unified, or bring your own -- per domain.
//
// Yarr.It runs two ways and any mix of them. Unified: one install, with the
// built-in engine answering for films and series and a bundled ROMarr for
// games. Bring your own: the same Yarr.It on top of the Radarr, Sonarr,
// Lidarr, ROMarr, Seerr and GG Requestz a household already runs. Mixed is
// normal -- an existing Radarr with the engine covering series, say.
//
// /api/stack answers "who does what here" without probing anything, so it is
// instant; /api/providers carries the health. Two calls, because a topology
// that waited on the slowest backend would be a topology nobody could load
// while that backend was the problem.

type stackMember struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Engine names one of Yarr.It's own engines ("yarrit", or a bundled
	// "romarr"); empty means a service the user brought.
	Engine string `json:"engine,omitempty"`
}

type stackDomain struct {
	Mode string `json:"mode"`
	// Role -> members in routing order. For acquisition the first member is
	// where an unqualified request goes.
	Roles map[string][]stackMember `json:"roles"`
}

type stackView struct {
	Mode    string                 `json:"mode"`
	Domains map[string]stackDomain `json:"domains"`
}

const (
	stackUnified = "unified"
	stackBYO     = "bring-your-own"
	stackMixed   = "mixed"
	stackEmpty   = "empty"
)

func stackMode(bundled, brought int) string {
	switch {
	case bundled > 0 && brought > 0:
		return stackMixed
	case bundled > 0:
		return stackUnified
	case brought > 0:
		return stackBYO
	}
	return stackEmpty
}

func buildStack(reg *Registry) stackView {
	type tally struct{ bundled, brought int }
	domains := map[string]stackDomain{}
	perDomain := map[string]*tally{}

	for _, p := range reg.All() {
		engine := ""
		if be, ok := p.(bundledEngine); ok {
			engine = be.Engine()
		}
		m := stackMember{ID: p.ID(), Name: p.Name(), Engine: engine}
		for _, d := range p.Domains() {
			sd, ok := domains[d]
			if !ok {
				sd = stackDomain{Roles: map[string][]stackMember{}}
				perDomain[d] = &tally{}
			}
			for _, role := range p.Roles() {
				sd.Roles[role] = append(sd.Roles[role], m)
			}
			domains[d] = sd
			// Mode is decided by who acquires: that is the half of the stack
			// "unified" replaces.
			if contains(p.Roles(), "acquisition") {
				if engine != "" {
					perDomain[d].bundled++
				} else {
					perDomain[d].brought++
				}
			}
		}
	}
	var total tally
	for d, sd := range domains {
		t := perDomain[d]
		sd.Mode = stackMode(t.bundled, t.brought)
		domains[d] = sd
		total.bundled += t.bundled
		total.brought += t.brought
	}
	return stackView{Mode: stackMode(total.bundled, total.brought), Domains: domains}
}

func (s *server) handleStack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "GET only"})
		return
	}
	if s.providers == nil {
		writeJSON(w, 200, stackView{Mode: stackEmpty, Domains: map[string]stackDomain{}})
		return
	}
	writeJSON(w, 200, buildStack(s.providers))
}

// --- request routing -------------------------------------------------------

// requestScoper says whether a Requester can obtain one specific thing.
//
// Without it, /api/arr/request handed a request to the first provider in the
// domain: every TV request went to Radarr when Radarr was registered first,
// and failed. With Cinephage, Radarr and Sonarr all claiming "video", picking
// by domain alone stops being merely wrong and starts being a coin toss.
type requestScoper interface {
	CanRequest(ref arrRef) bool
}

func (p *radarrProvider) CanRequest(ref arrRef) bool {
	return ref.Scheme == "tmdb" && ref.Type == "movie"
}

func (p *sonarrProvider) CanRequest(ref arrRef) bool {
	if ref.Scheme != "tvdb" {
		return false
	}
	switch ref.Type {
	case "series", "anime", "season", "episode":
		return true
	}
	return false
}

func (p *lidarrProvider) CanRequest(ref arrRef) bool { return ref.Scheme == "mbid" }

// romarrProvider is a bundled engine when the unified compose installed it.
func (p *romarrProvider) Engine() string {
	if isBundled("romarr") {
		return "romarr"
	}
	return ""
}

// requestTargets lists, in routing order, the providers that can obtain ref.
// Services the user brought go before Yarr.It's own engines, so an existing
// Radarr keeps winning over the built-in engine -- unless the owner turns on
// "prefer the engine", or names a provider on the request.
func requestTargets(all []Provider, ref arrRef) []Provider {
	want := canonicalDomain(ref.Type)
	var brought, bundled []Provider
	for _, p := range all {
		if _, ok := p.(Requester); !ok {
			continue
		}
		if !containsDomain(p.Domains(), want) {
			continue
		}
		if rs, ok := p.(requestScoper); ok && !rs.CanRequest(ref) {
			continue
		}
		be, isEngine := p.(bundledEngine)
		isEngine = isEngine && be.Engine() != ""
		if pref, ok := p.(enginePreferred); ok && isEngine && pref.Preferred() {
			// The owner asked the engine to go first.
			brought = append([]Provider{p}, brought...)
			continue
		}
		if isEngine {
			bundled = append(bundled, p)
		} else {
			brought = append(brought, p)
		}
	}
	return append(brought, bundled...)
}

// Get returns the provider with this id, or nil.
func (r *Registry) Get(id string) Provider {
	for _, p := range r.All() {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// isBundled reports an engine the unified compose installed (YARRIT_BUNDLED).
func isBundled(engine string) bool {
	for _, e := range strings.Split(os.Getenv("YARRIT_BUNDLED"), ",") {
		if strings.EqualFold(strings.TrimSpace(e), engine) {
			return true
		}
	}
	return false
}

// bundledEngine is implemented by providers Yarr.It runs itself -- the native
// engine, and a ROMarr the unified compose installed -- as opposed to a
// service the owner brought.
type bundledEngine interface {
	Engine() string
}

// enginePreferred lets the owner put a built-in engine ahead of the services
// they brought, for requests that both could take.
type enginePreferred interface {
	Preferred() bool
}
