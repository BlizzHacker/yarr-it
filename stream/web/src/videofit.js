/**
 * Make a small picture fill a big screen without turning it to mush.
 *
 * THE PROBLEM, measured. Nostalgia TV is the first thing on the home page and
 * the only thing on the site that is already running when a stranger arrives.
 * Its files are public-domain television: 320x240 to 496x368. The only rule
 * that applied to them was
 *
 *     video,#image { max-width:100%; max-height:100%; }
 *
 * -- which caps a picture and never lifts one. So on a 1920x1080 screen the
 * headline feature played at 320x240 in the middle of a wall of black, using
 * about 3% of the frame. Every archive.org video on the site had the same
 * treatment, so this is one fix in one place rather than a channel-strip patch.
 *
 * WHY NOT JUST `width:100%; object-fit:contain`. Because that is the same
 * mistake in the other direction. A browser scaling 320x240 up by 4.5x with its
 * default bilinear filter produces a soft, smeared picture -- every edge in a
 * 1970s cartoon becomes a gradient -- and the result looks like a broken stream
 * rather than an old one. Two rules avoid it:
 *
 *   1. CAP THE UPSCALE. Beyond a point, more magnification is not more picture;
 *      it is the same picture with bigger flaws. MAX_UPSCALE caps it and the
 *      rest of the stage stays black, which is a frame rather than a failure.
 *
 *   2. SNAP TO A WHOLE NUMBER, and ask for nearest-neighbour when we get one.
 *      An integer scale maps one source pixel onto an exact square block of
 *      screen pixels; a fractional one gives some source rows two screen rows
 *      and others three, which is what shimmering is. This is the same
 *      reasoning as embedfit.js's SNAP_TOLERANCE, applied to our own <video>
 *      rather than to somebody else's iframe.
 *
 * A HIGH-RESOLUTION SOURCE IS NOT TOUCHED. Anything that already fills the
 * stage, or overflows it, is left to `contain` exactly as before: this only
 * ever lifts a picture that was too small, and it never crops.
 */

/**
 * How far a small picture is worth magnifying.
 *
 * 3x turns the smallest thing on the channel list (320x240) into 960x720, which
 * is a television-sized picture on a 1080p screen and still recognisably the
 * source rather than a poster of it. Past about 4x the artefacts of the
 * original encode -- block edges, chroma bleed, halo around titles -- are
 * bigger than the detail, so the extra size buys nothing.
 */
export const MAX_UPSCALE = 3;

/** Below this a source is small enough that crisp beats smooth. */
export const CRISP_BELOW_WIDTH = 720;

/** Under this much magnification, the browser's own filtering is fine. */
export const CRISP_ABOVE_SCALE = 1.5;

/** How close to a whole number is close enough to take it. */
const SNAP_TOLERANCE = 0.25;

/**
 * The box a video should occupy inside a stage.
 *
 * Pure arithmetic, and separate from the DOM on purpose: a picture that is
 * off-centre, cropped or blurry is exactly the failure no unit test catches
 * when the maths lives inline in a style assignment. That is not a guess --
 * embedfit.js says the same thing about the same class of bug, and this file
 * exists because the CSS version of it shipped.
 *
 * Returns the size in CSS pixels plus whether nearest-neighbour upscaling
 * should be asked for. `{ width: 0 }` means "leave it alone": either the stage
 * has not been laid out yet or the video has not reported its dimensions, and
 * sizing from a zero is how a picture ends up 0x0.
 */
