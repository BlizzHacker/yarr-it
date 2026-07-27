# Universal Source Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Yarr.It's torrent-only entry point with a resolver registry so direct URLs, HLS streams, IPTV playlists and YouTube/Vimeo embeds all play through one pipeline.

**Architecture:** A `Source` is a resolver's input. Each resolver answers `canHandle(input)` and `resolve(source)`, returning either a `Playable` (a descriptor the player renders, discriminated by `render`) or a `Collection` (a list the library UI browses). Playback climbs a ladder — direct, then the user's LAN gateway, then the public relay — and only reports failure when all three are impossible. The existing `StreamEngine` is preserved verbatim as the body of one resolver.

**Tech Stack:** Vanilla ES modules, esbuild, Node's built-in test runner (`node --test`, no new dependencies), Go 1.x for the bridge, IndexedDB for local storage.

## Global Constraints

- Repo root: `C:\MoveWeight\vps-edge`. Frontend lives in `stream/web/`, bridge in `stream/bridge/`.
- **No new runtime dependencies in `stream/web/package.json`.** The page is served as raw ES modules plus one esbuild bundle; every added dependency inflates the bundle the ladder is trying to keep cheap.
- ES modules only (`"type": "module"`). No CommonJS.
- All new JS files use two-space indent, single quotes, semicolons — matching `stream/web/src/engine.js`.
- Pure logic must be separated from IO so it can be unit-tested without a network or a DOM. Parsers, sniffers and tier selection are pure functions.
- YouTube and Vimeo are **embeds only**. Never extract stream URLs — it violates their terms and gets the relay IP blocked.
- Existing Go tests must keep passing: `cd stream/search && go test ./...` and `cd stream/bridge && go test ./...`.
- Commit after every task. Never use `git add -A` at the repo root — it would sweep in unrelated uncommitted work.

---

### Task 1: JavaScript test harness

There is no JS test runner in this repo — `stream/web/package.json` has only a `build` script. Every later task is TDD, so this must exist first. Node 24 ships a test runner, so this needs no new dependency.

**Files:**
- Modify: `stream/web/package.json`
- Test: `stream/web/src/harness.test.js` (temporary, deleted in step 6)

**Interfaces:**
- Consumes: nothing
- Produces: `npm test` in `stream/web/` runs every `*.test.js` under `src/`

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/harness.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';

test('the test runner is wired up', () => {
  assert.equal(1 + 1, 2);
});
```

- [ ] **Step 2: Run it to confirm there is no test script**

Run: `cd stream/web && npm test`
Expected: FAIL — `npm error Missing script: "test"`

- [ ] **Step 3: Add the test script**

In `stream/web/package.json`, add to `scripts` (keep `build` unchanged):

```json
    "test": "node --test src/"
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 1`

- [ ] **Step 5: Confirm the build still works**

Run: `cd stream/web && npm run build`
Expected: esbuild prints `dist/app.js` with no error.

- [ ] **Step 6: Remove the scaffold test and commit**

```bash
rm stream/web/src/harness.test.js
git add stream/web/package.json
git commit -m "test: add node --test harness for the web frontend"
```

---

### Task 2: Source, Playable and Collection types plus the resolver registry

**Files:**
- Create: `stream/web/src/source.js`
- Test: `stream/web/src/source.test.js`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `makeSource({kind, uri, meta})` → `{kind, uri, meta}`
  - `makePlayable({render, src, mime, tier, cleanup})` → `Playable`
  - `makeCollection({title, sources})` → `{title, sources}`
  - `isCollection(x)` → boolean
  - `RENDER` → `{VIDEO:'video', AUDIO:'audio', IMAGE:'image', EMBED:'embed', CANVAS:'canvas'}`
  - `TIER` → `{DIRECT:'direct', GATEWAY:'gateway', RELAY:'relay'}`
  - `createRegistry()` → `{register(resolver), find(input), resolve(source, ctx)}`

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/source.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  makeSource, makePlayable, makeCollection, isCollection,
  RENDER, TIER, createRegistry,
} from './source.js';

test('makePlayable rejects an unknown render kind', () => {
  assert.throws(
    () => makePlayable({ render: 'hologram', src: 'x', mime: 'video/mp4' }),
    /unknown render/,
  );
});

test('makePlayable defaults tier to direct and cleanup to a no-op', () => {
  const p = makePlayable({ render: RENDER.VIDEO, src: 'x', mime: 'video/mp4' });
  assert.equal(p.tier, TIER.DIRECT);
  assert.equal(typeof p.cleanup, 'function');
  p.cleanup();
});

test('isCollection distinguishes a list from a playable', () => {
  const c = makeCollection({ title: 'IPTV', sources: [] });
  const p = makePlayable({ render: RENDER.VIDEO, src: 'x', mime: 'video/mp4' });
  assert.equal(isCollection(c), true);
  assert.equal(isCollection(p), false);
});

test('registry picks the first resolver that claims the input', () => {
  const reg = createRegistry();
  reg.register({ name: 'no', canHandle: () => false, resolve: async () => null });
  reg.register({ name: 'yes', canHandle: (i) => i.startsWith('magnet:'), resolve: async () => null });
  assert.equal(reg.find('magnet:?xt=1').name, 'yes');
  assert.equal(reg.find('https://example.com'), null);
});

test('registry resolve throws a typed error when nothing handles the input', async () => {
  const reg = createRegistry();
  await assert.rejects(
    () => reg.resolve(makeSource({ kind: 'unknown', uri: 'wat://x' })),
    /no resolver/,
  );
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './source.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/source.js`:

```js
/**
 * The three types the whole player is built on.
 *
 * `render` is a discriminator rather than an assumption that everything is a
 * <video src>. Ruffle and EmulatorJS draw to a canvas and YouTube is an iframe,
 * so normalising every source to a URL would have to be torn out one resolver
 * later.
 */

export const RENDER = {
  VIDEO: 'video',
  AUDIO: 'audio',
  IMAGE: 'image',
  EMBED: 'embed',
  CANVAS: 'canvas',
};

export const TIER = {
  DIRECT: 'direct',
  GATEWAY: 'gateway',
  RELAY: 'relay',
};

const RENDERS = new Set(Object.values(RENDER));

export function makeSource({ kind, uri, meta = {} }) {
  if (!kind) throw new Error('source needs a kind');
  if (!uri) throw new Error('source needs a uri');
  return { kind, uri, meta };
}

export function makePlayable({ render, src, mime, tier = TIER.DIRECT, cleanup }) {
  if (!RENDERS.has(render)) throw new Error(`unknown render kind: ${render}`);
  return { render, src, mime, tier, cleanup: cleanup ?? (() => {}) };
}

/**
 * A list, not a stream. An .m3u is thousands of channels; conflating it with a
 * Playable is what makes IPTV implementations feel fake.
 */
export function makeCollection({ title, sources }) {
  return { title, sources: [...sources] };
}

export function isCollection(x) {
  return Boolean(x) && Array.isArray(x.sources);
}

export function createRegistry() {
  const resolvers = [];
  return {
    register(resolver) {
      resolvers.push(resolver);
      return this;
    },
    find(input) {
      return resolvers.find((r) => r.canHandle(input)) ?? null;
    },
    async resolve(source, ctx = {}) {
      const resolver = resolvers.find((r) => r.canHandle(source.uri));
      if (!resolver) throw new Error(`no resolver for: ${source.uri}`);
      return resolver.resolve(source, ctx);
    },
  };
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 5`

- [ ] **Step 5: Commit**

