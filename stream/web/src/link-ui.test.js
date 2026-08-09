import { test } from 'node:test';
import assert from 'node:assert/strict';
import { PASTE_HELP, renderLink, renderLinkError } from './link-ui.js';

// No jsdom in this project (see library.test.js), so the DOM is a plain fake
// covering only what these functions touch.
function fakeMount() {
  const make = (tag) => ({
    tag,
    className: '',
    textContent: '',
    href: '',
    title: '',
    src: '',
    alt: '',
    loading: '',
    type: '',
    hidden: false,
    attrs: {},
    children: [],
    classList: {
      _s: new Set(),
      add(c) { this._s.add(c); },
      contains(c) { return this._s.has(c); },
    },
    append(...kids) { this.children.push(...kids); },
    replaceChildren() { this.children = []; },
    addEventListener(_n, fn) { this.handler = fn; },
    setAttribute(k, v) { this.attrs[k] = v; },
  });
  const original = globalThis.document;
  globalThis.document = { createElement: make };
  return { mount: make('div'), restore: () => { globalThis.document = original; } };
}

const nodes = (n) => [n, ...n.children.flatMap(nodes)];
const text = (n) => [n.textContent, ...n.children.map(text)].join(' ');
const actions = (n) => nodes(n).filter((x) => x.tag === 'button' || x.tag === 'a');

const silent1080 = {
  id: '299', label: '1080p60', ext: 'mp4', height: 1080, vcodec: 'H.264',
  hasVideo: true, hasAudio: false, audioKnown: true,
  sizeHuman: '245.7 MiB', media: '/api/link/media?t=silent',
};
const good360 = {
  id: '18', label: '360p', ext: 'mp4', height: 360, vcodec: 'H.264', acodec: 'AAC',
  hasVideo: true, hasAudio: true, audioKnown: true,
  sizeHuman: '27.2 MiB', media: '/api/link/media?t=good',
};
const audioOnly = {
  id: '140', label: 'audio 129k', ext: 'm4a', acodec: 'AAC',
  hasVideo: false, hasAudio: true, audioKnown: true,
  sizeHuman: '9.8 MiB', media: '/api/link/media?t=audio',
};

const youtube = {
  kind: 'video', title: 'Big Buck Bunny', uploader: 'Blender', extractor: 'youtube',
  duration: 635, best: 1, formats: [audioOnly, good360, silent1080],
  notes: ['22 carry no audio track and could not be paired with one.'],
};

test('the primary button is the download and it names size and quality', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLink(youtube, { mount, onPlay: () => {} });
    const primary = actions(mount).find((a) => a.className.includes('btn-primary'));
    assert.ok(primary, 'no primary action rendered');
    assert.match(primary.textContent, /Download 360p/);
    assert.match(primary.textContent, /27\.2 MiB/);
    assert.equal(primary.href, '/api/link/media?t=good&dl=1');
    assert.equal(primary.attrs.download, '');
  } finally {
    restore();
  }
});

// The competitors' whole business model is that the big button is an advert.
// This asserts it is the file.
test('the primary button points at the media, not at another page', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLink(youtube, { mount, onPlay: () => {} });
    for (const a of actions(mount).filter((x) => x.tag === 'a')) {
      assert.match(a.href, /^\/api\/link\/media\?/,
        `a link pointed somewhere other than the media: ${a.href}`);
      assert.ok(!a.attrs.target, 'nothing here should open a new tab');
    }
  } finally {
    restore();
  }
});

test('the default offered is never the silent 1080p', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLink(youtube, { mount, onPlay: () => {} });
    const primary = actions(mount).find((a) => a.className.includes('btn-primary'));
    assert.ok(!/1080p/.test(primary.textContent),
      'the silent track was offered as the headline download');
  } finally {
    restore();
  }
});

test('silent tracks get their own unmissable heading', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLink(youtube, { mount, onPlay: () => {} });
    const heading = nodes(mount).find((n) => /NO SOUND/.test(n.textContent) && n.tag === 'h4');
    assert.ok(heading, 'video-only tracks were not separated under their own heading');
    assert.ok(heading.className.includes('link-danger'));
  } finally {
    restore();
  }
});

test('the server notes are shown rather than dropped', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLink(youtube, { mount, onPlay: () => {} });
    assert.match(text(mount), /could not be paired/);
  } finally {
    restore();
  }
});

