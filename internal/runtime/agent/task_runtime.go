package agent

import (
	"context"
	"fmt"

	"github.com/Isites/anyai/internal/runtime/task"
	runtimetaskbuiltin "github.com/Isites/anyai/internal/runtime/task/builtin"
)

// ensureTaskRuntime makes doTask the default tool/process execution substrate
// even in lightweight test runtimes that were constructed without startup
// wiring.
func (r *Runtime) ensureTaskRuntime() {
	if r == nil || r.TaskRuntime != nil {
		return
	}

	registry := task.NewExecutorRegistry()
	_ = registry.Register(task.KindTool, runtimetaskbuiltin.NewToolExecutor(nil))
	_ = registry.Register(task.KindProcess, runtimetaskbuiltin.NewProcessExecutor())
	r.TaskRuntime = task.NewRuntime(task.NewStore(), registry)
}

func (r *Runtime) doTask(ctx context.Context, spec task.Spec) (string, error) {
	if r == nil {
		return "", fmt.Errorf("agent runtime is nil")
	}
	r.ensureTaskRuntime()
	return r.TaskRuntime.DoTask(ctx, spec)
}
