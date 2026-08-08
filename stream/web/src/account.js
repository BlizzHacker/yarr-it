/**
 * Signing in, and the network-protection panel.
 *
 * Both existed as working machinery with no way to reach them. `/auth/login`
 * has always redirected to Authentik correctly and `/auth/me` has always
 * reported who you are, but no control in the page ever called either -- so the
 * honest answer to "how do I sign in?" was "you cannot", despite the server
 * being ready the whole time.
 *
 * Signing in is never required to use your own services. It exists for a shelf
 * that follows you between devices, and for pairing a television. The service
 * configuration deliberately lives in localStorage and works signed out.
 */

import { api } from './server.js';

/** Who is signed in, or null. Never throws: this paints a header. */
export async function whoAmI(fetchImpl = fetch) {
  try {
    const res = await fetchImpl(api('/auth/me'), {
      credentials: 'same-origin',
      cache: 'no-store',
    });
    // 401 is the normal state for a visitor, not an error to report.
    if (res.status === 401 || res.status === 503) return null;
    if (!res.ok) return null;
    const d = await res.json();
    return d && (d.user || d.email || d.name) ? d : null;
  } catch {
    return null;
  }
}

/** A label for whoever is signed in, preferring the most human field. */
export function displayName(me) {
  if (!me) return '';
  return me.name || me.preferred_username || me.user || me.email || 'Signed in';
}

/**
 * Send the browser to sign in, returning to where it started.
 *
 * The current URL is carried so a visitor who signs in from halfway down a
 * search does not land back on an empty home page.
 */
export function signInURL(here = location.pathname + location.search + location.hash) {
  const u = new URL(api('/auth/login'), location.origin);
  u.searchParams.set('next', here);
  return u.toString();
}

export function signOutURL() {
  return new URL(api('/auth/logout'), location.origin).toString();
}

/**
 * What protecting this traffic actually means, given where the page is running.
 *
 * The honest part: a web page cannot configure a VPN. It has no access to the
 * machine's routing table, and any site that claims otherwise is describing
 * something else. What a VPN protects here is the SERVER's outbound traffic --
 * the box that talks to trackers and peers on your behalf -- and that is
 * configured where that box lives.
 *
 * So this returns instructions, plus whatever the server is willing to report
 * about its own egress. Anything more would be theatre.
 */
export function vpnGuidance(serverBase) {
  const own = !serverBase; // same-origin means the viewer's own instance
  return {
    canConfigureFromBrowser: false,
    heading: 'Network protection',
    body:
      'A web page cannot set up a VPN — it has no access to your machine\'s ' +
      'routing. What a VPN protects here is the outbound traffic of the server ' +
      'that fetches on your behalf, so it is set up on that server.',
    command: 'sudo stream/deploy/vpn-killswitch.sh <your-wireguard-config-name>',
    detail:
      'That seals the ordinary network interface, so if the tunnel drops the ' +
      'traffic stops instead of quietly falling back to your own address. Your ' +
      'LAN stays exempt, because the devices in your house were never meant to ' +
      'go through the tunnel.',
    appliesTo: own
      ? 'This applies to the machine serving this page.'
      : 'This applies to the server you have configured, not to this browser.',
  };
}

/** Ask the server what the outside world sees when it fetches. */
export async function egressStatus(fetchImpl = fetch) {
  try {
    const res = await fetchImpl(api('/api/health'), { cache: 'no-store' });
    if (!res.ok) return null;
    const d = await res.json();
    if (!d || typeof d.egress === 'undefined') return null;
    return d.egress;
  } catch {
    return null;
  }
}
