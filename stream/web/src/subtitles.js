/**
 * Subtitles that come with the release.
 *
 * Most video torrents already carry .srt files next to the video, so the first
 * source of subtitles is the one nobody has to fetch, key or account for. There
 * is no external service here and no API key to lose.
 *
 * Browsers only load WebVTT in a <track>, so SRT is converted in the page. The
 * two formats are close enough that the conversion is small: a header, and
 * timestamps that use a comma where VTT wants a period.
 *
 * ASS/SSA is detected and skipped rather than half-converted. It carries
 * positioning, styling and karaoke timing that VTT cannot express, and a
 * mangled subtitle track is worse than an honest "none found".
 */

const SUB_EXTENSIONS = ['.srt', '.vtt'];
const UNSUPPORTED_EXTENSIONS = ['.ass', '.ssa', '.sub', '.idx'];

// Enough to label the common cases. A wrong guess costs a label, not a track,
// so this errs towards "Unknown" rather than towards a confident mistake.
const LANGUAGES = [
  [/\b(en|eng|english)\b/i, 'English'],
  [/\b(es|spa|spanish|espanol|español)\b/i, 'Spanish'],
  [/\b(fr|fra|fre|french|francais|français)\b/i, 'French'],
  [/\b(de|ger|deu|german|deutsch)\b/i, 'German'],
  [/\b(it|ita|italian|italiano)\b/i, 'Italian'],
  [/\b(pt|por|portuguese|portugues|português)\b/i, 'Portuguese'],
  [/\b(ru|rus|russian)\b/i, 'Russian'],
  [/\b(ja|jpn|japanese)\b/i, 'Japanese'],
  [/\b(ko|kor|korean)\b/i, 'Korean'],
  [/\b(zh|chi|chs|cht|chinese)\b/i, 'Chinese'],
  [/\b(ar|ara|arabic)\b/i, 'Arabic'],
  [/\b(nl|dut|nld|dutch)\b/i, 'Dutch'],
  [/\b(pl|pol|polish)\b/i, 'Polish'],
  [/\b(tr|tur|turkish)\b/i, 'Turkish'],
];

function extensionOf(name = '') {
  const i = String(name).lastIndexOf('.');
  return i < 0 ? '' : name.slice(i).toLowerCase();
}

export function languageOf(name = '') {
  // Match against the filename only, so a folder called "English Movies" does
  // not label every track in it.
  const base = String(name).split(/[\\/]/).pop();
  for (const [re, label] of LANGUAGES) {
    if (re.test(base)) return label;
  }
  return 'Unknown';
}

/** True for a subtitle we can actually render. */
export function isSupportedSubtitle(name) {
  return SUB_EXTENSIONS.includes(extensionOf(name));
}

export function isUnsupportedSubtitle(name) {
  return UNSUPPORTED_EXTENSIONS.includes(extensionOf(name));
}

/**
 * Subtitle files from a torrent's file list, best first.
 *
 * "Forced" tracks translate only foreign dialogue, so they are ranked below a
 * full track of the same language rather than dropped -- someone watching a
 * dubbed release may want exactly that.
 */
export function findSubtitles(files = []) {
  return files
    .filter((f) => isSupportedSubtitle(f?.name))
    .map((f) => ({
      file: f,
      name: f.name,
      language: languageOf(f.name),
      forced: /\bforced\b/i.test(f.name),
      sdh: /\b(sdh|cc|hearing[ ._-]?impaired)\b/i.test(f.name),
    }))
    .sort((a, b) => {
      if (a.forced !== b.forced) return a.forced ? 1 : -1;
      if (a.sdh !== b.sdh) return a.sdh ? 1 : -1;
      // English first only as a tiebreak, then alphabetically for stability.
      const aEn = a.language === 'English';
      const bEn = b.language === 'English';
      if (aEn !== bEn) return aEn ? -1 : 1;
      return a.language.localeCompare(b.language);
    });
}

/**
 * Convert SubRip to WebVTT.
 *
 * Already-VTT input is returned as-is: a release that ships .vtt needs nothing
 * done to it, and re-processing risks breaking cue settings it may carry.
 */
export function srtToVtt(text = '') {
  // A BOM before the WEBVTT header makes the whole file invalid.
  let body = String(text).replace(/^﻿/, '').replace(/\r\n?/g, '\n');

  if (/^WEBVTT/.test(body.trimStart())) return body.trimStart();

  body = body
    // 00:00:01,000 --> 00:00:04,000  becomes  00:00:01.000 --> 00:00:04.000
    // The hours group is optional: some releases write mm:ss,mmm, and leaving
    // that comma in place also defeats the padding below.
    .replace(/(\d{2}:\d{2}(?::\d{2})?),(\d{1,3})/g, '$1.$2')
    // Some releases use two-digit hours only sometimes; VTT wants a full
    // timestamp, so a bare mm:ss.mmm cue is padded to hh:mm:ss.mmm.
    .replace(/^(\d{2}:\d{2}\.\d{3}) --> (\d{2}:\d{2}\.\d{3})/gm,
      '00:$1 --> 00:$2');

  return `WEBVTT\n\n${body.trim()}\n`;
}

/**
 * Attach tracks to a <video>, replacing any previously attached.
 *
 * `load` is given the cue text for a track; it is passed in rather than read
 * here so this stays testable without a torrent.
 */
export async function attachSubtitles(video, tracks, load) {
  if (!video || !tracks?.length) return [];

  for (const old of [...video.querySelectorAll('track[data-yarrit]')]) {
    URL.revokeObjectURL(old.src);
    old.remove();
  }

  const attached = [];
  for (const [i, t] of tracks.entries()) {
    let text;
    try {
      text = await load(t);
    } catch {
      // One unreadable subtitle must not cost the others.
      continue;
    }
    if (!text) continue;

    const blob = new Blob([srtToVtt(text)], { type: 'text/vtt' });
    const el = document.createElement('track');
    el.kind = 'subtitles';
    el.label = t.forced ? `${t.language} (forced)`
      : t.sdh ? `${t.language} (SDH)` : t.language;
    el.srclang = t.language === 'Unknown' ? '' : t.language.slice(0, 2).toLowerCase();
    el.src = URL.createObjectURL(blob);
    el.dataset.yarrit = '1';
    // Nothing is shown by default: a subtitle track that switches itself on is
    // an annoyance for the majority who did not ask for one.
    if (i === 0) el.default = false;
    video.append(el);
    attached.push(el);
  }
  return attached;
}
