// The front door for a Yarr.It running on somebody's own machine.
//
// In production Caddy sits in front of the three services: the web app on /,
// mw-search on /api and /auth, mw-bridge on /bridge. That single origin is not
// a packaging detail, it is the whole reason self-hosting needs no
// configuration. The client's server setting falls back to "whichever origin
// served this page" (web/src/server.js), and the personal endpoints -- the
// watchlist and resume points -- are deliberately same-origin only. Serve the
// page from one place and the API from another and every client has to be
// configured by hand, and the shelf stops working even then.
//
// Windows has no Caddy, so this does the same three jobs using nothing but the
// Node standard library. It is not a general-purpose web server: no TLS, no
// virtual hosts, no compression, and it is meant for a LAN.

import { createServer, request as httpRequest } from 'node:http';
import { connect } from 'node:net';
import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { extname, join, normalize, resolve, sep } from 'node:path';

// lastIndexOf, not indexOf: an IPv6 literal is full of colons and only the last
// one separates the port.
function addr(value, fallback) {
  const s = value || fallback;
  const i = s.lastIndexOf(':');
  return { host: s.slice(0, i), port: Number(s.slice(i + 1)) };
}

const WWW = resolve(process.env.YARRIT_WWW || 'www');
const LISTEN = addr(process.env.YARRIT_WEB_ADDR, '0.0.0.0:8800');
const SEARCH = addr(process.env.YARRIT_SEARCH_ADDR, '127.0.0.1:8802');
const BRIDGE = addr(process.env.YARRIT_BRIDGE_ADDR, '127.0.0.1:8801');

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.webmanifest': 'application/manifest+json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.ico': 'image/x-icon',
  '.txt': 'text/plain; charset=utf-8',
  // Ruffle backs the Flash resolver and its cores are WebAssembly. The browser
  // refuses to instantiate them from the wrong content-type, and the error it
  // reports names the compiler rather than the header.
  '.wasm': 'application/wasm',
};

function typeFor(file) {
  return TYPES[extname(file).toLowerCase()] || 'application/octet-stream';
}

async function statOrNull(file) {
  try {
    return await stat(file);
  } catch {
    return null;
  }
}

async function serveStatic(req, res) {
  let urlPath;
  try {
    urlPath = decodeURIComponent(new URL(req.url, 'http://localhost').pathname);
  } catch {
    res.writeHead(400).end();
    return;
  }

  let file = normalize(join(WWW, urlPath));
  // join() and normalize() are not a containment check by themselves: a path of
  // "/../config.env" normalises to a real file outside WWW, and serving it would
  // hand out the Prowlarr key.
  if (file !== WWW && !file.startsWith(WWW + sep)) {
    res.writeHead(403).end();
    return;
  }

  let info = await statOrNull(file);
  if (info && info.isDirectory()) {
    file = join(file, 'index.html');
    info = await statOrNull(file);
  }
  if (!info) {
    // The same rule as Caddy's try_files: a path with no extension is a route
    // inside the single-page app, not a missing file. A path with one is a real
    // asset that is genuinely absent, and saying so beats returning the app.
    if (extname(urlPath)) {
      res.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' });
      res.end('not found\n');
      return;
    }
    file = join(WWW, 'index.html');
    info = await statOrNull(file);
    if (!info) {
      res.writeHead(500, { 'content-type': 'text/plain; charset=utf-8' });
      res.end(`no web app in ${WWW}; re-run Install-Local-Server.ps1\n`);
      return;
    }
  }

  res.writeHead(200, {
    'content-type': typeFor(file),
    'content-length': info.size,
    // Nothing is cached, deliberately. There is no bandwidth worth saving on a
    // LAN, and a browser holding yesterday's app.js after a rebuild is the
    // classic way to spend an afternoon debugging code that is no longer
    // running.
    'cache-control': 'no-store',
    'x-content-type-options': 'nosniff',
  });
  if (req.method === 'HEAD') {
    res.end();
    return;
  }
  createReadStream(file).pipe(res);
}

function proxy(req, res, to) {
  const up = httpRequest(
    { host: to.host, port: to.port, method: req.method, path: req.url, headers: req.headers },
    (upRes) => {
      res.writeHead(upRes.statusCode || 502, upRes.headers);
      upRes.pipe(res);
    },
  );
  up.on('error', (err) => {
    // Naming the address that failed is the difference between a five-second
    // fix and a hunt: this is nearly always a service that exited at startup.
    res.writeHead(502, { 'content-type': 'text/plain; charset=utf-8' });
    res.end(`${to.host}:${to.port} did not answer: ${err.message}\n`);
  });
  req.pipe(up);
}

const server = createServer((req, res) => {
  const path = req.url || '/';
  if (path.startsWith('/api/') || path.startsWith('/auth/')) {
    proxy(req, res, SEARCH);
    return;
  }
  if (path.startsWith('/bridge/')) {
    proxy(req, res, BRIDGE);
    return;
  }
  serveStatic(req, res).catch((err) => {
    res.writeHead(500, { 'content-type': 'text/plain; charset=utf-8' });
    res.end(`${err.message}\n`);
  });
});

// The peer relay is a WebSocket, which Node hands over as a raw socket rather
// than a request -- so it is piped byte for byte instead of proxied.
//
// This route carries real traffic over plain http: the client builds the URL
// with bridgeSocketURL() (web/src/server.js), which derives ws:// or wss:// from
// wherever it is pointed rather than assuming TLS. That is what lets a local
// install reach ordinary TCP peers instead of only web seeds and WebRTC.
//
// It is worth keeping the upgrade wired even if that ever changes: a silently
// missing route is much harder to diagnose than a refused one.
server.on('upgrade', (req, socket, head) => {
  if (!(req.url || '').startsWith('/bridge/')) {
    socket.destroy();
    return;
  }
  const up = connect(BRIDGE.port, BRIDGE.host, () => {
    const lines = [`${req.method} ${req.url} HTTP/1.1`];
    for (let i = 0; i < req.rawHeaders.length; i += 2) {
      lines.push(`${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}`);
    }
    up.write(lines.join('\r\n') + '\r\n\r\n');
    // Bytes the client sent immediately after the handshake arrive with the
    // upgrade event, not on the socket, and are lost if they are not replayed.
    if (head && head.length) up.write(head);
    socket.pipe(up);
    up.pipe(socket);
  });
  up.on('error', () => socket.destroy());
  socket.on('error', () => up.destroy());
});

server.listen(LISTEN.port, LISTEN.host, () => {
  console.log(`front door on ${LISTEN.host}:${LISTEN.port} serving ${WWW}`);
  console.log(`  /api, /auth -> ${SEARCH.host}:${SEARCH.port}`);
  console.log(`  /bridge     -> ${BRIDGE.host}:${BRIDGE.port}`);
});
