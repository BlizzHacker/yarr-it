#!/usr/bin/env bash
# Fetch third-party browser runtimes that are too large to vendor into git.
#
# Ruffle is a Flash Player reimplementation (Rust/WASM, dual MIT/Apache). Its
# self-hosted bundle is ~28MB, almost all of it two .wasm cores, so it is
# downloaded here rather than committed -- the same treatment webtorrent.min.js
# gets from deploy.sh.
#
# EmulatorJS is deliberately NOT fetched. It is GPL-3.0 while this repo is MIT,
# and its release is 289MB because it carries every emulator core. The game
# resolver loads it from the project's own CDN instead, so nothing GPL is
# redistributed here and a player downloads only the core their game needs.
# To self-host it anyway: unpack the release and point localStorage
# `yarrit.emulatorjs` at its data/ directory.
set -euo pipefail
cd "$(dirname "$0")"

RUFFLE_VERSION=${RUFFLE_VERSION:-0.4.1}
OUT=dist/ruffle
URL="https://github.com/ruffle-rs/ruffle/releases/download/v${RUFFLE_VERSION}/ruffle-${RUFFLE_VERSION}-web-selfhosted.zip"

if [ -f "$OUT/ruffle.js" ] && [ "${FORCE:-0}" != "1" ]; then
  echo "==> ruffle already present ($OUT) -- FORCE=1 to refetch"
  exit 0
fi

echo "==> fetching ruffle ${RUFFLE_VERSION}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -sL --fail -o "$TMP/ruffle.zip" "$URL"

python - "$TMP/ruffle.zip" "$OUT" <<'PY'
import os, shutil, sys, zipfile

src, out = sys.argv[1], sys.argv[2]
shutil.rmtree(out, ignore_errors=True)
os.makedirs(out, exist_ok=True)

total = 0
with zipfile.ZipFile(src) as z:
    for name in z.namelist():
        # Source maps are ~1.5MB of debug data no browser needs in production.
        if name.endswith('.map'):
            continue
        data = z.read(name)
        open(os.path.join(out, os.path.basename(name)), 'wb').write(data)
        total += len(data)

# The licences are part of the redistribution terms, not optional extras.
for required in ('LICENSE_MIT', 'LICENSE_APACHE', 'ruffle.js'):
    if not os.path.exists(os.path.join(out, required)):
        sys.exit(f'ruffle bundle is missing {required} -- refusing to ship it')

print(f'   {total / 1048576:.1f} MB -> {out}')
PY

echo "==> done"
