package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// archive.org as a search source.
//
// Torrents were the only way to find a game here, and for games that is close
// to useless: ROM torrents are old, thinly seeded, and usually 200-game sets
// rather than the one game somebody searched for. Meanwhile the Internet
// Archive hosts tens of thousands of individually-titled games that are free,
// legal, permanently seeded by an actual institution, and playable in a browser
// through their own emulator.
//
// So a game search asks them too. This costs us nothing at runtime: the search
// is a metadata query, and playback is an iframe pointed at their player, so no
// game byte ever crosses our relay.

const archiveSearchAPI = "https://archive.org/advancedsearch.php"

// What counts as a playable game.
//
// This was a hand-written list of collections, which was both wrong and
// narrow: it was guessed rather than derived, and it missed roughly half the
// catalogue (52 Zelda items against 27, 313 Tetris against 119) because games
// live in far more collections than anyone can enumerate.
//
// An item that declares an `emulator` is exactly an item archive.org will run
// in a browser -- that field is the definition, not a proxy for it. Pairing it
// with mediatype:software keeps out anything that is not a program.
const archiveScope = `emulator:[* TO *] AND mediatype:(software)`

// Scopes for the other kinds. Each is the narrowest query that returns only
// things of that kind, for the same reason archiveScope uses `emulator`: a
// mediatype is a fact archive.org asserts, not a keyword we hope appears.
//
// Without these, `kind=image` and `kind=comic` had no source at all -- they
// searched the games scope, matched nothing of their own, and the results the
// caller saw were whatever the torrent indexers happened to return. A filter
// that changes nothing is worse than one that is absent, because it looks like
// an answer.
var archiveScopes = map[string]string{
	"game":  archiveScope,
	"image": `mediatype:(image)`,
	// Comics live in texts. The collection narrows it to scanned comics rather
	// than the whole book library, which is otherwise almost entirely prose.
	"comic": `mediatype:(texts) AND collection:(comics OR comicbooks)`,
	"video": `mediatype:(movies)`,
}

// scopeFor returns the archive.org scope for a kind, and whether one exists.
// An unknown kind has no scope rather than falling back to games: silently
// searching the wrong catalogue is how "comics" returned emulators.
func scopeFor(kind string) (string, bool) {
	if kind == "" {
		return archiveScope, true
	}
	scope, ok := archiveScopes[kind]
	return scope, ok
}

type archiveDoc struct {
	Identifier string          `json:"identifier"`
	Title      string          `json:"title"`
	Emulator   string          `json:"emulator"`
	Downloads  int             `json:"downloads"`
	Year       json.RawMessage `json:"year"`
	Collection []string        `json:"collection"`
}

type archiveResponse struct {
	Response struct {
		NumFound int          `json:"numFound"`
		Docs     []archiveDoc `json:"docs"`
	} `json:"response"`
}

// archiveQuery builds a Solr query scoped to the emulation collections.
//
// The user's terms go in as a phrase against the title. They are quoted rather
// than interpolated raw because a stray `:` or `AND` in a search box would
// otherwise become Solr syntax and either error or silently search for
// something else.
func archiveQuery(q string) string { return archiveQueryFor(q, "") }

