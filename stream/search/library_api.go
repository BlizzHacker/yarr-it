package main

// HTTP surface for the library and resume points.
//
// Every endpoint here needs a session, and needs one *regardless of
// AUTH_SCOPE*. That is not the same rule the catalogue follows: browsing is
// open, because a visitor should see the site without an account. But there is
// no such thing as anonymous personal data -- without a user there is nothing
// to key it on -- so requireUser always demands a session, where requireAuth
// only demands one when the scope says so.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
)

func logLibraryError(err error) {
	log.Printf("library: %v", err)
}

// requireUser gates on a session unconditionally and hands the handler the
// user it belongs to.
func (c *authConfig) requireUser(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !c.Enabled {
			// Auth is not configured, so no request can be attributed to anyone.
			// Saying so plainly beats writing everyone's library into one shared
			// bucket.
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "accounts are not configured on this server",
			})
			return
		}
		sess, ok := c.sessionFrom(r)
		if !ok || sess.User == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "sign-in required",
				"login": "/auth/login",
			})
			return
		}
		next(w, r, sess.User)
	}
}

// decodeBody reads a small JSON body. Bounded, because this is an
// unauthenticated-shaped endpoint from the parser's point of view.
func decodeBody(r *http.Request, into any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(into)
}

func (s *server) handleLibrary(w http.ResponseWriter, r *http.Request, user string) {
	switch r.Method {
	case http.MethodGet:
		items := s.library.list(user)
		// Attach any resume point, so one call renders the whole shelf --
		// a TV making two round trips per screen is a visible stall.
		type row struct {
			libraryItem
			Progress *progress `json:"progress,omitempty"`
		}
		out := make([]row, 0, len(items))
		for _, it := range items {
			r := row{libraryItem: it}
			if p, ok := s.library.progressFor(user, it.Key); ok {
				r.Progress = &p
			}
			out = append(out, r)
		}
		writeJSON(w, 200, map[string]any{"items": out})

	case http.MethodPost:
		var it libraryItem
		if err := decodeBody(r, &it); err != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed body"})
			return
		}
		it.Key = normaliseKey(it.Key)
		if it.Key == "" {
			writeJSON(w, 400, map[string]string{"error": "key is required"})
			return
		}
		s.library.add(user, it)
		writeJSON(w, 200, map[string]any{"ok": true, "key": it.Key})

	case http.MethodDelete:
		key := normaliseKey(r.URL.Query().Get("key"))
		if key == "" {
			writeJSON(w, 400, map[string]string{"error": "key is required"})
			return
		}
		s.library.remove(user, key)
		writeJSON(w, 200, map[string]any{"ok": true})

	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleProgress(w http.ResponseWriter, r *http.Request, user string) {
	switch r.Method {
	case http.MethodGet:
		key := normaliseKey(r.URL.Query().Get("key"))
		if key == "" {
			writeJSON(w, 400, map[string]string{"error": "key is required"})
			return
		}
		p, ok := s.library.progressFor(user, key)
		if !ok {
			// Not an error: most titles have never been started.
			writeJSON(w, 200, map[string]any{"progress": nil})
			return
		}
		writeJSON(w, 200, map[string]any{"progress": p})

	case http.MethodPut, http.MethodPost:
		var p progress
		if err := decodeBody(r, &p); err != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed body"})
			return
		}
		p.Key = normaliseKey(p.Key)
		if p.Key == "" {
			writeJSON(w, 400, map[string]string{"error": "key is required"})
			return
		}
		if p.Position < 0 {
			p.Position = 0
		}
		s.library.setProgress(user, p)
		writeJSON(w, 200, map[string]any{"ok": true, "finished": p.finished()})

	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleContinue is the resume rail: what you started and did not finish, on
// whichever device you started it.
func (s *server) handleContinue(w http.ResponseWriter, r *http.Request, user string) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	writeJSON(w, 200, map[string]any{"items": s.library.continueWatching(user, limit)})
}
