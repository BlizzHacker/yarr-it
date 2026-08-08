import test from 'node:test';
import assert from 'node:assert/strict';

import {
  parseProxy, buildPac, protectionSummary, SAFE_WEBRTC_POLICY,
} from './vpn-core.js';

test('a proxy address is read the way people paste it', () => {
  const a = parseProxy('proxy-nl.privateinternetaccess.com:1080');
  assert.equal(a.ok, true);
  assert.equal(a.scheme, 'socks5', 'a bare host:port should default to SOCKS5');
  assert.equal(a.port, 1080);

  assert.equal(parseProxy('socks5://10.0.0.5:1080').scheme, 'socks5');
  assert.equal(parseProxy('https://secure.example:443').scheme, 'https');
  assert.equal(parseProxy('  10.0.0.5:1080  ').host, '10.0.0.5');
});

test('credentials in the address are refused, not silently dropped', () => {
  // Chrome will not take them from the URL. Accepting the string would look
  // configured and then fail with an auth prompt on every single request.
  const r = parseProxy('socks5://user:pass@proxy.example:1080');
  assert.equal(r.ok, false);
  assert.match(r.error, /username and password/i);
});

test('nonsense is refused with something actionable', () => {
  for (const bad of ['', '   ', 'proxy.example', 'wireguard://x:1', 'host:99999']) {
    const r = parseProxy(bad);
    assert.equal(r.ok, false, `${bad} should be refused`);
    assert.ok(r.error && r.error.length > 10, `${bad} needs a useful message`);
  }
});

test('the kill switch means no DIRECT fallback anywhere in the PAC', () => {
  const pac = buildPac({ scheme: 'socks5', host: 'p.example', port: 1080, killSwitch: true, exemptLan: false });
  assert.match(pac, /SOCKS5 p\.example:1080/);
  // This is the whole kill switch: a DIRECT fallback is what makes a dead
  // proxy silently keep working from the real address.
  assert.doesNotMatch(pac, /DIRECT/, 'a DIRECT fallback defeats the kill switch');
});

test('without the kill switch a fallback exists, and it is not the default', () => {
  const off = buildPac({ scheme: 'socks5', host: 'p.example', port: 1080, killSwitch: false, exemptLan: false });
  assert.match(off, /DIRECT/);
  // Default must be the safe one.
  const dflt = buildPac({ scheme: 'socks5', host: 'p.example', port: 1080 });
  assert.doesNotMatch(dflt.replace(/return "DIRECT";/g, ''), /DIRECT/);
});

test('the local network is exempt, or self-hosting breaks and nothing is gained', () => {
  const pac = buildPac({ scheme: 'socks5', host: 'p.example', port: 1080 });
  // A proxy in another country cannot reach the Jellyfin in the next room, and
  // that traffic never leaves the house anyway.
  for (const net of ['192.168.0.0', '10.0.0.0', '172.16.0.0', '127.0.0.0']) {
    assert.ok(pac.includes(net), `${net} should be exempt`);
  }
  assert.match(pac, /isPlainHostName/);
  assert.match(pac, /\*\.local/);
});

test('a proxy with WebRTC left open is reported as leaking, not as protected', () => {
  // The failure this whole feature exists to prevent: page loads covered,
  // torrent peers still seeing the real address.
  const s = protectionSummary({
    enabled: true, killSwitch: true, exemptLan: true, webrtcPolicy: 'default',
  });
  assert.equal(s.level, 'partial');
  assert.match(s.headline, /leaking/i);
  assert.ok(s.points.some((p) => /WebRTC/.test(p) && /real address/.test(p)));
});

test('fully configured reports protected, and still says what it does not cover', () => {
  const s = protectionSummary({
    enabled: true, killSwitch: true, exemptLan: true, webrtcPolicy: SAFE_WEBRTC_POLICY,
  });
  assert.equal(s.level, 'on');
  assert.equal(s.headline, 'Protected');
  // Never claim more than the truth: this is one browser, not the machine.
  assert.ok(s.points.some((p) => /this browser only/i.test(p)));
});

test('no kill switch is surfaced as a warning rather than hidden', () => {
  const s = protectionSummary({
    enabled: true, killSwitch: false, exemptLan: true, webrtcPolicy: SAFE_WEBRTC_POLICY,
  });
  assert.equal(s.level, 'partial');
  assert.ok(s.points.some((p) => /silently continues/i.test(p)));
});

test('off says so plainly', () => {
  const s = protectionSummary({ enabled: false });
  assert.equal(s.level, 'off');
  assert.equal(s.headline, 'Not protected');
});

// ----------------------------------------------- the PAC, actually running --
//
// The tests above read the PAC as text, which proves it was written and not that
// it routes. These run it the way Chrome does, against the same four helpers
// Chrome provides, and ask where a given host is actually sent. A PAC that
// mentions 192.168.0.0 and still proxies 192.168.0.251 would pass every test
// above and break every self-hosted service in the house.

function ip2int(ip) {
  return ip.split('.').reduce((a, o) => ((a << 8) >>> 0) + (Number(o) & 255), 0) >>> 0;
}

