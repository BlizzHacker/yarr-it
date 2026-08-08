# Stream browser extension (MV3)

Turns the whole web into a "view before you download" surface.

## What it does

| Feature | How |
|---|---|
| **LAN relay** | A guest on `https://yarrit.com` can use their own Radarr, Jellyfin, RomM and the rest, with no server and no account. See below — this is the reason the extension exists. |
| **Magnet hijack** | Clicks on any `magnet:` link open in the streamer instead of a desktop torrent client. Hold Ctrl/Cmd/Shift to bypass. |
| **▶ on tracker pages** | A Stream button next to each result on TPB, 1337x, YTS, Nyaa, LimeTorrents, TorrentGalaxy, RuTracker, Archive.org — plus a generic fallback for any page with magnet links. |
| **Omnibox** | Type `st ` then a query in the address bar for live suggestions straight from the search API. |
| **Context menu** | Right-click a magnet to stream it, or right-click selected text to search for it. |
| **Popup** | Search box, magnet paste box, and toggles for the two injection behaviours. |

## The LAN relay

Without this, a guest on the hosted site sees an empty screen. Measured on the
live site:

```
Mixed Content: The page at 'https://yarrit.com/' was loaded over HTTPS, but
requested an insecure resource 'http://192.168.0.26/api/v3/system/status'.
This request has been blocked.
```

`mode: 'no-cors'` does not help — the request never leaves the browser. A
service worker is not a document, so it is not subject to that rule, and it is
also not subject to the page's CORS. That is the whole unlock.

The page side of the contract lives in `stream/web/src/transport.js` and is not
ours to change. The page sets nothing; the extension announces itself:

| Direction | Message |
|---|---|
| extension → page | sets `window.__yarritExtension = true` (MAIN world, `document_start`) |
| page → extension | `postMessage({type:'yarrit:relay:request', id, url, init}, location.origin)` |
| extension → page | `postMessage({type:'yarrit:relay:response', id, status, headers, body, error})` |

`relay-main.js` must run in the **MAIN** world. A normal content script has its
own `window`, so the flag would be invisible to the page and every guest would
be told their library is empty.

### The four gates

A relay into someone's home network is a door, so `relay-bg.js` checks all four:

1. **The page.** `sender.origin` — set by Chrome, unforgeable — must be
   `yarrit.com` or an instance the user added. Never `*`, and matched by exact
   string: `endsWith('yarrit.com')` would also accept `yarrit.com.evil.test`.
2. **The request.** Scheme must be http/https (`file:` and `chrome-extension:`
   are refused), method allowlisted, headers sanitised, body a capped string.
3. **The host.** The user's own allowlist *and* a Chrome host permission granted
   at runtime from a real click. Nothing is granted at install.
4. **The answer.** A redirect that leaves the granted host does not come back.

Cookies never cross, in either direction: `credentials: 'omit'`, `Cookie` and
`Referer` stripped on the way out, `Set-Cookie` on the way back. `X-Api-Key` and
`Authorization` are kept — those are the user's own keys for their own services,
which is the entire job. Six concurrent relays, 32 queued, 10s timeout.

### Two Chrome traps worth knowing

**Match patterns cannot carry a port.** `http://192.168.0.251:8096/*` is not
ignored, it is rejected, and every service this exists for lives on a port —
Jellyfin 8096, RomM 8080, Radarr 7878. Grants are per scheme+host and cover
every port on that host. The options page says so.

**`permissions.remove()` refuses.** It throws *"You cannot remove required
permissions"* for any origin overlapping a manifest content-script match — and
the ▶ feature matches `*://*/*`, so that is every host. Chrome's permission is
therefore only the *mechanism*; the extension keeps its own allowlist in
`storage.sync` (`relayHosts`) and that is what the relay checks and what Revoke
empties. An off-switch that depends on an API which can refuse is not an
off-switch.

For the same reason `permissions.getAll()` is not used to render the list — it
returns content-script matches too, so it reports `*://*/*` as granted.

### Seeing it and turning it off

`options.html` lists every allowed host with a Revoke beside it, every page
allowed to drive the relay, and anything waiting on permission. The toolbar
badge shows how many services are waiting.

## Why the magnet hijack works this way

MV3 removed blocking `webRequest`, and an extension cannot register a web page
as a `magnet:` protocol handler. So the click is caught in a **capture-phase
listener in the content script** — before the page's own handlers and before
Chrome starts the external-protocol navigation. That is the only route that
works in MV3.

Site layouts live in `sites.js` as **data, not code**: supporting a new tracker
is a table entry, not a code change.

## Install (unpacked, for testing)

1. `chrome://extensions` → enable **Developer mode**
2. **Load unpacked** → select this directory

## Tests

```bash
node --test          # relay-core.test.js: the rules the relay enforces
```

## Package for the Chrome Web Store

```bash
bash package.sh      # runs the tests, then dist/yarrit-extension-<version>.zip
```

The file list is derived from the manifest rather than kept beside it, and the
zip is built entry by entry: `Compress-Archive` writes Windows separators, so
`icons/icon-48.png` ships as a single file named `icons\icon-48.png` and Chrome
cannot resolve the manifest's reference to it.

Upload the zip in the Chrome Web Store developer dashboard. The store requires a
one-time $5 developer registration.

## Permissions, and why each is needed

- `contextMenus` — the right-click entries.
- `storage` — the two toggles, the relay allowlist, the trusted-page list.
- `scripting` — registering the relay content scripts on an instance the user
  adds; `offscreen` is reserved for the picture-in-picture player.
- `host_permissions: yarrit.com` — the omnibox queries the search API.
- `optional_host_permissions: *://*/*` — the pool the relay draws from. Nothing
  in it is granted at install: each host is requested individually, at the
  moment it is needed, from a click in the popup or options page. Asking for
  `*://*/*` up front is what makes people uninstall.
