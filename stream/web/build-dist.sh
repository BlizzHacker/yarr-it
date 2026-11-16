#!/usr/bin/env bash
# Build a complete, servable copy of the web client into one directory.
#
#   stream/web/build-dist.sh /path/to/www
#
# The same steps deploy.sh runs before it ships -- bundle, isolated player,
# content-hash stamping, icons -- without the shipping, so the unified image
# and a home deploy produce exactly what yarrit.com serves. Needs `npm ci`.
set -euo pipefail
cd "$(dirname "$0")"
OUT=${1:?usage: build-dist.sh OUTDIR}
ESBUILD=./node_modules/.bin/esbuild
mkdir -p "$OUT"

"$ESBUILD" src/main.js --bundle --format=esm --outfile="$OUT/app.js" \
  --define:global=globalThis --external:./webtorrent.min.js --minify
"$ESBUILD" src/play-page.js --bundle --format=esm --outfile="$OUT/play-page.js" \
  --define:global=globalThis --minify
cp -f node_modules/webtorrent/dist/webtorrent.min.js node_modules/webtorrent/dist/sw.min.js "$OUT/"

HASH=$(sha256sum "$OUT/app.js" | cut -c1-12)
PLAYHASH=$(sha256sum "$OUT/play-page.js" | cut -c1-12)
ICONHASH=$(sha256sum dist/icon-512.png | cut -c1-12)
sed -e "s/__BUILD__/$HASH/g" -e "s/__ICON__/$ICONHASH/g" index.html > "$OUT/index.html"
sed -e "s/__ICON__/$ICONHASH/g" manifest.webmanifest > "$OUT/manifest.webmanifest"
sed -e "s/__BUILD__/$PLAYHASH/g" play.html > "$OUT/play.html"

for f in privacy.html sam.html xbox-check.html selfhost.sh; do cp -f "$f" "$OUT/"; done
for f in tracker-udp.js engine.js bridge-peer.js server.js dht.js bencode.js; do cp -f "src/$f" "$OUT/"; done
for s in 192 512; do
  cp -f "dist/icon-$s.png" "$OUT/icon-$s.png"
  cp -f "dist/icon-$s.png" "$OUT/icon-$s.$ICONHASH.png"
done
mkdir -p "$OUT/.well-known" "$OUT/roms"
cp -f well-known/* "$OUT/.well-known/"
cp -f dist/roms/* "$OUT/roms/"
[ -d dist/ruffle ] && cp -rf dist/ruffle "$OUT/"
echo "built $OUT (app $HASH, player $PLAYHASH)"
