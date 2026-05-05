package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Isites/anyai/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFM   string
		wantBody string
	}{
		{
			name:     "with frontmatter",
			input:    "---\nname: test\n---\n# Body\nContent here",
			wantFM:   "name: test",
			wantBody: "# Body\nContent here",
		},
		{
			name:     "no frontmatter",
			input:    "# Just a heading\nSome content",
			wantFM:   "",
			wantBody: "# Just a heading\nSome content",
		},
		{
			name:     "empty",
			input:    "",
			wantFM:   "",
			wantBody: "",
		},
		{
			name:     "only frontmatter",
			input:    "---\nname: test\n---\n",
			wantFM:   "name: test",
			wantBody: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body := splitFrontmatter(tt.input)
			assert.Equal(t, tt.wantFM, fm)
			assert.Equal(t, tt.wantBody, body)
		})
	}
}

func TestParseSkillFile(t *testing.T) {
	dir := t.TempDir()

	// Create a skill directory with SKILL.md
	skillDir := filepath.Join(dir, "web-search")
	os.MkdirAll(skillDir, 0o755)

	content := `---
name: web-search
description: Search the web for current information
tags:
  - search
  - web
  - internet
---

# Web Search Skill

When the user asks about current events, news, or information that may have
changed since your training cutoff, use the web_search tool.

## Usage Guidelines
- Keep queries concise (3-6 words)
- Verify claims from multiple sources
`

	skillPath := filepath.Join(skillDir, "SKILL.md")
	os.WriteFile(skillPath, []byte(content), 0o644)

	skill, err := parseSkillFile(skillPath)
	require.NoError(t, err)

	assert.Equal(t, "web-search", skill.Name)
	assert.Equal(t, "Search the web for current information", skill.Description)
	assert.Equal(t, []string{"search", "web", "internet"}, skill.Tags)
	assert.Contains(t, skill.Body, "Web Search Skill")
	assert.Contains(t, skill.Body, "Usage Guidelines")
}

func TestLoaderLoadFrom(t *testing.T) {
	dir := t.TempDir()

	// Create two skills
	for _, name := range []string{"skill-a", "skill-b"} {
		skillDir := filepath.Join(dir, name)
		os.MkdirAll(skillDir, 0o755)
		content := "---\nname: " + name + "\ndescription: Test skill " + name + "\n---\n\nBody of " + name
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644)
	}

	loader := NewLoader()
	err := loader.LoadFrom(dir)
	require.NoError(t, err)

	skills := loader.Skills()
	assert.Len(t, skills, 2)
}

func TestLoaderLoadFromDirectMD(t *testing.T) {
	dir := t.TempDir()

	// Create a skill as a direct .md file (not in a subdirectory)
	content := `---
name: gog
description: Google Workspace CLI for Gmail, Calendar, Drive
tags:
  - gmail
  - calendar
  - google
---

# gog

Use gog for Gmail/Calendar/Drive.
`
	os.WriteFile(filepath.Join(dir, "gog.md"), []byte(content), 0o644)

	// Also create a SKILL.md in a subdirectory (should still work)
	skillDir := filepath.Join(dir, "web-search")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: web-search\n---\nBody"), 0o644)

	loader := NewLoader()
	err := loader.LoadFrom(dir)
	require.NoError(t, err)

	skills := loader.Skills()
	assert.Len(t, skills, 2)

	// Find the direct .md skill
	var gogSkill *Skill
	for i := range skills {
		if skills[i].Name == "gog" {
			gogSkill = &skills[i]
			break
		}
	}
	require.NotNil(t, gogSkill)
	assert.Equal(t, "Google Workspace CLI for Gmail, Calendar, Drive", gogSkill.Description)
	assert.Contains(t, gogSkill.Tags, "gmail")
}

func TestLoaderLoadFromDirectMDDefaultName(t *testing.T) {
	dir := t.TempDir()

	// Skill file without a name in frontmatter — should use filename stem
	content := "---\ndescription: A test skill\n---\n\nBody here"
	os.WriteFile(filepath.Join(dir, "my-skill.md"), []byte(content), 0o644)

	loader := NewLoader()
	err := loader.LoadFrom(dir)
	require.NoError(t, err)

	skills := loader.Skills()
	require.Len(t, skills, 1)
	assert.Equal(t, "my-skill", skills[0].Name)
}

