package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
)

const defaultIdentityBase = `You are AnyAI, a capable general-purpose AI assistant. Think carefully and independently about what the user is trying to achieve, break the work into concrete steps, and keep pushing until you reach a useful conclusion. Use your built-in capabilities proactively when they can help you inspect the situation, gather evidence, take action, and verify the result. Be concise, but not at the expense of correctness or completeness. Avoid shallow answers, unnecessary deferral, or partial work when you can make more progress yourself. Do not pretend to know things you have not actually established. When something is uncertain, say so plainly, ground your answer in what you do know, and continue advancing the parts you can verify. Do not cut corners or deliberately do less than the task requires.`

const runtimeCapabilityNote = `The AnyAI runtime exposes real working tools. Treat them as real working capabilities, not hypothetical suggestions. Use them directly to inspect context, gather evidence, take action, and verify results instead of asking the user to format tool calls for you. Prefer doing the work over merely describing it. If the runtime can verify something, do not bluff; use it or clearly state the remaining uncertainty.

When a tool call fails, read the exact error, adjust your next step, and retry with corrected arguments or a reasonable fallback. Do not pretend a failed tool succeeded or repeat the same broken call unchanged.

For filesystem and shell tools, relative paths resolve from your agent workspace, which may differ from the project root, so use the injected environment paths and prefer absolute paths when anything is ambiguous. When bash is available, combine shell tools freely and, for more complex or repetitive work, run a short helper program, including a Python script, when policy allows.`

const (
	promptQueryMaxSnippets   = 4
	promptQuerySnippetMaxLen = 220
)

// toolHints maps tool names to usage guidance injected into the runtime
// capability section.
var toolHints = map[string]string{
	"read_file":        "You can read files. It reads file contents, not directory listings. Use bash to inspect directories when bash is available. If an image is already attached, inspect that image directly with vision instead of calling read_file or scripts; use read_file for text/file contents and path verification.",
	"write_file":       "You can create, overwrite, append to, or patch files. For existing-file edits, prefer mode=patch with Codex-style *** Begin Patch / *** End Patch hunks. For large new files, write manageable chunks with mode=overwrite for the first chunk, mode=append for later chunks, and expected_offset to verify no chunk was missed.",
	"edit_file":        "You can make targeted edits to existing files.",
	"bash":             "You can execute bash commands on the user's machine from your agent workspace. Use it to inspect directories, verify file existence, and switch to absolute project paths when relative paths are ambiguous. For more complex workflows, you can write and run a short script through bash, including using installed interpreters such as python3 when available.",
	"web_fetch":        "You can fetch web pages using the web_fetch tool.",
	"web_search":       "You can search the web using the web_search tool.",
	"browser":          "You can automate a headless browser for interactive pages using the browser tool.",
	"skill_get":        "You can load the full instructions for an available skill by name using skill_get after a relevant skill summary appears.",
	"send_message":     "You can send messages to other users or channels using the send_message tool.",
	"cron":             "You can schedule recurring tasks using the cron tool.",
	"callagent":        "Use callagent when the request clearly needs a specialist or multiple specialist viewpoints. Every call must include target_agent. For parallel work, every tasks[] item must include target_agent and task. If collaboration is required, your first assistant action should be the callagent tool call, not a direct prose answer. Preferred shapes: single with target_agent plus task and no mode field, or parallel with mode=parallel plus tasks[]. Never send mode=sequential. After child results return, continue and write the final integrated user-facing answer yourself.",
	"memory_save":      "You can persist durable, cross-session facts, confirmed constraints, and important decisions into memory using the memory_save tool.",
	"goal_complete":    "You can call goal_complete to explicitly record that a runtime-managed goal is complete. The runtime still validates objective facts and rejects completion if unfinished work remains.",
	"await_user_input": "When you cannot continue without a user answer, call await_user_input to mark the goal as blocked. After that, ask the user exactly what you need and stop self-driving until they reply.",
}

type AgentSurface struct {
	ID           string
	Name         string
	Instructions string
}

type ExecutionSurface struct {
	Model string
}

type ToolSurface struct {
	Names []string
}

type EnvironmentSurface struct {
	ConfigPath         string
	ProjectRoot        string
	Workspace          string
	DataDir            string
	MemoryDir          string
	AgentDefinitionDir string
	AgentsRootDir      string
	SharedSkillsDir    string
}

type CollaborationSurface struct {
	TopologySummary string
	RunMode         string
	ParentAgentID   string
	TaskGoal        string
	Workspace       string
	ExpectedOutputs []string
	InputArtifacts  []string
	ReturnMode      string
}

type RetrievalSurface struct {
	CurrentQuery          string
	CurrentRequest        string
	PendingContextSummary string
	SessionSummary        string
	MatchedSkills         []skill.Skill
	MemoryEntries         []memory.Entry
}

type GoalSurface struct {
	ID                   string
	Description          string
	State                string
	ToolCalls            int
	TurnCount            int
	ShouldContinue       bool
	HardStop             bool
	ContinueReason       string
	PendingTaskIDs       []string
	PendingToolCalls     []string
	OpenTodos            []session.TodoItem
	CurrentPlan          *runtimeplan.Plan
	ReadyPlanStep        *runtimeplan.Step
	BlockedPlanSteps     []string
	Checkpoints          []task.Checkpoint
	AwaitingInputMessage string
}

// PromptContext is the normalized runtime state used to assemble the system
// prompt as a small set of stable blocks.
type PromptContext struct {
	Agent         AgentSurface
	Execution     ExecutionSurface
	Tools         ToolSurface
	Environment   EnvironmentSurface
	Collaboration CollaborationSurface
	Retrieval     RetrievalSurface
	Goal          GoalSurface
}

