package main

// The per-channel public/private boundary, exercised from both sides.
//
// Every test here asserts one of two things: that a stranger can watch the
// public-domain channels, or that a stranger cannot learn *anything* about a
// channel built from somebody's own library -- not by listing, not by guessing
// its id, not by asking for its bytes. The second half is the one that has to
// keep working, so most of these are written as attacks rather than as
// descriptions.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// linearAccessEngineOwnedBy builds an engine holding one public
// archive.org-style channel and one private library-backed channel, with a
// caller-supplied owner predicate.
//
// SetOwnerFunc is the engine's own seam and the only thing being substituted
// here. owner.go is deliberately left alone: a test hook reaching into the
// canonical owner check -- an override field, an exported setter, an env var
// read only by tests -- would be a bypass on the boundary itself, and one that
// shipped. The engine takes a predicate precisely so that nothing has to.
func linearAccessEngineOwnedBy(t *testing.T, owner linearOwnerFunc) (*LinearEngine, *httptest.Server) {
	t.Helper()
	clk := &linearClock{t: linearEpoch}
	e := NewLinearEngine("")
	e.nowFn = clk.now
	e.pastBuffer = time.Hour
	e.poolTTL = 24 * time.Hour
	e.retryBackoff = 0

	// A public-domain source and a private one, each with its own provider id.
	pub := &linearSliceLibrary{
		provider: archiveLinearProviderID, library: "Cartoons",
		items: linearFixtureItems(),
	}
	e.AddPublicLibrary(pub)
	e.AddLibrary(&linearSliceLibrary{
		provider: "jellyfin", library: "Films", items: linearFixtureItems(),
	})

	public := linearTestChannel("nostalgia-cartoons", StrategyCyclic)
	public.Number = 901
	public.Name = "Nostalgia Cartoons"
	public.SourceProvider = archiveLinearProviderID
	if _, err := e.SaveChannel(public); err != nil {
		t.Fatalf("save public channel: %v", err)
	}
	private := linearTestChannel("wades-films", StrategyCyclic)
	private.Number = 1
	private.Name = "Wade's Films"
	private.SourceProvider = "jellyfin"
	if _, err := e.SaveChannel(private); err != nil {
		t.Fatalf("save private channel: %v", err)
	}

	e.SetOwnerFunc(owner)

	mux := http.NewServeMux()
	e.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return e, srv
}

// linearAccessEngineWith wires the engine to a real auth config's owner check,
// which is what production does.
func linearAccessEngineWith(t *testing.T, c *authConfig) (*LinearEngine, *httptest.Server) {
	t.Helper()
	return linearAccessEngineOwnedBy(t, c.isOwner)
}

// linearAccessEngine is the same thing with the owner decided by an `owner=yes`
// query parameter.
//
// A query parameter stands in for a session in the tests that are about the
// per-channel boundary rather than about sign-in, so that a failure there points
// at the boundary instead of at cookie handling. The tests that are about
// ownership itself drive the real authConfig.isOwner -- see the owner-predicate
// section at the foot of this file, which is what stops the stand-in from
// quietly diverging from the thing it stands in for.
func linearAccessEngine(t *testing.T) (*LinearEngine, *httptest.Server) {
	t.Helper()
	return linearAccessEngineOwnedBy(t, func(r *http.Request) bool {
		return r.URL.Query().Get("owner") == "yes"
	})
}

