package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every httptest server listens on 127.0.0.1, and the shipped policy refuses
// loopback. That is not an inconvenience to work around -- it is the policy
// working, so the tests state it explicitly rather than quietly disabling the
// rule they are meant to be testing around.
func testAddonClient() *AddonClient {
	return NewAddonClientWith(addonPolicy{AllowLoopback: true, AllowPrivate: true}, nil)
}

// Real manifests, copied verbatim from what these addons answered over the
// network. Fixtures invented by hand agree with the documentation, which is
// exactly how a parser passes its tests and fails on the wire.
const (
	// Cinemeta: resources as bare strings, catalogs with both the new `extra`
	// and the older `extraSupported`.
	cinemetaManifest = `{"id":"com.linvo.cinemeta","version":"3.0.14",
	 "description":"The official addon for movie and series catalogs","name":"Cinemeta",
	 "resources":["catalog","meta","addon_catalog"],"types":["movie","series"],
	 "idPrefixes":["tt"],
	 "catalogs":[{"type":"movie","id":"top","name":"Popular",
	   "extra":[{"name":"genre","options":["Action"]},{"name":"search"},{"name":"skip"}],
	   "extraSupported":["search","genre","skip"]},
	  {"type":"series","id":"top","name":"Popular","extra":[{"name":"search"}]}]}`

	// Torrentio: resources as OBJECTS with their own types and idPrefixes, and
	// a type list that already goes past the four documented ones.
	torrentioManifest = `{"id":"com.stremio.torrentio.addon","version":"0.0.15","name":"Torrentio",
	 "description":"Provides torrent streams from scraped torrent providers.",
	 "catalogs":[],
	 "resources":[{"name":"stream","types":["movie","series","anime"],"idPrefixes":["tt","kitsu"]}],
	 "types":["movie","series","anime","other"],
	 "behaviorHints":{"configurable":true,"configurationRequired":false}}`

	// OpenSubtitles: the minimum shape, and a resource with no catalogs at all.
	opensubtitlesManifest = `{"id":"org.stremio.opensubtitlesv3","version":"1.0.0","name":"OpenSubtitles v3",
	 "description":"OpenSubtitles v3 Addon for Stremio","catalogs":[],"resources":["subtitles"],
	 "types":["movie","series"],"idPrefixes":["tt"]}`
)

func parseManifest(t *testing.T, raw string) *AddonManifest {
	t.Helper()
	var m AddonManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("manifest did not parse: %v", err)
	}
	return &m
}

// --- the wire format -------------------------------------------------------

// A resource may be a bare string or an object, and both forms are live right
// now. A parser that handles only one silently loses half the ecosystem.
func TestResourcesParseInBothDocumentedForms(t *testing.T) {
	cine := parseManifest(t, cinemetaManifest)
	if !cine.hasResource("catalog") || !cine.hasResource("meta") {
		t.Fatalf("string-form resources were lost: %+v", cine.Resources)
	}

	tor := parseManifest(t, torrentioManifest)
	r, ok := tor.resource("stream")
	if !ok {
		t.Fatal("object-form resource was lost")
	}
	if len(r.Types) != 3 || r.IDPrefixes[1] != "kitsu" {
		t.Fatalf("object-form resource lost its own types/prefixes: %+v", r)
	}
}

// protocol.md's own example wraps the meta in an array. Cinemeta, the addon the
// documentation points at, answers a bare object. Rejecting either would break
// a real addon, so both are read.
func TestMetaResponseAcceptsBothObjectAndArray(t *testing.T) {
	for name, body := range map[string]string{
		"object as Cinemeta sends it": `{"meta":{"id":"tt1254207","type":"movie","name":"Big Buck Bunny"}}`,
		"array as the docs show it":   `{"meta":[{"id":"tt1254207","type":"movie","name":"Big Buck Bunny"}]}`,
	} {
		var resp addonMetaResponse
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if resp.Meta == nil || resp.Meta.Name != "Big Buck Bunny" {
			t.Errorf("%s: meta was dropped", name)
		}
	}
}

