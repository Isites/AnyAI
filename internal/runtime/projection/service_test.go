package projection

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimesessions "github.com/Isites/anyai/internal/runtime/session"
	runtimesessionops "github.com/Isites/anyai/internal/runtime/session/ops"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceRebuildRestoresTraceTaskAndSessionViews(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{{ID: "assistant"}}

	sessionStore := runtimesessions.NewStore(t.TempDir())
	require.NoError(t, sessionStore.Create("assistant", "manual"))

	recorder, err := runtimeevents.NewPersistentRecorder(t.TempDir())
	require.NoError(t, err)
	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Model:     "test/model",
		Input:     "Investigate the issue",
		Status:    runtimeevents.RunStatusRunning,
		StartedAt: time.Now().UTC(),
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "run.started",
		Timestamp: time.Now().UTC(),
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "tool.call.started",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"id":    "tool_1",
			"tool":  "read_file",
			"input": map[string]any{"path": "README.md"},
		},
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "tool.completed",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"id":       "tool_1",
			"tool":     "read_file",
			"output":   "project notes",
			"metadata": map[string]any{"source": "disk"},
		},
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "text.delta",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"text": "done",
		},
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "task.running",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"task_id":      "task_1",
			"target_agent": "assistant",
			"input":        "agent call prompt",
		},
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "agentcall_sess",
		Name:      "task.completed",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"task_id": "task_1",
			"summary": "done",
		},
	})
	recorder.FinishRun("run_1", runtimeevents.RunStatusCompleted, "done", "")

	taskStore := task.NewStore()
	sessionSvc := runtimesessionops.NewService(
		func() *config.Config { return cfg },
		func() *runtimesessions.Store { return sessionStore },
		func() *runtimeevents.Recorder { return recorder },
	)
	service := NewService(
		func() *runtimeevents.Recorder { return recorder },
		func() *task.Store { return taskStore },
		sessionSvc,
	)

	require.NoError(t, service.Rebuild())

	run, ok := service.Run.GetRun("run_1")
	require.True(t, ok)
	assert.Equal(t, runtimeevents.RunStatusCompleted, run.Status)

	tasks := service.Task.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, "task_1", tasks[0].ID)
	assert.Equal(t, task.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "agentcall_sess", tasks[0].SessionID)

	events := service.Session.Events("assistant", "sess_1")
	require.Len(t, events, 4)
	assert.Equal(t, runtimeevents.EventSessionInputStored, events[0].Name)
	assert.Equal(t, runtimeevents.EventToolCallStarted, events[1].Name)
	assert.Equal(t, runtimeevents.EventToolCompleted, events[2].Name)
	assert.Equal(t, runtimeevents.EventSessionOutputStored, events[3].Name)

	rebuiltSession, err := service.Session.Load("assistant", "sess_1")
	require.NoError(t, err)
	entries := rebuiltSession.History()
	require.Len(t, entries, 4)
	assert.Equal(t, runtimesessions.EntryTypeMessage, entries[0].Type)
	assert.Equal(t, runtimesessions.EntryTypeToolCall, entries[1].Type)
	assert.Equal(t, runtimesessions.EntryTypeToolResult, entries[2].Type)
	assert.Equal(t, runtimesessions.EntryTypeMessage, entries[3].Type)

	var userMsg runtimesessions.MessageData
	require.NoError(t, json.Unmarshal(entries[0].Data, &userMsg))
	assert.Equal(t, "Investigate the issue", userMsg.Text)

	var toolCall runtimesessions.ToolCallData
	require.NoError(t, json.Unmarshal(entries[1].Data, &toolCall))
	assert.Equal(t, "read_file", toolCall.Tool)

	var toolResult runtimesessions.ToolResultData
	require.NoError(t, json.Unmarshal(entries[2].Data, &toolResult))
	assert.Equal(t, "project notes", toolResult.Output)
	assert.Equal(t, "disk", toolResult.Metadata["source"])

	var assistantMsg runtimesessions.MessageData
	require.NoError(t, json.Unmarshal(entries[3].Data, &assistantMsg))
	assert.Equal(t, "done", assistantMsg.Text)

	index, err := service.Session.ReadIndex()
	require.NoError(t, err)
	require.Len(t, index.Agents["assistant"], 2)
}
