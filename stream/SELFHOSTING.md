# Running your own Yarr.It

Every client here is built so that nothing depends on `yarrit.com`. That address
is a **default**, not a requirement — and this is how you stop using it.

Read the next section first. It is the part people are surprised by.

## There has to be a server. Here is why.

Yarr.It cannot be a page you open with nothing behind it. Three things make that
impossible, and none of them are implementation choices that could be worked
around with more effort:

- **Torrent indexers need API keys and send no CORS headers.** A browser refuses
  to read a cross-origin response that does not explicitly permit it, and
  indexers do not permit it. A page cannot query them directly, however the code
  is written.
- **Catalogue API keys must never ship inside a client.** TMDB and IGDB
  credentials in a downloadable app are credentials you have given away. They
  have to live somewhere the user cannot read them, which means a server.
- **A browser cannot open a TCP socket to a peer.** Browsers expose HTTP,
  WebSocket and WebRTC and nothing else. BitTorrent peers speak TCP and trackers
  speak UDP, so a page on its own reaches only the small WebRTC subset of a
  swarm — which is why a 50-seeder torrent in a plain in-browser client finds
  zero peers.

So the question was never "server or no server". It was **whose** server. The
answer here is: yours, if you want it to be.

## Prowlarr is required, and it is not part of this

Yarr.It does not scrape tracker sites. It asks **Prowlarr**, which is the piece
that knows how to talk to each indexer and holds the credentials for them.
Prowlarr is a separate application that you run and maintain — if you already
use Sonarr, Radarr or Lidarr, you almost certainly have it already, and Yarr.It
just needs its address and API key.

`mw-search` will not start without `PROWLARR_API_KEY`. That is deliberate: with
no indexer there is nothing to search, and a server that starts and then answers
every query with silence is worse than one that refuses to start and says why.

The key is in Prowlarr under **Settings → General → API Key**.

## What each platform can actually do

| Platform | Can it run the server? | What it does instead |
|---|---|---|
| Desktop — Windows, macOS, Linux | **Yes** | Runs all three services locally. Fully independent of anyone else's machine. |
| Web browser | No | Points at any server. By default, the one that served the page. |
| Android, Fire TV, Tizen, Xbox | No | Point at any server, set in the app. |
| Roku | No | Points at any server, set in the channel. Also needs a gateway on the LAN. |

One nuance the table cannot hold. The Android, Tizen and Windows/Xbox packages
are wrappers around a web page, and the page they load is `yarrit.com` unless the
package is rebuilt. Setting your own server inside the app moves the **data** to
your box — the catalogue and the search — while the page itself is still
delivered by ours, and the watchlist stops working, because personal data never
crosses origins. To take those clients off `yarrit.com` entirely you rebuild the
package against your own address: the Windows/Xbox MSIX reads `STREAM_START_PAGE`
(see [`installers/build_msix.mjs`](installers/build_msix.mjs)), and the Tizen and
Android builds carry the URL in their own manifests — an Android rebuild also
needs an `assetlinks.json` on your host, or it falls back to showing a browser
URL bar. Roku is the exception: its address is a genuine setting, so nothing has
to be rebuilt.

### The honest version for TV devices

"Not reliant on yarrit.com" means reliant on **some** server, which is normally
your own box. A Roku, an Xbox, a Fire Stick and a Samsung TV cannot run a
general-purpose service — no shell, no package manager, no way to keep a process
alive, and in Roku's case no browser engine, no WebRTC and no usable socket API
at all. No amount of work on our side changes that. It is a property of the
devices.

So the shape of a TV setup is always the same, and it is worth being blunt about
it: **you need one always-on machine on your network** — a desktop, a NAS, a
Raspberry Pi, an old laptop — and every TV in the house points at it. There is no
hosted gateway to fall back on and there is not going to be one. The machine that
joins a swarm on a television's behalf is yours, running on your connection.

If you have no such machine, the browser on a desktop still works on its own.
The televisions are what need the box.

## Pointing a client at a server

