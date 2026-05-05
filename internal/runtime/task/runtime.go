package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	"github.com/Isites/anyai/internal/runtime/turn"
)

// Runtime is the unified task runtime for doTask kernel.
// It manages task lifecycle, execution, and join frames.
type Runtime struct {
	store     *Store
	registry  *ExecutorRegistry
	turnStore *turn.Store
}

// NewRuntime creates a new task runtime.
func NewRuntime(store *Store, registry *ExecutorRegistry) *Runtime {
	if store == nil {
		store = NewStore()
	}
	if registry == nil {
		registry = NewExecutorRegistry()
	}
	return &Runtime{
		store:     store,
		registry:  registry,
		turnStore: turn.NewStore(),
	}
}

// SetEventAppender configures the task lifecycle event sink on the underlying
// store so task.* events can be recorded and replayed.
func (rt *Runtime) SetEventAppender(appender func(runtimeevents.EventRecord)) {
	if rt == nil {
		return
	}
	if rt.store != nil {
		rt.store.SetEventAppender(appender)
	}
}

// SetTurnStore configures the shared turn store used for lifecycle binding.
func (rt *Runtime) SetTurnStore(store *turn.Store) {
	if rt == nil {
		return
	}
	if store == nil {
		store = turn.NewStore()
	}
	rt.turnStore = store
}

// DoTask submits one task spec for execution and returns the created task ID.
func (rt *Runtime) DoTask(ctx context.Context, spec Spec) (string, error) {
	if rt == nil {
		return "", fmt.Errorf("task runtime is nil")
	}

	// Validate spec
	if spec.Kind == "" {
		spec.Kind = KindAgent
	}
	meta := tools.RuntimeContextFrom(ctx)
	if spec.ParentTaskID == "" {
		spec.ParentTaskID = meta.TaskID
	}
	if spec.RunID == "" {
		spec.RunID = meta.RunID
	}
	if spec.RunID == "" {
		spec.RunID = tools.NewRunID()
	}
	if spec.SessionID == "" {
		spec.SessionID = meta.SessionID
	}
	spec.Metadata = cloneMap(spec.Metadata)
	if spec.Metadata == nil {
		spec.Metadata = map[string]any{}
	}

	// Get executor for this kind
	executor := rt.registry.Get(spec.Kind)
	if executor == nil {
		return "", fmt.Errorf("no executor registered for task kind %q", spec.Kind)
	}
	boundCtx := rt.bindTurnContext(ctx, &spec)

	// Create task record
	callback := spec.OnComplete
	record := rt.store.CreateSpec(spec)
	if record == nil {
		return "", fmt.Errorf("failed to create task record")
	}

	runCtx, cancel := context.WithCancel(boundCtx)
	rt.store.setCancel(record.ID, cancel)

	// Execute asynchronously and report completion through the callback.
	go rt.executeTask(runCtx, *record, executor, cancel, callback)

	return record.ID, nil
}

