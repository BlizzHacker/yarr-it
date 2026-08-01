#!/usr/bin/env bash
# Run your own Yarr.It.
#
#   curl -fsSL https://yarrit.com/selfhost.sh | bash
#
# What you get: the search API, the peer bridge and the torrent gateway, all on
# your own machine, serving the same web app. Your apps point at your box and
# never touch anyone else's.
#
# What you still need is an indexer. Yarr.It does not scrape sites itself -- it
# asks Prowlarr, which is the piece that knows how to talk to each tracker and
# holds the credentials. If you already run Prowlarr (most *arr users do), give
# this its address. If not, it will offer to run one for you.
#
# Deliberately does NOT need root for the services themselves: everything runs
# as your user on high ports, so a mistake here cannot damage the machine.
set -euo pipefail

REPO=${REPO:-https://github.com/BlizzHacker/yarr-it.git}
BRANCH=${BRANCH:-feat/universal-source-layer}
HOME_DIR=${YARRIT_HOME:-$HOME/.yarrit}
SRC="$HOME_DIR/src"
BIN="$HOME_DIR/bin"
WWW="$HOME_DIR/www"
# The front door. This is the only port anyone types: it serves the web app and
# passes /api, /auth and /bridge through to the services behind it. That single
# origin is not tidiness -- the client falls back to "whichever server sent this
# page", and the watchlist and resume points are same-origin only, so splitting
# the page from the API means configuring every device by hand and losing the
# shelf even then. In production Caddy does this job; here Node does.
WEB_PORT=${YARRIT_WEB_PORT:-8800}
PORT=${YARRIT_PORT:-8802}
BRIDGE_PORT=${YARRIT_BRIDGE_PORT:-8801}
# The gateway is reached directly by TVs, not through the front door, so it has
# to be on the LAN rather than loopback.
GATEWAY_PORT=${YARRIT_GATEWAY_PORT:-8900}

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1; }

