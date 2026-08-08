package main

// Proof for the owner boundary.
//
// The claim under test is a negative one -- "a non-owner cannot reach the
// owner's media by any route" -- so these tests are organised by the ways that
// claim could be false rather than by function:
//
//	1. the route table itself: every owner route, every method, anonymous
//	   and signed-in-but-not-the-owner
//	2. identity: what counts as the owner, and what must not
//	3. failing safe: an unset, half-set or unauthenticated configuration
//	4. indistinguishability: the response must not say which of "no" it means
//	5. shared state: the cache and the job store, which is where a leak
//	   survives the request that caused it
//
// Two adversaries are modelled throughout and they are not the same one.
// `anonRequest` has no credential. `strangerRequest` holds a VALID session
// signed by this server -- an ordinary account in the identity provider, which
// is precisely what the old requireUser gate admitted. Every assertion below is
// made against both, because the entire point of this change is that the second
// one is no longer privileged.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- fixtures --------------------------------------------------------------

const (
	ownerSub     = "8f2c1e40-owner-subject"
	strangerSub  = "11119999-stranger-subject"
	ownerAddress = "wade@moveweight.com"
)

// ownerAuth is a fully configured instance: sign-in works and an owner is named
// by subject, which is the arrangement being recommended.
func ownerAuth() *authConfig {
	c := testAuth()
	c.Owner = &ownerPolicy{subs: map[string]bool{ownerSub: true}, emails: map[string]bool{}}
	return c
}

func sessionRequest(t *testing.T, c *authConfig, s session, method, url, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, url, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, url, nil)
	}
	s.Exp = time.Now().Add(time.Hour).Unix()
	v, err := c.encode(s)
	if err != nil {
		t.Fatalf("encoding a session: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
	return r
}

// ownerRequest is Wade.
func ownerRequest(t *testing.T, c *authConfig, method, url, body string) *http.Request {
	t.Helper()
	return sessionRequest(t, c,
		session{User: "wade", Email: ownerAddress, Sub: ownerSub, EmailOK: true},
		method, url, body)
}

// strangerRequest is the adversary this change exists for: a real account, a
// real session, signed by this very server, belonging to somebody else.
func strangerRequest(t *testing.T, c *authConfig, method, url, body string) *http.Request {
	t.Helper()
	return sessionRequest(t, c,
		session{User: "someone", Email: "someone@example.com", Sub: strangerSub, EmailOK: true},
		method, url, body)
}

// ownerJar makes an httptest server's client present the owner's session on
// every request, for the tests that drive a real HTTP client rather than a
// handler. The two firmware sources that read the owner's own storage are
// owner-only, so a test exercising them has to ask as the owner or it is
// testing the gate rather than the thing it is about.
func ownerJar(t *testing.T, c *authConfig, srv *httptest.Server) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.encode(session{User: "wade", Sub: ownerSub, EmailOK: true,
		Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: v}})
	srv.Client().Jar = jar
}

func anonRequest(method, url, body string) *http.Request {
	if body != "" {
		return httptest.NewRequest(method, url, strings.NewReader(body))
	}
	return httptest.NewRequest(method, url, nil)
}

// ownerMux builds the real route table the way main.go does, so these tests
// exercise the wiring rather than a restatement of it. Everything a provider
// could answer with is registered, because a boundary tested against an empty
// registry proves only that nothing was configured.
func ownerMux(t *testing.T, c *authConfig) (*http.ServeMux, *server) {
	t.Helper()

	reg := &Registry{}
	for _, p := range []Provider{
		radarrLike(),
		fakeProvider{id: "romm", name: "RomM", domains: []string{"game"},
			roles: []string{"library"}, caps: []string{"health"}},
	} {
		if err := reg.Add(p); err != nil {
			t.Fatalf("registering %s: %v", p.ID(), err)
		}
	}

	store, err := newLibraryStore("")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{providers: reg, cache: map[string]cacheEntry{}, library: store, auth: c}

	mux := http.NewServeMux()
	arr := &arrAPI{reg: reg}
	game := &gameAPI{reg: reg}
	linear := LinearDefaultEngine()

	mux.HandleFunc("/api/providers", publicCORS(s.handleProviders))
	mux.HandleFunc("/api/activity", c.requireOwnerUser(s.handleActivity))
	mux.HandleFunc("/api/arr/search", c.requireOwner(arr.handleSearch))
	mux.HandleFunc("/api/arr/details", c.requireOwner(arr.handleDetails))
	mux.HandleFunc("/api/arr/library", c.requireOwner(arr.handleLibrary))
	mux.HandleFunc("/api/arr/status", c.requireOwner(arr.handleStatus))
	mux.HandleFunc("/api/arr/request", c.requireOwner(arr.handleRequest))
	mux.HandleFunc("/api/game/search", c.requireOwner(game.handleSearch))
	mux.HandleFunc("/api/game/details", c.requireOwner(game.handleDetails))
	mux.HandleFunc("/api/game/library", c.requireOwner(game.handleLibrary))
	mux.HandleFunc("/api/game/status", c.requireOwner(game.handleStatus))
	mux.HandleFunc("/api/game/request", c.requireOwner(game.handleRequest))
	mux.HandleFunc("/api/v1/linear/guide", c.requireOwner(linear.handleGuide))
	mux.HandleFunc("/api/v1/linear/now", c.requireOwner(linear.handleNow))
	mux.HandleFunc("/api/v1/linear/stream", c.requireOwner(linear.handleStream))
	mux.HandleFunc("/api/v1/linear/channels", c.requireOwner(linear.handleChannels))
	mux.HandleFunc("/api/v1/linear/preview", c.requireOwner(linear.handlePreview))
	mux.HandleFunc("/api/v1/library", c.requireOwnerUser(s.handleLibrary))
	mux.HandleFunc("/api/v1/progress", c.requireOwnerUser(s.handleProgress))
	mux.HandleFunc("/api/v1/continue", c.requireOwnerUser(s.handleContinue))
	registerAddonRoutesWith(mux, c, reg)
	return mux, s
}

