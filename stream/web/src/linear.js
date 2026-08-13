/**
 * Nostalgia TV — the live channels, at the top of the front page.
 *
 * These are the only things on this site that need no account, no indexer and
 * no swarm: two public-domain channels scheduled out of the Internet Archive,
 * running whether anybody is watching or not. That is why they go first. A
 * visitor who has never heard of this place can press one button and be
 * watching television, and everything below — the eighteen discover rows, the
 * search, the paste box — is a thing they can decide about afterwards.
 *
 * WHY THIS IS NOT A POSTER ROW.
 *
 * A poster row says "here is a thing you could watch". A channel says "here is
 * what is on". Drawing the second as the first is a lie of exactly the kind
 * the rest of this page has been spending the session removing: the tile would
 * look identical at 20:00 and at 20:37, and the one fact that makes it live —
 * that it is already 37 minutes in and nobody can change that — would be the
 * one fact missing. So a card carries the programme, how far through it is,
 * and what is on next, and the bar moves while you look at it.
 *
 * WHERE THE NUMBERS COME FROM.
 *
 * `/api/v1/linear/now` answers with the programme, its start and end, and
 * `serverNow`. Everything drawn between polls is computed from those against
 * the local clock — see `describeChannel` — so the bar advances smoothly on a
 * page that is talking to the network twice a minute rather than once a
 * second. Basing it on `serverNow` rather than on `Date.now()` alone means a
 * viewer whose machine is ten minutes fast still sees the right position.
 *
 * WHAT IT REFUSES TO DO.
 *
 *   - It never draws an empty box. A channel whose pool is still being fetched
 *     from archive.org (~90 seconds from cold) reports `state: "empty"` with a
 *     sentence saying so; that sentence is shown, the card is disabled, and the
 *     poll keeps running so it turns into a live card on its own.
 *   - It never blocks the page. The strip loads in parallel with
 *     `/api/discover`; if the channel service is slow, or down, or answers
 *     nothing, the strip stays hidden and the discover rows below are
 *     unaffected.
 *   - It never asks for anything a session is needed for. Every call here is
 *     one of the four public linear routes, which is what makes these channels
 *     the right thing to put in front of a stranger.
 *
 * WHY PLAYBACK GOES THROUGH THE REGISTRY.
 *
 * `linearResolver` claims `yarrit-linear:<channel id>` and turns it into an
 * ordinary Playable, so a channel starts in the same player as everything else
 * — same overlay, same close, same escape key. It exists as a resolver rather
 * than as a second play path for one specific reason: the offset. The server
 * hands back a URL with a `#t=` media fragment on it, and that fragment is the
 * whole feature. Handing the channel's *file* to the normal archive.org route
 * would rebuild the URL from its identifier and drop the fragment, and the
 * channel would silently start every programme from zero while the card next
 * to it insisted it was eighteen minutes in.
 */

import { makePlayable, RENDER } from './source.js';
import { PlaybackError, FAILURE } from './failures.js';
import { apiFetch } from './server.js';

export const LINEAR_BASE = '/api/v1/linear';

/** How a channel is named to `play()`. See linearResolver. */
export const LINEAR_URI_PREFIX = 'yarrit-linear:';

export function linearURI(channelId) {
  return LINEAR_URI_PREFIX + String(channelId ?? '');
}

/** The states linear_position.go reports. Every one of them is drawable. */
export const LINEAR_STATE = {
  ON_AIR: 'on-air',
  OFF_AIR: 'off-air',
  EMPTY: 'empty',
  DISABLED: 'disabled',
  UNKNOWN: 'unknown',
};

// A programme is minutes long, so the heartbeat is measured in minutes too. The
// bar between heartbeats is arithmetic, not a request — see describeChannel.
export const HEARTBEAT_MS = 90_000;
// A channel that is still building its pool fixes itself in about ninety
// seconds, so it is asked about more often than a settled one. This is the only
// case where a faster poll buys anything: the answer is about to change.
export const WARMING_POLL_MS = 30_000;
// The floor on a boundary poll. Without it, a server whose `remainingSeconds`
// sits at zero for a moment would be asked again immediately, forever.
export const MIN_POLL_MS = 3_000;
// How long a tune-in is worth reusing. Long enough that a hover and the click
// that follows it are one request; short enough that the offset it carries is
// still where the channel actually is.
export const STALE_TUNE_MS = 15_000;

