package httpchannel

import (
	"sort"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
)

type agentInventoryView struct {
	Agents       []agentCapabilityView `json:"agents"`
	SharedSkills []skillCapabilityView `json:"shared_skills,omitempty"`
	Notes        []string              `json:"notes,omitempty"`
}

type agentCapabilityView struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Model       string               `json:"model"`
	Workspace   string               `json:"workspace"`
	Entry       bool                 `json:"entry"`
	Tags        []string             `json:"tags,omitempty"`
	Direct      directRequestView    `json:"direct_request"`
	ToolPolicy  toolPolicyView       `json:"tool_policy"`
	Tools       []toolCapabilityView `json:"tools,omitempty"`
	Skills      skillBundleView      `json:"skills"`
}

type directRequestView struct {
	Supported    bool     `json:"supported"`
	Recommended  bool     `json:"recommended"`
	SessionScope string   `json:"session_scope"`
	Warning      string   `json:"warning,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

type toolPolicyView struct {
	Mode  string   `json:"mode"`
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

type toolCapabilityView struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Metadata    gateway.ToolMetadata `json:"metadata"`
}

type skillBundleView struct {
	Shared    []skillCapabilityView `json:"shared,omitempty"`
	Private   []skillCapabilityView `json:"private,omitempty"`
	Effective []skillCapabilityView `json:"effective,omitempty"`
}

type skillCapabilityView struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Scope       string   `json:"scope"`
	Source      string   `json:"source,omitempty"`
}

func (p *ControlPlane) agentInventory() agentInventoryView {
	cfg := p.config()
	if cfg == nil {
		return agentInventoryView{}
	}
	var resources gateway.ResourceCatalog
	if p != nil && p.inventory != nil {
		resources = p.inventory.ResourceCatalog()
	}
	if len(resources.Agents) == 0 && len(resources.SharedSkills) == 0 {
		return agentInventoryView{}
	}
	return p.agentInventoryFromResources(cfg, resources)
}

func (p *ControlPlane) agentInventoryFromResources(cfg *config.Config, resources gateway.ResourceCatalog) agentInventoryView {
	sharedSkills := skillCapabilityViewsFromResources(resources.SharedSkills)
	agents := make([]agentCapabilityView, 0, len(resources.Agents))
	for _, resource := range resources.Agents {
		agents = append(agents, agentCapabilityFromResources(cfg, resource))
	}
	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].Direct.Recommended != agents[j].Direct.Recommended {
			return agents[i].Direct.Recommended
		}
		return agents[i].ID < agents[j].ID
	})

	return agentInventoryView{
		Agents:       agents,
		SharedSkills: sharedSkills,
		Notes: []string{
			"显式指定 agent_id 时，请求会直接命中该 agent，而不是先经过入口 agent 编排。",
			"session 是按 (agent_id, session_id) 隔离的；同一个 session_id 在不同 agent 下不会共享历史。",
			"非入口 agent 的直连更适合专家位或工具位调用；若要保留全局编排语义，优先从入口 agent 进入。",
		},
	}
}

func agentCapabilityFromResources(cfg *config.Config, resource gateway.AgentResources) agentCapabilityView {
	agentCfg := resource.Agent
	return agentCapabilityView{
		ID:          agentCfg.ID,
		Name:        agentCfg.Name,
		Description: agentCfg.Description,
		Model:       agentCfg.Model,
		Workspace:   agentCfg.Workspace,
		Entry:       agentCfg.Entry,
		Tags:        append([]string(nil), agentCfg.Tags...),
		Direct:      directRequestForAgent(cfg, agentCfg),
		ToolPolicy:  toolPolicyForAgent(agentCfg),
		Tools:       toolCapabilityViewsFromResources(resource.Tools),
		Skills: skillBundleView{
			Shared:    skillCapabilityViewsFromResources(resource.SharedSkills),
			Private:   skillCapabilityViewsFromResources(resource.PrivateSkills),
			Effective: skillCapabilityViewsFromResources(resource.EffectiveSkills),
		},
	}
}

func skillCapabilityViewsFromResources(items []gateway.SkillDescriptor) []skillCapabilityView {
	if len(items) == 0 {
		return nil
	}
	result := make([]skillCapabilityView, len(items))
	for i, item := range items {
		result[i] = skillCapabilityView{
			Name:        item.Name,
			Description: item.Description,
			Tags:        append([]string(nil), item.Tags...),
			Scope:       item.Scope,
			Source:      item.Source,
		}
	}
	return result
}

func toolCapabilityViewsFromResources(items []gateway.ToolDescriptor) []toolCapabilityView {
	if len(items) == 0 {
		return nil
	}
	result := make([]toolCapabilityView, len(items))
	for i, item := range items {
		result[i] = toolCapabilityView{
			Name:        item.Name,
			Description: item.Description,
			Metadata:    item.Metadata,
		}
	}
	return result
}

func directRequestForAgent(cfg *config.Config, agentCfg config.AgentConfig) directRequestView {
	recommended := isPreferredIngressAgent(cfg, agentCfg)
	view := directRequestView{
		Supported:    true,
		Recommended:  recommended,
		SessionScope: "agent-local",
		Notes: []string{
			"同一个 session_id 在不同 agent 下会落到不同的会话文件。",
			"显式直连不会自动继承入口 agent 的编排上下文或会话历史。",
		},
	}
	if !recommended {
		view.Warning = "直连该 agent 会绕过入口 agent 的路由与编排，更适合专家位/工具位调度，而不是默认用户入口。"
	}
	return view
}

func isPreferredIngressAgent(cfg *config.Config, agentCfg config.AgentConfig) bool {
	if agentCfg.Entry {
		return true
	}
	if cfg == nil || len(cfg.Agents.List) == 0 {
		return false
	}
	for _, item := range cfg.Agents.List {
		if item.Entry {
			return item.ID == agentCfg.ID
		}
	}
	return cfg.Agents.List[0].ID == agentCfg.ID
}

func toolPolicyForAgent(agentCfg config.AgentConfig) toolPolicyView {
	mode := "all"
	if len(agentCfg.Tools.Allow) > 0 && len(agentCfg.Tools.Deny) > 0 {
		mode = "mixed"
	} else if len(agentCfg.Tools.Allow) > 0 {
		mode = "allowlist"
	} else if len(agentCfg.Tools.Deny) > 0 {
		mode = "denylist"
	}
	return toolPolicyView{
		Mode:  mode,
		Allow: append([]string(nil), agentCfg.Tools.Allow...),
		Deny:  append([]string(nil), agentCfg.Tools.Deny...),
	}
}
