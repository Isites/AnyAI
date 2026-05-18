package tools

import (
	"context"
	"encoding/json"

	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
)

type runtimeContextKey struct{}
type runtimeInputBlocksKey struct{}
type runtimeExecutorKey struct{}
type runtimeToolExecutionKey struct{}
type runtimeBackgroundProcessManagerKey struct{}
type runtimeRawToolInputsKey struct{}

// RuntimeContext carries the current runtime call metadata through tool execution.
type RuntimeContext struct {
	RunID          string
	AgentID        string
	SessionID      string
	TaskID         string
	InputMessageID string
	ToolCallID     string
	AssetsBaseDir  string
	CurrentRequest string
	CallChain      []string
	Depth          int
}

// WithRuntimeContext attaches runtime metadata to a context.
func WithRuntimeContext(ctx context.Context, meta RuntimeContext) context.Context {
	return context.WithValue(ctx, runtimeContextKey{}, meta)
}

// RuntimeContextFrom extracts runtime metadata from a context.
func RuntimeContextFrom(ctx context.Context) RuntimeContext {
	if ctx == nil {
		return RuntimeContext{}
	}
	meta, _ := ctx.Value(runtimeContextKey{}).(RuntimeContext)
	return meta
}

// WithRuntimeInputBlocks attaches prepared runtime input blocks to the run
// context so session persistence can store lightweight attachment refs.
func WithRuntimeInputBlocks(ctx context.Context, blocks []input.InputBlock) context.Context {
	return context.WithValue(ctx, runtimeInputBlocksKey{}, append([]input.InputBlock(nil), blocks...))
}

// RuntimeInputBlocksFrom extracts prepared runtime input blocks from context.
func RuntimeInputBlocksFrom(ctx context.Context) []input.InputBlock {
	if ctx == nil {
		return nil
	}
	blocks, _ := ctx.Value(runtimeInputBlocksKey{}).([]input.InputBlock)
	return append([]input.InputBlock(nil), blocks...)
}

// WithExecutorContext attaches the current tool surface to the context so task
// executors can route tool work back through the same runtime-scoped registry.
func WithExecutorContext(ctx context.Context, executor Executor) context.Context {
	return context.WithValue(ctx, runtimeExecutorKey{}, executor)
}

// ExecutorFromContext returns the current runtime-scoped tool executor.
func ExecutorFromContext(ctx context.Context) Executor {
	if ctx == nil {
		return nil
	}
	executor, _ := ctx.Value(runtimeExecutorKey{}).(Executor)
	return executor
}

// TaskToolExecution executes one tool call inside the current runtime-scoped
// orchestration context.
type TaskToolExecution func(ctx context.Context, call llm.ToolCall) (ToolResult, error)

// WithTaskToolExecution attaches the current tool execution function so
// task-kind tool executors can preserve recovery logic and warnings.
func WithTaskToolExecution(ctx context.Context, execute TaskToolExecution) context.Context {
	return context.WithValue(ctx, runtimeToolExecutionKey{}, execute)
}

// TaskToolExecutionFromContext returns the current runtime-scoped tool
// execution function when available.
func TaskToolExecutionFromContext(ctx context.Context) TaskToolExecution {
	if ctx == nil {
		return nil
	}
	execute, _ := ctx.Value(runtimeToolExecutionKey{}).(TaskToolExecution)
	return execute
}

// WithRawToolInput keeps execution-only tool arguments out of persisted task
// records while still allowing the async task executor to run the original
// model call. Stored task input should remain transcript-safe.
func WithRawToolInput(ctx context.Context, toolCallID string, raw json.RawMessage) context.Context {
	if ctx == nil || toolCallID == "" {
		return ctx
	}
	next := map[string]json.RawMessage{}
	if current, ok := ctx.Value(runtimeRawToolInputsKey{}).(map[string]json.RawMessage); ok {
		for key, value := range current {
			next[key] = append(json.RawMessage(nil), value...)
		}
	}
	next[toolCallID] = append(json.RawMessage(nil), raw...)
	return context.WithValue(ctx, runtimeRawToolInputsKey{}, next)
}

func RawToolInputFromContext(ctx context.Context, toolCallID string) (json.RawMessage, bool) {
	if ctx == nil || toolCallID == "" {
		return nil, false
	}
	inputs, _ := ctx.Value(runtimeRawToolInputsKey{}).(map[string]json.RawMessage)
	raw, ok := inputs[toolCallID]
	if !ok {
		return nil, false
	}
	return append(json.RawMessage(nil), raw...), true
}

// WithBackgroundProcessManager attaches the current run-controller-owned
// process manager to a context.
func WithBackgroundProcessManager(ctx context.Context, manager *BackgroundProcessManager) context.Context {
	return context.WithValue(ctx, runtimeBackgroundProcessManagerKey{}, manager)
}

// BackgroundProcessManagerFromContext returns the manager that owns detached
// process groups for the current run controller invocation.
func BackgroundProcessManagerFromContext(ctx context.Context) *BackgroundProcessManager {
	if ctx == nil {
		return nil
	}
	manager, _ := ctx.Value(runtimeBackgroundProcessManagerKey{}).(*BackgroundProcessManager)
	return manager
}
