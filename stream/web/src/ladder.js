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

function directBlocker({ pageProtocol, streamUrl, corsHeader }) {
  const insecureStream = streamUrl.startsWith('http://');
  if (pageProtocol === 'https:' && insecureStream) return FAILURE.MIXED_CONTENT;
  if (!corsHeader) return FAILURE.CORS_BLOCKED;
  return null;
}
