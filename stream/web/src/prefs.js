/**
 * What this browser remembers.
 *
 * THE DEFECT THIS EXISTS TO FIX. Eleven filter controls shipped on the search
 * bar and not one of them survived a reload. Somebody would narrow a search to
 * 1080p x265 with fifty seeders, press F5, and get the unfiltered catalogue
 * back with every chip blank -- so the only way to keep a set of filters was to
 * never leave the page. Three keys were in localStorage (`mw-dht`,
 * `privacy-ack`, `yarrit.gateway`) and none of them was a preference anybody
 * had expressed on purpose.
 *
 * NO ACCOUNT. This is localStorage and nothing else. The site is public, most
 * visitors have no session, and a preference that only works signed in is a
 * preference that does not work -- so everything here is per browser, written
 * by the page, readable by nobody else, and never sent anywhere.
 *
 * WHAT IS PER-DOMAIN AND WHAT IS NOT
 *
 * Two groups, split on whether the setting means the same thing everywhere:
 *
 *   Shared      the categories you ticked (which IS the domain), 18+, "plays in
 *               browser", and the language. These are statements about you, not
 *               about a catalogue: somebody who does not want adult results
 *               does not want them in music either.
 *
 *   Per-domain  sort, minimum seeders, size range, quality, codec, source and
 *               the game system. "At least 40 seeders" is a sentence about a
 *               swarm and means nothing about an archive.org concert, which has
 *               no seeders at all; "1080p" is meaningless for a comic; "Most
 *               seeders" is the right default for films and a poor one for a
 *               live-music shelf where everything is hosted; and "Super
 *               Nintendo" is not a statement about books. Keeping one set of
 *               numbers for all of them means every switch between categories
 *               carries over constraints from a catalogue that has nothing in
 *               common with the one now on screen.
 *
 * The per-domain key follows the SERVER's own rule for what a domain is (see
 * kindFor in search/filter.go): exactly one category ticked is that domain,
 * none or several is "all". Any other rule here and the client would remember
 * numbers under a name the server does not agree exists.
 *
 * EVERYTHING IS A NO-OP ON A STORAGE THAT THROWS. Private windows and some TV
 * webviews throw on any localStorage access; there, preferences last for the
 * session and nothing else changes.
 */

export const PREFS_KEY = 'yarrit.prefs';

/**
 * Bumped only for a change that would make an old record wrong rather than
 * merely incomplete. Anything unrecognised is dropped field by field by the
 * readers below, so adding a field needs no bump.
 */
export const PREFS_VERSION = 1;

/** Filter settings that belong to one domain and would be wrong in another. */
export const DOMAIN_DEFAULTS = Object.freeze({
  sort: 'seeders',
  seeders: 1,
  minSize: '',
  maxSize: '',
  quality: [],
  codec: [],
  source: '',
  // Game systems -- snes, genesis, c64. Per-domain rather than shared because a
  // machine is only a thing in games: remembering "Super Nintendo" and applying
  // it to Books would empty that catalogue for a reason nobody could see.
  systems: [],
});

/** Filter settings that mean the same thing in every domain. */
export const SHARED_DEFAULTS = Object.freeze({
  groups: [],
  adult: false,
  webSafe: false,
  // '' means "say nothing and let the server decide", which is not the same as
  // "no filter": the server defaults MUSIC to English and every other domain to
  // no constraint at all. 'any' is the explicit "show me everything".
  lang: '',
});

export const APPEARANCE_DEFAULTS = Object.freeze({
  theme: 'dark',    // dark | midnight | light
  covers: 'medium', // small | medium | large
  motion: 'full',   // full | reduced
});

export const PLAYBACK_DEFAULTS = Object.freeze({
  autoplay: true,
  // How a small picture is treated on a big screen. See videofit.js.
  upscale: 'fill',  // fill | natural
  subtitles: 'off', // off | on -- turn a bundled subtitle track on by itself
});

