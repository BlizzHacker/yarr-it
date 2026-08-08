package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- helpers ---------------------------------------------------------------

func testAddonAPI(t *testing.T, items ...installedAddon) *addonAPI {
	t.Helper()
	store, err := newAddonStore(filepath.Join(t.TempDir(), "addons.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for _, it := range items {
		if err := store.Add(it); err != nil {
			t.Fatalf("seeding %s: %v", it.AddonID, err)
		}
	}
	return newAddonAPI(store, testAddonClient(), nil)
}

func installed(t *testing.T, url, manifest string, enabled bool) installedAddon {
	t.Helper()
	m := parseManifest(t, manifest)
	return installedAddon{URL: url, AddonID: m.ID, Manifest: m, Enabled: enabled, AddedAt: 1}
}

// signedIn(t, auth, method, url, body) already exists in library_test.go and is
// reused here rather than duplicated: two helpers that mint sessions is one
// helper that can drift out of agreement with the gate it is testing.

func decodeBodyInto(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("response was not JSON (%d): %s", rec.Code, rec.Body.String())
	}
}

// --- the store -------------------------------------------------------------

func TestAddonStoreSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "addons.json")
	s, err := newAddonStore(path)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := s.Add(installed(t, "https://a.example/manifest.json", cinemetaManifest, true)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(installed(t, "https://b.example/manifest.json", torrentioManifest, false)); err != nil {
		t.Fatal(err)
	}

	again, err := newAddonStore(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	got := again.List()
	if len(got) != 2 {
		t.Fatalf("reopened with %d addons, want 2", len(got))
	}
	// Order is the user's stated preference and must survive a restart.
	if got[0].AddonID != "com.linvo.cinemeta" || got[1].AddonID != "com.stremio.torrentio.addon" {
		t.Errorf("order was not preserved: %s, %s", got[0].AddonID, got[1].AddonID)
	}
	if got[1].Enabled {
		t.Error("a disabled addon came back enabled")
	}
}

// The file holds addon URLs, and an addon URL can carry a debrid key. It is a
// credential store whether or not it looks like one.
func TestAddonStoreFileIsNotWorldReadable(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("POSIX permission bits are not meaningful here")
	}
	path := filepath.Join(t.TempDir(), "addons.json")
	s, _ := newAddonStore(path)
	if err := s.Add(installed(t, "https://a.example/manifest.json", cinemetaManifest, true)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 && mode != 0o666 {
		// The second clause tolerates filesystems that do not implement the
		// bits at all rather than failing on Windows for the wrong reason.
		t.Errorf("addon store is mode %o; it holds URLs that can carry keys", mode)
	}
}

// A corrupt file must not stop the service: starting empty loses the list,
// refusing to start loses everything else too.
func TestACorruptStoreDoesNotStopTheServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "addons.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := newAddonStore(path)
	if err == nil {
		t.Error("the corruption should be reported so it reaches a log")
	}
	if s == nil {
		t.Fatal("a usable store must be returned anyway")
	}
	if len(s.List()) != 0 {
		t.Error("garbage was loaded as addons")
	}
}

// Re-pasting a URL is how someone changes the configuration encoded in it.
// Two rows called "Torrentio" that behave differently is how an evening is
// lost.
func TestReaddingAnAddonUpdatesItInPlace(t *testing.T) {
	s, _ := newAddonStore("")
	if err := s.Add(installed(t, "https://torrentio.strem.fun/manifest.json", torrentioManifest, true)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(installed(t, "https://torrentio.strem.fun/providers=yts/manifest.json", torrentioManifest, true)); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("re-adding produced %d rows, want 1", len(got))
	}
	if !strings.Contains(got[0].URL, "providers=yts") {
		t.Errorf("the new configuration was discarded: %s", got[0].URL)
	}
}

