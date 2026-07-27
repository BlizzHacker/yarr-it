/**
 * The three types the whole player is built on.
 *
 * `render` is a discriminator rather than an assumption that everything is a
 * <video src>. Ruffle and EmulatorJS draw to a canvas and YouTube is an iframe,
 * so normalising every source to a URL would have to be torn out one resolver
 * later.
 */

export const RENDER = {
  VIDEO: 'video',
  AUDIO: 'audio',
  IMAGE: 'image',
  EMBED: 'embed',
  CANVAS: 'canvas',
};

export const TIER = {
  DIRECT: 'direct',
  GATEWAY: 'gateway',
  RELAY: 'relay',
};

const RENDERS = new Set(Object.values(RENDER));

export function makeSource({ kind, uri, meta = {} }) {
  if (!kind) throw new Error('source needs a kind');
  if (!uri) throw new Error('source needs a uri');
  return { kind, uri, meta };
}

export function makePlayable({ render, src, mime, tier = TIER.DIRECT, cleanup }) {
  if (!RENDERS.has(render)) throw new Error(`unknown render kind: ${render}`);
  return { render, src, mime, tier, cleanup: cleanup ?? (() => {}) };
}

/**
 * A list, not a stream. An .m3u is thousands of channels; conflating it with a
 * Playable is what makes IPTV implementations feel fake.
 */
export function makeCollection({ title, sources }) {
  return { title, sources: [...sources] };
}

export function isCollection(x) {
  return Boolean(x) && Array.isArray(x.sources);
}

export function createRegistry() {
  const resolvers = [];
  return {
    register(resolver) {
      resolvers.push(resolver);
      return this;
    },
    find(input) {
      return resolvers.find((r) => r.canHandle(input)) ?? null;
    },
    async resolve(source, ctx = {}) {
      const resolver = resolvers.find((r) => r.canHandle(source.uri));
      if (!resolver) throw new Error(`no resolver for: ${source.uri}`);
      return resolver.resolve(source, ctx);
    },
  };
}