func (c PromptContext) normalized() PromptContext {
	c.Agent.ID = strings.TrimSpace(c.Agent.ID)
	c.Agent.Name = strings.TrimSpace(c.Agent.Name)
	c.Agent.Instructions = strings.TrimSpace(c.Agent.Instructions)
	c.Execution.Model = strings.TrimSpace(c.Execution.Model)
	c.Tools.Names = normalizeToolNames(c.Tools.Names)
	c.Environment.ConfigPath = strings.TrimSpace(c.Environment.ConfigPath)
	c.Environment.ProjectRoot = strings.TrimSpace(c.Environment.ProjectRoot)
	c.Environment.Workspace = strings.TrimSpace(c.Environment.Workspace)
	c.Environment.DataDir = strings.TrimSpace(c.Environment.DataDir)
	c.Environment.MemoryDir = strings.TrimSpace(c.Environment.MemoryDir)
	c.Environment.AgentDefinitionDir = strings.TrimSpace(c.Environment.AgentDefinitionDir)
	c.Environment.AgentsRootDir = strings.TrimSpace(c.Environment.AgentsRootDir)
	c.Environment.SharedSkillsDir = strings.TrimSpace(c.Environment.SharedSkillsDir)
	c.Collaboration.TopologySummary = strings.TrimSpace(c.Collaboration.TopologySummary)
	c.Collaboration.RunMode = strings.TrimSpace(c.Collaboration.RunMode)
	c.Collaboration.ParentAgentID = strings.TrimSpace(c.Collaboration.ParentAgentID)
	c.Collaboration.TaskGoal = strings.TrimSpace(c.Collaboration.TaskGoal)
	c.Collaboration.Workspace = strings.TrimSpace(c.Collaboration.Workspace)
	c.Collaboration.ExpectedOutputs = normalizePromptPaths(c.Collaboration.ExpectedOutputs)
	c.Collaboration.InputArtifacts = normalizePromptPaths(c.Collaboration.InputArtifacts)
	c.Collaboration.ReturnMode = strings.TrimSpace(c.Collaboration.ReturnMode)
	c.Retrieval.CurrentQuery = strings.TrimSpace(c.Retrieval.CurrentQuery)
	c.Retrieval.CurrentRequest = strings.TrimSpace(c.Retrieval.CurrentRequest)
	c.Retrieval.PendingContextSummary = strings.TrimSpace(c.Retrieval.PendingContextSummary)
	c.Retrieval.SessionSummary = strings.TrimSpace(c.Retrieval.SessionSummary)
	c.Goal.ID = strings.TrimSpace(c.Goal.ID)
	c.Goal.Description = strings.TrimSpace(c.Goal.Description)
	c.Goal.State = strings.TrimSpace(c.Goal.State)
	c.Goal.ContinueReason = strings.TrimSpace(c.Goal.ContinueReason)
	c.Goal.AwaitingInputMessage = strings.TrimSpace(c.Goal.AwaitingInputMessage)
	c.Goal.PendingTaskIDs = normalizePromptStrings(c.Goal.PendingTaskIDs)
	c.Goal.PendingToolCalls = normalizePromptStrings(c.Goal.PendingToolCalls)
	c.Goal.BlockedPlanSteps = normalizePromptStrings(c.Goal.BlockedPlanSteps)
	if c.Goal.CurrentPlan != nil {
		copy := runtimeplan.Normalize(*c.Goal.CurrentPlan)
		copy.Steps = append([]runtimeplan.Step(nil), copy.Steps...)
		c.Goal.CurrentPlan = &copy
	}
	if c.Goal.ReadyPlanStep != nil {
		copy := *c.Goal.ReadyPlanStep
		copy.Description = strings.TrimSpace(copy.Description)
		copy.ID = strings.TrimSpace(copy.ID)
		copy.Dependencies = normalizePromptStrings(copy.Dependencies)
		c.Goal.ReadyPlanStep = &copy
	}
	if len(c.Goal.Checkpoints) > 0 {
		items := make([]task.Checkpoint, 0, len(c.Goal.Checkpoints))
		for _, item := range c.Goal.Checkpoints {
			item.ID = strings.TrimSpace(item.ID)
			item.Description = strings.TrimSpace(item.Description)
			item.Evidence = strings.TrimSpace(item.Evidence)
			if item.ID == "" && item.Description == "" {
				continue
			}
			items = append(items, item)
		}
		c.Goal.Checkpoints = items
	}
	if len(c.Goal.OpenTodos) > 0 {
		items := make([]session.TodoItem, 0, len(c.Goal.OpenTodos))
		for _, item := range c.Goal.OpenTodos {
			item.ID = strings.TrimSpace(item.ID)
			item.Content = strings.TrimSpace(item.Content)
			item.Status = strings.TrimSpace(item.Status)
			if item.ID == "" && item.Content == "" {
				continue
			}
			items = append(items, item)
		}
		c.Goal.OpenTodos = items
	}
	return c
}

func (c PromptContext) hasTool(name string) bool {
	for _, toolName := range c.Tools.Names {
		if toolName == name {
			return true
		}
	}
	return false
}

func (c PromptContext) hasAnyTool(names ...string) bool {
	for _, name := range names {
		if c.hasTool(name) {
			return true
		}
	}
	return false
}

func (c PromptContext) effectiveConfigPath() string {
	if c.Environment.ConfigPath != "" {
		return c.Environment.ConfigPath
	}
	return config.DefaultConfigPath()
}

func (c PromptContext) effectiveProjectRoot() string {
	if c.Environment.ProjectRoot != "" {
		return c.Environment.ProjectRoot
	}
	if c.Environment.DataDir != "" {
		if filepath.Base(c.Environment.DataDir) == "anyai" {
			return filepath.Dir(c.Environment.DataDir)
		}
		return c.Environment.DataDir
	}
	if c.Environment.ConfigPath != "" {
		return filepath.Dir(c.Environment.ConfigPath)
	}
	return ""
}

