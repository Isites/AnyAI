package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AttachmentProvider loads attachment data from the attachment store.
type AttachmentProvider interface {
	GetAttachment(id string) (AttachmentInfo, bool)
}

// AttachmentInfo holds attachment metadata for tool results.
type AttachmentInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
	Path     string `json:"path"`
}

// InputManifestProvider provides the current run's input manifest.
type InputManifestProvider interface {
	InputManifest() []InputBlockInfo
}

// InputBlockInfo holds input block metadata for the manifest tool.
type InputBlockInfo struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Name     string         `json:"name,omitempty"`
	Text     string         `json:"text,omitempty"`
	Path     string         `json:"path,omitempty"`
	URL      string         `json:"url,omitempty"`
	MimeType string         `json:"mime_type,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

// AttachmentGetTool retrieves an attachment by ID.
type AttachmentGetTool struct {
	Provider AttachmentProvider
}

func (t *AttachmentGetTool) Name() string { return "attachment_get" }

func (t *AttachmentGetTool) Description() string {
	return "Retrieve an attachment's metadata by its ID. Use this to get details about files, images, or other attachments that were included with the input."
}

func (t *AttachmentGetTool) ToolMetadata() ToolMetadata {
	return readOnlyToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *AttachmentGetTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"attachment_id": {
				"type": "string",
				"description": "The attachment ID to retrieve"
			}
		},
		"required": ["attachment_id"]
	}`)
}

func (t *AttachmentGetTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		AttachmentID string `json:"attachment_id"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if t.Provider == nil {
		return ToolResult{Error: "attachment storage is not available"}, nil
	}

	info, ok := t.Provider.GetAttachment(params.AttachmentID)
	if !ok {
		return ToolResult{Error: fmt.Sprintf("attachment %q not found", params.AttachmentID)}, nil
	}

	payload, _ := json.Marshal(info)
	return ToolResult{Output: string(payload)}, nil
}

// InputManifestTool returns the list of input blocks for the current run.
type InputManifestTool struct {
	Provider InputManifestProvider
}

func (t *InputManifestTool) Name() string { return "input_manifest" }

func (t *InputManifestTool) Description() string {
	return "List all input blocks (text, files, images, URLs, etc.) that were included with the current request. Use this to understand the full context of what was sent."
}

func (t *InputManifestTool) ToolMetadata() ToolMetadata {
	return readOnlyToolMetadata(t.Name(), defaultToolTimeoutMS)
}

func (t *InputManifestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {}
	}`)
}

func (t *InputManifestTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.Provider == nil {
		return ToolResult{Output: "No input manifest available."}, nil
	}

	blocks := t.Provider.InputManifest()
	if len(blocks) == 0 {
		return ToolResult{Output: "No input blocks."}, nil
	}
	for i := range blocks {
		if blocks[i].Text != "" && len(blocks[i].Text) > 200 {
			blocks[i].Text = strings.TrimSpace(blocks[i].Text[:200]) + "..."
		}
	}
	payload, _ := json.Marshal(blocks)
	return ToolResult{Output: string(payload)}, nil
}
