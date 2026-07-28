/**
 * Given a list of files, which one does the player want?
 *
 * This is one question asked by three different callers, which is why it lives
 * on its own rather than inside any of them:
 *
 *   - a torrent may hold hundreds of files (a ROM set, a season, a release
 *     folder with a readme and box art)
 *   - an archive.org item lists screenshots, metadata XML, a .torrent, and
 *     somewhere among them the actual ROM
 *   - a zip inside either of those may hold the thing rather than being it
 *
 * The old logic picked the single largest file. For video that is usually
 * right. For a ROM collection it is nonsense: the biggest file in a 200-ROM
 * torrent is not "the game", it is whichever game happened to be biggest.
 */

const VIDEO = ['.mp4', '.m4v', '.mkv', '.webm', '.avi', '.mov', '.ts', '.m2ts', '.wmv'];
const AUDIO = ['.mp3', '.flac', '.m4a', '.ogg', '.opus', '.wav', '.aac'];
const IMAGE = ['.jpg', '.jpeg', '.png', '.gif', '.webp', '.bmp'];
const FLASH = ['.swf'];
const ROM = [
  '.nes', '.fds', '.unf', '.unif',
  '.smc', '.sfc', '.swc', '.fig',
  '.gb', '.gbc', '.gba',
  '.n64', '.z64', '.v64',
  '.md', '.gen', '.smd', '.sms', '.gg',
  '.a26', '.a78', '.lnx', '.pce', '.ws', '.wsc', '.ngp', '.ngc', '.vb',
  '.bin', '.col', '.int', '.j64', '.jag',
];
const ARCHIVE = ['.zip', '.7z'];

/** Files that are never the thing you came for. */
const JUNK = [
  '.nfo', '.txt', '.diz', '.sfv', '.md5', '.sha1', '.par2', '.url', '.srt',
  '.sub', '.idx', '.xml', '.sqlite', '.json', '.torrent', '.exe', '.bat',
  '.dll', '.ini', '.log', '.cue',
];

const KINDS = [
  ['video', VIDEO],
  ['audio', AUDIO],
  ['flash', FLASH],
  ['rom', ROM],
  ['image', IMAGE],
  ['archive', ARCHIVE],
];

const lower = (s) => String(s || '').toLowerCase();

export function kindOf(name) {
  const n = lower(name);
  if (JUNK.some((e) => n.endsWith(e))) return 'junk';
  for (const [kind, exts] of KINDS) {
    if (exts.some((e) => n.endsWith(e))) return kind;
  }
  return 'other';
}

/** Region preference, matching how dumps are conventionally labelled. */
function regionRank(name) {
  const n = lower(name);
  if (/\((usa|u)\)|\[u\]|ntsc-u/.test(n)) return 4;
  if (/\((world|w)\)/.test(n)) return 3;
  if (/\((europe|e)\)|pal/.test(n)) return 2;
  if (/\((japan|j)\)/.test(n)) return 1;
  return 0;
}

/**
 * Build the list of playable entries in a set of files.
 *
 * Returns every candidate rather than one winner, because with a ROM
 * collection the only honest answer is "here are 200 games, which do you
 * want?" -- and the UI needs that list to ask.
 *
 * `files` is `[{name, length}]`. Sorted best-first within each kind.
 */
export function playableFiles(files = []) {
  const scored = files
    .map((f) => ({ ...f, kind: kindOf(f.name) }))
    .filter((f) => f.kind !== 'junk' && f.kind !== 'other');

  // Video and audio: biggest wins, because a bigger encode is a better one and
  // the small files are samples and extras.
  const media = scored
    .filter((f) => f.kind === 'video' || f.kind === 'audio')
    .sort((a, b) => (b.length || 0) - (a.length || 0));

  // ROMs and Flash: size means nothing, so order by region then name. Every
  // entry is a real choice the user might want.
  const playable = scored
    .filter((f) => f.kind === 'rom' || f.kind === 'flash')
    .sort((a, b) => regionRank(b.name) - regionRank(a.name)
      || String(a.name).localeCompare(String(b.name)));

  const ordered = [...media, ...playable];
  if (ordered.length) return ordered;

  // Fallbacks, in order, and only when nothing above exists.
  //
  // Images are last-resort rather than first-class because almost every
  // archive.org item ships cover art and a thumbnail. Counting those as
  // playable choices turns a one-ROM item into a three-way question about
  // whether you would rather look at the box.
  const images = scored.filter((f) => f.kind === 'image')
    .sort((a, b) => (b.length || 0) - (a.length || 0));
  if (images.length) return images;

  // An archive is a maybe: it might contain the ROM.
  return scored.filter((f) => f.kind === 'archive')
    .sort((a, b) => (a.length || 0) - (b.length || 0));
}

/**
 * The single best file, for callers that just want to play something.
 *
 * Real media wins over a ROM: a film torrent that ships an .swf extra should
 * still play the film.
 */
export function bestFile(files = []) {
  return playableFiles(files)[0] ?? null;
}

/**
 * Whether the user should be asked to choose.
 *
 * One playable file is not a choice. Two hundred ROMs is not a default.
 */
export function needsChoice(files = []) {
  const candidates = playableFiles(files);
  if (candidates.length < 2) return false;
  // Several encodes of one film is not a meaningful choice; several ROMs is.
  return candidates.filter((f) => f.kind === 'rom' || f.kind === 'flash').length > 1;
}