```bash
git add stream/web/src/source.js stream/web/src/source.test.js
git commit -m "feat: source, playable and collection types with a resolver registry"
```

---

### Task 3: Typed playback failures

**Files:**
- Create: `stream/web/src/failures.js`
- Test: `stream/web/src/failures.test.js`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `PlaybackError` class with `.code` and `.message`
  - `FAILURE` → `{MIXED_CONTENT, CORS_BLOCKED, DEAD_STREAM, UNSUPPORTED_CODEC, BUDGET_EXHAUSTED}`
  - `describe(code)` → human-readable string
  - `nextTiers(code)` → array of tier names worth trying, in order

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/failures.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { PlaybackError, FAILURE, describe, nextTiers } from './failures.js';
import { TIER } from './source.js';

test('mixed content and cors both escalate to gateway then relay', () => {
  assert.deepEqual(nextTiers(FAILURE.MIXED_CONTENT), [TIER.GATEWAY, TIER.RELAY]);
  assert.deepEqual(nextTiers(FAILURE.CORS_BLOCKED), [TIER.GATEWAY, TIER.RELAY]);
});

test('a dead stream is terminal - no tier can fix it', () => {
  assert.deepEqual(nextTiers(FAILURE.DEAD_STREAM), []);
  assert.deepEqual(nextTiers(FAILURE.UNSUPPORTED_CODEC), []);
});

test('an exhausted budget is terminal but points at the gateway', () => {
  assert.deepEqual(nextTiers(FAILURE.BUDGET_EXHAUSTED), []);
  assert.match(describe(FAILURE.BUDGET_EXHAUSTED), /gateway/i);
});

test('every failure code has a description', () => {
  for (const code of Object.values(FAILURE)) {
    assert.equal(typeof describe(code), 'string');
    assert.ok(describe(code).length > 0, `${code} has no description`);
  }
});

test('PlaybackError carries its code', () => {
  const err = new PlaybackError(FAILURE.CORS_BLOCKED);
  assert.equal(err.code, FAILURE.CORS_BLOCKED);
  assert.ok(err instanceof Error);
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './failures.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/failures.js`:

```js
import { TIER } from './source.js';

/**
 * Typed failures, so the player can say which rule blocked playback and which
 * tier is worth trying next. A generic "playback error" tells the user nothing
 * and hides the two browser rules that cause most IPTV failures.
 */

export const FAILURE = {
  MIXED_CONTENT: 'MixedContent',
  CORS_BLOCKED: 'CorsBlocked',
  DEAD_STREAM: 'DeadStream',
  UNSUPPORTED_CODEC: 'UnsupportedCodec',
  BUDGET_EXHAUSTED: 'BudgetExhausted',
};

const DESCRIPTIONS = {
  [FAILURE.MIXED_CONTENT]:
    'This stream is served over plain HTTP. Browsers refuse to load it on a secure page.',
  [FAILURE.CORS_BLOCKED]:
    'This stream does not allow other sites to read it, so the browser blocked it.',
  [FAILURE.DEAD_STREAM]:
    'The source did not respond. It is probably offline.',
  [FAILURE.UNSUPPORTED_CODEC]:
    'This device cannot decode this format. The TV apps handle more formats.',
  [FAILURE.BUDGET_EXHAUSTED]:
    'The public relay has reached its monthly limit. Point the app at your own gateway to keep going.',
};

// Escalation is only meaningful for failures a different transport can fix.
const ESCALATION = {
  [FAILURE.MIXED_CONTENT]: [TIER.GATEWAY, TIER.RELAY],
  [FAILURE.CORS_BLOCKED]: [TIER.GATEWAY, TIER.RELAY],
  [FAILURE.DEAD_STREAM]: [],
  [FAILURE.UNSUPPORTED_CODEC]: [],
  [FAILURE.BUDGET_EXHAUSTED]: [],
};

export function describe(code) {
  return DESCRIPTIONS[code] ?? 'Playback failed for an unknown reason.';
}

export function nextTiers(code) {
  return ESCALATION[code] ?? [];
}

export class PlaybackError extends Error {
  constructor(code, detail = '') {
    super(detail ? `${describe(code)} (${detail})` : describe(code));
    this.name = 'PlaybackError';
    this.code = code;
  }
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 10`

- [ ] **Step 5: Commit**

```bash
git add stream/web/src/failures.js stream/web/src/failures.test.js
git commit -m "feat: typed playback failures with tier escalation"
```

---

### Task 4: Tier selection (pure)

The probe does IO; choosing a tier from what the probe found is pure and therefore testable without a network.

**Files:**
- Create: `stream/web/src/ladder.js`
- Test: `stream/web/src/ladder.test.js`

**Interfaces:**
- Consumes: `TIER` from `source.js`, `FAILURE` from `failures.js`
- Produces: `chooseTier({pageProtocol, streamUrl, corsHeader, gatewayUrl, relayAvailable})` → `{tier, blockedBy}` where `blockedBy` is a `FAILURE` code or `null`

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/ladder.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chooseTier } from './ladder.js';
import { TIER } from './source.js';
import { FAILURE } from './failures.js';

const base = {
  pageProtocol: 'https:',
  streamUrl: 'https://cdn.example.com/live.m3u8',
  corsHeader: '*',
  gatewayUrl: null,
  relayAvailable: true,
};

test('a clean https stream with cors plays direct and costs nothing', () => {
  assert.deepEqual(chooseTier(base), { tier: TIER.DIRECT, blockedBy: null });
});

test('http stream on an https page is mixed content and cannot go direct', () => {
  const r = chooseTier({ ...base, streamUrl: 'http://cdn.example.com/live.m3u8' });
  assert.equal(r.blockedBy, FAILURE.MIXED_CONTENT);
  assert.equal(r.tier, TIER.RELAY);
});

test('the user gateway is preferred over the public relay when configured', () => {
  const r = chooseTier({
    ...base,
    streamUrl: 'http://cdn.example.com/live.m3u8',
    gatewayUrl: 'http://192.168.0.118:8900',
  });
  assert.equal(r.tier, TIER.GATEWAY);
});

test('a missing cors header blocks direct playback', () => {
  const r = chooseTier({ ...base, corsHeader: null });
  assert.equal(r.blockedBy, FAILURE.CORS_BLOCKED);
  assert.equal(r.tier, TIER.RELAY);
});

test('an http page can play an http stream directly - no mixed content rule applies', () => {
  const r = chooseTier({
    ...base,
    pageProtocol: 'http:',
    streamUrl: 'http://cdn.example.com/live.m3u8',
  });
  assert.equal(r.tier, TIER.DIRECT);
});

test('with no gateway and no relay the blocker is reported and no tier is chosen', () => {
  const r = chooseTier({ ...base, corsHeader: null, relayAvailable: false });
  assert.equal(r.tier, null);
  assert.equal(r.blockedBy, FAILURE.CORS_BLOCKED);
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './ladder.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/ladder.js`:

```js
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
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 16`

- [ ] **Step 5: Commit**

```bash
git add stream/web/src/ladder.js stream/web/src/ladder.test.js
git commit -m "feat: cheapest-first tier selection for playback"
```

---

### Task 5: M3U parsing and HLS disambiguation (pure)

`.m3u8` is used by both IPTV channel lists and HLS media manifests. Sniff the body, never the extension.

**Files:**
- Create: `stream/web/src/m3u.js`
- Test: `stream/web/src/m3u.test.js`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `parseM3U(text)` → `{title, entries: [{title, uri, logo, group}]}`
  - `looksLikeHls(text)` → boolean

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/m3u.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseM3U, looksLikeHls } from './m3u.js';

test('parses attribute soup, logos and groups', () => {
  const text = [
    '#EXTM3U',
    '#EXTINF:-1 tvg-id="bbc1" tvg-logo="http://x/l.png" group-title="UK",BBC One',
    'http://stream.example/bbc1',
  ].join('\n');
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.deepEqual(entries[0], {
    title: 'BBC One',
    uri: 'http://stream.example/bbc1',
    logo: 'http://x/l.png',
    group: 'UK',
  });
});

// Real playlists are filthy: a BOM, CRLF endings and blank lines are normal.
test('survives a BOM, CRLF line endings and blank lines', () => {
  const text = '\uFEFF#EXTM3U\r\n\r\n#EXTINF:-1,Chan\r\nhttp://a/b\r\n';
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].title, 'Chan');
  assert.equal(entries[0].uri, 'http://a/b');
});

