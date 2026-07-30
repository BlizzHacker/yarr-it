// Build the Windows / Xbox MSIX for Yarr.It (yarrit.com).
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
const START_PAGE = process.env.STREAM_START_PAGE ?? 'https://yarrit.com/';

// Must match the existing Partner Center publisher exactly, or the upload is
// rejected as a different developer.
const STORE_IDENTITY = {
  name: 'MOVEWEIGHT.Stream',
  publisher: 'CN=6375D74B-5E4F-45B4-B246-B29507C1332A',
  publisherDisplayName: 'MOVE WEIGHT',
};

// Both makeappx and signtool ship in the same versioned SDK bin directory, so
// one lookup serves both.
function findSdkTool(exe) {
  const base = 'C:/Program Files (x86)/Windows Kits/10/bin';
  if (!existsSync(base)) return null;
  const versions = readdirSync(base)
    .filter((d) => /^10\./.test(d))
    .sort()
    .reverse();
  for (const v of versions) {
    const p = join(base, v, 'x64', exe);
    if (existsSync(p)) return p;
  }
  return null;
}

function findMakeAppx() {
  if (process.env.MAKEAPPX_PATH) return process.env.MAKEAPPX_PATH;
  const p = findSdkTool('makeappx.exe');
  if (!p) throw new Error('makeappx.exe not found; set MAKEAPPX_PATH');
  return p;
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
    <DisplayName>Yarr.It</DisplayName>
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
        DisplayName="Yarr.It"
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
// Copied from brand/msix-assets/, which brand/propagate.py generates from the
// one master mark. These used to be drawn here procedurally as a bare teal
// triangle, which quietly made Windows the only client not carrying the actual
// logo.

rmSync(staging, { recursive: true, force: true });
mkdirSync(join(staging, 'images'), { recursive: true });
mkdirSync(outDir, { recursive: true });

writeFileSync(join(staging, 'AppxManifest.xml'), manifest, 'utf8');
const assets = join(here, 'msix-assets');
for (const name of ['StoreLogo.png', 'Square44x44Logo.png',
                    'Square150x150Logo.png', 'SplashScreen.png']) {
  const src = join(assets, name);
  if (!existsSync(src)) {
    throw new Error(`missing ${src} -- run: python ../brand/propagate.py`);
  }
  copyFileSync(src, join(staging, 'images', name));
}

// The sideload installer is a tracked source file, not something written into
// dist/ by hand -- dist/ is gitignored, so anything authored there is invisible
// to the repo and cannot be reviewed or shipped.
copyFileSync(join(here, 'Install-Windows.ps1'), join(outDir, 'Install-Windows.ps1'));

const out = join(outDir, `Yarr.It_${VERSION}_neutral.msix`);
rmSync(out, { force: true });

const makeappx = findMakeAppx();
const res = spawnSync(makeappx, ['pack', '/d', staging, '/p', out, '/o'], { stdio: 'inherit' });
if (res.status !== 0) {
  throw new Error(`makeappx failed with exit code ${res.status}`);
}
console.log(`\nMSIX written -> ${out}`);

// Sign for sideloading.
//
// The Store re-signs on ingestion, so signing is irrelevant for the Partner
// Center upload -- but an UNSIGNED msix cannot be installed at all, which makes
// the tester build useless. The key lives in the CurrentUser\My certificate
// store; signtool is pointed at it by thumbprint so no .pfx sits on disk.
const THUMBPRINT = process.env.MSIX_THUMBPRINT ?? 'F09C9D34F205FD911836A0F554471E85A498C9CE';
const signtool = findSdkTool('signtool.exe');

if (signtool) {
  const sign = spawnSync(signtool,
    ['sign', '/fd', 'SHA256', '/sha1', THUMBPRINT, '/a', out],
    { stdio: 'inherit' });
  if (sign.status !== 0) {
    console.warn('\n!! signing failed -- the package will NOT install by sideload.');
  } else {
    console.log('Signed for sideloading.');
  }
} else {
  console.warn('\n!! signtool not found; package is unsigned and cannot be sideloaded.');
}

console.log('Upload in Partner Center. The Store re-signs on ingestion.');
