package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRuntimeSurface struct {
	run             runtimeevents.RunRecord
	trace           runtimeevents.RunTreeRecord
	tree            []runtimeevents.RunNode
	runSub          chan runtimeevents.EventRecord
	runSubscribed   chan struct{}
	getRunGate      chan struct{}
	traceSub        chan runtimeevents.EventRecord
	traceSubscribed chan struct{}
	getTraceGate    chan struct{}
}

func TestSessionIDForInboundMessageHonorsExplicitSessionID(t *testing.T) {
	msg := InboundMessage{
		SenderID:  "local",
		SessionID: "cli_custom",
		ChatType:  ChatTypeDirect,
	}

	assert.Equal(t, "cli_custom", sessionIDForInboundMessage("cli", msg))
	req := ingressRequestForInboundMessage("cli", msg)
	assert.Equal(t, "cli_custom", req.SessionID)
}

func TestChannelRunEventCarriesTraceIdentity(t *testing.T) {
	dispatch := &dispatcher{runtime: &stubChannelPort{
		runs: map[string]Run{
			"run_child": gatewayRun(runtimeevents.RunRecord{
				ID:                "run_child",
				TraceID:           "trace_root",
				TraceNodeID:       "node_child",
				ParentTraceNodeID: "node_parent",
				AgentID:           "coder",
				SessionID:         "sess",
				ParentAgentID:     "lead",
			}),
		},
	}}

	event := dispatch.channelRunEventFromRecord(Event{
		RunID:     "run_child",
		AgentID:   "coder",
		SessionID: "sess",
		Name:      runtimeevents.EventRunStarted,
	})

	assert.Equal(t, "trace_root", event.TraceID)
	assert.Equal(t, "node_child", event.TraceNodeID)
	assert.Equal(t, "node_parent", event.ParentTraceNodeID)
	assert.Equal(t, "lead", event.ParentAgentID)
}

