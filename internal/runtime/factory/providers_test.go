package factory

import (
	"testing"

	"github.com/Isites/anyai/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitProvidersAllowsBaseURLWithoutAPIKey(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: "http://127.0.0.1:11434/v1",
			},
		},
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "assistant", Model: "ollama/qwen3:1.7b"},
			},
		},
	}

	providers := InitProviders(cfg, nil)
	_, ok := providers["ollama"]
	assert.True(t, ok, "expected local openai-compatible provider to initialize without API key")
}

func TestResolveProviderOptsMergesHeadersFromConfigAndEnv(t *testing.T) {
	t.Setenv("OPENAI_HEADERS_JSON", `{"HTTP-Referer":"https://override.example/app","X-Trace":"enabled"}`)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Kind:    "openai",
				APIKey:  "config-key",
				BaseURL: "https://api.openai.com/v1",
				Headers: map[string]string{
					"HTTP-Referer": "https://example.com/app",
					"X-Title":      "AnyAI",
				},
			},
		},
	}

	opts, err := ResolveProviderOpts("openai", cfg)
	require.NoError(t, err)
	assert.Equal(t, "config-key", opts.APIKey)
	assert.Equal(t, "https://api.openai.com/v1", opts.BaseURL)
	assert.Equal(t, map[string]string{
		"HTTP-Referer": "https://override.example/app",
		"X-Title":      "AnyAI",
		"X-Trace":      "enabled",
	}, opts.Headers)
}

func TestResolveProviderOptsNormalizesProviderEnvPrefix(t *testing.T) {
	t.Setenv("CLAUDE_PROXY_API_KEY", "proxy-key")
	t.Setenv("CLAUDE_PROXY_HEADERS_JSON", `{"X-Route":"beta"}`)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"claude-proxy": {
				Kind:    "anthropic-compatible",
				BaseURL: "http://127.0.0.1:8080/v1",
			},
		},
	}

	opts, err := ResolveProviderOpts("claude-proxy", cfg)
	require.NoError(t, err)
	assert.Equal(t, "proxy-key", opts.APIKey)
	assert.Equal(t, "http://127.0.0.1:8080/v1", opts.BaseURL)
	assert.Equal(t, map[string]string{"X-Route": "beta"}, opts.Headers)
	assert.Equal(t, "CLAUDE_PROXY_V2", providerEnvPrefix("claude-proxy.v2"))
}

func TestInitProvidersAllowsAnthropicCompatibleBaseURLWithoutAPIKey(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"claude-proxy": {
				Kind:    "anthropic-compatible",
				BaseURL: "http://127.0.0.1:8080/v1",
			},
		},
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "assistant", Model: "claude-proxy/claude-sonnet-4-5-20250514"},
			},
		},
	}

	providers := InitProviders(cfg, nil)
	_, ok := providers["claude-proxy"]
	assert.True(t, ok, "expected local anthropic-compatible provider to initialize without API key")
}

func TestInitProvidersAllowsAnthropicMessagesBaseURLWithoutAPIKey(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {
				Kind:    "anthropic-messages",
				BaseURL: "https://coding.testbird.ai",
			},
		},
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "assistant", Model: "anthropic/glm-5.1"},
			},
		},
	}

	providers := InitProviders(cfg, nil)
	_, ok := providers["anthropic"]
	assert.True(t, ok, "expected anthropic-messages provider to initialize with a custom base URL")
}

func TestInitProvidersSkipsProviderWithoutAPIKeyOrBaseURL(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"test-provider": {},
		},
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "assistant", Model: "test-provider/claude-sonnet-4-5-20250514"},
			},
		},
	}

	providers := InitProviders(cfg, nil)
	assert.Empty(t, providers)
}
