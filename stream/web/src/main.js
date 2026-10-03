import { StreamEngine, classify, needsWebCodecs } from './engine.js';
import { createRegistry, makeSource, makeCollection, isCollection } from './source.js';
import { createTorrentResolver, normalizeMagnet } from './resolvers/torrent.js';
import { urlResolver } from './resolvers/url.js';
import { embedResolver } from './resolvers/embed.js';
import { webmulatorResolver } from './resolvers/webmulator.js';
import { playlistResolver } from './resolvers/playlist.js';
import { flashResolver } from './resolvers/flash.js';
import { gameResolver } from './resolvers/game.js';
import { archiveResolver } from './resolvers/archive.js';
import { createLinkResolver, resolveLink, shouldResolveAsLink } from './resolvers/link.js';
import { renderLink, renderLinkError } from './link-ui.js';
import { fetchMusicLink, isMusicLink, renderMusicLink } from './music-links.js';
import { renderPlayable, detachAll } from './player.js';
import { renderLibrary } from './library.js';
import { whoAmI, displayName, signInURL, signOutURL, vpnGuidance, egressStatus } from './account.js';
import { createAddonAPI, renderAddons, normaliseAddonURL, moveAddon } from './addons.js';
import { keepShaped } from './embedfit.js';
import {
  ROUTE, BIOS_SOURCE, fetchVerdict, toPlayable, canPlay, playerOptions, canSwitchPlayer, isolatedHref,
  choosePlayer, solePlayerSentence, readPlayerPreference, writePlayerPreference,
} from './play.js';
import { renderGuide, hasGuide } from './guide.js';
import { createBiosStore } from './bios.js';
import { identifierFrom, fileFrom } from './resolvers/archive.js';
import {
  getContinueWatching, trackProgress, watchedFraction,
  getLibrary, addToLibrary, removeFromLibrary, keyFor, isSignedIn,
} from './shelf.js';
import { attachSubtitles } from './subtitles.js';
// What a music card says beyond its title. A pure module because main.js
// cannot be imported by a test -- it touches `document` at load -- and a
// decision about what a card SAYS has to be testable.
import { musicBits } from './music.js';
import { PlaybackError } from './failures.js';
import { api, apiFetch, getServer, setServer, probeServer } from './server.js';
import {
  renderHome, itemFromCard, itemFromDiscover, tileAction, tile as homeTile, domainSentence,
  sourceBadge, setPlayProbe,
} from './home.js';
import { mountBrowse, systemChips, systemFilterHost, routeOf } from './browse.js';
import { mountLinear, linearResolver, linearURI } from './linear.js';
import { openReader } from './reader.js';
import {
  SERVICE_TYPES, allServiceTypes, createServiceStore, probeService,
  routeAdvice, describeService, healthLabel, normaliseServiceURL,
} from './services.js';
import { TRANSPORT, extensionAvailable } from './transport.js';
import { runSearch, sleeper, describeProgress } from './progressive.js';
import { createSuggester, suggestKey, suggestionHint } from './suggest.js';
import {
  createPrefs, domainKeyFor, activeFilterCount, describeStored, forgetStored,
  formatBytes, LANGUAGES, DOMAIN_DEFAULTS,
} from './prefs.js';
import { keepVideoFitted } from './videofit.js';
import { filtersFromSearchURL, paramsForSearch } from './search-route.js';
import { offsiteVerb } from './provider-actions.js';
import { playerDownloadURL } from './player-download.js';

const $ = (s) => document.querySelector(s);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

const state = {
  cards: [],
  facets: null,
  // Per-source progress from the latest search. A network provider that is
  // still answering remains visible as pending instead of disappearing from
  // the filter row while the local catalogues race ahead.
  sourceStatus: {},
  query: '',
  // The AbortController for the running search. A search is now a first
  // response plus a poll loop that outlives it, so "the previous search" is a
  // thing that has to be stopped rather than merely ignored.
  searchAbort: null,
  // Whether #grid holds real results yet, as opposed to placeholders from
  // before the first arrival.
  gridLive: false,
  // Type-ahead: the current list and which entry the keyboard is on.
  suggestions: [],
  suggestIndex: -1,
  engine: null,
  registry: null,
  playable: null,
  // Bumped at the start of every play() call. A resolve() that finishes
  // after a newer play() has started is stale -- comparing against the
  // counter it captured is how play() tells "still the one the user last
  // clicked" from "lost the race".
  resolveGen: 0,
  active: null,
  // The open page reader, if any. Held so a second open closes the first
  // rather than leaving its keydown listener on the document.
  reader: null,
  // The archive.org item currently in the player, and what the server said
  // about it. Held so the player switch can restart the SAME item in the other
  // player without a second metadata request, and so closing the player can
  // revoke the BIOS blob URL it handed out.
  game: null,
  filters: {
    seeders: 1, minSize: '', maxSize: '',
    quality: new Set(), codec: new Set(), groups: new Set(), providers: new Set(),
    // Game systems -- snes, genesis, c64. Per-domain, because a machine means
    // nothing outside games; see src/prefs.js.
    systems: new Set(),
    webSafe: false, adult: false, sort: 'seeders',
    // '' means both. A hosted backup always plays; a torrent depends on who
    // is seeding, so which you want is a real question.
    source: '',
    // '' means "say nothing and let the server decide", which is not the same
    // as "no filter": the server defaults MUSIC to English and leaves every
    // other domain alone. See src/prefs.js.
    lang: '',
  },
  // The stop function for the running video fit, so a closed player leaves no
  // ResizeObserver and no inline width behind for the next source.
  stopVideoFit: null,
};

// Everything this browser remembers. Read once, written on every change --
// see applyStoredFilters / persistFilters below.
const prefs = createPrefs();

// ------------------------------------------------------------------ search --

function filterParams() {
  return paramsForSearch(state.query, state.filters);
}

function applyURLFilters(params) {
  const f = filtersFromSearchURL(params);
  if (!f) return;
  state.filters.groups = new Set(f.groups);
  state.filters.adult = f.adult;
  state.filters.webSafe = f.webSafe;
  state.filters.lang = f.lang;
  state.filters.sort = f.sort;
  state.filters.seeders = f.seeders;
  state.filters.minSize = f.minSize;
  state.filters.maxSize = f.maxSize;
  state.filters.quality = new Set(f.quality);
  state.filters.codec = new Set(f.codec);
  state.filters.systems = new Set(f.systems);
  state.filters.providers = new Set(f.providers);
  state.filters.source = f.source;
  syncFilterInputs();
}

/**
 * One JSON call against the search API.
 *
 * A 410 is not an error: it means the job being collected has been swept, which
 * is a normal end to a search that was left running in a background tab. It is
 * handed back as data so the poll loop can stop rather than throw.
 */
async function fetchSearchJSON(url, opts) {
  const res = await apiFetch(url, opts);
  const data = await res.json().catch(() => ({}));
  if (res.status === 410) return { ...data, gone: true, complete: true };
  if (!res.ok) {
    const err = new Error(data.error || `Search failed (${res.status}).`);
    err.status = res.status;
    throw err;
  }
  return data;
}

/**
 * Search, painting results as they arrive.
 *
 * The server answers in a few hundred milliseconds with whatever it already
 * had -- the cache, archive.org -- and finishes the torrent fan-out behind a
 * job id. Before this, the browser waited for the whole thing: measured on the
 * live site, 27 seconds for a cold query and 112 seconds ending in an error
 * while Prowlarr was stalled, with a spinner over an empty screen throughout.
 */
async function search({ showSpinner = true } = {}) {
  // A category with no words is a valid search: "show me games".
  if (!state.query && !state.filters.groups.size) return;

  // Stop whatever the previous search is still doing. A poll loop that outlives
  // its query will happily paint its own results over the new one -- and the
  // older search, being older, usually finishes last and therefore wins.
  if (state.searchAbort) state.searchAbort.abort();
  const ctl = new AbortController();
  state.searchAbort = ctl;
  closeSuggestions();

  // The address is the complete search, not just its words. A copied bare
  // query must never inherit the recipient's hidden Games/provider filter;
  // conversely, reloading a deliberately filtered search must keep it exact.
  history.replaceState(null, '', `?${filterParams()}`);

  $('#filters').hidden = false;
  $('#intro').hidden = true;
  $('#discover').hidden = true;
  $('#get').hidden = true;
  // Puts the channel strip away AND stops its clock, so a page showing search
  // results is not still polling two channels nobody can see.
  tvStrip?.hide();
  // A playlist Collection browsed earlier leaves #library visible (it's
  // only ever shown, never hidden, by renderLibrary). #player is a
  // full-viewport overlay, so that stays invisible right up until the
  // player closes -- then the old channel list resurfaces underneath a
  // brand new, unrelated search. Every fresh search must start clean.
  $('#library').hidden = true;
  $('#saved-library').hidden = true;
  // The paste-a-link panel is shown, never hidden, by renderLink -- so a search
  // started after pasting a link would otherwise leave the old format list
  // sitting above the new results.
  $('#linkpanel').hidden = true;
  if (showSpinner) {
    $('#status').hidden = true;
    showSkeletons();
  }

  state.cards = [];
  // The grid still holds the previous search's tiles, or skeletons. It is
  // cleared on the first arrival that has something to put there, so a slow
  // first paint shows placeholders rather than a blank page.
  state.gridLive = false;
  let painted = 0;

  try {
    const params = filterParams();
    const data = await runSearch({
      url: `/api/search?${params}`,
      updatesURL: (job) => `/api/search/updates?job=${encodeURIComponent(job)}&${params}`,
      fetchJson: fetchSearchJSON,
      wait: sleeper,
      signal: ctl.signal,
      onPaint: ({ cards, added, data: body }) => {
        state.cards = cards;
        if (body.facets) state.facets = body.facets;
		if (body.sources) state.sourceStatus = body.sources;
        // The filter chips are drawn once and again at the end. Redrawing them
        // on every arrival churns the row a person is reaching for.
        if (!painted || body.complete) renderFilters();
        painted++;
        paintCards(added, body);
        updateResultBar(body);
      },
    });
    finishResults(data);
  } catch (err) {
    // A newer search took over. Its results are what should be on screen, so
    // saying anything here would put an error over them.
    if (err && err.name === 'AbortError') return;
    showRetry(err.status
      ? err.message
      : `Could not reach the search service: ${err.message}`, () => search());
  }
}

/** Re-filter without re-querying the indexers; the server filters its cache. */
const refilter = debounce(() => search({ showSpinner: false }), 250);

function showRetry(message, onRetry) {
  const s = $('#status');
  s.replaceChildren(document.createTextNode(message + ' '));
  const b = el('button', 'retry', 'Try again');
  b.type = 'button';
  b.addEventListener('click', () => { s.replaceChildren(); onRetry(); });
  s.append(b);
  s.hidden = false;
}

function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

// -------------------------------------------------- remembering the filters --

/**
 * Which per-domain slice the current selection belongs to.
 *
 * The rule is the server's, not ours: exactly one ticked category is that
 * domain, none or several is "all". See src/prefs.js.
 */
function currentDomainKey() {
  return domainKeyFor(state.filters.groups);
}

/** Put everything this browser remembers back into state and onto the page. */
function applyStoredFilters() {
  const shared = prefs.shared();
  state.filters.groups = new Set(shared.groups);
  state.filters.adult = shared.adult;
  state.filters.webSafe = shared.webSafe;
  state.filters.lang = shared.lang;
  loadDomainFilters();
}

/** The slice for whichever domain is selected now, into state and the inputs. */
function loadDomainFilters() {
  const d = prefs.domain(currentDomainKey());
  state.filters.sort = d.sort;
  state.filters.seeders = d.seeders;
  state.filters.minSize = d.minSize;
  state.filters.maxSize = d.maxSize;
  state.filters.quality = new Set(d.quality);
  state.filters.codec = new Set(d.codec);
  state.filters.systems = new Set(d.systems);
  state.filters.providers = new Set(d.provider ? [d.provider] : []);
  // `instant` was the old combined Site files chip. The four named source
  // controls supersede it; carrying the hidden old value forward would make a
  // filter active with no button showing why.
  state.filters.source = d.source === 'instant' ? '' : d.source;
  syncFilterInputs();
}

/**
 * Write the current filters back.
 *
 * Called after every change, including the ones that only move a chip. There is
 * no "save" here and there must not be: a preference somebody has to confirm is
 * a preference most people lose.
 */
function persistFilters() {
  const f = state.filters;
  prefs.setShared({
    groups: [...f.groups],
    adult: f.adult,
    webSafe: f.webSafe,
    lang: f.lang,
  });
  prefs.setDomain(currentDomainKey(), {
    sort: f.sort,
    seeders: f.seeders,
    minSize: f.minSize,
    maxSize: f.maxSize,
    quality: [...f.quality],
    codec: [...f.codec],
    systems: [...f.systems],
    source: f.source,
		provider: [...f.providers][0] || '',
  });
  renderFilterReset();
}

/**
 * Change which categories are ticked, keeping each domain's own numbers.
 *
 * The order matters and is the whole reason this is a function rather than two
 * lines at the call site: the outgoing domain's slice has to be written under
 * the OLD key before the selection moves, or "40 seeders, sorted by size" set
 * for films is saved against music the instant somebody ticks Music.
 */
function changeGroups(mutate) {
  persistFilters();          // under the key that is about to stop being current
  mutate(state.filters.groups);
  prefs.setShared({ groups: [...state.filters.groups] });
  loadDomainFilters();       // and the new domain's own numbers come back
  renderFilters();
}

/** Push state into the controls that hold a value rather than a class. */
function syncFilterInputs() {
  const f = state.filters;
  const set = (sel, value) => { const n = $(sel); if (n) n.value = value; };
  set('#f-seeders', f.seeders);
  set('#f-minsize', f.minSize);
  set('#f-maxsize', f.maxSize);
  set('#f-sort', f.sort);
  set('#f-lang', f.lang);
  renderFilterReset();
}

/**
 * The clear button, saying whether there is anything to clear.
 *
 * Filters survive a reload now, which makes this the difference between a
 * catalogue that looks small and a catalogue somebody narrowed last week and
 * forgot about.
 */
function renderFilterReset() {
  const b = $('#filter-reset');
  if (!b) return;
  const n = activeFilterCount(state.filters);
  b.classList.toggle('armed', n > 0);
  b.textContent = n ? `Clear ${n} filter${n === 1 ? '' : 's'}` : 'Reset';
  b.title = n
    ? 'These are remembered between visits. This puts them all back.'
    : 'Nothing is filtered right now.';
}

/**
 * Set the language everywhere it is shown, and remember it.
 *
 * Two controls point at one value -- the one on the filter bar, where you meet
 * it, and the one in Settings, where you would go looking for it -- so the
 * write goes through here rather than through either of them.
 */
function setLanguage(value) {
  state.filters.lang = String(value ?? '');
  persistFilters();
  const bar = $('#f-lang');
  if (bar) bar.value = state.filters.lang;
  const panel = $('#set-lang');
  if (panel) panel.value = state.filters.lang;
}

/**
 * Put every filter back, everywhere, and say so by re-running the search.
 *
 * Both the bar's Clear button and the one in Settings land here. It clears the
 * STORED filters as well as the live ones -- a reset that a reload undoes is
 * not a reset -- and leaves appearance and playback alone, because "clear my
 * filters" is a sentence about a search.
 */
function clearAllFilters({ research = true } = {}) {
  prefs.clearFilters();
  state.filters.groups = new Set();
  state.filters.quality = new Set();
  state.filters.codec = new Set();
  state.filters.systems = new Set();
  state.filters.providers = new Set();
  state.filters.adult = false;
  state.filters.webSafe = false;
  state.filters.lang = '';
  loadDomainFilters();
  renderFilters();
  // The panel's copies of adult, web-safe and language are separate controls
  // pointed at the same values, so clearing from the bar has to repaint them.
  // Without this the panel went on offering "French" after the bar had gone
  // back to the default -- two surfaces disagreeing about one setting, which is
  // the failure that makes a settings panel worse than no settings panel.
  syncSettingsControls();
  renderSavedFilters();
  if (research) search({ showSpinner: false });
}

/** The language menu, built from the vocabulary rather than written by hand. */
function fillLanguageMenu(node) {
  if (!node || node.options.length) return;
  for (const [value, label] of LANGUAGES) {
    const o = document.createElement('option');
    o.value = value;
    o.textContent = label;
    node.append(o);
  }
}

// ----------------------------------------------------------------- filters --

