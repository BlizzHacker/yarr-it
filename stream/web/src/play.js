/**
 * Whether to draw a Play button, and what happens when it is pressed.
 *
 * THE RULE THIS FILE IMPLEMENTS: only offer Play when it will actually play.
 *
 * That is not a rule about pressing the button, it is a rule about DRAWING it.
 * `resolvers/archive.js` already refuses correctly at play time -- but a refusal
 * at play time is a dead button, and a dead button is worse than no button. So
 * the verdict is fetched from `/api/play/archive` while the card is being
 * rendered, and everything a UI needs in order to be honest arrives with it:
 * whether it plays, by which route, and if not our own player, exactly why not.
 *
 * WHY THE SERVER DECIDES AND NOT THIS FILE.
 *
 * Every question that stops a game from playing needs the item's metadata:
 * whether the Internet Archive lets it be downloaded, which of its files is the
 * ROM, and how big that file is. Answering here would mean a metadata request
 * per card -- sixty per search -- and would put the core table in a third place
 * to drift. The server already holds it, checks it against ROM Hub's tables and
 * EmulatorJS's own vocabulary, and answers in one hop.
 *
 * WHAT THIS FILE ADDS ON TOP.
 *
 * Three things the server cannot know:
 *
 *   1. The verdict has THREE outcomes and a UI wants two words. `emulatorjs`
 *      and `archive` are both real games in a real browser tab; `none` is not.
 *      `playLabel` collapses them the way a person reads them, and keeps the
 *      caveat -- their player has no on-screen controls -- attached rather than
 *      dropped.
 *   2. Whether this device can use the route it was offered. A phone on the
 *      archive route is a game you can watch and not play, because their player
 *      expects a keyboard. That is a device fact, and it lives here.
 *   3. Booting EmulatorJS, which is a DOM job.
 *
 * WHAT THIS FILE DOES NOT DO: guess. If the server says no, this says no and
 * repeats the server's sentence. There is no local fallback table, because a
 * fallback that disagreed with the server would be a fourth copy of the map and
 * the disagreement would surface as a button that works on one screen and not
 * another.
 */

import { makePlayable, RENDER } from './source.js';
import { mountEmulator } from './resolvers/game.js';
import { PlaybackError, FAILURE } from './failures.js';

/** The three routes the server can return. Mirrors play_archive.go. */
export const ROUTE = {
  /** Our own EmulatorJS, ROM relayed through the bridge. Touch controls. */
  EMULATORJS: 'emulatorjs',
  /** The Internet Archive's own player, in an iframe. Keyboard only. */
  ARCHIVE: 'archive',
  /** Nothing plays this. */
  NONE: 'none',
};

/**
 * Refusal codes, mirroring play_archive.go.
 *
 * A UI should branch on these rather than on the sentence: the sentence is
 * written to be read by a person and may be reworded, the code may not.
 */
export const REASON = {
  NOT_FOUND: 'not_found',
  UPSTREAM: 'upstream',
  NOT_EMULATED: 'not_emulated',
  NO_CORE: 'no_core',
  NEEDS_BIOS: 'needs_bios',
  NEEDS_ISOLATION: 'needs_isolation',
  STREAM_ONLY: 'stream_only',
  NO_PAYLOAD: 'no_payload',
  TOO_LARGE: 'too_large',
};

/**
 * An answer that is safe to render even when everything went wrong.
 *
 * The default is `none` with an `upstream` reason rather than an empty object,
 * because the one thing a UI must never do with a missing verdict is assume the
 * happy one. A network failure here has to look like "we could not check", not
 * like "press Play".
 */
export function emptyVerdict(id = '', detail = 'the play service could not be reached, so whether this plays is unknown.') {
  return {
    id,
    playable: false,
    route: ROUTE.NONE,
    touch: false,
    reasons: [{ code: REASON.UPSTREAM, detail }],
  };
}

/**
 * Ask the server whether one archive.org item plays.
 *
 * `id` may be a bare identifier or any archive.org URL -- a card carries the
 * URL and should not have to take it apart.
 *
 * Never throws. A verdict that throws would have every caller wrap it in a
 * try/catch whose catch block re-invents this function's default, and one of
 * those catch blocks would eventually get it wrong in the optimistic direction.
 */
export async function fetchVerdict(id, { fetchImpl = fetch, base = '' } = {}) {
  const key = String(id ?? '').trim();
  if (!key) return emptyVerdict('', 'no item was named.');

  let response;
  try {
    response = await fetchImpl(`${base}/api/play/archive?id=${encodeURIComponent(key)}`);
  } catch (error) {
    return emptyVerdict(key, `the play service could not be reached (${error?.message ?? error}).`);
  }
  if (!response?.ok) {
    return emptyVerdict(key, `the play service answered ${response?.status ?? 'nothing'}.`);
  }

  let verdict;
  try {
    verdict = await response.json();
  } catch {
    return emptyVerdict(key, 'the play service did not answer with JSON.');
  }
  return normalise(verdict, key);
}

/**
 * Force a server answer into the shape the rest of this file relies on.
 *
 * Defensive on purpose, and defensive in ONE direction: anything missing or
 * unrecognised becomes not-playable. An older server that has never heard of
 * this endpoint, a proxy that rewrites the body, a field that gets renamed --
 * each of those must cost a button, never produce one that does not work.
 */
