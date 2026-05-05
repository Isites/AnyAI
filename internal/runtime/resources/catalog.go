package resources

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/tool"
)

// SkillDescriptor describes one loaded skill as exposed by the runtime.
type SkillDescriptor struct {
	Name        string
	Description string
	Tags        []string
	Scope       string
	Source      string
}

// ToolDescriptor describes one available tool as exposed by the runtime.
type ToolDescriptor struct {
	Name        string
	Description string
	Metadata    tools.ToolMetadata
}

// AgentResources captures the preloaded runtime resources for one agent.
type AgentResources struct {
	Agent           config.AgentConfig
	SystemSkills    []SkillDescriptor
	SharedSkills    []SkillDescriptor
	PrivateSkills   []SkillDescriptor
	EffectiveSkills []SkillDescriptor
	Tools           []ToolDescriptor
}

// BuildDeps contains the runtime-owned dependencies needed to build a static
// resource catalog for capability discovery and UI projection.
type BuildDeps struct {
	Sender       tools.MessageSender
	AgentRunner  tools.AgentCallRunner
	JobScheduler tools.JobScheduler
	Memory       *memory.Manager
}

// Catalog is the in-memory snapshot of agent, skill, and tool metadata that
// higher layers can read without rescanning the project on every request.
type Catalog struct {
	systemSkills []SkillDescriptor
	sharedSkills []SkillDescriptor
	agents       []AgentResources
	agentsByID   map[string]AgentResources
	skillLoaders map[string]*skill.Loader
	globalLoader *skill.Loader
}

func BuildCatalog(cfg *config.Config, deps BuildDeps) (*Catalog, error) {
	if cfg == nil {
		return &Catalog{
			agentsByID:   map[string]AgentResources{},
			skillLoaders: map[string]*skill.Loader{},
			globalLoader: skill.NewLoader(),
		}, nil
	}

	systemRaw, err := skill.LoadDir(cfg.SystemSkillsDir)
	if err != nil {
		return nil, fmt.Errorf("load system skills: %w", err)
	}
	sharedRaw, err := skill.LoadDir(cfg.SharedSkillsDir)
	if err != nil {
		return nil, fmt.Errorf("load shared skills: %w", err)
	}

	catalog := &Catalog{
		systemSkills: skillDescriptors(systemRaw, "system"),
		sharedSkills: skillDescriptors(sharedRaw, "shared"),
		agentsByID:   make(map[string]AgentResources, len(cfg.Agents.List)),
		skillLoaders: make(map[string]*skill.Loader, len(cfg.Agents.List)),
		globalLoader: skill.NewLoaderFromSkills(append(append([]skill.Skill(nil), systemRaw...), sharedRaw...)),
	}

	for _, agentCfg := range cfg.Agents.List {
		privateDir := resolvePrivateSkillsDir(agentCfg)
		privateRaw, err := skill.LoadDir(privateDir)
		if err != nil {
			return nil, fmt.Errorf("load private skills for agent %q: %w", agentCfg.ID, err)
		}

		effectiveRaw := mergeSkillLayers(systemRaw, sharedRaw, privateRaw, agentCfg.InheritSharedSkills)
		resource := AgentResources{
			Agent:           cloneAgentConfig(agentCfg),
			SystemSkills:    cloneSkillDescriptors(catalog.systemSkills),
			SharedSkills:    nilSkillDescriptorsUnless(agentCfg.InheritSharedSkills, cloneSkillDescriptors(catalog.sharedSkills)),
			PrivateSkills:   skillDescriptors(privateRaw, "private"),
			EffectiveSkills: effectiveSkillDescriptors(cfg, privateDir, effectiveRaw),
			Tools:           toolDescriptorsForAgent(cfg, &agentCfg, deps, len(effectiveRaw) > 0),
		}
		catalog.agents = append(catalog.agents, resource)
		catalog.agentsByID[agentCfg.ID] = cloneAgentResources(resource)
		catalog.skillLoaders[agentCfg.ID] = skill.NewLoaderFromSkills(effectiveRaw)
	}

	return catalog, nil
}

func (c *Catalog) SystemSkills() []SkillDescriptor {
	if c == nil {
		return nil
	}
	return cloneSkillDescriptors(c.systemSkills)
}

func (c *Catalog) SharedSkills() []SkillDescriptor {
	if c == nil {
		return nil
	}
	return cloneSkillDescriptors(c.sharedSkills)
}

