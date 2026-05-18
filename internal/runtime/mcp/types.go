package mcp

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/llm"
)

const (
	ScopeShared  = "shared"
	ScopePrivate = "private"
)

const (
	TransportStdio      = "stdio"
	TransportSSE        = "sse"
	TransportStreamable = "streamable_http"
)

const (
	DefaultStartupTimeout = 15 * time.Second
	DefaultToolTimeout    = 60 * time.Second
)

// ServerConfig describes one MCP server declared in a project.
type ServerConfig struct {
	Name             string            `json:"name" yaml:"name"`
	Description      string            `json:"description,omitempty" yaml:"description,omitempty"`
	Type             string            `json:"type,omitempty" yaml:"type,omitempty"`
	Command          string            `json:"command,omitempty" yaml:"command,omitempty"`
	Args             []string          `json:"args,omitempty" yaml:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	URL              string            `json:"url,omitempty" yaml:"url,omitempty"`
	Headers          map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Root             string            `json:"root,omitempty" yaml:"root,omitempty"`
	StartupTimeoutMS int               `json:"startup_timeout_ms,omitempty" yaml:"startup_timeout_ms,omitempty"`
	ToolTimeoutMS    int               `json:"tool_timeout_ms,omitempty" yaml:"tool_timeout_ms,omitempty"`
	Disabled         bool              `json:"disabled,omitempty" yaml:"disabled,omitempty"`
	Tools            ServerToolPolicy  `json:"tools,omitempty" yaml:"tools,omitempty"`

	Scope   string `json:"-" yaml:"-"`
	Source  string `json:"-" yaml:"-"`
	BaseDir string `json:"-" yaml:"-"`
}

type ServerToolPolicy struct {
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty" yaml:"deny,omitempty"`
}

func (c ServerConfig) StartupTimeout() time.Duration {
	if c.StartupTimeoutMS <= 0 {
		return DefaultStartupTimeout
	}
	return time.Duration(c.StartupTimeoutMS) * time.Millisecond
}

func (c ServerConfig) ToolTimeout() time.Duration {
	if c.ToolTimeoutMS <= 0 {
		return DefaultToolTimeout
	}
	return time.Duration(c.ToolTimeoutMS) * time.Millisecond
}

func (c ServerConfig) TransportType() string {
	kind := strings.TrimSpace(strings.ToLower(c.Type))
	switch kind {
	case "", TransportStdio, "command", "cmd":
		if strings.TrimSpace(c.URL) != "" && strings.TrimSpace(c.Command) == "" {
			return TransportStreamable
		}
		return TransportStdio
	case "http", "streamable", "streamable-http", "streamable_http":
		return TransportStreamable
	case "sse":
		return TransportSSE
	default:
		return kind
	}
}

