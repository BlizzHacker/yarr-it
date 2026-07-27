// Minimal bencode encode/decode, enough for DHT messages.
//
// DHT (BEP 5) speaks bencode over UDP. Full libraries pull in Node Buffer
// polyfills, so this handles the four types the protocol actually uses and
// keeps byte strings as Uint8Array — node IDs and compact peer lists are binary
// and must never be run through a UTF-8 decoder, which would corrupt them.

const textEncoder = new TextEncoder();

export function encode(value) {
  const parts = [];
  write(value, parts);
  let len = 0;
  for (const p of parts) len += p.length;
  const out = new Uint8Array(len);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return out;
}

function write(v, parts) {
  if (typeof v === 'number') {
    parts.push(textEncoder.encode(`i${Math.trunc(v)}e`));
    return;
  }
  if (typeof v === 'string') {
    const b = textEncoder.encode(v);
    parts.push(textEncoder.encode(`${b.length}:`), b);
    return;
  }
  if (v instanceof Uint8Array) {
    parts.push(textEncoder.encode(`${v.length}:`), v);
    return;
  }
  if (Array.isArray(v)) {
    parts.push(textEncoder.encode('l'));
    for (const item of v) write(item, parts);
    parts.push(textEncoder.encode('e'));
    return;
  }
  if (v && typeof v === 'object') {
    parts.push(textEncoder.encode('d'));
    // Bencode requires dictionary keys in lexicographic order; peers that
    // validate strictly will reject a message otherwise.
    for (const key of Object.keys(v).sort()) {
      write(key, parts);
      write(v[key], parts);
    }
    parts.push(textEncoder.encode('e'));
    return;
  }
  throw new TypeError(`cannot bencode ${typeof v}`);
}

export function decode(buf) {
  const state = { i: 0, b: buf instanceof Uint8Array ? buf : new Uint8Array(buf) };
  return read(state);
}

function read(s) {
  const c = s.b[s.i];
  if (c === 0x69) return readInt(s); // 'i'
  if (c === 0x6c) return readList(s); // 'l'
  if (c === 0x64) return readDict(s); // 'd'
  return readBytes(s);
}

function readInt(s) {
  s.i++; // skip 'i'
  let n = '';
  while (s.b[s.i] !== 0x65) n += String.fromCharCode(s.b[s.i++]);
  s.i++; // skip 'e'
  return parseInt(n, 10);
}

function readBytes(s) {
  let n = '';
  while (s.b[s.i] !== 0x3a) n += String.fromCharCode(s.b[s.i++]); // ':'
  s.i++;
  const len = parseInt(n, 10);
  const out = s.b.subarray(s.i, s.i + len);
  s.i += len;
  return out;
}

function readList(s) {
  s.i++;
  const out = [];
  while (s.b[s.i] !== 0x65) out.push(read(s));
  s.i++;
  return out;
}

function readDict(s) {
  s.i++;
  const out = {};
  while (s.b[s.i] !== 0x65) {
    const key = new TextDecoder().decode(readBytes(s));
    out[key] = read(s);
  }
  s.i++;
  return out;
}
