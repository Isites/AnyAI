package ops

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceCreateListIndexAndCompact(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{{ID: "assistant"}}

	store := session.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	service := NewService(
		func() *config.Config { return cfg },
		func() *session.Store { return store },
		func() *runtimeevents.Recorder { return recorder },
	)

	require.NoError(t, service.Create("assistant", "manual"))

	sess, err := service.Load("assistant", "manual")
	require.NoError(t, err)
	for i := 0; i < 4; i++ {
		sess.Append(session.UserMessageEntry("question"))
		sess.Append(session.AssistantMessageEntry("answer"))
	}

	items, err := service.List("assistant")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "manual", items[0].ID)

	index, err := service.ReadIndex()
	require.NoError(t, err)
	require.Len(t, index.Agents["assistant"], 1)

	compacted, err := service.Compact("assistant", "manual", 2)
	require.NoError(t, err)
	require.NotNil(t, compacted)
	assert.LessOrEqual(t, len(compacted.History()), 3)

	var compactEvents []runtimeevents.EventRecord
	for _, run := range recorder.ListRuns() {
		events := recorder.ListRunEvents(run.ID)
		for _, event := range events {
			if event.Name == "session.compact.requested" {
				compactEvents = events
				break
			}
		}
		if len(compactEvents) > 0 {
			break
		}
	}
	require.Len(t, compactEvents, 3)
	assert.Equal(t, runtimeevents.EventRunStarted, compactEvents[0].Name)
	assert.Equal(t, "session.compact.requested", compactEvents[1].Name)
	assert.Equal(t, "session.compact.completed", compactEvents[2].Name)
}

func TestServiceRecordsAssistantSessionOutputFromSessionAppend(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{{ID: "assistant"}}

	store := session.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	service := NewService(
		func() *config.Config { return cfg },
		func() *session.Store { return store },
		func() *runtimeevents.Recorder { return recorder },
	)

	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Model:     "test/model",
		Status:    runtimeevents.RunStatusRunning,
	})
	sess, err := service.Load("assistant", "sess_1")
	require.NoError(t, err)
	sess.SetActiveRefs(session.EntryRefs{RunID: "run_1", TaskID: "task_1"})

	sess.Append(session.UserMessageEntry("question"))
	sess.Append(session.AssistantMessageEntry("answer"))

	events := recorder.ListRunEvents("run_1")
	require.Len(t, events, 1)
	assert.Equal(t, runtimeevents.EventSessionOutputStored, events[0].Name)
	assert.Equal(t, "answer", events[0].Payload["text"])
}

func TestServiceRebuildFromEventsRestoresPlanTodoAndDeletion(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{{ID: "assistant"}}

	store := session.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	service := NewService(
		func() *config.Config { return cfg },
		func() *session.Store { return store },
		func() *runtimeevents.Recorder { return recorder },
	)

	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Model:     "test/model",
		Input:     "Need a rollout plan",
		Status:    runtimeevents.RunStatusRunning,
		StartedAt: time.Now().UTC(),
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      runtimeevents.EventSessionInputStored,
		Payload: map[string]any{
			"text": "Need a rollout plan",
			"images": []map[string]any{
				{
					"mime_type": "image/png",
					"data":      base64.StdEncoding.EncodeToString([]byte("pngdata")),
				},
			},
		},
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "session.plan.updated",
		Payload:   map[string]any{"plan": "1. Analyze\n2. Ship"},
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "session.todo.updated",
		Payload: map[string]any{
			"id":         "todo_1",
			"content":    "Write regression test",
			"status":     "completed",
			"created_at": float64(1710000000),
		},
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Name:      "text.delta",
		Payload:   map[string]any{"text": "done"},
	})
	recorder.FinishRun("run_1", runtimeevents.RunStatusCompleted, "done", "")

	require.NoError(t, service.Create("assistant", "manual"))
	require.NoError(t, service.Delete("assistant", "manual"))

	require.NoError(t, service.RebuildFromEvents())

	rebuilt, err := service.Load("assistant", "sess_1")
	require.NoError(t, err)
	entries := rebuilt.History()
	require.Len(t, entries, 4)
	assert.Equal(t, session.EntryTypeMessage, entries[0].Type)
	assert.Equal(t, session.EntryTypePlan, entries[1].Type)
	assert.Equal(t, session.EntryTypeTodo, entries[2].Type)
	assert.Equal(t, session.EntryTypeMessage, entries[3].Type)

	var userMsg session.MessageData
	require.NoError(t, json.Unmarshal(entries[0].Data, &userMsg))
	assert.Equal(t, "Need a rollout plan", userMsg.Text)
	require.Len(t, userMsg.Images, 1)
	assert.Equal(t, "image/png", userMsg.Images[0].MimeType)

	var plan session.PlanData
	require.NoError(t, json.Unmarshal(entries[1].Data, &plan))
	assert.Equal(t, "1. Analyze\n2. Ship", plan.Plan)

	var todo session.TodoData
	require.NoError(t, json.Unmarshal(entries[2].Data, &todo))
	assert.Equal(t, "todo_1", todo.ID)
	assert.Equal(t, "completed", todo.Status)

	items, err := service.List("assistant")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "sess_1", items[0].ID)
}

