package main

// Live position: where a channel is *right now*.
//
// The whole of linear television is one subtraction -- `now - programme.start`
// -- and the reason it gets a file of its own is that everything around that
// subtraction is where the feature is won or lost. Tune in at 20:37 to
// something that began at 20:00 and playback starts 37 minutes in, because the
// schedule said so and not because of anything this viewer did before. Two
// people tuning in at the same second land on the same frame. When a programme
// ends the channel moves to the next one on its own. When the file behind a
// programme has gone, the channel says so and the guide stays up.
//
// Nothing in this file consults per-viewer state. There is no resume point on
// a channel; that is what makes it a channel.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// LinearResolver turns a scheduled programme into something playable. It takes
// a Program rather than a library item so a resolver needs nothing but the
// canonical id -- the schedule is the contract between them.
type LinearResolver interface {
	LinearResolve(ctx context.Context, p Program, opts StreamOptions) (StreamSource, error)
}

var (
	// ErrLinearMissingMedia means the file is gone. Retrying will not bring it
	// back, so it short-circuits the retry loop rather than making the viewer
	// wait out three attempts for an answer that is already known.
	ErrLinearMissingMedia = errors.New("the media for this programme is missing")
	// ErrLinearSourceUnavailable means the source failed and kept failing.
	ErrLinearSourceUnavailable = errors.New("the source for this programme is unavailable")
	// ErrLinearNoResolver means nothing was configured to serve bytes.
	ErrLinearNoResolver = errors.New("no stream resolver is configured for linear channels")
	// ErrLinearSourceWarming means a source has not finished loading its
	// catalogue yet. It is not a failure and it is not "nothing matched": it
	// resolves itself in minutes without anybody doing anything.
	//
	// It exists so the engine can tell those three apart generically. A source
	// that is warming must not be logged on every request -- that is a line per
	// viewer per channel for as long as it takes, which on a channel meant to be
	// the first thing on the site is a lot of lines saying the same thing -- and
	// must not be described to the operator as a rule set that matches nothing,
	// which sends them to fix a channel that was about to work.
	ErrLinearSourceWarming = errors.New("this source is still loading its catalogue")
)

// Channel states, reported rather than thrown. Each is a thing a client can
// draw; none of them is a failed request.
const (
	LinearStateOnAir    = "on-air"
	LinearStateOffAir   = "off-air"  // a gap in the schedule, e.g. a slot grid
	LinearStateEmpty    = "empty"    // nothing matches this channel's rules
	LinearStateDisabled = "disabled" // switched off by its owner
	LinearStateUnknown  = "unknown"  // no such channel
)

// LinearNowPlaying is the answer to "what is on, and where is it up to".
type LinearNowPlaying struct {
	Channel Channel  `json:"channel"`
	Program *Program `json:"program,omitempty"`
	Next    *Program `json:"next,omitempty"`

	// OffsetSeconds is how far into the programme the channel is: the number a
	// player seeks to. It is derived from the clock and the schedule, never
	// from where anybody left off.
	OffsetSeconds    float64 `json:"offsetSeconds"`
	RemainingSeconds float64 `json:"remainingSeconds"`

	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
	ServerNow int64  `json:"serverNow"`

	// Rendered in the viewer's zone when one is asked for; the numbers above
	// stay UTC seconds so no client has to guess.
	StartLocal string `json:"startLocal,omitempty"`
	EndLocal   string `json:"endLocal,omitempty"`
}

// LinearTuneIn is NowPlaying plus a playable source. Separate type because the
// guide must be answerable without ever touching a resolver.
type LinearTuneIn struct {
	LinearNowPlaying
	Source    *StreamSource `json:"source,omitempty"`
	Available bool          `json:"available"`
}

// linearIndexAt finds the programme covering `now`.
//
// Programmes are sorted and non-overlapping, so this is a binary search for
// the last one that had started, then one check that it has not finished. End
// is exclusive: at exactly the moment a programme ends, the channel is already
// on the next one, which is what makes rollover a consequence of the data
// rather than a timer someone has to remember to fire.
func linearIndexAt(programs []Program, now int64) (int, bool) {
	if len(programs) == 0 {
		return -1, false
	}
	i := sort.Search(len(programs), func(k int) bool { return programs[k].StartTime > now })
	if i == 0 {
		return -1, false // now is before the schedule begins
	}
	i--
	if programs[i].EndTime > now {
		return i, true
	}
	return i, false
}

