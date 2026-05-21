package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicToolCallTrackerCompletesByContentBlockIndex(t *testing.T) {
	tracker := newAnthropicToolCallTracker()
	tracker.start(1, "call_a", "read_file")
	tracker.start(2, "call_b", "write_file")
	tracker.append(2, `{"path":"out.md"`)
	tracker.append(1, `{"path":"README.md"}`)
	tracker.append(2, `,"content":"done"}`)

	first, ok, err := tracker.stop(1)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, first)
	assert.Equal(t, "read_file", first.Name)
	assert.JSONEq(t, `{"path":"README.md"}`, string(first.Input))

	second, ok, err := tracker.stop(2)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, second)
	assert.Equal(t, "write_file", second.Name)
	assert.JSONEq(t, `{"path":"out.md","content":"done"}`, string(second.Input))
	assert.NoError(t, tracker.errIfPending())
}

func TestAnthropicToolCallTrackerDefaultsEmptyInputToObject(t *testing.T) {
	tracker := newAnthropicToolCallTracker()
	tracker.start(0, "call_1", "goal_complete")

	call, ok, err := tracker.stop(0)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, call)
	assert.JSONEq(t, `{}`, string(call.Input))
}

func TestAnthropicToolCallTrackerAcceptsInitialInputObject(t *testing.T) {
	tracker := newAnthropicToolCallTracker()
	tracker.start(0, "call_1", "write_file", map[string]any{
		"path":    "out.md",
		"content": "done",
	})

	call, ok, err := tracker.stop(0)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, call)
	assert.JSONEq(t, `{"path":"out.md","content":"done"}`, string(call.Input))
}

func TestAnthropicToolCallTrackerErrorsWhenInputOrBlockIsIncomplete(t *testing.T) {
	tracker := newAnthropicToolCallTracker()
	tracker.start(0, "call_1", "write_file")
	tracker.append(0, `{"path":"out.md"`)

	call, ok, err := tracker.stop(0)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Nil(t, call)
	assert.Contains(t, err.Error(), "malformed tool-call JSON")

	tracker = newAnthropicToolCallTracker()
	tracker.start(1, "call_2", "write_file")

	require.Error(t, tracker.errIfPending())
	assert.Contains(t, tracker.errIfPending().Error(), "write_file")
}
