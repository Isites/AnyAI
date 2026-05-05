package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type metadataToolStub struct {
	name string
	meta ToolMetadata
}

func (t metadataToolStub) Name() string        { return t.name }
func (t metadataToolStub) Description() string { return "metadata stub" }
func (t metadataToolStub) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t metadataToolStub) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}
func (t metadataToolStub) ToolMetadata() ToolMetadata { return t.meta }

func TestDefaultToolMetadataIsGenericFallback(t *testing.T) {
	meta := DefaultToolMetadata("read_file")

	assert.Equal(t, ToolEffectMutating, meta.Effect)
	assert.Nil(t, meta.Tags)
	assert.False(t, meta.AllowParallel)
	assert.False(t, meta.AllowsParallelFanout())
	assert.False(t, meta.IsReadOnly())
}

func TestDescribeToolMetadataUsesConcreteToolContract(t *testing.T) {
	meta := DescribeToolMetadata(&ReadFileTool{})

	assert.Equal(t, ToolEffectReadOnly, meta.Effect)
	assert.Equal(t, defaultToolTimeoutMS, meta.TimeoutHintMS)
	assert.True(t, meta.AllowParallel)
	assert.True(t, meta.AllowsParallelFanout())
	assert.True(t, meta.IsReadOnly())
}

func TestDescribeToolMetadataUsesWorkspaceWriteContract(t *testing.T) {
	meta := DescribeToolMetadata(&WriteFileTool{})

	assert.Equal(t, ToolEffectMutating, meta.Effect)
	assert.Equal(t, []string{"workspace_write"}, meta.Tags)
	assert.Equal(t, defaultToolTimeoutMS, meta.TimeoutHintMS)
	assert.False(t, meta.AllowParallel)
	assert.False(t, meta.AllowsParallelFanout())
	assert.False(t, meta.IsReadOnly())
}

func TestDescribeToolMetadataHonorsToolOverride(t *testing.T) {
	meta := DescribeToolMetadata(metadataToolStub{
		name: "custom_tool",
		meta: ToolMetadata{
			AllowParallel: true,
			Effect:        ToolEffectMutating,
			Tags:          []string{"external_io"},
			TimeoutHintMS: 777,
		},
	})

	assert.Equal(t, "custom_tool", meta.Name)
	assert.True(t, meta.AllowParallel)
	assert.Equal(t, ToolEffectMutating, meta.Effect)
	assert.Equal(t, []string{"external_io"}, meta.Tags)
	assert.Equal(t, int64(777), meta.TimeoutHintMS)
}

func TestBashToolTimeoutHintForInputUsesRequestedTimeout(t *testing.T) {
	tool := &BashTool{}
	timeout, ok := tool.TimeoutHintForInput(json.RawMessage(`{"command":"printf hello","timeout":45}`), 0)
	require.True(t, ok)
	assert.Equal(t, 45*time.Second, timeout)
}

func TestPythonToolTimeoutHintForInputUsesRequestedTimeout(t *testing.T) {
	tool := &PythonTool{}
	timeout, ok := tool.TimeoutHintForInput(json.RawMessage(`{"script":"print('ok')","timeout":12}`), 0)
	require.True(t, ok)
	assert.Equal(t, 12*time.Second, timeout)
}

func TestBrowserToolTimeoutHintForInputUsesRequestedTimeout(t *testing.T) {
	tool := &BrowserTool{}
	timeout, ok := tool.TimeoutHintForInput(json.RawMessage(`{"action":"navigate","url":"https://example.com","timeout":25}`), 0)
	require.True(t, ok)
	assert.Equal(t, 25*time.Second, timeout)
}
