package runtimeevents

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/runtime/llm"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorderBeginRunPromotesQueuedRun(t *testing.T) {
	recorder := NewRecorder()
	createdAt := time.Now().UTC().Add(-time.Minute)
	recorder.StartRun(RunRecord{
		ID:        "run_queued",
		AgentID:   "agent",
		SessionID: "session",
		Channel:   "http",
		Input:     "hello",
		Status:    RunStatusQueued,
		CreatedAt: createdAt,
	})

	recorder.BeginRun(RunRecord{
		ID:        "run_queued",
		AgentID:   "agent",
		SessionID: "session",
		Model:     "test/model",
	})

	run, ok := recorder.GetRun("run_queued")
	require.True(t, ok)
	assert.Equal(t, RunStatusRunning, run.Status)
	assert.Equal(t, createdAt, run.CreatedAt)
	assert.Equal(t, "http", run.Channel)
	assert.Equal(t, "hello", run.Input)
	assert.False(t, run.StartedAt.IsZero())
}

func TestRecorderStartRunDefaultsToQueuedWithoutStartedAt(t *testing.T) {
	recorder := NewRecorder()

	recorder.StartRun(RunRecord{
		ID:        "run_accepted",
		AgentID:   "agent",
		SessionID: "session",
	})

	run, ok := recorder.GetRun("run_accepted")
	require.True(t, ok)
	assert.Equal(t, RunStatusQueued, run.Status)
	assert.False(t, run.CreatedAt.IsZero())
	assert.True(t, run.StartedAt.IsZero())
}

func TestRecorderLifecycleEventsUpdateRunReadModel(t *testing.T) {
	recorder := NewRecorder()
	startedAt := time.Now().UTC().Add(-time.Second)
	completedAt := startedAt.Add(time.Second)

	recorder.AppendEvent(EventRecord{
		RunID:     "run_events",
		AgentID:   "agent",
		SessionID: "session",
		Name:      EventRunStarted,
		Timestamp: startedAt,
	})
	run, ok := recorder.GetRun("run_events")
	require.True(t, ok)
	assert.Equal(t, RunStatusRunning, run.Status)
	assert.Equal(t, startedAt, run.StartedAt)

	recorder.AppendEvent(EventRecord{
		RunID:     "run_events",
		AgentID:   "agent",
		SessionID: "session",
		Name:      EventRunCompleted,
		Timestamp: completedAt,
	})
	run, ok = recorder.GetRun("run_events")
	require.True(t, ok)
	assert.Equal(t, RunStatusCompleted, run.Status)
	assert.Equal(t, completedAt, run.CompletedAt)
}

func TestRecorderIgnoresChildTraceLifecycleForParentRunStatus(t *testing.T) {
	recorder := NewRecorder()
	recorder.BeginRun(RunRecord{
		ID:        "run_parent",
		AgentID:   "lead",
		SessionID: "session",
	})

	childCompletedAt := time.Now().UTC()
	recorder.AppendEvent(EventRecord{
		RunID:           "run_parent",
		AgentID:         "worker",
		SessionID:       "child-session",
		RunNodeID:       RunNodeID("run_parent", "worker", ""),
		ParentRunNodeID: RunNodeID("run_parent", "lead", ""),
		Name:            EventRunCompleted,
		Timestamp:       childCompletedAt,
	})

	run, ok := recorder.GetRun("run_parent")
	require.True(t, ok)
	assert.Equal(t, RunStatusRunning, run.Status)
	assert.True(t, run.CompletedAt.IsZero())
}

func TestRecorderDefaultsChildAgentEventsToChildTraceNode(t *testing.T) {
	recorder := NewRecorder()
	recorder.BeginRun(RunRecord{
		ID:        "run_parent",
		AgentID:   "lead",
		SessionID: "session",
	})

	recorder.AppendEvent(EventRecord{
		RunID:     "run_parent",
		AgentID:   "worker",
		SessionID: "child-session",
		Name:      EventRunStarted,
	})

	events := recorder.ListRunEvents("run_parent")
	require.Len(t, events, 1)
	childStarted := events[0]
	assert.Equal(t, "worker", childStarted.AgentID)
	assert.Equal(t, RunNodeID("run_parent", "worker", ""), childStarted.RunNodeID)
	assert.Equal(t, RunNodeID("run_parent", "lead", ""), childStarted.ParentRunNodeID)

	run, ok := recorder.GetRun("run_parent")
	require.True(t, ok)
	assert.Equal(t, RunStatusRunning, run.Status)
	assert.Equal(t, RunNodeID("run_parent", "lead", ""), run.RunNodeID)
}

func TestMemorySaveToolResultEmitsCapturedEvent(t *testing.T) {
	run := RunRecord{ID: "run_memory", AgentID: "agent", SessionID: "session"}

	records := EventRecordsForAgentEvent(run, AgentEvent{
		Type: AgentEventToolResult,
		ToolCall: &llm.ToolCall{
			ID:   "tc_memory",
			Name: "memory_save",
		},
		Result: &tools.ToolResult{Output: `{"id":"long-term/release-rule","title":"Release Rule","layer":"long-term"}`},
	})

	require.Len(t, records, 2)
	assert.Equal(t, EventToolCompleted, records[0].Name)
	assert.Equal(t, EventMemoryCaptured, records[1].Name)
	assert.Equal(t, "long-term/release-rule", records[1].Payload["id"])
	assert.Equal(t, "memory_save", records[1].Payload["source"])
}

