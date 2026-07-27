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
sed "s/__BUILD__/$HASH/g" index.html > dist/index.html

push() {
  ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $VPS 'cat > $WWW/$1'" < "$2"
}

echo "==> deploying"
push "index.html"          dist/index.html
push "app.js"              dist/app.js
push "webtorrent.min.js"   dist/webtorrent.min.js
push "sw.min.js"           dist/sw.min.js
push "manifest.webmanifest" manifest.webmanifest
# Raw modules, served for console diagnostics. Kept in the deploy so they can
# never drift from the bundle the way they silently did once.
push "tracker-udp.js"     src/tracker-udp.js
push "engine.js"          src/engine.js
push "bridge-peer.js"     src/bridge-peer.js
push "icon-192.png"        dist/icon-192.png
push "icon-512.png"        dist/icon-512.png

ssh "$JUMP" "ssh -i $KEY $VPS 'chown -R caddy:caddy /srv/stream'"
echo "==> done ($HASH)"
