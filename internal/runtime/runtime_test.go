package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	response string

	autoGoalFinalize bool
	autoGoalSeq      int
}

func (p *mockProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if events, ok := testutil.AutoGoalCompletionResponse(req, nil, &p.autoGoalFinalize, &p.autoGoalSeq); ok {
		return testutil.StaticEventStream(events), nil
	}
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.response}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *mockProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model", Provider: "test"}}
}

func (p *mockProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{Summary: "runtime compact summary"}, nil
}

func TestRuntimeStartTextRunUsesGatewayExecutionPath(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "assistant", Name: "Assistant", Model: "test/mock-model", Workspace: t.TempDir(), Entry: true},
	}

	rt := New(map[string]llm.LLMProvider{
		"test": &mockProvider{response: "hello from runtime"},
	}, session.NewStore(t.TempDir()), cfg)
	rt.SetRecorder(runtimeevents.NewRecorder())

	run, err := rt.StartTextRun(context.Background(), "cli", "assistant", "user-1", "user-1", "", "你好", runtimeport.ChatTypeDirect)
	require.NoError(t, err)
	require.NotNil(t, run)

	var text string
	for event := range run.Events {
		if delta := runtimeevents.TextDelta(event); delta != "" {
			text += delta
		}
	}

	assert.Equal(t, "hello from runtime", text)

	record, ok := rt.GetRun(run.RunID)
	require.True(t, ok)
	assert.Equal(t, runtimeevents.RunStatusCompleted, record.Status)
	assert.Equal(t, "assistant", record.AgentID)

	events := rt.ListRunEvents(run.RunID)
	require.NotEmpty(t, events)
	assert.Equal(t, runtimeevents.EventRunAccepted, events[0].Name)
	routed := eventByName(events, runtimeevents.EventRunRouted)
	require.NotNil(t, routed)
	assert.Equal(t, "requested", routed.Payload["source"])
	assert.Contains(t, eventNames(events), "run.started")
}

func TestRuntimeWrapsQueryAndControlHelpers(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "assistant", Name: "Assistant", Model: "test/mock-model", Workspace: t.TempDir(), Entry: true},
	}

	memMgr := memory.NewManager(t.TempDir())
	require.NoError(t, memMgr.Load())
	require.NoError(t, memMgr.SaveToLayer(memory.LayerLongTerm, "rule", "# Rule\n\nAlways keep trace visible."))

	store := session.NewStore(t.TempDir())
	rt := New(nil, store, cfg)
	rt.SetRecorder(runtimeevents.NewRecorder())
	rt.SetMemory(memMgr)
	rt.SetTaskStore(task.NewStore())

	taskItem := rt.TaskStore().Create("assistant", "agent call prompt", "sess_parent", "run_parent", task.Contract{})
	require.NotNil(t, taskItem)

	key, err := rt.CreateSession("assistant", "", "manual")
	require.NoError(t, err)
	loaded, err := rt.LoadSession("assistant", key)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	stats := rt.MemoryStats()
	assert.Equal(t, 1, stats.Total)
	entry, ok := rt.MemoryGet("long-term/rule", memory.SearchScope{})
	require.True(t, ok)
	assert.Equal(t, memory.LayerLongTerm, entry.Layer)

	listedTasks := rt.ListTasks()
	require.Len(t, listedTasks, 1)
	assert.Equal(t, taskItem.ID, listedTasks[0].ID)

	require.NoError(t, rt.CancelTask(taskItem.ID))
	currentTask, ok := rt.GetTask(taskItem.ID)
	require.True(t, ok)
	assert.Equal(t, task.StatusCancelled, currentTask.Status)

	require.NoError(t, rt.DeleteSession("assistant", key))
	reloaded, err := rt.LoadSession("assistant", key)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Empty(t, reloaded.Entries())
}

