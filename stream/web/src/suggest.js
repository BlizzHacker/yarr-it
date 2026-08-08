/**
 * Type-ahead.
 *
 * Two rules, and the second one is the one that was broken everywhere else in
 * this client:
 *
 *   1. A keystroke must never queue work that outlives it. Every request
 *      carries an AbortController, and the next keystroke aborts the last one.
 *      Without that, a fast typist leaves a queue of superseded requests, and
 *      whichever finishes last wins -- so the list ends up showing suggestions
 *      for a prefix that was typed three letters ago.
 *   2. Nothing here may ask a torrent indexer. The server's /api/suggest is
 *      memory and archive.org only, for exactly that reason.
 */

/**
 * A debouncer that cancels rather than queues.
 *
 * The difference from the ordinary debounce in main.js matters: that one drops
 * the *call*, this one also aborts the request the previous call started. A
 * dropped call still leaves its network request running.
 */
export function createSuggester({ fetchJson, debounceMs = 160, minChars = 2, onResult, onError }) {
  let timer = null;
  let controller = null;
  // Monotonic, so a response that arrives after a newer request went out is
  // discarded even if its abort lost the race.
  let generation = 0;

  function cancel() {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    if (controller) {
      controller.abort();
      controller = null;
    }
    generation++;
  }

  /**
   * Ask for suggestions for what has been typed so far.
   *
   * Resolves to the suggestion list, or to null when this request was
   * superseded or the input was too short to be worth asking about. Null is not
   * an error and must not be drawn as one.
   */
  function query(text, { kind = '', adult = false } = {}) {
    cancel();
    const q = String(text || '').trim();
    if (q.length < minChars) {
      if (onResult) onResult({ q, suggestions: [] });
      return Promise.resolve([]);
    }

    const gen = ++generation;
    return new Promise((resolve) => {
      timer = setTimeout(async () => {
        timer = null;
        controller = new AbortController();
        const params = new URLSearchParams({ q });
        if (kind) params.set('kind', kind);
        if (adult) params.set('adult', '1');

        try {
          const body = await fetchJson(`/api/suggest?${params}`, { signal: controller.signal });
          if (gen !== generation) return resolve(null);
          const suggestions = (body && body.suggestions) || [];
          if (onResult) onResult({ q, suggestions });
          resolve(suggestions);
        } catch (err) {
          if (gen !== generation) return resolve(null);
          if (err && err.name === 'AbortError') return resolve(null);
          // A suggestion list is a convenience. Failing to draw one is not
          // worth putting an error over a page somebody is typing into.
          if (onError) onError(err);
          resolve(null);
        }
      }, debounceMs);
    });
  }

  return { query, cancel };
}

/**
 * Keyboard behaviour for the list, kept apart from the DOM so it can be tested.
 *
 * Returns the new highlighted index and what the key means, or null when the
 * key is not one this list handles -- in which case the caller must not call
 * preventDefault, or typing in the box stops working.
 */
export function suggestKey(key, { index, count }) {
  if (!count) return null;
  switch (key) {
    case 'ArrowDown':
      return { index: index + 1 >= count ? 0 : index + 1, action: 'move' };
    case 'ArrowUp':
      return { index: index - 1 < 0 ? count - 1 : index - 1, action: 'move' };
    case 'Enter':
      // Enter with nothing highlighted is an ordinary search for what was
      // typed, not a selection. Swallowing it would make the Search button the
      // only way to search, which is worse than no suggestions at all.
      return index >= 0 ? { index, action: 'choose' } : null;
    case 'Escape':
      return { index: -1, action: 'close' };
    case 'Tab':
      return index >= 0 ? { index, action: 'choose' } : { index: -1, action: 'close' };
    default:
      return null;
  }
}

/** A one-line description of where a suggestion came from. */
export function suggestionHint(s) {
  if (!s) return '';
  const bits = [];
  if (s.year) bits.push(String(s.year));
  if (s.platform) bits.push(s.platform);
  else if (s.kind) bits.push(String(s.kind));
  if (s.source === 'results') bits.push('in your results');
  else if (s.source === 'archive') bits.push('plays instantly');
  else if (s.source === 'catalogue') bits.push('in the catalogue');
  return bits.join(' · ');
}
