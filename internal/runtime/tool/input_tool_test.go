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

// mockAttachmentProvider implements AttachmentProvider for testing.
type mockAttachmentProvider struct {
	info AttachmentInfo
	ok   bool
}

func (m *mockAttachmentProvider) GetAttachment(id string) (AttachmentInfo, bool) {
	return m.info, m.ok
}

// mockInputManifestProvider implements InputManifestProvider for testing.
type mockInputManifestProvider struct {
	blocks []InputBlockInfo
}

func (m *mockInputManifestProvider) InputManifest() []InputBlockInfo {
	return m.blocks
}

func TestAttachmentGetTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello attachment"), 0o644))
	tool := &AttachmentGetTool{
		Provider: &mockAttachmentProvider{
			info: AttachmentInfo{ID: "att_1", Name: "test.txt", MimeType: "text/plain", Size: int64(len("hello attachment")), Path: path},
			ok:   true,
		},
	}
	input, _ := json.Marshal(map[string]any{"attachment_id": "att_1"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "test.txt")
	assert.Contains(t, result.Output, "hello attachment")
}

func TestAttachmentGetToolBinaryRequiresBase64OptIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagram.png")
	require.NoError(t, os.WriteFile(path, []byte{0x89, 'P', 'N', 'G'}, 0o644))
	tool := &AttachmentGetTool{
		Provider: &mockAttachmentProvider{
			info: AttachmentInfo{ID: "att_1", Name: "diagram.png", MimeType: "image/png", Size: 4, Path: path},
			ok:   true,
		},
	}

	input, _ := json.Marshal(map[string]any{"attachment_id": "att_1"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, `"content_available":false`)

	input, _ = json.Marshal(map[string]any{"attachment_id": "att_1", "include_base64": true})
	result, err = tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, `"encoding":"base64"`)
}

func TestAttachmentGetToolDirectoryListsEntries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "brief.txt"), []byte("hello"), 0o644))
	tool := &AttachmentGetTool{
		Provider: &mockAttachmentProvider{
			info: AttachmentInfo{ID: "att_dir", Name: "reference", MimeType: "application/octet-stream", Path: dir},
			ok:   true,
		},
	}

	input, _ := json.Marshal(map[string]any{"attachment_id": "att_dir"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, `"kind":"directory"`)
	assert.Contains(t, result.Output, `"name":"brief.txt"`)
}

func TestAttachmentGetToolNotFound(t *testing.T) {
	tool := &AttachmentGetTool{Provider: &mockAttachmentProvider{}}
	input, _ := json.Marshal(map[string]any{"attachment_id": "missing"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "not found")
}

func TestInputManifestTool(t *testing.T) {
	tool := &InputManifestTool{
		Provider: &mockInputManifestProvider{
			blocks: []InputBlockInfo{
				{Type: "text", Text: "hello"},
				{Type: "file", Name: "data.csv", Path: "/tmp/data.csv", AttachmentID: "att_data"},
			},
		},
	}
	input, _ := json.Marshal(map[string]any{})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, `"type":"text"`)
	assert.Contains(t, result.Output, `"text":"hello"`)
	assert.Contains(t, result.Output, `"type":"file"`)
	assert.Contains(t, result.Output, `"name":"data.csv"`)
	assert.Contains(t, result.Output, `"attachment_id":"att_data"`)
}

func TestInputManifestToolEmpty(t *testing.T) {
	tool := &InputManifestTool{Provider: &mockInputManifestProvider{}}
	input, _ := json.Marshal(map[string]any{})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "No input blocks")
}