const TICK_MS = 1_000;

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));

// ----------------------------------------------------------------- reading --

/**
 * The public channel list.
 *
 * Returns [] for anything that is not a usable answer rather than throwing: a
 * strip with no channels is a strip that does not appear, which is the correct
 * outcome for an instance that has none and for a service that is down alike.
 * There is nothing a visitor could do with the difference.
 */
export async function fetchChannels({ fetchImpl = apiFetch } = {}) {
  let res;
  try {
    res = await fetchImpl(`${LINEAR_BASE}/channels`);
  } catch {
    return [];
  }
  if (!res?.ok) return [];
  let body;
  try {
    body = await res.json();
  } catch {
    return [];
  }
  return normaliseChannels(body);
}

/**
 * The rows worth drawing, in the order the server listed them.
 *
 * A channel its owner has switched off is dropped here rather than drawn
 * greyed: "disabled" is an administrative state, and a stranger has no use for
 * the knowledge that a channel exists but is off.
 */
export function normaliseChannels(body) {
  const rows = Array.isArray(body?.channels) ? body.channels : [];
  return rows
    .filter((c) => c && typeof c.id === 'string' && c.id)
    .filter((c) => c.status !== 'disabled' && c.definition?.enabled !== false)
    .map((c) => ({
      id: c.id,
      name: c.name || c.id,
      number: Number(c.number) || 0,
      status: c.status || '',
      detail: c.detail || '',
      description: c.definition?.description || '',
    }));
}

/** What is on one channel, or null when the question could not be asked. */
export async function fetchNow(channelId, { fetchImpl = apiFetch, signal } = {}) {
  let res;
  try {
    res = await fetchImpl(
      `${LINEAR_BASE}/now?channel=${encodeURIComponent(channelId)}`,
      signal ? { signal } : {},
    );
  } catch {
    return null;
  }
  // 404 is the answer for a channel a stranger may not see. Every other state
  // — off air, empty, still warming — is a 200 carrying a state field, so a
  // non-ok response here really is "no answer" rather than "no programme".
  if (!res?.ok) return null;
  try {
    return await res.json();
  } catch {
    return null;
  }
}

// -------------------------------------------------------------- describing --

/**
 * One channel, as it should read at instant `at`.
 *
 * PURE, and deliberately so: this is the only place that decides what a
 * channel says, and it decides it from the schedule and a clock rather than
 * from anything a renderer happens to have to hand.
 *
 * `serverNow` is the base. The alternative — trusting the browser's clock
 * against the server's schedule — puts a viewer whose machine is ten minutes
 * out on the wrong programme, and the error is invisible because both halves
 * look internally consistent.
 */
export function describeChannel(channel, now, { receivedAt = 0, at = 0 } = {}) {
  const base = {
    id: channel?.id || now?.channel?.id || '',
    name: channel?.name || now?.channel?.name || '',
    number: channel?.number || now?.channel?.number || 0,
    state: now?.state || LINEAR_STATE.UNKNOWN,
    live: false,
    watchable: false,
    kicker: 'Off air',
    title: channel?.name || '',
    artwork: '',
    detail: now?.detail || '',
    offsetSeconds: 0,
    durationSeconds: 0,
    remainingSeconds: 0,
    progress: 0,
    next: null,
  };

  if (!now) {
    // Nothing has been asked yet, or the last ask went unanswered. The channel
    // LISTING already carried a status and, when there is one, the server's
    // sentence — so the first paint is honest without waiting a round trip for
    // permission to say anything.
    const settling = channel?.status === 'empty' || channel?.status === 'error';
    return {
      ...base,
      state: LINEAR_STATE.UNKNOWN,
      kicker: settling ? 'Coming on air' : 'Tuning in…',
      title: base.name,
      detail: channel?.detail || base.detail,
    };
  }

  if (now.next) {
    base.next = { title: now.next.title || '', startTime: Number(now.next.startTime) || 0 };
  }

  // The server's clock, carried forward by however long this page has held the
  // answer. Seconds, to match every time field in the payload.
  const serverSeconds = (Number(now.serverNow) || 0) + Math.max(0, (at - receivedAt) / 1000);

  if (now.state !== LINEAR_STATE.ON_AIR || !now.program) {
    // Not on air. Two of these fix themselves and one does not, and the server
    // is the only side that knows which — so its sentence is carried through
    // verbatim rather than being re-decided from the state word.
    return {
      ...base,
      kicker: now.state === LINEAR_STATE.EMPTY ? 'Coming on air' : 'Off air',
      title: base.name,
    };
  }

  const p = now.program;
  const start = Number(p.startTime) || 0;
  const end = Number(p.endTime) || 0;
  const duration = Math.max(0, end - start);
  // Fall back to the offset the server computed when there is no usable
  // schedule arithmetic to do. Never below it: a channel does not rewind.
  const offset = duration > 0
    ? clamp(serverSeconds - start, 0, duration)
    : Math.max(0, Number(now.offsetSeconds) || 0);
  const remaining = duration > 0
    ? Math.max(0, end - serverSeconds)
    : Math.max(0, Number(now.remainingSeconds) || 0);

  return {
    ...base,
    state: LINEAR_STATE.ON_AIR,
    live: true,
    watchable: true,
    kicker: 'On now',
    title: p.title || base.name,
    artwork: p.artwork || '',
    offsetSeconds: offset,
    durationSeconds: duration,
    remainingSeconds: remaining,
    progress: duration > 0 ? clamp(offset / duration, 0, 1) : 0,
  };
}

