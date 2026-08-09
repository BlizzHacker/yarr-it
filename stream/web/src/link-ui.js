/**
 * The paste-a-link panel.
 *
 * The surface is copied from fdown.net and snap-insta.to on purpose: one box,
 * one paste, no network picker, no account. That shape is the entire reason
 * people use those sites instead of a real tool, and adding a "choose your
 * network" dropdown would already be a worse product than what they have.
 *
 * What is deliberately NOT copied: the interstitial, the fake download button
 * that is really an ad, the second click that opens a new tab, and the quality
 * list that says "HD" and means whatever came back. Every row here is a real
 * format with a real size, and the primary button is the actual file.
 *
 * DOM handling is kept thin and all the decisions live in link-format.js,
 * which is where the tests are -- this project has no jsdom.
 */

import {
  AUDIO, audioState, badges, downloadUrl, failureMessage, formatDuration,
  groupFormats, isOurFault, isRetryable, isSaveable, playbackWarning,
  qualityLabel, sizeLabel, summarise,
} from './link-format.js';

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

/**
 * Render a resolved link.
 *
 * `onPlay(format)` is called for the play action; `onOpenCollection(item)` for
 * an entry in a playlist/carousel result.
 */
export function renderLink(resolved, { mount, onPlay, onOpenCollection, onRetry }) {
  mount.replaceChildren();
  mount.hidden = false;

  if (resolved.kind === 'collection') {
    renderCollection(resolved, { mount, onOpenCollection });
    return mount;
  }

  mount.append(header(resolved));

  const { complete, audioOnly, silent } = groupFormats(resolved.formats || []);
  const best = resolved.formats?.[resolved.best];

  if (best) {
    mount.append(primary(best, resolved, onPlay));
  } else {
    // No safe default is a real state, not an error. It is what Vimeo and
    // Reddit look like on a server with no ffmpeg, and the visitor is told
    // rather than handed a silent file with a confident-looking button.
    const warn = el('p', 'link-warn',
      'Nothing here has both picture and sound in one file. Pick a video track and an audio track below and join them yourself.');
    mount.append(warn);
  }

  for (const note of resolved.notes || []) {
    mount.append(el('p', 'link-note', note));
  }

  section(mount, 'All qualities', complete, resolved, onPlay);
  section(mount, 'Audio only', audioOnly, resolved, onPlay);
  // Silent tracks get an explicit, unmissable heading. They are not hidden --
  // somebody may want a video-only file -- but nobody should reach one while
  // scanning for "the big one".
  section(mount, 'Video only — these have NO SOUND', silent, resolved, onPlay, 'link-danger');

  if (onRetry) mount.append(el('div', 'link-actions'));
  return mount;
}

function header(resolved) {
  const head = el('div', 'link-head');
  if (resolved.thumbnail) {
    const img = el('img', 'link-thumb');
    img.src = resolved.thumbnail;
    img.alt = '';
    img.loading = 'lazy';
    head.append(img);
  }
  const txt = el('div', 'link-headtext');
  txt.append(el('h3', null, resolved.title || 'Untitled'));
  const sub = [resolved.uploader, summarise(resolved)].filter(Boolean).join(' · ');
  if (sub) txt.append(el('div', 'link-sub', sub));
  if (resolved.isLive) {
    txt.append(el('span', 'tag warn', 'LIVE — nothing to save until it ends'));
  }
  head.append(txt);
  return head;
}

/**
 * The one big button. It is the download, not an ad, and it names exactly what
 * will land: quality, container, size, and whether it has sound.
 */
function primary(format, resolved, onPlay) {
  const box = el('div', 'link-primary');

  const dl = el('a', 'btn btn-primary');
  const href = downloadUrl(format);
  if (href) {
    dl.href = href;
    dl.setAttribute('download', '');
  } else {
    dl.classList.add('disabled');
  }
  dl.textContent = `Download ${qualityLabel(format)} · ${sizeLabel(format)}`;
  box.append(dl);

  const play = el('button', 'btn btn-ghost', 'Play here');
  play.type = 'button';
  play.addEventListener('click', () => onPlay?.(format, resolved));
  box.append(play);

  const state = audioState(format);
  if (state === AUDIO.JOINED) {
    box.append(el('span', 'link-hint',
      'Picture and sound arrive as separate tracks from this site and are joined as it downloads.'));
  } else if (state === AUDIO.UNKNOWN) {
    box.append(el('span', 'link-hint',
      'This network does not publish codec details, so we cannot promise the audio track — it almost always is there.'));
  }
  const warn = playbackWarning(format);
  if (warn) box.append(el('span', 'link-hint', warn));
  return box;
}

