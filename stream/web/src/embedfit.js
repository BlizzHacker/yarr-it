/**
 * Make a fixed-size third-party embed fill the screen properly.
 *
 * THE PROBLEM, measured rather than assumed. The Internet Archive's emulator
 * draws into a canvas that is 300x150 and stays 300x150: identical at 1482x415,
 * at 1600x900 and at 640x480. In a full-screen player that is roughly 3% of the
 * frame, sitting against the left edge. It is their page, cross-origin, so no
 * stylesheet of ours reaches inside it.
 *
 * THE SOLUTION. We do not need to reach inside. We own the iframe ELEMENT, and
 * a CSS transform on an element scales everything it contains, across the origin
 * boundary, because the transform is applied by our page's compositor and not by
 * theirs.
 *
 * The one thing needed is knowing where the canvas sits inside their page, and
 * that turned out to be exactly predictable:
 *
 *     canvas is 300x150, at x = 0, y = (pageHeight - 155) / 2
 *
 *   viewport 640x480  -> measured y 163, predicted (480-155)/2 = 162.5
 *   viewport 1600x900 -> measured y 373, predicted (900-155)/2 = 372.5
 *
 * So size the iframe to exactly 300x155 and the canvas lands at (0, 0) filling
 * it, with no offset arithmetic left to get wrong at runtime. Then scale that
 * box up and centre it.
 *
 * If they ever change that layout the worst case is a badly framed picture, not
 * a crash -- and `NATURAL` below is the single place to correct it.
 */

/** The natural box their embed draws into. */
export const NATURAL = { w: 300, h: 155 };

/**
 * Clamping the iframe to NATURAL is right up until the emulator boots.
 *
 * The Archive's player resizes its own canvas to the game's native resolution
 * once it starts -- Pac-Man is taller than 155px -- and a viewport locked to
 * 300x155 then overflows, clips, and anchors the picture top-left, with a
 * scrollbar. Pre-boot it looked perfect, which is why it shipped.
 *
 * We cannot observe that resize from out here, so a fixed natural size cannot
 * be correct for both states. `fitEmbed` is kept for the maths and the tests,
 * but the player now gives the frame room and lets the viewer choose the zoom.
 */
export const CLAMP_TO_NATURAL = false;

/** Pixel art scaled by a whole number stays sharp; 2.37x makes it mush. */
const SNAP_TOLERANCE = 0.12;

/**
 * Nearly every machine on archive.org drew to a 4:3 television. The exceptions
 * are handhelds, which are smaller than any stage this will ever compute, so a
 * 4:3 box is a good frame for them too -- they sit centred inside it rather than
 * jammed against the left edge of a 16:9 one.
 */
export const CRT = { w: 4, h: 3 };

/**
 * How wide their frame is ever worth making, measured rather than chosen.
 *
 * Their player picks its own canvas size and does not scale it to the viewport.
 * Read from live archive.org on 2026-08-08, post-boot, at five viewport sizes
 * each:
 *
 *     NES     512x480    Atari 2600  704x446
 *     Genesis 640x448    MS-DOS      640x400
 *
 * -- identical at 1920x1080, 1280x720, 800x600 and 640x480, always at x = 0 with
 * the canvas vertically centred (y = (viewportH - canvasH - 4) / 2). Below about
 * 640 wide their canvas is clamped to the viewport and squashes horizontally,
 * which is the one outcome worse than empty space.
 *
 * So a bigger frame does not buy a bigger picture -- it buys more black around
 * the same picture. 1024 is comfortably above every size measured, so nothing is
 * ever squashed, and on a 1920-wide stage it turns a 512px canvas adrift in a
 * 1440px box into a deliberate-looking window.
 */
export const MAX_EMBED_WIDTH = 1024;

/**
 * The largest box of a given shape that fits inside the stage, centred.
 *
 * This is what replaced "give the iframe the whole stage". Handing a
 * cross-origin frame a 16:9 stage does not centre anything: their canvas keeps
 * its own size and position inside a viewport that is now mostly empty, and the
 * result is the small picture against a wall of black that this work exists to
 * fix. We cannot move their canvas, but we CAN stop giving it a room the wrong
 * shape -- an 4:3 viewport is one their layout has far less room to be wrong in.
 *
 * Pure arithmetic on purpose: a mis-centred picture is exactly the bug no unit
 * test catches when the maths lives inline in a style assignment, which is how
 * the clamp above shipped.
 */
export function containBox(stageW, stageH, shape = CRT, maxWidth = MAX_EMBED_WIDTH) {
  const w = Number(stageW) || 0;
  const h = Number(stageH) || 0;
  const aw = Number(shape?.w) || CRT.w;
  const ah = Number(shape?.h) || CRT.h;
  if (w <= 0 || h <= 0) return { width: 0, height: 0 };

  const cap = Number(maxWidth) > 0 ? Number(maxWidth) : Infinity;
  const scale = Math.min(w / aw, h / ah, cap / aw);
  return { width: Math.floor(aw * scale), height: Math.floor(ah * scale) };
}