func TestPersistentRecorderRestoresRunsEventsAndRunTree(t *testing.T) {
	dir := t.TempDir()
	recorder, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	parentStarted := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	childStarted := parentStarted.Add(30 * time.Second)

	recorder.StartRun(RunRecord{
		ID:        "run_parent",
		AgentID:   "lead",
		SessionID: "sess_parent",
		Model:     "test/model",
		Status:    RunStatusRunning,
		StartedAt: parentStarted,
	})
	recorder.AppendEvent(EventRecord{
		RunID:     "run_parent",
		AgentID:   "lead",
		SessionID: "sess_parent",
		Name:      "run.started",
		Timestamp: parentStarted,
	})
	recorder.AppendEvent(EventRecord{
		RunID:     "run_parent",
		AgentID:   "lead",
		SessionID: "sess_parent",
		Name:      "text.delta",
		Timestamp: parentStarted.Add(5 * time.Second),
		Payload:   map[string]any{"text": "planning"},
	})
	recorder.AppendEvent(EventRecord{
		RunID:     "run_parent",
		AgentID:   "worker",
		SessionID: "sess_child",
		Name:      "run.started",
		Timestamp: childStarted,
	})
	recorder.AppendEvent(EventRecord{
		RunID:     "run_parent",
		AgentID:   "worker",
		SessionID: "sess_child",
		Name:      "run.completed",
		Timestamp: childStarted.Add(10 * time.Second),
	})
	recorder.FinishRun("run_parent", RunStatusCompleted, "planned", "")

	restored, err := NewPersistentRecorder(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "sessions"), filepath.Join(restored.StorageDir(), "sessions"))

	run, ok := restored.GetRun("run_parent")
	require.True(t, ok)
	assert.Equal(t, RunStatusCompleted, run.Status)
	assert.Equal(t, "planned", run.Output)

	events := restored.ListRunEvents("run_parent")
	require.Len(t, events, 4)
	assert.Equal(t, "run.started", events[0].Name)
	assert.Equal(t, "text.delta", events[1].Name)
	assert.Equal(t, "run.started", events[2].Name)
	assert.Equal(t, "run.completed", events[3].Name)
	assert.Equal(t, "worker", events[2].AgentID)
	assert.Equal(t, "sess_child", events[2].SessionID)

	treeRecord, ok := restored.GetRunTree("run_parent")
	require.True(t, ok)
	require.Len(t, treeRecord.Runs, 1)
	require.Len(t, treeRecord.Events, 4)

	tree, ok := restored.RunTree("run_parent")
	require.True(t, ok)
	require.Len(t, tree, 1)
	assert.Equal(t, "run_parent", tree[0].Run.ID)
	require.Empty(t, tree[0].Children)
	require.Len(t, tree[0].Events, 4)
	assert.Equal(t, "worker", tree[0].Events[2].AgentID)
}

func TestRecorderRebuildFromStorageRehydratesFreshInstanceState(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	writer.StartRun(RunRecord{
		ID:        "run_one",
		AgentID:   "assistant",
		SessionID: "sess_one",
		Model:     "test/model",
		Status:    RunStatusRunning,
	})
	writer.AppendEvent(EventRecord{
		RunID:     "run_one",
		AgentID:   "assistant",
		SessionID: "sess_one",
		Name:      "run.started",
	})
	writer.FinishRun("run_one", RunStatusCompleted, "ok", "")

	reader, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	run, ok := reader.GetRun("run_one")
	require.True(t, ok)
	assert.Equal(t, RunStatusCompleted, run.Status)

	err = reader.RebuildFromStorage()
	require.NoError(t, err)

	rebuilt, ok := reader.GetRun("run_one")
	require.True(t, ok)
	assert.Equal(t, RunStatusCompleted, rebuilt.Status)
	assert.Len(t, reader.ListRunEvents("run_one"), 1)
	assert.Nil(t, reader.LastPersistenceError())
}