func linearAccessGet(t *testing.T, url string, into any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

// --- provenance -------------------------------------------------------------

// The rule itself, before any HTTP. A channel is public because of where its
// programmes come from and for no other reason.
func TestLinearChannelIsPublicOnlyForARegisteredPublicProvider(t *testing.T) {
	e := NewLinearEngine("")
	e.AddPublicLibrary(&linearSliceLibrary{provider: archiveLinearProviderID, library: "Cartoons"})
	e.AddLibrary(&linearSliceLibrary{provider: "jellyfin", library: "Films"})

	cases := []struct {
		provider string
		want     bool
		why      string
	}{
		{archiveLinearProviderID, true, "the archive.org source is registered public"},
		{"ARCHIVE-ORG", true, "provider ids are matched case-insensitively"},
		{"jellyfin", false, "a media server is somebody's own library"},
		{"plex", false, "a provider nobody registered is unknown, and unknown is private"},
		{"", false, "a channel with no source at all cannot be public"},
		{"   ", false, "whitespace is not a provider id"},
	}
	for _, tc := range cases {
		ch := LinearChannel{ID: "x", SourceProvider: tc.provider}
		if got := e.channelIsPublic(&ch); got != tc.want {
			t.Errorf("channelIsPublic(%q) = %v, want %v: %s", tc.provider, got, tc.want, tc.why)
		}
	}
}

// The collision case. If a private library ever claims the same provider id as
// a public one -- by accident, or because somebody named their Jellyfin
// "archive-org" -- the answer must be private.
func TestLinearProviderCollisionResolvesToPrivate(t *testing.T) {
	e := NewLinearEngine("")
	e.AddPublicLibrary(&linearSliceLibrary{provider: archiveLinearProviderID, library: "Cartoons"})
	e.AddLibrary(&linearSliceLibrary{provider: archiveLinearProviderID, library: "Wade's Films"})

	ch := LinearChannel{ID: "x", SourceProvider: archiveLinearProviderID}
	if e.channelIsPublic(&ch) {
		t.Fatal("a provider id claimed by both a public and a private library was treated as public; " +
			"one private library under a public id must take the whole id private")
	}
}

// A fresh engine has told nobody anything. Nothing is public and no request is
// the owner's, so a stranger sees an empty world rather than everything.
func TestLinearFreshEngineIsDefaultDeny(t *testing.T) {
	e := NewLinearEngine("")
	e.AddLibrary(linearFixtureLibrary())
	if _, err := e.SaveChannel(linearTestChannel("private", StrategyCyclic)); err != nil {
		t.Fatal(err)
	}
	if ids := e.PublicChannelIDs(); len(ids) != 0 {
		t.Errorf("a fresh engine reports %v as public, want none", ids)
	}
	r := e.withViewer(httptest.NewRequest(http.MethodGet, "/", nil))
	if linearViewerOf(r).owner {
		t.Error("a request was treated as the owner's before any owner was configured")
	}
	if _, ok := e.visibleChannel(r, "private"); ok {
		t.Error("a private channel was visible with no owner configured")
	}
}

// A handler reached without the gate must not assume the best.
func TestLinearUnstampedRequestIsAStranger(t *testing.T) {
	if linearViewerOf(httptest.NewRequest(http.MethodGet, "/", nil)).owner {
		t.Fatal("a request that never passed through the gate was read as the owner's")
	}
}

// --- listing ----------------------------------------------------------------

func TestLinearChannelListShowsStrangersOnlyPublicChannels(t *testing.T) {
	_, srv := linearAccessEngine(t)

	var anon struct {
		Channels []linearChannelRow `json:"channels"`
	}
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"channels", &anon); code != 200 {
		t.Fatalf("anonymous channel list returned %d", code)
	}
	if len(anon.Channels) != 1 {
		t.Fatalf("a stranger saw %d channels, want 1", len(anon.Channels))
	}
	if anon.Channels[0].ID != "nostalgia-cartoons" {
		t.Errorf("a stranger saw %q", anon.Channels[0].ID)
	}
	// The private channel must not be present in any form -- not its id, not
	// its name, not its rule set.
	raw, _ := json.Marshal(anon)
	for _, leak := range []string{"wades-films", "Wade's Films", "jellyfin"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the anonymous channel list contains %q:\n%s", leak, raw)
		}
	}

	var owner struct {
		Channels []linearChannelRow `json:"channels"`
	}
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"channels?owner=yes", &owner); code != 200 {
		t.Fatalf("owner channel list returned %d", code)
	}
	if len(owner.Channels) != 2 {
		t.Fatalf("the owner saw %d channels, want both", len(owner.Channels))
	}
}

// --- guessing an id ---------------------------------------------------------