function renderFilters() {
  const f = state.facets;
  // The filter bar stays visible from the first paint. Hiding it until results
  // arrive means the one moment you would want to narrow a search -- before
  // running it -- is the one moment the controls are missing.
  $('#filters').hidden = false;
  $('#filters').classList.toggle('awaiting', !f);

  // The categories are drawn from the start, with counts filled in once a
  // search has produced them. Rendering them only from facets meant they did
  // not exist until after a search had already run, so a category could only
  // ever narrow results you had -- never ask for a category in the first
  // place, which is the first thing anyone tries.
  groupRow($('#f-groups'), categoryChips(f), state.filters.groups);
  renderLanguageFilter(f);
  renderFilterReset();

  // WHICH CHIPS ARE LIT depends only on what is set, so it is painted whether
  // or not a search has run. It used to sit below the `if (!f) return` with the
  // COUNTS, and the consequence only showed once these settings became things
  // you could change from somewhere else: ticking "Show adult results" in the
  // panel, or arriving with one remembered from last week, left the 18+ chip
  // dark on the landing page while the filter was on. A control that lies about
  // its own state is worse than one that is missing.
  $('#f-adult').classList.toggle('on', state.filters.adult);
  $('#f-websafe').classList.toggle('on', state.filters.webSafe);
  renderSourceFilters(f);

  // The COUNTS are facts about a result set, so they wait for one.
  if (!f) return;
  $('#f-adult').textContent = f.adultCount ? `18+ ${f.adultCount}` : '18+';
  chipRow($('#f-quality'), f.qualities, state.filters.quality);
  chipRow($('#f-codec'), f.codecs, state.filters.codec);
  // Deliberately inside the facet-dependent half: with no results there is no
  // honest list of machines to offer, and the whole catalogue is the wrong
  // answer -- see systemChips.
  systemChips(systemFilterHost(), f.systems, state.filters.systems, () => {
    // persist as well as re-search, exactly as every other chip row on this bar
    // does: a filter that a reload forgets is the defect prefs.js replaced.
    persistFilters();
    refilter();
  });
  renderSourceFilters(f);
}

function renderSourceFilters(facets) {
  const counts = new Map((facets?.providers || []).map((p) => [p.value.toLowerCase(), p.count]));
  const provider = [...state.filters.providers][0] || '';
  const known = [
    ['Archive.org', 'archive.org', 'archive', 'Hosted by Archive.org and playable here', false],
    ["Vimm's Lair", "Vimm's Lair", 'vimm', 'Vimm catalogue, Yarr.It vault, and public Vimm actions', false],
    ['The ROM Depot', 'The ROM Depot', 'theromdepot', 'Account required to download', false],
    ['Webmulator', 'Webmulator', 'webmulator', 'Public hosted browser player', true],
    ['Retrostic', 'Retrostic', 'retrostic', 'Public hosted player; browser required to download', true],
    ['EmuParadise', 'EmuParadise', 'emuparadise', 'Discovery only; game files are no longer offered', true],
    ['RomuLation', 'RomuLation', 'romulation', 'Paid account and signed-in browser required to download', true],
    ['CoolROM', 'CoolROM', 'romarr-catalog', 'Browser-captured catalogue pages', true],
    ['CDRomance', 'CDRomance', 'romarr-catalog', 'Browser-cleared, captured catalogue pages', true],
  ];
  const byKey = new Map(known.map((row) => [row[1].toLowerCase(), row]));
  for (const facet of facets?.providers || []) {
    if (!byKey.has(facet.value.toLowerCase())) {
      const row = [facet.value, facet.value, '', `Results from ${facet.value}`, false];
      known.push(row);
      byKey.set(facet.value.toLowerCase(), row);
    }
  }
  const host = $('#f-providers');
  if (host) {
    const gameContext = state.filters.groups.has('games')
      || state.cards.some((card) => card.kind === 'game');
    const visible = known.filter(([, key, , , gameOnly]) => !gameOnly
      || gameContext
      || counts.has(key.toLowerCase())
      || provider.toLowerCase() === key.toLowerCase());
    host.replaceChildren(...visible.map(([name, key, stage, hint]) => {
      const count = counts.get(key.toLowerCase());
      const pending = count == null && stage && state.sourceStatus?.[stage] === 'pending';
      const node = el('button', 'chip', count != null ? `${name} ${count}` : (pending ? `${name} …` : name));
      node.type = 'button';
      node.dataset.provider = key;
      node.title = hint;
      node.classList.toggle('on', provider.toLowerCase() === key.toLowerCase());
      return node;
    }));
  }
  const torrents = $('#f-swarm');
  if (torrents) {
    torrents.textContent = facets?.swarmCount ? `Torrents ${facets.swarmCount}` : 'Torrents';
    torrents.classList.toggle('on', state.filters.source === 'swarm');
  }
}

/**
 * The language control, drawn where and when it can do something.
 *
 * The server publishes a `languages` facet only when the results carry language
 * facts, which today means music -- and it counts them BEFORE applying the
 * filter, so the numbers say what the default is costing rather than what
 * survived it. Ticking Music also brings the control up before any search has
 * run, because that is the moment somebody would want it.
 *
 * A language chosen and then left set is still shown, whatever the results are.
 * Hiding the control that is doing the filtering is exactly how a default
 * nobody can see ends up looking like an empty catalogue.
 */
function renderLanguageFilter(facets) {
  const group = $('#f-lang-group');
  const sel = $('#f-lang');
  if (!group || !sel) return;
  fillLanguageMenu(sel);

  const counts = facets?.languages || [];
  const relevant = counts.length > 0
    || state.filters.lang !== ''
    || state.filters.groups.has('music');
  group.hidden = !relevant;
  if (!relevant) return;

  sel.value = state.filters.lang;
  // The option that is the default gets the number it is hiding, when there is
  // one to give: "Any language 214" is the whole argument for pressing it.
  const any = [...sel.options].find((o) => o.value === 'any');
  if (any) {
    const total = counts.reduce((n, c) => n + (Number(c.count) || 0), 0);
    any.textContent = total ? `Any language · ${total}` : 'Any language';
  }
  sel.title = counts.length
    ? `In these results: ${counts.slice(0, 6).map((c) => `${c.value} ${c.count}`).join(', ')}`
    : '';
}

// Human labels for the Newznab buckets the API reports.
// Every category the indexers bucket into, so the chips exist before any
// search has run. 'adult' is deliberately absent -- it has its own explicit
// 18+ toggle and must never be enabled by ticking a category box.
const ALL_GROUPS = ['movies', 'tv', 'anime', 'games', 'comics', 'music', 'books', 'apps', 'other'];

const GROUP_LABELS = {
  movies: 'Movies', tv: 'TV', anime: 'Anime', music: 'Music',
  games: 'Games', comics: 'Comics', apps: 'Apps', books: 'Books', other: 'Other',
};

/**
 * Every category, always, with whatever counts the last search produced.
 *
 * The row used to BE the facets, falling back to the full list only when there
 * were no facets at all. Two failures came out of that, and the second one is
 * severe:
 *
 *   - A search that matched some categories drew only those, so the only way
 *     back to TV from a films-only result set was to clear the search.
 *   - A search that matched NOTHING sent back `groups: []` -- an empty array,
 *     not a missing field -- which is truthy, so the fallback never fired and
 *     every category chip disappeared. Measured on this build: tick Movies, set
 *     minimum seeders to 40, and the SHOW row empties. The controls you would
 *     use to get out of an over-narrow filter are the ones the over-narrow
 *     filter removes.
 *
 * Now filters are remembered between visits, that state is reachable on a cold
 * load rather than only after a sequence of clicks, which turns an awkward
 * moment into a page that looks broken on arrival.
 *
 * A count of 0 is drawn as 0 rather than left blank: "TV 0" is a fact about
 * this search, and it is the fact that explains an empty grid.
 */
function categoryChips(facets) {
  const counts = new Map();
  for (const g of facets?.groups || []) counts.set(g.value, g.count);
  // Anything the server reports that this build has never heard of still gets a
  // chip, at the end -- a category added server-side should appear here without
  // a client release.
  const names = [...ALL_GROUPS, ...counts.keys()].filter(
    (v, i, all) => all.indexOf(v) === i,
  );
  return names.map((value) => ({
    value,
    count: facets ? (counts.get(value) ?? 0) : null,
  }));
}

/**
 * Category checkboxes, in the spirit of a torrent site's category bar.
 * 'adult' is excluded here because it has its own explicit 18+ toggle — it
 * should never be something you enable by accident while ticking boxes.
 */
function groupRow(host, values, selected) {
  host.replaceChildren();
  for (const { value, count } of (values || [])) {
    if (value === 'adult') continue;
    const c = el('span', 'chip', GROUP_LABELS[value] || value);
    // Before a search there is nothing to count yet.
    if (count != null) c.append(el('span', 'cnt', String(count)));
    if (selected.has(value)) c.classList.add('on');
    c.addEventListener('click', () => {
      // Through changeGroups so the outgoing domain's own numbers are written
      // under its own name before the selection moves, and the incoming
      // domain's come back. This also redraws every chip, so nothing here
      // toggles a class by hand any more.
      changeGroups((groups) => {
        groups.has(value) ? groups.delete(value) : groups.add(value);
      });
      // With no words typed, picking a category IS the search -- "show me
      // games" -- so it runs one rather than re-filtering an empty result set.
      // A chip is a choice, not a command. Firing a search the instant one is
      // ticked means the page runs off and fetches something while you are
      // still deciding what you want -- and ticking two categories fired two
      // searches, the first of them wasted.
      //
      // So: if results are already on screen, narrowing them is instant and
      // free (the server filters its cache). If there are none, the chip just
      // arms the search and the Search button runs it.
      if (state.cards.length) refilter();
      else armSearch();
    });
    host.append(c);
  }
}

function chipRow(host, values, selected) {
  host.replaceChildren();
  for (const { value, count } of (values || []).slice(0, 7)) {
    const c = el('span', 'chip', value);
    c.title = `${count} source${count === 1 ? '' : 's'}`;
    if (selected.has(value.toLowerCase())) c.classList.add('on');
    c.addEventListener('click', () => {
      const k = value.toLowerCase();
      selected.has(k) ? selected.delete(k) : selected.add(k);
      c.classList.toggle('on');
      persistFilters();
      refilter();
    });
    host.append(c);
  }
}

/** Placeholder tiles so the grid has shape while a cold search runs. */
function showSkeletons(n = 12) {
  const grid = $('#grid');
  grid.replaceChildren();
  for (let i = 0; i < n; i++) {
    const t = el('div', 'tile skel');
    t.append(el('div', 'poster'));
    t.append(el('div', 'tname-sk'));
    t.append(el('div', 'tmeta-sk'));
    grid.append(t);
  }
}

// ----------------------------------------------------------------- results --

/**
 * Add the cards that are new since the last arrival.
 *
 * Appended, never re-sorted. Somebody can start reading and clicking before the
 * search has finished, and a tile that moves between the decision and the click
 * is worse than a tile that arrives late.
 */
function paintCards(added, data) {
  const grid = $('#grid');
  if (!state.gridLive) {
    // Nothing yet and more is coming: leave the placeholders in place. Clearing
    // them here is what produces a spinner over an empty screen.
    if (!added.length && !data.complete) return;
    grid.replaceChildren();
    state.gridLive = true;
  }
  for (const c of added) grid.append(tile(c));
}

/**
 * The line above the grid: how many, for what, and what is still coming.
 *
 * All on one line and rewritten in place, deliberately. A note that appears
 * below the bar while a search runs and vanishes when it finishes moves the
 * entire grid up by a line at the exact moment somebody is aiming at it.
 */
function updateResultBar(data) {
  const bar = $('#resultbar');
  bar.replaceChildren();
  bar.hidden = false;

  const progress = describeProgress(data);
  bar.append(el('b', null, `${state.cards.length}`));

  // A category browse has no query, so naming one renders as empty quotes.
  const picked = [...state.filters.groups].map((g) => GROUP_LABELS[g] || g);
  const what = state.query
    ? `for “${state.query}”`
    : (picked.length ? `in ${picked.join(' + ')}` : '');
  const counted = `result${state.cards.length === 1 ? '' : 's'} ${what}`.trim()
    + (!progress.done ? ' so far' : '')
    + (data.total && data.total !== state.cards.length ? ` · ${data.total} before filters` : '');
  bar.append(el('span', null, counted));

  if (progress.text) {
    bar.append(el('span', `progress ${progress.tone}`, progress.text));
  }
  if (data.stale) {
    bar.append(el('span', 'progress degraded',
      'Showing the last known results — search again for fresh ones.'));
  }
}

/** The end of a search: say so, or say there was nothing. */
function finishResults(data) {
  updateResultBar(data);
  if (state.cards.length) {
    $('#status').hidden = true;
    return;
  }
  $('#grid').replaceChildren();
  state.gridLive = true;
  const progress = describeProgress(data);
  $('#status').textContent = progress.tone === 'degraded'
    ? `${progress.text} Nothing was found for this search.`
    : 'Nothing matched. Try loosening the filters.';
  $('#status').hidden = false;
}

function tile(card) {
  const t = el('button', 'tile');
  t.type = 'button';

  const p = el('div', 'poster');
  // The placeholder is drawn whether or not there is a poster to try, and the
  // image is laid over it. Half these results are archive.org items whose
  // thumbnail service answers with an error rather than a picture, and a
  // broken-image glyph in a 2:3 box is the worst way to say "no cover".
  p.append(el('div', 'noart', card.title));
  if (card.art?.poster) {
    const img = el('img');
    img.loading = 'lazy';
    img.alt = '';
    img.addEventListener('error', () => { img.remove(); });
    img.src = card.art.poster;
    p.append(img);
  }

  // A seeder count is a guess at whether something will play. For a result
  // served by a host that is always up there is nothing to guess, so showing
  // "0▲" there would read as broken when it is the most reliable card on the
  // page.
  const src = sourceBadge(card);
  const badge = el('span', src.cls, src.text);
  badge.title = src.hint;
  p.append(badge);
  if (card.art?.rating) p.append(el('span', 'rating', card.art.rating.toFixed(1)));
  const bq = card.platform || card.sources[card.best]?.quality;
  if (bq) p.append(el('span', 'best-q', bq));
  // What this card is for, in its own domain's word. A search for "batman"
  // returns films, comics and games together, and until now every one of them
  // was offered with the same silent "click me".
  p.append(el('span', `verb verb-${card.kind || 'other'}`, tileAction(itemFromCard(card, card.kind)).label));
  t.append(p);

  t.append(el('div', 'tname', card.title));
  const bits = [];
  if (card.year) bits.push(card.year);
  if (card.isSeries) bits.push(`S${card.season}E${card.episode}`);
  bits.push(...musicBits(card));
  if (card.external) {
    bits.push(`on ${card.external.name}`);
    // What is actually on offer over there, in the site's own two words. An
    // entry can be downloadable, playable in their player, or both, and those
    // are different enough that a person wants to know before they leave.
    const what = offsiteOffers(card);
    if (what) bits.push(what);
  } else if (card.instant) {
    bits.push('plays instantly');
  } else {
    bits.push(`${card.sources.length} source${card.sources.length === 1 ? '' : 's'}`);
  }
  t.append(el('div', 'tmeta', bits.join(' · ')));

  t.addEventListener('click', () => openCard(card));
  return t;
}


/**
 * Open a search result the way its own domain says it should open.
 *
 * A comic and a film both arrive here as a card, and both used to land in the
 * detail sheet, whose only offer is a source to stream. For anything you read
 * or look at there is nothing to stream: the source is a details page on
 * archive.org, and streaming it meant an iframe of somebody else's website.
 */
function openCard(card) {
  const action = tileAction(itemFromCard(card, card.kind));
  if (action.kind === 'reader') {
    openReaderFor(action.id, card.title, action.verb);
    return;
  }
  openDetail(card);
}

// ---------------------------------------------------------------- discover --

/**
 * Browsable landing rows.
 *
 * These are catalogue entries, not torrents — nothing here is indexed or
 * hosted.
 *
 * They used to be pure TMDB, and clicking one ran an ordinary search for its
 * title, "which is where any actual sources come from". That sentence was the
 * bug. A tile arrived holding a real identity and threw it away to go looking
 * for its own display name, so the tile promised something the search had not
 * yet been asked about — and on 2026-08-08, 171 of 172 catalogue tiles on the
 * live page reached zero results while looking exactly like the ones that
 * worked.
 *
 * A tile now says what is actually known about it. Where a source was found and
 * verified server-side (search/discover_resolve.go) the tile carries that
 * address and the click opens it. Where nothing has been established — because
 * the only backend that could answer is unreachable, or because asking it 172
 * times to build one page would take it down — the tile offers to go and look
 * and is labelled "Find" rather than "Watch".
 *
 * What it does NOT do is hide the second kind. Deleting a tile because a
 * backend was down is how five shelves vanished for an afternoon, which is the
 * same lie as the original defect pointing the other way.
 */
/**
 * Put the landing page back after the last category is deselected.
 *
 * Deselecting into an empty search would otherwise leave the results grid
 * showing whatever the previous category returned, with nothing selected to
 * explain it.
 */
