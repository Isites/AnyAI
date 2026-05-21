package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/task"
)

// Executor executes agent tasks through the runtime-managed run entrypoint.
type Executor struct {
	runner Runner
}

// Runner is the interface for running agent tasks.
type Runner interface {
	StartManagedRun(ctx context.Context, req runtimeport.RunRequest) (*runtimeport.ManagedRun, error)
}

// New creates a new agent task executor.
func New() *Executor {
	return &Executor{}
}

// SetRunner sets the agent runner.
func (e *Executor) SetRunner(runner Runner) {
	e.runner = runner
}

// Kind returns the task kind this executor handles.
func (e *Executor) Kind() task.Kind {
	return task.KindAgent
}

// Execute runs an agent task.
func (e *Executor) Execute(ctx context.Context, taskRecord task.Record) (task.Result, error) {
	if e.runner == nil {
		return task.Result{Status: task.StatusFailed, Error: "agent runner not set"}, nil
	}

	var callInput Input
	rawInput := strings.TrimSpace(taskRecord.Input)
	if rawInput != "" {
		if err := json.Unmarshal([]byte(rawInput), &callInput); err != nil && looksLikeJSON(rawInput) {
			return task.Result{Status: task.StatusFailed, Error: fmt.Sprintf("invalid agent call input: %v", err)}, nil
		}
	}

	targetAgent := firstNonEmptyString(taskRecord.TargetAgent, callInput.Agent)
	if targetAgent == "" {
		return task.Result{Status: task.StatusFailed, Error: "target agent not specified"}, nil
	}
	taskText := firstNonEmptyString(callInput.Task, taskRecord.Input)
	if taskText == "" {
		return task.Result{Status: task.StatusFailed, Error: "agent task input is empty"}, nil
	}
	childSessionID := firstNonEmptyString(metadataString(taskRecord.Metadata, "child_session_id"), taskRecord.SessionID)
	contract := runtimeport.Contract(taskRecord.Contract.Normalized())
	if contract.Empty() {
		contract.TaskGoal = taskText
	}

	envelope := input.InputEnvelope{
		SessionID: childSessionID,
		Blocks: []input.InputBlock{
			{Type: "text", Text: taskText},
		},
	}

	req := runtimeport.RunRequest{
		RunID:         taskRecord.RunID,
		AgentID:       targetAgent,
		Envelope:      envelope,
		SessionID:     childSessionID,
		TaskID:        taskRecord.ID,
		ParentTaskID:  taskRecord.ParentTaskID,
		ParentAgentID: firstNonEmptyString(metadataString(taskRecord.Metadata, "caller_agent"), taskRecord.AgentID),
		Contract:      contract,
	}

	managedRun, err := e.runner.StartManagedRun(ctx, req)
	if err != nil {
		return task.Result{Status: task.StatusFailed, Error: err.Error()}, nil
	}

	return e.waitForRun(ctx, managedRun), nil
}

func (e *Executor) waitForRun(ctx context.Context, run *runtimeport.ManagedRun) task.Result {
	var output string
	var status task.Status
	var runError string

	for {
		select {
		case event, ok := <-run.Events:
			if !ok {
				return task.Result{
					Status:    status,
					Summary:   output,
					Error:     runError,
					SessionID: run.SessionID,
				}
			}

			runtimeactivity.Emit(ctx, map[string]any{
				"run_id":     run.RunID,
				"session_id": run.SessionID,
			})

			switch event.Name {
			case runtimeevents.EventTextDelta:
				if text := runtimeevents.StringPayload(event.Payload, "text"); text != "" {
					output += text
				}
			case runtimeevents.EventRunCompleted:
				status = task.StatusCompleted
			case runtimeevents.EventRunFailed:
				status = task.StatusFailed
				runError = runtimeevents.FailureMessage(event)
			case runtimeevents.EventRunAborted:
				status = task.StatusCancelled
				runError = "aborted"
			}

		case <-ctx.Done():
			if run.Cancel != nil {
				run.Cancel()
			}
			return task.Result{
				Status:    task.StatusCancelled,
				Summary:   output,
				Error:     ctx.Err().Error(),
				SessionID: run.SessionID,
			}
		}
	}
}

type Input struct {
	Agent         string `json:"target_agent,omitempty"`
	Task          string `json:"task,omitempty"`
	IdleTimeoutMS int    `json:"idle_timeout_ms,omitempty"`
}

func looksLikeJSON(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
