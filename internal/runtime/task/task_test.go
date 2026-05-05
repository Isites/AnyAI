package task

import (
	"context"
	"sync"
	"testing"
	"time"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	"github.com/Isites/anyai/internal/runtime/turn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockExecutor is a test executor that returns fixed results.
type mockExecutor struct {
	kind   Kind
	result Result
	delay  time.Duration
}

func (m *mockExecutor) Kind() Kind {
	return m.kind
}

func (m *mockExecutor) Execute(ctx context.Context, task Record) (Result, error) {
	if m.delay > 0 {
		select {
		case <-ctx.Done():
			return Result{Status: StatusCancelled, Error: ctx.Err().Error()}, ctx.Err()
		case <-time.After(m.delay):
		}
	}
	return m.result, nil
}

type blockingExecutor struct {
	kind      Kind
	started   chan struct{}
	cancelled chan struct{}
}

func (b *blockingExecutor) Kind() Kind {
	return b.kind
}

func (b *blockingExecutor) Execute(ctx context.Context, task Record) (Result, error) {
	notify(b.started)
	<-ctx.Done()
	notify(b.cancelled)
	return Result{Status: StatusCancelled, Error: ctx.Err().Error()}, ctx.Err()
}

type heartbeatingExecutor struct {
	kind       Kind
	heartbeats int
	delay      time.Duration
	result     Result
}

func (h *heartbeatingExecutor) Kind() Kind {
	return h.kind
}

func (h *heartbeatingExecutor) Execute(ctx context.Context, task Record) (Result, error) {
	for i := 0; i < h.heartbeats; i++ {
		select {
		case <-ctx.Done():
			return Result{Status: StatusCancelled, Error: ctx.Err().Error()}, ctx.Err()
		case <-time.After(h.delay):
			runtimeactivity.Emit(ctx, map[string]any{
				"heartbeat": i + 1,
			})
		}
	}
	if h.result.Status == "" {
		h.result.Status = StatusCompleted
	}
	return h.result, nil
}

type retryingExecutor struct {
	kind      Kind
	mu        sync.Mutex
	attempts  int
	firstWait time.Duration
	result    Result
}

func (r *retryingExecutor) Kind() Kind {
	return r.kind
}

func (r *retryingExecutor) Execute(ctx context.Context, task Record) (Result, error) {
	r.mu.Lock()
	r.attempts++
	attempt := r.attempts
	r.mu.Unlock()

	if attempt == 1 && r.firstWait > 0 {
		select {
		case <-ctx.Done():
			return Result{Status: StatusCancelled, Error: ctx.Err().Error()}, ctx.Err()
		case <-time.After(r.firstWait):
		}
	}
	if r.result.Status == "" {
		r.result.Status = StatusCompleted
	}
	return r.result, nil
}

func notify(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func TestNewRuntime(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	require.NotNil(t, rt)
	assert.Same(t, store, rt.Store())
	assert.Same(t, registry, rt.Registry())
}

func TestExecutorRegistry(t *testing.T) {
	registry := NewExecutorRegistry()
	require.NotNil(t, registry)

	// Test registration
	exec := &mockExecutor{kind: KindAgent, result: Result{Status: StatusCompleted}}
	err := registry.Register(KindAgent, exec)
	require.NoError(t, err)

	// Test duplicate registration fails
	err = registry.Register(KindAgent, &mockExecutor{})
	assert.Error(t, err)

	// Test get
	retrieved := registry.Get(KindAgent)
	assert.Same(t, exec, retrieved)

	// Test has
	assert.True(t, registry.Has(KindAgent))
	assert.False(t, registry.Has(KindTool))

	// Test list
	kinds := registry.List()
	assert.Contains(t, kinds, KindAgent)
}

func TestRuntimeDoTask(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	// Register executor
	exec := &mockExecutor{
		kind: KindAgent,
		result: Result{
			Status:  StatusCompleted,
			Summary: "done",
		},
	}
	require.NoError(t, registry.Register(KindAgent, exec))

	// Submit task
	spec := Spec{
		Kind:    KindAgent,
		Input:   "test task",
		AgentID: "test-agent",
	}
	done := make(chan Completion, 1)
	spec.OnComplete = func(_ context.Context, completion Completion) {
		done <- completion
	}
	taskID, err := rt.DoTask(context.Background(), spec)
	require.NoError(t, err)
	assert.NotEmpty(t, taskID)

	completion := waitCompletion(t, done)
	assert.Equal(t, taskID, completion.TaskID)
	assert.Equal(t, StatusCompleted, completion.Record.Status)
	assert.Equal(t, "done", completion.Record.Summary)
}

func TestRuntimeDoTaskInvokesCompletionCallback(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	exec := &mockExecutor{
		kind: KindAgent,
		result: Result{
			Status:  StatusCompleted,
			Summary: "done",
		},
		delay: 10 * time.Millisecond,
	}
	require.NoError(t, registry.Register(KindAgent, exec))

	done := make(chan Completion, 1)
	taskID, err := rt.DoTask(context.Background(), Spec{
		Kind:    KindAgent,
		Input:   "test task",
		AgentID: "test-agent",
		OnComplete: func(_ context.Context, completion Completion) {
			done <- completion
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, taskID)

	completion := waitCompletion(t, done)
	assert.Equal(t, StatusCompleted, completion.Record.Status)
	assert.Equal(t, "done", completion.Record.Summary)
	assert.Equal(t, StatusCompleted, completion.Result.Status)
	assert.Equal(t, "done", completion.Result.Summary)
}

func TestRuntimeJoinAllSettled(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	exec := &mockExecutor{
		kind: KindTool,
		result: Result{
			Status:  StatusCompleted,
			Summary: "tool result",
		},
		delay: 10 * time.Millisecond,
	}
	require.NoError(t, registry.Register(KindTool, exec))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	joined, err := rt.JoinAllSettled(ctx, JoinSpec{
		Count:       3,
		MaxParallel: 2,
	}, func(joinCtx context.Context, index int, settle func(Record)) {
		_, submitErr := rt.DoTask(joinCtx, Spec{
			Kind:     KindTool,
			Input:    "test",
			ToolName: "test_tool",
			OnComplete: func(_ context.Context, completion Completion) {
				settle(completion.Record)
			},
		})
		if submitErr != nil {
			settle(Record{Status: StatusFailed, Error: submitErr.Error()})
			return
		}
	})
	require.NoError(t, err)
	assert.Len(t, joined.Results, 3)
	assert.Equal(t, 2, joined.MaxParallel)

	for _, result := range joined.Results {
		assert.Equal(t, StatusCompleted, result.Record.Status)
		assert.Equal(t, "tool result", result.Record.Summary)
		assert.NotEmpty(t, result.Record.ID)
		assert.Positive(t, result.StartedOrder)
		assert.Positive(t, result.FinishOrder)
	}
}

func TestRuntimeCancel(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	exec := &blockingExecutor{
		kind:      KindAgent,
		started:   make(chan struct{}, 1),
		cancelled: make(chan struct{}, 1),
	}
	require.NoError(t, registry.Register(KindAgent, exec))

	spec := Spec{
		Kind:    KindAgent,
		Input:   "slow task",
		AgentID: "test-agent",
	}
	taskID, err := rt.DoTask(context.Background(), spec)
	require.NoError(t, err)

	select {
	case <-exec.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}

	cancelled := rt.Cancel(taskID)
	assert.True(t, cancelled)

	select {
	case <-exec.cancelled:
	case <-time.After(time.Second):
		t.Fatal("executor did not receive cancellation")
	}

	record, ok := store.Get(taskID)
	assert.True(t, ok)
	assert.Equal(t, StatusCancelled, record.Status)
	assert.Equal(t, "cancelled", record.Error)
	assert.Nil(t, store.cancelFor(taskID))
}

func TestRuntimeDoTaskEventPayloadUsesInputOnly(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	var events []runtimeevents.EventRecord
	rt := NewRuntime(store, registry)
	rt.Store().SetEventAppender(func(event runtimeevents.EventRecord) {
		events = append(events, event)
	})

	exec := &mockExecutor{
		kind: KindAgent,
		result: Result{
			Status:  StatusCompleted,
			Summary: "done",
		},
	}
	require.NoError(t, registry.Register(KindAgent, exec))

	done := make(chan Completion, 1)
	_, err := rt.DoTask(context.Background(), Spec{
		Kind:    KindAgent,
		Input:   "normalize input field",
		AgentID: "test-agent",
		OnComplete: func(_ context.Context, completion Completion) {
			done <- completion
		},
	})
	require.NoError(t, err)
	_ = waitCompletion(t, done)
	require.NotEmpty(t, events)

	queued := events[0]
	legacyTraceKey := "trace" + "_id"
	assert.Equal(t, "normalize input field", queued.Payload["input"])
	_, hasLegacyTask := queued.Payload["task"]
	assert.False(t, hasLegacyTask)
	_, hasAgentID := queued.Payload["agent_id"]
	_, hasRunID := queued.Payload["run_id"]
	_, hasLegacyTreeKey := queued.Payload[legacyTraceKey]
	_, hasSessionID := queued.Payload["session_id"]
	assert.False(t, hasAgentID)
	assert.False(t, hasRunID)
	assert.False(t, hasLegacyTreeKey)
	assert.False(t, hasSessionID)
	assert.Equal(t, "test-agent", queued.AgentID)
}

func TestRuntimeDoTaskHeartbeatExtendsTimeout(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	exec := &heartbeatingExecutor{
		kind:       KindAgent,
		heartbeats: 3,
		delay:      20 * time.Millisecond,
		result: Result{
			Status:  StatusCompleted,
			Summary: "finished after heartbeats",
		},
	}
	require.NoError(t, registry.Register(KindAgent, exec))

	done := make(chan Completion, 1)
	taskID, err := rt.DoTask(context.Background(), Spec{
		Kind:          KindAgent,
		AgentID:       "heartbeat-agent",
		Input:         "keep working",
		IdleTimeoutMS: 30,
		OnComplete: func(_ context.Context, completion Completion) {
			done <- completion
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, taskID)

	completion := waitCompletion(t, done)
	assert.Equal(t, StatusCompleted, completion.Record.Status)
	assert.Equal(t, "finished after heartbeats", completion.Record.Summary)

	record, ok := store.Get(taskID)
	require.True(t, ok)
	assert.False(t, record.LastActivityAt.IsZero())
	assert.WithinDuration(t, record.CompletedAt, record.LastActivityAt, 100*time.Millisecond)
}

func TestRuntimeDoTaskPublishesTaskRunningHeartbeat(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	var events []runtimeevents.EventRecord
	rt.Store().SetEventAppender(func(event runtimeevents.EventRecord) {
		events = append(events, event)
	})

	exec := &heartbeatingExecutor{
		kind:       KindAgent,
		heartbeats: 1,
		delay:      10 * time.Millisecond,
		result: Result{
			Status:  StatusCompleted,
			Summary: "heartbeat emitted",
		},
	}
	require.NoError(t, registry.Register(KindAgent, exec))

	done := make(chan Completion, 1)
	_, err := rt.DoTask(context.Background(), Spec{
		Kind:          KindAgent,
		AgentID:       "heartbeat-agent",
		Input:         "emit heartbeat",
		IdleTimeoutMS: 100,
		OnComplete: func(_ context.Context, completion Completion) {
			done <- completion
		},
	})
	require.NoError(t, err)
	_ = waitCompletion(t, done)

	assert.Contains(t, eventNames(events), runtimeevents.EventTaskRunning)
}

func TestRuntimeDoTaskUsesRunLifecycleTimeoutFromCompatIdleTimeoutField(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	exec := &retryingExecutor{
		kind:      KindAgent,
		firstWait: 40 * time.Millisecond,
	}
	require.NoError(t, registry.Register(KindAgent, exec))

	done := make(chan Completion, 1)
	taskID, err := rt.DoTask(context.Background(), Spec{
		Kind:          KindAgent,
		AgentID:       "retry-agent",
		Input:         "retry me",
		IdleTimeoutMS: 20,
		OnComplete: func(_ context.Context, completion Completion) {
			done <- completion
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, taskID)

	completion := waitCompletion(t, done)
	assert.Equal(t, StatusCancelled, completion.Record.Status)
	assert.Equal(t, 1, exec.attempts)

	record, ok := store.Get(taskID)
	require.True(t, ok)
	runID := record.RunID
	assert.NotEmpty(t, runID)
	createdTurn, ok := rt.TurnStore().Get(turn.ID(runID))
	require.True(t, ok)
	assert.Equal(t, turn.StateTimedOut, createdTurn.State())
}

func TestRuntimeDoTaskInheritsRunIDFromContext(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)
	turnStore := turn.NewStore()
	rt.SetTurnStore(turnStore)

	exec := &mockExecutor{
		kind: KindAgent,
		result: Result{
			Status:  StatusCompleted,
			Summary: "done",
		},
	}
	require.NoError(t, registry.Register(KindAgent, exec))

	parentTurn := turnStore.Create(context.Background(), turn.Config{SessionID: "turn-session"})
	ctx := turn.WithBoundContext(context.Background(), parentTurn)
	ctx = tools.WithRuntimeContext(ctx, tools.RuntimeContext{
		SessionID: "turn-session",
		RunID:     string(parentTurn.ID),
	})

	done := make(chan Completion, 1)
	taskID, err := rt.DoTask(ctx, Spec{
		Kind:    KindAgent,
		AgentID: "test-agent",
		Input:   "inherit turn",
		OnComplete: func(callbackCtx context.Context, completion Completion) {
			assert.Equal(t, string(parentTurn.ID), tools.RuntimeContextFrom(callbackCtx).RunID)
			done <- completion
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, taskID)

	completion := waitCompletion(t, done)
	assert.Equal(t, string(parentTurn.ID), completion.Record.RunID)

	record, ok := store.Get(taskID)
	require.True(t, ok)
	assert.Equal(t, string(parentTurn.ID), record.RunID)
}

func waitCompletion(t *testing.T, done <-chan Completion) Completion {
	t.Helper()
	select {
	case completion := <-done:
		return completion
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task completion callback")
		return Completion{}
	}
}

func eventNames(events []runtimeevents.EventRecord) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return names
}
