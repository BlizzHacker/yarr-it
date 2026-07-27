package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// budget tracks how much traffic the relay has carried this calendar month.
//
// The VPS has a finite monthly transfer allowance and every relayed byte is
// billed twice (in from the peer, out to the browser). Without a ceiling a
// single busy evening could exhaust the month and take the mail edge on the
// same box down with it. When the cap is reached the relay stops accepting new
// sockets; clients keep working via web seeds and WebRTC peers, which cost
// nothing.
type budget struct {
	path string
	cap_ int64

	used  atomic.Int64
	mu    sync.Mutex
	month string
}

type budgetState struct {
	Month string `json:"month"`
	Used  int64  `json:"used"`
}

func newBudget(path string, cap_ int64) *budget {
	b := &budget{path: path, cap_: cap_, month: currentMonth()}
	b.load()
	return b
}

func currentMonth() string { return time.Now().UTC().Format("2006-01") }

func (b *budget) load() {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return
	}
	var st budgetState
	if json.Unmarshal(data, &st) != nil {
		return
	}
	if st.Month == currentMonth() {
		b.used.Store(st.Used)
		b.month = st.Month
	}
}

func (b *budget) persist() {
	b.mu.Lock()
	st := budgetState{Month: b.month, Used: b.used.Load()}
	b.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := b.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, b.path)
	}
}

func (b *budget) persistLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		b.rollover()
		b.persist()
	}
}

func (b *budget) rollover() {
	now := currentMonth()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.month != now {
		b.month = now
		b.used.Store(0)
	}
}

func (b *budget) add(n int64) { b.used.Add(n) }

func (b *budget) degraded() bool { return b.used.Load() >= b.cap_ }

func (b *budget) snapshot() (int64, int64) { return b.used.Load(), b.cap_ }

// defaultIPTVBudgetGiB caps continuous IPTV proxying at a quarter of the
// monthly allowance. Torrent relaying is bursty and finite per file; IPTV is
// continuous and unbounded, so a single viewer could otherwise drain the month
// and take the mail edge on this box down with it.
func defaultIPTVBudgetGiB(monthlyGiB int64) int64 { return monthlyGiB / 4 }
