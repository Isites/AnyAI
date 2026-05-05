package control

import (
	"fmt"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeprojection "github.com/Isites/anyai/internal/runtime/projection"
	runtimesessions "github.com/Isites/anyai/internal/runtime/session"
	runtimesessionops "github.com/Isites/anyai/internal/runtime/session/ops"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceCancelTaskAndCompactSession(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{{ID: "assistant"}}

	store := runtimesessions.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	sessionSvc := runtimesessionops.NewService(
		func() *config.Config { return cfg },
		func() *runtimesessions.Store { return store },
		func() *runtimeevents.Recorder { return recorder },
	)
	taskStore := task.NewStore(task.WithEventAppender(func(e runtimeevents.EventRecord) { recorder.AppendEvent(e) }))
	projections := runtimeprojection.NewService(
		func() *runtimeevents.Recorder { return recorder },
		func() *task.Store { return taskStore },
		sessionSvc,
	)
	service := NewService(projections, nil, taskStore, nil, func() *runtimeevents.Recorder { return recorder })

	require.NoError(t, sessionSvc.Create("assistant", "manual"))
	sess, err := sessionSvc.Load("assistant", "manual")
	require.NoError(t, err)
	for i := 0; i < 4; i++ {
		sess.Append(runtimesessions.UserMessageEntry("question"))
		sess.Append(runtimesessions.AssistantMessageEntry("answer"))
	}

	// Start a run to record task events
	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Status:    runtimeevents.RunStatusRunning,
	})

	tk := taskStore.Create("assistant", "agent call task", "sess_1", "run_1", task.Contract{})
	require.NotNil(t, tk)

	require.NoError(t, service.CancelTask(tk.ID))
	current, ok := taskStore.Get(tk.ID)
	require.True(t, ok)
	assert.Equal(t, task.StatusCancelled, current.Status)

	// Check that the event was recorded by looking at run_1 events directly
	events := recorder.ListRunEvents("run_1")
	var sawCancelEvent bool
	for _, event := range events {
		if event.Name == "task.cancelled" && event.Payload["task_id"] == tk.ID {
			sawCancelEvent = true
		}
	}
	assert.True(t, sawCancelEvent)

	taskStore.Reset()
	require.NoError(t, projections.Task.Rebuild())
	current, ok = taskStore.Get(tk.ID)
	require.True(t, ok)
	assert.Equal(t, task.StatusCancelled, current.Status)

	require.NoError(t, service.CompactSession("assistant", "manual", 2))
	compacted, err := sessionSvc.Load("assistant", "manual")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(compacted.History()), 3)

	require.NoError(t, service.CreateSession("assistant", "created-by-control"))
	require.NoError(t, service.DeleteSession("assistant", "created-by-control"))

	require.NoError(t, projections.Rebuild())
	items, err := sessionSvc.List("assistant")
	require.NoError(t, err)
	// Check that "manual" session exists and "created-by-control" was deleted
	var manualFound, createdFound bool
	for _, item := range items {
		if item.ID == "manual" {
			manualFound = true
		}
		if item.ID == "created-by-control" {
			createdFound = true
		}
	}
	assert.True(t, manualFound, "manual session should exist")
	assert.False(t, createdFound, "created-by-control session should be deleted")
}

func TestServiceMemoryMaintenance(t *testing.T) {
	recorder := runtimeevents.NewRecorder()
	service := NewService(nil, stubMemoryCleaner{
		removed:   3,
		reindexed: 7,
		promoted:  2,
	}, nil, nil, func() *runtimeevents.Recorder { return recorder })

	now := time.Date(2026, 4, 24, 10, 30, 0, 0, time.UTC)

	removed, err := service.MemoryStaleCleanup(now)
	require.NoError(t, err)
	assert.Equal(t, 3, removed)

	reindexed, err := service.MemoryReindex()
	require.NoError(t, err)
	assert.Equal(t, 7, reindexed)

	promoted, err := service.MemoryPromoteEligible(now)
	require.NoError(t, err)
	assert.Equal(t, 2, promoted)

	var eventNames []string
	payloads := map[string]map[string]any{}
	for _, run := range recorder.ListRuns() {
		for _, event := range recorder.ListRunEvents(run.ID) {
			eventNames = append(eventNames, event.Name)
			payloads[event.Name] = event.Payload
		}
	}

	assert.Contains(t, eventNames, "maintenance.memory.stale_cleanup.completed")
	assert.Contains(t, eventNames, "maintenance.memory.reindexed")
	assert.Contains(t, eventNames, "maintenance.memory.promotion.completed")
	assert.Equal(t, 3, intPayload(payloads["maintenance.memory.stale_cleanup.completed"], "removed"))
	assert.Equal(t, 7, intPayload(payloads["maintenance.memory.reindexed"], "total"))
	assert.Equal(t, 2, intPayload(payloads["maintenance.memory.promotion.completed"], "promoted"))
}

type stubMemoryCleaner struct {
	removed   int
	reindexed int
	promoted  int
}

func (s stubMemoryCleaner) Cleanup(time.Time) (int, error) {
	return s.removed, nil
}

func (s stubMemoryCleaner) Reindex() (int, error) {
	return s.reindexed, nil
}

func (s stubMemoryCleaner) PromoteEligible(time.Time) (int, error) {
	return s.promoted, nil
}

func intPayload(payload map[string]any, key string) int {
	if len(payload) == 0 {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		panic(fmt.Sprintf("unexpected payload type %T for %s", value, key))
	}
}
