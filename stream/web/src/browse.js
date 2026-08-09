/**
 * Browse: a page per category.
 *
 * WHAT THIS SOLVES
 *
 * The landing page shows one section per domain, each a rail or two. Behind
 * those the API describes hundreds of categories: fifty-odd game systems,
 * TMDB's film and television genres, and every Internet Archive collection
 * worth naming. Putting them on the page is not an option -- the paint budget
 * is ~0.2s, and thirty shelves of games is a worse home page than one shelf of
 * games.
 *
 * Growing the page in place is not the answer either. Expanding "Movies" into
 * twenty genre rows is twenty requests, twenty rails of skeletons, and a page
 * visibly working for ten seconds to show things nobody asked for -- strictly
 * worse than the one row that was there before.
 *
 * So a category is a PAGE. Three steps, each of which loads exactly one thing:
 *
 *   /                       the landing page, unchanged, plus one line of
 *                           domain links at the bottom
 *   /browse/games           every game system as a tile, with its real count.
 *                           No item data at all: the whole tree arrived in one
 *                           small payload, so this paints in the same frame as
 *                           the click even on a dead connection.
 *   /browse/games/snes      547 SNES games. This page fetches its own category
 *                           and nothing else.
 *
 * That is what makes hundreds of categories cost nothing until asked for, and
 * it is worth having on its own merits: the URL is linkable, the back button
 * works, and a reload lands where you were.
 *
 * WHY PATHS AND NOT `?cat=`
 *
 * Because a URL that 404s on refresh would be worse than an ugly one, this was
 * checked rather than assumed:
 *
 *   - the Caddyfile's last handler is `try_files {path} /index.html`, so any
 *     unknown path already serves this app;
 *   - every packaged client loads the site over https from the real origin (the
 *     MSIX StartPage is a URL, the APK wraps the site, the extension holds
 *     host_permissions on it) -- nothing runs from file://, where a path would
 *     have no meaning;
 *   - the service worker only claims `<scope>webtorrent/` and passes every
 *     other request through, so it cannot swallow a navigation.
 *
 * A cold load of /browse/games/snes therefore works, which is the requirement.
 * Search keeps its `?q=`: that is a query, not a place.
 */

const ROOT = '/browse';

/** Parse a location into a view. Anything unrecognised is "not a browse page". */
export function routeOf(pathname = '') {
  const parts = String(pathname).split('/').filter(Boolean);
  if (parts[0] !== 'browse') return { view: 'site' };
  if (parts.length === 1) return { view: 'index' };
  if (parts.length === 2) return { view: 'domain', domain: parts[1] };
  if (parts.length === 3) {
    return { view: 'category', domain: parts[1], slug: parts[2], path: `${parts[1]}/${parts[2]}` };
  }
  return { view: 'missing' };
}

/**
 * Counts are why a grid of tiles beats a list of names, so they have to be
 * readable at a glance: 99,993 is noise, 100k is the answer.
 */
export function countLabel(n) {
  if (!n) return '';
  if (n < 1000) return String(n);
  if (n < 10000) return `${(n / 1000).toFixed(1).replace(/\.0$/, '')}k`;
  if (n < 1000000) return `${Math.round(n / 1000)}k`;
  return `${(n / 1000000).toFixed(1).replace(/\.0$/, '')}M`;
}

/**
 * Filter categories by what somebody typed.
 *
 * Three things are matched, and the third is the one that matters: the title,
 * the badge, and the category's own list of other names. "snes" finds Super
 * Nintendo from the badge; "megadrive" finds the Genesis only from the aliases,
 * and "megadrive" is exactly what somebody looking for Sonic types. Matching
 * title and badge alone let "mega drive" through and dropped "megadrive",
 * which is the difference between an alias list and a coincidence.
 */
export function matchCategories(categories, term) {
  const q = String(term || '').trim().toLowerCase();
  if (!q) return categories;
  return categories.filter(
    (c) => `${c.title} ${c.badge || ''} ${c.find || ''}`.toLowerCase().includes(q),
  );
}

export function findDomain(tree, key) {
  return (tree || []).find((d) => d.key === key) || null;
}