// linearLiveOffset is the subtraction, written once. Clamped at zero because a
// negative seek is not a thing a player can do, and clamped below the
// programme's length because the alternative is asking for a frame past the
// end of the file.
func linearLiveOffset(p Program, now int64) time.Duration {
	off := now - p.StartTime
	if off < 0 {
		off = 0
	}
	if dur := p.EndTime - p.StartTime; dur > 0 && off >= dur {
		off = dur - 1
	}
	return time.Duration(off) * time.Second
}

// NowPlaying answers for one channel at the engine's current instant.
func (e *LinearEngine) NowPlaying(ctx context.Context, channelID, tz string) LinearNowPlaying {
	return e.nowPlayingAt(ctx, channelID, e.now(), tz)
}

func (e *LinearEngine) nowPlayingAt(ctx context.Context, channelID string, at time.Time, tz string) LinearNowPlaying {
	out := LinearNowPlaying{State: LinearStateUnknown, ServerNow: at.Unix()}

	def, ok := e.Channel(channelID)
	if !ok {
		out.Detail = fmt.Sprintf("no channel %q", channelID)
		return out
	}
	out.Channel = def.toChannel(linearProviderID)
	if !def.Enabled {
		out.State = LinearStateDisabled
		out.Detail = "this channel is switched off"
		return out
	}

	st, err := e.ensureWindow(ctx, channelID)
	if err != nil || st == nil {
		out.State = LinearStateEmpty
		out.Detail = "this channel has no schedule yet"
		if err != nil {
			out.Detail = err.Error()
		}
		return out
	}
	if len(st.Programs) == 0 {
		out.State = LinearStateEmpty
		out.Detail = st.Detail
		if out.Detail == "" {
			out.Detail = "this channel has nothing to play"
		}
		return out
	}

	now := at.Unix()
	i, live := linearIndexAt(st.Programs, now)
	if live {
		p := st.Programs[i]
		out.Program = &p
		out.State = LinearStateOnAir
		off := linearLiveOffset(p, now)
		out.OffsetSeconds = off.Seconds()
		out.RemainingSeconds = float64(p.EndTime - now)
		if i+1 < len(st.Programs) {
			n := st.Programs[i+1]
			out.Next = &n
		}
		if tz != "" {
			loc := linearLocation(tz)
			out.StartLocal = time.Unix(p.StartTime, 0).In(loc).Format(time.RFC3339)
			out.EndLocal = time.Unix(p.EndTime, 0).In(loc).Format(time.RFC3339)
		}
		return out
	}

	// In a gap, or past the end of what has been generated. Both are states a
	// client can draw -- "back at 21:00" -- rather than errors.
	out.State = LinearStateOffAir
	for k := range st.Programs {
		if st.Programs[k].StartTime > now {
			n := st.Programs[k]
			out.Next = &n
			out.Detail = fmt.Sprintf("off air; %s starts in %ds", n.Title, n.StartTime-now)
			break
		}
	}
	if out.Next == nil {
		out.Detail = "off air; the schedule has not been generated this far ahead yet"
	}
	return out
}

// --- tuning in --------------------------------------------------------------

// TuneIn resolves a playable source for whatever is on, at the offset the
// schedule dictates.
//
// A source that fails is retried, because the common failure is a media server
// that is a moment behind a restart, not a file that has gone. What it must
// never do is fail in a way that takes the channel list or the guide with it:
// the caller gets a described, drawable "this one is unavailable" and can
// change channel.
func (e *LinearEngine) TuneIn(ctx context.Context, channelID, tz string) LinearTuneIn {
	np := e.NowPlaying(ctx, channelID, tz)
	out := LinearTuneIn{LinearNowPlaying: np}
	if np.State != LinearStateOnAir || np.Program == nil {
		return out
	}

	e.mu.RLock()
	res := e.resolver
	attempts, backoff := e.retryAttempts, e.retryBackoff
	e.mu.RUnlock()

	if res == nil {
		out.Detail = ErrLinearNoResolver.Error()
		return out
	}
	src, err := linearResolveWithRetry(ctx, res, *np.Program,
		StreamOptions{OffsetSeconds: np.OffsetSeconds}, attempts, backoff)
	if err != nil {
		// The programme, its times and the offset all stay in the response.
		// A client that cannot play this one can still draw the guide row, see
		// when the next programme starts and move on.
		out.Detail = linearUnavailableDetail(*np.Program, np.Next, err)
		return out
	}
	out.Source = &src
	out.Available = true
	return out
}

