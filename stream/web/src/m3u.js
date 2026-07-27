/**
 * M3U parsing.
 *
 * The format is filthy in the wild: attribute soup on #EXTINF, inconsistent
 * quoting, byte-order marks, CRLF endings, blank lines, and entries whose URI
 * line is simply missing. Everything here is defensive on purpose.
 */

const HLS_MARKERS = ['#EXT-X-TARGETDURATION', '#EXT-X-STREAM-INF', '#EXT-X-VERSION'];

/**
 * Both IPTV channel lists and HLS manifests use the .m3u8 extension, so the
 * extension tells you nothing. HLS manifests carry #EXT-X-* tags; channel
 * lists do not.
 *
 * A marker only counts when it is a TAG - i.e. it appears at the very start
 * of a (trimmed) line. `text.includes(marker)` looks correct and will be
 * "simplified" back to that if you're not careful, but it's a substring
 * search over the WHOLE raw document: any IPTV channel list whose
 * group-title, tvg-name, channel name, or URL merely CONTAINS one of these
 * strings (e.g. group-title="#EXT-X-VERSION Sports") gets misclassified as
 * an HLS manifest and handed to a video element as a single stream instead
 * of being rendered as a channel list. Anchoring to the start of the line
 * avoids that false positive while staying a single linear pass (the inner
 * loop is over the small, fixed-size marker list, not the document).
 */
export function looksLikeHls(text) {
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    for (const marker of HLS_MARKERS) {
      if (line.startsWith(marker)) return true;
    }
  }
  return false;
}

// Precompiled once at module load rather than per-call: attr() runs twice
// per #EXTINF line, so a 50,000-channel playlist would otherwise compile
// 100,000 RegExp objects. The `(?:^|\s)` boundary before the name ensures a
// request for "group-title" doesn't also match a vendor-prefixed attribute
// like tvg-group-title="..." or parent-group-title="..." - without it, the
// no-boundary version matches "group-title=" anywhere it occurs as a
// substring, including inside a longer attribute name.
const ATTR_PATTERNS = {
  'tvg-logo': /(?:^|\s)tvg-logo="([^"]*)"/,
  'group-title': /(?:^|\s)group-title="([^"]*)"/,
};

function attr(line, name) {
  const m = line.match(ATTR_PATTERNS[name]);
  return m ? m[1] : '';
}

// Real IPTV playlists routinely quote attribute values that contain commas,
// e.g. group-title="News, UK" or group-title="Sports, US". A naive
// line.indexOf(',') looks correct (and will be "simplified" back to that if
// you're not careful) but it splits the channel name on the FIRST comma
// anywhere on the line, including ones inside quoted attribute values, which
// mangles the title. Scan for the first comma that is NOT inside a pair of
// double quotes instead. If a quote is left unterminated, treat the rest of
// the line as still "inside quotes" rather than looping or throwing - this
// is a linear single-pass scan, so it can't hang, and it deterministically
// yields "no unquoted comma found".
function findUnquotedComma(line) {
  let inQuotes = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (ch === '"') {
      inQuotes = !inQuotes;
    } else if (ch === ',' && !inQuotes) {
      return i;
    }
  }
  return -1;
}

export function parseM3U(text) {
  const clean = text.replace(/^﻿/, '').replace(/\r\n/g, '\n');
  const lines = clean.split('\n');

  const entries = [];
  let pending = null;

  for (const raw of lines) {
    const line = raw.trim();
    if (!line) continue;

    if (line.startsWith('#EXTINF')) {
      const comma = findUnquotedComma(line);
      pending = {
        title: comma === -1 ? '' : line.slice(comma + 1).trim(),
        uri: '',
        logo: attr(line, 'tvg-logo'),
        group: attr(line, 'group-title'),
      };
      continue;
    }

    if (line.startsWith('#')) continue;

    // A bare line is the URI for the #EXTINF above it. Without one, the entry
    // is unplayable, so drop it rather than emitting a half-parsed channel.
    if (pending) {
      pending.uri = line;
      entries.push(pending);
      pending = null;
    }
  }

  return { title: 'Playlist', entries };
}
