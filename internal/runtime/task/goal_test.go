package task

import (
	"context"
	"testing"
	"time"

	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoalRuntimeMaybeContinueWhenTodoOpen(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	gr := NewGoalRuntime(sess, nil, 4)
	goal := gr.StartGoal("Ship the patch", "goal-session", "")
	require.NotNil(t, goal)
	sess.Append(session.TodoEntry(session.TodoData{
		ID:        "todo_1",
		Content:   "Ship the patch",
		Status:    "open",
		CreatedAt: time.Now().Unix(),
		RunID:     string(goal.ID),
	}))

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldContinue)
	assert.True(t, result.ForceContinue)
	assert.Len(t, result.OpenTodos, 1)
	assert.Contains(t, result.Reason, "open todo")
}

func TestGoalRuntimeMaybeContinueWhenRunningTaskMatchesRun(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	record := store.CreateSpec(Spec{
		Kind:      KindTool,
		SessionID: "goal-session",
		RunID:     "run_1",
		ToolName:  "bash",
		Input:     "pwd",
	})
	require.NotNil(t, record)
	_, ok := store.Running(record.ID, nil)
	require.True(t, ok)

	gr := NewGoalRuntime(session.NewSession("agent", "goal-session"), rt, 4)
	goal := gr.StartGoal("Check task state", "goal-session", "run_1")
	require.NotNil(t, goal)

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldContinue)
	assert.Contains(t, result.PendingTaskIDs, record.ID)
}

func TestGoalRuntimeMaybeContinuePrefersSessionScopeOverSharedRun(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	current := store.CreateSpec(Spec{
		Kind:      KindTool,
		SessionID: "child-session-a",
		RunID:     "run_parent",
		ToolName:  "bash",
		Input:     "pwd",
	})
	require.NotNil(t, current)
	_, ok := store.Running(current.ID, nil)
	require.True(t, ok)

	sibling := store.CreateSpec(Spec{
		Kind:      KindTool,
		SessionID: "child-session-b",
		RunID:     "run_parent",
		ToolName:  "bash",
		Input:     "pwd",
	})
	require.NotNil(t, sibling)
	_, ok = store.Running(sibling.ID, nil)
	require.True(t, ok)

	gr := NewGoalRuntime(session.NewSession("agent", "child-session-a"), rt, 4)
	goal := gr.StartGoal("Check child task state", "child-session-a", "run_parent")
	require.NotNil(t, goal)

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldContinue)
	assert.Contains(t, result.PendingTaskIDs, current.ID)
	assert.NotContains(t, result.PendingTaskIDs, sibling.ID)
}

func TestGoalRuntimeMaybeContinueFallsBackToRunWhenSessionMissing(t *testing.T) {
	store := NewStore()
	registry := NewExecutorRegistry()
	rt := NewRuntime(store, registry)

	record := store.CreateSpec(Spec{
		Kind:      KindTool,
		SessionID: "child-session-a",
		RunID:     "run_1",
		ToolName:  "bash",
		Input:     "pwd",
	})
	require.NotNil(t, record)
	_, ok := store.Running(record.ID, nil)
	require.True(t, ok)

	gr := NewGoalRuntime(session.NewSession("agent", "parent-session"), rt, 4)
	goal := gr.StartGoal("Check task state", "", "run_1")
	require.NotNil(t, goal)

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldContinue)
	assert.Contains(t, result.PendingTaskIDs, record.ID)
}

func TestGoalRuntimeCompletesWhenNoObjectiveWorkRemains(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	gr := NewGoalRuntime(sess, nil, 4)
	goal := gr.StartGoal("Summarize work", "goal-session", "")
	require.NotNil(t, goal)

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.ShouldContinue)
	assert.Equal(t, GoalStateCompleted, result.State)
	assert.Contains(t, result.Reason, "goal already completed")

	completed, ok := gr.GetGoal(goal.ID)
	require.True(t, ok)
	assert.Equal(t, GoalStateCompleted, completed.State)
	require.NotNil(t, completed.CompletedAt)
}

