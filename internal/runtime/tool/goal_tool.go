package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GoalCompleteTool lets the model explicitly ask the runtime to mark the
// current goal as completed after all objective work is done.
type GoalCompleteTool struct {
	Manager GoalManager
}

func (t *GoalCompleteTool) Name() string { return "goal_complete" }

func (t *GoalCompleteTool) Description() string {
	return "Mark the current runtime-managed goal as complete. The runtime validates that no objective work remains before accepting completion."
}

func (t *GoalCompleteTool) ToolMetadata() ToolMetadata {
	return stateMutationToolMetadata(t.Name(), shortStateMutationTimeoutMS)
}

func (t *GoalCompleteTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

func (t *GoalCompleteTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct{}
	if len(strings.TrimSpace(string(input))) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
		}
	}
	if t.Manager == nil {
		return ToolResult{Error: "goal completion is not available in this runtime"}, nil
	}
	meta := RuntimeContextFrom(ctx)
	goalID := strings.TrimSpace(meta.RunID)
	resultMeta := map[string]any{
		"run_id":       goalID,
		"task_id":      strings.TrimSpace(meta.TaskID),
		"tool_call_id": strings.TrimSpace(meta.ToolCallID),
	}
	if goalID == "" {
		return ToolResult{Error: "no active goal in runtime context", Metadata: resultMeta}, nil
	}
	if err := t.Manager.CompleteGoal(ctx, goalID); err != nil {
		return ToolResult{Error: err.Error(), Metadata: resultMeta}, nil
	}
	return ToolResult{
		Output:   fmt.Sprintf("Goal %s marked as completed.", goalID),
		Metadata: resultMeta,
	}, nil
}

// AwaitUserInputTool lets the model explicitly block the current goal on user
// input so the runtime stops self-driving until the user replies.
type AwaitUserInputTool struct {
	Manager GoalManager
}

func (t *AwaitUserInputTool) Name() string { return "await_user_input" }

func (t *AwaitUserInputTool) Description() string {
	return "Mark the current runtime-managed goal as waiting for user input. Use this when you cannot continue without a user answer."
}

func (t *AwaitUserInputTool) ToolMetadata() ToolMetadata {
	return stateMutationToolMetadata(t.Name(), shortStateMutationTimeoutMS)
}

func (t *AwaitUserInputTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"message": {
				"type": "string",
				"description": "What information you need from the user before the goal can continue"
			}
		},
		"required": ["message"],
		"additionalProperties": false
	}`)
}

func (t *AwaitUserInputTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if t.Manager == nil {
		return ToolResult{Error: "goal input blocking is not available in this runtime"}, nil
	}

	meta := RuntimeContextFrom(ctx)
	goalID := strings.TrimSpace(meta.RunID)
	message := strings.TrimSpace(params.Message)
	resultMeta := map[string]any{
		"run_id":       goalID,
		"task_id":      strings.TrimSpace(meta.TaskID),
		"tool_call_id": strings.TrimSpace(meta.ToolCallID),
	}
	if goalID == "" {
		return ToolResult{Error: "no active goal in runtime context", Metadata: resultMeta}, nil
	}
	if message == "" {
		return ToolResult{Error: "message is required", Metadata: resultMeta}, nil
	}
	if err := t.Manager.AwaitUserInput(ctx, goalID, message); err != nil {
		return ToolResult{Error: err.Error(), Metadata: resultMeta}, nil
	}
	return ToolResult{
		Output:   fmt.Sprintf("Goal %s is now awaiting user input. Ask the user for: %s", goalID, message),
		Metadata: resultMeta,
	}, nil
}
