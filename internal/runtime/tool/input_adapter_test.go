package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Isites/anyai/internal/runtime/input"
)

func TestRunInputAdapterBuildsManifestAndAttachments(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "spec.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello spec"), 0o644))

	adapter := NewRunInputAdapter(input.InputEnvelope{
		Blocks: []input.InputBlock{
			{Type: "text", Text: "please inspect"},
			{Type: "file", Path: filePath, Name: "spec.txt"},
			{Type: "url", URL: "https://example.com"},
		},
	})

	manifest := adapter.InputManifest()
	require.Len(t, manifest, 3)
	assert.Equal(t, "text", manifest[0].Type)
	assert.Equal(t, "please inspect", manifest[0].Text)
	assert.Equal(t, "file", manifest[1].Type)
	assert.Equal(t, "spec.txt", manifest[1].Name)
	assert.NotEmpty(t, manifest[1].ID)
	assert.Equal(t, "url", manifest[2].Type)

	attachment, ok := adapter.GetAttachment(manifest[1].ID)
	require.True(t, ok)
	assert.Equal(t, "spec.txt", attachment.Name)
	assert.Equal(t, filePath, attachment.Path)
	assert.Equal(t, int64(len("hello spec")), attachment.Size)
}

func TestResolveEnvelopeForRuntimeBuildsTextAndImages(t *testing.T) {
	env := input.InputEnvelope{
		Blocks: []input.InputBlock{
			{Type: "text", Text: "analyze this"},
			{Type: "file", Name: "notes.md", MimeType: "text/markdown"},
			{Type: "image", Name: "diagram.png", MimeType: "image/png", Data: []byte{1, 2, 3}},
		},
	}

	text, images := ResolveEnvelopeForRuntime(env)
	assert.Contains(t, text, "analyze this")
	assert.Contains(t, text, "[File: notes.md, MIME: text/markdown]")
	assert.Contains(t, text, "[Image: diagram.png]")
	require.Len(t, images, 1)
	assert.Equal(t, "image/png", images[0].MimeType)
	assert.Equal(t, []byte{1, 2, 3}, images[0].Data)
}
