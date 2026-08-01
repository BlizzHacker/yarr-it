// DHT peer discovery (BEP 5) spoken from a browser, over the relay's UDP path.
//
// This is the source of last resort, and often the only one that works. Trackers
// die constantly — Tears of Steel's magnet leads with three dead hosts — but the
// DHT has no operator to abandon it. Any torrent with live peers is findable
// through it even when every tracker in the magnet is gone.
//
// A browser cannot send UDP, so each query goes through the same relay that
// carries tracker announces. The lookup is deliberately shallow: query the
// bootstrap routers, then one round against whatever nodes they name. That is
// usually enough to reach a node holding peers for a popular infohash, and it
// avoids maintaining a routing table the tab would throw away anyway.

import { encode, decode } from './bencode.js';
import { bridgeSocketURL } from './server.js';

const BOOTSTRAP = [
  { host: 'router.bittorrent.com', port: 6881 },
  { host: 'dht.transmissionbt.com', port: 6881 },
  { host: 'router.utorrent.com', port: 6881 },
];

const QUERY_TIMEOUT = 6000;
// Bootstrap routers mostly rate-limit and stay silent, and each round only
// halves the distance, so a shallow search finds nothing. Four rounds against
// the closest nodes reliably reaches one holding peers.
const MAX_ROUNDS = 6;
const MAX_NODES_PER_ROUND = 16;

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

/** Our node id for this tab. Random is fine; we never serve queries. */
let _nodeId = null;
function nodeId() {
  if (!_nodeId) _nodeId = randomBytes(20);
  return _nodeId;
}

/** The relay refuses hostnames, so resolve over DoH first. */
const dnsCache = new Map();
async function resolveHost(host) {
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) return host;
  if (dnsCache.has(host)) return dnsCache.get(host);
  try {
    const r = await fetch(`https://dns.google/resolve?name=${encodeURIComponent(host)}&type=A`);
    const d = await r.json();
    const ip = (d.Answer || []).find((x) => x.type === 1)?.data || null;
    dnsCache.set(host, ip);
    return ip;
  } catch {
    return null;
  }
}

/**
 * Send one get_peers query and return whatever the node replies with.
 * @returns {Promise<{peers:{host:string,port:number}[], nodes:{host:string,port:number}[]}>}
 */
async function getPeers(node, infoHashHex) {
  const ip = await resolveHost(node.host);
  if (!ip) return { peers: [], nodes: [] };

  const query = encode({
    t: randomBytes(2),
    y: 'q',
    q: 'get_peers',
    a: { id: nodeId(), info_hash: hexToBytes(infoHashHex) },
  });

  return new Promise((resolve) => {
    const ws = new WebSocket(bridgeSocketURL());
    ws.binaryType = 'arraybuffer';
    let settled = false;

    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try {
        ws.close();
      } catch {
        /* already closing */
      }
      resolve(result);
    };
    const timer = setTimeout(() => finish({ peers: [], nodes: [] }), QUERY_TIMEOUT);

    ws.onopen = () => ws.send(JSON.stringify({ proto: 'udp', host: ip, port: node.port }));

    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        try {
          if (JSON.parse(ev.data).ok) ws.send(query);
        } catch {
          finish({ peers: [], nodes: [] });
        }
        return;
      }
      // The relay length-prefixes each datagram so boundaries survive the
      // stream-shaped WebSocket.
      const raw = new Uint8Array(ev.data);
      if (raw.length < 2) return;
      const len = (raw[0] << 8) | raw[1];
      try {
        finish(parseResponse(decode(raw.subarray(2, 2 + len))));
      } catch {
        finish({ peers: [], nodes: [] });
      }
    };

    ws.onerror = () => finish({ peers: [], nodes: [] });
    ws.onclose = () => finish({ peers: [], nodes: [] });
  });
}

function parseResponse(msg) {
  const out = { peers: [], nodes: [] };
  const r = msg?.r;
  if (!r) return out;

  // `values` is a list of 6-byte compact peers -- an actual hit.
  if (Array.isArray(r.values)) {
    for (const v of r.values) {
      if (v instanceof Uint8Array && v.length >= 6) {
        out.peers.push({
          host: `${v[0]}.${v[1]}.${v[2]}.${v[3]}`,
          port: (v[4] << 8) | v[5],
        });
      }
    }
  }

  // `nodes` is 26 bytes each: 20-byte id + 6-byte compact address. The id is
  // kept, not discarded: without it the lookup cannot tell which nodes are
  // closer to the infohash and so never converges on one holding peers.
  if (r.nodes instanceof Uint8Array) {
    for (let i = 0; i + 26 <= r.nodes.length; i += 26) {
      const id = r.nodes.subarray(i, i + 20);
      const a = r.nodes.subarray(i + 20, i + 26);
      const port = (a[4] << 8) | a[5];
      if (port > 0) out.nodes.push({ id, host: `${a[0]}.${a[1]}.${a[2]}.${a[3]}`, port });
    }
  }
  return out;
}

/**
 * Kademlia distance: how close a node id is to the target infohash.
 * Returned as a comparable array of bytes, most significant first.
 */
function xorDistance(id, target) {
  const out = new Uint8Array(20);
  for (let i = 0; i < 20; i++) out[i] = (id?.[i] ?? 0xff) ^ target[i];
  return out;
}

function compareDistance(a, b) {
  for (let i = 0; i < 20; i++) {
    if (a[i] !== b[i]) return a[i] - b[i];
  }
  return 0;
}

/**
 * Look up peers for an infohash in the DHT.
 *
 * @param {string} infoHashHex
 * @param {(peers:{host:string,port:number}[]) => void} [onPeers] called as
 *        results arrive, so dialling can start before the lookup finishes.
 * @returns {Promise<{host:string,port:number}[]>}
 */
export async function dhtFindPeers(infoHashHex, onPeers) {
  const target = hexToBytes(infoHashHex);
  const seenNodes = new Set();
  const seenPeers = new Set();
  const found = [];

  // Candidates kept sorted by XOR distance to the target. Each round queries
  // the closest unvisited nodes, which is what makes the search converge on
  // whoever is actually storing peers for this infohash.
  let candidates = BOOTSTRAP.map((n) => ({ ...n, dist: new Uint8Array(20).fill(0xff) }));

  for (let round = 0; round < MAX_ROUNDS && candidates.length; round++) {
    const batch = candidates.splice(0, MAX_NODES_PER_ROUND);

    const results = await Promise.all(
      batch.map((n) => getPeers(n, infoHashHex).catch(() => ({ peers: [], nodes: [] }))),
    );

    const fresh = [];
    for (const res of results) {
      for (const p of res.peers) {
        const key = `${p.host}:${p.port}`;
        if (seenPeers.has(key)) continue;
        seenPeers.add(key);
        found.push(p);
        fresh.push(p);
      }
      for (const n of res.nodes) {
        const key = `${n.host}:${n.port}`;
        if (seenNodes.has(key)) continue;
        seenNodes.add(key);
        candidates.push({ ...n, dist: xorDistance(n.id, target) });
      }
    }

    // Hand peers back as soon as they appear so dialling overlaps the search.
    if (fresh.length && onPeers) onPeers(fresh);

    candidates.sort((a, b) => compareDistance(a.dist, b.dist));
    candidates = candidates.slice(0, 32);
  }

  return found;
}
