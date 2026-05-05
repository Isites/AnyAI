package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/agent"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/tool"
)

// AgentNotFoundError reports that a requested agent ID does not exist in the
// loaded project registry.
type AgentNotFoundError struct {
	AgentID string
}

func (e *AgentNotFoundError) Error() string {
	return fmt.Sprintf("agent %q not found", e.AgentID)
}

// ProviderUnavailableError reports that the agent's configured model provider
// is not initialized, typically due to missing credentials.
type ProviderUnavailableError struct {
	Provider string
	AgentID  string
}

func (e *ProviderUnavailableError) Error() string {
	return fmt.Sprintf("provider %q not available for agent %q", e.Provider, e.AgentID)
}

// ResolvedAgentRuntime bundles the static runtime dependencies resolved for one agent.
type ResolvedAgentRuntime struct {
	Agent     config.AgentConfig
	Provider  llm.LLMProvider
	ModelName string
}

func ResolveAgentRuntime(deps runtimeport.ExecutionDeps, requestedAgentID string) (ResolvedAgentRuntime, error) {
	if deps.Config == nil {
		return ResolvedAgentRuntime{}, fmt.Errorf("execution config is required")
	}

	agentID := strings.TrimSpace(requestedAgentID)
	if agentID == "" {
		if len(deps.Config.Agents.List) == 0 {
			return ResolvedAgentRuntime{}, fmt.Errorf("no agents are configured")
		}
		agentID = deps.Config.Agents.List[0].ID
	}

	agentCfg, ok := deps.Config.GetAgent(agentID)
	if !ok {
		return ResolvedAgentRuntime{}, &AgentNotFoundError{AgentID: agentID}
	}

	providerName, modelName := llm.ParseProviderModel(agentCfg.Model)
	provider, ok := deps.Providers[providerName]
	if !ok {
		return ResolvedAgentRuntime{}, &ProviderUnavailableError{Provider: providerName, AgentID: agentID}
	}

	return ResolvedAgentRuntime{
		Agent:     *agentCfg,
		Provider:  provider,
		ModelName: modelName,
	}, nil
}

func BuildAgentRuntimeFromDeps(
	deps runtimeport.ExecutionDeps,
	resolved ResolvedAgentRuntime,
	sess *session.Session,
	extras tools.ExtraToolDeps,
) (*agent.Runtime, error) {
	if deps.Config == nil {
		return nil, fmt.Errorf("execution config is required")
	}
	if sess == nil {
		return nil, fmt.Errorf("session is required")
	}

	agentCfg := resolved.Agent
	if extras.AssistantOutput == nil {
		extras.AssistantOutput = sessionAssistantOutputProvider{sess: sess}
	}

	var agentSkills *skill.Loader
	if deps.Resources != nil {
		agentSkills = deps.Resources.LoaderForAgent(agentCfg.ID)
	}
	if agentSkills == nil {
		loaded, err := skill.LoaderForAgent(deps.Config, &agentCfg)
		if err == nil {
			agentSkills = loaded
		}
	}
	if agentSkills == nil {
		agentSkills = deps.Skills
	}
	if extras.SkillProvider == nil && agentSkills != nil && len(agentSkills.Skills()) > 0 {
		extras.SkillProvider = tools.SkillProviderAdapter{Loader: agentSkills}
	}
	goalRuntime := task.NewGoalRuntime(sess, deps.TaskRuntime, agentCfg.MaxTurns)
	if extras.GoalManager == nil {
		extras.GoalManager = goalManagerAdapter{runtime: goalRuntime}
	}

	executor := tools.ExecutorForAgentWithExtras(
		deps.Config,
		&agentCfg,
		deps.Sender,
		deps.AgentRunner,
		deps.JobScheduler,
		extras,
	)

	rt := &agent.Runtime{
		LLM:                resolved.Provider,
		Tools:              executor,
		Session:            sess,
		AgentID:            agentCfg.ID,
		AgentName:          agentCfg.Name,
		Model:              resolved.ModelName,
		Workspace:          agentCfg.Workspace,
		ProjectRoot:        runtimeProjectRoot(deps.Config),
		AgentDefinitionDir: runtimeAgentDefinitionDir(deps.Config, agentCfg),
		AgentsRootDir:      runtimeAgentsRootDir(deps.Config),
		SharedSkillsDir:    strings.TrimSpace(deps.Config.SharedSkillsDir),
		MaxTurns:           agentCfg.MaxTurns,
		SystemPrompt:       agentCfg.SystemPrompt,
		ConfigPath:         runtimeConfigPath(deps.Config),
		DataDir:            deps.Config.RuntimeDataDir(),
		MemoryDir:          runtimeMemoryDir(deps.Config),
		MemoryMaxItems:     deps.Config.Memory.Inject.MaxItems,
		TopologySummary:    agent.ConfigSummary(deps.Config),
		ToolRecovery: agent.ToolRecoveryConfig{
			MaxAttempts:    deps.Config.Runtime.Tools.MaxAttempts,
			RetryBackoffMS: deps.Config.Runtime.Tools.RetryBackoffMS,
			LoopDetection: agent.ToolLoopDetectionConfig{
				Enabled:          deps.Config.Runtime.Tools.LoopDetection.EnabledValue(),
				HistorySize:      deps.Config.Runtime.Tools.LoopDetection.HistorySize,
				WarningThreshold: deps.Config.Runtime.Tools.LoopDetection.WarningThreshold,
				BlockThreshold:   deps.Config.Runtime.Tools.LoopDetection.BlockThreshold,
			},
		},
		TranscriptHygiene: agent.TranscriptHygieneConfig{
			Enabled:                   deps.Config.Runtime.Sessions.TranscriptHygiene.EnabledValue(),
			MergeConsecutiveUserTurns: deps.Config.Runtime.Sessions.TranscriptHygiene.MergeConsecutiveUserTurnsValue(),
			RepairToolPairs:           deps.Config.Runtime.Sessions.TranscriptHygiene.RepairToolPairsValue(),
			DropOrphanToolResults:     deps.Config.Runtime.Sessions.TranscriptHygiene.DropOrphanToolResultsValue(),
			TreatMetaAsSummaryContext: deps.Config.Runtime.Sessions.TranscriptHygiene.TreatMetaAsSummaryContextValue(),
		},
		Compaction: agent.CompactionConfig{
			Enabled:              deps.Config.Runtime.Sessions.Compaction.EnabledValue(),
			TriggerMode:          deps.Config.Runtime.Sessions.Compaction.TriggerMode,
			EntryThreshold:       deps.Config.Runtime.Sessions.Compaction.EntryThreshold,
			TokenThreshold:       deps.Config.Runtime.Sessions.Compaction.TokenThreshold,
			KeepRecentUserTurns:  deps.Config.Runtime.Sessions.Compaction.KeepRecentUserTurns,
			KeepRecentUserTokens: deps.Config.Runtime.Sessions.Compaction.KeepRecentUserTokens,
			SummaryMaxTokens:     deps.Config.Runtime.Sessions.Compaction.SummaryMaxTokens,
		},
		ToolPreflight: agent.ToolPreflightConfig{
			Enabled: deps.Config.Runtime.Tools.Preflight.EnabledValue(),
		},
		IncompleteTurn: agent.IncompleteTurnConfig{
			Enabled: deps.Config.Runtime.Sessions.IncompleteTurn.EnabledValue(),
		},
		Skills:        agentSkills,
		Memory:        deps.Memory,
		EventAppender: eventAppenderFromRecorder(deps.Recorder),
		TaskRuntime:   deps.TaskRuntime,
		GoalRuntime:   goalRuntime,
	}
	if deps.RuntimeConfigurer != nil {
		if err := deps.RuntimeConfigurer.ConfigureAgentRuntime(rt); err != nil {
			return nil, err
		}
	}
	return rt, nil
}

