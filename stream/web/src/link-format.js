/**
 * How a resolved link's formats are described to a person.
 *
 * Every function here is pure and takes plain data, because this is where the
 * honesty of the whole feature actually lives. The competitors' quality lists
 * lie in four specific ways, and each one has a function here whose job is to
 * not do that:
 *
 *   - "HD" with no resolution behind it            -> qualityLabel
 *   - a size that is really a bitrate guess        -> sizeLabel
 *   - a video-only track offered as "1080p"        -> audioState / badges
 *   - a segmented stream offered as a file         -> saveability
 *
 * The server already computes all of this; these functions turn it into words
 * without softening any of it.
 */

export const AUDIO = {
  YES: 'yes',
  NO: 'no',
  UNKNOWN: 'unknown',
  JOINED: 'joined',
};

/**
 * What can honestly be said about this format's audio.
 *
 * The three-way split matters. A format whose `audioKnown` is false is one
 * where the network reported no codec information at all -- Facebook and
 * Twitch both do this -- and calling that "no audio" is how a working video
 * gets labelled as silent. UNKNOWN is a real answer and it is shown as one.
 */
export function audioState(format) {
  if (!format) return AUDIO.UNKNOWN;
  if (format.muxed) return AUDIO.JOINED;
  if (format.hasAudio) return AUDIO.YES;
  if (format.audioKnown) return AUDIO.NO;
  return AUDIO.UNKNOWN;
}

/** True only when we can positively promise there is no sound. */
export function isSilent(format) {
  return audioState(format) === AUDIO.NO;
}

/**
 * The headline label. Falls back through resolution, then the network's own
 * note, then the format id -- never to the word "HD", which means nothing.
 */
export function qualityLabel(format) {
  if (!format) return '';
  if (format.label) return format.label;
  if (format.height) return `${format.height}p${format.fps >= 50 ? format.fps : ''}`;
  if (format.note) return format.note;
  return format.id || 'unknown';
}

/**
 * Size, with the estimate marker preserved.
 *
 * The server prefixes a derived size with "~" and this does not strip it. A
 * "~828 MiB" that turns out to be 900 is a mild surprise; an "828 MiB" that
 * does the same is a lie.
 */
export function sizeLabel(format) {
  if (!format) return '';
  return format.sizeHuman || 'size unknown';
}

/**
 * Can this be saved as a file the user ends up with?
 *
 * A segmented stream that the server will repack counts as saveable; a bare
 * manifest does not, and saying so beats a download button that produces a
 * 3 KB playlist file.
 */
export function isSaveable(format) {
  if (!format) return false;
  if (format.streaming && !format.muxed && !format.media) return false;
  return Boolean(format.media);
}

/**
 * Small tags shown beside a row. `kind` drives colour: 'warn' is the only one
 * that should ever be alarming, and it is reserved for genuinely silent video.
 */
export function badges(format) {
  const out = [];
  const state = audioState(format);

  if (format.hasVideo) {
    if (state === AUDIO.NO) {
      out.push({ text: 'NO SOUND', kind: 'warn', title: 'This track has no audio at all.' });
    } else if (state === AUDIO.JOINED) {
      out.push({
        text: 'video + audio',
        kind: 'ok',
        title: `Picture and sound are served separately by this site; they are joined as it downloads${
          format.pairedWith ? ` (audio track ${format.pairedWith})` : ''
        }.`,
      });
    } else if (state === AUDIO.UNKNOWN) {
      out.push({
        text: 'audio not reported',
        kind: 'muted',
        title: 'This network does not publish codec details. There is almost certainly sound.',
      });
    }
  } else if (state === AUDIO.YES || state === AUDIO.UNKNOWN) {
    out.push({ text: 'audio only', kind: 'muted', title: 'Sound with no picture.' });
  }

  if (format.vcodec) out.push({ text: format.vcodec, kind: 'plain' });
  if (format.acodec) out.push({ text: format.acodec, kind: 'plain' });
  if (format.dynamicRange && format.dynamicRange !== 'SDR') {
    out.push({ text: format.dynamicRange, kind: 'plain' });
  }
  if (format.streaming && !format.muxed) {
    out.push({
      text: 'stream',
      kind: 'muted',
      title: 'Delivered as segments rather than one file.',
    });
  }
  return out;
}

