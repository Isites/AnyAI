package runtimeevents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestPersistentRecorderAbortActiveRunsMarksRestartInterruptedRuns(t *testing.T) {
	dir := t.TempDir()
	recorder, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	recorder.StartRun(RunRecord{
		ID:        "run_queued",
		AgentID:   "assistant",
		SessionID: "sess_queued",
		Status:    RunStatusQueued,
	})
	recorder.StartRun(RunRecord{
		ID:        "run_running",
		AgentID:   "assistant",
		SessionID: "sess_running",
		Status:    RunStatusRunning,
		StartedAt: time.Now().UTC().Add(-time.Minute),
	})
	recorder.StartRun(RunRecord{
		ID:        "run_completed",
		AgentID:   "assistant",
		SessionID: "sess_completed",
		Status:    RunStatusCompleted,
	})

	aborted := recorder.AbortActiveRuns("runtime restarted")
	require.Equal(t, 2, aborted)

	for _, runID := range []string{"run_queued", "run_running"} {
		run, ok := recorder.GetRun(runID)
		require.True(t, ok)
		assert.Equal(t, RunStatusAborted, run.Status)
		assert.Equal(t, "runtime restarted", run.Error)
		assert.False(t, run.CompletedAt.IsZero())

		events := recorder.ListRunEvents(runID)
		require.Len(t, events, 1)
		assert.Equal(t, EventRunAborted, events[0].Name)
		assert.Equal(t, "runtime restarted", events[0].Payload["message"])
		assert.Equal(t, "runtime_restart", events[0].Payload["reason"])
	}

	run, ok := recorder.GetRun("run_completed")
	require.True(t, ok)
	assert.Equal(t, RunStatusCompleted, run.Status)
	assert.Empty(t, recorder.ListRunEvents("run_completed"))

	restored, err := NewPersistentRecorder(dir)
	require.NoError(t, err)
	restoredRun, ok := restored.GetRun("run_running")
	require.True(t, ok)
	assert.Equal(t, RunStatusAborted, restoredRun.Status)
	require.Len(t, restored.ListRunEvents("run_running"), 1)
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

func TestEventRecordsForAgentEventCompactsLargeToolResultPayload(t *testing.T) {
	run := RunRecord{ID: "run_large", AgentID: "agent", SessionID: "session"}

	records := EventRecordsForAgentEvent(run, AgentEvent{
		Type: AgentEventToolResult,
		ToolCall: &llm.ToolCall{
			ID:   "tc_large",
			Name: "site_profile",
		},
		Result: &tools.ToolResult{Output: strings.Repeat("x", 12*1024)},
	})

	require.Len(t, records, 1)
	output, _ := records[0].Payload["output"].(string)
	assert.Contains(t, output, "content omitted from durable transcript")
	assert.Less(t, len(output), 9*1024)
}

func TestPersistentRecorderRebuildCompactsLegacyLargePayloads(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	require.NoError(t, os.MkdirAll(runsDir, 0o755))

	payload := persistedRecord{
		Kind: "event",
		Event: &EventRecord{
			RunID:     "run_large",
			AgentID:   "agent",
			SessionID: "session",
			Name:      EventToolCompleted,
			Timestamp: time.Now().UTC(),
			Payload: map[string]any{
				"id":     "tc_large",
				"tool":   "site_profile",
				"output": strings.Repeat("x", 12*1024),
			},
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, "run_large.jsonl"), append(data, '\n'), 0o644))

	recorder, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	events := recorder.ListRunEvents("run_large")
	require.Len(t, events, 1)
	output, _ := events[0].Payload["output"].(string)
	assert.Contains(t, output, "content omitted from durable transcript")
	assert.Less(t, len(output), 9*1024)
}

func TestPersistentRecorderRawIndexSkipsHugeEventPayloadBody(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	require.NoError(t, os.MkdirAll(runsDir, 0o755))

	payload := persistedRecord{
		Kind: "event",
		Event: &EventRecord{
			RunID:     "run_huge",
			AgentID:   "lead",
			SessionID: "sess_parent",
			Name:      EventAgentCallCompleted,
			Timestamp: time.Now().UTC(),
			Payload: map[string]any{
				"id":         "call_1",
				"output":     strings.Repeat("x", rawIndexLinePrefixLimit+rawIndexLineSuffixLimit+1024),
				"session_id": "sess_child",
			},
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, "run_huge.jsonl"), append(data, '\n'), 0o644))

	recorder, err := NewPersistentRecorder(dir)
	require.NoError(t, err)
	require.Empty(t, recorder.runEvents["run_huge"])

	sessionEvents := recorder.ListSessionEvents("sess_parent")
	require.Len(t, sessionEvents, 1)
	assert.Equal(t, EventAgentCallCompleted, sessionEvents[0].Name)
}

