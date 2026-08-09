import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  fetchMusicLink, formatTrackDuration, isMusicLink, musicProvider, renderMusicLink,
} from './music-links.js';

test('Spotify and Apple Music links are recognised', () => {
  for (const uri of [
    'https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT',
    'https://open.spotify.com/album/4LH4d3cOWNNsVw41Gqt2kv?si=x',
    'https://open.spotify.com/intl-de/track/4cOdK2wGLETKBW3PvgPWqT',
    'https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M',
    'https://music.apple.com/us/album/the-dark-side-of-the-moon/1065973699',
    'https://music.apple.com/gb/song/time/1065973706',
  ]) {
    assert.equal(isMusicLink(uri), true, `${uri} should be recognised`);
  }
});

test('non-music links are not claimed', () => {
  for (const uri of [
    'https://www.youtube.com/watch?v=abc',
    'https://open.spotify.com/',
    'magnet:?xt=urn:btih:abc',
    'not a url',
  ]) {
    assert.equal(isMusicLink(uri), false, `${uri} should not be claimed`);
  }
});

// The host has to be theirs, not merely mentioned. Matching on a substring
// would let any site claim to be Spotify.
test('a lookalike host is not treated as Spotify', () => {
  assert.equal(isMusicLink('https://evil.example.com/open.spotify.com/track/abc'), false);
  assert.equal(isMusicLink('https://open.spotify.com.evil.example/track/abc'), false);
  assert.equal(isMusicLink('http://music.apple.com.attacker.net/us/album/x/1'), false);
});

test('the provider is named for the UI', () => {
  assert.equal(musicProvider('https://open.spotify.com/track/abc'), 'Spotify');
  assert.equal(musicProvider('https://music.apple.com/us/album/x/1'), 'Apple Music');
  assert.equal(musicProvider('https://youtube.com/watch?v=1'), '');
});

test('track durations render as minutes and seconds', () => {
  assert.equal(formatTrackDuration(64.333), '1:04');
  assert.equal(formatTrackDuration(422), '7:02');
  assert.equal(formatTrackDuration(0), '');
});

test('a music fetch failure carries the server code', async () => {
  const fetchImpl = async () => ({
    ok: false, status: 502,
    json: async () => ({ error: 'could not read the public track list', code: 'extractor_broken' }),
  });
  await assert.rejects(
    () => fetchMusicLink('https://open.spotify.com/album/x', { fetchImpl }),
    (err) => err.code === 'extractor_broken',
  );
});

// ------------------------------------------------------------ the wording --

// No jsdom in this project, so the DOM is faked. What is being asserted is
// the copy and the button labels, which is the part that has to be honest.
function fakeMount() {
  const make = (tag) => ({
    tag,
    className: '',
    textContent: '',
    children: [],
    hidden: false,
    title: '',
    src: '',
    alt: '',
    loading: '',
    type: '',
    append(...kids) { this.children.push(...kids); },
    replaceChildren() { this.children = []; },
    addEventListener(name, fn) { this.handler = fn; },
    setAttribute() {},
  });
  const original = globalThis.document;
  globalThis.document = { createElement: make };
  const mount = make('div');
  return { mount, restore: () => { globalThis.document = original; } };
}

function allText(node) {
  return [node.textContent, ...node.children.map(allText)].join(' ');
}

function allNodes(node) {
  return [node, ...node.children.flatMap(allNodes)];
}

const result = {
  provider: 'spotify', kind: 'album',
  title: 'The Dark Side of the Moon', artist: 'Pink Floyd',
  disclaimer: 'Spotify and Apple Music streams are DRM-protected — nothing is downloaded from them. '
    + "This reads the public track list and searches Yarr.It's own indexers for each title.",
  query: 'Pink Floyd The Dark Side of the Moon',
  tracks: [
    { title: 'Speak to Me', artist: 'Pink Floyd', duration: 64, query: 'Pink Floyd Speak to Me' },
    { title: 'Time', artist: 'Pink Floyd', duration: 422, query: 'Pink Floyd Time' },
  ],
};

// The prose is allowed to say "nothing is downloaded" -- that is the honest
// claim. What must never happen is an ACTION offering a download, because a
// button is a promise and this one could not be kept.
test('no clickable element offers to download anything from Spotify', () => {
  const { mount, restore } = fakeMount();
  try {
    renderMusicLink(result, { mount, onSearch: () => {} });
    const actions = allNodes(mount).filter((n) => n.tag === 'button' || n.tag === 'a');
    assert.ok(actions.length > 0, 'nothing was rendered to check');
    for (const a of actions) {
      const label = `${a.textContent} ${a.title}`.toLowerCase();
      for (const forbidden of ['download', 'convert', 'save', 'get mp3', 'rip']) {
        assert.ok(
          !label.includes(forbidden),
          `an action read "${a.textContent}", which promises something this does not do`,
        );
      }
    }
  } finally {
    restore();
  }
});

test('the prose only uses the word download to deny it', () => {
  const { mount, restore } = fakeMount();
  try {
    renderMusicLink(result, { mount, onSearch: () => {} });
    const text = allText(mount).toLowerCase();
    const idx = text.indexOf('download');
    assert.ok(idx >= 0, 'the disclaimer should address downloading head-on');
    assert.match(text.slice(Math.max(0, idx - 20), idx + 12), /nothing is download/);
  } finally {
    restore();
  }
});

test('every track action is labelled as a search', () => {
  const { mount, restore } = fakeMount();
  try {
    renderMusicLink(result, { mount, onSearch: () => {} });
    const buttons = allNodes(mount).filter((n) => n.tag === 'button');
    assert.ok(buttons.length >= 2, 'no track actions were rendered');
    for (const b of buttons) {
      assert.match(b.textContent, /Search/i,
        `a button read "${b.textContent}" instead of naming the search`);
    }
  } finally {
    restore();
  }
});

test('the DRM disclaimer is rendered, not left to a footnote', () => {
  const { mount, restore } = fakeMount();
  try {
    renderMusicLink(result, { mount, onSearch: () => {} });
    const text = allText(mount);
    assert.match(text, /DRM-protected/);
    assert.match(text, /nothing is downloaded from them/);
  } finally {
    restore();
  }
});

test('the panel says the lookup is a lookup', () => {
  const { mount, restore } = fakeMount();
  try {
    renderMusicLink(result, { mount, onSearch: () => {} });
    assert.match(allText(mount), /We looked up 2 tracks for you/);
  } finally {
    restore();
  }
});

test('clicking a track runs that track s search query', () => {
  const { mount, restore } = fakeMount();
  try {
    const seen = [];
    renderMusicLink(result, { mount, onSearch: (q) => seen.push(q) });
    const buttons = allNodes(mount).filter((n) => n.tag === 'button');
    for (const b of buttons) b.handler?.();
    assert.ok(seen.includes('Pink Floyd Speak to Me'));
    assert.ok(seen.includes('Pink Floyd Time'));
  } finally {
    restore();
  }
});

test('a single-track link renders without an album-wide search button', () => {
  const { mount, restore } = fakeMount();
  try {
    renderMusicLink({ ...result, tracks: [result.tracks[0]] }, { mount, onSearch: () => {} });
    const buttons = allNodes(mount).filter((n) => n.tag === 'button');
    assert.equal(buttons.length, 1, 'a one-track link needs one action, not two');
  } finally {
    restore();
  }
});
