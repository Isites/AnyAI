package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Isites/anyai/internal/config"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureRuntimeLoggingWritesToAnyAILogsDir(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root

	logRuntime, err := configureRuntimeLogging(cfg, LaunchModeChat)
	require.NoError(t, err)
	defer func() {
		_ = logRuntime.Close()
	}()

	runtimelogging.Debug("runtime debug log persisted", "component", "startup-test")
	runtimelogging.Info("runtime log persisted", "component", "startup-test")

	snapshot := logRuntime.Buffer().Snapshot()
	require.NotEmpty(t, snapshot)
	assert.Equal(t, "runtime log persisted", snapshot[len(snapshot)-1].Message)

	logPath := filepath.Join(root, "anyai", "logs", "runtime.log")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "runtime debug log persisted")
	assert.Contains(t, string(data), "runtime log persisted")
	assert.Contains(t, string(data), "component=startup-test")
}

func TestConfigureRuntimeLoggingUsesConfigRotationSettings(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.Logging.Rotation.Filename = "custom.log"
	cfg.Logging.Rotation.MaxBytes = 96
	cfg.Logging.Rotation.MaxBackups = 1
	disableMirror := false
	cfg.Logging.MirrorStderr = &disableMirror

	logRuntime, err := configureRuntimeLogging(cfg, LaunchModeStart)
	require.NoError(t, err)
	defer func() {
		_ = logRuntime.Close()
	}()

	for i := 0; i < 8; i++ {
		runtimelogging.Info("rotating log payload", "chunk", i, "payload", strings.Repeat("x", 40))
	}

	logsDir := filepath.Join(root, "anyai", "logs")
	entries, err := os.ReadDir(logsDir)
	require.NoError(t, err)

	var customCount int
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "custom") {
			customCount++
		}
	}
	assert.GreaterOrEqual(t, customCount, 1)
	assert.LessOrEqual(t, customCount, 2)
}
