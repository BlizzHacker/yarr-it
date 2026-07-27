// Render the store banner graphics from feature-graphic.html.
//
// Kept separate from capture.mjs because these are designed artwork rather than
// app screenshots, and the stores want them at fixed sizes that have nothing to
// do with any device.
import { chromium } from 'playwright';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
import { mkdir } from 'node:fs/promises';

const SIZES = [
  // Google Play requires exactly 1024x500, no alpha.
  { name: 'play-feature-graphic-1024x500', width: 1024, height: 500 },
  // Partner Center promotional art.
  { name: 'ms-promo-2400x1200', width: 2400, height: 1200 },
  { name: 'ms-promo-1920x1080', width: 1920, height: 1080 },
];

const source = pathToFileURL(resolve('feature-graphic.html')).href;

const browser = await chromium.launch();
await mkdir('assets', { recursive: true });

for (const size of SIZES) {
  const page = await browser.newPage({
    viewport: { width: size.width, height: size.height },
    deviceScaleFactor: 1,
  });
  await page.goto(source);
  // The HTML is authored at 1024x500; scale it to fill any other canvas so the
  // composition stays identical rather than being letterboxed.
  await page.evaluate(({ w, h }) => {
    const el = document.querySelector('.g');
    const scale = Math.max(w / 1024, h / 500);
    el.style.transformOrigin = 'top left';
    el.style.transform = `scale(${scale})`;
    document.body.style.width = `${w}px`;
    document.body.style.height = `${h}px`;
  }, { w: size.width, h: size.height });
  await page.waitForTimeout(400);
  await page.screenshot({ path: `assets/${size.name}.png` });
  console.log('ok  ', `assets/${size.name}.png`, `${size.width}x${size.height}`);
  await page.close();
}

await browser.close();
