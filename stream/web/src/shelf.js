/**
 * The signed-in shelf: watchlist and resume points.
 *
 * Everything here is optional by design. Browsing is open, so most visitors
 * have no session, and every call can legitimately answer 401. That is not an
 * error to report -- it means "no shelf", and the UI simply does not draw one.
 * Treating it as a failure would put a scary message in front of someone who
 * is just looking around.
 *
 * Progress is reported while a video plays, which is often. It is throttled
 * here rather than at the server: a position is only interesting when someone
 * comes back to it, so sending one every few seconds is pure waste.
 */

const SAVE_EVERY_MS = 15000;

// Below this a title is barely started; above it, effectively finished. Kept
// in step with the server, which makes the same judgement for Continue
// Watching -- if these disagree the row and the badge contradict each other.
const MIN_RESUME_SECONDS = 30;
const FINISHED_FRACTION = 0.92;

/** True when the viewer has a session; null until known. */
let signedIn = null;

async function call(path, options = {}) {
  const res = await fetch(path, { credentials: 'same-origin', ...options });
  if (res.status === 401 || res.status === 503) {
    signedIn = false;
    return null;
  }
  signedIn = true;
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
}

export function isSignedIn() {
  return signedIn === true;
}

/** A stable id for a title, so every client agrees on one shelf entry. */
export function keyFor(item) {
  if (!item) return '';
  if (item.key) return String(item.key).toLowerCase().trim();
  const year = item.year ? `-${item.year}` : '';
  return `t:${String(item.title || '').toLowerCase().trim()}${year}`;
}

export async function getLibrary() {
  const data = await call('/api/v1/library');
  return data ? data.items || [] : [];
}

export async function addToLibrary(item) {
  return call('/api/v1/library', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      key: keyFor(item),
      title: item.title || '',
      year: item.year || 0,
      kind: item.kind || '',
      poster: item.art?.poster || item.poster || '',
      isSeries: !!item.isSeries,
    }),
  });
}

export async function removeFromLibrary(item) {
  return call(`/api/v1/library?key=${encodeURIComponent(keyFor(item))}`, {
    method: 'DELETE',
  });
}

export async function getContinueWatching() {
  const data = await call('/api/v1/continue');
  return data ? data.items || [] : [];
}

export async function getProgress(item) {
  const data = await call(`/api/v1/progress?key=${encodeURIComponent(keyFor(item))}`);
  return data ? data.progress : null;
}

async function putProgress(item, position, duration) {
  return call('/api/v1/progress', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      key: keyFor(item),
      title: item.title || '',
      poster: item.art?.poster || item.poster || '',
      position,
      duration: duration || 0,
      device: 'web',
    }),
  });
}

/**
 * Report where a <video> has reached, for as long as it plays.
 *
 * Returns a function that stops reporting. The final position is sent on pause,
 * on ending and on teardown -- the throttled timer alone would lose up to
 * fifteen seconds at exactly the moment somebody walks away, which is the one
 * position that matters.
 */
export function trackProgress(media, item) {
  if (!media || !item) return () => {};

  let last = 0;
  let stopped = false;

  const send = (force) => {
    if (stopped || !isFinite(media.currentTime)) return;
    const now = Date.now();
    if (!force && now - last < SAVE_EVERY_MS) return;
    // Do not record a position nobody would want back.
    if (media.currentTime < MIN_RESUME_SECONDS && !force) return;
    last = now;
    putProgress(item, media.currentTime, media.duration).catch(() => {
      /* a lost position must never interrupt playback */
    });
  };

  const onTime = () => send(false);
  const onStop = () => send(true);

  media.addEventListener('timeupdate', onTime);
  media.addEventListener('pause', onStop);
  media.addEventListener('ended', onStop);

  return () => {
    if (stopped) return;
    send(true);
    stopped = true;
    media.removeEventListener('timeupdate', onTime);
    media.removeEventListener('pause', onStop);
    media.removeEventListener('ended', onStop);
  };
}

/** Fraction watched, 0..1, for a progress bar on a tile. */
export function watchedFraction(progress) {
  if (!progress || !progress.duration) return 0;
  return Math.min(1, Math.max(0, progress.position / progress.duration));
}

export function isFinished(progress) {
  return watchedFraction(progress) >= FINISHED_FRACTION;
}

export function isResumable(progress) {
  return !!progress && progress.position >= MIN_RESUME_SECONDS && !isFinished(progress);
}
