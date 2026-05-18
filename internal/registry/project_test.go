package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Isites/anyai/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadProjectDirectoryMode(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "anyai.yaml"), []byte(`
name: demo
models:
  default: anthropic/claude-sonnet-4-5
channels:
  gateway:
    enabled: [cli]
  http:
    listen: 127.0.0.1:19001
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "agent.md"), []byte(`
---
name: root-agent
---

You are the root.
`), 0o644))

	coderDir := filepath.Join(root, "agents", "coder")
	require.NoError(t, os.MkdirAll(filepath.Join(coderDir, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(coderDir, "mcps"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "common", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "common", "mcps"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(coderDir, "agent.md"), []byte(`
---
entry: true
model: anthropic/claude-sonnet-4-5
mcps:
  inherit_shared: false
tools:
  allow: [read_file]
---

You are the coder.
`), 0o644))

	project, err := LoadProject(root)
	require.NoError(t, err)

	assert.Equal(t, LoadModeDirectory, project.Mode)
	assert.Equal(t, root, project.RootDir)
	assert.Len(t, project.Agents, 2)
	assert.Equal(t, "coder", project.Config.Agents.List[0].ID)
	assert.Equal(t, "demo", project.Config.ProjectName)
	assert.Equal(t, 19001, project.Config.Gateway.Port)
	assert.Equal(t, filepath.Join(root, "common", "skills"), project.Config.SharedSkillsDir)
	assert.Equal(t, filepath.Join(root, "common", "mcps"), project.Config.SharedMCPsDir)
	assert.Equal(t, filepath.Join(root, "anyai", "memory"), project.Config.Memory.Dir)
	assert.True(t, project.Config.Memory.Enabled)
	assert.Contains(t, project.Config.Bindings, config.Binding{AgentID: "coder", Match: config.BindingMatch{Channel: "http"}})
	coderCfg, ok := project.Config.GetAgent("coder")
	require.True(t, ok)
	assert.Equal(t, filepath.Join(coderDir, "skills"), coderCfg.PrivateSkillsDir)
	assert.Equal(t, filepath.Join(coderDir, "mcps"), coderCfg.PrivateMCPsDir)
	assert.False(t, coderCfg.InheritSharedMCPs)
	entry, err := project.SelectEntry("")
	require.NoError(t, err)
	assert.Equal(t, "coder", entry.ID)
}

func TestLoadProjectFileModeWithConfigScansWholeProject(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "anyai.yaml"), []byte(`
models:
  default: anthropic/claude-sonnet-4-5
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "agent.md"), []byte("root body"), 0o644))
	otherDir := filepath.Join(root, "agents", "reviewer")
	require.NoError(t, os.MkdirAll(otherDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "agent.md"), []byte(`
---
entry: true
---

review body
`), 0o644))

	project, err := LoadProject(filepath.Join(otherDir, "agent.md"))
	require.NoError(t, err)

	assert.Equal(t, LoadModeFile, project.Mode)
	assert.False(t, project.FileModeSingle)
	assert.Len(t, project.Agents, 2)
	assert.Equal(t, "reviewer", project.ExplicitEntryID)
	entry, err := project.SelectEntry("")
	require.NoError(t, err)
	assert.Equal(t, "reviewer", entry.ID)
}

func TestLoadProjectFileModeWithoutConfigIsSingleAgent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "agent.md"), []byte(`
---
model: anthropic/claude-sonnet-4-5
---

solo
`), 0o644))
	otherDir := filepath.Join(root, "agents", "helper")
	require.NoError(t, os.MkdirAll(otherDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "agent.md"), []byte("helper"), 0o644))

	project, err := LoadProject(filepath.Join(root, "agent.md"))
	require.NoError(t, err)

	assert.True(t, project.FileModeSingle)
	assert.Len(t, project.Agents, 1)
	assert.Equal(t, filepath.Join(root, "agent.md"), project.Agents[0].FilePath)
}