/**
 * When to ask again.
 *
 * A live channel is asked once a heartbeat, plus once more the moment its
 * programme is due to end — that second poll is what makes a card roll over to
 * the next programme by itself rather than fifty seconds late. A channel that
 * is still loading its pool is asked more often because its answer is about to
 * change; anything else settles on the heartbeat.
 */
export function nextPollDelay(model, {
  heartbeat = HEARTBEAT_MS, warming = WARMING_POLL_MS, floor = MIN_POLL_MS,
} = {}) {
  if (model?.live) {
    // +1.5s so the request lands after the rollover rather than a hair before
    // it, which would return the outgoing programme and cost a second poll.
    return clamp(model.remainingSeconds * 1000 + 1500, floor, heartbeat);
  }
  if (model?.state === LINEAR_STATE.EMPTY) return warming;
  return heartbeat;
}

// ------------------------------------------------------------------ words --

/** "8:42 PM", in the viewer's own zone and format. */
export function clockTime(unixSeconds, { locale = undefined } = {}) {
  const t = Number(unixSeconds);
  if (!Number.isFinite(t) || t <= 0) return '';
  try {
    return new Date(t * 1000)
      .toLocaleTimeString(locale, { hour: 'numeric', minute: '2-digit' });
  } catch {
    return '';
  }
}

/** Whole minutes, floored, because "1 min left" that is really 40s is fine. */
export function minutes(seconds) {
  return Math.max(0, Math.floor((Number(seconds) || 0) / 60));
}

/**
 * How far through, in words.
 *
 * Both halves are stated. "18 min in" alone does not say whether there is any
 * point joining; "11 min left" alone does not say you have missed most of it.
 */
export function positionLine(model) {
  if (!model?.live) return '';
  const inMin = minutes(model.offsetSeconds);
  const leftMin = minutes(model.remainingSeconds);
  const started = inMin < 1 ? 'just started' : `${inMin} min in`;
  const left = leftMin < 1 ? 'ending now' : `${leftMin} min left`;
  return `${started} · ${left}`;
}

export function nextLine(model) {
  if (!model?.next?.title) return '';
  const at = clockTime(model.next.startTime);
  return at ? `Up next: ${model.next.title} at ${at}` : `Up next: ${model.next.title}`;
}

// ---------------------------------------------------------------- tuning in --

// One in-flight or recent tune-in per channel. Its real value is on the far
// side: the first request for a programme makes the server probe archive.org
// for range support, which is measured in tens of seconds; every request after
// that is answered from its cache in about two tenths.
const tuneCache = new Map();
// A full Archive pool has taken just under ten minutes on a cold production
// restart. Keep the player in its honest "Tuning in" state across that window
// instead of turning a warming catalogue into a playback failure after 60s.
const WARM_TUNE_RETRIES = 360;
const WARM_TUNE_DELAY_MS = 2000;

/** Forget everything. Exported for tests; nothing in the app calls it. */
export function clearTuneCache(cache = tuneCache) {
  cache.clear();
}