function restoreLanding() {
  state.cards = [];
  state.facets = null;
  $('#grid').replaceChildren();
  $('#resultbar').hidden = true;
  $('#status').hidden = true;
  $('#library').hidden = true;
  $('#saved-library').hidden = true;
  $('#filters').hidden = false;
  $('#intro').hidden = false;
  $('#discover').hidden = false;
  $('#get').hidden = false;
  // show() catches the channels up on whatever has been on in the meantime,
  // and stays away entirely if there were never any channels to draw.
  tvStrip?.show();
  renderFilters();
}

function libraryItemURI(item) {
  const key = String(item?.key || '');
  return key.startsWith('ia:')
    ? `https://archive.org/details/${encodeURIComponent(key.slice(3))}`
    : '';
}

/** The signed-in shelf reached from the header. */
async function showSavedLibrary() {
  if (homeAbort) homeAbort.abort();
  state.searchAbort?.abort();
  tvStrip?.hide();
  for (const selector of ['#intro', '#discover', '#get', '#resultbar', '#grid', '#linkpanel', '#filters']) {
    $(selector).hidden = true;
  }
  // #library is the transient playlist/torrent-collection browser. A personal
  // shelf needs its own mount so a later play or search cannot erase it.
  const host = $('#saved-library');
  host.replaceChildren(el('h2', null, 'Your library'));
  host.append(el('p', 'lede', 'Titles you saved. Open one to use its exact Archive item or search every live source again.'));
  host.hidden = false;

  let items = [];
  try {
    items = await getLibrary();
  } catch {
    host.append(el('p', 'set-none', 'Your library could not be reached. Try again in a moment.'));
    return;
  }
  if (!isSignedIn()) {
    const p = el('p', 'set-none', 'Sign in to see the library that follows you between devices. ');
    const a = el('a', 'btn btn-ghost', 'Sign in');
    a.href = signInURL('/?library=1');
    p.append(a);
    host.append(p);
    return;
  }
  if (!items.length) {
    host.append(el('p', 'set-none', 'Nothing saved yet. Open any result and choose “Save to library”.'));
    return;
  }

  const shelf = el('section', 'shelf');
  shelf.append(el('h3', null, `Saved · ${items.length}`));
  const rail = el('div', 'rail');
  for (const saved of items) {
    rail.append(homeTile({
      domain: saved.kind || '', mediaType: saved.kind || '',
      title: saved.title || saved.key, year: saved.year || 0,
      poster: saved.poster || '', uri: libraryItemURI(saved), card: null,
    }, tileHandlers()));
  }
  shelf.append(rail);
  host.append(shelf);
}

/**
 * Ask for everything one domain has, with no query.
 *
 * This is the fallback for a domain /api/discover has no curated row for --
 * today Music and Images. It is not the first choice because a cold browse is
 * a full indexer fan-out: measured against the live server, 20-45s cold and
 * about 0.3s once the 45-minute cache has it. Discover, by contrast, is
 * cached for three hours and answers immediately.
 */
async function browseDomain(domain, signal) {
  // Books and audiobooks are two different shelves in the same domain. Fetch
  // their exact Archive categories together so adding LibriVox never replaces
  // the printed Gutenberg shelf.
  if (domain === 'literature') {
    const keys = ['ia:col:gutenberg', 'ia:col:librivox'];
    const params = new URLSearchParams({ limit: '24' });
    for (const key of keys) params.append('key', key);
    const res = await apiFetch(`/api/rows?${params}`, { signal });
    if (!res.ok) throw new Error(`browse ${domain}: ${res.status}`);
    const data = await res.json();
    const rows = new Map((data.rows || []).map((row) => [row.key, row]));
    return {
      shelves: [
        {
          title: 'Public domain classics',
          items: rows.get('ia:col:gutenberg')?.items || [],
        },
        {
          title: 'Audiobooks',
          mediaType: 'audio',
          items: rows.get('ia:col:librivox')?.items || [],
        },
      ],
    };
  }

  // minSeeders=0 because a hosted archive.org result has no swarm at all, and
  // the default of 1 would drop the only results these rows have.
  //
  // The signal is not optional garnish. A browse is issued for every domain
  // discover has no row for, and someone who types a search two seconds after
  // the page opens leaves all of them in flight -- filling the connection pool
  // the search itself needs, for rows that are about to be hidden.
  // Music's normal default is English. A landing shelf is broader than a
  // language-filtered search and, crucially, uses a distinct cache entry from
  // a failed cold English browse instead of letting that empty entry remove
  // the Music section for its whole TTL.
  const lang = domain === 'music' ? '&lang=any' : '';
  const res = await apiFetch(
    `/api/search?kind=${encodeURIComponent(domain)}&minSeeders=0${lang}`, { signal });
  if (!res.ok) throw new Error(`browse ${domain}: ${res.status}`);
  const data = await res.json();
  // A rail is a rail, not a result set; the rest is a search away.
  const cards = (data.cards || []).slice(0, 24);
  if (domain === 'music') {
    return { title: 'Music from Archive.org', mediaType: 'music', cards };
  }
  return cards;
}

/**
 * The landing page: one section per domain in schema.json, in that order.
 *
 * Everything about which sections exist, what they are called and what verb
 * their cards carry comes from the vocabulary — see home.js. This function
 * only supplies the two things a module of pure structure cannot have: where
 * the data comes from, and what a click does.
 */
// Everything the landing page has in flight. Aborted the moment a search
// starts: the rails are about to be hidden, and their requests would otherwise
// go on competing with the search for the same connections.
let homeAbort = null;

/**
 * Nostalgia TV, above everything else.
 *
 * Mounted separately from loadHome and never awaited by it. The channel calls
 * and /api/discover are independent, so a slow channel service costs the strip
 * and nothing below it -- the eighteen discover rows paint on their own clock,
 * which is the standard this page was measured against.
 */
let tvStrip = null;

function mountChannels() {
  const host = $('#tv');
  if (!host) return;
  tvStrip = mountLinear(host, {
    fetchImpl: apiFetch,
    // The channel goes through the ordinary player by the ordinary route: the
    // registry resolves `yarrit-linear:<id>` into a positioned Playable. Live
    // is declared on the card so nothing downstream records a resume point for
    // something that cannot be resumed.
    onWatch: (channel, model) => {
      play({ title: channel.name, year: 0, live: true },
        { uri: linearURI(channel.id), title: model.title });
      // play() has already put "Resolving…" up by the time it hands back its
      // promise -- everything before its first await is synchronous. This says
      // the same thing in the vocabulary of the thing being pressed, and the
      // wait it covers is real: the first join to a new programme makes the
      // server prove the file can be ranged before it will promise an offset.
      setPlayerStatus(`Tuning in to ${channel.name}…`);
    },
  });
}

async function loadHome() {
  if (homeAbort) homeAbort.abort();
  homeAbort = new AbortController();
  const homeSignal = homeAbort.signal;
  const host = $('#discover');
  let rows = [];
  let indexer = null;
  try {
    const res = await apiFetch('/api/discover', { signal: homeSignal });
    if (res.ok) {
      const body = await res.json();
      rows = body.rows || [];
      // Which backends answered when the page was built. A shelf of "Find"
      // tiles is honest, and unexplained it still reads as the site having
      // got worse — see backendNotice.
      indexer = body.indexer || null;
    }
  } catch {
    // No curated rows is survivable: every domain simply falls back to a
    // browse, which is slower but is still a landing page.
  }

  // What you already started comes first. Anyone signed out gets an empty
  // list and no row, which is the correct amount of nagging.
  let resume = null;
  try {
    const started = await getContinueWatching();
    if (started.length) resume = resumeShelf(started);
  } catch {
    /* the resume row is a bonus; the catalogue still renders without it */
  }

  try {
    await renderHome(host, {
      discoverRows: rows,
      indexer,
      browse: (domain) => browseDomain(domain, homeSignal),
      resume,
      handlers: tileHandlers(),
    });
  } catch {
    /* discovery is a nicety; a failure just leaves the intro copy in place */
  }
}

/**
 * What a click on a tile does. One object, because the landing rails and a
 * category page must behave identically -- a SNES game opened from /browse has
 * to reach the player the same way the same tile does from the Games shelf.
 */
function tileHandlers() {
  return {
    onActivate(item, action) {
      if (action.kind === 'reader') {
        openReaderFor(action.id, item.title, action.verb);
        return;
      }
      if (action.kind === 'open') {
        // A card came from a search and has sources to choose between; a
        // discover item with a play target IS the thing and opens directly.
        if (item.card) openCard(item.card);
        else play({ title: item.title, year: item.year || 0 },
          { uri: item.uri, title: item.title });
        return;
      }
      // A catalogue entry is a name to go looking for.
      const q = item.year ? `${item.title} ${item.year}` : item.title;
      $('#q').value = q;
      state.query = q;
      history.replaceState(null, '', `?q=${encodeURIComponent(q)}`);
      search();
    },
  };
}

/** The Continue Watching row, newest first. */
function resumeShelf(items) {
  const shelf = el('section', 'shelf');
  shelf.append(el('h3', null, 'Continue watching'));
  const rail = el('div', 'rail');
  for (const p of items) rail.append(resumeTile(p));
  shelf.append(rail);
  return shelf;
}

function resumeTile(p) {
  const t = el('button', 'tile');
  t.type = 'button';
  const label = p.title || p.key;
  t.title = `Resume ${label}`;

  const poster = el('div', 'poster');
  if (p.poster) {
    const img = el('img');
    img.loading = 'lazy';
    img.alt = label;
    img.src = p.poster;
    poster.append(img);
  } else {
    poster.append(el('div', 'noart', label));
  }

  // A bar across the artwork says how far in you are without needing a number.
  const bar = el('div', 'progress');
  const fill = el('div', 'progress-fill');
  fill.style.width = `${Math.round(watchedFraction(p) * 100)}%`;
  bar.append(fill);
  poster.append(bar);
  t.append(poster);

  t.append(el('div', 'tname', label));
  const mins = Math.max(0, Math.round((p.duration - p.position) / 60));
  t.append(el('div', 'tmeta', p.duration ? `${mins} min left` : 'Resume'));

  t.addEventListener('click', () => {
    const q = p.title || p.key;
    $('#q').value = q;
    state.query = q;
    history.replaceState(null, '', `?q=${encodeURIComponent(q)}`);
    search();
  });
  return t;
}

// ------------------------------------------------------------------ reader --

/**
 * The overlay that shows a comic, a book or a picture set as pages.
 *
 * Kept beside the player rather than inside it: they share nothing but a
 * z-index. The player streams bytes into a media element; this one steps
 * through JPEGs from /api/pages and never touches the torrent engine.
 */
function readerElements() {
  return {
    root: $('#reader'),
    title: $('#reader-title'),
    count: $('#reader-count'),
    stage: $('#reader-stage'),
    img: $('#reader-page'),
    embed: $('#reader-embed'),
    prev: $('#reader-prev'),
    next: $('#reader-next'),
    status: $('#reader-status'),
  };
}

function openReaderFor(id, title, verb) {
  state.reader?.close();
  document.body.style.overflow = 'hidden';
  state.reader = openReader({
    id,
    title,
    verb,
    els: readerElements(),
    apiFetch,
    onClose: () => {
      state.reader = null;
      // A detail sheet left open underneath still wants the page frozen.
      if ($('#detail').hidden) document.body.style.overflow = '';
    },
  });
}

function closeReader() {
  state.reader?.close();
  state.reader = null;
}

// ------------------------------------------------------------------ detail --

function openDetail(card) {
  state.active = card;
  $('#detail').hidden = false;
  document.body.style.overflow = 'hidden';

  $('#d-hero').style.backgroundImage = card.art?.backdrop ? `url("${card.art.backdrop}")` : '';
  $('#d-poster').style.backgroundImage = card.art?.poster ? `url("${card.art.poster}")` : '';
  $('#d-title').textContent = card.title;

  const sub = [];
  if (card.year) sub.push(card.year);
  if (card.isSeries) sub.push(`Season ${card.season}, Episode ${card.episode}`);
  if (card.art?.rating) sub.push(`★ ${card.art.rating.toFixed(1)}`);
  if (card.platform) sub.push(card.platform);
  // Venue and date, for a card whose title is a sentence and whose identity is
  // a place and a day. See musicBits.
  sub.push(...musicBits(card));
  // Where it came from, then what that means for getting hold of it — the same
  // two facts the tile's badge carried, with room here to say them in words.
  const named = card.origin || card.sources?.[card.best ?? 0]?.indexer || '';
  if (card.external) {
    sub.push(`Catalogued on ${card.external.name}`);
  } else if (card.instant) {
    sub.push(`${named || 'Hosted'} — plays instantly, no download`);
  } else {
    sub.push(named ? `${named} · ${card.seeders} seeders` : `${card.seeders} seeders`);
  }
  $('#d-sub').textContent = sub.join('  ·  ');

  $('#d-overview').textContent = card.art?.overview || '';
  $('#d-overview').hidden = !card.art?.overview;

  const g = $('#d-genres');
  g.replaceChildren();
  for (const name of card.art?.genres || []) g.append(el('span', 'chip', name));

  // What the rows below will actually do, said before any of them is clicked.
  // An off-site card gets its own sentence because both of the others are
  // untrue of it: nothing here is pressed, and nothing here streams.
  if (card.external) {
    $('#d-srch').textContent =
      `${card.sources.length} link${card.sources.length === 1 ? '' : 's'} on `
      + `${card.external.name} — each one opens ${card.external.host} in a new tab`;
  } else {
    // Named from the card rather than spelled here. archive.org was the only
    // instant source when this line was written, so its name was written into
    // it; the Vimm vault is a second one, and a Zelda cartridge from Vimm
    // announcing itself as "Hosted by archive.org" is a plain falsehood on the
    // one line that is supposed to say where the thing comes from.
    $('#d-srch').textContent = card.instant
      ? `Hosted by ${card.origin || 'this site'} — press play`
      : `${card.sources.length} source${card.sources.length === 1 ? '' : 's'} — pick one to stream`;
  }

  const list = $('#d-sources');
  list.replaceChildren();
  card.sources.forEach((s, i) => list.append(sourceRow(card, s, i === card.best)));

  renderSaveButton(card);
}

/**
 * The save control, drawn only for a viewer who has somewhere to save to.
 *
 * Membership is read from the library rather than remembered locally, so the
 * button tells the truth after the same title was saved on another device.
 */
async function renderSaveButton(card) {
  const btn = $('#d-save');
  btn.hidden = true;

  let saved;
  try {
    const items = await getLibrary();
    saved = items.some((it) => it.key === keyFor(card));
  } catch {
    return; // signed out, or the shelf is unreachable: draw nothing
  }
  if (state.active !== card) return; // a newer detail opened while we waited

  const paint = () => {
    btn.dataset.saved = saved ? '1' : '0';
    btn.textContent = saved ? '✓ In your library' : '+ Save to library';
  };
  paint();
  btn.hidden = false;

  btn.onclick = async () => {
    btn.disabled = true;
    const next = !saved;
    try {
      await (next ? addToLibrary(card) : removeFromLibrary(card));
      saved = next;
      paint();
    } catch {
      // Leave the button showing what the server still believes.
      paint();
    } finally {
      btn.disabled = false;
    }
  };
}

/**
 * What an off-site card is actually offering, in the site's own words.
 *
 * `downloadable` and `playable` are separate facts on the far side and the
 * server keeps them separate — one source row each — so this reads them back
 * off the rows rather than guessing from the card.
 */
function offsiteOffers(card) {
  const has = new Set((card.sources || []).map((s) => s.action).filter(Boolean));
  if (has.has('play') && has.has('download')) return 'play or download there';
  if (has.has('play')) return 'plays there';
  if (has.has('download')) return 'download';
  if (has.has('open')) return 'open its catalogue page';
  return '';
}

/**
 * One row of the source list.
 *
 * An off-site source is an <a>, not a <button>, and that is the whole point
 * rather than a styling choice. This site does not host it, cannot stream it
 * and cannot boot it in our player, so the only honest thing a click can do is
 * leave — and an anchor is the one control that both SAYS so before it is
 * pressed (the browser shows the destination on hover, middle-click and
 * ctrl-click work, a screen reader announces a link) and cannot accidentally be
 * routed into the player, because there is no handler to route.
 */
function sourceRow(card, s, isBest) {
  if (s.onSite) return isolatedSourceRow(s, isBest);
  if (s.offsite) return offsiteSourceRow(card, s, isBest);

  const row = el('button', isBest ? 'source best' : 'source');
  row.type = 'button';

  const l = el('div', 'sl');
  // Quality, codec, size and seeders are all torrent vocabulary. On a hosted
  // game every one of them renders as "unknown" or "0", which reads as a
  // broken row rather than the most reliable one on the page.
  if (card.instant) {
    l.append(el('span', 'q', 'PLAY'));
    l.append(el('span', 'tag ok', s.source || 'Game'));
  } else {
    l.append(el('span', 'q', s.quality || '—'));
    const codec = el('span', s.webSafe ? 'tag ok' : 'tag warn', s.codec || 'unknown');
    codec.title = s.webSafe
      ? 'Plays directly in your browser'
      : 'Needs hardware decode on your device — no server transcoding';
    l.append(codec);
    if (s.source) l.append(el('span', 'tag', s.source));
  }
  l.append(el('span', 'name', s.title));
  row.append(l);

  const r = el('div', 'sr');
  if (card.instant) {
    r.append(el('span', 'seeds', 'INSTANT'));
  } else {
    r.append(el('span', null, s.sizeHuman));
    r.append(el('span', s.seeders > 0 ? 'seeds' : 'seeds dead', `${s.seeders}▲`));
  }
  r.append(el('span', null, s.indexer));
  row.append(r);

  row.addEventListener('click', () => play(card, s));
  return row;
}

