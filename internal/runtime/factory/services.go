package factory

import (
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimememorylifecycle "github.com/Isites/anyai/internal/runtime/memory/lifecycle"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/tool"
	"github.com/Isites/anyai/internal/runtime/turn"
)

type BaseComponents struct {
	Layout         ProjectLayout
	Providers      map[string]llm.LLMProvider
	SessionStore   *session.Store
	ToolRegistry   *tools.Registry
	SkillLoader    *skill.Loader
	Memory         *memory.Manager
	Recorder       *runtimeevents.Recorder
	MemoryPipeline *runtimememorylifecycle.Pipeline
	TaskStore      *task.Store
	TaskRuntime    *task.Runtime
	TurnStore      *turn.Store
	Dependencies   *runtimeport.DependencySet
}

func NewToolRegistry(cfg *config.Config) *tools.Registry {
	registry := tools.NewRegistry()
	execPolicy := &tools.ExecPolicy{
		Level:     cfg.Security.ExecApprovals.Level,
		Allowlist: cfg.Security.ExecApprovals.Allowlist,
	}
	tools.RegisterCoreTools(registry, "", execPolicy)
	return registry
}

func BuildBaseComponents(
	cfg *config.Config,
	layout ProjectLayout,
	providers map[string]llm.LLMProvider,
	toolRegistry *tools.Registry,
) (*BaseComponents, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if toolRegistry == nil {
		toolRegistry = NewToolRegistry(cfg)
	}

	sessionStore := session.NewStore(layout.SessionsDir)
	skillLoader := skill.NewLoader()

	var memMgr *memory.Manager
	if cfg.Memory.Enabled {
		memMgr = memory.NewManager(layout.MemoryDir)
		if interval, err := time.ParseDuration(strings.TrimSpace(cfg.Memory.AutoCapture.CleanupInterval)); err == nil && interval > 0 {
			memMgr.SetCleanupInterval(interval)
		}
		if err := memMgr.Load(); err != nil {
			runtimelogging.Warn("failed to load memory", "error", err)
		}
	}

	recorder, err := runtimeevents.NewPersistentRecorder(layout.EventsDir)
	if err != nil {
		return nil, fmt.Errorf("create event recorder: %w", err)
	}

	memoryPipeline := runtimememorylifecycle.NewPipeline(memMgr, cfg.Memory)
	deps := runtimeport.NewDependencySet(providers, sessionStore, cfg)

	turnStore := turn.NewStore()
	taskStore := task.NewStore(task.WithEventAppender(func(e runtimeevents.EventRecord) {
		runtimeevents.AppendEventWithReplayPolicy(recorder, e)
	}))
	taskRuntime := task.NewRuntime(taskStore, nil)
	taskRuntime.SetEventAppender(func(e runtimeevents.EventRecord) {
		runtimeevents.AppendEventWithReplayPolicy(recorder, e)
	})
	taskRuntime.SetTurnStore(turnStore)
	deps.SetMemory(memMgr)
	deps.SetRecorder(recorder)
	deps.SetMemoryPipeline(memoryPipeline)
	deps.SetTaskStore(taskStore)
	deps.SetTaskRuntime(taskRuntime)
	deps.SetTurnStore(turnStore)

	return &BaseComponents{
		Layout:         layout,
		Providers:      providers,
		SessionStore:   sessionStore,
		ToolRegistry:   toolRegistry,
		SkillLoader:    skillLoader,
		Memory:         memMgr,
		Recorder:       recorder,
		MemoryPipeline: memoryPipeline,
		TaskStore:      taskStore,
		TaskRuntime:    taskRuntime,
		TurnStore:      turnStore,
		Dependencies:   deps,
	}, nil
}

func ResolveFallbackAgentID(cfg *config.Config, requested string) string {
	fallbackAgent := strings.TrimSpace(requested)
	if fallbackAgent != "" {
		return fallbackAgent
	}
	if cfg == nil || len(cfg.Agents.List) == 0 {
		return "default"
	}
	return cfg.Agents.List[0].ID
}

func RegisterRuntimeTools(
	registry *tools.Registry,
	sender tools.MessageSender,
	agentRunner tools.AgentCallRunner,
	memMgr *memory.Manager,
	maxParallelAgentCalls int,
	defaultIdleTimeoutMS int,
) {
	if registry == nil {
		return
	}

	if sender != nil {
		tools.RegisterSendMessage(registry, sender)
	}
	if agentRunner != nil {
		tools.RegisterCallAgent(registry, agentRunner, maxParallelAgentCalls, defaultIdleTimeoutMS)
	}

	if memMgr != nil {
		memAdapter := &tools.MemoryProviderAdapter{Manager: memMgr}
		tools.RegisterMemoryTools(registry, memAdapter)
	}
}

func BuildResourceCatalog(cfg *config.Config, deps runtimeport.ExecutionDeps) (*runtimeresources.Catalog, *skill.Loader, error) {
	catalog, err := runtimeresources.BuildCatalog(cfg, runtimeresources.BuildDeps{
		Sender:       deps.Sender,
		AgentRunner:  deps.AgentRunner,
		JobScheduler: deps.JobScheduler,
		Memory:       deps.Memory,
	})
	if err != nil {
		return nil, nil, err
	}

	loader := skill.NewLoader()
	if catalog != nil && catalog.GlobalLoader() != nil {
		loader = catalog.GlobalLoader()
	}
	return catalog, loader, nil
}