func TestPersistentRecorderRebuildCompactsLegacyLargeRunOutput(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	require.NoError(t, os.MkdirAll(runsDir, 0o755))

	payload := persistedRecord{
		Kind: "run",
		Run: &RunRecord{
			ID:        "run_large",
			AgentID:   "agent",
			SessionID: "session",
			Status:    RunStatusCompleted,
			CreatedAt: time.Now().UTC(),
			Output:    strings.Repeat("x", 12*1024),
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, "run_large.jsonl"), append(data, '\n'), 0o644))

	recorder, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	run, ok := recorder.GetRun("run_large")
	require.True(t, ok)
	assert.Contains(t, run.Output, "content omitted from durable transcript")
	assert.Less(t, len(run.Output), 9*1024)
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

	summary, ok := restored.RunTreeSummary("run_parent")
	require.True(t, ok)
	require.Len(t, summary, 1)
	assert.Equal(t, "run_parent", summary[0].Run.ID)
	require.Empty(t, summary[0].Events)
}

func TestPersistentRecorderRebuildKeepsHistoricalEventsLazy(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	writer.StartRun(RunRecord{
		ID:        "run_lazy",
		AgentID:   "assistant",
		SessionID: "sess_lazy",
		Model:     "test/model",
		Status:    RunStatusRunning,
	})
	for i := 0; i < 5; i++ {
		writer.AppendEvent(EventRecord{
			RunID:     "run_lazy",
			AgentID:   "assistant",
			SessionID: "sess_lazy",
			Name:      EventToolCompleted,
			Timestamp: time.Now().UTC().Add(time.Duration(i) * time.Second),
			Payload: map[string]any{
				"id":     "tool_lazy",
				"output": strings.Repeat("x", 12*1024),
			},
		})
	}

	restored, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	require.Empty(t, restored.runEvents["run_lazy"])
	assert.Equal(t, 5, restored.runSequences["run_lazy"])

	events := restored.ListRunEvents("run_lazy")
	require.Len(t, events, 5)
	output, _ := events[0].Payload["output"].(string)
	assert.Contains(t, output, "content omitted from durable transcript")
	assert.Less(t, len(output), 9*1024)
	require.Empty(t, restored.runEvents["run_lazy"])
}

func TestPersistentRecorderUsesSidecarIndexForLazyRebuild(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	parentStarted := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	writer.StartRun(RunRecord{
		ID:        "run_parent",
		AgentID:   "lead",
		SessionID: "sess_parent",
		Model:     "test/model",
		Status:    RunStatusRunning,
		StartedAt: parentStarted,
	})
	writer.StartRun(RunRecord{
		ID:        "run_child",
		AgentID:   "worker",
		SessionID: "sess_child",
		Model:     "test/model",
		Status:    RunStatusRunning,
		StartedAt: parentStarted.Add(time.Second),
	})
	writer.AppendEvent(EventRecord{
		RunID:     "run_parent",
		AgentID:   "lead",
		SessionID: "sess_parent",
		Name:      EventAgentCallCompleted,
		Timestamp: parentStarted.Add(2 * time.Second),
		Payload: map[string]any{
			"id":           "call_1",
			"session_id":   "sess_child",
			"target_agent": "worker",
			"status":       "success",
		},
	})
	writer.AppendEvent(EventRecord{
		RunID:     "run_child",
		AgentID:   "worker",
		SessionID: "sess_child",
		Name:      EventToolCompleted,
		Timestamp: parentStarted.Add(3 * time.Second),
		Payload:   map[string]any{"id": "tool_1", "output": "ok"},
	})

	indexInfo, err := os.Stat(filepath.Join(dir, "index.jsonl"))
	require.NoError(t, err)
	require.Greater(t, indexInfo.Size(), int64(0))

	restored, err := NewPersistentRecorder(dir)
	require.NoError(t, err)
	require.Empty(t, restored.runEvents["run_parent"])
	require.Empty(t, restored.runEvents["run_child"])
	assert.Equal(t, 1, restored.runSequences["run_parent"])
	assert.Equal(t, 1, restored.runSequences["run_child"])

	parentEvents := restored.ListRunEvents("run_parent")
	require.Len(t, parentEvents, 1)
	assert.Equal(t, EventAgentCallCompleted, parentEvents[0].Name)

	sessionEvents := restored.ListSessionEvents("sess_parent")
	require.Len(t, sessionEvents, 2)
	assert.Equal(t, EventAgentCallCompleted, sessionEvents[0].Name)
	assert.Equal(t, EventToolCompleted, sessionEvents[1].Name)
}

