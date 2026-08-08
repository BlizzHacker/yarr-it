#!/usr/bin/env bash
# Package the extension for the Chrome Web Store.
set -euo pipefail
cd "$(dirname "$0")"
VERSION=$(node -p "require('./manifest.json').version")
mkdir -p dist
OUT="dist/yarrit-extension-${VERSION}.zip"
rm -f "$OUT"

# The rules the relay enforces are worth failing the build over.
node --test relay-core.test.js

# Only ship what the extension actually loads, and derive that from the manifest
# rather than keeping a second list beside it. A relay script silently missing
# from the zip is an extension that announces itself on yarrit.com and then
# answers nothing -- which looks exactly like a broken home network.
node -e "
const fs=require('fs');
const m=JSON.parse(fs.readFileSync('manifest.json','utf8'));
const s=new Set(['manifest.json']);
for (const f of Object.values(m.icons||{})) s.add(f);
for (const f of Object.values(m.action?.default_icon||{})) s.add(f);
if (m.action?.default_popup) s.add(m.action.default_popup);
if (m.options_ui?.page) s.add(m.options_ui.page);
if (m.background?.service_worker) s.add(m.background.service_worker);
for (const cs of m.content_scripts||[]) {
  for (const f of cs.js||[]) s.add(f);
  for (const f of cs.css||[]) s.add(f);
}
// Loaded by the pages and the worker above rather than named in the manifest.
for (const f of ['sites.js','popup.js','options.js','relay-core.js','relay-bg.js']) s.add(f);
const files=[...s].sort();
const missing=files.filter(f=>!fs.existsSync(f));
if (missing.length) { console.error('missing: '+missing.join(', ')); process.exit(1); }
fs.writeFileSync('dist/.filelist', files.join('\n'));
console.log('all '+files.length+' files present');
"

# Built entry by entry rather than with Compress-Archive, which writes Windows
# path separators into the archive: 'icons\icon-48.png' is one file with a
# backslash in its name, not a file in a directory, so the manifest's reference
# to 'icons/icon-48.png' resolves to nothing and Chrome rejects the package.
powershell -NoProfile -Command "
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  \$out = (Resolve-Path 'dist').Path + '\\' + (Split-Path -Leaf '$OUT')
  \$zip = [System.IO.Compression.ZipFile]::Open(\$out, 'Create')
  try {
    foreach (\$f in Get-Content 'dist/.filelist') {
      if (-not \$f) { continue }
      [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile(\$zip, \$f, \$f.Replace('\\','/')) | Out-Null
    }
  } finally { \$zip.Dispose() }
"
rm -f dist/.filelist
echo "packaged -> $OUT"