// The attack this whole design is shaped around: a stranger who already knows
// the id of a private channel must get exactly what somebody typing nonsense
// gets, byte for byte. Anything else makes the id space an oracle.
func TestLinearStrangerGuessingAPrivateIDLearnsNothing(t *testing.T) {
	_, srv := linearAccessEngine(t)

	for _, route := range []string{"now", "stream"} {
		private := srv.URL + linearRoutePrefix + route + "?channel=wades-films"
		invented := srv.URL + linearRoutePrefix + route + "?channel=no-such-channel-at-all"

		pResp, err := http.Get(private)
		if err != nil {
			t.Fatal(err)
		}
		pBody := make(map[string]any)
		_ = json.NewDecoder(pResp.Body).Decode(&pBody)
		pResp.Body.Close()

		iResp, err := http.Get(invented)
		if err != nil {
			t.Fatal(err)
		}
		iBody := make(map[string]any)
		_ = json.NewDecoder(iResp.Body).Decode(&iBody)
		iResp.Body.Close()

		if pResp.StatusCode != http.StatusNotFound {
			t.Errorf("%s for a private channel returned %d, want 404", route, pResp.StatusCode)
		}
		if pResp.StatusCode != iResp.StatusCode {
			t.Errorf("%s: private returned %d but an invented id returned %d; the two must be indistinguishable",
				route, pResp.StatusCode, iResp.StatusCode)
		}
		if pBody["state"] != iBody["state"] {
			t.Errorf("%s: private reports state %v, invented reports %v", route, pBody["state"], iBody["state"])
		}
		// The one difference allowed is the id echoed back, which the caller
		// supplied. Nothing derived from the channel may appear.
		if _, leaked := pBody["program"]; leaked {
			t.Errorf("%s leaked the programme on air on a private channel", route)
		}
		if _, leaked := pBody["source"]; leaked {
			t.Errorf("%s leaked a playable source for a private channel", route)
		}
		if raw, _ := json.Marshal(pBody); strings.Contains(string(raw), "Wade's Films") {
			t.Errorf("%s leaked the private channel's name: %s", route, raw)
		}
	}
}

// The owner asking for the same id gets the real answer, which is what proves
// the tests above are refusing rather than the channel simply being broken.
func TestLinearOwnerSeesThePrivateChannel(t *testing.T) {
	_, srv := linearAccessEngine(t)

	var np LinearNowPlaying
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"now?channel=wades-films&owner=yes", &np); code != 200 {
		t.Fatalf("the owner asking for their own channel got %d", code)
	}
	if np.State != LinearStateOnAir || np.Program == nil {
		t.Fatalf("the owner's channel reports %+v, want something on air", np)
	}
}

// --- the guide --------------------------------------------------------------

func TestLinearGuideNarrowsToWhatTheCallerMaySee(t *testing.T) {
	_, srv := linearAccessEngine(t)

	type guideBody struct {
		Programs []LinearGuideEntry `json:"programs"`
	}

	// Naming the private channel explicitly gets an empty grid, not its
	// listings and not an error.
	var named guideBody
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"guide?channels=wades-films", &named); code != 200 {
		t.Fatalf("guide for a private channel returned %d, want a 200 with nothing in it", code)
	}
	if len(named.Programs) != 0 {
		t.Fatalf("a stranger read %d programmes off a private channel", len(named.Programs))
	}

	// Asking for both gets only the public one.
	var both guideBody
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"guide?channels=wades-films,nostalgia-cartoons", &both); code != 200 {
		t.Fatalf("mixed guide returned %d", code)
	}
	if len(both.Programs) == 0 {
		t.Fatal("the public channel disappeared when asked for alongside a private one")
	}
	for _, p := range both.Programs {
		if p.ChannelID != "nostalgia-cartoons" {
			t.Fatalf("a stranger got a programme on channel %q", p.ChannelID)
		}
	}

	// The dangerous one: no `channels` parameter at all. The engine reads an
	// empty list as "every enabled channel", so this is where a stranger would
	// be handed the whole lineup if the narrowing were done carelessly.
	var all guideBody
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"guide", &all); code != 200 {
		t.Fatalf("unparameterised guide returned %d", code)
	}
	if len(all.Programs) == 0 {
		t.Fatal("an unparameterised guide showed a stranger nothing at all, not even the public channel")
	}
	for _, p := range all.Programs {
		if p.ChannelID != "nostalgia-cartoons" {
			t.Fatalf("an unparameterised guide leaked channel %q to a stranger", p.ChannelID)
		}
	}

	// And the owner still gets everything from the same unparameterised call.
	var ownerAll guideBody
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"guide?owner=yes", &ownerAll); code != 200 {
		t.Fatalf("owner guide returned %d", code)
	}
	seen := map[string]bool{}
	for _, p := range ownerAll.Programs {
		seen[p.ChannelID] = true
	}
	if !seen["wades-films"] || !seen["nostalgia-cartoons"] {
		t.Errorf("the owner's unparameterised guide covered %v, want both channels", seen)
	}
}

