package main

// Type-ahead.
//
// "Instant search" means something appears while you are still typing, and
// nothing that has to ask a torrent indexer can ever do that. So this endpoint
// asks only sources that answer in memory or in a couple of hundred
// milliseconds: the discover rows, the search cache, and archive.org.
//
// The second job this does is less obvious and matters more. Every keystroke
// warms the archive.org cache for the prefix being typed -- so by the time
// somebody presses Enter, /api/search finds the answer already in memory and
// its first paint carries real results rather than a promise. Type-ahead is
// both the feature and the warmer for the thing it precedes.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	suggestLimit = 10

	// How long a suggestion request may wait on archive.org, and only in the
	// cases where waiting is the difference between a list and a stub of one.
	suggestArchiveBudget = 400 * time.Millisecond

	// Below this many local hits, archive.org is worth waiting for.
	//
	// Not zero. A single catalogue match is enough to suppress the wait, and a
	// dropdown showing one entry for "castlevania" while archive.org holds
	// dozens is worse than one that took an extra two hundred milliseconds --
	// measured on the live site, which is exactly what it did.
	suggestLocalEnough = 4
)

type suggestion struct {
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Poster   string `json:"poster,omitempty"`
	Platform string `json:"platform,omitempty"`
	// Instant marks a suggestion that is itself playable rather than a name to
	// go looking for.
	Instant bool `json:"instant,omitempty"`
	// Where it came from, so the client can say "in your results" against
	// "on archive.org" instead of presenting a guess as a fact.
	Source string `json:"source"`
}

func (s *server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 128 {
		q = q[:128]
	}
	kind := kindFor(r.URL.Query())
	showAdult := r.URL.Query().Get("adult") == "1" || r.URL.Query().Get("adult") == "true"

	// One character matches almost everything, which is noise rather than a
	// suggestion. Answered rather than rejected so the client needs no special
	// case for it.
	if len(q) < 2 {
		writeJSON(w, 200, map[string]any{"q": q, "suggestions": []suggestion{}})
		return
	}

	terms := strings.Fields(strings.ToLower(q))
	out := s.localSuggestions(terms, kind, showAdult)

	// archive.org is asked on every keystroke, but waited for on almost none.
	//
	// The fetch always goes out, because warming it is half the reason this
	// endpoint exists -- the search that follows reads the result out of
	// memory. Waiting for it is a different question, and the answer is "only
	// when there is otherwise little to show". A full list now beats a fuller
	// one in four hundred milliseconds, and by the next keystroke the cache is
	// warm and the archive's answers appear for free.
	wait := len(out) < suggestLocalEnough
	out = append(out, s.archiveSuggestions(r.Context(), q, kind, showAdult, wait)...)

	out = rankSuggestions(out, terms, suggestLimit)
	writeJSON(w, 200, map[string]any{"q": q, "suggestions": out})
}

// localSuggestions answers from memory alone: the discover rows and the search
// cache. This is the part that is genuinely instant, and it is also the answer
// to "make the rows searchable" -- the rows are already in memory, so typing
// three letters finds what is on the landing page without a single request
// leaving the box.
func (s *server) localSuggestions(terms []string, kind string, showAdult bool) []suggestion {
	out := make([]suggestion, 0, 32)

	s.discover.mu.RLock()
	rows := s.discover.rows
	s.discover.mu.RUnlock()
	for _, row := range rows {
		for _, it := range row.Items {
			if !titleHasAll(it.Title, terms) {
				continue
			}
			d := canonicalDomain(it.MediaType)
			if kind != "" && d != "" && d != kind {
				continue
			}
			if !showAdult && looksAdult(it.Title) {
				continue
			}
			out = append(out, suggestion{
				Title: it.Title, Year: it.Year, Kind: d,
				Poster: it.Poster, Instant: it.Play != "", Source: "catalogue",
			})
		}
	}

	for _, c := range s.localCards(strings.Join(terms, " "), kind, 60) {
		if c.Adult && !showAdult {
			continue
		}
		out = append(out, suggestion{
			Title: c.Title, Year: c.Year, Kind: c.Kind, Poster: c.Art.Poster,
			Platform: c.Platform, Instant: c.Instant, Source: "results",
		})
	}
	return out
}