/**
 * Keep a wrapper sized to the largest correctly-shaped box its stage will hold.
 *
 * A ResizeObserver rather than a window resize listener, for the reason
 * keepFitted gives: the stage also changes when the player's own chrome shows or
 * hides, and that fires no window event at all. Returns a stop function.
 */
export function keepShaped(wrapper, stage, { shape = CRT, reserveHeight = 0 } = {}) {
  const fit = () => {
    // The CONTENT box, not the border box. The stage has padding, and measuring
    // through it produced a frame 27px too wide -- which max-height then clipped
    // back to the right height, leaving a 4:3 box that was not 4:3. The caption
    // under the frame is subtracted for exactly the same reason.
    const { width, height } = contentBox(stage);
    const box = containBox(width, height - Math.max(0, reserveHeight), shape);
    if (box.width <= 0) return;
    wrapper.style.width = `${box.width}px`;
    wrapper.style.height = `${box.height}px`;
    wrapper.style.margin = 'auto';
  };
  fit();

  if (typeof ResizeObserver === 'undefined') {
    const onResize = () => fit();
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }
  const ro = new ResizeObserver(fit);
  ro.observe(stage);
  return () => ro.disconnect();
}

/** An element's content box, with its own padding taken off. */
export function contentBox(node) {
  const r = node.getBoundingClientRect();
  const style = typeof getComputedStyle === 'function' ? getComputedStyle(node) : null;
  const pad = (name) => (style ? parseFloat(style[name]) || 0 : 0);
  return {
    width: r.width - pad('paddingLeft') - pad('paddingRight'),
    height: r.height - pad('paddingTop') - pad('paddingBottom'),
  };
}

/**
 * How to place a fixed-size embed inside an arbitrary stage.
 *
 * Returns the scale and the size of the wrapper that should hold it. Kept pure
 * so the arithmetic is testable without a browser -- the failure mode here is a
 * picture that is off-centre or cropped, which no unit test would catch if the
 * maths lived inline in a style assignment.
 */
export function fitEmbed(stageW, stageH, natural = NATURAL) {
  const w = Number(stageW) || 0;
  const h = Number(stageH) || 0;
  if (w <= 0 || h <= 0) return { scale: 1, width: natural.w, height: natural.h, snapped: false };

  // Contain, never cover: cropping a game's HUD to fill the screen loses the
  // part of the picture that tells you how many lives you have.
  const raw = Math.min(w / natural.w, h / natural.h);

  // Snap to a whole number when we are close to one. An integer scale maps one
  // source pixel to a square block of screen pixels; a fractional one smears
  // every edge, and on 8-bit art that is the difference between crisp and
  // blurry. Only snaps DOWN, so it can never overflow the stage.
  let scale = raw;
  let snapped = false;
  const floor = Math.floor(raw);
  if (floor >= 1 && raw - floor <= SNAP_TOLERANCE) {
    scale = floor;
    snapped = true;
  }

  return {
    scale,
    width: Math.round(natural.w * scale),
    height: Math.round(natural.h * scale),
    snapped,
  };
}

/**
 * Apply the fit to a real iframe and its wrapper.
 *
 * The iframe keeps its natural pixel size and is scaled; setting width/height
 * directly would just give their fixed canvas more empty page to sit in, which
 * is the thing that made it 3% of the frame in the first place.
 */
export function applyEmbedFit(wrapper, iframe, stageW, stageH, natural = NATURAL) {
  const fit = fitEmbed(stageW, stageH, natural);

  iframe.style.width = `${natural.w}px`;
  iframe.style.height = `${natural.h}px`;
  iframe.style.border = '0';
  iframe.style.transformOrigin = '0 0';
  iframe.style.transform = `scale(${fit.scale})`;
  // Ask for crisp upscaling. It only reaches our own compositing of the frame,
  // not their canvas's internal drawing, but it costs nothing and helps where
  // the browser honours it.
  iframe.style.imageRendering = 'pixelated';

  wrapper.style.width = `${fit.width}px`;
  wrapper.style.height = `${fit.height}px`;
  // The scaled iframe is larger than its layout box, so without this it paints
  // over the controls around it.
  wrapper.style.overflow = 'hidden';
  wrapper.style.position = 'relative';
  // Centring is the wrapper's job; the stage is already a centring flex box.
  wrapper.style.margin = 'auto';

  return fit;
}

/**
 * Keep it fitted as the window changes.
 *
 * Returns a stop function. A ResizeObserver rather than a window resize
 * listener because the stage also changes when the player's own chrome shows or
 * hides, which fires no window event at all.
 */
export function keepFitted(wrapper, iframe, stage, natural = NATURAL) {
  const fit = () => {
    const r = stage.getBoundingClientRect();
    applyEmbedFit(wrapper, iframe, r.width, r.height, natural);
  };
  fit();

  if (typeof ResizeObserver === 'undefined') {
    const onResize = () => fit();
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }
  const ro = new ResizeObserver(fit);
  ro.observe(stage);
  return () => ro.disconnect();
}
