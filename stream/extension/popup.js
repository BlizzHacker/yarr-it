const SITE = 'https://yarrit.com';
const $ = (s) => document.querySelector(s);

/** Accept a magnet URI or a bare 40-character info hash. */
function normalizeMagnet(v) {
  v = (v || '').trim();
  if (/^magnet:\?/i.test(v)) return v;
  if (/^[a-f0-9]{40}$/i.test(v)) {
    const trackers = [
      'udp://tracker.opentrackr.org:1337/announce',
      'udp://open.demonii.com:1337/announce',
      'udp://open.stealth.si:80/announce',
      'udp://exodus.desync.com:6969/announce',
      'udp://tracker.torrent.eu.org:451/announce',
    ];
    return `magnet:?xt=urn:btih:${v.toLowerCase()}` +
      trackers.map((t) => `&tr=${encodeURIComponent(t)}`).join('');
  }
  return null;
}

function doSearch() {
  const q = $('#q').value.trim();
  if (!q) return;
  chrome.tabs.create({ url: `${SITE}/?q=${encodeURIComponent(q)}` });
  window.close();
}

function doStream() {
  const m = normalizeMagnet($('#magnet').value);
  if (!m) {
    $('#magnet').style.borderColor = '#f87171';
    return;
  }
  chrome.tabs.create({ url: `${SITE}/?magnet=${encodeURIComponent(m)}` });
  window.close();
}

$('#search').addEventListener('click', doSearch);
$('#q').addEventListener('keydown', (e) => { if (e.key === 'Enter') doSearch(); });
$('#stream').addEventListener('click', doStream);
$('#magnet').addEventListener('keydown', (e) => { if (e.key === 'Enter') doStream(); });

// Settings round-trip.
const boxes = ['hijack', 'injectButtons'];
chrome.storage.sync.get({ hijack: true, injectButtons: true }).then((s) => {
  for (const k of boxes) $(`#${k}`).checked = !!s[k];
});
for (const k of boxes) {
  $(`#${k}`).addEventListener('change', (e) => {
    chrome.storage.sync.set({ [k]: e.target.checked });
  });
}

$('#opts').addEventListener('click', (e) => {
  e.preventDefault();
  chrome.runtime.openOptionsPage();
});

// ------------------------------------------------- pending network grants --
//
// The relay refused something because the user has not allowed that host yet.
// The prompt needs a user gesture in an extension surface, and this popup is
// the nearest one to where they are -- the badge on the toolbar icon is what
// brought them here.

async function renderPending() {
  let pending = [];
  try {
    const got = await chrome.storage.session.get({ relayPending: [] });
    pending = Array.isArray(got.relayPending) ? got.relayPending : [];
  } catch { return; }

  // The extension's own allowlist is the gate the relay checks, not
  // chrome.permissions — see the note in options.js for why they differ.
  const { relayHosts = [] } = await chrome.storage.sync.get({ relayHosts: [] });
  const live = pending.filter((p) => !relayHosts.includes(p.pattern));

  const box = $('#ask');
  const list = $('#asklist');
  list.textContent = '';
  box.hidden = live.length === 0;

  for (const p of live) {
    const li = document.createElement('li');
    const h = document.createElement('span');
    h.className = 'h';
    h.textContent = p.label || p.pattern.replace(/\/\*$/, '');
    const b = document.createElement('button');
    b.textContent = 'Allow';
    b.addEventListener('click', async () => {
      // Deliberately inside the click handler and not behind an await: Chrome
      // only honours the gesture for the duration of the handler.
      const ok = await chrome.permissions.request({ origins: [p.pattern] });
      if (!ok) return;
      const { relayHosts: cur = [] } = await chrome.storage.sync.get({ relayHosts: [] });
      await chrome.storage.sync.set({ relayHosts: [...new Set([...cur, p.pattern])].sort() });
      renderPending();
    });
    li.append(h, b);
    list.append(li);
  }
}

chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'session' && changes.relayPending) renderPending();
});
chrome.permissions.onAdded.addListener(renderPending);
renderPending();
