/**
 * The rules the LAN relay enforces, with no browser in them.
 *
 * This module is the reason the extension exists. A page on https://yarrit.com
 * cannot reach http://192.168.0.26 -- the browser refuses before the request
 * leaves, and no page-side trick gets around it:
 *
 *   Mixed Content: The page at 'https://yarrit.com/' was loaded over HTTPS, but
 *   requested an insecure resource 'http://192.168.0.26/api/v3/system/status'.
 *   This request has been blocked.
 *
 * An extension service worker is not a document, so that wall does not apply to
 * it. That makes the extension a door into someone's home network, and a door
 * is exactly the thing you have to be careful about. Everything below is the
 * lock: which pages may knock, what they may ask for, and what never crosses.
 *
 * Kept free of `chrome.*` on purpose, so the rules can be tested for real
 * rather than asserted in a README.
 */

/** Pages that may drive the relay without the user adding anything. */
export const BUILTIN_ORIGINS = ['https://yarrit.com', 'https://www.yarrit.com'];

/** Wire protocol, fixed by stream/web/src/transport.js. Do not rename. */
export const RELAY_REQUEST = 'yarrit:relay:request';
export const RELAY_RESPONSE = 'yarrit:relay:response';
/** Content script -> service worker. Internal, so it may change freely. */
export const RELAY_INTERNAL = 'yarrit:relay';

/**
 * A wedged service must not be able to pile up. Six matches what a browser
 * allows per host, and the page gives up after four seconds anyway, so a deeper
 * queue would only produce answers nobody is still listening for.
 */
export const MAX_CONCURRENT = 6;
export const MAX_QUEUE = 32;
/** Longer than the page's own 4s wait, so the page's message is the one seen. */
export const RELAY_TIMEOUT_MS = 10000;
export const MAX_BODY_BYTES = 8 * 1024 * 1024;
export const MAX_HEADERS = 32;

const METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS']);

/**
 * Headers the relay refuses to carry.
 *
 * `cookie` is the whole point: the relay must never become a way to launder a
 * session from one origin to another. fetch() would drop most of these itself,
 * but silently -- and a security rule you cannot see is a security rule nobody
 * maintains. An explicit `Authorization` or `X-Api-Key` is deliberately kept:
 * that is the user's own key for their own service, which is the entire job.
 */
const BLOCKED_HEADERS = new Set([
  'cookie', 'cookie2', 'set-cookie', 'set-cookie2',
  'host', 'origin', 'referer', 'connection', 'upgrade', 'via',
  'keep-alive', 'transfer-encoding', 'te', 'trailer', 'expect',
  'content-length', 'date', 'dnt',
]);
const BLOCKED_PREFIXES = ['sec-', 'proxy-', 'access-control-request-'];

/** Response headers never handed back to the page, for the same reason. */
const STRIPPED_RESPONSE_HEADERS = new Set(['set-cookie', 'set-cookie2']);

const TOKEN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

function safeUrl(raw) {
  if (typeof raw !== 'string' || !raw) return null;
  try {
    return new URL(raw);
  } catch {
    return null;
  }
}

/**
 * A hostname, and specifically not a wildcard.
 *
 * `new URL('https://*')` parses perfectly happily and yields the hostname "*".
 * Without this check, typing `*` into the options page produces the match
 * pattern `https://*&#47;*` -- which Chrome accepts, and which would grant the
 * relay every HTTPS site on the web and inject it into all of them. One
 * character, in a text box, and the whole allowlist is gone.
 */
function isPlainHostname(h) {
  if (typeof h !== 'string' || !h || h.length > 253) return false;
  return /^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*\.?$/i.test(h);
}

/** Reduce anything the user typed to a bare origin, or '' if it is not one. */
export function normaliseOrigin(raw) {
  if (typeof raw !== 'string') return '';
  let s = raw.trim();
  if (!s) return '';
  // Only prepend a scheme when there genuinely is none. "starts with http" is a
  // different and wrong test: "ftp://x" has a scheme, fails it, and would turn
  // into "https://ftp://x" -- a rejected scheme becoming a plausible host.
  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(s);
  if (scheme) {
    const p = scheme[1].toLowerCase();
    if (p !== 'http' && p !== 'https') return '';
  } else {
    s = 'https://' + s;
  }
  const u = safeUrl(s);
  if (!u) return '';
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
  if (!isPlainHostname(u.hostname)) return '';
  return u.origin;
}

/** May this page origin drive the relay? Never `*`, never a suffix match. */
export function isTrustedOrigin(origin, extra = []) {
  const o = typeof origin === 'string' ? origin : '';
  if (!o) return false;
  // Exact string equality, deliberately. `endsWith('yarrit.com')` would also
  // accept https://yarrit.com.evil.test, which is the classic way this goes
  // wrong and would hand a stranger a port scanner pointed at the user's LAN.
  if (BUILTIN_ORIGINS.includes(o)) return true;
  return Array.isArray(extra) && extra.some((e) => normaliseOrigin(e) === o);
}

/**
 * The Chrome match pattern covering a URL's host.
 *
 * Match patterns cannot carry a port -- `http://192.168.0.251:8096/*` is not
 * merely ignored, Chrome rejects the whole call. Which matters here more than
 * most places, because the services this exists for live on ports: Jellyfin on
 * 8096, RomM on 8080, Radarr on 7878. So the grant is per scheme+host and
 * covers every port on it, and the options page has to say so plainly.
 *
 * IPv6 literals have no valid match-pattern spelling at all, so they are
 * refused up front rather than failing later inside chrome.permissions.
 */
