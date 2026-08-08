/**
 * A stranger's own services, configured in their own browser.
 *
 * This is the file that decides whether yarrit.com is a demo or a product. The
 * demo answer is "sign in and we will hold your keys"; the product answer is
 * that a guest who has never made an account types in the address of the Radarr
 * on their kitchen shelf and it works. So:
 *
 *   - configuration lives in localStorage, per browser, with no account
 *   - a key typed here is sent to the address the user gave and nowhere else
 *   - nothing here ever posts a key to yarrit.com, logs one, or writes one into
 *     a URL that gets rendered into the page
 *
 * Reachability is not decided here. transport.js already owns that question and
 * already writes the sentence explaining which wall was hit and what clears it;
 * this file calls it and repeats its answer verbatim rather than inventing a
 * second, worse vocabulary for the same failure.
 *
 * Health is not invented here either. The six states come from schema.json and
 * are deliberately not collapsed: "unreachable" sends someone hunting a network
 * fault that a missing API key would have explained in one line.
 */

import { HEALTH_STATES, labelFor } from './schema.js';
import {
  TRANSPORT, chooseTransport, transportFetch, wouldBeMixedContent,
} from './transport.js';

const KEY = 'yarrit_services';

/**
 * What each kind of service is, as data.
 *
 * `probe` is the request that proves the thing at that address really is what
 * it says it is: the endpoint, how the credential is carried, and a marker in
 * the answer. Without the marker check, a reverse proxy that serves its own
 * error page with a 200 reads as "healthy" -- which is the one answer that
 * stops someone looking any further.
 *
 * `auth: 'query'` exists because two of these genuinely have no header scheme.
 * It is recorded here so the UI can say so out loud, and so every other part of
 * the code can keep the rule that a key never appears in a URL.
 */
