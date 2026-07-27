import { TIER } from './source.js';
import { FAILURE } from './failures.js';

/**
 * Pick the cheapest tier that will actually work, given what a probe observed.
 *
 * Two browser rules block most real IPTV and no resolver can code around
 * either, so they have to be detected before playback rather than discovered as
 * a mystery failure:
 *
 *   - Mixed content: an HTTPS page cannot load HTTP media. No override exists.
 *   - CORS: without Access-Control-Allow-Origin the browser will not let the
 *     player read the bytes, even over HTTPS.
 *
 * Tier order is deliberate. Direct is free. The user's own gateway costs the
 * project nothing and is unlimited. The public relay is billed twice per byte
 * and shares a monthly allowance with the mail edge, so it is the last resort.
 */
export function chooseTier({
  pageProtocol,
  streamUrl,
  corsHeader,
  gatewayUrl,
  relayAvailable,
}) {
  const blockedBy = directBlocker({ pageProtocol, streamUrl, corsHeader });
  if (!blockedBy) return { tier: TIER.DIRECT, blockedBy: null };

  if (gatewayUrl) return { tier: TIER.GATEWAY, blockedBy };
  if (relayAvailable) return { tier: TIER.RELAY, blockedBy };
  return { tier: null, blockedBy };
}

/**
 * The user's own gateway, if they have pointed the app at one. Read lazily
 * (never cached at module scope) so a user who sets it mid-session doesn't
 * have to reload, and guarded end to end so a browser that blocks storage
 * access (private mode, some webviews) degrades to "no gateway" instead of
 * throwing out of a probe that has to be safe to call from anywhere.
 */
export function gatewayUrl() {
  try {
    if (typeof localStorage === 'undefined' || !localStorage) return null;
    return localStorage.getItem('yarrit.gateway') || null;
  } catch {
    return null;
  }
}

/** Turn a stream URI into the URL the chosen tier actually plays through. */
export function proxiedUrl(uri, tier, gateway) {
  if (tier === TIER.GATEWAY) return `${gateway}/iptv?u=${encodeURIComponent(uri)}`;
  if (tier === TIER.RELAY) return `/bridge/iptv?u=${encodeURIComponent(uri)}`;
  return uri;
}

/**
 * Probe a stream URL and decide which tier can actually play it.
 *
 * A HEAD request is enough to read the CORS header without pulling the whole
 * body across the wire. A request that fails at the network level (DNS,
 * connection refused, a CORS preflight rejection surfacing as an opaque
 * TypeError) can't be told apart from "no header present" from here, so it
 * is treated the same way chooseTier treats a response with no
 * Access-Control-Allow-Origin header: as blocked, not as proof the stream is
 * dead. Whether that block is actually fatal is chooseTier's call, not this
 * function's - a caller that doesn't care about CORS at all (a <video src>,
 * say) may choose to ignore blockedBy entirely.
 *
 * This must never throw: every branch is wrapped so a caller can always
 * await it and get a tier decision back, never an exception.
 */
export async function probeTier(uri, { fetchImpl = fetch, pageProtocol, gateway, relayAvailable = true } = {}) {
  try {
    const resolvedGateway = gateway !== undefined ? gateway : gatewayUrl();
    const resolvedProtocol = pageProtocol ??
      (typeof location !== 'undefined' ? location.protocol : 'https:');

    let corsHeader = null;
    try {
      const res = await fetchImpl(uri, { method: 'HEAD' });
      corsHeader = res.headers?.get ? res.headers.get('access-control-allow-origin') : null;
    } catch {
      // Network-level failure: no response to read a header from, so treat
      // it exactly like a response that came back without one.
      corsHeader = null;
    }

    const { tier, blockedBy } = chooseTier({
      pageProtocol: resolvedProtocol,
      streamUrl: uri,
      corsHeader,
      gatewayUrl: resolvedGateway,
      relayAvailable,
    });

    return { tier, blockedBy, url: tier ? proxiedUrl(uri, tier, resolvedGateway) : null };
  } catch {
    return { tier: null, blockedBy: FAILURE.CORS_BLOCKED, url: null };
  }
}

function directBlocker({ pageProtocol, streamUrl, corsHeader }) {
  // Mixed content and CORS are rules the browser enforces only on network
  // fetches made over http(s). A blob:, data: or filesystem: URL never
  // triggers a cross-origin request - it resolves to bytes already sitting
  // in this tab's memory - so there is no response header to check and no
  // scheme mismatch to trip over. Treating those schemes as CORS_BLOCKED
  // would send them up the ladder to gateway/relay, but those are network
  // proxies and cannot fetch a URL that only exists in this tab. Do not
  // "simplify" this back to an unconditional corsHeader check.
  const isHttpScheme = /^https?:\/\//i.test(streamUrl);
  if (!isHttpScheme) return null;

  const insecureStream = /^http:\/\//i.test(streamUrl);
  if (pageProtocol === 'https:' && insecureStream) return FAILURE.MIXED_CONTENT;
  if (!corsHeader) return FAILURE.CORS_BLOCKED;
  return null;
}
