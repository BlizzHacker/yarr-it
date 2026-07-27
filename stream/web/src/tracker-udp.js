// UDP tracker client (BEP 15) spoken from a browser, over mw-bridge.
//
// This is the piece that makes real swarms reachable. A browser cannot send
// UDP, so it cannot ask a normal tracker for peers -- which is why adding a
// well-seeded torrent to a plain WebTorrent client yields zero peers even
// though 50 seeds are sitting right there. The relay carries the datagrams;
// this module speaks the protocol.
//
// Exchange is two round trips:
//   connect:  magic 0x41727101980, action=0  -> connection_id
//   announce: connection_id, action=1, ...   -> compact peer list (BEP 23)

const PROTOCOL_MAGIC = 0x41727101980n;
const ACTION_CONNECT = 0;
const ACTION_ANNOUNCE = 1;
const ACTION_ERROR = 3;

const bridgeURL = () => `wss://${location.host}/bridge/socket`;

function randomBytes(n) {
  const b = new Uint8Array(n);
  crypto.getRandomValues(b);
  return b;
}

function hexToBytes(hex) {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.substr(i * 2, 2), 16);
  return out;
}

/** Parse `udp://host:port/announce` into its parts. */
export function parseUdpTracker(url) {
  const m = /^udp:\/\/([^:/]+):(\d+)/i.exec(url);
  if (!m) return null;
  return { host: m[1], port: Number(m[2]) };
}

/** Resolve a tracker hostname to an IP -- the bridge refuses names by policy. */
async function resolveHost(host) {
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) return host;
  try {
    const r = await fetch(`https://dns.google/resolve?name=${encodeURIComponent(host)}&type=A`);
    const d = await r.json();
    const a = (d.Answer || []).find((x) => x.type === 1);
    return a?.data || null;
  } catch {
    return null;
  }
}

/**
 * Announce to one UDP tracker and return its peer list.
 * @returns {Promise<{host:string,port:number}[]>}
 */
export async function announceUdp(trackerUrl, infoHashHex, { numWant = 80, timeoutMs = 9000 } = {}) {
  const parsed = parseUdpTracker(trackerUrl);
  if (!parsed) return [];
  const ip = await resolveHost(parsed.host);
  if (!ip) return [];

  return new Promise((resolve) => {
    const ws = new WebSocket(bridgeURL());
    ws.binaryType = 'arraybuffer';
    let settled = false;
    let connectionId = null;
    const transactionId = randomBytes(4);

    const finish = (peers) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try {
        ws.close();
      } catch {
        /* already closing */
      }
      resolve(peers);
    };
    const timer = setTimeout(() => finish([]), timeoutMs);

    ws.onopen = () => ws.send(JSON.stringify({ proto: 'udp', host: ip, port: parsed.port }));

    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        try {
          if (JSON.parse(ev.data).ok) ws.send(buildConnect(transactionId));
        } catch {
          finish([]);
        }
        return;
      }

      // Frames come back length-prefixed (2 bytes) by the relay.
      const raw = new Uint8Array(ev.data);
      if (raw.length < 2) return;
      const len = (raw[0] << 8) | raw[1];
      const msg = raw.subarray(2, 2 + len);
      if (msg.length < 8) return;

      const view = new DataView(msg.buffer, msg.byteOffset, msg.byteLength);
      const action = view.getUint32(0, false);

      if (action === ACTION_ERROR) {
        finish([]);
        return;
      }
      if (action === ACTION_CONNECT && msg.length >= 16) {
        connectionId = msg.slice(8, 16);
        ws.send(buildAnnounce(connectionId, transactionId, infoHashHex, numWant));
        return;
      }
      if (action === ACTION_ANNOUNCE && msg.length >= 20) {
        finish(decodeCompactPeers(msg.subarray(20)));
      }
    };

    ws.onerror = () => finish([]);
    ws.onclose = () => finish([]);
  });
}

function buildConnect(transactionId) {
  const buf = new Uint8Array(16);
  const view = new DataView(buf.buffer);
  view.setBigUint64(0, PROTOCOL_MAGIC, false);
  view.setUint32(8, ACTION_CONNECT, false);
  buf.set(transactionId, 12);
  return buf;
}

function buildAnnounce(connectionId, transactionId, infoHashHex, numWant) {
  const buf = new Uint8Array(98);
  const view = new DataView(buf.buffer);
  buf.set(connectionId, 0);
  view.setUint32(8, ACTION_ANNOUNCE, false);
  buf.set(transactionId, 12);
  buf.set(hexToBytes(infoHashHex), 16); // info_hash (20)
  buf.set(peerId(), 36); // peer_id  (20)
  view.setBigUint64(56, 0n, false); // downloaded
  view.setBigUint64(64, 0n, false); // left  (0 = we want peers regardless)
  view.setBigUint64(72, 0n, false); // uploaded
  view.setUint32(80, 2, false); // event: started
  view.setUint32(84, 0, false); // ip: default
  view.setUint32(88, Math.floor(Math.random() * 0xffffffff), false); // key
  view.setInt32(92, numWant, false);
  view.setUint16(96, 6881, false); // port (advisory; we cannot listen)
  return buf;
}

let _peerId = null;
function peerId() {
  if (!_peerId) {
    // Azureus-style client id. -MW0001- keeps us identifiable and honest.
    const prefix = new TextEncoder().encode('-MW0001-');
    _peerId = new Uint8Array(20);
    _peerId.set(prefix, 0);
    _peerId.set(randomBytes(12), 8);
  }
  return _peerId;
}

/** BEP 23 compact peers: 6 bytes each, 4 for IPv4 + 2 for port, big-endian. */
export function decodeCompactPeers(bytes) {
  const out = [];
  for (let i = 0; i + 6 <= bytes.length; i += 6) {
    const host = `${bytes[i]}.${bytes[i + 1]}.${bytes[i + 2]}.${bytes[i + 3]}`;
    const port = (bytes[i + 4] << 8) | bytes[i + 5];
    if (port > 0) out.push({ host, port });
  }
  return out;
}

/** Pull every udp:// tracker out of a magnet link. */
export function trackersFromMagnet(magnet) {
  const out = [];
  const re = /[?&]tr=([^&]+)/g;
  let m;
  while ((m = re.exec(magnet)) !== null) {
    const url = decodeURIComponent(m[1]);
    if (url.startsWith('udp://')) out.push(url);
  }
  return out;
}

export function infoHashFromMagnet(magnet) {
  const m = /xt=urn:btih:([a-fA-F0-9]{40})/.exec(magnet);
  return m ? m[1].toLowerCase() : null;
}
