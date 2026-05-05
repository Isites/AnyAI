package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockGoalManager struct {
	lastGoalID string
	lastAwait  string
	err        error
}

func (m *mockGoalManager) CompleteGoal(_ context.Context, goalID string) error {
	m.lastGoalID = goalID
	return m.err
}

func (m *mockGoalManager) AwaitUserInput(_ context.Context, goalID, message string) error {
	m.lastGoalID = goalID
	m.lastAwait = message
	return m.err
}

func TestGoalCompleteToolSuccess(t *testing.T) {
	manager := &mockGoalManager{}
	tool := &GoalCompleteTool{Manager: manager}
	ctx := WithRuntimeContext(context.Background(), RuntimeContext{RunID: "run_1"})

	result, err := tool.Execute(ctx, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "run_1", manager.lastGoalID)
	assert.Equal(t, "Goal run_1 marked as completed.", result.Output)
	assert.Empty(t, result.Error)
	assert.Equal(t, "run_1", result.Metadata["run_id"])
}

func TestGoalCompleteToolRequiresGoalContext(t *testing.T) {
	tool := &GoalCompleteTool{Manager: &mockGoalManager{}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, result.Error, "no active goal")
}

func TestGoalCompleteToolSurfacesRuntimeRejection(t *testing.T) {
	tool := &GoalCompleteTool{
		Manager: &mockGoalManager{
			err: errors.New(`goal "run_1" still has unfinished work: open todo items: Ship patch (todo_1)`),
		},
	}
	ctx := WithRuntimeContext(context.Background(), RuntimeContext{RunID: "run_1"})

	result, err := tool.Execute(ctx, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, result.Error, "unfinished work")
	assert.Contains(t, result.Error, "todo_1")
}

func TestRegisterGoalTools(t *testing.T) {
	reg := NewRegistry()
	RegisterGoalTools(reg, &mockGoalManager{})

	tool, ok := reg.Get("goal_complete")
	require.True(t, ok)
	assert.Equal(t, "goal_complete", tool.Name())

	awaitTool, ok := reg.Get("await_user_input")
	require.True(t, ok)
	assert.Equal(t, "await_user_input", awaitTool.Name())
}
