package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

func (r *Runtime) ensureGoalRuntime() {
	if r == nil {
		return
	}
	if r.GoalRuntime == nil {
		r.GoalRuntime = task.NewGoalRuntime(r.Session, r.TaskRuntime, r.MaxTurns)
	}
	r.ensureGoalTool()
}

type goalToolRegistrar interface {
	Register(tools.Tool)
}

type runtimeGoalManager struct {
	runtime *task.GoalRuntime
}

func (m runtimeGoalManager) CompleteGoal(ctx context.Context, goalID string) error {
	if m.runtime == nil {
		return fmt.Errorf("goal runtime is not available")
	}
	meta := tools.RuntimeContextFrom(ctx)
	return m.runtime.CompleteGoalIgnoringTask(
		task.GoalID(strings.TrimSpace(goalID)),
		strings.TrimSpace(meta.TaskID),
		strings.TrimSpace(meta.ToolCallID),
	)
}

func (m runtimeGoalManager) AwaitUserInput(_ context.Context, goalID, message string) error {
	if m.runtime == nil {
		return fmt.Errorf("goal runtime is not available")
	}
	return m.runtime.AwaitUserInput(task.GoalID(strings.TrimSpace(goalID)), strings.TrimSpace(message))
}

func (r *Runtime) ensureGoalTool() {
	if r == nil || r.GoalRuntime == nil || r.Tools == nil {
		return
	}
	if _, ok := r.Tools.Get("goal_complete"); ok {
		if _, awaitingOK := r.Tools.Get("await_user_input"); awaitingOK {
			return
		}
	}
	registrar, ok := r.Tools.(goalToolRegistrar)
	if !ok {
		return
	}
	manager := runtimeGoalManager{runtime: r.GoalRuntime}
	registrar.Register(&tools.GoalCompleteTool{Manager: manager})
	registrar.Register(&tools.AwaitUserInputTool{Manager: manager})
}
