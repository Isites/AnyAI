package task

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

const (
	defaultGoalRuntimeMaxTurns    = 500
	defaultGoalRuntimeMaxDuration = 0
)

// GoalID is the stable identifier for one runtime-managed goal.
type GoalID string

// GoalState is the lifecycle state for one goal.
type GoalState string

const (
	GoalStateInProgress    GoalState = "in_progress"
	GoalStateAwaitingInput GoalState = "awaiting_input"
	GoalStateCompleted     GoalState = "completed"
	GoalStateAbandoned     GoalState = "abandoned"
)

// Checkpoint captures one runtime-visible milestone within a goal.
type Checkpoint struct {
	ID          string     `json:"id,omitempty"`
	Description string     `json:"description,omitempty"`
	Completed   bool       `json:"completed,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Evidence    string     `json:"evidence,omitempty"`
}

// Goal captures the runtime-owned execution state for one user objective.
type Goal struct {
	ID          GoalID
	SessionID   string
	Description string
	State       GoalState
	PlanID      string

	CreatedAt            time.Time
	StartedAt            time.Time
	CompletedAt          *time.Time
	TurnCount            int
	ToolCalls            int
	Checkpoints          []Checkpoint
	AwaitingInputMessage string
}

// ShouldContinueResult captures the current goal state plus the runtime's
// continuation decision. It is used both for pure inspection and for
// post-turn settle checks.
type ShouldContinueResult struct {
	GoalID                GoalID
	RunID                 string
	SessionID             string
	Description           string
	State                 GoalState
	PlanID                string
	TurnCount             int
	ToolCalls             int
	ShouldContinue        bool
	ForceContinue         bool
	HardStop              bool
	Reason                string
	PendingTaskIDs        []string
	PendingToolCalls      []string
	OpenTodos             []session.TodoItem
	CurrentPlan           *runtimeplan.Plan
	ReadyPlanStep         *runtimeplan.Step
	BlockedPlanSteps      []string
	IncompleteCheckpoints []Checkpoint
	AwaitingInputMessage  string
}

// GoalRuntime evaluates whether the current objective still has unfinished
// work from the runtime's point of view.
type GoalRuntime struct {
	session     *session.Session
	taskRuntime *Runtime
	maxTurns    int
	maxDuration time.Duration
	planEngine  *runtimeplan.Engine

	mu    sync.RWMutex
	goals map[GoalID]*Goal
}

// NewGoalRuntime creates a new runtime-backed goal evaluator.
func NewGoalRuntime(sess *session.Session, taskRuntime *Runtime, maxTurns int) *GoalRuntime {
	if maxTurns <= 0 {
		maxTurns = defaultGoalRuntimeMaxTurns
	}
	return &GoalRuntime{
		session:     sess,
		taskRuntime: taskRuntime,
		maxTurns:    maxTurns,
		maxDuration: defaultGoalRuntimeMaxDuration,
		planEngine:  runtimeplan.NewEngine(sessionPlanStore{session: sess}),
		goals:       make(map[GoalID]*Goal),
	}
}

// StartGoal creates a new in-progress goal.
func (gr *GoalRuntime) StartGoal(description, sessionID, runID string) *Goal {
	if gr == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		runID = tools.NewRunID()
	}
	now := time.Now().UTC()
	goal := &Goal{
		ID:          GoalID(runID),
		SessionID:   strings.TrimSpace(sessionID),
		Description: strings.TrimSpace(description),
		State:       GoalStateInProgress,
		CreatedAt:   now,
		StartedAt:   now,
	}
	if currentPlan, ok := gr.relevantStructuredPlan(goal); ok {
		goal.PlanID = strings.TrimSpace(currentPlan.ID)
	}
	gr.mu.Lock()
	gr.goals[goal.ID] = goal
	gr.mu.Unlock()
	return cloneGoal(goal)
}

// GetGoal returns a snapshot of the goal state.
func (gr *GoalRuntime) GetGoal(id GoalID) (*Goal, bool) {
	if gr == nil {
		return nil, false
	}
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	goal, ok := gr.goals[id]
	if !ok {
		return nil, false
	}
	return cloneGoal(goal), true
}

// RecordTurn records the highest turn index observed for the goal.
func (gr *GoalRuntime) RecordTurn(id GoalID, turn int) {
	if gr == nil || id == "" || turn <= 0 {
		return
	}
	gr.mu.Lock()
	defer gr.mu.Unlock()
	if goal := gr.goals[id]; goal != nil && turn > goal.TurnCount {
		goal.TurnCount = turn
	}
}

// RecordToolCalls increments the runtime-visible tool call count.
func (gr *GoalRuntime) RecordToolCalls(id GoalID, count int) {
	if gr == nil || id == "" || count <= 0 {
		return
	}
	gr.mu.Lock()
	defer gr.mu.Unlock()
	if goal := gr.goals[id]; goal != nil {
		goal.ToolCalls += count
	}
}

// SetCheckpoints replaces the goal checkpoint list.
func (gr *GoalRuntime) SetCheckpoints(id GoalID, checkpoints []Checkpoint) error {
	if gr == nil || id == "" {
		return nil
	}
	gr.mu.Lock()
	defer gr.mu.Unlock()
	goal := gr.goals[id]
	if goal == nil {
		return fmt.Errorf("goal %q not found", id)
	}
	goal.Checkpoints = normalizeCheckpoints(checkpoints)
	return nil
}

// MarkCheckpointComplete marks a checkpoint complete with supporting evidence.
func (gr *GoalRuntime) MarkCheckpointComplete(id GoalID, checkpointID, evidence string) error {
	if gr == nil || id == "" {
		return nil
	}
	gr.mu.Lock()
	defer gr.mu.Unlock()
	goal := gr.goals[id]
	if goal == nil {
		return fmt.Errorf("goal %q not found", id)
	}
	for i := range goal.Checkpoints {
		if goal.Checkpoints[i].ID != strings.TrimSpace(checkpointID) {
			continue
		}
		now := time.Now().UTC()
		goal.Checkpoints[i].Completed = true
		goal.Checkpoints[i].CompletedAt = &now
		goal.Checkpoints[i].Evidence = strings.TrimSpace(evidence)
		return nil
	}
	return fmt.Errorf("checkpoint %q not found", checkpointID)
}

// AwaitUserInput marks the goal as blocked on user input.
func (gr *GoalRuntime) AwaitUserInput(id GoalID, message string) error {
	if gr == nil || id == "" {
		return nil
	}
	gr.mu.Lock()
	defer gr.mu.Unlock()
	goal := gr.goals[id]
	if goal == nil {
		return fmt.Errorf("goal %q not found", id)
	}
	if goal.State == GoalStateCompleted {
		return fmt.Errorf("goal %q is already completed", id)
	}
	if goal.State == GoalStateAbandoned {
		return fmt.Errorf("goal %q is abandoned", id)
	}
	goal.State = GoalStateAwaitingInput
	goal.AwaitingInputMessage = strings.TrimSpace(message)
	return nil
}

// MaybeContinueIfIdle decides whether the goal still has objective unfinished
// work after the current agent turn has settled.
func (gr *GoalRuntime) MaybeContinueIfIdle(_ context.Context, id GoalID) (*ShouldContinueResult, error) {
	if gr == nil {
		return nil, nil
	}
	result, err := gr.Inspect(id)
	if err != nil || result == nil {
		return result, err
	}
	if result.State == GoalStateInProgress && !result.ShouldContinue && !result.HardStop {
		if err := gr.CompleteGoalIgnoringTask(id, "", ""); err != nil {
			return nil, err
		}
		return gr.Inspect(id)
	}
	return result, nil
}

// Inspect returns the latest prompt- and controller-friendly view of one goal
// without mutating goal state.
func (gr *GoalRuntime) Inspect(id GoalID) (*ShouldContinueResult, error) {
	if gr == nil || id == "" {
		return nil, nil
	}
	goal, ok := gr.lookupGoal(id)
	if !ok {
		return nil, fmt.Errorf("goal %q not found", id)
	}
	result := gr.inspectGoal(goal, "", "", "")
	if result == nil {
		return nil, nil
	}
	return result, nil
}

// CompleteGoal marks the goal as completed after validating that the runtime
// no longer sees unfinished objective work.
func (gr *GoalRuntime) CompleteGoal(id GoalID) error {
	return gr.CompleteGoalIgnoringTask(id, "", "")
}

// CompleteGoalIgnoringTask marks the goal as completed after validating that
// no unfinished objective work remains aside from the caller task/tool call
// that is issuing the completion request.
func (gr *GoalRuntime) CompleteGoalIgnoringTask(id GoalID, ignoredTaskID, ignoredToolCallID string) error {
	if gr == nil || id == "" {
		return nil
	}
	ignoredTaskID = strings.TrimSpace(ignoredTaskID)
	ignoredToolCallID, ignoredToolName := gr.resolveIgnoredToolCall(ignoredTaskID, ignoredToolCallID)
	if ignoredToolName == "" {
		ignoredToolName = "goal_complete"
	}
	goal, ok := gr.lookupGoal(id)
	if !ok {
		return fmt.Errorf("goal %q not found", id)
	}
	switch goal.State {
	case GoalStateCompleted:
		return nil
	case GoalStateAwaitingInput:
		return fmt.Errorf("goal %q is awaiting user input and cannot be completed", id)
	case GoalStateInProgress:
	default:
		return fmt.Errorf("goal %q is not in progress (state=%s)", id, goal.State)
	}

	result := gr.evaluateObjectiveWork(goal, ignoredTaskID, ignoredToolCallID, ignoredToolName)
	if result != nil && result.ShouldContinue {
		return fmt.Errorf("goal %q still has unfinished work: %s", id, describeRemainingGoalWork(*result))
	}

	gr.mu.Lock()
	defer gr.mu.Unlock()
	current := gr.goals[id]
	if current == nil {
		return fmt.Errorf("goal %q not found", id)
	}
	if current.State == GoalStateCompleted {
		return nil
	}
	if current.State != GoalStateInProgress {
		return fmt.Errorf("goal %q is not in progress (state=%s)", id, current.State)
	}
	now := time.Now().UTC()
	current.State = GoalStateCompleted
	current.CompletedAt = &now
	current.AwaitingInputMessage = ""
	return nil
}

func (gr *GoalRuntime) lookupGoal(id GoalID) (*Goal, bool) {
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	goal, ok := gr.goals[id]
	if !ok {
		return nil, false
	}
	return cloneGoal(goal), true
}

func (gr *GoalRuntime) inspectGoal(goal *Goal, ignoredTaskID, ignoredToolCallID, ignoredToolName string) *ShouldContinueResult {
	if goal == nil {
		return nil
	}

	result := goalStatusBase(goal)
	if currentPlan, ok := gr.relevantStructuredPlan(goal); ok {
		result.CurrentPlan = cloneRuntimePlan(&currentPlan)
	}

	switch goal.State {
	case GoalStateAwaitingInput:
		result.Reason = "goal is awaiting user input"
		if result.AwaitingInputMessage != "" {
			result.Reason += ": " + result.AwaitingInputMessage
		}
		return result
	case GoalStateCompleted:
		result.Reason = "goal already completed"
		return result
	case GoalStateAbandoned:
		result.Reason = "goal abandoned"
		return result
	}

	objective := gr.evaluateObjectiveWork(goal, ignoredTaskID, ignoredToolCallID, ignoredToolName)
	if objective != nil && objective.ShouldContinue {
		return gr.applyHardConstraints(goal, mergeGoalStatus(result, objective))
	}

	result.Reason = "no unfinished objective runtime work is currently visible"
	return result
}

func (gr *GoalRuntime) applyHardConstraints(goal *Goal, result *ShouldContinueResult) *ShouldContinueResult {
	if goal == nil || result == nil || !result.ShouldContinue {
		return result
	}
	if gr.maxTurns > 0 && goal.TurnCount >= gr.maxTurns {
		result.HardStop = true
		result.Reason = hardConstraintReason(
			fmt.Sprintf("reached max turns (%d)", gr.maxTurns),
			result.Reason,
		)
		return result
	}
	if gr.maxDuration > 0 && !goal.StartedAt.IsZero() && time.Since(goal.StartedAt) > gr.maxDuration {
		result.HardStop = true
		result.Reason = hardConstraintReason(
			fmt.Sprintf("exceeded max duration (%s)", gr.maxDuration),
			result.Reason,
		)
	}
	return result
}

func hardConstraintReason(constraint, objective string) string {
	constraint = strings.TrimSpace(constraint)
	objective = strings.TrimSpace(objective)
	if constraint == "" {
		return objective
	}
	if objective == "" {
		return constraint
	}
	return constraint + " while objective work remains: " + objective
}

func goalStatusBase(goal *Goal) *ShouldContinueResult {
	if goal == nil {
		return nil
	}
	return &ShouldContinueResult{
		GoalID:               goal.ID,
		RunID:                strings.TrimSpace(string(goal.ID)),
		SessionID:            strings.TrimSpace(goal.SessionID),
		Description:          strings.TrimSpace(goal.Description),
		State:                goal.State,
		PlanID:               strings.TrimSpace(goal.PlanID),
		TurnCount:            goal.TurnCount,
		ToolCalls:            goal.ToolCalls,
		AwaitingInputMessage: strings.TrimSpace(goal.AwaitingInputMessage),
	}
}

func mergeGoalStatus(base, delta *ShouldContinueResult) *ShouldContinueResult {
	if base == nil {
		base = &ShouldContinueResult{}
	}
	if delta == nil {
		return base
	}
	base.ShouldContinue = delta.ShouldContinue
	base.ForceContinue = delta.ForceContinue
	base.HardStop = delta.HardStop
	base.Reason = strings.TrimSpace(delta.Reason)
	base.PendingTaskIDs = append([]string(nil), delta.PendingTaskIDs...)
	base.PendingToolCalls = append([]string(nil), delta.PendingToolCalls...)
	base.OpenTodos = append([]session.TodoItem(nil), delta.OpenTodos...)
	base.CurrentPlan = cloneRuntimePlan(delta.CurrentPlan)
	base.ReadyPlanStep = clonePlanStep(delta.ReadyPlanStep)
	base.BlockedPlanSteps = append([]string(nil), delta.BlockedPlanSteps...)
	base.IncompleteCheckpoints = cloneCheckpoints(delta.IncompleteCheckpoints)
	if msg := strings.TrimSpace(delta.AwaitingInputMessage); msg != "" {
		base.AwaitingInputMessage = msg
	}
	return base
}

func (gr *GoalRuntime) evaluateObjectiveWork(goal *Goal, ignoredTaskID, ignoredToolCallID, ignoredToolName string) *ShouldContinueResult {
	if goal == nil {
		return nil
	}
	var currentPlanRef *runtimeplan.Plan
	if currentPlan, ok := gr.relevantStructuredPlan(goal); ok {
		currentPlanRef = cloneRuntimePlan(&currentPlan)
	}

	pendingTasks := gr.pendingTaskIDs(goal, ignoredTaskID)
	if len(pendingTasks) > 0 {
		return &ShouldContinueResult{
			GoalID:         goal.ID,
			ShouldContinue: true,
			ForceContinue:  true,
			Reason:         "goal still has pending runtime tasks",
			PendingTaskIDs: pendingTasks,
			CurrentPlan:    cloneRuntimePlan(currentPlanRef),
		}
	}

	pendingToolCalls := gr.pendingToolCallIDs(ignoredToolCallID, ignoredToolName)
	if len(pendingToolCalls) > 0 {
		return &ShouldContinueResult{
			GoalID:           goal.ID,
			ShouldContinue:   true,
			ForceContinue:    true,
			Reason:           "goal still has unresolved tool calls",
			PendingToolCalls: pendingToolCalls,
			CurrentPlan:      cloneRuntimePlan(currentPlanRef),
		}
	}

	incompleteCheckpoints := incompleteCheckpoints(goal.Checkpoints)
	if len(incompleteCheckpoints) > 0 {
		return &ShouldContinueResult{
			GoalID:                goal.ID,
			ShouldContinue:        true,
			ForceContinue:         true,
			Reason:                fmt.Sprintf("%d checkpoints incomplete", len(incompleteCheckpoints)),
			IncompleteCheckpoints: cloneCheckpoints(incompleteCheckpoints),
			CurrentPlan:           cloneRuntimePlan(currentPlanRef),
		}
	}

	openTodos := gr.openTodos(goal.ID)
	if len(openTodos) > 0 {
		return &ShouldContinueResult{
			GoalID:         goal.ID,
			ShouldContinue: true,
			ForceContinue:  true,
			Reason:         "goal still has open todo items",
			OpenTodos:      openTodos,
			CurrentPlan:    cloneRuntimePlan(currentPlanRef),
		}
	}

	currentPlan, planEval, ok := gr.evaluatePlan(goal)
	if ok && !planEval.AllCompleted {
		switch {
		case planEval.ReadyStep != nil:
			return &ShouldContinueResult{
				GoalID:         goal.ID,
				ShouldContinue: true,
				ForceContinue:  true,
				Reason:         fmt.Sprintf("plan step %s ready to execute", planEval.ReadyStep.ID),
				CurrentPlan:    cloneRuntimePlan(&currentPlan),
				ReadyPlanStep:  clonePlanStep(planEval.ReadyStep),
			}
		case currentPlan.State == runtimeplan.PlanStateFailed:
			return &ShouldContinueResult{
				GoalID:         goal.ID,
				ShouldContinue: true,
				ForceContinue:  true,
				Reason:         "plan is failed and still has unfinished steps",
				CurrentPlan:    cloneRuntimePlan(&currentPlan),
			}
		case currentPlan.State == runtimeplan.PlanStatePaused:
			return &ShouldContinueResult{
				GoalID:         goal.ID,
				ShouldContinue: true,
				ForceContinue:  true,
				Reason:         "plan is paused with unfinished steps",
				CurrentPlan:    cloneRuntimePlan(&currentPlan),
			}
		case len(planEval.BlockedSteps) > 0:
			return &ShouldContinueResult{
				GoalID:           goal.ID,
				ShouldContinue:   true,
				ForceContinue:    true,
				Reason:           fmt.Sprintf("plan has %d blocked pending steps", len(planEval.BlockedSteps)),
				BlockedPlanSteps: append([]string(nil), planEval.BlockedSteps...),
				CurrentPlan:      cloneRuntimePlan(&currentPlan),
			}
		}
	}

	return nil
}

func (gr *GoalRuntime) pendingTaskIDs(goal *Goal, ignoredTaskID string) []string {
	if gr == nil || goal == nil || gr.taskRuntime == nil || gr.taskRuntime.Store() == nil {
		return nil
	}
	goalSessionID := strings.TrimSpace(goal.SessionID)
	goalRunID := strings.TrimSpace(string(goal.ID))
	records := gr.taskRuntime.Store().ListAll()
	ids := make([]string, 0)
	for _, record := range records {
		if record.Status != StatusRunning {
			continue
		}
		if ignoredTaskID != "" && record.ID == ignoredTaskID {
			continue
		}
		switch {
		case goalSessionID != "" && strings.TrimSpace(record.SessionID) == goalSessionID && strings.TrimSpace(record.RunID) == goalRunID:
			ids = append(ids, record.ID)
		case goalSessionID == "" && strings.TrimSpace(record.RunID) == goalRunID:
			ids = append(ids, record.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (gr *GoalRuntime) pendingToolCallIDs(ignoredToolCallID, ignoredToolName string) []string {
	if gr == nil || gr.session == nil {
		return nil
	}
	pending := session.PendingToolCalls(gr.session.History())
	if len(pending) == 0 {
		return nil
	}
	ids := make([]string, 0, len(pending))
	for _, call := range pending {
		if strings.TrimSpace(call.ID) == "" {
			continue
		}
		if ignoredToolCallID != "" && call.ID == ignoredToolCallID {
			continue
		}
		if ignoredToolName != "" && strings.EqualFold(strings.TrimSpace(call.Tool), ignoredToolName) {
			continue
		}
		ids = append(ids, call.ID)
	}
	sort.Strings(ids)
	return ids
}

func (gr *GoalRuntime) openTodos(goalID GoalID) []session.TodoItem {
	if gr == nil || gr.session == nil || goalID == "" {
		return nil
	}
	items := session.TodoItemsForRun(gr.session, string(goalID), true)
	if len(items) == 0 {
		return nil
	}
	open := make([]session.TodoItem, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Status), "completed") {
			continue
		}
		open = append(open, item)
	}
	return open
}

func (gr *GoalRuntime) evaluatePlan(goal *Goal) (runtimeplan.Plan, runtimeplan.Evaluation, bool) {
	if gr == nil || gr.planEngine == nil || goal == nil {
		return runtimeplan.Plan{}, runtimeplan.Evaluation{}, false
	}
	currentPlan, ok := gr.relevantStructuredPlan(goal)
	if !ok {
		return runtimeplan.Plan{}, runtimeplan.Evaluation{}, false
	}
	return currentPlan, gr.planEngine.Evaluate(currentPlan), true
}

func (gr *GoalRuntime) relevantStructuredPlan(goal *Goal) (runtimeplan.Plan, bool) {
	if gr == nil || gr.planEngine == nil || goal == nil {
		return runtimeplan.Plan{}, false
	}
	currentPlan, ok := gr.planEngine.GetPlan()
	if !ok {
		return runtimeplan.Plan{}, false
	}
	currentPlan = runtimeplan.Normalize(currentPlan)
	if goal.PlanID != "" {
		if currentPlan.ID == goal.PlanID || currentPlan.RunID == string(goal.ID) {
			return currentPlan, true
		}
		return runtimeplan.Plan{}, false
	}
	if currentPlan.RunID != string(goal.ID) {
		return runtimeplan.Plan{}, false
	}
	if currentPlan.ID != "" {
		gr.bindPlanID(goal.ID, currentPlan.ID)
		goal.PlanID = currentPlan.ID
	}
	return currentPlan, true
}

func (gr *GoalRuntime) bindPlanID(goalID GoalID, planID string) {
	if gr == nil || goalID == "" || strings.TrimSpace(planID) == "" {
		return
	}
	gr.mu.Lock()
	defer gr.mu.Unlock()
	if goal := gr.goals[goalID]; goal != nil && goal.PlanID == "" {
		goal.PlanID = strings.TrimSpace(planID)
	}
}

func (gr *GoalRuntime) resolveIgnoredToolCall(ignoredTaskID, ignoredToolCallID string) (string, string) {
	ignoredToolCallID = strings.TrimSpace(ignoredToolCallID)
	ignoredToolName := ""
	if gr == nil || gr.taskRuntime == nil || gr.taskRuntime.Store() == nil || ignoredTaskID == "" {
		return ignoredToolCallID, ignoredToolName
	}
	record, ok := gr.taskRuntime.Store().Get(ignoredTaskID)
	if !ok || len(record.Metadata) == 0 {
		return ignoredToolCallID, ignoredToolName
	}
	if ignoredToolCallID == "" {
		value, _ := record.Metadata["tool_call_id"].(string)
		ignoredToolCallID = strings.TrimSpace(value)
	}
	value, _ := record.Metadata["tool_name"].(string)
	ignoredToolName = strings.TrimSpace(value)
	return ignoredToolCallID, ignoredToolName
}

func describeRemainingGoalWork(result ShouldContinueResult) string {
	parts := make([]string, 0, 6)
	if reason := strings.TrimSpace(result.Reason); reason != "" {
		parts = append(parts, reason)
	}
	if len(result.PendingTaskIDs) > 0 {
		parts = append(parts, "pending runtime tasks: "+strings.Join(result.PendingTaskIDs, ", "))
	}
	if len(result.PendingToolCalls) > 0 {
		parts = append(parts, "pending tool calls: "+strings.Join(result.PendingToolCalls, ", "))
	}
	if len(result.IncompleteCheckpoints) > 0 {
		names := make([]string, 0, len(result.IncompleteCheckpoints))
		for _, checkpoint := range result.IncompleteCheckpoints {
			label := firstNonEmpty(checkpoint.Description, checkpoint.ID)
			names = append(names, label)
		}
		if len(names) > 0 {
			parts = append(parts, "incomplete checkpoints: "+strings.Join(names, ", "))
		}
	}
	if len(result.OpenTodos) > 0 {
		todos := make([]string, 0, len(result.OpenTodos))
		for _, item := range result.OpenTodos {
			label := strings.TrimSpace(item.ID)
			if content := strings.TrimSpace(item.Content); content != "" {
				if label != "" {
					label = content + " (" + label + ")"
				} else {
					label = content
				}
			}
			if label == "" {
				continue
			}
			todos = append(todos, label)
		}
		if len(todos) > 0 {
			parts = append(parts, "open todo items: "+strings.Join(todos, ", "))
		}
	}
	if result.ReadyPlanStep != nil {
		label := firstNonEmpty(result.ReadyPlanStep.Description, result.ReadyPlanStep.ID)
		parts = append(parts, "ready plan step: "+label)
	}
	if len(result.BlockedPlanSteps) > 0 {
		parts = append(parts, "blocked plan steps: "+strings.Join(result.BlockedPlanSteps, ", "))
	}
	if len(parts) == 0 {
		return "unfinished runtime work remains"
	}
	return strings.Join(parts, "; ")
}

func cloneGoal(goal *Goal) *Goal {
	if goal == nil {
		return nil
	}
	copy := *goal
	copy.Checkpoints = cloneCheckpoints(goal.Checkpoints)
	copy.CompletedAt = cloneTime(goal.CompletedAt)
	return &copy
}

func cloneCheckpoints(items []Checkpoint) []Checkpoint {
	if len(items) == 0 {
		return nil
	}
	out := make([]Checkpoint, 0, len(items))
	for _, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		item.Description = strings.TrimSpace(item.Description)
		item.Evidence = strings.TrimSpace(item.Evidence)
		item.CompletedAt = cloneTime(item.CompletedAt)
		out = append(out, item)
	}
	return out
}

func normalizeCheckpoints(items []Checkpoint) []Checkpoint {
	if len(items) == 0 {
		return nil
	}
	out := make([]Checkpoint, 0, len(items))
	for _, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = tools.NewOpaqueID("checkpoint")
		}
		item.Description = strings.TrimSpace(item.Description)
		item.Evidence = strings.TrimSpace(item.Evidence)
		if item.Completed {
			item.CompletedAt = cloneTime(item.CompletedAt)
		} else {
			item.CompletedAt = nil
			item.Evidence = ""
		}
		out = append(out, item)
	}
	return out
}

func incompleteCheckpoints(items []Checkpoint) []Checkpoint {
	out := make([]Checkpoint, 0)
	for _, item := range items {
		if item.Completed {
			continue
		}
		out = append(out, item)
	}
	return out
}

func clonePlanStep(step *runtimeplan.Step) *runtimeplan.Step {
	if step == nil {
		return nil
	}
	copy := *step
	copy.Dependencies = append([]string(nil), step.Dependencies...)
	copy.StartedAt = cloneTime(step.StartedAt)
	copy.CompletedAt = cloneTime(step.CompletedAt)
	return &copy
}

func cloneRuntimePlan(plan *runtimeplan.Plan) *runtimeplan.Plan {
	if plan == nil {
		return nil
	}
	copy := runtimeplan.Normalize(*plan)
	copy.Steps = append([]runtimeplan.Step(nil), copy.Steps...)
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type sessionPlanStore struct {
	session *session.Session
}

func (s sessionPlanStore) UpdateStructuredPlan(plan runtimeplan.Plan) error {
	if s.session == nil {
		return nil
	}
	s.session.Append(session.StructuredPlanEntry(plan))
	return nil
}

func (s sessionPlanStore) GetStructuredPlan() (runtimeplan.Plan, bool) {
	return session.LatestStructuredPlan(s.session)
}
