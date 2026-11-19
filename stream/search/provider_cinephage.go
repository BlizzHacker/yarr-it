package main

import (
	"context"
	"os"
	"strings"
)

// Cinephage as a third-party service -- for someone who already runs one.
//
// Yarr.It does not need Cinephage: the built-in engine (engine_*.go) does the
// same job natively. But "works with what you already run" includes the
// all-in-ones, so an existing Cinephage connects like an existing Radarr
// does, through Cinephage's own Radarr/Sonarr-compatible API. Every Radarr
// and Sonarr feature Yarr.It has then works against it unchanged.
//
// Cinephage ships that compatibility layer OFF (Settings -> System ->
// General); Health says so in one line instead of "not a Radarr".

// Cinephage versions itself 0.x; the arr adapters gate on Radarr majors.
const cinephageMinMajor = 0

type cinephageMovies struct{ *radarrProvider }
type cinephageTV struct{ *sonarrProvider }

func (p *cinephageMovies) Health(ctx context.Context) Health {
	return cinephageHealth(p.radarrProvider.Health(ctx))
}

func (p *cinephageTV) Health(ctx context.Context) Health {
	return cinephageHealth(p.sonarrProvider.Health(ctx))
}

func cinephageHealth(h Health) Health {
	switch h.State {
	case HealthIncompatible:
		if h.Version == "" {
			h.Detail = "Cinephage answers, but its Radarr/Sonarr compatibility layer is off — " +
				"turn it on in Cinephage under Settings → System → General"
		}
	case HealthNotConfigured:
		h.Detail = "no API key set for Cinephage — create one under Settings → API Keys in Cinephage"
	case HealthAuthFailed:
		h.Detail = "Cinephage rejected the API key — check it under Settings → API Keys"
	case HealthUnreachable:
		h.Detail = strings.NewReplacer("Radarr", "Cinephage", "Sonarr", "Cinephage").Replace(h.Detail)
	}
	return h
}

// newCinephageProviders builds both personas from one address (Cinephage's
// root, e.g. http://host:3000).
func newCinephageProviders(cfg arrConfig) []Provider {
	root := strings.TrimRight(cfg.BaseURL, "/")
	movies, tv := cfg, cfg
	movies.ID, movies.Name, movies.BaseURL = "cinephage-movies", "Cinephage (films)", root+"/api/radarr"
	tv.ID, tv.Name, tv.BaseURL = "cinephage-tv", "Cinephage (series)", root+"/api/sonarr"
	tv.RootFolder = cfg.SeriesRootFolder

	rp := newRadarrProvider(movies)
	rp.c.minMajor = cinephageMinMajor
	sp := newSonarrProvider(tv)
	sp.c.minMajor = cinephageMinMajor
	return []Provider{&cinephageMovies{rp}, &cinephageTV{sp}}
}

func cinephageProvidersFromEnv() []Provider {
	u := strings.TrimSpace(os.Getenv("CINEPHAGE_URL"))
	if u == "" {
		return nil
	}
	return newCinephageProviders(arrConfig{
		BaseURL:          u,
		APIKey:           os.Getenv("CINEPHAGE_API_KEY"),
		RootFolder:       os.Getenv("CINEPHAGE_MOVIE_ROOT_FOLDER"),
		SeriesRootFolder: os.Getenv("CINEPHAGE_SERIES_ROOT_FOLDER"),
		QualityProfileID: arrEnvInt("CINEPHAGE_QUALITY_PROFILE_ID"),
	})
}
