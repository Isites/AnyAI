package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Isites/anyai/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSkillProvider struct {
	docs map[string]SkillDocument
}

func (m mockSkillProvider) GetSkill(name string) (SkillDocument, bool) {
	doc, ok := m.docs[name]
	return doc, ok
}

func TestSkillGetToolReturnsFullSkillDocument(t *testing.T) {
	tool := &SkillGetTool{
		Provider: mockSkillProvider{
			docs: map[string]SkillDocument{
				"rollout-playbook": {
					Name:        "rollout-playbook",
					Description: "Apollo rollout plan",
					Tags:        []string{"rollout", "apollo"},
					Source:      "skills/rollout-playbook.md",
					Content:     "Verify staging before rollout.",
				},
			},
		},
	}

	input, _ := json.Marshal(map[string]string{"name": "rollout-playbook"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)
	assert.Contains(t, result.Output, "## Skill: rollout-playbook")
	assert.Contains(t, result.Output, "- Summary: Apollo rollout plan")
	assert.Contains(t, result.Output, "- Tags: rollout, apollo")
	assert.Contains(t, result.Output, "Verify staging before rollout.")
}

func TestSkillGetToolReturnsErrorWhenMissing(t *testing.T) {
	tool := &SkillGetTool{Provider: mockSkillProvider{docs: map[string]SkillDocument{}}}

	input, _ := json.Marshal(map[string]string{"name": "missing"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, `skill "missing" not found`, result.Error)
}

func TestExecutorForAgentWithExtrasAutoAllowsSkillGet(t *testing.T) {
	cfg := config.DefaultConfig()
	agentCfg := &config.AgentConfig{
		Workspace: t.TempDir(),
	}

	executor := ExecutorForAgentWithExtras(cfg, agentCfg, nil, nil, nil, ExtraToolDeps{
		SkillProvider: mockSkillProvider{
			docs: map[string]SkillDocument{
				"rollout-playbook": {Name: "rollout-playbook", Content: "Verify staging before rollout."},
			},
		},
	})

	assert.Contains(t, executor.Names(), "skill_get")
}

func TestExecutorForAgentWithExtrasDenyOnlyDoesNotBecomeSkillAllowList(t *testing.T) {
	cfg := config.DefaultConfig()
	agentCfg := &config.AgentConfig{
		Workspace: t.TempDir(),
		Tools: config.ToolPolicy{
			Deny: []string{"browser"},
		},
	}

	executor := ExecutorForAgentWithExtras(cfg, agentCfg, nil, &mockAgentCallRunner{}, nil, ExtraToolDeps{
		SkillProvider: mockSkillProvider{
			docs: map[string]SkillDocument{
				"rollout-playbook": {Name: "rollout-playbook", Content: "Verify staging before rollout."},
			},
		},
	})

	names := executor.Names()
	assert.Contains(t, names, "skill_get")
	assert.Contains(t, names, "read_file")
	assert.Contains(t, names, "bash")
	assert.Contains(t, names, "callagent")
	assert.NotContains(t, names, "browser")
}

func TestExecutorForAgentWithExtrasDoesNotAutoAllowSkillGetWhenAllowListIsExplicit(t *testing.T) {
	cfg := config.DefaultConfig()
	agentCfg := &config.AgentConfig{
		Workspace: t.TempDir(),
		Tools: config.ToolPolicy{
			Allow: []string{"read_file"},
		},
	}

	executor := ExecutorForAgentWithExtras(cfg, agentCfg, nil, nil, nil, ExtraToolDeps{
		SkillProvider: mockSkillProvider{
			docs: map[string]SkillDocument{
				"rollout-playbook": {Name: "rollout-playbook", Content: "Verify staging before rollout."},
			},
		},
	})

	assert.NotContains(t, executor.Names(), "skill_get")
}

func TestExecutorForAgentWithExtrasRespectsSkillGetDeny(t *testing.T) {
	cfg := config.DefaultConfig()
	agentCfg := &config.AgentConfig{
		Workspace: t.TempDir(),
		Tools: config.ToolPolicy{
			Deny: []string{"skill_get"},
		},
	}

	executor := ExecutorForAgentWithExtras(cfg, agentCfg, nil, nil, nil, ExtraToolDeps{
		SkillProvider: mockSkillProvider{
			docs: map[string]SkillDocument{
				"rollout-playbook": {Name: "rollout-playbook", Content: "Verify staging before rollout."},
			},
		},
	})

	assert.NotContains(t, executor.Names(), "skill_get")
}