/**
 * A source that lives on another website.
 *
 * Every torrent word is wrong here and so is every hosted-result word. There is
 * no quality, no codec, no seeder count and no "INSTANT" — there is a verb, a
 * filename, a size and the name of the site it is on. The verb is the server's
 * `action`, which is the distinction between the two things Vimm publishes
 * about an entry: their in-browser player, and the file itself.
 */
/**
 * A row that opens this site's own isolated player.
 *
 * It looks like an offsite row because it is a navigation rather than a play,
 * and it must not look like the in-page rows or somebody will expect the game
 * to appear where they are standing. But every word that says "leaves this
 * site" is wrong: it does not. The bytes come from the same vault they always
 * did; the only thing that changed is which of OUR documents holds the
 * emulator, and it changed because that one can be cross-origin isolated and
 * this one cannot.
 *
 * Same tab, deliberately, unlike the offsite row. There is nothing to protect
 * this window from -- it is our own page -- and the back button then returns
 * somebody to their search results, which a new tab does not.
 */
function isolatedSourceRow(s, isBest) {
  const row = el('a', isBest ? 'source best offsite' : 'source offsite');
  row.href = s.magnet ?? s.uri ?? '';
  row.setAttribute('aria-label', `Play ${s.title} in the Yarr.It player`);
  row.title = row.getAttribute('aria-label');

  const l = el('div', 'sl');
  l.append(el('span', 'q', 'PLAY'));
  // Not the warning colour: this is not a departure. It is the same tag the
  // vault rows carry, because it is the same promise -- our player, our
  // on-screen controls.
  l.append(el('span', 'tag', 'our player'));
  if (s.source) l.append(el('span', 'tag', s.source));
  l.append(el('span', 'name', s.title));
  row.append(l);

  const r = el('div', 'sr');
  if (s.size) r.append(el('span', null, s.sizeHuman));
  r.append(el('span', null, 'opens the player'));
  row.append(r);
  return row;
}

function offsiteSourceRow(card, s, isBest) {
  const row = el('a', isBest ? 'source best offsite' : 'source offsite');
  row.href = s.magnet ?? s.uri ?? '';
  row.target = '_blank';
  // noopener is the one that matters: without it the opened page gets a handle
  // on this window and can navigate it. noreferrer keeps the search terms in
  // the URL from travelling with the click.
  row.rel = 'noopener noreferrer';

  const verb = offsiteVerb(s.action);
  row.setAttribute('aria-label',
    `${verb === 'PLAY THERE' ? 'Play' : (verb === 'DOWNLOAD' ? 'Download' : 'Open')} ${s.title} on ${card.external?.name || s.indexer}`
    + ' (opens in a new tab)');
  row.title = row.getAttribute('aria-label');

  const l = el('div', 'sl');
  l.append(el('span', 'q', verb));
  // Deliberately the warning colour rather than the ok one. It is not a fault,
  // it is a departure, and the row should not look like the ones that play here.
  l.append(el('span', 'tag warn', 'leaves this site'));
  if (s.source) l.append(el('span', 'tag', s.source));
  l.append(el('span', 'name', s.title));
  row.append(l);

  const r = el('div', 'sr');
  if (s.size) r.append(el('span', null, s.sizeHuman));
  r.append(el('span', null, `${s.indexer} ↗`));
  row.append(r);
  return row;
}

function closeDetail() {
  $('#detail').hidden = true;
  document.body.style.overflow = '';
}

// ------------------------------------------------------------------ player --


/**
 * Frame a third-party embed.
 *
 * TWO DIFFERENT JOBS, and conflating them is how a film ends up in a 4:3 box.
 *
 * A YouTube video or an archive.org FILM is a modern player that fills whatever
 * viewport it is given, so it gets the whole stage, as it always has.
 *
 * The Internet Archive's EMULATOR does not. Measured on live archive.org
 * 2026-08-08: post-boot its canvas is a fixed size -- 512x480 for the NES,
 * 704x446 for the 2600, 640x448 for the Genesis, 640x400 for DOS -- identical at
 * 1920x1080, 1280x720, 800x600 and 640x480, always at x = 0 with the canvas
 * vertically centred. It is their page and cross-origin, so nothing of ours
 * reaches inside it and nothing can measure it at run time.
 *
 * That rules out both of the things already tried. Clamping the iframe to a
 * guessed natural size clips the picture the moment the game boots and resizes
 * its canvas -- which looked right until somebody pressed play. Handing it the
 * whole stage does not make the picture bigger either, because their canvas
 * ignores the room: it just surrounds the same small picture with more black.
 *
 * What is left is to give it a room of the right SHAPE and a sensible size: a
 * 4:3 box, the television every one of these machines drew to, centred on a
 * clean backdrop and capped at a width comfortably above every canvas measured.
 * The picture inside it is still theirs to place. That is the honest ceiling on
 * what can be done from out here, and it is why our own player is the default.
 */
/**
 * Give a small picture the screen it is being shown on.
 *
 * Nostalgia TV is the first thing on the home page and the only thing on the
 * site that is already running when a stranger arrives. Its files are 320x240
 * to 496x368 -- old television, at the size it was broadcast -- and the only
 * rule that ever applied to them was `max-width:100%; max-height:100%`, which
 * caps a picture and never lifts one. So the headline feature played at 320x240
 * in the middle of a 1920x1080 screen, using about 3% of the frame, and so did
 * every other archive.org video on the site.
 *
 * The arithmetic and the reasoning are in src/videofit.js. This is the seam:
 * called for every <video>, a no-op for everything else, and stopped on the way
 * out so the next source does not inherit the last one's inline width.
 */
function fitVideoToStage(playable, el) {
  state.stopVideoFit?.();
  state.stopVideoFit = null;
  if (!playable || playable.render !== 'video' || !el) return;
  const stage = document.querySelector('#play-body .stage') || document.querySelector('.stage');
  if (!stage) return;
  state.stopVideoFit = keepVideoFitted(el, stage, { mode: prefs.playback().upscale });
}

/** Re-fit what is playing right now, for the moment the preference changes. */
function refitVideo() {
  const el = $('#video');
  if (!el || el.hidden) return;
  fitVideoToStage({ render: 'video' }, el);
}

function fitEmbedToStage(playable, el) {
  state.stopFit?.();
  state.stopFit = null;

  const outer = document.querySelector('#embed-wrap');
  const wrap = document.querySelector('#embed-fit');
  const note = document.querySelector('#embed-note');
  if (!wrap || !outer) return;
  if (playable.render !== 'embed') {
    outer.hidden = true;
    return;
  }
  outer.hidden = false;
  wrap.style.overflow = 'hidden';
  el.style.width = '100%';
  el.style.height = '100%';
  el.style.transform = '';
  el.style.border = '0';

  // Only an emulated item gets the emulator treatment.
  const emulated = state.game?.route === ROUTE.ARCHIVE;
  if (note) note.hidden = !emulated;
  if (!emulated) {
    wrap.style.width = '100%';
    wrap.style.height = '100%';
    wrap.style.margin = '';
    return;
  }

  // The caption under the frame is part of the wrapper, so its height is not
  // available to the picture. Left unsubtracted, `max-height:100%` clipped the
  // box back and the result was a 4:3 frame that measured 1.40:1.
  const reserveHeight = note ? note.offsetHeight + 10 : 0;
  state.stopFit = keepShaped(wrap, document.querySelector('#play-body .stage'), { reserveHeight });
}

// ------------------------------------------------------------ archive games --

/**
 * Firmware the viewer supplied, held in their own browser.
 *
 * Created lazily and once: opening IndexedDB costs nothing until something is in
 * it, and the list of machines it will accept comes from the server rather than
 * from a table here -- see bios.js for why that matters.
 */
let biosStore = null;
async function getBiosStore() {
  if (biosStore) return biosStore;
  let allowed = [];
  try {
    const res = await apiFetch('/api/play/systems');
    if (res.ok) allowed = ((await res.json()).bios || []).map((b) => b.system);
  } catch {
    // No list means no machine may be stored, which is the safe direction: a
    // BIOS filed under a name the server does not use would never be found.
  }
  biosStore = createBiosStore({ allowed });
  return biosStore;
}

/**
 * Whether a source is an archive.org item that the play service should judge.
 *
 * A DIRECT FILE link is excluded on purpose. `/download/<id>/movie.mp4` is a
 * video, and a <video> element gives real seeking and fullscreen that an iframe
 * does not; sending it through the emulation verdict would cost a metadata
 * request to be told it is not a game. Everything else -- including the plain
 * details URL that a film or a comic also arrives as -- goes through, because
 * `not_emulated` is exactly the answer that hands it back to the registry.
 */
function archiveItemFor(uri) {
  const id = identifierFrom(uri);
  if (!id) return null;
  return fileFrom(uri) ? null : id;
}

/**
 * Start an archive.org item in whichever player the viewer is owed.
 *
 * THIS IS THE CHANGE. Every emulated archive.org item used to be an iframe of
 * somebody else's page: a 300x150 canvas that does not scale, no on-screen
 * controls, and no way to say what the keys are. Our own player was reachable
 * only from a second row in the source list that most people never saw.
 *
 * Now the server decides -- one metadata request, the same one the old path
 * spent anyway -- and our player is the default everywhere it can run. Where it
 * cannot, the Archive's player is offered on purpose rather than by accident,
 * with the reason attached and the frame given the whole stage.
 *
 * Returns false when the item is not an emulated one at all, which hands it back
 * to the resolver registry unchanged: an archive.org film is still a film.
 */
async function playArchiveItem(id, card, gen) {
  let declared = [];
  try {
    declared = await (await getBiosStore()).declared();
  } catch {
    /* no firmware stored, or no storage at all: the conservative answer */
  }

  const verdict = await fetchVerdict(id, { bios: declared });
  if (gen !== state.resolveGen) return true;

  // Not a game. Hand it back rather than showing an emulator's refusal for
  // something that was never going to be one.
  if (!canPlay(verdict) && verdict.reasons?.[0]?.code === 'not_emulated') return false;

  if (!canPlay(verdict)) {
    setPlayerStatus(verdict.reasons?.[0]?.detail || 'This cannot be played here.');
    renderGamePanels(verdict, ROUTE.NONE);
    return true;
  }

  await startInPlayer(verdict, choosePlayer(verdict), card);
  return true;
}

/**
 * Boot one verdict in one named player, and draw everything around it.
 *
 * Split out from playArchiveItem because the switch calls it too: flipping
 * players must restart the same item from the same verdict rather than
 * re-resolving it, or every flip costs a metadata request and the two halves of
 * the switch could disagree about what they are switching between.
 */
async function startInPlayer(verdict, route, card) {
  try {
    state.playable?.cleanup();
  } catch (err) {
    console.warn('[player] cleanup of outgoing playable failed:', err?.message || err);
  }
  state.playable = null;
  setPlayerDownload(route === ROUTE.EMULATORJS ? verdict?.rom?.direct : '');
  releaseBios();

  const els = playerElements();
  detachAll(els);
  setPlayerStatus('');

  // Firmware, when this machine needs any, and THEIR file wins.
  //
  // A blob: URL from this browser's own storage is tried first and is used if
  // it is there: somebody who went and found a specific Kickstart revision has
  // said something, and quietly running the library's copy instead would be us
  // overruling them about their own machine. Only when there is nothing here
  // does the library's copy get used, and that arrives as a path on the verdict
  // -- resolved against the configured server, because a self-hoster's client
  // may be served from somewhere other than the server it is pointed at.
  let biosUrl = null;
  let biosBlob = false;
  if (route === ROUTE.EMULATORJS && verdict.biosNeeded?.system) {
    try {
      biosUrl = await (await getBiosStore()).objectURL(verdict.biosNeeded.system);
      biosBlob = Boolean(biosUrl);
    } catch {
      /* storage that vanished mid-session. Fall through to the library, and if
         there is none the emulator draws its own missing-BIOS screen, which is
         the truth. */
    }
    if (!biosUrl && verdict.biosNeeded.url) biosUrl = api(verdict.biosNeeded.url);
  }

  let out;
  try {
    out = toPlayable(verdict, { route, biosUrl });
  } catch (err) {
    setPlayerStatus(err.message);
    renderGamePanels(verdict, route);
    return;
  }

  // `biosBlob` is remembered rather than re-derived: only a blob: URL has bytes
  // held alive behind it, and revoking anything else is a no-op that reads as if
  // it were doing something.
  state.game = { verdict, route, card, biosUrl, biosBlob };
  const el = renderPlayable(out, els);
  state.playable = out;
  fitEmbedToStage(out, el);
  fitVideoToStage(out, el);
  // The swarm HUD means nothing over an iframe, an image or an emulator, and
  // permanent zeros read as a stalled stream.
  { const st = document.querySelector('.stats');
    if (st) st.hidden = !(out.render === 'video' || out.render === 'audio'); }
  renderGamePanels(verdict, route);
}

/**
 * Revoke the blob URL a BIOS was handed over as.
 *
 * Only a blob: URL: the library's firmware is an ordinary path on our own
 * server, and handing that to revokeObjectURL does nothing at all -- which is
 * harmless but reads like cleanup that is happening when it is not.
 */
function releaseBios() {
  if (state.game?.biosBlob && state.game?.biosUrl) {
    try {
      URL.revokeObjectURL(state.game.biosUrl);
    } catch {
      /* already gone */
    }
  }
}

/**
 * The switch and the instructions.
 *
 * Both are drawn from the same verdict so they can never disagree about which
 * player is running, and both are cleared for anything that is not an
 * archive.org game -- a film has no player switch to offer.
 */
function renderGamePanels(verdict, route) {
  renderPlayerSwitch(verdict, route);
  // Peers, relay peers, speed and buffered are torrent vocabulary. On a game
  // served by archive.org every one of them is 0 for the whole session, and
  // four zeroes under a running game is the most reliable-looking part of the
  // page reporting itself broken.
  showSwarmStats(false);

  const host = $('#guide');
  if (!host) return;
  host.replaceChildren();
  const panel = hasGuide(verdict?.guide)
    ? renderGuide(verdict.guide, { route })
    : null;
  if (panel) host.append(panel);
  host.hidden = !panel;
}

function clearGamePanels() {
  const bar = $('#player-switch');
  if (bar) { bar.replaceChildren(); bar.hidden = true; }
  const host = $('#guide');
  if (host) { host.replaceChildren(); host.hidden = true; }
  showSwarmStats(true);
}

function setPlayerDownload(raw) {
  const link = $('#player-download');
  if (!link) return;
  const href = playerDownloadURL(raw);
  link.hidden = !href;
  if (href) link.href = href;
  else link.removeAttribute('href');
}

/** The peers/speed/buffered row, which only means anything for a torrent. */
function showSwarmStats(on) {
  for (const sel of ['.progress-track', '.stats']) {
    const node = document.querySelector(sel);
    if (node) node.hidden = !on;
  }
}

/**
 * Two buttons, both always drawn.
 *
 * A switch that hides its other half leaves a viewer wondering whether the
 * option exists at all; a switch that offers a dead option is the dead button
 * this whole path exists to remove. Both drawn, one disabled, and the disabled
 * one carries the server's own sentence explaining itself -- that is the only
 * version that is neither.
 */
