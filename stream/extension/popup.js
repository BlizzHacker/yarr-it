const SITE = 'https://stream.moveweight.com';
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