// ownerRoutes is the enumeration the report's table is built from. Every route
// that can carry, name, count or imply the owner's media appears here with
// every method it accepts -- including the ones that only ever answer an error,
// because an error that varies is an oracle.
var ownerRoutes = []struct{ method, path, body string }{
	{"GET", "/api/activity", ""},

	{"GET", "/api/arr/search?q=dune", ""},
	{"GET", "/api/arr/details?id=tmdb:movie:78", ""},
	{"GET", "/api/arr/library?domain=video", ""},
	{"GET", "/api/arr/status?id=tmdb:movie:78", ""},
	{"POST", "/api/arr/request", `{"canonicalId":"tmdb:movie:78"}`},
	{"GET", "/api/arr/request", ""},

	{"GET", "/api/game/search?q=zelda", ""},
	{"GET", "/api/game/details?id=romm:game:1", ""},
	{"GET", "/api/game/library", ""},
	{"GET", "/api/game/status?id=romm:game:1", ""},
	{"POST", "/api/game/request", `{"canonicalId":"romm:game:1"}`},

	{"GET", "/api/v1/linear/guide", ""},
	{"GET", "/api/v1/linear/now", ""},
	{"GET", "/api/v1/linear/stream?channel=1", ""},
	{"GET", "/api/v1/linear/channels", ""},
	{"POST", "/api/v1/linear/channels", `{"id":"c1","name":"Mine"}`},
	{"DELETE", "/api/v1/linear/channels?id=c1", ""},
	{"POST", "/api/v1/linear/preview", `{"sourceProvider":"plex"}`},

	{"GET", "/api/v1/library", ""},
	{"POST", "/api/v1/library", `{"key":"tt1375666","title":"Inception"}`},
	{"DELETE", "/api/v1/library?key=tt1375666", ""},
	{"GET", "/api/v1/progress?key=tt1375666", ""},
	{"PUT", "/api/v1/progress", `{"key":"tt1375666","position":120}`},
	{"GET", "/api/v1/continue", ""},

	{"GET", "/api/addons", ""},
	{"POST", "/api/addons", `{"url":"https://example.com/manifest.json"}`},
	{"DELETE", "/api/addons?id=x", ""},
	{"POST", "/api/addons/remove", `{"id":"x"}`},
	{"POST", "/api/addons/enabled", `{"id":"x","enabled":false}`},
	{"POST", "/api/addons/order", `{"ids":["x"]}`},
	{"GET", "/api/addons/search?q=dune", ""},
	{"GET", "/api/addons/meta?id=addon:x:movie:tt1", ""},
	{"GET", "/api/addons/stream?id=addon:x:movie:tt1", ""},
	{"GET", "/api/addons/subtitles?id=addon:x:movie:tt1", ""},
}

// --- 1. the route table ----------------------------------------------------

// The headline claim, stated against every route and every method at once.
func TestNoOwnerRouteAnswersAnonymously(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	for _, rt := range ownerRoutes {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, anonRequest(rt.method, rt.path, rt.body))
		if rec.Code != http.StatusNotFound {
			t.Errorf("anonymous %s %s got %d, want 404\n%s",
				rt.method, rt.path, rec.Code, rec.Body.String())
		}
	}
}

// The same claim against the adversary that actually motivated this work: a
// valid session belonging to somebody who is not the owner. Before this change
// every one of these answered 200.
func TestNoOwnerRouteAnswersASignedInStranger(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	for _, rt := range ownerRoutes {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, strangerRequest(t, c, rt.method, rt.path, rt.body))
		if rec.Code != http.StatusNotFound {
			t.Errorf("signed-in stranger %s %s got %d, want 404\n%s",
				rt.method, rt.path, rec.Code, rec.Body.String())
		}
	}
}

// The other half of every gate: the owner must actually get through it. A
// boundary that refuses everybody passes the two tests above and is useless.
//
// Tested on the BODY rather than the status, because a handler that ran and
// found nothing legitimately answers 404 too -- /api/arr/details says so when
// no adapter owns the id, and /api/addons/remove says so for an addon that is
// not installed. Those are the handler's 404 and they carry the handler's
// words; the gate's 404 is one exact constant. Distinguishing them is the whole
// assertion: the owner must never receive the constant.
const gateRefusal = "{\"error\":\"not found\"}\n"

func TestTheOwnerReachesEveryOwnerRoute(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	for _, rt := range ownerRoutes {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, ownerRequest(t, c, rt.method, rt.path, rt.body))
		if rec.Body.String() == gateRefusal {
			t.Errorf("the owner was refused %s %s with the stranger's constant 404",
				rt.method, rt.path)
		}
	}
}

// And the constant really is what a stranger gets, so the test above is
// comparing against the right string rather than one that never appears.
func TestTheGateRefusalIsTheConstant(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, strangerRequest(t, c, "GET", "/api/arr/library", ""))
	if rec.Body.String() != gateRefusal {
		t.Fatalf("the gate answered %q, not the constant %q", rec.Body.String(), gateRefusal)
	}
}

