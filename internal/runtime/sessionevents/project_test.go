package sessionevents

import (
	"encoding/json"
	"testing"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHistoryEventRecordsProjectsCallAgentLifecycleFromSessionRecords(t *testing.T) {
	history := []session.SessionEntry{
		session.ApplyEntryRefs(session.ToolCallEntry("call_1", "callagent", json.RawMessage(`{"target_agent":"coder","task":"实现修复"}`)), session.EntryRefs{RunID: "run_parent"}),
		session.ApplyEntryRefs(session.ToolResultEntry("call_1", `{"target_agent":"coder","status":"completed","summary":"修复已完成","run_id":"run_child","session_id":"sess_child"}`, "", nil), session.EntryRefs{RunID: "run_parent"}),
	}
	for i := range history {
		history[i].Timestamp = time.Unix(1700000000+int64(i), 0).Unix()
	}

	events := HistoryEventRecords("lead", "sess_parent", history)
	require.Len(t, events, 4)

	assert.Equal(t, runtimeevents.EventToolCallStarted, events[0].Name)
	assert.Equal(t, "callagent", events[0].Payload["tool"])
	assert.Equal(t, runtimeevents.EventAgentCallStarted, events[1].Name)
	assert.Equal(t, "coder", events[1].Payload["target_agent"])
	assert.Equal(t, runtimeevents.EventToolCompleted, events[2].Name)
	assert.Equal(t, runtimeevents.EventAgentCallCompleted, events[3].Name)
	assert.Equal(t, "coder", events[3].Payload["target_agent"])
	assert.Equal(t, "修复已完成", events[3].Payload["summary"])
	assert.Equal(t, "run_parent", events[3].RunID)
}

func TestHistoryEventRecordsPreservesTaskRunNodeForSessionOutput(t *testing.T) {
	entry := session.ApplyEntryRefs(
		session.AssistantMessageEntry("done"),
		session.EntryRefs{RunID: "run_parent", TaskID: "task_child"},
	)
	entry.Timestamp = time.Unix(1700000000, 0).Unix()

	events := HistoryEventRecords("worker", "sess_worker", []session.SessionEntry{entry})
	require.Len(t, events, 1)

	assert.Equal(t, runtimeevents.EventSessionOutputStored, events[0].Name)
	assert.Equal(t, "task_child", events[0].Payload["task_id"])
	assert.Equal(t, runtimeevents.RunNodeID("run_parent", "worker", "task_child"), events[0].RunNodeID)
	assert.Equal(t, "done", events[0].Payload["text"])
}
