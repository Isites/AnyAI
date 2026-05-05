package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Isites/anyai/internal/runtime/memory"
)

func TestMemoryProviderAdapterGetUsesDirectIDLookup(t *testing.T) {
	dir := t.TempDir()
	mgr := memory.NewManager(dir)
	require.NoError(t, mgr.Load())
	require.NoError(t, mgr.SaveToLayer(memory.LayerEpisodic, "current-focus", "# Current Focus\n\nRuntime validation is active."))
	require.NoError(t, mgr.SaveToLayer(memory.LayerLongTerm, "runtime-principles", "# Runtime Principles\n\nUse natural language."))

	adapter := &MemoryProviderAdapter{Manager: mgr}

	entry, ok := adapter.Get("episodic/current-focus")
	require.True(t, ok)
	assert.Equal(t, "episodic/current-focus", entry.ID)
	assert.Equal(t, "Current Focus", entry.Title)
	assert.Equal(t, "Runtime validation is active.", entry.Summary)
	assert.Contains(t, entry.Content, "Runtime validation is active.")
	assert.Equal(t, filepath.Join(dir, "episodic", "current-focus.md"), entry.Source)
}

func TestMemoryProviderAdapterSaveCreatesCanonicalEntry(t *testing.T) {
	dir := t.TempDir()
	mgr := memory.NewManager(dir)
	require.NoError(t, mgr.Load())

	adapter := &MemoryProviderAdapter{Manager: mgr}

	saved, err := adapter.Save(MemoryEntry{
		ID:      "delivery-rule",
		Title:   "Delivery Rule",
		Content: "Always confirm the customer address before dispatch.",
		Layer:   MemoryLayer(memory.LayerLongTerm),
	})
	require.NoError(t, err)
	assert.Equal(t, "long-term/delivery-rule", saved.ID)
	assert.Equal(t, "Delivery Rule", saved.Title)
	assert.Contains(t, saved.Content, "# Delivery Rule")
	assert.Equal(t, filepath.Join(dir, "long-term", "delivery-rule.md"), saved.Source)
}

func TestMemoryProviderAdapterRespectsSessionScope(t *testing.T) {
	dir := t.TempDir()
	mgr := memory.NewManager(dir)
	require.NoError(t, mgr.Load())
	require.NoError(t, mgr.SaveToLayer(memory.LayerEpisodic, "current-focus", "# Current Focus\n\nShared note for every session."))
	require.NoError(t, mgr.SaveToLayer(memory.LayerEpisodic, "session-only", managedAdapterDoc("Session Only", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "episodic",
		"Scope":      "session",
		"Agent":      "lead",
		"Session":    "sess-a",
	}, "Outcome", "Only session A should see this.")))

	adapterA := &MemoryProviderAdapter{Manager: mgr, AgentID: "lead", SessionID: "sess-a"}
	adapterB := &MemoryProviderAdapter{Manager: mgr, AgentID: "lead", SessionID: "sess-b"}

	resultsA := adapterA.Search("session current", 5)
	assert.Len(t, resultsA, 2)

	resultsB := adapterB.Search("session current", 5)
	assert.Len(t, resultsB, 1)
	assert.Equal(t, "episodic/current-focus", resultsB[0].ID)

	_, ok := adapterB.Get("episodic/session-only")
	assert.False(t, ok)
}

func TestMemoryProviderAdapterDoesNotReuseAgentCallSessionScopedMemory(t *testing.T) {
	dir := t.TempDir()
	mgr := memory.NewManager(dir)
	require.NoError(t, mgr.Load())
	require.NoError(t, mgr.SaveToLayer(memory.LayerLongTerm, "agentcall-noise", managedAdapterDoc("Agent Call Noise", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "long-term",
		"Scope":      "session",
		"Agent":      "context-analyst",
		"Session":    "agentcall_context-analyst_trace_1",
	}, "Decision", "阶段 3：方案审批，第 1 轮未通过。")))
	require.NoError(t, mgr.SaveToLayer(memory.LayerLongTerm, "agent-rule", "# Agent Rule\n\nAlways keep intermediate artifacts."))

	adapter := &MemoryProviderAdapter{
		Manager:   mgr,
		AgentID:   "context-analyst",
		SessionID: "agentcall_context-analyst_trace_1",
	}

	results := adapter.Search("artifacts stage", 5)
	require.Len(t, results, 1) // Only agent-rule should match the search query

	_, ok := adapter.Get("long-term/agentcall-noise")
	assert.False(t, ok) // Session-scoped noise from child agent runs must not leak back in.
}

func managedAdapterDoc(title string, meta map[string]string, section string, lines ...string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\nGenerated at: 2026-04-19T00:00:00Z\n\n")
	for key, value := range meta {
		b.WriteString("- ")
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(value)
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