func TestGoalRuntimeCompleteGoalRejectsWhenTodoOpen(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	gr := NewGoalRuntime(sess, nil, 4)
	goal := gr.StartGoal("Ship the patch", "goal-session", "")
	require.NotNil(t, goal)
	sess.Append(session.TodoEntry(session.TodoData{
		ID:        "todo_1",
		Content:   "Ship the patch",
		Status:    "open",
		CreatedAt: time.Now().Unix(),
		RunID:     string(goal.ID),
	}))

	err := gr.CompleteGoal(goal.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unfinished work")
	assert.Contains(t, err.Error(), "todo_1")

	current, ok := gr.GetGoal(goal.ID)
	require.True(t, ok)
	assert.Equal(t, GoalStateInProgress, current.State)
}

func TestGoalRuntimeInspectIncludesCurrentStructuredPlan(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	sess.Append(session.StructuredPlanEntry(runtimeplan.Plan{
		ID:          "plan_1",
		RunID:       "run_goal",
		Description: "Close the loop",
		State:       runtimeplan.PlanStateRunning,
		Steps: []runtimeplan.Step{
			{ID: "inspect", Description: "Inspect", State: runtimeplan.StepStateCompleted},
			{ID: "patch", Description: "Patch", State: runtimeplan.StepStatePending},
			{ID: "verify", Description: "Verify", State: runtimeplan.StepStatePending},
		},
	}))
	gr := NewGoalRuntime(sess, nil, 4)
	goal := gr.StartGoal("Close the loop", "goal-session", "run_goal")
	require.NotNil(t, goal)
	gr.RecordTurn(goal.ID, 2)

	status, err := gr.Inspect(goal.ID)
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, string(goal.ID), status.RunID)
	assert.Equal(t, "Close the loop", status.Description)
	assert.Equal(t, 2, status.TurnCount)
	require.NotNil(t, status.CurrentPlan)
	assert.Equal(t, "plan_1", status.CurrentPlan.ID)
	assert.Equal(t, runtimeplan.PlanStateRunning, status.CurrentPlan.State)
}

func TestGoalRuntimeCompleteGoalIgnoresPendingGoalCompleteCall(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	sess.Append(session.ToolCallEntry("tc_goal", "goal_complete", nil))
	gr := NewGoalRuntime(sess, nil, 4)
	goal := gr.StartGoal("Finalize the task", "goal-session", "")
	require.NotNil(t, goal)

	err := gr.CompleteGoalIgnoringTask(goal.ID, "", "")
	require.NoError(t, err)

	current, ok := gr.GetGoal(goal.ID)
	require.True(t, ok)
	assert.Equal(t, GoalStateCompleted, current.State)
}

func TestGoalRuntimeAwaitUserInputBlocksContinuation(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	gr := NewGoalRuntime(sess, nil, 4)
	goal := gr.StartGoal("Need a decision", "goal-session", "")
	require.NotNil(t, goal)

	require.NoError(t, gr.AwaitUserInput(goal.ID, "Choose option A or B"))

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.ShouldContinue)
	assert.Contains(t, result.Reason, "awaiting user input")
	assert.Equal(t, "Choose option A or B", result.AwaitingInputMessage)
}

func TestGoalRuntimeStructuredPlanReadyStepForcesContinuation(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	gr := NewGoalRuntime(sess, nil, 4)
	goal := gr.StartGoal("Ship the fix", "goal-session", "")
	require.NotNil(t, goal)
	sess.Append(session.StructuredPlanEntry(runtimeplan.Plan{
		ID:          "plan_1",
		RunID:       string(goal.ID),
		Description: "Ship the fix",
		State:       runtimeplan.PlanStateRunning,
		Steps: []runtimeplan.Step{
			{ID: "inspect", Description: "Inspect", State: runtimeplan.StepStateCompleted},
			{ID: "patch", Description: "Patch", State: runtimeplan.StepStatePending, Dependencies: []string{"inspect"}},
		},
	}))

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldContinue)
	require.NotNil(t, result.ReadyPlanStep)
	assert.Equal(t, "patch", result.ReadyPlanStep.ID)
}

func TestGoalRuntimeObjectiveFactsWinOverMaxTurnsWhenWorkIsDone(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	gr := NewGoalRuntime(sess, nil, 1)
	goal := gr.StartGoal("Summarize work", "goal-session", "")
	require.NotNil(t, goal)
	gr.RecordTurn(goal.ID, 1)

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.ShouldContinue)
	assert.False(t, result.HardStop)
	assert.Equal(t, GoalStateCompleted, result.State)
	assert.Contains(t, result.Reason, "goal already completed")

	current, ok := gr.GetGoal(goal.ID)
	require.True(t, ok)
	assert.Equal(t, GoalStateCompleted, current.State)
}

func TestGoalRuntimeHardStopStillAppliesWhenObjectiveWorkRemains(t *testing.T) {
	sess := session.NewSession("agent", "goal-session")
	gr := NewGoalRuntime(sess, nil, 1)
	goal := gr.StartGoal("Ship the patch", "goal-session", "")
	require.NotNil(t, goal)
	gr.RecordTurn(goal.ID, 1)
	sess.Append(session.TodoEntry(session.TodoData{
		ID:        "todo_1",
		Content:   "Ship the patch",
		Status:    "open",
		CreatedAt: time.Now().Unix(),
		RunID:     string(goal.ID),
	}))

	result, err := gr.MaybeContinueIfIdle(context.Background(), goal.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldContinue)
	assert.True(t, result.HardStop)
	assert.Contains(t, result.Reason, "reached max turns")
	assert.Contains(t, result.Reason, "open todo")

	current, ok := gr.GetGoal(goal.ID)
	require.True(t, ok)
	assert.Equal(t, GoalStateInProgress, current.State)
}