Three ways, most specific first:

1. **Whatever the user set.** In the web app that is the Server box in Settings.
   On Roku it is the Settings screen inside the channel.
2. **`?server=` in the URL** — `https://yarrit.com/?server=https://mybox:8800`.
   A link can carry an instance, which is the easy way to hand a working address
   to somebody else.
3. **The origin that served the page**, when neither of the above is set.

That last rule is the one that matters for self-hosting: **leave the setting
blank**. Your copy is served by your own instance, so every call already points
at you and there is nothing to configure. The Settings box says so too — blank
means "this site".

Two details worth knowing:

- **Roku takes one address and derives the rest.** Enter `http://yourbox:8800`
  and the channel works out the playback gateway itself, as the same host on port
  **8900**. That port is fixed in the channel, so do not move the gateway off it.
- **Personal data stays same-origin.** The watchlist and resume points never
  travel with a cross-origin request — a session cookie belongs to the instance
  that issued it, and posting it to another server because a URL said so would be
  a real leak. So if you point a client at your server while the *page* still
  comes from somewhere else, the catalogue works and the shelf does not. Serving
  the page from your own instance avoids the whole question.

## Install it: Linux or macOS

```bash
curl -fsSL https://yarrit.com/selfhost.sh | bash
```

It checks for git, Go 1.24+ and Node, clones the repo into `~/.yarrit/src`,
builds the services into `~/.yarrit/bin`, builds the web app, and writes
`~/.yarrit/config.env` — which it will not overwrite if you already have one.
Then put your Prowlarr key in that file and run `~/.yarrit/start.sh`.

Nothing in it needs root. Everything runs as you, on high ports.

One thing to know about `start.sh` as it currently stands: it launches the peer
bridge and the search API. It does not start the gateway, and it does not serve
the built web app in `~/.yarrit/www` — on a box that already runs Caddy or nginx,
point it at that directory and proxy `/api`, `/auth` and `/bridge` to the two
services, the way [`deploy/Caddyfile`](deploy/Caddyfile) does. Without a
television in the picture the gateway is not needed; with one, run
`~/.yarrit/bin/mw-gateway -addr 0.0.0.0:8900 -data ~/.yarrit/data` beside them.
The Windows installer does all of this for you.

## Install it: Windows

```powershell
powershell -ExecutionPolicy Bypass -File .\Install-Local-Server.ps1
```

The same job, making the same decisions, from
[`installers/Install-Local-Server.ps1`](installers/Install-Local-Server.ps1). It
does not need Administrator.

You need **Go 1.24 or newer**, **Node.js** and **Git** first. The script names
whatever is missing and where to get it, and stops — it will not install
anything on your behalf. After installing a toolchain, open a new terminal, or
PATH will still look empty to the window you were in.

What it does:

- clones or updates the repo into `%LOCALAPPDATA%\Yarrit\src`
- builds `mw-search.exe`, `mw-bridge.exe` and `mw-gateway.exe` into
  `%LOCALAPPDATA%\Yarrit\bin`
- builds the web app with esbuild into `%LOCALAPPDATA%\Yarrit\www`
- writes `%LOCALAPPDATA%\Yarrit\config.env`, and leaves an existing one alone
- writes `%LOCALAPPDATA%\Yarrit\Start-Yarrit.ps1`
- tries to add one Windows Firewall rule, and prints the command if it cannot

Then:

```powershell
powershell -ExecutionPolicy Bypass -File "$env:LOCALAPPDATA\Yarrit\Start-Yarrit.ps1"
```

and open **http://localhost:8800/**.

Uninstalling is deleting `%LOCALAPPDATA%\Yarrit`. Nothing is registered with
Windows and no service is installed. If you want it running at boot, point a
Task Scheduler task at `Start-Yarrit.ps1`.

### The firewall step

Adding a firewall rule needs an elevated prompt, and the installer deliberately
does not demand one. It tries, and if it is refused it prints exactly this for
you to run once in an Administrator PowerShell:

