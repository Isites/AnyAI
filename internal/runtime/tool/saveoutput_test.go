package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticAssistantOutputProvider struct {
	text string
	ok   bool
}

func (p staticAssistantOutputProvider) LatestAssistantMessageText() (string, bool) {
	return p.text, p.ok
}

func TestSaveOutputToolWritesLatestAssistantMessage(t *testing.T) {
	dir := t.TempDir()
	tool := &SaveOutputTool{
		WorkDir:        dir,
		OutputProvider: staticAssistantOutputProvider{text: "# Report\n\nAll checks passed.\n", ok: true},
	}
	input, _ := json.Marshal(saveOutputInput{Path: "artifacts/report.md"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)
	assert.Contains(t, result.Output, "Successfully saved")

	data, err := os.ReadFile(filepath.Join(dir, "artifacts", "report.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Report\n\nAll checks passed.\n", string(data))
}

func TestSaveOutputToolAppendsWhenRequested(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "artifacts", "report.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("prefix\n"), 0o644))

	tool := &SaveOutputTool{
		WorkDir:        dir,
		OutputProvider: staticAssistantOutputProvider{text: "suffix\n", ok: true},
	}
	input, _ := json.Marshal(saveOutputInput{Path: "artifacts/report.md", Mode: "append"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "prefix\nsuffix\n", string(data))
}

func TestSaveOutputToolFailsWithoutAssistantMessage(t *testing.T) {
	tool := &SaveOutputTool{
		WorkDir:        t.TempDir(),
		OutputProvider: staticAssistantOutputProvider{},
	}
	input, _ := json.Marshal(saveOutputInput{Path: "report.md"})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "no assistant message content")
}
