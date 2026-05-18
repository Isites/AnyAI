package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

// ProcessExecutor executes process tasks such as bash commands and python
// scripts through the unified task runtime.
type ProcessExecutor struct{}

// NewProcessExecutor creates a new process task executor.
func NewProcessExecutor() *ProcessExecutor {
	return &ProcessExecutor{}
}

// Kind returns the task kind this executor handles.
func (e *ProcessExecutor) Kind() task.Kind {
	return task.KindProcess
}

// Execute runs a process task.
func (e *ProcessExecutor) Execute(ctx context.Context, taskRecord task.Record) (task.Result, error) {
	switch processName := normalizeProcessName(taskRecord.ProcessName); processName {
	case "bash":
		return e.executeBash(ctx, taskRecord)
	case "python", "python3":
		return e.executePython(ctx, taskRecord)
	default:
		return task.Result{
			Status: task.StatusFailed,
			Error:  fmt.Sprintf("unsupported process executor %q for task %s", taskRecord.ProcessName, taskRecord.ID),
		}, nil
	}
}

func (e *ProcessExecutor) executeBash(ctx context.Context, taskRecord task.Record) (task.Result, error) {
	if result, err, ok := executeProcessViaToolRecovery(ctx, taskRecord, "bash"); ok {
		taskResult := taskResultFromToolResult(result)
		taskResult.Metadata = mergeProcessMetadata(taskResult.Metadata, taskRecord, "bash")
		return taskResult, err
	}
	in, err := tools.ParseBashProcessInput(json.RawMessage(taskRecord.Input))
	if err != nil {
		return task.Result{Status: task.StatusFailed, Error: err.Error()}, nil
	}
	result, execErr := tools.RunBashProcess(ctx, taskWorkDir(taskRecord), taskExecPolicy(taskRecord), in)
	taskResult := taskResultFromToolResult(result)
	taskResult.Metadata = mergeProcessMetadata(taskResult.Metadata, taskRecord, "bash")
	return taskResult, execErr
}

func (e *ProcessExecutor) executePython(ctx context.Context, taskRecord task.Record) (task.Result, error) {
	processName := normalizeProcessName(taskRecord.ProcessName)
	if result, err, ok := executeProcessViaToolRecovery(ctx, taskRecord, processToolName(processName)); ok {
		taskResult := taskResultFromToolResult(result)
		taskResult.Metadata = mergeProcessMetadata(taskResult.Metadata, taskRecord, processName)
		return taskResult, err
	}
	in, err := tools.ParsePythonProcessInput(json.RawMessage(taskRecord.Input))
	if err != nil {
		return task.Result{Status: task.StatusFailed, Error: err.Error()}, nil
	}
	result, execErr := tools.RunPythonProcess(ctx, taskWorkDir(taskRecord), taskExecPolicy(taskRecord), in)
	taskResult := taskResultFromToolResult(result)
	taskResult.Metadata = mergeProcessMetadata(taskResult.Metadata, taskRecord, normalizeProcessName(taskRecord.ProcessName))
	return taskResult, execErr
}

func processToolName(processName string) string {
	switch normalizeProcessName(processName) {
	case "python3":
		return "python"
	default:
		return normalizeProcessName(processName)
	}
}

func executeProcessViaToolRecovery(ctx context.Context, taskRecord task.Record, toolName string) (tools.ToolResult, error, bool) {
	execute := tools.TaskToolExecutionFromContext(ctx)
	if execute == nil {
		return tools.ToolResult{}, nil, false
	}
	call := llm.ToolCall{
		ID:    metadataString(taskRecord.Metadata, "tool_call_id"),
		Name:  toolName,
		Input: json.RawMessage(taskRecord.Input),
	}
	if strings.TrimSpace(call.ID) == "" {
		call.ID = taskRecord.ID
	}
	result, err := execute(ctx, call)
	return result, err, true
}

func taskWorkDir(taskRecord task.Record) string {
	if workDir := strings.TrimSpace(taskRecord.Contract.Workspace); workDir != "" {
		return workDir
	}
	return metadataString(taskRecord.Metadata, "workdir")
}

func taskExecPolicy(taskRecord task.Record) *tools.ExecPolicy {
	level := metadataString(taskRecord.Metadata, "exec_policy_level")
	allowlist := metadataStringSlice(taskRecord.Metadata, "exec_allowlist")
	if level == "" && len(allowlist) == 0 {
		return nil
	}
	return &tools.ExecPolicy{
		Level:     level,
		Allowlist: allowlist,
	}
}

func mergeProcessMetadata(metadata map[string]any, taskRecord task.Record, processName string) map[string]any {
	if metadata == nil {
		metadata = map[string]any{}
	}
	if metadata["process_name"] == nil {
		metadata["process_name"] = processName
	}
	if metadata["workdir"] == nil {
		if workDir := taskWorkDir(taskRecord); workDir != "" {
			metadata["workdir"] = workDir
		}
	}
	if toolName := metadataString(taskRecord.Metadata, "tool_name"); toolName != "" {
		metadata["tool_name"] = toolName
	}
	return metadata
}

func normalizeProcessName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	switch name {
	case "python3":
		return "python3"
	case "python":
		return "python"
	default:
		return name
	}
}

func metadataStringSlice(metadata map[string]any, key string) []string {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata[key]
	if !ok {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text, _ := value.(string)
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
