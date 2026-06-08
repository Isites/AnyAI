package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeDurableRawJSONPreservesLargeObjectShape(t *testing.T) {
	raw := json.RawMessage(`{"path":"README.md","content":"` + strings.Repeat("a", DurableTextPreviewLimit+64) + `","token":"sk-test-secret-value"}`)

	sanitized := SanitizeDurableRawJSON(raw)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(sanitized, &decoded))
	assert.Equal(t, "README.md", decoded["path"])
	assert.Equal(t, "[REDACTED]", decoded["token"])

	content, ok := decoded["content"].(string)
	require.True(t, ok)
	assert.Contains(t, content, "[content omitted from durable transcript;")
	assert.LessOrEqual(t, len(content), DurableTextPreviewLimit+128)
}

func TestSanitizeDurableRawJSONUsesStringPreviewForInvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{"path":` + strings.Repeat("x", DurableTextPreviewLimit+64))

	sanitized := SanitizeDurableRawJSON(raw)

	var decoded string
	require.NoError(t, json.Unmarshal(sanitized, &decoded))
	assert.Contains(t, decoded, "[content omitted from durable transcript;")
}
