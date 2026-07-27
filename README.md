<p align="center">
  <img src="brand/yarrit-256.png" width="128" alt="Yarr.It">
</p>

<h1 align="center">Yarr.It</h1>

<p align="center"><b>View before you download.</b><br>
Ad-free, open-source torrent <i>streaming</i> — not a downloader.</p>

<p align="center">
  <a href="https://stream.moveweight.com">stream.moveweight.com</a> ·
  <a href="../../releases">Downloads</a> ·
  <a href="SIDELOADING.md">Sideloading guide</a>
</p>

---

**Nothing is hosted, stored or transcoded here.** Your own device joins the
swarm, verifies each piece and decodes it locally.

> **Use a VPN.** Streaming is peer-to-peer, so your IP address is visible to
> other peers exactly as it is with any BitTorrent client. That is inherent to
> how peer-to-peer works, not a choice made here.

## What it does

- Searches **48 public torrent indexes** in a single query
- Groups results **by title** — one film is one poster with artwork, synopsis
  and rating, and a quality picker underneath, not forty near-identical rows
- Plays immediately: video starts in seconds while the rest is still arriving
- Filters by category, minimum seeders, size, resolution and codec
- Adult content filtered out by default behind an explicit 18+ toggle

## Apps

| Platform | Artifact | Notes |
|---|---|---|
| Web | [stream.moveweight.com](https://stream.moveweight.com) | Full peer-to-peer in the browser |
| Chrome / Edge / Brave | `yarrit-extension-*.zip` | Magnet hijack, ▶ on torrent sites, omnibox |
| Android | `Yarr.It-*.apk` | |
| Google TV / Android TV | `Yarr.It-tv-*.apk` | Leanback launcher, D-pad, plays via the LAN gateway |
| Windows 10/11 + Xbox | `Yarr.It_*.msix` | |
| Roku | `Yarr.It-roku.zip` | Sideload only; plays via the LAN gateway |

None of these are in an app store yet, and some realistically never will be.
**[SIDELOADING.md](SIDELOADING.md)** covers every platform end to end, including
the failure modes each one actually hits.

Every client talks to a configurable API endpoint and carries bundled defaults,
so nothing depends on a store listing — or on `stream.moveweight.com` staying up.

## How playback works

A browser cannot open a TCP socket or send UDP; it gets HTTP, WebSocket and
WebRTC and nothing else. BitTorrent peers speak TCP/uTP and trackers speak UDP,
which is why adding a healthy 50-seeder torrent to a plain WebTorrent client
yields **zero** peers. Pieces are therefore sourced in three tiers, cheapest
first:

1. **HTTP web seeds** — fetched straight from the origin, costs nothing
2. **WebRTC peers** — other viewers' browsers
3. **TCP peers via a byte relay** — the only tier that costs bandwidth, so it is
   capped and degrades back to the free tiers at 80% of the monthly budget

`mw-bridge` closes exactly that gap and nothing more: it is told an address and
moves bytes. It never parses the BitTorrent protocol, never assembles a piece,
never learns a filename or infohash, never writes payload to disk, and keeps no
cache. Peer addresses arrive in the first WebSocket frame rather than in the URL
so they cannot land in a proxy access log, and private/loopback/CGNAT ranges are
refused so the relay cannot be turned into a route into the network behind it.

It is a conduit, not a host.

### TVs are different, and this README should say so

Roku and Android TV have no browser engine, no WebRTC and no usable socket API,
so they cannot join a swarm at all. Those clients browse through the search API
and ask a **LAN gateway** to turn a magnet into an HTTP stream they can play.
That inverts the "your device does the work" posture, which is exactly why the
gateway runs on your own network rather than someone else's.

## Components

| Piece | Where | Role |
|---|---|---|
| `bridge/` | `127.0.0.1:8801` | WebSocket↔TCP/UDP byte relay, per-IP caps, monthly budget |
| `search/` | `127.0.0.1:8802` | Prowlarr front end; groups releases into title cards, TMDB artwork |
| `web/` | static | SPA, in-browser engine, service-worker player |
| `gateway/` | LAN | Torrent→HTTP for devices that cannot do peer-to-peer |
| `roku/` | Roku | SceneGraph channel |
| `extension/` | Chrome MV3 | Magnet hijack and page injection |
| `installers/` | — | MSIX, TV APK, packaging |
| `brand/` | — | One master mark, propagated to every app |

[ARCHITECTURE.md](ARCHITECTURE.md) goes into the protocol detail.

## Building the mark

The logo is not hand-authored vector art. Five attempts at drawing a tricorn as
SVG paths each read as something else — a dome, a cowboy hat, a boat hull, a
mountain range, a sombrero. It is generated instead:

```bash
python brand/generate.py    # SD 3.5 Large, six concepts
python brand/recolor.py     # remap onto the brand palette, centre, trim
python brand/propagate.py   # push the master into every app's icons
```

## Not affiliated with any indexer

Yarr.It does not host, upload, index or store media. It is a client for public
indexes and peer-to-peer networks, the way a browser is a client for websites.
You are responsible for complying with the laws of your country.

## Licence

[MIT](LICENSE).
