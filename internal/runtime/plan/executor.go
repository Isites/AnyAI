package plan

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ExecuteStatus captures the outcome of one runtime-driven plan-step attempt.
type ExecuteStatus string

const (
	ExecuteStatusCompleted ExecuteStatus = "completed"
	ExecuteStatusPaused    ExecuteStatus = "paused"
	ExecuteStatusFailed    ExecuteStatus = "failed"
)

// ExecuteResult describes the updated plan plus the step that was acted on.
type ExecuteResult struct {
	Plan   Plan
	Step   *Step
	Status ExecuteStatus
	Output string
	Error  string
}

// Dispatcher executes one structured step action against the surrounding
// runtime without importing higher-level runtime packages into the plan layer.
type Dispatcher interface {
	ExecuteTool(ctx context.Context, name, input string) (string, error)
	ExecuteAgent(ctx context.Context, target, input string) (string, error)
	AwaitUserInput(ctx context.Context, message string) error
	CompleteCheckpoint(ctx context.Context, checkpointID, evidence string) error
}

// Executor dispatches one ready structured plan step and persists the updated
// plan state back into the runtime store.
type Executor struct {
	store      Store
	dispatcher Dispatcher
}

// NewExecutor creates a new structured plan executor.
func NewExecutor(store Store, dispatcher Dispatcher) *Executor {
	return &Executor{store: store, dispatcher: dispatcher}
}

// Execute finds the next ready step in the latest stored plan and executes it.
// planID is optional; when provided, it must match the latest stored plan.
func (e *Executor) Execute(ctx context.Context, planID string) (*ExecuteResult, error) {
	if e == nil || e.store == nil {
		return nil, fmt.Errorf("plan store is not available")
	}
	if e.dispatcher == nil {
		return nil, fmt.Errorf("plan dispatcher is not available")
	}
	current, ok := e.store.GetStructuredPlan()
	if !ok {
		return nil, fmt.Errorf("structured plan is not available")
	}
	current = Normalize(current)
	planID = strings.TrimSpace(planID)
	if planID != "" && current.ID != planID {
		return nil, fmt.Errorf("structured plan %q is not the latest active plan", planID)
	}
	if current.State != PlanStateRunning {
		return nil, fmt.Errorf("structured plan %q is not runnable (state=%s)", current.ID, current.State)
	}

	ready := findNextExecutableStep(current)
	if ready == nil {
		return nil, fmt.Errorf("structured plan %q has no executable ready step", current.ID)
	}
	stepID := ready.ID
	now := time.Now().UTC()
	if current.StartedAt == nil {
		current.StartedAt = &now
	}
	for i := range current.Steps {
		if current.Steps[i].ID != stepID {
			continue
		}
		current.Steps[i].State = StepStateRunning
		if current.Steps[i].StartedAt == nil {
			current.Steps[i].StartedAt = &now
		}
		current.Steps[i].CompletedAt = nil
		current.Steps[i].Error = ""
	}
	if err := e.store.UpdateStructuredPlan(current); err != nil {
		return nil, err
	}

	step := findStep(current, stepID)
	if step == nil {
		return nil, fmt.Errorf("structured plan step %q disappeared during execution", stepID)
	}

	result, err := e.dispatch(ctx, current, *step)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			reverted := revertStepToPending(current, stepID)
			_ = e.store.UpdateStructuredPlan(reverted)
			return nil, ctx.Err()
		}
		failed := failStep(current, stepID, err.Error())
		if updateErr := e.store.UpdateStructuredPlan(failed); updateErr != nil {
			return nil, updateErr
		}
		return &ExecuteResult{
			Plan:   failed,
			Step:   cloneStep(findStep(failed, stepID)),
			Status: ExecuteStatusFailed,
			Error:  strings.TrimSpace(err.Error()),
		}, nil
	}

	switch result.Status {
	case ExecuteStatusPaused:
		paused := pauseStep(current, stepID)
		if updateErr := e.store.UpdateStructuredPlan(paused); updateErr != nil {
			return nil, updateErr
		}
		return &ExecuteResult{
			Plan:   paused,
			Step:   cloneStep(findStep(paused, stepID)),
			Status: ExecuteStatusPaused,
			Output: strings.TrimSpace(result.Output),
		}, nil
	default:
		completed := completeStep(current, stepID, result.Output)
		if updateErr := e.store.UpdateStructuredPlan(completed); updateErr != nil {
			return nil, updateErr
		}
		return &ExecuteResult{
			Plan:   completed,
			Step:   cloneStep(findStep(completed, stepID)),
			Status: ExecuteStatusCompleted,
			Output: strings.TrimSpace(result.Output),
		}, nil
	}
}