export function findCategory(tree, path) {
  for (const domain of tree || []) {
    for (const group of domain.groups || []) {
      for (const cat of group.categories || []) {
        if (cat.path === path) return { domain, group, cat };
      }
    }
  }
  return null;
}

/** How many categories a domain publishes, for the landing-page links. */
export function categoryCount(domain) {
  return (domain?.groups || []).reduce((a, g) => a + (g.categories || []).length, 0);
}

// --------------------------------------------------------------------- DOM --

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

/**
 * This module ships its own stylesheet rather than adding rules to index.html.
 *
 * The rules reuse the page's existing custom properties, so it inherits the
 * site's theme -- including the two alternatives the settings panel offers --
 * rather than restating any of it. Keeping them here also means the whole
 * feature is one file: nothing else has to change for it to work, and nothing
 * else breaks if it is removed.
 */
const CSS = `
#browse-links { margin:30px 0 0; padding-top:22px; border-top:1px solid var(--line); }
#browse-links h3 { font-size:15px; margin:0 0 4px; }
#browse-links p { margin:0 0 12px; color:var(--muted); font-size:13px; }
.blink-row { display:flex; flex-wrap:wrap; gap:8px; }
.blink { display:inline-flex; align-items:baseline; gap:7px; padding:7px 13px; border-radius:999px;
  background:var(--panel); border:1px solid var(--line); color:var(--fg);
  text-decoration:none; font-size:13px; }
.blink:hover, .blink:focus-visible { border-color:var(--accent-dim); color:var(--accent); }
.blink small { color:var(--dim); font-size:11.5px; }

#browse-page { padding:6px 0 40px; }
.bcrumb { display:flex; align-items:center; gap:8px; font-size:12.5px; color:var(--dim); margin:0 0 14px;
  flex-wrap:wrap; }
.bcrumb a { color:var(--accent); text-decoration:none; }
.bcrumb a:hover { text-decoration:underline; }
.bhead { margin:0 0 4px; font-size:26px; letter-spacing:-.025em; }
.bsub { color:var(--muted); font-size:13.5px; margin:0 0 20px; }
.bsub .bplays { color:var(--accent); }
.bfind { width:min(300px,100%); padding:8px 12px; font-size:13px; background:var(--panel);
  color:var(--fg); border:1px solid var(--line); border-radius:10px; outline:none; margin:0 0 18px; }
.bfind:focus { border-color:var(--accent-dim); }
.bgroup { margin:0 0 24px; }
.bgroup-t { font-size:11.5px; text-transform:uppercase; letter-spacing:.06em; color:var(--dim); margin:0 0 10px; }
.bcards { display:grid; grid-template-columns:repeat(auto-fill,minmax(190px,1fr)); gap:10px; }
.bcard { display:block; text-align:left; text-decoration:none; padding:13px 15px; border-radius:var(--r);
  background:var(--panel); border:1px solid var(--line); color:var(--fg); cursor:pointer;
  transition:border-color .15s ease, transform .15s ease; }
.bcard:hover, .bcard:focus-visible { border-color:var(--accent-dim); transform:translateY(-2px); }
.bcard b { display:block; font-size:14px; font-weight:600; line-height:1.3; }
.bcard .bmeta { display:block; margin-top:5px; font-size:12px; color:var(--dim); }
.bcard .bmeta .on { color:var(--accent); }
.bempty { color:var(--muted); font-size:13.5px; }
.bmore { display:block; margin:22px auto 0; padding:10px 22px; border-radius:var(--r);
  background:var(--panel); border:1px solid var(--line); color:var(--fg); cursor:pointer; font-size:13px; }
.bmore:hover { border-color:var(--accent-dim); color:var(--accent); }
.bmore[disabled] { opacity:.5; cursor:default; }
/* Same shape as the search grid, which is an #id rule and so cannot be reused
   by class -- and matching it is the point: a category page should look like
   the rest of the site, not like a second site. */
.bitems, .bsk { display:grid; grid-template-columns:repeat(auto-fill,minmax(168px,1fr)); gap:20px 16px; }
.bsk-t { aspect-ratio:2/3; border-radius:var(--r);
  background:linear-gradient(100deg,var(--panel) 30%,var(--panel-2) 50%,var(--panel) 70%);
  background-size:220% 100%; animation:sheen 1.3s linear infinite; }
@media (prefers-reduced-motion: reduce) { .bsk-t { animation:none; } }
@media (max-width:720px) {
  .bitems, .bsk { grid-template-columns:repeat(auto-fill,minmax(132px,1fr)); gap:16px 12px; }
  .bhead { font-size:22px; }
}
`;