func (s *stubRuntimeSurface) Config() *config.Config               { return nil }
func (s *stubRuntimeSurface) Agents() []config.AgentConfig         { return nil }
func (s *stubRuntimeSurface) Resources() *runtimeresources.Catalog { return nil }
func (s *stubRuntimeSurface) JobScheduler() tools.JobScheduler     { return nil }
func (s *stubRuntimeSurface) EventStorageDir() string              { return "" }
func (s *stubRuntimeSurface) RebuildEventProjections() error       { return nil }
func (s *stubRuntimeSurface) StartManagedRun(context.Context, runtimeport.RunRequest) (*runtimeport.ManagedRun, error) {
	return nil, nil
}
func (s *stubRuntimeSurface) StartIngressRun(context.Context, runtimeport.IngressRequest) (*runtimeport.ManagedRun, error) {
	return nil, nil
}
func (s *stubRuntimeSurface) StartTextRun(context.Context, string, string, string, string, string, string, string, runtimeport.ChatType) (*runtimeport.ManagedRun, error) {
	return nil, nil
}
func (s *stubRuntimeSurface) DoTask(context.Context, task.Spec) (string, error) { return "", nil }
func (s *stubRuntimeSurface) GetRun(runID string) (runtimeevents.RunRecord, bool) {
	if s.getRunGate != nil {
		<-s.getRunGate
	}
	if s.run.ID == runID {
		return s.run, true
	}
	return runtimeevents.RunRecord{}, false
}
func (s *stubRuntimeSurface) ListRuns() []runtimeevents.RunRecord                 { return nil }
func (s *stubRuntimeSurface) ListRawRunEvents(string) []runtimeevents.EventRecord { return nil }
func (s *stubRuntimeSurface) GetRawRunTree(string) (runtimeevents.RunTreeRecord, bool) {
	return runtimeevents.RunTreeRecord{}, false
}
func (s *stubRuntimeSurface) RawRunTree(string) ([]runtimeevents.RunNode, bool) { return nil, false }
func (s *stubRuntimeSurface) ListRunEvents(runID string) []runtimeevents.EventRecord {
	run, ok := s.GetRun(runID)
	events := s.ListRawRunEvents(runID)
	if !ok {
		return events
	}
	return runtimeevents.ReplayRunEvents(run, events)
}
func (s *stubRuntimeSurface) SubscribeRawRun(string) (<-chan runtimeevents.EventRecord, func(), error) {
	if s.runSubscribed != nil {
		close(s.runSubscribed)
		s.runSubscribed = nil
	}
	if s.runSub == nil {
		ch := make(chan runtimeevents.EventRecord)
		close(ch)
		return ch, func() {}, nil
	}
	return s.runSub, func() {}, nil
}
func (s *stubRuntimeSurface) CancelRun(string) error { return nil }
func (s *stubRuntimeSurface) GetRunTree(runID string) (runtimeevents.RunTreeRecord, bool) {
	if s.getTraceGate != nil {
		<-s.getTraceGate
	}
	for _, run := range s.trace.Runs {
		if run.ID == runID {
			return runtimeevents.ReplayRunTreeRecord(s.trace), true
		}
	}
	if len(s.trace.Runs) == 0 && len(s.trace.Events) > 0 {
		for _, event := range s.trace.Events {
			if event.RunID == runID {
				return runtimeevents.ReplayRunTreeRecord(s.trace), true
			}
		}
	}
	return runtimeevents.RunTreeRecord{}, false
}
func (s *stubRuntimeSurface) RunTree(runID string) ([]runtimeevents.RunNode, bool) {
	for _, node := range s.tree {
		if node.Run.ID == runID {
			return runtimeevents.ReplayRunTree(s.tree), true
		}
	}
	if len(s.tree) == 0 && len(s.trace.Events) > 0 {
		for _, event := range s.trace.Events {
			if event.RunID == runID {
				return runtimeevents.ReplayRunTree(s.tree), true
			}
		}
	}
	return nil, false
}
func (s *stubRuntimeSurface) SubscribeRawRunTree(string) (<-chan runtimeevents.EventRecord, func(), error) {
	if s.traceSubscribed != nil {
		close(s.traceSubscribed)
		s.traceSubscribed = nil
	}
	if s.traceSub == nil {
		ch := make(chan runtimeevents.EventRecord)
		close(ch)
		return ch, func() {}, nil
	}
	return s.traceSub, func() {}, nil
}
func (s *stubRuntimeSurface) SubscribeRunReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	return stubSubscribeReplayStream(
		func() (<-chan runtimeevents.EventRecord, func(), error) {
			return s.SubscribeRawRun(runID)
		},
		func() ([]runtimeevents.EventRecord, error) {
			run, ok := s.GetRun(runID)
			if !ok {
				return nil, assert.AnError
			}
			return runtimeevents.ReplayRunEvents(run, s.ListRawRunEvents(runID)), nil
		},
	)
}
func (s *stubRuntimeSurface) SubscribeRunTreeReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	return stubSubscribeReplayStream(
		func() (<-chan runtimeevents.EventRecord, func(), error) {
			return s.SubscribeRawRunTree(runID)
		},
		func() ([]runtimeevents.EventRecord, error) {
			tree, ok := s.GetRunTree(runID)
			if !ok {
				return nil, assert.AnError
			}
			replayed := runtimeevents.ReplayRunTreeRecord(tree)
			return append([]runtimeevents.EventRecord(nil), replayed.Events...), nil
		},
	)
}
func (s *stubRuntimeSurface) ListSessionEvents(string, string) []runtimeevents.EventRecord {
	return nil
}
func (s *stubRuntimeSurface) ListSessions(string) ([]session.SessionInfo, error)   { return nil, nil }
func (s *stubRuntimeSurface) LoadSession(string, string) (*session.Session, error) { return nil, nil }
func (s *stubRuntimeSurface) LoadSessionSnapshot(string, string) (runtimeport.SessionSnapshot, error) {
	return runtimeport.SessionSnapshot{}, nil
}
func (s *stubRuntimeSurface) CreateSession(string, string, string) (string, error) { return "", nil }
func (s *stubRuntimeSurface) DeleteSession(string, string) error                   { return nil }
func (s *stubRuntimeSurface) SubscribeSession(string, string) (<-chan runtimeevents.EventRecord, func(), error) {
	return nil, func() {}, nil
}
func (s *stubRuntimeSurface) ListTasks() []task.Info           { return nil }
func (s *stubRuntimeSurface) GetTask(string) (task.Info, bool) { return task.Info{}, false }
func (s *stubRuntimeSurface) SubscribeTask(string) (<-chan runtimeevents.EventRecord, func(), error) {
	return nil, func() {}, nil
}
func (s *stubRuntimeSurface) CancelTask(string) error   { return nil }
func (s *stubRuntimeSurface) MemoryStats() memory.Stats { return memory.Stats{} }
func (s *stubRuntimeSurface) MemorySearch(string, int, memory.SearchScope, ...memory.Layer) []memory.SearchMatch {
	return nil
}
func (s *stubRuntimeSurface) MemoryGet(string, memory.SearchScope) (memory.Entry, bool) {
	return memory.Entry{}, false
}
func (s *stubRuntimeSurface) MemoryStaleCleanup(time.Time) (int, error)    { return 0, nil }
func (s *stubRuntimeSurface) MemoryReindex() (int, error)                  { return 0, nil }
func (s *stubRuntimeSurface) MemoryPromoteEligible(time.Time) (int, error) { return 0, nil }
func (s *stubRuntimeSurface) CompactSession(string, string, int) error     { return nil }

