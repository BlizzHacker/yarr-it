/**
 * The home screen, one section per media domain.
 *
 * The landing page used to be whatever /api/discover happened to return, in
 * whatever order the server built it, with no statement anywhere about what
 * kind of thing each row held. That made it a video site with some other rows
 * bolted on: a comic and a film were the same tile, offered with the same
 * unspoken "watch", and the only route to a book was to type one into a search
 * box.
 *
 * So the structure here comes from the vocabulary rather than from the
 * response. `allDomains()` decides which sections exist and in what order,
 * `labelFor()` names them and `verbFor()` says what you do with a card in one.
 * Add a domain to schema.json and a section appears; nothing in this file
 * enumerates domains, because a hand-kept list in the client is precisely the
 * defect that let the browser send `groups=movies` at a server comparing
 * against `video` and filter every card out while reporting hundreds of hits.
 *
 * WHERE EACH SECTION'S CONTENT COMES FROM
 *
 * Two sources, preferred in this order:
 *
 *   1. /api/discover rows attributed to the domain. These are curated, they
 *      carry real artwork, and the server caches them for three hours, so the
 *      page paints in about a third of a second.
 *   2. a browse -- /api/search?kind=<domain> with no query -- for any domain
 *      discover did not cover. A cold browse costs a full indexer fan-out
 *      (20-45s measured), a warm one about 0.3s, so this is the fallback and
 *      not the first choice.
 *
 * A section that ends with nothing in it is removed rather than drawn empty.
 * An empty shelf for a service the visitor does not run is a worse answer than
 * no shelf: it says something exists and then refuses to produce it.
 */

import {
  allDomains, canonicalDomain, labelFor, verbFor,
} from './schema.js';

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

// --------------------------------------------------------------- attribution

/**
 * Resolve a piece of free text onto a canonical domain.
 *
 * Every candidate token is passed through `canonicalDomain`, so the vocabulary
 * stays in schema.json and this function only decides *which strings to try*.
 *
 * Right to left, because the specific word is at the end of the names the
 * server actually uses: "ia-books", "popular-movies", "games-classics". Read
 * left to right, "popular-movies" would resolve on "popular" if that ever
 * became an alias, and "movie-soundtracks" would resolve to video rather than
 * music.
 */
export function domainInText(text) {
  const parts = String(text ?? '').split(/[^a-z0-9]+/i).filter(Boolean);
  for (let i = parts.length - 1; i >= 0; i--) {
    const d = canonicalDomain(parts[i]);
    if (d) return d;
  }
  return '';
}

/**
 * Which domain a /api/discover row belongs to.
 *
 * `row.domain` is checked first and is the only one of these that is not a
 * guess. The server does not send it yet; when it does, everything below stops
 * being consulted. Until then the row's key is the best signal, its title the
 * next, and its items' `mediaType` the last.
 *
 * mediaType is last deliberately, because it cannot answer the question this
 * page is about. Both the books row and the comics row are `text`, which
 * resolves to nothing, and the audiobooks row is `audio`, which resolves to
 * music -- while schema.json files audiobooks under literature on purpose,
 * since an audiobook is an edition of a book and shares its author. Trusting
 * mediaType would put Books nowhere and Audiobooks under Music.
 */
export function domainOfRow(row) {
  if (!row) return '';
  const explicit = canonicalDomain(row.domain);
  if (explicit) return explicit;

  const fromKey = domainInText(row.key);
  if (fromKey) return fromKey;

  const fromTitle = domainInText(row.title);
  if (fromTitle) return fromTitle;

  for (const item of row.items || []) {
    const d = canonicalDomain(item?.mediaType);
    if (d) return d;
  }
  return '';
}

/**
 * Group discover rows by domain, preserving the server's order within each.
 *
 * WHICH TILES EXIST IS NOT THIS SIDE'S DECISION, and an earlier version of this
 * function got that wrong. It filtered out every item with no `play` target, on
 * the reasoning that a tile with nowhere to go is a dead button — true of some
 * of them, and catastrophic as a rule, because the browser cannot tell the
 * difference between "nothing has this" and "the torrent indexer was down when
 * the page was built". Applied during a Prowlarr outage it removed five whole
 * shelves whose tiles work perfectly well the moment the backend answers.
 *
 * Only the server knows what sources exist and which of them were asked, so
 * only the server decides what is published. This side's job is to render what
 * arrives without overstating it — see tileAction, where an item that carries
 * no verified target offers to go looking rather than promising to play.
 */
export function groupRowsByDomain(rows) {
  const out = new Map();
  for (const row of rows || []) {
    if (!row?.items?.length) continue;
    const d = domainOfRow(row);
    if (!d) continue;
    if (!out.has(d)) out.set(d, []);
    out.get(d).push(row);
  }
  return out;
}

