package input

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareEnvelopeAssignsStableIDsAndStoresAttachments(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "brief.txt")
	require.NoError(t, os.WriteFile(sourcePath, []byte("hello brief"), 0o644))

	env, attachments, err := PrepareEnvelope(InputEnvelope{
		SessionID: "sess",
		Blocks: []InputBlock{
			{Type: "text", Text: "inspect"},
			{Type: "file", Name: "brief.txt", Path: sourcePath},
			{Type: "image", Name: "diagram.png", MimeType: "image/png", Data: []byte{1, 2, 3}},
		},
	}, PrepareOptions{RunID: "run_1", BaseDir: filepath.Join(dir, "anyai", "assets")})
	require.NoError(t, err)
	require.Len(t, env.Blocks, 3)
	require.Len(t, attachments, 2)

	assert.Equal(t, "input_1", env.Blocks[0].ID)
	assert.Equal(t, "input_2", env.Blocks[1].ID)
	assert.Equal(t, "input_3", env.Blocks[2].ID)
	assert.Equal(t, "text/plain; charset=utf-8", env.Blocks[1].MimeType)
	assert.Empty(t, env.Blocks[2].Data)

	fileData, err := os.ReadFile(env.Blocks[1].Path)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello brief"), fileData)

	imageData, err := os.ReadFile(env.Blocks[2].Path)
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, imageData)
	assert.Equal(t, "input_3", env.Blocks[2].Meta["attachment_id"])
	assert.Contains(t, env.Blocks[2].Path, filepath.Join("anyai", "assets", "run_1", "input_3"))
}

func TestPrepareEnvelopeKeepsUploadedAttachmentReference(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "anyai", "assets")
	uploadPath := filepath.Join(baseDir, "uploads", "att_uploaded", "diagram.png")
	require.NoError(t, os.MkdirAll(filepath.Dir(uploadPath), 0o755))
	require.NoError(t, os.WriteFile(uploadPath, []byte{0x89, 'P', 'N', 'G'}, 0o644))

	env, attachments, err := PrepareEnvelope(InputEnvelope{
		SessionID: "sess",
		Blocks: []InputBlock{
			{
				ID:           "att_uploaded",
				Type:         "image",
				Name:         "diagram.png",
				Path:         uploadPath,
				AttachmentID: "att_uploaded",
				MimeType:     "image/png",
			},
		},
	}, PrepareOptions{RunID: "run_2", BaseDir: baseDir})
	require.NoError(t, err)
	require.Empty(t, attachments)
	require.Len(t, env.Blocks, 1)
	assert.Equal(t, "att_uploaded", env.Blocks[0].AttachmentID)
	assert.Equal(t, uploadPath, env.Blocks[0].Path)
	assert.EqualValues(t, 4, env.Blocks[0].Meta["size"])
	assert.NoFileExists(t, filepath.Join(baseDir, "run_2", "att_uploaded", "diagram.png"))
}