test('an entry with no URI line is dropped rather than half-parsed', () => {
  const text = '#EXTM3U\n#EXTINF:-1,Orphan\n#EXTINF:-1,Real\nhttp://a/b\n';
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].title, 'Real');
});

test('missing logo and group come back as empty strings, not undefined', () => {
  const { entries } = parseM3U('#EXTM3U\n#EXTINF:-1,Bare\nhttp://a/b\n');
  assert.equal(entries[0].logo, '');
  assert.equal(entries[0].group, '');
});

test('an HLS media manifest is not an IPTV playlist', () => {
  const hls = [
    '#EXTM3U',
    '#EXT-X-VERSION:3',
    '#EXT-X-TARGETDURATION:10',
    '#EXTINF:9.9,',
    'seg1.ts',
  ].join('\n');
  assert.equal(looksLikeHls(hls), true);
  assert.equal(looksLikeHls('#EXTM3U\n#EXTINF:-1,Chan\nhttp://a/b'), false);
});

test('an HLS master playlist is also HLS', () => {
  const master = '#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nlow.m3u8';
  assert.equal(looksLikeHls(master), true);
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './m3u.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/m3u.js`:

```js
/**
 * M3U parsing.
 *
 * The format is filthy in the wild: attribute soup on #EXTINF, inconsistent
 * quoting, byte-order marks, CRLF endings, blank lines, and entries whose URI
 * line is simply missing. Everything here is defensive on purpose.
 */

const HLS_MARKERS = ['#EXT-X-TARGETDURATION', '#EXT-X-STREAM-INF', '#EXT-X-VERSION'];

/**
 * Both IPTV channel lists and HLS manifests use the .m3u8 extension, so the
 * extension tells you nothing. HLS manifests carry #EXT-X-* tags; channel
 * lists do not.
 */
export function looksLikeHls(text) {
  return HLS_MARKERS.some((m) => text.includes(m));
}

function attr(line, name) {
  const m = line.match(new RegExp(`${name}="([^"]*)"`));
  return m ? m[1] : '';
}

export function parseM3U(text) {
  const clean = text.replace(/^\uFEFF/, '').replace(/\r\n/g, '\n');
  const lines = clean.split('\n');

  const entries = [];
  let pending = null;

  for (const raw of lines) {
    const line = raw.trim();
    if (!line) continue;

    if (line.startsWith('#EXTINF')) {
      const comma = line.indexOf(',');
      pending = {
        title: comma === -1 ? '' : line.slice(comma + 1).trim(),
        uri: '',
        logo: attr(line, 'tvg-logo'),
        group: attr(line, 'group-title'),
      };
      continue;
    }

    if (line.startsWith('#')) continue;

    // A bare line is the URI for the #EXTINF above it. Without one, the entry
    // is unplayable, so drop it rather than emitting a half-parsed channel.
    if (pending) {
      pending.uri = line;
      entries.push(pending);
      pending = null;
    }
  }

  return { title: 'Playlist', entries };
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 22`

- [ ] **Step 5: Commit**

```bash
git add stream/web/src/m3u.js stream/web/src/m3u.test.js
git commit -m "feat: m3u parser and hls disambiguation"
```

---

### Task 6: URL and embed resolvers

**Files:**
- Create: `stream/web/src/resolvers/url.js`
- Create: `stream/web/src/resolvers/embed.js`
- Test: `stream/web/src/resolvers/resolvers.test.js`

**Interfaces:**
- Consumes: `makePlayable`, `RENDER` from `../source.js`
- Produces:
  - `urlResolver` → `{name:'url', canHandle, resolve}`
  - `renderForMime(mime)` → a `RENDER` value or `null`
  - `embedResolver` → `{name:'embed', canHandle, resolve}`
  - `embedUrlFor(input)` → embed URL string or `null`

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/resolvers/resolvers.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { urlResolver, renderForMime } from './url.js';
import { embedResolver, embedUrlFor } from './embed.js';
import { RENDER } from '../source.js';

test('mime maps to the right render path', () => {
  assert.equal(renderForMime('video/mp4'), RENDER.VIDEO);
  assert.equal(renderForMime('audio/mpeg'), RENDER.AUDIO);
  assert.equal(renderForMime('image/png'), RENDER.IMAGE);
  assert.equal(renderForMime('application/pdf'), null);
});

test('url resolver claims http(s) but not magnets', () => {
  assert.equal(urlResolver.canHandle('https://a/b.mp4'), true);
  assert.equal(urlResolver.canHandle('magnet:?xt=urn:btih:abc'), false);
});

test('youtube watch, short and embed forms all become one embed url', () => {
  const want = 'https://www.youtube.com/embed/dQw4w9WgXcQ';
  assert.equal(embedUrlFor('https://www.youtube.com/watch?v=dQw4w9WgXcQ'), want);
  assert.equal(embedUrlFor('https://youtu.be/dQw4w9WgXcQ'), want);
  assert.equal(embedUrlFor('https://www.youtube.com/embed/dQw4w9WgXcQ'), want);
});

test('vimeo becomes a player embed url', () => {
  assert.equal(embedUrlFor('https://vimeo.com/123456789'),
    'https://player.vimeo.com/video/123456789');
});

test('a non-embeddable url is not claimed by the embed resolver', () => {
  assert.equal(embedUrlFor('https://example.com/x.mp4'), null);
  assert.equal(embedResolver.canHandle('https://example.com/x.mp4'), false);
});

test('embed resolver produces an embed playable', async () => {
  const p = await embedResolver.resolve({ uri: 'https://youtu.be/dQw4w9WgXcQ' });
  assert.equal(p.render, RENDER.EMBED);
  assert.equal(p.src, 'https://www.youtube.com/embed/dQw4w9WgXcQ');
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './url.js'`

- [ ] **Step 3: Write the url resolver**

Create `stream/web/src/resolvers/url.js`:

```js
import { makePlayable, RENDER } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';

const PREFIX = {
  'video/': RENDER.VIDEO,
  'audio/': RENDER.AUDIO,
  'image/': RENDER.IMAGE,
};

export function renderForMime(mime = '') {
  const found = Object.entries(PREFIX).find(([p]) => mime.startsWith(p));
  return found ? found[1] : null;
}

export const urlResolver = {
  name: 'url',
  canHandle(input) {
    return /^https?:\/\//i.test(input);
  },
  async resolve(source, { fetchImpl = fetch } = {}) {
    let res;
    try {
      res = await fetchImpl(source.uri, { method: 'HEAD' });
    } catch {
      throw new PlaybackError(FAILURE.DEAD_STREAM, source.uri);
    }
    if (!res.ok) throw new PlaybackError(FAILURE.DEAD_STREAM, `HTTP ${res.status}`);

    const mime = (res.headers.get('content-type') ?? '').split(';')[0].trim();
    const render = renderForMime(mime);
    if (!render) throw new PlaybackError(FAILURE.UNSUPPORTED_CODEC, mime || 'unknown type');

    return makePlayable({ render, src: source.uri, mime });
  },
};
```

- [ ] **Step 4: Write the embed resolver**

Create `stream/web/src/resolvers/embed.js`:

```js
import { makePlayable, RENDER } from '../source.js';

/**
 * YouTube and Vimeo are embeds, never extractions. Pulling stream URLs out of
 * either service violates its terms and gets the relay IP blocked, so this
 * resolver only ever emits an official iframe URL.
 */

const YOUTUBE = [
  /(?:youtube\.com\/watch\?(?:.*&)?v=)([\w-]{11})/,
  /(?:youtu\.be\/)([\w-]{11})/,
  /(?:youtube\.com\/embed\/)([\w-]{11})/,
];
const VIMEO = /vimeo\.com\/(?:video\/)?(\d+)/;

export function embedUrlFor(input) {
  for (const re of YOUTUBE) {
    const m = input.match(re);
    if (m) return `https://www.youtube.com/embed/${m[1]}`;
  }
  const v = input.match(VIMEO);
  if (v) return `https://player.vimeo.com/video/${v[1]}`;
  return null;
}

