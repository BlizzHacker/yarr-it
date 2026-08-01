package main

// Cross-origin access, so a client is not welded to one instance.
//
// Yarr.It is meant to be self-hosted: anyone runs the stack and points their
// apps at it, and ours is only the default. That is impossible without CORS --
// a browser refuses to read a response from another origin, so a client served
// from one host could never talk to a server on another.
//
// The split is deliberate and it is the whole security model here:
//
//   * the catalogue is public data and is opened to any origin
//   * the library, resume points and sign-in are personal, stay same-origin,
//     and never see a cross-origin credential
//
// `Access-Control-Allow-Origin: *` cannot be combined with credentials -- the
// browser itself refuses that pairing. That rule is doing useful work here: it
// means a cookie physically cannot leak to another instance, even if a future
// change forgot the distinction.

import "net/http"

// publicCORS wraps a handler that serves catalogue data.
func publicCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		// A television revalidates often; letting the preflight be cached
		// keeps that from doubling every request.
		w.Header().Set("Access-Control-Max-Age", "86400")
		// The response varies by origin even when the value is a wildcard, and
		// a shared cache that ignores this serves the wrong header.
		w.Header().Add("Vary", "Origin")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}