function renderPlayerSwitch(verdict, route) {
  const bar = $('#player-switch');
  if (!bar) return;
  bar.replaceChildren();

  if (!verdict || route === ROUTE.NONE) {
    // Even here the reason is worth showing: this is the case where somebody
    // pressed Play and got nothing, and silence is what makes that feel broken.
    const why = verdict?.reasons?.[0]?.detail;
    if (!why) { bar.hidden = true; return; }
    bar.hidden = false;
    bar.append(el('p', 'switch-note', why));
    return;
  }

  bar.hidden = false;
  bar.append(el('span', 'switch-label', 'Player'));

  const group = el('div', 'switch-group');
  group.setAttribute('role', 'radiogroup');
  group.setAttribute('aria-label', 'Which player to use');

  for (const option of playerOptions(verdict)) {
    const on = option.route === route;
    const btn = el('button', on ? 'switch-btn on' : 'switch-btn');
    btn.type = 'button';
    btn.setAttribute('role', 'radio');
    btn.setAttribute('aria-checked', on ? 'true' : 'false');
    btn.append(el('span', 'switch-name', option.label));
    btn.append(el('span', 'switch-sub', option.available ? option.note : option.why));
    if (!option.available) {
      btn.disabled = true;
      btn.title = option.why;
    } else if (!on) {
      btn.addEventListener('click', () => {
        // Remembered before the restart, so a viewer who switches and then
        // closes the tab still gets their choice next time.
        writePlayerPreference(option.route);
        startInPlayer(verdict, option.route, state.game?.card);
      });
    }
    group.append(btn);
  }
  bar.append(group);

  // When there is no choice, say which player this is and why in one sentence
  // rather than leaving a greyed-out button to be interpreted.
  const sole = solePlayerSentence(verdict);
  if (sole && !canSwitchPlayer(verdict)) bar.append(el('p', 'switch-note', sole));

  // Firmware would turn the greyed-out half into a real option. Offering it
  // here, next to the thing it unlocks, is the only place a person is actually
  // wondering about it.
  if (verdict.biosNeeded && route !== ROUTE.EMULATORJS) bar.append(biosOffer(verdict));
  if (verdict.biosNeeded && route === ROUTE.EMULATORJS) bar.append(biosInUse(verdict));

  // Isolation is the other refusal a visitor can lift, and it is the cheaper of
  // the two: no file to find, just a different page on this same site. Offered
  // in the same place and for the same reason as the firmware offer above.
  const isolated = isolatedHref(verdict);
  if (isolated) bar.append(isolationOffer(isolated));
}

/**
 * "This one plays in our player over here."
 *
 * MS-DOS and PSP run on threaded cores, threads need a cross-origin-isolated
 * document, and this app cannot be one without giving up the Internet Archive's
 * iframe player. `/play/` is a document that embeds nothing and therefore can
 * be. See isolatedHref and stream/Caddyfile.
 *
 * A plain link, not a button that navigates: it is a different DOCUMENT, and it
 * has to be -- the whole point is the headers it is served with. A link is also
 * the thing a person can middle-click, which is what somebody who does not want
 * to lose their search results will do.
 */
function isolationOffer(href) {
  const box = el('div', 'bios-offer');
  box.append(el('p', 'bios-why',
    'This machine needs a threaded emulator, which only runs on an isolated '
    + 'page. We have one.'));
  const link = el('a', 'switch-btn');
  link.href = href;
  link.append(el('span', 'switch-name', 'Open in the Yarr.It player'));
  link.append(el('span', 'switch-sub', 'On-screen controls, save states'));
  box.append(link);
  return box;
}

/**
 * "Running with the BIOS you supplied."
 *
 * This exists because of a failure that was measured rather than imagined: a
 * file of the right SIZE but the wrong contents is accepted by us and rejected
 * by the core, which draws NO BIOS across the screen at a healthy frame rate and
 * reports itself started. Nothing downstream can tell that from a working game,
 * so the only place it can be explained is here, before it happens -- otherwise
 * a person who did everything right is looking at two words and no way back.
 */
/**
 * One sentence about the firmware a game is running on.
 *
 * The wording differs per source on purpose. "Running with a BIOS" is not the
 * useful part -- WHERE it came from is, because that is the only thing that
 * tells somebody what to change when the game misbehaves, and because a machine
 * that silently started working is a machine that will silently stop.
 */
function biosSourceSentence(need) {
  const wrong = 'If the game shows NO BIOS or the emulator\'s own error screen, '
    + 'that firmware is not what it should be.';
  switch (need.source) {
    case BIOS_SOURCE.YOURS:
      return `Running with the ${need.label} you supplied, held in this browser only. `
        + `${wrong} Replace it below.`;
    case BIOS_SOURCE.LIBRARY:
      return `Running with ${need.file || need.label} from your own library — nothing `
        + 'to add. It is sent from your own server rather than from the library '
        + `directly, so it works from anywhere you can reach this page. ${wrong}`;
    case BIOS_SOURCE.ARCHIVE:
      return `Running with the ${need.label} the Internet Archive's own player uses, `
        + 'relayed through your server — nothing to add, and nothing kept here. '
        + `${wrong}`;
    case BIOS_SOURCE.BUILTIN:
      return need.detail
        || `Running on the emulator's own free ${need.label}. Nothing to supply.`;
    default:
      return `Running with the ${need.label}.`;
  }
}

function biosInUse(verdict) {
  const need = verdict.biosNeeded;
  const ownFile = need.source === BIOS_SOURCE.YOURS;
  const box = el('div', 'bios-offer');

  // WHOSE firmware is running is said out loud, and it is not a detail.
  //
  // Somebody who supplied a file deserves to know it is the one being used.
  // Somebody who supplied nothing and got a game anyway deserves to know why --
  // because otherwise a machine that refused them last month works today for no
  // reason they can see, and they will have no idea where to look when it
  // stops. Four sources, four sentences, and none of them says "it just works".
  box.append(el('p', 'bios-why', biosSourceSentence(need)));

  const label = el('label', 'bios-pick');
  label.append(el('span', null, ownFile ? 'Replace the file' : 'Use your own file instead'));
  const input = el('input');
  input.type = 'file';
  input.accept = '.rom,.bin,.A500,.A1200,application/octet-stream';
  label.append(input);

  const status = el('p', 'bios-status');
  status.hidden = true;

  input.addEventListener('change', async () => {
    const file = input.files?.[0];
    if (!file) return;
    try {
      const store = await getBiosStore();
      await store.put(need.system, { name: file.name, bytes: await file.arrayBuffer() });
      // Re-asked rather than restarted with the old verdict: the answer now has
      // a different SOURCE, and this panel is the thing that reports it. Reusing
      // the old one would run the new file while still saying the library's is
      // the one in use, which is the exact confusion this panel exists to end.
      const fresh = await fetchVerdict(verdict.id, { bios: await store.declared() });
      await startInPlayer(fresh, ROUTE.EMULATORJS, state.game?.card);
    } catch (err) {
      status.textContent = err?.message || 'That file could not be stored.';
      status.className = 'bios-status bad';
      status.hidden = false;
    }
  });

  box.append(label);

  // Only when there IS one of theirs to forget. Offering it over the library's
  // copy would be a button that either does nothing or reads as "stop using my
  // library", which is not what it does.
  if (ownFile) {
    const forget = el('button', 'bios-forget', 'Forget this BIOS');
    forget.type = 'button';
    forget.addEventListener('click', async () => {
      try {
        await (await getBiosStore()).remove(need.system);
      } catch {
        /* nothing stored: the button has already done its job */
      }
      const fresh = await fetchVerdict(verdict.id, { bios: await (await getBiosStore()).declared() });
      await startInPlayer(fresh, choosePlayer(fresh), state.game?.card);
    });
    box.append(forget);
  }
  box.append(status);
  return box;
}

/**
 * "Add your ColecoVision BIOS."
 *
 * The file is read in the browser and stored in the browser. It is never
 * uploaded -- there is no request in this path that leaves the machine -- which
 * is both the correct place for it and the only place it can legitimately live:
 * the firmware is not this project's to distribute and IS the console owner's
 * to use.
 */
function biosOffer(verdict) {
  const need = verdict.biosNeeded;
  const box = el('div', 'bios-offer');
  box.append(el('p', 'bios-why', `${need.label} needed. ${need.detail}`));
  box.append(el('p', 'bios-files', `Look for: ${need.files.join(', ')}`));

  const label = el('label', 'bios-pick');
  label.append(el('span', null, 'Choose your BIOS file'));
  const input = el('input');
  input.type = 'file';
  input.accept = '.rom,.bin,.A500,.A1200,application/octet-stream';
  label.append(input);

  const status = el('p', 'bios-status');
  status.hidden = true;
  const say = (text, bad = false) => {
    status.textContent = text;
    status.className = bad ? 'bios-status bad' : 'bios-status';
    status.hidden = !text;
  };

  input.addEventListener('change', async () => {
    const file = input.files?.[0];
    if (!file) return;
    say('Reading…');
    try {
      const store = await getBiosStore();
      await store.put(need.system, { name: file.name, bytes: await file.arrayBuffer() });
      say('Stored in this browser. Re-checking…');
      // Re-ask rather than assume: the item still has to pass every other check,
      // and a BIOS does not make a 460 MB disc image smaller.
      const fresh = await fetchVerdict(verdict.id, { bios: await store.declared() });
      if (fresh.route === ROUTE.EMULATORJS) {
        writePlayerPreference(ROUTE.EMULATORJS);
        await startInPlayer(fresh, ROUTE.EMULATORJS, state.game?.card);
        return;
      }
      say(fresh.reasons?.[0]?.detail || 'Stored, but this item still will not play here.', true);
    } catch (err) {
      say(err?.message || 'That file could not be stored.', true);
    }
  });

  box.append(label);
  box.append(status);
  return box;
}

function playerElements() {
  return {
    video: $('#video'),
    audio: $('#audio'),
    image: $('#image'),
    embed: $('#embed'),
    canvas: $('#canvas'), // Ruffle (.swf) and EmulatorJS (ROMs) mount here
  };
}

function buildRegistry() {
  if (!state.engine) state.engine = new StreamEngine({ onStats: renderStats });
  window.__engine = state.engine; // diagnostics
  const registry = createRegistry()
    .register(linearResolver)     // yarrit-linear:<channel>, claimed by nothing else
    .register(createTorrentResolver({ engine: state.engine, classify }))
    // Claims only the signed /api/link/media URLs the paste-a-link resolver
    // mints, so a chosen format plays through this same path instead of a
    // parallel one. Before url, which would otherwise HEAD the media URL to
    // sniff a type we already know -- and for a format the server has to
    // repack, that HEAD would start an ffmpeg run for nothing.
    .register(createLinkResolver())
    .register(embedResolver)      // before url: a YouTube link is also an http URL
    .register(webmulatorResolver) // official hosted EmulatorJS page, never its hidden ROM URL
    .register(archiveResolver)    // before url/game: archive.org runs its own player
    .register(flashResolver)      // before url: a .swf is also an http URL
    .register(gameResolver)       // before url: a .nes/.smc is also an http URL
    .register(playlistResolver)   // before url: .m3u8 is also an http URL
    .register(urlResolver);
  window.__registry = registry; // diagnostics
  return registry;
}

/**
 * Teach the tiles which games this client can actually launch.
 *
 * home.js ships a default that recognises archive.org and nothing else, and
 * says why: the button should appear the moment a backend exists that can
 * honour it, and not one commit before. The Vimm vault is that backend. It
 * serves ROMs over HTTP from an origin that answers CORS and declares the core
 * on the URI, so a vault card is exactly as launchable as an archive.org one
 * and there is no longer any reason for the tile to say "Open" over a game
 * that plays.
 *
 * Asked of the resolvers themselves rather than of the registry, because the
 * registry is built on the first play -- it needs the torrent engine -- and a
 * label is needed before that, on the first paint. These three are plain
 * objects with no such dependency.
 *
 * Only the resolvers that boot something IN THE PAGE are consulted. The
 * torrent resolver claims a magnet, but a ROM set to download is not a thing
 * to press play on, and refusing to say so is the promise this keeps.
 */
const PLAYS_IN_PAGE = [webmulatorResolver, archiveResolver, gameResolver, flashResolver];
setPlayProbe((item) => {
  const uri = item?.uri ?? '';
  return PLAYS_IN_PAGE.some((r) => {
    try {
      return r.canHandle(uri);
    } catch {
      return false;
    }
  });
});

/**
 * Best-effort filename for a resolved Playable, so needsWebCodecs can look at
 * the actual container extension instead of a magnet link.
 *
 * WebTorrent's file.streamURL (what the torrent resolver sets as `src`) is
 * `<sw-scope>/<infoHash>/<encoded file path>` -- the final path segment is
 * the real filename, which is what this recovers. The url/playlist resolvers
 * set `src` to the source URI itself, which is usually the same shape.
 *
 * This is not reliable in every case, and there is nothing to fall back to
 * that fixes that: source.js's makePlayable (render/src/mime/tier/cleanup)
 * does not carry the original filename as its own field, so when `src` is
 * not a filename-shaped URL -- e.g. the no-service-worker torrent fallback in
 * torrent.js's resolve(), which plays from a bare `blob:` URL -- there is no
 * filename to recover at all. In that case this just returns `src` itself;
 * needsWebCodecs will find no matching extension and stay silent rather than
 * guess. That's a known gap, not a bug: it only affects the rare
 * service-worker-unavailable fallback path, not normal torrent playback.
 */
function playableFilename(playable) {
  try {
    const { pathname } = new URL(playable.src, location.href);
    const last = pathname.split('/').pop();
    if (last) return decodeURIComponent(last);
  } catch {
    /* src isn't a parseable URL (e.g. an opaque blob: id) -- fall through */
  }
  return playable.src;
}

/**
 * Resolve any source and render whatever comes back. `src.magnet` is still
 * honoured so existing search results keep working unchanged.
 *
 * Two guards protect this against the async gap between "resolve() called"
 * and "resolve() settles":
 *
 *  - The outgoing Playable's cleanup() is called up front, before anything
 *    else, so a torrent's swarm (or any other resolver's held resource) is
 *    always released the instant a new source is picked -- not just when
 *    the player is closed, and not left to whatever side effect the next
 *    resolver happens to have (StreamEngine.add() tearing down the previous
 *    torrent is one such side effect, not a substitute for this).
 *  - `resolveGen` guards overlapping calls: if the user picks a second
 *    source before the first has finished resolving, only the resolution
 *    matching the *latest* play() call is allowed to touch the DOM or
 *    state.playable. A resolution that loses the race still gets its
 *    Playable cleaned up so it can't leak in the background.
 */
async function play(card, src) {
  const gen = ++state.resolveGen;

  try {
    state.playable?.cleanup();
  } catch (err) {
    console.warn('[player] cleanup of outgoing playable failed:', err?.message || err);
  }
  state.playable = null;

  $('#library').hidden = true;
  $('#player').hidden = false;
  // The player is a full-viewport overlay, so the page behind it must stop
  // scrolling -- otherwise a full-screen game sits beside a live scrollbar for
  // a search results page nobody can see. Restored on close the same way the
  // reader does it, only when there is no detail sheet still open underneath.
  document.body.style.overflow = 'hidden';
  $('#player-title').textContent = card.title + (card.year ? ` (${card.year})` : '');
  $('#player-sub').textContent = src.title ?? '';
  setPlayerDownload((card.sources || []).find((source) => source.action === 'download')?.magnet || '');
  setPlayerStatus('Resolving…');

  const els = playerElements();
  detachAll(els);
  clearGamePanels();
  releaseBios();
  state.game = null;

  if (!state.registry) state.registry = buildRegistry();

  const uri = src.magnet ?? src.uri;
  try {
    // An archive.org item is asked about before it is resolved. `#swf` is the
    // one exception: Flash is Ruffle's, the play service knows nothing about it,
    // and routing it here would lose a working player to gain a verdict about a
    // machine that is not involved.
    const item = /#swf$/.test(uri) ? null : archiveItemFor(uri);
    if (item) {
      const handled = await playArchiveItem(item, card, gen);
      if (gen !== state.resolveGen) return;
      // `false` means the Archive says this is not an emulated item at all --
      // a film, a comic, a photo set. It falls through to the registry, which
      // plays it exactly as it did before.
      if (handled) return;
    }

    // meta carries the already-resolved format through to the link resolver,
    // which is how a chosen quality plays without a second round trip to
    // re-learn what it is.
    const out = await state.registry.resolve(
      makeSource({ kind: 'auto', uri, meta: src.meta ?? {} }),
    );

    if (gen !== state.resolveGen) {
      // A newer play() call has since taken over. Never touch the DOM or
      // state.playable with a stale result -- but still release whatever
      // this resolution acquired (a torrent's cleanup destroys its swarm)
      // so the loser of the race doesn't leak.
      if (!isCollection(out)) {
        try {
          out.cleanup?.();
        } catch (err) {
          console.warn('[player] cleanup of abandoned playable failed:', err?.message || err);
        }
      }
      return;
    }

    if (isCollection(out)) {
      $('#player').hidden = true;
      renderLibrary(out, {
        mount: $('#library'),
        onPick: (picked) => play({ title: picked.meta.title || picked.uri }, { uri: picked.uri }),
      });
      return;
    }

    $('#library').hidden = true;
    if (needsWebCodecs(playableFilename(out))) {
      setPlayerStatus('Unusual container — if this stalls, pick an MP4 source.');
    }
    const el = renderPlayable(out, els);
    state.playable = out;
    fitEmbedToStage(out, el);
    fitVideoToStage(out, el);

    // Remember where this viewer gets to, so the same title resumes on any
    // other device. Only real media has a position; an image or an emulator
    // has nothing to record.
    //
    // A live channel is excluded, and that is not an optimisation. There is no
    // resume point on a channel -- that is what makes it a channel -- so a
    // position saved here would put "Nostalgia Classic TV, 18 minutes in" in
    // Continue Watching, offering to return somebody to a moment that no
    // longer exists.
    state.stopTracking?.();
    state.stopTracking = (!card.live && (out.render === 'video' || out.render === 'audio'))
      ? trackProgress(el, card)
      : null;

    // Subtitles that shipped with the release. Attached after playback has
    // started so fetching them never delays the picture, and failure is
    // silent -- no subtitles is the normal case, not an error.
    if (out.subtitles?.length) {
      attachSubtitles(el, out.subtitles, (t) => t.load())
        .then((tracks) => {
          if (!tracks.length) return;
          const shown = showPreferredSubtitle(el, tracks);
          setPlayerStatus(shown
            ? `Subtitles on: ${shown}`
            : `${tracks.length} subtitle track${tracks.length === 1 ? '' : 's'} available — use the player's captions menu`);
          setTimeout(() => setPlayerStatus(''), 6000);
        })
        .catch(() => {});
    }
    const isMedia = out.render === 'video' || out.render === 'audio';
    if (isMedia) {
      el.addEventListener('playing', () => setPlayerStatus(''), { once: true });
      if (prefs.playback().autoplay) {
        // Setting src is not a request to start an <audio> element in every
        // Chromium build. Ask explicitly while this is still the user's track
        // click; if policy refuses, leave working controls and say what to do.
        try {
          const starting = el.play();
          starting?.catch(() => {
            if (gen === state.resolveGen) setPlayerStatus('Ready — press play');
          });
        } catch {
          setPlayerStatus('Ready — press play');
        }
      } else {
        setPlayerStatus('Ready — press play');
      }
    }
    // Only <video>/<audio> fire a 'playing' event. An image, an iframe embed and
    // a canvas player (Ruffle/EmulatorJS) never will, so their status has to be
    // cleared here or the overlay sits on "Resolving…" forever.
    if (!isMedia) setPlayerStatus('');
  } catch (err) {
    if (gen !== state.resolveGen) return;
    const why = err instanceof PlaybackError ? err.message : `Could not start: ${err.message}`;
    setPlayerStatus(why);
  }
}