export const embedResolver = {
  name: 'embed',
  canHandle(input) {
    return embedUrlFor(input) !== null;
  },
  async resolve(source) {
    return makePlayable({
      render: RENDER.EMBED,
      src: embedUrlFor(source.uri),
      mime: 'text/html',
    });
  },
};
```

- [ ] **Step 5: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 28`

- [ ] **Step 6: Commit**

```bash
git add stream/web/src/resolvers/url.js stream/web/src/resolvers/embed.js stream/web/src/resolvers/resolvers.test.js
git commit -m "feat: url and embed resolvers"
```

---

### Task 7: Playlist and HLS resolvers

**Files:**
- Create: `stream/web/src/resolvers/playlist.js`
- Test: `stream/web/src/resolvers/playlist.test.js`

**Interfaces:**
- Consumes: `parseM3U`, `looksLikeHls` from `../m3u.js`; `makeCollection`, `makeSource`, `makePlayable`, `RENDER` from `../source.js`
- Produces: `playlistResolver` → `{name:'playlist', canHandle, resolve}` returning a `Collection` for channel lists and a `Playable` for HLS manifests

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/resolvers/playlist.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { playlistResolver } from './playlist.js';
import { isCollection, RENDER } from '../source.js';

function fakeFetch(body) {
  return async () => ({ ok: true, text: async () => body });
}

test('a channel list resolves to a collection of sources', async () => {
  const body = [
    '#EXTM3U',
    '#EXTINF:-1 group-title="UK",BBC One',
    'http://s/bbc1',
    '#EXTINF:-1,ITV',
    'http://s/itv',
  ].join('\n');
  const out = await playlistResolver.resolve(
    { uri: 'http://x/list.m3u' }, { fetchImpl: fakeFetch(body) },
  );
  assert.equal(isCollection(out), true);
  assert.equal(out.sources.length, 2);
  assert.equal(out.sources[0].meta.title, 'BBC One');
  assert.equal(out.sources[0].uri, 'http://s/bbc1');
});

test('an hls manifest resolves to a video playable, not a collection', async () => {
  const body = '#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:9.9,\nseg1.ts';
  const out = await playlistResolver.resolve(
    { uri: 'http://x/live.m3u8' }, { fetchImpl: fakeFetch(body) },
  );
  assert.equal(isCollection(out), false);
  assert.equal(out.render, RENDER.VIDEO);
  assert.equal(out.mime, 'application/vnd.apple.mpegurl');
});

