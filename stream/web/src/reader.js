/**
 * Reading a comic or a book, and looking through a picture set.
 *
 * A card for one of these carries an archive.org *details page* URL, which is
 * HTML. Handing that to the player gets an iframe of somebody else's site --
 * on a desktop that is their BookReader and it works; on a television it is a
 * page of controls nothing can reach, and in this app it is a hole in the
 * middle of the product. /api/pages exists to close it: it turns an item into
 * a list of ordinary JPEGs, which is the one thing every client can already
 * draw.
 *
 * NOBODY HERE KNOWS HOW LONG A COMIC IS, AND THIS DOES NOT PRETEND TO
 *
 * /api/pages returns `probe: true` for a comic and documents that the client
 * should stop at the first page that fails to load, because the derive does
 * not publish a page count. Measured against archive.org, that never happens:
 * a page number past the end returns HTTP 200 carrying page 0's bytes. On
 * `i-have-no-mouth-and-i-must-scream_202202`, which runs out in the thirties:
 *
 *   n0  -> 200, 174345 bytes, md5 4ca7a11cd14d
 *   n25 -> 200, 245950 bytes, md5 e103a52f3607   (a real page)
 *   n40 -> 200, 174345 bytes, md5 4ca7a11cd14d   (page 0 again)
 *   n59 -> 200, 174345 bytes, md5 4ca7a11cd14d   (page 0 again)
 *
 * So a thirty-page comic offers sixty pages, thirty of them the cover. That is
 * ugly, and the obvious client-side fix is worse: the bytes cannot be compared
 * (archive.org sends no CORS header on downloads, so a canvas holding one of
 * these is tainted) but the decoded dimensions can be -- and that was tried.
 * It ends a comic whose pages all share one trim size at page two. Measured on
 * the item above: n0 and n1 are both 1233x1595, so the reader reported "Page 1
 * of 1" for a thirty-page book.
 *
 * Truncating a comic is the failure this reader exists to remove; running long
 * is untidy. So there is no guessing here at all. Pages run to the list the
 * server sent, a page that genuinely fails to load ends the item, and the
 * counter says "Page 4" rather than inventing a total nobody checked.
 *
 * The real fix is a page count in the /api/pages response -- archive.org
 * publishes one in each item's `_scandata.xml` and `_page_numbers.json`, which
 * the server can read and the browser cannot. Noted in the report.
 */

const READER_FALLBACK_EMBED = 'https://archive.org/embed/';

/**
 * Which shape of /api/pages to ask for.
 *
 * `kind=comic` short-circuits the metadata call on the server and returns
 * derived page images, which is right for anything scanned -- a comic or a
 * scanned book alike. A picture set has no derive and wants its own files, so
 * it asks without a kind and lets the server look.
 */
export function pagesPath(id, verb) {
  const q = new URLSearchParams({ id });
  if (verb === 'read') q.set('kind', 'comic');
  return `/api/pages?${q}`;
}

export function embedFallbackUrl(id) {
  return READER_FALLBACK_EMBED + encodeURIComponent(id);
}

/**
 * Page bookkeeping, with no DOM in it.
 *
 * `end` is where paging stops: for a picture set that is the file list, which
 * is exact; for a probed comic it is the ceiling the server guessed, pulled in
 * only when a page actually fails to load.
 */
export function createPager(pages, { probe = false } = {}) {
  return {
    pages: pages.slice(),
    probe,
    index: 0,
    end: pages.length,
  };
}

export function canGoNext(p) {
  return p.index + 1 < Math.min(p.pages.length, p.end);
}

export function canGoPrev(p) {
  return p.index > 0;
}

/**
 * How the counter reads.
 *
 * A picture set states its own length, so it says so. A probed comic does
 * not: printing "of 60" would quote the ceiling the server guessed at, and
 * "Page 4 of 60" for a thirty-page book is a specific claim that is wrong.
 * "Page 4" is the part that is true.
 */
export function pagerLabel(p) {
  const n = p.index + 1;
  return p.probe ? `Page ${n}` : `Page ${n} of ${Math.min(p.end, p.pages.length)}`;
}

/**
 * Record what a page turned out to be, and say whether it is real.
 *
 * Returns false when the page failed to load, in which case the caller steps
 * back to the last page that did. This is the whole of the end detection --
 * see the note at the top of the file for what was tried instead and why it
 * had to go.
 */