say "checking what is already here"
MISSING=()
need git || MISSING+=(git)
need go || MISSING+=(golang)
need node || MISSING+=(nodejs)
if [ ${#MISSING[@]} -gt 0 ]; then
  echo "    missing: ${MISSING[*]}"
  echo "    install them and re-run. On Debian/Ubuntu:"
  echo "      sudo apt install -y git golang nodejs npm"
  echo "    Go must be 1.24 or newer; Debian's package is older, so if the"
  echo "    build complains about the version, take it from https://go.dev/dl/"
  exit 1
fi
printf '    go %s, node %s\n' "$(go version | awk '{print $3}')" "$(node -v)"

say "fetching the source into $SRC"
mkdir -p "$HOME_DIR" "$BIN" "$WWW"
if [ -d "$SRC/.git" ]; then
  git -C "$SRC" fetch --depth 1 origin "$BRANCH" && git -C "$SRC" reset --hard FETCH_HEAD
else
  git clone --depth 1 --branch "$BRANCH" "$REPO" "$SRC"
fi

say "building"
for svc in search bridge gateway; do
  [ -d "$SRC/stream/$svc" ] || continue
  ( cd "$SRC/stream/$svc" && CGO_ENABLED=0 go build -trimpath -o "$BIN/mw-$svc" . )
  echo "    mw-$svc"
done

say "building the web app"
( cd "$SRC/stream/web" \
  && npm install --silent --no-audit --no-fund >/dev/null 2>&1 \
  && ./node_modules/.bin/esbuild src/main.js --bundle --format=esm \
       --outfile=dist/app.js --define:global=globalThis \
       --external:./webtorrent.min.js --minify >/dev/null )
cp -f "$SRC"/stream/web/dist/*.js "$WWW"/ 2>/dev/null || true
cp -f "$SRC"/stream/web/node_modules/webtorrent/dist/webtorrent.min.js "$WWW"/ 2>/dev/null || true
cp -f "$SRC"/stream/web/node_modules/webtorrent/dist/sw.min.js "$WWW"/ 2>/dev/null || true
sed -e "s/__BUILD__/local/g" -e "s/__ICON__/local/g" \
    "$SRC/stream/web/index.html" > "$WWW/index.html"
for m in engine.js bridge-peer.js server.js tracker-udp.js dht.js bencode.js; do
  cp -f "$SRC/stream/web/src/$m" "$WWW"/ 2>/dev/null || true
done
# The front door itself. Node standard library only, so there is nothing to
# install and it runs the same here as it does on Windows.
cp -f "$SRC/stream/installers/serve-local.mjs" "$HOME_DIR/serve-local.mjs"

say "configuration"
CONF="$HOME_DIR/config.env"
if [ ! -f "$CONF" ]; then
  cat > "$CONF" <<EOF
# Where your Prowlarr lives, and its API key (Settings -> General in Prowlarr).
# Without these, search returns nothing -- everything else still works.
PROWLARR_URL=http://127.0.0.1:9696
PROWLARR_API_KEY=

# Optional artwork. Without them the catalogue still works, just plainer.
TMDB_API_KEY=
IGDB_CLIENT_ID=
IGDB_CLIENT_SECRET=

# Sign-in is off unless you fill these in. Leave blank to run open, which is
# the sensible default for a server only you can reach.
SSO_CLIENT_ID=
SSO_CLIENT_SECRET=
SESSION_SECRET=
AUTH_SCOPE=off

# Where your watchlist and resume points are kept.
LIBRARY_PATH=$HOME_DIR/library.json
EOF
  echo "    wrote $CONF"
else
  echo "    keeping the $CONF you already have"
fi

cat > "$HOME_DIR/start.sh" <<EOF
#!/usr/bin/env bash
# Starts all four pieces and stops the lot if any one of them dies. A partial
# stack is the worst outcome: the page still loads, so it looks like it works
# right up until the part that is missing is the part you needed.
set -a; . "$CONF"; set +a

pids=()
cleanup() { kill "\${pids[@]}" 2>/dev/null; }
trap cleanup EXIT INT TERM

"$BIN/mw-bridge" -addr 127.0.0.1:$BRIDGE_PORT & pids+=(\$!)
"$BIN/mw-search" -addr 127.0.0.1:$PORT -prowlarr "\${PROWLARR_URL}" & pids+=(\$!)
# Bound to the LAN on purpose: the TVs talk to this one directly, and on
# loopback it would be useless to every device except this machine.
if [ -x "$BIN/mw-gateway" ]; then
  "$BIN/mw-gateway" -addr 0.0.0.0:$GATEWAY_PORT & pids+=(\$!)
fi
YARRIT_WWW="$WWW" \\
YARRIT_WEB_ADDR=0.0.0.0:$WEB_PORT \\
YARRIT_SEARCH_ADDR=127.0.0.1:$PORT \\
YARRIT_BRIDGE_ADDR=127.0.0.1:$BRIDGE_PORT \\
  node "$HOME_DIR/serve-local.mjs" & pids+=(\$!)

# Report which one fell over rather than exiting silently.
wait -n "\${pids[@]}"
echo "a Yarr.It service exited; shutting the rest down" >&2
EOF
chmod +x "$HOME_DIR/start.sh"

say "done"
cat <<EOF

  1. Put your Prowlarr address and API key in:
       $CONF

  2. Start it:
       $HOME_DIR/start.sh

  3. Open it, and point any Yarr.It app at this same address:
       http://$(hostname -I 2>/dev/null | awk '{print $1}'):$WEB_PORT

     In the web app that is the server box in Settings. On a Roku it is under
     Settings in the channel. Opening the address directly needs no setting at
     all -- a client served by your instance already talks to it.

  Nothing here phones home. yarrit.com is only the default for clients that
  have not been told otherwise.

  If you want this machine's torrent traffic to leave over your own VPN, put
  your provider's WireGuard config at /etc/wireguard/<name>.conf and run:

      sudo $SRC/stream/deploy/vpn-killswitch.sh <name>

  That seals the ordinary network interface, so if the tunnel ever drops the
  traffic stops instead of quietly falling back to your home address. Your LAN
  keeps working, because the TVs in the house were never meant to go through
  the tunnel anyway.
EOF
