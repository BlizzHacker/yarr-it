#!/usr/bin/env bash
# Package the extension for the Chrome Web Store.
set -euo pipefail
cd "$(dirname "$0")"
VERSION=$(node -p "require('./manifest.json').version")
mkdir -p dist
OUT="dist/yarrit-extension-${VERSION}.zip"
rm -f "$OUT"

# Only ship what the extension actually loads.
node -e "
const fs=require('fs'),path=require('path');
const files=['manifest.json','background.js','content.js','content.css','sites.js','popup.html','popup.js','icons/icon-48.png','icons/icon-128.png'];
for (const f of files) if (!fs.existsSync(f)) { console.error('missing: '+f); process.exit(1); }
console.log('all '+files.length+' files present');
"
powershell -NoProfile -Command "Compress-Archive -Path manifest.json,background.js,content.js,content.css,sites.js,popup.html,popup.js,icons -DestinationPath '$OUT' -Force"
echo "packaged -> $OUT"
