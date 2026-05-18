package startup

import (
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	airuntime "github.com/Isites/anyai/internal/runtime"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeexecution "github.com/Isites/anyai/internal/runtime/execution"
	runtimefactory "github.com/Isites/anyai/internal/runtime/factory"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimememorylifecycle "github.com/Isites/anyai/internal/runtime/memory/lifecycle"
	runtimeResources "github.com/Isites/anyai/internal/runtime/resources"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	runtimetaskagent "github.com/Isites/anyai/internal/runtime/task/agentexec"
	runtimetaskbuiltin "github.com/Isites/anyai/internal/runtime/task/builtin"
	"github.com/Isites/anyai/internal/runtime/tool"
)

// CoreRuntime contains the shared runtime components used by both interactive
// CLI sessions and the long-running gateway/server process.
type CoreRuntime struct {
	Config         *config.Config
	DataDir        string
	Providers      map[string]llm.LLMProvider
	SessionStore   *session.Store
	ToolRegistry   *tools.Registry
	SkillLoader    *skill.Loader
	Memory         *memory.Manager
	Recorder       *runtimeevents.Recorder
	MemoryPipeline *runtimememorylifecycle.Pipeline
	TaskStore      *task.Store
	TaskRuntime    *task.Runtime
	Runtime        *airuntime.Runtime
	Gateway        *gateway.Service
	Dependencies   *runtimeport.DependencySet
	ChannelManager *gateway.ChannelManager
	AgentRunner    RuntimeAgentCaller
}

type RuntimeAgentCaller interface {
	tools.AgentCallRunner
	SetSender(sender tools.MessageSender)
	SetSkills(skills *skill.Loader)
	SetResources(resources *runtimeResources.Catalog)
	SetMemory(mem *memory.Manager)
	SetRecorder(recorder *runtimeevents.Recorder)
	SetMemoryPipeline(pipeline *runtimememorylifecycle.Pipeline)
	SetTaskStore(store *task.Store)
	SetTaskRuntime(runtime *task.Runtime)
}

// BuildCoreRuntimeWithConfig assembles the shared runtime without attaching any
// transport-specific channels or servers. Callers can register channels and
// start the channel manager afterwards.
func BuildCoreRuntimeWithConfig(cfg *config.Config, opts ...Options) (*CoreRuntime, error) {
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}

	spec, err := runtimefactory.BuildRuntimeSpec(cfg, opt.ProviderOverrides)
	if err != nil {
		return nil, err
	}
	applyLaunchModeChannels(cfg, opt.LaunchMode, spec.Layout.DataDir)
	base, err := runtimefactory.BuildBaseComponents(
		spec.Config,
		spec.Layout,
		spec.Providers,
		runtimefactory.NewToolRegistry(cfg),
	)
	if err != nil {
		return nil, err
	}

	sessionCoordinator := runtimeexecution.NewCoordinator()
	base.Dependencies.SetSessionCoordinator(sessionCoordinator)

	runtimeService := airuntime.WrapDependencies(base.Dependencies)
	gatewayService := gateway.New(runtimeService)
	gatewayService.ApplyRuntimeConfig(cfg, runtimefactory.ResolveFallbackAgentID(cfg, opt.FallbackAgentID))
	base.Dependencies.SetIngressResolver(gatewayService.RuntimeIngressResolver())
	chanMgr := gateway.NewChannelManager(gatewayService, cfg.Security.DMPolicy.UnknownSenders)
	if opt.ConnectTimeout > 0 {
		chanMgr.SetConnectTimeout(opt.ConnectTimeout)
	}
	gatewayService.SetChannelManager(chanMgr)
	base.Dependencies.SetSender(chanMgr)

	agentRunner := RuntimeAgentCaller(runtimeService)

	if base.TaskRuntime != nil {
		agentExecutor := runtimetaskagent.New()
		agentExecutor.SetRunner(runtimeService)
		if err := base.TaskRuntime.Registry().Register(task.KindAgent, agentExecutor); err != nil {
			return nil, err
		}
		if err := base.TaskRuntime.Registry().Register(task.KindTool, runtimetaskbuiltin.NewToolExecutor(base.ToolRegistry)); err != nil {
			return nil, err
		}
		if err := base.TaskRuntime.Registry().Register(task.KindProcess, runtimetaskbuiltin.NewProcessExecutor()); err != nil {
			return nil, err
		}
		agentRunner.SetTaskRuntime(base.TaskRuntime)
	}

	runtimefactory.RegisterRuntimeTools(
		base.ToolRegistry,
		chanMgr,
		agentRunner,
		base.Memory,
		cfg.Runtime.AgentCall.MaxParallel,
		cfg.Runtime.IdleTimeoutMS,
	)

	base.Dependencies.SetAgentRunner(agentRunner)

	spec, err = spec.WithRuntimeResources(base.Dependencies.ExecutionDeps())
	if err != nil {
		runtimelogging.Warn("failed to build runtime resource catalog", "error", err)
		spec.Skills = base.SkillLoader
	}
	if spec.Skills == nil {
		spec.Skills = base.SkillLoader
	}
	if err := runtimeService.ApplySpec(spec); err != nil {
		return nil, err
	}

	return &CoreRuntime{
		Config:         cfg,
		DataDir:        spec.Layout.DataDir,
		Providers:      spec.Providers,
		SessionStore:   base.SessionStore,
		ToolRegistry:   base.ToolRegistry,
		SkillLoader:    spec.Skills,
		Memory:         base.Memory,
		Recorder:       base.Recorder,
		MemoryPipeline: base.MemoryPipeline,
		TaskStore:      base.TaskStore,
		TaskRuntime:    base.TaskRuntime,
		Runtime:        runtimeService,
		Gateway:        gatewayService,
		Dependencies:   base.Dependencies,
		ChannelManager: chanMgr,
		AgentRunner:    agentRunner,
	}, nil
}