func (c PromptContext) effectiveWorkspace() string {
	if c.Environment.Workspace != "" {
		return c.Environment.Workspace
	}
	return c.effectiveProjectRoot()
}

func (c PromptContext) effectiveDataDir() string {
	if c.Environment.DataDir != "" {
		return c.Environment.DataDir
	}
	return config.DefaultDataDir()
}

func (c PromptContext) effectiveSessionsDir() string {
	dataDir := c.effectiveDataDir()
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, "sessions")
}

func (c PromptContext) effectiveMemoryDir() string {
	if c.Environment.MemoryDir != "" {
		return c.Environment.MemoryDir
	}
	dataDir := c.effectiveDataDir()
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, "memory")
}

func (c PromptContext) effectiveAgentDefinitionDir() string {
	if c.Environment.AgentDefinitionDir != "" {
		return c.Environment.AgentDefinitionDir
	}
	return ""
}

func (c PromptContext) effectiveAgentsRootDir() string {
	if c.Environment.AgentsRootDir != "" {
		return c.Environment.AgentsRootDir
	}
	projectRoot := c.effectiveProjectRoot()
	if projectRoot == "" {
		return ""
	}
	return filepath.Join(projectRoot, "agents")
}

func (c PromptContext) effectiveSharedSkillsDir() string {
	if c.Environment.SharedSkillsDir != "" {
		return c.Environment.SharedSkillsDir
	}
	projectRoot := c.effectiveProjectRoot()
	if projectRoot == "" {
		return ""
	}
	return filepath.Join(projectRoot, "common", "skills")
}

func (c PromptContext) effectiveTopologySummary() string {
	return c.Collaboration.TopologySummary
}

type promptSectionStability int

const (
	promptSectionStatic promptSectionStability = iota
	promptSectionDynamic
)

type promptSection interface {
	key() string
	stability() promptSectionStability
	priority() int
	applicable(PromptContext) bool
	build(PromptContext) string
}

type promptPlanner struct {
	sections []promptSection
}

func newPromptPlanner() promptPlanner {
	return promptPlanner{
		sections: []promptSection{
			promptSelfIdentitySection{},
			promptAgentInstructionsSection{},
			promptDefaultIdentitySection{},
			promptContractSection{},
			promptCapabilitySection{},
			promptToolCallStyleSection{},
			promptEnvironmentSection{},
			promptTopologySection{},
			promptGoalRulesSection{},
			promptRuntimeFactsSection{},
			promptGoalStateSection{},
			promptRequestFocusSection{},
			promptSkillsSection{},
			promptMemorySection{},
		},
	}
}

func (p promptPlanner) Build(ctx PromptContext) string {
	return p.build(ctx, func(promptSection) bool { return true })
}

func (p promptPlanner) BuildStatic(ctx PromptContext) string {
	return p.build(ctx, func(section promptSection) bool {
		return section.stability() == promptSectionStatic
	})
}

func (p promptPlanner) BuildDynamic(ctx PromptContext) string {
	return p.build(ctx, func(section promptSection) bool {
		return section.stability() == promptSectionDynamic
	})
}

func (p promptPlanner) Compose(staticPrompt, dynamicPrompt string) string {
	staticPrompt = strings.TrimSpace(staticPrompt)
	dynamicPrompt = strings.TrimSpace(dynamicPrompt)
	switch {
	case staticPrompt == "":
		return dynamicPrompt
	case dynamicPrompt == "":
		return staticPrompt
	default:
		return staticPrompt + "\n\n" + dynamicPrompt
	}
}

