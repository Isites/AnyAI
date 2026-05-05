package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SkillProvider exposes agent-scoped skills to runtime tools.
type SkillProvider interface {
	GetSkill(name string) (SkillDocument, bool)
}

// SkillDocument is the full runtime-facing representation of one skill.
type SkillDocument struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source,omitempty"`
	Content     string   `json:"content"`
}

// SkillGetTool loads the full text of one available skill by name.
type SkillGetTool struct {
	Provider SkillProvider
}

func (t *SkillGetTool) Name() string { return "skill_get" }

func (t *SkillGetTool) Description() string {
	return "Load the full instructions for an available skill by name. Use this after a relevant skill summary appears and you need the complete skill text before acting."
}

func (t *SkillGetTool) ToolMetadata() ToolMetadata {
	return readOnlyToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *SkillGetTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "The skill name to load, using the name shown in the relevant skill summaries"
			}
		},
		"required": ["name"]
	}`)
}

func (t *SkillGetTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if t.Provider == nil {
		return ToolResult{Error: "skill catalog is not available"}, nil
	}

	name := strings.TrimSpace(params.Name)
	if name == "" {
		return ToolResult{Error: "name is required"}, nil
	}

	doc, ok := t.Provider.GetSkill(name)
	if !ok {
		return ToolResult{Error: fmt.Sprintf("skill %q not found", name)}, nil
	}
	return ToolResult{Output: formatSkillDocument(doc)}, nil
}

func formatSkillDocument(doc SkillDocument) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Skill: %s\n", strings.TrimSpace(doc.Name))
	if summary := strings.TrimSpace(doc.Description); summary != "" {
		fmt.Fprintf(&b, "- Summary: %s\n", summary)
	}
	if len(doc.Tags) > 0 {
		fmt.Fprintf(&b, "- Tags: %s\n", strings.Join(doc.Tags, ", "))
	}
	if source := strings.TrimSpace(doc.Source); source != "" {
		fmt.Fprintf(&b, "- Source: %s\n", source)
	}
	body := strings.TrimSpace(doc.Content)
	if body == "" {
		b.WriteString("\nNo additional skill instructions.\n")
		return strings.TrimSpace(b.String())
	}
	b.WriteString("\n")
	b.WriteString(body)
	return strings.TrimSpace(b.String())
}