func TestRuntimeRebuildEventProjectionsUsesPersistentRecorder(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	rt := New(nil, session.NewStore(t.TempDir()), cfg)
	rt.SetTaskStore(task.NewStore())

	recorder, err := runtimeevents.NewPersistentRecorder(t.TempDir())
	require.NoError(t, err)
	rt.SetRecorder(recorder)

	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Model:     "test/model",
		Status:    runtimeevents.RunStatusRunning,
		StartedAt: time.Now().UTC(),
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "run.started",
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "task.running",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"task_id":      "task_1",
			"status":       "running",
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
			"task_id":      "task_1",
			"status":       "completed",
			"target_agent": "assistant",
			"input":        "agent call prompt",
			"summary":      "agent call done",
		},
	})
	recorder.FinishRun("run_1", runtimeevents.RunStatusCompleted, "done", "")

	require.NoError(t, rt.RebuildEventProjections())
	assert.NotEmpty(t, rt.EventStorageDir())

	run, ok := rt.GetRun("run_1")
	require.True(t, ok)
	assert.Equal(t, runtimeevents.RunStatusCompleted, run.Status)

	tasks := rt.ListTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "task_1", tasks[0].ID)
	assert.Equal(t, task.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "agent call done", tasks[0].Summary)
	assert.Equal(t, "agentcall_sess", tasks[0].SessionID)
}

func TestRuntimeCompactSessionUsesAgentCompactionEngine(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "assistant", Name: "Assistant", Model: "test/mock-model", Workspace: t.TempDir(), Entry: true},
	}

	rt := New(map[string]llm.LLMProvider{
		"test": &mockProvider{response: "unused"},
	}, session.NewStore(t.TempDir()), cfg)
	recorder := runtimeevents.NewRecorder()
	rt.SetRecorder(recorder)

	key, err := rt.CreateSession("assistant", "", "manual")
	require.NoError(t, err)
	sess, err := rt.LoadSession("assistant", key)
	require.NoError(t, err)
	for i := 0; i < 6; i++ {
		sess.Append(session.UserMessageEntry("question " + string(rune('0'+i))))
		sess.Append(session.AssistantMessageEntry("answer " + string(rune('0'+i))))
	}

	require.NoError(t, rt.CompactSession("assistant", key, 4))

	compacted, err := rt.LoadSession("assistant", key)
	require.NoError(t, err)
	history := compacted.History()
	require.NotEmpty(t, history)
	assert.Equal(t, session.EntryTypeCompaction, history[0].Type)

	var compact session.CompactionData
	require.NoError(t, json.Unmarshal(history[0].Data, &compact))
	assert.Equal(t, "runtime compact summary", compact.Text)
	assert.Equal(t, "manual", compact.Trigger)
	assert.False(t, compact.LegacyHeuristic)

	var compactRunID string
	for _, run := range recorder.ListRuns() {
		events := recorder.ListRunEvents(run.ID)
		for _, event := range events {
			if event.Name == runtimeevents.EventSessionCompactRequested {
				compactRunID = run.ID
				break
			}
		}
		if compactRunID != "" {
			break
		}
	}
	require.NotEmpty(t, compactRunID)

	run, ok := rt.GetRun(compactRunID)
	require.True(t, ok)
	assert.Equal(t, "control", run.Channel)
	assert.Equal(t, "runtime compact summary", run.Output)

	events := recorder.ListRunEvents(compactRunID)
	require.Len(t, events, 3)
	assert.Equal(t, runtimeevents.EventRunStarted, events[0].Name)
	assert.Equal(t, runtimeevents.EventSessionCompactRequested, events[1].Name)
	assert.Equal(t, runtimeevents.EventSessionCompactCompleted, events[2].Name)
	assert.Equal(t, "manual", events[1].Payload["trigger"])
	assert.Equal(t, "provider_model", events[2].Payload["summary_source"])
	assert.Equal(t, "runtime compact summary", events[2].Payload["summary"])
}

func eventNames(events []runtimeevents.EventRecord) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return names
}

func eventByName(events []runtimeevents.EventRecord, name string) *runtimeevents.EventRecord {
	for i := range events {
		if events[i].Name == name {
			return &events[i]
		}
	}
	return nil
}
