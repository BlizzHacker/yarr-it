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
export async function fetchVerdict(id, {
  fetchImpl = fetch, base = '', bios = [], isolated = undefined,
} = {}) {
  const key = String(id ?? '').trim();
  if (!key) return emptyVerdict('', 'no item was named.');

  let response;
  try {
    response = await fetchImpl(`${base}/api/play/archive?${verdictQuery(key, bios, isolated)}`);
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
 * The two things only THIS browser knows, put on the query string.
 *
 * Both are capabilities, and both only ever turn a refusal into a game:
 *
 *   bios      the machines this browser holds firmware for. Three machines --
 *             ColecoVision, PlayStation, Amiga -- are refused solely because
 *             console firmware is not ours to ship, and it IS the owner's to
 *             supply. The file never leaves this browser; only the machine's
 *             NAME is sent, which says nothing about the file and everything
 *             about which button to draw.
 *   isolated  whether this document is cross-origin isolated, which is what
 *             makes SharedArrayBuffer -- and therefore the threaded DOS and PSP
 *             cores -- exist at all. Read from the platform rather than
 *             configured, because a claim that disagreed with the browser would
 *             produce a core that loads and then throws.
 *
 * Sending nothing is always safe: the server's answer for a caller that declares
 * nothing is the answer for a visitor who has neither.
 */
export function verdictQuery(id, bios = [], isolated = undefined) {
  const params = new URLSearchParams({ id });
  const machines = [...new Set((bios ?? []).map((s) => String(s ?? '').trim()).filter(Boolean))];
  if (machines.length) params.set('bios', machines.sort().join(','));
  const isolatedNow = isolated === undefined ? globalThis.crossOriginIsolated === true : isolated === true;
  if (isolatedNow) params.set('isolated', '1');
  return params.toString();
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
    // What the game is and which key is fire. Optional in exactly one direction:
    // its absence costs an instructions panel, never a game.
    guide: raw.guide && typeof raw.guide === 'object' && !Array.isArray(raw.guide)
      ? raw.guide
      : null,
    // The firmware this machine needs, named. On a refusal it is an invitation;
    // on a success it says which file is in use, whose it is, and -- when it is
    // the household library's -- where to fetch it.
    biosNeeded: normaliseBios(raw.biosNeeded),
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

/** Where firmware the household's library holds is relayed from. */
export const BIOS_PATH = '/api/play/bios/';

/**
 * Whose firmware a game is running with. Mirrors play_bios.go, in precedence
 * order: the first that can answer is the one that does.
 */
export const BIOS_SOURCE = {
  /** A file this visitor supplied, held in this browser and never uploaded. */
  YOURS: 'yours',
  /** A file the household's library server holds, relayed through our server. */
  LIBRARY: 'library',
  /** The file the Internet Archive's own player uses, relayed through ours. */
  ARCHIVE: 'archive',
  /** A free replacement already inside the core; a setting, not a file. */
  BUILTIN: 'builtin',
};

/**
 * The firmware block of a verdict, forced into a shape that cannot mislead.
 *
 * There are two owners of a BIOS now -- the file this browser holds, and the one
 * the household's library server holds -- and the second arrives with a URL the
 * emulator will be pointed at. That makes this the one field in a verdict that
 * turns into a NETWORK DESTINATION, so it is the one field that is checked
 * rather than copied.
 *
 * `url` is accepted only as a relative path under `/api/play/bios/`, which is
 * our own relay. Nothing else: not an absolute URL, not a protocol-relative one,
 * not a path that climbs out. The server has no reason to send anything else,
 * and if something between here and it ever did, the cost must be a firmware
 * file that does not load rather than a browser aimed somewhere on somebody
 * else's say-so.
 */
export function normaliseBios(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw) || !raw.system) return null;

  const bios = {
    system: String(raw.system),
    label: String(raw.label ?? ''),
    files: Array.isArray(raw.files) ? raw.files.map((f) => String(f)) : [],
    detail: String(raw.detail ?? ''),
    // "yours" or "library", and only ever one of those two. Anything else is
    // treated as "no firmware is in use", which is the direction that costs a
    // sentence rather than inventing a source.
    source: Object.values(BIOS_SOURCE).includes(raw.source) ? raw.source : '',
    file: String(raw.file ?? ''),
    sizeBytes: Number.isFinite(raw.sizeBytes) ? raw.sizeBytes : 0,
    url: '',
    // Core options, for the one source that is a setting rather than a file.
    // Copied key by key as strings: these are written straight into
    // EmulatorJS's own configuration, so a nested object arriving here would be
    // serialised into the core's config file as "[object Object]".
    options: raw.options && typeof raw.options === 'object' && !Array.isArray(raw.options)
      ? Object.fromEntries(Object.entries(raw.options)
        .filter(([k, v]) => k && (typeof v === 'string' || typeof v === 'number'))
        .map(([k, v]) => [String(k), String(v)]))
      : {},
  };

  const url = String(raw.url ?? '');
  if (url.startsWith(BIOS_PATH) && !url.includes('..') && !url.startsWith('//')) {
    bios.url = url;
  }
  // A source that says it has a FILE and gives nowhere to fetch it from is not
  // a source: there would be nothing to hand the emulator, and saying "running
  // from your library" over a game with no firmware is the exact lie this whole
  // area exists to prevent. The built-in source is exempt because it genuinely
  // has no file -- its whole payload is `options`, so an empty one is the same
  // kind of nothing.
  const needsFile = bios.source === BIOS_SOURCE.LIBRARY || bios.source === BIOS_SOURCE.ARCHIVE;
  if (needsFile && !bios.url) bios.source = '';
  if (bios.source === BIOS_SOURCE.BUILTIN && !Object.keys(bios.options).length) bios.source = '';
  return bios;
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

// --------------------------------------------------------- the player switch --

/**
 * Where the viewer's choice of player is remembered.
 *
 * A preference, not a route: it says which player they would rather have, and it
 * is applied only where that player can actually run the item. Storing a route
 * per item would be a cache to invalidate; storing a taste is one value that
 * never goes stale.
 */
export const PLAYER_PREF_KEY = 'yarrit.player';

export function readPlayerPreference(storage = safeStorage()) {
  try {
    const stored = storage?.getItem(PLAYER_PREF_KEY);
    return stored === ROUTE.ARCHIVE || stored === ROUTE.EMULATORJS ? stored : '';
  } catch {
    return '';
  }
}

export function writePlayerPreference(route, storage = safeStorage()) {
  if (route !== ROUTE.ARCHIVE && route !== ROUTE.EMULATORJS) return;
  try {
    storage?.setItem(PLAYER_PREF_KEY, route);
  } catch {
    /* private mode, or a webview with storage disabled: the choice lasts for
       this session and that is the whole cost. */
  }
}

function safeStorage() {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

/**
 * The two players, and whether each can run THIS item.
 *
 * Returns both entries always, in a fixed order, each with `available` and -- if
 * not -- the one plain sentence saying why. A switch that hides its other half
 * leaves a viewer wondering whether the option exists; a switch that offers a
 * dead option is the dead button this whole path exists to remove. Showing both
 * with one disabled and explained is the only version that is neither.
 *
 * The sentence is never invented here. It is the server's `reasons[0].detail`,
 * which was written against the actual item, or -- where the server said nothing
 * because there was nothing to say -- a statement of fact about the route.
 */
export function playerOptions(verdict) {
  const reason = firstDetail(verdict);
  const ours = verdict?.route === ROUTE.EMULATORJS;

  return [
    {
      route: ROUTE.EMULATORJS,
      label: 'Yarr.It player',
      // The reason to want it, in four words.
      note: 'On-screen controls, save states',
      available: ours,
      why: ours ? '' : (reason || 'This item cannot run in the Yarr.It player.'),
    },
    {
      route: ROUTE.ARCHIVE,
      label: 'Internet Archive player',
      note: "Opens the Internet Archive's own player",
      available: Boolean(verdict?.embed),
      why: verdict?.embed
        ? ''
        : 'The Internet Archive has no player for this item.',
    },
  ];
}

/** Whether there is a real choice to offer, rather than one option and a label. */
export function canSwitchPlayer(verdict) {
  return playerOptions(verdict).filter((o) => o.available).length > 1;
}

/**
 * Which player to start in.
 *
 * EmulatorJS by default, everywhere it can run -- that is the whole point. It is
 * the only one of the two with on-screen controls, the only one whose canvas we
 * can size, and the only one whose instructions we can be sure of. The viewer's
 * remembered preference overrides that, and availability overrides everything:
 * a preference for a player that cannot run this item is not a reason to show
 * them nothing.
 */
export function choosePlayer(verdict, preference = readPlayerPreference()) {
  const options = playerOptions(verdict);
  const available = options.filter((o) => o.available);
  if (!available.length) return ROUTE.NONE;

  const wanted = available.find((o) => o.route === preference);
  if (wanted) return wanted.route;

  const ours = available.find((o) => o.route === ROUTE.EMULATORJS);
  return (ours ?? available[0]).route;
}

/**
 * The one plain sentence for a viewer who has no choice.
 *
 * '' when both players work, because then the switch speaks for itself.
 */
export function solePlayerSentence(verdict) {
  const options = playerOptions(verdict);
  const available = options.filter((o) => o.available);
  if (available.length !== 1) return '';
  const missing = options.find((o) => !o.available);
  return `${available[0].label} only — ${missing.why}`;
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
export function toPlayable(verdict, { doc = undefined, route = undefined, biosUrl = null } = {}) {
  if (!canPlay(verdict)) {
    throw new PlaybackError(
      FAILURE.UNSUPPORTED_CODEC,
      firstDetail(verdict) || 'this item cannot be played here.',
    );
  }

  // `route` is the viewer working the switch. It may only ever select a player
  // that playerOptions() says can run this item -- a switch that could ask for
  // an impossible route would be a way to build the dead button by hand.
  const wanted = route ?? verdict.route;
  const option = playerOptions(verdict).find((o) => o.route === wanted);
  if (!option?.available) {
    throw new PlaybackError(
      FAILURE.UNSUPPORTED_CODEC,
      option?.why || 'that player cannot run this item.',
    );
  }

  if (wanted === ROUTE.ARCHIVE) {
    return makePlayable({ render: RENDER.EMBED, src: verdict.embed, mime: 'text/html' });
  }

  // EmulatorJS. The ROM URL is the server's -- it points at the relay, because
  // archive.org sends no Access-Control-Allow-Origin on downloads and a WASM
  // emulator fetches the ROM itself. Rebuilding that URL here would be a second
  // place for the relay path to be wrong.
  const { url, name } = verdict.rom;
  const label = name || verdict.title || 'game';
  let handle = null;

  // Firmware, if this machine needs any. Two owners, one precedence rule, and
  // it is enforced here as well as on the server so that the two cannot
  // disagree about which file is running:
  //
  //   1. `biosUrl` -- a blob: URL the caller made from a file THIS BROWSER
  //      holds. Those bytes never left the machine and never will.
  //   2. the library's copy, relayed from our own server. Only ever the
  //      relative path normaliseBios() already checked; the library itself is
  //      never named here, and could not be reached from here if it were.
  //
  // Absent both, nothing is set at all -- and that must stay true rather than
  // becoming an empty string, because EJS_biosUrl is a global that outlives the
  // game that set it.
  const firmware = biosUrl || verdict.biosNeeded?.url || null;

  // Core options are the whole of the "builtin" firmware source: the free
  // replacement is already inside the core and only has to be switched on. They
  // are passed even when there is also a firmware URL, because they are the
  // server's statement about how this core should be configured for this
  // machine and there is no case where half of it is right.
  const coreOptions = verdict.biosNeeded?.options ?? null;

  return makePlayable({
    render: RENDER.CANVAS,
    src: url,
    mime: 'application/octet-stream',
    mount(el) {
      handle = mountEmulator(el, url, {
        core: verdict.core,
        name: label,
        ...(firmware ? { biosUrl: firmware } : {}),
        ...(coreOptions && Object.keys(coreOptions).length ? { coreOptions } : {}),
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
export async function play(id, {
  fetchImpl = fetch, base = '', doc = undefined,
  bios = [], isolated = undefined, route = undefined, biosUrl = null,
} = {}) {
  const verdict = await fetchVerdict(id, { fetchImpl, base, bios, isolated });
  return toPlayable(verdict, { doc, route: route ?? choosePlayer(verdict), biosUrl });
}
