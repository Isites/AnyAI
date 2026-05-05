package agentexec

import (
	"context"
	"testing"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingRunner struct {
	req runtimeport.RunRequest
}

func (r *capturingRunner) StartManagedRun(_ context.Context, req runtimeport.RunRequest) (*runtimeport.ManagedRun, error) {
	r.req = req
	events := make(chan runtimeevents.EventRecord, 1)
	events <- runtimeevents.EventRecord{Name: runtimeevents.EventRunCompleted}
	close(events)
	return &runtimeport.ManagedRun{
		RunID:  "run_child",
		Events: events,
	}, nil
}

func TestExecutorExecutePropagatesTaskIDToChildRun(t *testing.T) {
	runner := &capturingRunner{}
	exec := New()
	exec.SetRunner(runner)

	result, err := exec.Execute(context.Background(), task.Record{
		ID:          "task_parent",
		Kind:        task.KindAgent,
		SessionID:   "child-session",
		RunID:       "run_parent",
		AgentID:     "tech-lead",
		TargetAgent: "coder",
		Input:       "please review this change",
	})
	require.NoError(t, err)
	assert.Equal(t, task.StatusCompleted, result.Status)
	assert.Equal(t, "task_parent", runner.req.TaskID)
	assert.Equal(t, "coder", runner.req.AgentID)
	assert.Equal(t, "child-session", runner.req.SessionID)
}