// archiveSuggestions asks archive.org and, when `wait` is set, gives it a
// budget to answer inside.
//
// The request is never abandoned when the budget expires. It keeps running on a
// detached context and lands in the cache, which is what makes the search that
// follows this keystroke fast -- and what makes the *next* keystroke's
// suggestions free.
func (s *server) archiveSuggestions(ctx context.Context, q, kind string,
	showAdult, wait bool) []suggestion {

	if _, ok := scopeFor(kind); !ok {
		return nil
	}

	// Already answered on an earlier keystroke. Read it here rather than
	// racing a goroutine for it, so a warm cache is a certainty and not a
	// scheduling accident.
	//
	// The key has to match whichever path will actually be taken below: music
	// goes through searchArchiveMusicCached, which keeps its results under its
	// own key, and reading the generic one here would miss every warm music
	// answer and re-ask the Archive on every keystroke.
	if cached, ok := s.getCached(archiveSuggestKey(q, kind)); ok {
		return archiveSuggestionsFrom(cached, showAdult)
	}

	done := make(chan []card, 1)
	go func() {
		// Detached deliberately. Tying this to the request context means a
		// keystroke that is superseded 200ms later cancels the very fetch the
		// next keystroke is about to want.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		cards, err := s.searchArchiveDomain(bg, q, kind)
		if err != nil {
			done <- nil
			return
		}
		done <- cards
	}()

	if !wait {
		return nil
	}

	var cards []card
	select {
	case cards = <-done:
	case <-time.After(suggestArchiveBudget):
		return nil
	case <-ctx.Done():
		return nil
	}
	return archiveSuggestionsFrom(cards, showAdult)
}

func archiveSuggestionsFrom(cards []card, showAdult bool) []suggestion {

	out := make([]suggestion, 0, len(cards))
	for _, c := range cards {
		if c.Adult && !showAdult {
			continue
		}
		out = append(out, suggestion{
			Title: c.Title, Year: c.Year, Kind: c.Kind, Poster: c.Art.Poster,
			Platform: c.Platform, Instant: c.Instant, Source: "archive",
		})
	}
	return out
}

// rankSuggestions dedupes by title and puts the likeliest first.
//
// A title that *starts* with what was typed is almost always what was meant --
// "mario" should offer "Mario Bros" before "Dr. Mario" -- so that outranks
// everything else. After that, a suggestion drawn from results already on this
// server beats one that would need a fresh search.
func rankSuggestions(in []suggestion, terms []string, limit int) []suggestion {
	prefix := strings.Join(terms, " ")

	type scored struct {
		s suggestion
		n int
	}
	best := map[string]scored{}
	order := []string{}

	sourceRank := map[string]int{"results": 60, "catalogue": 40, "archive": 20}

	for _, sg := range in {
		title := strings.TrimSpace(sg.Title)
		if title == "" {
			continue
		}
		k := strings.ToLower(title)
		n := sourceRank[sg.Source]
		if strings.HasPrefix(k, prefix) {
			n += 500
		}
		if sg.Instant {
			n += 15
		}
		if sg.Poster != "" {
			n += 5
		}
		// A shorter title containing the same words is the more likely intent:
		// "Zelda" over "Zelda II Randomizer Hack v3".
		n -= len(title) / 8

		if cur, ok := best[k]; ok {
			if n > cur.n {
				best[k] = scored{s: sg, n: n}
			}
			continue
		}
		best[k] = scored{s: sg, n: n}
		order = append(order, k)
	}

	sort.SliceStable(order, func(i, j int) bool {
		return best[order[i]].n > best[order[j]].n
	})

	out := make([]suggestion, 0, limit)
	for _, k := range order {
		if len(out) >= limit {
			break
		}
		out = append(out, best[k].s)
	}
	return out
}