const SORTS = new Set(['seeders', 'relevance', 'quality', 'size', 'recent', 'title']);
const SOURCES = new Set(['', 'instant', 'swarm']);
const THEMES = new Set(['dark', 'midnight', 'light']);
const COVERS = new Set(['small', 'medium', 'large']);
const MOTION = new Set(['full', 'reduced']);
const UPSCALE = new Set(['fill', 'natural']);
const SUBS = new Set(['off', 'on']);

/**
 * The languages worth offering, and what they are called.
 *
 * A subset of search/music.go's table on purpose: that one exists to RECOGNISE
 * whatever an archive.org item happens to say about itself, spellings and all,
 * and half of it would be a list of codes nobody is going to pick from a menu.
 * A code missing from here still works if it arrives in a stored preference --
 * see readShared -- so this list decides what the menu offers and nothing else.
 */
export const LANGUAGES = Object.freeze([
  ['', 'Default (English for music)'],
  ['any', 'Any language'],
  ['en', 'English'],
  ['es', 'Spanish'],
  ['fr', 'French'],
  ['de', 'German'],
  ['it', 'Italian'],
  ['pt', 'Portuguese'],
  ['ru', 'Russian'],
  ['ja', 'Japanese'],
  ['ko', 'Korean'],
  ['zh', 'Chinese'],
  ['hi', 'Hindi'],
  ['ar', 'Arabic'],
  ['nl', 'Dutch'],
  ['sv', 'Swedish'],
  ['pl', 'Polish'],
  ['tr', 'Turkish'],
  ['he', 'Hebrew'],
  ['el', 'Greek'],
  ['la', 'Latin'],
]);

/**
 * Which per-domain slice a set of ticked categories refers to.
 *
 * One category is that domain. None or several is "all", because that is
 * exactly what the server does with them -- several categories at once imply no
 * single Newznab bucket, so there is no domain to be specific about.
 */
export function domainKeyFor(groups) {
  const list = [...(groups || [])]
    .map((g) => String(g || '').toLowerCase().trim())
    .filter(Boolean);
  return list.length === 1 ? list[0] : 'all';
}

/**
 * How many filters are doing something, for the button that clears them.
 *
 * The count is the whole point: "Reset" as a permanent grey word next to a bar
 * of controls says nothing about whether anything is set, and a filter somebody
 * forgot they set is how a working site starts looking broken. Categories are
 * counted as one regardless of how many are ticked -- they read as one decision
 * ("show me games") rather than as three separate constraints.
 */
export function activeFilterCount(filters) {
  if (!filters) return 0;
  let n = 0;
  if (size(filters.groups)) n++;
  if (filters.adult) n++;
  if (filters.webSafe) n++;
  if (filters.lang) n++;
  if (Number(filters.seeders || 0) !== DOMAIN_DEFAULTS.seeders) n++;
  if (String(filters.minSize ?? '') !== '') n++;
  if (String(filters.maxSize ?? '') !== '') n++;
  if (size(filters.quality)) n++;
  if (size(filters.codec)) n++;
  if (size(filters.systems)) n++;
  if (filters.source) n++;
  if (filters.sort && filters.sort !== DOMAIN_DEFAULTS.sort) n++;
  return n;
}

function size(v) {
  if (!v) return 0;
  if (typeof v.size === 'number') return v.size;
  return Array.isArray(v) ? v.length : 0;
}

// ------------------------------------------------------------------ reading --

function str(v, fallback = '') {
  return typeof v === 'string' ? v : fallback;
}

function bool(v, fallback) {
  return typeof v === 'boolean' ? v : fallback;
}

function oneOf(set, v, fallback) {
  return typeof v === 'string' && set.has(v) ? v : fallback;
}