// A bearer token is the other credential this service accepts, and a television
// is the client that has to use it. If ownership were readable only from a
// cookie, every set-top box would be a stranger to its owner's own library.
func TestOwnershipTravelsOnABearerSession(t *testing.T) {
	c := ownerAuth()

	// The bearer path is validated against the identity provider, so this test
	// works one level down: it asserts that a session carrying the owner's
	// subject is recognised however it arrived. bearer.go's job is to populate
	// Sub, and it is the only field the check reads.
	if !c.Owner.matches(session{User: "tv", Sub: ownerSub}) {
		t.Error("a session bearing the owner's subject was not recognised as the owner")
	}
	if c.Owner.matches(session{User: "tv", Sub: strangerSub}) {
		t.Error("a stranger's subject was accepted")
	}
}

// --- 2. identity -----------------------------------------------------------

// Ownership must come from a claim the identity provider asserted, never from
// anything the caller can write. These are the spoofs a request can attempt.
func TestOwnershipCannotBeClaimedByTheCaller(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	attempts := []struct {
		name string
		with func(*http.Request)
	}{
		{"X-Yarrit-User header", func(r *http.Request) {
			// The header /auth/verify sets for downstream. If anything trusted
			// it on the way IN, the edge could be bypassed by sending it.
			r.Header.Set("X-Yarrit-User", "wade")
		}},
		{"X-Yarrit-Email header", func(r *http.Request) {
			r.Header.Set("X-Yarrit-Email", ownerAddress)
		}},
		{"forwarded-user header", func(r *http.Request) {
			r.Header.Set("X-Forwarded-User", ownerSub)
			r.Header.Set("Remote-User", ownerSub)
		}},
		{"owner query parameter", func(r *http.Request) {
			q := r.URL.Query()
			q.Set("owner", "1")
			q.Set("sub", ownerSub)
			q.Set("user", "wade")
			r.URL.RawQuery = q.Encode()
		}},
		{"unsigned session cookie", func(r *http.Request) {
			raw, _ := json.Marshal(session{User: "wade", Sub: ownerSub,
				Exp: time.Now().Add(time.Hour).Unix()})
			r.AddCookie(&http.Cookie{Name: sessionCookie,
				Value: string(raw) + ".not-a-signature"})
		}},
		{"session signed with another key", func(r *http.Request) {
			other := &authConfig{Secret: []byte("a-different-secret")}
			v, _ := other.encode(session{User: "wade", Sub: ownerSub,
				Exp: time.Now().Add(time.Hour).Unix()})
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
		}},
		{"expired owner session", func(r *http.Request) {
			v, _ := c.encode(session{User: "wade", Sub: ownerSub,
				Exp: time.Now().Add(-time.Hour).Unix()})
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
		}},
	}

	for _, a := range attempts {
		for _, path := range []string{"/api/arr/library", "/api/v1/library", "/api/activity"} {
			req := anonRequest("GET", path, "")
			a.with(req)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s got %d on %s; ownership was claimable by the caller",
					a.name, rec.Code, path)
			}
		}
	}
}

// preferred_username is a display name in most directories and a changeable one
// in several, so a rename must not hand the library to whoever picks up the
// vacated name.
func TestUsernameAloneIsNotOwnership(t *testing.T) {
	c := ownerAuth()
	if c.Owner.matches(session{User: "wade", Sub: strangerSub, Email: "x@example.com", EmailOK: true}) {
		t.Error("a stranger who took the owner's username became the owner")
	}
}

// The two settings are matched against their own claim and only their own, so a
// directory that lets someone choose their username cannot be walked into a
// match against a subject id.
func TestClaimsAreNotCrossMatched(t *testing.T) {
	bySub := &ownerPolicy{subs: map[string]bool{ownerSub: true}, emails: map[string]bool{}}
	if bySub.matches(session{User: ownerSub, Email: ownerSub, EmailOK: true}) {
		t.Error("a subject id presented as an email or username matched the subject rule")
	}

	byEmail := &ownerPolicy{subs: map[string]bool{}, emails: map[string]bool{ownerAddress: true}}
	if byEmail.matches(session{Sub: ownerAddress}) {
		t.Error("an email presented as a subject matched the email rule")
	}
	if !byEmail.matches(session{Email: "WADE@MoveWeight.com", EmailOK: true}) {
		t.Error("the email rule is case-sensitive; addresses are not")
	}
}

// An address the provider says is unverified is an address anybody may have
// typed into a profile field.
func TestUnverifiedEmailIsNotOwnership(t *testing.T) {
	byEmail := &ownerPolicy{subs: map[string]bool{}, emails: map[string]bool{ownerAddress: true}}
	if byEmail.matches(session{Email: ownerAddress, EmailOK: false}) {
		t.Error("an unverified email address was accepted as ownership")
	}
	if !byEmail.matches(session{Email: ownerAddress, EmailOK: true}) {
		t.Error("a verified email address was refused")
	}
}

