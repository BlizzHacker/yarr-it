#!/usr/bin/env bash
# Package the Yarr.It channel into a sideloadable zip.
#
#   ./package.sh            -> Yarr.It-roku.zip
#
# Two things matter here and both have bitten this channel before:
#
#   1. Paths inside the archive MUST use forward slashes. A zip built on Windows
#      with backslash separators installs without complaint and then fails at
#      runtime, because Roku looks for `pkg:/components/...` and the archive
#      contains `components\...` as a single flat filename.
#   2. The manifest must be at the archive ROOT, not inside a folder. Zipping the
#      directory itself produces `roku/manifest`, which the dev server rejects.
set -euo pipefail

cd "$(dirname "$0")"
OUT=${1:-Yarr.It-roku.zip}
rm -f "$OUT"

# `zip` stores forward slashes regardless of platform; python's zipfile with
# explicit arcnames is used instead so this works on Windows without zip.exe.
python - "$OUT" <<'PY'
import os, sys, zipfile

out = sys.argv[1]
include = ("manifest", "source", "components", "images")

with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
    for entry in include:
        if os.path.isfile(entry):
            z.write(entry, entry)
            continue
        for root, _dirs, files in os.walk(entry):
            for f in files:
                path = os.path.join(root, f)
                # Force POSIX separators -- see note above.
                arc = path.replace(os.sep, "/")
                z.write(path, arc)

with zipfile.ZipFile(out) as z:
    names = z.namelist()
    assert "manifest" in names, "manifest must sit at the archive root"
    assert not any("\\" in n for n in names), "backslash path in archive"
    print(f"{out}: {len(names)} entries")
PY

echo "==> sideload with: ROKU_PASS=<dev password> ./sideload.sh <roku-ip>"
