package main

// Who may see which channel.
//
// Every linear route is owner-only, and that rule is right: a channel built
// from a Jellyfin library or a Plex section is a timetable of somebody's
// personal files. Publishing the guide publishes what they own, when they
// watch it, and -- through `stream` -- the bytes themselves. None of that is
// catalogue data and none of it belongs to a stranger.
//
// But Nostalgia TV is public-domain film scheduled off archive.org. It touches
// no private library, and the entire point of it is that anyone can tune in
// without an account. Locking it behind the owner gate would leave the site
// with a Live TV section nobody but Wade can watch.
//
// So the gate is not per route. It is **per channel, decided by where the
// channel's programmes come from**, and it is default-deny:
//
//   - a channel whose source provider is registered as a public-domain
//     library is public;
//   - every other channel -- Jellyfin, Plex, a provider that was never
//     registered, a provider field left empty, a channel restored from a state
//     file written by a future version -- is owner-only.
//
// Nothing a channel says about itself can make it public. There is deliberately
// no `"public": true` field on LinearChannel, because provenance is a fact
// about the library behind it and not a preference somebody can type. The only
// way into the public set is for the process to have registered a library that
// declared itself public-domain, which only linear_source_archive.go does.
//
// The failure this shape is built to prevent is the quiet one: a private
// channel that becomes readable because a route was registered without its
// wrapper, or because a provider id collided. Both are handled below --
// registration has exactly one path and it is always gated, and a provider id
// claimed by both a public and a private library resolves to private.

import (
	"context"
	"net/http"
	"strings"
)

// linearOwnerFunc reports whether a request comes from the owner of this
// installation.
//
// The engine holds one of these rather than reaching for the auth config
// itself, so that what "owner" means stays in one place and this file cannot
// drift from it. A nil func means nobody is the owner, which is the correct
// reading when accounts are not configured: an instance that cannot identify
// anyone must not hand a stranger the private half of the schedule.
type linearOwnerFunc func(*http.Request) bool

// SetOwnerFunc installs the owner predicate. Until it is called, no request is
// the owner's and only public channels are visible -- default-deny, and the
// state a freshly constructed engine starts in.
func (e *LinearEngine) SetOwnerFunc(f linearOwnerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ownerFn = f
}

// AddPublicLibrary registers a library whose contents are public domain and
// whose channels a stranger may watch.
//
// Separate from AddLibrary on purpose. Making publicness an argument to one
// function -- AddLibrary(l, true) -- would put the safe and the unsafe case one
// keystroke apart, and the unsafe one is the silent direction to be wrong in.
// Two names mean a reviewer can grep for the short list of callers that publish
// anything at all.
func (e *LinearEngine) AddPublicLibrary(l LinearLibrary) {
	e.mu.Lock()
	if e.publicProviders == nil {
		e.publicProviders = map[string]bool{}
	}
	e.publicProviders[linearProviderKey(l.LinearProviderID())] = true
	e.libs = append(e.libs, l)
	e.mu.Unlock()
}

func linearProviderKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// channelIsPublic decides whether a stranger may see a channel at all.
//
// The order of the checks is the whole safety argument. Private is consulted
// first, so a provider id claimed by both a public and a private library --
// whether by accident or because somebody named a Jellyfin instance
// "archive-org" -- is private. Then public must be explicitly present. An id
// nobody registered, and an empty SourceProvider, fall off the end as private.
func (e *LinearEngine) channelIsPublic(ch *LinearChannel) bool {
	if ch == nil {
		return false
	}
	key := linearProviderKey(ch.SourceProvider)
	if key == "" {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.privateProviders[key] {
		return false
	}
	return e.publicProviders[key]
}

// PublicChannelIDs is what a stranger is allowed to know exists.
func (e *LinearEngine) PublicChannelIDs() []string {
	var out []string
	for _, c := range e.ChannelDefs() {
		def := c
		if e.channelIsPublic(&def) {
			out = append(out, def.ID)
		}
	}
	return out
}

// --- the viewer on a request ------------------------------------------------

type linearViewerKey struct{}

type linearViewer struct{ owner bool }

// withViewer stamps a request with who is asking. Every gated handler reads it
// back; a request that somehow arrives without one is treated as a stranger,
// which is the direction a missing stamp must fail in.
func (e *LinearEngine) withViewer(r *http.Request) *http.Request {
	e.mu.RLock()
	f := e.ownerFn
	e.mu.RUnlock()
	owner := f != nil && f(r)
	return r.WithContext(context.WithValue(r.Context(), linearViewerKey{}, linearViewer{owner: owner}))
}

func linearViewerOf(r *http.Request) linearViewer {
	if v, ok := r.Context().Value(linearViewerKey{}).(linearViewer); ok {
		return v
	}
	// No stamp means the request did not come through the gate. A stranger is
	// the only safe assumption.
	return linearViewer{}
}

// visibleChannel resolves a channel id for a request, or reports that -- as far
// as this caller is concerned -- there is no such channel.
//
// A private channel and a channel that does not exist are deliberately
// indistinguishable. Returning 403 for one and 404 for the other would turn the
// id space into an oracle: a stranger could enumerate ids and learn the names,
// count and numbering of channels built from a private library without ever
// being allowed to read one.
func (e *LinearEngine) visibleChannel(r *http.Request, id string) (*LinearChannel, bool) {
	ch, ok := e.Channel(id)
	if !ok {
		return nil, false
	}
	if linearViewerOf(r).owner {
		return ch, true
	}
	if e.channelIsPublic(ch) {
		return ch, true
	}
	return nil, false
}

// visibleIDs narrows a requested channel list to what this caller may read.
//
// The second return says whether there is anything left to ask the engine for,
// and it exists because an empty id list is not an empty answer anywhere else
// in this package -- Guide reads `len(ids) == 0` as "every enabled channel".
// Returning a bare empty slice would therefore turn "this stranger may see
// nothing" into "show this stranger everything", which is the exact leak the
// whole file is here to prevent. It is a boolean rather than a nil-versus-empty
// convention on purpose: nil and empty are one keystroke apart and read the
// same at a glance.
//
// An unparameterised request means "every channel I am allowed to see": for the
// owner that is the engine's own default, for a stranger it is the public list
// enumerated explicitly.
func (e *LinearEngine) visibleIDs(r *http.Request, requested []string) (ids []string, any bool) {
	owner := linearViewerOf(r).owner
	if len(requested) == 0 {
		if owner {
			return nil, true // the engine's own "every enabled channel"
		}
		pub := e.PublicChannelIDs()
		return pub, len(pub) > 0
	}
	out := make([]string, 0, len(requested))
	for _, id := range requested {
		if _, ok := e.visibleChannel(r, id); ok {
			out = append(out, id)
		}
	}
	return out, len(out) > 0
}

// --- the gate ---------------------------------------------------------------

// gate stamps the viewer onto the request and hands it to the handler. It is
// the only way a linear handler is ever reached, so there is no unwrapped
// surface for a future edit to register by mistake.
//
// The owner's responses go through privateWriter, exactly as requireOwner's do.
// These routes are the awkward case for caching: the same URL answers
// differently depending on who asked -- a stranger gets the public channels, the
// owner gets those plus his own -- so a shared cache in front of this service
// could hand the owner's guide to the next person to request the same path. The
// stranger's copy is ordinary public catalogue data and stays cacheable; the
// owner's is marked no-store and Vary'd on both credentials this service
// accepts.
func (e *LinearEngine) gate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = e.withViewer(r)
		if linearViewerOf(r).owner {
			next(&privateWriter{ResponseWriter: w}, r)
			return
		}
		next(w, r)
	}
}

