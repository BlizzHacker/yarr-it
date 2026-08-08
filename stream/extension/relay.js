// The page side of the relay bridge. Isolated world, trusted origins only.
//
// Deliberately thin. Every decision that matters -- is this page allowed, is
// this host granted, is this URL sane -- is made in the service worker against
// `sender.origin`, which a page cannot forge. This file only carries messages
// and drops the ones that are obviously not from the page it is sitting in.
//
// Not an ES module: MV3 content scripts are classic scripts, so relay-core.js
// cannot be imported here. That is why this file holds no rules of its own.

(() => {
  const REQUEST = 'yarrit:relay:request';
  const RESPONSE = 'yarrit:relay:response';
  const INTERNAL = 'yarrit:relay';

  function reply(message) {
    // Answering with the page's exact origin, never '*': a wildcard target
    // would hand the response to whatever is listening, and responses carry
    // the contents of the user's private services.
    window.postMessage(message, window.location.origin);
  }

  window.addEventListener('message', (ev) => {
    // Only this window talks to us. `ev.source !== window` rejects anything
    // posted in by a frame, an opener, or an embedder.
    if (ev.source !== window) return;
    // And only under this page's own origin. A sandboxed or cross-origin frame
    // posting up arrives with a different (or "null") origin and is dropped.
    if (ev.origin !== window.location.origin) return;

    const d = ev.data;
    if (!d || d.type !== REQUEST || typeof d.id !== 'string') return;

    let answered = false;
    const answerOnce = (m) => {
      if (answered) return;
      answered = true;
      reply(m);
    };

    try {
      chrome.runtime.sendMessage(
        { type: INTERNAL, payload: { id: d.id, url: d.url, init: d.init } },
        (res) => {
          // A service worker that was torn down mid-flight sets lastError and
          // calls back with undefined. Reading it is what stops Chrome logging
          // "Unchecked runtime.lastError" into the user's console.
          if (chrome.runtime.lastError || !res) {
            answerOnce({
              type: RESPONSE,
              id: d.id,
              error: chrome.runtime.lastError?.message || 'the extension did not answer',
            });
            return;
          }
          answerOnce({
            type: RESPONSE,
            id: d.id,
            status: res.status,
            headers: res.headers,
            body: res.body,
            error: res.error,
          });
        },
      );
    } catch (e) {
      // Thrown when the extension was updated or disabled under a live page.
      answerOnce({
        type: RESPONSE,
        id: d.id,
        error: 'the Yarr.It extension was reloaded; refresh this page',
      });
    }
  });
})();