func (p promptPlanner) build(ctx PromptContext, include func(promptSection) bool) string {
	ctx = ctx.normalized()
	sections := append([]promptSection(nil), p.sections...)
	sort.SliceStable(sections, func(i, j int) bool {
		return sections[i].priority() < sections[j].priority()
	})

	var parts []string
	seen := make(map[string]struct{}, len(sections))
	for _, section := range sections {
		if !include(section) || !section.applicable(ctx) {
			continue
		}
		if _, ok := seen[section.key()]; ok {
			continue
		}
		part := strings.TrimSpace(section.build(ctx))
		if part == "" {
			continue
		}
		seen[section.key()] = struct{}{}
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n\n")
}

// buildDefaultIdentity constructs the fallback identity prompt used only when
// the agent does not define custom instructions in agent.md/config.
func buildDefaultIdentity() string {
	return defaultIdentityBase
}

func buildContractSection() string {
	return "## Runtime Contract\n\n" + runtimeCapabilityNote
}

func buildCapabilitySection(toolNames []string) string {
	normalized := normalizeToolNames(toolNames)
	if len(normalized) == 0 {
		return "## Runtime Capabilities\n\nNo extra tools are available in this run."
	}

	var hints []string
	for _, name := range normalized {
		if hint := strings.TrimSpace(toolHints[name]); hint != "" {
			hints = append(hints, fmt.Sprintf("- `%s`: %s", name, hint))
			continue
		}
		hints = append(hints, fmt.Sprintf("- `%s`: available in this run.", name))
	}
	return "## Runtime Capabilities\n\nAvailable tools in this run:\n" + strings.Join(hints, "\n")
}

func buildToolCallStyleSection() string {
	return strings.Join([]string{
		"## Tool Call Style",
		"",
		"- When a tool is the obvious next step, call it directly instead of asking the user to format the tool call for you.",
		"- Keep narration brief. Add a short preamble only when the work is multi-step, risky, slow, or the user explicitly asked for extra visibility.",
		"- Prefer first-class tools over describing equivalent manual steps for the user.",
		"- Tool results are callback-delivered. If a tool result says it failed, that call has already settled; inspect the returned error and continue from it.",
		"- After a tool failure, read the exact error and change arguments, paths, or method before retrying.",
		"- If the same tool and input are not making progress, switch approach instead of looping on the same call.",
	}, "\n")
}

// assembleSystemPrompt renders the prompt from a structured context.
func assembleSystemPrompt(ctx PromptContext) string {
	return newPromptPlanner().Build(ctx)
}

type promptSelfIdentitySection struct{}

func (promptSelfIdentitySection) key() string                       { return "self_identity" }
func (promptSelfIdentitySection) stability() promptSectionStability { return promptSectionStatic }
func (promptSelfIdentitySection) priority() int                     { return 20 }
func (promptSelfIdentitySection) applicable(ctx PromptContext) bool {
	return ctx.Agent.ID != "" || ctx.Agent.Name != ""
}
func (promptSelfIdentitySection) build(ctx PromptContext) string {
	var lines []string
	lines = append(lines, "## Runtime Identity")
	if ctx.Agent.Name != "" {
		lines = append(lines, "- Agent name: "+ctx.Agent.Name)
	}
	if ctx.Agent.ID != "" {
		lines = append(lines, "- Agent id: "+ctx.Agent.ID)
	}
	return strings.Join(lines, "\n")
}

type promptAgentInstructionsSection struct{}

func (promptAgentInstructionsSection) key() string                       { return "agent_instructions" }
func (promptAgentInstructionsSection) stability() promptSectionStability { return promptSectionStatic }
func (promptAgentInstructionsSection) priority() int                     { return 10 }
func (promptAgentInstructionsSection) applicable(ctx PromptContext) bool {
	return strings.TrimSpace(ctx.Agent.Instructions) != ""
}
func (promptAgentInstructionsSection) build(ctx PromptContext) string {
	return ctx.Agent.Instructions
}

type promptDefaultIdentitySection struct{}

func (promptDefaultIdentitySection) key() string                       { return "default_identity" }
func (promptDefaultIdentitySection) stability() promptSectionStability { return promptSectionStatic }
func (promptDefaultIdentitySection) priority() int                     { return 10 }
func (promptDefaultIdentitySection) applicable(ctx PromptContext) bool {
	return strings.TrimSpace(ctx.Agent.Instructions) == ""
}
func (promptDefaultIdentitySection) build(ctx PromptContext) string {
	return buildDefaultIdentity()
}

type promptContractSection struct{}

func (promptContractSection) key() string                       { return "runtime_contract" }
func (promptContractSection) stability() promptSectionStability { return promptSectionStatic }
func (promptContractSection) priority() int                     { return 25 }
func (promptContractSection) applicable(PromptContext) bool     { return true }
func (promptContractSection) build(PromptContext) string {
	return buildContractSection()
}

type promptCapabilitySection struct{}

func (promptCapabilitySection) key() string                       { return "capabilities" }
func (promptCapabilitySection) stability() promptSectionStability { return promptSectionStatic }
func (promptCapabilitySection) priority() int                     { return 30 }
func (promptCapabilitySection) applicable(PromptContext) bool     { return true }
func (promptCapabilitySection) build(ctx PromptContext) string {
	return buildCapabilitySection(ctx.Tools.Names)
}

type promptToolCallStyleSection struct{}

func (promptToolCallStyleSection) key() string                       { return "tool_call_style" }
func (promptToolCallStyleSection) stability() promptSectionStability { return promptSectionStatic }
func (promptToolCallStyleSection) priority() int                     { return 35 }
func (promptToolCallStyleSection) applicable(ctx PromptContext) bool {
	return len(ctx.Tools.Names) > 0
}
func (promptToolCallStyleSection) build(PromptContext) string {
	return buildToolCallStyleSection()
}

type promptEnvironmentSection struct{}

func (promptEnvironmentSection) key() string                       { return "environment" }
func (promptEnvironmentSection) stability() promptSectionStability { return promptSectionStatic }
func (promptEnvironmentSection) priority() int                     { return 40 }
func (promptEnvironmentSection) applicable(ctx PromptContext) bool {
	return ctx.hasAnyTool("read_file", "write_file", "edit_file", "bash", "python")
}
func (promptEnvironmentSection) build(ctx PromptContext) string {
	lines := []string{"## Environment Facts", ""}
	if projectRoot := ctx.effectiveProjectRoot(); projectRoot != "" {
		lines = append(lines, "- Project root: "+projectRoot)
	}
	if workspace := ctx.effectiveWorkspace(); workspace != "" {
		lines = append(lines, "- Agent workspace: "+workspace)
		lines = append(lines, "- Relative path base for file/bash/python tools: "+workspace)
	}
	if configPath := ctx.effectiveConfigPath(); configPath != "" {
		lines = append(lines, "- Config file: "+configPath)
	}
	if dataDir := ctx.effectiveDataDir(); dataDir != "" {
		lines = append(lines, "- Runtime data directory: "+dataDir)
	}
	if sessionsDir := ctx.effectiveSessionsDir(); sessionsDir != "" {
		lines = append(lines, "- Sessions directory: "+sessionsDir)
	}
	if memoryDir := ctx.effectiveMemoryDir(); memoryDir != "" {
		lines = append(lines, "- Memory directory: "+memoryDir)
	}
	if agentDir := ctx.effectiveAgentDefinitionDir(); agentDir != "" {
		lines = append(lines, "- Agent definition directory: "+agentDir)
	}
	if agentsDir := ctx.effectiveAgentsRootDir(); agentsDir != "" {
		lines = append(lines, "- Agent definitions root: "+agentsDir)
	}
	if sharedSkillsDir := ctx.effectiveSharedSkillsDir(); sharedSkillsDir != "" {
		lines = append(lines, "- Shared skills directory: "+sharedSkillsDir)
	}
	return strings.Join(lines, "\n")
}

type promptTopologySection struct{}

func (promptTopologySection) key() string                       { return "topology" }
func (promptTopologySection) stability() promptSectionStability { return promptSectionStatic }
func (promptTopologySection) priority() int                     { return 50 }
func (promptTopologySection) applicable(ctx PromptContext) bool {
	hasContract := ctx.Collaboration.RunMode != "" ||
		ctx.Collaboration.ParentAgentID != "" ||
		ctx.Collaboration.TaskGoal != "" ||
		ctx.Collaboration.Workspace != "" ||
		len(ctx.Collaboration.ExpectedOutputs) > 0 ||
		len(ctx.Collaboration.InputArtifacts) > 0 ||
		ctx.Collaboration.ReturnMode != ""
	return hasContract || (ctx.hasTool("callagent") && strings.TrimSpace(ctx.effectiveTopologySummary()) != "")
}
func (promptTopologySection) build(ctx PromptContext) string {
	lines := []string{"## Collaboration Contract", ""}
	switch ctx.Collaboration.RunMode {
	case "agent_call":
		lines = append(lines, "- Execution mode: child agent call")
	case "root":
		lines = append(lines, "- Execution mode: entry/root agent")
	}
	if ctx.Collaboration.ParentAgentID != "" {
		lines = append(lines, "- Parent agent: "+ctx.Collaboration.ParentAgentID)
	}
	if ctx.Collaboration.TaskGoal != "" {
		lines = append(lines, "- Task goal: "+ctx.Collaboration.TaskGoal)
	}
	if ctx.Collaboration.Workspace != "" {
		lines = append(lines, "- Task workspace hint: "+ctx.Collaboration.Workspace)
	}
	if len(ctx.Collaboration.InputArtifacts) > 0 {
		lines = append(lines, "- Input artifacts:")
		for _, item := range ctx.Collaboration.InputArtifacts {
			lines = append(lines, "  - "+item)
		}
	}
	if len(ctx.Collaboration.ExpectedOutputs) > 0 {
		lines = append(lines, "- Expected outputs:")
		for _, item := range ctx.Collaboration.ExpectedOutputs {
			lines = append(lines, "  - "+item)
		}
	}
	if ctx.Collaboration.ReturnMode != "" {
		lines = append(lines, "- Return mode: "+ctx.Collaboration.ReturnMode)
	}
	if ctx.Collaboration.RunMode == "agent_call" {
		lines = append(lines,
			"- Stay narrowly scoped to the delegated task and the explicitly named workspace artifacts.",
			"- If the delegated task names a concrete file path, your first action should be reading that exact file before drawing conclusions.",
			"- Once you have enough evidence, return one concise grounded result to the parent instead of inventing extra files, scripts, directories, or follow-up work.",
		)
	}
	if topology := strings.TrimSpace(ctx.effectiveTopologySummary()); topology != "" {
		lines = append(lines, "", topology)
	}
	if ctx.hasTool("callagent") {
		lines = append(lines,
			"",
			"- If the current request clearly maps to one or more configured specialist agents, you must use callagent instead of inventing their work yourself.",
			"- If collaboration is required, your first assistant action should be the callagent tool call, not a direct prose answer.",
			"- Every callagent object must include target_agent. Never emit a call with only task.",
			"- Preferred shapes: single uses target_agent plus task and no mode field; parallel uses mode=parallel plus tasks[].",
			"- Never send mode=sequential. Single calls omit mode entirely.",
			"- For parallel work, every tasks[] item must include both target_agent and task.",
			"- If multiple listed specialists can work independently, prefer one parallel callagent invocation and then integrate the results yourself.",
			"- When the user asks to rerun a specialist pass, call the specialists again instead of pretending old results are enough.",
		)
	}
	return strings.Join(lines, "\n")
}

type promptSkillsSection struct{}

type promptGoalRulesSection struct{}

func (promptGoalRulesSection) key() string                       { return "goal_rules" }
func (promptGoalRulesSection) stability() promptSectionStability { return promptSectionDynamic }
func (promptGoalRulesSection) priority() int                     { return 51 }
func (promptGoalRulesSection) applicable(ctx PromptContext) bool {
	return ctx.Goal.ID != "" || ctx.Goal.Description != ""
}
func (promptGoalRulesSection) build(ctx PromptContext) string {
	lines := []string{
		"## Goal Completion Rules",
		"",
		"- This run is working on a runtime-managed goal. Ending is validated against objective runtime facts, not only your subjective judgment.",
		"- Hard runtime constraints win over your subjective judgment. If runtime says the goal is blocked, incomplete, or stopped, obey that state.",
		"- If runtime reports pending tasks, unresolved tool calls, incomplete checkpoints, open todo items, or unfinished plan steps, continue the work instead of ending.",
		"- If a todo item is already done, update it before trying to finish.",
	}
	if ctx.hasTool("goal_complete") {
		lines = append(lines,
			"- If you want to explicitly record completion, you may call `goal_complete` after objective runtime work is actually finished.",
			"- Never use `goal_complete` to bypass pending tasks, unresolved tool calls, incomplete checkpoints, open todo items, or unfinished plan steps.",
		)
	} else {
		lines = append(lines, "- This run does not expose `goal_complete`, so runtime completion is decided entirely from objective runtime facts.")
	}
	if ctx.hasTool("await_user_input") {
		lines = append(lines,
			"- When you cannot continue without a user answer, call `await_user_input` with the missing information you need.",
			"- After calling `await_user_input`, ask the user that question clearly and stop instead of continuing to self-drive.",
		)
	}
	return strings.Join(lines, "\n")
}

type promptRuntimeFactsSection struct{}

func (promptRuntimeFactsSection) key() string                       { return "runtime_facts" }
func (promptRuntimeFactsSection) stability() promptSectionStability { return promptSectionStatic }
func (promptRuntimeFactsSection) priority() int                     { return 52 }
func (promptRuntimeFactsSection) applicable(PromptContext) bool     { return true }
func (promptRuntimeFactsSection) build(ctx PromptContext) string {
	lines := []string{"## Runtime Facts", ""}
	if ctx.Execution.Model != "" {
		lines = append(lines, "- Current model: "+ctx.Execution.Model)
	}
	lines = append(lines,
		"- This prompt is assembled by the AnyAI runtime for the current run.",
		"- The actual tool set and injected environment facts are determined by the runtime for this run, not by agent.md alone.",
	)
	if ctx.Collaboration.RunMode != "" {
		lines = append(lines, "- Current execution mode: "+ctx.Collaboration.RunMode)
	}
	return strings.Join(lines, "\n")
}

type promptGoalStateSection struct{}

func (promptGoalStateSection) key() string                       { return "goal_state" }
func (promptGoalStateSection) stability() promptSectionStability { return promptSectionDynamic }
func (promptGoalStateSection) priority() int                     { return 54 }
func (promptGoalStateSection) applicable(ctx PromptContext) bool {
	return ctx.Goal.ID != "" || ctx.Goal.Description != ""
}
func (promptGoalStateSection) build(ctx PromptContext) string {
	lines := []string{"## Goal State", ""}
	if ctx.Goal.Description != "" {
		lines = append(lines, "- Goal: "+ctx.Goal.Description)
	}
	if ctx.Goal.ID != "" {
		lines = append(lines, "- Goal id: "+ctx.Goal.ID)
	}
	if ctx.Goal.State != "" {
		lines = append(lines, "- Goal state: "+ctx.Goal.State)
	}
	if ctx.Goal.TurnCount > 0 {
		lines = append(lines, fmt.Sprintf("- Runtime turn count: %d", ctx.Goal.TurnCount))
	}
	if ctx.Goal.ToolCalls > 0 {
		lines = append(lines, fmt.Sprintf("- Runtime tool calls so far: %d", ctx.Goal.ToolCalls))
	}
	switch {
	case strings.EqualFold(ctx.Goal.State, string(task.GoalStateCompleted)):
		lines = append(lines, "- Runtime assessment: goal completion has already been accepted. Provide the final answer unless new objective work appears.")
	case strings.EqualFold(ctx.Goal.State, string(task.GoalStateAwaitingInput)):
		lines = append(lines, "- Runtime assessment: the goal is blocked on user input. Ask the user for the missing information and stop.")
	case ctx.Goal.ContinueReason != "":
		lines = append(lines, "- Runtime assessment: "+ctx.Goal.ContinueReason)
	case ctx.Goal.ShouldContinue:
		lines = append(lines, "- Runtime assessment: unfinished objective work remains.")
	default:
		lines = append(lines, "- Runtime assessment: no unfinished objective runtime work is currently visible.")
	}
	if ctx.Goal.AwaitingInputMessage != "" {
		lines = append(lines, "- Missing user input: "+ctx.Goal.AwaitingInputMessage)
	}
	if len(ctx.Goal.PendingTaskIDs) > 0 {
		lines = append(lines, "- Pending runtime tasks: "+strings.Join(ctx.Goal.PendingTaskIDs, ", "))
	}
	if len(ctx.Goal.PendingToolCalls) > 0 {
		lines = append(lines, "- Pending tool calls: "+strings.Join(ctx.Goal.PendingToolCalls, ", "))
	}
	if len(ctx.Goal.Checkpoints) > 0 {
		lines = append(lines, "- Incomplete checkpoints:")
		for _, checkpoint := range ctx.Goal.Checkpoints {
			label := firstNonEmpty(checkpoint.Description, checkpoint.ID)
			if checkpoint.Evidence != "" {
				label += " evidence=" + checkpoint.Evidence
			}
			lines = append(lines, "  - "+label)
		}
	}
	if len(ctx.Goal.OpenTodos) > 0 {
		lines = append(lines, "- Open todo items:")
		for _, item := range ctx.Goal.OpenTodos {
			label := firstNonEmpty(item.Content, item.ID)
			if item.ID != "" && item.Content != "" && item.ID != item.Content {
				label = item.Content + " (" + item.ID + ")"
			}
			if status := strings.TrimSpace(item.Status); status != "" {
				label = fmt.Sprintf("%s [%s]", label, status)
			}
			lines = append(lines, "  - "+label)
		}
	}
	if ctx.Goal.ReadyPlanStep != nil {
		label := firstNonEmpty(ctx.Goal.ReadyPlanStep.Description, ctx.Goal.ReadyPlanStep.ID)
		lines = append(lines, "- Next ready plan step: "+label)
	}
	if len(ctx.Goal.BlockedPlanSteps) > 0 {
		lines = append(lines, "- Blocked plan steps: "+strings.Join(ctx.Goal.BlockedPlanSteps, ", "))
	}
	if ctx.Goal.CurrentPlan != nil {
		if rendered := strings.TrimSpace(runtimeplan.Render(*ctx.Goal.CurrentPlan)); rendered != "" {
			lines = append(lines, "", "Latest recorded plan:", rendered)
		}
	}
	return strings.Join(lines, "\n")
}

type promptRequestFocusSection struct{}

func (promptRequestFocusSection) key() string                       { return "request_focus" }
func (promptRequestFocusSection) stability() promptSectionStability { return promptSectionDynamic }
func (promptRequestFocusSection) priority() int                     { return 55 }
func (promptRequestFocusSection) applicable(ctx PromptContext) bool {
	return ctx.Retrieval.PendingContextSummary != "" ||
		ctx.Retrieval.SessionSummary != ""
}
func (promptRequestFocusSection) build(ctx PromptContext) string {
	lines := []string{"## Current Request Focus", ""}
	if ctx.Retrieval.PendingContextSummary != "" {
		lines = append(lines, "- Earlier pending user context: "+ctx.Retrieval.PendingContextSummary)
	}
	if ctx.Retrieval.SessionSummary != "" {
		lines = append(lines, "- Session summary context: "+ctx.Retrieval.SessionSummary)
	}
	return strings.Join(lines, "\n")
}

func (promptSkillsSection) key() string                       { return "skills" }
func (promptSkillsSection) stability() promptSectionStability { return promptSectionDynamic }
func (promptSkillsSection) priority() int                     { return 60 }
func (promptSkillsSection) applicable(ctx PromptContext) bool {
	return len(ctx.Retrieval.MatchedSkills) > 0
}
func (promptSkillsSection) build(ctx PromptContext) string {
	lines := []string{
		"## Relevant Skills",
		"",
		"Relevant skills matched the current task. The list below only includes each skill's name and summary.",
		"- First scan the summaries and decide whether one skill is clearly the best match.",
	}
	if ctx.hasTool("skill_get") {
		lines = append(lines,
			"- If exactly one skill is clearly relevant, call `skill_get` for that skill before relying on it.",
			"- Do not preload multiple skills just in case. If none clearly match, continue without loading one.",
		)
	}
	lines = append(lines, "- Adapt any loaded skill guidance to the tools available in this run.")
	body := strings.TrimSpace(skill.FormatForPrompt(ctx.Retrieval.MatchedSkills))
	body = strings.TrimSpace(strings.TrimPrefix(body, "## Relevant Skills"))
	if body != "" {
		lines = append(lines, "", body)
	}
	return strings.Join(lines, "\n")
}

type promptMemorySection struct{}

func (promptMemorySection) key() string                       { return "memory" }
func (promptMemorySection) stability() promptSectionStability { return promptSectionDynamic }
func (promptMemorySection) priority() int                     { return 70 }
func (promptMemorySection) applicable(ctx PromptContext) bool {
	return len(ctx.Retrieval.MemoryEntries) > 0
}
func (promptMemorySection) build(ctx PromptContext) string {
	return strings.TrimSpace(memory.FormatForPrompt(ctx.Retrieval.MemoryEntries))
}

func normalizeToolNames(toolNames []string) []string {
	seen := make(map[string]struct{}, len(toolNames))
	normalized := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return normalized
}

// ConfigSummary returns a concise topology summary for prompt injection.
func ConfigSummary(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}

	var parts []string
	if len(cfg.Agents.List) > 0 {
		lines := make([]string, 0, len(cfg.Agents.List))
		for _, item := range cfg.Agents.List {
			label := strings.TrimSpace(firstNonEmpty(item.Name, item.ID))
			id := strings.TrimSpace(item.ID)
			switch {
			case label != "" && id != "":
				line := fmt.Sprintf("- %s (id: %s)", label, id)
				if desc := strings.TrimSpace(item.Description); desc != "" {
					line += ": " + desc
				}
				lines = append(lines, line)
			case label != "":
				lines = append(lines, "- "+label)
			}
		}
		if len(lines) > 0 {
			parts = append(parts, "Configured agents:\n"+strings.Join(lines, "\n"))
		}
	}

	channels := activeChannelNames(cfg)
	if len(channels) > 0 {
		parts = append(parts, "Configured channels: "+strings.Join(channels, ", "))
	}

	return strings.Join(parts, "\n\n")
}

func activeChannelNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	if len(cfg.ActiveChannels) > 0 {
		return append([]string(nil), cfg.ActiveChannels...)
	}

	var channels []string
	if cfg.Channels.Telegram.Token != "" {
		channels = append(channels, "telegram")
	}
	if cfg.Channels.WhatsApp.DBPath != "" {
		channels = append(channels, "whatsapp")
	}
	if cfg.Channels.CLI.Enabled {
		channels = append(channels, "cli")
	}
	return channels
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizePromptPaths(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	return normalizePromptStrings(items)
}

func normalizePromptStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func goalSurfaceFromStatus(status *task.ShouldContinueResult) GoalSurface {
	if status == nil {
		return GoalSurface{}
	}
	return GoalSurface{
		ID:                   strings.TrimSpace(status.RunID),
		Description:          strings.TrimSpace(status.Description),
		State:                strings.TrimSpace(string(status.State)),
		ToolCalls:            status.ToolCalls,
		TurnCount:            status.TurnCount,
		ShouldContinue:       status.ShouldContinue,
		HardStop:             status.HardStop,
		ContinueReason:       strings.TrimSpace(status.Reason),
		PendingTaskIDs:       normalizePromptStrings(status.PendingTaskIDs),
		PendingToolCalls:     normalizePromptStrings(status.PendingToolCalls),
		OpenTodos:            append([]session.TodoItem(nil), status.OpenTodos...),
		CurrentPlan:          clonePromptPlan(status.CurrentPlan),
		ReadyPlanStep:        clonePromptPlanStep(status.ReadyPlanStep),
		BlockedPlanSteps:     normalizePromptStrings(status.BlockedPlanSteps),
		Checkpoints:          append([]task.Checkpoint(nil), status.IncompleteCheckpoints...),
		AwaitingInputMessage: strings.TrimSpace(status.AwaitingInputMessage),
	}
}