func (c ServerConfig) Clone() ServerConfig {
	cloned := c
	cloned.Args = append([]string(nil), c.Args...)
	cloned.Env = cloneStringMap(c.Env)
	cloned.Headers = cloneStringMap(c.Headers)
	cloned.Tools.Allow = append([]string(nil), c.Tools.Allow...)
	cloned.Tools.Deny = append([]string(nil), c.Tools.Deny...)
	return cloned
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		dst[key] = strings.TrimSpace(value)
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

// ToolDescriptor describes one tool exposed by a configured MCP server.
type ToolDescriptor struct {
	Name        string
	ServerName  string
	RemoteName  string
	Description string
	InputSchema json.RawMessage
	Scope       string
	Source      string
	ReadOnly    bool
}

// CallResult is the transport-neutral result returned by an MCP server.
type CallResult struct {
	Output   string
	Error    string
	Metadata map[string]any
	Images   []llm.ImageContent
}

type Catalog struct {
	mu             sync.RWMutex
	sharedServers  []ServerConfig
	agentServers   map[string][]ServerConfig
	toolsByAgent   map[string][]ToolDescriptor
	managerFactory func([]ServerConfig) *Manager
}

func NewCatalog(sharedServers []ServerConfig, agentServers map[string][]ServerConfig) *Catalog {
	c := &Catalog{
		sharedServers: cloneServers(sharedServers),
		agentServers:  map[string][]ServerConfig{},
		toolsByAgent:  map[string][]ToolDescriptor{},
	}
	for agentID, servers := range agentServers {
		c.agentServers[strings.TrimSpace(agentID)] = cloneServers(servers)
	}
	return c
}

func EmptyCatalog() *Catalog {
	return NewCatalog(nil, nil)
}

func (c *Catalog) SharedServers() []ServerConfig {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneServers(c.sharedServers)
}

func (c *Catalog) ServersForAgent(agentID string) []ServerConfig {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneServers(c.agentServers[strings.TrimSpace(agentID)])
}

func (c *Catalog) ToolsForAgent(agentID string) []ToolDescriptor {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	tools := c.toolsByAgent[strings.TrimSpace(agentID)]
	out := make([]ToolDescriptor, len(tools))
	copy(out, tools)
	return out
}

func (c *Catalog) SetToolsForAgent(agentID string, tools []ToolDescriptor) {
	if c == nil {
		return
	}
	agentID = strings.TrimSpace(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.toolsByAgent == nil {
		c.toolsByAgent = map[string][]ToolDescriptor{}
	}
	c.toolsByAgent[agentID] = append([]ToolDescriptor(nil), tools...)
}

func (c *Catalog) ManagerForAgent(agentID string) *Manager {
	if c == nil {
		return nil
	}
	servers := c.ServersForAgent(agentID)
	if len(servers) == 0 {
		return nil
	}
	c.mu.RLock()
	factory := c.managerFactory
	c.mu.RUnlock()
	if factory != nil {
		return factory(servers)
	}
	return NewManager(servers)
}

func (c *Catalog) SetManagerFactory(factory func([]ServerConfig) *Manager) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.managerFactory = factory
}

func BuildCatalog(cfg *config.Config) (*Catalog, error) {
	if cfg == nil {
		return EmptyCatalog(), nil
	}
	sharedServers, err := LoadDir(cfg.SharedMCPsDir, ScopeShared)
	if err != nil {
		return nil, err
	}

	agentServers := make(map[string][]ServerConfig, len(cfg.Agents.List))
	for _, agentCfg := range cfg.Agents.List {
		privateServers, err := LoadDir(resolvePrivateMCPsDir(agentCfg), ScopePrivate)
		if err != nil {
			return nil, err
		}
		var effective []ServerConfig
		if agentCfg.InheritSharedMCPs {
			effective = mergeServerLayers(sharedServers)
		}
		effective = mergeServerLayers(effective, privateServers)
		agentServers[agentCfg.ID] = effective
	}

	return NewCatalog(sharedServers, agentServers), nil
}

func resolvePrivateMCPsDir(agentCfg config.AgentConfig) string {
	privateDir := strings.TrimSpace(agentCfg.PrivateMCPsDir)
	if privateDir == "" && strings.TrimSpace(agentCfg.Workspace) != "" {
		privateDir = filepath.Join(agentCfg.Workspace, "mcps")
	}
	return privateDir
}

func mergeServerLayers(layers ...[]ServerConfig) []ServerConfig {
	ordered := map[string]ServerConfig{}
	order := []string{}
	for _, layer := range layers {
		for _, server := range layer {
			name := strings.TrimSpace(server.Name)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, seen := ordered[key]; !seen {
				order = append(order, key)
			}
			ordered[key] = server.Clone()
		}
	}
	out := make([]ServerConfig, 0, len(order))
	for _, key := range order {
		out = append(out, ordered[key])
	}
	return out
}

func cloneServers(items []ServerConfig) []ServerConfig {
	if len(items) == 0 {
		return nil
	}
	out := make([]ServerConfig, len(items))
	for i, item := range items {
		out[i] = item.Clone()
	}
	return out
}

func SortServers(servers []ServerConfig) {
	sort.SliceStable(servers, func(i, j int) bool {
		return strings.ToLower(servers[i].Name) < strings.ToLower(servers[j].Name)
	})
}

func serverBaseName(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func compactJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}