function installStyles(doc) {
  if (doc.getElementById('browse-css')) return;
  const s = doc.createElement('style');
  s.id = 'browse-css';
  s.textContent = CSS;
  doc.head.append(s);
}

/**
 * Mount browsing.
 *
 * `renderTile` is handed in rather than imported so this module never decides
 * what an item looks like or what clicking one does: home.js owns that, and
 * this owns which items are on screen.
 */
export function mountBrowse({
  renderTile,
  doc = document,
  fetchImpl = fetch,
  history: hist = globalThis.history,
  loc = globalThis.location,
} = {}) {
  if (typeof renderTile !== 'function') return null;
  const main = doc.querySelector('main');
  const anchor = doc.getElementById('discover');
  if (!main || !anchor) return null;
  installStyles(doc);

  // Everything the site normally shows on the landing page. A browse page
  // replaces it and puts it back on the way out, so this module never has to be
  // wired into main.js's own show/hide logic.
  const siteSections = ['#tv', '#intro', '#get', '#discover', '#resultbar', '#grid', '#library']
    .map((sel) => doc.querySelector(sel)).filter(Boolean);

  const links = el('section');
  links.id = 'browse-links';
  anchor.after(links);

  const page = el('section');
  page.id = 'browse-page';
  page.hidden = true;
  main.append(page);

  const state = { tree: null, route: routeOf(loc?.pathname || '/'), find: {}, items: [], nextPage: 1 };

  // The links strip belongs to the landing page, so it appears and disappears
  // with the rest of it. Mirroring #discover's `hidden` rather than exporting a
  // show/hide pair is what keeps the edit to main.js to one line: running a
  // search hides #discover, and this follows without main.js knowing it exists.
  if (typeof MutationObserver === 'function') {
    const mirror = () => {
      if (state.route.view === 'site') links.hidden = anchor.hidden;
    };
    new MutationObserver(mirror).observe(anchor, { attributes: true, attributeFilter: ['hidden'] });
  }

  const treeOnce = (async () => {
    try {
      const res = await fetchImpl('/api/categories');
      if (!res.ok) throw new Error(String(res.status));
      const body = await res.json();
      state.tree = Array.isArray(body?.domains) ? body.domains : [];
    } catch {
      state.tree = [];
    }
    return state.tree;
  })();

  // ------------------------------------------------------------ navigation --

  function go(href, { replace = false } = {}) {
    if (replace) hist.replaceState(null, '', href);
    else hist.pushState(null, '', href);
    state.route = routeOf(new URL(href, loc.href).pathname);
    render();
  }

  function link(href, cls, text) {
    const a = el('a', cls, text);
    a.href = href;
    a.addEventListener('click', (e) => {
      // Let a middle click or a modifier open a real new tab: these are links,
      // not buttons dressed as links.
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      go(href);
    });
    return a;
  }

  globalThis.addEventListener?.('popstate', () => {
    state.route = routeOf(loc.pathname);
    render();
  });

  // A search is a different place. Leaving the browse route in the capture
  // phase, before main.js's own submit handler runs, means the URL it writes is
  // `/?q=…` and not `/browse/games/snes?q=…` -- done here so main.js needs no
  // line for it.
  doc.getElementById('search-form')?.addEventListener('submit', () => {
    if (state.route.view !== 'site') go('/', { replace: true });
  }, true);

  // ---------------------------------------------------------------- views --

  // Entering remembers what the page was showing and hides it; leaving puts
  // exactly that back. Recording the previous state rather than un-hiding
  // everything matters: #resultbar and #grid belong to search, and revealing
  // them on the way out would resurrect an old result set under the landing
  // page.
  function enterBrowse() {
    for (const s of siteSections) {
      if (s.dataset.browseWas === undefined) s.dataset.browseWas = s.hidden ? '1' : '0';
      s.hidden = true;
    }
    links.hidden = true;
    page.hidden = false;
    globalThis.scrollTo?.(0, 0);
  }

  function leaveBrowse() {
    for (const s of siteSections) {
      if (s.dataset.browseWas !== undefined) {
        s.hidden = s.dataset.browseWas === '1';
        delete s.dataset.browseWas;
      }
    }
    links.hidden = false;
    page.hidden = true;
    page.replaceChildren();
  }

  function crumbs(...trail) {
    const c = el('nav', 'bcrumb');
    trail.forEach(([href, label], i) => {
      if (i) c.append(el('span', null, '›'));
      c.append(href ? link(href, null, label) : el('span', null, label));
    });
    return c;
  }

  /** The landing page's one-line way in. Never grows; it is a row of links. */
  function renderLinks() {
    links.replaceChildren();
    if (!state.tree?.length) return;
    links.append(el('h3', null, 'Browse by category'));
    links.append(el('p', null,
      'Everything here, sorted the way it actually is — by console, by genre, by collection.'));
    const row = el('div', 'blink-row');
    for (const d of state.tree) {
      const a = link(`${ROOT}/${d.key}`, 'blink', d.title);
      a.append(el('small', null, String(categoryCount(d))));
      row.append(a);
    }
    links.append(row);
  }

  /** /browse — every domain. */
  function renderIndex() {
    page.replaceChildren();
    page.append(crumbs(['/', 'Home'], [null, 'Browse']));
    page.append(el('h1', 'bhead', 'Browse'));
    page.append(el('p', 'bsub', 'Pick what you are in the mood for.'));
    const cards = el('div', 'bcards');
    for (const d of state.tree || []) {
      const a = link(`${ROOT}/${d.key}`, 'bcard');
      a.append(el('b', null, d.title));
      a.append(el('span', 'bmeta', `${categoryCount(d)} categories`));
      cards.append(a);
    }
    page.append(cards);
  }

  /**
   * /browse/games — the deliberate middle step.
   *
   * Tiles rather than a list, because the count is half the answer: seeing that
   * the Commodore 64 holds 100k items and the VIC-20 holds one is what stops
   * somebody opening the VIC-20. Nothing is fetched to draw this -- it all came
   * with the tree -- so it is instant regardless of connection.
   */
  function renderDomain(key) {
    page.replaceChildren();
    const domain = findDomain(state.tree, key);
    if (!domain) return renderMissing();

    page.append(crumbs(['/', 'Home'], [ROOT, 'Browse'], [null, domain.title]));
    page.append(el('h1', 'bhead', domain.title));
    const total = (domain.groups || []).flatMap((g) => g.categories || [])
      .reduce((a, c) => a + (c.count || 0), 0);
    // Counts arrive from a background refresh, so a process that has only just
    // started has none. Saying "0 items" then would be a lie; saying nothing is
    // a page without a subtitle for a minute.
    page.append(el('p', 'bsub', total
      ? `${categoryCount(domain)} categories · ${countLabel(total)} items`
      : `${categoryCount(domain)} categories`));

    const body = el('div');
    // A find box only earns its place once scanning stops working. Games has
    // fifty-odd systems; Comics has four.
    if (categoryCount(domain) > 12) {
      const find = el('input', 'bfind');
      find.type = 'search';
      find.placeholder = `Find in ${domain.title.toLowerCase()}…`;
      find.value = state.find[key] || '';
      find.addEventListener('input', () => { state.find[key] = find.value; draw(); });
      page.append(find);
    }
    page.append(body);

    function draw() {
      body.replaceChildren();
      const term = state.find[key] || '';
      let shown = 0;
      for (const g of domain.groups || []) {
        const cats = matchCategories(g.categories || [], term);
        if (!cats.length) continue;
        shown += cats.length;
        const box = el('div', 'bgroup');
        if (g.title) box.append(el('div', 'bgroup-t', g.title));
        const cards = el('div', 'bcards');
        for (const c of cats) cards.append(categoryCard(c));
        box.append(cards);
        body.append(box);
      }
      if (!shown) body.append(el('p', 'bempty', 'Nothing here by that name.'));
    }
    draw();
    return undefined;
  }

  function categoryCard(cat) {
    const a = link(`${ROOT}/${cat.path}`, 'bcard');
    a.append(el('b', null, cat.title));
    const meta = el('span', 'bmeta');
    meta.append(doc.createTextNode(cat.count ? `${countLabel(cat.count)} items` : 'browse'));
    // Whether our own player can run it is the difference between a game with a
    // touch pad and one you can only watch on a phone. Worth saying up front.
    if (cat.plays) {
      meta.append(doc.createTextNode(' · '));
      meta.append(el('span', 'on', 'plays here'));
    }
    a.append(meta);
    return a;
  }

  /** /browse/games/snes — the category itself, and nothing else. */
  async function renderCategory(route) {
    page.replaceChildren();
    state.items = [];
    state.nextPage = 1;

    let known = findCategory(state.tree, route.path);
    const trail = () => crumbs(
      ['/', 'Home'], [ROOT, 'Browse'],
      [`${ROOT}/${route.domain}`, known?.domain.title || route.domain],
      [null, known?.cat.title || route.slug],
    );
    let crumb = trail();
    page.append(crumb);
    const head = el('h1', 'bhead', known?.cat.title || route.slug);
    page.append(head);
    const sub = el('p', 'bsub');
    page.append(sub);

    const grid = el('div', 'bsk');
    for (let i = 0; i < 12; i++) grid.append(el('div', 'bsk-t'));
    page.append(grid);

    const more = el('button', 'bmore', 'Load more');
    more.type = 'button';
    more.hidden = true;
    page.append(more);

    let total = known?.cat.count || 0;
    let first = true;
    const perPage = 60;

    function paintSub() {
      const bits = [];
      if (total) bits.push(`${total.toLocaleString('en-US')} items`);
      if (state.items.length && total > state.items.length) bits.push(`showing ${state.items.length}`);
      sub.replaceChildren(doc.createTextNode(bits.join(' · ')));
      if (known?.cat.plays) {
        sub.append(doc.createTextNode(bits.length ? ' · ' : ''));
        sub.append(el('span', 'bplays', 'plays here, with touch controls'));
      }
    }

    function showMissing() {
      page.replaceChildren();
      page.append(crumbs(['/', 'Home'], [ROOT, 'Browse'], [null, 'Not found']));
      page.append(el('h1', 'bhead', 'No such category'));
      page.append(el('p', 'bsub', 'That link points at something this site does not have.'));
      page.append(link(ROOT, 'bmore', 'See what there is'));
    }

    async function load() {
      more.disabled = true;
      more.textContent = 'Loading…';
      try {
        const res = await fetchImpl(
          `/api/rows?path=${encodeURIComponent(route.path)}&limit=${perPage}&page=${state.nextPage}`,
        );
        if (res.status === 404) { showMissing(); return; }
        if (!res.ok) throw new Error(String(res.status));
        const body = await res.json();
        const items = body?.row?.items || [];
        if (body?.total) total = body.total;
        if (first) {
          grid.replaceChildren();
          grid.className = 'bitems';
          first = false;
        }
        if (!items.length && !state.items.length) {
          grid.append(el('p', 'bempty', 'Nothing in here right now.'));
          more.hidden = true;
          return;
        }
        for (const item of items) { state.items.push(item); grid.append(renderTile(item, route.domain)); }
        state.nextPage += 1;
        more.hidden = items.length < perPage || (total > 0 && state.items.length >= total);
        more.disabled = false;
        more.textContent = 'Load more';
        paintSub();
      } catch {
        if (first) {
          grid.replaceChildren(el('p', 'bempty',
            'Could not load that category — try again in a moment.'));
          first = false;
        }
        more.disabled = false;
        more.textContent = 'Try again';
        more.hidden = false;
      }
    }

    paintSub();
    more.addEventListener('click', load);
    // The tree may still be in flight on a cold load of this URL, so the
    // category's own data is fetched without waiting for it: the items are what
    // the page is for, the title is decoration.
    await load();
    // On a cold load of this URL the tree is still in flight, so the page was
    // drawn from the path alone -- "games › snes" rather than "Games › Super
    // Nintendo". When it lands, every part of the page that named the category
    // is repainted, not only the heading: a breadcrumb still reading `snes`
    // next to a heading reading "Super Nintendo" is worse than either.
    if (!known) {
      const late = findCategory(await treeOnce, route.path);
      if (late) {
        known = late;
        head.textContent = late.cat.title;
        total = late.cat.count || total;
        const fresh = trail();
        crumb.replaceWith(fresh);
        crumb = fresh;
        paintSub();
      }
    }
  }

  function renderMissing() {
    page.replaceChildren();
    page.append(crumbs(['/', 'Home'], [ROOT, 'Browse'], [null, 'Not found']));
    page.append(el('h1', 'bhead', 'No such page'));
    page.append(link(ROOT, 'bmore', 'See what there is'));
    return undefined;
  }

  function render() {
    const r = state.route;
    if (r.view === 'site') { leaveBrowse(); renderLinks(); return; }
    enterBrowse();
    if (r.view === 'index') renderIndex();
    else if (r.view === 'domain') renderDomain(r.domain);
    else if (r.view === 'category') renderCategory(r);
    else renderMissing();
  }

  // A cold load of a category URL must work, so the route is honoured before
  // the tree arrives; the tree only fills in names and counts when it lands.
  render();
  const ready = treeOnce.then(() => {
    if (state.route.view === 'site') renderLinks();
    else if (state.route.view !== 'category') render();
    return state.tree;
  });

  return { ready, state, go, render };
}