/** A list of short lowercase tokens, deduplicated and capped. */
function tokens(v, cap = 24) {
  if (!Array.isArray(v)) return [];
  const out = [];
  for (const raw of v) {
    const s = String(raw ?? '').toLowerCase().trim().slice(0, 40);
    if (s && !out.includes(s)) out.push(s);
    if (out.length >= cap) break;
  }
  return out;
}

/** A number typed into one of the size boxes: kept as the string the input holds. */
function numberish(v) {
  if (typeof v === 'number' && Number.isFinite(v)) return String(Math.max(0, Math.trunc(v)));
  const s = String(v ?? '').trim();
  if (!s) return '';
  return /^\d{1,9}$/.test(s) ? s : '';
}

function readShared(raw) {
  const r = raw && typeof raw === 'object' ? raw : {};
  // The language is NOT checked against LANGUAGES. That list decides what the
  // menu offers; a code stored by a newer build, or typed into a URL, is still
  // a thing the server understands, and quietly rewriting it to the default
  // would be this file overruling a choice it merely failed to recognise.
  const lang = str(r.lang).toLowerCase().trim().slice(0, 12);
  return {
    groups: tokens(r.groups, 12),
    adult: bool(r.adult, SHARED_DEFAULTS.adult),
    webSafe: bool(r.webSafe, SHARED_DEFAULTS.webSafe),
    lang: /^[a-z*]{1,12}$/.test(lang) ? lang : '',
  };
}

function readDomain(raw) {
  const r = raw && typeof raw === 'object' ? raw : {};
  const seeders = Number(r.seeders);
  return {
    sort: oneOf(SORTS, r.sort, DOMAIN_DEFAULTS.sort),
    seeders: Number.isFinite(seeders) && seeders >= 0
      ? Math.min(9999, Math.trunc(seeders))
      : DOMAIN_DEFAULTS.seeders,
    minSize: numberish(r.minSize),
    maxSize: numberish(r.maxSize),
    quality: tokens(r.quality),
    codec: tokens(r.codec),
    source: oneOf(SOURCES, r.source, DOMAIN_DEFAULTS.source),
    // Not checked against a list of machines, for the same reason `lang` is
    // not checked against LANGUAGES: the server owns that vocabulary and knows
    // aliases this file has never heard of, and quietly dropping a slug merely
    // because this build does not recognise it would be a preference thrown
    // away rather than honoured. tokens() already bounds the length and count.
    systems: tokens(r.systems, 12),
  };
}

function readAppearance(raw) {
  const r = raw && typeof raw === 'object' ? raw : {};
  return {
    theme: oneOf(THEMES, r.theme, APPEARANCE_DEFAULTS.theme),
    covers: oneOf(COVERS, r.covers, APPEARANCE_DEFAULTS.covers),
    motion: oneOf(MOTION, r.motion, APPEARANCE_DEFAULTS.motion),
  };
}

function readPlayback(raw) {
  const r = raw && typeof raw === 'object' ? raw : {};
  return {
    autoplay: bool(r.autoplay, PLAYBACK_DEFAULTS.autoplay),
    upscale: oneOf(UPSCALE, r.upscale, PLAYBACK_DEFAULTS.upscale),
    subtitles: oneOf(SUBS, r.subtitles, PLAYBACK_DEFAULTS.subtitles),
  };
}

/**
 * Everything, cleaned, from whatever was in storage.
 *
 * Field by field rather than all-or-nothing. A record written by a newer build
 * with one field this one has never heard of is not a corrupt record, and
 * throwing the whole thing away would silently reset somebody's entire setup
 * because of one unknown key.
 */
export function normalisePrefs(raw) {
  const r = raw && typeof raw === 'object' ? raw : {};
  const domains = {};
  const src = r.domains && typeof r.domains === 'object' ? r.domains : {};
  for (const key of Object.keys(src).slice(0, 40)) {
    const k = String(key).toLowerCase().trim();
    if (!/^[a-z]{1,20}$/.test(k)) continue;
    domains[k] = readDomain(src[key]);
  }
  return {
    v: PREFS_VERSION,
    shared: readShared(r.shared),
    domains,
    appearance: readAppearance(r.appearance),
    playback: readPlayback(r.playback),
  };
}