function runPac(pac, host) {
  const helpers = {
    isPlainHostName: (h) => !h.includes('.'),
    dnsResolve: (h) => (/^\d+\.\d+\.\d+\.\d+$/.test(h)
      ? h
      : ({ 'nas.local': '192.168.0.9', 'jellyfin.lan': '192.168.0.251' }[h] || '203.0.113.9')),
    isInNet: (ip, net, mask) => (ip2int(ip) & ip2int(mask)) === (ip2int(net) & ip2int(mask)),
    shExpMatch: (str, pat) => new RegExp(
      `^${pat.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.')}$`,
    ).test(str),
  };
  const make = new Function(...Object.keys(helpers), `${pac}\nreturn FindProxyForURL;`);
  return make(...Object.values(helpers))(`http://${host}/`, host);
}

test('running the PAC sends the internet to the proxy and nowhere else', () => {
  const pac = buildPac({ scheme: 'socks5', host: 'p.example', port: 1080 });
  assert.equal(runPac(pac, 'api.ipify.org'), 'SOCKS5 p.example:1080');
  // No trailing DIRECT: this is the kill switch, measured rather than grepped.
  assert.doesNotMatch(runPac(pac, 'api.ipify.org'), /DIRECT/);
});

test('running the PAC leaves the house alone', () => {
  const pac = buildPac({ scheme: 'socks5', host: 'p.example', port: 1080 });
  // 192.168.0.251:8096 is the case this exists for: a Jellyfin one room away
  // cannot be reached through a proxy in another country.
  for (const h of ['192.168.0.251', '10.0.0.5', '172.16.4.4', '127.0.0.1', '169.254.1.1',
    'nas.local', 'jellyfin.lan', 'router']) {
    assert.equal(runPac(pac, h), 'DIRECT', `${h} should go direct`);
  }
});

test('turning the LAN exemption off really does route the LAN through the proxy', () => {
  const pac = buildPac({ scheme: 'socks5', host: 'p.example', port: 1080, exemptLan: false });
  assert.equal(runPac(pac, '192.168.0.251'), 'SOCKS5 p.example:1080');
});

test('every scheme maps to the PAC keyword Chrome expects', () => {
  const at = (scheme) => runPac(
    buildPac({ scheme, host: 'p.example', port: 8080, exemptLan: false }), 'example.org',
  );
  assert.equal(at('socks5'), 'SOCKS5 p.example:8080');
  assert.equal(at('socks4'), 'SOCKS p.example:8080');   // Chrome's SOCKS means SOCKS4
  assert.equal(at('http'), 'PROXY p.example:8080');
  assert.equal(at('https'), 'HTTPS p.example:8080');
});

test('without the kill switch the running PAC offers the fallback second', () => {
  const pac = buildPac({ scheme: 'socks5', host: 'p.example', port: 1080, killSwitch: false, exemptLan: false });
  assert.equal(runPac(pac, 'example.org'), 'SOCKS5 p.example:1080; DIRECT');
});

// ------------------------------------------- the credential rules, stubbed --
//
// decideAuth lives in vpn-bg.js because it needs chrome.*, but what it decides
// is a security rule and belongs under test. chrome is stubbed rather than
// mocked deeply: all four refusals are reachable with storage and nothing else.

const store = {};
globalThis.chrome = {
  runtime: { id: 'test-id', onMessage: { addListener() {} } },
  permissions: { contains: async () => false, onAdded: { addListener() {} } },
  storage: {
    local: {
      async get(req) {
        const out = {};
        for (const [k, d] of Object.entries(req)) out[k] = k in store ? store[k] : d;
        return out;
      },
      async set(obj) { Object.assign(store, obj); },
      async remove(k) { delete store[k]; },
    },
  },
};
const { decideAuth } = await import('./vpn-bg.js');

const CONFIGURED = {
  enabled: true, scheme: 'https', host: 'proxy.example', port: 8443,
  killSwitch: true, exemptLan: true,
};
function ready() {
  store.vpnConfig = { ...CONFIGURED };
  store.vpnCreds = { username: 'wade', password: 'hunter2' };
}
const challenge = (over = {}) => ({
  isProxy: true, requestId: `r${Math.random()}`, challenger: { host: 'proxy.example', port: 8443 }, ...over,
});

test('the proxy password is given to the proxy that was configured', async () => {
  ready();
  const r = await decideAuth(challenge());
  assert.deepEqual(r.authCredentials, { username: 'wade', password: 'hunter2' });
});

test('a website asking for a password never sees the proxy credentials', async () => {
  ready();
  // isProxy false is a site's own 401. Answering it would hand a VPN password
  // to any page that pops an auth box.
  assert.deepEqual(await decideAuth(challenge({ isProxy: false })), {});
});

test('a different proxy gets nothing, even while protection is on', async () => {
  ready();
  assert.deepEqual(await decideAuth(challenge({ challenger: { host: 'evil.example', port: 8443 } })), {});
  assert.deepEqual(await decideAuth(challenge({ challenger: { host: 'proxy.example', port: 3128 } })), {});
});

test('nothing is offered while protection is off', async () => {
  ready();
  store.vpnConfig = { ...CONFIGURED, enabled: false };
  assert.deepEqual(await decideAuth(challenge()), {});
});

test('a wrong password fails once instead of retrying forever', async () => {
  ready();
  const c = challenge();
  assert.ok((await decideAuth(c)).authCredentials, 'first attempt answers');
  // Chrome re-fires onAuthRequired for the same requestId when the credentials
  // are rejected. Answering again is an infinite loop that looks exactly like
  // the proxy being down.
  assert.deepEqual(await decideAuth(c), {});
});
