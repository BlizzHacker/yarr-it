#!/usr/bin/env bash
# Build and deploy the stream.moveweight.com frontend.
#
# Cache-busting is the point of this script. /app.js is a stable URL, so a
# browser will happily keep serving a stale copy after a deploy -- which once
# made a whole round of "verification" run against old code. Every build stamps
# a content hash into the script URL, so a new build is always a new URL.
set -euo pipefail
cd "$(dirname "$0")"

JUMP=root@192.168.0.6
KEY=/root/.ssh/vps_edge
VPS=root@104.129.28.137
WWW=/srv/stream/www

ESBUILD=./node_modules/@esbuild/win32-x64/esbuild.exe
[ -x "$ESBUILD" ] || ESBUILD=./node_modules/.bin/esbuild

echo "==> bundling"
"$ESBUILD" src/main.js --bundle --format=esm --outfile=dist/app.js \
  --define:global=globalThis --external:./webtorrent.min.js --minify

cp -f node_modules/webtorrent/dist/webtorrent.min.js dist/
cp -f node_modules/webtorrent/dist/sw.min.js dist/

HASH=$(sha256sum dist/app.js | cut -c1-12)
echo "==> build $HASH"

# Icons need the same treatment as app.js and for the same reason. They sit on
# stable URLs behind `Cache-Control: max-age=86400`, so after a rebrand both
# Cloudflare and every visitor's browser keep serving the OLD mark for a day --
# which reads as "the rebrand did not ship" even though it did.
ICONHASH=$(sha256sum dist/icon-512.png | cut -c1-12)
echo "==> icons $ICONHASH"

sed -e "s/__BUILD__/$HASH/g" -e "s/__ICON__/$ICONHASH/g" index.html > dist/index.html
sed -e "s/__ICON__/$ICONHASH/g" manifest.webmanifest > dist/manifest.webmanifest

push() {
  ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $VPS 'cat > $WWW/$1'" < "$2"
}

# Whole directories go over as one tar stream. Ruffle alone is 9 files and 28MB,
# and a `cat` per file over a double hop is painfully slow.
pushdir() {
  local local_dir=$1 remote_dir=$2
  [ -d "$local_dir" ] || { echo "   skip $remote_dir (not fetched)"; return 0; }
  echo "   $remote_dir"
  tar -cz -C "$(dirname "$local_dir")" "$(basename "$local_dir")"     | ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $VPS 'mkdir -p $WWW/$remote_dir && tar -xz --strip-components=1 -C $WWW/$remote_dir'"
}

echo "==> deploying"
push "index.html"          dist/index.html
push "app.js"              dist/app.js
push "webtorrent.min.js"   dist/webtorrent.min.js
push "sw.min.js"           dist/sw.min.js
push "manifest.webmanifest" dist/manifest.webmanifest
push "privacy.html"       privacy.html
# Raw modules, served for console diagnostics. Kept in the deploy so they can
# never drift from the bundle the way they silently did once.
push "tracker-udp.js"     src/tracker-udp.js
push "engine.js"          src/engine.js
push "bridge-peer.js"     src/bridge-peer.js
push "dht.js"            src/dht.js
push "bencode.js"        src/bencode.js
# Both the hashed name (referenced by the page and the web manifest) and the
# bare name (referenced by installed PWAs, the TWA and anything already cached).
push "icon-192.$ICONHASH.png" dist/icon-192.png
push "icon-512.$ICONHASH.png" dist/icon-512.png
push "icon-192.png"        dist/icon-192.png
push "icon-512.png"        dist/icon-512.png

# Ruffle's WASM cores back the Flash resolver. fetch-vendor.sh puts them here;
# if it has not been run the directory is absent and this is a no-op.
pushdir "dist/ruffle" "ruffle"

ssh "$JUMP" "ssh -i $KEY $VPS 'chown -R caddy:caddy /srv/stream'"
echo "==> done ($HASH)"
