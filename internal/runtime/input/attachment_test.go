package input

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachmentStoreSaveAndGet(t *testing.T) {
	dir := t.TempDir()
	store := NewAttachmentStore(dir)

	content := []byte("hello world")
	att, err := store.Save("test.txt", "text/plain", content)
	require.NoError(t, err)
	assert.NotEmpty(t, att.ID)
	assert.Equal(t, "test.txt", att.Name)
	assert.Equal(t, "text/plain", att.MimeType)
	assert.Equal(t, int64(11), att.Size)

	got, ok := store.Get(att.ID)
	assert.True(t, ok)
	assert.Equal(t, att.ID, got.ID)
	assert.Equal(t, att.Name, got.Name)

	data, err := os.ReadFile(filepath.Join(dir, att.ID, att.Name))
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestAttachmentStoreGetNotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewAttachmentStore(dir)
	_, ok := store.Get("nonexistent")
	assert.False(t, ok)
}

func TestAttachmentStoreMultipleSaves(t *testing.T) {
	dir := t.TempDir()
	store := NewAttachmentStore(dir)

	att1, err := store.Save("a.txt", "text/plain", []byte("aaa"))
	require.NoError(t, err)
	att2, err := store.Save("b.txt", "text/plain", []byte("bbb"))
	require.NoError(t, err)

	assert.NotEqual(t, att1.ID, att2.ID)

	got1, ok1 := store.Get(att1.ID)
	assert.True(t, ok1)
	assert.Equal(t, "a.txt", got1.Name)

	got2, ok2 := store.Get(att2.ID)
	assert.True(t, ok2)
	assert.Equal(t, "b.txt", got2.Name)
}
