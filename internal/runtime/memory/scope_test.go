package memory

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeScopeTrimsValues(t *testing.T) {
	scope := NormalizeScope(SearchScope{AgentID: " lead ", SessionID: " sess-1 "})
	assert.Equal(t, "lead", scope.AgentID)
	assert.Equal(t, "sess-1", scope.SessionID)
}

func TestNormalizeMaxResultsFallsBackToDefault(t *testing.T) {
	assert.Equal(t, DefaultSearchLimit, NormalizeMaxResults(0, 0))
	assert.Equal(t, 9, NormalizeMaxResults(9, DefaultSearchLimit))
}

func TestNormalizeLayersDeduplicatesAndUsesDefaults(t *testing.T) {
	layers := NormalizeLayers([]Layer{" episodic ", "episodic", "", "long_term"})
	assert.Equal(t, []Layer{LayerEpisodic, LayerLongTerm}, layers)
	assert.Equal(t, []Layer{LayerCandidates}, NormalizeLayers(nil, LayerCandidates))
}
