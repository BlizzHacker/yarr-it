// Render the Yarr.It mark to every raster size the platforms need.
//
// PROVENANCE
// The mark is not hand-authored vector art. Five attempts at drawing the
// tricorn as SVG paths each read as something else -- a dome, a cowboy hat, a
// boat hull, a mountain range, a sombrero -- so it was generated instead with
// SD 3.5 Large on the local GPU box (see generate.py) and recoloured onto the
// brand palette by recolor.py. `source-sd35.png` is the raw generation and
// `yarrit-mark-1024.png` is the master everything below is derived from.
//
// The master is 1024px. Navigating straight to the file and shooting a small
// viewport CROPS it rather than scaling, so it is embedded in a page as an
// <img> sized to the target instead.
import { chromium } from 'playwright';
import { readFile, writeFile } from 'node:fs/promises';

const SIZES = [16, 32, 48, 64, 128, 144, 150, 192, 256, 300, 512, 1024];
const master = await readFile('yarrit-mark-1024.png');
const dataUri = 'data:image/png;base64,' + master.toString('base64');

const browser = await chromium.launch();
for (const s of SIZES) {
  const page = await browser.newPage({ viewport: { width: s, height: s }, deviceScaleFactor: 1 });
  await page.setContent(
    `<body style="margin:0"><img src="${dataUri}" width="${s}" height="${s}"></body>`,
  );
  await page.waitForTimeout(120);
  await page.screenshot({ path: `yarrit-${s}.png` });
  await page.close();
}

// A wordmark for headers and store banners, where the icon alone is too small
// to carry the name.
const page = await browser.newPage({ viewport: { width: 900, height: 240 } });
await page.setContent(`
  <body style="margin:0;background:#0b0d11;display:flex;align-items:center;gap:28px;padding:0 40px;
               font-family:'Segoe UI',system-ui,sans-serif;height:240px">
    <img src="${dataUri}" width="150" height="150">
    <div>
      <div style="font-size:76px;font-weight:800;letter-spacing:-.03em;color:#e9edf3;line-height:1">
        Yarr<span style="color:#5eead4">.It</span>
      </div>
      <div style="font-size:22px;color:#8b96a8;margin-top:8px">view before you download</div>
    </div>
  </body>`);
await page.waitForTimeout(200);
await page.screenshot({ path: 'yarrit-wordmark.png' });
await page.close();

// Android TV / Google TV launcher banner. The size is fixed by the platform at
// 320x180; anything else is rejected or letterboxed by the launcher.
const tv = await browser.newPage({ viewport: { width: 320, height: 180 } });
await tv.setContent(`
  <body style="margin:0;background:#0b0d11;display:flex;align-items:center;gap:14px;padding:0 20px;
               font-family:'Segoe UI',system-ui,sans-serif;height:180px">
    <img src="${dataUri}" width="74" height="74">
    <div style="font-size:34px;font-weight:800;letter-spacing:-.03em;color:#e9edf3;line-height:1">
      Yarr<span style="color:#5eead4">.It</span>
    </div>
  </body>`);
await tv.waitForTimeout(200);
await tv.screenshot({ path: 'yarrit-tv-banner.png' });
await tv.close();

await browser.close();
await writeFile('SIZES.txt', SIZES.join('\n') + '\n');
console.log('rendered', SIZES.join(' '), '+ wordmark');