export const SERVICE_TYPES = {
  radarr: {
    label: 'Radarr',
    domains: ['video'],
    roles: ['discovery', 'acquisition', 'library'],
    capabilities: ['health', 'search', 'details', 'library', 'libraryStatus', 'request', 'activity'],
    credential: 'API key',
    credentialHint: 'Settings → General → API Key',
    probe: {
      path: '/api/v3/system/status',
      auth: 'header', header: 'X-Api-Key',
      marker: (b) => typeof b?.version === 'string',
      version: (b) => b?.version || '',
    },
  },
  sonarr: {
    label: 'Sonarr',
    domains: ['video'],
    roles: ['discovery', 'acquisition', 'library'],
    capabilities: ['health', 'search', 'details', 'library', 'libraryStatus', 'request', 'activity'],
    credential: 'API key',
    credentialHint: 'Settings → General → API Key',
    probe: {
      path: '/api/v3/system/status',
      auth: 'header', header: 'X-Api-Key',
      marker: (b) => typeof b?.version === 'string',
      version: (b) => b?.version || '',
    },
  },
  lidarr: {
    label: 'Lidarr',
    domains: ['music'],
    roles: ['discovery', 'acquisition', 'library'],
    capabilities: ['health', 'search', 'details', 'library', 'libraryStatus', 'request', 'activity'],
    credential: 'API key',
    credentialHint: 'Settings → General → API Key',
    probe: {
      path: '/api/v1/system/status',
      auth: 'header', header: 'X-Api-Key',
      marker: (b) => typeof b?.version === 'string',
      version: (b) => b?.version || '',
    },
  },
  romarr: {
    label: 'ROMarr',
    domains: ['game'],
    roles: ['discovery', 'acquisition', 'library'],
    capabilities: ['health', 'search', 'details', 'library', 'libraryStatus', 'request', 'activity'],
    credential: 'API key',
    credentialHint: 'Settings → General → API Key',
    probe: {
      // The authenticated status, not /api/health: ROMarr answers {ok:true} on
      // /api/health to anyone, key or no key, so a wrong key would look healthy.
      path: '/api/v1/system/status',
      auth: 'header', header: 'X-Api-Key',
      marker: (b) => typeof b?.version === 'string',
      version: (b) => b?.version || '',
    },
  },
  readarr: {
    label: 'Readarr-compatible',
    domains: ['literature'],
    roles: ['discovery', 'acquisition', 'library'],
    capabilities: ['health', 'search', 'details', 'library', 'libraryStatus', 'request', 'activity'],
    credential: 'API key',
    credentialHint: 'Settings → General → API Key',
    probe: {
      path: '/api/v1/system/status',
      auth: 'header', header: 'X-Api-Key',
      marker: (b) => typeof b?.version === 'string',
      version: (b) => b?.version || '',
    },
  },
  prowlarr: {
    label: 'Prowlarr',
    domains: ['video', 'music', 'game', 'literature', 'comic'],
    roles: ['indexer'],
    capabilities: ['health', 'search'],
    credential: 'API key',
    credentialHint: 'Settings → General → API Key',
    probe: {
      path: '/api/v1/system/status',
      auth: 'header', header: 'X-Api-Key',
      marker: (b) => typeof b?.version === 'string',
      version: (b) => b?.version || '',
    },
  },
  mylar3: {
    label: 'Mylar3',
    domains: ['comic'],
    roles: ['discovery', 'acquisition', 'library'],
    capabilities: ['health', 'search', 'library', 'request'],
    credential: 'API key',
    credentialHint: 'Settings → Web Interface → API key',
    // Mylar3 has no header scheme at all; its key goes in the query string.
    // Said out loud in the UI rather than hidden, because it changes where that
    // key can end up (proxy logs, browser history) and that is the user's call.
    keyInURL: true,
    probe: {
      path: '/api',
      auth: 'query', param: 'apikey',
      query: { cmd: 'getVersion' },
      marker: (b) => b != null && (b.success === true || 'data' in b || 'git_path' in b),
      version: (b) => b?.data?.version || '',
    },
  },
  jellyfin: {
    label: 'Jellyfin',
    domains: ['video', 'music', 'literature', 'image'],
    roles: ['library', 'stream'],
    capabilities: ['health', 'library', 'libraryStatus', 'stream'],
    credential: 'API key',
    credentialHint: 'Dashboard → API Keys → new key',
    probe: {
      path: '/System/Info',
      auth: 'header', header: 'Authorization',
      // Jellyfin's documented scheme. The token rides in a header rather than
      // the query string precisely so it stays out of logs.
      value: (k) => `MediaBrowser Token="${k}", Client="Yarr.It", Device="Yarr.It", DeviceId="yarrit-web", Version="1"`,
      marker: (b) => typeof b?.Version === 'string',
      version: (b) => b?.Version || '',
    },
  },
  plex: {
    label: 'Plex',
    domains: ['video', 'music', 'image'],
    roles: ['library', 'stream'],
    capabilities: ['health', 'library', 'libraryStatus', 'stream'],
    credential: 'Token',
    credentialHint: 'copy an X-Plex-Token from a Plex client',
    probe: {
      // /identity answers without a token; /library/sections needs one. Asking
      // for the one behind the gate is what separates a wrong token from an
      // absent server.
      path: '/library/sections',
      auth: 'header', header: 'X-Plex-Token',
      headers: { Accept: 'application/json' },
      marker: (b) => b?.MediaContainer != null,
      version: (b) => b?.MediaContainer?.version || '',
    },
  },
  emby: {
    label: 'Emby',
    domains: ['video', 'music', 'image'],
    roles: ['library', 'stream'],
    capabilities: ['health', 'library', 'libraryStatus', 'stream'],
    credential: 'API key',
    credentialHint: 'Dashboard → Advanced → API Keys',
    probe: {
      path: '/System/Info',
      auth: 'header', header: 'X-Emby-Token',
      marker: (b) => typeof b?.Version === 'string',
      version: (b) => b?.Version || '',
    },
  },
  komga: {
    label: 'Komga',
    domains: ['comic'],
    roles: ['library', 'reader'],
    capabilities: ['health', 'library', 'libraryStatus'],
    credential: 'API key',
    credentialHint: 'Account settings → API keys',
    probe: {
      path: '/api/v2/users/me',
      auth: 'header', header: 'X-API-Key',
      marker: (b) => typeof b?.id === 'string' || typeof b?.email === 'string',
      version: () => '',
    },
  },
  kavita: {
    label: 'Kavita',
    domains: ['comic', 'literature'],
    roles: ['library', 'reader'],
    capabilities: ['health', 'library', 'libraryStatus'],
    credential: 'API key',
    credentialHint: 'User settings → 3rd Party Clients → API Key',
    // Kavita trades an API key for a JWT and takes the key as a query
    // parameter on that one call. Same disclosure as Mylar3.
    keyInURL: true,
    probe: {
      path: '/api/Plugin/authenticate',
      method: 'POST',
      auth: 'query', param: 'apiKey',
      query: { pluginName: 'Yarr.It' },
      marker: (b) => typeof b?.token === 'string',
      version: () => '',
    },
  },
  audiobookshelf: {
    label: 'Audiobookshelf',
    domains: ['literature'],
    roles: ['library', 'stream'],
    capabilities: ['health', 'library', 'libraryStatus', 'stream'],
    credential: 'API token',
    credentialHint: 'Settings → Users → your user → API token',
    probe: {
      path: '/api/me',
      auth: 'header', header: 'Authorization',
      value: (k) => `Bearer ${k}`,
      marker: (b) => typeof b?.id === 'string' || typeof b?.username === 'string',
      version: () => '',
    },
  },
  romm: {
    label: 'RomM',
    domains: ['game'],
    roles: ['library', 'play'],
    capabilities: ['health', 'library', 'libraryStatus', 'details'],
    credential: 'Bearer token',
    credentialHint: 'from RomM /api/token, or a personal access token',
    probe: {
      // /api/heartbeat is open to anyone, so it proves nothing about the token.
      // /api/platforms is behind the gate.
      path: '/api/platforms',
      auth: 'header', header: 'Authorization',
      value: (k) => `Bearer ${k}`,
      marker: (b) => Array.isArray(b) || Array.isArray(b?.items),
      version: () => '',
    },
  },
};

