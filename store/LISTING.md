# Store listing copy — Stream

One source of truth for both Google Play and the Microsoft Store. Copy the
fields straight out of here; the character limits noted are the store's.

---

## App identity

| Field | Value |
|---|---|
| App name | **Stream** |
| Full name | Stream — view before you download |
| Android package | `com.moveweight.stream` |
| MSIX identity | `MOVEWEIGHT.Stream` |
| Publisher | MOVE WEIGHT (`CN=6375D74B-5E4F-45B4-B246-B29507C1332A`) |
| Website | https://stream.moveweight.com |
| Privacy policy | https://stream.moveweight.com/privacy.html |
| Support email | me@moveweight.com |
| Category | Entertainment (Play) / Multimedia & video (Microsoft) |
| Price | Free, no in-app purchases, no ads |

---

## Google Play

### Short description (80 chars max)

```
Search every torrent index and stream the result. Nothing downloads or installs.
```

*(79 characters.)*

### Full description (4000 chars max)

```
Stream is a search and playback tool for the open web. Search across dozens of
public torrent indexes at once, then play the result immediately — films, TV,
music and images — without downloading anything first.

VIEW BEFORE YOU DOWNLOAD

Most tools make you commit to a file before you know whether it is the right
one, the right quality, or even watchable. Stream flips that around: press play
and the video starts in seconds, while the rest is still arriving. If it is the
wrong cut, the wrong language, or a bad rip, you find out in five seconds
instead of forty minutes.

IT PLAYS ON YOUR DEVICE, NOT OUR SERVER

We host nothing and stream nothing. Your device joins the swarm directly,
verifies each piece, and decodes it locally using your own hardware. There is no
transcoding server in the middle, no library sitting on our disks, and no copy
of anything kept anywhere.

A LIBRARY, NOT A DIRECTORY LISTING

Results are grouped by title rather than dumped as a flat list of filenames. One
film is one poster — with artwork, synopsis and rating — and a quality picker
underneath showing every available source, its resolution, codec, size and
seeder count. Sources that your device can play without help are marked, so you
always know what will start instantly.

REAL FILTERS

Filter by category, minimum seeders, file size, resolution and codec, or sort by
best match, most seeders, quality, size or newest. Adult material is filtered
out by default and only appears if you deliberately turn it on.

BROWSE, DON'T JUST SEARCH

Trending, popular, top rated and in-cinemas-now rails on the home screen, so
there is somewhere to start when you do not already know what you want.

FREE AND OPEN

No accounts. No sign-up. No advertising. No analytics. No tracking. No payment
of any kind. The whole thing is open source.

PLEASE USE A VPN

Streaming is peer-to-peer, which means your device connects directly to other
people sharing the same file, and your IP address is visible to them — exactly
as it is with any BitTorrent client. That is inherent to how peer-to-peer
works. We recommend using a VPN.

WHAT THIS APP IS NOT

Stream does not host, upload, index or store any media. It is a client for
publicly available indexes and peer-to-peer networks, in the same way a web
browser is a client for websites. You are responsible for complying with the
laws of your country regarding the material you access.
```

### What's new (500 chars max) — v1.0.0

```
First release.

• Search dozens of public indexes in one query
• Play instantly in the app — nothing downloads first
• Results grouped by title with artwork, synopsis and ratings
• Filter by category, seeders, size, resolution and codec
• Adult content filtered out by default
• Trending and popular rails to browse
• No accounts, no ads, no tracking
```

### Content rating questionnaire (IARC) — answers

Answer these truthfully; a wrong answer here is what gets an app pulled later.

| Question | Answer | Note |
|---|---|---|
| Violence | None in the app itself | The app contains no content of its own |
| Sexuality | None by default | Adult results are filtered out unless the user explicitly enables an 18+ toggle |
| Profanity | None in the app itself | — |
| Controlled substances | None | — |
| Gambling | None | — |
| Users interact | No | No accounts, no chat, no user-to-user features |
| Shares location | No | — |
| Shares personal info | No | — |
| Digital purchases | No | — |
| User-generated content | **Yes** | Search results come from third-party indexes; declare this |

Expected rating: **Teen / PEGI 12** because of unfiltered third-party search
results, even though the app ships nothing itself.

### Data safety form — answers

| Section | Answer |
|---|---|
| Does your app collect or share user data? | **No** |
| Is data encrypted in transit? | Yes (HTTPS/TLS throughout) |
| Can users request data deletion? | No data is collected, so there is nothing to delete |
| Data types collected | None |

Note for the reviewer field: *search terms are sent to third-party indexes to
return results and are cached transiently without being linked to any user,
device or identifier.*

---

## Microsoft Store (Partner Center)

### Short title (50 chars)

```
Stream — view before you download
```

### Description

Reuse the Play full description above; the Microsoft Store allows 10,000
characters, so it fits without trimming.

### Product features (up to 20, ~200 chars each)

```
Search dozens of public torrent indexes in a single query
Play immediately — video starts in seconds, nothing downloads first
Runs on your own device; no media is hosted, stored or transcoded by us
Results grouped by title with poster art, synopsis and ratings
Quality picker showing resolution, codec, size and seeder count per source
Filter by category, minimum seeders, file size, resolution and codec
Adult content filtered out by default behind an explicit 18+ toggle
Trending, popular and top-rated rails to browse
No accounts, no advertising, no analytics, no tracking
Free and open source
```

### Search terms (7 max, 30 chars each)

```
torrent streaming
magnet player
stream torrents
webtorrent
p2p video player
torrent search
media streaming
```

### Age rating

Same answers as the IARC questionnaire above. Declare **user-generated /
third-party content**.

---

## Required assets

See `ASSETS.md` for exact sizes and how they are generated.

## Pre-submission checklist

- [ ] Privacy policy URL returns 200 — https://stream.moveweight.com/privacy.html
- [ ] App installs and runs on a real device (Android) — see `TESTING.md`
- [ ] App installs and runs on a real machine (Windows) — see `TESTING.md`
- [ ] Search returns results inside the app
- [ ] Playback actually starts inside the app
- [ ] Adult filter verified ON by default in a fresh install
- [ ] VPN notice visible on first run
- [ ] Version numbers match across manifest, AAB and MSIX
- [ ] Screenshots captured from the real app, not the website
