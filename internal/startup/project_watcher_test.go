package startup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProjectWatcherIgnoresDataDirButReloadsProjectFiles(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "anyai")
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "logs"), 0o755))

	agentPath := filepath.Join(root, "agent.md")
	require.NoError(t, os.WriteFile(agentPath, []byte("# Agent\n\ninitial"), 0o644))

	reloads := make(chan struct{}, 8)
	watcher, err := newProjectWatcher(root, dataDir, func() {
		select {
		case reloads <- struct{}{}:
		default:
		}
	})
	require.NoError(t, err)
	defer watcher.Stop()
	watcher.Start()

	time.Sleep(150 * time.Millisecond)

	logPath := filepath.Join(dataDir, "logs", "runtime.log")
	require.NoError(t, os.WriteFile(logPath, []byte("ignored"), 0o644))

	select {
	case <-reloads:
		t.Fatal("dataDir write should not trigger project reload")
	case <-time.After(800 * time.Millisecond):
	}

	require.NoError(t, os.WriteFile(agentPath, []byte("# Agent\n\nupdated"), 0o644))

	select {
	case <-reloads:
	case <-time.After(2 * time.Second):
		t.Fatal("project file write should trigger project reload")
	}
}
