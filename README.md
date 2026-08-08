# Yarr.It

> **Yarr.It is dedicated to the memory of Samuel Thomas Trimble Cliffe
> (1992–2011), who wanted it to exist.** See [DEDICATION.md](DEDICATION.md).

One front door for a self-hosted media library.

Search, request, watch, listen, read and play across the services you already
run — without caring which one answered. [yarrit.com](https://yarrit.com)

## Where this sits

```
Move Weight
└─ Yarr.It ................ you are here
   └─ Cartridge ........... tools for self-hosting a retro game library
      └─ Romarr ........... the *arr for games: request it, get it, file it
         └─ ROM Hub ....... the plugin host underneath
```

Each layer is usable on its own. Yarr.It does not require Cartridge, and Romarr
and ROM Hub each run perfectly well without anything above them — they answer
the same webhook by different means, and
[ROM Hub's README](https://github.com/BlizzHacker/rom-hub#an-alternative-to-romarr-not-a-replacement-for-it)
explains when to prefer which.

## What works today

Everything below is running, not planned:

| | |
|---|---|
| **Search** | torrent indexers via your own Prowlarr, plus archive.org |
| **Domains** | video, music, games, books, comics, images |
| **Watch** | in-browser streaming from a torrent, with seeking and subtitles |
| **Shelf** | watchlist and resume points, shared across your devices |
| **TV** | a Roku channel, pointed at your own server |
| **Self-host** | `curl -fsSL https://yarrit.com/selfhost.sh \| bash` |

**In progress, and not yet claimed:** Radarr / Sonarr / Lidarr / Romarr
request routing, Live TV with a real programme guide, and the in-app readers.
Those are being built against the provider contract in
[`stream/search/provider.go`](stream/search/provider.go); this README will say
so when each is proven against real hardware, and not before.

## It is not tied to one server

Yarr.It cannot run with no server — indexers need API keys, catalogue keys must
never ship inside a client, and a browser cannot open a TCP socket to a peer.
What it can do is stop depending on one *particular* server. yarrit.com is a
default, not a requirement: point any client at your own instance, or run the
whole stack yourself.

A client served by your own instance needs no configuration at all, because
"whichever server sent this page" is the fallback.

See [stream/SELFHOSTING.md](stream/SELFHOSTING.md) for what each platform can
and cannot do — including the honest ceiling, which is that a TV cannot run a
server and needs one machine on the network that can.

## Layout

| Path | Purpose |
|---|---|
| `stream/web/` | the reference client (also the Android, Fire, Tizen and Xbox app) |
| `stream/search/` | the API: search, library, providers, the canonical schema |
| `stream/bridge/` | WebSocket↔TCP relay, so a browser can reach ordinary peers |
| `stream/gateway/` | joins a swarm and re-serves it as HTTP, for devices that cannot torrent |
| `stream/roku/` | the Roku channel |
| `stream/deploy/` | self-host installer, VPN kill switch, provisioning |
| `scripts/`, `postfix/` | the mail edge this repo grew out of — see below |

## One vocabulary

[`stream/search/schema.json`](stream/search/schema.json) is the single
definition of what a media kind is. Go embeds it, the web build imports the same
file, and `GET /api/schema` serves it to clients that cannot import at build
time.

It is one file for a reason. When the mapping lived in two places, the browser
sent `groups=movies`, the server compared against `video`, and every card was
filtered out — while the response still reported hundreds of results, because
facets are counted before the filter runs. Nothing errored. It survived until
somebody counted the rows.

## Network protection

The torrent gateway can be put behind your own VPN, failing closed:

```bash
sudo stream/deploy/vpn-killswitch.sh <your-wireguard-config-name>
```

It seals the physical interface, pins the VPN endpoint by resolved address (a
hostname would need DNS, which the kill switch blocks), seals IPv6, and makes
the gateway require the tunnel so it cannot win the boot race and announce to a
tracker before the VPN is up. Your LAN stays exempt — the TVs in the house were
never meant to go through the tunnel.

No account is required for any of it.

## The mail edge

This repository began as the configuration for the estate's mail edge, and that
still lives here: `scripts/`, `postfix/` and `baseline/`, serving
`relay.moveweight.net` on `104.129.28.137`. It is unrelated to Yarr.It beyond
sharing a host. Access is key-only from the Proxmox host, through the wrapper:

```bash
bash scripts/vps.sh 'postconf -n'
```

No secrets are stored in this repository.
