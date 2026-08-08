package main

// Jellyfin as a source of television.
//
// Two small objects connect a finished scheduling engine to a real library:
//
//   - jellyfinLinearLibrary answers "what could this channel play", by paging a
//     Jellyfin library into LinearItems that carry a duration and the facets
//     the rule editor selects on.
//   - jellyfinLinearResolver answers "the schedule says this programme is 37
//     minutes in; give me something that plays from there", by handing the
//     canonical id and the offset to the provider's Stream.
//
// The resolver is deliberately thin. Every decision about which endpoint can
// honour an offset, and whether the result is really direct play and really
// seekable, lives in provider_jellyfin.go where it can be tested against a
// server. Duplicating any of it here is how the two would drift.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
)

// --- library ----------------------------------------------------------------

// jellyfinLinearLibrary is one Jellyfin library, presented as a source of
// programmes.
//
// LinearLibraryID is the library's *name*, not its GUID. That is what a person
// types into a channel's sourceLibraries and what the "library" rule field
// compares against; a GUID would be technically tidier and unusable by hand.
type jellyfinLinearLibrary struct {
	p    *jellyfinProvider
	view jellyfinVirtualFolder

	// itemTypes is what this library contributes. A TV library contributes
	// episodes, not series: a series has no runtime and cannot be given a slot,
	// and scheduling one would silently drop the whole library on the floor at
	// the schedulable() guard.
	itemTypes []string

	// warnedTruncated keeps the "this library is larger than the cap" notice to
	// once per process. The pool is refetched every few minutes, and a log line
	// every time would bury everything else.
	warnedTruncated sync.Once
}

func (l *jellyfinLinearLibrary) LinearProviderID() string { return l.p.id }

func (l *jellyfinLinearLibrary) LinearLibraryID() string {
	if n := strings.TrimSpace(l.view.Name); n != "" {
		return n
	}
	return l.view.ItemID
}

// LinearItems pages the library into schedulable programmes.
//
// It pages rather than asking for everything at once because a real library
// here holds ~35,650 items: one request for all of them makes Jellyfin build
// the whole response in memory before it writes a byte, and a guide request
// that waits for that has already timed out.
//
// Items with no runtime are kept rather than dropped. The scheduler's own
// schedulable() guard skips them, and the preview counts them as "matched but
// not schedulable" -- which is the message that explains a thin channel.
// Filtering them here would make that number wrong.
func (l *jellyfinLinearLibrary) LinearItems(ctx context.Context) ([]LinearItem, error) {
	libID := l.LinearLibraryID()
	var out []LinearItem
	total := -1

	for start := 0; ; start += jellyfinPageSize {
		if start >= l.p.poolLimit {
			l.warnedTruncated.Do(func() {
				log.Printf("jellyfin: library %q has %d items, using the first %d for scheduling "+
					"(raise PoolLimit to use more)", libID, total, l.p.poolLimit)
			})
			break
		}
		limit := jellyfinPageSize
		if rem := l.p.poolLimit - start; rem < limit {
			limit = rem
		}
		items, count, err := l.p.page(ctx, l.view.ItemID, l.itemTypes, start, limit)
		if err != nil {
			if len(out) > 0 {
				// Partway through is a real answer, and a better one than
				// nothing: the engine keeps its last good copy on error, and a
				// half library beats blanking a channel that was working.
				return out, fmt.Errorf("jellyfin library %q: after %d items: %w", libID, len(out), err)
			}
			return nil, fmt.Errorf("jellyfin library %q: %w", libID, err)
		}
		if count > 0 {
			total = count
		}
		for _, it := range items {
			if it.IsFolder {
				continue
			}
			out = append(out, l.p.toLinearItem(it, libID))
		}
		if len(items) < limit {
			break
		}
		if total >= 0 && start+len(items) >= total {
			break
		}
	}
	return out, nil
}

// --- resolver ---------------------------------------------------------------

// jellyfinLinearResolver turns a scheduled programme into playable bytes.
type jellyfinLinearResolver struct{ p *jellyfinProvider }

// LinearResolve hands the canonical id and the schedule's offset straight to
// Stream.
//
// The one piece of judgement it adds is error classification. A file that has
// gone becomes ErrLinearMissingMedia, which the engine treats as final and does
// not retry -- retrying a deleted file three times only makes the viewer wait
// longer for the same answer, and the guide still needs to render.
func (r jellyfinLinearResolver) LinearResolve(ctx context.Context, p Program, opts StreamOptions) (StreamSource, error) {
	if _, ok := parseJellyfinID(p.CanonicalID); !ok {
		return StreamSource{}, fmt.Errorf("%w: %q was not scheduled from %s",
			ErrLinearMissingMedia, p.CanonicalID, r.p.name)
	}
	src, err := r.p.Stream(ctx, p.CanonicalID, opts)
	if err != nil {
		if errors.Is(err, errMediaItemGone) {
			return StreamSource{}, fmt.Errorf("%w: %s no longer has %q",
				ErrLinearMissingMedia, r.p.name, p.Title)
		}
		return StreamSource{}, err
	}
	if src.URL == "" {
		return StreamSource{}, fmt.Errorf("%w: %s returned no playable URL for %q",
			ErrLinearMissingMedia, r.p.name, p.Title)
	}
	return src, nil
}

