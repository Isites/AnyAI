package tools

import (
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/memory"
)

// MemoryProviderAdapter adapts memory.Manager to the tools.MemoryProvider interface.
type MemoryProviderAdapter struct {
	Manager   *memory.Manager
	AgentID   string
	SessionID string
}

func (a *MemoryProviderAdapter) scope() memory.SearchScope {
	if a == nil {
		return memory.SearchScope{}
	}
	scope := memory.SearchScope{AgentID: a.AgentID}
	if !isAgentCallSessionID(a.SessionID) {
		scope.SessionID = a.SessionID
	}
	return scope
}

// Search implements tools.MemoryProvider.
func (a *MemoryProviderAdapter) Search(query string, maxItems int) []MemoryEntry {
	if a == nil || a.Manager == nil {
		return nil
	}
	matches := a.Manager.SearchExplainedScoped(query, maxItems, a.scope(), memory.LayerEpisodic, memory.LayerLongTerm, memory.LayerCandidates)
	result := make([]MemoryEntry, len(matches))
	for i, match := range matches {
		e := match.Entry
		result[i] = MemoryEntry{
			ID:           e.ID,
			Title:        e.Title,
			Summary:      memory.PromptSummary(e),
			Content:      e.Content,
			Layer:        MemoryLayer(e.Layer),
			Source:       e.FilePath,
			Metadata:     cloneStringMap(e.Metadata),
			MatchedTerms: append([]string(nil), match.MatchedTerms...),
		}
	}
	return result
}

// Get implements tools.MemoryProvider.
func (a *MemoryProviderAdapter) Get(id string) (MemoryEntry, bool) {
	if a == nil || a.Manager == nil {
		return MemoryEntry{}, false
	}
	entry, ok := a.Manager.GetScoped(id, a.scope())
	if !ok {
		return MemoryEntry{}, false
	}
	return MemoryEntry{
		ID:       entry.ID,
		Title:    entry.Title,
		Summary:  memory.PromptSummary(entry),
		Content:  entry.Content,
		Layer:    MemoryLayer(entry.Layer),
		Source:   entry.FilePath,
		Metadata: cloneStringMap(entry.Metadata),
	}, true
}

// Save persists a new memory entry and returns the canonical stored record.
func (a *MemoryProviderAdapter) Save(entry MemoryEntry) (MemoryEntry, error) {
	if a == nil || a.Manager == nil {
		return MemoryEntry{}, fmt.Errorf("memory is not available")
	}

	layer := memory.LayerLongTerm
	switch strings.TrimSpace(string(entry.Layer)) {
	case string(memory.LayerCandidates):
		layer = memory.LayerCandidates
	case string(memory.LayerEpisodic):
		layer = memory.LayerEpisodic
	case "", string(memory.LayerLongTerm):
		layer = memory.LayerLongTerm
	default:
		return MemoryEntry{}, fmt.Errorf("unsupported memory layer %q", entry.Layer)
	}

	id := strings.TrimSpace(entry.ID)
	if id == "" {
		id = NewOpaqueID("memory")
	}
	id = strings.TrimPrefix(id, string(memory.LayerEpisodic)+"/")
	id = strings.TrimPrefix(id, string(memory.LayerLongTerm)+"/")
	id = strings.TrimPrefix(id, string(memory.LayerCandidates)+"/")
	id = strings.TrimPrefix(id, "long_term/")

	content := strings.TrimSpace(entry.Content)
	if content == "" {
		return MemoryEntry{}, fmt.Errorf("memory content is required")
	}

	title := strings.TrimSpace(entry.Title)
	document := content
	if !strings.HasPrefix(strings.TrimSpace(document), "# ") {
		if title == "" {
			title = "Memory Entry"
		}
		document = "# " + title + "\n\n" + document
	}

	if err := a.Manager.SaveToLayer(layer, id, document); err != nil {
		return MemoryEntry{}, err
	}

	canonicalID := string(layer) + "/" + strings.Trim(strings.TrimSpace(id), "/")
	saved, ok := a.Get(canonicalID)
	if !ok {
		return MemoryEntry{}, fmt.Errorf("memory entry %q was saved but could not be reloaded", canonicalID)
	}
	return saved, nil
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func isAgentCallSessionID(sessionID string) bool {
	sessionID = strings.ToLower(strings.TrimSpace(sessionID))
	return strings.HasPrefix(sessionID, "agentcall_")
}

// FormatForPrompt implements the same function as memory.FormatForPrompt.
func formatMemoryEntries(entries []MemoryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	result := "\n## Relevant Memory\n"
	for _, e := range entries {
		title := e.Title
		if title == "" {
			title = e.ID
		}
		summary := e.Summary
		if summary == "" {
			summary = e.Content
		}
		result += fmt.Sprintf("\n### [%s] %s\n- ID: %s\n- Summary: %s", e.Layer, title, e.ID, summary)
	}
	return result
}
