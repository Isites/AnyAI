package plan

import (
	"fmt"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/runtime/contract"
)

// StepState captures one plan step lifecycle state.
type StepState string

const (
	StepStatePending   StepState = "pending"
	StepStateRunning   StepState = "running"
	StepStateCompleted StepState = "completed"
	StepStateFailed    StepState = "failed"
	StepStateSkipped   StepState = "skipped"
)

// PlanState captures the plan lifecycle state.
type PlanState string

const (
	PlanStateDraft     PlanState = "draft"
	PlanStateRunning   PlanState = "running"
	PlanStatePaused    PlanState = "paused"
	PlanStateCompleted PlanState = "completed"
	PlanStateFailed    PlanState = "failed"
)

// StepAction describes the intended action for one step.
type StepAction struct {
	Type   string `json:"type,omitempty"`
	Target string `json:"target,omitempty"`
	Input  string `json:"input,omitempty"`
}

// Step is one structured plan step.
type Step struct {
	ID           string     `json:"id,omitempty"`
	Description  string     `json:"description,omitempty"`
	Action       StepAction `json:"action,omitempty"`
	Dependencies []string   `json:"dependencies,omitempty"`
	State        StepState  `json:"state,omitempty"`
	Output       string     `json:"output,omitempty"`
	Error        string     `json:"error,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// Plan is the runtime-owned structured plan model.
type Plan struct {
	ID          string     `json:"id,omitempty"`
	RunID       string     `json:"run_id,omitempty"`
	Description string     `json:"description,omitempty"`
	Steps       []Step     `json:"steps,omitempty"`
	State       PlanState  `json:"state,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Evaluation is the runtime view of whether a plan still has executable work.
type Evaluation struct {
	Plan         Plan
	ReadyStep    *Step
	BlockedSteps []string
	AllCompleted bool
	HasPending   bool
}

// Engine evaluates plan progress and readiness.
type Engine struct {
	store Store
}

// NewEngine creates a new plan engine.
func NewEngine(store Store) *Engine {
	return &Engine{store: store}
}

// GetPlan returns the latest stored structured plan when available.
func (e *Engine) GetPlan() (Plan, bool) {
	if e == nil || e.store == nil {
		return Plan{}, false
	}
	return e.store.GetStructuredPlan()
}

// EvaluateLatest evaluates the latest stored plan.
func (e *Engine) EvaluateLatest() (Evaluation, bool) {
	if e == nil {
		return Evaluation{}, false
	}
	current, ok := e.GetPlan()
	if !ok {
		return Evaluation{}, false
	}
	return e.Evaluate(current), true
}

// Evaluate returns the normalized readiness view for a plan.
func (e *Engine) Evaluate(current Plan) Evaluation {
	current = Normalize(current)
	eval := Evaluation{
		Plan: current,
	}

	if len(current.Steps) == 0 {
		eval.AllCompleted = current.State == PlanStateCompleted
		return eval
	}

	ready := findNextExecutableStep(current)
	if ready != nil {
		copy := *ready
		eval.ReadyStep = &copy
	}

	blocked := blockedPendingSteps(current)
	if len(blocked) > 0 {
		eval.BlockedSteps = blocked
	}

	allCompleted := true
	hasPending := false
	for _, step := range current.Steps {
		switch step.State {
		case StepStateCompleted, StepStateSkipped:
		default:
			allCompleted = false
		}
		if step.State == StepStatePending || step.State == StepStateRunning {
			hasPending = true
		}
	}
	eval.AllCompleted = allCompleted
	eval.HasPending = hasPending
	return eval
}

// Normalize trims fields, fills defaults, and guarantees stable step IDs.
func Normalize(current Plan) Plan {
	current.ID = strings.TrimSpace(current.ID)
	if current.ID == "" {
		current.ID = contract.NewOpaqueID("plan")
	}
	current.RunID = strings.TrimSpace(current.RunID)
	current.Description = strings.TrimSpace(current.Description)
	current.State = normalizePlanState(current.State, len(current.Steps))

	steps := make([]Step, 0, len(current.Steps))
	for i := range current.Steps {
		step := current.Steps[i]
		step.ID = strings.TrimSpace(step.ID)
		if step.ID == "" {
			step.ID = contract.NewOpaqueID("step")
		}
		step.Description = strings.TrimSpace(step.Description)
		step.Action.Type = strings.TrimSpace(step.Action.Type)
		step.Action.Target = strings.TrimSpace(step.Action.Target)
		step.Action.Input = strings.TrimSpace(step.Action.Input)
		step.Output = strings.TrimSpace(step.Output)
		step.Error = strings.TrimSpace(step.Error)
		step.State = normalizeStepState(step.State)
		step.Dependencies = normalizeDependencies(step.Dependencies)
		steps = append(steps, step)
	}
	current.Steps = steps

	if allStepsCompleted(current) {
		current.State = PlanStateCompleted
	}
	return current
}

// Render converts a structured plan into a prompt-friendly text form.
func Render(current Plan) string {
	current = Normalize(current)
	lines := make([]string, 0, len(current.Steps)+2)
	if current.Description != "" {
		lines = append(lines, current.Description)
	}
	for i, step := range current.Steps {
		label := step.Description
		if label == "" {
			label = step.ID
		}
		lines = append(lines, fmt.Sprintf("%d. [%s] %s", i+1, step.State, label))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func findNextExecutableStep(current Plan) *Step {
	if current.State != PlanStateRunning {
		return nil
	}
	for i := range current.Steps {
		step := &current.Steps[i]
		if step.State != StepStatePending {
			continue
		}
		if dependenciesSatisfied(current, step.Dependencies) {
			return step
		}
	}
	return nil
}

func blockedPendingSteps(current Plan) []string {
	blocked := make([]string, 0)
	for _, step := range current.Steps {
		if step.State != StepStatePending {
			continue
		}
		if dependenciesSatisfied(current, step.Dependencies) {
			continue
		}
		label := step.ID
		if step.Description != "" {
			label = step.Description + " (" + step.ID + ")"
		}
		blocked = append(blocked, label)
	}
	return blocked
}

func allStepsCompleted(current Plan) bool {
	if len(current.Steps) == 0 {
		return false
	}
	for _, step := range current.Steps {
		switch step.State {
		case StepStateCompleted, StepStateSkipped:
		default:
			return false
		}
	}
	return true
}

func dependenciesSatisfied(current Plan, deps []string) bool {
	for _, depID := range deps {
		dep := findStep(current, depID)
		if dep == nil || dep.State != StepStateCompleted {
			return false
		}
	}
	return true
}

func findStep(current Plan, stepID string) *Step {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return nil
	}
	for i := range current.Steps {
		if current.Steps[i].ID == stepID {
			return &current.Steps[i]
		}
	}
	return nil
}

func normalizeDependencies(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func normalizePlanState(state PlanState, stepCount int) PlanState {
	switch token := normalizeStateToken(string(state)); token {
	case string(PlanStateDraft), string(PlanStateRunning), string(PlanStatePaused), string(PlanStateCompleted), string(PlanStateFailed):
		return PlanState(token)
	default:
		if stepCount > 0 {
			return PlanStateRunning
		}
		return PlanStateDraft
	}
}

func normalizeStepState(state StepState) StepState {
	switch token := normalizeStateToken(string(state)); token {
	case string(StepStatePending), string(StepStateRunning), string(StepStateCompleted), string(StepStateFailed), string(StepStateSkipped):
		return StepState(token)
	default:
		return StepStatePending
	}
}

func normalizeStateToken(raw string) string {
	token := strings.TrimSpace(strings.ToLower(raw))
	switch token {
	case "in_progress", "in-progress", "doing", "active":
		return "running"
	default:
		return token
	}
}