func stubReplaySeenSet(events []runtimeevents.EventRecord) map[string]struct{} {
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		seen[stubReplayEventKey(event)] = struct{}{}
	}
	return seen
}

func stubReplayEventKey(event runtimeevents.EventRecord) string {
	return fmt.Sprintf("%s/%d/%s", event.RunID, event.Sequence, event.Name)
}

func stubSubscribeReplayStream(
	subscribe func() (<-chan runtimeevents.EventRecord, func(), error),
	loadSnapshot func() ([]runtimeevents.EventRecord, error),
) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	raw, rawCancel, err := subscribe()
	if err != nil || rawCancel == nil {
		return nil, nil, nil, err
	}

	out := make(chan runtimeevents.EventRecord, 128)
	done := make(chan struct{})
	release := make(chan map[string]struct{}, 1)

	go func() {
		defer close(out)
		backlog := make([]runtimeevents.EventRecord, 0, 16)
		seen := map[string]struct{}{}
		ready := false
		emit := func(event runtimeevents.EventRecord) bool {
			key := stubReplayEventKey(event)
			if _, ok := seen[key]; ok {
				return true
			}
			seen[key] = struct{}{}
			select {
			case <-done:
				return false
			case out <- event:
				return true
			}
		}
		for {
			if !ready {
				select {
				case <-done:
					return
				case snapshotSeen := <-release:
					seen = snapshotSeen
					for _, event := range backlog {
						if !emit(event) {
							return
						}
					}
					backlog = nil
					ready = true
				case event, ok := <-raw:
					if !ok {
						return
					}
					backlog = append(backlog, event)
				}
				continue
			}
			select {
			case <-done:
				return
			case event, ok := <-raw:
				if !ok {
					return
				}
				if !emit(event) {
					return
				}
			}
		}
	}()

	snapshot, err := loadSnapshot()
	if err != nil {
		close(done)
		rawCancel()
		return nil, nil, nil, err
	}
	release <- stubReplaySeenSet(snapshot)

	cancel := func() {
		close(done)
		rawCancel()
	}
	return snapshot, out, cancel, nil
}

func TestServiceGetRunTreeReplaysTransientRunOutput(t *testing.T) {
	now := time.Now().UTC()
	rt := &stubRuntimeSurface{
		trace: runtimeevents.RunTreeRecord{
			Runs: []runtimeevents.RunRecord{
				{
					ID:          "run_1",
					AgentID:     "assistant",
					SessionID:   "sess_1",
					Status:      runtimeevents.RunStatusCompleted,
					Output:      "hello from trace replay",
					StartedAt:   now.Add(-time.Second),
					CompletedAt: now,
				},
			},
			Events: []runtimeevents.EventRecord{
				{Name: runtimeevents.EventRunStarted, RunID: "run_1", Timestamp: now.Add(-time.Second), Sequence: 1},
				{Name: runtimeevents.EventRunCompleted, RunID: "run_1", Timestamp: now, Sequence: 2},
			},
		},
	}

	service := &Service{runtime: rt}
	trace, ok := service.GetRunTree("run_1")
	require.True(t, ok)
	require.Len(t, trace.Events, 3)
	assert.Equal(t, runtimeevents.EventTextDelta, trace.Events[1].Name)
	assert.Equal(t, "hello from trace replay", trace.Events[1].Payload["text"])
}

