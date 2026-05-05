package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileTool creates or overwrites a file.
type WriteFileTool struct {
	WorkDir string // base directory for resolving relative paths
}

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *WriteFileTool) Name() string { return "write_file" }

func (t *WriteFileTool) Description() string {
	return "Write content to a file at the given path. Creates the file and any parent directories if they don't exist. Overwrites existing files."
}

func (t *WriteFileTool) ToolMetadata() ToolMetadata {
	return workspaceWriteToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *WriteFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The absolute or relative path to the file to write"
			},
			"content": {
				"type": "string",
				"description": "The content to write to the file"
			}
		},
		"required": ["path", "content"]
	}`)
}

func (t *WriteFileTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in writeFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}

	if in.Path == "" {
		return ToolResult{Error: "path is required"}, nil
	}
	targetPath := resolvePathForBase(in.Path, t.WorkDir)

	// Create parent directories
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
	}

	if err := os.WriteFile(targetPath, []byte(in.Content), 0o644); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}

	return ToolResult{Output: fmt.Sprintf("Successfully wrote %d bytes to %s", len(in.Content), targetPath)}, nil
}