/**
 * Ask the server to tune a channel in.
 *
 * Deliberately re-fetched once the answer is more than a few seconds old,
 * rather than adjusted locally. The URL carries an offset the server computed
 * against the schedule, and by the time somebody has hovered, read the card and
 * clicked, the programme itself may have changed — at which point the cached
 * URL is not a stale number but the wrong file.
 */
export function tuneIn(channelId, {
  fetchImpl = apiFetch, clock = () => Date.now(), cache = tuneCache, staleMs = STALE_TUNE_MS,
  wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  warmRetries = WARM_TUNE_RETRIES,
} = {}) {
  const id = String(channelId ?? '');
  const held = cache.get(id);
  if (held && clock() - held.at < staleMs) return held.promise;

  const promise = requestTune(id, fetchImpl, wait, warmRetries);
  cache.set(id, { at: clock(), promise });
  // A refusal must not be remembered. Otherwise the second press of a channel
  // that was briefly unavailable replays the first failure without asking.
  promise.catch(() => {
    if (cache.get(id)?.promise === promise) cache.delete(id);
  });
  return promise;
}

async function requestTune(channelId, fetchImpl, wait, warmRetries) {
  for (let attempt = 0; ; attempt++) {
  let res;
  try {
    res = await fetchImpl(`${LINEAR_BASE}/stream?channel=${encodeURIComponent(channelId)}`);
  } catch (err) {
    throw new PlaybackError(FAILURE.DEAD_STREAM,
      `the channel service could not be reached (${err?.message ?? err})`);
  }
  // The body is read whatever the status. A channel that cannot be played right
  // now answers 503 carrying the whole picture — what should be on, where it is
  // up to, when the next programme starts — and that sentence is the only
  // useful thing to put in front of somebody.
  let body = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  if (!body) {
    throw new PlaybackError(FAILURE.DEAD_STREAM,
      `the channel service answered ${res?.status ?? 'nothing'}`);
  }
    // A cold Archive-backed channel is a self-healing 503. Keep the existing
    // “Tuning in…” player state and ask again while its catalogue warms.
    const warming = !body.available && body.state === LINEAR_STATE.EMPTY
      && /\bloading\b|\bwarm/i.test(String(body.detail || ''));
    if (!warming || attempt >= warmRetries) return body;
    await wait(WARM_TUNE_DELAY_MS);
  }
}

/**
 * The tuned-in channel as a Playable.
 *
 * `source.url` is used exactly as given. It ends in a `#t=` media fragment
 * which is what starts playback where the channel actually is, and which the
 * server only attaches once it has proved the file can be ranged — so
 * rebuilding, re-encoding or "cleaning" this URL here would either lose the
 * offset or invent one that the file cannot honour.
 */
export function playableFor(tune) {
  if (!tune?.available || !tune?.source?.url) {
    throw new PlaybackError(FAILURE.DEAD_STREAM, tuneDetail(tune));
  }
  return makePlayable({
    render: RENDER.VIDEO,
    src: tune.source.url,
    mime: tune.source.mimeType || 'video/mp4',
  });
}

function tuneDetail(tune) {
  if (tune?.detail) return tune.detail;
  if (tune?.state === LINEAR_STATE.EMPTY) {
    return 'this channel is still loading its programmes; it will start playing shortly';
  }
  return 'this channel is not on air right now';
}

/**
 * `yarrit-linear:<channel id>` — a channel, as something the player can be
 * pointed at.
 *
 * Registered ahead of every other resolver. Nothing else claims this scheme, so
 * the ordering is belt and braces rather than a dependency.
 */
export const linearResolver = {
  name: 'linear',
  canHandle(input) {
    return typeof input === 'string' && input.startsWith(LINEAR_URI_PREFIX);
  },
  async resolve(source, { fetchImpl = apiFetch, clock = () => Date.now() } = {}) {
    const id = String(source.uri).slice(LINEAR_URI_PREFIX.length);
    return playableFor(await tuneIn(id, { fetchImpl, clock }));
  },
};

// ---------------------------------------------------------------- rendering --

/**
 * One channel card, built once and updated in place.
 *
 * Rebuilt-per-poll cards would take the keyboard focus off whatever a person
 * was on every ninety seconds, and take the progress bar's transition with it.
 * So the shape is fixed and only text, width and the disabled flag move.
 */