func TestLoadProjectRejectsDuplicateAgentIDs(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "anyai.yaml"), []byte(`
models:
  default: anthropic/claude-sonnet-4-5
`), 0o644))
	dirA := filepath.Join(root, "agents", "a")
	dirB := filepath.Join(root, "agents", "b")
	require.NoError(t, os.MkdirAll(dirA, 0o755))
	require.NoError(t, os.MkdirAll(dirB, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dirA, "agent.md"), []byte("---\nid: dup\n---\na"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirB, "agent.md"), []byte("---\nid: dup\n---\nb"), 0o644))

	_, err := LoadProject(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent id")
}

func TestSelectEntryRequiresDisambiguation(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "anyai.yaml"), []byte(`
models:
  default: anthropic/claude-sonnet-4-5
`), 0o644))
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(root, "agents", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "agent.md"), []byte("body"), 0o644))
	}

	project, err := LoadProject(root)
	require.NoError(t, err)

	_, err = project.SelectEntry("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple agents found")

	entry, err := project.SelectEntry("a")
	require.NoError(t, err)
	assert.Equal(t, "a", entry.ID)
}

func TestLoadProjectUsesRuntimeMemoryPath(t *testing.T) {
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
	require.NoError(t, os.MkdirAll(filepath.Join(root, "memory", "long-term"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "memory", "long-term", "policy.md"), []byte("# Policy\n"), 0o644))

	project, err := LoadProject(root)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(root, "anyai", "memory"), project.Config.Memory.Dir)
	assert.True(t, project.Config.Memory.Enabled)
}

func TestLoadProjectRespectsExplicitMemoryDisable(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "anyai.yaml"), []byte(`
models:
  default: anthropic/claude-sonnet-4-5
memory:
  enabled: false
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "agent.md"), []byte(`
---
model: anthropic/claude-sonnet-4-5
---
`), 0o644))

	project, err := LoadProject(root)
	require.NoError(t, err)

	assert.False(t, project.Config.Memory.Enabled)
	assert.Equal(t, filepath.Join(root, "anyai", "memory"), project.Config.Memory.Dir)
}

func TestBuildProviderConfigsMergesHeaderEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "runtime-key")
	t.Setenv("OPENAI_HEADERS_JSON", `{"HTTP-Referer":"https://override.example/app","X-Route":"staging"}`)

	providers, err := buildProviderConfigs(&config.ProjectConfig{
		Providers: map[string]config.ProjectProviderConfig{
			"openai": {
				Kind:      "openai",
				APIKeyEnv: "OPENAI_API_KEY",
				BaseURL:   "https://api.openai.com/v1",
				Headers: map[string]string{
					"HTTP-Referer": "https://example.com/app",
					"X-Title":      "AnyAI",
				},
				HeadersEnv: "OPENAI_HEADERS_JSON",
			},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, map[string]config.ProviderConfig{
		"openai": {
			Kind:    "openai",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "runtime-key",
			Headers: map[string]string{
				"HTTP-Referer": "https://override.example/app",
				"X-Title":      "AnyAI",
				"X-Route":      "staging",
			},
		},
	}, providers)
}

func TestBuildProviderConfigsUsesInlineAPIKeyWhenPresent(t *testing.T) {
	providers, err := buildProviderConfigs(&config.ProjectConfig{
		Providers: map[string]config.ProjectProviderConfig{
			"openai": {
				Kind:    "openai-compatible",
				APIKey:  "inline-secret",
				BaseURL: "https://coding.testbird.ai/v1",
			},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, map[string]config.ProviderConfig{
		"openai": {
			Kind:    "openai-compatible",
			BaseURL: "https://coding.testbird.ai/v1",
			APIKey:  "inline-secret",
		},
	}, providers)
}

func TestBuildProviderConfigsRejectsInvalidHeaderEnvJSON(t *testing.T) {
	_, err := buildProviderConfigs(&config.ProjectConfig{
		Providers: map[string]config.ProjectProviderConfig{
			"openai": {
				Kind:       "openai",
				HeadersEnv: "OPENAI_HEADERS_JSON",
			},
		},
	})
	require.NoError(t, err)

	t.Setenv("OPENAI_HEADERS_JSON", `{"broken":`)

	_, err = buildProviderConfigs(&config.ProjectConfig{
		Providers: map[string]config.ProjectProviderConfig{
			"openai": {
				Kind:       "openai",
				HeadersEnv: "OPENAI_HEADERS_JSON",
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "headers_env")
}