// linearUnavailableDetail is shown to a viewer verbatim, so it says when the
// next programme starts in the way a person reads a clock rather than as the
// epoch seconds the schedule happens to store. A viewer was being told a
// programme was "next at 1786236610".
func linearUnavailableDetail(p Program, next *Program, err error) string {
	base := fmt.Sprintf("%q cannot be played right now: %v", p.Title, err)
	if next == nil {
		return base
	}
	// Relative, because the viewer's clock and this server's timezone are not
	// the same clock, and "in 4 minutes" needs neither to be right.
	wait := time.Until(time.Unix(next.StartTime, 0)).Round(time.Minute)
	switch {
	case wait <= 0:
		return base + fmt.Sprintf("; %q is starting now", next.Title)
	case wait < time.Minute:
		return base + fmt.Sprintf("; %q starts in under a minute", next.Title)
	case wait < 2*time.Minute:
		return base + fmt.Sprintf("; %q starts in a minute", next.Title)
	default:
		return base + fmt.Sprintf("; %q starts in %d minutes", next.Title, int(wait.Minutes()))
	}
}

// linearResolveWithRetry retries transient failures with a linear backoff and
// gives up immediately on a missing file.
func linearResolveWithRetry(ctx context.Context, r LinearResolver, p Program, opts StreamOptions, attempts int, backoff time.Duration) (StreamSource, error) {
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		src, err := r.LinearResolve(ctx, p, opts)
		if err == nil {
			return src, nil
		}
		last = err
		if errors.Is(err, ErrLinearMissingMedia) {
			// Nothing to wait for. Two more attempts would only make the
			// viewer stare at a spinner before the same answer.
			return StreamSource{}, err
		}
		if i == attempts-1 {
			break
		}
		if backoff > 0 {
			t := time.NewTimer(backoff * time.Duration(i+1))
			select {
			case <-ctx.Done():
				t.Stop()
				return StreamSource{}, ctx.Err()
			case <-t.C:
			}
		}
	}
	return StreamSource{}, fmt.Errorf("%w after %d attempts: %v", ErrLinearSourceUnavailable, attempts, last)
}

// --- HTTP -------------------------------------------------------------------

func (e *LinearEngine) handleNow(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("channel"))
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "channel is required"})
		return
	}
	// Checked before anything is computed, and the refusal is byte-identical to
	// the one for an id that does not exist. Guessing a private channel's id
	// must teach a stranger nothing -- not its name, not that it is there.
	if _, ok := e.visibleChannel(r, id); !ok {
		writeJSON(w, 404, LinearNowPlaying{
			State:     LinearStateUnknown,
			Detail:    fmt.Sprintf("no channel %q", id),
			ServerNow: e.now().Unix(),
		})
		return
	}
	np := e.NowPlaying(r.Context(), id, strings.TrimSpace(r.URL.Query().Get("tz")))
	if np.State == LinearStateUnknown {
		writeJSON(w, 404, np)
		return
	}
	// Every other state is a 200 carrying a state field. An empty channel is
	// not a server error and must not be drawn as one.
	writeJSON(w, 200, np)
}

func (e *LinearEngine) handleStream(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("channel"))
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "channel is required"})
		return
	}
	// The route that hands out bytes, so the check comes first and is the same
	// 404 a nonexistent channel gets. Nothing about a private channel -- not
	// its programme, not its artwork, not a signed URL -- is computed for a
	// caller who may not have it.
	if _, ok := e.visibleChannel(r, id); !ok {
		writeJSON(w, 404, LinearTuneIn{LinearNowPlaying: LinearNowPlaying{
			State:     LinearStateUnknown,
			Detail:    fmt.Sprintf("no channel %q", id),
			ServerNow: e.now().Unix(),
		}})
		return
	}
	t := e.TuneIn(r.Context(), id, strings.TrimSpace(r.URL.Query().Get("tz")))
	switch {
	case t.State == LinearStateUnknown:
		writeJSON(w, 404, t)
	case t.Available:
		writeJSON(w, 200, t)
	default:
		// 503 with the whole picture attached: what should be on, where it is
		// up to, and when the next thing starts. The client has enough to
		// retry, to wait, or to change channel.
		writeJSON(w, http.StatusServiceUnavailable, t)
	}
}