/**
 * The sections to draw, in schema order.
 *
 * Every domain gets an entry. `rows` is what discover already answered with;
 * `needsBrowse` marks the ones that have to go and ask. Nothing is dropped
 * here -- a section is only removed once it has actually failed to produce
 * anything, which is a fact rather than a prediction.
 */
export function homePlan(discoverRows) {
  const grouped = groupRowsByDomain(discoverRows);
  return allDomains().map((domain) => {
    const rows = grouped.get(domain) || [];
    return {
      domain,
      label: labelFor(domain),
      verb: verbFor(domain),
      rows,
      needsBrowse: rows.length === 0,
    };
  });
}

// ---------------------------------------------------------------- play seam

/**
 * Whether something in a `play` domain can genuinely be launched.
 *
 * The default answer covers only what this client can actually do today: an
 * archive.org item, which either boots in EmulatorJS (`#ejs`), in Ruffle
 * (`#swf`), or in the archive's own Emularity player. Everything else in the
 * games domain is a torrent -- a ROM set to download, not a thing to press
 * play on -- and labelling it "Play" would be a promise this client cannot
 * keep.
 *
 * A real launcher backend replaces this through setPlayProbe rather than by
 * editing anything here, so the button appears the moment a backend that can
 * honour it exists and not one commit before.
 */
let playProbe = null;

export function setPlayProbe(fn) {
  playProbe = typeof fn === 'function' ? fn : null;
}

export function canPlay(item) {
  if (playProbe) {
    try {
      return Boolean(playProbe(item));
    } catch {
      // A backend that throws is a backend that cannot answer, which is the
      // same as "no" -- and must not take the row down with it.
      return false;
    }
  }
  return Boolean(archiveIdFrom(item?.uri));
}

// ------------------------------------------------------------------ archive

/**
 * The archive.org identifier inside any of the URL shapes the API hands out:
 * a details page, a download URL, or one of those with a `#ejs`/`#swf`
 * fragment, plus the `ia:<id>` form used as a card key.
 *
 * Returns '' for anything else, which callers read as "not an archive item"
 * rather than risking a request built from a guess.
 */