/** Every type id, in the order the UI should offer them. */
export function allServiceTypes() {
  return Object.keys(SERVICE_TYPES);
}

/**
 * Clean up an address without silently turning a typo into a plausible host.
 *
 * A service base URL is not a server origin, so unlike server.js this keeps the
 * path: reverse proxies at /radarr are ordinary, and stripping the path there
 * would point every call at the proxy's front page instead.
 *
 * Returns '' for anything that is not usable, and callers must refuse rather
 * than store: a rejected address that appears to save is a setting that looks
 * configured and answers nothing.
 */
export function normaliseServiceURL(raw) {
  if (raw == null) return '';
  let s = String(raw).trim();
  if (!s) return '';

  // Test for a scheme rather than for "starts with http". They are not the same
  // test: "javascript:alert(1)" and "ftp://box" both fail the second one and
  // would then be prefixed into something that parses as a perfectly ordinary
  // hostname.
  //
  // But "a colon means a scheme" is wrong in the other direction, and it is
  // wrong on the single most common thing anyone types into this box:
  // "localhost:8096" parses as the scheme "localhost", gets rejected as a
  // scheme we do not speak, and the address vanishes with a message about it
  // not looking like an address. Host-and-port is checked first, and it can
  // only ever match word-then-digits -- so no dangerous scheme can hide in it.
  const hostPort = /^[a-z][a-z0-9+.-]*:\d+([/?#]|$)/i.test(s);
  const scheme = hostPort ? null : /^([a-z][a-z0-9+.-]*):/i.exec(s);
  if (scheme) {
    const proto = scheme[1].toLowerCase();
    if (proto !== 'http' && proto !== 'https') return '';
  } else {
    // A bare host is what people actually type. Which scheme to assume is not a
    // coin flip: a home-network box is overwhelmingly plain HTTP, and guessing
    // https for 192.168.0.26 produces a TLS error that reads as "my Radarr is
    // broken". Anything public gets https, which is the safe assumption there.
    s = (looksPrivate(s) ? 'http://' : 'https://') + s;
  }

  let u;
  try {
    u = new URL(s);
  } catch {
    return '';
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
  if (!u.hostname) return '';
  // Query and hash are meaningless on a base address and would corrupt every
  // path built on top of it.
  u.search = '';
  u.hash = '';
  const path = u.pathname.replace(/\/+$/, '');
  return u.origin + path;
}

/** Does a bare, scheme-less host look like something on a home network? */
function looksPrivate(hostish) {
  const h = String(hostish).split('/')[0].split(':')[0].toLowerCase();
  if (h === 'localhost' || h.endsWith('.local') || h.endsWith('.lan')) return true;
  const m = h.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  if (a === 10 || a === 127) return true;
  if (a === 192 && b === 168) return true;
  if (a === 172 && b >= 16 && b <= 31) return true;
  return a === 169 && b === 254;
}

/**
 * A stable, unique id for one instance.
 *
 * Two Radarrs is the normal case, not an edge case -- Radarr and Radarr-4K,
 * Sonarr-TV and Sonarr-Anime -- so the id cannot be the type. It is generated
 * once, at add, and never derived from anything the user can edit: an id built
 * from the name would change when the name did, and every reference to it
 * (a saved route, a pinned instance) would quietly point at nothing.
 */
export function newServiceId(type, rand = Math.random) {
  const suffix = Math.floor(rand() * 0xffffff).toString(36).padStart(4, '0');
  return `${type}-${Date.now().toString(36)}${suffix}`;
}

function safeStorage() {
  try {
    // Private windows and some TV webviews throw on any storage access.
    const s = globalThis.localStorage;
    if (s) { s.getItem(KEY); return s; }
  } catch { /* falls through */ }
  const mem = new Map();
  return {
    getItem: (k) => (mem.has(k) ? mem.get(k) : null),
    setItem: (k, v) => { mem.set(k, String(v)); },
    removeItem: (k) => { mem.delete(k); },
  };
}

/** One record, cleaned. Returns null for anything that cannot be salvaged. */
function sanitise(row) {
  if (!row || typeof row !== 'object') return null;
  if (typeof row.id !== 'string' || !row.id) return null;
  if (!SERVICE_TYPES[row.type]) return null;
  const url = normaliseServiceURL(row.url);
  return {
    id: row.id,
    type: row.type,
    name: String(row.name || SERVICE_TYPES[row.type].label).slice(0, 80),
    url,
    key: typeof row.key === 'string' ? row.key : '',
    enabled: row.enabled !== false,
    added: Number(row.added) || 0,
  };
}

/**
 * The config store.
 *
 * A factory rather than a module singleton so tests can hand it a storage that
 * is not the browser's, and so a second store can exist without a global.
 */
export function createServiceStore(storage = safeStorage()) {
  function list() {
    let raw;
    try {
      raw = storage.getItem(KEY);
    } catch {
      return [];
    }
    if (!raw) return [];
    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch {
      // Corrupt storage must not take down the screen that would let someone
      // fix it. An empty list is wrong but recoverable; a thrown exception in
      // the settings renderer is not.
      return [];
    }
    if (!Array.isArray(parsed)) return [];
    // A single unreadable row is dropped, not fatal, for the same reason.
    return parsed.map(sanitise).filter(Boolean);
  }

  function write(rows) {
    try {
      storage.setItem(KEY, JSON.stringify(rows));
    } catch { /* storage unavailable; the setting simply will not persist */ }
    return rows;
  }

  function get(id) {
    return list().find((s) => s.id === id) || null;
  }

  /**
   * Add one instance. Returns { ok, service } or { ok:false, error }.
   *
   * A bad address is refused here rather than stored and discovered later:
   * saving a typo produces a row that looks configured, answers nothing, and
   * gives no clue which of the two the user got wrong.
   */
  function add(input, rand = Math.random) {
    const type = String(input?.type || '');
    if (!SERVICE_TYPES[type]) return { ok: false, error: 'Pick what kind of service that is.' };
    const url = normaliseServiceURL(input?.url);
    if (!url) return { ok: false, error: 'That does not look like an address.' };

    // Uniqueness is enforced against what is already stored rather than trusted
    // from the clock: two instances added in the same millisecond is exactly
    // what happens when someone pastes in a setup, and a duplicate id would
    // make "remove Radarr-4K" delete both Radarrs.
    const rows = list();
    const taken = new Set(rows.map((s) => s.id));
    let id = newServiceId(type, rand);
    for (let n = 2; taken.has(id); n++) id = `${newServiceId(type, rand)}-${n}`;

    const svc = {
      id,
      type,
      name: String(input?.name || '').trim().slice(0, 80) || SERVICE_TYPES[type].label,
      url,
      key: typeof input?.key === 'string' ? input.key.trim() : '',
      enabled: input?.enabled !== false,
      added: Date.now(),
    };
    write([...rows, svc]);
    return { ok: true, service: svc };
  }

  /** Edit in place. The id never moves, whatever else changes. */
  function update(id, patch) {
    const rows = list();
    const i = rows.findIndex((s) => s.id === id);
    if (i < 0) return { ok: false, error: 'That service is no longer in this browser.' };

    const next = { ...rows[i] };
    if (patch.type !== undefined) {
      if (!SERVICE_TYPES[patch.type]) return { ok: false, error: 'Pick what kind of service that is.' };
      next.type = patch.type;
    }
    if (patch.url !== undefined) {
      const url = normaliseServiceURL(patch.url);
      if (!url) return { ok: false, error: 'That does not look like an address.' };
      next.url = url;
    }
    if (patch.name !== undefined) {
      next.name = String(patch.name).trim().slice(0, 80) || SERVICE_TYPES[next.type].label;
    }
    if (patch.key !== undefined) next.key = String(patch.key).trim();
    if (patch.enabled !== undefined) next.enabled = patch.enabled !== false;

    rows[i] = next;
    write(rows);
    return { ok: true, service: next };
  }

  function remove(id) {
    const rows = list();
    const next = rows.filter((s) => s.id !== id);
    write(next);
    return { ok: true, removed: rows.length - next.length };
  }

  function clear() { write([]); }

  return { list, get, add, update, remove, clear };
}

// --------------------------------------------------------------- probing --

/**
 * What each health state means to a person, and what it implies they do.
 *
 * Six rows, matching schema.json's six. Collapsing any pair of them into "down"
 * is the failure this whole vocabulary exists to prevent: a rejected key and an
 * unplugged switch look identical from a red dot and have nothing in common.
 */
export const HEALTH_LABELS = {
  healthy: 'Healthy',
  not_configured: 'Not set up yet',
  unreachable: 'Cannot be reached',
  auth_failed: 'Key rejected',
  incompatible: 'Not what it claims',
  degraded: 'Up, but unwell',
};

/** Build the request, keeping the credential out of anything rendered. */
export function buildProbeRequest(svc) {
  const spec = SERVICE_TYPES[svc.type];
  const p = spec.probe;
  const headers = { ...(p.headers || {}) };
  let url = svc.url.replace(/\/+$/, '') + p.path;

  if (p.auth === 'header' && svc.key) {
    headers[p.header] = p.value ? p.value(svc.key) : svc.key;
  }
  const q = new URLSearchParams(p.query || {});
  if (p.auth === 'query' && svc.key) q.set(p.param, svc.key);
  if ([...q].length) url += (url.includes('?') ? '&' : '?') + q.toString();

  return { url, method: p.method || 'GET', headers, carriesKeyInURL: p.auth === 'query' && !!svc.key };
}

/**
 * Turn one answer into one of the six states.
 *
 * Pure, and separate from the fetch, because this is the part that is easy to
 * get wrong and the part worth pinning down: every branch here is a different
 * sentence shown to a person who is trying to fix something.
 */
export function classifyProbe({ status, body, bodyIsJSON = true, error, type, name, transport }) {
  const spec = SERVICE_TYPES[type];
  const who = name || spec?.label || 'that service';
  if (!spec) {
    return { state: 'incompatible', detail: `Yarr.It does not know how to talk to a "${type}".` };
  }

  if (error) {
    // The address answered when transport.js knocked on its front door, and
    // then refused the API call. That pair looks like a contradiction on the
    // screen -- "reachable directly" above "did not answer" -- so it has to be
    // named rather than left for the reader to reconcile. It is nearly always
    // CORS: a service that serves its own web UI happily still refuses to let
    // another website call its API.
    if (transport === TRANSPORT.DIRECT) {
      return {
        state: 'unreachable',
        detail: `${who} answered at that address, then refused this API call. Most self-hosted `
          + 'services will not let another website call their API. The Yarr.It extension, or '
          + 'running Yarr.It on your own machine, gets past that.',
      };
    }
    return {
      state: 'unreachable',
      detail: `${who} did not answer. ${error}`.trim(),
    };
  }

  if (status === 401 || status === 403) {
    return {
      state: 'auth_failed',
      // The service is there. This is a settings problem, and saying
      // "unreachable" here sends someone to check a network that is fine.
      detail: `${who} answered, and rejected the ${spec.credential.toLowerCase()}. `
        + `Check it: ${spec.credentialHint}.`,
    };
  }
  if (status === 404 || status === 405 || status === 501) {
    return {
      state: 'incompatible',
      detail: `Something answered at that address, but it has no ${spec.probe.path}. `
        + `Either that is not ${spec.label}, or it is too old a version.`,
    };
  }
  if (status >= 500) {
    return {
      state: 'degraded',
      detail: `${who} is up but answered ${status}. Something inside it is wrong — check its own logs.`,
    };
  }
  if (status < 200 || status >= 300) {
    return {
      state: 'degraded',
      detail: `${who} answered ${status}, which is not a refusal and not a yes.`,
    };
  }

  if (!bodyIsJSON) {
    return {
      state: 'incompatible',
      detail: `That address answered with something that is not ${spec.label}'s API — `
        + 'often a login page or a reverse proxy answering for it.',
    };
  }
  if (!spec.probe.marker(body)) {
    return {
      state: 'incompatible',
      detail: `That address answered, but not in the shape ${spec.label} answers in. `
        + 'Check the address points at the service itself, not at a proxy in front of it.',
    };
  }

  const version = spec.probe.version(body) || '';
  return {
    state: 'healthy',
    version,
    detail: version ? `${who} answered — version ${version}.` : `${who} answered.`,
  };
}

/**
 * Which transport reaches this service, and what it said when we asked.
 *
 * Two separate answers on purpose. The route is about the wall between this
 * page and that address; the health is about the service behind it. Merging
 * them is how "the browser refused to send this" becomes "your Radarr is
 * offline" -- wrong, and wrong in a direction that makes people go and restart
 * a container that was never broken.
 */
export async function probeService(svc, opts = {}) {
  const {
    pageProtocol,
    hasExtension,
    chooseTransport: chooseT = chooseTransport,
    transportFetch: tFetch = transportFetch,
  } = opts;

  const spec = SERVICE_TYPES[svc?.type];
  if (!spec) {
    return {
      route: { transport: TRANSPORT.NONE, detail: '' },
      health: { state: 'incompatible', detail: `Yarr.It does not know how to talk to a "${svc?.type}".` },
    };
  }

  if (!svc.url) {
    return {
      route: { transport: TRANSPORT.NONE, detail: '' },
      health: { state: 'not_configured', detail: `No address set for ${svc.name}.` },
    };
  }

  const routeOpts = {};
  if (pageProtocol !== undefined) routeOpts.pageProtocol = pageProtocol;
  if (hasExtension !== undefined) routeOpts.hasExtension = hasExtension;
  const route = await chooseT(svc.url, routeOpts);

  if (route.transport === TRANSPORT.NONE) {
    // transport.js already wrote the sentence that names the wall and the fix.
    // Repeating it verbatim is the point; paraphrasing it here would put two
    // different explanations of one problem in front of the same person.
    return { route, health: { state: 'unreachable', detail: route.detail } };
  }

  if (!svc.key) {
    return {
      route,
      health: {
        state: 'not_configured',
        detail: `No ${spec.credential.toLowerCase()} set for ${svc.name} — ${spec.credentialHint}.`,
      },
    };
  }

  const req = buildProbeRequest(svc);
  let res;
  try {
    res = await tFetch(route.transport, req.url, { method: req.method, headers: req.headers });
  } catch (e) {
    return {
      route,
      health: classifyProbe({
        error: e?.message || 'the request failed',
        type: svc.type, name: svc.name, transport: route.transport,
      }),
    };
  }

  let body = null;
  let bodyIsJSON = true;
  try {
    body = await res.json();
  } catch {
    bodyIsJSON = false;
  }

  return {
    route,
    health: classifyProbe({
      status: res.status, body, bodyIsJSON,
      type: svc.type, name: svc.name, transport: route.transport,
    }),
  };
}

// ----------------------------------------------------------------- advice --

/**
 * The two routes that actually exist, offered once.
 *
 * Returns null when there is nothing honest to say -- which is most of the
 * time. A self-hoster opening their own copy over HTTP is already fine and must
 * not be told to install anything, and someone who has the extension has
 * already taken one of the two routes. Nagging in either case teaches people to
 * stop reading the box that will one day carry the sentence that matters.
 */
export function routeAdvice(services, { hasExtension, pageProtocol } = {}) {
  const blocked = (services || []).filter(
    (s) => s.enabled && s.url && wouldBeMixedContent(s.url, pageProtocol),
  );
  if (hasExtension || blocked.length === 0) return null;

  return {
    count: blocked.length,
    reason:
      blocked.length === 1
        ? `${blocked[0].name} is on a plain-HTTP address, and this page is HTTPS, so the browser `
          + 'blocks the request before it is sent. Two things get past that:'
        : `${blocked.length} of these are on plain-HTTP addresses, and this page is HTTPS, so the `
          + 'browser blocks those requests before they are sent. Two things get past that:',
    routes: [
      {
        title: 'Install the Yarr.It extension',
        href: 'https://github.com/BlizzHacker/yarr-it/releases/latest/download/yarrit-extension-1.0.1.zip',
        detail: 'It relays these calls from outside the page, so your LAN services work here with no server of your own.',
      },
      {
        title: 'Run Yarr.It yourself',
        href: '/selfhost.sh',
        detail: 'Your own copy is served from your own network, so it reaches these addresses directly and nothing is blocked.',
      },
    ],
  };
}

/** A one-line description of what this instance covers, from the schema. */
export function describeService(svc) {
  const spec = SERVICE_TYPES[svc?.type];
  if (!spec) return '';
  const domains = spec.domains.map(labelFor).filter(Boolean).join(' · ');
  return domains ? `${domains} — ${spec.roles.join(', ')}` : spec.roles.join(', ');
}

/** Every health state the schema defines has a label. Guards a silent gap. */
export function healthLabel(state) {
  return HEALTH_LABELS[state] || state;
}

export { HEALTH_STATES };
