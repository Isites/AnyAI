package session

import (
	"testing"

	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLatestPlanAndTodoViews(t *testing.T) {
	sess := NewSession("assistant", "session-1")
	sess.Append(PlanEntry("1. Read code\n2. Add tests"))
	sess.Append(TodoEntry(TodoData{
		ID:        "todo_1",
		Content:   "Write regression tests",
		Status:    "open",
		CreatedAt: 1710000000,
	}))
	sess.Append(TodoEntry(TodoData{
		ID:          "todo_1",
		Content:     "Write regression tests",
		Status:      "completed",
		CreatedAt:   1710000000,
		CompletedAt: 1710000300,
	}))

	plan, ok := LatestPlan(sess)
	require.True(t, ok)
	assert.Equal(t, "1. Read code\n2. Add tests", plan)

	todos := TodoItems(sess)
	require.Len(t, todos, 1)
	assert.Equal(t, "todo_1", todos[0].ID)
	assert.Equal(t, "Write regression tests", todos[0].Content)
	assert.Equal(t, "completed", todos[0].Status)
}

func TestSerializeSessionHistoryIncludesPlanAndTodo(t *testing.T) {
	sess := NewSession("assistant", "session-2")
	sess.Append(PlanEntry("先分析，再实现。"))
	sess.Append(TodoEntry(TodoData{
		ID:        "todo_1",
		Content:   "补测试",
		Status:    "open",
		CreatedAt: 1710000000,
	}))

	history := SerializeHistory(sess)
	require.Len(t, history, 2)
	assert.Equal(t, "plan", history[0]["type"])
	assert.Equal(t, "先分析，再实现。", history[0]["plan"])
	assert.Equal(t, "todo", history[1]["type"])
	assert.Equal(t, "todo_1", history[1]["id"])
	assert.Equal(t, "补测试", history[1]["content"])
	assert.Equal(t, "open", history[1]["status"])
}

func TestLatestStructuredPlanSkipsNewerLegacyPlanEntries(t *testing.T) {
	sess := NewSession("assistant", "session-3")
	sess.Append(StructuredPlanEntry(runtimeplan.Plan{
		ID:          "plan_1",
		Description: "Ship the fix",
		State:       runtimeplan.PlanStateRunning,
		Steps: []runtimeplan.Step{
			{ID: "inspect", Description: "Inspect", State: runtimeplan.StepStateCompleted},
			{ID: "patch", Description: "Patch", State: runtimeplan.StepStatePending},
		},
	}))
	sess.Append(PlanEntry("1. Inspect\n2. Patch"))

	structured, ok := LatestStructuredPlan(sess)
	require.True(t, ok)
	assert.Equal(t, "plan_1", structured.ID)

	plan, ok := LatestPlan(sess)
	require.True(t, ok)
	assert.Equal(t, "1. Inspect\n2. Patch", plan)
}

func TestTodoItemsForRunFiltersSessionTodos(t *testing.T) {
	sess := NewSession("assistant", "session-4")
	sess.Append(TodoEntry(TodoData{
		ID:        "todo_goal_1",
		Content:   "Goal 1 item",
		Status:    "open",
		CreatedAt: 1710000000,
		RunID:     "run_1",
	}))
	sess.Append(TodoEntry(TodoData{
		ID:        "todo_goal_2",
		Content:   "Goal 2 item",
		Status:    "open",
		CreatedAt: 1710000001,
		RunID:     "run_2",
	}))

	items := TodoItemsForRun(sess, "run_1", false)
	require.Len(t, items, 1)
	assert.Equal(t, "todo_goal_1", items[0].ID)
	assert.Equal(t, "run_1", items[0].RunID)
}