// best === -1 is a real state, not a crash: it is what a split-track network
// looks like on a server without ffmpeg.
test('no safe default is explained instead of throwing', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLink({ ...youtube, best: -1, formats: [silent1080, audioOnly], notes: [] },
      { mount, onPlay: () => {} });
    assert.match(text(mount), /Nothing here has both picture and sound/);
    const primary = actions(mount).find((a) => a.className.includes('btn-primary'));
    assert.equal(primary, undefined, 'a confident download button was offered with no safe default');
  } finally {
    restore();
  }
});

test('a play action passes the chosen format back', () => {
  const { mount, restore } = fakeMount();
  try {
    const picked = [];
    renderLink(youtube, { mount, onPlay: (f) => picked.push(f.id) });
    for (const b of actions(mount).filter((a) => a.tag === 'button')) b.handler?.();
    assert.ok(picked.includes('18'));
    assert.ok(picked.includes('299'), 'a silent track is still playable if explicitly chosen');
  } finally {
    restore();
  }
});

test('a bare manifest shows "not saveable" rather than a broken download', () => {
  const { mount, restore } = fakeMount();
  try {
    const manifest = {
      id: 'hls', label: '720p', ext: 'mp4', height: 720, hasVideo: true, hasAudio: true,
      audioKnown: true, streaming: true, sizeHuman: 'size unknown (stream)', media: '',
    };
    renderLink({ ...youtube, best: 0, formats: [manifest], notes: [] },
      { mount, onPlay: () => {} });
    assert.match(text(mount), /not saveable/);
  } finally {
    restore();
  }
});

test('a collection lists its items instead of pretending to be one file', () => {
  const { mount, restore } = fakeMount();
  try {
    const picked = [];
    renderLink({
      kind: 'collection', title: 'A playlist',
      items: [
        { title: 'One', url: 'https://x/1', duration: 60 },
        { title: 'Two', url: 'https://x/2' },
      ],
      notes: ['that link holds 80 items — showing the first 60'],
    }, { mount, onOpenCollection: (i) => picked.push(i.url) });

    assert.match(text(mount), /2 items/);
    assert.match(text(mount), /showing the first 60/);
    for (const b of actions(mount)) b.handler?.();
    assert.deepEqual(picked, ['https://x/1', 'https://x/2']);
  } finally {
    restore();
  }
});

// --------------------------------------------------------------- errors ---

test('an error names what happened rather than saying it failed', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLinkError({ code: 'no_video_in_post', extractor: 'Instagram',
      upstream: '[Instagram] x: There is no video in this post' }, { mount });
    const t = text(mount);
    assert.match(t, /photos/);
    assert.match(t, /signed-in/);
    assert.ok(!/could not download/i.test(t));
  } finally {
    restore();
  }
});

test('the upstream detail is available rather than only logged', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLinkError({ code: 'extractor_broken', extractor: 'facebook',
      upstream: '[facebook] 123: Cannot parse data' }, { mount });
    const t = text(mount);
    assert.match(t, /reader: facebook/);
    assert.match(t, /Cannot parse data/, 'the site’s own words are the bug report');
  } finally {
    restore();
  }
});

test('our own breakage is owned in the copy', () => {
  const { mount, restore } = fakeMount();
  try {
    renderLinkError({ code: 'extractor_broken' }, { mount });
    assert.match(text(mount), /on us, not you/);
  } finally {
    restore();
  }
});

test('retry is offered only where retrying could help', () => {
  for (const [code, expected] of [['timeout', true], ['private', false]]) {
    const { mount, restore } = fakeMount();
    try {
      renderLinkError({ code }, { mount, onRetry: () => {} });
      const retry = actions(mount).find((a) => /Try again/.test(a.textContent));
      assert.equal(Boolean(retry), expected, `retry presence wrong for ${code}`);
    } finally {
      restore();
    }
  }
});

test('the paste hint names the networks and how to get a link on a phone', () => {
  assert.match(PASTE_HELP, /Copy link/);
  for (const network of ['Facebook', 'Instagram', 'TikTok', 'YouTube', 'X', 'Reddit']) {
    assert.ok(PASTE_HELP.includes(network), `${network} is not mentioned in the hint`);
  }
});