export function normalise(raw, id = '') {
  // `typeof [] === 'object'` and an array is truthy, so an array would fall
  // straight through this guard and come out the other side as a verdict whose
  // every field is undefined. That is the optimistic direction, which is the
  // one direction this must never fail in.
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    return emptyVerdict(id, 'the play service returned nothing usable.');
  }

  const route = [ROUTE.EMULATORJS, ROUTE.ARCHIVE, ROUTE.NONE].includes(raw.route)
    ? raw.route
    : ROUTE.NONE;

  const reasons = Array.isArray(raw.reasons)
    ? raw.reasons
      .filter((r) => r && typeof r.code === 'string')
      .map((r) => ({ code: r.code, detail: String(r.detail ?? '') }))
    : [];

  const verdict = {
    id: raw.id || id,
    title: raw.title || '',
    emulator: raw.emulator || '',
    platform: raw.platform || '',
    system: raw.system || '',
    core: raw.core || '',
    coreFile: raw.coreFile || '',
    embed: raw.embed || '',
    rom: raw.rom && raw.rom.url ? { ...raw.rom } : null,
    touch: raw.touch === true,
    route,
    reasons,
    playable: raw.playable === true,
  };

  // The invariants the server promises, re-checked. Not distrust of the server
  // so much as of everything between it and here -- and each of these, if it
  // ever came through wrong, would produce exactly the dead button this whole
  // path exists to remove.
  if (verdict.route === ROUTE.EMULATORJS && (!verdict.core || !verdict.rom)) {
    return {
      ...verdict,
      route: ROUTE.ARCHIVE,
      playable: Boolean(verdict.embed),
      touch: false,
      reasons: [...reasons, {
        code: REASON.NO_PAYLOAD,
        detail: 'the play service offered our own player without a core or a ROM.',
      }],
    };
  }
  if (verdict.route === ROUTE.ARCHIVE && !verdict.embed) {
    return { ...verdict, route: ROUTE.NONE, playable: false };
  }
  if (verdict.route === ROUTE.NONE) verdict.playable = false;

  return verdict;
}

/** Whether a Play control should exist at all. */
export function canPlay(verdict) {
  return Boolean(verdict) && verdict.playable === true && verdict.route !== ROUTE.NONE;
}

/**
 * The words on and under the button.
 *
 * Returns `{label, hint, caveat, blocked}`:
 *
 *   label   what the control says, or "" when there should be no control
 *   hint    the one-line why, always present when something was given up
 *   caveat  a warning about the route that WAS offered -- not a refusal.
 *           This is the honest way to say "their player has no touch controls"
 *           without claiming the game does not play.
 *   blocked true when there is nothing to press
 *
 * `touchOnly` is the device fact this file exists to add: on a phone, the
 * archive route is a game you can watch and not play, so the caveat becomes the
 * hint and the button says so.
 */
export function playLabel(verdict, { touchOnly = false } = {}) {
  if (!canPlay(verdict)) {
    return {
      label: '',
      hint: firstDetail(verdict) || 'This cannot be played here.',
      caveat: '',
      blocked: true,
    };
  }

  if (verdict.route === ROUTE.EMULATORJS) {
    return {
      label: 'Play',
      hint: verdict.system ? `Plays here — ${verdict.system}` : 'Plays here',
      caveat: '',
      blocked: false,
    };
  }

  // The archive route. It plays; it just plays over there, and on a phone it
  // plays badly. Both of those are said out loud.
  const why = firstDetail(verdict);
  const noTouch = 'Their player has no on-screen controls, so it needs a keyboard.';
  return {
    label: 'Play at the Internet Archive',
    hint: why,
    caveat: touchOnly ? noTouch : '',
    blocked: false,
  };
}

function firstDetail(verdict) {
  const reason = verdict?.reasons?.[0];
  return reason ? reason.detail : '';
}

/**
 * Turn a verdict into a Playable.
 *
 * The two routes produce genuinely different players and that is why `render`
 * is a discriminator rather than everything being a `<video src>`: EmulatorJS
 * draws to a canvas and has to be handed a container and told to boot, while
 * the Archive's own player is an iframe.
 *
 * Refuses rather than guessing when the verdict says no, and the message is the
 * server's own sentence -- so what a person is told after pressing is the same
 * thing they were told before it.
 */
export function toPlayable(verdict, { doc = undefined } = {}) {
  if (!canPlay(verdict)) {
    throw new PlaybackError(
      FAILURE.UNSUPPORTED_CODEC,
      firstDetail(verdict) || 'this item cannot be played here.',
    );
  }

  if (verdict.route === ROUTE.ARCHIVE) {
    return makePlayable({ render: RENDER.EMBED, src: verdict.embed, mime: 'text/html' });
  }

  // EmulatorJS. The ROM URL is the server's -- it points at the relay, because
  // archive.org sends no Access-Control-Allow-Origin on downloads and a WASM
  // emulator fetches the ROM itself. Rebuilding that URL here would be a second
  // place for the relay path to be wrong.
  const { url, name } = verdict.rom;
  const label = name || verdict.title || 'game';
  let handle = null;

  return makePlayable({
    render: RENDER.CANVAS,
    src: url,
    mime: 'application/octet-stream',
    mount(el) {
      handle = mountEmulator(el, url, {
        core: verdict.core,
        name: label,
        ...(doc ? { doc } : {}),
      });
    },
    cleanup() {
      handle?.destroy();
      handle = null;
    },
  });
}

/**
 * Resolve and start in one call, for a caller that has an item and a container.
 *
 * Deliberately still two steps underneath: a UI that wants to draw an honest
 * button needs the verdict BEFORE the click, and this convenience must not
 * become the reason somebody skips that.
 */
export async function play(id, { fetchImpl = fetch, base = '', doc = undefined } = {}) {
  return toPlayable(await fetchVerdict(id, { fetchImpl, base }), { doc });
}