export function createCard(handlers = {}) {
  const node = el('button', 'tv-card');
  node.type = 'button';

  const art = el('div', 'tv-art');
  const placeholder = el('div', 'tv-noart');
  art.append(placeholder);
  const img = el('img');
  img.loading = 'lazy';
  img.alt = '';
  // Appended after the placeholder so it covers it, and taken away by its own
  // error handler rather than by anything having to know in advance whether
  // archive.org has a thumbnail for this item. Same rule as home.js: never
  // hidden while loading, because a display:none lazy image is never fetched.
  let arted = true;
  img.addEventListener('error', () => { img.remove(); arted = false; });
  art.append(img);
  const dot = el('span', 'tv-live', 'LIVE');
  art.append(dot);
  node.append(art);

  const body = el('div', 'tv-body');
  const chan = el('div', 'tv-chan');
  const kicker = el('span', 'tv-kicker');
  const name = el('span', 'tv-name');
  chan.append(kicker, name);
  const title = el('div', 'tv-now');
  const track = el('div', 'tv-bar');
  const fill = el('div', 'tv-fill');
  track.append(fill);
  const time = el('div', 'tv-time');
  const next = el('div', 'tv-next');
  const detail = el('div', 'tv-detail');
  body.append(chan, title, track, time, next, detail);
  node.append(body);

  let current = null;
  node.addEventListener('click', () => {
    if (!current?.watchable) return;
    handlers.onWatch?.(current);
  });
  // Hovering or tabbing onto a channel is somebody about to press it. One
  // request per channel per programme, and it turns a cold tune-in — which is
  // the server probing archive.org, and slow — into a warm one.
  const warm = () => { if (current?.watchable) handlers.onIntent?.(current); };
  node.addEventListener('pointerenter', warm);
  node.addEventListener('focus', warm);

  function update(model) {
    current = model;
    node.disabled = !model.watchable;
    node.setAttribute('aria-disabled', model.watchable ? 'false' : 'true');
    node.className = model.live ? 'tv-card live' : 'tv-card off';
    node.setAttribute('aria-label', model.watchable
      ? `Watch ${model.name} — ${model.title}`
      : `${model.name} — ${model.kicker}`);

    placeholder.textContent = model.name;
    if (model.artwork) {
      if (img.src !== model.artwork) {
        // A new programme deserves a fresh attempt even if the last one's
        // thumbnail 404'd — it is a different item at a different URL.
        if (!arted) { art.append(img); arted = true; }
        img.src = model.artwork;
      }
    } else if (arted) {
      // A channel that has just gone off air must not keep the last
      // programme's still under a card that says nothing is on.
      img.remove();
      arted = false;
    }
    dot.hidden = !model.live;

    kicker.textContent = model.kicker;
    name.textContent = model.number ? `${model.number} · ${model.name}` : model.name;
    title.textContent = model.title;

    track.hidden = !model.live;
    fill.style.width = `${(model.progress * 100).toFixed(2)}%`;

    const pos = positionLine(model);
    time.textContent = pos;
    time.hidden = !pos;

    const up = nextLine(model);
    next.textContent = up;
    next.hidden = !up;

    // The server's own sentence, shown only when the card cannot be pressed.
    // On a live card it would be an explanation of nothing.
    const why = model.live ? '' : model.detail;
    detail.textContent = why;
    detail.hidden = !why;
  }

  return { node, update };
}

/** The heading, written once. */
function head() {
  const h = el('div', 'tv-head');
  h.append(el('h2', null, 'Nostalgia TV'));
  const badge = el('span', 'tv-flag', 'Live · free · no account');
  h.append(badge);
  h.append(el('p', 'tv-lede',
    'Public-domain television and cartoons, scheduled as real channels. '
    + 'Whatever is on when you arrive is what you get.'));
  return h;
}

// --------------------------------------------------------------- the strip --

const defaultTimers = {
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (t) => clearTimeout(t),
  setInterval: (fn, ms) => setInterval(fn, ms),
  clearInterval: (t) => clearInterval(t),
};

/**
 * Mount the strip. Returns immediately; the channels arrive when they arrive.
 *
 * SYNCHRONOUS ON PURPOSE. The caller gets a handle it can hide and show on the
 * very next line, before a single request has come back — which is what lets
 * `search()` put the strip away without having to know whether it had finished
 * loading. And nothing above it is waiting on a promise, so a slow or dead
 * channel service costs this strip and nothing else on the page.
 */