test('claims .m3u and .m3u8 urls including ones with a query string', () => {
  assert.equal(playlistResolver.canHandle('http://x/a.m3u'), true);
  assert.equal(playlistResolver.canHandle('http://x/a.m3u8?token=1'), true);
  assert.equal(playlistResolver.canHandle('http://x/a.mp4'), false);
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './playlist.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/resolvers/playlist.js`:

```js
import { parseM3U, looksLikeHls } from '../m3u.js';
import { makeCollection, makeSource, makePlayable, RENDER } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';

const HLS_MIME = 'application/vnd.apple.mpegurl';

export const playlistResolver = {
  name: 'playlist',
  canHandle(input) {
    return /^https?:\/\/.*\.m3u8?(\?|$)/i.test(input);
  },
  async resolve(source, { fetchImpl = fetch } = {}) {
    let res;
    try {
      res = await fetchImpl(source.uri);
    } catch {
      throw new PlaybackError(FAILURE.DEAD_STREAM, source.uri);
    }
    if (!res.ok) throw new PlaybackError(FAILURE.DEAD_STREAM, `HTTP ${res.status}`);

    const body = await res.text();

    // Both shapes share the .m3u8 extension, so the body decides.
    if (looksLikeHls(body)) {
      return makePlayable({ render: RENDER.VIDEO, src: source.uri, mime: HLS_MIME });
    }

    const { entries } = parseM3U(body);
    return makeCollection({
      title: source.meta?.title ?? 'Playlist',
      sources: entries.map((e) => makeSource({
        kind: 'url',
        uri: e.uri,
        meta: { title: e.title, logo: e.logo, group: e.group },
      })),
    });
  },
};
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 31`

- [ ] **Step 5: Commit**

```bash
git add stream/web/src/resolvers/playlist.js stream/web/src/resolvers/playlist.test.js
git commit -m "feat: playlist resolver returning collections and hls playables"
```

---

### Task 8: Torrent resolver wrapping the existing engine

The proven swarm code is not rewritten — it is wrapped so it satisfies the same interface as every other resolver.

**Files:**
- Create: `stream/web/src/resolvers/torrent.js`
- Test: `stream/web/src/resolvers/torrent.test.js`

**Interfaces:**
- Consumes: `StreamEngine`, `classify` from `../engine.js`; `makePlayable`, `RENDER` from `../source.js`
- Produces: `createTorrentResolver({engine})` → `{name:'torrent', canHandle, resolve}`

`canHandle` and the `classify`→`RENDER` mapping are pure and tested; `resolve` needs a real engine and is exercised by the smoke test in Task 12.

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/resolvers/torrent.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createTorrentResolver, renderForKind } from './torrent.js';
import { RENDER } from '../source.js';

test('claims magnet links and bare infohashes only', () => {
  const r = createTorrentResolver({ engine: null });
  assert.equal(r.canHandle('magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10'), true);
  assert.equal(r.canHandle('08ada5a7a6183aae1e09d831df6748d566095a10'), true);
  assert.equal(r.canHandle('https://example.com/a.mp4'), false);
  assert.equal(r.canHandle('not a hash'), false);
});

test('engine file kinds map onto render paths', () => {
  assert.equal(renderForKind('video'), RENDER.VIDEO);
  assert.equal(renderForKind('audio'), RENDER.AUDIO);
  assert.equal(renderForKind('image'), RENDER.IMAGE);
});

test('resolve rejects when the engine reports no playable file', async () => {
  const engine = {
    add(_uri, { onError }) { onError(new Error('no playable media in this torrent')); },
  };
  const r = createTorrentResolver({ engine });
  await assert.rejects(
    () => r.resolve({ uri: 'magnet:?xt=urn:btih:abc' }),
    /no playable media/,
  );
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './torrent.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/resolvers/torrent.js`:

```js
import { makePlayable, RENDER, TIER } from '../source.js';

/**
 * The torrent path is the one thing here that was already proven in production,
 * so it is wrapped rather than rewritten: StreamEngine keeps its swarm,
 * bridge-peer and web-seed behaviour exactly as it is and simply gains the same
 * shape as every other resolver.
 */

const INFOHASH = /^[0-9a-f]{40}$/i;

const KIND_TO_RENDER = {
  video: RENDER.VIDEO,
  audio: RENDER.AUDIO,
  image: RENDER.IMAGE,
};

export function renderForKind(kind) {
  return KIND_TO_RENDER[kind] ?? RENDER.VIDEO;
}

export function createTorrentResolver({ engine, classify = () => 'video' }) {
  return {
    name: 'torrent',
    canHandle(input) {
      return input.startsWith('magnet:') || INFOHASH.test(input.trim());
    },
    resolve(source) {
      const uri = source.uri.startsWith('magnet:')
        ? source.uri
        : `magnet:?xt=urn:btih:${source.uri.trim()}`;

      return new Promise((resolve, reject) => {
        engine.add(uri, {
          onReady: (file) => {
            const render = renderForKind(classify(file.name));
            resolve(makePlayable({
              render,
              src: file.streamURL,
              mime: '',
              tier: TIER.DIRECT,
              cleanup: () => engine.destroyTorrent(),
            }));
          },
          onError: reject,
        });
      });
    },
  };
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 34`

- [ ] **Step 5: Commit**

```bash
git add stream/web/src/resolvers/torrent.js stream/web/src/resolvers/torrent.test.js
git commit -m "feat: torrent resolver wrapping the existing stream engine"
```

---

### Task 9: Player render dispatch

**Files:**
- Create: `stream/web/src/player.js`
- Test: `stream/web/src/player.test.js`

**Interfaces:**
- Consumes: `RENDER` from `./source.js`
- Produces: `renderPlayable(playable, elements)` → the element it attached to; `detachAll(elements)`

`elements` is `{video, audio, image, embed, canvas}` — DOM nodes supplied by the caller, so the module has no DOM dependency of its own and is testable with plain objects.

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/player.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderPlayable, detachAll } from './player.js';
import { makePlayable, RENDER } from './source.js';

function fakeElements() {
  const make = () => ({ src: '', hidden: true, removeAttribute(k) { this[k] = ''; } });
  return { video: make(), audio: make(), image: make(), embed: make(), canvas: make() };
}

test('each render kind attaches its own element and hides the others', () => {
  for (const kind of [RENDER.VIDEO, RENDER.AUDIO, RENDER.IMAGE, RENDER.EMBED]) {
    const els = fakeElements();
    const el = renderPlayable(
      makePlayable({ render: kind, src: 'http://a/b', mime: 'video/mp4' }), els,
    );
    assert.equal(el, els[kind], `${kind} attached the wrong element`);
    assert.equal(el.hidden, false);
    assert.equal(el.src, 'http://a/b');
    for (const [name, other] of Object.entries(els)) {
      if (name !== kind) assert.equal(other.hidden, true, `${name} should stay hidden`);
    }
  }
});

test('detachAll clears every source so nothing keeps streaming', () => {
  const els = fakeElements();
  renderPlayable(makePlayable({ render: RENDER.VIDEO, src: 'http://a/b', mime: 'v' }), els);
  detachAll(els);
  assert.equal(els.video.hidden, true);
  assert.equal(els.video.src, '');
});

test('an unknown element for a valid render kind throws rather than silently doing nothing', () => {
  const els = fakeElements();
  delete els.embed;
  assert.throws(
    () => renderPlayable(makePlayable({ render: RENDER.EMBED, src: 'x', mime: 'text/html' }), els),
    /no element/,
  );
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './player.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/player.js`:

```js
/**
 * Renders a Playable. Knows nothing about torrents, HTTP or playlists -- it
 * switches on `render` and attaches a source to the matching element. That is
 * the whole point of the descriptor: Ruffle and EmulatorJS will arrive as
 * `canvas` and need no change here.
 */

export function renderPlayable(playable, elements) {
  const el = elements[playable.render];
  if (!el) throw new Error(`no element for render kind: ${playable.render}`);

  detachAll(elements);
  el.src = playable.src;
  el.hidden = false;
  return el;
}

export function detachAll(elements) {
  for (const el of Object.values(elements)) {
    if (!el) continue;
    el.hidden = true;
    el.removeAttribute('src');
  }
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 37`

- [ ] **Step 5: Commit**

```bash
git add stream/web/src/player.js stream/web/src/player.test.js
git commit -m "feat: player render dispatch by descriptor"
```

---

### Task 10: LocalSourceStore on IndexedDB

**Files:**
- Create: `stream/web/src/store.js`
- Test: `stream/web/src/store.test.js`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `createMemoryStore()` → `{list, get, put, remove}` (used by tests and as the fallback when IndexedDB is unavailable, e.g. private browsing)
  - `createLocalStore(idbFactory)` → same interface, backed by IndexedDB
  - Both return promises. `put(record)` requires `record.id`.

Node has no IndexedDB, so the persistent implementation is verified against the shared contract test using the memory store, and the IndexedDB wiring is exercised by the browser smoke test in Task 12. `SyncedSourceStore` will implement this same interface in the next slice.

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/store.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createMemoryStore } from './store.js';

test('put then get round-trips a record', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', title: 'UK channels', sources: [] });
  assert.equal((await s.get('a')).title, 'UK channels');
});

test('put with the same id replaces rather than duplicating', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', title: 'one' });
  await s.put({ id: 'a', title: 'two' });
  const all = await s.list();
  assert.equal(all.length, 1);
  assert.equal(all[0].title, 'two');
});

test('remove deletes and get returns null for a missing id', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', title: 'one' });
  await s.remove('a');
  assert.equal(await s.get('a'), null);
  assert.deepEqual(await s.list(), []);
});