func clonePromptPlan(plan *runtimeplan.Plan) *runtimeplan.Plan {
	if plan == nil {
		return nil
	}
	copy := runtimeplan.Normalize(*plan)
	copy.Steps = append([]runtimeplan.Step(nil), copy.Steps...)
	return &copy
}

func clonePromptPlanStep(step *runtimeplan.Step) *runtimeplan.Step {
	if step == nil {
		return nil
	}
	copy := *step
	copy.Dependencies = append([]string(nil), step.Dependencies...)
	return &copy
}

func (r *Runtime) basePromptContext(toolNames []string) PromptContext {
	contract := r.AgentCallContract.Normalized()
	runMode := strings.TrimSpace(r.RunMode)
	if runMode == "" && strings.TrimSpace(r.ParentAgentID) != "" {
		runMode = "agent_call"
	}
	return PromptContext{
		Agent: AgentSurface{
			Instructions: r.SystemPrompt,
			ID:           r.AgentID,
			Name:         r.AgentName,
		},
		Execution: ExecutionSurface{
			Model: r.Model,
		},
		Tools: ToolSurface{
			Names: toolNames,
		},
		Environment: EnvironmentSurface{
			ConfigPath:         r.ConfigPath,
			ProjectRoot:        r.ProjectRoot,
			Workspace:          r.Workspace,
			DataDir:            r.DataDir,
			MemoryDir:          r.MemoryDir,
			AgentDefinitionDir: r.AgentDefinitionDir,
			AgentsRootDir:      r.AgentsRootDir,
			SharedSkillsDir:    r.SharedSkillsDir,
		},
		Collaboration: CollaborationSurface{
			TopologySummary: r.TopologySummary,
			RunMode:         runMode,
			ParentAgentID:   r.ParentAgentID,
			TaskGoal:        contract.TaskGoal,
			Workspace:       contract.Workspace,
			ExpectedOutputs: append([]string(nil), contract.ExpectedOutputs...),
			InputArtifacts:  append([]string(nil), contract.InputArtifacts...),
			ReturnMode:      contract.ReturnMode,
		},
	}
}