// Documented as strings; sent as numbers by enough addons that a type error
// here loses a whole catalog page over a rating.
func TestLooseStringAcceptsNumbersAndStrings(t *testing.T) {
	var m AddonMeta
	raw := `{"id":"x","type":"movie","name":"n","imdbRating":6.8,"year":2008,"releaseInfo":"2000-2014"}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("numeric fields broke decoding: %v", err)
	}
	if m.IMDBRating.String() != "6.8" || m.Year.String() != "2008" {
		t.Fatalf("numbers were not preserved: rating=%q year=%q", m.IMDBRating, m.Year)
	}
	if m.ReleaseInfo.String() != "2000-2014" {
		t.Fatalf("string form was damaged: %q", m.ReleaseInfo)
	}
}

func TestYearIsFoundInWhicheverFieldCarriesIt(t *testing.T) {
	for name, tc := range map[string]struct {
		meta AddonMeta
		want int
	}{
		"year, which is what Cinemeta sends": {AddonMeta{Year: "2008"}, 2008},
		"releaseInfo, which is documented":   {AddonMeta{ReleaseInfo: "1994"}, 1994},
		"a range takes its first year":       {AddonMeta{ReleaseInfo: "2000-2014"}, 2000},
		"an open range still works":          {AddonMeta{ReleaseInfo: "2000-"}, 2000},
		"an ISO timestamp":                   {AddonMeta{Released: "2010-12-06T05:00:00.000Z"}, 2010},
		"nothing at all":                     {AddonMeta{}, 0},
		"a runtime is not a year":            {AddonMeta{Year: "120m"}, 0},
	} {
		if got := addonYear(tc.meta); got != tc.want {
			t.Errorf("%s: got %d want %d", name, got, tc.want)
		}
	}
}

// --- manifest validation ---------------------------------------------------

func TestValidManifestsFromRealAddonsAreAccepted(t *testing.T) {
	for name, raw := range map[string]string{
		"Cinemeta":      cinemetaManifest,
		"Torrentio":     torrentioManifest,
		"OpenSubtitles": opensubtitlesManifest,
	} {
		if err := validateManifest(parseManifest(t, raw)); err != nil {
			t.Errorf("%s was rejected: %v", name, err)
		}
	}
}

func TestManifestValidationRejectsWhatTheProtocolRequires(t *testing.T) {
	for name, raw := range map[string]string{
		"no id":             `{"name":"x","resources":["meta"],"types":["movie"]}`,
		"no name":           `{"id":"x","resources":["meta"],"types":["movie"]}`,
		"no resources":      `{"id":"x","name":"x","resources":[],"types":["movie"]}`,
		"no types anywhere": `{"id":"x","name":"x","resources":["meta"]}`,
	} {
		err := validateManifest(parseManifest(t, raw))
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		if !errors.Is(err, ErrAddonInvalid) {
			t.Errorf("%s: error should be ErrAddonInvalid so health can say 'incompatible', got %v", name, err)
		}
	}
}

// A cosmetic complaint must not stop an install. The user pasted the URL
// because they wanted the addon, not a linting report.
func TestManifestValidationIsNoStricterThanTheSpec(t *testing.T) {
	raw := `{"id":"x","name":"Nonsemver","version":"v2 beta","resources":["stream"],"types":["movie"]}`
	if err := validateManifest(parseManifest(t, raw)); err != nil {
		t.Errorf("a non-semver version blocked an install: %v", err)
	}
}

// --- the type vocabulary ---------------------------------------------------

// The improvement, stated as a test: an addon may serve books, comics, games,
// music or images and land in the right Yarr.It domain, while a stock
// movie/series addon resolves through exactly the same path unchanged.
func TestTypeVocabularyReachesEverySixDomains(t *testing.T) {
	for typ, wantDomain := range map[string]string{
		// Stock Stremio types, which must keep working untouched.
		"movie":  "video",
		"series": "video",
		"tv":     "video",
		"anime":  "video",
		// The extension. None of these needed a new word: schema.json already
		// carries the aliases, so this is a routing change, not a vocabulary one.
		"book":      "literature",
		"audiobook": "literature",
		"comic":     "comic",
		"manga":     "comic",
		"game":      "game",
		"rom":       "game",
		"album":     "music",
		"track":     "music",
		"image":     "image",
		"photo":     "image",
	} {
		if got := addonDomain(typ); got != wantDomain {
			t.Errorf("addonDomain(%q) = %q, want %q", typ, got, wantDomain)
		}
	}
}

// `channel` and `tv` belong to Stremio's protocol and not to Yarr.It's
// vocabulary, so they are translated here rather than added to the frozen
// schema.json.
func TestStremioOwnTypesAreTranslatedNotAdded(t *testing.T) {
	if d, ty := addonDomain("channel"), addonType("channel"); d != "video" || ty != "series" {
		t.Errorf("channel mapped to %q/%q, want video/series", d, ty)
	}
	if d, ty := addonDomain("tv"), addonType("tv"); d != "video" || ty != "live_channel" {
		t.Errorf("tv mapped to %q/%q, want video/live_channel", d, ty)
	}
}

// `other` and `all` mean "unspecified". Guessing a domain for them files a
// random item under a random tab, which is worse than not showing it because
// the user cannot see why it is wrong.
func TestWildcardTypesAreLeftUnplacedRatherThanGuessed(t *testing.T) {
	for _, typ := range []string{"other", "all", ""} {
		if got := addonDomain(typ); got != "" {
			t.Errorf("addonDomain(%q) = %q; a wildcard must not be filed anywhere", typ, got)
		}
	}
}

// An addon whose items simply never appear looks identical to a broken addon.
// Naming the types we cannot place is the difference between a bug report and
// an explanation.
func TestUnsupportedTypesAreReportedNotSwallowed(t *testing.T) {
	m := parseManifest(t, `{"id":"x","name":"Podcatcher","resources":["catalog"],
	 "types":["podcast","movie","other"],"catalogs":[{"type":"podcast","id":"c"}]}`)

	got := unmappedTypes(m)
	if len(got) != 1 || got[0] != "podcast" {
		t.Fatalf("unmappedTypes = %v, want exactly [podcast]", got)
	}
	// `movie` is supported and `other` is a wildcard; neither is a complaint.
	if d := addonDomains(m); len(d) != 1 || d[0] != "video" {
		t.Errorf("addonDomains = %v, want [video] -- the supported type must still work", d)
	}
}

func TestDomainsAreCollectedFromEverywhereAManifestDeclaresThem(t *testing.T) {
	// Torrentio declares its types on the resource, not at the top level only.
	got := addonDomains(parseManifest(t, torrentioManifest))
	if len(got) != 1 || got[0] != "video" {
		t.Fatalf("addonDomains = %v, want [video]", got)
	}
}

// --- the capability bridge -------------------------------------------------

// Advertise only what is implemented, which is provider.go's rule. A feed-only
// catalog answers a search with its unfiltered feed: results that ignore the
// query look like an answer, which is the worst available outcome.
func TestSearchIsClaimedOnlyWhenACatalogActuallySearches(t *testing.T) {
	searchable := parseManifest(t, cinemetaManifest)
	if !addonSupportsSearch(searchable) {
		t.Error("Cinemeta declares a search extra and was reported as unsearchable")
	}
	if !contains(addonCapabilities(searchable), "search") {
		t.Error("search capability missing for an addon that can search")
	}

	feedOnly := parseManifest(t, `{"id":"f","name":"Feed","resources":["catalog"],"types":["movie"],
	 "catalogs":[{"type":"movie","id":"top","name":"Top"}]}`)
	if addonSupportsSearch(feedOnly) {
		t.Error("a feed-only catalog was reported as searchable")
	}
	if contains(addonCapabilities(feedOnly), "search") {
		t.Error("search capability claimed for a catalog that cannot search")
	}
	// It is still discovery -- it just cannot be queried.
	if !contains(addonRoles(feedOnly), "discovery") {
		t.Error("a browsable catalog should still be a discovery provider")
	}
}

// Older addons only carry extraSupported. Cinemeta emits both; something older
// emits only the first, and reading one spelling reports it as feed-only.
func TestLegacyExtraSpellingIsRead(t *testing.T) {
	legacy := parseManifest(t, `{"id":"l","name":"Legacy","resources":["catalog"],"types":["movie"],
	 "catalogs":[{"type":"movie","id":"top","extraSupported":["search"]}]}`)
	if !addonSupportsSearch(legacy) {
		t.Error("extraSupported was ignored; a searchable addon looks feed-only")
	}
}

func TestResourcesMapOntoSchemaRolesAndCapabilities(t *testing.T) {
	streamOnly := parseManifest(t, torrentioManifest)
	roles, caps := addonRoles(streamOnly), addonCapabilities(streamOnly)
	if !contains(roles, "stream") || !contains(caps, "stream") {
		t.Errorf("a stream addon did not claim the stream role/capability: %v %v", roles, caps)
	}
	if contains(caps, "search") {
		t.Error("Torrentio has no catalogs and must not claim search")
	}

	meta := parseManifest(t, cinemetaManifest)
	if !contains(addonCapabilities(meta), "details") {
		t.Error("a meta resource did not become the details capability")
	}

	// Every role and capability claimed must be one schema.json defines, or
	// Registry.Add refuses the provider outright.
	for _, raw := range []string{cinemetaManifest, torrentioManifest, opensubtitlesManifest} {
		m := parseManifest(t, raw)
		for _, role := range addonRoles(m) {
			if _, ok := schema.Roles[role]; !ok {
				t.Errorf("%s claims role %q, which schema.json does not define", m.Name, role)
			}
		}
		for _, c := range addonCapabilities(m) {
			if !contains(schema.Capabilities, c) {
				t.Errorf("%s claims capability %q, which schema.json does not define", m.Name, c)
			}
		}
	}
}

// p2p is the protocol's own warning that a viewer's IP is exposed to a swarm.
// Naming it as the indexer role is what lets a client say so before playing.
func TestP2PAddonsAreMarkedAsIndexers(t *testing.T) {
	m := parseManifest(t, `{"id":"p","name":"Swarm","resources":["stream"],"types":["movie"],
	 "behaviorHints":{"p2p":true}}`)
	if !contains(addonRoles(m), "indexer") {
		t.Errorf("a p2p addon did not claim the indexer role: %v", addonRoles(m))
	}
}

// --- routing ---------------------------------------------------------------

func TestHandlesHonoursIDPrefixesAndTypes(t *testing.T) {
	tor := parseManifest(t, torrentioManifest)

	if !tor.Handles("stream", "movie", "tt1254207") {
		t.Error("a declared type and prefix was refused")
	}
	if !tor.Handles("stream", "anime", "kitsu:1") {
		t.Error("the second declared prefix was refused")
	}
	if tor.Handles("stream", "movie", "yt_id:abc") {
		t.Error("an undeclared id prefix was accepted; that spends a timeout to learn what the manifest already said")
	}
	if tor.Handles("stream", "book", "tt1") {
		t.Error("an undeclared type was accepted")
	}
	if tor.Handles("meta", "movie", "tt1") {
		t.Error("a resource this addon does not serve was accepted")
	}
}

// The protocol is explicit that idPrefixes does not apply to catalogs, which
// are addressed by catalog id rather than by content id.
func TestCatalogsAreExemptFromIDPrefixFiltering(t *testing.T) {
	cine := parseManifest(t, cinemetaManifest)
	if !cine.Handles("catalog", "movie", "top") {
		t.Error("a catalog was filtered out by an id prefix that does not apply to it")
	}
}

// --- URL construction ------------------------------------------------------

func TestResourceURLsUsePathSegmentExtrasWithUnambiguousEncoding(t *testing.T) {
	got := addonResourceURL("https://v3-cinemeta.strem.io/manifest.json", "catalog", "movie", "top",
		url.Values{"search": {"big buck bunny"}})
	want := "https://v3-cinemeta.strem.io/catalog/movie/top/search=big%20buck%20bunny.json"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if strings.Contains(got, "+") {
		t.Error("a space became '+', which is a literal plus inside a path segment")
	}
	if strings.Contains(got, "?") {
		t.Error("extras were put in a query string; the protocol puts them in the path")
	}
}

func TestExtraSegmentIsStableSoAddonCachingWorks(t *testing.T) {
	v := url.Values{"skip": {"100"}, "genre": {"Action"}, "search": {"x"}}
	first := addonExtraSegment(v)
	for i := 0; i < 20; i++ {
		if addonExtraSegment(v) != first {
			t.Fatal("the same request produced two different URLs; the addon's cache can never hit")
		}
	}
	if first != "genre=Action&search=x&skip=100" {
		t.Errorf("unexpected segment %q", first)
	}
}

// Users paste the manifest URL because that is what an install button gives
// them, and configured addons carry settings in the path that must survive.
func TestAddonBaseKeepsConfigurationInThePath(t *testing.T) {
	for raw, want := range map[string]string{
		"https://x.example/manifest.json":  "https://x.example",
		"https://x.example/manifest.json/": "https://x.example",
		"https://x.example":                "https://x.example",
		// Confirmed live: this shape answers 200 with a manifest of its own.
		"https://torrentio.strem.fun/providers=yts/manifest.json": "https://torrentio.strem.fun/providers=yts",
	} {
		if got := addonBase(raw); got != want {
			t.Errorf("addonBase(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Stremio episode ids contain colons ("tt0898266:9:17"). Splitting on every
// colon truncates an episode to its series, and the viewer silently gets the
// wrong streams.
func TestCanonicalIDsSurviveColonsInsideTheItemID(t *testing.T) {
	id := addonCanonicalID("com.stremio.torrentio.addon", "series", "tt0898266:9:17")
	ref, err := parseAddonRef(id)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if ref.AddonID != "com.stremio.torrentio.addon" || ref.Type != "series" || ref.ID != "tt0898266:9:17" {
		t.Fatalf("round trip lost data: %+v", ref)
	}
}

func TestMalformedCanonicalIDsAreRejected(t *testing.T) {
	for _, bad := range []string{"", "tmdb:movie:78", "addon:", "addon:x", "addon:x:y", "addon::series:tt1"} {
		if _, err := parseAddonRef(bad); err == nil {
			t.Errorf("parseAddonRef(%q) was accepted", bad)
		}
	}
}

// --- where an addon may point ----------------------------------------------

func TestOnlyHTTPSchemesAreAccepted(t *testing.T) {
	p := addonPolicy{AllowPrivate: true}
	for _, raw := range []string{
		"file:///etc/passwd",
		"gopher://x.example/",
		"ftp://x.example/manifest.json",
		"x.example/manifest.json", // no scheme at all
	} {
		if _, err := p.checkAddonURL(raw); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
	// IPFS is a real transport in the protocol and genuinely not implemented
	// here, so the refusal should say that rather than "malformed".
	_, err := p.checkAddonURL("ipfs://bafy/manifest.json")
	if err == nil || !strings.Contains(err.Error(), "IPFS") {
		t.Errorf("an ipfs address should be refused by name, got %v", err)
	}
}

// A username and password in the URL would be sent to whatever the host
// resolves to, which is a credential handed to a stranger.
func TestCredentialsInTheURLAreRefused(t *testing.T) {
	p := addonPolicy{AllowPrivate: true}
	if _, err := p.checkAddonURL("https://user:secret@x.example/manifest.json"); err == nil {
		t.Error("userinfo was accepted")
	}
}

// Redis, memcached and Elasticsearch all read an HTTP request as a sequence of
// commands. There is no addon on port 6379.
func TestDangerousPortsAreRefusedEvenOnPublicHosts(t *testing.T) {
	p := addonPolicy{AllowPrivate: true, AllowLoopback: true}
	for _, port := range []int{22, 25, 445, 3306, 5432, 6379, 9200, 11211, 27017} {
		raw := fmt.Sprintf("http://example.com:%d/manifest.json", port)
		if _, err := p.checkAddonURL(raw); err == nil {
			t.Errorf("port %d was accepted", port)
		}
	}
	// Ports an addon really uses must stay open.
	for _, port := range []int{80, 443, 3000, 7000, 8080, 11470} {
		raw := fmt.Sprintf("http://example.com:%d/manifest.json", port)
		if _, err := p.checkAddonURL(raw); err != nil {
			t.Errorf("port %d was refused: %v", port, err)
		}
	}
}

// THE POLICY, STATED. The rule here is not "ban RFC1918" -- this is a LAN
// gateway on purpose and a self-hoster's addon may well be on their LAN. What
// is refused is the server's own surface.
func TestAddressPolicyAllowsTheLANAndRefusesTheServerItself(t *testing.T) {
	shipped := addonPolicy{AllowLoopback: false, AllowPrivate: true}

	allowed := []string{
		"192.168.1.50",  // the ordinary self-hoster
		"10.0.0.8",      // ditto
		"172.16.4.4",    // ditto
		"100.100.1.1",   // CGNAT, which is what Tailscale hands out
		"fd00::1",       // IPv6 unique-local
		"93.184.216.34", // a public addon
	}
	for _, s := range allowed {
		if err := shipped.checkAddonAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s was refused, but a self-hoster's addon lives there: %v", s, err)
		}
	}

	refused := map[string]string{
		"127.0.0.1":        "loopback is the Yarr.It server itself",
		"::1":              "loopback over IPv6",
		"::ffff:127.0.0.1": "loopback wearing an IPv4-mapped disguise",
		"169.254.169.254":  "cloud instance metadata, which hands out credentials",
		"169.254.1.1":      "link-local",
		"fe80::1":          "link-local over IPv6",
		"0.0.0.0":          "the unspecified address",
		"224.0.0.1":        "a multicast group",
	}
	for s, why := range refused {
		if err := shipped.checkAddonAddr(netip.MustParseAddr(s)); err == nil {
			t.Errorf("%s was allowed (%s)", s, why)
		}
	}
}

func TestLoopbackCanBeOptedBackInForSingleBoxInstalls(t *testing.T) {
	if err := (addonPolicy{AllowLoopback: true}).checkAddonAddr(netip.MustParseAddr("127.0.0.1")); err != nil {
		t.Errorf("YARRIT_ADDON_ALLOW_LOOPBACK did not take effect: %v", err)
	}
}

// The knob a public multi-tenant instance must flip, where "the user's LAN" is
// really the operator's.
func TestPrivateAddressesCanBeDeniedWholesale(t *testing.T) {
	p := addonPolicy{AllowPrivate: false}
	if err := p.checkAddonAddr(netip.MustParseAddr("192.168.1.50")); err == nil {
		t.Error("YARRIT_ADDON_PRIVATE=deny did not take effect")
	}
	if err := p.checkAddonAddr(netip.MustParseAddr("93.184.216.34")); err != nil {
		t.Errorf("a public address was refused under deny: %v", err)
	}
}

func TestPolicyIsLoadedFromTheEnvironment(t *testing.T) {
	t.Setenv("YARRIT_ADDON_ALLOW_LOOPBACK", "")
	t.Setenv("YARRIT_ADDON_PRIVATE", "")
	if p := loadAddonPolicy(); p.AllowLoopback || !p.AllowPrivate {
		t.Fatalf("defaults changed: %+v -- loopback must be off and the LAN must be on", p)
	}
	t.Setenv("YARRIT_ADDON_ALLOW_LOOPBACK", "1")
	t.Setenv("YARRIT_ADDON_PRIVATE", "deny")
	if p := loadAddonPolicy(); !p.AllowLoopback || p.AllowPrivate {
		t.Fatalf("environment was ignored: %+v", p)
	}
}

// A budget bounds enumeration without ever getting in a real user's way: a
// person adds one or two addons, a scan needs thousands.
func TestPrivateProbesAreRateLimited(t *testing.T) {
	l := newAddonRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.allow() {
			t.Fatalf("probe %d was refused inside the budget", i)
		}
	}
	if l.allow() {
		t.Fatal("the budget did not stop the fourth probe")
	}

	// The window is a window, not a lifetime ban.
	base := time.Now()
	l.now = func() time.Time { return base.Add(2 * time.Minute) }
	if !l.allow() {
		t.Error("the budget never refilled")
	}
}

func TestRateLimitIsChargedForLiteralPrivateAddresses(t *testing.T) {
	c := NewAddonClientWith(addonPolicy{AllowPrivate: true}, newAddonRateLimiter(2, time.Minute))
	for i := 0; i < 2; i++ {
		if err := c.budgetForPrivate("192.168.1.9"); err != nil {
			t.Fatalf("probe %d refused inside budget: %v", i, err)
		}
	}
	if err := c.budgetForPrivate("192.168.1.9"); err == nil {
		t.Error("scanning the LAN was not bounded")
	}
	// A public host is not charged, so a rate-limited LAN never blocks a normal
	// install from the internet.
	if err := c.budgetForPrivate("v3-cinemeta.strem.io"); err != nil {
		t.Errorf("a public host was refused by the private budget: %v", err)
	}
}

// --- hostile addons --------------------------------------------------------

// An addon that never answers must cost its own request and nothing else.
func TestAnAddonThatNeverAnswersIsBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // held open until the test finishes
	}))
	// Order matters: httptest's Close waits for outstanding handlers, so the
	// handler has to be let go first or the test deadlocks on its own fixture.
	defer srv.Close()
	defer close(release)

	c := testAddonClient()
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.FetchManifest(ctx, srv.URL+"/manifest.json")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a hanging addon appeared to succeed")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("the caller's deadline was ignored; waited %v", elapsed)
	}
	// It must be reported as unreachable, not as a broken manifest: the fix is
	// a network, not a URL.
	if h := addonHealthFromError(err, ""); h.State != HealthUnreachable {
		t.Errorf("health = %q, want unreachable", h.State)
	}
}

// An addon that answers with something that is not an addon.
func TestGarbageIsReportedAsIncompatibleNotUnreachable(t *testing.T) {
	for name, body := range map[string]string{
		"an HTML error page":                "<!doctype html><html><body>502 Bad Gateway</body></html>",
		"valid JSON that is not a manifest": `{"hello":"world"}`,
		"truncated JSON":                    `{"id":"x","name":`,
		"an empty body":                     "",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		}))

		_, err := testAddonClient().FetchManifest(context.Background(), srv.URL+"/manifest.json")
		srv.Close()

		if err == nil {
			t.Errorf("%s was accepted as an addon", name)
			continue
		}
		if !errors.Is(err, ErrAddonInvalid) {
			t.Errorf("%s: got %v, want ErrAddonInvalid", name, err)
		}
		if h := addonHealthFromError(err, ""); h.State != HealthIncompatible {
			t.Errorf("%s: health = %q, want incompatible -- the fix is the URL, not the network", name, h.State)
		}
	}
}

// An addon that answers forever must not be able to exhaust this server's
// memory. The body here is far larger than the cap and is never fully read.
func TestAnEndlessBodyIsRefusedAtTheCap(t *testing.T) {
	var written int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := strings.Repeat("A", 64<<10)
		// Well past the 256 KiB manifest cap; stops when the client hangs up.
		for i := 0; i < 512; i++ {
			n, err := fmt.Fprint(w, chunk)
			atomic.AddInt64(&written, int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, err := testAddonClient().FetchManifest(context.Background(), srv.URL+"/manifest.json")
	if err == nil {
		t.Fatal("a 32 MiB manifest was accepted")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("the refusal should name the size limit, got: %v", err)
	}
	// The read stopped near the cap rather than draining the whole 32 MiB.
	if got := atomic.LoadInt64(&written); got > 8<<20 {
		t.Errorf("read %d bytes; the cap did not stop the transfer early", got)
	}
}

// --- redirects and rebinding -----------------------------------------------

// Redirects are re-checked on every hop, not just cross-host ones: a public
// host redirecting to its own 127.0.0.1 is exactly the move this stops.
func TestRedirectsToRefusedTargetsAreNotFollowed(t *testing.T) {
	var reached int32
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reached, 1)
		fmt.Fprint(w, `{"id":"x","name":"x","resources":["meta"],"types":["movie"]}`)
	}))
	defer victim.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Point at a port the policy refuses outright, on the same host.
		http.Redirect(w, r, "http://127.0.0.1:6379/manifest.json", http.StatusFound)
	}))
	defer redirector.Close()

	c := testAddonClient()
	if _, err := c.FetchManifest(context.Background(), redirector.URL+"/manifest.json"); err == nil {
		t.Fatal("a redirect to a refused port was followed")
	}
	if atomic.LoadInt32(&reached) != 0 {
		t.Error("the redirect target was contacted")
	}
}

func TestRedirectLoopsAreCapped(t *testing.T) {
	var hops int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hops, 1)
		http.Redirect(w, r, srv.URL+"/manifest.json", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := testAddonClient().FetchManifest(context.Background(), srv.URL+"/manifest.json"); err == nil {
		t.Fatal("an infinite redirect loop was followed to completion")
	}
	if n := atomic.LoadInt32(&hops); n > addonMaxRedirects+2 {
		t.Errorf("followed %d hops; the cap is %d", n, addonMaxRedirects)
	}
}

// The rule, tested rather than asserted: never forward the viewer's cookies or
// any provider API key to an addon.
func TestNoCredentialsAreEverSentToAnAddon(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		fmt.Fprint(w, `{"id":"x","name":"x","resources":["meta"],"types":["movie"]}`)
	}))
	defer srv.Close()

	if _, err := testAddonClient().FetchManifest(context.Background(), srv.URL+"/manifest.json"); err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	for _, h := range []string{"Cookie", "Authorization", "X-Api-Key", "Proxy-Authorization"} {
		if v := got.Get(h); v != "" {
			t.Errorf("%s reached the addon with value %q", h, v)
		}
	}
	if ua := got.Get("User-Agent"); !strings.Contains(ua, "Yarr.It") {
		t.Errorf("User-Agent = %q; an addon operator should be able to identify us", ua)
	}
}

// Even across a redirect: Go copies most headers on a same-host hop, so the
// request is rebuilt clean at every one.
func TestHeadersAreNotCarriedAcrossARedirect(t *testing.T) {
	var final http.Header
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/second/manifest.json", http.StatusFound)
	})
	mux.HandleFunc("/second/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		final = r.Header.Clone()
		fmt.Fprint(w, `{"id":"x","name":"x","resources":["meta"],"types":["movie"]}`)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	if _, err := testAddonClient().FetchManifest(context.Background(), srv.URL+"/manifest.json"); err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if final.Get("Cookie") != "" || final.Get("Authorization") != "" {
		t.Errorf("credentials survived a redirect: %v", final)
	}
	if !strings.Contains(final.Get("User-Agent"), "Yarr.It") {
		t.Error("the rebuilt request lost its own headers")
	}
}

// --- health mapping --------------------------------------------------------

// Six distinct states, deliberately not collapsed into "down". Each implies a
// different fix, and "unreachable" sends someone hunting a network fault that a
// wrong URL would have explained in one line.
func TestHealthDistinguishesTheSixStates(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   HealthState
	}{
		"a working addon":     {200, cinemetaManifest, HealthOK},
		"needs a key":         {401, "", HealthAuthFailed},
		"forbidden":           {403, "", HealthAuthFailed},
		"wrong URL":           {404, "", HealthIncompatible},
		"not an addon at all": {200, "<html>hello</html>", HealthIncompatible},
		"the addon is broken": {500, "", HealthUnreachable},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))

		p := newAddonProvider(testAddonClient(), srv.URL+"/manifest.json", parseManifest(t, cinemetaManifest))
		got := p.Health(context.Background())
		srv.Close()

		if got.State != tc.want {
			t.Errorf("%s: health = %q, want %q (detail %q)", name, got.State, tc.want, got.Detail)
		}
		if got.State != HealthOK && got.Detail == "" {
			t.Errorf("%s: an unhealthy state with no detail tells the operator nothing", name)
		}
	}
}

// The protocol already carries the flag that answers this, and it maps exactly
// onto a state schema.json already has. An addon awaiting configuration is not
// broken and must not be reported as such.
func TestConfigurationRequiredMapsOntoNotConfigured(t *testing.T) {
	m := parseManifest(t, `{"id":"c","name":"Needs setup","resources":["stream"],"types":["movie"],
	 "behaviorHints":{"configurationRequired":true}}`)

	// Deliberately given an address that could never answer: the point is that
	// it is never asked.
	p := newAddonProvider(testAddonClient(), "http://127.0.0.1:1/manifest.json", m)
	got := p.Health(context.Background())
	if got.State != HealthNotConfigured {
		t.Fatalf("health = %q, want not_configured", got.State)
	}
	if !strings.Contains(strings.ToLower(got.Detail), "configur") {
		t.Errorf("the detail should say what to do, got %q", got.Detail)
	}
}

// It answers, it is valid, and nothing it serves can be shown here. Degraded is
// the honest word -- usable in part, not usable as advertised.
func TestAnAddonServingOnlyUnsupportedTypesIsDegraded(t *testing.T) {
	body := `{"id":"p","name":"Podcatcher","version":"1.0.0","resources":["catalog"],
	 "types":["podcast"],"catalogs":[{"type":"podcast","id":"c"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	p := newAddonProvider(testAddonClient(), srv.URL+"/manifest.json", parseManifest(t, body))
	got := p.Health(context.Background())
	if got.State != HealthDegraded {
		t.Fatalf("health = %q, want degraded", got.State)
	}
	if !strings.Contains(got.Detail, "podcast") {
		t.Errorf("the detail should name the unsupported type, got %q", got.Detail)
	}
}

// --- the Provider bridge ---------------------------------------------------

// An addon must satisfy the same contract Radarr does, or none of the existing
// fan-out, health and filtering code can see it.
func TestAnAddonRegistersAsAnOrdinaryProvider(t *testing.T) {
	p := newAddonProvider(testAddonClient(), "https://x.example/manifest.json", parseManifest(t, cinemetaManifest))

	r := &Registry{}
	if err := r.Add(p); err != nil {
		t.Fatalf("an addon was refused by the registry: %v", err)
	}
	if got := r.For("video", "discovery"); len(got) != 1 {
		t.Fatalf("the addon did not answer a domain/role query: %v", got)
	}
	if !strings.HasPrefix(p.ID(), "addon:") {
		t.Errorf("provider id %q must be namespaced so ReplaceAddons can find it", p.ID())
	}
}

func TestSearchOnlyAsksCatalogsThatCanSearch(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		fmt.Fprint(w, `{"metas":[{"id":"tt1","type":"movie","name":"Found","poster":"p.png","year":"2008"}]}`)
	}))
	defer srv.Close()

	m := parseManifest(t, `{"id":"m","name":"Mixed","resources":["catalog"],"types":["movie"],
	 "catalogs":[{"type":"movie","id":"searchable","extra":[{"name":"search"}]},
	             {"type":"movie","id":"feedonly"}]}`)

	p := newAddonProvider(testAddonClient(), srv.URL+"/manifest.json", m)
	items, err := p.Search(context.Background(), "dune", "video")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	for _, path := range asked {
		if strings.Contains(path, "feedonly") {
			t.Error("the feed-only catalog was searched; it would answer with its unfiltered feed")
		}
	}
	got := items[0]
	if got.Domain != "video" || got.Type != "movie" || got.Year != 2008 {
		t.Errorf("item was not translated into the canonical shape: %+v", got)
	}
	if !strings.HasPrefix(got.CanonicalID, "addon:m:movie:") {
		t.Errorf("canonical id %q cannot be routed back to the addon", got.CanonicalID)
	}
}

// One sick catalog costs its own results, not the search.
func TestOneBrokenCatalogDoesNotTakeTheOtherWithIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "broken") {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"metas":[{"id":"tt1","type":"movie","name":"Survivor"}]}`)
	}))
	defer srv.Close()

	m := parseManifest(t, `{"id":"m","name":"Mixed","resources":["catalog"],"types":["movie"],
	 "catalogs":[{"type":"movie","id":"broken","extra":[{"name":"search"}]},
	             {"type":"movie","id":"working","extra":[{"name":"search"}]}]}`)

	items, err := newAddonProvider(testAddonClient(), srv.URL+"/manifest.json", m).
		Search(context.Background(), "x", "")
	if err != nil {
		t.Fatalf("one broken catalog failed the whole search: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Survivor" {
		t.Fatalf("the working catalog's results were lost: %+v", items)
	}
}

// A catalog item with no title has nothing to render and nowhere to be filed;
// dropping it beats a blank card that does nothing when clicked.
func TestUnplaceableItemsAreDroppedRatherThanRenderedBlank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"metas":[
		  {"id":"a","type":"movie","name":"Good"},
		  {"id":"b","type":"movie","name":""},
		  {"id":"c","type":"podcast","name":"Unplaceable"}]}`)
	}))
	defer srv.Close()

	m := parseManifest(t, `{"id":"m","name":"M","resources":["catalog"],"types":["movie"],
	 "catalogs":[{"type":"movie","id":"c","extra":[{"name":"search"}]}]}`)

	items, err := newAddonProvider(testAddonClient(), srv.URL+"/manifest.json", m).
		Search(context.Background(), "x", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "Good" {
		t.Fatalf("expected only the renderable item, got %+v", items)
	}
}

