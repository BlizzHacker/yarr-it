/**
 * Browser-level network protection: the rules, with no chrome.* in them.
 *
 * A page cannot do this. An extension can, and that is the difference that
 * matters: `chrome.proxy` sets the browser's own proxy, and
 * `chrome.privacy.network.webRTCIPHandlingPolicy` closes the hole that would
 * otherwise make the proxy pointless.
 *
 * WHY WEBRTC IS THE WHOLE POINT
 * Torrenting in a browser uses WebRTC, and WebRTC does not go through an HTTP
 * proxy. Set a proxy and nothing else, and every page load is protected while
 * every torrent peer still learns the real address -- which is the exact
 * traffic someone turned this on for. Protection that covers everything except
 * the thing you wanted covered is worse than none, because it feels safe.
 *
 * WHAT THIS IS NOT
 * A SOCKS5 proxy from a VPN provider protects this browser. It does not protect
 * the rest of the machine, and it is not a WireGuard tunnel. Said plainly in the
 * UI rather than implied away.
 */

/** Proxy schemes a browser can actually use. */
export const SCHEMES = ['socks5', 'socks4', 'https', 'http'];

/**
 * WebRTC policies, worst to best for privacy.
 *
 * `disable_non_proxied_udp` is the one that closes the leak: it forbids UDP
 * that does not go through the proxy, so a peer connection cannot quietly take
 * the direct route.
 */
export const WEBRTC_POLICIES = [
  'default',
  'default_public_interface_only',
  'default_public_and_private_interfaces',
  'disable_non_proxied_udp',
];

export const SAFE_WEBRTC_POLICY = 'disable_non_proxied_udp';

/** Parse what a person pastes from their VPN provider's SOCKS page. */
export function parseProxy(raw) {
  const s = String(raw ?? '').trim();
  if (!s) return { ok: false, error: 'Enter your provider\'s proxy address.' };

  let scheme = 'socks5';
  let rest = s;
  const m = /^([a-z0-9]+):\/\/(.*)$/i.exec(s);
  if (m) {
    scheme = m[1].toLowerCase();
    rest = m[2];
    if (!SCHEMES.includes(scheme)) {
      return { ok: false, error: `${scheme} is not a proxy scheme a browser can use.` };
    }
  }
  // Credentials in the URL are rejected rather than silently dropped: Chrome
  // will not apply them from here, so accepting them would look configured and
  // fail with an auth prompt on every request.
  if (rest.includes('@')) {
    return {
      ok: false,
      error: 'Leave the username and password out of the address — enter them below, ' +
             'because a browser cannot take them from the URL.',
    };
  }
  rest = rest.replace(/\/+$/, '');
  const hm = /^([a-z0-9.-]+|\[[0-9a-f:]+\]):(\d{1,5})$/i.exec(rest);
  if (!hm) {
    return { ok: false, error: 'Use host:port, for example proxy-nl.privateinternetaccess.com:1080.' };
  }
  const port = Number(hm[2]);
  if (port < 1 || port > 65535) return { ok: false, error: 'That port number is not valid.' };
  return { ok: true, scheme, host: hm[1], port };
}

/**
 * Build the PAC script.
 *
 * A PAC script rather than `fixedServers` for one reason: the kill switch. A
 * fixed-server config falls back to DIRECT when the proxy is unreachable, which
 * is precisely the silent failure to avoid -- traffic keeps flowing, from the
 * real address, and nothing says so. A PAC that returns only the proxy, with no
 * DIRECT after it, fails closed: if the proxy is down the request fails, which
 * is visible and correct.
 *
 * LAN addresses are exempt, because a proxy in another country cannot reach the
 * Jellyfin in the next room, and routing them through one would break the
 * self-hosted case entirely while protecting nothing -- that traffic never
 * leaves the house.
 */
export function buildPac({ scheme, host, port, killSwitch = true, exemptLan = true }) {
  const kind = scheme === 'https' ? 'HTTPS'
    : scheme === 'http' ? 'PROXY'
      : scheme === 'socks4' ? 'SOCKS'
        : 'SOCKS5';
  const proxy = `${kind} ${host}:${port}`;
  // Without the kill switch, DIRECT is appended as a fallback. Offered because
  // some people would rather stay online than stay covered -- but it is not the
  // default, and the UI says which one is on.
  const chain = killSwitch ? `"${proxy}"` : `"${proxy}; DIRECT"`;

  const lan = exemptLan
    ? `
  if (isPlainHostName(host)
      || shExpMatch(host, "*.local") || shExpMatch(host, "*.lan")
      || isInNet(dnsResolve(host), "10.0.0.0", "255.0.0.0")
      || isInNet(dnsResolve(host), "172.16.0.0", "255.240.0.0")
      || isInNet(dnsResolve(host), "192.168.0.0", "255.255.0.0")
      || isInNet(dnsResolve(host), "127.0.0.0", "255.0.0.0")
      || isInNet(dnsResolve(host), "169.254.0.0", "255.255.0.0")) {
    return "DIRECT";
  }`
    : '';

  return `function FindProxyForURL(url, host) {${lan}
  return ${chain};
}`;
}

/**
 * What the user is actually protected against, stated without flattery.
 *
 * Returned as data so the UI cannot quietly drift from the truth, and so a
 * half-configured state is describable rather than being shown as "on".
 */
export function protectionSummary({ enabled, killSwitch, webrtcPolicy, exemptLan }) {
  if (!enabled) {
    return {
      level: 'off',
      headline: 'Not protected',
      points: ['This browser reaches the internet from your own address.'],
    };
  }
  const points = [
    'This browser\'s traffic goes through your proxy.',
    exemptLan
      ? 'Your own network is exempt, so the servers in your house still work.'
      : 'Your own network also goes through the proxy, which usually breaks local servers.',
  ];
  const leaking = webrtcPolicy !== SAFE_WEBRTC_POLICY;
  points.push(leaking
    ? 'WARNING: WebRTC can still use your real address, and torrent peers use WebRTC. ' +
      'Most of what you turned this on for is unprotected.'
    : 'WebRTC cannot go around the proxy, so torrent peers see the proxy and not you.');
  points.push(killSwitch
    ? 'If the proxy stops answering, requests fail rather than falling back to your own address.'
    : 'WARNING: if the proxy stops answering, traffic silently continues from your own address.');
  points.push('This covers this browser only, not the rest of this machine.');

  return {
    level: leaking || !killSwitch ? 'partial' : 'on',
    headline: leaking ? 'Protected, but leaking' : killSwitch ? 'Protected' : 'Protected, no kill switch',
    points,
  };
}
