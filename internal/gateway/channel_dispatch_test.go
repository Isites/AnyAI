package gateway

import (
	"context"
	"testing"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubChannelPort struct {
	snapshot     []Event
	live         <-chan Event
	runs         map[string]Run
	cancelCalled bool
}

func (*stubChannelPort) channelPort() {}

func (*stubChannelPort) EvaluateMessagePolicy(string, string, InboundMessage) MessagePolicyDecision {
	return MessagePolicyDecision{Accepted: true}
}

func (*stubChannelPort) ResolveIngressAgent(req IngressRequest) string {
	return req.RequestedID
}

func (*stubChannelPort) StartIngressRun(context.Context, IngressRequest) (*ManagedRun, error) {
	return nil, nil
}

func (s *stubChannelPort) SubscribeRunTreeReplay(string) ([]Event, <-chan Event, func(), error) {
	snapshot := append([]Event(nil), s.snapshot...)
	return snapshot, s.live, func() { s.cancelCalled = true }, nil
}

func (s *stubChannelPort) GetRun(runID string) (Run, bool) {
	if s == nil || s.runs == nil {
		return Run{}, false
	}
	run, ok := s.runs[runID]
	return run, ok
}

func TestDispatcherForwardRunTreeReplaysSnapshotBeforeLiveEvents(t *testing.T) {
	now := time.Now().UTC()
	live := make(chan Event, 1)
	live <- Event{
		Name:      runtimeevents.EventRunCompleted,
		RunID:     "run_root",
		AgentID:   "worker",
		SessionID: "sess_child",
		Timestamp: now.Add(time.Second),
		Sequence:  3,
	}
	close(live)

	port := &stubChannelPort{
		snapshot: []Event{
			{
				Name:      runtimeevents.EventRunStarted,
				RunID:     "run_root",
				AgentID:   "lead",
				SessionID: "sess_1",
				Timestamp: now.Add(-time.Second),
				Sequence:  1,
			},
			{
				Name:      runtimeevents.EventRunStarted,
				RunID:     "run_root",
				AgentID:   "worker",
				SessionID: "sess_child",
				Timestamp: now,
				Sequence:  2,
			},
		},
		live: live,
		runs: map[string]Run{
			"run_root": {
				ID:      "run_root",
				AgentID: "lead",
			},
		},
	}

	dispatch := newDispatcher(port, "respond")
	mock := newMockChannel("mock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := dispatch.forwardRunTree(ctx, mock, &ManagedRun{
		RunID:   "run_root",
		AgentID: "lead",
	})
	require.NotNil(t, done)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for run tree forwarding to finish")
	}

	events := mock.Events()
	require.Len(t, events, 2)
	assert.Equal(t, "run_root", events[0].RunID)
	assert.Equal(t, runtimeevents.EventRunStarted, events[0].Name)
	assert.Equal(t, "", events[0].ParentAgentID)
	assert.Equal(t, "run_root", events[1].RunID)
	assert.Equal(t, runtimeevents.EventRunCompleted, events[1].Name)
	assert.True(t, port.cancelCalled)
	for _, event := range events {
		assert.Equal(t, "run_root", event.RunID)
		assert.Equal(t, "worker", event.AgentID)
		assert.Equal(t, "run_root::worker", event.RunNodeID)
		assert.Equal(t, "run_root::lead", event.ParentRunNodeID)
	}
}

func TestChannelResponseBufferUsesOnlyPostToolAssistantText(t *testing.T) {
	tests := []struct {
		name   string
		events []Event
		want   string
	}{
		{
			name: "direct response",
			events: []Event{
				{Name: runtimeevents.EventTextDelta, Payload: map[string]any{"text": "hello "}},
				{Name: runtimeevents.EventTextDelta, Payload: map[string]any{"text": "world"}},
			},
			want: "hello world",
		},
		{
			name: "tool clears progress",
			events: []Event{
				{Name: runtimeevents.EventTextDelta, Payload: map[string]any{"text": "I will inspect."}},
				{Name: runtimeevents.EventToolCallStarted, Payload: map[string]any{"tool": "read_file"}},
				{Name: runtimeevents.EventToolCompleted, Payload: map[string]any{"tool": "read_file"}},
			},
			want: "",
		},
		{
			name: "post tool final response",
			events: []Event{
				{Name: runtimeevents.EventTextDelta, Payload: map[string]any{"text": "I will inspect."}},
				{Name: runtimeevents.EventToolCompleted, Payload: map[string]any{"tool": "read_file"}},
				{Name: runtimeevents.EventTextDelta, Payload: map[string]any{"text": "Done."}},
			},
			want: "Done.",
		},
		{
			name: "completion tool preserves final response",
			events: []Event{
				{Name: runtimeevents.EventTextDelta, Payload: map[string]any{"text": "Done."}},
				{Name: runtimeevents.EventToolCallStarted, Payload: map[string]any{"tool": "goal_complete"}},
				{Name: runtimeevents.EventToolCompleted, Payload: map[string]any{"tool": "goal_complete"}},
			},
			want: "Done.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buffer channelResponseBuffer
			for _, event := range tt.events {
				buffer.Observe(event)
			}
			assert.Equal(t, tt.want, buffer.Final())
		})
	}
}
