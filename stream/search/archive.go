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

// The emulation collections. Restricting to these keeps a game search from
// returning concert bootlegs and scanned magazines.
var archiveCollections = []string{
	"consolelivingroom",           // console games, browser-playable
	"internetarcade",              // coin-op arcade
	"softwarelibrary_msdos_games", // MS-DOS
	"softwarelibrary_flash_games", // Flash, the other half of the games ask
	"gamegear_library",
	"nintendo_entertainment_system_library",
	"softwarelibrary_apple",
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
func archiveQuery(q string) string {
	safe := strings.NewReplacer(`"`, " ", `\`, " ").Replace(q)
	safe = strings.TrimSpace(safe)
	return fmt.Sprintf(`title:(%q) AND collection:(%s)`,
		safe, strings.Join(archiveCollections, " OR "))
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

// searchArchive queries archive.org and returns one card per game.
func (s *server) searchArchive(ctx context.Context, q string) ([]card, error) {
	params := url.Values{}
	params.Set("q", archiveQuery(q))
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
	return archiveCards(out.Response.Docs), nil
}

func archiveCards(docs []archiveDoc) []card {
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

		cards = append(cards, card{
			Key:      "ia:" + d.Identifier,
			Title:    title,
			Year:     archiveYear(d.Year),
			Kind:     "game",
			Instant:  true,
			Seeders:  0,
			Popular:  d.Downloads,
			Platform: system,
			// The "Games" chip filters on this, and a card without it would be
			// hidden the moment somebody narrowed to exactly what they wanted.
			Groups: []string{"games"},
			Sources: []source{{
				Title:   title,
				Indexer: "Archive.org",
				// The frontend plays whatever is in Magnet; an archive.org URL
				// routes to the resolver that hands off to their player.
				Magnet:  "https://archive.org/details/" + d.Identifier,
				Source:  system,
				WebSafe: true,
			}},
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
