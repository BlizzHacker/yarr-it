package main

// Bearer tokens, for clients that cannot hold a cookie.
//
// A television signs in with the OAuth device flow: the viewer types a code on
// a phone, and the TV ends up holding an access token. It has no cookie jar and
// no browser to receive a Set-Cookie, so the only way it can prove who it is
// on a later request is to present that token.
//
// Without this the TV completed sign-in successfully and was then refused by
// every API call -- which surfaces as "could not reach the search service",
// because from BrightScript a 401 and a dead network look identical.
//
// Tokens are checked against Authentik's userinfo endpoint, which is the
// authority on whether one is still valid. That is a network round trip, so
// results are cached briefly: a TV polls while someone browses, and asking the
// identity provider to re-approve the same token every few seconds is both slow
// and rude.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// How long a validated token is trusted without re-asking. Short enough
	// that a revoked token stops working promptly, long enough that browsing
	// does not generate a request per keystroke.
	bearerCacheTTL = 5 * time.Minute

	// A token check must not become the reason a search is slow.
	bearerTimeout = 6 * time.Second
)

type bearerEntry struct {
	sess    session
	expires time.Time
}

var (
	bearerMu    sync.RWMutex
	bearerCache = map[string]bearerEntry{}
)

// bearerToken pulls the credential out of an Authorization header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// userinfoURL is where Authentik answers "who is this token".
func (c *authConfig) userinfoURL() string {
	// Issuer is .../application/o/<slug>/ ; userinfo sits beside the other
	// OAuth endpoints rather than under the application.
	base := strings.TrimSuffix(c.Issuer, "/")
	if i := strings.Index(base, "/application/o/"); i >= 0 {
		base = base[:i]
	}
	return base + "/application/o/userinfo/"
}

// sessionFromBearer validates a token and returns who it belongs to.
func (c *authConfig) sessionFromBearer(r *http.Request) (session, bool) {
	tok := bearerToken(r)
	if tok == "" {
		return session{}, false
	}

	bearerMu.RLock()
	if e, ok := bearerCache[tok]; ok && time.Now().Before(e.expires) {
		bearerMu.RUnlock()
		return e.sess, true
	}
	bearerMu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), bearerTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", c.userinfoURL(), nil)
	if err != nil {
		return session{}, false
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return session{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Do not cache a rejection: a token can become valid again after a
		// re-approval, and caching "no" would keep a freshly signed-in TV
		// locked out for the whole TTL.
		return session{}, false
	}

	var claims struct {
		Sub               string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&claims); err != nil {
		return session{}, false
	}

	user := claims.PreferredUsername
	if user == "" {
		user = claims.Email
	}
	if user == "" {
		user = claims.Sub
	}
	if user == "" {
		return session{}, false
	}

	s := session{User: user, Email: claims.Email,
		Exp: time.Now().Add(bearerCacheTTL).Unix()}

	bearerMu.Lock()
	bearerCache[tok] = bearerEntry{sess: s, expires: time.Now().Add(bearerCacheTTL)}
	// The map is only ever written on a cache miss, so a periodic sweep here is
	// enough to stop it growing without bound on a long-running process.
	if len(bearerCache) > 512 {
		now := time.Now()
		for k, v := range bearerCache {
			if now.After(v.expires) {
				delete(bearerCache, k)
			}
		}
	}
	bearerMu.Unlock()

	return s, true
}