func eventAppenderFromRecorder(recorder runtimeport.EventAppender) func(runtimeevents.EventRecord) {
	if recorder == nil {
		return nil
	}
	return recorder.AppendEvent
}

func runtimeAgentDefinitionDir(cfg *config.Config, agentCfg config.AgentConfig) string {
	candidates := []string{
		strings.TrimSpace(agentCfg.Workspace),
	}
	if root := runtimeProjectRoot(cfg); root != "" {
		candidates = append(candidates,
			filepath.Join(root, "agents", strings.TrimSpace(agentCfg.ID)),
			root,
		)
	}
	for _, dir := range candidates {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, "agent.md")); err == nil && !info.IsDir() {
			return dir
		}
	}
	return ""
}

func runtimeAgentsRootDir(cfg *config.Config) string {
	root := runtimeProjectRoot(cfg)
	if root == "" {
		return ""
	}
	dir := filepath.Join(root, "agents")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return dir
}

type sessionAssistantOutputProvider struct {
	sess *session.Session
}

type goalManagerAdapter struct {
	runtime *task.GoalRuntime
}

func (a goalManagerAdapter) CompleteGoal(ctx context.Context, goalID string) error {
	if a.runtime == nil {
		return fmt.Errorf("goal runtime is not available")
	}
	meta := tools.RuntimeContextFrom(ctx)
	return a.runtime.CompleteGoalIgnoringTask(
		task.GoalID(strings.TrimSpace(goalID)),
		strings.TrimSpace(meta.TaskID),
		strings.TrimSpace(meta.ToolCallID),
	)
}

func (a goalManagerAdapter) AwaitUserInput(_ context.Context, goalID, message string) error {
	if a.runtime == nil {
		return fmt.Errorf("goal runtime is not available")
	}
	return a.runtime.AwaitUserInput(task.GoalID(strings.TrimSpace(goalID)), strings.TrimSpace(message))
}

func (p sessionAssistantOutputProvider) LatestAssistantMessageText() (string, bool) {
	if p.sess == nil {
		return "", false
	}
	history := p.sess.History()
	for i := len(history) - 1; i >= 0; i-- {
		entry := history[i]
		if entry.Type != session.EntryTypeMessage || entry.Role != "assistant" {
			continue
		}
		var data session.MessageData
		if err := json.Unmarshal(entry.Data, &data); err != nil {
			return "", false
		}
		if strings.TrimSpace(data.Text) == "" {
			continue
		}
		return data.Text, true
	}
	return "", false
}

func runtimeConfigPath(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if path := strings.TrimSpace(cfg.Path()); path != "" {
		return path
	}
	if dir := strings.TrimSpace(cfg.ProjectConfigDir); dir != "" {
		return filepath.Join(dir, "anyai.yaml")
	}
	return ""
}

func runtimeProjectRoot(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if dir := strings.TrimSpace(cfg.ProjectRoot); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(cfg.ProjectConfigDir); dir != "" {
		return dir
	}
	if dataDir := strings.TrimSpace(cfg.RuntimeDataDir()); dataDir != "" {
		return filepath.Dir(dataDir)
	}
	return ""
}

func runtimeMemoryDir(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if dir := strings.TrimSpace(cfg.Memory.Dir); dir != "" {
		return dir
	}
	if dataDir := strings.TrimSpace(cfg.RuntimeDataDir()); dataDir != "" {
		return filepath.Join(dataDir, "memory")
	}
	return ""
}