export function hostPattern(raw) {
  const u = raw instanceof URL ? raw : safeUrl(raw);
  if (!u) return '';
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
  // Rejects IPv6 literals ("[::1]", which has no match-pattern spelling) and
  // wildcards ("*", which has one and it is catastrophic) in the same test.
  if (!isPlainHostname(u.hostname)) return '';
  return `${u.protocol}//${u.hostname}/*`;
}

/** What to show a human: scheme, host and port, and nothing from the path. */
export function describeTarget(raw) {
  const u = raw instanceof URL ? raw : safeUrl(raw);
  if (!u) return '';
  // Never the path or query. Radarr accepts `?apikey=`, so a full URL in a
  // permission prompt or a stored pending-request is a leaked key on screen.
  return u.origin;
}

function byteLength(s) {
  if (typeof TextEncoder !== 'undefined') return new TextEncoder().encode(s).length;
  return unescape(encodeURIComponent(s)).length;
}

/** Drop everything not on the list, and anything carrying a header injection. */
export function sanitiseHeaders(raw) {
  const out = {};
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return out;
  let n = 0;
  for (const [k, v] of Object.entries(raw)) {
    if (n >= MAX_HEADERS) break;
    const name = String(k).toLowerCase();
    if (!TOKEN.test(name)) continue;
    if (BLOCKED_HEADERS.has(name)) continue;
    if (BLOCKED_PREFIXES.some((p) => name.startsWith(p))) continue;
    if (typeof v !== 'string' && typeof v !== 'number') continue;
    const value = String(v);
    // A CR or LF in a value is how one request becomes two.
    if (/[\r\n\0]/.test(value)) continue;
    out[name] = value;
    n += 1;
  }
  return out;
}

/** Response headers worth handing back, minus anything cookie-shaped. */
export function sanitiseResponseHeaders(entries) {
  const out = {};
  for (const [k, v] of entries) {
    const name = String(k).toLowerCase();
    if (STRIPPED_RESPONSE_HEADERS.has(name)) continue;
    out[name] = String(v);
  }
  return out;
}

/**
 * Turn whatever arrived over postMessage into something safe to hand fetch,
 * or say why not. The message crossed a trust boundary, so nothing in it is
 * assumed: not the shape, not the types, not the method.
 */
export function validateRequest(msg) {
  const fail = (error) => ({ ok: false, error });
  if (!msg || typeof msg !== 'object') return fail('malformed relay request');

  const { id } = msg;
  if (typeof id !== 'string' || !id || id.length > 128) return fail('malformed relay request id');

  const u = safeUrl(msg.url);
  if (!u) return fail('that is not a valid address');
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    // file:, data:, chrome-extension: and friends. A relay that will fetch
    // file:///C:/Users/... is a file-exfiltration tool, not a LAN relay.
    return fail(`the relay only speaks http and https, not ${u.protocol}`);
  }
  if (!hostPattern(u)) {
    // Both are refused, but for opposite reasons, and one message for both sent
    // a wildcard request back saying "IPv6 addresses cannot be granted" -- which
    // would send whoever read it looking in entirely the wrong place.
    return fail(u.hostname.includes(':')
      ? 'IPv6 addresses cannot be granted a Chrome host permission'
      : `"${u.hostname}" is not an address the relay can be granted`);
  }

  const init = msg.init && typeof msg.init === 'object' ? msg.init : {};
  const method = String(init.method || 'GET').toUpperCase();
  if (!METHODS.has(method)) return fail(`the relay does not carry ${method} requests`);

  let body;
  if (init.body !== undefined && init.body !== null && init.body !== '') {
    if (typeof init.body !== 'string') return fail('a relayed body must be text');
    if (byteLength(init.body) > MAX_BODY_BYTES) return fail('that request body is too large to relay');
    if (method === 'GET' || method === 'HEAD') return fail(`a ${method} cannot carry a body`);
    body = init.body;
  }

  return {
    ok: true,
    id,
    url: u.href,
    origin: u.origin,
    pattern: hostPattern(u),
    method,
    headers: sanitiseHeaders(init.headers),
    body,
  };
}

/**
 * Run at most `max` jobs at once, queue the rest, and refuse past `maxQueue`.
 *
 * A home service that has wedged answers nothing rather than answering an
 * error, so without this one dead Radarr quietly consumes every relay slot the
 * extension has and takes the working services down with it.
 */
export function createLimiter({ max = MAX_CONCURRENT, maxQueue = MAX_QUEUE } = {}) {
  let active = 0;
  const queue = [];

  function pump() {
    while (active < max && queue.length) {
      const job = queue.shift();
      active += 1;
      job().then(
        () => { active -= 1; pump(); },
        () => { active -= 1; pump(); },
      );
    }
  }

  return {
    run(fn) {
      return new Promise((resolve, reject) => {
        if (queue.length >= maxQueue) {
          reject(new Error('too many requests already waiting on your services'));
          return;
        }
        queue.push(() => Promise.resolve().then(fn).then(resolve, reject));
        pump();
      });
    },
    get active() { return active; },
    get queued() { return queue.length; },
  };
}

/** Reject rather than hang: a wedged service must not hold a slot forever. */
export function withTimeout(promise, ms, message) {
  let timer;
  return Promise.race([
    promise.finally(() => clearTimeout(timer)),
    new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(message)), ms);
    }),
  ]);
}

/**
 * The message shown when a host has not been granted yet.
 *
 * Written for the person reading it, and it names the next click, because
 * `chrome.permissions.request` needs a user gesture in an extension surface --
 * a web page cannot summon that prompt and neither can the service worker.
 */
export function permissionNeededMessage(target) {
  return (
    `Yarr.It needs your permission before it can reach ${describeTarget(target)}. ` +
    'Click the Yarr.It icon in your toolbar and allow it there.'
  );
}
