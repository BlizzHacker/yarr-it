# stream.moveweight.com

Ad-free, open-source torrent **streaming** — not a downloader. Search every
configured indexer at once and play the result in the browser.

## Why it is built this way

A web page cannot open a TCP socket or send UDP. Browsers expose only HTTP,
WebSocket and WebRTC. BitTorrent peers speak TCP/uTP and trackers speak UDP, so
a pure in-browser client reaches only WebRTC peers — which is why adding a
50-seeder torrent to a plain WebTorrent client yields **zero** peers.

`mw-bridge` closes exactly that gap and nothing more: it is told an address and
moves bytes. It never parses the BitTorrent protocol, never assembles a piece,
never learns a filename or infohash, never writes payload to disk, and keeps no
cache. Peer addresses arrive in the first WebSocket frame rather than the URL so
they cannot land in a proxy access log, and private/loopback/CGNAT ranges are
refused so the relay cannot be used to reach the estate behind the tunnel.

## Components

| Piece | Where | Role |
|---|---|---|
| `bridge/` | VPS `127.0.0.1:8801` | WebSocket↔TCP/UDP byte relay, per-IP caps, monthly budget |
| `search/` | VPS `127.0.0.1:8802` | Prowlarr front-end over WireGuard; groups releases into title cards |
| `web/` | Caddy static | SPA, in-browser engine, service-worker player |

Bytes are sourced cheapest-first: HTTP web seeds → WebRTC peers → relayed TCP.
Only the last tier costs bandwidth, so it is capped and degrades to the free
tiers at 80% of the monthly budget.

## Sources

A source is anything a resolver claims. Each resolver answers `canHandle(input)`
and `resolve(source)`, returning either a **`Playable`** — a descriptor the
player renders, discriminated by `render`
(`video｜audio｜image｜embed｜canvas`) — or a **`Collection`**, a list the
library browses. That split is what makes an IPTV playlist a real channel
browser instead of a faked single stream.

| Resolver | Input | Result |
|---|---|---|
| `torrent` | magnet, 40-char info hash | playable — wraps `StreamEngine` unchanged |
| `embed` | YouTube, Vimeo | playable, official iframe only — never extraction |
| `playlist` | `.m3u`, `.m3u8` | collection, or a playable when the body is an HLS manifest |
| `flash` | `.swf` | playable, `canvas` — Ruffle (vendored, dual MIT/Apache) |
| `game` | ROM (`.nes`, `.smc`, `.gba`, `.z64`, `.md`, …) | playable, `canvas` — EmulatorJS |
| `url` | direct media URL | playable, `render` chosen by sniffed content-type |

Registration order is `torrent, embed, flash, game, playlist, url`. `url` is the catch-all and
must stay last: a YouTube link and an `.m3u8` link are both http(s) URLs, so the
specific resolvers need first refusal.

`.m3u8` is used by *both* IPTV channel lists and HLS media manifests, so the
extension proves nothing — the body decides. HLS manifests carry `#EXT-X-*` tags
at the start of a line; channel lists do not. That is one fetch, sniffed, which
is why these are one resolver rather than two.

### Flash and games out of torrents

The torrent resolver classifies `.swf` and cartridge ROMs alongside video and
audio, so a torrent carrying either renders on the canvas rather than being
handed to a `<video>` that can never play it. Neither can be streamed — Ruffle
needs the whole SWF and an emulator needs the whole cartridge before it boots —
so those wait for the file to complete, showing progress, then hand the bytes
over as a blob. Cartridge-era ROMs are kilobytes to a few megabytes, so that is
seconds on a healthy swarm.

`pickPlayableFile` prefers real media when a torrent has both, so a film that
happens to ship an `.swf` extra still plays the film.

**`.torrent` file URLs work as well as magnets, and are more reliable for this.**
A magnet carries no metadata: WebTorrent has to fetch the file list from a peer
via `ut_metadata` first, and a BEP-19 web seed serves content, not metadata — so
a magnet with only a web seed and no peers never becomes ready. A `.torrent`
file has the metadata inline and starts immediately.

### The playback ladder

Two browser rules block most real IPTV and no resolver can code around either:
an HTTPS page cannot load HTTP media, and most IPTV endpoints send no
`Access-Control-Allow-Origin`. So playback probes and climbs, cheapest first:

1. **Direct** — the client fetches it. Free, and the only tier TV clients need:
   Roku and Android TV use native players with neither restriction.
2. **Your own gateway** — the LAN gateway proxies it, on your hardware and
   bandwidth. Unlimited, and costs the project nothing.
3. **Public relay** — `/bridge/iptv` terminates TLS and adds a CORS header,
   fixing both blockers at once.
4. **Explain** — only when all three are impossible, naming the rule that blocked
   it rather than showing a generic error.

Relayed video is billed twice per byte, roughly **4.4 GiB per viewer-hour**. It
therefore has its own sub-budget (`-iptv-budget-gib`, default a quarter of
`-budget-gib`) so continuous IPTV can never drain the month — the mail edge
shares that allowance.

`/bridge/iptv` refuses any non-public address via `resolvesPublic`, **re-checked
on every redirect hop**, because an upstream that passes the first check can
redirect to a private address and walk into the network behind the tunnel.

## Build and deploy

```bash
cd bridge && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mw-bridge .
cd ../search && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mw-search .
cd ../web && npx esbuild src/main.js --bundle --format=esm \
  --outfile=dist/app.js --define:global=globalThis --external:./webtorrent.min.js --minify
```

`dist/webtorrent.min.js` and `dist/sw.min.js` are copied unmodified from the
`webtorrent` package. Both are ES modules and must not be bundled.

## Gotchas found the hard way

- **WebTorrent 3 removed `file.streamTo()`/`appendTo()`.** Playback goes through
  `client.createServer({controller})` + a registered `sw.min.js` and
  `file.streamURL`. The old call fails silently — data downloads, video stays blank.
- **Never emit Prowlarr's `magnetUrl`/`downloadUrl`.** They point at Prowlarr
  itself with the API key in the query string. Rebuild from `infoHash` instead.
  Guarded by tests.
- **Always merge default udp trackers into every magnet.** Indexers often return
  a bare `magnet:?xt=...&dn=...`; with no announce targets the browser finds no
  peers however many seeders are reported.
- **Test relay policy from off-box.** `permit_mynetworks`-style trust means a
  localhost probe proves nothing.