func (r *Runtime) composeSystemPrompt(
	ctx context.Context,
	turn int,
	userMsg string,
	history []session.SessionEntry,
	toolNames []string,
	dynamic PromptContext,
) (string, error) {
	staticCtx := r.basePromptContext(toolNames)
	if staticCtx.Collaboration.RunMode == "agent_call" && strings.TrimSpace(staticCtx.Collaboration.TaskGoal) == "" {
		staticCtx.Collaboration.TaskGoal = strings.TrimSpace(userMsg)
	}

	state := PromptBuildState{
		Turn:          turn,
		UserMessage:   userMsg,
		History:       append([]session.SessionEntry(nil), history...),
		StaticPrompt:  staticCtx,
		DynamicPrompt: dynamic,
	}
	next, err := r.applyBeforePromptBuildHooks(ctx, state)
	if err != nil {
		return "", fmt.Errorf("before_prompt_build hook failed: %w", err)
	}

	planner := newPromptPlanner()
	return planner.Compose(
		planner.BuildStatic(next.StaticPrompt),
		planner.BuildDynamic(next.DynamicPrompt),
	), nil
}

// derivePromptQuery builds a retrieval query from the latest session context so
// skill and memory injection can follow the conversation instead of being
// anchored to the initial user message for the whole run.
func derivePromptQuery(history []session.SessionEntry, fallback string) string {
	return derivePromptQueryWithFocus(history, deriveRequestFocus(history, fallback), fallback)
}