// ---------------------------------------------------- the search-side filter --

/**
 * The system chips in the filter bar.
 *
 * Drawn from the FACET a search returns, never from the catalogue. The
 * catalogue has fifty-odd machines; a search for "sonic" has six. Offering the
 * other forty-odd would be forty-odd chips that each empty the page, which is
 * worse than having no system filter at all -- so a machine appears here only
 * once the current results actually contain one.
 *
 * There is deliberately NO full-list fallback for an empty facet. That is the
 * bug categoryChips had and has since been fixed for: `systems: []` is an empty
 * array, not a missing field, and it means "no games in these results" -- the
 * one case where showing the whole catalogue would be exactly wrong.
 *
 * The chip says "Super Nintendo" and the request says `system=snes`: the API
 * sends both, because the slug is what a URL should carry and the name is what
 * a person should read.
 */
export function systemChips(host, systems, selected, onChange) {
  if (!host) return;
  host.replaceChildren();
  const list = systems || [];
  // Nothing to narrow by is not an empty filter row: hide it, so the bar does
  // not carry a permanently blank "System" label on a film search.
  const group = host.closest('.fgroup') || host.parentElement;
  if (group) group.hidden = list.length === 0;
  if (!list.length) return;

  for (const { value, label, count } of list) {
    const c = el('span', 'chip', label || value);
    c.append(el('span', 'cnt', String(count)));
    c.title = `${count} game${count === 1 ? '' : 's'} on ${label || value}`;
    if (selected.has(value)) c.classList.add('on');
    c.addEventListener('click', () => {
      if (selected.has(value)) selected.delete(value);
      else selected.add(value);
      c.classList.toggle('on');
      onChange();
    });
    host.append(c);
  }
}

/**
 * Find (or create) the filter bar's system row.
 *
 * Created here rather than written into index.html so the whole feature is one
 * module: nothing else has to be edited for the chips to have somewhere to
 * live, and nothing else breaks if this is removed.
 */
export function systemFilterHost(doc = document) {
  let host = doc.getElementById('f-systems');
  if (host) return host;
  const bar = doc.querySelector('#filters .wrap');
  if (!bar) return null;
  const group = doc.createElement('div');
  group.className = 'fgroup fcats';
  group.hidden = true;
  const label = doc.createElement('span');
  label.className = 'flabel';
  label.textContent = 'System';
  host = doc.createElement('span');
  host.id = 'f-systems';
  group.append(label, host);
  // Immediately after the category row, because "Games → SNES" is the order
  // somebody narrows in.
  const after = doc.getElementById('f-groups')?.closest('.fgroup');
  if (after) after.after(group);
  else bar.append(group);
  return host;
}