// A browser reordering three rows while a phone installs a fourth must not
// delete the fourth.
func TestReorderKeepsAddonsTheClientDidNotName(t *testing.T) {
	s, _ := newAddonStore("")
	for _, id := range []string{"a", "b", "c", "d"} {
		m := fmt.Sprintf(`{"id":%q,"name":%q,"resources":["meta"],"types":["movie"]}`, id, id)
		if err := s.Add(installed(t, "https://"+id+".example/manifest.json", m, true)); err != nil {
			t.Fatal(err)
		}
	}
	s.Reorder([]string{"c", "a"})

	var order []string
	for _, it := range s.List() {
		order = append(order, it.AddonID)
	}
	if len(order) != 4 {
		t.Fatalf("reorder lost an addon: %v", order)
	}
	if order[0] != "c" || order[1] != "a" {
		t.Errorf("named addons were not moved to the front: %v", order)
	}
	if order[2] != "b" || order[3] != "d" {
		t.Errorf("unnamed addons lost their relative order: %v", order)
	}
}

func TestReorderIgnoresUnknownIDs(t *testing.T) {
	s, _ := newAddonStore("")
	if err := s.Add(installed(t, "https://a.example/manifest.json", cinemetaManifest, true)); err != nil {
		t.Fatal(err)
	}
	s.Reorder([]string{"not-installed", "com.linvo.cinemeta"})
	if len(s.List()) != 1 {
		t.Fatal("a phantom id changed the list")
	}
}

// --- redaction -------------------------------------------------------------

// Confirmed on the wire: https://torrentio.strem.fun/providers=yts/manifest.json
// answers 200 with a manifest of its own, which is how a debrid key ends up in
// an addon URL. That makes the URL a credential.
func TestConfiguredAddonURLsAreRedactedForLogs(t *testing.T) {
	raw := "https://torrentio.strem.fun/providers=yts%7Crealdebrid=SECRETKEY/manifest.json"
	got := redactAddonURL(raw)
	if strings.Contains(got, "SECRETKEY") || strings.Contains(got, "realdebrid") {
		t.Fatalf("the key survived redaction: %s", got)
	}
	if !strings.Contains(got, "torrentio.strem.fun") {
		t.Errorf("the host should survive so a log line is still useful: %s", got)
	}

	// An unconfigured URL has nothing to hide and stays legible.
	if got := redactAddonURL("https://v3-cinemeta.strem.io/manifest.json"); got != "https://v3-cinemeta.strem.io/manifest.json" {
		t.Errorf("an unconfigured URL was needlessly redacted: %s", got)
	}
	if redactAddonURL("not a url at all") != "(addon)" {
		t.Error("an unparseable URL must not be echoed back")
	}
}

func TestConfiguredAddonsAreFlaggedSoAUICanWarn(t *testing.T) {
	if !addonIsConfigured("https://torrentio.strem.fun/providers=yts/manifest.json") {
		t.Error("a configured URL was not flagged")
	}
	if addonIsConfigured("https://v3-cinemeta.strem.io/manifest.json") {
		t.Error("a plain URL was flagged as configured")
	}
}

// --- listing ---------------------------------------------------------------

// The rule from providers_api.go, applied here: one sick addon costs a row,
// never the page. A settings screen that will not paint because an addon is
// hanging cannot be used to remove the addon that is hanging.
func TestOneHangingAddonStillLetsThePagePaint(t *testing.T) {
	release := make(chan struct{})
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer dead.Close()
	defer close(release)

	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, cinemetaManifest)
	}))
	defer alive.Close()

	api := testAddonAPI(t,
		installed(t, dead.URL+"/manifest.json", torrentioManifest, true),
		installed(t, alive.URL+"/manifest.json", cinemetaManifest, true),
	)

	rec := httptest.NewRecorder()
	start := time.Now()
	api.handleList(rec, httptest.NewRequest("GET", "/api/addons", nil), "wade")
	elapsed := time.Since(start)

	if rec.Code != 200 {
		t.Fatalf("the page did not paint: %d %s", rec.Code, rec.Body.String())
	}
	if elapsed > addonProbeTimeout+3*time.Second {
		t.Fatalf("the hanging addon serialised the whole page: %v", elapsed)
	}

	var body struct {
		Addons []addonView `json:"addons"`
	}
	decodeBodyInto(t, rec, &body)
	if len(body.Addons) != 2 {
		t.Fatalf("got %d rows, want 2", len(body.Addons))
	}

	var healthy, sick int
	for _, a := range body.Addons {
		switch a.Health.State {
		case HealthOK:
			healthy++
		case HealthUnreachable:
			sick++
		}
	}
	if healthy != 1 || sick != 1 {
		t.Errorf("expected one healthy and one unreachable, got %d/%d", healthy, sick)
	}
}