export function notePage(p, index, dims) {
  if (!dims?.w) {
    p.end = index;
    return false;
  }
  return true;
}

// ------------------------------------------------------------------- DOM --

/**
 * Open the reader over the page.
 *
 * `els` is passed in rather than queried so this module owns no selectors and
 * the caller keeps one place where the overlay's markup is named.
 */
export function openReader({ id, title, verb, els, apiFetch, onClose }) {
  const { root, title: titleEl, count, stage, img, embed, prev, next, status } = els;

  root.hidden = false;
  titleEl.textContent = title || id;
  count.textContent = '';
  status.hidden = true;
  embed.hidden = true;
  embed.removeAttribute('src');
  img.hidden = true;
  img.removeAttribute('src');
  stage.classList.remove('reader-embedding');

  let pager = null;
  let disposed = false;

  const say = (text) => {
    status.textContent = text;
    status.hidden = !text;
  };

  /**
   * What to do when there are no page images: show the item in archive.org's
   * own reader.
   *
   * This is not a nicety. Project Gutenberg items are born-digital text with
   * no scan behind them, so the page derive 404s for every page -- and those
   * are exactly the items in the Books row. Their BookReader renders them
   * properly. A dead end here would be the failure this whole change exists
   * to remove.
   */
  const fallback = (why) => {
    if (disposed) return;
    img.hidden = true;
    embed.src = embedFallbackUrl(id);
    embed.hidden = false;
    stage.classList.add('reader-embedding');
    count.textContent = '';
    prev.disabled = true;
    next.disabled = true;
    say(why);
    // The explanation is worth reading once and then in the way: it sits at
    // the foot of the stage, which is where the embedded reader keeps its own
    // page controls.
    setTimeout(() => { if (!disposed) say(''); }, 7000);
  };

  const paint = () => {
    count.textContent = pagerLabel(pager);
    prev.disabled = !canGoPrev(pager);
    next.disabled = !canGoNext(pager);
  };

  const show = (index) => {
    if (disposed || !pager) return;
    pager.index = index;
    say(index === 0 ? 'Loading…' : '');
    img.hidden = false;
    img.src = pager.pages[index];
    paint();
  };

  img.onload = () => {
    if (disposed || !pager) return;
    notePage(pager, pager.index, { w: img.naturalWidth, h: img.naturalHeight });
    say('');
    paint();
  };

  img.onerror = () => {
    if (disposed || !pager) return;
    notePage(pager, pager.index, null);
    if (pager.index === 0) {
      fallback('No page images for this one — opened in the Internet Archive reader.');
      return;
    }
    show(pager.index - 1);
    say('That is as far as this one goes.');
  };

  const go = (delta) => {
    if (!pager) return;
    const target = pager.index + delta;
    if (target < 0 || target >= Math.min(pager.pages.length, pager.end)) return;
    show(target);
  };

  const onKey = (e) => {
    if (root.hidden) return;
    if (e.key === 'ArrowRight' || e.key === 'PageDown') { e.preventDefault(); go(1); }
    else if (e.key === 'ArrowLeft' || e.key === 'PageUp') { e.preventDefault(); go(-1); }
  };

  prev.onclick = () => go(-1);
  next.onclick = () => go(1);
  document.addEventListener('keydown', onKey);

  const dispose = () => {
    disposed = true;
    document.removeEventListener('keydown', onKey);
    img.onload = null;
    img.onerror = null;
    img.removeAttribute('src');
    embed.removeAttribute('src');
    root.hidden = true;
    onClose?.();
  };

  say('Loading…');
  Promise.resolve()
    .then(() => apiFetch(pagesPath(id, verb)))
    .then((res) => (res.ok ? res.json() : Promise.reject(new Error(`pages ${res.status}`))))
    .then((body) => {
      if (disposed) return;
      const pages = Array.isArray(body?.pages) ? body.pages : [];
      if (!pages.length) {
        fallback('Nothing paginated in this one — opened in the Internet Archive reader.');
        return;
      }
      pager = createPager(pages, { probe: Boolean(body.probe) });
      // The focusable control the D-pad lands on first should be the one that
      // turns the page, not the one that closes the book.
      next.focus?.();
      show(0);
    })
    .catch(() => {
      if (disposed) return;
      fallback('Could not read the page list — opened in the Internet Archive reader.');
    });

  return { close: dispose };
}