// A stranger on an installation with nothing public gets an empty grid. The
// same code path as above, but with the public list empty -- which is where an
// empty slice would be misread as "everything".
func TestLinearGuideWithNoPublicChannelsShowsAStrangerNothing(t *testing.T) {
	clk := &linearClock{t: linearEpoch}
	e := NewLinearEngine("")
	e.nowFn = clk.now
	e.pastBuffer = time.Hour
	e.poolTTL = 24 * time.Hour
	e.AddLibrary(linearFixtureLibrary())
	if _, err := e.SaveChannel(linearTestChannel("private", StrategyCyclic)); err != nil {
		t.Fatal(err)
	}
	e.SetOwnerFunc(func(r *http.Request) bool { return r.URL.Query().Get("owner") == "yes" })

	mux := http.NewServeMux()
	e.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var body struct {
		Programs []LinearGuideEntry `json:"programs"`
	}
	if code := linearAccessGet(t, srv.URL+linearRoutePrefix+"guide", &body); code != 200 {
		t.Fatalf("guide returned %d", code)
	}
	if len(body.Programs) != 0 {
		t.Fatalf("an installation with nothing public showed a stranger %d programmes", len(body.Programs))
	}
}

// --- administration ---------------------------------------------------------

// Preview reads the whole pool before any rule is applied, so answering it for
// a stranger would describe a private library item by item.
func TestLinearPreviewIsOwnerOnly(t *testing.T) {
	_, srv := linearAccessEngine(t)
	body := `{"sourceProvider":"jellyfin","rules":{"match":"all"}}`

	resp, err := http.Post(srv.URL+linearRoutePrefix+"preview", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw := make(map[string]any)
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an anonymous preview returned %d, want 404", resp.StatusCode)
	}
	if _, leaked := raw["totalPool"]; leaked {
		t.Errorf("an anonymous preview reported the size of a private library: %v", raw)
	}

	resp, err = http.Post(srv.URL+linearRoutePrefix+"preview?owner=yes", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("the owner's preview returned %d", resp.StatusCode)
	}
}

// Creating and deleting channels is administration whoever they would belong
// to, so neither is reachable without the owner -- including creating one that
// would have been public.
func TestLinearChannelWritesAreOwnerOnly(t *testing.T) {
	e, srv := linearAccessEngine(t)

	newCh := linearTestChannel("sneaky", StrategyCyclic)
	newCh.Number = 777
	newCh.SourceProvider = archiveLinearProviderID
	body, _ := json.Marshal(newCh)

	resp, err := http.Post(srv.URL+linearRoutePrefix+"channels", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an anonymous channel creation returned %d, want 404", resp.StatusCode)
	}
	if _, exists := e.Channel("sneaky"); exists {
		t.Fatal("a stranger created a channel")
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+linearRoutePrefix+"channels?id=nostalgia-cartoons", nil)
	dresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	dresp.Body.Close()
	if dresp.StatusCode != http.StatusNotFound {
		t.Errorf("an anonymous delete returned %d, want 404", dresp.StatusCode)
	}
	if _, exists := e.Channel("nostalgia-cartoons"); !exists {
		t.Fatal("a stranger deleted a channel")
	}
}

