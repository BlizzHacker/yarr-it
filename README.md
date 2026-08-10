# Yarr.It

> **Yarr.It is dedicated to the memory of Samuel Thomas Trimble Cliffe
> (1992–2011), who wanted it to exist.** See [DEDICATION.md](DEDICATION.md).

Ad-free, open-source torrent **streaming** — not a downloader. Type a search,
every indexer you've configured answers at once, and the result plays right in
the browser. Nothing is hosted, nothing is transcoded, nothing lands on disk.

## Use it

Open **[yarrit.com](https://yarrit.com)** and search. The web app is the whole
product — the same page also ships as the Android, Fire TV, Samsung Tizen and
Windows apps, and there is a Roku channel in [`roku/`](roku/). Point it at the
public server or at your own (the server picker is in settings); your watch
history and library never leave whichever server you chose.

It plays more than torrents. Paste almost anything:

- a **magnet link or `.torrent` URL** — video and audio stream as they download
- an **IPTV playlist** (`.m3u`/`.m3u8`) — browsed as a real channel list
- a **YouTube or Vimeo link** — official embeds only, never extraction
- a **Flash game** (`.swf`) — runs on canvas via Ruffle
- a **cartridge ROM** (`.nes`, `.smc`, `.gba`, `.z64`, …) — runs via EmulatorJS

## Self-host

Three small services and a static web app; the [`Caddyfile`](Caddyfile) in this
repo is the deployed shape, and [`installers/`](installers/) has setup scripts.

```bash
cd bridge && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mw-bridge .
cd ../search && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mw-search .
cd ../web && npx esbuild src/main.js --bundle --format=esm \
  --outfile=dist/app.js --define:global=globalThis --external:./webtorrent.min.js --minify
```

- `bridge/` — a WebSocket↔TCP/UDP byte relay with per-IP caps and a monthly
  bandwidth budget. It never learns what it carries.
- `search/` — a front-end over your [Prowlarr](https://prowlarr.com) that
  groups releases into title cards.
- `web/` — the SPA, in-browser engine, and service-worker player.
  (`dist/webtorrent.min.js` and `dist/sw.min.js` are copied unmodified from
  the `webtorrent` package — they are ES modules and must not be bundled.)

For the TV builds, [SIDELOADING.md](SIDELOADING.md) walks through each
platform; [`store/`](store/) holds the store packaging.

## How it works, briefly

A web page cannot open a TCP socket, so a pure in-browser torrent client only
ever reaches WebRTC peers — which is why a 50-seeder torrent yields zero peers
in a plain WebTorrent client. Yarr.It sources bytes cheapest-first: HTTP web
seeds, then WebRTC peers, then TCP peers relayed through `mw-bridge`. Only the
last tier costs the server bandwidth, so it's capped and degrades gracefully.

The relay is deliberately blind: it is told an address and moves bytes. It
never parses the BitTorrent protocol, never learns a filename or infohash, and
refuses private/loopback address ranges so it can't be used to reach anything
behind it.

The full design — the resolver system, the IPTV playback ladder, and the
hard-won WebTorrent gotchas — is in [ARCHITECTURE.md](ARCHITECTURE.md).

## State of the project

284 tests run green today; here is exactly where they are and where they
aren't, so you know what to trust and where a contribution lands hardest.

| Component | Tests | Confidence | Notes |
|---|---|---|---|
| `bridge/` (relay, caps, budget) | 28 Go tests | **High** | The budget math, IPTV proxy, tracker and page paths are all asserted; runs in production behind yarrit.com |
| `search/` (Prowlarr front-end) | 68 Go tests | **High** | Grouping, filtering, magnet handling, device shaping |
| `web/` (SPA, engine, resolvers) | 188 node tests | **High for logic** | Resolvers, playback ladder, m3u sniffing, failure handling — all pure-function tested. The *browser* half (service worker, WebTorrent glue) is exercised by use, not by CI |
| `gateway/` (VPN egress helper) | none | **Untested** | Small and stable, but nothing asserts it. A Go contributor could own this in an afternoon |
| `extension/` (browser extension) | none | **Untested** | Manual testing only. **Needs an owner** |
| `roku/` (Roku channel) | none | **Untested in CI** | BrightScript has no CI here; tested on real hardware per release |
| TV / store builds (`installers/`, `store/`) | none | **Manual** | Sideloading verified per [SIDELOADING.md](SIDELOADING.md); store packaging exercised at submission time |

Known limits worth stating plainly: playback quality depends entirely on the
swarm and your indexers — Yarr.It hosts nothing and cannot make a dead
torrent stream; WebRTC peer counts are low on most swarms, which is the
entire reason `mw-bridge` exists; and the relay's monthly budget means the
public instance degrades to web-seed/WebRTC tiers near the end of a heavy
month, by design.

**Where help lands hardest:** tests for `gateway/` and the extension; a
Tizen/webOS person to verify the TV builds each release; IPTV edge-case
playlists (send ones that misbehave); and issues with a reproduction —
[github.com/BlizzHacker/yarr-it/issues](https://github.com/BlizzHacker/yarr-it/issues).

Yarr.It is the front door of a larger self-hosted stack — the
[Cartridge](https://github.com/BlizzHacker/rom-hub#readme) retro-gaming
suite, [ROMarr](https://github.com/BlizzHacker/romarr) (the *arr for games)
and [ROM Hub](https://github.com/BlizzHacker/rom-hub) (its plugin host) —
and improvements to any of them tend to surface here.

## License

MIT — see [LICENSE](LICENSE).
