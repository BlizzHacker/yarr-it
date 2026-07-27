// Content script: magnet hijacking and ▶ injection.
//
// Two jobs:
//
//   1. Intercept clicks on magnet: links anywhere on the web, before the
//      browser hands them to a desktop torrent client. MV3 removed blocking
//      webRequest, and an extension cannot register a web page as a protocol
//      handler, so catching the click in the page is the only route that works.
//
//   2. Add a ▶ next to results on tracker sites, so streaming is one click
//      from where people already browse.
//
// Site layouts come from sites.js (data, not code) with a generic fallback, so
// a new tracker is a table entry rather than a code change.

const SITE = 'https://stream.moveweight.com';

// Inlined rather than imported: content scripts are not modules by default and
// a dynamic import of an extension URL is blocked by many sites' CSP.
const SITE_ADAPTERS = [
  { match: /(^|\.)thepiratebay|tpb/i, row: '#searchResult tr, .list-entry, ol#torrents li', anchor: 'td:nth-child(2), .item-name, .list-item' },
  { match: /(^|\.)1337x\./i, row: 'table.table-list tbody tr, .box-info-detail', anchor: 'td.name, .box-info-detail' },
  { match: /(^|\.)yts\./i, row: '.browse-movie-wrap, #movie-info', anchor: '.browse-movie-bottom, #movie-info' },
  { match: /(^|\.)nyaa\./i, row: 'table.torrent-list tbody tr', anchor: 'td:nth-child(2)' },
  { match: /(^|\.)limetorrents|torrentgalaxy|torrentdownloads|kickass|katcr/i, row: 'table tbody tr, .tgxtablerow', anchor: 'td:nth-child(1), td:nth-child(2)' },
  { match: /(^|\.)rutracker\./i, row: 'tr.tCenter, #tor-tbl tbody tr', anchor: 'td.t-title, .t-title-col' },
  { match: /archive\.org/i, row: '.item-ia, .details-container', anchor: '.item-ttl, .details-container' },
];

const settings = { hijack: true, injectButtons: true };

chrome.runtime.sendMessage({ type: 'get-settings' }, (s) => {
  if (chrome.runtime.lastError || !s) return;
  Object.assign(settings, s);
  if (settings.injectButtons) inject();
});

// ------------------------------------------------------------ 1. hijacking --

// Capture phase, so we run before the page's own handlers and before the
// browser starts the external-protocol navigation.
document.addEventListener(
  'click',
  (ev) => {
    if (!settings.hijack) return;
    const a = ev.target?.closest?.('a[href^="magnet:"]');
    if (!a) return;
    // Let the user opt out per-click with a modifier, the way they would to
    // open a link in a new tab normally.
    if (ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;

    ev.preventDefault();
    ev.stopPropagation();
    chrome.runtime.sendMessage({ type: 'stream-magnet', magnet: a.href });
  },
  true,
);

// -------------------------------------------------------- 2. ▶ injection ----

function adapterFor(hostname) {
  return SITE_ADAPTERS.find((a) => a.match.test(hostname)) || null;
}

function makeButton(magnet) {
  const b = document.createElement('button');
  b.className = 'mw-stream-btn';
  b.type = 'button';
  b.textContent = '▶ Stream';
  b.title = 'Play this in your browser instead of downloading it';
  b.addEventListener('click', (ev) => {
    ev.preventDefault();
    ev.stopPropagation();
    chrome.runtime.sendMessage({ type: 'stream-magnet', magnet });
  });
  return b;
}

function injectInto(root) {
  const adapter = adapterFor(location.hostname);
  const rows = adapter ? root.querySelectorAll(adapter.row) : [];

  if (rows.length) {
    for (const row of rows) {
      if (row.dataset.mwStream) continue;
      const link = row.querySelector('a[href^="magnet:"]');
      if (!link) continue;
      row.dataset.mwStream = '1';
      const host = (adapter.anchor && row.querySelector(adapter.anchor)) || link.parentElement || row;
      host.appendChild(makeButton(link.href));
    }
    return;
  }

  // Generic fallback: any magnet link on any page gets a button beside it.
  for (const link of root.querySelectorAll('a[href^="magnet:"]')) {
    if (link.dataset.mwStream) continue;
    link.dataset.mwStream = '1';
    link.insertAdjacentElement('afterend', makeButton(link.href));
  }
}

function inject() {
  injectInto(document);

  // Trackers paginate and filter client-side, so new rows appear without a
  // navigation. Debounced to stay cheap on busy pages.
  let pending = null;
  new MutationObserver(() => {
    clearTimeout(pending);
    pending = setTimeout(() => injectInto(document), 300);
  }).observe(document.body, { childList: true, subtree: true });
}
