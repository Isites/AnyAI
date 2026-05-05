package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MemoryProvider is the interface for memory search/get operations.
type MemoryProvider interface {
	Search(query string, maxItems int) []MemoryEntry
	Get(id string) (MemoryEntry, bool)
	Save(entry MemoryEntry) (MemoryEntry, error)
}

// MemoryLayer identifies the logical layer a memory entry belongs to.
type MemoryLayer string

const (
	MemoryLayerCandidates MemoryLayer = "candidates"
	MemoryLayerEpisodic   MemoryLayer = "episodic"
	MemoryLayerLongTerm   MemoryLayer = "long-term"
)

// MemoryEntry represents a memory record.
type MemoryEntry struct {
	ID           string            `json:"id"`
	Title        string            `json:"title,omitempty"`
	Summary      string            `json:"summary,omitempty"`
	Content      string            `json:"content"`
	Layer        MemoryLayer       `json:"layer,omitempty"`
	Source       string            `json:"source,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	MatchedTerms []string          `json:"matched_terms,omitempty"`
}

// MemorySearchTool searches the agent's memory for relevant entries.
type MemorySearchTool struct {
	Memory MemoryProvider
}

func (t *MemorySearchTool) Name() string { return "memory_search" }

func (t *MemorySearchTool) Description() string {
	return "Search the agent's memory for relevant information. Returns matching memory IDs and summaries so you can decide whether to read a full entry with memory_get."
}

func (t *MemorySearchTool) ToolMetadata() ToolMetadata {
	return readOnlyToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *MemorySearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "The search query to find relevant memories"
			},
			"max_items": {
				"type": "integer",
				"description": "Maximum number of results to return (default 5)"
			}
		},
		"required": ["query"]
	}`)
}

func (t *MemorySearchTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Query    string `json:"query"`
		MaxItems int    `json:"max_items"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if t.Memory == nil {
		return ToolResult{Error: "memory is not available"}, nil
	}
	if params.MaxItems <= 0 {
		params.MaxItems = 5
	}

	entries := t.Memory.Search(params.Query, params.MaxItems)
	if len(entries) == 0 {
		return ToolResult{Output: "No matching memories found."}, nil
	}

	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		title := strings.TrimSpace(e.Title)
		if title == "" {
			title = e.ID
		}
		fmt.Fprintf(&sb, "### [%s] %s\n- ID: %s", e.Layer, title, e.ID)
		summary := strings.TrimSpace(e.Summary)
		if summary == "" {
			summary = strings.TrimSpace(e.Content)
		}
		if summary != "" {
			fmt.Fprintf(&sb, "\n- Summary: %s", summary)
		}
		if len(e.MatchedTerms) > 0 {
			fmt.Fprintf(&sb, "\n- Matched Terms: %s", strings.Join(e.MatchedTerms, ", "))
		}
	}
	return ToolResult{Output: sb.String()}, nil
}

// MemoryGetTool retrieves a specific memory entry by ID.
type MemoryGetTool struct {
	Memory MemoryProvider
}

func (t *MemoryGetTool) Name() string { return "memory_get" }

func (t *MemoryGetTool) Description() string {
	return "Retrieve a specific memory entry by its ID. Use this to get the full content of a memory reference found via memory_search."
}

func (t *MemoryGetTool) ToolMetadata() ToolMetadata {
	return readOnlyToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *MemoryGetTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {
				"type": "string",
				"description": "The memory entry ID to retrieve"
			}
		},
		"required": ["id"]
	}`)
}

func (t *MemoryGetTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if t.Memory == nil {
		return ToolResult{Error: "memory is not available"}, nil
	}

	entry, ok := t.Memory.Get(params.ID)
	if !ok {
		return ToolResult{Error: fmt.Sprintf("memory entry %q not found", params.ID)}, nil
	}

	payload, _ := json.Marshal(entry)
	return ToolResult{Output: string(payload)}, nil
}

// MemorySaveTool persists a new memory entry.
type MemorySaveTool struct {
	Memory MemoryProvider
}

func (t *MemorySaveTool) Name() string { return "memory_save" }

func (t *MemorySaveTool) Description() string {
	return "Persist a new memory entry so future runs can recall stable, cross-session facts, confirmed constraints, or important decisions. Do not save temporary workflow chatter or prompt scaffolding; if unsure, ask the user first."
}

func (t *MemorySaveTool) ToolMetadata() ToolMetadata {
	return stateMutationToolMetadata(t.Name(), shortStateMutationTimeoutMS)
}

func (t *MemorySaveTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {
				"type": "string",
				"description": "Optional memory ID. If omitted, the runtime generates one."
			},
			"title": {
				"type": "string",
				"description": "Short title for the memory card"
			},
			"content": {
				"type": "string",
				"description": "The durable fact, decision, or summary to store. This should be stable and useful across future runs, not temporary workflow chatter."
			},
			"layer": {
				"type": "string",
				"enum": ["candidates", "episodic", "long-term"],
				"description": "Storage layer. Defaults to long-term."
			}
		},
		"required": ["content"]
	}`)
}

func (t *MemorySaveTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Content string `json:"content"`
		Layer   string `json:"layer"`
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if t.Memory == nil {
		return ToolResult{Error: "memory is not available"}, nil
	}
	if strings.TrimSpace(params.Content) == "" {
		return ToolResult{Error: "content is required"}, nil
	}
	if strings.TrimSpace(params.ID) == "" {
		params.ID = NewOpaqueID("memory")
	}
	if strings.TrimSpace(params.Title) == "" {
		params.Title = summarizeMemoryTitle(params.Content)
	}
	layer := strings.TrimSpace(params.Layer)
	if layer == "" {
		layer = string(MemoryLayerLongTerm)
	}

	entry, err := t.Memory.Save(MemoryEntry{
		ID:      params.ID,
		Title:   params.Title,
		Content: params.Content,
		Layer:   MemoryLayer(layer),
	})
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}

	payload, _ := json.Marshal(entry)
	return ToolResult{Output: string(payload)}, nil
}

func summarizeMemoryTitle(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "Memory Entry"
	}
	if strings.HasPrefix(content, "# ") {
		line := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(content, "\n", 2)[0], "# "))
		if line != "" {
			return line
		}
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if line == "" {
			continue
		}
		if len([]rune(line)) > 48 {
			return string([]rune(line)[:48])
		}
		return line
	}
	return "Memory Entry"
}