// Probing something the user has already turned off spends a timeout on a
// question nobody asked, and then reports it unhealthy for not answering.
func TestDisabledAddonsAreNotProbed(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, cinemetaManifest)
	}))
	defer srv.Close()

	api := testAddonAPI(t, installed(t, srv.URL+"/manifest.json", cinemetaManifest, false))
	rec := httptest.NewRecorder()
	api.handleList(rec, httptest.NewRequest("GET", "/api/addons", nil), "wade")

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("a disabled addon was contacted %d times", n)
	}
	var body struct {
		Addons []addonView `json:"addons"`
	}
	decodeBodyInto(t, rec, &body)
	if body.Addons[0].Health.State != HealthNotConfigured {
		t.Errorf("a turned-off addon reported %q; that is not the same as broken", body.Addons[0].Health.State)
	}
}

// The list is what a settings screen renders, so it must carry the same
// vocabulary /api/providers uses or it needs a second renderer.
func TestListCarriesTheSchemaVocabulary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, cinemetaManifest)
	}))
	defer srv.Close()

	api := testAddonAPI(t, installed(t, srv.URL+"/manifest.json", cinemetaManifest, true))
	rec := httptest.NewRecorder()
	api.handleList(rec, httptest.NewRequest("GET", "/api/addons", nil), "wade")

	var body struct {
		Addons  []addonView `json:"addons"`
		Domains []string    `json:"domains"`
	}
	decodeBodyInto(t, rec, &body)

	a := body.Addons[0]
	if len(a.Domains) == 0 || a.Domains[0] != "video" {
		t.Errorf("domains = %v", a.Domains)
	}
	if !contains(a.Roles, "discovery") || !contains(a.Capabilities, "search") {
		t.Errorf("roles/capabilities were not bridged: %v %v", a.Roles, a.Capabilities)
	}
	if !a.Searchable || a.Catalogs != 2 {
		t.Errorf("catalog summary is wrong: searchable=%v catalogs=%d", a.Searchable, a.Catalogs)
	}
	if len(body.Domains) != 1 || body.Domains[0] != "video" {
		t.Errorf("top-level domains = %v; a client uses this to pick which tabs to show", body.Domains)
	}
}

// --- installing ------------------------------------------------------------

