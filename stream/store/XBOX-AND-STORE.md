# Xbox, and the Microsoft Store submission

Two separate things, and the first one changes whether the second is worth
doing today.

---

## 1. The app will not fix the Xbox

Your MSIX is a **hosted web app**:

```xml
<Application Id="App" StartPage="https://yarrit.com/" />
```

There is no bundled code. On Xbox it opens yarrit.com in the same Edge engine
the browser already uses. It changes the frame around the page — full screen,
a tile on the dashboard, no address bar — and nothing underneath.

**So if the site does not work in the Xbox browser, the app will not work
either.** Worth knowing before you spend a submission on it.

### Find out what is actually failing — 30 seconds

On the Xbox, open Edge and go to:

> **yarrit.com/xbox-check.html**

It tests eight things and prints a verdict in text you can read from the sofa.
Tell me the last line, or the short `Service worker=Y/N …` summary at the
bottom.

### What I expect it to say

Torrent playback needs a **service worker** — that is what serves the video
while it is still downloading. Without one the code falls back to a blob, which
means the *entire file* must finish before anything appears. On a film that is
indistinguishable from the app hanging, which matches "not working".

If that is the failure:

- **Archive.org results marked OPEN will still play** — those are ordinary HTTP
  downloads and need no worker
- torrents will not, on that device, in a browser or in an app
- the fix is a different playback path for Xbox, not packaging

Which is exactly the design your own RomM-for-Xbox work landed on: server-side
rendering streamed over WebRTC, rather than asking the console to do it.

---

## 2. Sideloading needs Developer Mode

I scanned the LAN: nothing is answering on port 11443, so the console is not in
Developer Mode and there is nothing for me to upload to.

Your `RommForXbox` README says the same thing — *"no dev mode, no
sideloading"*.

To change that: **Dev Mode Activation** app on the console, which needs a
Partner Center developer account. It is a one-off, and it is yours to do — it
requires signing in on the console. Once it is on, Device Portal opens on
`https://<xbox-ip>:11443` and I can push builds to it directly, the same way I
do with your Roku.

---

## 3. The Store submission — ready to click Submit

Package built and verified:

| | |
|---|---|
| File | `installers/dist/Yarr.It_1.0.0.0_neutral.msix` |
| Identity | `MOVEWEIGHT.Stream` |
| Publisher | `CN=6375D74B-5E4F-45B4-B246-B29507C1332A` |
| Display name | Yarr.It |
| Version | 1.0.0.0 |
| Device families | `Windows.Universal`, **`Windows.Xbox`** |
| Start page | `https://yarrit.com/` |
| Assets | StoreLogo, Square150x150, Square44x44, SplashScreen |

Identity and publisher match Partner Center exactly. Change either and the
upload is rejected.

### At partner.microsoft.com/dashboard

1. **Packages** → upload `Yarr.It_1.0.0.0_neutral.msix`. Do not sign it
   yourself; the Store re-signs on ingestion.
2. **Availability** — markets, and free.
3. **Properties** → category **Entertainment**.
4. **Age ratings** → the IARC questionnaire. Answer honestly: the app displays
   content from third-party indexes that is not moderated by you. Understating
   this is the most common cause of a later takedown.
5. **Store listing** — copy from `store/LISTING.md`.
6. **Submit**.

### 🔶 Two things to expect from certification

**It is a hosted app with no offline behaviour.** Microsoft sometimes rejects
web wrappers that add nothing beyond the website. The honest counter is that it
adds a TV-shaped launcher, a dashboard tile and a controller-friendly surface.

**The content question is the real one.** A search interface over public torrent
indexes will draw scrutiny, and the reviewer will follow the app to the live
site. Everything there is a search result — nothing is hosted, transcoded or
stored — and the listing should say so plainly rather than leave them to guess.

Neither is a reason not to submit. Both are reasons to answer the ratings
questionnaire carefully rather than optimistically.