// Silence is not a denial: most deployments never send email_verified at all,
// and treating its absence as "unverified" would break owner-by-email
// everywhere. An explicit false is still honoured.
func TestEmailVerifiedClaimParsing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   bool
	}{
		{"absent", map[string]any{}, true},
		{"true", map[string]any{"email_verified": true}, true},
		{"false", map[string]any{"email_verified": false}, false},
		{"string true", map[string]any{"email_verified": "true"}, true},
		{"string false", map[string]any{"email_verified": "false"}, false},
		{"nonsense", map[string]any{"email_verified": 7}, false},
	} {
		if got := emailIsVerified(tc.claims); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A cookie minted before Sub existed decodes with it empty. That must fail the
// check rather than pass it -- one extra sign-in is the correct price.
func TestPreUpgradeSessionIsNotTheOwner(t *testing.T) {
	c := ownerAuth()
	req := sessionRequest(t, c, session{User: "wade", Email: ownerAddress}, "GET", "/api/v1/library", "")
	if c.isOwner(req) {
		t.Error("a session predating the subject claim was treated as the owner")
	}
}

// --- 3. failing safe -------------------------------------------------------

// The setting this whole boundary rests on, left unset. Nobody is the owner --
// not the first caller, not every caller, nobody.
func TestUnsetOwnerConfigLetsNobodyThrough(t *testing.T) {
	c := testAuth() // sign-in configured, no owner named
	c.Owner = loadOwnerPolicyFromValues("", "")
	if c.Owner.configured() {
		t.Fatal("an empty configuration reported itself as configured")
	}
	mux, _ := ownerMux(t, c)

	for _, rt := range ownerRoutes {
		for _, req := range []*http.Request{
			anonRequest(rt.method, rt.path, rt.body),
			ownerRequest(t, c, rt.method, rt.path, rt.body),
			strangerRequest(t, c, rt.method, rt.path, rt.body),
		} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("with no owner configured, %s %s got %d, want 404",
					rt.method, rt.path, rec.Code)
			}
		}
	}
}

// A configuration of whitespace and separators is an unset one. Getting this
// wrong the other way would make `YARRIT_OWNER_SUB=" "` mean "the owner is a
// space", which nothing would match -- safe -- or worse, be trimmed into an
// empty allow-anything set.
func TestBlankOwnerConfigIsUnsetNotWildcard(t *testing.T) {
	for _, raw := range []string{"", " ", "\t\n", ",", " , , "} {
		o := loadOwnerPolicyFromValues(raw, raw)
		if o.configured() {
			t.Errorf("%q was read as a configured owner", raw)
		}
		if o.matches(session{Sub: "anything", Email: "anyone@example.com", EmailOK: true}) {
			t.Errorf("%q matched a session", raw)
		}
		if o.matches(session{}) {
			t.Errorf("%q matched an empty session", raw)
		}
	}
}

// An owner named against a sign-in that does not exist. Nothing can be
// attributed, so nothing is the owner -- a signed cookie alone must not amount
// to an owner credential when SESSION_SECRET is set and SSO is not.
func TestOwnerWithoutSignInIsNobody(t *testing.T) {
	c := ownerAuth()
	c.Enabled = false
	req := ownerRequest(t, c, "GET", "/api/v1/library", "")
	if c.isOwner(req) {
		t.Error("an owner was recognised on an instance with no sign-in configured")
	}
}

// A server assembled without an auth config -- how most tests build one, and
// how a refactor might briefly leave it -- must behave like an instance with no
// owner rather than one where everybody is.
func TestNilAuthIsNeverTheOwner(t *testing.T) {
	var c *authConfig
	if c.isOwner(anonRequest("GET", "/api/v1/library", "")) {
		t.Error("a nil auth config answered yes")
	}
	if c.enabled() || c.ownerConfigured() {
		t.Error("a nil auth config reported itself configured")
	}

	// And through the endpoint that reads it directly.
	s := &server{providers: &Registry{}, cache: map[string]cacheEntry{}}
	rec := httptest.NewRecorder()
	s.handleProviders(rec, anonRequest("GET", "/api/providers", ""))
	var got struct {
		Providers []providerView `json:"providers"`
	}
	decodeBodyInto(t, rec, &got)
	if len(got.Providers) != 0 {
		t.Errorf("a server with no auth config listed %d providers", len(got.Providers))
	}
}

// The empty policy is a value, not a nil pointer, and both must answer the same.
func TestNilOwnerPolicyMatchesNothing(t *testing.T) {
	var o *ownerPolicy
	if o.configured() || o.matches(session{Sub: ownerSub, Email: ownerAddress, EmailOK: true}) {
		t.Error("a nil policy behaved as though it were configured")
	}
}

// --- 4. indistinguishability -----------------------------------------------

// The status code and the body are themselves answers. If a signed-in stranger
// got 403 where an anonymous caller got 401, or if a route that has data
// answered differently from one that does not, the refusal becomes an oracle:
// walk a film list through /api/arr/status and read the shelf a title at a
// time without ever being allowed in.
func TestEveryRefusalIsByteIdentical(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	var first string
	var firstRoute string
	for _, rt := range ownerRoutes {
		for _, who := range []struct {
			name string
			req  *http.Request
		}{
			{"anonymous", anonRequest(rt.method, rt.path, rt.body)},
			{"stranger", strangerRequest(t, c, rt.method, rt.path, rt.body)},
		} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, who.req)

			got := rec.Body.String()
			if first == "" {
				first, firstRoute = got, rt.method+" "+rt.path
				continue
			}
			if got != first {
				t.Errorf("%s %s %s answered %q; %s answered %q -- the refusal varies",
					who.name, rt.method, rt.path, got, firstRoute, first)
			}
		}
	}
	if first == "" {
		t.Fatal("no route was exercised")
	}
}

