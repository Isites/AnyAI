package resources

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Isites/anyai/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCatalogExposesSkillScopesAndLoaders(t *testing.T) {
	root := t.TempDir()
	sharedDir := filepath.Join(root, "shared-skills")
	privateDir := filepath.Join(root, "assistant-skills")
	sharedMCPDir := filepath.Join(root, "shared-mcps")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	require.NoError(t, os.MkdirAll(privateDir, 0o755))
	require.NoError(t, os.MkdirAll(sharedMCPDir, 0o755))

	writeSkill(t, filepath.Join(sharedDir, "shared", "SKILL.md"), "shared-search", "Shared skill", "Use shared search.")
	writeSkill(t, filepath.Join(privateDir, "private", "SKILL.md"), "private-build", "Private skill", "Build private artifacts.")
	require.NoError(t, os.WriteFile(filepath.Join(sharedMCPDir, "filesystem.yaml"), []byte("name: filesystem\ncommand: npx\n"), 0o644))

	cfg := config.DefaultConfig()
	cfg.SharedSkillsDir = sharedDir
	cfg.SharedMCPsDir = sharedMCPDir
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:                  "assistant",
			Model:               "test/model",
			Workspace:           root,
			PrivateSkillsDir:    privateDir,
			InheritSharedSkills: true,
			InheritSharedMCPs:   true,
			Tools: config.ToolPolicy{
				Allow: []string{"memory_search", "skill_get"},
			},
		},
	}

	catalog, err := BuildCatalog(cfg, BuildDeps{})
	require.NoError(t, err)

	shared := catalog.SharedSkills()
	require.Len(t, shared, 1)
	assert.Equal(t, "shared-search", shared[0].Name)

	agentResources, ok := catalog.Agent("assistant")
	require.True(t, ok)
	require.Len(t, agentResources.EffectiveSkills, 2)
	assert.Equal(t, "private", agentResources.EffectiveSkills[0].Scope)
	assert.Equal(t, "private-build", agentResources.EffectiveSkills[0].Name)
	assert.Equal(t, "shared", agentResources.EffectiveSkills[1].Scope)
	assert.NotEmpty(t, agentResources.Tools)

	loader := catalog.LoaderForAgent("assistant")
	require.NotNil(t, loader)
	item, ok := loader.Get("private-build")
	require.True(t, ok)
	assert.Contains(t, item.Body, "Build private artifacts.")

	globalLoader := catalog.GlobalLoader()
	require.NotNil(t, globalLoader)
	_, ok = globalLoader.Get("shared-search")
	require.True(t, ok)

	require.NotNil(t, catalog.MCPs())
	mcps := catalog.MCPs().ServersForAgent("assistant")
	require.Len(t, mcps, 1)
	assert.Equal(t, "filesystem", mcps[0].Name)
}

func writeSkill(t *testing.T, path, name, description, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
