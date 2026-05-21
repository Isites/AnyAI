package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureOpenAICompatibleDiagnostics(t *testing.T) *[]openAICompatibleStreamDiagnostic {
	t.Helper()

	diags := make([]openAICompatibleStreamDiagnostic, 0, 4)
	previous := emitOpenAICompatibleStreamDiagnostic
	emitOpenAICompatibleStreamDiagnostic = func(diag openAICompatibleStreamDiagnostic) {
		diags = append(diags, diag)
	}
	t.Cleanup(func() {
		emitOpenAICompatibleStreamDiagnostic = previous
	})
	return &diags
}

func captureOpenAICompatibleRawDiagnostics(t *testing.T) *[]openAICompatibleRawStreamDiagnostic {
	t.Helper()

	diags := make([]openAICompatibleRawStreamDiagnostic, 0, 4)
	previous := emitOpenAICompatibleRawStreamDiagnostic
	emitOpenAICompatibleRawStreamDiagnostic = func(diag openAICompatibleRawStreamDiagnostic) {
		diags = append(diags, diag)
	}
	t.Cleanup(func() {
		emitOpenAICompatibleRawStreamDiagnostic = previous
	})
	return &diags
}

func TestOpenAIProviderChatStreamUsesMaxCompletionTokensForReasoningModels(t *testing.T) {
	body := captureOpenAIChatRequest(t, "gpt-5.4")

	_, hasMaxTokens := body["max_tokens"]
	assert.False(t, hasMaxTokens)
	assert.Equal(t, float64(1234), body["max_completion_tokens"])
}

func TestOpenAIProviderChatStreamUsesMaxTokensForNonReasoningModels(t *testing.T) {
	body := captureOpenAIChatRequest(t, "gpt-4o")

	assert.Equal(t, float64(1234), body["max_tokens"])
	_, hasMaxCompletionTokens := body["max_completion_tokens"]
	assert.False(t, hasMaxCompletionTokens)
}

func TestOpenAIProviderChatStreamDisablesThinkingForQwen3(t *testing.T) {
	body := captureOpenAIChatRequest(t, "qwen3:1.7b")

	kwargs, ok := body["chat_template_kwargs"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, kwargs["enable_thinking"])
}

func TestOpenAIProviderChatStreamUsesNonStreamingCompletionForQwen3(t *testing.T) {
	var body map[string]any
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"qwen3:1.7b","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		require.NoError(t, err)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL+"/v1", nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model:    "qwen3:1.7b",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var text string
	var done bool
	for event := range stream {
		require.NotEqual(t, EventError, event.Type, event.Error)
		if event.Type == EventTextDelta {
			text += event.Text
		}
		if event.Type == EventDone {
			done = true
		}
	}

	require.True(t, done)
	assert.Equal(t, "你好", text)
	assert.NotNil(t, body)
	if streamFlag, ok := body["stream"].(bool); ok {
		assert.False(t, streamFlag)
	}
}

func TestOpenAIProviderChatStreamUsesNonStreamingCompletionForQwen3WithTools(t *testing.T) {
	body := captureOpenAIChatRequestWithTools(t, "qwen3:1.7b", []ToolDef{{
		Name:        "callagent",
		Description: "delegate",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}})

	if streamFlag, ok := body["stream"].(bool); ok {
		assert.False(t, streamFlag)
	}
}

func TestNewOpenAIProviderNormalizesBareBaseURLToV1(t *testing.T) {
	var requestPath string
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(w, "data: {\"id\":\"2\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(w, "data: [DONE]\n\n")
		require.NoError(t, err)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model:     "gpt-5.4",
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 1234,
	})
	require.NoError(t, err)

	for event := range stream {
		require.NotEqual(t, EventError, event.Type, event.Error)
	}

	assert.Equal(t, "/v1/chat/completions", requestPath)
}

func TestRecoverOpenAICompatibleStreamRecoversCompletedToolCall(t *testing.T) {
	completed, recovered, err, diag := recoverOpenAICompatibleStream(io.ErrUnexpectedEOF, map[int]*streamedToolCall{
		0: {
			id:       "call_1",
			name:     "callagent",
			argsJSON: `{"target_agent":"coder","task":"read spec"}`,
		},
	}, openAICompatibleStreamProgress{
		decodedChunks:     1,
		toolArgumentBytes: len(`{"target_agent":"coder","task":"read spec"}`),
	})

	require.True(t, recovered)
	require.NoError(t, err)
	require.NotNil(t, diag)
	require.Len(t, completed, 1)
	assert.Equal(t, "callagent", completed[0].Name)
	assert.JSONEq(t, `{"target_agent":"coder","task":"read spec"}`, string(completed[0].Input))
	assert.Equal(t, openAICompatibleTruncationStageToolCallArguments, diag.Stage)
	assert.Equal(t, "tool_calls", diag.RecoveryMode)
}

func TestRecoverOpenAICompatibleStreamClassifiesFirstPacketTruncation(t *testing.T) {
	_, recovered, err, diag := recoverOpenAICompatibleStream(io.ErrUnexpectedEOF, nil, openAICompatibleStreamProgress{})

	require.False(t, recovered)
	require.Error(t, err)
	require.NotNil(t, diag)
	assert.Equal(t, openAICompatibleTruncationStageFirstPacket, diag.Stage)
	assert.Equal(t, "none", diag.RecoveryMode)
	assert.Contains(t, err.Error(), "before the first chunk could be decoded")
}

