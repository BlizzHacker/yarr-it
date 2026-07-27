# Yarr.It — universal source layer

**Date:** 2026-07-27
**Status:** approved, ready for implementation planning
**Slice:** 1 of 6. The other five (sync, Stremio addons, Ruffle/EmulatorJS, Jellyfin, NZB) are out of scope here and enumerated at the end.

## Problem

Yarr.It can play exactly one kind of thing: a BitTorrent magnet. `StreamEngine.add(magnet)`
is the only entry point, it is hard-wired to WebTorrent, and it ends at
`file.streamURL` fed into a `<video>` tag. There is no concept of "a playable thing
that is not a torrent".

Every item on the roadmap — IPTV, direct URLs, Stremio addons, YouTube embeds, Flash
via Ruffle, ROMs via EmulatorJS — needs that concept. Built in roadmap order without
it, each one becomes a separate hack bolted onto a torrent-shaped engine, and the
render paths genuinely differ: Ruffle and EmulatorJS draw to a canvas, YouTube is an
iframe, neither is a `<video src>`.

This slice introduces the abstraction and proves it with the two cheapest resolvers.

## Goals

- A source is a resolver's input, not a magnet link.
- The player renders a descriptor and knows nothing about transport.
- Direct URLs, HLS streams, `.m3u` playlists and YouTube/Vimeo embeds all play.
- Playback never dead-ends where a working path exists.
- The existing, proven torrent engine is preserved, not rewritten.

## Non-goals for this slice

- Accounts, cross-device sync (interface is provided; implementation is the next slice).
- Third-party/external addons. The interface must prove itself on built-in resolvers first.
- NZB/Usenet.

## Architecture

Five modules under `web/src/`.

| Module | Responsibility |
|---|---|
| `source.js` | Type definitions only. No logic. |
| `resolvers/` | Registry plus one file per resolver. |
| `player.js` | Renders a `Playable`. |
| `store.js` | `SourceStore` interface + `LocalSourceStore`. |
| `library.js` | Browses a `Collection`. |

### Types

```js
// What the user asked for.
Source = { kind: string, uri: string, meta: { title?, logo?, group? } }

// What the player draws. `render` is the discriminator -- NOT every source is a
// <video src>, which is exactly why normalising everything to a URL fails.
Playable = {
  render: 'video' | 'audio' | 'image' | 'embed' | 'canvas',
  src: string,
  mime: string,
  tier: 'direct' | 'gateway' | 'relay',
  cleanup: () => void,
}

// A list, not a stream. An .m3u is thousands of channels; conflating it with a
// Playable is what makes IPTV implementations feel fake.
Collection = { title: string, sources: Source[] }
```

### Resolver contract

```js
{ canHandle(input) -> boolean,
  resolve(source)  -> Promise<Playable | Collection> }
```

`resolve` returning a `Collection` sends the user to `library.js`; picking an entry
calls `resolve` again on that `Source`. Exactly one level of recursion; a playlist
that references another playlist resolves the same way, capped at depth 3 to stop
malicious or accidental cycles.

### Resolvers in this slice

| Resolver | Input | Returns |
|---|---|---|
| `torrent` | `magnet:`, infohash | `Playable` — wraps today's `StreamEngine` verbatim |
| `url` | direct media URL | `Playable` — `render` chosen by sniffed content-type |
| `hls` | `.m3u8` single stream | `Playable` — native in Safari, `hls.js` elsewhere |
| `m3u` | `.m3u`/`.m3u8` playlist | `Collection` |
| `embed` | YouTube/Vimeo watch URL | `Playable` with `render: 'embed'` |

`hls` and `m3u` share an extension. Disambiguate by content: a playlist contains
`#EXTINF` entries pointing at other resources; a media manifest contains
`#EXT-X-TARGETDURATION`. Sniff the body, never the extension.

**YouTube and Vimeo are embeds, not extractions.** Extracting stream URLs violates
both services' terms and gets the relay IP blocked. The resolver emits an official
iframe embed URL and nothing else.

## The playback ladder

Browsers block most real IPTV outright, for two independent reasons that no resolver
can code around:

- **Mixed content.** The site is HTTPS; a large share of IPTV providers serve `http://`.
  Browsers hard-block HTTP media on an HTTPS page. No override exists.
- **CORS.** Most IPTV endpoints send no `Access-Control-Allow-Origin`, so `hls.js`
  cannot fetch segments even over HTTPS.

So playback probes and climbs, cheapest tier first — the same philosophy the torrent
engine already uses for pieces:

1. **Direct.** The client fetches it itself. Free. Covers HTTPS+CORS sources and
   *every* TV client, since native players have no CORS or mixed-content rules.
2. **User's own gateway.** The existing LAN gateway proxies it, on the user's hardware
   and bandwidth. Unlimited, costs the project nothing, and gives the gateway a reason
   to exist beyond Roku.
3. **Public relay.** Terminates TLS (fixing mixed content) and adds CORS headers.
   Budget-guarded; first thing to degrade as the cap approaches.