// executeTask runs the task executor and handles the result.
func (rt *Runtime) executeTask(ctx context.Context, task Record, executor Executor, cancel context.CancelFunc, callback Callback) {
	defer cancel()
	defer rt.store.clearCancel(task.ID)

	running, ok := rt.store.Running(task.ID, nil)
	if !ok {
		if final, exists := rt.store.Get(task.ID); exists {
			finalResult := resultFromRecord(final)
			rt.invokeCallback(ctx, callback, final, finalResult)
		}
		return
	}

	maxAttempts := taskMaxAttempts(running)
	backoff := taskRetryBackoff(running)
	current := running
	var (
		final       Record
		finalResult Result
		finalCtx    = ctx
	)
	var activeTurn *turn.Turn
	if rt != nil && rt.turnStore != nil && current.RunID != "" {
		if t, ok := rt.turnStore.Get(turn.ID(current.RunID)); ok {
			activeTurn = t
			activeTurn.RegisterTask(task.ID)
			activeTurn.Touch()
			defer activeTurn.UnregisterTask(task.ID)
		}
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			current, _ = rt.store.Touch(task.ID, map[string]any{
				"attempt":        attempt,
				"max_attempts":   maxAttempts,
				"retrying":       true,
				"retry_backoff":  int(backoff / time.Millisecond),
				"retry_count":    attempt - 1,
				"lifecycle_step": "retry",
			})
		}

		execCtx := ctx
		if activeTurn != nil {
			execCtx = turn.WithBoundContext(execCtx, activeTurn)
		}
		execMeta := tools.RuntimeContextFrom(execCtx)
		execMeta.TaskID = task.ID
		execCtx = tools.WithRuntimeContext(execCtx, execMeta)
		execCtx = runtimeactivity.WithHook(execCtx, func(metadata map[string]any) {
			rt.store.Touch(task.ID, metadata)
		})

		result, err := executor.Execute(execCtx, current)
		timedOut := activeTurn != nil && activeTurn.State() == turn.StateTimedOut
		result, err = normalizeAttemptResult(result, err, timedOut)

		if shouldRetryTask(current, result, err, attempt, maxAttempts, timedOut) {
			wait := taskRetryDelay(backoff, attempt)
			if wait > 0 {
				if waitErr := waitForTaskRetry(ctx, wait); waitErr != nil {
					result = Result{Status: StatusCancelled, Error: waitErr.Error(), Metadata: cloneMap(result.Metadata)}
					err = waitErr
				} else {
					continue
				}
			} else {
				continue
			}
		}

		finalCtx = execCtx
		final, ok = rt.store.finishExecution(task.ID, execCtx, result, err)
		if !ok {
			final, ok = rt.store.Get(task.ID)
		}
		finalResult = mergeResultWithRecord(final, result)
		break
	}

	if ok {
		rt.invokeCallback(finalCtx, callback, final, finalResult)
	}
}

// Cancel cancels a running task.
func (rt *Runtime) Cancel(taskID string) bool {
	if rt == nil || rt.store == nil {
		return false
	}
	return rt.store.Cancel(taskID)
}

// Get retrieves a task record by ID.
func (rt *Runtime) Get(taskID string) (Record, bool) {
	if rt == nil || rt.store == nil {
		return Record{}, false
	}
	return rt.store.Get(taskID)
}

// List returns all tasks.
func (rt *Runtime) List() []Record {
	if rt == nil || rt.store == nil {
		return nil
	}
	return rt.store.List()
}

// Store returns the underlying task store.
func (rt *Runtime) Store() *Store {
	return rt.store
}

// Registry returns the executor registry.
func (rt *Runtime) Registry() *ExecutorRegistry {
	return rt.registry
}

// TurnStore returns the lifecycle store used by the task runtime.
func (rt *Runtime) TurnStore() *turn.Store {
	if rt == nil {
		return nil
	}
	return rt.turnStore
}

// JoinAllSettled runs a bounded parallel join and returns child results in the
// original submission order.
func (rt *Runtime) JoinAllSettled(ctx context.Context, spec JoinSpec, worker func(context.Context, int, func(Record))) (JoinResult, error) {
	if rt == nil {
		return JoinResult{}, fmt.Errorf("task runtime is nil")
	}
	if worker == nil {
		return JoinResult{}, fmt.Errorf("join worker is nil")
	}
	if spec.Count < 0 {
		return JoinResult{}, fmt.Errorf("join child count must be >= 0")
	}
	if spec.Count == 0 {
		return JoinResult{}, nil
	}

	limit := spec.MaxParallel
	if limit <= 0 || limit > spec.Count {
		limit = spec.Count
	}

	results := make([]JoinChildResult, spec.Count)
	guards := make([]sync.Once, spec.Count)
	type joinLaunch struct {
		index        int
		startedAt    time.Time
		startedOrder int
	}

	var (
		mu          sync.Mutex
		startedSeq  int
		finishedSeq int
		nextIndex   int
		inFlight    int
		settled     int
	)
	cond := sync.NewCond(&mu)

	scheduleLocked := func() []joinLaunch {
		launches := make([]joinLaunch, 0, limit)
		for inFlight < limit && nextIndex < spec.Count {
			launches = append(launches, joinLaunch{
				index:        nextIndex,
				startedAt:    time.Now().UTC(),
				startedOrder: startedSeq + 1,
			})
			nextIndex++
			startedSeq++
			inFlight++
		}
		return launches
	}

	var launch func(joinLaunch)
	launch = func(item joinLaunch) {
		go worker(ctx, item.index, func(record Record) {
			guards[item.index].Do(func() {
				completedAt := time.Now().UTC()
				var launches []joinLaunch

				mu.Lock()
				finishedSeq++
				results[item.index] = JoinChildResult{
					Record:       record,
					StartedOrder: item.startedOrder,
					FinishOrder:  finishedSeq,
					StartedAt:    item.startedAt,
					CompletedAt:  completedAt,
				}
				inFlight--
				settled++
				launches = scheduleLocked()
				cond.Broadcast()
				mu.Unlock()

				for _, next := range launches {
					launch(next)
				}
			})
		})
	}

	mu.Lock()
	initial := scheduleLocked()
	mu.Unlock()

	for _, item := range initial {
		launch(item)
	}

	mu.Lock()
	for settled < spec.Count {
		cond.Wait()
	}
	mu.Unlock()

	return JoinResult{
		MaxParallel: limit,
		Results:     results,
	}, nil
}

