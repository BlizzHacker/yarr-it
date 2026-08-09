/**
 * Reading a music card.
 *
 * A pure module with no DOM in it, for the same reason home.js is one: main.js
 * cannot be imported by a test -- it touches `document` at load -- so anything
 * that decides what a card SAYS has to live somewhere a test can reach.
 *
 * The server's shape is stream/search/music.go: a card carries an optional
 * `music` object with the artist, the form, the venue, the place and the date.
 * Nothing else in the app has one, so every function here answers with nothing
 * for a film, a ROM or a comic and no caller has to check first.
 */

/**
 * What a music card says about itself beyond its title.
 *
 * A concert's title is "Grateful Dead Live at Barton Hall, Cornell University
 * on 1977-05-08" and everything worth reading is buried in it. The artist is
 * already shown -- `platform` is where a game card puts its console and a music
 * card puts its artist, so no renderer had to change for that -- and this adds
 * the two facts that separate one show from the four hundred others by the same
 * band: where it was, and when.
 */
export function musicBits(card) {
  const m = card?.music;
  if (!m) return [];
  const out = [];
  if (m.venue) out.push(m.venue);
  // The date only where it is the identity. Two Grateful Dead cards differ by
  // nothing else, so on a concert it is the whole answer; on a single the year
  // is already in the line above and the day is noise.
  if (m.date && m.form === 'concert') out.push(m.date);
  // The town, but only when there is no venue to be more specific than it.
  if (m.place && !m.venue) out.push(m.place);
  return out;
}

/**
 * A track length, as a person writes one.
 *
 * `6:21`, and `1:08:41` once it passes an hour -- the two shapes archive.org
 * itself uses for the same field. Zero and nonsense produce an empty string
 * rather than "0:00", because a track whose length nobody recorded should show
 * nothing instead of claiming to be instantaneous.
 */
export function trackLength(seconds) {
  const n = Math.floor(Number(seconds));
  if (!Number.isFinite(n) || n <= 0) return '';
  const s = n % 60;
  const m = Math.floor(n / 60) % 60;
  const h = Math.floor(n / 3600);
  const pad = (v) => String(v).padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}

/**
 * How one entry in a Collection is labelled.
 *
 * Lives here rather than in library.js because it is the only part of that
 * renderer with a decision in it, and a decision has to be testable without a
 * DOM. library.js itself is DOM and nothing else.
 *
 * `number` and `durationSeconds` are not music-only concepts -- an IPTV
 * playlist entry can carry both -- so an entry that has neither comes back
 * exactly as it did before this existed: its title, or its URI.
 */
export function entryLabel(source) {
  const meta = source?.meta ?? {};
  const title = meta.title || source?.uri || '';
  const parts = [];
  if (Number(meta.number) > 0) parts.push(`${Number(meta.number)}.`);
  parts.push(title);
  const length = trackLength(meta.durationSeconds);
  if (length) parts.push(`· ${length}`);
  return parts.join(' ');
}