function defaults() {
  return normalisePrefs(null);
}

// ------------------------------------------------------------------ storage --

/**
 * localStorage, or a Map that behaves like it.
 *
 * A private window throws on the FIRST access rather than on construction,
 * which is why this touches it before handing it back -- a store that throws on
 * every write would otherwise be discovered one preference at a time.
 */
export function safeStorage() {
  try {
    const s = globalThis.localStorage;
    if (s) { s.getItem(PREFS_KEY); return s; }
  } catch { /* falls through to memory */ }
  const mem = new Map();
  // length/key are part of the shape on purpose: describeStored enumerates a
  // storage the way a real one is enumerated, and a fallback missing them would
  // report "nothing stored" for a session that has stored plenty.
  return {
    get length() { return mem.size; },
    key: (i) => [...mem.keys()][i] ?? null,
    getItem: (k) => (mem.has(k) ? mem.get(k) : null),
    setItem: (k, v) => { mem.set(k, String(v)); },
    removeItem: (k) => { mem.delete(k); },
  };
}

/**
 * The preference store.
 *
 * A factory rather than a module singleton, matching createServiceStore: the
 * tests hand it a storage that is not a browser's, and a second store can exist
 * without a global.
 */
export function createPrefs(storage = safeStorage()) {
  let data = load();

  function load() {
    let raw;
    try {
      raw = storage.getItem(PREFS_KEY);
    } catch {
      return defaults();
    }
    if (!raw) return defaults();
    try {
      return normalisePrefs(JSON.parse(raw));
    } catch {
      // Someone else's key, a truncated write, a half-cleared profile. There is
      // nothing to salvage and nothing worth reporting: defaults are a working
      // site.
      return defaults();
    }
  }

  function flush() {
    try {
      storage.setItem(PREFS_KEY, JSON.stringify(data));
    } catch {
      /* full, or disabled. The in-memory copy is still correct for this
         session, which is the whole of what is lost. */
    }
  }

  return {
    /** Everything, as a plain object. A copy: callers must not mutate storage. */
    snapshot() { return structuredClone(data); },

    shared() { return { ...data.shared }; },
    setShared(patch) {
      data.shared = readShared({ ...data.shared, ...patch });
      flush();
      return this.shared();
    },

    /** One domain's slice, defaults for a domain never configured. */
    domain(key) {
      const k = String(key || 'all');
      return { ...(data.domains[k] || readDomain(null)) };
    },
    setDomain(key, patch) {
      const k = String(key || 'all');
      data.domains[k] = readDomain({ ...(data.domains[k] || {}), ...patch });
      flush();
      return { ...data.domains[k] };
    },

    appearance() { return { ...data.appearance }; },
    setAppearance(patch) {
      data.appearance = readAppearance({ ...data.appearance, ...patch });
      flush();
      return this.appearance();
    },

    playback() { return { ...data.playback }; },
    setPlayback(patch) {
      data.playback = readPlayback({ ...data.playback, ...patch });
      flush();
      return this.playback();
    },

    /**
     * Forget the filters and nothing else.
     *
     * Appearance and playback survive on purpose: "clear my filters" is a
     * statement about a search, and answering it by also putting the site back
     * to a theme somebody deliberately changed is a different, larger thing
     * than the one they asked for.
     */
    clearFilters() {
      data.shared = readShared(null);
      data.domains = {};
      flush();
    },

    /** Forget all of it, including the key itself. */
    clearAll() {
      data = defaults();
      try {
        storage.removeItem(PREFS_KEY);
      } catch { /* nothing left to do about it */ }
    },
  };
}

// ------------------------------------------------------------- stored data --