// A refusal must not leak through headers either: no Allow list enumerating the
// methods a route really supports, no CORS headers inviting another origin to
// try, and nothing that would let a shared cache store the answer.
func TestRefusalsCarryNoDescriptiveHeaders(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	for _, rt := range ownerRoutes {
		req := strangerRequest(t, c, rt.method, rt.path, rt.body)
		req.Header.Set("Origin", "https://someone-else.example")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if got := rec.Header().Get("Allow"); got != "" {
			t.Errorf("%s %s refused with Allow: %q", rt.method, rt.path, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s %s refused with Allow-Origin: %q", rt.method, rt.path, got)
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Errorf("%s %s refused with Cache-Control: %q, want no-store", rt.method, rt.path, got)
		}
	}
}

// A shared cache between this service and the internet must never store an
// owner's response. writeJSON stamps `public, max-age=60` on everything,
// which is right for a catalogue and would be a slow leak here: the CDN
// answers the next caller from the owner's copy.
func TestOwnerResponsesAreNeverPubliclyCacheable(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	for _, rt := range ownerRoutes {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, ownerRequest(t, c, rt.method, rt.path, rt.body))

		cc := rec.Header().Get("Cache-Control")
		if strings.Contains(cc, "public") {
			t.Errorf("owner response to %s %s is publicly cacheable: %q", rt.method, rt.path, cc)
		}
		if !strings.Contains(cc, "no-store") {
			t.Errorf("owner response to %s %s has Cache-Control %q, want no-store",
				rt.method, rt.path, cc)
		}
		if v := rec.Header().Get("Vary"); !strings.Contains(v, "Cookie") {
			t.Errorf("owner response to %s %s has Vary %q, want it to include Cookie",
				rt.method, rt.path, v)
		}
	}
}

// Naming the configured backends describes what a household holds nearly as
// well as listing it would. The answer must also be the SAME answer whether or
// not anything is configured, or its emptiness is the disclosure.
func TestProvidersAreNotNamedToStrangers(t *testing.T) {
	c := ownerAuth()
	mux, _ := ownerMux(t, c)

	var bodies []string
	for _, req := range []*http.Request{
		anonRequest("GET", "/api/providers", ""),
		strangerRequest(t, c, "GET", "/api/providers", ""),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("/api/providers got %d; clients bootstrap against it", rec.Code)
		}
		body := rec.Body.String()
		for _, name := range []string{"radarr", "Radarr", "romm", "RomM"} {
			if strings.Contains(body, name) {
				t.Errorf("/api/providers named %q to a non-owner: %s", name, body)
			}
		}
		bodies = append(bodies, body)
	}

	// An instance with nothing configured must answer identically, so an empty
	// list never means "he runs nothing" and never means "he runs something".
	bare := &server{providers: &Registry{}, cache: map[string]cacheEntry{}, auth: c}
	rec := httptest.NewRecorder()
	bare.handleProviders(rec, anonRequest("GET", "/api/providers", ""))
	for _, b := range bodies {
		if b != rec.Body.String() {
			t.Errorf("a configured instance answers %q where a bare one answers %q",
				b, rec.Body.String())
		}
	}

	// And the owner does see them, or the endpoint is useless.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, ownerRequest(t, c, "GET", "/api/providers", ""))
	if !strings.Contains(rec.Body.String(), "radarr") {
		t.Errorf("the owner cannot see his own providers: %s", rec.Body.String())
	}
}

// --- 5. shared state -------------------------------------------------------

// The cache-poisoning case, stated end to end: the owner searches, then an
// anonymous caller searches the same words, and must not be served the owner's
// copy. The cache is keyed on the query alone, so without the invariant in
// owner.go this is not a subtle bug -- it is the default behaviour.
func TestOwnerSearchDoesNotPoisonTheSharedCacheForAnonymous(t *testing.T) {
	c := ownerAuth()
	s := &server{cache: map[string]cacheEntry{}, auth: c, ttl: defaultTTL}

	// A result set as it would look if a provider ever contributed to search:
	// one public card and one drawn from the owner's own media server.
	s.putCached(searchCacheKey("the matrix", ""), []card{
		{Key: "public-1", Title: "The Matrix", Kind: "video"},
		{Key: "owner-1", Title: "The Matrix (my rip).mkv", Kind: "video", owner: true},
	})

	// Straight out of the cache, which is what any later search reads.
	cached, ok := s.getCached(searchCacheKey("the matrix", ""))
	if !ok {
		t.Fatal("nothing was cached")
	}
	for _, card := range cached {
		if card.owner {
			t.Fatalf("an owner-visible card reached the shared cache: %q", card.Title)
		}
	}

	// And through the endpoint, for both audiences.
	for _, who := range []struct {
		name string
		req  *http.Request
	}{
		{"anonymous", anonRequest("GET", "/api/search?q=the+matrix", "")},
		{"stranger", strangerRequest(t, c, "GET", "/api/search?q=the+matrix", "")},
		{"owner", ownerRequest(t, c, "GET", "/api/search?q=the+matrix", "")},
	} {
		rec := httptest.NewRecorder()
		s.handleSearch(rec, who.req)
		if strings.Contains(rec.Body.String(), "my rip") {
			t.Errorf("%s was served the owner's card out of the shared cache: %s",
				who.name, rec.Body.String())
		}
	}
}

