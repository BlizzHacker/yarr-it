# Stream

Ad-free, open-source torrent streaming that plays entirely in your browser.
**We host nothing, store nothing and transcode nothing** — your own device joins
the swarm, verifies each piece and decodes it locally.

**Live:** https://stream.moveweight.com · **Downloads:** [Releases](../../releases)

> **Use a VPN.** Streaming is peer-to-peer, so your IP address is visible to
> other peers exactly as it is with any BitTorrent client. That is inherent to
> how peer-to-peer works, not a choice we made.

## What it does

- Searches **48 public torrent indexes** in a single query
- Groups results **by title** — one film is one poster with artwork, synopsis
  and rating, and a quality picker underneath, not forty near-identical rows
- Plays immediately: video starts in seconds while the rest is still arriving
- Filters by category, minimum seeders, size, resolution and codec
- Adult content filtered out by default behind an explicit 18+ toggle

## How playback works

A browser cannot open a TCP socket or send UDP, so a pure in-browser client can
only reach WebRTC peers — which is almost nothing on a public tracker. Pieces
are therefore sourced in three tiers, cheapest first:

1. **HTTP web seeds** — fetched straight from the origin, costs us nothing
2. **WebRTC peers** — other viewers' browsers
3. **TCP peers via a byte relay** — the only tier that costs bandwidth

The relay never parses the BitTorrent protocol, never assembles a piece, never
learns a filename, and keeps no cache. It is a conduit, not a host.

## Not affiliated with any indexer

Stream does not host, upload, index or store media. It is a client for public
indexes and peer-to-peer networks, the way a browser is a client for websites.
You are responsible for complying with the laws of your country.

## Licence

AGPL-3.0
