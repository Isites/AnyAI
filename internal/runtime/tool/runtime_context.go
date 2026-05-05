package tools

import (
	"context"

	"github.com/Isites/anyai/internal/runtime/llm"
)

type runtimeContextKey struct{}
type runtimeExecutorKey struct{}
type runtimeToolExecutionKey struct{}

// RuntimeContext carries the current runtime call metadata through tool execution.
type RuntimeContext struct {
	RunID          string
	AgentID        string
	SessionID      string
	TaskID         string
	ToolCallID     string
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
