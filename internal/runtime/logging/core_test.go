package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotatingFileWriterRenamesExistingActiveLogOnStartup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	activePath := filepath.Join(dir, defaultRuntimeLogFilename)
	require.NoError(t, os.WriteFile(activePath, []byte("old log entry\n"), 0o644))

	writer, err := NewRotatingFileWriter(dir, RotatingFileOptions{
		Filename:   defaultRuntimeLogFilename,
		MaxBytes:   1024,
		MaxBackups: 5,
	})
	require.NoError(t, err)
	defer writer.Close()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var archives []string
	for _, entry := range entries {
		if entry.Name() != defaultRuntimeLogFilename {
			archives = append(archives, entry.Name())
		}
	}

	require.Len(t, archives, 1)
	archivedData, err := os.ReadFile(filepath.Join(dir, archives[0]))
	require.NoError(t, err)
	assert.Contains(t, string(archivedData), "old log entry")

	activeData, err := os.ReadFile(activePath)
	require.NoError(t, err)
	assert.Empty(t, string(activeData))
}

func TestRotatingFileWriterRollsAndPrunesArchivedLogs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	writer, err := NewRotatingFileWriter(dir, RotatingFileOptions{
		Filename:   defaultRuntimeLogFilename,
		MaxBytes:   48,
		MaxBackups: 2,
	})
	require.NoError(t, err)
	defer writer.Close()

	for i := 0; i < 4; i++ {
		payload := strings.Repeat(string(rune('A'+i)), 36) + "\n"
		_, err := writer.Write([]byte(payload))
		require.NoError(t, err)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var archives []string
	for _, entry := range entries {
		if entry.Name() != defaultRuntimeLogFilename {
			archives = append(archives, entry.Name())
		}
	}

	assert.Len(t, archives, 2)

	activeData, err := os.ReadFile(filepath.Join(dir, defaultRuntimeLogFilename))
	require.NoError(t, err)
	assert.Contains(t, string(activeData), strings.Repeat("D", 36))
}
