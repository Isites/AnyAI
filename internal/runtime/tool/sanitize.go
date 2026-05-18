package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/contract"
)

// SanitizeText redacts obvious credentials and secrets from a free-form string.
func SanitizeText(text string) string {
	return contract.SanitizeText(text)
}

// SanitizeMetadata recursively redacts obvious credentials from metadata maps.
func SanitizeMetadata(metadata map[string]any) map[string]any {
	return contract.SanitizeMetadata(metadata)
}

// SanitizeRawJSON redacts obvious credentials from structured JSON payloads.
func SanitizeRawJSON(raw json.RawMessage) json.RawMessage {
	return contract.SanitizeRawJSON(raw)
}

// SanitizeToolInputForTranscript redacts secrets and summarizes large fields
// that would otherwise bloat future model context. It must not be used for
// actual execution, only for logs, events, tasks, and session history.
func SanitizeToolInputForTranscript(toolName string, raw json.RawMessage) json.RawMessage {
	sanitized := SanitizeRawJSON(raw)
	if strings.TrimSpace(toolName) != "write_file" {
		return sanitized
	}

	var payload map[string]any
	if err := json.Unmarshal(sanitized, &payload); err != nil {
		return sanitized
	}
	if patch, ok := payload["patch"].(string); ok {
		paths := summarizePatchPaths(patch)
		if len(paths) > 0 {
			payload["patch_paths"] = paths
		}
	}
	for _, key := range []string{"content", "patch", "new_string", "old_string"} {
		value, ok := payload[key].(string)
		if !ok {
			continue
		}
		payload[key] = summarizeLargeToolString(value)
		payload[key+"_bytes"] = len(value)
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return sanitized
	}
	return out
}

// SanitizeToolResult redacts obvious credentials from tool outputs, errors,
// and structured metadata before those results flow back into the transcript
// or runtime event stream.
func SanitizeToolResult(result ToolResult) ToolResult {
	if strings.TrimSpace(result.Output) != "" {
		result.Output = string(contract.SanitizeRawJSON(json.RawMessage(result.Output)))
	}
	if strings.TrimSpace(result.Error) != "" {
		result.Error = contract.SanitizeText(result.Error)
	}
	if len(result.Metadata) > 0 {
		result.Metadata = contract.SanitizeMetadata(result.Metadata)
	}
	return result
}

func summarizeLargeToolString(value string) string {
	const previewLimit = 240
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if len(value) <= previewLimit {
		return value
	}
	preview := value[:previewLimit]
	if idx := strings.LastIndex(preview, "\n"); idx > previewLimit/2 {
		preview = preview[:idx]
	}
	return fmt.Sprintf("%s\n[content omitted from transcript; %d bytes total]", preview, len(value))
}