func (c *Catalog) Agents() []AgentResources {
	if c == nil || len(c.agents) == 0 {
		return nil
	}
	out := make([]AgentResources, len(c.agents))
	for i, item := range c.agents {
		out[i] = cloneAgentResources(item)
	}
	return out
}

func (c *Catalog) Agent(agentID string) (AgentResources, bool) {
	if c == nil {
		return AgentResources{}, false
	}
	item, ok := c.agentsByID[strings.TrimSpace(agentID)]
	if !ok {
		return AgentResources{}, false
	}
	return cloneAgentResources(item), true
}

func (c *Catalog) LoaderForAgent(agentID string) *skill.Loader {
	if c == nil {
		return nil
	}
	return c.skillLoaders[strings.TrimSpace(agentID)]
}

func (c *Catalog) GlobalLoader() *skill.Loader {
	if c == nil {
		return nil
	}
	return c.globalLoader
}

func mergeSkillLayers(systemSkills, sharedSkills, privateSkills []skill.Skill, inheritShared bool) []skill.Skill {
	ordered := map[string]skill.Skill{}
	order := []string{}
	appendLayer := func(items []skill.Skill) {
		for _, item := range items {
			if _, seen := ordered[item.Name]; !seen {
				order = append(order, item.Name)
			}
			ordered[item.Name] = item
		}
	}

	appendLayer(systemSkills)
	if inheritShared {
		appendLayer(sharedSkills)
	}
	appendLayer(privateSkills)

	result := make([]skill.Skill, 0, len(order))
	for _, name := range order {
		result = append(result, ordered[name])
	}
	return result
}

