package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
)

// PlanStore is an alias for session.PlanStore.
type PlanStore = session.PlanStore

// TodoStore is an alias for session.TodoStore.
type TodoStore = session.TodoStore

// TodoItem is an alias for session.TodoItem.
type TodoItem = session.TodoItem

// UpdatePlanTool updates the current plan state in the session.
type UpdatePlanTool struct {
	Store PlanStore
}

func (t *UpdatePlanTool) Name() string { return "update_plan" }

func (t *UpdatePlanTool) Description() string {
	return "Update the current plan or task breakdown. Use this to record your planned approach, milestones, or strategy for complex tasks."
}

func (t *UpdatePlanTool) ToolMetadata() ToolMetadata {
	return stateMutationToolMetadata(t.Name(), shortStateMutationTimeoutMS)
}

func (t *UpdatePlanTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"plan": {
				"type": "object",
				"description": "Structured runtime plan. You may either provide the whole plan here or use the top-level fields below.",
				"properties": {
					"id": {"type": "string"},
					"run_id": {"type": "string"},
					"description": {"type": "string"},
					"state": {
						"type": "string",
						"enum": ["draft", "running", "paused", "completed", "failed"],
						"description": "Canonical plan state. Use 'running' when the plan is actively in progress."
					},
					"steps": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"id": {"type": "string"},
								"description": {"type": "string"},
								"state": {
									"type": "string",
									"enum": ["pending", "running", "completed", "failed", "skipped"],
									"description": "Canonical step state. Use 'running' when work is actively in progress."
								},
								"dependencies": {"type": "array", "items": {"type": "string"}},
								"action": {
									"type": "object",
									"properties": {
										"type": {"type": "string"},
										"target": {"type": "string"},
										"input": {"type": "string"}
									}
								},
								"output": {"type": "string"},
								"error": {"type": "string"}
							}
						}
					}
				}
			},
			"id": {"type": "string"},
			"run_id": {"type": "string"},
			"description": {"type": "string"},
			"state": {
				"type": "string",
				"enum": ["draft", "running", "paused", "completed", "failed"],
				"description": "Canonical plan state. Use 'running' when the plan is actively in progress."
			},
			"steps": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "string"},
						"description": {"type": "string"},
						"state": {
							"type": "string",
							"enum": ["pending", "running", "completed", "failed", "skipped"],
							"description": "Canonical step state. Use 'running' when work is actively in progress."
						},
						"dependencies": {"type": "array", "items": {"type": "string"}},
						"action": {
							"type": "object",
							"properties": {
								"type": {"type": "string"},
								"target": {"type": "string"},
								"input": {"type": "string"}
							}
						},
						"output": {"type": "string"},
						"error": {"type": "string"}
					}
				}
			}
		}
	}`)
}

func (t *UpdatePlanTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	structured, err := parsePlanUpdateInput(input)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if t.Store == nil {
		return ToolResult{Error: "plan storage is not available"}, nil
	}

	meta := RuntimeContextFrom(ctx)
	if strings.TrimSpace(structured.RunID) == "" {
		structured.RunID = strings.TrimSpace(meta.RunID)
	}
	if err := t.Store.UpdateStructuredPlan(*structured); err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	return ToolResult{Output: "Structured plan updated."}, nil
}

// TodoTool manages a checklist of items for the current task.
type TodoTool struct {
	Store TodoStore
}

func (t *TodoTool) Name() string { return "todo" }

func (t *TodoTool) Description() string {
	return "Manage a todo list for tracking progress on multi-step tasks. Add items, mark them complete, or list current items."
}

func (t *TodoTool) ToolMetadata() ToolMetadata {
	return stateMutationToolMetadata(t.Name(), shortStateMutationTimeoutMS)
}

func (t *TodoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["add", "complete", "list"],
				"description": "The action to perform: add a new item, complete an existing item, or list all items"
			},
			"item": {
				"type": "string",
				"description": "For 'add': the todo item content. For 'complete': the item ID."
			}
		},
		"required": ["action"]
	}`)
}

func (t *TodoTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Action string `json:"action"`
		Item   string `json:"item"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if t.Store == nil {
		return ToolResult{Error: "todo storage is not available"}, nil
	}

	switch params.Action {
	case "add":
		if params.Item == "" {
			return ToolResult{Error: "item is required for add action"}, nil
		}
		id := t.Store.AddTodo(ctx, params.Item)
		return ToolResult{Output: fmt.Sprintf("Todo item added: %s", id)}, nil
	case "complete":
		if params.Item == "" {
			return ToolResult{Error: "item ID is required for complete action"}, nil
		}
		if t.Store.CompleteTodo(ctx, params.Item) {
			return ToolResult{Output: fmt.Sprintf("Todo item %s completed.", params.Item)}, nil
		}
		return ToolResult{Error: fmt.Sprintf("todo item %q not found", params.Item)}, nil
	case "list":
		items := t.Store.ListTodos(ctx)
		if len(items) == 0 {
			return ToolResult{Output: "No todo items."}, nil
		}
		payload, _ := json.Marshal(items)
		return ToolResult{Output: string(payload)}, nil
	default:
		return ToolResult{Error: fmt.Sprintf("unknown action %q, use add/complete/list", params.Action)}, nil
	}
}

func parsePlanUpdateInput(input json.RawMessage) (*runtimeplan.Plan, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(input, &payload); err != nil {
		return nil, err
	}

	if rawPlan, ok := payload["plan"]; ok {
		var legacy string
		if err := json.Unmarshal(rawPlan, &legacy); err == nil {
			if strings.TrimSpace(legacy) == "" {
				return nil, fmt.Errorf("structured plan content is required")
			}
			return nil, fmt.Errorf("legacy text plan payload is no longer supported; send structured plan data instead")
		}

		var structured runtimeplan.Plan
		if err := json.Unmarshal(rawPlan, &structured); err == nil {
			normalized := runtimeplan.Normalize(structured)
			return &normalized, nil
		}
		return nil, fmt.Errorf("plan must be a structured object")
	}

	if _, hasSteps := payload["steps"]; hasSteps || payload["description"] != nil || payload["state"] != nil || payload["run_id"] != nil || payload["id"] != nil {
		var structured runtimeplan.Plan
		if err := json.Unmarshal(input, &structured); err != nil {
			return nil, err
		}
		normalized := runtimeplan.Normalize(structured)
		return &normalized, nil
	}

	return nil, fmt.Errorf("missing structured plan payload")
}
