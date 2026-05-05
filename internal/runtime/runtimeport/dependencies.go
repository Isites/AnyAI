package runtimeport

import (
	"sync"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimememorylifecycle "github.com/Isites/anyai/internal/runtime/memory/lifecycle"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/tool"
	"github.com/Isites/anyai/internal/runtime/turn"
)

const (
	SystemAgentID = runtimeevents.SystemAgentID
)

// DependencySet is the mutable runtime dependency holder assembled at startup.
type DependencySet struct {
	mu                 sync.RWMutex
	providers          map[string]llm.LLMProvider
	config             *config.Config
	sessionStore       *session.Store
	sessionCoordinator IngressCoordinator
	ingressResolver    IngressAgentResolver
	sender             tools.MessageSender
	agentRunner        tools.AgentCallRunner
	jobScheduler       tools.JobScheduler
	skills             *skill.Loader
	resources          *runtimeresources.Catalog
	memory             *memory.Manager
	recorder           *runtimeevents.Recorder
	memoryPipeline     *runtimememorylifecycle.Pipeline
	taskStore          *task.Store
	taskRuntime        *task.Runtime
	turnStore          *turn.Store
	runtimeConfigurer  AgentRuntimeConfigurer
}

func NewDependencySet(
	providers map[string]llm.LLMProvider,
	sessionStore *session.Store,
	cfg *config.Config,
) *DependencySet {
	return &DependencySet{
		providers:    providers,
		config:       cfg,
		sessionStore: sessionStore,
	}
}

func (r *DependencySet) Config() *config.Config {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config
}

func (r *DependencySet) SetProviders(providers map[string]llm.LLMProvider) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
}

func (r *DependencySet) Agents() []config.AgentConfig {
	cfg := r.Config()
	if cfg == nil {
		return nil
	}
	return append([]config.AgentConfig(nil), cfg.Agents.List...)
}

func (r *DependencySet) SessionStore() *session.Store {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessionStore
}

func (r *DependencySet) Resources() *runtimeresources.Catalog {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resources
}

func (r *DependencySet) Recorder() *runtimeevents.Recorder {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.recorder
}

func (r *DependencySet) Memory() *memory.Manager {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.memory
}

func (r *DependencySet) ExecutionDeps() ExecutionDeps {
	if r == nil {
		return ExecutionDeps{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return ExecutionDeps{
		Providers:          r.providers,
		Config:             r.config,
		SessionStore:       r.sessionStore,
		SessionCoordinator: r.sessionCoordinator,
		IngressResolver:    r.ingressResolver,
		Sender:             r.sender,
		AgentRunner:        r.agentRunner,
		JobScheduler:       r.jobScheduler,
		Skills:             r.skills,
		Resources:          r.resources,
		Memory:             r.memory,
		Recorder:           r.recorder,
		Pipeline:           r.memoryPipeline,
		TaskStore:          r.taskStore,
		TaskRuntime:        r.taskRuntime,
		TurnStore:          r.turnStore,
		RuntimeConfigurer:  r.runtimeConfigurer,
	}
}

func (r *DependencySet) UpdateConfig(cfg *config.Config) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.config = cfg
	recorder := r.recorder
	r.mu.Unlock()

	if recorder != nil {
		payload := map[string]any{
			"project_name":    "",
			"agent_count":     0,
			"active_channels": []string{},
		}
		if cfg != nil {
			payload["project_name"] = cfg.ProjectName
			payload["agent_count"] = len(cfg.Agents.List)
			payload["active_channels"] = append([]string(nil), cfg.ActiveChannels...)
		}
		r.appendSystemEvent(recorder, "config.reloaded", payload)
	}
}

func (r *DependencySet) SetSessionCoordinator(coordinator IngressCoordinator) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionCoordinator = coordinator
}

func (r *DependencySet) SetIngressResolver(resolver IngressAgentResolver) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ingressResolver = resolver
}

func (r *DependencySet) SetSender(sender tools.MessageSender) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sender = sender
}

func (r *DependencySet) SetJobScheduler(js tools.JobScheduler) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobScheduler = js
}

func (r *DependencySet) SetAgentRunner(runner tools.AgentCallRunner) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentRunner = runner
}

func (r *DependencySet) SetRecorder(recorder *runtimeevents.Recorder) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recorder = recorder
}

func (r *DependencySet) TaskStore() *task.Store {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.taskStore
}

func (r *DependencySet) TaskRuntime() *task.Runtime {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.taskRuntime
}

func (r *DependencySet) TurnStore() *turn.Store {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.turnStore
}

func (r *DependencySet) SetTaskStore(store *task.Store) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taskStore = store
}

func (r *DependencySet) SetTaskRuntime(runtime *task.Runtime) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taskRuntime = runtime
}

func (r *DependencySet) SetTurnStore(store *turn.Store) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnStore = store
}

func (r *DependencySet) SetSkills(skills *skill.Loader) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.skills = skills
	recorder := r.recorder
	r.mu.Unlock()

	if recorder != nil {
		payload := map[string]any{"skill_count": 0}
		if skills != nil {
			payload["skill_count"] = len(skills.Skills())
		}
		r.appendSystemEvent(recorder, "skills.reloaded", payload)
	}
}

func (r *DependencySet) SetResources(resources *runtimeresources.Catalog) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.resources = resources
	recorder := r.recorder
	r.mu.Unlock()

	if recorder != nil {
		payload := map[string]any{
			"agent_count":        0,
			"system_skill_count": 0,
			"shared_skill_count": 0,
		}
		if resources != nil {
			payload["agent_count"] = len(resources.Agents())
			payload["system_skill_count"] = len(resources.SystemSkills())
			payload["shared_skill_count"] = len(resources.SharedSkills())
		}
		r.appendSystemEvent(recorder, "resources.reindexed", payload)
	}
}

func (r *DependencySet) SetMemory(mem *memory.Manager) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memory = mem
}

func (r *DependencySet) appendSystemEvent(recorder *runtimeevents.Recorder, name string, payload map[string]any) {
	if r == nil || recorder == nil {
		return
	}
	runtimeevents.AppendSyntheticRunEvent(recorder, runtimeevents.SyntheticRunSpec{
		AgentID: SystemAgentID,
		Model:   "system/runtime",
		Channel: "system",
	}, name, payload, name, "")
}

func (r *DependencySet) SetMemoryPipeline(pipeline *runtimememorylifecycle.Pipeline) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memoryPipeline = pipeline
}

func (r *DependencySet) SetRuntimeConfigurer(configurer AgentRuntimeConfigurer) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runtimeConfigurer = configurer
}