4. **Explain.** Only when all three fail, and then name the specific rule that blocked
   it.

The probe is a `HEAD` on load: check scheme, check `Access-Control-Allow-Origin`, pick
the highest free tier that will actually work. The user pastes a playlist and it plays;
none of this is surfaced.

### Why the relay is the last resort, not the first

Relayed bytes are billed twice. A 1080p stream costs roughly **4.4 GiB per viewer-hour**.
The configured cap is `-budget-gib 2600`, i.e. about **590 proxied viewer-hours per
month** — around five regular users. `budget.go` already documents the stake: the mail
edge shares that box and that allowance, so unbounded IPTV proxying is an outage
waiting to happen.

Torrent relaying is bursty and finite per file. IPTV proxying is continuous and
unbounded. They are not the same risk and must not share one undifferentiated budget.

IPTV proxying therefore gets its own sub-cap, defaulting to **25% of the monthly
allowance (650 GiB, ~147 proxied viewer-hours)**, exposed as `-iptv-budget-gib` so it
can be tuned without a rebuild. Reaching the sub-cap disables tier 3 for IPTV only;
torrent relaying and the mail edge are unaffected.

## Error taxonomy

Resolvers return typed failures. Each maps to a specific message and the next tier to
attempt — no generic "playback error".

| Failure | Meaning | Next tier |
|---|---|---|
| `MixedContent` | HTTP stream on an HTTPS page | gateway, then relay |
| `CorsBlocked` | No `Access-Control-Allow-Origin` | gateway, then relay |
| `DeadStream` | Origin returned 4xx/5xx or timed out | none — report |
| `UnsupportedCodec` | Container/codec the device cannot decode | none — report, suggest a TV client |
| `BudgetExhausted` | Relay sub-cap reached | none — report, suggest the gateway |

## Storage

`SourceStore` is an interface with two methods over collections and sources:
`list()`, `put()`, `remove()`, `get()`.

`LocalSourceStore` (IndexedDB) ships in this slice. `SyncedSourceStore` implements the
same interface in the next one, so sync is a drop-in rather than a rewrite. Nothing
outside `store.js` knows which is in use.

## TV clients

Roku and Android TV use native players: no CORS, no mixed-content rules. They resolve
at tier 1 for URL, HLS and IPTV, and only need the gateway for torrents. This slice
makes TV playback *simpler* than browser playback, reversing the current situation
where the gateway is mandatory for everything.

## Required privacy-policy change — a hard prerequisite of the sync slice

Recorded here so it is not lost between slices. No account work may ship before it.
This slice adds no accounts, so it does not block this slice.

`web/privacy.html` currently states *"We have no accounts, no logins, no analytics"*
and *"No account, email address, name or phone number — there is nothing to sign up
for."* The agreed direction adds optional accounts (sync code, Authentik SSO, email
magic link). Email logins collect personal data, so those sentences cannot survive.

The policy must be restructured into two tiers:

- **Anonymous tier** — current guarantees kept verbatim. No account, no identifiers,
  nothing stored server-side. Streaming works fully without signing in.
- **Account tier** — states exactly what each method collects: sync code (nothing
  identifying), Authentik (whatever the IdP holds), email link (an email address).

Plus the operational commitment already agreed: **logs deleted daily**, implemented as
a systemd timer, not merely asserted.

## Testing

- **`m3u` parser against real-world fixtures.** The format is filthy: `#EXTINF`
  attribute soup, inconsistent quoting, BOMs, CRLF, nested playlists. Fixtures come
  from actual playlists, not idealised samples.
- **Resolver unit tests** as pure input → output. No network; `canHandle` and content
  sniffing are pure functions over strings.
- **One smoke test per `render` path** — video, audio, image, embed, canvas — asserting
  the player attaches the right element and `cleanup()` releases it.
- **Ladder tests** with a stubbed probe: an HTTP-on-HTTPS source must select gateway or
  relay, never direct; a CORS-less source likewise.
- **Budget test**: the relay refuses new IPTV proxying at the sub-cap. This is the
  failure that would take the mail edge down, so it gets an explicit regression test.

## Later slices

In dependency order:

1. **Sync** — `SyncedSourceStore`, sync codes, Authentik SSO, email magic link, device
   pairing for TVs, privacy-policy rewrite.
2. **Stremio addon client** — an addon's `/stream` response is nearly a `Source`
   already, so this becomes a translation layer rather than a parallel pipeline.
3. **Ruffle + EmulatorJS** — both `render: 'canvas'`, no backend, no new transport.
   Note that emulators need the *whole* ROM before boot, so "streaming" a game means
   completing the download first; fine for retro ROMs, not for modern game torrents.
4. **Jellyfin plugin** — separate C# repo and release cycle. Plex is not viable: it
   removed third-party plugin support in 2018.
5. **NZB/Usenet** — last, if ever. Usenet needs full download, par2 repair and unrar
   before a frame plays, and per-user paid provider credentials. It is a downloader
   workflow, which contradicts the product's stated purpose.