func (rt *Runtime) invokeCallback(ctx context.Context, callback Callback, record Record, result Result) {
	if rt == nil || callback == nil {
		return
	}
	callbackCtx := ctx
	meta := tools.RuntimeContextFrom(callbackCtx)
	meta.TaskID = record.ID
	callbackCtx = tools.WithRuntimeContext(callbackCtx, meta)

	defer func() {
		if recover() != nil {
			return
		}
	}()

	callback(callbackCtx, Completion{
		TaskID: record.ID,
		Record: record,
		Result: result,
	})
}

func resultFromRecord(record Record) Result {
	return Result{
		Status:    record.Status,
		Summary:   record.Summary,
		Error:     record.Error,
		SessionID: record.SessionID,
		Metadata:  cloneMap(record.Metadata),
	}
}

func mergeResultWithRecord(record Record, result Result) Result {
	final := resultFromRecord(record)
	final.Images = append([]llm.ImageContent(nil), result.Images...)
	return final
}

func describeTask(record Record) string {
	switch record.Kind {
	case KindAgent:
		return firstNonEmptyString(record.TargetAgent, record.AgentID, string(record.Kind))
	case KindTool:
		return firstNonEmptyString(record.ToolName, string(record.Kind))
	case KindProcess:
		return firstNonEmptyString(record.ProcessName, string(record.Kind))
	default:
		return string(record.Kind)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (rt *Runtime) bindTurnContext(ctx context.Context, spec *Spec) context.Context {
	if spec == nil {
		return ctx
	}
	if rt == nil || rt.turnStore == nil {
		return ctx
	}

	meta := tools.RuntimeContextFrom(ctx)
	if spec.RunID != "" {
		if existing, ok := rt.turnStore.Get(turn.ID(spec.RunID)); ok {
			if spec.RunID == meta.RunID {
				return turn.WithBoundContext(ctx, existing)
			}
			return turn.RebindContext(existing.Context(), ctx, existing)
		}
		newTurn := rt.turnStore.Create(ctx, turn.Config{
			ID:          turn.ID(spec.RunID),
			SessionID:   firstNonEmptyString(spec.SessionID, meta.SessionID),
			IdleTimeout: deriveTurnIdleTimeout(ctx, spec.IdleTimeoutMS),
		})
		return turn.RebindContext(newTurn.Context(), ctx, newTurn)
	}

	if inherited := meta.RunID; inherited != "" {
		spec.RunID = inherited
		if existing, ok := rt.turnStore.Get(turn.ID(inherited)); ok {
			return turn.WithBoundContext(ctx, existing)
		}
		return ctx
	}

	newTurn := rt.turnStore.Create(ctx, turn.Config{
		ID:          turn.ID(spec.RunID),
		SessionID:   firstNonEmptyString(spec.SessionID, meta.SessionID),
		IdleTimeout: deriveTurnIdleTimeout(ctx, spec.IdleTimeoutMS),
	})
	spec.RunID = string(newTurn.ID)
	return turn.RebindContext(newTurn.Context(), ctx, newTurn)
}

func deriveTurnIdleTimeout(ctx context.Context, idleTimeoutMS int) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	if idleTimeoutMS <= 0 {
		return 0
	}
	return time.Duration(idleTimeoutMS) * time.Millisecond
}
