// Capture store screenshots at the exact sizes Play and Partner Center require.
//
// Both stores reject assets that are the wrong pixel size, so these are captured
// at device dimensions rather than resized afterwards -- a downscaled 1280px
// shot looks soft next to competitors and reads as low effort.
//
// The Android and Windows apps are wrappers around the live site, so capturing
// the site at device viewports is capturing the app.
//
//   npm i -D playwright && npx playwright install chromium
//   node capture.mjs
import { chromium } from 'playwright';
import { mkdir } from 'node:fs/promises';

const SITE = process.env.SITE || 'https://yarrit.com';
const OUT = 'assets';

// Play wants 16:9 or 9:16 within 320-3840px. Partner Center wants 1366x768
// minimum for desktop. These cover both stores' required sets.
const DEVICES = [
  { name: 'phone', width: 1080, height: 1920, scale: 1, mobile: true },
  { name: 'tablet7', width: 1200, height: 1920, scale: 1, mobile: true },
  { name: 'tablet10', width: 1600, height: 2560, scale: 1, mobile: true },
  { name: 'desktop', width: 1920, height: 1080, scale: 1, mobile: false },
  { name: 'winstore', width: 1366, height: 768, scale: 1, mobile: false },
];

// Each shot is a distinct selling point, in the order a reviewer scrolls.
const SHOTS = [
  {
    id: '1-discover',
    path: '/',
    wait: async (page) => {
      await page.waitForSelector('.shelf .rail img', { timeout: 45000 });
      await page.waitForTimeout(2500); // let posters decode
    },
  },
  {
    id: '2-results',
    path: '/?q=the%20matrix',
    wait: async (page) => {
      await page.waitForSelector('#grid .tile .poster img', { timeout: 90000 });
      await page.waitForTimeout(2500);
    },
  },
  {
    id: '3-filters',
    path: '/?q=the%20matrix',
    wait: async (page) => {
      await page.waitForSelector('#f-groups .chip', { timeout: 90000 });
      await page.waitForTimeout(2000);
      await page.evaluate(() => document.querySelector('#filters')?.scrollIntoView());
    },
  },
  {
    id: '4-detail',
    path: '/?q=inception',
    wait: async (page) => {
      await page.waitForSelector('#grid .tile', { timeout: 90000 });
      await page.waitForTimeout(1500);
      await page.evaluate(() => document.querySelector('#grid .tile')?.click());
      await page.waitForSelector('#d-sources .source', { timeout: 20000 });
      await page.waitForTimeout(2000);
    },
  },
];

async function main() {
  await mkdir(OUT, { recursive: true });
  const browser = await chromium.launch();
  let count = 0;

  for (const device of DEVICES) {
    const context = await browser.newContext({
      viewport: { width: device.width, height: device.height },
      deviceScaleFactor: device.scale,
      isMobile: device.mobile,
      hasTouch: device.mobile,
      colorScheme: 'dark',
    });

    for (const shot of SHOTS) {
      const page = await context.newPage();
      // Dismiss the VPN banner so it does not eat the top of every shot --
      // it is still shown on a real first run, this only affects the asset.
      await page.addInitScript(() => localStorage.setItem('privacy-ack', '1'));
      try {
        await page.goto(SITE + shot.path, { waitUntil: 'domcontentloaded', timeout: 60000 });
        await shot.wait(page);
        const file = `${OUT}/${device.name}-${shot.id}.png`;
        await page.screenshot({ path: file });
        console.log('ok  ', file);
        count++;
      } catch (err) {
        console.warn('FAIL', device.name, shot.id, '-', err.message.split('\n')[0]);
      }
      await page.close();
    }
    await context.close();
  }

  await browser.close();
  console.log(`\n${count} screenshots written to ${OUT}/`);
}

main();