export function archiveIdFrom(input) {
  const raw = String(input ?? '').trim();
  if (!raw) return '';
  if (raw.startsWith('ia:')) return raw.slice(3).split(/[/?#]/)[0];
  const m = /archive\.org\/(?:details|download|metadata|embed)\/([^/?#]+)/i.exec(raw);
  if (!m) return '';
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return m[1];
  }
}

// ------------------------------------------------------------- normalisation

/**
 * One tile shape, whichever endpoint the thing came from.
 *
 * discover items and search cards describe the same objects with different
 * field names, and every consumer below would otherwise have to know which it
 * was holding.
 */
export function itemFromDiscover(it, domain) {
  return {
    domain,
    // Who will serve it, when the server says. Not used to decide anything —
    // that decision was made when the tile was resolved — but a row that turns
    // out to be entirely one source should be able to say so.
    source: it.source || '',
    // What is actually KNOWN about getting hold of it: '' means the target
    // below was verified, 'found' means a source confirmed it holds this,
    // 'unchecked' means nobody has been asked yet. Carried so a client can
    // explain a row rather than leaving a wall of "Find" unaccounted for.
    state: it.state || '',
    // What the server says this individual thing is, which is not always what
    // its section is. The Books section holds a row of audiobooks, and an
    // audiobook is something you listen to -- see tileAction.
    mediaType: it.mediaType || '',
    title: it.title || '',
    year: it.year || 0,
    poster: it.poster || '',
    overview: it.overview || '',
    rating: it.rating || 0,
    uri: it.play || '',
    card: null,
  };
}

export function itemFromCard(card, domain) {
  return {
    domain,
    title: card.title || '',
    year: card.year || 0,
    poster: card.art?.poster || '',
    overview: card.art?.overview || '',
    rating: card.art?.rating || 0,
    // The best source is what a click would open, and for an archive.org card
    // that is the details URL the identifier is recovered from. The card key
    // carries the same id and is the fallback when sources are torrents.
    uri: card.sources?.[card.best ?? 0]?.magnet || card.key || '',
    card,
  };
}

/**
 * The domain a single item belongs to, which is not always its section's.
 *
 * The Books section holds a row of audiobooks. An audiobook is filed under
 * literature by schema.json on purpose -- it is an edition of a book and
 * shares its author -- but it is still a recording, and offering one with
 * "Read" over a page reader that has no pages to show is a promise about the
 * wrong medium. The server states the item's own type; when that resolves,
 * it is the more specific fact and wins.
 */
export function domainOfItem(item) {
  return canonicalDomain(item?.mediaType) || item?.domain || '';
}

/**
 * What a click on this tile will do, and what to call it.
 *
 * The verb comes from schema.json, so a book is never offered with "Watch".
 * The one place it is not taken at face value is `play`: see canPlay above.
 */
export function tileAction(item) {
  const verb = verbFor(domainOfItem(item));
  const id = archiveIdFrom(item.uri);

  if ((verb === 'read' || verb === 'view') && id) {
    return { kind: 'reader', label: title(verb), id, verb };
  }
  if (!item.uri && !item.card) {
    // No verified target. The click runs a search, and the LABEL has to say so
    // — in every domain, not just the ones whose verb happens to be vague.
    //
    // This was the visible half of the original defect. A TMDB film tile said
    // "Watch" over a click that ran a fuzzy text search, and on 2026-08-08, 171
    // of 172 such searches returned nothing: the tile was indistinguishable
    // from the ones that worked right up until somebody had paid for the click.
    // "Find" is not a softer word for the same promise, it is a different and
    // accurate one — this will go and look.
    //
    // These tiles are NOT hidden. The server publishes an item in this state
    // only when a source that could hold it exists and has not been asked, and
    // "we have not checked" is not a reason to withhold something; it is a
    // reason not to oversell it.
    return { kind: 'search', label: 'Find' };
  }
  if (verb === 'play') {
    return canPlay(item)
      ? { kind: 'open', label: 'Play' }
      // Found, but not launchable from here. "Open" is the honest word: it
      // shows the sources and lets somebody decide, which is all this can do.
      : { kind: 'open', label: 'Open' };
  }
  return { kind: 'open', label: title(verb) };
}

function title(s) {
  return s ? s[0].toUpperCase() + s.slice(1) : 'Open';
}

// --------------------------------------------------------------- rendering

/**
 * Artwork, or something deliberate in its place.
 *
 * Films come with TMDB posters. Comics, games and archive.org scans very often
 * come with nothing, or with a thumbnail service URL that answers with an
 * error -- and a broken-image glyph in a 2:3 box is the ugliest possible way
 * to say "no cover". So a placeholder always exists underneath, and the image
 * is laid over it; if the image never arrives, what was already there stays.
 *
 * The image is NOT hidden while it loads, and that is load-bearing rather than
 * an oversight. `hidden` resolves to `display: none`, and a `loading=lazy`
 * image that is display:none can never intersect the viewport, so Chrome never
 * fetches it, so the load event that would unhide it never fires. Written that
 * way, every one of 388 tiles on this page sat on its placeholder forever.
 */
function poster(item, action) {
  const p = el('div', 'poster');
  const placeholder = el('div', 'noart');
  placeholder.append(el('span', 'noart-kind', labelFor(domainOfItem(item)) || ''));
  placeholder.append(el('span', 'noart-title', item.title));
  p.append(placeholder);

  if (item.poster) {
    const img = el('img');
    img.loading = 'lazy';
    // Empty alt, not the title: the title is already the line under the tile,
    // and a screen reader should not read it twice.
    img.alt = '';
    img.addEventListener('load', () => {
      // A 1x1 or otherwise degenerate response is a placeholder from the far
      // end, not a cover; ours says more.
      if (img.naturalWidth <= 2 || img.naturalHeight <= 2) img.remove();
    });
    img.addEventListener('error', () => { img.remove(); });
    img.src = item.poster;
    // Appended after the placeholder so it paints over it, and takes it away
    // by covering it rather than by toggling anything.
    p.append(img);
  }

  if (item.rating) p.append(el('span', 'rating', item.rating.toFixed(1)));
  // The verb, on the artwork. This is the whole point of the exercise: you
  // read a comic, you watch a film, and the card says which before you click.
  p.append(el('span', `verb verb-${item.domain}`, action.label));
  return p;
}

/**
 * One tile. Exported so a category page draws the same thing the rails do --
 * the verb, the placeholder behaviour and the `hidden`/lazy-loading trap above
 * are all decided here, and a second copy would get one of them wrong.
 */
export function tile(item, handlers) {
  const action = tileAction(item);
  const t = el('button', 'tile');
  t.type = 'button';
  t.title = item.overview || item.title;
  t.setAttribute('aria-label', `${action.label} ${item.title}`);
  t.append(poster(item, action));
  t.append(el('div', 'tname', item.title));
  t.append(el('div', 'tmeta', item.year ? String(item.year) : ''));
  t.addEventListener('click', () => handlers.onActivate(item, action));
  return t;
}

/** One horizontal rail of tiles. */
function shelf(heading, items, handlers) {
  const s = el('section', 'shelf');
  if (heading) s.append(el('h3', null, heading));
  const rail = el('div', 'rail');
  rail.tabIndex = -1;
  for (const item of items) rail.append(tile(item, handlers));
  attachRailKeys(rail);
  s.append(rail);
  return s;
}

/**
 * Arrow keys along a rail.
 *
 * This page runs on a television, where the only input is a D-pad. A rail is a
 * horizontally scrolling strip, and without this, left and right scroll the
 * strip while focus stays put -- so the highlight walks off screen and the
 * remote appears to stop working. Up and down are left alone so they still
 * move between rails the way the document order implies.
 */
export function attachRailKeys(rail) {
  rail.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    const tiles = [...rail.querySelectorAll('.tile')];
    const i = tiles.indexOf(document.activeElement);
    if (i < 0) return;
    const next = tiles[i + (e.key === 'ArrowRight' ? 1 : -1)];
    if (!next) return; // let the edge fall through rather than trapping focus
    e.preventDefault();
    next.focus();
    next.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  });
}

/**
 * One line saying a backend is down, when one is.
 *
 * Without this the page has a quieter dishonesty than the one it started with:
 * shelves full of "Find" tiles and no explanation, which reads as the site
 * having become worse rather than as a service being temporarily out. The rows
 * stay — deleting them was the bug — so something has to account for them.
 *
 * Only rendered for a backend that is CONFIGURED and not answering. An instance
 * with no indexer at all is not broken; it is a smaller instance, and its
 * shelves are already sized to what it can actually serve.
 */
export function backendNotice(indexer) {
  if (!indexer?.configured || indexer.reachable) return null;
  const el2 = el('p', 'backend-notice');
  el2.setAttribute('role', 'status');
  el2.textContent = indexer.detail
    || 'The torrent indexer is not answering, so some titles cannot be checked '
     + 'until it is back.';
  return el2;
}

/** A section still waiting on its browse, so the page has its shape at once. */
function skeletonShelf() {
  const rail = el('div', 'rail');
  for (let i = 0; i < 8; i++) {
    const t = el('div', 'tile skel');
    t.append(el('div', 'poster'));
    t.append(el('div', 'tname-sk'));
    t.append(el('div', 'tmeta-sk'));
    rail.append(t);
  }
  const s = el('section', 'shelf');
  s.append(rail);
  return s;
}

/**
 * Draw the home screen.
 *
 * `browse(domain)` is injected rather than imported so this module never needs
 * to know how the client talks to a server, and so the tests can drive it.
 * It resolves to an array of cards, or to an empty array for a domain that has
 * nothing -- in which case the whole section is removed.
 */
export async function renderHome(host, {
  discoverRows, browse, handlers, resume, indexer,
}) {
  host.replaceChildren();

  // An element, not a list -- the caller builds the Continue Watching shelf,
  // because only it knows how far into something you are.
  if (resume) host.append(resume);

  const notice = backendNotice(indexer);
  if (notice) host.append(notice);

  const plan = homePlan(discoverRows);
  const pending = [];

  for (const section of plan) {
    const sec = el('section', 'domain');
    sec.dataset.domain = section.domain;

    const head = el('div', 'domain-head');
    head.append(el('h2', null, section.label));
    head.append(el('span', 'domain-verb', title(section.verb)));
    sec.append(head);

    if (section.rows.length) {
      for (const row of section.rows) {
        sec.append(shelf(
          row.title,
          row.items.map((it) => itemFromDiscover(it, section.domain)),
          handlers,
        ));
      }
      host.append(sec);
      continue;
    }

    // Nothing curated for this domain. Reserve its place, ask, and either fill
    // it or take it away -- never leave the heading standing over nothing.
    const skel = skeletonShelf();
    sec.append(skel);
    host.append(sec);

    pending.push(
      Promise.resolve()
        .then(() => browse(section.domain))
        .then((cards) => {
          skel.remove();
          const items = (cards || []).map((c) => itemFromCard(c, section.domain));
          if (!items.length) {
            sec.remove();
            return;
          }
          sec.append(shelf('', items, handlers));
        })
        .catch(() => {
          // A domain we could not ask about is not a domain with nothing in
          // it, but there is no honest way to draw the difference on a shelf.
          sec.remove();
        }),
    );
  }

  await Promise.allSettled(pending);
  return plan;
}

/** "films, TV, music, games, books, comics and images", from the vocabulary. */
export function domainSentence() {
  const labels = allDomains().map((d) => labelFor(d)).filter(Boolean);
  if (labels.length < 2) return labels[0] || '';
  return `${labels.slice(0, -1).join(', ')} and ${labels[labels.length - 1]}`;
}
