package main

// The owner boundary.
//
// One person's media library lives behind this file. The rule it enforces is
// the whole of it:
//
//	the owner sees the household's own media plus everything public;
//	everybody else -- signed in or not -- sees only what is public.
//
// WHY "EVERYBODY ELSE" INCLUDES SIGNED-IN PEOPLE
//
// Before this, the personal endpoints were gated with requireUser, which asks
// only "is there a session". That is the right question for a watchlist, where
// the session is what the data is keyed on. It is the wrong question for a
// media library, where the session says nothing about whose library it is: any
// account in the identity provider passed the check and read the same shelf.
// Nothing leaked only because no provider was configured yet. The moment one is,
// every account is an owner. So ownership is now its own axis, and having a
// session is not a claim on anything.
//
// WHAT IDENTIFIES THE OWNER, AND WHAT DELIBERATELY DOES NOT
//
// The owner is named in the process environment and matched against claims the
// identity provider asserted. It is never read from anything the caller sends:
// no header, no query parameter, no body field, no cookie value other than the
// session this server itself signed. A request cannot declare itself the owner,
// which is the property that makes this a boundary rather than a convention.
//
// Two settings, each matched against its own claim and only its own claim:
//
//   - YARRIT_OWNER_SUB matches the OIDC `sub`. This is the right anchor. It is
//     opaque, stable across renames, and an identity provider does not hand the
//     same one to two people.
//
//   - YARRIT_OWNER_EMAIL matches the `email` claim, case-folded, and only when
//     the provider did not say the address was unverified. Weaker, and offered
//     anyway: reading a `sub` out of Authentik is a genuine chore, and a control
//     nobody can configure is a control that gets switched off. The startup log
//     says which one is in force.
//
// They are never cross-matched -- an email is never compared against a `sub` --
// so a directory that lets someone choose their own username cannot be walked
// into an owner match against a subject id.
//
// WHAT HAPPENS WHEN IT IS UNSET
//
// Nobody is the owner. Not "everybody", not "the first person to sign in", not
// "whoever is signed in" -- nobody. Every owner surface answers exactly as it
// answers a stranger, which means an unconfigured instance serves none of its
// own media to anyone, including the person who installed it. That is the only
// safe direction for this particular setting to be wrong in, because the other
// direction is silently publishing somebody's library.
//
// It is a state the operator can see rather than one they have to deduce: the
// process logs it at startup and /api/health reports it. That matters, because
// "my library is empty" and "I have not told it who I am" look identical from
// the front, and an operator who cannot tell them apart reaches for the setting
// that turns the gate off.

import (
	"log"
	"net/http"
	"os"
	"strings"
)

const (
	envOwnerSub   = "YARRIT_OWNER_SUB"
	envOwnerEmail = "YARRIT_OWNER_EMAIL"
)

// ownerPolicy is the configured answer to "who owns this instance".
//
// Sets rather than single values because one person is reasonably two entries:
// an account migrated between identity providers has two subjects, and both
// should keep working through the cutover. It is not a mechanism for granting
// several people access -- that would be an account system, which is a
// different feature with a different privacy story.
type ownerPolicy struct {
	subs   map[string]bool
	emails map[string]bool
}

func loadOwnerPolicy() *ownerPolicy {
	return loadOwnerPolicyFromValues(os.Getenv(envOwnerSub), os.Getenv(envOwnerEmail))
}

// loadOwnerPolicyFromValues is the whole of the parsing, separated from the
// environment so the rules that decide who owns a library can be tested as
// values rather than through process state.
func loadOwnerPolicyFromValues(subs, emails string) *ownerPolicy {
	o := &ownerPolicy{
		subs:   splitOwnerList(subs),
		emails: map[string]bool{},
	}
	for e := range splitOwnerList(emails) {
		o.emails[strings.ToLower(e)] = true
	}
	return o
}

