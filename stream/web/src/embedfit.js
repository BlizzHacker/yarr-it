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
