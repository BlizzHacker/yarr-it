// The streaming engine. Everything expensive happens here, in the viewer's tab:
// piece selection, SHA-1 verification, assembly, and decode. The server's only
// job is to relay bytes it cannot interpret.
//
// Pieces are sourced in a deliberate order, cheapest first:
//
//   1. HTTP web seeds (BEP 19)  -- browser fetches straight from the origin.
//                                  Costs the relay nothing.
//   2. WebRTC peers             -- other viewers' browsers. Also free, and the
//                                  reason a $22/yr box can serve a public site:
//                                  viewer #2 pulls from viewer #1.
//   3. TCP peers via mw-bridge  -- the only tier that spends bandwidth, so it
//                                  is used last and capped.

import { BridgePeerConn, isPublicPeer } from './bridge-peer.js';
import { announceUdp, trackersFromMagnet, infoHashFromMagnet } from './tracker-udp.js';

// WebTorrent's prebuilt browser bundle is an ES module (`export {default}`), so
// it must be imported rather than loaded as a global via <script>. It is marked
// external at build time and resolved by the browser at runtime -- bundling it
// from source would mean shimming half of Node's stdlib.
import WebTorrent from './webtorrent.min.js';

// Trackers reachable from a browser. Plain http/udp trackers cannot be
// contacted directly from a page, so we use the WSS ones; the bridge covers
// the rest via peer exchange once any connection is established.
const WSS_TRACKERS = [
  'wss://tracker.openwebtorrent.com',
  'wss://tracker.webtorrent.dev',
  'wss://tracker.files.fm:7073/announce',
];

const MAX_BRIDGE_PEERS = 12;

export class StreamEngine {
  constructor({ onStats } = {}) {
    this.client = new WebTorrent({ tracker: { announce: WSS_TRACKERS } });
    this.torrent = null;
    this.onStats = onStats || (() => {});
    this._bridgePeers = new Set();
    this._statsTimer = null;
    this._serverReady = this._initServer();

    this.client.on('error', (err) => console.warn('[engine]', err?.message || err));
  }

  /**
   * Register the service worker that backs file.streamURL.
   *
   * WebTorrent 3 removed the old file.streamTo()/appendTo() helpers. Playback
   * now works by having a service worker answer range requests for a virtual
   * URL, which is what lets the <video> element seek into a file that is still
   * downloading instead of waiting for the whole thing.
   */
  async _initServer() {
    if (!('serviceWorker' in navigator)) return false;
    try {
      const reg = await navigator.serviceWorker.register('/sw.min.js', { scope: '/' });
      await navigator.serviceWorker.ready;
      this.client.createServer({ controller: reg });
      return true;
    } catch (err) {
      console.warn('[engine] service worker unavailable:', err?.message || err);
      return false;
    }
  }

  /**
   * Find peers for a normal (non-WebRTC) swarm and dial them through the relay.
   *
   * Waiting on torrent.on('peer') is not enough on its own: that event only
   * fires for peers the browser's own tracker client found, and a browser
   * cannot reach udp:// or http:// trackers at all. So a torrent with 50 seeds
   * produces zero peer events. We announce to its UDP trackers ourselves,
   * through the relay, and feed the results in.
   */
  async _discoverViaBridge(torrent, magnet) {
    const infoHash = infoHashFromMagnet(magnet) || torrent.infoHash;
    const trackers = trackersFromMagnet(magnet);
    if (!infoHash || !trackers.length) return;

    // Dial as each tracker answers rather than awaiting all of them. Dead
    // trackers are common and a Promise.all would let the slowest one hold up
    // playback for its full timeout.
    const seen = new Set();
    await Promise.all(
      trackers.slice(0, 6).map(async (tr) => {
        const peers = await announceUdp(tr, infoHash).catch(() => []);
        for (const peer of peers) {
          const key = `${peer.host}:${peer.port}`;
          if (seen.has(key)) continue;
          seen.add(key);
          this._dialBridgePeer(torrent, peer);
        }
      }),
    );
  }

