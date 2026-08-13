import { DOMAIN_DEFAULTS, SHARED_DEFAULTS } from './prefs.js';

const FILTER_KEYS = Object.freeze([
  'sort', 'minSeeders', 'minSizeMB', 'maxSizeMB', 'quality', 'codec',
  'provider', 'webSafe', 'source', 'groups', 'system', 'adult', 'lang',
]);

const SORTS = new Set(['seeders', 'relevance', 'quality', 'size', 'recent', 'title']);
const SOURCES = new Set(['', 'swarm']);

function csv(value, cap = 24) {
  return String(value ?? '').split(',')
    .map((v) => v.trim())
    .filter(Boolean)
    .slice(0, cap);
}

function natural(value, fallback) {
  const n = Number(value);
  return Number.isFinite(n) && n >= 0 ? Math.trunc(n) : fallback;
}

/**
 * Search state carried by a URL.
 *
 * A bare `?q=` is deliberately the default, all-domain search. Saved browser
 * filters are useful while browsing, but they must not silently change what a
 * shared link means. Once a filter is used it is serialized into the URL by
 * paramsForSearch(), so reloading a filtered search remains exact too.
 */
export function filtersFromSearchURL(params) {
  if (!params?.has('q')) return null;

  const explicit = FILTER_KEYS.some((key) => params.has(key));
  const groups = explicit ? csv(params.get('groups'), 12) : [...SHARED_DEFAULTS.groups];
  const sort = params.get('sort');
  const source = params.get('source') || '';

  return {
    groups,
    adult: explicit && params.get('adult') === '1',
    webSafe: explicit && params.get('webSafe') === '1',
    lang: explicit ? String(params.get('lang') || '') : SHARED_DEFAULTS.lang,
    sort: explicit && SORTS.has(sort) ? sort : DOMAIN_DEFAULTS.sort,
    seeders: explicit && params.has('minSeeders')
      ? natural(params.get('minSeeders'), DOMAIN_DEFAULTS.seeders)
      : DOMAIN_DEFAULTS.seeders,
    minSize: explicit ? String(params.get('minSizeMB') || '') : DOMAIN_DEFAULTS.minSize,
    maxSize: explicit ? String(params.get('maxSizeMB') || '') : DOMAIN_DEFAULTS.maxSize,
    quality: explicit ? csv(params.get('quality')) : [...DOMAIN_DEFAULTS.quality],
    codec: explicit ? csv(params.get('codec')) : [...DOMAIN_DEFAULTS.codec],
    systems: explicit ? csv(params.get('system'), 12) : [...DOMAIN_DEFAULTS.systems],
    providers: explicit ? csv(params.get('provider'), 1) : [],
    source: explicit && SOURCES.has(source) ? source : DOMAIN_DEFAULTS.source,
  };
}

/** The canonical, reloadable URL for one search. */
export function paramsForSearch(query, filters) {
  const f = filters || {};
  const p = new URLSearchParams({ q: String(query || ''), sort: f.sort || DOMAIN_DEFAULTS.sort });
  p.set('minSeeders', String(natural(f.seeders, DOMAIN_DEFAULTS.seeders)));
  if (f.minSize) p.set('minSizeMB', String(f.minSize));
  if (f.maxSize) p.set('maxSizeMB', String(f.maxSize));
  if (f.quality?.size || Array.isArray(f.quality) && f.quality.length) p.set('quality', [...f.quality].join(','));
  if (f.codec?.size || Array.isArray(f.codec) && f.codec.length) p.set('codec', [...f.codec].join(','));
  if (f.providers?.size || Array.isArray(f.providers) && f.providers.length) p.set('provider', [...f.providers].join(','));
  if (f.webSafe) p.set('webSafe', '1');
  if (f.source) p.set('source', f.source);
  if (f.groups?.size || Array.isArray(f.groups) && f.groups.length) p.set('groups', [...f.groups].join(','));
  if (f.systems?.size || Array.isArray(f.systems) && f.systems.length) p.set('system', [...f.systems].join(','));
  if (f.adult) p.set('adult', '1');
  if (f.lang) p.set('lang', f.lang);
  return p;
}