func TestLoaderSkipsMissingBinary(t *testing.T) {
	dir := t.TempDir()

	// Skill that requires a binary that doesn't exist
	content := `---
name: fake-tool
description: Requires a nonexistent binary
metadata:
  openclaw:
    requires:
      bins: ["this-binary-does-not-exist-xyz"]
---

Body here
`
	os.WriteFile(filepath.Join(dir, "fake-tool.md"), []byte(content), 0o644)

	// Skill with no binary requirement (should load fine)
	os.WriteFile(filepath.Join(dir, "simple.md"), []byte("---\nname: simple\n---\nBody"), 0o644)

	loader := NewLoader()
	err := loader.LoadFrom(dir)
	require.NoError(t, err)

	skills := loader.Skills()
	assert.Len(t, skills, 1)
	assert.Equal(t, "simple", skills[0].Name)
}

func TestLoaderLoadFromNonexistent(t *testing.T) {
	loader := NewLoader()
	err := loader.LoadFrom("/nonexistent/path")
	require.NoError(t, err) // should not error, just skip
	assert.Empty(t, loader.Skills())
}

func TestMatchSkills(t *testing.T) {
	loader := NewLoader()

	// Manually set skills for testing
	loader.skills = []Skill{
		{Name: "web-search", Description: "Search the web for current information", Tags: []string{"search", "web"}},
		{Name: "calendar", Description: "Manage calendar events and appointments", Tags: []string{"calendar", "schedule"}},
		{Name: "code-review", Description: "Review code for bugs and improvements", Tags: []string{"code", "review"}},
	}

	// Search-related query should match web-search
	matches := loader.MatchSkills("search the web for latest news", 3)
	assert.NotEmpty(t, matches)
	assert.Equal(t, "web-search", matches[0].Name)

	// Calendar-related query
	matches = loader.MatchSkills("what's on my calendar today?", 3)
	assert.NotEmpty(t, matches)
	assert.Equal(t, "calendar", matches[0].Name)

	// Unrelated query should return nothing
	matches = loader.MatchSkills("hello there", 3)
	assert.Empty(t, matches)
}

func TestMatchSkillsIgnoresPromptBoilerplate(t *testing.T) {
	loader := NewLoader()
	loader.skills = []Skill{
		{Name: "web-search", Description: "Search the web for current information", Tags: []string{"search", "web"}},
		{Name: "calendar", Description: "Manage calendar events and appointments", Tags: []string{"calendar", "schedule"}},
	}

	query := "Current request: search the web for latest news. Pending context summary: none. Session summary context: previous small talk."
	matches := loader.MatchSkills(query, 2)
	require.NotEmpty(t, matches)
	assert.Equal(t, "web-search", matches[0].Name)
}

func TestMatchSkillsPrefersExactTagPhrase(t *testing.T) {
	loader := NewLoader()
	loader.skills = []Skill{
		{Name: "release-checklist", Description: "Release readiness checklist", Tags: []string{"release", "staging"}},
		{Name: "general-notes", Description: "General project notes", Tags: []string{"project", "notes"}},
	}

	matches := loader.MatchSkills("Need the release staging checklist before publishing", 2)
	require.NotEmpty(t, matches)
	assert.Equal(t, "release-checklist", matches[0].Name)
}

func TestMatchSkillsCanUseBodyKeywords(t *testing.T) {
	loader := NewLoaderFromSkills([]Skill{
		{Name: "metrics-dictionary", Description: "H5 metrics glossary", Body: "CAC ARPU ROI Meta channel strategy"},
		{Name: "general-notes", Description: "General notes", Body: "miscellaneous"},
	})

	matches := loader.MatchSkills("Need CAC and ARPU guidance for a Meta channel plan", 2)
	require.NotEmpty(t, matches)
	assert.Equal(t, "metrics-dictionary", matches[0].Name)
}

func TestFormatForPrompt(t *testing.T) {
	skills := []Skill{
		{Name: "test-skill", Description: "Short summary", Body: "This is the body."},
	}

	result := FormatForPrompt(skills)
	assert.Contains(t, result, "## Relevant Skills")
	assert.Contains(t, result, "### test-skill")
	assert.Contains(t, result, "- Summary: Short summary")
	assert.NotContains(t, result, "This is the body.")
	assert.NotContains(t, result, "- Tags:")
}

func TestFormatForPromptEmpty(t *testing.T) {
	result := FormatForPrompt(nil)
	assert.Equal(t, "", result)
}

func TestFormatForPromptUsesBodyAsSummaryFallback(t *testing.T) {
	skills := []Skill{
		{Name: "test-skill", Body: "Fallback body summary"},
	}

	result := FormatForPrompt(skills)
	assert.Contains(t, result, "- Summary: Fallback body summary")
}