/**
 * Every localStorage key this site writes, named in plain words.
 *
 * `match` rather than a flat list because two of them are not fixed strings:
 * EmulatorJS writes one key per game save, under a path-shaped name of its own
 * choosing, and those are the single most valuable thing in here -- somebody's
 * actual save file. A "clear everything" that quietly ate them would be the
 * worst button on the site.
 */
export const STORED_KINDS = Object.freeze([
  {
    id: 'prefs',
    label: 'Your preferences',
    detail: 'Filters, sort order, theme and playback choices.',
    match: (k) => k === PREFS_KEY,
  },
  {
    id: 'services',
    label: 'Your services',
    detail: 'Addresses and keys for the Radarr, Plex or Jellyfin you run.',
    match: (k) => k === 'yarrit_services',
  },
  {
    id: 'server',
    label: 'Server address',
    detail: 'The Yarr.It this browser talks to, when it is not this one.',
    match: (k) => k === 'yarrit_server' || k === 'yarrit.gateway',
  },
  {
    id: 'saves',
    label: 'Game saves',
    detail: 'Save states written by the emulator. Deleting these loses progress.',
    match: (k) => /\/bridge\/|^EJS_|^ejs-/.test(k),
  },
  {
    id: 'player',
    label: 'Player choice',
    detail: 'Which of the two players a game starts in.',
    match: (k) => k === 'yarrit.player',
  },
  {
    id: 'notices',
    label: 'Dismissed notices',
    detail: 'The VPN banner you already read.',
    match: (k) => k === 'privacy-ack' || k === 'mw-dht',
  },
]);

/**
 * What is actually in this browser, grouped and measured.
 *
 * Measured rather than merely listed: "Game saves" with nothing next to it says
 * nothing, and 41 KB of them says "there is something here you would miss".
 * Sizes are the stored characters, which is what a quota is counted in.
 */
export function describeStored(storage = safeStorage()) {
  const keys = storageKeys(storage);
  const out = [];
  for (const kind of STORED_KINDS) {
    const mine = keys.filter((k) => kind.match(k));
    if (!mine.length) continue;
    let bytes = 0;
    for (const k of mine) {
      try {
        bytes += (storage.getItem(k) || '').length + k.length;
      } catch { /* a key that vanished between listing and reading */ }
    }
    out.push({ ...kind, keys: mine, count: mine.length, bytes });
  }
  return out;
}

/**
 * Every key in a storage, however that storage lets you ask.
 *
 * A real Storage answers `length`/`key(i)`; the Map-backed fallback above and
 * the plain objects the tests pass do not. Both shapes are handled here rather
 * than at each call site, and a storage that throws is reported as empty --
 * "nothing stored" is the honest reading of "cannot be read".
 */
function storageKeys(storage) {
  try {
    if (storage && typeof storage.key === 'function' && typeof storage.length === 'number') {
      const out = [];
      for (let i = 0; i < storage.length; i++) {
        const k = storage.key(i);
        if (typeof k === 'string') out.push(k);
      }
      return out;
    }
    if (storage instanceof Map) return [...storage.keys()];
    return Object.keys(storage || {});
  } catch {
    return [];
  }
}

/** "1.4 KB", "312 bytes" -- for a list nobody should need a calculator for. */
export function formatBytes(n) {
  const b = Number(n) || 0;
  if (b < 1024) return `${b} byte${b === 1 ? '' : 's'}`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`;
  return `${(b / (1024 * 1024)).toFixed(1)} MB`;
}

/** Remove every key one of these kinds owns. Returns how many went. */
export function forgetStored(ids, storage = safeStorage()) {
  const want = new Set([...(ids || [])]);
  let removed = 0;
  for (const kind of describeStored(storage)) {
    if (!want.has(kind.id)) continue;
    for (const k of kind.keys) {
      try {
        storage.removeItem(k);
        removed++;
      } catch { /* leave it and carry on: a partial clear beats an exception */ }
    }
  }
  return removed;
}
