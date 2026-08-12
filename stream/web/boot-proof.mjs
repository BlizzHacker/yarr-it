// Does a game actually RUN?
//
// This is not a unit test and it cannot be one. Every cheap signal in this area
// lies in the same direction:
//
//   * `started: true` is set when the core has been constructed, not when it
//     has drawn anything. A core with no firmware, or a threaded core on a page
//     without SharedArrayBuffer, reports started and paints a black rectangle.
//   * a healthy frame RATE proves only that something is compositing. The
//     stream tier learned this the hard way -- x11grab streamed a RetroArch
//     error screen at a perfectly healthy 30 fps, and later streamed an empty
//     display at 30 fps with RetroArch not running at all.
//   * the absence of a console error proves nothing either; EmulatorJS draws
//     its "Error for site owner" into the CANVAS, where no listener sees it.
//
// So the proof is: the canvas's BACKING STORE grew from its 300x150 default to
// the core's real resolution, its pixels are not a single flat colour, and they
// CHANGED between two samples several seconds apart. A core that boots to a
// black screen fails the second test; a core that boots to a static error
// screen fails the third.
//
// Headless Chromium is used rather than a headed one on purpose -- it still
// composites and still fires requestAnimationFrame, which a hidden tab in a
// devtools-driven browser does not. That difference is what makes this script
// exist instead of a few evaluate() calls.
//
//   node boot-proof.mjs <url> [--seconds=12] [--shot=out.png]
//
// Exits non-zero when the game did not run, so it can gate a deploy.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';

// Playwright is a heavy dependency and is deliberately NOT in this directory's
// package.json -- the web build must stay installable without a browser
// download. It lives in ../brand, which already needs it for the store
// screenshots. Resolved from either place so this runs wherever it was
// installed, and says which to install when it is in neither.
const { chromium } = await (async () => {
  for (const from of [import.meta.url, new URL('../brand/package.json', import.meta.url)]) {
    try {
      // pathToFileURL, not the bare path: require.resolve answers with a
      // filesystem path, and on Windows `import("C:\\...")` reads C: as a URL
      // scheme and fails with a message about an unsupported protocol.
      const mod = await import(pathToFileURL(createRequire(from).resolve('playwright')).href);
      // playwright is CommonJS, so importing it from an ES module puts the
      // named exports on `.default` rather than on the namespace itself.
      return mod.chromium ? mod : mod.default;
    } catch { /* try the next */ }
  }
  console.error('playwright is not installed. Run: npm install --prefix ../brand');
  process.exit(2);
})();

const args = process.argv.slice(2);
const url = args.find((a) => !a.startsWith('--'));
const flag = (name, fallback) => {
  const hit = args.find((a) => a.startsWith(`--${name}=`));
  return hit ? hit.slice(name.length + 3) : fallback;
};
if (!url) {
  console.error('usage: node boot-proof.mjs <url> [--seconds=12] [--shot=out.png]');
  process.exit(2);
}
const seconds = Number(flag('seconds', 12));
const shot = flag('shot', '');

// --gpu asks for the machine's real graphics rather than the software renderer
// headless Chromium falls back to. It matters for exactly one core so far:
// EmulatorJS gates ppsspp on WebGL2, and PPSSPP renders black under SwiftShader
// while reporting itself started -- which is indistinguishable, from here, from
// a game that does not work. Off by default so the common case stays
// reproducible on a machine with no GPU at all.
const browser = await chromium.launch(args.includes('--gpu') ? {
  args: [
    '--enable-gpu', '--ignore-gpu-blocklist', '--enable-unsafe-swiftshader',
    '--use-angle=default', '--enable-features=Vulkan',
  ],
} : {});
// A real viewport, because EmulatorJS sizes its canvas from one and a 0x0
// window is its own way of producing a canvas that never grows.
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });

