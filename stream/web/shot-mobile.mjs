// Capture the phone view, for checking layout changes against something real
// rather than against an assumption about how they render.
//
//   node shot-mobile.mjs [url] [outfile]
import { chromium, devices } from 'playwright';

const url = process.argv[2] ?? 'https://yarrit.com/';
const out = process.argv[3] ?? 'mobile.png';

const browser = await chromium.launch();
const ctx = await browser.newContext({ ...devices['Pixel 7'] });
const page = await ctx.newPage();

await page.goto(url, { waitUntil: 'networkidle' });
// Dismiss the VPN notice so the shot shows the steady state a returning user
// sees, not just the first-run banner.
await page.evaluate(() => document.querySelector('#privacy-ok')?.click());
await page.waitForTimeout(900);
await page.screenshot({ path: out });

const m = await page.evaluate(() => {
  const r = (s) => {
    const e = document.querySelector(s);
    if (!e) return null;
    const b = e.getBoundingClientRect();
    return { x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width), h: Math.round(b.height) };
  };
  return {
    viewport: [innerWidth, innerHeight],
    header: r('header'),
    brand: r('.brand'),
    mark: r('.brandmark'),
    search: r('#search-form'),
    magnet: r('#magnet-row'),
    firstCard: r('#grid > *'),
  };
});
console.log(JSON.stringify(m, null, 2));

await browser.close();