// localCards scans the WHOLE cache, so a card stored under one query surfaces
// in a different one's first paint. That is the path by which an owner card
// cached under "the matrix" would appear in a stranger's search for "matrix".
func TestOwnerCardsNeverSurfaceThroughLocalCards(t *testing.T) {
	s := &server{cache: map[string]cacheEntry{}, ttl: defaultTTL}
	s.putCached(searchCacheKey("the matrix", ""), []card{
		{Key: "owner-1", Title: "The Matrix (my rip).mkv", Kind: "video", owner: true},
		{Key: "public-1", Title: "The Matrix", Kind: "video"},
	})

	got := s.localCards("matrix", "", 60)
	if len(got) == 0 {
		t.Fatal("the public card did not survive; the filter is too broad")
	}
	for _, c := range got {
		if c.owner || strings.Contains(c.Title, "my rip") {
			t.Errorf("an owner card surfaced through an adjacent query: %q", c.Title)
		}
	}
}

// A job is shared by everyone searching the same words and collectable by id,
// and the person collecting is often not the one who started it.
func TestOwnerCardsNeverEnterAJob(t *testing.T) {
	j := newSearchJob("the matrix", "")
	j.add([]card{
		{Key: "owner-1", Title: "The Matrix (my rip).mkv", owner: true},
		{Key: "public-1", Title: "The Matrix"},
	})

	snap := j.snapshot()
	if len(snap.cards) != 1 {
		t.Fatalf("job holds %d cards, want only the public one", len(snap.cards))
	}
	if snap.cards[0].owner {
		t.Error("an owner card was collectable by job id")
	}
}

