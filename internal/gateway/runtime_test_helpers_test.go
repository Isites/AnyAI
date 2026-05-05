package gateway

import (
	"testing"

	"github.com/Isites/anyai/internal/config"
	airuntime "github.com/Isites/anyai/internal/runtime"
	runtimefactory "github.com/Isites/anyai/internal/runtime/factory"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/task"
	runtimetaskagent "github.com/Isites/anyai/internal/runtime/task/agentexec"
	runtimetaskbuiltin "github.com/Isites/anyai/internal/runtime/task/builtin"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

func configureGatewayTaskRuntime(
	t *testing.T,
	rt *airuntime.Runtime,
	cfg *config.Config,
	taskStore *task.Store,
	sender tools.MessageSender,
) *task.Runtime {
	t.Helper()

	toolRegistry := runtimefactory.NewToolRegistry(cfg)
	rt.SetAgentRunner(rt)
	if sender != nil {
		rt.SetSender(sender)
	}
	maxParallel := 0
	if cfg != nil {
		maxParallel = cfg.Runtime.AgentCall.MaxParallel
	}
	runtimefactory.RegisterRuntimeTools(toolRegistry, sender, rt, rt.Memory(), maxParallel, cfg.Runtime.IdleTimeoutMS)
	catalog, skillLoader, err := runtimefactory.BuildResourceCatalog(cfg, runtimeport.ExecutionDeps{
		Sender:      sender,
		AgentRunner: rt,
		Memory:      rt.Memory(),
	})
	if err != nil {
		t.Fatalf("build runtime resource catalog: %v", err)
	}
	rt.SetResources(catalog)
	rt.SetSkills(skillLoader)

	registry := task.NewExecutorRegistry()

	agentExecutor := runtimetaskagent.New()
	agentExecutor.SetRunner(rt)
	if err := registry.Register(task.KindAgent, agentExecutor); err != nil {
		t.Fatalf("register agent executor: %v", err)
	}
	if err := registry.Register(task.KindTool, runtimetaskbuiltin.NewToolExecutor(toolRegistry)); err != nil {
		t.Fatalf("register tool executor: %v", err)
	}
	if err := registry.Register(task.KindProcess, runtimetaskbuiltin.NewProcessExecutor()); err != nil {
		t.Fatalf("register process executor: %v", err)
	}

	taskRuntime := task.NewRuntime(taskStore, registry)
	rt.SetTaskRuntime(taskRuntime)
	return taskRuntime
}
