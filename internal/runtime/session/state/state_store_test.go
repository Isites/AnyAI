package state

import (
	"context"
	"testing"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateStorePersistsPlanTodoAndEvents(t *testing.T) {
	recorder := runtimeevents.NewRecorder()
	run := runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "assistant",
		SessionID: "sess_1",
		Status:    runtimeevents.RunStatusRunning,
	}
	recorder.StartRun(run)

	sess := session.NewSession("assistant", "sess_1")
	store := NewStateStore(sess, recorder, run)
	now := time.Unix(1710000000, 0).UTC()
	store.nowFn = func() time.Time { return now }

	require.NoError(t, store.UpdateStructuredPlan(runtimeplan.Plan{
		ID:          "plan_1",
		Description: "Ship the fix",
		State:       runtimeplan.PlanStateRunning,
		Steps: []runtimeplan.Step{
			{ID: "inspect", Description: "Inspect", State: runtimeplan.StepStateCompleted},
			{ID: "patch", Description: "Patch", State: runtimeplan.StepStatePending},
		},
	}))
	todoID := store.AddTodo(context.Background(), "Write tests")
	require.NotEmpty(t, todoID)

	now = now.Add(5 * time.Minute)
	store.nowFn = func() time.Time { return now }
	require.True(t, store.CompleteTodo(context.Background(), todoID))

	plan, ok := store.GetStructuredPlan()
	require.True(t, ok)
	assert.Equal(t, "plan_1", plan.ID)
	assert.Equal(t, runtimeplan.PlanStateRunning, plan.State)

	todos := store.ListTodos(context.Background())
	require.Len(t, todos, 1)
	assert.Equal(t, todoID, todos[0].ID)
	assert.Equal(t, "completed", todos[0].Status)

	events := recorder.ListRunEvents(run.ID)
	require.Len(t, events, 3)
	assert.Equal(t, "session.plan.updated", events[0].Name)
	assert.Equal(t, "session.todo.updated", events[1].Name)
	assert.Equal(t, "session.todo.updated", events[2].Name)
	_, hasPlanText := events[0].Payload["plan"]
	assert.False(t, hasPlanText)
	_, hasStructured := events[0].Payload["structured"]
	assert.True(t, hasStructured)
	assert.Equal(t, todoID, events[2].Payload["id"])
	assert.Equal(t, "completed", events[2].Payload["status"])
}

func TestStateStoreStructuredPlanEventDoesNotDuplicateRenderedText(t *testing.T) {
	recorder := runtimeevents.NewRecorder()
	run := runtimeevents.RunRecord{
		ID:        "run_structured",
		AgentID:   "assistant",
		SessionID: "sess_structured",
		Status:    runtimeevents.RunStatusRunning,
	}
	recorder.StartRun(run)

	sess := session.NewSession("assistant", "sess_structured")
	store := NewStateStore(sess, recorder, run)

	require.NoError(t, store.UpdateStructuredPlan(runtimeplan.Plan{
		ID:          "plan_1",
		Description: "Ship the fix",
		State:       runtimeplan.PlanStateRunning,
		Steps: []runtimeplan.Step{
			{ID: "inspect", Description: "Inspect", State: runtimeplan.StepStateCompleted},
			{ID: "patch", Description: "Patch", State: runtimeplan.StepStatePending},
		},
	}))

	events := recorder.ListRunEvents(run.ID)
	require.Len(t, events, 1)
	assert.Equal(t, "session.plan.updated", events[0].Name)
	_, hasPlanText := events[0].Payload["plan"]
	assert.False(t, hasPlanText)
	_, hasStructured := events[0].Payload["structured"]
	assert.True(t, hasStructured)

	latest, ok := session.LatestStructuredPlan(sess)
	require.True(t, ok)
	assert.Equal(t, "plan_1", latest.ID)
}