export function mountLinear(host, {
  fetchImpl = apiFetch,
  onWatch = () => {},
  clock = () => Date.now(),
  timers = defaultTimers,
  heartbeatMs = HEARTBEAT_MS,
  warmingMs = WARMING_POLL_MS,
  autoStart = true,
} = {}) {
  const cards = new Map();   // channel id -> { channel, card, now, receivedAt, timer }
  let rail = null;
  let ticker = null;
  let wanted = true;         // the landing page is showing
  let running = false;
  let stopped = false;

  function visible() {
    return host && !host.hidden;
  }

  function sync() {
    if (!host) return;
    host.hidden = !(wanted && cards.size > 0);
  }

  function draw(entry) {
    const model = describeChannel(entry.channel, entry.now, {
      receivedAt: entry.receivedAt, at: clock(),
    });
    entry.model = model;
    entry.card.update(model);
    return model;
  }

  function tick() {
    for (const entry of cards.values()) if (entry.model?.live) draw(entry);
  }

  function schedule(entry, model) {
    timers.clearTimeout(entry.timer);
    if (!running) return;
    entry.timer = timers.setTimeout(
      () => { refresh(entry).catch(() => {}); },
      nextPollDelay(model, { heartbeat: heartbeatMs, warming: warmingMs }),
    );
  }

  async function refresh(entry) {
    const now = await fetchNow(entry.channel.id, { fetchImpl });
    if (stopped) return;
    // A poll that could not be answered leaves the last known state on screen.
    // Blanking a live card because one request out of forty timed out would be
    // the strip lying about the channel rather than about the network.
    if (now) {
      entry.now = now;
      entry.receivedAt = clock();
    }
    const model = draw(entry);
    schedule(entry, model);
  }

  function start() {
    if (running || stopped || !cards.size || !wanted || !tabVisible()) return;
    running = true;
    ticker = timers.setInterval(tick, TICK_MS);
    for (const entry of cards.values()) refresh(entry).catch(() => {});
  }

  function pause() {
    running = false;
    timers.clearInterval(ticker);
    ticker = null;
    for (const entry of cards.values()) timers.clearTimeout(entry.timer);
  }

  const onVisibility = () => {
    if (typeof document === 'undefined') return;
    // A hidden tab is a tab nobody is reading. Polling it is bytes spent on
    // a picture of a channel that no eye is on.
    if (document.visibilityState === 'hidden') pause();
    else if (wanted && visible()) start();
  };

  // Whether the tab is being looked at right now. Unknown counts as visible:
  // a runtime that does not report it (a TV webview, a test) must not end up
  // with a strip that never polls.
  function tabVisible() {
    return typeof document === 'undefined' || document.visibilityState !== 'hidden';
  }

  async function load() {
    const channels = await fetchChannels({ fetchImpl });
    if (stopped || !channels.length) { sync(); return []; }

    host.replaceChildren();
    host.append(head());
    rail = el('div', 'tv-rail');
    host.append(rail);

    const first = [];
    for (const channel of channels) {
      const card = createCard({
        onWatch: (model) => onWatch(channel, model),
        // Pre-warm. The result is deliberately dropped: what is wanted is the
        // server's own probe cache, not the answer.
        onIntent: () => { tuneIn(channel.id, { fetchImpl, clock }).catch(() => {}); },
      });
      const entry = { channel, card, now: null, receivedAt: clock(), timer: null, model: null };
      cards.set(channel.id, entry);
      rail.append(card.node);
      draw(entry);
      first.push(entry);
    }
    sync();

    if (typeof document !== 'undefined' && document.addEventListener) {
      document.addEventListener('visibilitychange', onVisibility);
    }
    if (autoStart) start();
    return first;
  }

  const ready = load().catch(() => { sync(); return []; });

  return {
    ready,
    /** Put the strip away — a search is taking the page over. */
    hide() { wanted = false; pause(); sync(); },
    /** Bring it back, and catch up on what has been on in the meantime. */
    show() { wanted = true; sync(); start(); },
    /** Everything currently drawn, for tests and diagnostics. */
    models() { return [...cards.values()].map((e) => e.model); },
    stop() {
      stopped = true;
      pause();
      if (typeof document !== 'undefined' && document.removeEventListener) {
        document.removeEventListener('visibilitychange', onVisibility);
      }
    },
  };
}