type dispatchResult struct {
	Status ExecuteStatus
	Output string
}

func (e *Executor) dispatch(ctx context.Context, current Plan, step Step) (dispatchResult, error) {
	actionType := normalizeActionType(step.Action.Type)
	switch actionType {
	case "tool":
		output, err := e.dispatcher.ExecuteTool(ctx, strings.TrimSpace(step.Action.Target), strings.TrimSpace(step.Action.Input))
		return dispatchResult{Status: ExecuteStatusCompleted, Output: output}, err
	case "agent":
		output, err := e.dispatcher.ExecuteAgent(ctx, strings.TrimSpace(step.Action.Target), strings.TrimSpace(step.Action.Input))
		return dispatchResult{Status: ExecuteStatusCompleted, Output: output}, err
	case "user_input":
		message := firstNonEmpty(step.Action.Input, step.Description, step.ID)
		if err := e.dispatcher.AwaitUserInput(ctx, message); err != nil {
			return dispatchResult{}, err
		}
		return dispatchResult{Status: ExecuteStatusPaused, Output: message}, nil
	case "checkpoint":
		checkpointID := firstNonEmpty(step.Action.Target, step.ID)
		evidence := strings.TrimSpace(step.Action.Input)
		if err := e.dispatcher.CompleteCheckpoint(ctx, checkpointID, evidence); err != nil {
			return dispatchResult{}, err
		}
		output := firstNonEmpty(evidence, "checkpoint completed")
		return dispatchResult{Status: ExecuteStatusCompleted, Output: output}, nil
	default:
		return dispatchResult{}, fmt.Errorf("unsupported structured plan action type %q", strings.TrimSpace(step.Action.Type))
	}
}

func completeStep(current Plan, stepID, output string) Plan {
	current = Normalize(current)
	now := time.Now().UTC()
	for i := range current.Steps {
		if current.Steps[i].ID != stepID {
			continue
		}
		current.Steps[i].State = StepStateCompleted
		current.Steps[i].Output = strings.TrimSpace(output)
		current.Steps[i].Error = ""
		current.Steps[i].CompletedAt = &now
		if current.Steps[i].StartedAt == nil {
			current.Steps[i].StartedAt = &now
		}
		break
	}
	if allStepsCompleted(current) {
		current.State = PlanStateCompleted
		current.CompletedAt = &now
	} else {
		current.State = PlanStateRunning
	}
	return current
}

func failStep(current Plan, stepID, errMsg string) Plan {
	current = Normalize(current)
	now := time.Now().UTC()
	for i := range current.Steps {
		if current.Steps[i].ID != stepID {
			continue
		}
		current.Steps[i].State = StepStateFailed
		current.Steps[i].Error = strings.TrimSpace(errMsg)
		current.Steps[i].CompletedAt = &now
		if current.Steps[i].StartedAt == nil {
			current.Steps[i].StartedAt = &now
		}
		break
	}
	current.State = PlanStateFailed
	return current
}

func pauseStep(current Plan, stepID string) Plan {
	current = Normalize(current)
	for i := range current.Steps {
		if current.Steps[i].ID != stepID {
			continue
		}
		current.Steps[i].State = StepStatePending
		current.Steps[i].CompletedAt = nil
		current.Steps[i].Error = ""
		break
	}
	current.State = PlanStatePaused
	return current
}

func revertStepToPending(current Plan, stepID string) Plan {
	current = Normalize(current)
	for i := range current.Steps {
		if current.Steps[i].ID != stepID {
			continue
		}
		current.Steps[i].State = StepStatePending
		current.Steps[i].CompletedAt = nil
		current.Steps[i].Error = ""
		break
	}
	current.State = PlanStateRunning
	return current
}

func normalizeActionType(actionType string) string {
	switch strings.ToLower(strings.TrimSpace(actionType)) {
	case "tool", "tool_call":
		return "tool"
	case "agent", "callagent", "agent_call":
		return "agent"
	case "user_input", "await_user_input":
		return "user_input"
	case "checkpoint":
		return "checkpoint"
	default:
		return strings.ToLower(strings.TrimSpace(actionType))
	}
}

func cloneStep(step *Step) *Step {
	if step == nil {
		return nil
	}
	copy := *step
	copy.Dependencies = append([]string(nil), step.Dependencies...)
	copy.StartedAt = cloneTime(step.StartedAt)
	copy.CompletedAt = cloneTime(step.CompletedAt)
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
