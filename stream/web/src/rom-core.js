/**
 * Which emulator core does this ROM need?
 *
 * archive.org answers this for us -- every item declares its emulator. A
 * torrent declares nothing, so for the torrent path this has to be worked out
 * from the file itself, and getting it wrong is worse than failing: EmulatorJS
 * boots the wrong core happily, writes the ROM into its filesystem, reports
 * `started`, and then runs a black screen forever. There is no error to see.
 *
 * Extensions alone cannot do it. `.bin` is Colecovision, Atari 2600, Mega
 * Drive, PC Engine and half a dozen others; `.rom` is anyone's guess. That
 * ambiguity is exactly what produced a Colecovision game booted as an NES.
 *
 * So the order of evidence is:
 *
 *   1. the ROM's own header      -- authoritative, most systems have one
 *   2. an unambiguous extension  -- `.nes` is only ever an NES ROM
 *   3. the surrounding filename  -- "(SNES)" in a release name is a real hint
 *   4. size, for headerless systems -- Atari 2600 carts have no header at all
 *
 * and when none of them answers, this refuses rather than guessing. A refusal
 * is a message somebody can act on; a wrong core is a black rectangle.
 */

/** Extensions that identify exactly one system. */
const UNAMBIGUOUS = {
  nes: 'nes', fds: 'nes', unf: 'nes', unif: 'nes',
  smc: 'snes', sfc: 'snes', swc: 'snes', fig: 'snes',
  gb: 'gb', gbc: 'gb',
  gba: 'gba',
  n64: 'n64', z64: 'n64', v64: 'n64',
  md: 'segaMD', gen: 'segaMD', smd: 'segaMD',
  sms: 'segaMS', gg: 'segaGG',
  a26: 'atari2600', a78: 'atari7800',
  lnx: 'lynx',
  pce: 'pce',
  ws: 'ws', wsc: 'ws',
  ngp: 'ngp', ngc: 'ngp',
  vb: 'vb',
  col: 'coleco',
  int: 'intellivision',
  j64: 'jaguar', jag: 'jaguar',
};

/**
 * Extensions that mean "a ROM, system unknown". These are the ones that need
 * the header read; treating them as any particular system is the bug.
 */
export const AMBIGUOUS = new Set(['bin', 'rom', 'img', 'dat', 'a52', 'car']);