func TestServiceRunTreeReplaysNodeEvents(t *testing.T) {
	now := time.Now().UTC()
	rt := &stubRuntimeSurface{
		trace: runtimeevents.RunTreeRecord{},
		tree: []runtimeevents.RunNode{
			{
				Run: runtimeevents.RunRecord{
					ID:          "run_root",
					AgentID:     "assistant",
					SessionID:   "sess_1",
					Status:      runtimeevents.RunStatusCompleted,
					Output:      "root output",
					CompletedAt: now,
				},
				Events: []runtimeevents.EventRecord{
					{Name: runtimeevents.EventRunStarted, RunID: "run_root", Timestamp: now.Add(-time.Second), Sequence: 1},
					{Name: runtimeevents.EventRunCompleted, RunID: "run_root", Timestamp: now, Sequence: 2},
				},
				Children: []runtimeevents.RunNode{
					{
						Run: runtimeevents.RunRecord{
							ID:          "run_child",
							AgentID:     "worker",
							SessionID:   "sess_2",
							Status:      runtimeevents.RunStatusCompleted,
							Output:      "child output",
							CompletedAt: now.Add(time.Second),
						},
						Events: []runtimeevents.EventRecord{
							{Name: runtimeevents.EventRunStarted, RunID: "run_child", Timestamp: now, Sequence: 1},
							{Name: runtimeevents.EventRunCompleted, RunID: "run_child", Timestamp: now.Add(time.Second), Sequence: 2},
						},
					},
				},
			},
		},
	}

	service := &Service{runtime: rt}
	tree, ok := service.RunTree("run_root")
	require.True(t, ok)
	require.Len(t, tree, 1)
	require.Len(t, tree[0].Events, 3)
	require.Len(t, tree[0].Children, 1)
	require.Len(t, tree[0].Children[0].Events, 3)
	assert.Equal(t, runtimeevents.EventTextDelta, tree[0].Events[1].Name)
	assert.Equal(t, runtimeevents.EventTextDelta, tree[0].Children[0].Events[1].Name)
}