/**
 * Turn a subtitle track on, if that is what this viewer asked for.
 *
 * attachSubtitles deliberately shows nothing by default -- a track that
 * switches itself on is an annoyance for the majority who did not ask for one.
 * This is the minority who did, and they said so in Settings; the language they
 * chose there decides WHICH track, falling back to the first one attached
 * rather than to nothing, because "on" with the wrong language still beats
 * silence for somebody who turned it on deliberately.
 *
 * Returns the label of whatever was shown, or '' for nothing.
 */
function showPreferredSubtitle(video, tracks) {
  if (prefs.playback().subtitles !== 'on' || !tracks?.length) return '';
  const want = state.filters.lang;
  const pick = (want && want !== 'any'
    && tracks.find((t) => (t.srclang || '').toLowerCase() === want.slice(0, 2)))
    || tracks[0];
  if (!pick) return '';
  // The <track> element's `mode`, not the `default` attribute: default only
  // does anything before the element is attached, and by here it is attached.
  const list = video.textTracks || [];
  for (const t of list) t.mode = 'disabled';
  const i = tracks.indexOf(pick);
  if (list[i]) list[i].mode = 'showing';
  return pick.label || 'subtitles';
}

function setPlayerStatus(t) {
  const n = $('#player-status');
  n.textContent = t;
  n.hidden = !t;
}

function fmtBytes(b) {
  if (!b) return '0 B';
  const u = ['B', 'KiB', 'MiB', 'GiB'];
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1);
  return `${(b / 1024 ** i).toFixed(1)} ${u[i]}`;
}

function renderStats(s) {
  $('#stat-peers').textContent = s.peers;
  $('#stat-bridge').textContent = s.bridgePeers;
  $('#stat-speed').textContent = `${fmtBytes(s.downloadSpeed)}/s`;
  $('#stat-done').textContent = `${(s.progress * 100).toFixed(1)}%`;
  $('#progress').style.width = `${Math.min(100, s.progress * 100)}%`;
}

function closePlayer() {
  state.playable?.cleanup();
  state.playable = null;
  // A blob URL keeps its bytes alive until it is revoked, so a session that
  // played ten ColecoVision games would otherwise be holding ten copies of the
  // same BIOS.
  releaseBios();
  state.game = null;
  clearGamePanels();
  setPlayerDownload('');
  // Disconnects the ResizeObserver AND clears the inline width, so the next
  // source -- which may be a 4K film needing no help at all -- does not open
  // wearing the last one's 960px.
  state.stopVideoFit?.();
  state.stopVideoFit = null;
  $('#player').hidden = true;
  $('#library').hidden = true;
  if ($('#detail').hidden) document.body.style.overflow = '';
  // detachAll pauses/loads every real media element (video, audio) and clears
  // src on all of them, not just video+image -- with the audio and embed
  // elements now in play, leaving those untouched would let a paused-looking
  // player keep an <audio> element playing invisibly in the background.
  // Flush the final position before the element is torn down: after
  // detachAll, currentTime is gone, and that last position is the one
  // somebody actually wants back.
  state.stopTracking?.();
  state.stopTracking = null;

  state.stopFit?.();

  state.stopFit = null;

  { const w = document.querySelector('#embed-wrap'); if (w) w.hidden = true; }

  detachAll(playerElements());
  state.engine?.destroyTorrent();
}

// ------------------------------------------------------------------ magnet --

const PASTE_HINT =
  'That is not a link this can read. Paste a post or video link from Facebook, ' +
  'Instagram, TikTok, YouTube, X, Reddit, Vimeo, Dailymotion or Twitch — or a ' +
  'magnet link, an info hash, a direct media URL, or an .m3u/.m3u8 playlist. ' +
  'On a phone: tap Share, then "Copy link".';

/**
 * Best-effort human title for a non-magnet paste: the URL's last path
 * segment (e.g. "movie.mp4" or a playlist name), falling back to its
 * hostname when the path is empty (e.g. a bare "https://example.com/").
 */
function titleFromUri(uri) {
  try {
    const u = new URL(uri);
    const last = u.pathname.split('/').filter(Boolean).pop();
    if (!last) return u.hostname;
    try {
      return decodeURIComponent(last);
    } catch {
      return last;
    }
  } catch {
    return uri;
  }
}

/**
 * Pull the playable links out of a web page.
 *
 * A browser cannot do this itself. Reading cross-origin HTML needs an
 * Access-Control-Allow-Origin header and no torrent site sends one, so pasting
 * a description page produced only a CORS error in the console -- which reads
 * like a bug here rather than a rule of the platform. The relay fetches the
 * page instead and returns just the links.
 */
async function linksFromPage(pageUrl) {
  const response = await fetch(`/bridge/page?u=${encodeURIComponent(pageUrl)}`);
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || 'could not read that page');
  return body;
}

/**
 * Show what a page had on it. One link plays straight away -- a description
 * page with a single magnet is unambiguous and making somebody click twice for
 * it is just friction.
 */
function offerPageLinks(page) {
  const links = page.links ?? [];
  if (links.length === 1) {
    const only = links[0];
    play({ title: only.name || page.title || titleFromUri(only.url), year: 0 },
      { uri: only.url, title: only.url.slice(0, 90) });
    return;
  }

  $('#status').hidden = true;
  $('#player').hidden = true;
  renderLibrary(
    makeCollection({
      title: page.title || titleFromUri(page.source),
      sources: links.map((l) => makeSource({
        kind: 'auto',
        uri: l.url,
        meta: { title: l.name || l.url.slice(0, 80) },
      })),
    }),
    {
      mount: $('#library'),
      onPick: (picked) => play({ title: picked.meta.title || picked.uri }, { uri: picked.uri }),
    },
  );
  $('#library').hidden = false;
}

/**
 * Show what pressing Search will do, once a category is ticked but nothing has
 * been searched for yet.
 *
 * Without this a ticked chip appears to do nothing at all, which reads as
 * broken rather than as staged.
 */
function armSearch() {
  const picked = [...state.filters.groups].map((g) => GROUP_LABELS[g] || g);
  const btn = $('#search-form button[type=submit]');
  if (picked.length) {
    btn.textContent = 'Browse';
    showStatus(`Press Browse to see everything in ${picked.join(' + ')}`
      + ', or type a title to search within it.');
  } else {
    btn.textContent = 'Search';
    $('#status').hidden = true;
    restoreLanding();
  }
}

function showStatus(text) {
  const s = $('#status');
  s.replaceChildren(document.createTextNode(text));
  s.hidden = false;
}

// --------------------------------------------------------------- type-ahead --

/**
 * Suggestions while you type.
 *
 * The server side of this asks only memory and archive.org -- never a torrent
 * indexer -- because nothing that asks an indexer can keep up with a keyboard.
 * It also has a second job: every keystroke pulls archive.org's answer for that
 * prefix into the server's cache, so the search that follows a moment later
 * reads it out of memory instead of paying for it.
 */
const suggester = createSuggester({
  fetchJson: async (url, opts) => {
    const res = await apiFetch(url, opts);
    if (!res.ok) throw new Error(`suggest: ${res.status}`);
    return res.json();
  },
  onResult: ({ q, suggestions }) => {
    // The box moved on while this was in flight. Drawing it would put
    // suggestions for an older prefix under the cursor.
    if (q !== $('#q').value.trim()) return;
    renderSuggestions(suggestions);
  },
});

function renderSuggestions(list) {
  const host = $('#suggest');
  state.suggestions = list || [];
  state.suggestIndex = -1;
  host.replaceChildren();

  if (!state.suggestions.length) {
    closeSuggestions();
    return;
  }
  for (const [i, s] of state.suggestions.entries()) {
    const li = el('li', 'sg');
    li.setAttribute('role', 'option');
    li.id = `sg-${i}`;
    li.append(el('span', 'sg-title', s.title));
    const hint = suggestionHint(s);
    if (hint) li.append(el('span', 'sg-hint', hint));
    // mousedown, not click: the input's blur fires first and would close the
    // list out from under the pointer before a click could land.
    li.addEventListener('mousedown', (e) => {
      e.preventDefault();
      chooseSuggestion(i);
    });
    li.addEventListener('mouseenter', () => highlightSuggestion(i));
    host.append(li);
  }
  host.hidden = false;
  $('#q').setAttribute('aria-expanded', 'true');
}

function highlightSuggestion(i) {
  state.suggestIndex = i;
  const host = $('#suggest');
  for (const [n, li] of [...host.children].entries()) {
    li.classList.toggle('on', n === i);
  }
  $('#q').setAttribute('aria-activedescendant', i >= 0 ? `sg-${i}` : '');
}

function closeSuggestions() {
  const host = $('#suggest');
  if (!host) return;
  host.hidden = true;
  host.replaceChildren();
  state.suggestions = [];
  state.suggestIndex = -1;
  $('#q').setAttribute('aria-expanded', 'false');
  $('#q').setAttribute('aria-activedescendant', '');
}

function chooseSuggestion(i) {
  const picked = state.suggestions[i];
  if (!picked) return;
  suggester.cancel();
  closeSuggestions();
  $('#q').value = picked.title;
  state.query = picked.title;
  history.replaceState(null, '', `?q=${encodeURIComponent(picked.title)}`);
  $('#search-form button[type=submit]').textContent = 'Search';
  search();
}

function wireTypeAhead() {
  const input = $('#q');

  input.addEventListener('input', () => {
    const kind = state.filters.groups.size === 1 ? [...state.filters.groups][0] : '';
    suggester.query(input.value, { kind, adult: state.filters.adult });
  });

  input.addEventListener('keydown', (e) => {
    const move = suggestKey(e.key, {
      index: state.suggestIndex, count: state.suggestions.length,
    });
    // Not a key this list handles -- including Enter with nothing highlighted,
    // which must stay an ordinary search for what was typed.
    if (!move) return;
    e.preventDefault();
    if (move.action === 'close') {
      suggester.cancel();
      closeSuggestions();
      return;
    }
    if (move.action === 'choose') {
      chooseSuggestion(move.index);
      return;
    }
    highlightSuggestion(move.index);
  });

  input.addEventListener('blur', () => {
    suggester.cancel();
    closeSuggestions();
  });
  input.addEventListener('focus', () => {
    if (input.value.trim().length >= 2) {
      const kind = state.filters.groups.size === 1 ? [...state.filters.groups][0] : '';
      suggester.query(input.value, { kind, adult: state.filters.adult });
    }
  });
}

/**
 * Show the paste-a-link panel for a URL from any network.
 *
 * One box, any link, no network picker -- which is the whole reason people use
 * fdown.net and snap-insta.to instead of a real tool. Detection happens here
 * and on the server; the visitor never says which site it is.
 */
async function resolvePastedLink(uri) {
  const mount = $('#linkpanel');
  $('#intro').hidden = true;
  $('#discover').hidden = true;
  $('#get').hidden = true;
  $('#library').hidden = true;
  showStatus('Reading that link…');

  const retry = () => resolvePastedLink(uri);
  try {
    const resolved = await resolveLink(uri);
    $('#status').hidden = true;
    renderLink(resolved, {
      mount,
      onRetry: retry,
      onPlay: (format) => play(
        { title: resolved.title, year: 0 },
        {
          uri: format.media,
          title: `${format.label} · ${format.sizeHuman}`,
          meta: { format, kind: resolved.kind },
        },
      ),
      // A collection entry is just another link, so it goes back through the
      // same door rather than getting its own half-implemented path.
      onOpenCollection: (item) => resolvePastedLink(item.url),
    });
  } catch (err) {
    if (err?.name === 'AbortError') return;
    $('#status').hidden = true;
    renderLinkError(err, { mount, onRetry: retry });
  }
}

/**
 * Spotify/Apple Music: read the public track list, then search for it here.
 *
 * The searches are the real ones -- the same /api/search every other query
 * uses, narrowed to the music domain -- so a track row finds actual releases
 * rather than being a button that looks like it does something.
 */
async function resolveMusicLink(uri) {
  const mount = $('#linkpanel');
  $('#intro').hidden = true;
  $('#discover').hidden = true;
  $('#get').hidden = true;
  showStatus('Reading that track list…');
  try {
    const result = await fetchMusicLink(uri);
    $('#status').hidden = true;
    renderMusicLink(result, {
      mount,
      onSearch: (query) => {
        mount.hidden = true;
        $('#q').value = query;
        state.query = query;
        // Narrow to music. A track title on its own drags in film and TV rips
        // that happen to share a word, and the domain filter is what makes the
        // result list look like the album somebody just pasted.
        //
        // Through changeGroups like every other route into the category chips:
        // it saves the numbers belonging to whatever was selected before and
        // brings music's own back, so this arrives with music's sort rather
        // than with a film's.
        changeGroups((groups) => { groups.clear(); groups.add('music'); });
        history.replaceState(null, '', `?q=${encodeURIComponent(query)}&groups=music`);
        search();
      },
    });
  } catch (err) {
    $('#status').hidden = true;
    renderLinkError({ code: err.code, error: err.message }, { mount });
  }
}

async function streamPasted() {
  const raw = $('#magnet').value.trim();
  if (!raw) {
    showRetry(PASTE_HINT, () => {});
    return;
  }
  $('#linkpanel').hidden = true;

  // normalizeMagnet's real job: turn a bare 40-hex info hash into a magnet
  // with trackers. If it doesn't recognize the input as a magnet/info hash,
  // pass the raw trimmed input straight through -- the registry decides
  // whether any resolver (url/embed/playlist) claims it.
  const magnet = normalizeMagnet(raw);
  const uri = magnet ?? raw;

  if (!state.registry) state.registry = buildRegistry();

  // Spotify and Apple Music are metadata-only: their audio is DRM-protected
  // and nothing here will pretend otherwise. Checked before everything else so
  // one of their links can never fall through to a video extractor.
  if (!magnet && isMusicLink(uri)) {
    resolveMusicLink(uri);
    return;
  }

  // Any ordinary page link -- Facebook, Instagram, TikTok, YouTube, X, Reddit,
  // Vimeo, Twitch and whatever else the engine covers -- gets the format list.
  // The specific resolvers (playlist, flash, ROM, archive.org) keep their own
  // behaviour; only the two generic ones hand over.
  if (!magnet && shouldResolveAsLink(uri, state.registry)) {
    resolvePastedLink(uri);
    return;
  }

  if (!state.registry.find(uri)) {
    // Nothing here can play a web page, but a torrent site's page is a
    // perfectly reasonable thing to paste -- it is where the magnet lives.
    if (/^https?:\/\//i.test(uri)) {
      showStatus('Reading that page…');
      try {
        const page = await linksFromPage(uri);
        offerPageLinks(page);
      } catch (err) {
        showRetry(`${err.message}. ${PASTE_HINT}`, () => {});
      }
      return;
    }
    showRetry(PASTE_HINT, () => {});
    return;
  }

  if (magnet) {
    const dn = /[?&]dn=([^&]+)/.exec(magnet);
    play(
      { title: dn ? decodeURIComponent(dn[1]).replace(/\+/g, ' ') : 'Pasted magnet', year: 0 },
      { magnet, title: magnet.slice(0, 90) },
    );
    return;
  }

  play(
    { title: titleFromUri(uri), year: 0 },
    { uri, title: uri.slice(0, 90) },
  );
}

