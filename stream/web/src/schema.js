/**
 * The canonical media vocabulary, imported from the same file the Go server
 * embeds.
 *
 * This is a relative import into ../../search/ rather than a copy, and that is
 * the whole point. The previous arrangement kept the mapping in two places: the
 * browser sent `groups=movies`, the server compared against `video`, and every
 * card was filtered out while the response still reported hundreds of results.
 * Nothing errored, so it survived until someone counted the rows.
 *
 * Clients that cannot import at build time -- Roku, Tizen -- fetch the same
 * bytes from /api/schema.
 */

import schema from '../../search/schema.json' with { type: 'json' };

export const SCHEMA_VERSION = schema.version;
export const DOMAINS = schema.domains;
export const ROLES = schema.roles;
export const CAPABILITIES = schema.capabilities;
export const HEALTH_STATES = schema.health;
export const CHANNEL_SOURCES = schema.channelSources;

/** Fold the spellings that arrive from a URL, a remote, or a config file. */
function token(s) {
  return String(s ?? '').trim().toLowerCase().replace(/[-_\s]/g, '');
}

const byAlias = new Map();
const byType = new Map();
for (const [id, d] of Object.entries(DOMAINS)) {
  byAlias.set(token(id), id);
  for (const a of d.aliases || []) byAlias.set(token(a), id);
  for (const t of d.types || []) byType.set(token(t), id);
}

/**
 * Resolve any name onto a canonical domain id.
 *
 * Returns '' for anything unknown, and callers must read that as "no filter"
 * rather than "match nothing". A filter nothing can satisfy looks identical to
 * a broken server; showing everything is at least an answer someone can argue
 * with.
 */
export function canonicalDomain(name) {
  const t = token(name);
  if (!t) return '';
  return byAlias.get(t) ?? byType.get(t) ?? '';
}

/**
 * Compare two names through their canonical forms.
 *
 * Both sides are normalised deliberately: a card cached before this vocabulary
 * existed still says 'audio' where a new client says 'music'.
 */
export function sameDomain(a, b) {
  const ca = canonicalDomain(a);
  const cb = canonicalDomain(b);
  return ca !== '' && ca === cb;
}

/** Which domain a media type belongs to: 'episode' -> 'video'. */
export function domainOfType(type) {
  return byType.get(token(type)) ?? '';
}

/** The verb for a domain -- watch, listen, read, play, view. */
export function verbFor(name) {
  const id = canonicalDomain(name);
  return id ? DOMAINS[id].verb : '';
}

/** Human label for a domain, for tabs and headings. */
export function labelFor(name) {
  const id = canonicalDomain(name);
  return id ? DOMAINS[id].label : '';
}

/** Every canonical domain id, in a stable order for building UI. */
export function allDomains() {
  return Object.keys(DOMAINS);
}

/**
 * Does this provider advertise the role needed for an action?
 *
 * Callers must ask rather than assume. Radarr has no guide and never will;
 * asking it for one is how a universal front door turns into an if-type tree.
 */
export function hasRole(provider, role) {
  return Array.isArray(provider?.roles) && provider.roles.includes(role);
}

export function hasCapability(provider, capability) {
  return Array.isArray(provider?.capabilities) && provider.capabilities.includes(capability);
}