// notWebReady is the addon telling us its URL is not something a browser can
// play. Reporting it lets a client transcode instead of failing at the video
// element.
func TestStreamHonoursTheAddonsOwnPlayabilityHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"streams":[
		  {"infoHash":"abc","name":"torrent, not a URL"},
		  {"url":"https://cdn.example/a.mkv","behaviorHints":{"notWebReady":true}}]}`)
	}))
	defer srv.Close()

	m := parseManifest(t, torrentioManifest)
	p := newAddonProvider(testAddonClient(), srv.URL+"/manifest.json", m)

	src, err := p.Stream(context.Background(), "addon:com.stremio.torrentio.addon:movie:tt1254207", StreamOptions{})
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if src.URL != "https://cdn.example/a.mkv" {
		t.Errorf("picked %q; the info-hash entry is not a URL a player can open", src.URL)
	}
	if src.DirectPlay || src.Seekable {
		t.Error("notWebReady was ignored; the client will fail at the video element instead of transcoding")
	}
}

// The manifest already said this addon does not serve that id. Asking anyway
// spends a timeout to learn it.
func TestStreamRefusesIDsTheManifestExcludes(t *testing.T) {
	var called int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		fmt.Fprint(w, `{"streams":[]}`)
	}))
	defer srv.Close()

	p := newAddonProvider(testAddonClient(), srv.URL+"/manifest.json", parseManifest(t, torrentioManifest))
	if _, err := p.Stream(context.Background(), "addon:com.stremio.torrentio.addon:movie:yt_id:abc", StreamOptions{}); err == nil {
		t.Fatal("an id outside the declared prefixes was requested anyway")
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Error("the addon was contacted for an id its manifest excludes")
	}
}

// --- against the real ecosystem --------------------------------------------
//
// Everything above runs against fixtures and httptest servers, which proves the
// code does what it was told and nothing about whether it was told the right
// thing. This one talks to the addons people actually have installed.
//
// Skipped unless YARRIT_ADDON_LIVE=1. A test suite that fails when the network
// is down is a suite people learn to ignore, and the failure it reports is
// never the failure it is named after.
func TestAgainstRealPublicAddons(t *testing.T) {
	if os.Getenv("YARRIT_ADDON_LIVE") != "1" {
		t.Skip("set YARRIT_ADDON_LIVE=1 to check against the live addon ecosystem")
	}
	c := NewAddonClientWith(addonPolicy{AllowPrivate: true}, nil)
	ctx := context.Background()

	// 1. Cinemeta -- the canonical public addon, catalogs and meta.
	cine, err := c.FetchManifest(ctx, "https://v3-cinemeta.strem.io/manifest.json")
	if err != nil {
		t.Fatalf("Cinemeta manifest: %v", err)
	}
	t.Logf("Cinemeta: id=%s v%s resources=%d types=%v catalogs=%d domains=%v roles=%v caps=%v",
		cine.ID, cine.Version, len(cine.Resources), cine.Types, len(cine.Catalogs),
		addonDomains(cine), addonRoles(cine), addonCapabilities(cine))
	if addonDomains(cine)[0] != "video" || !addonSupportsSearch(cine) {
		t.Errorf("Cinemeta was not understood: domains=%v searchable=%v", addonDomains(cine), addonSupportsSearch(cine))
	}

	// A catalog page, searched.
	extra := url.Values{"search": {"big buck bunny"}}
	metas, err := c.Catalog(ctx, "https://v3-cinemeta.strem.io/manifest.json", "movie", "top", extra)
	if err != nil {
		t.Fatalf("Cinemeta catalog: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("Cinemeta returned no results for a search that has results")
	}
	for i, m := range metas {
		if i >= 3 {
			break
		}
		item := m.toMediaItem("addon:"+cine.ID, cine.ID)
		t.Logf("  catalog[%d] %-28q type=%-6s -> domain=%s type=%s year=%d id=%s",
			i, m.Name, m.Type, item.Domain, item.Type, item.Year, item.CanonicalID)
		if item.Domain == "" || item.Title == "" {
			t.Errorf("  catalog[%d] did not translate: %+v", i, item)
		}
	}

	// Meta for one item.
	meta, err := c.Meta(ctx, "https://v3-cinemeta.strem.io/manifest.json", "movie", "tt1254207")
	if err != nil {
		t.Fatalf("Cinemeta meta: %v", err)
	}
	t.Logf("Cinemeta meta: %q (%d) genres=%v runtime=%q rating=%s",
		meta.Name, addonYear(*meta), meta.AllGenres(), meta.Runtime, meta.IMDBRating)
	t.Logf("  raw genre=%v genres=%v -- both are sent, which is why AllGenres dedupes",
		meta.Genre, meta.Genres)
	if meta.Name == "" {
		t.Error("meta came back nameless")
	}

	// 2. Torrentio -- streams, and a manifest whose resources are objects.
	tor, err := c.FetchManifest(ctx, "https://torrentio.strem.fun/manifest.json")
	if err != nil {
		t.Fatalf("Torrentio manifest: %v", err)
	}
	t.Logf("Torrentio: id=%s v%s types=%v domains=%v roles=%v unmapped=%v",
		tor.ID, tor.Version, tor.Types, addonDomains(tor), addonRoles(tor), unmappedTypes(tor))

	streams, err := c.Streams(ctx, "https://torrentio.strem.fun/manifest.json", "movie", "tt1254207")
	if err != nil {
		t.Fatalf("Torrentio streams: %v", err)
	}
	if len(streams) == 0 {
		t.Fatal("Torrentio returned no streams for Big Buck Bunny")
	}
	for i, s := range streams {
		if i >= 3 {
			break
		}
		kind := "url"
		switch {
		case s.InfoHash != "":
			kind = "infoHash"
		case s.YTID != "":
			kind = "ytId"
		case s.ExternalURL != "":
			kind = "externalUrl"
		}
		t.Logf("  stream[%d] %s=%.20s name=%q binge=%q file=%q",
			i, kind, firstNonEmpty(s.InfoHash, s.URL, s.YTID, s.ExternalURL),
			strings.ReplaceAll(s.Name, "\n", " "), s.BehaviorHints.BingeGroup, s.BehaviorHints.Filename)
	}

	// 3. OpenSubtitles -- the resource with no catalogs at all.
	subs, err := c.FetchManifest(ctx, "https://opensubtitles-v3.strem.io/manifest.json")
	if err != nil {
		t.Fatalf("OpenSubtitles manifest: %v", err)
	}
	t.Logf("OpenSubtitles: id=%s resources=%v domains=%v caps=%v",
		subs.ID, subs.Resources, addonDomains(subs), addonCapabilities(subs))

	tracks, err := c.Subtitles(ctx, "https://opensubtitles-v3.strem.io/manifest.json", "movie", "tt1254207", nil)
	if err != nil {
		t.Logf("OpenSubtitles subtitles: %v (reported, not failed -- an empty subtitle set is a normal answer)", err)
	} else {
		t.Logf("OpenSubtitles: %d tracks", len(tracks))
		for i, s := range tracks {
			if i >= 3 {
				break
			}
			t.Logf("  sub[%d] lang=%s id=%s", i, s.Lang, s.ID)
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Measured, not assumed: the live Cinemeta sends the documented `genres` AND
// the undocumented `genre`, carrying the same values. Concatenating them puts
// every genre on a details page twice.
func TestGenreSpellingsAreMergedNotDoubled(t *testing.T) {
	var m AddonMeta
	raw := `{"id":"x","type":"movie","name":"n",
	 "genre":["Animation","Short","Comedy"],
	 "genres":["Animation","Short","Comedy"]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if got := m.AllGenres(); len(got) != 3 {
		t.Fatalf("AllGenres = %v, want three -- both spellings carry the same values", got)
	}

	// Only one spelling present is the other common case.
	m = AddonMeta{Genre: []string{"Thriller"}}
	if got := m.AllGenres(); len(got) != 1 || got[0] != "Thriller" {
		t.Fatalf("the undocumented spelling was lost: %v", got)
	}

	// The first spelling seen survives, so "Sci-Fi" does not become "sci-fi".
	m = AddonMeta{Genres: []string{"Sci-Fi"}, Genre: []string{"sci-fi", " "}}
	if got := m.AllGenres(); len(got) != 1 || got[0] != "Sci-Fi" {
		t.Fatalf("casing or blanks were mishandled: %v", got)
	}
}