func TestInstallingAnAddonStoresItAndReportsWhatItProvides(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, cinemetaManifest)
	}))
	defer srv.Close()

	api := testAddonAPI(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/addons", strings.NewReader(`{"url":"`+srv.URL+`/manifest.json"}`))
	api.handleAdd(rec, req, "wade")

	if rec.Code != 200 {
		t.Fatalf("install failed: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Addon addonView `json:"addon"`
	}
	decodeBodyInto(t, rec, &body)
	if body.Addon.ID != "com.linvo.cinemeta" || body.Addon.Name != "Cinemeta" {
		t.Errorf("the manifest was not read back: %+v", body.Addon)
	}
	if !body.Addon.Enabled {
		t.Error("a freshly installed addon should be on")
	}
	if len(api.store.List()) != 1 {
		t.Error("the addon was not stored")
	}
}

// A user pasting a URL is the person who can fix it, so the refusal has to say
// what is wrong. "Could not add addon" tells them nothing.
func TestInstallRefusalsExplainThemselves(t *testing.T) {
	api := testAddonAPI(t)

	for name, tc := range map[string]struct {
		url  string
		want string
	}{
		"a file path":            {"file:///etc/passwd", "http"},
		"credentials in the URL": {"https://u:p@x.example/manifest.json", "username"},
		"a database port":        {"http://192.168.1.5:6379/manifest.json", "Redis"},
		"cloud metadata":         {"http://169.254.169.254/manifest.json", "Link-local"},
		"an IPFS addon":          {"ipfs://bafy/manifest.json", "IPFS"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/addons", strings.NewReader(`{"url":"`+tc.url+`"}`))
		api.handleAdd(rec, req, "wade")

		if rec.Code == 200 {
			t.Errorf("%s was installed", name)
			continue
		}
		var body struct {
			Error string `json:"error"`
		}
		decodeBodyInto(t, rec, &body)
		if !strings.Contains(body.Error, tc.want) {
			t.Errorf("%s: message %q does not mention %q", name, body.Error, tc.want)
		}
	}
	if len(api.store.List()) != 0 {
		t.Error("a refused addon was stored anyway")
	}
}

// Loopback is refused by the shipped policy, and the message must say what to
// do about it rather than just "no".
func TestLoopbackRefusalNamesTheEscapeHatch(t *testing.T) {
	api := newAddonAPI(mustStore(t), NewAddonClientWith(addonPolicy{AllowPrivate: true}, nil), nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/addons", strings.NewReader(`{"url":"http://127.0.0.1:7000/manifest.json"}`))
	api.handleAdd(rec, req, "wade")

	if rec.Code == 200 {
		t.Fatal("loopback was accepted under the shipped policy")
	}
	var body struct {
		Error string `json:"error"`
	}
	decodeBodyInto(t, rec, &body)
	if !strings.Contains(body.Error, "YARRIT_ADDON_ALLOW_LOOPBACK") {
		t.Errorf("the refusal does not tell a single-box self-hoster how to proceed: %q", body.Error)
	}
}

func mustStore(t *testing.T) *addonStore {
	t.Helper()
	s, err := newAddonStore(filepath.Join(t.TempDir(), "addons.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// --- searching -------------------------------------------------------------

// "No results" and "we could not ask" must never look the same. A short list
// that is silently short is worse than an error, because it reads as an answer.
func TestSearchNamesTheAddonsThatCouldNotAnswer(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"metas":[{"id":"tt1","type":"movie","name":"Found","year":"2001"}]}`)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()

	goodManifest := `{"id":"good","name":"Good","resources":["catalog"],"types":["movie"],
	 "catalogs":[{"type":"movie","id":"c","extra":[{"name":"search"}]}]}`
	badManifest := `{"id":"bad","name":"Bad","resources":["catalog"],"types":["movie"],
	 "catalogs":[{"type":"movie","id":"c","extra":[{"name":"search"}]}]}`

	api := testAddonAPI(t,
		installed(t, good.URL+"/manifest.json", goodManifest, true),
		installed(t, bad.URL+"/manifest.json", badManifest, true),
	)

	rec := httptest.NewRecorder()
	api.handleSearch(rec, httptest.NewRequest("GET", "/api/addons/search?q=dune", nil))
	if rec.Code != 200 {
		t.Fatalf("one broken addon failed the whole search: %d", rec.Code)
	}

	var body addonSearchResult
	decodeBodyInto(t, rec, &body)
	if len(body.Items) != 1 || body.Items[0].Title != "Found" {
		t.Fatalf("the working addon's results were lost: %+v", body.Items)
	}
	if len(body.Failed) != 1 || body.Failed[0].ID != "bad" {
		t.Fatalf("the broken addon was not reported: %+v", body.Failed)
	}
	if body.Failed[0].Detail == "" {
		t.Error("a failure with no reason cannot be acted on")
	}
}

// Asked, not assumed: an addon with no searchable catalog is skipped rather
// than queried and reported as failing.
func TestSearchSkipsAddonsThatCannotSearch(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{"metas":[]}`)
	}))
	defer srv.Close()

	api := testAddonAPI(t,
		installed(t, srv.URL+"/manifest.json", torrentioManifest, true),     // stream only
		installed(t, srv.URL+"/manifest.json", opensubtitlesManifest, true), // subtitles only
	)
	rec := httptest.NewRecorder()
	api.handleSearch(rec, httptest.NewRequest("GET", "/api/addons/search?q=x", nil))

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("addons with no catalog were queried %d times", n)
	}
	var body addonSearchResult
	decodeBodyInto(t, rec, &body)
	if len(body.Failed) != 0 {
		t.Errorf("skipping is not failing: %+v", body.Failed)
	}
}

