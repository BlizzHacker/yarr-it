/**
 * Where this client talks to.
 *
 * Yarr.It cannot work with no server: torrent indexers need API keys and send
 * no CORS headers, catalogue keys must never ship inside a client, and a
 * browser cannot open a TCP socket to a peer. What it can do is stop depending
 * on one *particular* server -- ours becomes a default, not a requirement.
 *
 * Resolution order, most specific first:
 *
 *   1. what the user set, if anything
 *   2. ?server= in the URL, so a link can carry an instance
 *   3. the origin that served this page -- correct for anyone self-hosting,
 *      because their copy is served by their own instance
 *
 * The last rule is what makes self-hosting work with no configuration at all:
 * run the stack, open it, and every call already points at you.
 */

const KEY = 'yarrit_server';


/**
 * A host that can only be plain HTTP in practice.
 *
 * A LAN box has no certificate for 192.168.x, so defaulting it to https gives
 * ERR_CONNECTION_REFUSED and a message about the server being unreachable --
 * when the real problem is that we guessed the wrong scheme. services.js has
 * always got this right; the server box did not, so the same text typed into
 * two boxes in the same panel produced two different answers.
 */
function looksPrivate(hostish) {
  const h = String(hostish).split(':')[0].toLowerCase();
  if (h === 'localhost' || h.endsWith('.local') || h.endsWith('.lan')) return true;
  const m = h.match(/^(\d+)\.(\d+)\.\d+\.\d+$/);
  if (!m) return false;
  const a = Number(m[1]);
  const b = Number(m[2]);
  if (a === 10 || a === 127) return true;
  if (a === 192 && b === 168) return true;
  if (a === 172 && b >= 16 && b <= 31) return true;
  return a === 169 && b === 254;
}

/** Trailing slashes and stray whitespace are the usual paste damage. */
export function normaliseServer(raw) {
  if (!raw) return '';
  let s = String(raw).trim();
  if (!s) return '';

  // host:port has to be settled BEFORE anything treats a colon as a scheme
  // separator, because "localhost:8096" is letters-then-colon and matches a
  // scheme pattern perfectly. That rejected the single most common thing anyone
  // types into this box, along with "nas:8080" and "box.local:5000" -- every
  // hostname with a port that was not a bare IP.
  //
  // Kept narrow on purpose so it cannot readmit the scheme it is standing in
  // front of: the port must be digits to the end, and a scheme like "ftp://x"
  // has slashes after the colon and cannot match.
  if (/^[a-z0-9][a-z0-9.-]*:\d+$/i.test(s)) {
    s = (looksPrivate(s) ? 'http://' : 'https://') + s;
  } else {
    // Only prepend a scheme when there is genuinely none. Testing for "starts
    // with http" is not the same test: "ftp://example.com" has a scheme, fails
    // that check, and becomes "https://ftp://example.com" -- which URL happily
    // parses with the hostname "ftp". A rejected scheme must not turn into a
    // plausible-looking host.
    const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(s);
    if (scheme) {
      const proto = scheme[1].toLowerCase();
      if (proto !== 'http' && proto !== 'https') return '';
    } else {
      // A bare host is the common case: people type "yarrit.com", not a URL.
      s = (looksPrivate(s) ? 'http://' : 'https://') + s;
    }
  }
  try {
    const u = new URL(s);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
    // Path, query and hash are meaningless for an API base and only cause
    // double-slash URLs later.
    return u.origin;
  } catch {
    return '';
  }
}

function fromQuery() {
  try {
    const q = new URLSearchParams(location.search).get('server');
    return normaliseServer(q);
  } catch {
    return '';
  }
}

function stored() {
  try {
    return normaliseServer(localStorage.getItem(KEY));
  } catch {
    // Private windows and some TV webviews throw on any storage access.
    return '';
  }
}

/**
 * The base every request should be built on. Empty string means same-origin,
 * which is deliberately the fallback rather than a hard-coded hostname: a
 * self-hoster's own instance serves their page, so relative URLs are already
 * right and nothing has to be configured.
 */
export function serverBase() {
  const chosen = stored() || fromQuery();
  if (!chosen) return '';
  // Pointing at the origin that served the page is the same as same-origin,
  // and relative URLs avoid a pointless CORS preflight.
  try {
    if (chosen === location.origin) return '';
  } catch { /* no location in tests */ }
  return chosen;
}

export function setServer(raw) {
  const v = normaliseServer(raw);
  try {
    if (v) localStorage.setItem(KEY, v);
    else localStorage.removeItem(KEY);
  } catch { /* storage unavailable; the setting simply will not persist */ }
  return v;
}

export function getServer() {
  return stored();
}

/** Build an absolute URL for an API path. */
export function api(path) {
  const base = serverBase();
  if (!path.startsWith('/')) path = '/' + path;
  return base ? base + path : path;
}

/**
 * The peer relay's WebSocket URL, aimed at the same server as everything else.
 *
 * The scheme has to be derived, never assumed. A hard-coded `wss://` works
 * perfectly on yarrit.com and fails everywhere else that matters: someone
 * running this on their own machine reaches it over plain http, and a browser
 * refuses to open a secure socket to a server with no certificate. That failure
 * is silent in the worst way -- search and playback of direct files carry on
 * working, so only torrents break, and only for self-hosters.
 *
 * Computed on each call rather than once at import: the server setting can
 * change while the page is open.
 */
export function bridgeSocketURL() {
  const base = serverBase();
  if (base) return base.replace(/^http/, 'ws') + '/bridge/socket';
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${scheme}//${location.host}/bridge/socket`;
}

/**
 * fetch, aimed at the configured server.
 *
 * Credentials are only sent same-origin. A session cookie belongs to the
 * instance that issued it, and posting one to somebody else's server because a
 * URL said so would be a genuine leak.
 */
export function apiFetch(path, options = {}) {
  const base = serverBase();
  const url = api(path);
  return fetch(url, {
    credentials: base ? 'omit' : 'same-origin',
    ...options,
  });
}

/**
 * Confirm an address actually speaks this protocol before adopting it.
 * A typo should fail in a settings box, not silently break every search.
 */
export async function probeServer(raw) {
  const base = normaliseServer(raw);
  if (!base) return { ok: false, error: 'That does not look like an address.' };
  try {
    const res = await fetch(base + '/api/health', {
      credentials: 'omit', cache: 'no-store',
    });
    if (!res.ok) return { ok: false, error: `Answered ${res.status}.` };
    const body = await res.json();
    // Any Yarr.It instance answers this shape; a random web server will not.
    if (typeof body !== 'object' || body === null || !('prowlarr' in body)) {
      return { ok: false, error: 'Reachable, but it is not a Yarr.It server.' };
    }
    return { ok: true, info: body };
  } catch (e) {
    return { ok: false, error: 'Could not reach it. ' + (e.message || '') };
  }
}
