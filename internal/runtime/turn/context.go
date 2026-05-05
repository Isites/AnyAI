package turn

import (
	"context"
	"strings"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

// CurrentID returns the lifecycle ID stored on the runtime context.
func CurrentID(ctx context.Context) ID {
	meta := tools.RuntimeContextFrom(ctx)
	return ID(strings.TrimSpace(meta.RunID))
}

// WithBoundContext keeps the current context chain and ensures turn metadata
// and activity hooks are attached to it.
func WithBoundContext(ctx context.Context, t *Turn) context.Context {
	if t == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	meta := tools.RuntimeContextFrom(ctx)
	if strings.TrimSpace(meta.RunID) == "" {
		meta.RunID = string(t.ID)
	}
	ctx = tools.WithRuntimeContext(ctx, meta)
	return runtimeactivity.WithHook(ctx, func(_ map[string]any) {
		t.Touch()
	})
}

// RebindContext rebuilds the context on top of a new parent turn context while
// preserving the runtime-scoped values AnyAI relies on for task/tool routing.
func RebindContext(parent, source context.Context, t *Turn) context.Context {
	if t == nil {
		if parent != nil {
			return parent
		}
		return source
	}
	if parent == nil {
		parent = context.Background()
	}

	ctx := parent
	if source != nil {
		meta := tools.RuntimeContextFrom(source)
		if strings.TrimSpace(meta.RunID) == "" {
			meta.RunID = string(t.ID)
		}
		ctx = tools.WithRuntimeContext(ctx, meta)
		if executor := tools.ExecutorFromContext(source); executor != nil {
			ctx = tools.WithExecutorContext(ctx, executor)
		}
		if execute := tools.TaskToolExecutionFromContext(source); execute != nil {
			ctx = tools.WithTaskToolExecution(ctx, execute)
		}
		if hook := runtimeactivity.HookFromContext(source); hook != nil {
			ctx = runtimeactivity.WithHook(ctx, hook)
		}
	} else {
		meta := tools.RuntimeContextFrom(ctx)
		if strings.TrimSpace(meta.RunID) == "" {
			meta.RunID = string(t.ID)
		}
		ctx = tools.WithRuntimeContext(ctx, meta)
	}

	return runtimeactivity.WithHook(ctx, func(_ map[string]any) {
		t.Touch()
	})
}