// splitOwnerList accepts commas or whitespace, because a value pasted out of a
// dashboard arrives with either and neither spelling should silently configure
// nothing.
func splitOwnerList(raw string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		if f = strings.TrimSpace(f); f != "" {
			out[f] = true
		}
	}
	return out
}

// configured reports whether anyone at all can be the owner here.
func (o *ownerPolicy) configured() bool {
	return o != nil && (len(o.subs) > 0 || len(o.emails) > 0)
}

// matches decides whether a session belongs to the owner.
//
// Note what is absent: the session's User. That field carries
// preferred_username, which is a display name in most directories and a
// changeable one in several. Matching on it would mean a rename could hand the
// library to whoever picked up the vacated name.
func (o *ownerPolicy) matches(s session) bool {
	if !o.configured() {
		return false
	}
	if s.Sub != "" && o.subs[s.Sub] {
		return true
	}
	if s.Email != "" && s.EmailOK && o.emails[strings.ToLower(strings.TrimSpace(s.Email))] {
		return true
	}
	return false
}

// describe is what the startup log prints. It names the mechanism, never the
// value: an owner's subject id in a log file that gets pasted into an issue is
// a small gift to whoever reads it.
func (o *ownerPolicy) describe() string {
	switch {
	case !o.configured():
		return "no owner is configured (" + envOwnerSub + " and " + envOwnerEmail +
			" are both unset); this instance will serve none of its own media to anyone"
	case len(o.subs) > 0 && len(o.emails) > 0:
		return "owner matched by OIDC subject or email address"
	case len(o.subs) > 0:
		return "owner matched by OIDC subject"
	default:
		return "owner matched by email address; prefer " + envOwnerSub +
			" if any account in your identity provider can change its own email"
	}
}

// isOwner is the one question every gate in this service asks.
//
// A nil receiver answers false. That is not defensiveness for its own sake: a
// server assembled without an auth config -- which is how most tests build one,
// and how a future refactor might briefly leave it -- must behave like an
// instance with no owner rather than one where everybody is.
func (c *authConfig) isOwner(r *http.Request) bool {
	if c == nil || !c.Enabled {
		// Without a configured identity provider nothing can prove who it is,
		// so nothing is the owner. A signed cookie is not enough on its own:
		// SESSION_SECRET may be set while SSO is not, and that combination must
		// not amount to an owner credential.
		return false
	}
	if !c.Owner.configured() {
		return false
	}
	s, ok := c.sessionFrom(r)
	if !ok {
		return false
	}
	return c.Owner.matches(s)
}

// enabled and ownerConfigured are the nil-safe forms health reporting needs. A
// server built without an auth config reports "no sign-in", which is the
// truthful description of an instance where nothing can be attributed.
func (c *authConfig) enabled() bool {
	return c != nil && c.Enabled
}

func (c *authConfig) ownerConfigured() bool {
	return c != nil && c.Owner.configured()
}

// --- the gate --------------------------------------------------------------

// ownerNotFound is the single response every non-owner gets from every owner
// route, whoever they are and whatever they asked for.
//
// 404 rather than 403, and identical for an anonymous caller and a signed-in
// one, because the status code is itself an answer. A 403 says "this exists and
// is not yours", which for a media library is the fact worth hiding: it turns
// any request into a question about what the household owns, and a list of 403s
// against /api/arr/status is a list of films. 401 is no better -- it says a
// session would help, which for anyone who is not the owner is false.
//
// Nothing about the request reaches the body. No method, no path, no id, no
// hint that the route would have behaved differently. The response is a
// constant.
func ownerNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("{\"error\":\"not found\"}\n"))
}

// privateWriter forces owner responses to be uncacheable.
//
// writeJSON sets `Cache-Control: public, max-age=60` on everything it sends,
// which is correct for a catalogue and dangerous here: a shared cache in front
// of this service -- Caddy, a CDN, a corporate proxy -- may store an owner's
// response and hand it to the next caller who asks for the same URL, who is
// very likely not the owner. That is the same poisoning hazard the search cache
// has, one layer further out and outside this process's control.
//
// The header is stamped at WriteHeader time so it wins regardless of what the
// handler set, rather than being written first and quietly overwritten.
type privateWriter struct {
	http.ResponseWriter
	done bool
}