// ---------------------------------------------------------------- settings --

/**
 * Point this client at a different Yarr.It, and connect your own services.
 *
 * The panel exists so that someone who runs their own stack is not reduced to
 * editing localStorage in a console -- and, since the services section landed,
 * so that a guest with no account has somewhere to put the address of their own
 * home server.
 */
function openSettings() {
  $('#set-server').value = getServer();
  showServerResult('', null);
  closeServiceEditor();
  renderServices();
  // Re-read on every open rather than only at startup. Another tab may have
  // changed a preference, a game may have written a save since, and a panel
  // that shows what WAS true is a panel that gets believed.
  syncSettingsControls();
  renderSavedFilters();
  renderStorage();
  // Probed on open rather than only on demand. A settings screen that shows
  // stale state is a settings screen that gets believed, and the states worth
  // showing here (a rejected key, a blocked address) are the ones nobody would
  // think to press a button to discover.
  testAllServices();
  $('#settings').hidden = false;
  document.body.style.overflow = 'hidden';
  // select(), not focus(). Focus alone leaves a cursor sitting in the existing
  // value, so typing an address merges with the old one instead of replacing
  // it -- "192.168.0.50" typed over "yarrit.com" becomes "192.168.0.50yarrit.com".
  // Almost nobody opens this box to edit one character; they open it to enter a
  // different server, so the whole value should go on the first keystroke.
  $('#set-server').select();
}

function closeSettings() {
  $('#settings').hidden = true;
  // A detail sheet left open underneath still wants the page behind it frozen.
  if ($('#detail').hidden) document.body.style.overflow = '';
}

// ------------------------------------------------------- settings: the panes --

/**
 * Six sections, one press apart.
 *
 * Nine stacked sections in a modal is a page somebody scrolls past looking for
 * the one thing they came for. The tab strip is the site's own chips, and the
 * panes are plain elements toggled with `hidden` -- no framework, no state to
 * get out of step with the strip.
 */
function wireSettingsTabs() {
  const strip = document.querySelector('.set-tabs');
  if (!strip) return;
  const tabs = [...strip.querySelectorAll('[data-pane]')];
  const show = (name) => {
    for (const t of tabs) {
      const on = t.dataset.pane === name;
      t.classList.toggle('on', on);
      t.setAttribute('aria-selected', on ? 'true' : 'false');
      const pane = document.querySelector(`#pane-${t.dataset.pane}`);
      if (pane) pane.hidden = !on;
    }
    // A pane opened after a scroll down another one starts halfway down.
    const sheet = document.querySelector('.set-sheet');
    if (sheet) sheet.scrollIntoView({ block: 'start' });
  };
  for (const t of tabs) t.addEventListener('click', () => show(t.dataset.pane));
  show(tabs[0]?.dataset.pane || 'look');
}

/**
 * A row of chips where exactly one is chosen.
 *
 * Buttons rather than a <select> because three short words side by side can be
 * compared at a glance, which is the entire question a theme picker asks; a
 * closed dropdown shows you one of the three answers.
 */
function wireChoice(sel, current, onPick) {
  const host = $(sel);
  if (!host) return () => {};
  const buttons = [...host.querySelectorAll('[data-value]')];
  const paint = (value) => {
    for (const b of buttons) {
      const on = b.dataset.value === value;
      b.classList.toggle('on', on);
      b.setAttribute('aria-checked', on ? 'true' : 'false');
      b.setAttribute('role', 'radio');
    }
  };
  for (const b of buttons) {
    b.addEventListener('click', () => { paint(b.dataset.value); onPick(b.dataset.value); });
  }
  paint(current);
  return paint;
}

/**
 * The three attributes the stylesheet reads, set on <html>.
 *
 * The same three the inline script in <head> sets before the first paint. This
 * is what makes a change take effect without a reload; that one is what stops a
 * light-theme visitor seeing a dark page flash past on every navigation.
 */
function applyAppearance(a = prefs.appearance()) {
  const r = document.documentElement;
  r.setAttribute('data-theme', a.theme);
  r.setAttribute('data-covers', a.covers);
  r.setAttribute('data-motion', a.motion);
  // The address bar and the Android task switcher take their colour from this,
  // so a light theme with a near-black meta tag looks like two different apps.
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) {
    meta.content = a.theme === 'light' ? '#f5f7fa' : (a.theme === 'midnight' ? '#000000' : '#0b0d11');
  }
}

/** Every settings control, set from what is stored. */
function syncSettingsControls() {
  const a = prefs.appearance();
  const p = prefs.playback();
  paintTheme?.(a.theme);
  paintCovers?.(a.covers);
  paintUpscale?.(p.upscale);
  paintGamePlayer?.(readPlayerPreference() || ROUTE.EMULATORJS);
  const check = (sel, on) => { const n = $(sel); if (n) n.checked = Boolean(on); };
  check('#set-motion', a.motion === 'reduced');
  check('#set-autoplay', p.autoplay);
  check('#set-subtitles', p.subtitles === 'on');
  check('#set-adult', state.filters.adult);
  check('#set-websafe', state.filters.webSafe);
  const lang = $('#set-lang');
  if (lang) { fillLanguageMenu(lang); lang.value = state.filters.lang; }
}

let paintTheme = null;
let paintCovers = null;
let paintUpscale = null;
let paintGamePlayer = null;

function wireSettingsPrefs() {
  wireSettingsTabs();

  paintTheme = wireChoice('#set-theme', prefs.appearance().theme, (v) => {
    applyAppearance(prefs.setAppearance({ theme: v }));
  });
  paintCovers = wireChoice('#set-covers', prefs.appearance().covers, (v) => {
    applyAppearance(prefs.setAppearance({ covers: v }));
  });
  paintUpscale = wireChoice('#set-upscale', prefs.playback().upscale, (v) => {
    prefs.setPlayback({ upscale: v });
    // Re-fit whatever is on screen now rather than at the next play: a picture
    // setting you have to close and reopen the player to judge is a setting
    // nobody can judge.
    refitVideo();
  });
  paintGamePlayer = wireChoice('#set-gameplayer', readPlayerPreference() || ROUTE.EMULATORJS,
    (v) => writePlayerPreference(v));

  const onCheck = (sel, fn) => {
    const n = $(sel);
    if (n) n.addEventListener('change', () => fn(n.checked));
  };
  onCheck('#set-motion', (on) => {
    applyAppearance(prefs.setAppearance({ motion: on ? 'reduced' : 'full' }));
  });
  onCheck('#set-autoplay', (on) => {
    prefs.setPlayback({ autoplay: on });
    for (const selector of ['#video', '#audio']) {
      const media = $(selector);
      if (media) media.autoplay = on;
    }
  });
  onCheck('#set-subtitles', (on) => prefs.setPlayback({ subtitles: on ? 'on' : 'off' }));

  // The two that are also chips on the filter bar. Both surfaces write the one
  // value and both are repainted, so they can never disagree -- which is the
  // failure that makes a settings panel worse than no settings panel.
  onCheck('#set-adult', (on) => {
    state.filters.adult = on;
    persistFilters();
    renderFilters();
    refilter();
  });
  onCheck('#set-websafe', (on) => {
    state.filters.webSafe = on;
    persistFilters();
    renderFilters();
    refilter();
  });

  const lang = $('#set-lang');
  if (lang) {
    fillLanguageMenu(lang);
    lang.addEventListener('change', () => {
      setLanguage(lang.value);
      renderFilters();
      refilter();
    });
  }

  applyAppearance();
}

// ------------------------------------------------- settings: what is stored --

const DOMAIN_FILTER_LABELS = {
  sort: 'sort', seeders: 'min seeders', minSize: 'min size',
  maxSize: 'max size', quality: 'quality', codec: 'codec', source: 'source',
};

/** "Most seeders, min seeders 40, quality 1080p" -- or nothing, for a default. */
function describeDomainFilters(d) {
  const bits = [];
  for (const [key, label] of Object.entries(DOMAIN_FILTER_LABELS)) {
    const v = d[key];
    if (Array.isArray(v)) { if (v.length) bits.push(`${label} ${v.join('/')}`); continue; }
    if (v === '' || v == null) continue;
    if (v === DOMAIN_DEFAULTS[key]) continue;
    bits.push(`${label} ${v}`);
  }
  return bits.join(' · ');
}

/**
 * Everything the filter bar is holding, per category, with a way to drop it.
 *
 * The point is not the list, it is that the list can be READ. A filter set once
 * and remembered forever is only an improvement if there is somewhere that says
 * what is currently set -- otherwise the first time somebody notices is when
 * the catalogue looks half its size and nothing explains why.
 */
function renderSavedFilters() {
  const host = $('#set-filters');
  if (!host) return;
  host.replaceChildren();

  const snap = prefs.snapshot();
  const rows = [];

  const shared = [];
  if (snap.shared.groups.length) {
    shared.push(snap.shared.groups.map((g) => GROUP_LABELS[g] || g).join(' + '));
  }
  if (snap.shared.adult) shared.push('adult results shown');
  if (snap.shared.webSafe) shared.push('browser-playable only');
  if (snap.shared.lang) shared.push(`language ${snap.shared.lang}`);
  if (shared.length) rows.push({ name: 'Everywhere', sub: shared.join(' · ') });

  for (const [key, d] of Object.entries(snap.domains)) {
    const sub = describeDomainFilters(d);
    if (!sub) continue;
    rows.push({ name: GROUP_LABELS[key] || (key === 'all' ? 'Any category' : key), sub });
  }

  if (!rows.length) {
    host.append(el('p', 'set-none', 'Nothing saved — the filter bar is at its defaults.'));
    return;
  }

  for (const row of rows) {
    const item = el('div', 'set-item');
    const txt = el('div', 'set-item-txt');
    txt.append(el('div', 'set-item-name', row.name));
    txt.append(el('div', 'set-item-sub', row.sub));
    item.append(txt);
    host.append(item);
  }

  const clear = el('button', 'btn btn-ghost', 'Clear all saved filters');
  clear.type = 'button';
  clear.addEventListener('click', () => clearAllFilters());
  host.append(clear);
}

/**
 * What is in this browser, and the way to remove it.
 *
 * Grouped and measured rather than listed as raw keys: "yarrit_services" is not
 * a thing anybody recognises, and 41 KB next to "Game saves" is the difference
 * between a list and a warning.
 */
function renderStorage() {
  const host = $('#set-storage');
  if (!host) return;
  host.replaceChildren();

  const kinds = describeStored();
  if (!kinds.length) {
    host.append(el('p', 'set-none', 'Nothing at all. This browser has never saved anything here.'));
    return;
  }

  for (const kind of kinds) {
    const item = el('div', 'set-item');
    const txt = el('div', 'set-item-txt');
    txt.append(el('div', 'set-item-name', kind.label));
    txt.append(el('div', 'set-item-sub',
      `${kind.detail} · ${formatBytes(kind.bytes)}${kind.count > 1 ? ` · ${kind.count} entries` : ''}`));
    item.append(txt);

    const drop = el('button', null, 'Forget');
    drop.type = 'button';
    drop.addEventListener('click', () => {
      // Named, and never a single button that takes the lot. A "clear
      // everything" that quietly ate somebody's emulator save states would be
      // the worst control on this site.
      const n = forgetStored([kind.id]);
      showStorageResult(n ? `Removed ${kind.label.toLowerCase()}.` : 'Nothing to remove.', true);
      if (kind.id === 'prefs') {
        // The store still holds the old values in memory; put both back to
        // defaults so the panel is not describing a record that is gone.
        prefs.clearAll();
        applyAppearance();
        clearAllFilters({ research: false });
        syncSettingsControls();
      }
      renderStorage();
      renderSavedFilters();
    });
    item.append(drop);
    host.append(item);
  }
}

function showStorageResult(text, ok) {
  const r = $('#set-storage-result');
  if (!r) return;
  r.textContent = text;
  r.classList.toggle('ok', ok === true);
  r.classList.toggle('bad', ok === false);
  r.hidden = !text;
}

function showServerResult(text, ok) {
  const r = $('#set-result');
  r.textContent = text;
  r.classList.toggle('ok', ok === true);
  r.classList.toggle('bad', ok === false);
  r.hidden = !text;
}

async function testServer() {
  const raw = $('#set-server').value.trim();
  if (!raw) {
    showServerResult('Blank uses this site, which is already answering.', true);
    return;
  }
  showServerResult('Checking…', null);
  const res = await probeServer(raw);
  showServerResult(res.ok ? 'Reached it — that is a Yarr.It server.' : res.error, res.ok);
}

function saveServer() {
  const raw = $('#set-server').value.trim();
  const saved = setServer(raw);
  // Refusing to save beats saving nothing quietly: a typo would otherwise look
  // like it had been accepted while the client fell back to this site.
  if (raw && !saved) {
    showServerResult('That does not look like an address.', false);
    return;
  }
  // Every module resolves the address as it makes each call, but searches and
  // shelves already on the page came from the old one. A reload is the honest
  // way to leave nothing behind from the previous server.
  location.reload();
}

// ----------------------------------------------------------- your services --

/**
 * Somebody else's home server, configured by somebody with no account.
 *
 * This section is what separates a demo from a product. A guest opens it, types
 * in the address of the Radarr on their own shelf, and it works -- or it says,
 * in one sentence, exactly which wall stopped it and what gets past. Nothing
 * here asks who they are, because nothing here needs to: the config lives in
 * this browser and the requests go straight from this browser to their box.
 */

const svcStore = createServiceStore();

// id -> { status: 'testing' | 'done', route, health }. Kept out of the store on
// purpose: a probe result is about right now, and persisting it would let a
// stale "healthy" outlive the service it described.
const svcProbes = new Map();
// The id being edited, '' for a new one, null when the editor is closed.
let svcEditing = null;

function renderServices() {
  const list = $('#svc-list');
  list.textContent = '';
  const rows = svcStore.list();

  if (!rows.length) {
    list.append(el('p', 'svc-empty',
      'Nothing connected yet. Add the Radarr, Sonarr, Jellyfin, Plex, Komga or RomM you '
      + 'already run and it will show up here.'));
  }

  for (const svc of rows) list.append(serviceRow(svc));
  renderServiceAdvice(rows);
}

function serviceRow(svc) {
  const spec = SERVICE_TYPES[svc.type];
  const node = el('div', 'svc' + (svc.enabled ? '' : ' off'));
  node.dataset.id = svc.id;

  const top = el('div', 'svc-top');
  top.append(el('b', 'svc-name', svc.name));
  top.append(el('span', 'svc-kind', spec.label));

  const probe = svcProbes.get(svc.id);
  if (!svc.enabled) top.append(el('span', 'hp hp-not_configured', 'Not in use'));
  else if (!probe) top.append(el('span', 'hp hp-untested', 'Not tested yet'));
  else if (probe.status === 'testing') top.append(el('span', 'hp hp-testing', 'Testing…'));
  else top.append(el('span', `hp hp-${probe.health.state}`, healthLabel(probe.health.state)));

  const actions = el('div', 'svc-actions');
  for (const [act, label, cls] of [
    ['test', 'Test', ''],
    ['edit', 'Edit', ''],
    ['toggle', svc.enabled ? 'Disable' : 'Enable', ''],
    ['remove', 'Remove', 'danger'],
  ]) {
    const b = el('button', cls, label);
    b.type = 'button';
    b.dataset.act = act;
    actions.append(b);
  }
  top.append(actions);
  node.append(top);

  // The address, never the key. Whether a key exists is worth saying; what it
  // is, is not, and putting it on screen is how it ends up in a screenshot.
  const meta = el('p', 'svc-meta');
  meta.append(document.createTextNode(svc.url));
  meta.append(el('span', '', ` · ${describeService(svc)} · `));
  meta.append(el('span', '', svc.key ? `${spec.credential} saved` : `no ${spec.credential.toLowerCase()} yet`));
  node.append(meta);

  if (svc.enabled && probe && probe.status === 'done') {
    node.append(el('p', 'svc-route', routeLine(probe.route)));
    // transport.js and the probe already wrote a sentence aimed at whoever is
    // reading this screen. It is repeated as written: paraphrasing it here
    // would put two explanations of one problem in front of the same person.
    if (probe.health.detail) node.append(el('p', 'svc-detail', probe.health.detail));
  }
  return node;
}

/** Which of the three doors this service came through, in plain words. */
function routeLine(route) {
  if (route.transport === TRANSPORT.DIRECT) return 'Route: reachable directly from this page.';
  if (route.transport === TRANSPORT.EXTENSION) return 'Route: reached through the Yarr.It extension.';
  if (route.transport === TRANSPORT.SERVER) return 'Route: proxied by your own Yarr.It instance.';
  return 'Route: nothing here can reach it — you would need your own instance.';
}

