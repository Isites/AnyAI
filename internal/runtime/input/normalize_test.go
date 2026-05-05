package input

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeBlocksUsesTextFallback(t *testing.T) {
	blocks, err := NormalizeBlocks("  你好  ", nil)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	assert.Equal(t, InputBlock{Type: "text", Text: "你好"}, blocks[0])
}

func TestNormalizeBlocksRejectsEmptyInput(t *testing.T) {
	_, err := NormalizeBlocks("", nil)
	require.Error(t, err)
}

func TestDefaultSessionIDUsesPrefixAndTimestamp(t *testing.T) {
	sessionID := DefaultSessionID("chat", time.Date(2026, 4, 23, 9, 8, 7, 0, time.UTC))
	assert.Equal(t, "chat_20260423T090807", sessionID)
}