// --- construction -----------------------------------------------------------

// newJellyfinLinear builds everything Live TV needs from one Jellyfin instance:
// the provider to register, one LinearLibrary per video library, and the
// resolver that turns a scheduled programme into bytes.
//
// The provider and resolver are returned even when library discovery fails, and
// the error is returned alongside them. That is not sloppiness: a Jellyfin that
// is mid-restart or busy scanning cannot list its libraries but is still a real
// configured instance, and it must appear in settings with an honest health
// state rather than vanish. The caller registers what it got and logs the error.
func newJellyfinLinear(ctx context.Context, cfg jellyfinConfig) (Provider, []LinearLibrary, LinearResolver, error) {
	p := newJellyfinProvider(cfg)
	resolver := jellyfinLinearResolver{p: p}

	views, err := p.views(ctx)
	if err != nil {
		return p, nil, resolver, fmt.Errorf("could not list %s libraries: %w", p.name, err)
	}

	var libs []LinearLibrary
	for _, v := range views {
		if !jellyfinVideoCollection(v.CollectionType) {
			continue
		}
		libs = append(libs, &jellyfinLinearLibrary{
			p:         p,
			view:      v,
			itemTypes: jellyfinScheduleTypes(v.CollectionType),
		})
	}
	if len(libs) == 0 {
		return p, nil, resolver, fmt.Errorf("%s has no video libraries to build channels from", p.name)
	}
	return p, libs, resolver, nil
}

// jellyfinScheduleTypes decides what a library contributes.
//
// A "tvshows" library contributes episodes and nothing else. Contributing the
// Series rows too would put items with no runtime into the pool, which the
// scheduler skips and the preview then reports as hundreds of unschedulable
// matches -- technically true and completely baffling to whoever built the
// channel.
func jellyfinScheduleTypes(collectionType string) []string {
	switch strings.ToLower(strings.TrimSpace(collectionType)) {
	case "tvshows":
		return []string{"Episode"}
	case "movies":
		return []string{"Movie"}
	case "musicvideos":
		return []string{"MusicVideo"}
	case "homevideos":
		return []string{"Video", "Movie"}
	}
	// A mixed or unlabelled library: take everything with a runtime.
	return []string{"Movie", "Episode", "Video", "MusicVideo"}
}

// --- resolving across more than one server ----------------------------------

// linearResolverChain lets one engine serve channels built from several media
// servers.
//
// The engine holds exactly one resolver, which is right -- a schedule resolves
// through one door -- but an installation with both a Jellyfin and a Plex needs
// that door to know which server issued an id. Each resolver already rejects
// ids it did not issue, so the chain is just "ask each in turn".
//
// Missing is only reported when every member says missing. Anything else would
// let the first server's "I have never heard of this" cancel the second
// server's perfectly good answer.
type linearResolverChain struct{ resolvers []LinearResolver }

func newLinearResolverChain(rs ...LinearResolver) LinearResolver {
	var kept []LinearResolver
	for _, r := range rs {
		if r != nil {
			kept = append(kept, r)
		}
	}
	if len(kept) == 1 {
		return kept[0]
	}
	return linearResolverChain{resolvers: kept}
}

func (c linearResolverChain) LinearResolve(ctx context.Context, p Program, opts StreamOptions) (StreamSource, error) {
	if len(c.resolvers) == 0 {
		return StreamSource{}, ErrLinearNoResolver
	}
	// The first error that was *not* "I do not have this". It is the one worth
	// reporting: a server that is merely down is the reason we cannot answer,
	// and returning a missing-media error instead would tell the engine to stop
	// retrying something that a retry would fix.
	var firstReal error
	for _, r := range c.resolvers {
		src, err := r.LinearResolve(ctx, p, opts)
		if err == nil {
			return src, nil
		}
		if !errors.Is(err, ErrLinearMissingMedia) && firstReal == nil {
			firstReal = err
		}
	}
	if firstReal != nil {
		return StreamSource{}, firstReal
	}
	// Every configured server agrees it does not have this. Final, so the
	// engine stops retrying.
	return StreamSource{}, fmt.Errorf("%w: no configured media server has %q",
		ErrLinearMissingMedia, p.CanonicalID)
}