func TestPersistentRecorderRawIndexExtractsLegacyChildSessionPayload(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	require.NoError(t, os.MkdirAll(runsDir, 0o755))

	base := time.Now().UTC().Truncate(time.Second)
	records := []persistedRecord{
		{
			Kind: "run",
			Run: &RunRecord{
				ID:        "run_parent",
				AgentID:   "lead",
				SessionID: "sess_parent",
				Status:    RunStatusRunning,
				StartedAt: base,
			},
		},
		{
			Kind: "event",
			Event: &EventRecord{
				RunID:     "run_parent",
				AgentID:   "lead",
				SessionID: "sess_parent",
				Name:      EventAgentCallCompleted,
				Timestamp: base.Add(time.Second),
				Payload: map[string]any{
					"session_id":   "sess_child",
					"target_agent": "worker",
					"status":       "success",
				},
			},
		},
		{
			Kind: "run",
			Run: &RunRecord{
				ID:        "run_child",
				AgentID:   "worker",
				SessionID: "sess_child",
				Status:    RunStatusRunning,
				StartedAt: base.Add(2 * time.Second),
			},
		},
		{
			Kind: "event",
			Event: &EventRecord{
				RunID:     "run_child",
				AgentID:   "worker",
				SessionID: "sess_child",
				Name:      EventToolCompleted,
				Timestamp: base.Add(3 * time.Second),
				Payload:   map[string]any{"id": "tool_1"},
			},
		},
	}
	file, err := os.Create(filepath.Join(runsDir, "legacy.jsonl"))
	require.NoError(t, err)
	encoder := json.NewEncoder(file)
	for _, record := range records {
		require.NoError(t, encoder.Encode(record))
	}
	require.NoError(t, file.Close())

	restored, err := NewPersistentRecorder(dir)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "index.jsonl"))

	sessionEvents := restored.ListSessionEvents("sess_parent")
	require.Len(t, sessionEvents, 2)
	assert.Equal(t, EventAgentCallCompleted, sessionEvents[0].Name)
	assert.Equal(t, EventToolCompleted, sessionEvents[1].Name)

	restoredAgain, err := NewPersistentRecorder(dir)
	require.NoError(t, err)
	sessionEvents = restoredAgain.ListSessionEvents("sess_parent")
	require.Len(t, sessionEvents, 2)
	assert.Equal(t, EventAgentCallCompleted, sessionEvents[0].Name)
	assert.Equal(t, EventToolCompleted, sessionEvents[1].Name)
}

func TestPersistentRecorderCapsLiveEventsButReturnsPersistedHistory(t *testing.T) {
	dir := t.TempDir()
	recorder, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	recorder.StartRun(RunRecord{
		ID:        "run_tail",
		AgentID:   "assistant",
		SessionID: "sess_tail",
		Model:     "test/model",
		Status:    RunStatusRunning,
	})
	total := maxPersistentLiveRunEventsPerRun + 25
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < total; i++ {
		recorder.AppendEvent(EventRecord{
			RunID:     "run_tail",
			AgentID:   "assistant",
			SessionID: "sess_tail",
			Name:      EventToolCompleted,
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Payload: map[string]any{
				"id":     "tool_tail",
				"output": i,
			},
		})
	}

	require.Len(t, recorder.runEvents["run_tail"], maxPersistentLiveRunEventsPerRun)

	events := recorder.ListRunEvents("run_tail")
	require.Len(t, events, total)
	assert.Equal(t, 1, events[0].Sequence)
	assert.Equal(t, total, events[len(events)-1].Sequence)
}

func TestListRunEventsByNamePrefixSkipsUnmatchedLargePayloads(t *testing.T) {
	dir := t.TempDir()
	recorder, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	recorder.StartRun(RunRecord{
		ID:        "run_large_filter",
		AgentID:   "assistant",
		SessionID: "sess_large_filter",
		Model:     "test/model",
		Status:    RunStatusRunning,
	})
	recorder.AppendEvent(EventRecord{
		RunID:     "run_large_filter",
		AgentID:   "assistant",
		SessionID: "sess_large_filter",
		Name:      EventToolCompleted,
		Payload: map[string]any{
			"output": strings.Repeat("x", 2*1024*1024),
		},
	})
	recorder.AppendEvent(EventRecord{
		RunID:     "run_large_filter",
		AgentID:   "assistant",
		SessionID: "sess_large_filter",
		Name:      EventTaskCompleted,
		Payload: map[string]any{
			"task_id": "task_1",
			"summary": "done",
		},
	})

	restored, err := NewPersistentRecorder(dir)
	require.NoError(t, err)

	events := restored.ListRunEventsByNamePrefix("run_large_filter", "task.")
	require.Len(t, events, 1)
	assert.Equal(t, EventTaskCompleted, events[0].Name)
	assert.Equal(t, "task_1", events[0].Payload["task_id"])
	assert.NotContains(t, events[0].Payload, "output")
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
