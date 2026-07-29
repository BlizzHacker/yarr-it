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

export function makePlayable({ render, src, mime, tier = TIER.DIRECT, cleanup, mount, subtitles }) {
  if (!RENDERS.has(render)) throw new Error(`unknown render kind: ${render}`);
  // A canvas render is driven by code, not by a src attribute: Ruffle and
  // EmulatorJS are WASM players that need to be handed a container element and
  // told to boot. `mount(el)` is how they get it, and it is required for that
  // render kind precisely because a silent no-op would look like a blank screen.
  if (render === RENDER.CANVAS && typeof mount !== 'function') {
    throw new Error('a canvas playable needs a mount(el) function');
  }
  return {
    render, src, mime, tier,
    cleanup: cleanup ?? (() => {}),
    mount: mount ?? null,
    // Subtitle tracks that shipped inside the source, if any. Loaded lazily:
    // discovering them is free, fetching them is not.
    subtitles: subtitles ?? [],
  };
}

/**
 * A list, not a stream. An .m3u is thousands of channels; conflating it with a
 * Playable is what makes IPTV implementations feel fake.
 */
export function makeCollection({ title, sources }) {
  if (!sources) throw new Error('collection needs sources');
  return { type: 'collection', title, sources: [...sources] };
}

export function isCollection(x) {
  return Boolean(x) && x.type === 'collection';
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
    // Delegate to find() instead of re-running resolvers.find() here, so
    // there is exactly one place that decides which resolver claims an
    // input. Two independent selections that happen to agree today would
    // silently desync the moment one gets priority ordering and the other
    // doesn't.
    async resolve(source, ctx = {}) {
      const resolver = this.find(source.uri);
      if (!resolver) throw new Error(`no resolver for: ${source.uri}`);
      return resolver.resolve(source, ctx);
    },
  };
}
