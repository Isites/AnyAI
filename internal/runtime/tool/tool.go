package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Isites/anyai/internal/runtime/llm"
)

// Tool is the interface that all AnyAI tools must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage // JSON Schema
	Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

// ToolResult holds the output of a tool execution.
type ToolResult struct {
	Output   string             `json:"output"`
	Error    string             `json:"error,omitempty"`
	Metadata map[string]any     `json:"metadata,omitempty"`
	Images   []llm.ImageContent `json:"-"` // image attachments (not JSON-serialized)
}

// Executor is the interface used by agent runtime for tool operations.
// Both Registry and FilteredRegistry implement this.
type Executor interface {
	Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error)
	ToolDefs() []llm.ToolDef
	Names() []string
	Get(name string) (Tool, bool)
}

// Registry manages a collection of available tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Execute runs a tool by name with the given input.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error) {
	t, ok := r.Get(name)
	if !ok {
		return ToolResult{}, fmt.Errorf("unknown tool: %q", name)
	}
	return t.Execute(ctx, input)
}

// ToolDefs returns the tool definitions for the LLM API.
func (r *Registry) ToolDefs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]llm.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return defs
}

// Names returns the names of all registered tools.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// RegisterCoreTools registers the core tools.
// workDir controls the command/file base directory for relative paths.
// execPolicy is optional — pass nil for unrestricted bash execution.
func RegisterCoreTools(reg *Registry, workDir string, execPolicy *ExecPolicy) {
	reg.Register(&ReadFileTool{WorkDir: workDir})
	reg.Register(&WriteFileTool{WorkDir: workDir})
	reg.Register(&EditFileTool{WorkDir: workDir})
	reg.Register(&BashTool{WorkDir: workDir, ExecPolicy: execPolicy})
	reg.Register(&PythonTool{WorkDir: workDir, ExecPolicy: execPolicy})
	reg.Register(&WebFetchTool{})
	reg.Register(&WebSearchTool{})
	reg.Register(&BrowserTool{})
}

func resolvePathForBase(path, baseDir string) string {
	if baseDir == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

// RegisterSendMessage registers the send_message tool with the given sender.
func RegisterSendMessage(reg *Registry, sender MessageSender) {
	reg.Register(&SendMessageTool{Sender: sender})
}

// RegisterCron registers the cron tool with the given scheduler.
func RegisterCron(reg *Registry, scheduler JobScheduler) {
	reg.Register(&CronTool{Scheduler: scheduler})
}

// RegisterCallAgent registers the callagent tool with the given runner.
func RegisterCallAgent(reg *Registry, runner AgentCallRunner, maxParallel int, defaultIdleTimeoutMS int) {
	reg.Register(&CallAgentTool{
		Runner:               runner,
		MaxParallel:          maxParallel,
		DefaultIdleTimeoutMS: defaultIdleTimeoutMS,
	})
}

// RegisterMemoryTools registers memory_search, memory_get, and memory_save tools.
func RegisterMemoryTools(reg *Registry, memoryProvider MemoryProvider) {
	if memoryProvider == nil {
		return
	}
	reg.Register(&MemorySearchTool{Memory: memoryProvider})
	reg.Register(&MemoryGetTool{Memory: memoryProvider})
	reg.Register(&MemorySaveTool{Memory: memoryProvider})
}

// RegisterSkillTools registers skill_get for loading full skill instructions on demand.
func RegisterSkillTools(reg *Registry, skillProvider SkillProvider) {
	if skillProvider == nil {
		return
	}
	reg.Register(&SkillGetTool{Provider: skillProvider})
}

// RegisterInputTools registers attachment_get and input_manifest tools.
func RegisterInputTools(reg *Registry, attachProvider AttachmentProvider, manifestProvider InputManifestProvider) {
	if attachProvider != nil {
		reg.Register(&AttachmentGetTool{Provider: attachProvider})
	}
	if manifestProvider != nil {
		reg.Register(&InputManifestTool{Provider: manifestProvider})
	}
}

// RegisterPlanTodoTools registers update_plan and todo tools.
func RegisterPlanTodoTools(reg *Registry, planStore PlanStore, todoStore TodoStore) {
	if planStore != nil {
		reg.Register(&UpdatePlanTool{Store: planStore})
	}
	if todoStore != nil {
		reg.Register(&TodoTool{Store: todoStore})
	}
}

// RegisterGoalTools registers goal_complete for runtime-managed goal
// completion when a goal manager is available.
func RegisterGoalTools(reg *Registry, manager GoalManager) {
	if manager == nil {
		return
	}
	reg.Register(&GoalCompleteTool{Manager: manager})
	reg.Register(&AwaitUserInputTool{Manager: manager})
}
