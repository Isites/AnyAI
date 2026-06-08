package factory

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildBaseComponentsAbortsPersistedActiveRunsOnStartup(t *testing.T) {
	dataDir := t.TempDir()
	layout := ProjectLayout{
		DataDir:     dataDir,
		SessionsDir: filepath.Join(dataDir, "sessions"),
		MemoryDir:   filepath.Join(dataDir, "memory"),
		EventsDir:   filepath.Join(dataDir, "events"),
	}

	writer, err := runtimeevents.NewPersistentRecorder(layout.EventsDir)
	require.NoError(t, err)
	writer.StartRun(runtimeevents.RunRecord{
		ID:        "run_stale",
		AgentID:   "assistant",
		SessionID: "sess_stale",
		Status:    runtimeevents.RunStatusRunning,
		StartedAt: time.Now().UTC().Add(-time.Minute),
	})

	cfg := config.DefaultConfig()
	cfg.Memory.Enabled = false
	cfg.Memory.Dir = layout.MemoryDir

	base, err := BuildBaseComponents(cfg, layout, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, base)

	run, ok := base.Recorder.GetRun("run_stale")
	require.True(t, ok)
	assert.Equal(t, runtimeevents.RunStatusAborted, run.Status)
	assert.Equal(t, "run interrupted by runtime restart", run.Error)
	assert.False(t, run.CompletedAt.IsZero())

	events := base.Recorder.ListRunEvents("run_stale")
	require.Len(t, events, 1)
	assert.Equal(t, runtimeevents.EventRunAborted, events[0].Name)
	assert.Equal(t, "runtime_restart", events[0].Payload["reason"])
}
