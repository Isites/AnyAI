package tools

import (
	"encoding/json"
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