func (p *privateWriter) WriteHeader(code int) {
	if !p.done {
		p.done = true
		p.Header().Set("Cache-Control", "no-store, private")
		// The response depends on who asked, and both credentials this service
		// accepts must be part of any cache key that survives the header above.
		//
		// Added rather than set: publicCORS puts `Vary: Origin` on the search
		// routes before the handler runs, and replacing it would undo the
		// property that makes its wildcard origin safe.
		p.Header().Add("Vary", "Cookie")
		p.Header().Add("Vary", "Authorization")
	}
	p.ResponseWriter.WriteHeader(code)
}

func (p *privateWriter) Write(b []byte) (int, error) {
	if !p.done {
		p.WriteHeader(http.StatusOK)
	}
	return p.ResponseWriter.Write(b)
}

// requireOwner puts a handler behind the boundary.
//
// Deliberately not composed with publicCORS anywhere. An owner route is
// same-origin by construction, and a wildcard origin cannot carry credentials,
// so leaving these uncovered is what stops a session ever reaching another
// instance -- the same argument cors.go makes for the library, applied to the
// larger set of things that are now personal.
func (c *authConfig) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !c.isOwner(r) {
			ownerNotFound(w)
			return
		}
		next(&privateWriter{ResponseWriter: w}, r)
	}
}

// requireOwnerUser is the same gate in the shape requireUser handlers expect.
//
// The user id handed down is still the session's User, not the subject: the
// watchlist and resume points on disk are keyed on it, and changing the key
// would orphan every row already stored.
func (c *authConfig) requireOwnerUser(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !c.isOwner(r) {
			ownerNotFound(w)
			return
		}
		s, ok := c.sessionFrom(r)
		if !ok || s.User == "" {
			// Unreachable in practice -- isOwner already read a valid session --
			// but the fallback is the same 404 rather than a different error,
			// so a future change to sessionFrom cannot open a new shape here.
			ownerNotFound(w)
			return
		}
		next(&privateWriter{ResponseWriter: w}, r, s.User)
	}
}

// logOwnerPolicy is called once at startup.
func logOwnerPolicy(c *authConfig) {
	if !c.Enabled {
		log.Printf("owner boundary: sign-in is not configured, so no request can be " +
			"attributed to anyone; this instance will serve none of its own media")
		return
	}
	log.Printf("owner boundary: %s", c.Owner.describe())
}

// --- the shared-state invariant --------------------------------------------

// dropOwnerCards removes anything drawn from the household's own media.
//
// It is applied at the two places search results become shared -- the response
// cache and a job's card set -- so the invariant is structural rather than
// remembered: NOTHING OWNER-VISIBLE IS EVER WRITTEN TO SHARED STATE.
//
// That invariant is what closes the cache-poisoning case. The search cache is
// keyed on the query alone, and a job is shared by everyone asking the same
// question, so if an owner's search could deposit an owner's card there, the
// next anonymous search for the same words would be served it -- correctly, as
// far as the cache is concerned, which is what makes that class of bug so hard
// to see. The fix is not a second cache keyed on the viewer, which doubles the
// upstream traffic and gets the owner a cold cache for every query the site has
// already answered. It is to keep the shared copy public, always, and merge
// anything owner-visible per response.
//
// Cheap in the case that matters: with nothing marked, it returns the input.
func dropOwnerCards(cards []card) []card {
	n := 0
	for i := range cards {
		if cards[i].owner {
			n++
		}
	}
	if n == 0 {
		return cards
	}
	out := make([]card, 0, len(cards)-n)
	for i := range cards {
		if !cards[i].owner {
			out = append(out, cards[i])
		}
	}
	return out
}
