package tools

import (
	"context"
	"encoding/json"
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
	tool := &AttachmentGetTool{
		Provider: &mockAttachmentProvider{
			info: AttachmentInfo{ID: "att_1", Name: "test.txt", MimeType: "text/plain", Size: 100},
			ok:   true,
		},
	}
	input, _ := json.Marshal(map[string]any{"attachment_id": "att_1"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "test.txt")
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
				{Type: "file", Name: "data.csv", Path: "/tmp/data.csv"},
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
}

func TestInputManifestToolEmpty(t *testing.T) {
	tool := &InputManifestTool{Provider: &mockInputManifestProvider{}}
	input, _ := json.Marshal(map[string]any{})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "No input blocks")
}