func TestPersistentRecorderAggregatesChildSessionEventsIntoParentSessionView(t *testing.T) {
	dir := t.TempDir()
	recorder, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	parentStarted := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	childStarted := parentStarted.Add(10 * time.Second)

	recorder.StartRun(RunRecord{
		ID:        "run_parent",
		AgentID:   "lead",
		SessionID: "sess_parent",
		Model:     "test/model",
		Status:    RunStatusRunning,
		StartedAt: parentStarted,
	})
	recorder.StartRun(RunRecord{
		ID:            "run_child",
		ParentAgentID: "lead",
		AgentID:       "worker",
		SessionID:     "sess_child",
		Model:         "test/model",
		Status:        RunStatusRunning,
		StartedAt:     childStarted,
	})

	recorder.AppendEvent(EventRecord{
		RunID:     "run_parent",
		AgentID:   "lead",
		SessionID: "sess_parent",
		Name:      EventAgentCallCompleted,
		Timestamp: childStarted.Add(30 * time.Second),
		Payload: map[string]any{
			"id":           "call_1",
			"target_agent": "worker",
			"session_id":   "sess_child",
			"status":       "success",
			"summary":      "child completed",
		},
	})
	recorder.AppendEvent(EventRecord{
		RunID:     "run_child",
		AgentID:   "worker",
		SessionID: "sess_child",
		Name:      EventToolCallStarted,
		Timestamp: childStarted,
		Payload: map[string]any{
			"id":    "tool_1",
			"tool":  "read_file",
			"input": map[string]any{"path": "docs/spec.md"},
		},
	})
	recorder.AppendEvent(EventRecord{
		RunID:     "run_child",
		AgentID:   "worker",
		SessionID: "sess_child",
		Name:      EventToolCompleted,
		Timestamp: childStarted.Add(5 * time.Second),
		Payload: map[string]any{
			"id":    "tool_1",
			"tool":  "read_file",
			"input": map[string]any{"path": "docs/spec.md"},
		},
	})

	restored, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	events := restored.ListSessionEvents("sess_parent")
	require.Len(t, events, 3)
	assert.Equal(t, EventToolCallStarted, events[0].Name)
	assert.Equal(t, "sess_child", events[0].SessionID)
	assert.Equal(t, EventToolCompleted, events[1].Name)
	assert.Equal(t, EventAgentCallCompleted, events[2].Name)
}

func TestEventRecordsForAgentEventUsesToolLifecycleNames(t *testing.T) {
	run := RunRecord{
		ID:        "run_one",
		AgentID:   "assistant",
		SessionID: "sess_one",
	}
	call := &llm.ToolCall{
		ID:    "tool_1",
		Name:  "callagent",
		Input: json.RawMessage(`{"target_agent":"coder","task":"ship patch"}`),
	}

	requested := EventRecordsForAgentEvent(run, AgentEvent{
		Type:     AgentEventToolCallRequested,
		ToolCall: call,
	})
	require.Len(t, requested, 1)
	assert.Equal(t, "tool.call.requested", requested[0].Name)

	started := EventRecordsForAgentEvent(run, AgentEvent{
		Type:     AgentEventToolCallStart,
		ToolCall: call,
		ToolMetadata: &tools.ToolMetadata{
			Name:          "callagent",
			Effect:        tools.ToolEffectMutating,
			Tags:          []string{"workflow"},
			AllowParallel: true,
		},
	})
	require.Len(t, started, 2)
	assert.Equal(t, "tool.call.started", started[0].Name)
	assert.Equal(t, "agent.call.started", started[1].Name)

	failed := EventRecordsForAgentEvent(run, AgentEvent{
		Type:     AgentEventToolResult,
		ToolCall: call,
		Result:   &tools.ToolResult{Error: "boom"},
	})
	require.Len(t, failed, 2)
	assert.Equal(t, "tool.failed", failed[0].Name)
	assert.Equal(t, "agent.call.failed", failed[1].Name)
	assert.Equal(t, "failed", failed[1].Payload["status"])

	fanout := EventRecordsForAgentEvent(run, AgentEvent{
		Type: AgentEventToolFanoutCompleted,
		ToolFanout: &ToolFanoutInfo{
			TotalCount:     2,
			StartedCount:   2,
			CompletedCount: 2,
			FailedCount:    1,
			Status:         "completed",
		},
	})
	require.Len(t, fanout, 1)
	assert.Equal(t, "tool.fanout.completed", fanout[0].Name)
	assert.Equal(t, 1, fanout[0].Payload["failed_count"])
}

func TestRecorderPublishesTransientTextDeltasWithoutPersistingThem(t *testing.T) {
	recorder := NewRecorder()
	run := RunRecord{
		ID:        "run_one",
		AgentID:   "assistant",
		SessionID: "sess_one",
	}
	recorder.StartRun(run)

	ch, cancel := recorder.Subscribe(run.ID)
	defer cancel()

	recorder.RecordAgentEvent(run, AgentEvent{
		Type: AgentEventTextDelta,
		Text: "hello",
	})

	select {
	case event := <-ch:
		assert.Equal(t, EventTextDelta, event.Name)
		assert.Equal(t, "hello", event.Payload["text"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transient text delta")
	}

	assert.Empty(t, recorder.ListRunEvents(run.ID))
}

func TestRecorderPublishesTransientRunActivityWithoutPersistingIt(t *testing.T) {
	recorder := NewRecorder()
	run := RunRecord{
		ID:        "run_one",
		AgentID:   "assistant",
		SessionID: "sess_one",
	}
	recorder.StartRun(run)

	ch, cancel := recorder.Subscribe(run.ID)
	defer cancel()

	recorder.RecordAgentEvent(run, AgentEvent{Type: AgentEventActivity})

	select {
	case event := <-ch:
		assert.Equal(t, EventRunActivity, event.Name)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transient run activity")
	}

	assert.Empty(t, recorder.ListRunEvents(run.ID))
}
