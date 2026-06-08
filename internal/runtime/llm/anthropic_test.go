package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	tracker.append(0, `{"path":"out.md`)

	call, ok, err := tracker.stop(0)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Nil(t, call)
	assert.Contains(t, err.Error(), "malformed tool-call JSON")
	var malformedErr *anthropicMalformedToolCallJSONError
	require.True(t, errors.As(err, &malformedErr))
	assert.Equal(t, "write_file", malformedErr.ToolName)
	assert.Equal(t, int64(0), malformedErr.Index)
	assert.Equal(t, 1, malformedErr.DeltaCount)
	assert.Equal(t, len(`{"path":"out.md`), malformedErr.InputBytes)
	assert.NotEmpty(t, malformedErr.Prefix)
	assert.NotEmpty(t, malformedErr.Suffix)

	tracker = newAnthropicToolCallTracker()
	tracker.start(1, "call_2", "write_file")

	require.Error(t, tracker.errIfPending())
	assert.Contains(t, tracker.errIfPending().Error(), "write_file")
}

func TestAnthropicToolCallTrackerRepairsMissingClosers(t *testing.T) {
	tracker := newAnthropicToolCallTracker()
	tracker.start(0, "call_1", "python")
	tracker.append(0, `{"file":"script.py","args":["--text","hello"]`)

	call, ok, err := tracker.stop(0)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, call)
	assert.Equal(t, "python", call.Name)
	assert.JSONEq(t, `{"file":"script.py","args":["--text","hello"]}`, string(call.Input))
}

func TestRepairAnthropicToolCallJSONRejectsUnsafeTruncation(t *testing.T) {
	_, ok := repairAnthropicToolCallJSON(`{"script":"print('hello')`)
	assert.False(t, ok)

	_, ok = repairAnthropicToolCallJSON(`{"file":"script.py",`)
	assert.False(t, ok)

	repaired, ok := repairAnthropicToolCallJSON(`{"file":"script.py","args":["--x"]`)
	require.True(t, ok)
	assert.JSONEq(t, `{"file":"script.py","args":["--x"]}`, repaired)
}

func TestAnthropicProviderChatStreamGroupsConsecutiveToolResults(t *testing.T) {
	var body map[string]any
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/messages", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		writeAnthropicStreamFixture(t, w, "done")
	}))

	provider := NewAnthropicProvider("test-key", server.URL, nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model: "claude-sonnet-4-5",
		Messages: []Message{
			{
				Role:    MessageRoleAssistant,
				Content: "I'll inspect both files.",
				ToolCalls: []ToolCall{
					{ID: "tc_1", Name: "read_file", Input: json.RawMessage(`{"path":"a.md"}`)},
					{ID: "tc_2", Name: "read_file", Input: json.RawMessage(`{"path":"b.md"}`)},
				},
			},
			{Role: MessageRoleUser, ToolCallID: "tc_1", Content: "A"},
			{Role: MessageRoleUser, ToolCallID: "tc_2", Content: "B"},
		},
		MaxTokens: 64,
	})
	require.NoError(t, err)
	drainAnthropicStream(t, stream)

	messages, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)

	toolResultMessage, ok := messages[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user", toolResultMessage["role"])
	content, ok := toolResultMessage["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2)

	first, ok := content[0].(map[string]any)
	require.True(t, ok)
	second, ok := content[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool_result", first["type"])
	assert.Equal(t, "tc_1", first["tool_use_id"])
	assert.Equal(t, "tool_result", second["type"])
	assert.Equal(t, "tc_2", second["tool_use_id"])
}

func TestAnthropicProviderChatStreamSendsToolUseInputAsObject(t *testing.T) {
	var body map[string]any
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		require.NoError(t, err)
	}))

	provider := NewAnthropicProvider("test-key", server.URL, nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model: "claude-sonnet-4-5",
		Messages: []Message{{
			Role: MessageRoleAssistant,
			ToolCalls: []ToolCall{{
				ID:    "tc_1",
				Name:  "legacy_tool",
				Input: json.RawMessage(`"large durable preview"`),
			}},
		}},
		MaxTokens: 64,
	})
	require.NoError(t, err)
	drainAnthropicStream(t, stream)

	messages, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	message, ok := messages[0].(map[string]any)
	require.True(t, ok)
	content, ok := message["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	toolUse, ok := content[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool_use", toolUse["type"])
	input, ok := toolUse["input"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "large durable preview", input["value"])
}

func TestAnthropicProviderChatStreamEmitsTextFromContentBlockStart(t *testing.T) {
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/messages", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"start summary\"}}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		require.NoError(t, err)
	}))

	provider := NewAnthropicProvider("test-key", server.URL, nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model:     "compatible-model",
		Messages:  []Message{{Role: MessageRoleUser, Content: "summarize"}},
		MaxTokens: 64,
	})
	require.NoError(t, err)

	var text string
	for event := range stream {
		require.NotEqual(t, EventError, event.Type, "stream error: %v", event.Error)
		if event.Type == EventTextDelta {
			text += event.Text
		}
	}
	assert.Equal(t, "start summary", text)
}

func TestBuildAnthropicBetaMessagesGroupsConsecutiveToolResults(t *testing.T) {
	msgs, _ := buildAnthropicBetaMessages([]Message{
		{
			Role: MessageRoleAssistant,
			ToolCalls: []ToolCall{
				{ID: "tc_1", Name: "read_file", Input: json.RawMessage(`{"path":"a.md"}`)},
				{ID: "tc_2", Name: "read_file", Input: json.RawMessage(`"legacy"`)},
			},
		},
		{Role: MessageRoleUser, ToolCallID: "tc_1", Content: "A"},
		{Role: MessageRoleUser, ToolCallID: "tc_2", Content: "B"},
	})

	data, err := json.Marshal(msgs)
	require.NoError(t, err)
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded, 2)

	assistantContent, ok := decoded[0]["content"].([]any)
	require.True(t, ok)
	require.Len(t, assistantContent, 2)
	secondToolUse, ok := assistantContent[1].(map[string]any)
	require.True(t, ok)
	input, ok := secondToolUse["input"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "legacy", input["value"])

	userContent, ok := decoded[1]["content"].([]any)
	require.True(t, ok)
	require.Len(t, userContent, 2)
}

func drainAnthropicStream(t *testing.T, stream <-chan ChatEvent) {
	t.Helper()
	for event := range stream {
		require.NotEqual(t, EventError, event.Type, "stream error: %v", event.Error)
	}
}
