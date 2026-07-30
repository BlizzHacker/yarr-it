package main

// Sign-in for the web app, which is also the sign-in for every wrapper built
// on it -- Android, Google TV, Fire TV, Tizen and the Windows package all load
// this same site, so gating here gates all of them at once.
//
// Sessions are a signed cookie rather than server-side state. There is nothing
// to lose on restart, nothing to replicate, and a stolen cookie cannot be
// extended: the expiry is inside the signature.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	sessionCookie = "yarrit_session"
	stateCookie   = "yarrit_state"
	sessionTTL    = 30 * 24 * time.Hour
)

// How much of the service the sign-in gate covers.
//
// The TV apps are the surface that has to demonstrate an account behind every
// viewer; a browser is not what gets a channel pulled from a store. Gating
// everything also gates the people we hand the URL to for testing, so the two
// need to be separable.
const (
	scopeTV  = "tv"  // TV clients must sign in; browsers are open. The default.
	scopeAll = "all" // Everything requires a session.
	scopeOff = "off" // Nothing does.
)

// Clients that identify as living-room devices. A TV app names itself with
// ?device=, which is the same parameter that already selects its playback
// profile -- so a client that can play anything at all has told us what it is.
var tvDevices = map[string]bool{
	"roku":      true,
	"androidtv": true,
	"firetv":    true,
	"tizen":     true,
	"tvos":      true,
	"webos":     true,
}

type authConfig struct {
	Issuer       string // https://authentik.moveweight.com/application/o/yarrit-web/
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Secret       []byte // signs session cookies
	Enabled      bool
	Scope        string // scopeTV, scopeAll or scopeOff
}

func loadAuthConfig() *authConfig {
	base := strings.TrimRight(os.Getenv("SSO_BASE"), "/")
	if base == "" {
		base = "https://authentik.moveweight.com"
	}
	slug := os.Getenv("SSO_APP_SLUG")
	if slug == "" {
		slug = "yarrit-web"
	}
	c := &authConfig{
		AuthorizeURL: base + "/application/o/authorize/",
		TokenURL:     base + "/application/o/token/",
		ClientID:     os.Getenv("SSO_CLIENT_ID"),
		ClientSecret: os.Getenv("SSO_CLIENT_SECRET"),
		RedirectURI:  os.Getenv("SSO_REDIRECT_URI"),
		Issuer:       base + "/application/o/" + slug + "/",
		Scope:        strings.ToLower(strings.TrimSpace(os.Getenv("AUTH_SCOPE"))),
	}
	switch c.Scope {
	case scopeTV, scopeAll, scopeOff:
	default:
		// An unset or misspelt scope lands on TV-only rather than on "all".
		// Getting this wrong should lock out the surface that can survive it,
		// not every visitor.
		c.Scope = scopeTV
	}
	if c.RedirectURI == "" {
		c.RedirectURI = "https://yarrit.com/auth/callback"
	}

	if s := os.Getenv("SESSION_SECRET"); s != "" {
		c.Secret = []byte(s)
	} else {
		// A random per-process secret still works -- it just signs everybody
		// out on restart. That is a worse experience but never an insecure
		// one, which is the right way round for a missing setting.
		buf := make([]byte, 32)
		_, _ = rand.Read(buf)
		c.Secret = buf
	}

	// Auth stays off unless it is fully configured. A half-configured gate that
	// rejects everyone is worse than an open site: it looks like an outage and
	// invites someone to disable it entirely.
	c.Enabled = c.ClientID != "" && c.ClientSecret != ""
	return c
}

type session struct {
	User  string `json:"u"`
	Email string `json:"e"`
	Exp   int64  `json:"x"`
}

func (c *authConfig) sign(payload []byte) string {
	mac := hmac.New(sha256.New, c.Secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *authConfig) encode(s session) (string, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	b := base64.RawURLEncoding.EncodeToString(payload)
	return b + "." + c.sign([]byte(b)), nil
}

func (c *authConfig) decode(raw string) (session, bool) {
	var s session
	b, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return s, false
	}
	// Compared with hmac.Equal rather than == so the check does not leak
	// where two signatures first differ.
	if !hmac.Equal([]byte(sig), []byte(c.sign([]byte(b)))) {
		return s, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return s, false
	}
	if err := json.Unmarshal(payload, &s); err != nil {
		return s, false
	}
	if time.Now().Unix() > s.Exp {
		return s, false
	}
	return s, true
}

