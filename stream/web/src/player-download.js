// Only providers that visibly publish a public ROM download are allowed into
// the player toolbar. Webmulator is deliberately absent: its download-looking
// controls are counters and its nested rom_url is not a public download link.
export function playerDownloadURL(raw) {
  let url;
  try {
    url = new URL(String(raw || ''));
  } catch {
    return '';
  }
  if (url.protocol !== 'https:') return '';
  const host = url.hostname.toLowerCase();
  if (host === 'archive.org' || host.endsWith('.archive.org')
      || host === 'vimm.net' || host.endsWith('.vimm.net')) {
    return url.href;
  }
  return '';
}
