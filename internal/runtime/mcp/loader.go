package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"gopkg.in/yaml.v3"
)

func LoadDir(dir, scope string) ([]ServerConfig, error) {
	dir = normalizeDir(dir)
	if dir == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}

	var servers []ServerConfig
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isConfigFile(path) {
			return nil
		}

		loaded, err := parseConfigFile(path)
		if err != nil {
			runtimelogging.Warn("failed to parse mcp config", "path", path, "error", err)
			return nil
		}
		for _, server := range loaded {
			server = normalizeServerConfig(server, path, scope)
			if err := validateServerConfig(server); err != nil {
				runtimelogging.Warn("invalid mcp config skipped", "path", path, "server", server.Name, "error", err)
				continue
			}
			if server.Disabled {
				continue
			}
			servers = append(servers, server)
			runtimelogging.Info("loaded mcp server", "name", server.Name, "scope", server.Scope, "path", path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	SortServers(servers)
	return servers, nil
}

func parseConfigFile(path string) ([]ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var single ServerConfig
	if err := unmarshalConfig(path, data, &single); err == nil && strings.TrimSpace(single.Name) != "" {
		return []ServerConfig{single}, nil
	}

	var wrapped struct {
		MCPServers      map[string]ServerConfig `json:"mcp_servers" yaml:"mcp_servers"`
		MCPServersCamel map[string]ServerConfig `json:"mcpServers" yaml:"mcpServers"`
		Servers         map[string]ServerConfig `json:"servers" yaml:"servers"`
	}
	if err := unmarshalConfig(path, data, &wrapped); err == nil {
		source := wrapped.MCPServers
		if len(source) == 0 {
			source = wrapped.MCPServersCamel
		}
		if len(source) == 0 {
			source = wrapped.Servers
		}
		if len(source) > 0 {
			out := make([]ServerConfig, 0, len(source))
			for name, cfg := range source {
				if strings.TrimSpace(cfg.Name) == "" {
					cfg.Name = name
				}
				out = append(out, cfg)
			}
			return out, nil
		}
	}

	var named map[string]ServerConfig
	if err := unmarshalConfig(path, data, &named); err == nil && len(named) > 0 {
		out := make([]ServerConfig, 0, len(named))
		for name, cfg := range named {
			if !looksLikeServerConfig(cfg) {
				continue
			}
			if strings.TrimSpace(cfg.Name) == "" {
				cfg.Name = name
			}
			out = append(out, cfg)
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	return nil, fmt.Errorf("no MCP server definitions found")
}

func unmarshalConfig(path string, data []byte, out any) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return json.Unmarshal(data, out)
	default:
		return yaml.Unmarshal(data, out)
	}
}

func normalizeServerConfig(server ServerConfig, path, scope string) ServerConfig {
	server.Name = strings.TrimSpace(server.Name)
	if server.Name == "" {
		server.Name = serverBaseName(path)
	}
	server.Description = strings.TrimSpace(server.Description)
	server.Type = strings.TrimSpace(server.Type)
	server.Command = strings.TrimSpace(server.Command)
	server.URL = strings.TrimSpace(server.URL)
	server.Root = strings.TrimSpace(server.Root)
	server.Scope = strings.TrimSpace(scope)
	server.Source = path
	server.BaseDir = filepath.Dir(path)
	server.Env = cloneStringMap(server.Env)
	server.Headers = cloneStringMap(server.Headers)
	server.Args = normalizeStringSlice(server.Args)
	server.Tools.Allow = normalizeStringSlice(server.Tools.Allow)
	server.Tools.Deny = normalizeStringSlice(server.Tools.Deny)
	return server
}

func looksLikeServerConfig(server ServerConfig) bool {
	return strings.TrimSpace(server.Name) != "" ||
		strings.TrimSpace(server.Command) != "" ||
		strings.TrimSpace(server.URL) != "" ||
		strings.TrimSpace(server.Type) != "" ||
		len(server.Args) > 0 ||
		len(server.Env) > 0 ||
		len(server.Headers) > 0
}

func validateServerConfig(server ServerConfig) error {
	if strings.TrimSpace(server.Name) == "" {
		return fmt.Errorf("name is required")
	}
	switch server.TransportType() {
	case TransportStdio:
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("command is required for stdio MCP server")
		}
	case TransportSSE, TransportStreamable:
		if strings.TrimSpace(server.URL) == "" {
			return fmt.Errorf("url is required for %s MCP server", server.TransportType())
		}
	default:
		return fmt.Errorf("unsupported MCP transport type %q", server.Type)
	}
	if server.StartupTimeoutMS < 0 {
		return fmt.Errorf("startup_timeout_ms must be >= 0")
	}
	if server.ToolTimeoutMS < 0 {
		return fmt.Errorf("tool_timeout_ms must be >= 0")
	}
	return nil
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func normalizeDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	normalized := filepath.Clean(dir)
	if abs, err := filepath.Abs(normalized); err == nil {
		normalized = abs
	}
	if resolved, err := filepath.EvalSymlinks(normalized); err == nil && strings.TrimSpace(resolved) != "" {
		normalized = resolved
	}
	return normalized
}

func isConfigFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml", ".json":
		return true
	default:
		return false
	}
}
