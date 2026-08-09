import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  routeOf, countLabel, matchCategories, findDomain, findCategory, categoryCount,
} from './browse.js';

const TREE = [
  {
    key: 'games',
    title: 'Games',
    groups: [
      {
        key: 'nintendo',
        title: 'Nintendo',
        categories: [
          { key: 'ia:sys:snes', path: 'games/snes', title: 'Super Nintendo', badge: 'SNES', find: 'snes super nintendo sfc', count: 547, plays: true },
          { key: 'ia:sys:nes', path: 'games/nes', title: 'Nintendo Entertainment System', badge: 'NES', find: 'nes famicom fc', count: 543, plays: true },
        ],
      },
      {
        key: 'commodore',
        title: 'Commodore',
        categories: [
          { key: 'ia:sys:c64', path: 'games/c64', title: 'Commodore 64', badge: 'C64', count: 99993, plays: true },
        ],
      },
    ],
  },
  {
    key: 'books',
    title: 'Books',
    groups: [{
      key: 'books',
      title: 'Shelves',
      categories: [{ key: 'ia:col:gutenberg', path: 'books/gutenberg', title: 'Public domain classics', count: 56051 }],
    }],
  },
];

// A cold load of a category URL has to work, not just navigation from inside
// the app. Caddy serves index.html for any unknown path, so this is the only
// thing that decides what the app then shows.
test('a url is turned into the page it names', () => {
  assert.deepEqual(routeOf('/'), { view: 'site' });
  assert.deepEqual(routeOf('/browse'), { view: 'index' });
  assert.deepEqual(routeOf('/browse/'), { view: 'index' });
  assert.deepEqual(routeOf('/browse/games'), { view: 'domain', domain: 'games' });
  assert.deepEqual(routeOf('/browse/games/snes'), {
    view: 'category', domain: 'games', slug: 'snes', path: 'games/snes',
  });
  // Trailing slashes are what a browser adds, not a different page.
  assert.equal(routeOf('/browse/games/snes/').view, 'category');
  // Anything deeper is not a category.
  assert.equal(routeOf('/browse/games/snes/extra').view, 'missing');
  // A path this module does not own is left alone, so the rest of the site
  // works -- including the reader and the privacy page.
  assert.equal(routeOf('/privacy.html').view, 'site');
  assert.equal(routeOf('').view, 'site');
});

// The count is why a grid of tiles beats a list of names: seeing that the C64
// holds 100k and the VIC-20 holds one is what stops somebody opening the VIC-20.
test('counts are abbreviated the way a person reads them', () => {
  assert.equal(countLabel(0), '');
  assert.equal(countLabel(18), '18');
  assert.equal(countLabel(547), '547');
  assert.equal(countLabel(2662), '2.7k');
  assert.equal(countLabel(12975), '13k');
  assert.equal(countLabel(99993), '100k');
  assert.equal(countLabel(623671), '624k');
  assert.equal(countLabel(3410619), '3.4M');
});

// Fifty-odd systems is past the point of scanning, so a domain page has a find
// box -- and it has to match the shorthand as well as the name, because "snes"
// is what people type for "Super Nintendo".
test('finding a system works by name or by shorthand', () => {
  const cats = TREE[0].groups[0].categories;
  assert.equal(matchCategories(cats, 'super').length, 1);
  assert.equal(matchCategories(cats, 'SNES')[0].path, 'games/snes');
  assert.equal(matchCategories(cats, 'nes').length, 2); // "SNES" and "NES"
  assert.equal(matchCategories(cats, 'nintendo 64').length, 0);
  assert.equal(matchCategories(cats, '').length, 2);
  assert.equal(matchCategories(cats, '   ').length, 2);
});

test('a category is found by the path in its url', () => {
  const hit = findCategory(TREE, 'books/gutenberg');
  assert.equal(hit.domain.key, 'books');
  assert.equal(hit.cat.title, 'Public domain classics');
  assert.equal(findCategory(TREE, 'games/nonesuch'), null);
  assert.equal(findCategory(null, 'games/snes'), null);
});

test('a domain is found by the key in its url', () => {
  assert.equal(findDomain(TREE, 'games').title, 'Games');
  assert.equal(findDomain(TREE, 'nonesuch'), null);
  assert.equal(findDomain(null, 'games'), null);
});

test('a domain reports how many categories it has, for the landing links', () => {
  assert.equal(categoryCount(TREE[0]), 3);
  assert.equal(categoryCount(TREE[1]), 1);
  assert.equal(categoryCount({ key: 'x' }), 0);
  assert.equal(categoryCount(null), 0);
});

// A tree that arrived malformed must not throw on a page somebody is looking
// at; browsing is an addition to the site, never a requirement of it.
test('a malformed tree does not throw', () => {
  assert.equal(findCategory([{ key: 'x' }], 'a/b'), null);
  assert.equal(findCategory([{ key: 'x', groups: [{ key: 'g' }] }], 'a/b'), null);
  assert.equal(categoryCount({ groups: [{ key: 'g' }] }), 0);
});

// The alias list is the whole reason a find box beats scanning: nobody types
// "Sega Genesis / Mega Drive", they type "megadrive". Matching title and badge
// alone let "mega drive" through and dropped "megadrive".
test('a machine is found by an alias nobody would guess from its title', () => {
  const cats = [{ title: 'Sega Genesis / Mega Drive', badge: 'Genesis', find: 'megadrive md megadriv' }];
  assert.equal(matchCategories(cats, 'megadrive').length, 1);
  assert.equal(matchCategories(cats, 'mega drive').length, 1);
  assert.equal(matchCategories(cats, 'MEGADRIV').length, 1);
  assert.equal(matchCategories(cats, 'saturn').length, 0);
  // A category with no alias list still matches on what it does have.
  assert.equal(matchCategories([{ title: 'Cookbooks' }], 'cook').length, 1);
  assert.equal(matchCategories([{ title: 'Cookbooks' }], 'zzz').length, 0);
});

test('famicom finds the NES, which shares not one word with it', () => {
  const cats = TREE[0].groups[0].categories;
  assert.equal(matchCategories(cats, 'famicom')[0].path, 'games/nes');
  assert.equal(matchCategories(cats, 'sfc')[0].path, 'games/snes');
});