function renderServiceAdvice(rows) {
  const box = $('#svc-advice');
  box.textContent = '';
  const advice = routeAdvice(rows, {
    hasExtension: extensionAvailable(),
    pageProtocol: location.protocol,
  });
  box.hidden = !advice;
  if (!advice) return;

  box.append(el('p', 'adv-why', advice.reason));
  for (const r of advice.routes) {
    const a = el('a', '', r.title);
    a.href = r.href;
    a.rel = 'noopener';
    a.append(el('em', '', r.detail));
    box.append(a);
  }
}

async function testService(id) {
  const svc = svcStore.get(id);
  if (!svc) return;
  svcProbes.set(id, { status: 'testing' });
  renderServices();
  const result = await probeService(svc);
  svcProbes.set(id, { status: 'done', ...result });
  renderServices();
}

async function testAllServices() {
  // In parallel, and each one already bounded by transport.js. Serially, ten
  // services behind a dead VPN would take ten timeouts to paint one panel --
  // the panel someone opened in order to fix them.
  await Promise.all(svcStore.list().filter((s) => s.enabled).map((s) => testService(s.id)));
}

// --- the editor ------------------------------------------------------------

function fillServiceTypes() {
  const sel = $('#svc-type');
  if (sel.options.length) return;
  // Built from the table rather than typed into the markup, so the picker can
  // never drift from the set of things this client can actually talk to.
  for (const id of allServiceTypes()) {
    const o = document.createElement('option');
    o.value = id;
    o.textContent = SERVICE_TYPES[id].label;
    sel.append(o);
  }
}

function syncKeyLabels() {
  const spec = SERVICE_TYPES[$('#svc-type').value];
  if (!spec) return;
  $('#svc-key-label').textContent = spec.credential;
  const where = `Where to find it: ${spec.credentialHint}.`;
  // Two of these have no header scheme and take the key in the query string.
  // Said out loud, because it changes where that key can end up.
  $('#svc-key-hint').textContent = spec.keyInURL
    ? `${where} ${spec.label} has no header for this, so the key travels in the query `
      + 'string of the request to your own server — it may appear in that server\'s logs.'
    : where;
}

function openServiceEditor(id) {
  fillServiceTypes();
  svcEditing = id ?? '';
  const svc = id ? svcStore.get(id) : null;

  $('#svc-type').value = svc ? svc.type : 'radarr';
  $('#svc-name').value = svc ? svc.name : '';
  $('#svc-url').value = svc ? svc.url : '';
  $('#svc-key').value = svc ? svc.key : '';
  $('#svc-enabled').checked = svc ? svc.enabled : true;
  syncKeyLabels();
  showServiceResult('', null);

  $('#svc-editor').hidden = false;
  $('#svc-buttons').hidden = true;
  $('#svc-name').focus();
}

function closeServiceEditor() {
  svcEditing = null;
  $('#svc-editor').hidden = true;
  $('#svc-buttons').hidden = false;
  // The key must not sit in a DOM node after the box is closed.
  $('#svc-key').value = '';
}

function showServiceResult(text, ok) {
  const r = $('#svc-result');
  r.textContent = text;
  r.classList.toggle('ok', ok === true);
  r.classList.toggle('bad', ok === false);
  r.hidden = !text;
}

/** What the boxes currently say, as a service that may not be saved yet. */
function draftService() {
  return {
    id: svcEditing || 'draft',
    type: $('#svc-type').value,
    name: $('#svc-name').value.trim() || SERVICE_TYPES[$('#svc-type').value].label,
    url: normaliseServiceURL($('#svc-url').value),
    key: $('#svc-key').value.trim(),
    enabled: true,
  };
}

/**
 * Probe what is in the boxes, before it is saved.
 *
 * Testing the draft rather than the stored copy is the whole value of the
 * button: it answers "is this key right" while the key is still in front of
 * you, instead of after you have saved it and have to guess which field to
 * change.
 */
async function testDraftService() {
  const draft = draftService();
  if (!draft.url) {
    showServiceResult('That does not look like an address.', false);
    return;
  }
  showServiceResult('Checking…', null);
  const { route, health } = await probeService(draft);
  showServiceResult(
    `${healthLabel(health.state)} — ${health.detail} (${routeLine(route).replace(/^Route: /, '')})`,
    health.state === 'healthy',
  );
}

function saveServiceEditor() {
  const input = {
    type: $('#svc-type').value,
    name: $('#svc-name').value,
    url: $('#svc-url').value,
    key: $('#svc-key').value,
    enabled: $('#svc-enabled').checked,
  };
  const res = svcEditing ? svcStore.update(svcEditing, input) : svcStore.add(input);
  // Refusing to save beats saving nothing quietly: an address that appears to
  // save is a row that looks configured, answers nothing, and gives no clue
  // which half of it is wrong.
  if (!res.ok) {
    showServiceResult(res.error, false);
    return;
  }
  const id = res.service.id;
  svcProbes.delete(id);
  closeServiceEditor();
  renderServices();
  if (res.service.enabled) testService(id);
}

function onServiceListClick(e) {
  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  const id = btn.closest('.svc')?.dataset.id;
  if (!id) return;

  if (btn.dataset.act === 'test') { testService(id); return; }
  if (btn.dataset.act === 'edit') { openServiceEditor(id); return; }
  if (btn.dataset.act === 'toggle') {
    const svc = svcStore.get(id);
    svcStore.update(id, { enabled: !svc.enabled });
    svcProbes.delete(id);
    renderServices();
    if (!svc.enabled) testService(id);
    return;
  }
  if (btn.dataset.act === 'remove') {
    const svc = svcStore.get(id);
    if (!confirm(`Remove ${svc.name}? The key stored for it is deleted from this browser too.`)) return;
    svcStore.remove(id);
    svcProbes.delete(id);
    renderServices();
  }
}

// -------------------------------------------------------------------- init --


/**
 * Paint the account and network-protection panels.
 *
 * Both were fully working on the server and unreachable in the page: /auth/login
 * has always redirected to Authentik and /auth/me has always answered, but
 * nothing ever called them, so "how do I sign in?" had no answer.
 */
async function refreshAccount() {
  const g = vpnGuidance(getServer());
  const lede = $('#vpn-lede');
  if (lede) lede.textContent = g.body + ' ' + g.appliesTo;
  const cmd = $('#vpn-cmd');
  if (cmd) cmd.textContent = g.command;

  // Report the address the outside world sees, when the server offers it. A
  // claim of protection nobody can check is worth nothing.
  egressStatus().then((e) => {
    const el = $('#vpn-status');
    if (!el || !e) return;
    el.textContent = 'Your server currently reaches the internet as ' + e + '.';
    el.hidden = false;
  });

  const me = await whoAmI();
  const who = $('#acct-who');
  const inBtn = $('#acct-signin');
  const outBtn = $('#acct-signout');
  const hdrIn = $('#signin-btn');
  const hdrOut = $('#signout-btn');
  const hdrLibrary = $('#library-open');

  if (me) {
    if (who) who.textContent = displayName(me);
    if (inBtn) inBtn.hidden = true;
    if (outBtn) outBtn.hidden = false;
    if (hdrIn) hdrIn.hidden = true;
    if (hdrOut) hdrOut.hidden = false;
    if (hdrLibrary) hdrLibrary.hidden = false;
  } else {
    if (who) who.textContent = 'Not signed in';
    if (inBtn) inBtn.hidden = false;
    if (outBtn) outBtn.hidden = true;
    if (hdrIn) hdrIn.hidden = false;
    if (hdrOut) hdrOut.hidden = true;
    if (hdrLibrary) hdrLibrary.hidden = true;
  }
}

function wireAccount() {
  const go = (url) => () => { location.href = url(); };
  for (const id of ['#acct-signin', '#signin-btn']) {
    const el = $(id);
    if (el) el.addEventListener('click', go(signInURL));
  }
  for (const id of ['#acct-signout', '#signout-btn']) {
    const el = $(id);
    if (el) el.addEventListener('click', go(signOutURL));
  }
  refreshAccount();
}


/**
 * The add-on panel.
 *
 * Wired here rather than left as a module nobody imports, which is how the
 * service setup and the sign-in button both shipped invisible. A finished
 * feature that no control reaches has not shipped.
 */
const addonAPI = createAddonAPI();

async function refreshAddons() {
  const host = $('#addon-list');
  if (!host) return;
  let addons = [];
  try {
    addons = await addonAPI.list();
  } catch {
    // Signed out is the normal case: the list route needs a session because an
    // add-on URL can carry a debrid key. An empty panel is the right answer,
    // not an error.
    addons = [];
  }
  renderAddons(host, {
    addons,
    handlers: {
      onRemove: async (id) => { await addonAPI.remove(id).catch(() => {}); refreshAddons(); },
      onMove: async (id, delta) => {
        const next = moveAddon(addons, id, delta);
        await addonAPI.reorder(next.map((a) => a.id)).catch(() => {});
        refreshAddons();
      },
    },
  });
}

function wireAddons() {
  const input = $('#addon-url');
  const btn = $('#addon-add');
  const out = $('#addon-result');
  if (!input || !btn) return;

  const say = (msg, ok) => {
    if (!out) return;
    out.textContent = msg;
    out.classList.toggle('ok', ok === true);
    out.classList.toggle('bad', ok === false);
    out.hidden = !msg;
  };

  const add = async () => {
    const url = normaliseAddonURL(input.value);
    if (!url) {
      say('That does not look like an add-on manifest URL.', false);
      return;
    }
    say('Checking…', null);
    try {
      const a = await addonAPI.add(url);
      say(`Added ${a?.name || 'add-on'}.`, true);
      input.value = '';
      refreshAddons();
    } catch (e) {
      say(String(e && e.message ? e.message : e), false);
    }
  };

  btn.addEventListener('click', add);
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') add(); });
  refreshAddons();
}

function init() {
  if (localStorage.getItem('privacy-ack') === '1') $('#privacy').hidden = true;
  $('#privacy-ok').addEventListener('click', () => {
    localStorage.setItem('privacy-ack', '1');
    $('#privacy').hidden = true;
  });

  wireTypeAhead();

  $('#search-form').addEventListener('submit', (e) => {
    e.preventDefault();
    suggester.cancel();
    closeSuggestions();
    state.query = $('#q').value.trim();
    // No words but a category ticked is a browse: "show me games".
    if (!state.query && !state.filters.groups.size) return;
    history.replaceState(null, '', state.query ? `?q=${encodeURIComponent(state.query)}` : '?');
    $('#search-form button[type=submit]').textContent = 'Search';
    search();
  });

  $('#magnet-go').addEventListener('click', streamPasted);
  $('#magnet').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); streamPasted(); }
  });

  // Every one of these ends in persistFilters(). There is no Save button on
  // this bar and there must not be: eleven controls that all had to be re-set
  // after every reload is the defect this replaces, and a preference somebody
  // has to confirm is a preference most people lose.
  $('#f-seeders').addEventListener('input', (e) => {
    state.filters.seeders = Number(e.target.value) || 0; persistFilters(); refilter();
  });
  $('#f-minsize').addEventListener('input', (e) => {
    state.filters.minSize = e.target.value; persistFilters(); refilter();
  });
  $('#f-maxsize').addEventListener('input', (e) => {
    state.filters.maxSize = e.target.value; persistFilters(); refilter();
  });
  $('#f-sort').addEventListener('change', (e) => {
    state.filters.sort = e.target.value; persistFilters(); refilter();
  });
  $('#f-lang').addEventListener('change', (e) => {
    setLanguage(e.target.value);
    refilter();
  });
  $('#f-adult').addEventListener('click', () => {
    state.filters.adult = !state.filters.adult;
    $('#f-adult').classList.toggle('on', state.filters.adult);
    persistFilters();
    refilter();
  });

  $('#f-websafe').addEventListener('click', () => {
    state.filters.webSafe = !state.filters.webSafe;
    $('#f-websafe').classList.toggle('on', state.filters.webSafe);
    persistFilters();
    refilter();
  });
  const chooseProvider = (name) => {
    const current = [...state.filters.providers][0] || '';
    state.filters.providers = current.toLowerCase() === name.toLowerCase()
      ? new Set()
      : new Set([name]);
    state.filters.source = '';
    renderSourceFilters(state.facets);
  };
  $('#f-providers').addEventListener('click', (event) => {
    const chip = event.target.closest('[data-provider]');
    if (!chip) return;
    chooseProvider(chip.dataset.provider);
    persistFilters();
    if (state.cards.length) refilter();
  });
  $('#f-swarm').addEventListener('click', () => {
    const on = state.filters.source === 'swarm';
    state.filters.source = on ? '' : 'swarm';
    state.filters.providers = new Set();
    renderSourceFilters(state.facets);
    persistFilters();
    if (state.cards.length) refilter();
  });

  $('#filter-reset').addEventListener('click', clearAllFilters);

  $('#settings-open').addEventListener('click', openSettings);
  wireAccount();
  wireAddons();
  wireSettingsPrefs();
  $('#settings-close').addEventListener('click', closeSettings);
  $('#settings-x').addEventListener('click', closeSettings);
  $('#settings').addEventListener('click', (e) => { if (e.target.id === 'settings') closeSettings(); });
  $('#set-test').addEventListener('click', testServer);
  $('#set-save').addEventListener('click', saveServer);
  $('#set-server').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); testServer(); }
  });
  $('#set-reset').addEventListener('click', () => {
    setServer('');
    location.reload();
  });

  $('#svc-add').addEventListener('click', () => openServiceEditor(null));
  $('#svc-test-all').addEventListener('click', testAllServices);
  $('#svc-list').addEventListener('click', onServiceListClick);
  $('#svc-type').addEventListener('change', syncKeyLabels);
  $('#svc-test-one').addEventListener('click', testDraftService);
  $('#svc-cancel').addEventListener('click', closeServiceEditor);
  $('#svc-editor').addEventListener('submit', (e) => {
    e.preventDefault();
    saveServiceEditor();
  });

  $('#detail-close').addEventListener('click', closeDetail);
  $('#detail').addEventListener('click', (e) => { if (e.target.id === 'detail') closeDetail(); });
  $('#player-close').addEventListener('click', closePlayer);
  $('#reader-close').addEventListener('click', closeReader);
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    // Innermost first: settings can be opened over a detail sheet, and the
    // service editor sits inside settings. Escaping out of a half-typed key
    // straight past the panel would lose the whole entry.
    if (!$('#suggest').hidden) { suggester.cancel(); closeSuggestions(); }
    else if (!$('#svc-editor').hidden) closeServiceEditor();
    else if (!$('#settings').hidden) closeSettings();
    else if (!$('#reader').hidden) closeReader();
    else if (!$('#player').hidden) closePlayer();
    else if (!$('#detail').hidden) closeDetail();
  });

  // Everything this browser remembers, back onto the page BEFORE the chips are
  // drawn and before any search runs -- the restored filters have to be in
  // state by the time a `?q=` in the URL triggers one, or the first search of
  // the session is the only one that ignores them.
  applyStoredFilters();
  for (const selector of ['#video', '#audio']) {
    const media = $(selector);
    if (media) media.autoplay = prefs.playback().autoplay;
  }

  // Draw the category chips immediately. Without this they appear only once a
  // search has returned facets, which is exactly the state where you cannot
  // use them to choose what to search for.
  renderFilters();

  // The intro promises whatever the vocabulary actually covers. Hand-written,
  // it said "films, TV, music and images" long after books, comics and games
  // had been added — and the copy is the first thing that tells a visitor a
  // domain exists at all.
  const covers = domainSentence();
  if (covers) {
    $('#intro-covers').textContent = covers;
    $('#q').placeholder = `Search ${covers.toLowerCase()}…`;
  }

  // Category pages own the /browse/* routes and add one row of links to the
  // landing page. Nothing is fetched for a category until one is opened, so
  // this cannot touch the landing paint. Mounted before the route check
  // because a cold load of /browse/games/snes carries no ?q= either.
  mountBrowse({
    renderTile: (it, domain) => homeTile(itemFromDiscover(it, domain), tileHandlers()),
  });

  const params = new URLSearchParams(location.search);
  const initial = params.get('q');
  const wantsLibrary = params.get('library') === '1';
  if (!initial && !wantsLibrary && routeOf(location.pathname).view === 'site') { mountChannels(); loadHome(); }
  if (wantsLibrary) showSavedLibrary();
  if (initial) {
    applyURLFilters(params);
    renderFilters();
    $('#q').value = initial;
    state.query = initial;
    search();
  }
  const magnet = normalizeMagnet(params.get('magnet'));
  if (magnet) {
    const dn = /[?&]dn=([^&]+)/.exec(magnet);
    play({ title: dn ? decodeURIComponent(dn[1]).replace(/\+/g, ' ') : 'Direct magnet', year: 0 },
      { magnet, title: magnet.slice(0, 90) });
  }
}

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
else init();
