# Standalone, or with what you already run

Yarr.It is a whole media manager on its own, and it is also a front door for
an existing *arr stack. Which one answers is decided per kind of media, and
mixing them is normal.

| | Standalone (Yarr.It does it) | Bring your own |
|---|---|---|
| Films & series | **Yarr.It engine**: monitor, search indexers, score, grab, file | Radarr, Sonarr (or an existing Cinephage) |
| Indexers | Any Torznab / Newznab feed, set in Yarr.It | Prowlarr |
| Download client | qBittorrent (bundled with `--profile downloader`, or yours) | the one your *arrs use |
| Requests | **Yarr.It requests**: anyone signed in asks, the owner approves | Seerr, GG Requestz — shown in the same queue |
| Games | ROMarr (bundled) | your ROMarr |
| Music, books, comics | — | Lidarr, Komga, Kavita, Audiobookshelf |
| Search, streaming, play, shelves, TV apps | Yarr.It | Yarr.It |

A service you bring is preferred over the engine for the same job, so adding
an existing Radarr takes over films while the engine keeps covering series.
"Send requests to the engine" in Settings → Stack flips that.

## Install

```bash
git clone https://github.com/BlizzHacker/yarr-it.git && cd yarr-it
cp engine/env.example engine/.env
docker compose --env-file engine/versions.env --env-file engine/.env \
  -f engine/compose.yaml --profile downloader up -d
```

Open `http://<host>:8800` → Settings → Stack. Add an indexer, confirm the
qBittorrent login, and search for something to add. Films land in
`$MEDIA_DIR/movies/Title (Year)/`, series in `$MEDIA_DIR/tv/Title (Year)/Season 01/`.

Sign-in (`SSO_*` in `.env`, any OIDC provider) is what opens the owner
screens. Without it Yarr.It never treats an anonymous visitor as the owner.

## The engine

- **Library** — films, and series down to the episode. Unaired episodes are
  never searched for.
- **Search** — every enabled indexer at once. Torznab/Newznab feeds directly
  (no Prowlarr needed), or Prowlarr's aggregate search if you run one.
- **Scoring** — your quality order and cutoff, source (REMUX > BluRay >
  WEB-DL > …), codec, PROPER/REPACK, preferred and rejected words, seeders,
  size. CAM/TS/TC/screeners are rejected by default. Interactive search shows
  every candidate with the reason any was turned down.
- **Season packs** — two or more missing episodes in a season ask for the pack
  first, then fall back to single episodes.
- **Grabbing** — qBittorrent, with the category `yarrit`. A `Fails.` answer is
  an error, a restarted client gets one fresh login.
- **Filing** — hardlink, or copy across filesystems; never move (the client is
  still seeding) and never overwrite. Plex/Jellyfin/Emby layout.
- **Scheduling** — finished downloads filed every minute; missing titles
  searched every 12 hours, backing off for titles nobody seeds.
- **Metadata** — keyless by default (Cinemeta); TMDB optional.

State is one JSON file (`/data/engine.json`, mode 600 — it holds indexer keys
and the client password). API keys and passwords are never sent back to the
browser.

## Keeping ahead of Cinephage

Cinephage is the all-in-one Yarr.It competes with.
[`docs/parity/cinephage.md`](../docs/parity/cinephage.md) is the honest
side-by-side, and `engine/cinephage-watch.sh` (weekly in CI) flags every new
Cinephage release for review, so each feature it ships is matched here or
knowingly declined. We match behaviour, never code: Cinephage is GPL-3.0.