export function fitVideo(videoW, videoH, stageW, stageH, {
  maxUpscale = MAX_UPSCALE,
  mode = 'fill',
} = {}) {
  const vw = Number(videoW) || 0;
  const vh = Number(videoH) || 0;
  const sw = Number(stageW) || 0;
  const sh = Number(stageH) || 0;
  if (vw <= 0 || vh <= 0 || sw <= 0 || sh <= 0) {
    return { width: 0, height: 0, scale: 1, crisp: false, snapped: false };
  }

  // Contain, never cover. Cropping a 4:3 broadcast to fill a 16:9 screen throws
  // away the top and bottom of every shot, and on old television that is where
  // the captions are.
  const room = Math.min(sw / vw, sh / vh);

  // "Keep original size" is a real answer and somebody may prefer it: a viewer
  // who thinks any upscale looks wrong is not wrong, they are stating a
  // preference about their own screen. It still shrinks an oversized source,
  // because the alternative is a picture running off the edge of the page.
  const ceiling = mode === 'natural' ? 1 : Math.max(1, Number(maxUpscale) || 1);
  let scale = Math.min(room, ceiling);

  let snapped = false;
  if (scale > 1) {
    // Snap DOWN to a whole number when we are close to one, never up: rounding
    // up would overflow the stage, and an overflowing video is clipped by the
    // player's own edges with no way to see what is missing.
    const floor = Math.floor(scale);
    if (floor >= 1 && scale - floor <= SNAP_TOLERANCE) {
      scale = floor;
      snapped = true;
    }
  }

  return {
    width: Math.round(vw * scale),
    height: Math.round(vh * scale),
    scale,
    // Nearest-neighbour only where it helps: a small source magnified a long
    // way. Asking for it on a 1080p file being nudged 1.05x would throw away
    // the browser's filtering for no reason and make a good picture worse.
    crisp: scale >= CRISP_ABOVE_SCALE && vw <= CRISP_BELOW_WIDTH,
    snapped,
  };
}

/**
 * Apply the fit to a real <video>, and keep applying it.
 *
 * A ResizeObserver on the stage rather than a window listener, for the reason
 * embedfit.js gives: the stage also changes size when the player's own chrome
 * (the switch bar, the guide rail, the stats row) appears or disappears, and
 * none of that fires a window resize event.
 *
 * Returns a stop function. Calling it puts the element back exactly as it was,
 * so a later source that needs no help is not left wearing the last one's
 * inline width.
 */
export function keepVideoFitted(video, stage, { mode = 'fill', maxUpscale = MAX_UPSCALE } = {}) {
  if (!video || !stage) return () => {};

  const clear = () => {
    video.style.width = '';
    video.style.height = '';
    video.style.imageRendering = '';
  };

  const fit = () => {
    // videoWidth is 0 until metadata has loaded. That is the normal state for
    // the first frames of a torrent stream, not an error -- the loadedmetadata
    // listener below runs this again the moment it is knowable.
    const vw = video.videoWidth || 0;
    const vh = video.videoHeight || 0;
    const box = contentBox(stage);
    const out = fitVideo(vw, vh, box.width, box.height, { mode, maxUpscale });
    if (!out.width) { clear(); return; }
    video.style.width = `${out.width}px`;
    video.style.height = `${out.height}px`;
    video.style.imageRendering = out.crisp ? 'pixelated' : '';
  };

  fit();
  // resize fires when a stream switches resolution mid-play, which HLS and
  // some archive.org derivatives do; loadedmetadata is the first time there is
  // anything to measure at all.
  video.addEventListener('loadedmetadata', fit);
  video.addEventListener('resize', fit);

  let stopObserving = () => {};
  if (typeof ResizeObserver !== 'undefined') {
    const ro = new ResizeObserver(fit);
    ro.observe(stage);
    stopObserving = () => ro.disconnect();
  } else if (typeof window !== 'undefined') {
    // Older TV webviews have no ResizeObserver. A window listener misses the
    // player's own chrome appearing, which is a worse fit rather than no fit.
    const onResize = () => fit();
    window.addEventListener('resize', onResize);
    stopObserving = () => window.removeEventListener('resize', onResize);
  }

  return () => {
    stopObserving();
    video.removeEventListener('loadedmetadata', fit);
    video.removeEventListener('resize', fit);
    clear();
  };
}

/** An element's content box, with its own padding taken off. */
function contentBox(node) {
  const r = node.getBoundingClientRect();
  const style = typeof getComputedStyle === 'function' ? getComputedStyle(node) : null;
  const pad = (name) => (style ? parseFloat(style[name]) || 0 : 0);
  return {
    width: r.width - pad('paddingLeft') - pad('paddingRight'),
    height: r.height - pad('paddingTop') - pad('paddingBottom'),
  };
}
