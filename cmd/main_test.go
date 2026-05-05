package main

import (
	"testing"

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
	assert.Contains(t, names, "init")
	assert.Contains(t, names, "version")
	assert.NotContains(t, names, "run")

	chat, _, err := root.Find([]string{"chat"})
	assert.NoError(t, err)
	assert.Equal(t, "chat", chat.Name())

	start, _, err := root.Find([]string{"start"})
	assert.NoError(t, err)
	assert.Equal(t, "start", start.Name())
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
