package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "anyai.yaml")

	content := `
name: ecommerce-support
models:
  default: anthropic/claude-sonnet-4-5
  aliases:
    fast: openai/gpt-4.1-mini
providers:
  anthropic:
    api_key_env: ANTHROPIC_API_KEY
runtime:
  agent_call:
    depth_limit: 8
channels:
  http:
    listen: 127.0.0.1:19000
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cfg, err := LoadProject(path)
	require.NoError(t, err)

	assert.Equal(t, "ecommerce-support", cfg.Name)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", cfg.Models.Default)
	assert.Equal(t, "openai/gpt-4.1-mini", cfg.ResolveModel("fast"))
	assert.Equal(t, 8, cfg.Runtime.AgentCall.DepthLimit)
	assert.Equal(t, 4, cfg.Runtime.AgentCall.MaxParallel)
	assert.Equal(t, "127.0.0.1:19000", cfg.Channels.HTTP.Listen)
	assert.Equal(t, path, cfg.Path())
	assert.Equal(t, dir, cfg.Dir())
}

func TestLoadProjectProviderHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "anyai.yaml")

	content := `
name: provider-headers
models:
  default: openai/gpt-4.1-mini
providers:
  openai:
    kind: openai
    api_key_env: OPENAI_API_KEY
    headers:
      HTTP-Referer: https://example.com/app
      X-Title: AnyAI Demo
    headers_env: OPENAI_HEADERS_JSON
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cfg, err := LoadProject(path)
	require.NoError(t, err)

	provider, ok := cfg.Providers["openai"]
	require.True(t, ok)
	assert.Equal(t, "openai", provider.Kind)
	assert.Equal(t, "OPENAI_API_KEY", provider.APIKeyEnv)
	assert.Equal(t, map[string]string{
		"HTTP-Referer": "https://example.com/app",
		"X-Title":      "AnyAI Demo",
	}, provider.Headers)
	assert.Equal(t, "OPENAI_HEADERS_JSON", provider.HeadersEnv)
}

func TestLoadProjectProviderInlineAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "anyai.yaml")

	content := `
name: inline-api-key
models:
  default: openai/gpt-5.4
providers:
  openai:
    kind: openai-compatible
    api_key: inline-secret
    base_url: https://coding.testbird.ai/v1
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cfg, err := LoadProject(path)
	require.NoError(t, err)

	provider, ok := cfg.Providers["openai"]
	require.True(t, ok)
	assert.Equal(t, "inline-secret", provider.APIKey)
	assert.Equal(t, "https://coding.testbird.ai/v1", provider.BaseURL)
}

func TestProjectDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "anyai.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: test\n"), 0o644))

	cfg, err := LoadProject(path)
	require.NoError(t, err)

	assert.Equal(t, 300000, cfg.Runtime.IdleTimeoutMS)
	assert.Equal(t, 4, cfg.Runtime.AgentCall.DepthLimit)
	assert.Equal(t, 4, cfg.Runtime.AgentCall.MaxParallel)
	assert.Equal(t, 1, cfg.Runtime.Tools.MaxAttempts)
	require.NotNil(t, cfg.Runtime.Tools.LoopDetection.Enabled)
	assert.True(t, cfg.Runtime.Tools.LoopDetection.EnabledValue())
	assert.Equal(t, 750, cfg.Runtime.Tools.RetryBackoffMS)
	assert.Equal(t, 24, cfg.Runtime.Tools.LoopDetection.HistorySize)
	assert.Equal(t, 4, cfg.Runtime.Tools.LoopDetection.WarningThreshold)
	assert.Equal(t, 6, cfg.Runtime.Tools.LoopDetection.BlockThreshold)
	assert.Equal(t, "debug", cfg.Logging.FileLevel)
	assert.Equal(t, "info", cfg.Logging.StderrLevel)
	assert.Equal(t, "warn", cfg.Logging.WhatsMeowLevel)
	assert.Equal(t, "runtime.log", cfg.Logging.Rotation.Filename)
	assert.EqualValues(t, 10<<20, cfg.Logging.Rotation.MaxBytes)
	assert.Equal(t, 20, cfg.Logging.Rotation.MaxBackups)
	assert.Equal(t, []string{"cli", "http"}, cfg.Channels.Gateway.Enabled)
	assert.Equal(t, "127.0.0.1:2333", cfg.Channels.HTTP.Listen)
	require.NotNil(t, cfg.Memory.Enabled)
	assert.True(t, cfg.Memory.EnabledValue())
	assert.Equal(t, 3, cfg.Memory.Inject.MaxItems)
	assert.False(t, cfg.Memory.AutoCapture.Enabled)
	assert.False(t, cfg.Memory.AutoCapture.OnSessionEnd)
	assert.False(t, cfg.Memory.AutoCapture.OnUserConfirmed)
	assert.False(t, cfg.Memory.AutoCapture.OnHighValueDecision)
	assert.False(t, cfg.Memory.AutoCapture.OnAgentCallComplete)
	assert.Equal(t, 0.7, cfg.Memory.AutoCapture.MinImportance)
}

func TestProjectMemoryCanBeExplicitlyDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "anyai.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: test\nmemory:\n  enabled: false\n"), 0o644))

	cfg, err := LoadProject(path)
	require.NoError(t, err)

	require.NotNil(t, cfg.Memory.Enabled)
	assert.False(t, cfg.Memory.EnabledValue())
	assert.False(t, cfg.Memory.AutoCapture.Enabled)
}

func TestProjectAutoCaptureEnabledDoesNotInventTriggers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "anyai.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: test\nmemory:\n  auto_capture:\n    enabled: true\n"), 0o644))

	cfg, err := LoadProject(path)
	require.NoError(t, err)

	require.NotNil(t, cfg.Memory.Enabled)
	assert.True(t, cfg.Memory.EnabledValue())
	assert.True(t, cfg.Memory.AutoCapture.Enabled)
	assert.False(t, cfg.Memory.AutoCapture.OnSessionEnd)
	assert.False(t, cfg.Memory.AutoCapture.OnAgentCallComplete)
	assert.False(t, cfg.Memory.AutoCapture.OnUserConfirmed)
	assert.False(t, cfg.Memory.AutoCapture.OnHighValueDecision)
}

func TestProjectResolveModelFallback(t *testing.T) {
	cfg := &ProjectConfig{
		Models: ProjectModelsConfig{
			Default: "anthropic/claude-sonnet-4-5",
			Aliases: map[string]string{"fast": "openai/gpt-4.1-mini"},
		},
	}

	assert.Equal(t, "anthropic/claude-sonnet-4-5", cfg.ResolveModel(""))
	assert.Equal(t, "openai/gpt-4.1-mini", cfg.ResolveModel("fast"))
	assert.Equal(t, "openai/gpt-4.1", cfg.ResolveModel("openai/gpt-4.1"))
}

func TestProjectValidateRejectsNegativeValues(t *testing.T) {
	cfg := &ProjectConfig{}
	cfg.applyDefaults()
	cfg.Runtime.AgentCall.MaxParallel = -1

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_parallel")
}

func TestProjectValidateRejectsInvalidLoggingLevel(t *testing.T) {
	cfg := &ProjectConfig{}
	cfg.applyDefaults()
	cfg.Logging.FileLevel = "verbose"

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logging.file_level")
}
