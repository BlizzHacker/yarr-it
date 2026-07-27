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
