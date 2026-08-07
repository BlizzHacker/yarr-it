package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// The canonical media vocabulary, embedded from the one file the web client
// also reads. Two hand-kept lists is exactly how `kind=movies` came to match
// nothing while the same request spelled `groups=movies` worked: the mapping
// existed on one side only. Embedding makes drift a build-time impossibility
// rather than a thing to remember.
//
//go:embed schema.json
var schemaJSON []byte

type mediaDomain struct {
	Label   string   `json:"label"`
	Verb    string   `json:"verb"`
	Types   []string `json:"types"`
	Aliases []string `json:"aliases"`
}

type mediaSchema struct {
	Version        int                    `json:"version"`
	Domains        map[string]mediaDomain `json:"domains"`
	Roles          map[string]string      `json:"roles"`
	Capabilities   []string               `json:"capabilities"`
	Health         []string               `json:"health"`
	ChannelSources []string               `json:"channelSources"`
}

var (
	schema mediaSchema
	// alias -> canonical domain id. Built once; every lookup is a map hit.
	domainByAlias = map[string]string{}
	// type -> canonical domain id, so a caller may name either.
	domainByType = map[string]string{}
)

func init() {
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		// Unrecoverable and always a build-time mistake: the binary carries a
		// malformed vocabulary and nothing downstream can be trusted.
		log.Fatalf("media schema is not valid JSON: %v", err)
	}
	for id, d := range schema.Domains {
		domainByAlias[id] = id
		for _, a := range d.Aliases {
			domainByAlias[normaliseToken(a)] = id
		}
		for _, t := range d.Types {
			domainByType[normaliseToken(t)] = id
		}
	}
}

// normaliseToken folds the spellings that reach an API from a URL, a TV remote
// or a hand-written config: case, spacing and separators.
func normaliseToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", "_", "", " ", "").Replace(s)
	return s
}

// canonicalDomain resolves any name a caller might use onto a domain id.
//
// Returns "" for anything unrecognised, which callers must treat as "no
// filter" rather than "match nothing". A filter nothing can satisfy is
// indistinguishable from a broken server; showing everything is at least a
// visible, arguable answer.
func canonicalDomain(s string) string {
	if s == "" {
		return ""
	}
	t := normaliseToken(s)
	if id, ok := domainByAlias[t]; ok {
		return id
	}
	return domainByType[t] // "" when unknown
}

// sameDomain compares two names through the canonical form.
//
// Both sides are normalised on purpose. A cached card may still carry "audio"
// while a new client sends "music"; comparing raw strings would silently drop
// every one of those cards, which is the original bug wearing a different hat.
func sameDomain(a, b string) bool {
	ca, cb := canonicalDomain(a), canonicalDomain(b)
	if ca == "" || cb == "" {
		return false
	}
	return ca == cb
}

// handleSchema serves the vocabulary to clients that cannot embed it at build
// time -- Roku, Tizen, anything on a TV. Those clients are the ones that were
// bitten last time, precisely because their copy of the mapping was written by
// hand and then drifted.
func (s *server) handleSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Safe to cache: the vocabulary changes with a deploy, not with a request.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(schemaJSON)
}