test('put rejects a record with no id', async () => {
  const s = createMemoryStore();
  await assert.rejects(() => s.put({ title: 'no id' }), /id/);
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './store.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/store.js`:

```js
/**
 * SourceStore: saved playlists and sources.
 *
 * Two implementations behind one interface. LocalSourceStore ships now;
 * SyncedSourceStore will implement the same four methods in the sync slice, so
 * nothing outside this file needs to know which is in use.
 */

const STORE = 'sources';

export function createMemoryStore() {
  const rows = new Map();
  return {
    async list() { return [...rows.values()]; },
    async get(id) { return rows.get(id) ?? null; },
    async put(record) {
      if (!record?.id) throw new Error('record needs an id');
      rows.set(record.id, record);
      return record;
    },
    async remove(id) { rows.delete(id); },
  };
}

export function createLocalStore(idbFactory = globalThis.indexedDB) {
  // Private browsing and some embedded webviews expose no IndexedDB at all.
  // Falling back keeps the app usable rather than throwing on first paint.
  if (!idbFactory) return createMemoryStore();

  const ready = new Promise((resolve, reject) => {
    const req = idbFactory.open('yarrit', 1);
    req.onupgradeneeded = () => {
      req.result.createObjectStore(STORE, { keyPath: 'id' });
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });

  const run = async (mode, fn) => {
    const db = await ready;
    return new Promise((resolve, reject) => {
      const tx = db.transaction(STORE, mode);
      const req = fn(tx.objectStore(STORE));
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  };

  return {
    async list() { return (await run('readonly', (s) => s.getAll())) ?? []; },
    async get(id) { return (await run('readonly', (s) => s.get(id))) ?? null; },
    async put(record) {
      if (!record?.id) throw new Error('record needs an id');
      await run('readwrite', (s) => s.put(record));
      return record;
    },
    async remove(id) { await run('readwrite', (s) => s.delete(id)); },
  };
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 41`

- [ ] **Step 5: Commit**

```bash
git add stream/web/src/store.js stream/web/src/store.test.js
git commit -m "feat: source store with memory and indexeddb implementations"
```

---

### Task 11: Library UI for collections

**Files:**
- Create: `stream/web/src/library.js`
- Modify: `stream/web/index.html` (add the library section markup and styles)
- Test: `stream/web/src/library.test.js`

**Interfaces:**
- Consumes: `isCollection` from `./source.js`
- Produces: `groupByCategory(collection)` → `Map<string, Source[]>`; `renderLibrary(collection, {mount, onPick})`

- [ ] **Step 1: Write the failing test**

Create `stream/web/src/library.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groupByCategory } from './library.js';
import { makeCollection, makeSource } from './source.js';

const src = (title, group) =>
  makeSource({ kind: 'url', uri: `http://s/${title}`, meta: { title, group } });

test('sources are grouped by their playlist group', () => {
  const c = makeCollection({
    title: 'IPTV',
    sources: [src('BBC', 'UK'), src('ITV', 'UK'), src('CNN', 'US')],
  });
  const groups = groupByCategory(c);
  assert.deepEqual([...groups.keys()], ['UK', 'US']);
  assert.equal(groups.get('UK').length, 2);
});

test('ungrouped channels collect under Ungrouped rather than an empty label', () => {
  const c = makeCollection({ title: 'IPTV', sources: [src('X', '')] });
  const groups = groupByCategory(c);
  assert.deepEqual([...groups.keys()], ['Ungrouped']);
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/web && npm test`
Expected: FAIL — `Cannot find module './library.js'`

- [ ] **Step 3: Write the implementation**

Create `stream/web/src/library.js`:

```js
/**
 * Browses a Collection. A playlist is frequently thousands of channels, so it
 * is grouped by category rather than rendered as one flat list.
 */

export function groupByCategory(collection) {
  const groups = new Map();
  for (const source of collection.sources) {
    const key = source.meta?.group || 'Ungrouped';
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(source);
  }
  return groups;
}

export function renderLibrary(collection, { mount, onPick }) {
  mount.textContent = '';
  for (const [group, sources] of groupByCategory(collection)) {
    const heading = document.createElement('h3');
    heading.className = 'lib-group';
    heading.textContent = `${group} · ${sources.length}`;
    mount.append(heading);

    const row = document.createElement('div');
    row.className = 'lib-row';
    for (const source of sources) {
      const btn = document.createElement('button');
      btn.className = 'lib-item';
      btn.type = 'button';
      btn.textContent = source.meta?.title || source.uri;
      btn.addEventListener('click', () => onPick(source));
      row.append(btn);
    }
    mount.append(row);
  }
  mount.hidden = false;
}
```

- [ ] **Step 4: Add the markup and styles**

In `stream/web/index.html`, add these rules immediately before the `@media (max-width:720px)` block:

```css
  /* ---------- library ---------- */
  #library { padding:8px 0 40px; }
  .lib-group { margin:22px 0 10px; font-size:13px; text-transform:uppercase;
               letter-spacing:.06em; color:var(--dim); font-weight:600; }
  .lib-row { display:grid; grid-template-columns:repeat(auto-fill,minmax(190px,1fr)); gap:10px; }
  .lib-item { text-align:left; padding:11px 14px; background:var(--panel);
              border:1px solid var(--line); border-radius:var(--r); color:var(--fg);
              cursor:pointer; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .lib-item:hover { border-color:var(--accent-dim); color:var(--accent); }
```

And add this element inside `<main>`, immediately before the closing `</main>` tag:

```html
  <section id="library" hidden></section>
```

- [ ] **Step 5: Run the tests and make sure they pass**

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 43`

- [ ] **Step 6: Commit**

```bash
git add stream/web/src/library.js stream/web/src/library.test.js stream/web/index.html
git commit -m "feat: library view for browsing playlist collections"
```

---

### Task 12: Wire the pipeline into main.js

Replaces the torrent-only `play()` with resolve-then-render, and adds `<audio>` and `<iframe>` so the non-video render paths have somewhere to land.

**Files:**
- Modify: `stream/web/src/main.js` — the `play()` function (locate by `function play(card, src) {`)
- Modify: `stream/web/index.html` (add `<audio>` and `<iframe>` to the player stage)

**Interfaces:**
- Consumes: everything built in Tasks 2-11
- Produces: `state.registry`, and a `play()` that accepts any source URI

- [ ] **Step 1: Add the missing player elements**

In `stream/web/index.html`, inside `<div class="stage">`, immediately after the `<img id="image" hidden alt="" />` line, add:

```html
    <audio id="audio" controls hidden></audio>
    <iframe id="embed" hidden allowfullscreen
            allow="autoplay; encrypted-media; picture-in-picture"
            referrerpolicy="strict-origin-when-cross-origin"></iframe>
```

And add this style next to the existing `video,#image` rule:

```css
  #embed { width:100%; height:100%; border:0; border-radius:9px; background:#000; }
  #audio { width:min(560px,90%); }
```

- [ ] **Step 2: Replace the imports at the top of main.js**

In `stream/web/src/main.js`, find the single existing import line and replace it. It reads:

```js
import { StreamEngine, classify, needsWebCodecs } from './engine.js';
```

to:

```js
import { StreamEngine, classify, needsWebCodecs } from './engine.js';
import { createRegistry, makeSource, isCollection } from './source.js';
import { createTorrentResolver } from './resolvers/torrent.js';
import { urlResolver } from './resolvers/url.js';
import { embedResolver } from './resolvers/embed.js';
import { playlistResolver } from './resolvers/playlist.js';
import { renderPlayable, detachAll } from './player.js';
import { renderLibrary } from './library.js';
import { PlaybackError } from './failures.js';
```

- [ ] **Step 3: Replace the play() function**

Replace the whole `play()` function — from `function play(card, src) {` through its closing brace, immediately before the `/**` comment introducing `attachMedia` — with:

```js
function playerElements() {
  return {
    video: $('#video'),
    audio: $('#audio'),
    image: $('#image'),
    embed: $('#embed'),
    canvas: null, // arrives with Ruffle and EmulatorJS
  };
}

function buildRegistry() {
  if (!state.engine) state.engine = new StreamEngine({ onStats: renderStats });
  window.__engine = state.engine; // diagnostics
  return createRegistry()
    .register(createTorrentResolver({ engine: state.engine, classify }))
    .register(embedResolver)      // before url: a YouTube link is also an http URL
    .register(playlistResolver)   // before url: .m3u8 is also an http URL
    .register(urlResolver);
}

/**
 * Resolve any source and render whatever comes back. `src.magnet` is still
 * honoured so existing search results keep working unchanged.
 */
async function play(card, src) {
  $('#player').hidden = false;
  $('#player-title').textContent = card.title + (card.year ? ` (${card.year})` : '');
  $('#player-sub').textContent = src.title ?? '';
  setPlayerStatus('Resolving…');

  const els = playerElements();
  detachAll(els);

  if (!state.registry) state.registry = buildRegistry();

  const uri = src.magnet ?? src.uri;
  try {
    const out = await state.registry.resolve(makeSource({ kind: 'auto', uri }));

    if (isCollection(out)) {
      $('#player').hidden = true;
      renderLibrary(out, {
        mount: $('#library'),
        onPick: (picked) => play({ title: picked.meta.title }, { uri: picked.uri }),
      });
      return;
    }

    if (needsWebCodecs(uri)) {
      setPlayerStatus('Unusual container — if this stalls, pick an MP4 source.');
    }
    const el = renderPlayable(out, els);
    state.playable = out;
    el.addEventListener('playing', () => setPlayerStatus(''), { once: true });
    if (out.render === 'image' || out.render === 'embed') setPlayerStatus('');
  } catch (err) {
    const why = err instanceof PlaybackError ? err.message : `Could not start: ${err.message}`;
    setPlayerStatus(why);
  }
}
```

- [ ] **Step 4: Release the playable when the player closes**

Find `function closePlayer(` in `stream/web/src/main.js` and add these two lines as the first statements in its body:

```js
  state.playable?.cleanup();
  state.playable = null;
```

- [ ] **Step 5: Build and verify no bundling errors**

Run: `cd stream/web && npm run build`
Expected: esbuild writes `dist/app.js` with no errors.

Run: `cd stream/web && npm test`
Expected: PASS — `# pass 43`

- [ ] **Step 6: Smoke-test the render paths in a real browser**

Deploy and check each path resolves. Run: `cd stream/web && bash deploy.sh`

Then in the browser console on `https://stream.moveweight.com`:

```js
// image via direct URL
await __registry.resolve({kind:'auto', uri:'https://stream.moveweight.com/icon-512.png', meta:{}})
// expect: {render:'image', mime:'image/png', tier:'direct', ...}
```

Expose the registry for this by adding `window.__registry = state.registry;` at the end of `buildRegistry()` before the `return`.

Verify by hand: a magnet from search still plays; a YouTube URL pasted into the magnet box renders in the iframe; an `.m3u` URL renders the library.

- [ ] **Step 7: Commit**

```bash
git add stream/web/src/main.js stream/web/index.html stream/web/dist
git commit -m "feat: route playback through the resolver registry"
```

---

### Task 13: IPTV sub-budget in the bridge

Relayed bytes are billed twice. A 1080p stream costs ~4.4 GiB per viewer-hour against a 2600 GiB cap that `budget.go` already notes is shared with the mail edge. Torrent relaying is bursty and finite per file; IPTV proxying is continuous and unbounded, so it must not draw on the same undifferentiated pool.

**Files:**
- Modify: `stream/bridge/budget.go`
- Modify: `stream/bridge/main.go` — the flag block in `func main()`, the server literal, and `handleHealth`
- Test: `stream/bridge/budget_test.go`

**Interfaces:**
- Consumes: existing `newBudget(path, cap)`, `(*budget).add`, `.degraded`, `.snapshot`
- Produces: `server.iptvBudget *budget`; flag `-iptv-budget-gib` (default 650); health JSON keys `iptv_budget_used`, `iptv_budget_cap`

- [ ] **Step 1: Write the failing test**

Create `stream/bridge/budget_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"
)

// The IPTV sub-cap exists so continuous video can never consume the whole
// month. Exhausting it must not degrade torrent relaying, which shares the box
// with the mail edge.
func TestIPTVBudgetIsIndependentOfTheMainBudget(t *testing.T) {
	dir := t.TempDir()
	main_ := newBudget(filepath.Join(dir, "main.json"), 1000)
	iptv := newBudget(filepath.Join(dir, "iptv.json"), 250)

	iptv.add(250)

	if !iptv.degraded() {
		t.Fatal("iptv budget should be degraded at its cap")
	}
	if main_.degraded() {
		t.Fatal("main budget must not degrade when only the iptv sub-cap is spent")
	}
}

func TestIPTVBudgetDefaultIsAQuarterOfTheMonthlyCap(t *testing.T) {
	if got := defaultIPTVBudgetGiB(2600); got != 650 {
		t.Fatalf("want 650, got %d", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/bridge && go test ./...`
Expected: FAIL — `undefined: defaultIPTVBudgetGiB`

- [ ] **Step 3: Add the helper to budget.go**

Append to `stream/bridge/budget.go`:

```go
// defaultIPTVBudgetGiB caps continuous IPTV proxying at a quarter of the
// monthly allowance. Torrent relaying is bursty and finite per file; IPTV is
// continuous and unbounded, so a single viewer could otherwise drain the month
// and take the mail edge on this box down with it.
func defaultIPTVBudgetGiB(monthlyGiB int64) int64 { return monthlyGiB / 4 }
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd stream/bridge && go test ./...`
Expected: PASS — `ok  	mw-bridge`

- [ ] **Step 5: Wire the flag and the second budget**

In `stream/bridge/main.go`, immediately after the `budgetGiB := flag.Int64(...)` line, add:

```go
	iptvBudgetGiB := flag.Int64("iptv-budget-gib", 0,
		"monthly IPTV proxy budget in GiB (0 = a quarter of -budget-gib)")
```

After `flag.Parse()` add:

```go
	if *iptvBudgetGiB == 0 {
		*iptvBudgetGiB = defaultIPTVBudgetGiB(*budgetGiB)
	}
```

In the `&server{...}` literal, on the line after `budget:`, add:

```go
		iptvBudget: newBudget(*statePath+".iptv", *iptvBudgetGiB<<30),
```

Add the field to the `server` struct definition, immediately after its existing `budget *budget` field:

```go
	iptvBudget *budget
```

- [ ] **Step 6: Expose it in health output**

In `handleHealth`, in the response map alongside the existing `"budget_cap"` entry, add:

```go
		"iptv_budget_used": iptvUsed,
		"iptv_budget_cap":  iptvCap,
```

and immediately above that map, add:

```go
	iptvUsed, iptvCap := s.iptvBudget.snapshot()
```

- [ ] **Step 7: Build and test**

Run: `cd stream/bridge && go build ./... && go test ./...`
Expected: build succeeds, `ok  	mw-bridge`

- [ ] **Step 8: Commit**

```bash
git add stream/bridge/budget.go stream/bridge/main.go stream/bridge/budget_test.go
git commit -m "feat: separate IPTV proxy budget so continuous video cannot drain the month"
```

---

### Task 14: The `/bridge/iptv` proxy endpoint

Tier 3 of the ladder is unreachable without this. The relay fetches the stream server-side and re-serves it over HTTPS with a CORS header, which fixes both browser blockers at once. It is also the most security-sensitive code in this plan: an open HTTP proxy on a box with a WireGuard tunnel into the home estate is an SSRF hole, so the existing `resolvesPublic` guard is mandatory and re-checked on every redirect.

**Files:**
- Create: `stream/bridge/iptv.go`
- Modify: `stream/bridge/main.go` — route registration in `func main()`
- Test: `stream/bridge/iptv_test.go`

**Interfaces:**
- Consumes: `resolvesPublic(host)` from `tracker.go`; `s.iptvBudget` from Task 13
- Produces: `GET /bridge/iptv?u=<url-encoded upstream>` → the upstream body with `Access-Control-Allow-Origin: *`; `(*server).handleIPTV`

- [ ] **Step 1: Write the failing test**

Create `stream/bridge/iptv_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func testServer(t *testing.T, capBytes int64) *server {
	t.Helper()
	return &server{iptvBudget: newBudget(filepath.Join(t.TempDir(), "i.json"), capBytes)}
}

// The box runs a WireGuard tunnel into the home estate, so a proxy that will
// fetch any URL is a route into the LAN. This must never regress.
func TestIPTVRefusesPrivateAddresses(t *testing.T) {
	s := testServer(t, 1<<30)
	for _, target := range []string{
		"http://192.168.0.115:9696/x.m3u8",
		"http://127.0.0.1:8801/x.m3u8",
		"http://10.0.0.5/x.m3u8",
		"http://[::1]/x.m3u8",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/bridge/iptv?u="+target, nil)
		s.handleIPTV(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d", target, rec.Code)
		}
	}
}

func TestIPTVRejectsMissingOrNonHTTPTarget(t *testing.T) {
	s := testServer(t, 1<<30)
	for _, q := range []string{"", "u=", "u=file:///etc/passwd", "u=gopher://x/1"} {
		rec := httptest.NewRecorder()
		s.handleIPTV(rec, httptest.NewRequest("GET", "/bridge/iptv?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: want 400, got %d", q, rec.Code)
		}
	}
}

// Exhausting the sub-cap must refuse new IPTV work. This is the failure that
// would otherwise drain the month and take the mail edge down with it.
func TestIPTVRefusesWhenSubCapExhausted(t *testing.T) {
	s := testServer(t, 100)
	s.iptvBudget.add(100)

	rec := httptest.NewRecorder()
	s.handleIPTV(rec, httptest.NewRequest("GET", "/bridge/iptv?u=http://example.com/a.m3u8", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 at the cap, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd stream/bridge && go test ./...`
Expected: FAIL — `s.handleIPTV undefined`

- [ ] **Step 3: Write the implementation**

Create `stream/bridge/iptv.go`:

```go
package main

import (
	"io"
	"net/http"
	"net/url"
)

// handleIPTV re-serves an upstream stream over HTTPS with a CORS header.
//
// It exists because two browser rules block most real IPTV and neither can be
// worked around client-side: an HTTPS page cannot load HTTP media, and most
// IPTV endpoints send no Access-Control-Allow-Origin. Fetching server-side
// fixes both.
//
// This is the last tier of the ladder precisely because it is the expensive
// one: every byte is billed twice and the allowance is shared with the mail
// edge, so it is guarded by its own sub-budget.
func (s *server) handleIPTV(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	if raw == "" {
		http.Error(w, "missing u", http.StatusBadRequest)
		return
	}
	target, err := url.Parse(raw)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		http.Error(w, "u must be an http(s) URL", http.StatusBadRequest)
		return
	}

	// SSRF guard. This box has a WireGuard tunnel into the home estate, so a
	// proxy that will fetch anything is a route into the LAN.
	if !resolvesPublic(target.Hostname()) {
		http.Error(w, "refusing non-public address", http.StatusForbidden)
		return
	}

	if s.iptvBudget.degraded() {
		http.Error(w, "iptv relay budget exhausted", http.StatusServiceUnavailable)
		return
	}

	client := &http.Client{
		Timeout: 0, // streams are long-lived; the request context governs lifetime
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			// Re-check on every hop: an upstream can redirect into the LAN.
			if !resolvesPublic(req.URL.Hostname()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "Yarr.It/1.0")

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)

	n, _ := io.Copy(w, resp.Body)
	// Billed twice: in from upstream, out to the client.
	s.iptvBudget.add(n * 2)
}
```

- [ ] **Step 4: Register the route**

In `stream/bridge/main.go`, immediately after the `mux.HandleFunc("/bridge/health", s.handleHealth)` line, add:

```go
	mux.HandleFunc("/bridge/iptv", s.handleIPTV)
```

- [ ] **Step 5: Run the tests and make sure they pass**

Run: `cd stream/bridge && go vet ./... && go test ./...`
Expected: vet clean, `ok  	mw-bridge`

- [ ] **Step 6: Commit**

```bash
git add stream/bridge/iptv.go stream/bridge/main.go stream/bridge/iptv_test.go
git commit -m "feat: iptv relay endpoint with SSRF guard and sub-budget accounting"
```

---

### Task 15: Documentation

**Files:**
- Modify: `stream/README.md`
- Modify: `stream/ARCHITECTURE.md` if present, else skip

- [ ] **Step 1: Document the source layer**

Add to `stream/README.md` immediately after the `## Components` table:

```markdown
## Sources

A source is anything a resolver claims. Each resolver answers `canHandle(input)`
and `resolve(source)`, returning either a `Playable` — a descriptor the player
renders, discriminated by `render` (`video｜audio｜image｜embed｜canvas`) — or a
`Collection`, a list the library browses. Playlists resolve to collections;
everything else resolves to a playable.

| Resolver | Input | Result |
|---|---|---|
| `torrent` | magnet, infohash | playable (wraps `StreamEngine`) |
| `playlist` | `.m3u`, `.m3u8` | collection, or a playable when the body is an HLS manifest |
| `embed` | YouTube, Vimeo | playable, iframe embed only — never extraction |
| `url` | direct media URL | playable, render chosen by content-type |

Playback climbs a ladder, cheapest first: direct, then the user's own LAN
gateway, then the public relay. Most real IPTV cannot play directly in a
browser — an HTTPS page cannot load HTTP media, and most IPTV endpoints send no
CORS header — so the ladder exists to route around both without showing the
user a dead end. TV clients use native players with neither restriction and
resolve at the direct tier.
```

- [ ] **Step 2: Run the full suite one last time**

Run: `cd stream/web && npm test && npm run build`
Expected: PASS, build clean.

Run: `cd stream/bridge && go test ./...` and `cd stream/search && go test ./...`
Expected: both `ok`.

- [ ] **Step 3: Commit**

```bash
git add stream/README.md
git commit -m "docs: describe the source layer and the playback ladder"
```

---

## Self-review notes

Checked against `docs/superpowers/specs/2026-07-27-universal-source-layer-design.md`:

- **Five modules** — `source.js` (T2), `resolvers/` (T6-T8), `player.js` (T9), `store.js` (T10), `library.js` (T11). Covered.
- **Five resolvers** — the spec lists `torrent`, `url`, `hls`, `m3u`, `embed`. `hls` and `m3u` are implemented as one `playlist` resolver because they share an extension and the disambiguation must happen after fetching the body; splitting them would mean fetching twice. Same behaviour, one fetch.
- **Ladder** — `chooseTier` (T4) and typed failures (T3). The relay's IPTV sub-cap is T13.
- **Collection vs Playable** — T2 types, T7 resolver, T11 UI.
- **`LocalSourceStore`** — T10, with the memory fallback for environments without IndexedDB.
- **Privacy-policy rewrite** — correctly absent: the spec marks it a prerequisite of the *sync* slice, and this slice adds no accounts.
- **Test harness** — no JS runner existed, so T1 adds one before any TDD task.

Naming is consistent across tasks: `makePlayable`/`makeSource`/`makeCollection`, `RENDER`/`TIER`/`FAILURE`, `canHandle`/`resolve`, `list`/`get`/`put`/`remove`.

- **Relay tier (tier 3)** — T13 adds the sub-budget, T14 adds the `/bridge/iptv` endpoint it governs. Self-review initially deferred the endpoint, which would have left the ladder unable to reach its own last tier; the task was added rather than shipping the hole. It reuses `resolvesPublic` from `tracker.go` and re-checks on every redirect, because an upstream can redirect into the LAN across the WireGuard tunnel.

Task count: 15. Steps contain complete code throughout; no step defers work to a later reader.