const notes = [];
page.on('console', (m) => { if (m.type() === 'error') notes.push(`console: ${m.text()}`); });
page.on('pageerror', (e) => notes.push(`pageerror: ${e.message}`));

const fail = async (why, extra = {}) => {
  console.log(JSON.stringify({ url, ok: false, why, ...extra, notes }, null, 2));
  await browser.close();
  process.exit(1);
};

await page.goto(url, { waitUntil: 'domcontentloaded' });

// Isolation is checked before anything else, because it is the one failure that
// explains every later one and is invisible in all of them.
// WebGL2 is reported alongside isolation because it is the second capability a
// core can silently need: EmulatorJS gates ppsspp on it specifically
// (`requiresWebGL2`). A browser without a real one still answers with a
// software renderer, so the RENDERER STRING is worth carrying -- a black PSP
// screen under SwiftShader is a fact about the test machine, not about the site.
const isolated = await page.evaluate(() => {
  const gl = document.createElement('canvas').getContext('webgl2');
  const info = gl?.getExtension('WEBGL_debug_renderer_info');
  return {
    crossOriginIsolated: self.crossOriginIsolated === true,
    sharedArrayBuffer: typeof SharedArrayBuffer === 'function',
    webgl2: Boolean(gl),
    renderer: info ? gl.getParameter(info.UNMASKED_RENDERER_WEBGL) : (gl ? 'unknown' : 'none'),
  };
});

// The start button is deliberate: autoplay policy holds a WASM emulator's run
// loop until the page has had a real gesture, leaving the core loaded, the ROM
// written into its filesystem, `started` true and the frame counter at zero.
// Our own page hangs the boot off this button. Another EmulatorJS page -- the
// vault's, which is the control this script gets pointed at when the question
// is "is it us or is it the core" -- may start on its own or use EmulatorJS's
// own overlay. So the button is taken when it is there and its absence is not
// itself a failure; a page that never draws is caught by the frame tests below,
// which is where that finding belongs.
for (const selector of ['button.canvas-start', '.ejs_start_button']) {
  const start = page.locator(selector).first();
  try {
    await start.waitFor({ state: 'visible', timeout: 30_000 });
    await start.click();
    break;
  } catch { /* try the next, then run without one */ }
}

// The picture is sampled with a SCREENSHOT of the canvas element, not by
// reading the canvas back from script. That is not fussiness -- it is the
// difference between a proof and a false alarm. EmulatorJS renders through
// WebGL without `preserveDrawingBuffer`, so `drawImage(canvas, …)` from outside
// a requestAnimationFrame callback copies an already-cleared buffer and returns
// a solid black rectangle for a game that is running perfectly. The first
// version of this script did exactly that and reported a working DOS game as a
// black screen.
//
// A screenshot goes through the compositor, which is the same path the person
// looking at the screen is on, so what it captures is what is actually there.
//
// Two properties of the PNG then answer the two questions. A solid-colour frame
// compresses to almost nothing, so BYTE COUNT separates a black screen from a
// drawn one; and two captures seconds apart that are byte-identical mean a
// static picture, which is what an error screen or a hung core looks like.
const state = () => page.evaluate(() => {
  const canvas = document.querySelector('canvas');
  const em = globalThis.EJS_emulator;
  return {
    // The backing store, not the CSS box. A canvas laid out at 1280x677 whose
    // backing store is still 300x150 has had no frame written to it.
    width: canvas?.width ?? 0,
    height: canvas?.height ?? 0,
    hasCanvas: Boolean(canvas),
    started: em?.started === true,
    core: em?.getCore?.() ?? '',
    threads: globalThis.EJS_threads === true,
    // EmulatorJS paints its own failure into the page rather than throwing.
    errorText: document.querySelector('.ejs_error_text')?.textContent?.trim() || null,
  };
});

const sample = async () => {
  const info = await state();
  if (!info.hasCanvas) return { ...info, bytes: 0, png: null };
  const png = await page.locator('canvas').screenshot().catch(() => null);
  return { ...info, bytes: png?.length ?? 0, png };
};

