package runtime

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/runtime/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceWrapsMemoryManagerOperations(t *testing.T) {
	manager := memory.NewManager(t.TempDir())
	require.NoError(t, manager.Load())
	require.NoError(t, manager.SaveToLayer(memory.LayerLongTerm, "rule", "# Rule\n\nKeep traces visible."))

	service := NewMemoryService(func() *memory.Manager { return manager })

	stats := service.Stats()
	assert.Equal(t, 1, stats.Total)

	entry, ok := service.Get("long-term/rule")
	require.True(t, ok)
	assert.Equal(t, memory.LayerLongTerm, entry.Layer)

	matches := service.Search("visible", 5, memory.LayerLongTerm)
	require.NotEmpty(t, matches)
	assert.Equal(t, "long-term/rule", matches[0].Entry.ID)

	removed, err := service.Cleanup(time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
}

func TestServiceExposesMaintenanceOperations(t *testing.T) {
	manager := memory.NewManager(t.TempDir())
	require.NoError(t, manager.Load())

	now := time.Date(2026, 4, 24, 11, 0, 0, 0, time.UTC)
	require.NoError(t, manager.SaveToLayer(memory.LayerCandidates, "expired", managedServiceDoc("Expired", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "candidate",
		"Expire At":  now.Add(-time.Hour).Format(time.RFC3339),
	}, "Observed", "remove me")))
	require.NoError(t, manager.SaveToLayer(memory.LayerEpisodic, "eligible", managedServiceDoc("Eligible", map[string]string{
		"Managed By":                 "test",
		"Lifecycle":                  "episodic",
		"Recall Count":               "2",
		"Promote After Recall Count": "2",
		"Promotion Status":           "observing",
		"Expire At":                  now.Add(time.Hour).Format(time.RFC3339),
	}, "Summary", "promote me")))
	require.NoError(t, manager.SaveToLayer(memory.LayerLongTerm, "seed", "# Seed\n\nBaseline."))

	seed, ok := manager.Get("long-term/seed")
	require.True(t, ok)
	baseDir := filepath.Dir(filepath.Dir(seed.FilePath))
	externalPath := filepath.Join(baseDir, "long-term", "external.md")
	require.NoError(t, os.WriteFile(externalPath, []byte("# External\n\nLoaded by reindex."), 0o644))

	service := NewMemoryService(func() *memory.Manager { return manager })

	removed, err := service.Cleanup(now)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	reindexed, err := service.Reindex()
	require.NoError(t, err)
	assert.Equal(t, 3, reindexed)

	promoted, err := service.PromoteEligible(now)
	require.NoError(t, err)
	assert.Equal(t, 1, promoted)

	_, ok = service.Get("long-term/external")
	assert.True(t, ok)
	_, ok = service.Get("long-term/eligible")
	assert.True(t, ok)

	stats := service.Stats()
	assert.False(t, stats.LastReindexAt.IsZero())
	assert.Equal(t, now, stats.LastPromotionAt)
	assert.Equal(t, 1, stats.LastPromotionCount)
	assert.Equal(t, 1, stats.LastCleanupRemoved)
}

func managedServiceDoc(title string, meta map[string]string, section string, lines ...string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString("Generated at: ")
	b.WriteString(time.Now().UTC().Format(time.RFC3339))
	b.WriteString("\n\n")

	keys := make([]string, 0, len(meta))
	for key := range meta {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString("- ")
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(meta[key])
		b.WriteString("\n")
	}

	b.WriteString("\n## ")
	b.WriteString(section)
	b.WriteString("\n")
	for _, line := range lines {
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
