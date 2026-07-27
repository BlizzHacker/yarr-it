// Bridge transport: gives the in-browser torrent engine access to ordinary
// TCP BitTorrent peers.
//
// A page cannot open a TCP socket, so a pure browser client only ever reaches
// WebRTC peers -- which is why WebTorrent alone plays almost nothing from a
// public tracker. This wraps a WebSocket to mw-bridge in a duplex-stream shape,
// so the engine can treat a relayed TCP peer exactly like a WebRTC one.
//
// The relay is a dumb pipe: it is told an address and moves bytes. It is not
// told, and cannot infer, which torrent it is carrying.

// streamx gives us a real .pipe()-able duplex. WebTorrent's peer plumbing does
// `conn.pipe(wire).pipe(conn)`, so a hand-rolled EventEmitter is not enough.
import { Duplex } from 'streamx';

const BRIDGE_URL = `wss://${location.host}/bridge/socket`;

export class BridgePeerConn extends Duplex {
  // Connection-attempt counters. Relay failures are otherwise invisible: a peer
  // that never answers looks identical to a bug in the handover.
  static stats = { created: 0, wsOpen: 0, relayOk: 0, wsClose: 0, wsError: 0, lastCode: null };

  /**
   * @param {string} host  peer IPv4/IPv6 literal (never a hostname -- the
   *                       bridge refuses names so it can't be used for DNS
   *                       rebinding onto private space)
   * @param {number} port
   */
  constructor(host, port) {
    super();
    this.host = host;
    this.port = port;

    // The engine keys its peer map on `conn.id` and rejects duplicates. Without
    // a unique id every relayed peer collides on `undefined`, so exactly one
    // could ever attach and the rest were silently dropped as duplicates.
    this.id = `${host}:${port}`;
    this.remoteAddress = host;
    this.remotePort = port;

    this.connected = false;
    // NOT `destroyed`: streamx defines that as a getter with no setter, so
    // assigning to it throws and the whole constructor fails.
    this._closed = false;
    this._pending = [];

    BridgePeerConn.stats.created++;

    this._ws = new WebSocket(BRIDGE_URL);
    this._ws.binaryType = 'arraybuffer';

    this._ws.onopen = () => {
      BridgePeerConn.stats.wsOpen++;
      this._ws.send(JSON.stringify({ proto: 'tcp', host, port }));
    };

    this._ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        // Control frame. Only two exist: {"ok":true} on connect, or nothing.
        try {
          if (JSON.parse(ev.data).ok) {
            BridgePeerConn.stats.relayOk++;
            this.connected = true;
            for (const chunk of this._pending) this._ws.send(chunk);
            this._pending = [];
            this.emit('connect');
          }
        } catch {
          /* ignore malformed control frame */
        }
        return;
      }
      this.push(new Uint8Array(ev.data));
    };

    this._ws.onclose = (e) => { BridgePeerConn.stats.wsClose++; BridgePeerConn.stats.lastCode = e?.code; this._cleanup(); };
    this._ws.onerror = () => { BridgePeerConn.stats.wsError++; this._cleanup(); };
  }

  _write(chunk, cb) {
    if (this._closed) return cb();
    const buf = chunk instanceof Uint8Array ? chunk : new Uint8Array(chunk);
    if (!this.connected) {
      this._pending.push(buf);
    } else if (this._ws.readyState === WebSocket.OPEN) {
      this._ws.send(buf);
    }
    cb();
  }

  _destroy(cb) {
    this._cleanup();
    cb();
  }

  _cleanup() {
    if (this._closed) return;
    this._closed = true;
    try {
      this._ws.close();
    } catch {
      /* already closing */
    }
    this.push(null);
    this.emit('close');
  }

  // simple-peer compatibility: the engine calls these on WebRTC peers.
  signal() {}
  get bufferSize() {
    return this._ws?.bufferedAmount ?? 0;
  }
}

/**
 * Decode a compact peer list (BEP 23): 6 bytes per peer, 4 for IPv4 + 2 for
 * port, big-endian. This is what trackers actually return.
 */
export function decodeCompactPeers(bytes) {
  const out = [];
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  for (let i = 0; i + 6 <= bytes.length; i += 6) {
    const host = `${bytes[i]}.${bytes[i + 1]}.${bytes[i + 2]}.${bytes[i + 3]}`;
    const port = view.getUint16(i + 4, false);
    if (port > 0) out.push({ host, port });
  }
  return out;
}

/** Reject anything the bridge would refuse, so we don't waste a socket. */
export function isPublicPeer(host) {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  if (a === 0 || a === 127 || a === 10) return false;
  if (a === 172 && b >= 16 && b <= 31) return false;
  if (a === 192 && b === 168) return false;
  if (a === 169 && b === 254) return false;
  if (a === 100 && b >= 64 && b <= 127) return false;
  if (a >= 224) return false;
  return true;
}