// There is deliberately NO owner predicate defined in this file.
//
// Who owns an installation is decided once, in owner.go, by authConfig.isOwner:
// the OIDC `sub` named in YARRIT_OWNER_SUB, or a verified address in
// YARRIT_OWNER_EMAIL, and nobody at all when neither is set. main.go passes that
// method straight to SetOwnerFunc.
//
// An earlier draft of this file grew its own predicate reading its own variable,
// and it was wrong in three separate ways that are worth recording because each
// is easy to re-introduce:
//
//   - it defaulted to "any valid session is the owner" when *its* variable was
//     unset. In production that variable was never set -- the deployed instance
//     configures YARRIT_OWNER_SUB -- so the default was the live behaviour, and
//     it made every account in the directory an owner of Live TV.
//   - it matched preferred_username, which owner.go rules out on purpose: it is
//     a display name in Authentik and reassignable, so a rename hands over the
//     library.
//   - two predicates drift. Whichever is stricter stops mattering the moment
//     somebody edits only the other one.
//
// So this file answers "which channels may this caller see" and nothing else.
// Ownership is somebody else's question and it is asked in exactly one place.

// linearEdge is what main.go registers: the cross-origin decision for a route
// that answers differently depending on who asked.
//
// cors.go draws one line -- catalogue data is open to any origin, personal data
// is same-origin and never sees a cross-origin credential -- and owner.go is
// explicit that requireOwner is never composed with publicCORS, because
// `Access-Control-Allow-Origin: *` cannot be paired with credentials and that
// refusal is what physically stops a cookie reaching another instance.
//
// These four routes sit across that line, so the line is drawn per request
// rather than per route:
//
//   - a stranger is reading public-domain listings. Catalogue data, wildcard
//     origin, exactly as the guide has always been served -- which is what lets
//     a Roku or a Tizen set read Nostalgia TV from a different origin.
//   - the owner is reading his own schedule. No wildcard, so a browser will not
//     let another origin read the response even though the credential travelled.
//
// A preflight carries no credentials, so it is answered as a stranger and the
// public case keeps working. The credentialed request that follows is judged on
// its own, which is the correct place for the decision to be made.
func (c *authConfig) linearEdge(next http.HandlerFunc) http.HandlerFunc {
	public := publicCORS(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if c.isOwner(r) {
			next(w, r)
			return
		}
		public(w, r)
	}
}

// ownerOnly refuses anything but the owner outright.
//
// This is for the routes where there is no per-channel question to ask:
// creating, editing and deleting channels, and previewing a rule set against a
// library. Preview in particular reads the *whole* pool before any rule is
// applied, so answering it for a stranger would describe a private library
// item by item -- which is the disclosure the owner gate exists to stop, just
// arriving through a door marked "preview".
//
// The refusal is ownerNotFound, which is the same constant body, status and
// headers every other owner route in the service returns. That matters more
// than it looks: if this route refused with a differently-shaped 404, the shape
// itself would tell a caller which endpoints are owner-gated and which merely
// do not exist -- and the whole point of ownerNotFound is that those two are
// one answer.
func (e *LinearEngine) ownerOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = e.withViewer(r)
		if !linearViewerOf(r).owner {
			ownerNotFound(w)
			return
		}
		next(&privateWriter{ResponseWriter: w}, r)
	}
}
