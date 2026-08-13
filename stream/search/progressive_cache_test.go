package main

import "testing"

func TestDegradedProviderResultIsNeverMadeCanonical(t *testing.T) {
	for _, state := range []string{stageFailed, stageUnavailable, stagePending} {
		if cacheableSearchSources(map[string]string{"archive": state, "vimm": stageOK}) {
			t.Errorf("archive state %q was cacheable", state)
		}
	}
	if !cacheableSearchSources(map[string]string{
		"archive": stageOK, "vimm": stageOK, "theromdepot": stageOK,
		"indexers": stageOK,
	}) {
		t.Error("a complete healthy mixed-source answer was not cacheable")
	}
	if !cacheableSearchSources(map[string]string{
		"archive": stageNone, "indexers": stageNotConfigured,
	}) {
		t.Error("sources that do not apply or are intentionally absent should not poison caching")
	}
}
