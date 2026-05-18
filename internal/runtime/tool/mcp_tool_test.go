package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Isites/anyai/internal/runtime/llm"
	runtimemcp "github.com/Isites/anyai/internal/runtime/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterMCPToolsExecutesViaManager(t *testing.T) {
	manager := &fakeMCPManager{
		result: runtimemcp.CallResult{
			Output: "ok",
			Metadata: map[string]any{
				"server": "filesystem",
			},
		},
	}
	reg := NewRegistry()
	RegisterMCPTools(reg, manager, []runtimemcp.ToolDescriptor{
		{
			Name:        "mcp__filesystem__read_file",
			ServerName:  "filesystem",
			RemoteName:  "read_file",
			Description: "Read a file",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			ReadOnly:    true,
		},
	})

	tool, ok := reg.Get("mcp__filesystem__read_file")
	require.True(t, ok)
	assert.Equal(t, "Read a file", tool.Description())
	require.JSONEq(t, `{"type":"object","properties":{"path":{"type":"string"}}}`, string(tool.Parameters()))

	result, err := reg.Execute(t.Context(), "mcp__filesystem__read_file", json.RawMessage(`{"path":"README.md"}`))
	require.NoError(t, err)
	assert.Equal(t, "ok", result.Output)
	assert.Equal(t, "filesystem", manager.serverName)
	assert.Equal(t, "read_file", manager.remoteName)
	require.JSONEq(t, `{"path":"README.md"}`, string(manager.input))

	meta := DescribeToolMetadata(tool)
	assert.True(t, meta.IsReadOnly())
	assert.True(t, meta.AllowsParallelFanout())
	assert.Contains(t, meta.Tags, "mcp")
}

type fakeMCPManager struct {
	result     runtimemcp.CallResult
	serverName string
	remoteName string
	input      json.RawMessage
}

func (m *fakeMCPManager) ListTools(context.Context) ([]runtimemcp.ToolDescriptor, error) {
	return nil, nil
}

func (m *fakeMCPManager) CallTool(_ context.Context, serverName, remoteName string, input json.RawMessage) (runtimemcp.CallResult, error) {
	m.serverName = serverName
	m.remoteName = remoteName
	m.input = append(json.RawMessage(nil), input...)
	m.result.Images = []llm.ImageContent{{Name: "ignored"}}
	return m.result, nil
}

func (m *fakeMCPManager) Close() error {
	return nil
}
