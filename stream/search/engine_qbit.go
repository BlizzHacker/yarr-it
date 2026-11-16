package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// qBittorrent's Web API v2, the download client the engine hands grabs to.
//
// Two traps this client exists to avoid, both learned the hard way on this
// estate:
//
//   - qBittorrent answers 200 with a body of "Ok." OR "Fails.", so a 200 is
//     not success; and for a .torrent URL it says "Ok." immediately and
//     fetches later, so even "Ok." does not prove a torrent exists. A grab
//     with a known infohash is therefore confirmed against the torrent list.
//   - restarting qBittorrent invalidates the session cookie. A 403 triggers
//     one fresh login and one retry rather than a permanent failure.

type qbitTorrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Progress    float64 `json:"progress"`
	State       string  `json:"state"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	Size        int64   `json:"size"`
	Category    string  `json:"category"`
}

func (t qbitTorrent) done() bool {
	if t.Progress >= 1 {
		return true
	}
	switch t.State {
	case "uploading", "stalledUP", "pausedUP", "queuedUP", "forcedUP", "stoppedUP", "checkingUP":
		return true
	}
	return false
}

func (t qbitTorrent) failed() bool { return t.State == "error" || t.State == "missingFiles" }

type qbitClient struct {
	cfg engineClient
	hc  *http.Client
	mu  sync.Mutex
	in  bool
}

func newQbitClient(cfg engineClient, base *http.Client) *qbitClient {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	if base != nil {
		hc.Transport = base.Transport
		if base.Timeout > 0 {
			hc.Timeout = base.Timeout
		}
	}
	return &qbitClient{cfg: cfg, hc: hc}
}

func (c *qbitClient) base() string { return strings.TrimRight(strings.TrimSpace(c.cfg.URL), "/") }

func (c *qbitClient) category() string {
	if c.cfg.Category != "" {
		return c.cfg.Category
	}
	return "yarrit"
}

var errQbitAuth = errors.New("qBittorrent rejected the username or password")

func (c *qbitClient) login(ctx context.Context) error {
	form := url.Values{"username": {c.cfg.Username}, "password": {c.cfg.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/api/v2/auth/login",
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// qBittorrent's CSRF check compares Referer/Origin with its own address.
	req.Header.Set("Referer", c.base())
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach qBittorrent at %s", c.base())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode == 403 {
		return errors.New("qBittorrent has banned this address after failed logins — wait, or unban it in its settings")
	}
	if strings.TrimSpace(string(b)) != "Ok." {
		return errQbitAuth
	}
	c.in = true
	return nil
}

// call performs one request with a single re-login on 403.
func (c *qbitClient) call(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if !c.in {
			if err := c.login(ctx); err != nil {
				return nil, err
			}
		}
		var body io.Reader
		u := c.base() + path
		if method == http.MethodGet && len(form) > 0 {
			u += "?" + form.Encode()
		} else if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Referer", c.base())
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("could not reach qBittorrent at %s", c.base())
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if resp.StatusCode == 403 && attempt == 0 {
			c.in = false // stale session, e.g. qBittorrent restarted
			continue
		}
		if resp.StatusCode >= 300 && resp.StatusCode != 409 {
			return nil, fmt.Errorf("qBittorrent answered HTTP %d on %s", resp.StatusCode, path)
		}
		return b, nil
	}
	return nil, errQbitAuth
}

func (c *qbitClient) version(ctx context.Context) (string, error) {
	b, err := c.call(ctx, http.MethodGet, "/api/v2/app/version", nil)
	return strings.TrimSpace(string(b)), err
}

func (c *qbitClient) add(ctx context.Context, link, savePath string) error {
	// Category first; 409 means it already exists, which is fine.
	_, _ = c.call(ctx, http.MethodPost, "/api/v2/torrents/createCategory",
		url.Values{"category": {c.category()}})
	form := url.Values{"urls": {link}, "category": {c.category()}, "tags": {"yarrit"}}
	if savePath != "" {
		form.Set("savepath", savePath)
	}
	b, err := c.call(ctx, http.MethodPost, "/api/v2/torrents/add", form)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) == "Fails." {
		return errors.New("qBittorrent refused the torrent (often: it is already in the client)")
	}
	return nil
}

func (c *qbitClient) torrents(ctx context.Context) ([]qbitTorrent, error) {
	b, err := c.call(ctx, http.MethodGet, "/api/v2/torrents/info", url.Values{"category": {c.category()}})
	if err != nil {
		return nil, err
	}
	var out []qbitTorrent
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, errors.New("qBittorrent's torrent list was not JSON")
	}
	return out, nil
}