```powershell
New-NetFirewallRule -DisplayName 'Yarr.It (self-hosted)' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 8800,8802,8900 -Profile Private
```

Skip it and this machine still works perfectly — and every TV in the house times
out with no explanation at all. That failure is silent from the TV's side, which
is why it is called out here rather than left to be discovered.

The rule is scoped to **Private** networks. If Windows has this network marked
Public — which it does by default for networks it does not recognise — change the
network to Private or the rule will not apply.

### What runs, and on which port

| Port | Process | Bound to | Who talks to it |
|---|---|---|---|
| 8800 | front door (Node) | LAN | browsers and TVs — serves the app, proxies the rest |
| 8802 | `mw-search` | LAN | the catalogue API |
| 8801 | `mw-bridge` | loopback | reached only through the front door |
| 8900 | `mw-gateway` | LAN | TV clients, turning a magnet into an HTTP stream |

The front door exists because production puts Caddy in front of these and serves
everything from one origin. That single origin is not cosmetic — it is what makes
"leave the setting blank" work, and it is what keeps the watchlist same-origin.

## What goes in config.env

Same keys on both platforms, so a config can be carried between a Linux box and
a Windows one unchanged.

| Key | Needed? | What it does |
|---|---|---|
| `PROWLARR_URL` | yes | where your Prowlarr is |
| `PROWLARR_API_KEY` | **yes** | `mw-search` will not start without it |
| `TMDB_API_KEY` | no | film and TV artwork; without it the catalogue is plainer |
| `IGDB_CLIENT_ID` / `IGDB_CLIENT_SECRET` | no | game artwork |
| `SSO_CLIENT_ID` / `SSO_CLIENT_SECRET` / `SESSION_SECRET` | no | sign-in |
| `AUTH_SCOPE` | no | `off` runs the server open, which is the sensible default for a box only you can reach |
| `LIBRARY_PATH` | no | where the watchlist and resume points are kept |

## Honest limits of a local install

None of these are bugs to be filed. They are consequences of running on plain
HTTP on a home network, and it is better to know them up front.

- **The peer relay cannot be driven from an HTTPS page.** The client derives the
  relay's scheme from wherever it is pointed, so a plain-`http://` local install
  gets ordinary TCP peers like any other — no TLS terminator required. The one
  case that cannot work is mixing the two: opening the app over `https://` while
  pointing it at a plain-`http://` server of your own. That needs an insecure
  WebSocket from a secure page, which every browser blocks as mixed content, and
  no amount of configuration changes it. Load the app from the same box you are
  pointing it at, or put TLS on that box. Television clients are unaffected —
  they go through the gateway, which joins swarms properly.
- **Browsers only trust `localhost`.** The in-browser player streams through a
  service worker, and browsers refuse to register one on a plain-`http://` LAN
  address because it is not a secure context. So the browser on the machine
  itself (`http://localhost:8800/`) plays; a browser on another machine reaching
  `http://192.168.x.x:8800/` will load the app and fail to play. TV clients do
  not use a service worker and are unaffected.
- **The watchlist needs sign-in configured.** Those endpoints belong to a user,
  so with `SSO_*` blank the server answers "accounts are not configured" and the
  app simply draws no shelf. Everything else works.
- **The gateway joins swarms as you.** It really does fetch and re-serve the
  media, from your connection and your IP address, exactly as any BitTorrent
  client does. The public instance runs it behind a VPN for that reason. **Use a
  VPN.**
- **Windows may prompt for the gateway.** Its peer traffic listens on its own
  port, so Windows Defender may ask about `mw-gateway.exe` the first time it
  runs. Allowing it on private networks is enough.

## Nothing here is a downloader

Yarr.It plays; it does not save. It hosts no media, indexes nothing itself, and
stores nothing on any server of ours — running it yourself means it stores
nothing on anyone's server but your own. It is a client for public indexes and
peer-to-peer networks the way a browser is a client for websites. You are
responsible for complying with the laws where you live.
