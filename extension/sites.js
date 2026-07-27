// Tracker site adapters.
//
// Data, not code: supporting a new tracker is an entry here, not a change to
// the content script. `row` is the element a ▶ button gets appended to, and
// `magnet` finds the magnet within that row.
//
// The generic fallback in content.js handles any site not listed, so this table
// only exists to place the button somewhere sensible on the sites people
// actually use.

export const SITE_ADAPTERS = [
  {
    match: /(^|\.)thepiratebay|tpb/i,
    row: '#searchResult tr, .list-entry, ol#torrents li',
    magnet: 'a[href^="magnet:"]',
    anchor: 'td:nth-child(2), .item-name, .list-item',
  },
  {
    match: /(^|\.)1337x\./i,
    row: 'table.table-list tbody tr, .box-info-detail',
    magnet: 'a[href^="magnet:"]',
    anchor: 'td.name, .box-info-detail',
  },
  {
    match: /(^|\.)yts\./i,
    row: '.browse-movie-wrap, #movie-info',
    magnet: 'a[href^="magnet:"]',
    anchor: '.browse-movie-bottom, #movie-info .bottom-info',
  },
  {
    match: /(^|\.)nyaa\./i,
    row: 'table.torrent-list tbody tr',
    magnet: 'a[href^="magnet:"]',
    anchor: 'td:nth-child(2)',
  },
  {
    match: /(^|\.)limetorrents|torrentgalaxy|torrentdownloads|kickass|katcr/i,
    row: 'table tbody tr, .tgxtablerow',
    magnet: 'a[href^="magnet:"]',
    anchor: 'td:nth-child(1), td:nth-child(2)',
  },
  {
    match: /(^|\.)rutracker\./i,
    row: 'tr.tCenter, #tor-tbl tbody tr',
    magnet: 'a[href^="magnet:"]',
    anchor: 'td.t-title, .t-title-col',
  },
  {
    match: /archive\.org/i,
    row: '.item-ia, .details-container',
    magnet: 'a[href$=".torrent"], a[href^="magnet:"]',
    anchor: '.item-ttl, .details-container',
  },
];

export function adapterFor(hostname) {
  return SITE_ADAPTERS.find((a) => a.match.test(hostname)) || null;
}