function section(mount, title, formats, resolved, onPlay, cls) {
  if (!formats.length) return;
  mount.append(el('h4', cls ? `link-h ${cls}` : 'link-h', title));
  const list = el('div', 'link-rows');
  for (const f of formats) list.append(row(f, resolved, onPlay));
  mount.append(list);
}

function row(format, resolved, onPlay) {
  const r = el('div', 'link-row');

  const left = el('div', 'link-left');
  left.append(el('span', 'link-q', qualityLabel(format)));
  left.append(el('span', 'link-ext', format.ext || ''));
  for (const b of badges(format)) {
    const tag = el('span', `tag ${b.kind}`, b.text);
    if (b.title) tag.title = b.title;
    left.append(tag);
  }
  r.append(left);

  const right = el('div', 'link-right');
  right.append(el('span', 'link-size', sizeLabel(format)));

  const play = el('button', 'link-play', 'Play');
  play.type = 'button';
  play.addEventListener('click', () => onPlay?.(format, resolved));
  right.append(play);

  const href = downloadUrl(format);
  if (href) {
    const dl = el('a', 'link-dl', 'Save');
    dl.href = href;
    dl.setAttribute('download', '');
    right.append(dl);
  } else {
    // A bare manifest cannot be saved. Saying so is better than a button that
    // downloads a three-kilobyte playlist and calls it a video.
    const no = el('span', 'link-nodl', 'not saveable');
    no.title = 'This is a segmented stream, not a single file.';
    right.append(no);
  }
  r.append(right);
  return r;
}

function renderCollection(resolved, { mount, onOpenCollection }) {
  mount.append(el('h3', null, resolved.title || 'Collection'));
  mount.append(el('p', 'link-sub', `${resolved.items.length} items — pick one to see its formats.`));
  for (const note of resolved.notes || []) mount.append(el('p', 'link-note', note));

  const list = el('div', 'link-rows');
  for (const item of resolved.items) {
    const b = el('button', 'link-row link-item', item.title || item.url);
    b.type = 'button';
    if (item.duration) {
      b.append(el('span', 'link-size', formatDuration(item.duration)));
    }
    b.addEventListener('click', () => onOpenCollection?.(item));
    list.append(b);
  }
  mount.append(list);
}

/**
 * Render a failure.
 *
 * The whole point of the panel is that this is specific. "Could not download"
 * is what the competitors say; it tells the reader nothing and it makes an
 * outage on our side look like their mistake.
 */
export function renderLinkError(error, { mount, onRetry }) {
  mount.replaceChildren();
  mount.hidden = false;

  mount.append(el('p', 'link-fail', failureMessage(error)));

  if (isOurFault(error)) {
    mount.append(el('p', 'link-note',
      'This one is on us, not you — the link is probably fine.'));
  }
  // The extractor name and the site's own words are shown rather than logged
  // away. When a network changes something, this line is the bug report.
  const detail = [error?.extractor && `reader: ${error.extractor}`, error?.upstream]
    .filter(Boolean).join(' — ');
  if (detail) {
    const d = el('details', 'link-detail');
    d.append(el('summary', null, 'Technical detail'));
    d.append(el('p', null, detail));
    mount.append(d);
  }
  if (isRetryable(error) && onRetry) {
    const b = el('button', 'btn btn-ghost', 'Try again');
    b.type = 'button';
    b.addEventListener('click', onRetry);
    mount.append(b);
  }
  return mount;
}

/**
 * The "how do I get the link" hint.
 *
 * Both reference sites ship a whole page for this, because getting a shareable
 * URL out of a mobile app genuinely is not obvious. One line inline beats a
 * page nobody clicks through to.
 */
export const PASTE_HELP =
  'On a phone: tap Share on the post, then "Copy link", and paste it here. ' +
  'Works with Facebook, Instagram, TikTok, YouTube, X, Reddit, Vimeo, Dailymotion, Twitch and many more.';

export { isSaveable };
