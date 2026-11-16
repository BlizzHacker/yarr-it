# Parity: Yarr.It vs Cinephage

[Cinephage](https://github.com/Cinephage/Cinephage) is the all-in-one Yarr.It
competes with. This ledger is how we keep up and pull ahead: every Cinephage
release is flagged by `engine/cinephage-watch.sh` (weekly, in CI), read, and
either matched here or deliberately declined.

**Rule for porting:** Cinephage is GPL-3.0. We match *features* — behaviour,
not code. Nothing is copied from its source.

Status: ✅ shipped · 🟡 partial · ❌ not yet · ➕ Yarr.It only

| Area | Cinephage | Yarr.It | Notes |
|---|---|---|---|
| Film & series monitoring | ✅ | ✅ | Native engine; series by episode, season packs preferred when ≥2 missing |
| Works with an existing *arr stack | ❌ (replaces it) | ➕ | Radarr, Sonarr, Lidarr, Prowlarr, ROMarr, Seerr, GG Requestz, Cinephage itself |
| Indexers | ✅ built-in Cardigann definitions | 🟡 | Any Torznab/Newznab feed, or Prowlarr. Built-in definitions: not yet |
| Release scoring | ✅ 50+ factors, custom formats | 🟡 | Quality order + cutoff, source/codec/PROPER, preferred & rejected words, seeders, size. Custom formats: not yet |
| Download clients | ✅ qBit, SABnzbd, NZBGet, Debrid | 🟡 | qBittorrent. Usenet and Debrid: not yet |
| Import & naming | ✅ TRaSH naming | 🟡 | Hardlink-or-copy, Plex/Jellyfin layout; never overwrites |
| Requests | ✅ (Overseerr-style) | ✅➕ | Native requests **plus** Seerr and GG Requestz in one queue |
| Metadata without a key | ❌ needs TMDB key | ➕ | Keyless via Cinemeta; TMDB optional |
| Streaming | `.strm` files, usenet streaming | ➕ | In-browser torrent streaming with seeking, no server transcode |
| Live TV | ✅ Stalker portals, EPG | 🟡 | Native linear-TV engine, M3U, Plex/Jellyfin live, EPG. Stalker: not yet |
| Subtitles | ✅ 6+ providers | 🟡 | Player subtitles; automated subtitle download: not yet |
| Smart lists | ✅ IMDb/Trakt/TMDb | ❌ | Next |
| Cloudflare bypass | ✅ Camoufox | ❌ | |
| Media-server notify | ✅ Jellyfin/Emby/Plex | ❌ | Next |
| Games | ❌ | ➕ | ROMarr acquisition, RomM library, in-browser play |
| Music, books, comics | ❌ | ➕ | Lidarr, Komga, Kavita, Audiobookshelf |
| TV apps | ❌ | ➕ | Roku, Tizen, Android TV, Fire TV, Xbox |

## Next to close

1. Smart lists (IMDb / Trakt / TMDb list → monitored, with a ledger so fulfilled titles never return)
2. Jellyfin / Plex library refresh after import
3. SABnzbd + Newznab grabs (usenet)
4. Custom formats in the scorer
5. Automated subtitle download

## Releases reviewed

The watcher appends each new Cinephage release below; a person ticks it off
once it has been read and this table updated.

<!-- cinephage-watch: releases below -->
- [x] v0.18.1 (2026-11-03) — baseline for this ledger