func TestFormatForPromptSummarizesLongSummary(t *testing.T) {
	body := strings.Repeat("important rollout detail ", 30) + "TAIL_MARKER"
	skills := []Skill{
		{Name: "rollout", Description: body},
	}

	result := FormatForPromptWithOptions(skills, PromptOptions{SummaryMaxLen: 80})
	assert.Contains(t, result, "### rollout")
	assert.Contains(t, result, "...")
	assert.NotContains(t, result, "TAIL_MARKER")
}

func TestLoaderGetSkillByNormalizedName(t *testing.T) {
	loader := NewLoaderFromSkills([]Skill{
		{Name: "release-checklist", Description: "Release checklist"},
	})

	item, ok := loader.Get("Release Checklist")
	require.True(t, ok)
	assert.Equal(t, "release-checklist", item.Name)
}

func TestLoaderForAgentScopesAndOverrides(t *testing.T) {
	systemDir := t.TempDir()
	sharedDir := t.TempDir()
	privateDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(systemDir, "shared.md"), []byte("---\nname: common\n---\nsystem"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "shared.md"), []byte("---\nname: common\n---\nshared"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(privateDir, "private.md"), []byte("---\nname: common\n---\nprivate"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(privateDir, "unique.md"), []byte("---\nname: unique\n---\nonly private"), 0o644))

	cfg := &config.Config{
		SystemSkillsDir: systemDir,
		SharedSkillsDir: sharedDir,
	}
	agentCfg := &config.AgentConfig{
		Workspace:           t.TempDir(),
		PrivateSkillsDir:    privateDir,
		InheritSharedSkills: true,
	}

	loader, err := LoaderForAgent(cfg, agentCfg)
	require.NoError(t, err)

	skills := loader.Skills()
	require.Len(t, skills, 2)
	assert.Equal(t, "common", skills[0].Name)
	assert.Empty(t, skills[0].Body)
	assert.Equal(t, "unique", skills[1].Name)
	assert.Empty(t, skills[1].Body)

	common, ok := loader.Get("common")
	require.True(t, ok)
	assert.Equal(t, "private", common.Body)
}

func TestLoaderForAgentWithoutSystemDirUsesSharedAndPrivateOnly(t *testing.T) {
	sharedDir := t.TempDir()
	privateDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "shared.md"), []byte("---\nname: shared-only\n---\nshared"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(privateDir, "private.md"), []byte("---\nname: private-only\n---\nprivate"), 0o644))

	cfg := &config.Config{
		SharedSkillsDir: sharedDir,
	}
	agentCfg := &config.AgentConfig{
		Workspace:           t.TempDir(),
		PrivateSkillsDir:    privateDir,
		InheritSharedSkills: true,
	}

	loader, err := LoaderForAgent(cfg, agentCfg)
	require.NoError(t, err)

	skills := loader.Skills()
	require.Len(t, skills, 2)
	assert.Equal(t, "shared-only", skills[0].Name)
	assert.Equal(t, "private-only", skills[1].Name)
}

func TestLoaderForAgentRefreshesDiskChangesPerCall(t *testing.T) {
	sharedDir := t.TempDir()
	privateDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "shared.md"), []byte("---\nname: shared\n---\nshared body"), 0o644))

	cfg := &config.Config{
		SharedSkillsDir: sharedDir,
	}
	agentCfg := &config.AgentConfig{
		Workspace:           t.TempDir(),
		PrivateSkillsDir:    privateDir,
		InheritSharedSkills: true,
	}

	loader, err := LoaderForAgent(cfg, agentCfg)
	require.NoError(t, err)
	require.Len(t, loader.Skills(), 1)

	require.NoError(t, os.WriteFile(filepath.Join(privateDir, "private.md"), []byte("---\nname: private\n---\nprivate body"), 0o644))

	loader, err = LoaderForAgent(cfg, agentCfg)
	require.NoError(t, err)

	skills := loader.Skills()
	require.Len(t, skills, 2)
	assert.Equal(t, "shared", skills[0].Name)
	assert.Equal(t, "private", skills[1].Name)
	assert.Empty(t, skills[1].Body)

	privateSkill, ok := loader.Get("private")
	require.True(t, ok)
	assert.Equal(t, "private body", privateSkill.Body)
}

func TestLoadDirRefreshesNestedSkillFileChanges(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested-skill")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	skillPath := filepath.Join(nested, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: nested\n---\nfirst body"), 0o644))

	skills, err := LoadDir(root)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	loader := NewLoaderFromSkills(skills)
	item, ok := loader.Get("nested")
	require.True(t, ok)
	assert.Equal(t, "first body", item.Body)

	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: nested\n---\nsecond body"), 0o644))

	skills, err = LoadDir(root)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	loader = NewLoaderFromSkills(skills)
	item, ok = loader.Get("nested")
	require.True(t, ok)
	assert.Equal(t, "second body", item.Body)
}
