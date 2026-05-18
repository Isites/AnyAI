package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	defaultAttachmentReadLimit = 64 * 1024
	maxAttachmentReadLimit     = 256 * 1024
	maxDirectoryEntries        = 200
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
	Source   string `json:"source,omitempty"`
}

// InputManifestProvider provides the current run's input manifest.
type InputManifestProvider interface {
	InputManifest() []InputBlockInfo
}

// InputBlockInfo holds input block metadata for the manifest tool.
type InputBlockInfo struct {
	ID           string         `json:"id,omitempty"`
	Type         string         `json:"type"`
	Name         string         `json:"name,omitempty"`
	Text         string         `json:"text,omitempty"`
	Path         string         `json:"path,omitempty"`
	AttachmentID string         `json:"attachment_id,omitempty"`
	URL          string         `json:"url,omitempty"`
	MimeType     string         `json:"mime_type,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

// AttachmentGetTool retrieves an attachment by ID.
type AttachmentGetTool struct {
	Provider AttachmentProvider
}

func (t *AttachmentGetTool) Name() string { return "attachment_get" }

func (t *AttachmentGetTool) Description() string {
	return "Retrieve an attachment by its ID. Text files return content, directories return an entry listing, and binary files return metadata with base64 content when requested."
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
			},
			"include_base64": {
				"type": "boolean",
				"description": "Set true to include base64 content for binary attachments"
			},
			"offset": {
				"type": "integer",
				"description": "Byte offset for file content"
			},
			"limit": {
				"type": "integer",
				"description": "Maximum bytes to read"
			}
		},
		"required": ["attachment_id"]
	}`)
}

func (t *AttachmentGetTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		AttachmentID  string `json:"attachment_id"`
		IncludeBase64 bool   `json:"include_base64"`
		Offset        int64  `json:"offset"`
		Limit         int64  `json:"limit"`
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

	payload, err := attachmentPayload(info, params.IncludeBase64, params.Offset, params.Limit)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	return ToolResult{Output: string(payload)}, nil
}

func attachmentPayload(info AttachmentInfo, includeBase64 bool, offset, limit int64) ([]byte, error) {
	payload := map[string]any{
		"id":        info.ID,
		"name":      info.Name,
		"mime_type": info.MimeType,
		"size":      info.Size,
	}
	if strings.TrimSpace(info.Path) != "" {
		payload["path"] = info.Path
	}
	if strings.TrimSpace(info.Source) != "" {
		payload["source"] = info.Source
	}
	path := strings.TrimSpace(info.Path)
	if path == "" {
		return json.Marshal(payload)
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read attachment metadata: %w", err)
	}
	if stat.IsDir() {
		entries, err := directoryEntries(path)
		if err != nil {
			return nil, err
		}
		payload["kind"] = "directory"
		payload["entries"] = entries
		payload["truncated"] = len(entries) >= maxDirectoryEntries
		return json.Marshal(payload)
	}
	data, truncated, err := readAttachmentSlice(path, offset, limit)
	if err != nil {
		return nil, err
	}
	payload["kind"] = "file"
	payload["offset"] = offset
	payload["bytes_read"] = len(data)
	payload["truncated"] = truncated
	if isTextualAttachment(info, data) {
		payload["content"] = string(data)
		payload["encoding"] = "utf-8"
	} else if includeBase64 {
		payload["content_base64"] = base64.StdEncoding.EncodeToString(data)
		payload["encoding"] = "base64"
	} else {
		payload["content_available"] = false
		payload["hint"] = "binary attachment; call attachment_get with include_base64=true if raw bytes are needed"
	}
	return json.Marshal(payload)
}

func readAttachmentSlice(path string, offset, limit int64) ([]byte, bool, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultAttachmentReadLimit
	}
	if limit > maxAttachmentReadLimit {
		limit = maxAttachmentReadLimit
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("read attachment: %w", err)
	}
	defer file.Close()
	if offset > 0 {
		if _, err := file.Seek(offset, 0); err != nil {
			return nil, false, fmt.Errorf("seek attachment: %w", err)
		}
	}
	buf := make([]byte, limit+1)
	n, err := file.Read(buf)
	if err != nil && n == 0 {
		return nil, false, fmt.Errorf("read attachment: %w", err)
	}
	truncated := int64(n) > limit
	if truncated {
		n = int(limit)
	}
	return buf[:n], truncated, nil
}

func directoryEntries(path string) ([]map[string]any, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read directory attachment: %w", err)
	}
	limit := len(entries)
	if limit > maxDirectoryEntries {
		limit = maxDirectoryEntries
	}
	out := make([]map[string]any, 0, limit)
	for _, entry := range entries[:limit] {
		item := map[string]any{
			"name": entry.Name(),
			"type": "file",
		}
		if entry.IsDir() {
			item["type"] = "dir"
		}
		if info, err := entry.Info(); err == nil {
			item["size"] = info.Size()
		}
		out = append(out, item)
	}
	return out, nil
}

func isTextualAttachment(info AttachmentInfo, data []byte) bool {
	mimeType := strings.ToLower(strings.TrimSpace(info.MimeType))
	if strings.HasPrefix(mimeType, "text/") ||
		strings.Contains(mimeType, "json") ||
		strings.Contains(mimeType, "xml") ||
		strings.Contains(mimeType, "yaml") ||
		strings.Contains(mimeType, "toml") ||
		strings.Contains(mimeType, "csv") ||
		strings.Contains(mimeType, "markdown") {
		return utf8.Valid(data)
	}
	switch strings.ToLower(filepath.Ext(info.Name)) {
	case ".txt", ".md", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".csv", ".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".rs", ".java", ".c", ".h", ".cpp", ".hpp", ".html", ".css", ".scss", ".sql", ".sh":
		return utf8.Valid(data)
	default:
		return false
	}
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