// --- the owner predicate is owner.go's, and only owner.go's -----------------

// linearAuthWithOwner builds a real auth config with a real owner policy, so
// the tests below drive the engine through the same predicate production does
// rather than through a stand-in that could disagree with it.
func linearAuthWithOwner(t *testing.T, subs, emails string) *authConfig {
	t.Helper()
	return &authConfig{
		Enabled: true,
		Secret:  []byte("test-secret"),
		Owner:   loadOwnerPolicyFromValues(subs, emails),
	}
}

func linearSignedRequest(t *testing.T, c *authConfig, s session) *http.Request {
	t.Helper()
	s.Exp = time.Now().Add(time.Hour).Unix()
	value, err := c.encode(s)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
	return r
}

// The regression this file exists to prevent from coming back.
//
// An earlier draft of linear_access.go carried its own owner predicate reading
// its own environment variable, and defaulted to "any valid session is the
// owner" when that variable was unset. It was unset in production, because the
// deployed instance configures YARRIT_OWNER_SUB -- so every other account in
// the directory would have become an owner of Live TV.
//
// The engine now takes authConfig.isOwner, so the only way this can regress is
// if somebody reintroduces a second predicate. This test pins the behaviour
// that a second predicate would change.
func TestLinearOwnershipComesFromTheCanonicalPolicyOnly(t *testing.T) {
	// The deployed shape: an owner named by subject, and other real accounts
	// signed in to the same identity provider.
	c := linearAuthWithOwner(t, "wade-sub-0001", "")

	owner := linearSignedRequest(t, c, session{
		User: "wade", Sub: "wade-sub-0001", Email: "wade@example.com", EmailOK: true,
	})
	if !c.isOwner(owner) {
		t.Fatal("the configured owner was not recognised")
	}

	// Every one of these is a real account on the deployed Authentik. None of
	// them is the owner.
	for _, other := range []string{"joshgraston", "soundslegit", "callum.levystreet", "KIKI"} {
		r := linearSignedRequest(t, c, session{
			User: other, Sub: other + "-sub", Email: other + "@example.com", EmailOK: true,
		})
		if c.isOwner(r) {
			t.Errorf("%q holds a valid session and was treated as the owner of the media library", other)
		}
	}
}

// Unset means nobody, not everybody. This is the exact default the deleted
// predicate got backwards.
func TestLinearOwnershipUnsetMeansNobody(t *testing.T) {
	c := linearAuthWithOwner(t, "", "")
	r := linearSignedRequest(t, c, session{
		User: "wade", Sub: "wade-sub-0001", Email: "wade@example.com", EmailOK: true,
	})
	if c.isOwner(r) {
		t.Fatal("with no owner configured, a signed-in account was treated as the owner")
	}

	// And the engine agrees: with that predicate installed, a library-backed
	// channel is invisible to everybody, including the person at the keyboard.
	e := NewLinearEngine("")
	e.SetOwnerFunc(c.isOwner)
	e.AddLibrary(linearFixtureLibrary())
	if _, err := e.SaveChannel(linearTestChannel("private", StrategyCyclic)); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.visibleChannel(e.withViewer(r), "private"); ok {
		t.Error("a private channel was visible on an instance with no owner configured")
	}
}

// preferred_username is a display name and reassignable, so a rename must not
// hand over the library. owner.go rules it out; this pins that the linear gate
// inherits the rule rather than re-deciding it.
func TestLinearOwnershipIgnoresPreferredUsername(t *testing.T) {
	c := linearAuthWithOwner(t, "wade-sub-0001", "")
	impostor := linearSignedRequest(t, c, session{
		User: "wade", Sub: "someone-else-sub", Email: "someone@example.com", EmailOK: true,
	})
	if c.isOwner(impostor) {
		t.Fatal("an account that merely took the display name \"wade\" was treated as the owner")
	}

	e := NewLinearEngine("")
	e.SetOwnerFunc(c.isOwner)
	e.AddLibrary(linearFixtureLibrary())
	if _, err := e.SaveChannel(linearTestChannel("private", StrategyCyclic)); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.visibleChannel(e.withViewer(impostor), "private"); ok {
		t.Error("a renamed account could see the owner's channel")
	}
}