func TestRecoverOpenAICompatibleStreamClassifiesToolCallArgumentFailure(t *testing.T) {
	_, recovered, err, diag := recoverOpenAICompatibleStream(io.ErrUnexpectedEOF, map[int]*streamedToolCall{
		0: {
			id:       "call_1",
			name:     "callagent",
			argsJSON: `{"target_agent":"coder"`,
		},
	}, openAICompatibleStreamProgress{
		decodedChunks:     1,
		toolArgumentBytes: len(`{"target_agent":"coder"`),
	})

	require.False(t, recovered)
	require.Error(t, err)
	require.NotNil(t, diag)
	assert.Equal(t, openAICompatibleTruncationStageToolCallArguments, diag.Stage)
	assert.Contains(t, err.Error(), "during tool-call arguments for callagent")
}

func TestFinalizeOpenAICompatibleToolCallsRejectsNamelessPendingCall(t *testing.T) {
	_, err := finalizeOpenAICompatibleToolCalls(map[int]*streamedToolCall{
		0: {
			id: "call_1",
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a function name")
}

func TestOpenAIProviderRecoversTextWhenStreamJSONIsTruncatedAfterContent(t *testing.T) {
	diags := captureOpenAICompatibleDiagnostics(t)
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		require.NoError(t, err)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, err = io.WriteString(w, "data: {\"id\":\"2\",\"object\":\"chat.completion.chunk\"\n\n")
		require.NoError(t, err)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL+"/v1", nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-5.4",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var text string
	var done bool
	for event := range stream {
		require.NotEqual(t, EventError, event.Type, event.Error)
		if event.Type == EventTextDelta {
			text += event.Text
		}
		if event.Type == EventDone {
			done = true
		}
	}

	require.True(t, done)
	assert.Equal(t, "hello", text)
	require.Len(t, *diags, 1)
	assert.Equal(t, openAICompatibleTruncationStageMidStream, (*diags)[0].Stage)
	assert.True(t, (*diags)[0].Recovered)
	assert.Equal(t, "text", (*diags)[0].RecoveryMode)
}

func TestOpenAIProviderFailsWhenFirstPacketIsTruncated(t *testing.T) {
	diags := captureOpenAICompatibleDiagnostics(t)
	rawDiags := captureOpenAICompatibleRawDiagnostics(t)
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\"\n\n")
		require.NoError(t, err)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL+"/v1", nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-5.4",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var streamErr error
	for event := range stream {
		if event.Type == EventError {
			streamErr = event.Error
		}
	}

	require.Error(t, streamErr)
	assert.Contains(t, streamErr.Error(), "before the first chunk could be decoded")
	require.Len(t, *diags, 1)
	assert.Equal(t, openAICompatibleTruncationStageFirstPacket, (*diags)[0].Stage)
	assert.False(t, (*diags)[0].Recovered)
	require.Len(t, *rawDiags, 1)
	assert.Equal(t, "first_data_frame", (*rawDiags)[0].StageHint)
	assert.False(t, (*rawDiags)[0].SawDone)
}

func TestOpenAIProviderFailsWhenToolCallArgumentsAreTruncated(t *testing.T) {
	diags := captureOpenAICompatibleDiagnostics(t)
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"callagent\",\"arguments\":\"{\\\"target_agent\\\":\\\"coder\\\"\"}}]},\"finish_reason\":null}]}\n\n")
		require.NoError(t, err)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, err = io.WriteString(w, "data: {\"id\":\"2\",\"object\":\"chat.completion.chunk\"\n\n")
		require.NoError(t, err)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL+"/v1", nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-5.4",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var streamErr error
	for event := range stream {
		if event.Type == EventError {
			streamErr = event.Error
		}
	}

	require.Error(t, streamErr)
	assert.Contains(t, streamErr.Error(), "during tool-call arguments for callagent")
	require.Len(t, *diags, 1)
	assert.Equal(t, openAICompatibleTruncationStageToolCallArguments, (*diags)[0].Stage)
	assert.False(t, (*diags)[0].Recovered)
}

func TestOpenAIProviderEmitsToolCallStartOnlyOnceWhenNameRepeats(t *testing.T) {
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"callagent\",\"arguments\":\"{\\\"target_agent\\\":\"}}]},\"finish_reason\":null}]}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(w, "data: {\"id\":\"2\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"callagent\",\"arguments\":\"\\\"coder\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(w, "data: [DONE]\n\n")
		require.NoError(t, err)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL+"/v1", nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-5.4",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var starts int
	var completed int
	for event := range stream {
		require.NotEqual(t, EventError, event.Type, event.Error)
		switch event.Type {
		case EventToolCallStart:
			starts++
		case EventToolCallDone:
			completed++
		}
	}

	assert.Equal(t, 1, starts)
	assert.Equal(t, 1, completed)
}

func captureOpenAIChatRequest(t *testing.T, model string) map[string]any {
	t.Helper()
	return captureOpenAIChatRequestWithTools(t, model, nil)
}

func captureOpenAIChatRequestWithTools(t *testing.T, model string, tools []ToolDef) map[string]any {
	t.Helper()

	var body map[string]any
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		streamEnabled, _ := body["stream"].(bool)
		if streamEnabled {
			w.Header().Set("Content-Type", "text/event-stream")
			_, err := io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\""+model+"\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
			require.NoError(t, err)
			_, err = io.WriteString(w, "data: {\"id\":\"2\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\""+model+"\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			require.NoError(t, err)
			_, err = io.WriteString(w, "data: [DONE]\n\n")
			require.NoError(t, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, "{\"id\":\"chatcmpl-1\",\"object\":\"chat.completion\",\"created\":0,\"model\":\""+model+"\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}")
		require.NoError(t, err)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL+"/v1", nil)
	stream, err := provider.ChatStream(context.Background(), ChatRequest{
		Model:     model,
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 1234,
		Tools:     tools,
	})
	require.NoError(t, err)

	for event := range stream {
		require.NotEqual(t, EventError, event.Type, event.Error)
	}

	require.NotNil(t, body)
	return body
}
