package main

// Plex as a source of television.
//
// The same two objects as the Jellyfin side -- a library that lists what a
// channel could play, and a resolver that turns a scheduled programme into
// playable bytes -- with one extra job that Plex forces on us.
//
// Plex hangs genres, studio and content rating off the *show*, and repeats none
// of it on the episode. So a "Cartoons" library paged for episodes yields 1,337
// rows with no genres at all, and a channel whose rule says "genre is Comedy"
// previews as zero matches and gives no clue why. This file fetches the show
// rows once per refresh and folds their facets down onto the episodes. It is
// one extra request per section, and it is the difference between rules working
// on a TV library and appearing to be broken.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
)

// --- library ----------------------------------------------------------------

// plexLinearLibrary is one Plex section, presented as a source of programmes.
//
// LinearLibraryID is the section *title* -- "Movies", "Cartoons" -- because
// that is what a person types into a channel's sourceLibraries and what the
// "library" rule field compares against. The numeric section key is kept for
// the API calls and never shown.
type plexLinearLibrary struct {
	p       *plexProvider
	section plexDirectory

	warnedTruncated sync.Once
}

func (l *plexLinearLibrary) LinearProviderID() string { return l.p.id }

func (l *plexLinearLibrary) LinearLibraryID() string {
	if t := strings.TrimSpace(l.section.Title); t != "" {
		return t
	}
	return l.section.Key
}

// LinearItems pages the section into schedulable programmes.
//
// Items with no duration are kept rather than dropped, for the same reason as
// on the Jellyfin side: the scheduler skips them and the preview counts them,
// and that count is what explains a channel that came out thinner than the rule
// editor promised. Filtering here would make the explanation wrong.
func (l *plexLinearLibrary) LinearItems(ctx context.Context) ([]LinearItem, error) {
	libID := l.LinearLibraryID()
	isShow := strings.EqualFold(strings.TrimSpace(l.section.Type), "show")

	// The show facets, fetched once for the whole refresh rather than once per
	// episode. Per episode would be 1,337 extra requests to learn 40 answers.
	facets := map[string]plexMetadata{}
	if isShow {
		f, err := l.p.showFacets(ctx, l.section.Key)
		if err != nil {
			// Not fatal. Episodes without genres still schedule; only rules that
			// name a genre go quiet, and saying so beats emptying the channel.
			log.Printf("plex: could not read show details for library %q, "+
				"genre and network rules will not match there: %v", libID, err)
		}
		facets = f
	}

	typeCode := plexItemTypeCode(l.section.Type)
	var out []LinearItem
	total := -1

	for start := 0; ; start += plexPageSize {
		if start >= l.p.poolLimit {
			l.warnedTruncated.Do(func() {
				log.Printf("plex: library %q has %d items, using the first %d for scheduling "+
					"(raise PoolLimit to use more)", libID, total, l.p.poolLimit)
			})
			break
		}
		size := plexPageSize
		if rem := l.p.poolLimit - start; rem < size {
			size = rem
		}
		rows, count, err := l.p.page(ctx, l.section.Key, typeCode, start, size)
		if err != nil {
			if len(out) > 0 {
				// Partway through beats nothing: the engine keeps its last good
				// copy on error, and half a library still fills a channel.
				return out, fmt.Errorf("plex library %q: after %d items: %w", libID, len(out), err)
			}
			return nil, fmt.Errorf("plex library %q: %w", libID, err)
		}
		if count > 0 {
			total = count
		}
		for _, r := range rows {
			out = append(out, l.p.toLinearItem(r, facets[r.GrandparentRatingKey], libID))
		}
		if len(rows) < size {
			break
		}
		if total >= 0 && start+len(rows) >= total {
			break
		}
	}
	return out, nil
}

// --- resolver ---------------------------------------------------------------

type plexLinearResolver struct{ p *plexProvider }

// LinearResolve hands the canonical id and the schedule's offset to Stream.
//
// Its only judgement is error classification: a file Plex no longer has becomes
// ErrLinearMissingMedia, which the engine treats as final. Retrying a deleted
// file three times only makes the viewer wait longer for the same answer.
func (r plexLinearResolver) LinearResolve(ctx context.Context, p Program, opts StreamOptions) (StreamSource, error) {
	if _, ok := parsePlexID(p.CanonicalID); !ok {
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

// newPlexLinear builds everything Live TV needs from one Plex instance: the
// provider to register, one LinearLibrary per video section, and the resolver.
//
// As on the Jellyfin side, the provider and resolver come back even when
// section discovery failed, with the error alongside them. A Plex that is
// mid-restart is still a configured instance and must appear in settings with
// an honest health state rather than disappear.
func newPlexLinear(ctx context.Context, cfg plexConfig) (Provider, []LinearLibrary, LinearResolver, error) {
	p := newPlexProvider(cfg)
	resolver := plexLinearResolver{p: p}

	secs, err := p.sections(ctx)
	if err != nil {
		return p, nil, resolver, fmt.Errorf("could not list %s libraries: %w", p.name, err)
	}

	var libs []LinearLibrary
	for _, s := range secs {
		if !plexVideoSection(s.Type) {
			continue
		}
		libs = append(libs, &plexLinearLibrary{p: p, section: s})
	}
	if len(libs) == 0 {
		return p, nil, resolver, fmt.Errorf("%s has no video libraries to build channels from", p.name)
	}
	return p, libs, resolver, nil
}