// The domain filter has to work through the canonical vocabulary, or a books
// addon answers a video search.
func TestSearchRespectsTheDomainFilter(t *testing.T) {
	var asked int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asked, 1)
		fmt.Fprint(w, `{"metas":[]}`)
	}))
	defer srv.Close()

	books := `{"id":"books","name":"Books","resources":["catalog"],"types":["book"],
	 "catalogs":[{"type":"book","id":"c","extra":[{"name":"search"}]}]}`
	api := testAddonAPI(t, installed(t, srv.URL+"/manifest.json", books, true))

	rec := httptest.NewRecorder()
	api.handleSearch(rec, httptest.NewRequest("GET", "/api/addons/search?q=x&domain=movies", nil))
	if n := atomic.LoadInt32(&asked); n != 0 {
		t.Errorf("a books addon was asked a video question %d times", n)
	}

	// And the alias resolves: "ebooks" is literature, and this addon serves it.
	rec = httptest.NewRecorder()
	api.handleSearch(rec, httptest.NewRequest("GET", "/api/addons/search?q=x&domain=ebooks", nil))
	if n := atomic.LoadInt32(&asked); n != 1 {
		t.Errorf("the literature alias did not reach the books addon (asked %d times)", n)
	}
}

// --- resolving one item ----------------------------------------------------

func TestStreamRefusesItemsFromAddonsThatAreNotInstalled(t *testing.T) {
	api := testAddonAPI(t)
	rec := httptest.NewRecorder()
	api.handleStream(rec, httptest.NewRequest("GET", "/api/addons/stream?id=addon:ghost:movie:tt1", nil), "wade")
	if rec.Code != 404 {
		t.Errorf("got %d, want 404 for an addon that is not installed", rec.Code)
	}
}

func TestStreamRefusesItemsFromDisabledAddons(t *testing.T) {
	api := testAddonAPI(t, installed(t, "https://x.example/manifest.json", torrentioManifest, false))
	rec := httptest.NewRecorder()
	api.handleStream(rec, httptest.NewRequest("GET",
		"/api/addons/stream?id=addon:com.stremio.torrentio.addon:movie:tt1", nil), "wade")
	if rec.Code != 409 {
		t.Errorf("got %d, want 409 -- turned off is not the same as missing", rec.Code)
	}
}

