package tools

import (
	"context"

	"github.com/Isites/anyai/internal/config"
)

// AssistantOutputProvider exposes the latest assistant-authored text so tools
// can persist large artifacts without repeating them inside JSON arguments.
type AssistantOutputProvider interface {
	LatestAssistantMessageText() (string, bool)
}

// GoalManager exposes runtime-managed goal completion to tools without
// importing the task package into the tool layer.
type GoalManager interface {
	CompleteGoal(ctx context.Context, goalID string) error
	AwaitUserInput(ctx context.Context, goalID, message string) error
}

// ExtraToolDeps groups optional runtime-bound tools that are added on top of
// the base per-agent registry.
type ExtraToolDeps struct {
	MemoryProvider        MemoryProvider
	SkillProvider         SkillProvider
	AttachmentProvider    AttachmentProvider
	InputManifestProvider InputManifestProvider
	PlanStore             PlanStore
	TodoStore             TodoStore
	AssistantOutput       AssistantOutputProvider
	GoalManager           GoalManager
}

// BuildAgentRegistry builds a workspace-aware registry for a specific agent.
func BuildAgentRegistry(cfg *config.Config, agentCfg *config.AgentConfig, sender MessageSender, runner AgentCallRunner, scheduler JobScheduler) *Registry {
	return BuildAgentRegistryWithExtras(cfg, agentCfg, sender, runner, scheduler, ExtraToolDeps{})
}

// BuildAgentRegistryWithExtras builds a workspace-aware registry for a
// specific agent and augments it with runtime-scoped tools when available.
func BuildAgentRegistryWithExtras(cfg *config.Config, agentCfg *config.AgentConfig, sender MessageSender, runner AgentCallRunner, scheduler JobScheduler, extras ExtraToolDeps) *Registry {
	reg := NewRegistry()

	execPolicy := &ExecPolicy{}
	if cfg != nil {
		execPolicy.Level = cfg.Security.ExecApprovals.Level
		execPolicy.Allowlist = cfg.Security.ExecApprovals.Allowlist
	}
	RegisterCoreTools(reg, agentCfg.Workspace, execPolicy)

	if sender != nil {
		RegisterSendMessage(reg, sender)
	}
	if runner != nil {
		maxParallel := 0
		defaultIdleTimeoutMS := 0
		if cfg != nil {
			maxParallel = cfg.Runtime.AgentCall.MaxParallel
			defaultIdleTimeoutMS = cfg.Runtime.IdleTimeoutMS
		}
		RegisterCallAgent(reg, runner, maxParallel, defaultIdleTimeoutMS)
	}
	if scheduler != nil {
		RegisterCron(reg, scheduler)
	}
	RegisterMemoryTools(reg, extras.MemoryProvider)
	RegisterSkillTools(reg, extras.SkillProvider)
	RegisterInputTools(reg, extras.AttachmentProvider, extras.InputManifestProvider)
	RegisterPlanTodoTools(reg, extras.PlanStore, extras.TodoStore)
	RegisterGoalTools(reg, extras.GoalManager)
	if extras.AssistantOutput != nil {
		reg.Register(&SaveOutputTool{
			WorkDir:        agentCfg.Workspace,
			OutputProvider: extras.AssistantOutput,
		})
	}

	return reg
}

// ExecutorForAgent applies the agent's allow/deny policy on top of a workspace-aware registry.
func ExecutorForAgent(cfg *config.Config, agentCfg *config.AgentConfig, sender MessageSender, runner AgentCallRunner, scheduler JobScheduler) Executor {
	return ExecutorForAgentWithExtras(cfg, agentCfg, sender, runner, scheduler, ExtraToolDeps{})
}

// ExecutorForAgentWithExtras applies the agent's allow/deny policy on top of a
// workspace-aware registry with optional runtime-scoped tools.
func ExecutorForAgentWithExtras(cfg *config.Config, agentCfg *config.AgentConfig, sender MessageSender, runner AgentCallRunner, scheduler JobScheduler, extras ExtraToolDeps) Executor {
	reg := BuildAgentRegistryWithExtras(cfg, agentCfg, sender, runner, scheduler, extras)
	if agentCfg == nil {
		return reg
	}
	if len(agentCfg.Tools.Allow) == 0 && len(agentCfg.Tools.Deny) == 0 {
		return reg
	}
	allow := append([]string(nil), agentCfg.Tools.Allow...)
	if len(allow) == 0 {
		if extras.SkillProvider != nil && !includesTool(allow, "skill_get") {
			allow = append(allow, "skill_get")
		}
	} else {
		if extras.GoalManager != nil && !includesTool(allow, "goal_complete") {
			allow = append(allow, "goal_complete")
		}
		if extras.GoalManager != nil && !includesTool(allow, "await_user_input") {
			allow = append(allow, "await_user_input")
		}
	}
	return NewFilteredRegistry(reg, Policy{
		Allow: allow,
		Deny:  agentCfg.Tools.Deny,
	})
}

func includesTool(tools []string, target string) bool {
	for _, name := range tools {
		if name == target {
			return true
		}
	}
	return false
}
