package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

// ToolExecutor executes tool tasks.
type ToolExecutor struct {
	registry tools.Executor
}

// NewToolExecutor creates a new tool task executor.
func NewToolExecutor(registry tools.Executor) *ToolExecutor {
	return &ToolExecutor{registry: registry}
}

// Kind returns the task kind this executor handles.
func (e *ToolExecutor) Kind() task.Kind {
	return task.KindTool
}

// Execute runs a single tool task.
func (e *ToolExecutor) Execute(ctx context.Context, taskRecord task.Record) (task.Result, error) {
	result, err := e.executeToolCall(ctx, taskRecord)
	if err != nil {
		failed := taskResultFromToolResult(result)
		if failed.Error == "" {
			failed.Error = fmt.Sprintf("tool execution failed: %v", err)
		}
		failed.Status = task.StatusFailed
		return failed, err
	}
	return taskResultFromToolResult(result), nil
}

func (e *ToolExecutor) executeToolCall(ctx context.Context, taskRecord task.Record) (tools.ToolResult, error) {
	toolCall := llm.ToolCall{
		ID:   taskRecord.ID,
		Name: taskRecord.ToolName,
	}
	if taskRecord.Input != "" {
		toolCall.Input = json.RawMessage(taskRecord.Input)
	}

	executor := e.resolveExecutor(ctx)
	if executor == nil {
		return tools.ToolResult{}, fmt.Errorf("tool registry not set")
	}

	var (
		result tools.ToolResult
		err    error
	)
	if execute := tools.TaskToolExecutionFromContext(ctx); execute != nil {
		result, err = execute(ctx, toolCall)
	} else {
		result, err = executor.Execute(ctx, toolCall.Name, toolCall.Input)
	}
	return tools.SanitizeToolResult(result), err
}

func (e *ToolExecutor) resolveExecutor(ctx context.Context) tools.Executor {
	if executor := tools.ExecutorFromContext(ctx); executor != nil {
		return executor
	}
	return e.registry
}

func taskResultFromToolResult(result tools.ToolResult) task.Result {
	status := task.StatusCompleted
	if result.Error != "" {
		status = task.StatusFailed
	}
	return task.Result{
		Status:   status,
		Summary:  result.Output,
		Error:    result.Error,
		Metadata: cloneMetadata(result.Metadata),
		Images:   append([]llm.ImageContent(nil), result.Images...),
	}
}

func cloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
