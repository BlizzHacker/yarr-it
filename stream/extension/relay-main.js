// Runs in the page's own world, at document_start, on trusted origins only.
//
// Its whole job is one flag. The contract in stream/web/src/transport.js says
// the page sets nothing and the extension announces itself:
//
//   export function extensionAvailable() {
//     return typeof window !== 'undefined' && window.__yarritExtension === true;
//   }
//
// This has to be a MAIN-world script. A normal content script runs in an
// isolated world with its own `window`, so setting the flag there is invisible
// to the page -- the page would look, see nothing, and tell a guest their
// library is empty when the relay was sitting right there.
//
// document_start matters too: the page's own bundle checks the flag while
// deciding which transport to use, and a flag that arrives afterwards is a flag
// that arrived too late.

try {
  Object.defineProperty(window, '__yarritExtension', {
    value: true,
    // Configurable so a later injection (a reload, a second registration) can
    // redefine it instead of throwing. Not writable, so page script cannot
    // quietly clear it and strand every request.
    configurable: true,
    enumerable: true,
    writable: false,
  });
} catch {
  window.__yarritExtension = true;
}