func TestServiceSubscribeRunTreeReplayDedupesBufferedReplayEvents(t *testing.T) {
	now := time.Now().UTC()
	traceSub := make(chan runtimeevents.EventRecord, 2)
	rt := &stubRuntimeSurface{
		traceSub:        traceSub,
		traceSubscribed: make(chan struct{}),
		getTraceGate:    make(chan struct{}),
		trace: runtimeevents.RunTreeRecord{
			Runs: []runtimeevents.RunRecord{
				{
					ID:          "run_1",
					AgentID:     "assistant",
					SessionID:   "sess_1",
					Status:      runtimeevents.RunStatusCompleted,
					Output:      "hello from trace replay",
					StartedAt:   now.Add(-time.Second),
					CompletedAt: now,
				},
			},
			Events: []runtimeevents.EventRecord{
				{Name: runtimeevents.EventRunStarted, RunID: "run_1", Timestamp: now.Add(-time.Second), Sequence: 1},
				{Name: runtimeevents.EventRunCompleted, RunID: "run_1", Timestamp: now, Sequence: 3},
			},
		},
	}

	service := &Service{runtime: rt}
	type result struct {
		snapshot []Event
		events   <-chan Event
		cancel   func()
		err      error
	}
	results := make(chan result, 1)
	go func() {
		snapshot, events, cancel, err := service.SubscribeRunTreeReplay("run_1")
		results <- result{snapshot: snapshot, events: events, cancel: cancel, err: err}
	}()

	<-rt.traceSubscribed
	traceSub <- runtimeevents.EventRecord{
		Name:      runtimeevents.EventTextDelta,
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Timestamp: now,
		Sequence:  2,
		Payload:   map[string]any{"text": "hello from trace replay"},
	}
	close(rt.getTraceGate)

	res := <-results
	require.NoError(t, res.err)
	require.Len(t, res.snapshot, 3)
	assert.Equal(t, runtimeevents.EventTextDelta, res.snapshot[1].Name)

	close(traceSub)
	select {
	case event, ok := <-res.events:
		if ok {
			t.Fatalf("unexpected replay duplicate event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replay stream to close")
	}
	if res.cancel != nil {
		res.cancel()
	}
}

func TestServiceSubscribeRunTreeReplayFlushesBufferedLiveEvents(t *testing.T) {
	now := time.Now().UTC()
	traceSub := make(chan runtimeevents.EventRecord, 2)
	rt := &stubRuntimeSurface{
		traceSub:        traceSub,
		traceSubscribed: make(chan struct{}),
		getTraceGate:    make(chan struct{}),
		trace: runtimeevents.RunTreeRecord{
			Runs: []runtimeevents.RunRecord{
				{
					ID:        "run_1",
					AgentID:   "assistant",
					SessionID: "sess_1",
					Status:    runtimeevents.RunStatusRunning,
					StartedAt: now.Add(-time.Second),
				},
			},
			Events: []runtimeevents.EventRecord{
				{Name: runtimeevents.EventRunStarted, RunID: "run_1", Timestamp: now.Add(-time.Second), Sequence: 1},
			},
		},
	}

	service := &Service{runtime: rt}
	type result struct {
		snapshot []Event
		events   <-chan Event
		cancel   func()
		err      error
	}
	results := make(chan result, 1)
	go func() {
		snapshot, events, cancel, err := service.SubscribeRunTreeReplay("run_1")
		results <- result{snapshot: snapshot, events: events, cancel: cancel, err: err}
	}()

	<-rt.traceSubscribed
	traceSub <- runtimeevents.EventRecord{
		Name:      runtimeevents.EventTextDelta,
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Timestamp: now,
		Sequence:  2,
		Payload:   map[string]any{"text": "live buffered text"},
	}
	close(rt.getTraceGate)

	res := <-results
	require.NoError(t, res.err)
	require.Len(t, res.snapshot, 1)
	assert.Equal(t, runtimeevents.EventRunStarted, res.snapshot[0].Name)

	select {
	case event := <-res.events:
		assert.Equal(t, runtimeevents.EventTextDelta, event.Name)
		assert.Equal(t, "live buffered text", event.Payload["text"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for buffered live trace event")
	}
	if res.cancel != nil {
		res.cancel()
	}
}

func TestServiceSubscribeRunReplayDedupesBufferedReplayEvents(t *testing.T) {
	now := time.Now().UTC()
	runSub := make(chan runtimeevents.EventRecord, 2)
	rt := &stubRuntimeSurface{
		runSub:        runSub,
		runSubscribed: make(chan struct{}),
		getRunGate:    make(chan struct{}),
		run: runtimeevents.RunRecord{
			ID:          "run_1",
			AgentID:     "assistant",
			SessionID:   "sess_1",
			Status:      runtimeevents.RunStatusCompleted,
			Output:      "hello from run replay",
			StartedAt:   now.Add(-time.Second),
			CompletedAt: now,
		},
	}

	service := &Service{runtime: rt}
	type result struct {
		snapshot []Event
		events   <-chan Event
		cancel   func()
		err      error
	}
	results := make(chan result, 1)
	go func() {
		snapshot, events, cancel, err := service.SubscribeRunReplay("run_1")
		results <- result{snapshot: snapshot, events: events, cancel: cancel, err: err}
	}()

	<-rt.runSubscribed
	runSub <- runtimeevents.EventRecord{
		Name:      runtimeevents.EventTextDelta,
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Timestamp: now,
		Sequence:  1,
		Payload:   map[string]any{"text": "hello from run replay"},
	}
	close(rt.getRunGate)

	res := <-results
	require.NoError(t, res.err)
	require.Len(t, res.snapshot, 2)
	assert.Equal(t, runtimeevents.EventTextDelta, res.snapshot[0].Name)
	assert.Equal(t, runtimeevents.EventRunCompleted, res.snapshot[1].Name)

	close(runSub)
	select {
	case event, ok := <-res.events:
		if ok {
			t.Fatalf("unexpected replay duplicate run event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replay run stream to close")
	}
	if res.cancel != nil {
		res.cancel()
	}
}

func TestServiceSubscribeRunReplayFlushesBufferedLiveEvents(t *testing.T) {
	now := time.Now().UTC()
	runSub := make(chan runtimeevents.EventRecord, 2)
	rt := &stubRuntimeSurface{
		runSub:        runSub,
		runSubscribed: make(chan struct{}),
		getRunGate:    make(chan struct{}),
		run: runtimeevents.RunRecord{
			ID:        "run_1",
			AgentID:   "assistant",
			SessionID: "sess_1",
			Status:    runtimeevents.RunStatusRunning,
			StartedAt: now.Add(-time.Second),
		},
	}

	service := &Service{runtime: rt}
	type result struct {
		snapshot []Event
		events   <-chan Event
		cancel   func()
		err      error
	}
	results := make(chan result, 1)
	go func() {
		snapshot, events, cancel, err := service.SubscribeRunReplay("run_1")
		results <- result{snapshot: snapshot, events: events, cancel: cancel, err: err}
	}()

	<-rt.runSubscribed
	runSub <- runtimeevents.EventRecord{
		Name:      runtimeevents.EventTextDelta,
		RunID:     "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Timestamp: now,
		Sequence:  1,
		Payload:   map[string]any{"text": "buffered live run text"},
	}
	close(rt.getRunGate)

	res := <-results
	require.NoError(t, res.err)
	assert.Empty(t, res.snapshot)

	select {
	case event := <-res.events:
		assert.Equal(t, runtimeevents.EventTextDelta, event.Name)
		assert.Equal(t, "buffered live run text", event.Payload["text"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for buffered live run event")
	}
	if res.cancel != nil {
		res.cancel()
	}
}
