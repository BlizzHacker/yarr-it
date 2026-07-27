#!/usr/bin/env bash
# Build a Yarr.It Android APK.
#
#   VARIANT=tv     (default) Google TV / Android TV
#   VARIANT=phone            phone and tablet
#
# Run on LXC 171, where the Bubblewrap project and the signing keystore live:
#
#   VARIANT=tv VERSION=1.0.0 bash build_apk.sh
#
# WHY THE TV VARIANT EXISTS
# The phone APK installs fine on Google TV and then never appears in the
# launcher, because Android TV lists only activities carrying the
# LEANBACK_LAUNCHER category. Two further declarations matter:
#   - touchscreen must be marked not-required, or Play filters the app off every
#     TV and some devices refuse to install it;
#   - a 320x180 banner is what the launcher actually draws for the tile.
#
# Bubblewrap REGENERATES AndroidManifest.xml from twa-manifest.json on every
# `bubblewrap build`, so the patch is applied to a copy and built with gradle
# directly. Running bubblewrap in this directory would silently revert it.
#
# The package id stays com.moveweight.stream so the published assetlinks.json
# keeps verifying. That does mean one device cannot hold both variants at once --
# irrelevant for a TV, which only ever wants the one.
set -euo pipefail

VARIANT=${VARIANT:-tv}
case "$VARIANT" in
  tv|phone) ;;
  *) echo "VARIANT must be tv or phone"; exit 1 ;;
esac

VERSION=${VERSION:-1.0.0}
SRC=${SRC:-/opt/stream-installers}
DST=${DST:-/opt/yarrit-$VARIANT}
ASSETS=${ASSETS:-/opt/yarrit-assets}      # android-assets/, from brand/propagate.py
BANNER=${BANNER:-$ASSETS/tv-banner.png}

if [ "$VARIANT" = tv ] && [ ! -f "$BANNER" ]; then
  echo "missing TV banner at $BANNER"; exit 1
fi

rm -rf "$DST"
cp -a "$SRC" "$DST"
cd "$DST"
rm -rf app/build build .gradle

if [ "$VARIANT" = tv ]; then
  install -D -m644 "$BANNER" app/src/main/res/drawable/banner.png
fi

# Launcher icons AND splash art. Bubblewrap generates both from the site icon at
# project-creation time, so neither follows a rebrand on its own.
#
# The splash is easy to miss: the launcher icon comes from ic_maskable (which the
# adaptive-icon XML insets over a white background) while the launch screen comes
# from drawable-*/splash.png. Replace only the first and the app shows the new
# mark on the home screen and the OLD one every time it starts.
for density in mdpi hdpi xhdpi xxhdpi xxxhdpi; do
  icon="$ASSETS/mipmap-$density/ic_launcher.png"
  if [ -f "$icon" ]; then
    install -D -m644 "$icon" "app/src/main/res/mipmap-$density/ic_launcher.png"
    install -D -m644 "$icon" "app/src/main/res/mipmap-$density/ic_maskable.png"
  fi
  splash="$ASSETS/drawable-$density/splash.png"
  if [ -f "$splash" ]; then
    install -D -m644 "$splash" "app/src/main/res/drawable-$density/splash.png"
  fi
done

VARIANT="$VARIANT" VERSION="$VERSION" python3 - <<'PY'
import os
import sys

variant = os.environ['VARIANT']

p = 'app/src/main/AndroidManifest.xml'
s = open(p, encoding='utf8').read()

feats = '''
    <!-- Android TV. Without the leanback declaration plus the LEANBACK_LAUNCHER
         category below, the app installs but never shows in the TV launcher. -->
    <uses-feature android:name="android.software.leanback" android:required="false" />
    <uses-feature android:name="android.hardware.touchscreen" android:required="false" />
'''

subs = []
if variant == 'tv':
    subs = [
        ('package="com.moveweight.stream">',
         'package="com.moveweight.stream">\n' + feats),
        ('android:icon="@mipmap/ic_launcher"',
         'android:icon="@mipmap/ic_launcher"\n        android:banner="@drawable/banner"'),
        ('<category android:name="android.intent.category.LAUNCHER" />',
         '<category android:name="android.intent.category.LAUNCHER" />\n'
         '                <category android:name="android.intent.category.LEANBACK_LAUNCHER" />'),
    ]

for old, new in subs:
    if old not in s:
        sys.exit(f"anchor not found, manifest layout changed: {old[:60]}")
    s = s.replace(old, new, 1)

open(p, 'w', encoding='utf8').write(s)

# The visible names are NOT in strings.xml -- Bubblewrap generates those string
# resources at build time from the twaManifest block in app/build.gradle, so
# that is the only place worth patching. versionName was also left at a stray
# "Y" by an earlier build.
gp = 'app/build.gradle'
g = open(gp, encoding='utf8').read()
g = g.replace("name: 'Stream — view before you download'",
              "name: 'Yarr.It — view before you download'")
g = g.replace("launcherName: 'Stream'", "launcherName: 'Yarr.It'")
g = g.replace('versionName "Y"', 'versionName "%s"' % os.environ['VERSION'])
open(gp, 'w', encoding='utf8').write(g)
print(f"patched for variant={variant}")
PY

if [ "$VARIANT" = tv ]; then
  grep -q LEANBACK_LAUNCHER app/src/main/AndroidManifest.xml || {
    echo "leanback patch did not apply"; exit 1; }
fi

# Bubblewrap injects ANDROID_HOME when it shells out to gradle, and the project
# has no local.properties as a result. Building with gradle directly therefore
# fails with "SDK location not found" unless it is supplied here.
ANDROID_SDK=$(python3 -c "import json;print(json.load(open('$HOME/.bubblewrap/config.json'))['androidSdkPath'])" 2>/dev/null || echo /opt/android-sdk)
export ANDROID_HOME="$ANDROID_SDK"
echo "sdk.dir=$ANDROID_SDK" > local.properties

./gradlew --no-daemon assembleRelease

UNSIGNED=$(find app/build/outputs/apk/release -name '*.apk' | head -1)
[ -n "$UNSIGNED" ] || { echo "gradle produced no apk"; exit 1; }

# `ls` on a missing candidate path fails the whole pipeline under `set -o
# pipefail`, which once killed this script silently AFTER a successful gradle
# build. `find` returns empty instead of erroring.
SDK=$(find "$ANDROID_SDK/build-tools" -maxdepth 1 -mindepth 1 -type d | sort -V | tail -1)
[ -n "$SDK" ] || { echo "android build-tools not found under $ANDROID_SDK"; exit 1; }

PASS=$(grep storePassword keystore.properties | cut -d= -f2)
if [ "$VARIANT" = tv ]; then
  OUT="/opt/Yarr.It-tv-${VERSION}.apk"
else
  OUT="/opt/Yarr.It-${VERSION}.apk"
fi
rm -f "$OUT"

"$SDK/zipalign" -f -p 4 "$UNSIGNED" "/tmp/yarrit-$VARIANT-aligned.apk"
"$SDK/apksigner" sign \
  --ks stream-upload.keystore --ks-key-alias stream \
  --ks-pass "pass:$PASS" --key-pass "pass:$PASS" \
  --out "$OUT" "/tmp/yarrit-$VARIANT-aligned.apk"

"$SDK/apksigner" verify --print-certs "$OUT" | grep -i 'SHA-256'
"$SDK/aapt2" dump badging "$OUT" | grep -E '^package|application-label:|launchable-activity|leanback' || true
echo "==> $OUT"
