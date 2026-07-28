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
if (typeof window !== 'undefined') window.__bridgeStats = () => BridgePeerConn.stats;
import {
  announceUdp,
  announceHttp,
  trackersFromMagnet,
  httpTrackersFromMagnet,
  infoHashFromMagnet,
} from './tracker-udp.js';
import { dhtFindPeers } from './dht.js';

const ENABLE_DHT =
  typeof localStorage !== 'undefined' && localStorage.getItem('mw-dht') === '1';

// WebTorrent's prebuilt browser bundle is an ES module (`export {default}`), so
// it must be imported rather than loaded as a global via <script>. It is marked
// external at build time and resolved by the browser at runtime -- bundling it
// from source would mean shimming half of Node's stdlib.
import WebTorrent from './webtorrent.min.js';

// Trackers reachable from a browser. Plain http/udp trackers cannot be
// contacted directly from a page, so we use the WSS ones; the bridge covers
// the rest via peer exchange once any connection is established.
// tracker.files.fm:7073 was dropped: it is dead and every attempt spams the
// console with a failed WebSocket handshake.
const WSS_TRACKERS = [
  'wss://tracker.openwebtorrent.com',
  'wss://tracker.webtorrent.dev',
];

// Enough relayed peers to sustain playback without spending relay bandwidth
// on a swarm we may abandon in seconds.
const MAX_BRIDGE_PEERS = 8;
// Kept well under the relay per-IP socket cap so dials are refused rarely.
const MAX_INFLIGHT_DIALS = 6;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export class StreamEngine {
  constructor({ onStats } = {}) {
    this.client = new WebTorrent({ tracker: { announce: WSS_TRACKERS } });
    this.torrent = null;
    this.onStats = onStats || (() => {});
    this._bridgePeers = new Set();
    this._bridgeTried = new Set();
    this._bridgeInFlight = 0;
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
    const udp = trackersFromMagnet(magnet);
    const http = httpTrackersFromMagnet(magnet);
    if (!infoHash || (!udp.length && !http.length)) return;

    // Dial as each tracker answers rather than awaiting all of them. Dead
    // trackers are common and a Promise.all would let the slowest one hold up
    // playback for its full timeout.
    await Promise.all([
      ...udp.slice(0, 6).map(async (tr) => {
        const peers = await announceUdp(tr, infoHash).catch(() => []);
        await this._dialInBatches(torrent, peers);
      }),
      // http(s) trackers are reached through the relay's announce proxy; a
      // browser cannot contact them directly at all.
      ...http.slice(0, 4).map(async (tr) => {
        const peers = await announceHttp(tr, infoHash).catch(() => []);
        await this._dialInBatches(torrent, peers);
      }),
      // DHT is implemented and its transport is verified, but it is OFF by
      // default because it does not yet yield peers: every query egresses from
      // the single relay IP, so DHT nodes see one address issuing ~100
      // get_peers queries and rate-limit it. Enable to experiment:
      //   localStorage.setItem("mw-dht", "1")
      ...(ENABLE_DHT
        ? [
            dhtFindPeers(infoHash, (peers) => {
              this._dialInBatches(torrent, peers).catch(() => {});
            }).catch(() => []),
          ]
        : []),
    ]);
  }

  /**
   * Work through a peer list a few at a time.
   *
   * Most public-swarm addresses never answer, so this keeps a small number of
   * dials in flight and moves on rather than waiting on each one. It stops
   * early once enough peers are actually connected.
   */
  async _dialInBatches(torrent, peers) {
    for (const peer of peers) {
      if (this._bridgePeers.size >= MAX_BRIDGE_PEERS) return;
      while (this._bridgeInFlight >= MAX_INFLIGHT_DIALS) {
        await sleep(250);
        if (this._bridgePeers.size >= MAX_BRIDGE_PEERS) return;
      }
      this._dialBridgePeer(torrent, peer);
    }
  }

  /**
   * Open one relayed TCP peer and hand it to the engine.
   *
   * Concurrency is the whole difficulty here. The relay caps concurrent sockets
   * per client IP, and most public-swarm peers are dead or firewalled, so naive
   * dialling opens hundreds at once, gets refused, and the failures free slots
   * that instantly refill — a thrash loop in which no connection ever survives.
   *
   * So three separate counts are tracked: peers already tried (never retried),
   * dials currently in flight (bounded), and peers actually connected (the
   * thing we want). Only the last is allowed to stop the search.
   */
  _dialBridgePeer(torrent, { host, port }) {
    const key = `${host}:${port}`;
    if (this._bridgeTried.has(key) || !isPublicPeer(host)) return false;
    if (this._bridgeInFlight >= MAX_INFLIGHT_DIALS) return false;
    if (this._bridgePeers.size >= MAX_BRIDGE_PEERS) return false;

    this._bridgeTried.add(key);
    this._bridgeInFlight++;

    let settled = false;
    const settle = () => {
      if (settled) return;
      settled = true;
      this._bridgeInFlight--;
    };

    try {
      const conn = new BridgePeerConn(host, port);

      conn.on('connect', () => {
        settle();
        this._bridgePeers.add(key);
        try {
          // Hand the relayed socket over as an ordinary peer. The engine keys
          // its peer map on conn.id, which BridgePeerConn sets to host:port.
          torrent._addPeer(conn, 'bridge');
        } catch {
          this._bridgePeers.delete(key);
          conn.destroy();
        }
      });

      const drop = () => {
        settle();
        this._bridgePeers.delete(key);
      };
      conn.on('close', drop);
      conn.on('error', drop);
      return true;
    } catch (err) {
      console.warn('[engine] bridge dial failed:', err?.message || err);
      settle();
      return false;
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
  add(magnet, { onReady, onError, onFiles } = {}) {
    this.destroyTorrent();

    const torrent = this.client.add(magnet, { announce: WSS_TRACKERS }, (t) => {
      // A caller that wants to choose for itself gets the whole torrent first
      // and returns true to say it has taken over. A 200-ROM pack has no
      // single right answer, and picking one on the user's behalf is how a
      // pack of games becomes whichever game happened to be biggest.
      if (onFiles?.(t)) return;

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
    this._bridgeTried.clear();
    this._bridgeInFlight = 0;
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

// Flash movies/games and cartridge ROMs are playable too -- just on a canvas,
// via Ruffle and EmulatorJS, rather than in a media element. Without these the
// picker below discards them and a ROM torrent reports "no playable media".
const FLASH_EXT = ['.swf'];
const ROM_EXT = [
  '.nes', '.fds', '.unf', '.unif',
  '.smc', '.sfc', '.swc', '.fig',
  '.gb', '.gbc', '.gba',
  '.n64', '.z64', '.v64',
  '.md', '.gen', '.smd', '.sms', '.gg',
  '.a26', '.a78', '.lnx', '.pce', '.ws', '.wsc', '.ngp', '.ngc', '.vb',
];

export function classify(name) {
  const n = name.toLowerCase();
  if (VIDEO_EXT.some((e) => n.endsWith(e))) return 'video';
  if (AUDIO_EXT.some((e) => n.endsWith(e))) return 'audio';
  if (IMAGE_EXT.some((e) => n.endsWith(e))) return 'image';
  if (FLASH_EXT.some((e) => n.endsWith(e))) return 'flash';
  if (ROM_EXT.some((e) => n.endsWith(e))) return 'rom';
  return 'other';
}

/**
 * Largest playable file wins; sample clips and extras are always smaller.
 *
 * ROMs are the exception: a ROM torrent is usually a whole collection, and the
 * biggest file in it is nothing special. Prefer a ROM/Flash file only when the
 * torrent has no video or audio at all, so a movie torrent that happens to
 * carry an .swf extra still plays the movie.
 */
export function pickPlayableFile(files) {
  const kindOf = (f) => classify(f.name);
  const media = files.filter((f) => ['video', 'audio', 'image'].includes(kindOf(f)));
  const pool = media.length
    ? media
    : files.filter((f) => ['flash', 'rom'].includes(kindOf(f)));
  if (!pool.length) return null;
  return pool.reduce((a, b) => (b.length > a.length ? b : a));
}

/** Codecs a browser plays directly. Others need the WebCodecs path. */
export function needsWebCodecs(name) {
  const n = name.toLowerCase();
  return n.endsWith('.mkv') || n.endsWith('.avi') || n.endsWith('.ogv') || n.endsWith('.mov');
}