func archiveQueryFor(q, kind string) string {
	scope, ok := scopeFor(kind)
	if !ok {
		return ""
	}
	safe := strings.NewReplacer(`"`, " ", `\`, " ").Replace(q)
	safe = strings.TrimSpace(safe)
	// An empty term is a browse rather than a search: everything playable,
	// which the caller then orders by how often it has been downloaded. Without
	// this, `title:("")` is a syntax error and picking a category with no query
	// returns nothing.
	if safe == "" {
		return scope
	}
	return fmt.Sprintf(`title:(%q) AND %s`, safe, scope)
}

// archiveSystems maps archive.org's emulator id to a name a person reads.
// The ids here are the ones their catalogue actually uses, sampled from the
// collections above rather than guessed.
var archiveSystems = map[string]string{
	"nes": "NES", "snes": "SNES", "gameboy": "Game Boy", "gb": "Game Boy",
	"gbcolor": "Game Boy Color", "gbc": "Game Boy Color", "gba": "Game Boy Advance",
	"n64": "Nintendo 64", "genesis": "Genesis", "megadriv": "Mega Drive",
	"segaMD": "Mega Drive", "32x": "Sega 32X", "sms": "Master System",
	"smsj": "Master System", "gamegear": "Game Gear", "gg": "Game Gear",
	"psx": "PlayStation", "coleco": "ColecoVision",
	"a2600": "Atari 2600", "atari2600": "Atari 2600", "a7800": "Atari 7800",
	"a800": "Atari 800", "atari800": "Atari 800", "lynx": "Lynx",
	"intv2": "Intellivision", "intv": "Intellivision", "intvsrs": "Intellivision",
	"tg16": "TurboGrafx-16", "pce": "PC Engine",
	"wswan": "WonderSwan", "wscolor": "WonderSwan Color",
	"ngpc": "Neo Geo Pocket Color", "ngp": "Neo Geo Pocket",
	"odyssey2": "Odyssey 2", "arcadia": "Arcadia 2001", "gamecom": "Game.com",
	"vice_x64": "Commodore 64", "cpc6128": "Amstrad CPC", "gx4000": "Amstrad GX4000",
	"coco3disk": "TRS-80 CoCo", "sae-a1200": "Amiga",
	"dosbox": "MS-DOS", "dos": "MS-DOS", "sb486": "MS-DOS",
	"ruffle-swf": "Flash", "flash": "Flash",
	"arcade": "Arcade", "mame": "Arcade",
}

// collectionSystems is the fallback, and it exists because of a real failure
// mode: for arcade items the `emulator` field is the MAME *driver* name, not a
// system. "contra", "gberet" and "drgnunit" are all arcade machines, so passing
// an unrecognised id through as a label produced a Contra result whose platform
// read "Contra". The collection an item lives in is coarser but never wrong.
// Ordered most-specific first, because items belong to several collections at
// once and the order they arrive in is arbitrary. An arcade cabinet also sits
// in consolelivingroom, so matching on arrival order labelled it "Console".
var collectionSystems = []struct{ collection, name string }{
	{"internetarcade", "Arcade"},
	{"softwarelibrary_msdos_games", "MS-DOS"},
	{"softwarelibrary_msdos", "MS-DOS"},
	{"softwarelibrary_flash_games", "Flash"},
	{"softwarelibrary_flash", "Flash"},
	{"consolelivingroom", "Console"},
}

func friendlySystem(emulator string, collections []string) string {
	if name, ok := archiveSystems[emulator]; ok {
		return name
	}
	for _, cs := range collectionSystems {
		for _, c := range collections {
			if c == cs.collection {
				return cs.name
			}
		}
	}
	// Better to say nothing than to label a game with a MAME driver name.
	return ""
}

// ejsCores are the archive.org emulator ids EmulatorJS also has a core for.
//
// This is what decides whether a game can be offered with touch controls. The
// Internet Archive's own player is excellent on a desktop and close to
// unusable on a phone: it expects a keyboard and offers no on-screen pad, so a
// console game there is a screen you can watch and not play. EmulatorJS has a
// virtual gamepad, which for a phone is the difference between a playable game
// and a demo.
//
// So where both can run a title, both are offered -- EmulatorJS first.
var ejsCores = map[string]string{
	"nes": "nes", "famicom": "nes",
	"snes": "snes", "superfamicom": "snes",
	"gameboy": "gb", "gb": "gb", "gbcolor": "gb", "gbc": "gb",
	"gba":      "gba",
	"n64":      "n64",
	"genesis":  "segaMD",
	"megadriv": "segaMD",
	"segaMD":   "segaMD",
	"32x":      "sega32x",
	"sms":      "segaMS", "smsj": "segaMS",
	"gamegear": "segaGG", "gg": "segaGG",
	"psx":    "psx",
	"coleco": "coleco",
	"a2600":  "atari2600",
	"a7800":  "atari7800",
	"lynx":   "lynx",
	"intv2":  "atari2600", // Intellivision has no EmulatorJS core; excluded below
	"tg16":   "pce",
	"wswan":  "ws", "wscolor": "ws",
	"ngpc": "ngp", "ngp": "ngp",
	"vb": "vb",
}

// Intellivision maps to no EmulatorJS core; listing it above would offer a
// player that cannot run it.
func ejsCoreFor(emulator string) string {
	if emulator == "intv2" || emulator == "intv" || emulator == "intvsrs" {
		return ""
	}
	return ejsCores[emulator]
}

// sourcesFor builds the play options for an item.
//
// The EmulatorJS option carries an `#ejs` fragment; the resolver reads that and
// fetches the item's file list at play time rather than here, because finding
// the ROM inside an item costs one metadata request each and doing sixty of
// them per search would make every search slower for a link most people never
// click.
func sourcesFor(d archiveDoc, title, system string) []source {
	details := "https://archive.org/details/" + d.Identifier
	out := make([]source, 0, 2)

	// Flash is the same trade as a console game: their player works, ours has
	// touch support and our own controls. A SWF is about a megabyte, so
	// relaying one costs almost nothing -- much less than a ROM.
	if d.Emulator == "ruffle-swf" || d.Emulator == "flash" {
		out = append(out, source{
			Title:   title,
			Indexer: "Ruffle",
			Magnet:  details + "#swf",
			Source:  system,
			Quality: "TOUCH",
			WebSafe: true,
		})
	}

	if core := ejsCoreFor(d.Emulator); core != "" {
		out = append(out, source{
			Title:   title,
			Indexer: "EmulatorJS",
			Magnet:  details + "#ejs",
			Source:  system,
			Quality: "TOUCH",
			WebSafe: true,
		})
	}

	out = append(out, source{
		Title:   title,
		Indexer: "Archive.org",
		Magnet:  details,
		Source:  system,
		WebSafe: true,
	})
	return out
}

// searchArchive queries archive.org and returns one card per game.
func (s *server) searchArchive(ctx context.Context, q, kind string) ([]card, error) {
	query := archiveQueryFor(q, kind)
	if query == "" {
		return nil, nil
	}
	params := url.Values{}
	params.Set("q", query)
	for _, f := range []string{"identifier", "title", "emulator", "downloads", "year", "collection"} {
		params.Add("fl[]", f)
	}
	params.Set("rows", "60")
	params.Set("page", "1")
	params.Set("output", "json")
	// Their own popularity signal. Downloads is the closest thing they have to
	// a health metric, and it does the same job seeders do for a torrent:
	// separating the canonical upload from the six near-duplicate ones.
	params.Add("sort[]", "downloads desc")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		archiveSearchAPI+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yarr.it/1.0 (+https://stream.moveweight.com)")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("archive.org search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive.org search: status %d", resp.StatusCode)
	}

	var out archiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("archive.org search: %w", err)
	}
	return archiveCards(out.Response.Docs, kind), nil
}

func archiveCards(docs []archiveDoc, kind string) []card {
	if kind == "" {
		kind = "game"
	}
	cards := make([]card, 0, len(docs))
	seen := make(map[string]bool, len(docs))
	for _, d := range docs {
		if d.Identifier == "" || seen[d.Identifier] {
			continue
		}
		seen[d.Identifier] = true

		system := friendlySystem(d.Emulator, d.Collection)
		title := strings.TrimSpace(d.Title)
		if title == "" {
			title = d.Identifier
		}

		// The kind and group must match what was asked for, or the Kind
		// filter discards every card the archive just returned.
		group := map[string]string{
			"game": "games", "image": "images", "comic": "comics",
			"video": "movies",
		}[kind]

		cards = append(cards, card{
			Key:      "ia:" + d.Identifier,
			Title:    title,
			Year:     archiveYear(d.Year),
			Kind:     kind,
			Instant:  true,
			Seeders:  0,
			Popular:  d.Downloads,
			Platform: system,
			// The "Games" chip filters on this, and a card without it would be
			// hidden the moment somebody narrowed to exactly what they wanted.
			Groups:  []string{group},
			Sources: sourcesFor(d, title, system),
			Art: artwork{
				// Their thumbnail service. An <img> is not subject to CORS, so
				// this loads directly from them and costs us nothing.
				Poster: "https://archive.org/services/img/" + d.Identifier,
				Found:  true,
			},
		})
	}
	// Most-downloaded first, which is their equivalent of best-seeded.
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].Popular > cards[j].Popular })
	return cards
}

// year arrives as a number for some items and a string for others.
func archiveYear(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil && len(str) >= 4 {
		var parsed int
		if _, err := fmt.Sscanf(str[:4], "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}
