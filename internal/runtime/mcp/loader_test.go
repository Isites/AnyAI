package mcp

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Isites/anyai/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDirSupportsSingleWrappedAndNamedConfigs(t *testing.T) {
	dir := t.TempDir()
	writeMCPConfig(t, filepath.Join(dir, "single.yaml"), `
name: filesystem
type: stdio
command: npx
args: ["-y", "@modelcontextprotocol/server-filesystem"]
`)
	writeMCPConfig(t, filepath.Join(dir, "wrapped.yaml"), `
mcp_servers:
  browser:
    type: streamable_http
    url: http://127.0.0.1:3333/mcp
`)
	writeMCPConfig(t, filepath.Join(dir, "named.json"), `{
  "search": {
    "type": "sse",
    "url": "http://127.0.0.1:3334/sse"
  }
}`)

	servers, err := LoadDir(dir, ScopeShared)
	require.NoError(t, err)

	var names []string
	for _, server := range servers {
		names = append(names, server.Name)
		assert.Equal(t, ScopeShared, server.Scope)
		assert.NotEmpty(t, server.Source)
	}
	assert.ElementsMatch(t, []string{"filesystem", "browser", "search"}, names)
}

func TestBuildCatalogAppliesScopesAndOverrides(t *testing.T) {
	root := t.TempDir()
	sharedDir := filepath.Join(root, "common", "mcps")
	privateDir := filepath.Join(root, "agent", "mcps")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	require.NoError(t, os.MkdirAll(privateDir, 0o755))

	writeMCPConfig(t, filepath.Join(sharedDir, "shared.yaml"), `
name: shared-tool
command: shared-mcp
`)
	writeMCPConfig(t, filepath.Join(privateDir, "shared.yaml"), `
name: shared-tool
command: private-mcp
`)

	cfg := config.DefaultConfig()
	cfg.SharedMCPsDir = sharedDir
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:                "assistant",
			Model:             "test/model",
			Workspace:         root,
			PrivateMCPsDir:    privateDir,
			InheritSharedMCPs: true,
		},
		{
			ID:                "isolated",
			Model:             "test/model",
			Workspace:         root,
			InheritSharedMCPs: false,
		},
	}

	catalog, err := BuildCatalog(cfg)
	require.NoError(t, err)

	assistant := catalog.ServersForAgent("assistant")
	require.Len(t, assistant, 1)
	assert.Equal(t, "shared-tool", assistant[0].Name)
	assert.Equal(t, "private-mcp", assistant[0].Command)
	assert.Equal(t, ScopePrivate, assistant[0].Scope)

	isolated := catalog.ServersForAgent("isolated")
	assert.Empty(t, isolated)
}

func TestToolNameSanitizesServerAndTool(t *testing.T) {
	assert.Equal(t, "mcp__playwright_browser__browser_click", ToolName("Playwright Browser", "browser.click"))
}

func TestCatalogToolsForAgentConcurrentAccess(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alpha", Model: "test/model"},
		{ID: "beta", Model: "test/model"},
	}
	catalog, err := BuildCatalog(cfg)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			catalog.SetToolsForAgent("alpha", []ToolDescriptor{{Name: "tool-alpha"}})
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = catalog.ToolsForAgent("alpha")
			_ = catalog.ServersForAgent("beta")
			catalog.SetManagerFactory(nil)
		}(i)
	}
	wg.Wait()
	assert.Equal(t, "tool-alpha", catalog.ToolsForAgent("alpha")[0].Name)
}

func writeMCPConfig(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