export function extensionOf(name = '') {
  const base = String(name).split(/[?#]/)[0];
  const dot = base.lastIndexOf('.');
  return dot === -1 ? '' : base.slice(dot + 1).toLowerCase();
}

export function coreFromExtension(name = '') {
  return UNAMBIGUOUS[extensionOf(name)] ?? null;
}

export function isRomName(name = '') {
  const ext = extensionOf(name);
  return ext in UNAMBIGUOUS || AMBIGUOUS.has(ext);
}

// ------------------------------------------------------------------ headers --

const ascii = (bytes, offset, text) => {
  if (offset + text.length > bytes.length) return false;
  for (let i = 0; i < text.length; i += 1) {
    if (bytes[offset + i] !== text.charCodeAt(i)) return false;
  }
  return true;
};

const bytesAt = (bytes, offset, expected) => {
  if (offset + expected.length > bytes.length) return false;
  for (let i = 0; i < expected.length; i += 1) {
    if (bytes[offset + i] !== expected[i]) return false;
  }
  return true;
};

// The first bytes of the Nintendo logo each handheld checks at boot. Verifying
// a prefix is enough to identify the system and avoids carrying 200 bytes of
// somebody else's copyrighted bitmap.
const GB_LOGO = [0xce, 0xed, 0x66, 0x66, 0xcc, 0x0d, 0x00, 0x0b];
const GBA_LOGO = [0x24, 0xff, 0xae, 0x51, 0x69, 0x9a, 0xa2, 0x21];

/**
 * A Super Nintendo cartridge has no magic number. What it does have is a
 * 32-byte internal header whose checksum and complement must XOR to 0xFFFF,
 * at one of three fixed addresses depending on how the cartridge is mapped.
 * That test is strong enough to identify the system on its own.
 */
function looksSNES(bytes, base) {
  for (const header of [0x7fc0, 0xffc0, 0x40ffc0]) {
    const at = base + header;
    if (at + 0x20 > bytes.length) continue;
    const complement = bytes[at + 0x1c] | (bytes[at + 0x1d] << 8);
    const checksum = bytes[at + 0x1e] | (bytes[at + 0x1f] << 8);
    if (checksum !== 0 && (checksum ^ complement) === 0xffff) return true;
  }
  return false;
}

/**
 * Master System and Game Gear share the "TMR SEGA" signature and are told
 * apart by the region nibble that follows it.
 */
function segaHandheld(bytes, at) {
  const region = (bytes[at + 0x0f] ?? 0) >> 4;
  // 5, 6 and 7 are the Game Gear regions; 3 and 4 are Master System.
  return region >= 5 && region <= 7 ? 'segaGG' : 'segaMS';
}

/**
 * Identify a system from the first bytes of a ROM.
 *
 * `bytes` is a Uint8Array. Only a header's worth is needed -- 64KB is more
 * than enough for every check here except the SNES HiROM address, so callers
 * streaming a torrent can sniff a prefix rather than waiting for the file.
 */
export function coreFromHeader(bytes) {
  if (!bytes || bytes.length < 16) return null;

  // Magic numbers, in order of how specific they are.
  if (ascii(bytes, 0, 'NES\x1a')) return 'nes';
  if (ascii(bytes, 0, 'FDS\x1a')) return 'nes';
  if (ascii(bytes, 0, 'LYNX')) return 'lynx';
  if (bytes[0] === 0x01 && ascii(bytes, 1, 'ATARI7800')) return 'atari7800';
  if (ascii(bytes, 0, 'SEGADISCSYSTEM')) return 'segaCD';

  // Nintendo 64, in each of the three byte orders its dumps come in.
  if (bytesAt(bytes, 0, [0x80, 0x37, 0x12, 0x40])) return 'n64'; // z64, big-endian
  if (bytesAt(bytes, 0, [0x37, 0x80, 0x40, 0x12])) return 'n64'; // v64, byteswapped
  if (bytesAt(bytes, 0, [0x40, 0x12, 0x37, 0x80])) return 'n64'; // n64, little-endian

  // Sega cartridges name themselves at 0x100.
  if (ascii(bytes, 0x100, 'SEGA')) {
    return ascii(bytes, 0x100, 'SEGA 32X') ? 'sega32x' : 'segaMD';
  }
  for (const at of [0x1ff0, 0x3ff0, 0x7ff0]) {
    if (ascii(bytes, at, 'TMR SEGA')) return segaHandheld(bytes, at);
  }

  // Handhelds identify themselves by the boot logo the hardware verifies.
  if (bytesAt(bytes, 0x104, GB_LOGO)) return 'gb';
  if (bytesAt(bytes, 0x04, GBA_LOGO)) return 'gba';

  if (ascii(bytes, 0x0a, 'COPYRIGHT BY SNK') || ascii(bytes, 0x0a, 'LICENSED BY SNK')) {
    return 'ngp';
  }

  // Colecovision carts begin with one of two boot signatures.
  if (bytesAt(bytes, 0, [0xaa, 0x55]) || bytesAt(bytes, 0, [0x55, 0xaa])) return 'coleco';

  // Super Nintendo, allowing for the 512-byte copier header some dumps carry.
  if (looksSNES(bytes, 0)) return 'snes';
  if (bytes.length % 1024 === 512 && looksSNES(bytes, 512)) return 'snes';

  return null;
}

// ------------------------------------------------------------------ context --

/**
 * Platform names as they appear in release names and folder names --
 * "Chrono Trigger (USA) [SNES]", "Sega Genesis Collection/".
 *
 * Ordered longest-first so "Game Boy Advance" is not matched as "Game Boy".
 */
const CONTEXT = [
  [/\b(game\s?boy\s?advance|gba)\b/i, 'gba'],
  [/\b(game\s?boy\s?colou?r|gbc)\b/i, 'gb'],
  [/\bgame\s?boy\b|\bgb\b/i, 'gb'],
  [/\b(super\s?nintendo|super\s?famicom|snes|sfc)\b/i, 'snes'],
  [/\b(nintendo\s?64|n64)\b/i, 'n64'],
  [/\b(famicom\s?disk|nintendo\s?entertainment|famicom|nes)\b/i, 'nes'],
  [/\b(mega\s?drive|megadrive|genesis|smd)\b/i, 'segaMD'],
  [/\b(sega\s?32x|32x)\b/i, 'sega32x'],
  [/\b(master\s?system|sega\s?mark\s?iii)\b/i, 'segaMS'],
  [/\b(game\s?gear|gamegear)\b/i, 'segaGG'],
  [/\b(atari\s?7800)\b/i, 'atari7800'],
  [/\b(atari\s?2600|vcs)\b/i, 'atari2600'],
  [/\b(atari\s?lynx|lynx)\b/i, 'lynx'],
  [/\b(turbo\s?grafx|pc\s?engine|tg-?16)\b/i, 'pce'],
  [/\b(neo\s?geo\s?pocket)\b/i, 'ngp'],
  [/\b(wonder\s?swan)\b/i, 'ws'],
  [/\b(virtual\s?boy)\b/i, 'vb'],
  [/\b(coleco\s?vision|colecovision)\b/i, 'coleco'],
  [/\b(intellivision)\b/i, 'intellivision'],
  [/\b(atari\s?jaguar|jaguar)\b/i, 'jaguar'],
];

export function coreFromContext(text = '') {
  for (const [pattern, core] of CONTEXT) {
    if (pattern.test(text)) return core;
  }
  return null;
}

// --------------------------------------------------------------------- size --

/**
 * Atari 2600 cartridges have no header of any kind -- the file is raw 6507
 * machine code from byte zero. The only thing left to go on is that carts came
 * in a small set of exact sizes.
 *
 * This is last, and only for a file nothing else explained, because plenty of
 * other ROMs are also exactly 8KB.
 */
const VCS_SIZES = new Set([2048, 4096, 8192, 12288, 16384, 32768, 65536]);

export function coreFromSize(length) {
  return VCS_SIZES.has(length) ? 'atari2600' : null;
}

// ------------------------------------------------------------------ combine --

/**
 * Work out the core, and say which evidence decided it.
 *
 * Returns `{core, via}`, or `{core: null, via: 'unknown'}` when nothing is
 * confident enough -- callers must treat that as a refusal, not a default.
 *
 * The header is trusted over the extension on purpose. A file named `.bin` in
 * a Colecovision set is the case that started this, and the header is right
 * where the name is silent. When an unambiguous extension and the header
 * disagree the header still wins: dumps get renamed, headers do not.
 */
export function detectCore({ name = '', bytes = null, context = '', length = null } = {}) {
  const fromHeader = bytes ? coreFromHeader(bytes) : null;
  if (fromHeader) return { core: fromHeader, via: 'header' };

  const fromExtension = coreFromExtension(name);
  if (fromExtension) return { core: fromExtension, via: 'extension' };

  // Only consult the surroundings for a file that is plausibly a ROM, so a
  // stray "(SNES)" in a folder name cannot turn a readme into a cartridge.
  if (isRomName(name)) {
    const fromContext = coreFromContext(`${context} ${name}`);
    if (fromContext) return { core: fromContext, via: 'context' };

    const size = length ?? bytes?.length ?? null;
    if (size !== null) {
      const fromSize = coreFromSize(size);
      if (fromSize) return { core: fromSize, via: 'size' };
    }
  }

  return { core: null, via: 'unknown' };
}

/** How much of a file is worth reading to identify it. */
export const SNIFF_BYTES = 0x8000 + 0x40; // through the SNES LoROM header