/**
 * Split the list into the sections the UI shows.
 *
 * Silent tracks are not hidden -- somebody may genuinely want a video-only
 * file -- but they are put behind their own heading so that no one reaches one
 * by accident while scanning for "the big one".
 */
export function groupFormats(formats = []) {
  const complete = [];
  const audioOnly = [];
  const silent = [];
  for (const f of formats) {
    if (!f.hasVideo && f.hasAudio) audioOnly.push(f);
    else if (isSilent(f)) silent.push(f);
    else complete.push(f);
  }
  return { complete, audioOnly, silent };
}

/** The download URL for a format, or null when there is nothing to save. */
export function downloadUrl(format) {
  if (!isSaveable(format)) return null;
  const sep = format.media.includes('?') ? '&' : '?';
  return `${format.media}${sep}dl=1`;
}

/**
 * Codecs a browser will not decode, so the UI can warn before the picture goes
 * black rather than after. HEVC is the one that actually bites: Safari plays
 * it, Chrome and Firefox generally do not, and TikTok serves it by default.
 */
const BROWSER_HOSTILE = new Set(['HEVC', 'AV1', 'VP9']);

export function playbackWarning(format) {
  if (!format || !format.hasVideo) return null;
  if (format.vcodec === 'HEVC') {
    return 'HEVC video — Safari plays this, most other browsers do not. It will still download fine.';
  }
  if (BROWSER_HOSTILE.has(format.vcodec)) {
    return `${format.vcodec} video — older browsers and TVs may not decode this.`;
  }
  return null;
}

/**
 * One line summarising the whole result, used above the list.
 */
export function summarise(resolved) {
  if (!resolved) return '';
  const n = (resolved.formats || []).length;
  const bits = [`${n} format${n === 1 ? '' : 's'}`];
  if (resolved.extractor) bits.push(`via ${resolved.extractor}`);
  if (resolved.duration) bits.push(formatDuration(resolved.duration));
  return bits.join(' · ');
}

export function formatDuration(seconds) {
  if (!seconds || seconds < 0) return '';
  const s = Math.round(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const pad = (n) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(sec)}` : `${m}:${pad(sec)}`;
}

/**
 * The sentence shown when a link cannot be resolved.
 *
 * "Could not download" is what every competitor says and it tells nobody
 * anything. Each of these names what actually happened and, where there is
 * one, what to do instead.
 */
const FAILURE_TEXT = {
  unsupported_site:
    'No reader here recognises that link. If it is a normal video page, it may be a site nothing supports yet.',
  no_video_in_post:
    'That post is photos rather than video. Instagram only shows photo posts to a signed-in session, so an anonymous tool cannot fetch them.',
  login_required:
    'That network will only show this to a signed-in account, so it cannot be read anonymously.',
  private: 'That post is private.',
  not_found: 'That link points at something that has been removed or never existed.',
  geo_blocked: 'That video is blocked in the country this server sits in.',
  drm_protected: 'That stream is DRM-protected. There is nothing here that can be handed over.',
  live_not_supported: 'That is a live stream — there is nothing to save until it ends.',
  rate_limited: 'That network is rate-limiting this server right now. Try again shortly.',
  timeout: 'That site took too long to answer.',
  blocked_address: 'That link points at an address this server will not fetch.',
  resolver_unavailable: 'The link resolver is not installed on this server.',
  extractor_broken:
    "That site's reader is broken right now. This is a fault on our side, not yours — it usually means the network changed something.",
};

export function failureMessage(error) {
  if (!error) return 'Something went wrong.';
  const base = FAILURE_TEXT[error.code];
  if (base) return base;
  return error.error || 'That link could not be read.';
}

/**
 * Whether the failure is ours or theirs. It changes the tone of the panel and,
 * more usefully, whether "try again" is worth offering.
 */
export function isOurFault(error) {
  return ['extractor_broken', 'resolver_unavailable', 'timeout'].includes(error?.code);
}

export function isRetryable(error) {
  return ['timeout', 'rate_limited', 'extractor_broken'].includes(error?.code);
}