// The response boundary, tested on its own. This is the second of the two
// independent guards: it catches anything merged into a single response after
// the cache was read, which the write-side invariant cannot see.
func TestRespondSearchScrubsForNonOwners(t *testing.T) {
	// Cards need a playable source or the ordinary filter drops them before
	// this test gets to say anything about ownership.
	withSource := func(key, title string, owned bool) card {
		return card{
			Key: key, Title: title, Kind: "video", owner: owned,
			Sources: []source{{Title: title, Seeders: 20, Magnet: "magnet:?xt=x"}},
		}
	}
	cards := []card{
		withSource("public-1", "The Matrix", false),
		withSource("owner-1", "The Matrix (my rip).mkv", true),
	}
	f := parseFilters(url.Values{})

	rec := httptest.NewRecorder()
	respondSearch(rec, "the matrix", f, deviceProfileFor(""), cards, searchState{}, false)
	if strings.Contains(rec.Body.String(), "my rip") {
		t.Errorf("a non-owner response carried an owner card: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "The Matrix") {
		t.Errorf("the public card was scrubbed along with it: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	respondSearch(rec, "the matrix", f, deviceProfileFor(""), cards, searchState{}, true)
	if !strings.Contains(rec.Body.String(), "my rip") {
		t.Errorf("the owner's own response was scrubbed: %s", rec.Body.String())
	}
}

// dropOwnerCards must not reorder or lose the public cards it keeps: search
// paints progressively, and a card that moves after it is drawn moves whatever
// somebody was about to click.
func TestDropOwnerCardsPreservesOrder(t *testing.T) {
	in := []card{
		{Key: "a"}, {Key: "x", owner: true}, {Key: "b"},
		{Key: "y", owner: true}, {Key: "c"},
	}
	got := dropOwnerCards(in)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("kept %d cards, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Key != want[i] {
			t.Errorf("position %d is %q, want %q", i, got[i].Key, want[i])
		}
	}
	// The common case is untouched, and returned as the same slice.
	clean := []card{{Key: "a"}, {Key: "b"}}
	if out := dropOwnerCards(clean); len(out) != 2 {
		t.Errorf("a clean set lost cards")
	}
}

// The regression guard for the leak most likely to be reintroduced.
//
// Search does not consult the provider registry today, which is the only
// reason /api/search is safe to leave open. That is a property of the current
// wiring rather than a rule the code states, so it is asserted here: a server
// with every provider configured must answer a search identically to one with
// none. Wiring provider results into search will fail this test, which is the
// point -- the fix is to mark those cards `owner` so the boundary catches them,
// not to delete the assertion.
func TestSearchOutputDoesNotDependOnConfiguredProviders(t *testing.T) {
	c := ownerAuth()
	seed := []card{{Key: "public-1", Title: "The Matrix", Kind: "video"}}

	answer := func(reg *Registry, req *http.Request) string {
		s := &server{cache: map[string]cacheEntry{}, providers: reg, auth: c, ttl: defaultTTL}
		s.putCached(searchCacheKey("the matrix", ""), seed)
		rec := httptest.NewRecorder()
		s.handleSearch(rec, req)
		return rec.Body.String()
	}

	loaded := &Registry{}
	if err := loaded.Add(radarrLike()); err != nil {
		t.Fatal(err)
	}

	for _, who := range []struct {
		name string
		req  func() *http.Request
	}{
		{"anonymous", func() *http.Request { return anonRequest("GET", "/api/search?q=the+matrix", "") }},
		{"owner", func() *http.Request { return ownerRequest(t, c, "GET", "/api/search?q=the+matrix", "") }},
	} {
		bare := answer(&Registry{}, who.req())
		full := answer(loaded, who.req())
		if bare != full {
			t.Errorf("%s: search now depends on the provider registry.\n"+
				"with none: %s\nwith one:  %s\n"+
				"If provider results are being merged into search, mark them "+
				"`owner: true` so the boundary applies to them.", who.name, bare, full)
		}
	}
}

// The same guard for the two surfaces that feed off the landing page.
//
// /api/discover and /api/suggest are open, and are being worked on separately
// (discover.go, archive.go, igdb.go). They draw from TMDB, IGDB, archive.org
// and the search cache today -- none of which knows this household exists --
// so there is nothing to filter. These assert that this stays true rather than
// trusting that it will: wiring the provider registry into either one would
// publish the owner's shelf on the landing page of an open site, and it would
// do so without touching a single route registration.
func TestDiscoverAndSuggestDoNotDependOnConfiguredProviders(t *testing.T) {
	c := ownerAuth()

	loaded := &Registry{}
	if err := loaded.Add(radarrLike()); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Add(fakeProvider{id: "plex", name: "Plex", domains: []string{"video"},
		roles: []string{"library"}, caps: []string{"health"}}); err != nil {
		t.Fatal(err)
	}

	// A cached discover row set, so both endpoints have something to answer
	// with and the comparison is not empty-against-empty.
	//
	// Five matching titles, not one, and that is load-bearing: type-ahead waits
	// on archive.org only when it has fewer than suggestLocalEnough answers of
	// its own, so a single local hit turns this into a live network call and
	// the comparison starts measuring archive.org's mood instead of the
	// provider registry.
	rows := []discoverRow{{Title: "Trending", Key: "trending", Items: []discoverItm{
		{Title: "The Matrix", Year: 1999, MediaType: "movie"},
		{Title: "The Matrix Reloaded", Year: 2003, MediaType: "movie"},
		{Title: "The Matrix Revolutions", Year: 2003, MediaType: "movie"},
		{Title: "The Matrix Resurrections", Year: 2021, MediaType: "movie"},
		{Title: "Matrix: Recoded", Year: 2004, MediaType: "movie"},
	}}}

	answer := func(reg *Registry, path string, req *http.Request) string {
		s := &server{cache: map[string]cacheEntry{}, providers: reg, auth: c, ttl: defaultTTL}
		s.discover.rows = rows
		s.discover.expires = time.Now().Add(time.Hour)
		rec := httptest.NewRecorder()
		switch path {
		case "discover":
			s.handleDiscover(rec, req)
		default:
			s.handleSuggest(rec, req)
		}
		return rec.Body.String()
	}

	for _, tc := range []struct {
		endpoint string
		req      func() *http.Request
	}{
		{"discover", func() *http.Request { return anonRequest("GET", "/api/discover", "") }},
		{"suggest", func() *http.Request { return anonRequest("GET", "/api/suggest?q=matrix", "") }},
	} {
		bare := answer(&Registry{}, tc.endpoint, tc.req())
		full := answer(loaded, tc.endpoint, tc.req())
		if bare != full {
			t.Errorf("/api/%s now depends on the provider registry.\n"+
				"with none: %s\nwith two:  %s\n"+
				"This endpoint is open to anonymous callers. If provider content "+
				"is being merged in, it has to be gated the way the owner routes "+
				"are -- an open landing page is not the place to publish a "+
				"private library.", tc.endpoint, bare, full)
		}
	}
}

// The search cache is shared with anonymous callers, so an owner's search must
// not leave a differently-cacheable trace in front of it either. The owner's
// response may carry more than the public one for the same words, and a CDN
// keyed on the URL alone would hand it to whoever asks next.
func TestOwnerSearchResponsesAreNotSharedCacheable(t *testing.T) {
	c := ownerAuth()
	s := &server{cache: map[string]cacheEntry{}, auth: c, ttl: defaultTTL}
	s.putCached(searchCacheKey("the matrix", ""), []card{{
		Key: "public-1", Title: "The Matrix", Kind: "video",
		Sources: []source{{Title: "The Matrix", Seeders: 9, Magnet: "magnet:?xt=x"}},
	}})

	rec := httptest.NewRecorder()
	s.handleSearch(rec, ownerRequest(t, c, "GET", "/api/search?q=the+matrix", ""))
	cc := rec.Header().Get("Cache-Control")
	if strings.Contains(cc, "public") || !strings.Contains(cc, "no-store") {
		t.Errorf("the owner's search response has Cache-Control %q; a shared cache "+
			"would serve it to the next caller", cc)
	}

	// The public answer keeps its caching, which is what makes the site fast
	// for the traffic that is not the owner.
	rec = httptest.NewRecorder()
	s.handleSearch(rec, anonRequest("GET", "/api/search?q=the+matrix", ""))
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Errorf("the public search response lost its caching: %q", cc)
	}
}

// The firmware relay is the one place the boundary is drawn per SOURCE rather
// than per route, so it needs its own proof in both directions.
//
// /api/play/bios/{source}/{system}/{file} has three sources. The Internet
// Archive's copy is public at the other end and is what makes a ColecoVision
// play for a visitor who owns nothing, so it stays open. The other two read the
// owner's firmware tree and his RomM -- his files, off his storage, and an
// index of them is an enumeration of what he holds.
func TestOwnerFirmwareSourcesAreNotRelayedToStrangers(t *testing.T) {
	const core = "coleco"

	// Both private sources really hold the file, and the index is warmed the
	// way a real verdict warms it. Without that the owner would 404 too and the
	// test would prove nothing: an empty index refuses everybody, which is not
	// the property under test.
	body := make([]byte, 8192)
	root := t.TempDir()
	writeBIOS(t, root, "colecovision", "coleco.rom", body)
	withColecoHash(t, md5str(body))

	c := ownerAuth()
	p := newPlayArchive(nil)
	p.auth = c
	dir := newDirFirmware(root)
	dir.Firmware(context.Background(), core)
	p.diskFirmware = dir
	p.firmware = colecoLibrary()

	mux := http.NewServeMux()
	p.register(mux)

	for _, source := range []string{biosSourceDisk, biosSourceLibrary} {
		path := biosContentPath(source, core, "coleco.rom")

		// The owner gets the bytes, so the refusals below are the gate rather
		// than an absent file.
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, ownerRequest(t, c, "GET", path, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("the owner could not read his own %s firmware: %d", source, rec.Code)
		}
		if rec.Body.Len() != 8192 {
			t.Fatalf("%s firmware served %d bytes, want 8192", source, rec.Body.Len())
		}

		for _, who := range []struct {
			name string
			req  *http.Request
		}{
			{"anonymous", anonRequest("GET", path, "")},
			{"signed-in stranger", strangerRequest(t, c, "GET", path, "")},
		} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, who.req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s reached %s firmware: %d", who.name, source, rec.Code)
			}
			if rec.Body.Len() == 8192 {
				t.Errorf("%s was served the owner's %s firmware bytes", who.name, source)
			}
		}
	}

	// The public source must not have been taken down with them: a visitor who
	// owns nothing still gets a machine that boots.
	rec := httptest.NewRecorder()
	req := anonRequest("GET", biosContentPath(biosSourceArchive, core, "coleco.rom"), "")
	req.Header.Set("Origin", "https://someone-else.example")
	mux.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("the public firmware source stopped answering other origins: %q", got)
	}
}