// The protocol's own P2P warning, carried to the point of decision rather than
// left on a settings screen nobody rereads.
func TestStreamRepeatsTheP2PWarningAtPlaybackTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"streams":[{"url":"https://cdn.example/a.mp4"}]}`)
	}))
	defer srv.Close()

	m := `{"id":"swarm","name":"Swarm","resources":["stream"],"types":["movie"],
	 "behaviorHints":{"p2p":true}}`
	api := testAddonAPI(t, installed(t, srv.URL+"/manifest.json", m, true))

	rec := httptest.NewRecorder()
	api.handleStream(rec, httptest.NewRequest("GET", "/api/addons/stream?id=addon:swarm:movie:tt1", nil), "wade")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		P2P bool `json:"p2p"`
	}
	decodeBodyInto(t, rec, &body)
	if !body.P2P {
		t.Error("the p2p warning did not reach the point where a viewer decides to play")
	}
}

// --- the registry bridge ---------------------------------------------------

// The whole point of the bridge: an addon lands in the same registry as Radarr,
// so /api/providers, the health probe and every domain/role query see it with
// no code in those places knowing addons exist.
func TestEnabledAddonsAppearInTheProviderRegistry(t *testing.T) {
	reg := &Registry{}
	if err := reg.Add(radarrLike()); err != nil {
		t.Fatal(err)
	}

	api := testAddonAPI(t,
		installed(t, "https://a.example/manifest.json", cinemetaManifest, true),
		installed(t, "https://b.example/manifest.json", torrentioManifest, false),
	)
	api.reg = reg
	api.syncRegistry()

	var ids []string
	for _, p := range reg.All() {
		ids = append(ids, p.ID())
	}
	if !contains(ids, "radarr") {
		t.Errorf("syncing addons removed a real provider: %v", ids)
	}
	if !contains(ids, "addon:com.linvo.cinemeta") {
		t.Errorf("the enabled addon did not register: %v", ids)
	}
	if contains(ids, "addon:com.stremio.torrentio.addon") {
		t.Errorf("a disabled addon registered anyway: %v", ids)
	}

	// Turning it on puts it there, turning it off takes it away, and Radarr
	// never moves.
	api.store.SetEnabled("com.stremio.torrentio.addon", true)
	api.syncRegistry()
	if len(reg.For("video", "stream")) != 1 {
		t.Error("the newly enabled addon did not answer a role query")
	}
	api.store.Remove("com.linvo.cinemeta")
	api.syncRegistry()
	for _, p := range reg.All() {
		if p.ID() == "addon:com.linvo.cinemeta" {
			t.Error("a removed addon is still registered")
		}
	}
	if len(reg.All()) != 2 {
		t.Errorf("registry has %d providers, want radarr + torrentio", len(reg.All()))
	}
}

// --- route gating ----------------------------------------------------------

// The route that makes this server fetch a user-supplied URL is the one an
// anonymous caller must never reach. This is the exact trap routes_test.go
// documents: a convenience registrar that leaves handlers bare.
func TestAddonRoutesRefuseAnonymousCallers(t *testing.T) {
	mux := http.NewServeMux()
	registerAddonRoutesWith(mux, testAuth(), nil)

	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/addons", ""},
		{"POST", "/api/addons", `{"url":"http://169.254.169.254/manifest.json"}`},
		{"DELETE", "/api/addons?id=x", ""},
		{"POST", "/api/addons/remove", `{"id":"x"}`},
		{"POST", "/api/addons/enabled", `{"id":"x","enabled":false}`},
		{"POST", "/api/addons/order", `{"ids":["x"]}`},
		{"GET", "/api/addons/stream?id=addon:x:movie:tt1", ""},
		{"GET", "/api/addons/subtitles?id=addon:x:movie:tt1", ""},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))

		if rec.Code == http.StatusOK {
			t.Errorf("%s %s answered 200 with no session", tc.method, tc.path)
		}
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s returned %d, want 401 or 503", tc.method, tc.path, rec.Code)
		}
	}
}

// Catalogue data is read by a television from another origin, exactly like
// /api/search.
func TestAddonCatalogueRoutesAreReadableCrossOrigin(t *testing.T) {
	mux := http.NewServeMux()
	registerAddonRoutesWith(mux, testAuth(), nil)

	for _, path := range []string{"/api/addons/search?q=dune", "/api/addons/meta?id=addon:x:movie:tt1"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Origin", "https://someone-else.example")
		mux.ServeHTTP(rec, req)

		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s needs a session; a TV cannot browse", path)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s sent Allow-Origin %q; a TV is a different origin", path, got)
		}
	}
}

// A configured debrid addon answers stream lookups with links bound to a paid
// account. Those must never carry CORS headers, because a wildcard origin plus
// credentials is what the browser refuses on the household's behalf.
func TestAddonPlaybackRoutesAreNotCORSOpen(t *testing.T) {
	mux := http.NewServeMux()
	registerAddonRoutesWith(mux, testAuth(), nil)

	for _, path := range []string{"/api/addons", "/api/addons/stream", "/api/addons/subtitles"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Origin", "https://someone-else.example")
		mux.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s sent Allow-Origin %q; household data must stay same-origin", path, got)
		}
	}
}

// registerAddonRoutes is the one-line form main.go calls. It must apply the
// same gates -- a helper that registers the add route bare is a helper that
// hands out an SSRF probe.
func TestTheOneLineRegistrarStillGatesTheAddRoute(t *testing.T) {
	t.Setenv("SSO_CLIENT_ID", "x")
	t.Setenv("SSO_CLIENT_SECRET", "y")
	t.Setenv("SESSION_SECRET", "test-secret")

	mux := http.NewServeMux()
	registerAddonRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/addons",
		strings.NewReader(`{"url":"http://169.254.169.254/manifest.json"}`)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("the one-line registrar left the add route open: %d", rec.Code)
	}
}

// And with a session it works, which is the other half of the same claim.
func TestASignedInCallerReachesTheAddonList(t *testing.T) {
	auth := testAuth()
	mux := http.NewServeMux()
	registerAddonRoutesWith(mux, auth, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, signedIn(t, auth, "GET", "/api/addons", ""))

	if rec.Code != 200 {
		t.Fatalf("a signed-in caller was refused: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Addons []addonView    `json:"addons"`
		Limits map[string]any `json:"limits"`
	}
	decodeBodyInto(t, rec, &body)
	if body.Limits["maxInstalled"] == nil {
		t.Error("the limits a client should respect were not reported")
	}
}
