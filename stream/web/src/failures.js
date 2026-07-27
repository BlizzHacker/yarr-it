import { TIER } from './source.js';

/**
 * Typed failures, so the player can say which rule blocked playback and which
 * tier is worth trying next. A generic "playback error" tells the user nothing
 * and hides the two browser rules that cause most IPTV failures.
 */

export const FAILURE = {
  MIXED_CONTENT: 'MixedContent',
  CORS_BLOCKED: 'CorsBlocked',
  DEAD_STREAM: 'DeadStream',
  UNSUPPORTED_CODEC: 'UnsupportedCodec',
  BUDGET_EXHAUSTED: 'BudgetExhausted',
};

export const DESCRIPTIONS = {
  [FAILURE.MIXED_CONTENT]:
    'This stream is served over plain HTTP. Browsers refuse to load it on a secure page.',
  [FAILURE.CORS_BLOCKED]:
    'This stream does not allow other sites to read it, so the browser blocked it.',
  [FAILURE.DEAD_STREAM]:
    'The source did not respond. It is probably offline.',
  [FAILURE.UNSUPPORTED_CODEC]:
    'This device cannot decode this format. The TV apps handle more formats.',
  [FAILURE.BUDGET_EXHAUSTED]:
    'The public relay has reached its monthly limit. Point the app at your own gateway to keep going.',
};

// Escalation is only meaningful for failures a different transport can fix.
export const ESCALATION = {
  [FAILURE.MIXED_CONTENT]: [TIER.GATEWAY, TIER.RELAY],
  [FAILURE.CORS_BLOCKED]: [TIER.GATEWAY, TIER.RELAY],
  [FAILURE.DEAD_STREAM]: [],
  [FAILURE.UNSUPPORTED_CODEC]: [],
  [FAILURE.BUDGET_EXHAUSTED]: [],
};

export function describe(code) {
  return DESCRIPTIONS[code] ?? 'Playback failed for an unknown reason.';
}

export function nextTiers(code) {
  return ESCALATION[code] ?? [];
}

export class PlaybackError extends Error {
  constructor(code, detail = '') {
    super(detail ? `${describe(code)} (${detail})` : describe(code));
    this.name = 'PlaybackError';
    this.code = code;
  }
}
