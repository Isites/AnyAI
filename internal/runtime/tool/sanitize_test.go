package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeTextRedactsSecrets(t *testing.T) {
	input := `Authorization: Bearer abcdefghijklmnop
api_key=sk-1234567890abcdefghijkl
token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature`

	output := SanitizeText(input)
	assert.NotContains(t, output, "abcdefghijklmnop")
	assert.NotContains(t, output, "sk-1234567890abcdefghijkl")
	assert.NotContains(t, output, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature")
	assert.Contains(t, output, "[REDACTED]")
}

func TestSanitizeRawJSONRedactsNestedSecrets(t *testing.T) {
	raw := json.RawMessage(`{
		"headers": {
			"Authorization": "Bearer secret-token-value"
		},
		"api_key": "sk-secret-value",
		"query": "safe"
	}`)

	sanitized := SanitizeRawJSON(raw)
	require.JSONEq(t, `{
		"headers": {
			"Authorization": "Bearer [REDACTED]"
		},
		"api_key": "[REDACTED]",
		"query": "safe"
	}`, string(sanitized))
}

func TestSanitizeToolResultRedactsOutputAndMetadata(t *testing.T) {
	result := ToolResult{
		Output: `{"token":"secret-token","status":"ok"}`,
		Error:  "authorization=Bearer topsecret",
		Metadata: map[string]any{
			"api_key": "sk-super-secret",
			"nested": map[string]any{
				"password": "p@ssw0rd",
			},
		},
	}

	sanitized := SanitizeToolResult(result)
	require.JSONEq(t, `{"token":"[REDACTED]","status":"ok"}`, sanitized.Output)
	assert.Equal(t, "authorization=[REDACTED]", sanitized.Error)
	assert.Equal(t, "[REDACTED]", sanitized.Metadata["api_key"])

	nested, ok := sanitized.Metadata["nested"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "[REDACTED]", nested["password"])
}

func TestSanitizeToolInputForTranscriptSummarizesWriteFileContent(t *testing.T) {
	raw := json.RawMessage(`{"path":"report.md","content":"` + strings.Repeat("a", 400) + `"}`)

	sanitized := SanitizeToolInputForTranscript("write_file", raw)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(sanitized, &payload))
	assert.Equal(t, "report.md", payload["path"])
	assert.Equal(t, float64(400), payload["content_bytes"])
	assert.Contains(t, payload["content"], "content omitted")
	assert.Less(t, len(payload["content"].(string)), 320)
}

func TestSanitizeToolInputForTranscriptSummarizesWriteFilePatchPaths(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** Move to: b.txt\n*** End Patch"
	raw, err := json.Marshal(map[string]any{"patch": patch})
	require.NoError(t, err)

	sanitized := SanitizeToolInputForTranscript("write_file", raw)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(sanitized, &payload))
	assert.Equal(t, []any{"a.txt", "b.txt"}, payload["patch_paths"])
	assert.Equal(t, float64(len(patch)), payload["patch_bytes"])
}
