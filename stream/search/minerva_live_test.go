package main

// Table-driven tests for the live Minerva search API client.
//
// Every network test runs against httptest: the real API takes 20-25s per
// query and sits behind Cloudflare, so a test suite that touched it would be
// both slow and flaky. What is pinned here is the contract this client
// depends on -- the wire shapes verified against the live site on
// 2026-10-14 -- and the join rules that keep a file hit from becoming a card
// without a size.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ------------------------------------------------------- URL construction --

func TestMinervaLiveQueryValues(t *testing.T) {
	cases := []struct {
		name string
		q    minervaLiveQuery
		want url.Values
	}{
		{
			name: "bare query lowercases and sets nothing else",
			q:    minervaLiveQuery{Query: "Chrono Trigger"},
			want: url.Values{"query": {"chrono trigger"}},
		},
		{
			name: "every filter the site's own client can send",
			q: minervaLiveQuery{
				Query: "zelda", Offset: 200, Tokenised: true,
				Group: "No-Intro", Console: "Nintendo SNES", Region: "US",
				Manufacturer: "Nintendo", MinSizeBytes: 1024, MaxSizeBytes: 1 << 30,
			},
			want: url.Values{
				"query": {"zelda"}, "offset": {"200"}, "tokenised": {"true"},
				"group": {"No-Intro"}, "console": {"Nintendo SNES"}, "region": {"US"},
				"manufacturer": {"Nintendo"}, "min_size": {"1024"}, "max_size": {"1073741824"},
			},
		},
		{
			name: "zero size bounds and blank filters are omitted, not sent empty",
			q:    minervaLiveQuery{Query: "mario", Group: "  ", MinSizeBytes: 0, MaxSizeBytes: 0},
			want: url.Values{"query": {"mario"}},
		},
		{
			name: "negative size bounds are omitted",
			q:    minervaLiveQuery{Query: "mario", MinSizeBytes: -5, MaxSizeBytes: -1},
			want: url.Values{"query": {"mario"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.q.values()
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got.Get(k) != v[0] {
					t.Errorf("%s: got %q, want %q", k, got.Get(k), v[0])
				}
			}
		})
	}
}

// --------------------------------------------------------- search parsing --

func TestMinervaLiveSearch(t *testing.T) {
	rows := []minervaLiveRow{
		{ID: 58881, FullPath: "./TOSEC/Texas Instruments/TI-83/Games/Chrono Trigger.zip",
			Magnet: "magnet:?xt=urn:btih:3b163b6fe74f31cd586e98dda1b8f8477047cf09&dn=Minerva_Myrient", SoID: 4831},
	}
	var gotUA, gotPath, gotQuery string
	var gotTokenised string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		gotTokenised = r.URL.Query().Get("tokenised")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()

	c := &minervaLiveClient{base: srv.URL, http: srv.Client()}
	out, hasMore, err := c.search(context.Background(), minervaLiveQuery{Query: "chrono trigger", Tokenised: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(out) != 1 || out[0].ID != 58881 || out[0].SoID != 4831 {
		t.Fatalf("rows: %+v", out)
	}
	if out[0].FullPath != rows[0].FullPath || out[0].Magnet != rows[0].Magnet {
		t.Fatalf("row fields not decoded: %+v", out[0])
	}
	if hasMore {
		t.Error("1 row must not signal another page")
	}
	if gotPath != "/v1/api/rom/search" {
		t.Errorf("path: %s", gotPath)
	}
	if gotQuery != "chrono trigger" {
		t.Errorf("query: %s", gotQuery)
	}
	if gotTokenised != "true" {
		t.Errorf("tokenised: %s", gotTokenised)
	}
	// The one header that decides whether Cloudflare answers at all.
	if !strings.Contains(gotUA, "Mozilla/5.0") {
		t.Errorf("browser UA not sent: %q", gotUA)
	}
}

func TestMinervaLiveSearchPaginationSignal(t *testing.T) {
	// The site's own client reads len(rows) > 100 as "another page exists":
	// the API sends 101 rows when there is more. Pinned because it is a
	// contract nobody would guess from the docs.
	mk := func(n int) []minervaLiveRow {
		out := make([]minervaLiveRow, n)
		for i := range out {
			out[i] = minervaLiveRow{ID: int64(i), FullPath: "x", Magnet: "magnet:?xt=urn:btih:aaaa"}
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		n        int
		wantMore bool
		wantKept int
	}{
		{"100 rows is exactly one page", 100, false, 100},
		{"101 rows means another page and the extra is trimmed", 101, true, 100},
		{"empty answer", 0, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(mk(tc.n))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(body)
			}))
			defer srv.Close()
			c := &minervaLiveClient{base: srv.URL, http: srv.Client()}
			rows, more, err := c.search(context.Background(), minervaLiveQuery{Query: "q"})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if more != tc.wantMore {
				t.Errorf("hasMore=%v want %v", more, tc.wantMore)
			}
			if len(rows) != tc.wantKept {
				t.Errorf("kept %d rows, want %d", len(rows), tc.wantKept)
			}
		})
	}
}

func TestMinervaLiveSearchErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	c := &minervaLiveClient{base: srv.URL, http: srv.Client()}
	_, _, err := c.search(context.Background(), minervaLiveQuery{Query: "q"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want a 403 error, got %v", err)
	}
}

func TestMinervaLiveSearchEmptyQuery(t *testing.T) {
	c := &minervaLiveClient{base: "http://example.invalid", http: http.DefaultClient}
	rows, more, err := c.search(context.Background(), minervaLiveQuery{Query: "   "})
	if err != nil || rows != nil || more {
		t.Fatalf("an empty query must ask nothing: %v %v %v", rows, more, err)
	}
}

// --------------------------------------------------------- hash extraction --

func TestMinervaLiveInfoHash(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"magnet:?xt=urn:btih:3B163B6FE74F31CD586E98DDA1B8F8477047CF09&dn=Minerva_Myrient",
			"3b163b6fe74f31cd586e98dda1b8f8477047cf09"},
		{"magnet:?xt=urn:btih:3b163b6fe74f31cd586e98dda1b8f8477047cf09", "3b163b6fe74f31cd586e98dda1b8f8477047cf09"},
		{"magnet:?xt=urn:btih:short&dn=x", ""},
		{"magnet:?xt=urn:btih:zz163b6fe74f31cd586e98dda1b8f8477047cf09", ""},
		{"https://example.com/file.zip", ""},
		{"", ""},
	} {
		if got := minervaLiveInfoHash(tc.in); got != tc.want {
			t.Errorf("minervaLiveInfoHash(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ------------------------------------------------------- detail parsing --

func TestParseMinervaDetail(t *testing.T) {
	page := `<html><body><script>window.rom = {"id":58881,"full_path":"./TOSEC/Texas Instruments/TI-83/Games/Chrono Trigger v0.1 (demo).zip","file_name":"Chrono Trigger v0.1 (demo).zip","size":1476,"md5":"a37a","torrents":"Minerva_Myrient_v0.3/x.torrent","magnet":"magnet:?xt=urn:btih:3b16&dn=Minerva_Myrient","region":"World","console":"Texas Instruments TI-83","group":"TOSEC","so_id":4831};</script></body></html>`
	d, err := parseMinervaDetail(page)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.ID != 58881 || d.Size != 1476 || d.Group != "TOSEC" || d.Console != "Texas Instruments TI-83" || d.SoID != 4831 {
		t.Fatalf("detail: %+v", d)
	}
	if !strings.Contains(d.FileName, "Chrono Trigger") {
		t.Fatalf("file name: %s", d.FileName)
	}

	// A file name containing braces and quotes must not break the scan.
	tricky := `<script>window.rom = {"id":1,"file_name":"weird {}} \" name.zip","size":10};</script>`
	d2, err := parseMinervaDetail(tricky)
	if err != nil {
		t.Fatalf("tricky parse: %v", err)
	}
	if d2.Size != 10 || !strings.Contains(d2.FileName, "{") {
		t.Fatalf("tricky detail: %+v", d2)
	}

	for _, bad := range []string{"", "<html>nothing</html>", `<script>window.rom = {"id":1,` /* unclosed */} {
		if _, err := parseMinervaDetail(bad); err == nil {
			t.Errorf("parseMinervaDetail(%q) must fail", bad)
		}
	}
}

// -------------------------------------------------------- group detection --

func TestDetectMinervaGroup(t *testing.T) {
	for _, tc := range []struct{ q, group, rest string }{
		{"zelda", "", "zelda"},
		{"no-intro zelda", "No-Intro", "zelda"},
		{"zelda no-intro", "No-Intro", "zelda"},
		{"NO-INTRO zelda", "No-Intro", "zelda"},
		{"total dos collection doom", "Total DOS Collection", "doom"},
		{"doom total dos collection", "Total DOS Collection", "doom"},
		{"No-Intro", "No-Intro", "No-Intro"}, // nothing left: keep the full query
		{"mame", "MAME", "mame"},
		{"not a collection", "", "not a collection"},
	} {
		g, r := detectMinervaGroup(tc.q)
		if g != tc.group || strings.TrimSpace(r) != strings.TrimSpace(tc.rest) {
			t.Errorf("detectMinervaGroup(%q) = (%q,%q), want (%q,%q)", tc.q, g, r, tc.group, tc.rest)
		}
	}
}

// --------------------------------------------------- the join, end to end --

func TestMinervaLiveSearchCards(t *testing.T) {
	store := minervaStoreWith(t, []string{"udp://tracker.example:1337/announce"},
		snesSet(),
		minervaEntry{
			InfoHash:   "3b163b6fe74f31cd586e98dda1b8f8477047cf09",
			Path:       "TOSEC/Texas Instruments",
			Collection: "TOSEC",
			Name:       "Texas Instruments",
			SizeBytes:  163_263_283,
		},
	)

	// Two files inside the SAME torrent plus one file in a torrent the
	// catalogue has never heard of. The join must produce ONE card for the
	// known hash, with Contains naming the first (best-ranked) file, and must
	// count the unknown one rather than carding it without a size.
	rows := []minervaLiveRow{
		{ID: 58881, FullPath: "./TOSEC/Texas Instruments/TI-83/Games/Chrono Trigger.zip",
			Magnet: "magnet:?xt=urn:btih:3b163b6fe74f31cd586e98dda1b8f8477047cf09&dn=Minerva_Myrient"},
		{ID: 58882, FullPath: "./TOSEC/Texas Instruments/TI-83/Games/Chrono Trigger (alt).zip",
			Magnet: "magnet:?xt=urn:btih:3b163b6fe74f31cd586e98dda1b8f8477047cf09&dn=Minerva_Myrient"},
		{ID: 99999, FullPath: "./Unknown/Future Collection/game.zip",
			Magnet: "magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff&dn=Minerva_Myrient"},
	}
	detailCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/api/rom/search":
			json.NewEncoder(w).Encode(rows)
		case r.URL.Path == "/rom":
			detailCalls++
			w.Write([]byte(`<script>window.rom = {"id":58881,"size":1476,"file_name":"Chrono Trigger.zip"};</script>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &minervaLiveClient{base: srv.URL, http: srv.Client()}
	cards, err := c.searchCards(context.Background(), store, "chrono trigger", "")
	if err != nil {
		t.Fatalf("searchCards: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("want 1 card (dedup by hash), got %d: %+v", len(cards), cards)
	}
	got := cards[0]
	if got.Key != "minerva-live:3b163b6fe74f31cd586e98dda1b8f8477047cf09" {
		t.Errorf("key: %s", got.Key)
	}
	if got.Set == nil {
		t.Fatal("a live minerva card must be a SET card, or it lands in the works grid")
	}
	if got.Set.SizeBytes != 163_263_283 {
		t.Errorf("set size must come from the catalogue, got %d", got.Set.SizeBytes)
	}
	if got.Set.Contains == nil {
		t.Fatal("Contains must be filled -- that is the whole point of the live API")
	}
	if got.Set.Contains.File != "TOSEC/Texas Instruments/TI-83/Games/Chrono Trigger.zip" {
		t.Errorf("contains file: %s", got.Set.Contains.File)
	}
	if got.Set.Contains.SizeBytes != 1476 {
		t.Errorf("contains size from detail page: %d", got.Set.Contains.SizeBytes)
	}
	// The magnet must be the catalogue's composed one (trackers + real dn),
	// never the bare site magnet the API returns.
	if !strings.Contains(got.Sources[0].Magnet, "tracker.example") {
		t.Errorf("magnet lost the catalogue's trackers: %s", got.Sources[0].Magnet)
	}
	st := c.stats()
	if st["unknownHashes"] != 1 {
		t.Errorf("unknownHashes: %v", st["unknownHashes"])
	}
	if st["searches"] != 1 || st["failures"] != 0 {
		t.Errorf("stats: %v", st)
	}
	if detailCalls != 1 {
		t.Errorf("detail enrichment ran %d times, want 1 (one distinct hash)", detailCalls)
	}
}

func TestMinervaLiveSearchCardsKindGate(t *testing.T) {
	store := minervaStoreWith(t, nil, snesSet())
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		json.NewEncoder(w).Encode([]minervaLiveRow{})
	}))
	defer srv.Close()
	c := &minervaLiveClient{base: srv.URL, http: srv.Client()}

	// No Minerva collection serves music, so a music search must not spend
	// 23 seconds asking.
	cards, err := c.searchCards(context.Background(), store, "daft punk", "music")
	if err != nil || cards != nil || called {
		t.Fatalf("music search must be refused without calling the API: %v %v called=%v", cards, err, called)
	}
	// Games are served.
	if _, err := c.searchCards(context.Background(), store, "zelda", "game"); err != nil {
		t.Fatalf("game search: %v", err)
	}
	if !called {
		t.Fatal("game search must call the API")
	}
}

func TestMinervaLiveSearchCardsGroupPassthrough(t *testing.T) {
	var gotGroup, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotGroup = r.URL.Query().Get("group")
		gotQuery = r.URL.Query().Get("query")
		json.NewEncoder(w).Encode([]minervaLiveRow{})
	}))
	defer srv.Close()
	c := &minervaLiveClient{base: srv.URL, http: srv.Client()}
	store := minervaStoreWith(t, nil)
	if _, err := c.searchCards(context.Background(), store, "no-intro zelda", ""); err != nil {
		t.Fatalf("searchCards: %v", err)
	}
	if gotGroup != "No-Intro" {
		t.Errorf("group: %q", gotGroup)
	}
	if gotQuery != "zelda" {
		t.Errorf("query: %q", gotQuery)
	}
}

func TestMinervaLiveNilClientIsInert(t *testing.T) {
	var c *minervaLiveClient
	if c.enabled() {
		t.Error("nil client must not be enabled")
	}
	store := minervaStoreWith(t, nil, snesSet())
	cards, err := c.searchCards(context.Background(), store, "zelda", "")
	if err != nil || cards != nil {
		t.Fatalf("nil client must answer nothing: %v %v", cards, err)
	}
	if st := c.stats(); st["enabled"] != false {
		t.Errorf("stats: %v", st)
	}
}

func TestMinervaLiveTimeoutIsAboveAPITime(t *testing.T) {
	// The previous integration died of a client timeout shorter than the
	// API's 20-25s answer time. Pin the default above it so the constant
	// cannot be tuned back down through the failure it already caused once.
	if minervaLiveTimeoutDefault < 30*time.Second {
		t.Errorf("minervaLiveTimeoutDefault=%v is shorter than the API's measured answer time", minervaLiveTimeoutDefault)
	}
	if minervaLiveTimeoutDefault >= jobTotalBudget {
		t.Errorf("minervaLiveTimeoutDefault=%v must stay inside the job budget %v", minervaLiveTimeoutDefault, jobTotalBudget)
	}
}

func TestMinervaLiveLookupByHash(t *testing.T) {
	store := minervaStoreWith(t, nil, snesSet())
	if _, ok := store.lookupByHash(strings.ToUpper(snesSet().InfoHash)); !ok {
		t.Error("lookup must be case-insensitive")
	}
	if _, ok := store.lookupByHash("0000000000000000000000000000000000000000"); ok {
		t.Error("an absent hash must not be found")
	}
	var nilStore *minervaStore
	if _, ok := nilStore.lookupByHash(snesSet().InfoHash); ok {
		t.Error("nil store must answer nothing")
	}
}
