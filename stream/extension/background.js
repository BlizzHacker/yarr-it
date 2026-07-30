// Service worker: magnet interception, omnibox search, context menu.
//
// Chrome cannot register a web page as a `magnet:` protocol handler from an
// extension, and MV3 removed blocking webRequest. So magnet capture is done in
// the content script (which sees the click before navigation) and this worker
// handles everything that needs extension-level APIs.

const SITE = 'https://yarrit.com';

const streamURL = (magnet) => `${SITE}/?magnet=${encodeURIComponent(magnet)}`;
const searchURL = (q) => `${SITE}/?q=${encodeURIComponent(q)}`;

/** Open a magnet in the streamer, reusing an existing tab when we have one. */
async function openStream(magnet, { active = true } = {}) {
  await chrome.tabs.create({ url: streamURL(magnet), active });
}

// ---------------------------------------------------------------- messages --

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg?.type === 'stream-magnet' && msg.magnet) {
    openStream(msg.magnet);
    sendResponse({ ok: true });
    return true;
  }
  if (msg?.type === 'get-settings') {
    chrome.storage.sync.get({ hijack: true, injectButtons: true }).then(sendResponse);
    return true; // async
  }
  return false;
});

// ---------------------------------------------------------------- omnibox ---

chrome.omnibox.setDefaultSuggestion({
  description: 'Search every torrent indexer and stream the result',
});

let suggestTimer = null;

chrome.omnibox.onInputChanged.addListener((text, suggest) => {
  const q = text.trim();
  if (q.length < 3) return;

  // Debounced: the search API fans out to ~28 indexers, so firing on every
  // keystroke would be abusive.
  clearTimeout(suggestTimer);
  suggestTimer = setTimeout(async () => {
    try {
      const res = await fetch(`${SITE}/api/search?q=${encodeURIComponent(q)}&minSeeders=1`);
      if (!res.ok) return;
      const data = await res.json();
      suggest(
        (data.cards || []).slice(0, 6).map((c) => ({
          content: c.sources?.[0]?.magnet || `${SITE}/?q=${encodeURIComponent(q)}`,
          description:
            `<match>${escapeXml(c.title)}</match>` +
            (c.year ? ` <dim>(${c.year})</dim>` : '') +
            ` <url>${c.seeders}▲ ${escapeXml(c.sources?.[0]?.quality || '')}</url>`,
        })),
      );
    } catch {
      /* offline or blocked; leave the default suggestion */
    }
  }, 350);
});

chrome.omnibox.onInputEntered.addListener((text) => {
  const v = text.trim();
  if (v.startsWith('magnet:')) openStream(v);
  else chrome.tabs.create({ url: searchURL(v) });
});

function escapeXml(s) {
  return String(s).replace(/[<>&'"]/g, (c) =>
    ({ '<': '&lt;', '>': '&gt;', '&': '&amp;', "'": '&apos;', '"': '&quot;' }[c]));
}

// ----------------------------------------------------------- context menus --

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.removeAll(() => {
    chrome.contextMenus.create({
      id: 'stream-link',
      title: 'Stream this magnet',
      contexts: ['link'],
      targetUrlPatterns: ['magnet:*'],
    });
    chrome.contextMenus.create({
      id: 'stream-selection',
      title: 'Search Stream for “%s”',
      contexts: ['selection'],
    });
  });
});

chrome.contextMenus.onClicked.addListener((info) => {
  if (info.menuItemId === 'stream-link' && info.linkUrl) {
    openStream(info.linkUrl);
  } else if (info.menuItemId === 'stream-selection' && info.selectionText) {
    chrome.tabs.create({ url: searchURL(info.selectionText.trim()) });
  }
});
