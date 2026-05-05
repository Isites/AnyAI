package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMemoryProvider implements MemoryProvider for testing.
type mockMemoryProvider struct {
	entries []MemoryEntry
	byID    map[string]MemoryEntry
}

func (m *mockMemoryProvider) Search(query string, maxItems int) []MemoryEntry {
	if maxItems > len(m.entries) {
		maxItems = len(m.entries)
	}
	return m.entries[:maxItems]
}

func (m *mockMemoryProvider) Get(id string) (MemoryEntry, bool) {
	e, ok := m.byID[id]
	return e, ok
}

func (m *mockMemoryProvider) Save(entry MemoryEntry) (MemoryEntry, error) {
	if m.byID == nil {
		m.byID = map[string]MemoryEntry{}
	}
	m.byID[entry.ID] = entry
	m.entries = append(m.entries, entry)
	return entry, nil
}

func TestMemorySearchTool(t *testing.T) {
	mem := &mockMemoryProvider{
		entries: []MemoryEntry{
			{ID: "mem1", Title: "Memory One", Summary: "test content 1", Content: "test content 1", Layer: MemoryLayerEpisodic},
			{ID: "mem2", Title: "Memory Two", Summary: "test content 2", Content: "test content 2", Layer: MemoryLayerLongTerm},
		},
	}
	tool := &MemorySearchTool{Memory: mem}

	input, _ := json.Marshal(map[string]any{"query": "test"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "mem1")
	assert.Contains(t, result.Output, "Memory One")
	assert.Contains(t, result.Output, "Summary:")
	assert.Contains(t, result.Output, "test content")
}

func TestMemorySearchToolNoResults(t *testing.T) {
	tool := &MemorySearchTool{Memory: &mockMemoryProvider{}}
	input, _ := json.Marshal(map[string]any{"query": "nothing"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "No matching memories")
}

func TestMemoryGetTool(t *testing.T) {
	mem := &mockMemoryProvider{
		byID: map[string]MemoryEntry{
			"mem1": {ID: "mem1", Title: "Memory One", Summary: "hello world", Content: "hello world", Layer: MemoryLayerEpisodic},
		},
	}
	tool := &MemoryGetTool{Memory: mem}

	input, _ := json.Marshal(map[string]any{"id": "mem1"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "hello world")
}

func TestMemoryGetToolNotFound(t *testing.T) {
	tool := &MemoryGetTool{Memory: &mockMemoryProvider{}}
	input, _ := json.Marshal(map[string]any{"id": "missing"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "not found")
}

func TestMemorySaveTool(t *testing.T) {
	mem := &mockMemoryProvider{}
	tool := &MemorySaveTool{Memory: mem}

	input, _ := json.Marshal(map[string]any{
		"id":      "release-rule",
		"title":   "Release Rule",
		"content": "Blue-green deployment is mandatory for production release.",
		"layer":   "long-term",
	})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "release-rule")
	assert.Contains(t, result.Output, "Release Rule")

	saved, ok := mem.Get("release-rule")
	require.True(t, ok)
	assert.Equal(t, "Release Rule", saved.Title)
	assert.Equal(t, MemoryLayerLongTerm, saved.Layer)
}

func TestMemorySaveToolRejectsUnknownField(t *testing.T) {
	mem := &mockMemoryProvider{}
	tool := &MemorySaveTool{Memory: mem}

	input, _ := json.Marshal(map[string]any{
		"id":      "legacy",
		"title":   "Legacy",
		"content": "legacy payload",
		"type":    "episodic",
	})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, `unknown field "type"`)
}
