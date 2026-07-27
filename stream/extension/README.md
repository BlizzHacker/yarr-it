# Stream browser extension (MV3)

Turns the whole web into a "view before you download" surface.

## What it does

| Feature | How |
|---|---|
| **Magnet hijack** | Clicks on any `magnet:` link open in the streamer instead of a desktop torrent client. Hold Ctrl/Cmd/Shift to bypass. |
| **▶ on tracker pages** | A Stream button next to each result on TPB, 1337x, YTS, Nyaa, LimeTorrents, TorrentGalaxy, RuTracker, Archive.org — plus a generic fallback for any page with magnet links. |
| **Omnibox** | Type `st ` then a query in the address bar for live suggestions straight from the search API. |
| **Context menu** | Right-click a magnet to stream it, or right-click selected text to search for it. |
| **Popup** | Search box, magnet paste box, and toggles for the two injection behaviours. |

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

## Package for the Chrome Web Store

```bash
bash package.sh      # produces dist/stream-extension-<version>.zip
```

Upload the zip in the Chrome Web Store developer dashboard. The store requires a
one-time $5 developer registration.

## Permissions, and why each is needed

- `contextMenus` — the right-click entries.
- `storage` — remembers the two toggles.
- `scripting` / `offscreen` — reserved for the picture-in-picture player.
- `host_permissions: stream.moveweight.com` — the omnibox queries the search API.
- `optional_host_permissions: *://*/*` — the content script only needs this on
  sites the user actually browses; it is optional so the install prompt is not
  alarming.
