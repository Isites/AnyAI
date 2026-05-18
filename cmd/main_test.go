package main

import (
	"os"
	"path/filepath"
	"testing"

	runtimemcp "github.com/Isites/anyai/internal/runtime/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommandKeepsChatAndRemovesRun(t *testing.T) {
	root := newRootCmd()

	var names []string
	for _, cmd := range root.Commands() {
		names = append(names, cmd.Name())
	}
	assert.Contains(t, names, "chat")
	assert.Contains(t, names, "start")
	assert.Contains(t, names, "mcp")
	assert.Contains(t, names, "init")
	assert.Contains(t, names, "version")
	assert.NotContains(t, names, "run")

	chat, _, err := root.Find([]string{"chat"})
	assert.NoError(t, err)
	assert.Equal(t, "chat", chat.Name())

	start, _, err := root.Find([]string{"start"})
	assert.NoError(t, err)
	assert.Equal(t, "start", start.Name())

	mcp, _, err := root.Find([]string{"mcp"})
	assert.NoError(t, err)
	assert.Equal(t, "mcp", mcp.Name())
}

func TestChatAndStartShareProjectAndAgentParameters(t *testing.T) {
	chat := chatCmd()
	start := startCmd()

	require.Equal(t, "chat [agent-id]", chat.Use)
	require.Equal(t, "start [agent-id]", start.Use)

	chatProject := chat.Flag("project")
	startProject := start.Flag("project")
	require.NotNil(t, chatProject)
	require.NotNil(t, startProject)
	assert.Equal(t, chatProject.DefValue, startProject.DefValue)
	assert.Equal(t, chatProject.Usage, startProject.Usage)
}

func TestResolveProjectTargetAndAgent(t *testing.T) {
	target, agentID, err := resolveProjectTargetAndAgent("/tmp/project", []string{"coder"})
	require.NoError(t, err)
	assert.Equal(t, "/tmp/project", target)
	assert.Equal(t, "coder", agentID)
}

func TestMCPInstallCommonScopeWritesConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "anyai.yaml"), []byte(`
models:
  default: anthropic/claude-sonnet-4-5
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "agent.md"), []byte(`
---
model: anthropic/claude-sonnet-4-5
---
`), 0o644))

	err := runMCPInstall(root, "common", "", testMCPServer("filesystem"), false)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(root, "common", "mcps", "filesystem.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: filesystem")
	assert.Contains(t, string(data), "command: npx")
}

func TestMCPInstallAgentScopeWritesConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "anyai.yaml"), []byte(`
models:
  default: anthropic/claude-sonnet-4-5
`), 0o644))
	agentDir := filepath.Join(root, "agents", "researcher")
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte(`
---
id: researcher
model: anthropic/claude-sonnet-4-5
---
`), 0o644))

	err := runMCPInstall(root, "agent", "researcher", testMCPServer("browser"), false)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(agentDir, "mcps", "browser.yaml"))
	require.NoError(t, err)
}

func TestMCPInstallPreinstallsNPXDependencyAndRewritesConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "anyai.yaml"), []byte(`
models:
  default: anthropic/claude-sonnet-4-5
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "agent.md"), []byte(`
---
model: anthropic/claude-sonnet-4-5
---
`), 0o644))

	originalNPMInstall := npmInstallPackage
	t.Cleanup(func() { npmInstallPackage = originalNPMInstall })
	npmInstallPackage = func(dir, packageSpec string) error {
		require.Equal(t, "chrome-devtools-mcp@latest", packageSpec)
		packageDir := filepath.Join(dir, "node_modules", "chrome-devtools-mcp")
		require.NoError(t, os.MkdirAll(packageDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(`{
  "name": "chrome-devtools-mcp",
  "bin": {
    "chrome-devtools-mcp": "bin/cli.js"
  }
}`), 0o644))
		binDir := filepath.Join(dir, "node_modules", ".bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "chrome-devtools-mcp"), []byte("#!/usr/bin/env node\n"), 0o755))
		return nil
	}

	server := runtimemcp.ServerConfig{
		Name:    "chrome-devtools",
		Type:    runtimemcp.TransportStdio,
		Command: "npx",
		Args: []string{
			"-y",
			"chrome-devtools-mcp@latest",
			"--headless=true",
			"--isolated=true",
		},
	}
	err := runMCPInstallWithOptions(root, "common", "", server, mcpInstallOptions{
		Force:       false,
		InstallDeps: true,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(root, "common", "mcps", "chrome-devtools.yaml"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "name: chrome-devtools")
	assert.Contains(t, content, "command: ../../anyai/mcps/chrome-devtools/node_modules/.bin/chrome-devtools-mcp")
	assert.Contains(t, content, "- --headless=true")
	assert.Contains(t, content, "- --isolated=true")
	assert.NotContains(t, content, "command: npx")
	assert.NotContains(t, content, "chrome-devtools-mcp@latest")
	assert.NotContains(t, content, "- -y")
	_, err = os.Stat(filepath.Join(root, "anyai", "mcps", "chrome-devtools", "package.json"))
	require.NoError(t, err)
}

func TestSplitNPXArgs(t *testing.T) {
	packageSpec, runtimeArgs, ok := splitNPXArgs([]string{
		"-y",
		"chrome-devtools-mcp@latest",
		"--headless=true",
		"--isolated=true",
	})
	require.True(t, ok)
	assert.Equal(t, "chrome-devtools-mcp@latest", packageSpec)
	assert.Equal(t, []string{"--headless=true", "--isolated=true"}, runtimeArgs)

	packageSpec, runtimeArgs, ok = splitNPXArgs([]string{
		"--package",
		"@modelcontextprotocol/server-filesystem@1.0.0",
		"mcp-server-filesystem",
		"/tmp",
	})
	require.True(t, ok)
	assert.Equal(t, "@modelcontextprotocol/server-filesystem@1.0.0", packageSpec)
	assert.Equal(t, []string{"/tmp"}, runtimeArgs)
}

func TestPackageNameFromNPXSpec(t *testing.T) {
	tests := map[string]string{
		"chrome-devtools-mcp@latest":                    "chrome-devtools-mcp",
		"@modelcontextprotocol/server-filesystem@1.0.0": "@modelcontextprotocol/server-filesystem",
		"@scope/pkg":            "@scope/pkg",
		"npm:server-name@2.0.0": "server-name",
		"github:owner/repo":     "",
	}
	for spec, want := range tests {
		assert.Equal(t, want, packageNameFromNPXSpec(spec), spec)
	}
}

func TestBinNameFromPackageJSON(t *testing.T) {
	binName, ok, err := binNameFromPackageJSON([]byte(`{"bin":"bin/cli.js"}`), "chrome-devtools-mcp")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "chrome-devtools-mcp", binName)

	binName, ok, err = binNameFromPackageJSON([]byte(`{"bin":{"other":"bin/other.js","server-filesystem":"bin/fs.js"}}`), "@modelcontextprotocol/server-filesystem")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "server-filesystem", binName)
}

func testMCPServer(name string) runtimemcp.ServerConfig {
	return runtimemcp.ServerConfig{
		Name:    name,
		Type:    runtimemcp.TransportStdio,
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-" + name},
	}
}
