package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SaveOutputTool persists the latest assistant message without forcing the
// model to repeat a large payload inside JSON tool arguments.
type SaveOutputTool struct {
	WorkDir        string
	OutputProvider AssistantOutputProvider
}

type saveOutputInput struct {
	Path string `json:"path"`
	Mode string `json:"mode,omitempty"`
}

func (t *SaveOutputTool) Name() string { return "save_output" }

func (t *SaveOutputTool) Description() string {
	return "Save the text from your most recent assistant message to a file. Use this after drafting a long report, plan, review, or artifact in normal assistant text so you do not have to repeat the entire content inside JSON tool arguments."
}

func (t *SaveOutputTool) ToolMetadata() ToolMetadata {
	return workspaceWriteToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *SaveOutputTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The absolute or relative path to write"
			},
			"mode": {
				"type": "string",
				"enum": ["overwrite", "append"],
				"description": "Write mode. Defaults to overwrite."
			}
		},
		"required": ["path"]
	}`)
}

func (t *SaveOutputTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in saveOutputInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if strings.TrimSpace(in.Path) == "" {
		return ToolResult{Error: "path is required"}, nil
	}
	if t.OutputProvider == nil {
		return ToolResult{Error: "session output is not available"}, nil
	}

	content, ok := t.OutputProvider.LatestAssistantMessageText()
	if !ok {
		return ToolResult{Error: "no assistant message content is available to save"}, nil
	}

	targetPath := resolvePathForBase(in.Path, t.WorkDir)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
	}

	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	modeLabel := "overwrite"
	if strings.EqualFold(strings.TrimSpace(in.Mode), "append") {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		modeLabel = "append"
	}

	f, err := os.OpenFile(targetPath, flags, 0o644)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to open file: %v", err)}, nil
	}
	defer f.Close()

	written, err := f.WriteString(content)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}

	return ToolResult{Output: fmt.Sprintf("Successfully saved %d bytes to %s (%s)", written, targetPath, modeLabel)}, nil
}