/** A frame this small is a flat fill; a drawn one does not compress that far. */
const FLAT_PNG_BYTES = 3000;

// FOUR samples across the window, not two, and the reason is a real failure
// this script let through: a PSP title drew a loading screen, went black, and
// stayed black. Two samples straddled that transition, saw the picture "change"
// and passed it. A game is not something that changed once -- it is something
// still drawing at the END, so the last frame is judged on its own and the
// movement test is applied to the late samples where a booted game must still
// be moving.
const SAMPLES = 4;
const shots = [];
for (let i = 0; i < SAMPLES; i += 1) {
  await page.waitForTimeout(Math.round((seconds * 1000) / SAMPLES));
  shots.push(await sample());
}
const first = shots[0];
const second = shots[shots.length - 1];

if (shot) await page.screenshot({ path: shot });

// --dump answers "what did the core actually get", which is the question every
// silent emulator failure turns into. EmulatorJS extracts an archive and then
// picks ONE file out of it by extension -- so the file it chose, and the
// extension list it chose from, are the two facts that explain a core sitting
// on a menu with a data file's name in the title bar.
if (args.includes('--dump')) {
  console.error(JSON.stringify(await page.evaluate(() => {
    const em = globalThis.EJS_emulator;
    const files = [];
    try {
      const FS = em.gameManager.FS;
      const walk = (dir, depth) => {
        if (depth > 2) return;
        for (const name of FS.readdir(dir)) {
          if (name === '.' || name === '..') continue;
          const full = `${dir === '/' ? '' : dir}/${name}`;
          try {
            const st = FS.stat(full);
            const isDir = FS.isDir(st.mode);
            files.push(full + (isDir ? '/' : ` (${st.size})`));
            if (isDir) walk(full, depth + 1);
          } catch { /* unreadable node */ }
        }
      };
      walk('/', 0);
    } catch (e) { files.push(`ERR ${e.message}`); }
    return {
      coreExtensions: em?.extensions,
      chosenFile: em?.fileName,
      gameName: globalThis.EJS_gameName,
      files: files.slice(0, 40),
    };
  }), null, 1));
}

const seen = (s) => ({
  width: s.width, height: s.height, started: s.started, core: s.core,
  threads: s.threads, errorText: s.errorText, pngBytes: s.bytes,
});

if (!second.hasCanvas) await fail('no canvas was ever created', { isolated });
if (second.errorText) {
  await fail('the emulator drew its own error screen',
    { isolated, first: seen(first), second: seen(second) });
}
if (!second.started) {
  await fail('the emulator never reported started',
    { isolated, first: seen(first), second: seen(second) });
}
if (second.width <= 300 && second.height <= 150) {
  await fail('the canvas never grew past its default size, so no frame was drawn',
    { isolated, first: seen(first), second: seen(second) });
}
const frames = shots.map((s) => s.bytes);

// The LAST frame, not any frame. A game that drew a loading screen and then
// went black is not a game that runs, and judging the first sample would call
// it one.
if (second.bytes < FLAT_PNG_BYTES) {
  await fail('the last frame is a flat fill -- it drew something and then went blank',
    { isolated, frames, first: seen(first), second: seen(second) });
}

// Movement at the END, between the last two samples. Not "changed at some
// point": a core that draws a loading screen and then goes black has changed,
// and it is exactly the thing this must not pass. A game that is running is
// still drawing when the clock runs out.
const [before, last] = shots.slice(-2);
if (before.png && last.png && before.png.equals(last.png)) {
  await fail('the picture stopped changing -- a static screen, not a running game',
    { isolated, frames, first: seen(first), second: seen(second) });
}

console.log(JSON.stringify({
  url, ok: true, isolated,
  core: second.core, threads: second.threads,
  resolution: `${second.width}x${second.height}`,
  frames,
  notes,
}, null, 2));
await browser.close();
