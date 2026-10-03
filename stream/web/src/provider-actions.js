/** Human-facing verbs for public provider rows. Unknown capabilities are an
 * open page, never silently upgraded to a download. */
export function offsiteVerb(action) {
  if (action === 'play') return 'PLAY THERE';
  if (action === 'download') return 'DOWNLOAD';
  return 'OPEN PAGE';
}