func derivePromptQueryWithFocus(history []session.SessionEntry, focus RequestFocus, fallback string) string {
	history = session.ModelVisibleEntries(history)
	type snippet struct {
		label string
		text  string
	}

	var snippets []snippet
	seen := map[string]struct{}{}
	addSnippet := func(label, text string) {
		text = promptSnippet(text, promptQuerySnippetMaxLen)
		if text == "" {
			return
		}
		key := label + "\n" + text
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		snippets = append(snippets, snippet{label: label, text: text})
	}

	addSnippet("Current request", firstNonEmpty(focus.CurrentRequest, fallback))
	if focus.PendingContextSummary != "" {
		addSnippet("Earlier pending context", focus.PendingContextSummary)
	}

	for i := len(history) - 1; i >= 0 && len(snippets) < promptQueryMaxSnippets; i-- {
		entry := history[i]
		switch entry.Type {
		case session.EntryTypeToolResult:
			var tr session.ToolResultData
			if err := json.Unmarshal(entry.Data, &tr); err != nil {
				continue
			}
			addSnippet("Tool result", firstNonEmpty(tr.Error, tr.Output))
		case session.EntryTypeMessage:
			if entry.Role != llm.MessageRoleUser {
				continue
			}
			var md session.MessageData
			if err := json.Unmarshal(entry.Data, &md); err != nil {
				continue
			}
			if compactFocusText(md.Text, promptQuerySnippetMaxLen) == compactFocusText(focus.CurrentRequest, promptQuerySnippetMaxLen) {
				continue
			}
			addSnippet("Recent user request", md.Text)
			if len(snippets) >= 3 {
				break
			}
		case session.EntryTypeMeta:
			if focus.SessionSummary != "" || len(snippets) > 2 {
				continue
			}
			var md session.MessageData
			if err := json.Unmarshal(entry.Data, &md); err != nil {
				continue
			}
			addSnippet("Session summary", md.Text)
		case session.EntryTypeCompaction:
			if len(snippets) > 2 {
				continue
			}
			var compact session.CompactionData
			if err := json.Unmarshal(entry.Data, &compact); err != nil {
				continue
			}
			addSnippet("Compaction summary", compact.Text)
		}
	}
	if focus.SessionSummary != "" {
		addSnippet("Session summary", focus.SessionSummary)
	}
	if len(snippets) == 0 {
		return promptSnippet(fallback, promptQuerySnippetMaxLen)
	}

	for left, right := 0, len(snippets)-1; left < right; left, right = left+1, right-1 {
		snippets[left], snippets[right] = snippets[right], snippets[left]
	}

	lines := make([]string, 0, len(snippets))
	for _, item := range snippets {
		lines = append(lines, item.label+": "+item.text)
	}
	return strings.Join(lines, "\n")
}

func promptSnippet(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	cut := maxLen - 3
	if cut < 1 {
		return text[:maxLen]
	}
	if idx := strings.LastIndex(text[:cut], " "); idx >= cut/2 {
		cut = idx
	}
	return strings.TrimSpace(text[:cut]) + "..."
}
