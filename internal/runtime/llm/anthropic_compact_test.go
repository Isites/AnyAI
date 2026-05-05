package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicProviderCompactUsesNativeCompactionWhenEligible(t *testing.T) {
	var body map[string]any
	var betaHeaders []string

	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, "true", r.URL.Query().Get("beta"))
		betaHeaders = append(betaHeaders, r.Header.Values("Anthropic-Beta")...)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{
			"id":"msg_1",
			"container":{},
			"type":"message",
			"role":"assistant",
			"model":"claude-sonnet-4-6",
			"content":[{"type":"compaction","content":"native summary"}],
			"context_management":{"applied_edits":[]},
			"stop_details":{},
			"stop_reason":"compaction",
			"stop_sequence":"",
			"usage":{
				"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},
				"cache_creation_input_tokens":0,
				"cache_read_input_tokens":0,
				"inference_geo":"us",
				"input_tokens":60000,
				"iterations":[],
				"output_tokens":12,
				"server_tool_use":{"web_fetch_requests":0,"web_search_requests":0},
				"service_tier":"standard",
				"speed":"standard"
			}
		}`)
		require.NoError(t, err)
	}))

	provider := NewAnthropicProvider("test-key", server.URL, nil)
	resp, err := provider.Compact(context.Background(), CompactRequest{
		Model:        "claude-sonnet-4-6",
		Messages:     longAnthropicCompactHistory(),
		MaxTokens:    512,
		Temperature:  0.01,
		SystemPrompt: "compact system",
		UserPrompt:   "compact user",
	})

	require.NoError(t, err)
	assert.Equal(t, "native summary", resp.Summary)
	assert.Equal(t, CompactStrategyNative, resp.Strategy)
	assert.Contains(t, betaHeaders, anthropicNativeCompactBetaHeader)

	caps := DescribeProviderCapabilities(provider)
	assert.Equal(t, "anthropic", caps.Provider)
	assert.Equal(t, CompactCapabilityNativeAvailable, caps.Compact)

	require.NotNil(t, body)
	assert.Equal(t, "claude-sonnet-4-6", body["model"])
	assert.Equal(t, float64(512), body["max_tokens"])

	contextManagement, ok := body["context_management"].(map[string]any)
	require.True(t, ok)
	edits, ok := contextManagement["edits"].([]any)
	require.True(t, ok)
	require.Len(t, edits, 1)

	edit, ok := edits[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "compact_20260112", edit["type"])
	assert.Equal(t, true, edit["pause_after_compaction"])
	assert.Equal(t, "compact user", edit["instructions"])

	trigger, ok := edit["trigger"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "input_tokens", trigger["type"])
	assert.Equal(t, float64(anthropicNativeCompactMinInputTokens), trigger["value"])

	systemBlocks, ok := body["system"].([]any)
	require.True(t, ok)
	require.Len(t, systemBlocks, 1)
	systemBlock, ok := systemBlocks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "compact system", systemBlock["text"])

	messages, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 3)

	assistantMessage, ok := messages[1].(map[string]any)
	require.True(t, ok)
	assistantBlocks, ok := assistantMessage["content"].([]any)
	require.True(t, ok)
	require.Len(t, assistantBlocks, 2)
	assistantToolBlock, ok := assistantBlocks[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool_use", assistantToolBlock["type"])
	assert.Equal(t, "read_file", assistantToolBlock["name"])

	toolResultMessage, ok := messages[2].(map[string]any)
	require.True(t, ok)
	toolResultBlocks, ok := toolResultMessage["content"].([]any)
	require.True(t, ok)
	require.Len(t, toolResultBlocks, 1)
	toolResultBlock, ok := toolResultBlocks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool_result", toolResultBlock["type"])
	assert.Equal(t, "tc_1", toolResultBlock["tool_use_id"])
}

func TestAnthropicProviderCompactFallsBackWhenNativeCompactionFails(t *testing.T) {
	var betaBody map[string]any
	var fallbackBody map[string]any
	requests := 0

	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		require.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/messages", r.URL.Path)

		switch requests {
		case 1:
			assert.Equal(t, "true", r.URL.Query().Get("beta"))
			require.NoError(t, json.NewDecoder(r.Body).Decode(&betaBody))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, err := io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"native compaction unsupported"}}`)
			require.NoError(t, err)
		case 2:
			assert.Equal(t, "", r.URL.Query().Get("beta"))
			require.NoError(t, json.NewDecoder(r.Body).Decode(&fallbackBody))
			writeAnthropicStreamFixture(t, w, "fallback summary")
		default:
			t.Fatalf("unexpected request #%d", requests)
		}
	}))

	provider := NewAnthropicProvider("test-key", server.URL, nil)
	resp, err := provider.Compact(context.Background(), CompactRequest{
		Model:        "claude-sonnet-4-6",
		Messages:     longAnthropicCompactHistory(),
		MaxTokens:    256,
		SystemPrompt: "compact system",
		UserPrompt:   "compact user",
	})

	require.NoError(t, err)
	assert.Equal(t, "fallback summary", resp.Summary)
	assert.Equal(t, CompactStrategyChatFallback, resp.Strategy)
	assert.Equal(t, 2, requests)
	require.NotNil(t, betaBody)
	require.NotNil(t, fallbackBody)

	streamFlag, ok := fallbackBody["stream"].(bool)
	require.True(t, ok)
	assert.True(t, streamFlag)

	messages, ok := fallbackBody["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 4)

	lastMessage, ok := messages[len(messages)-1].(map[string]any)
	require.True(t, ok)
	content, ok := lastMessage["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	textBlock, ok := content[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "text", textBlock["type"])
	assert.Equal(t, "compact user", textBlock["text"])
}

func longAnthropicCompactHistory() []Message {
	return []Message{
		{
			Role:    "user",
			Content: "session log:\n" + strings.Repeat("Alpha Beta Gamma Delta ", 12000),
		},
		{
			Role:    "assistant",
			Content: "I will inspect the repository state.",
			ToolCalls: []ToolCall{{
				ID:    "tc_1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			}},
		},
		{
			Role:       "user",
			ToolCallID: "tc_1",
			Content:    "README excerpt",
		},
	}
}

func writeAnthropicStreamFixture(t *testing.T, w http.ResponseWriter, summary string) {
	t.Helper()

	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []struct {
		name string
		data string
	}{
		{name: "message_start", data: `{"type":"message_start","message":{}}`},
		{name: "content_block_start", data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{name: "content_block_delta", data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + summary + `"}}`},
		{name: "content_block_stop", data: `{"type":"content_block_stop","index":0}`},
		{name: "message_delta", data: `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`},
		{name: "message_stop", data: `{"type":"message_stop"}`},
	} {
		_, err := io.WriteString(w, "event: "+event.name+"\n")
		require.NoError(t, err)
		_, err = io.WriteString(w, "data: "+event.data+"\n\n")
		require.NoError(t, err)
	}
}
