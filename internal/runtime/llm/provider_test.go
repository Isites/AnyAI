package llm

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProviderModel(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantProvider string
		wantModel    string
	}{
		{
			name:         "provider and model",
			input:        "anthropic/claude-sonnet",
			wantProvider: "anthropic",
			wantModel:    "claude-sonnet",
		},
		{
			name:         "model only",
			input:        "model-only",
			wantProvider: "",
			wantModel:    "model-only",
		},
		{
			name:         "empty string",
			input:        "",
			wantProvider: "",
			wantModel:    "",
		},
		{
			name:         "multiple slashes",
			input:        "openai/gpt-4/turbo",
			wantProvider: "openai",
			wantModel:    "gpt-4/turbo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, model := ParseProviderModel(tt.input)
			assert.Equal(t, tt.wantProvider, provider)
			assert.Equal(t, tt.wantModel, model)
		})
	}
}

func TestNewProviderAnthropic(t *testing.T) {
	p, err := NewProvider("anthropic", ProviderOptions{
		APIKey:  "test-key",
		Headers: map[string]string{"X-Test": "anthropic"},
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	typed, ok := p.(*AnthropicProvider)
	assert.True(t, ok, "expected *AnthropicProvider")
	assert.Equal(t, map[string]string{"X-Test": "anthropic"}, typed.headers)
}

func TestNewProviderOpenAI(t *testing.T) {
	p, err := NewProvider("openai", ProviderOptions{
		APIKey:  "test-key",
		Headers: map[string]string{"X-Test": "openai"},
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	typed, ok := p.(*OpenAIProvider)
	assert.True(t, ok, "expected *OpenAIProvider")
	assert.Equal(t, map[string]string{"X-Test": "openai"}, typed.headers)
}

func TestNewProviderOpenAICompatible(t *testing.T) {
	p, err := NewProvider("openai-compatible", ProviderOptions{
		APIKey:  "test-key",
		BaseURL: "http://localhost:8080/v1",
		Kind:    ProviderKindOpenAICompatible,
		Headers: map[string]string{"X-Test": "compatible"},
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	typed, ok := p.(*OpenAIProvider)
	assert.True(t, ok, "expected *OpenAIProvider for openai-compatible")
	assert.Equal(t, "http://localhost:8080/v1", typed.baseURL)
	assert.Equal(t, map[string]string{"X-Test": "compatible"}, typed.headers)
}

func TestNewProviderAnthropicCompatible(t *testing.T) {
	p, err := NewProvider("anthropic-compatible", ProviderOptions{
		APIKey:  "test-key",
		BaseURL: "http://localhost:8081/v1",
		Kind:    ProviderKindAnthropicCompatible,
		Headers: map[string]string{"X-Test": "compatible"},
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	typed, ok := p.(*AnthropicProvider)
	assert.True(t, ok, "expected *AnthropicProvider for anthropic-compatible")
	assert.Equal(t, "http://localhost:8081/v1", typed.baseURL)
	assert.Equal(t, map[string]string{"X-Test": "compatible"}, typed.headers)
}

func TestNewProviderAnthropicMessages(t *testing.T) {
	p, err := NewProvider("anthropic", ProviderOptions{
		APIKey:  "test-key",
		BaseURL: "https://coding.testbird.ai",
		Kind:    ProviderKindAnthropicMessages,
		Headers: map[string]string{"X-Test": "messages"},
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	typed, ok := p.(*AnthropicProvider)
	assert.True(t, ok, "expected *AnthropicProvider for anthropic-messages")
	assert.Equal(t, "https://coding.testbird.ai", typed.baseURL)
	assert.Equal(t, map[string]string{"X-Test": "messages"}, typed.headers)
}

func TestNewProviderUnknown(t *testing.T) {
	p, err := NewProvider("unknown-provider", ProviderOptions{})
	assert.Nil(t, p)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown LLM provider kind")
}

func TestNewProviderBaseURLDefault(t *testing.T) {
	// When kind is empty but base URL is set, should default to openai-compatible
	p, err := NewProvider("anything", ProviderOptions{
		APIKey:  "test-key",
		BaseURL: "http://localhost:11434/v1",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	_, ok := p.(*OpenAIProvider)
	assert.True(t, ok, "expected *OpenAIProvider when base URL is set")
}

func TestCanInitializeProviderForOpenAICompatibleEndpoint(t *testing.T) {
	assert.True(t, CanInitializeProvider("ollama", ProviderOptions{
		BaseURL: "http://127.0.0.1:11434/v1",
		Kind:    ProviderKindOpenAICompatible,
	}))
}

func TestCanInitializeProviderForAnthropicCompatibleEndpoint(t *testing.T) {
	assert.True(t, CanInitializeProvider("claude-proxy", ProviderOptions{
		BaseURL: "http://127.0.0.1:8080/v1",
		Kind:    ProviderKindAnthropicCompatible,
	}))
}

func TestCanInitializeProviderForAnthropicMessagesEndpoint(t *testing.T) {
	assert.True(t, CanInitializeProvider("anthropic", ProviderOptions{
		BaseURL: "https://coding.testbird.ai",
		Kind:    ProviderKindAnthropicMessages,
	}))
}

func TestCanInitializeProviderRejectsUnsupportedKinds(t *testing.T) {
	assert.False(t, CanInitializeProvider("gemini", ProviderOptions{
		BaseURL: "http://127.0.0.1:9999",
		Kind:    ProviderKindGemini,
	}))
	assert.False(t, CanInitializeProvider("anthropic", ProviderOptions{}))
	assert.True(t, CanInitializeProvider("anthropic", ProviderOptions{APIKey: "test-key"}))
}

func TestAnthropicProviderModels(t *testing.T) {
	p := NewAnthropicProvider("test-key", "", nil)
	models := p.Models()
	assert.NotEmpty(t, models)
	for _, m := range models {
		assert.NotEmpty(t, m.ID)
		assert.NotEmpty(t, m.Name)
		assert.Equal(t, "anthropic", m.Provider)
	}
}

func TestOpenAIProviderModels(t *testing.T) {
	p := NewOpenAIProvider("test-key", "", nil)
	models := p.Models()
	assert.NotEmpty(t, models)
	for _, m := range models {
		assert.NotEmpty(t, m.ID)
		assert.NotEmpty(t, m.Name)
		assert.Equal(t, "openai", m.Provider)
	}
}

func TestNewProviderGemini(t *testing.T) {
	p, err := NewProvider("gemini", ProviderOptions{
		APIKey:  "test-key",
		Kind:    ProviderKindGemini,
		BaseURL: "https://gemini.example/v1beta",
		Headers: map[string]string{"X-Test": "gemini"},
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	typed, ok := p.(*GeminiProvider)
	assert.True(t, ok, "expected *GeminiProvider")
	assert.Equal(t, "https://gemini.example/v1beta", typed.baseURL)
	assert.Equal(t, map[string]string{"X-Test": "gemini"}, typed.headers)
}

func TestGeminiProviderModels(t *testing.T) {
	p, err := NewGeminiProvider(context.Background(), "test-key", "", nil)
	require.NoError(t, err)
	models := p.Models()
	assert.NotEmpty(t, models)
	for _, m := range models {
		assert.NotEmpty(t, m.ID)
		assert.NotEmpty(t, m.Name)
		assert.Equal(t, "gemini", m.Provider)
	}
}

func TestNewProviderQwenCarriesHeaders(t *testing.T) {
	p, err := NewProvider("qwen", ProviderOptions{
		APIKey:  "test-key",
		Kind:    ProviderKindQwen,
		Headers: map[string]string{"X-Test": "qwen"},
	})
	require.NoError(t, err)
	require.NotNil(t, p)

	typed, ok := p.(*QwenProvider)
	require.True(t, ok, "expected *QwenProvider")
	assert.Equal(t, "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", typed.baseURL)
	assert.Equal(t, map[string]string{"X-Test": "qwen"}, typed.headers)
}

func TestNewHeaderHTTPClientAddsHeaders(t *testing.T) {
	var gotHeader string
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Test")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newHeaderHTTPClient(map[string]string{"X-Test": "enabled"})
	require.NotNil(t, client)

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, "enabled", gotHeader)
}