func TestServiceRebuildFromEventsReplaysCompactionAsHistoryRewrite(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{{ID: "assistant"}}

	store := session.NewStore(t.TempDir())
	recorder := runtimeevents.NewRecorder()
	service := NewService(
		func() *config.Config { return cfg },
		func() *session.Store { return store },
		func() *runtimeevents.Recorder { return recorder },
	)

	startedAt := time.Now().UTC()
	for i := 0; i < 6; i++ {
		runID := "run_hist_" + string(rune('0'+i))
		recorder.StartRun(runtimeevents.RunRecord{
			ID:        runID,
			AgentID:   "assistant",
			SessionID: "sess_compact",
			Model:     "test/model",
			Input:     "question " + string(rune('0'+i)),
			Status:    runtimeevents.RunStatusRunning,
			StartedAt: startedAt.Add(time.Duration(i) * time.Minute),
		})
		recorder.AppendEvent(runtimeevents.EventRecord{
			RunID:     runID,
			AgentID:   "assistant",
			SessionID: "sess_compact",
			Name:      "text.delta",
			Payload:   map[string]any{"text": "answer " + string(rune('0'+i))},
		})
		recorder.FinishRun(runID, runtimeevents.RunStatusCompleted, "answer "+string(rune('0'+i)), "")
	}

	recorder.StartRun(runtimeevents.RunRecord{
		ID:        "run_compact",
		AgentID:   "assistant",
		SessionID: "sess_compact",
		Model:     "test/model",
		Input:     "latest question",
		Status:    runtimeevents.RunStatusRunning,
		StartedAt: startedAt.Add(10 * time.Minute),
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_compact",
		AgentID:   "assistant",
		SessionID: "sess_compact",
		Name:      "session.compact.completed",
		Payload: map[string]any{
			"keep_entries": 4,
			"trigger":      "entry_count",
			"summary":      "compacted summary",
		},
	})
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     "run_compact",
		AgentID:   "assistant",
		SessionID: "sess_compact",
		Name:      "text.delta",
		Payload:   map[string]any{"text": "done"},
	})
	recorder.FinishRun("run_compact", runtimeevents.RunStatusCompleted, "done", "")

	require.NoError(t, service.RebuildFromEvents())

	rebuilt, err := service.Load("assistant", "sess_compact")
	require.NoError(t, err)
	history := rebuilt.History()
	require.NotEmpty(t, history)
	assert.Equal(t, session.EntryTypeCompaction, history[0].Type)
	assert.Equal(t, session.EntryTypeMessage, history[len(history)-1].Type)

	var compact session.CompactionData
	require.NoError(t, json.Unmarshal(history[0].Data, &compact))
	assert.Equal(t, "compacted summary", compact.Text)
	assert.Equal(t, "entry_count", compact.Trigger)

	serialized := session.SerializeHistory(rebuilt)
	require.NotEmpty(t, serialized)
	blob, err := json.Marshal(serialized)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), "question 0")
	assert.Contains(t, string(blob), "latest question")
	assert.Contains(t, string(blob), "done")
}