func skillDescriptors(items []skill.Skill, scope string) []SkillDescriptor {
	if len(items) == 0 {
		return nil
	}
	result := make([]SkillDescriptor, 0, len(items))
	for _, item := range items {
		result = append(result, SkillDescriptor{
			Name:        item.Name,
			Description: item.Description,
			Tags:        append([]string(nil), item.Tags...),
			Scope:       scope,
			Source:      shortSkillSource(item.FilePath),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

func effectiveSkillDescriptors(cfg *config.Config, privateDir string, items []skill.Skill) []SkillDescriptor {
	if len(items) == 0 {
		return nil
	}
	result := make([]SkillDescriptor, 0, len(items))
	for _, item := range items {
		result = append(result, SkillDescriptor{
			Name:        item.Name,
			Description: item.Description,
			Tags:        append([]string(nil), item.Tags...),
			Scope:       resolveSkillScope(cfg, privateDir, item.FilePath),
			Source:      shortSkillSource(item.FilePath),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Scope != result[j].Scope {
			return result[i].Scope < result[j].Scope
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func toolDescriptorsForAgent(cfg *config.Config, agentCfg *config.AgentConfig, deps BuildDeps, hasSkills bool) []ToolDescriptor {
	extras := tools.ExtraToolDeps{
		AttachmentProvider:    capabilityAttachmentProvider{},
		InputManifestProvider: capabilityManifestProvider{},
		PlanStore:             capabilityPlanStore{},
		TodoStore:             capabilityTodoStore{},
	}
	if hasSkills {
		extras.SkillProvider = capabilitySkillProvider{}
	}
	if deps.Memory != nil {
		extras.MemoryProvider = &capabilityMemoryProvider{}
	}

	registry := tools.BuildAgentRegistryWithExtras(cfg, agentCfg, deps.Sender, deps.AgentRunner, deps.JobScheduler, extras)
	if registry == nil {
		return nil
	}
	executor := tools.ExecutorForAgentWithExtras(cfg, agentCfg, deps.Sender, deps.AgentRunner, deps.JobScheduler, extras)
	names := executor.Names()
	sort.Strings(names)

	result := make([]ToolDescriptor, 0, len(names))
	for _, name := range names {
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		view := ToolDescriptor{
			Name:        name,
			Description: tool.Description(),
			Metadata:    tools.DescribeToolMetadata(tool),
		}
		result = append(result, view)
	}
	return result
}

func resolvePrivateSkillsDir(agentCfg config.AgentConfig) string {
	privateDir := strings.TrimSpace(agentCfg.PrivateSkillsDir)
	if privateDir == "" && strings.TrimSpace(agentCfg.Workspace) != "" {
		privateDir = filepath.Join(agentCfg.Workspace, "skills")
	}
	return privateDir
}

func resolveSkillScope(cfg *config.Config, privateDir, filePath string) string {
	switch {
	case pathWithin(filePath, strings.TrimSpace(privateDir)):
		return "private"
	case cfg != nil && pathWithin(filePath, strings.TrimSpace(cfg.SharedSkillsDir)):
		return "shared"
	case cfg != nil && pathWithin(filePath, strings.TrimSpace(cfg.SystemSkillsDir)):
		return "system"
	default:
		return "custom"
	}
}

func shortSkillSource(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	parent := filepath.Base(filepath.Dir(filePath))
	name := filepath.Base(filePath)
	if parent == "." || parent == "/" || parent == "" {
		return name
	}
	return filepath.ToSlash(filepath.Join(parent, name))
}

func pathWithin(path, root string) bool {
	path = strings.TrimSpace(path)
	root = strings.TrimSpace(root)
	if path == "" || root == "" {
		return false
	}
	pathAbs, err := canonicalPath(path)
	if err != nil {
		return false
	}
	rootAbs, err := canonicalPath(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && rel != "")
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		return resolved, nil
	}
	return abs, nil
}

func cloneSkillDescriptors(items []SkillDescriptor) []SkillDescriptor {
	if len(items) == 0 {
		return nil
	}
	out := make([]SkillDescriptor, len(items))
	for i, item := range items {
		out[i] = item
		if len(item.Tags) > 0 {
			out[i].Tags = append([]string(nil), item.Tags...)
		}
	}
	return out
}

func nilSkillDescriptorsUnless(ok bool, values []SkillDescriptor) []SkillDescriptor {
	if !ok {
		return nil
	}
	return values
}

func cloneToolDescriptors(items []ToolDescriptor) []ToolDescriptor {
	if len(items) == 0 {
		return nil
	}
	out := make([]ToolDescriptor, len(items))
	copy(out, items)
	return out
}

func cloneAgentConfig(agentCfg config.AgentConfig) config.AgentConfig {
	cloned := agentCfg
	cloned.Fallbacks = append([]string(nil), agentCfg.Fallbacks...)
	cloned.Tags = append([]string(nil), agentCfg.Tags...)
	cloned.Tools.Allow = append([]string(nil), agentCfg.Tools.Allow...)
	cloned.Tools.Deny = append([]string(nil), agentCfg.Tools.Deny...)
	if len(agentCfg.Cron) > 0 {
		cloned.Cron = append([]config.CronConfig(nil), agentCfg.Cron...)
	}
	return cloned
}

func cloneAgentResources(item AgentResources) AgentResources {
	cloned := item
	cloned.Agent = cloneAgentConfig(item.Agent)
	cloned.SystemSkills = cloneSkillDescriptors(item.SystemSkills)
	cloned.SharedSkills = cloneSkillDescriptors(item.SharedSkills)
	cloned.PrivateSkills = cloneSkillDescriptors(item.PrivateSkills)
	cloned.EffectiveSkills = cloneSkillDescriptors(item.EffectiveSkills)
	cloned.Tools = cloneToolDescriptors(item.Tools)
	return cloned
}

type capabilityAttachmentProvider struct{}

func (capabilityAttachmentProvider) GetAttachment(id string) (tools.AttachmentInfo, bool) {
	return tools.AttachmentInfo{}, false
}

type capabilityManifestProvider struct{}

func (capabilityManifestProvider) InputManifest() []tools.InputBlockInfo {
	return nil
}

type capabilitySkillProvider struct{}

func (capabilitySkillProvider) GetSkill(name string) (tools.SkillDocument, bool) {
	return tools.SkillDocument{}, false
}

type capabilityPlanStore struct{}

func (capabilityPlanStore) UpdateStructuredPlan(plan runtimeplan.Plan) error {
	return nil
}

func (capabilityPlanStore) GetStructuredPlan() (runtimeplan.Plan, bool) {
	return runtimeplan.Plan{}, false
}

type capabilityTodoStore struct{}

func (capabilityTodoStore) AddTodo(context.Context, string) string {
	return ""
}

func (capabilityTodoStore) CompleteTodo(context.Context, string) bool {
	return false
}

func (capabilityTodoStore) ListTodos(context.Context) []tools.TodoItem {
	return nil
}

type capabilityMemoryProvider struct{}

func (capabilityMemoryProvider) Search(query string, maxItems int) []tools.MemoryEntry {
	return nil
}

func (capabilityMemoryProvider) Get(id string) (tools.MemoryEntry, bool) {
	return tools.MemoryEntry{}, false
}

func (capabilityMemoryProvider) Save(entry tools.MemoryEntry) (tools.MemoryEntry, error) {
	return tools.MemoryEntry{}, fmt.Errorf("capability catalog provider does not persist memory")
}