// An unverified email must not match, or a directory that lets people set their
// own address is an owner-promotion mechanism.
func TestLinearOwnershipRequiresAVerifiedEmail(t *testing.T) {
	c := linearAuthWithOwner(t, "", "wade@example.com")
	unverified := linearSignedRequest(t, c, session{
		User: "impostor", Sub: "x", Email: "wade@example.com", EmailOK: false,
	})
	if c.isOwner(unverified) {
		t.Fatal("an unverified email claim matched the owner address")
	}
	verified := linearSignedRequest(t, c, session{
		User: "wade", Sub: "x", Email: "Wade@Example.com", EmailOK: true,
	})
	if !c.isOwner(verified) {
		t.Error("a verified owner address was refused; the match is case-folded on purpose")
	}
}

// A forged cookie is not a credential.
func TestLinearOwnershipRejectsATamperedSession(t *testing.T) {
	c := linearAuthWithOwner(t, "wade-sub-0001", "")
	good := linearSignedRequest(t, c, session{User: "wade", Sub: "wade-sub-0001"})
	ck, err := good.Cookie(sessionCookie)
	if err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewRequest(http.MethodGet, "/", nil)
	bad.AddCookie(&http.Cookie{Name: sessionCookie, Value: ck.Value + "x"})
	if c.isOwner(bad) {
		t.Fatal("a tampered session cookie was accepted as the owner's")
	}
}

// --- the edge ---------------------------------------------------------------

// Owner responses must not carry a wildcard origin or be cacheable; a
// stranger's copy of the same route is ordinary catalogue data and must stay
// both CORS-open and cacheable, or a television cannot read Nostalgia TV.
func TestLinearEdgeSplitsCORSAndCachingByViewer(t *testing.T) {
	c := linearAuthWithOwner(t, "wade-sub-0001", "")
	e, _ := linearAccessEngineWith(t, c)

	mux := http.NewServeMux()
	linearMux := http.NewServeMux()
	e.RegisterRoutes(linearMux)
	mux.HandleFunc(linearRoutePrefix+"channels", c.linearEdge(linearMux.ServeHTTP))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Stranger.
	resp, err := http.Get(srv.URL + linearRoutePrefix + "channels")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("a stranger's channel list carried Allow-Origin %q; a TV on another origin cannot read it", got)
	}
	if strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Error("public listings were marked no-store")
	}

	// Owner.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+linearRoutePrefix+"channels", nil)
	owner := linearSignedRequest(t, c, session{User: "wade", Sub: "wade-sub-0001"})
	ck, _ := owner.Cookie(sessionCookie)
	req.AddCookie(ck)
	oresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	oresp.Body.Close()
	if got := oresp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("the owner's channel list carried Allow-Origin %q; a wildcard origin must never cover owner data", got)
	}
	if !strings.Contains(oresp.Header.Get("Cache-Control"), "no-store") {
		t.Errorf("the owner's channel list was cacheable: %q", oresp.Header.Get("Cache-Control"))
	}
	vary := strings.Join(oresp.Header.Values("Vary"), ",")
	if !strings.Contains(vary, "Cookie") || !strings.Contains(vary, "Authorization") {
		t.Errorf("the owner's response varies on %q, which is not enough to key a shared cache", vary)
	}
}

// A preflight carries no credentials, so it must be answered as a stranger --
// otherwise a television can never get as far as the real request.
func TestLinearEdgeAnswersPreflight(t *testing.T) {
	c := linearAuthWithOwner(t, "wade-sub-0001", "")
	e, _ := linearAccessEngineWith(t, c)
	mux := http.NewServeMux()
	linearMux := http.NewServeMux()
	e.RegisterRoutes(linearMux)
	mux.HandleFunc(linearRoutePrefix+"guide", c.linearEdge(linearMux.ServeHTTP))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+linearRoutePrefix+"guide", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight returned %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("preflight did not carry a permissive origin")
	}
}