// --- reporting -------------------------------------------------------------

// An owner-less instance serves nothing and looks, from the front, exactly like
// an empty library or a broken backend. An operator who cannot tell those apart
// reaches for the setting that turns the gate off.
func TestHealthReportsWhetherAnOwnerIsConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth *authConfig
		want string
	}{
		{"configured", ownerAuth(), "configured"},
		{"no owner named", func() *authConfig {
			c := testAuth()
			c.Owner = loadOwnerPolicyFromValues("", "")
			return c
		}(), "not-configured"},
		{"no sign-in", func() *authConfig {
			c := ownerAuth()
			c.Enabled = false
			return c
		}(), "no-sign-in"},
	} {
		s := &server{cache: map[string]cacheEntry{}, auth: tc.auth}
		rec := httptest.NewRecorder()
		s.handleHealth(rec, anonRequest("GET", "/api/health", ""))

		var got map[string]any
		decodeBodyInto(t, rec, &got)
		if got["owner"] != tc.want {
			t.Errorf("%s: health reports owner=%v, want %q", tc.name, got["owner"], tc.want)
		}
		// Never the identity, only whether one exists.
		if strings.Contains(rec.Body.String(), ownerSub) ||
			strings.Contains(rec.Body.String(), ownerAddress) {
			t.Errorf("%s: health disclosed the owner's identity: %s", tc.name, rec.Body.String())
		}
	}
}

// The app needs to know whether to paint owner-only screens at all. This is the
// caller's own status and discloses nothing they could not learn by making one
// request.
func TestAuthMeReportsOwnership(t *testing.T) {
	c := ownerAuth()

	for _, tc := range []struct {
		name string
		req  *http.Request
		want bool
	}{
		{"owner", ownerRequest(t, c, "GET", "/auth/me", ""), true},
		{"stranger", strangerRequest(t, c, "GET", "/auth/me", ""), false},
	} {
		rec := httptest.NewRecorder()
		c.handleMe(rec, tc.req)
		var got map[string]any
		decodeBodyInto(t, rec, &got)
		if got["owner"] != tc.want {
			t.Errorf("%s: /auth/me says owner=%v, want %v", tc.name, got["owner"], tc.want)
		}
	}

	// Anonymous gets the existing 401 and no ownership claim at all.
	rec := httptest.NewRecorder()
	c.handleMe(rec, anonRequest("GET", "/auth/me", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous /auth/me got %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "\"owner\":true") {
		t.Errorf("anonymous /auth/me claimed ownership: %s", rec.Body.String())
	}
}

// The startup log has to name the mechanism, because "owner by email" and
// "owner by subject" have materially different strength -- and must never name
// the value, because a subject id pasted into an issue is a gift.
func TestOwnerPolicyDescriptionNamesTheMechanismNotTheValue(t *testing.T) {
	for _, tc := range []struct {
		sub, email string
		wants      string
	}{
		{"", "", "no owner is configured"},
		{ownerSub, "", "OIDC subject"},
		{"", ownerAddress, "email address"},
		{ownerSub, ownerAddress, "subject or email"},
	} {
		got := loadOwnerPolicyFromValues(tc.sub, tc.email).describe()
		if !strings.Contains(got, tc.wants) {
			t.Errorf("describe() = %q, want it to mention %q", got, tc.wants)
		}
		if tc.sub != "" && strings.Contains(got, tc.sub) {
			t.Errorf("describe() leaked the subject: %q", got)
		}
		if tc.email != "" && strings.Contains(got, tc.email) {
			t.Errorf("describe() leaked the address: %q", got)
		}
	}
}

// The environment is the only source of this configuration.
func TestOwnerPolicyReadsTheEnvironment(t *testing.T) {
	t.Setenv(envOwnerSub, ownerSub)
	t.Setenv(envOwnerEmail, "  WADE@MoveWeight.com , second@example.com ")

	o := loadOwnerPolicy()
	if !o.configured() {
		t.Fatal("the environment was not read")
	}
	if !o.matches(session{Sub: ownerSub}) {
		t.Error("the subject from the environment did not match")
	}
	if !o.matches(session{Email: "second@example.com", EmailOK: true}) {
		t.Error("a comma-separated second address did not match")
	}
	if o.matches(session{Sub: strangerSub, Email: "nobody@example.com", EmailOK: true}) {
		t.Error("an unrelated session matched")
	}
}