  /** Open one relayed TCP peer and hand it to the engine. */
  _dialBridgePeer(torrent, { host, port }) {
    if (this._bridgePeers.size >= MAX_BRIDGE_PEERS) return;
    const key = `${host}:${port}`;
    if (this._bridgePeers.has(key) || !isPublicPeer(host)) return;
    this._bridgePeers.add(key);

    try {
      const conn = new BridgePeerConn(host, port);
      conn.on('connect', () => {
        try {
          // Hand the relayed socket to the engine as an ordinary peer.
          torrent._addPeer(conn, 'bridge');
        } catch {
          conn.destroy();
          this._bridgePeers.delete(key);
        }
      });
      conn.on('close', () => this._bridgePeers.delete(key));
      conn.on('error', () => this._bridgePeers.delete(key));
    } catch {
      this._bridgePeers.delete(key);
    }
  }

  /** Also relay any peer the browser's own tracker client happens to find. */
  _attachBridgePeers(torrent) {
    torrent.on('peer', (addr) => {
      if (typeof addr !== 'string') return;
      const i = addr.lastIndexOf(':');
      if (i < 0) return;
      this._dialBridgePeer(torrent, {
        host: addr.slice(0, i),
        port: Number(addr.slice(i + 1)),
      });
    });
  }

  /**
   * Start streaming a magnet. Resolves with the chosen file once metadata
   * arrives, which is when playback can begin -- not when the download is done.
   */
  add(magnet, { onReady, onError } = {}) {
    this.destroyTorrent();

    const torrent = this.client.add(magnet, { announce: WSS_TRACKERS }, (t) => {
      const file = pickPlayableFile(t.files);
      if (!file) {
        onError?.(new Error('no playable media in this torrent'));
        return;
      }
      onReady?.(file, t);
    });

    this.torrent = torrent;
    this._attachBridgePeers(torrent);
    // Most public torrents have no WebRTC peers at all, so relay-based
    // discovery is the only way they ever start.
    this._discoverViaBridge(torrent, magnet).catch(() => {});
    torrent.on('error', (err) => onError?.(err));

    clearInterval(this._statsTimer);
    this._statsTimer = setInterval(() => {
      if (!this.torrent) return;
      const t = this.torrent;
      this.onStats({
        progress: t.progress,
        downloaded: t.downloaded,
        downloadSpeed: t.downloadSpeed,
        uploadSpeed: t.uploadSpeed,
        peers: t.numPeers,
        bridgePeers: this._bridgePeers.size,
        webSeeds: t._servers?.length ?? 0,
        ready: t.ready,
      });
    }, 1000);

    return torrent;
  }

  destroyTorrent() {
    clearInterval(this._statsTimer);
    this._bridgePeers.clear();
    if (this.torrent) {
      try {
        this.torrent.destroy();
      } catch {
        /* already gone */
      }
      this.torrent = null;
    }
  }
}

const VIDEO_EXT = ['.mp4', '.m4v', '.webm', '.mkv', '.avi', '.mov', '.ogv'];
const AUDIO_EXT = ['.mp3', '.m4a', '.aac', '.flac', '.ogg', '.opus', '.wav'];
const IMAGE_EXT = ['.jpg', '.jpeg', '.png', '.gif', '.webp', '.avif', '.bmp'];

export function classify(name) {
  const n = name.toLowerCase();
  if (VIDEO_EXT.some((e) => n.endsWith(e))) return 'video';
  if (AUDIO_EXT.some((e) => n.endsWith(e))) return 'audio';
  if (IMAGE_EXT.some((e) => n.endsWith(e))) return 'image';
  return 'other';
}

/** Largest media file wins; sample clips and extras are always smaller. */
export function pickPlayableFile(files) {
  const media = files.filter((f) => classify(f.name) !== 'other');
  if (!media.length) return null;
  return media.reduce((a, b) => (b.length > a.length ? b : a));
}

/** Codecs a browser plays directly. Others need the WebCodecs path. */
export function needsWebCodecs(name) {
  const n = name.toLowerCase();
  return n.endsWith('.mkv') || n.endsWith('.avi') || n.endsWith('.ogv') || n.endsWith('.mov');
}
