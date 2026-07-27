// Build the Windows / Xbox MSIX for stream.moveweight.com.
//
// This is a *hosted web app* package: the MSIX contains no application code,
// only a manifest whose start page is the live site. That matches how Cryptic
// Realm ships (see /opt/cr-visuals/scripts/build_xbox_msix.mjs on LXC 171) and
// means a site deploy updates the installed app with no store resubmission.
//
// Identity uses the same MOVE WEIGHT publisher as the existing Partner Center
// account, with a new product name, so it lists alongside the other apps.

import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync, copyFileSync, rmSync, existsSync, readdirSync } from 'node:fs';
import { resolve, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..');
const staging = join(here, 'build', 'msix');
const outDir = join(here, 'dist');

const VERSION = process.env.STREAM_VERSION ?? '1.0.0.0';
const START_PAGE = process.env.STREAM_START_PAGE ?? 'https://stream.moveweight.com/';

// Must match the existing Partner Center publisher exactly, or the upload is
// rejected as a different developer.
const STORE_IDENTITY = {
  name: 'MOVEWEIGHT.Stream',
  publisher: 'CN=6375D74B-5E4F-45B4-B246-B29507C1332A',
  publisherDisplayName: 'MOVE WEIGHT',
};

function findMakeAppx() {
  if (process.env.MAKEAPPX_PATH) return process.env.MAKEAPPX_PATH;
  const base = 'C:/Program Files (x86)/Windows Kits/10/bin';
  if (!existsSync(base)) throw new Error('Windows SDK not found; set MAKEAPPX_PATH');
  const versions = readdirSync(base)
    .filter((d) => /^10\./.test(d))
    .sort()
    .reverse();
  for (const v of versions) {
    const p = join(base, v, 'x64', 'makeappx.exe');
    if (existsSync(p)) return p;
  }
  throw new Error('makeappx.exe not found in the Windows SDK');
}

const manifest = `<?xml version="1.0" encoding="utf-8"?>
<Package
  xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10"
  xmlns:uap="http://schemas.microsoft.com/appx/manifest/uap/windows10"
  IgnorableNamespaces="uap">

  <Identity
    Name="${STORE_IDENTITY.name}"
    Publisher="${STORE_IDENTITY.publisher}"
    Version="${VERSION}"
    ProcessorArchitecture="neutral" />

  <Properties>
    <DisplayName>Stream</DisplayName>
    <PublisherDisplayName>${STORE_IDENTITY.publisherDisplayName}</PublisherDisplayName>
    <Logo>images\\StoreLogo.png</Logo>
    <Description>Search every major torrent index and play the result in your browser. Nothing is downloaded, transcoded or stored on any server.</Description>
  </Properties>

  <Dependencies>
    <TargetDeviceFamily Name="Windows.Universal" MinVersion="10.0.19041.0" MaxVersionTested="10.0.26200.0" />
    <TargetDeviceFamily Name="Windows.Xbox" MinVersion="10.0.19041.0" MaxVersionTested="10.0.26200.0" />
  </Dependencies>

  <Resources>
    <Resource Language="en-us" />
  </Resources>

  <Capabilities>
    <Capability Name="internetClient" />
  </Capabilities>

  <Applications>
    <Application Id="App" StartPage="${START_PAGE}">
      <uap:VisualElements
        DisplayName="Stream"
        Description="View before you download — browser-native torrent streaming."
        BackgroundColor="#0b0d11"
        Square150x150Logo="images\\Square150x150Logo.png"
        Square44x44Logo="images\\Square44x44Logo.png">
        <uap:SplashScreen Image="images\\SplashScreen.png" BackgroundColor="#0b0d11" />
      </uap:VisualElements>
      <uap:ApplicationContentUriRules>
        <uap:Rule Type="include" Match="${START_PAGE}*" WindowsRuntimeAccess="none" />
      </uap:ApplicationContentUriRules>
    </Application>
  </Applications>
</Package>
`;

// --- brand tiles -------------------------------------------------------------
// Generated rather than checked in, so the mark stays in one place.

import zlib from 'node:zlib';

function pngTile(w, h) {
  const bg = [0x0b, 0x0d, 0x11];
  const fg = [0x5e, 0xea, 0xd4];
  const rows = [];
  for (let y = 0; y < h; y++) {
    const row = [0];
    for (let x = 0; x < w; x++) {
      const s = Math.min(w, h);
      const cx = w / 2 - s * 0.06;
      const cy = h / 2;
      const t = (x - (cx - s * 0.16)) / (s * 0.38);
      const half = (1 - t) * s * 0.24;
      const inTri = t >= 0 && t <= 1 && Math.abs(y - cy) <= half;
      const c = inTri ? fg : bg;
      row.push(c[0], c[1], c[2]);
    }
    rows.push(Buffer.from(row));
  }
  const idat = zlib.deflateSync(Buffer.concat(rows), { level: 9 });
  const table = [...Array(256)].map((_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type), data]);
    let crc = 0xffffffff;
    for (const b of td) crc = table[(crc ^ b) & 0xff] ^ (crc >>> 8);
    const cb = Buffer.alloc(4);
    cb.writeUInt32BE((crc ^ 0xffffffff) >>> 0);
    return Buffer.concat([len, td, cb]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    chunk('IHDR', ihdr),
    chunk('IDAT', idat),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

rmSync(staging, { recursive: true, force: true });
mkdirSync(join(staging, 'images'), { recursive: true });
mkdirSync(outDir, { recursive: true });

writeFileSync(join(staging, 'AppxManifest.xml'), manifest, 'utf8');
const tiles = {
  'StoreLogo.png': [50, 50],
  'Square44x44Logo.png': [44, 44],
  'Square150x150Logo.png': [150, 150],
  'SplashScreen.png': [620, 300],
};
for (const [name, [w, h]] of Object.entries(tiles)) {
  writeFileSync(join(staging, 'images', name), pngTile(w, h));
}

const out = join(outDir, `Stream_${VERSION}_neutral.msix`);
rmSync(out, { force: true });

const makeappx = findMakeAppx();
const res = spawnSync(makeappx, ['pack', '/d', staging, '/p', out, '/o'], { stdio: 'inherit' });
if (res.status !== 0) {
  throw new Error(`makeappx failed with exit code ${res.status}`);
}
console.log(`\nMSIX written -> ${out}`);
console.log('Upload in Partner Center. The package is unsigned; the Store signs it on ingestion.');
