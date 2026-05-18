package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.md")

	content := `---
id: support
name: Customer Support
description: Handle support tickets
entry: true
model: anthropic/claude-sonnet-4-5
max_turns: 12
tools:
  allow:
    - read_file
  deny:
    - bash
skills:
  inherit_shared: false
mcps:
  inherit_shared: false
tags:
  - support
---

You are the support agent.
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	doc, err := ParseFile(path)
	require.NoError(t, err)

	require.NotNil(t, doc)
	assert.Equal(t, "support", doc.ID)
	assert.Equal(t, "Customer Support", doc.Name)
	assert.Equal(t, "Handle support tickets", doc.Description)
	assert.True(t, doc.Entry)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", doc.Model)
	assert.Equal(t, 12, doc.MaxTurns)
	assert.Equal(t, []string{"read_file"}, doc.Tools.Allow)
	assert.Equal(t, []string{"bash"}, doc.Tools.Deny)
	require.NotNil(t, doc.Skills.InheritShared)
	assert.False(t, *doc.Skills.InheritShared)
	require.NotNil(t, doc.MCPs.InheritShared)
	assert.False(t, *doc.MCPs.InheritShared)
	assert.Equal(t, []string{"support"}, doc.Tags)
	assert.Equal(t, "You are the support agent.", doc.Body)
	assert.Equal(t, dir, doc.Dir)
}

func TestParseFileWithoutFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.md")
	require.NoError(t, os.WriteFile(path, []byte("You are a direct prompt."), 0o644))

	doc, err := ParseFile(path)
	require.NoError(t, err)

	assert.Equal(t, "You are a direct prompt.", doc.Body)
	assert.Empty(t, doc.ID)
	assert.Nil(t, doc.Skills.InheritShared)
	assert.Nil(t, doc.MCPs.InheritShared)
}
