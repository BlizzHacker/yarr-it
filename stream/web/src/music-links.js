/**
 * Spotify and Apple Music links.
 *
 * This is not a Spotify downloader and the UI is written so nobody could
 * mistake it for one. Their audio is DRM-protected; extracting it means
 * defeating that protection, which is not a feature this will ever have.
 *
 * What it does instead is the thing that is actually useful: read the public
 * track list, and hand each title to the search Yarr.It already has. The
 * wording matters as much as the behaviour here -- "we looked these up for
 * you" is true, "downloading from Spotify" would not be, and a tool that
 * implies the second while doing the first is lying about the one thing the
 * reader most needs to understand.
 */

const SPOTIFY_HOSTS = new Set(['open.spotify.com', 'play.spotify.com']);
const APPLE_HOSTS = new Set(['music.apple.com', 'itunes.apple.com', 'embed.music.apple.com']);

/**
 * Is this a music-service link?
 *
 * Matched on the parsed hostname, never on a substring of the raw text, so
 * https://evil.example/open.spotify.com/track/x is not treated as Spotify.
 */
export function isMusicLink(uri) {
  try {
    const url = new URL(uri);
    if (!/^https?:$/.test(url.protocol)) return false;
    const host = url.hostname.toLowerCase();
    if (SPOTIFY_HOSTS.has(host)) {
      return /^\/(intl-[a-z]{2}\/)?(track|album|playlist|artist)\//.test(url.pathname);
    }
    if (APPLE_HOSTS.has(host)) {
      return /^\/[a-z]{2}\/(album|playlist|song|artist)\//.test(url.pathname);
    }
    return false;
  } catch {
    return false;
  }
}

export function musicProvider(uri) {
  try {
    const host = new URL(uri).hostname.toLowerCase();
    if (SPOTIFY_HOSTS.has(host)) return 'Spotify';
    if (APPLE_HOSTS.has(host)) return 'Apple Music';
  } catch {
    /* not a URL */
  }
  return '';
}

export async function fetchMusicLink(uri, { fetchImpl = fetch, signal } = {}) {
  const res = await fetchImpl(`/api/link/music?u=${encodeURIComponent(uri)}`, { signal });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(body.error || 'could not read that link');
    err.code = body.code;
    throw err;
  }
  return body;
}

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

export function formatTrackDuration(seconds) {
  if (!seconds || seconds <= 0) return '';
  const s = Math.round(seconds);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

/**
 * Render a resolved music link.
 *
 * `onSearch(query)` runs Yarr.It's ordinary search. Every row is a search, not
 * a download -- there is no button here that claims to fetch anything from
 * Spotify, because there is no such thing to claim.
 */
export function renderMusicLink(result, { mount, onSearch }) {
  mount.replaceChildren();
  mount.hidden = false;

  const head = el('div', 'link-head');
  if (result.artwork) {
    const img = el('img', 'link-thumb');
    img.src = result.artwork;
    img.alt = '';
    img.loading = 'lazy';
    head.append(img);
  }
  const txt = el('div', 'link-headtext');
  txt.append(el('h3', null, result.title || 'Unknown'));
  if (result.artist) txt.append(el('div', 'link-sub', result.artist));
  head.append(txt);
  mount.append(head);

  // The disclaimer comes from the server and is rendered before anything
  // clickable. It is not a footnote and it is not collapsible.
  mount.append(el('p', 'link-disclaimer', result.disclaimer));

  for (const note of result.notes || []) mount.append(el('p', 'link-note', note));

  if (result.query && result.tracks?.length > 1) {
    const all = el('button', 'btn btn-primary', `Search Yarr.It for “${result.title}”`);
    all.type = 'button';
    all.addEventListener('click', () => onSearch?.(result.query));
    mount.append(all);
  }

  mount.append(el('h4', 'link-h', `We looked up ${result.tracks?.length ?? 0} track${
    result.tracks?.length === 1 ? '' : 's'} for you`));

  const list = el('div', 'link-rows');
  for (const track of result.tracks || []) {
    const row = el('div', 'link-row');
    const left = el('div', 'link-left');
    left.append(el('span', 'link-q', track.title));
    if (track.artist) left.append(el('span', 'link-ext', track.artist));
    row.append(left);

    const right = el('div', 'link-right');
    const dur = formatTrackDuration(track.duration);
    if (dur) right.append(el('span', 'link-size', dur));
    // "Search" and nothing else. A button labelled "Get" or "Download" here
    // would be the lie this whole module exists to avoid.
    const btn = el('button', 'link-play', 'Search');
    btn.type = 'button';
    btn.title = `Search Yarr.It's indexers for “${track.query}”`;
    btn.addEventListener('click', () => onSearch?.(track.query));
    right.append(btn);
    row.append(right);

    list.append(row);
  }
  mount.append(list);
  return mount;
}
