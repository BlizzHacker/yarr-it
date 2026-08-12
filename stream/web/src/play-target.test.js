import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  SOURCE, parseTarget, targetPath, romOrigins, checkRomUrl,
} from './play-target.js';

/**
 * The isolated player's address book.
 *
 * WHY THIS FILE IS THE PARANOID ONE. Every other page on yarrit.com decides
 * what to fetch from an answer the server gave it. This page decides from its
 * own URL, because that is what a route IS -- and a URL is written by whoever
 * sends the link. So the only two things it may take on trust are an opaque
 * identifier and a core name, and the one field that becomes a NETWORK
 * DESTINATION is checked against an allowlist rather than copied. Same rule
 * normaliseBios applies to a firmware URL, applied to a ROM.
 */

test('an archive.org target is its identifier and nothing else', () => {
  const t = parseTarget('/play/archive/msdos_Oregon_Trail_The_1990');
  assert.equal(t.source, SOURCE.ARCHIVE);
  assert.equal(t.id, 'msdos_Oregon_Trail_The_1990');
});

test('a trailing slash is not a different item', () => {
  assert.equal(parseTarget('/play/archive/zzt_WEIRDMM/').id, 'zzt_WEIRDMM');
});

test('a vault target carries its core and name on the fragment', () => {
  const t = parseTarget('/play/vimm/4', '#ejs=psp&name=Daxter');
  assert.equal(t.source, SOURCE.VIMM);
  assert.equal(t.id, '4');
  assert.equal(t.core, 'psp');
  assert.equal(t.name, 'Daxter');
});

test('a fragment core may only be shaped like a core name', () => {
  // EmulatorJS interpolates this straight into the path it fetches its core
  // from, so a name containing a separator is a traversal inside whichever
  // origin serves the cores. Nothing here decides whether the name is REAL --
  // that is the server's published list, checked in play-page.js, because a
  // second copy of the vocabulary is exactly the drift this codebase keeps
  // being bitten by.
  assert.equal(parseTarget('/play/vimm/4', '#ejs=../../etc&name=x').core, '');
  assert.equal(parseTarget('/play/vimm/4', '#ejs=seg a').core, '');
  assert.equal(parseTarget('/play/vimm/4', '#ejs=segaSaturn').core, 'segaSaturn');
});

test('an unknown source is no target at all', () => {
  assert.equal(parseTarget('/play/wherever/1').source, '');
  assert.equal(parseTarget('/play/').source, '');
  assert.equal(parseTarget('/').source, '');
});

test('an identifier may not climb out of its route', () => {
  // `/play/archive/..%2F..%2Fetc` decodes to a path, and an identifier that is
  // a path is not an identifier.
  assert.equal(parseTarget('/play/archive/..%2F..%2Fadmin').id, '');
  assert.equal(parseTarget('/play/archive/a b').id, '');
});

test('targetPath round-trips what parseTarget reads', () => {
  const path = targetPath({ source: SOURCE.VIMM, id: '4', core: 'psp', name: 'Daxter & Co' });
  const back = parseTarget(path.split('#')[0], `#${path.split('#')[1] ?? ''}`);
  assert.equal(back.source, SOURCE.VIMM);
  assert.equal(back.id, '4');
  assert.equal(back.core, 'psp');
  assert.equal(back.name, 'Daxter & Co');
});

test('targetPath escapes an identifier rather than trusting it', () => {
  assert.equal(targetPath({ source: SOURCE.ARCHIVE, id: 'a b/c' }), '/play/archive/a%20b%2Fc');
});

// ------------------------------------------------------------ the allowlist --

test('the Archive and our own relay are always allowed', () => {
  const allow = romOrigins('https://yarrit.com', '');
  assert.ok(checkRomUrl('https://archive.org/download/x/y.zip', allow, 'https://yarrit.com'));
  assert.ok(checkRomUrl('https://ia801603.us.archive.org/0/items/x/y.zip', allow, 'https://yarrit.com'));
  assert.ok(checkRomUrl('/bridge/iptv?u=https%3A%2F%2Farchive.org%2Fx', allow, 'https://yarrit.com'));
});

test('the vault is allowed only when this server publishes one', () => {
  assert.equal(
    checkRomUrl('https://vimm.yarrit.com/api/rom/4', romOrigins('https://yarrit.com', ''), 'https://yarrit.com'),
    false,
  );
  assert.ok(checkRomUrl(
    'https://vimm.yarrit.com/api/rom/4',
    romOrigins('https://yarrit.com', 'https://vimm.yarrit.com'),
    'https://yarrit.com',
  ));
});

test('a lookalike host is not the Archive', () => {
  // `archive.org.evil.test` ends with nothing that matters, and
  // `notarchive.org` merely contains the name. Both were accepted by the
  // obvious `endsWith` version of this check.
  const allow = romOrigins('https://yarrit.com', '');
  assert.equal(checkRomUrl('https://archive.org.evil.test/x', allow, 'https://yarrit.com'), false);
  assert.equal(checkRomUrl('https://notarchive.org/x', allow, 'https://yarrit.com'), false);
});

test('a non-http scheme is refused however it is spelled', () => {
  const allow = romOrigins('https://yarrit.com', 'https://vimm.yarrit.com');
  for (const url of ['javascript:alert(1)', 'data:application/octet-stream,AAAA', 'file:///etc/passwd']) {
    assert.equal(checkRomUrl(url, allow, 'https://yarrit.com'), false, url);
  }
});

test('http is refused even for an allowed host', () => {
  // The page is cross-origin isolated and served over TLS; a plain-http
  // subresource is blocked as mixed content anyway. Refusing it here means the
  // message says why instead of the fetch failing with nothing in it.
  const allow = romOrigins('https://yarrit.com', '');
  assert.equal(checkRomUrl('http://archive.org/download/x/y.zip', allow, 'https://yarrit.com'), false);
});

test('a self-hosted origin allows its own relay, not yarrit.com', () => {
  const allow = romOrigins('https://games.example.test', '');
  assert.ok(checkRomUrl('/bridge/iptv?u=x', allow, 'https://games.example.test'));
  assert.equal(
    checkRomUrl('https://yarrit.com/bridge/iptv?u=x', allow, 'https://games.example.test'),
    false,
  );
});
