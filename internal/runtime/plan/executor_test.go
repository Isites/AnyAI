package plan

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockStore struct {
	plan Plan
}

func (m *mockStore) UpdateStructuredPlan(plan Plan) error {
	m.plan = Normalize(plan)
	return nil
}

func (m *mockStore) GetStructuredPlan() (Plan, bool) {
	if m.plan.ID == "" {
		return Plan{}, false
	}
	return Normalize(m.plan), true
}

type mockDispatcher struct {
	toolName      string
	toolInput     string
	toolOutput    string
	toolErr       error
	agentTarget   string
	agentInput    string
	agentOutput   string
	agentErr      error
	awaitMessage  string
	awaitErr      error
	checkpointID  string
	checkEvidence string
	checkErr      error
}

func (m *mockDispatcher) ExecuteTool(_ context.Context, name, input string) (string, error) {
	m.toolName = name
	m.toolInput = input
	return m.toolOutput, m.toolErr
}

func (m *mockDispatcher) ExecuteAgent(_ context.Context, target, input string) (string, error) {
	m.agentTarget = target
	m.agentInput = input
	return m.agentOutput, m.agentErr
}

func (m *mockDispatcher) AwaitUserInput(_ context.Context, message string) error {
	m.awaitMessage = message
	return m.awaitErr
}

func (m *mockDispatcher) CompleteCheckpoint(_ context.Context, checkpointID, evidence string) error {
	m.checkpointID = checkpointID
	m.checkEvidence = evidence
	return m.checkErr
}

func TestExecutorExecutesReadyToolStep(t *testing.T) {
	store := &mockStore{plan: Plan{
		ID:          "plan_1",
		Description: "Run tool plan",
		State:       PlanStateRunning,
		Steps: []Step{
			{
				ID:          "step_1",
				Description: "Read the file",
				State:       StepStatePending,
				Action: StepAction{
					Type:   "tool",
					Target: "read_file",
					Input:  `{"path":"README.md"}`,
				},
			},
		},
	}}
	dispatcher := &mockDispatcher{toolOutput: "file contents"}
	exec := NewExecutor(store, dispatcher)

	result, err := exec.Execute(context.Background(), "plan_1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ExecuteStatusCompleted, result.Status)
	assert.Equal(t, "read_file", dispatcher.toolName)
	assert.Equal(t, `{"path":"README.md"}`, dispatcher.toolInput)
	assert.Equal(t, PlanStateCompleted, result.Plan.State)
	require.NotNil(t, result.Step)
	assert.Equal(t, StepStateCompleted, result.Step.State)
	assert.Equal(t, "file contents", result.Step.Output)
}

func TestExecutorExecutesAgentStepAfterDependencies(t *testing.T) {
	store := &mockStore{plan: Plan{
		ID:    "plan_2",
		State: PlanStateRunning,
		Steps: []Step{
			{ID: "step_done", State: StepStateCompleted},
			{
				ID:           "step_agent",
				Description:  "Ask reviewer",
				State:        StepStatePending,
				Dependencies: []string{"step_done"},
				Action: StepAction{
					Type:   "agent",
					Target: "reviewer",
					Input:  "Review the patch",
				},
			},
		},
	}}
	dispatcher := &mockDispatcher{agentOutput: "approved"}
	exec := NewExecutor(store, dispatcher)

	result, err := exec.Execute(context.Background(), "plan_2")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ExecuteStatusCompleted, result.Status)
	assert.Equal(t, "reviewer", dispatcher.agentTarget)
	assert.Equal(t, "Review the patch", dispatcher.agentInput)
	require.NotNil(t, result.Step)
	assert.Equal(t, "step_agent", result.Step.ID)
	assert.Equal(t, StepStateCompleted, result.Step.State)
	assert.Equal(t, "approved", result.Step.Output)
}

func TestExecutorPausesPlanForUserInput(t *testing.T) {
	store := &mockStore{plan: Plan{
		ID:    "plan_3",
		State: PlanStateRunning,
		Steps: []Step{
			{
				ID:          "step_user",
				Description: "Need the target environment",
				State:       StepStatePending,
				Action: StepAction{
					Type:  "user_input",
					Input: "Which environment should I use?",
				},
			},
		},
	}}
	dispatcher := &mockDispatcher{}
	exec := NewExecutor(store, dispatcher)

	result, err := exec.Execute(context.Background(), "plan_3")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ExecuteStatusPaused, result.Status)
	assert.Equal(t, "Which environment should I use?", dispatcher.awaitMessage)
	assert.Equal(t, PlanStatePaused, result.Plan.State)
	require.NotNil(t, result.Step)
	assert.Equal(t, StepStatePending, result.Step.State)
}

func TestExecutorMarksStepFailedOnDispatchError(t *testing.T) {
	store := &mockStore{plan: Plan{
		ID:    "plan_4",
		State: PlanStateRunning,
		Steps: []Step{
			{
				ID:    "step_fail",
				State: StepStatePending,
				Action: StepAction{
					Type:   "tool",
					Target: "bash",
					Input:  `{"command":"exit 1"}`,
				},
			},
		},
	}}
	dispatcher := &mockDispatcher{toolErr: errors.New("boom")}
	exec := NewExecutor(store, dispatcher)

	result, err := exec.Execute(context.Background(), "plan_4")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ExecuteStatusFailed, result.Status)
	assert.Equal(t, PlanStateFailed, result.Plan.State)
	require.NotNil(t, result.Step)
	assert.Equal(t, StepStateFailed, result.Step.State)
	assert.Equal(t, "boom", result.Step.Error)
}

func TestExecutorCompletesCheckpointStep(t *testing.T) {
	store := &mockStore{plan: Plan{
		ID:    "plan_5",
		State: PlanStateRunning,
		Steps: []Step{
			{
				ID:    "cp_1",
				State: StepStatePending,
				Action: StepAction{
					Type:   "checkpoint",
					Target: "cp_release",
					Input:  "release notes published",
				},
			},
		},
	}}
	dispatcher := &mockDispatcher{}
	exec := NewExecutor(store, dispatcher)

	result, err := exec.Execute(context.Background(), "plan_5")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "cp_release", dispatcher.checkpointID)
	assert.Equal(t, "release notes published", dispatcher.checkEvidence)
	require.NotNil(t, result.Step)
	assert.Equal(t, StepStateCompleted, result.Step.State)
	assert.Equal(t, "release notes published", result.Step.Output)
}
