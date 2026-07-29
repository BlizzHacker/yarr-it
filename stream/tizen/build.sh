#!/usr/bin/env bash
# Package Yarr.It as a Samsung TV app (.wgt).
#
# Tizen Studio is not installable unattended and its CLI is the only supported
# packager, so this script does the parts that can be automated and states
# plainly what it cannot.
#
# WHAT YOU NEED ONCE
#   1. Tizen Studio with the TV extension:
#        https://developer.tizen.org/development/tizen-studio/download
#   2. A Samsung certificate profile. Samsung signs TV apps with a certificate
#      tied to a Samsung account, issued through Tizen Studio's Certificate
#      Manager. It cannot be generated offline, and an unsigned .wgt is
#      rejected by both the store and a retail TV.
#        Certificate Manager -> Samsung -> TV -> create author + distributor
#
# THEN
#   TIZEN_HOME=~/tizen-studio PROFILE=YarritTV ./build.sh
set -euo pipefail
cd "$(dirname "$0")"

TIZEN_HOME=${TIZEN_HOME:-$HOME/tizen-studio}
TIZEN="$TIZEN_HOME/tools/ide/bin/tizen"
PROFILE=${PROFILE:-YarritTV}
OUT=${OUT:-../installers/dist}

if [ ! -x "$TIZEN" ]; then
  cat >&2 <<EOF
Tizen CLI not found at $TIZEN

Install Tizen Studio with the TV extension, then re-run with TIZEN_HOME set.
Everything else in this directory is ready: config.xml, index.html and the
icon are complete and valid, so packaging is the only remaining step.
EOF
  exit 1
fi

echo "==> building"
"$TIZEN" build-web -- .

echo "==> packaging (signing with profile $PROFILE)"
# -s selects the certificate profile. Without it the .wgt is unsigned and a
# retail TV refuses to install it, which is a confusing failure because the
# package itself looks fine.
"$TIZEN" package -t wgt -s "$PROFILE" -- .buildResult

mkdir -p "$OUT"
mv .buildResult/*.wgt "$OUT/Yarr.It-tizen-1.0.0.wgt"
echo "==> wrote $OUT/Yarr.It-tizen-1.0.0.wgt"

cat <<'EOF'

Next:
  Install on a TV in developer mode:
    sdb connect <tv-ip>:26101
    tizen install -n Yarr.It-tizen-1.0.0.wgt -t <target>

  Submit at https://seller.samsungapps.com (TV category).
EOF
