#!/usr/bin/env bash
# Build the Google TV / Android TV variant of the Yarr.It APK.
#
# Run on LXC 171, where the Bubblewrap project and the signing keystore live:
#   scp brand/yarrit-tv-banner.png root@171:/opt/  &&  bash build_tv_apk.sh
#
# WHY A SEPARATE BUILD
# The phone APK installs fine on Google TV and then never appears in the
# launcher. Android TV lists only activities carrying the LEANBACK_LAUNCHER
# category, so without it the app is installed-but-unreachable unless the user
# has a third-party app drawer. Two further declarations matter:
#   - touchscreen must be marked not-required, or Play filters the app off every
#     TV and some devices refuse to install it;
#   - a 320x180 banner is what the launcher actually draws for the tile.
#
# Bubblewrap REGENERATES AndroidManifest.xml from twa-manifest.json on every
# `bubblewrap build`, so the patch is applied to a copy and built with gradle
# directly. Running bubblewrap in this directory would silently revert it.
#
# The package id stays com.moveweight.stream so the published assetlinks.json
# keeps verifying. That does mean a device cannot hold both the phone and TV
# builds at once -- irrelevant for a TV, which only ever wants this one.
set -euo pipefail

SRC=${SRC:-/opt/stream-installers}
DST=${DST:-/opt/yarrit-tv}
BANNER=${BANNER:-/opt/yarrit-tv-banner.png}
VERSION=${VERSION:-1.0.0}

[ -f "$BANNER" ] || { echo "missing banner at $BANNER"; exit 1; }

rm -rf "$DST"
cp -a "$SRC" "$DST"
cd "$DST"
rm -rf app/build build .gradle

install -D -m644 "$BANNER" app/src/main/res/drawable/banner.png

python3 - <<'PY'
import sys

p = 'app/src/main/AndroidManifest.xml'
s = open(p, encoding='utf8').read()

feats = '''
    <!-- Android TV. Without the leanback declaration plus the LEANBACK_LAUNCHER
         category below, the app installs but never shows in the TV launcher. -->
    <uses-feature android:name="android.software.leanback" android:required="false" />
    <uses-feature android:name="android.hardware.touchscreen" android:required="false" />
'''

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
g = g.replace("name: 'Stream — view before you download'", "name: 'Yarr.It — view before you download'")
g = g.replace("launcherName: 'Stream'", "launcherName: 'Yarr.It'")
g = g.replace('versionName "Y"', 'versionName "%s"' % __import__('os').environ.get('VERSION', '1.0.0'))
open(gp, 'w', encoding='utf8').write(g)
print("manifest and gradle patched")
PY

grep -q LEANBACK_LAUNCHER app/src/main/AndroidManifest.xml || { echo "patch did not apply"; exit 1; }

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
# pipefail`, which killed this script silently after a successful gradle build.
# `find` returns empty instead of erroring.
SDK=$(find "$ANDROID_SDK/build-tools" -maxdepth 1 -mindepth 1 -type d | sort -V | tail -1)
[ -n "$SDK" ] || { echo "android build-tools not found under $ANDROID_SDK"; exit 1; }

PASS=$(grep storePassword keystore.properties | cut -d= -f2)
OUT="/opt/Yarr.It-tv-${VERSION}.apk"

"$SDK/zipalign" -f -p 4 "$UNSIGNED" /tmp/tv-aligned.apk
"$SDK/apksigner" sign \
  --ks stream-upload.keystore --ks-key-alias stream \
  --ks-pass "pass:$PASS" --key-pass "pass:$PASS" \
  --out "$OUT" /tmp/tv-aligned.apk

"$SDK/apksigner" verify --print-certs "$OUT" | grep -i 'SHA-256'
echo "==> $OUT"