// sessionFrom reads a valid session off a request, if there is one.
func (c *authConfig) sessionFrom(r *http.Request) (session, bool) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	return c.decode(ck.Value)
}

func (c *authConfig) handleLogin(w http.ResponseWriter, r *http.Request) {
	state := randomToken()
	// The state is kept in a short-lived cookie and compared on return, which
	// is what stops a third party from completing a login into your session.
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/",
		MaxAge: 600, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})

	next := r.URL.Query().Get("next")
	if next == "" || !strings.HasPrefix(next, "/") {
		// Only same-site paths: an absolute URL here is an open redirect.
		next = "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name: "yarrit_next", Value: next, Path: "/",
		MaxAge: 600, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})

	q := url.Values{}
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", c.RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "openid profile email")
	q.Set("state", state)
	http.Redirect(w, r, c.AuthorizeURL+"?"+q.Encode(), http.StatusFound)
}

func (c *authConfig) handleCallback(w http.ResponseWriter, r *http.Request) {
	want, err := r.Cookie(stateCookie)
	if err != nil || want.Value == "" || r.URL.Query().Get("state") != want.Value {
		http.Error(w, "sign-in expired or was tampered with; start again", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "no authorization code returned", http.StatusBadRequest)
		return
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.RedirectURI)
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)

	resp, err := http.PostForm(c.TokenURL, form)
	if err != nil {
		http.Error(w, "could not reach the sign-in service", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "sign-in was refused", http.StatusForbidden)
		return
	}

	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.IDToken == "" {
		http.Error(w, "sign-in response was not understood", http.StatusBadGateway)
		return
	}

	// The id_token comes straight from the token endpoint over TLS with the
	// client secret, so its claims are read without re-verifying a signature
	// we already trust the transport for.
	claims := parseJWTClaims(tok.IDToken)
	user, _ := claims["preferred_username"].(string)
	email, _ := claims["email"].(string)
	if user == "" {
		user, _ = claims["sub"].(string)
	}
	if user == "" {
		http.Error(w, "sign-in did not identify a user", http.StatusForbidden)
		return
	}

	value, err := c.encode(session{User: user, Email: email, Exp: time.Now().Add(sessionTTL).Unix()})
	if err != nil {
		http.Error(w, "could not start a session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode,
	})

	next := "/"
	if ck, err := r.Cookie("yarrit_next"); err == nil && strings.HasPrefix(ck.Value, "/") {
		next = ck.Value
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func (c *authConfig) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleVerify is what Caddy asks before serving anything. 200 means let it
// through; 401 means send them to sign in.
func (c *authConfig) handleVerify(w http.ResponseWriter, r *http.Request) {
	if !c.Enabled {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := c.sessionFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Passed back to the origin so downstream can attribute a request without
	// parsing the cookie again.
	w.Header().Set("X-Yarrit-User", s.User)
	w.Header().Set("X-Yarrit-Email", s.Email)
	w.WriteHeader(http.StatusOK)
}

// handleMe lets the page show who is signed in, and lets a TV confirm its
// token is still good without fetching a whole search.
func (c *authConfig) handleMe(w http.ResponseWriter, r *http.Request) {
	s, ok := c.sessionFrom(r)
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": false})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated": true, "user": s.User, "email": s.Email,
	})
}

// requireAuth wraps a handler so the data behind it needs a session.
// isTVRequest reports whether the caller is one of the living-room apps.
//
// The ?device= parameter is the declaration, but a TV app that omits it would
// silently fall through to the open path -- so Roku, which announces itself in
// its User-Agent, is also recognised there. Anything that fails both checks is
// treated as a browser, which is the safe direction to be wrong in for a gate
// whose purpose is the app stores rather than access control.
func isTVRequest(r *http.Request) bool {
	if tvDevices[strings.ToLower(strings.TrimSpace(r.URL.Query().Get("device")))] {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "roku")
}

func (c *authConfig) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !c.Enabled || c.Scope == scopeOff {
			next(w, r)
			return
		}
		if c.Scope == scopeTV && !isTVRequest(r) {
			next(w, r)
			return
		}
		if _, ok := c.sessionFrom(r); !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "sign-in required",
				"login": "/auth/login",
			})
			return
		}
		next(w, r)
	}
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseJWTClaims reads the payload of a JWT without verifying it. Safe only
// because the caller received it directly from the token endpoint over TLS.
func parseJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}
