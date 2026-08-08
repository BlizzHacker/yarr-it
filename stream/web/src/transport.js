/**
 * How a client reaches somebody's own services.
 *
 * This exists because of a browser rule that cannot be coded around, and the
 * whole design follows from it. Measured on the live site:
 *
 *   Mixed Content: The page at 'https://yarrit.com/' was loaded over HTTPS, but
 *   requested an insecure resource 'http://192.168.0.26/...'. This request has
 *   been blocked.
 *
 * Even `mode: 'no-cors'` fails — the request never leaves the browser. So a
 * page served over HTTPS cannot talk to a plain-HTTP box on a home network, in
 * any browser, ever. CORS is a second wall behind that one.
 *
 * Three transports genuinely work, and the right one depends on where the page
 * came from and what the viewer has:
 *
 *   DIRECT     the service is already reachable — same origin, or public HTTPS,
 *              or the page itself is served over HTTP on the same LAN. This is
 *              what a self-hoster gets for free.
 *   EXTENSION  the browser extension relays. It holds optional host permissions
 *              for any origin and is not subject to page CORS, so a guest on
 *              yarrit.com can reach their own LAN with no server at all.
 *   SERVER     the viewer's own Yarr.It instance proxies, holding the keys.
 *
 * The point of naming them is honesty: when none apply, the UI must say which
 * wall it hit and what would clear it, rather than showing an empty library and
 * letting someone conclude their setup is broken.
 */

export const TRANSPORT = {
  DIRECT: 'direct',
  EXTENSION: 'extension',
  SERVER: 'server',
  NONE: 'none',
};

/** Reasons a service is unreachable, each with a different fix. */
export const BLOCKED = {
  MIXED_CONTENT: 'mixed-content',
  CORS: 'cors',
  UNREACHABLE: 'unreachable',
};

const EXT_TIMEOUT_MS = 4000;
const PROBE_TIMEOUT_MS = 6000;

/** A private address, i.e. one no hosted page can reach over plain HTTP. */
export function isPrivateHost(urlStr) {
  let h;
  try {
    h = new URL(urlStr).hostname;
  } catch {
    return false;
  }
  if (h === 'localhost' || h.endsWith('.local') || h.endsWith('.lan')) return true;
  const m = h.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  if (a === 10 || a === 127) return true;
  if (a === 192 && b === 168) return true;
  if (a === 172 && b >= 16 && b <= 31) return true;
  // 169.254.x is link-local; treated as private for the same reason.
  return a === 169 && b === 254;
}

/**
 * Would the browser refuse this request before it left?
 *
 * Checked ahead of trying, because the resulting TypeError is indistinguishable
 * from "the server is down" — and telling someone their Radarr is offline when
 * the browser simply refused to ask is the wrong answer twice over.
 */
export function wouldBeMixedContent(target, pageProtocol = location.protocol) {
  if (pageProtocol !== 'https:') return false;
  try {
    return new URL(target).protocol === 'http:';
  } catch {
    return false;
  }
}

/** Is the Yarr.It extension present and able to relay? */
export function extensionAvailable() {
  return typeof window !== 'undefined' && window.__yarritExtension === true;
}

/**
 * Ask the extension to make a request on the page's behalf.
 *
 * The extension answers on `window` via postMessage rather than through
 * chrome.runtime, because a page cannot call chrome.runtime and we do not want
 * the extension id baked into the site.
 */
export function extensionFetch(url, options = {}, timeoutMs = EXT_TIMEOUT_MS) {
  return new Promise((resolve, reject) => {
    const id = `yf_${Date.now()}_${Math.random().toString(36).slice(2)}`;
    const timer = setTimeout(() => {
      window.removeEventListener('message', onMessage);
      reject(new Error('the extension did not answer'));
    }, timeoutMs);

    function onMessage(ev) {
      if (ev.source !== window) return;
      const d = ev.data;
      if (!d || d.type !== 'yarrit:relay:response' || d.id !== id) return;
      clearTimeout(timer);
      window.removeEventListener('message', onMessage);
      if (d.error) {
        reject(new Error(d.error));
        return;
      }
      resolve({
        ok: d.status >= 200 && d.status < 300,
        status: d.status,
        headers: d.headers || {},
        json: async () => JSON.parse(d.body),
        text: async () => d.body,
      });
    }

    window.addEventListener('message', onMessage);
    window.postMessage({
      type: 'yarrit:relay:request',
      id,
      url,
      // Only what a relay needs. Deliberately not forwarding credentials: the
      // extension must never be a way to launder a cookie to another origin.
      init: {
        method: options.method || 'GET',
        headers: options.headers || {},
        body: options.body,
      },
    }, window.location.origin);
  });
}

/**
 * Which transport can reach this service, and if none, why not.
 *
 * Returns { transport, blocked, detail }. `detail` is written for the person
 * reading a settings screen, so it says what to do rather than what failed.
 */
export async function chooseTransport(target, opts = {}) {
  const {
    pageProtocol = location.protocol,
    hasExtension = extensionAvailable(),
    fetchImpl = fetch,
    probePath = '/',
  } = opts;

  const mixed = wouldBeMixedContent(target, pageProtocol);

  // The extension is checked first when the page could not ask directly. It is
  // not a fallback for a broken service -- it is the only door that is open.
  if (mixed) {
    if (hasExtension) {
      return { transport: TRANSPORT.EXTENSION, detail: 'reached through the Yarr.It extension' };
    }
    return {
      transport: TRANSPORT.NONE,
      blocked: BLOCKED.MIXED_CONTENT,
      detail:
        'This page is served over HTTPS and that address is plain HTTP, so the ' +
        'browser blocks the request before it is sent. Install the Yarr.It ' +
        'extension, run Yarr.It on your own machine, or serve this service over HTTPS.',
    };
  }

  try {
    const ctl = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const t = ctl ? setTimeout(() => ctl.abort(), PROBE_TIMEOUT_MS) : null;
    const res = await fetchImpl(target.replace(/\/+$/, '') + probePath, {
      mode: 'cors',
      credentials: 'omit',
      signal: ctl ? ctl.signal : undefined,
    });
    if (t) clearTimeout(t);
    // Any answered status proves the round trip. 401 in particular means the
    // service is there and simply wants a key, which is a configuration step
    // rather than a reachability problem.
    if (res.status > 0) {
      return { transport: TRANSPORT.DIRECT, detail: 'reachable directly' };
    }
  } catch {
    if (hasExtension) {
      return { transport: TRANSPORT.EXTENSION, detail: 'reached through the Yarr.It extension' };
    }
  }

  if (hasExtension) {
    return { transport: TRANSPORT.EXTENSION, detail: 'reached through the Yarr.It extension' };
  }
  return {
    transport: TRANSPORT.NONE,
    blocked: BLOCKED.CORS,
    detail:
      'That address did not answer this page. It may be offline, or it may be ' +
      'refusing requests from another site — most self-hosted services do. The ' +
      'Yarr.It extension, or running Yarr.It on your own machine, gets past that.',
  };
}

/** fetch through whichever transport was chosen. */
export function transportFetch(transport, url, options = {}) {
  if (transport === TRANSPORT.EXTENSION) return extensionFetch(url, options);
  return fetch(url, { credentials: 'omit', ...options });
}
