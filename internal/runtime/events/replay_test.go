package runtimeevents

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplayRunEventsInjectsFinalOutputBeforeTerminalEvent(t *testing.T) {
	run := RunRecord{
		ID:          "run_1",
		AgentID:     "assistant",
		SessionID:   "sess_1",
		Status:      RunStatusCompleted,
		Output:      "hello from replay",
		CompletedAt: time.Now().UTC(),
	}
	events := []EventRecord{
		{Name: EventRunStarted, Sequence: 1},
		{Name: EventRunCompleted, Sequence: 2},
	}

	replay := ReplayRunEvents(run, events)
	require.Len(t, replay, 3)
	assert.Equal(t, EventRunStarted, replay[0].Name)
	assert.Equal(t, EventTextDelta, replay[1].Name)
	assert.Equal(t, "hello from replay", replay[1].Payload["text"])
	assert.Equal(t, EventRunCompleted, replay[2].Name)
	assert.Equal(t, 1, replay[0].Sequence)
	assert.Equal(t, 2, replay[1].Sequence)
	assert.Equal(t, 3, replay[2].Sequence)
}

func TestReplayRunEventsSynthesizesTerminalEventWhenMissing(t *testing.T) {
	run := RunRecord{
		ID:          "run_2",
		AgentID:     "assistant",
		SessionID:   "sess_2",
		Status:      RunStatusCompleted,
		Output:      "merged followup reply",
		CompletedAt: time.Now().UTC(),
	}
	events := []EventRecord{
		{Name: EventRunQueued, Sequence: 1},
	}

	replay := ReplayRunEvents(run, events)
	require.Len(t, replay, 3)
	assert.Equal(t, EventRunQueued, replay[0].Name)
	assert.Equal(t, EventTextDelta, replay[1].Name)
	assert.Equal(t, EventRunCompleted, replay[2].Name)
}

func TestReplayRunEventsDoesNotDuplicatePersistedTextDelta(t *testing.T) {
	run := RunRecord{
		ID:          "run_3",
		AgentID:     "assistant",
		SessionID:   "sess_3",
		Status:      RunStatusCompleted,
		Output:      "already streamed",
		CompletedAt: time.Now().UTC(),
	}
	events := []EventRecord{
		{Name: EventRunStarted, Sequence: 1},
		{Name: EventTextDelta, Sequence: 2, Payload: map[string]any{"text": "already streamed"}},
		{Name: EventRunCompleted, Sequence: 3},
	}

	replay := ReplayRunEvents(run, events)
	require.Len(t, replay, 3)
	assert.Equal(t, EventTextDelta, replay[1].Name)
	assert.Equal(t, 2, replay[1].Sequence)
}

func TestReplayRunTreeRecordReplaysEachRunIntoCombinedEvents(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Minute)
	completedAt := startedAt.Add(10 * time.Second)
	childCompletedAt := completedAt.Add(5 * time.Second)
	tree := RunTreeRecord{
		Runs: []RunRecord{
			{
				ID:          "run_parent",
				AgentID:     "lead",
				SessionID:   "sess_parent",
				Status:      RunStatusCompleted,
				Output:      "parent output",
				StartedAt:   startedAt,
				CompletedAt: completedAt,
			},
		},
		Events: []EventRecord{
			{Name: EventRunStarted, RunID: "run_parent", Timestamp: startedAt, Sequence: 1},
			{Name: EventRunStarted, RunID: "run_parent", AgentID: "worker", SessionID: "sess_child", Timestamp: completedAt, Sequence: 2},
			{Name: EventRunCompleted, RunID: "run_parent", Timestamp: completedAt, Sequence: 3},
			{Name: EventRunCompleted, RunID: "run_parent", AgentID: "worker", SessionID: "sess_child", Timestamp: childCompletedAt, Sequence: 4},
		},
	}

	replay := ReplayRunTreeRecord(tree)
	require.Len(t, replay.Events, 5)
	assert.Equal(t, []string{
		EventRunStarted,
		EventRunStarted,
		EventTextDelta,
		EventRunCompleted,
		EventRunCompleted,
	}, eventNamesForReplay(replay.Events))
}

func TestReplayRunTreeReplaysNodeEventsRecursively(t *testing.T) {
	now := time.Now().UTC()
	tree := []RunNode{
		{
			Run: RunRecord{
				ID:          "run_parent",
				AgentID:     "lead",
				SessionID:   "sess_parent",
				Status:      RunStatusCompleted,
				Output:      "parent output",
				CompletedAt: now,
			},
			Events: []EventRecord{
				{Name: EventRunStarted, RunID: "run_parent", Timestamp: now.Add(-time.Second), Sequence: 1},
				{Name: EventRunCompleted, RunID: "run_parent", Timestamp: now, Sequence: 2},
			},
			Children: []RunNode{
				{
					Run: RunRecord{
						ID:          "run_child",
						AgentID:     "worker",
						SessionID:   "sess_child",
						Status:      RunStatusCompleted,
						Output:      "child output",
						CompletedAt: now.Add(time.Second),
					},
					Events: []EventRecord{
						{Name: EventRunStarted, RunID: "run_child", Timestamp: now, Sequence: 1},
						{Name: EventRunCompleted, RunID: "run_child", Timestamp: now.Add(time.Second), Sequence: 2},
					},
				},
			},
		},
	}

	replay := ReplayRunTree(tree)
	require.Len(t, replay, 1)
	require.Len(t, replay[0].Events, 3)
	require.Len(t, replay[0].Children, 1)
	require.Len(t, replay[0].Children[0].Events, 3)
	assert.Equal(t, EventTextDelta, replay[0].Events[1].Name)
	assert.Equal(t, EventTextDelta, replay[0].Children[0].Events[1].Name)
}

func eventNamesForReplay(events []EventRecord) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return names
}
