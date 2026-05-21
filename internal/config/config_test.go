package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)

	assert.Equal(t, "127.0.0.1", cfg.Gateway.Host)
	assert.Equal(t, 2333, cfg.Gateway.Port)
	assert.Len(t, cfg.Agents.List, 1)
	assert.Equal(t, "default", cfg.Agents.List[0].ID)
	assert.Equal(t, "anthropic/claude-sonnet-4-5-20250514", cfg.Agents.List[0].Model)
	assert.True(t, cfg.Memory.Enabled)
	assert.Equal(t, filepath.Join(DefaultDataDir(), "memory"), cfg.Memory.Dir)
	assert.False(t, cfg.Memory.AutoCapture.Enabled)
	assert.False(t, cfg.Memory.AutoCapture.OnSessionEnd)
	assert.False(t, cfg.Memory.AutoCapture.OnUserConfirmed)
	assert.False(t, cfg.Memory.AutoCapture.OnHighValueDecision)
	assert.False(t, cfg.Memory.AutoCapture.OnAgentCallComplete)
	assert.Equal(t, 3, cfg.Memory.Inject.MaxItems)
	require.NotNil(t, cfg.Runtime.Tools.LoopDetection.Enabled)
	assert.True(t, cfg.Runtime.Tools.LoopDetection.EnabledValue())
	assert.Equal(t, 300000, cfg.Runtime.IdleTimeoutMS)
	assert.Equal(t, 2, cfg.Runtime.Tools.MaxAttempts)
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
}

func TestDefaultGatewayHostPortDerivedFromListen(t *testing.T) {
	host, port, err := SplitListenAddr(defaultGatewayListen)
	require.NoError(t, err)

	cfg := DefaultConfig()
	assert.Equal(t, host, cfg.Gateway.Host)
	assert.Equal(t, port, cfg.Gateway.Port)

	partial := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{{ID: "assistant", Model: "anthropic/claude-sonnet-4-5"}},
		},
	}
	require.NoError(t, partial.Validate())
	assert.Equal(t, host, partial.Gateway.Host)
	assert.Equal(t, port, partial.Gateway.Port)
}

func TestSplitListenAddr(t *testing.T) {
	tests := []struct {
		name     string
		listen   string
		wantHost string
		wantPort int
	}{
		{name: "host port", listen: "127.0.0.1:2333", wantHost: "127.0.0.1", wantPort: 2333},
		{name: "host port with spaces", listen: " 0.0.0.0:19000 ", wantHost: "0.0.0.0", wantPort: 19000},
		{name: "ipv6", listen: "[::1]:2333", wantHost: "::1", wantPort: 2333},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := SplitListenAddr(tt.listen)
			require.NoError(t, err)
			assert.Equal(t, tt.wantHost, host)
			assert.Equal(t, tt.wantPort, port)
		})
	}
}

func TestValidateKeepsMemoryAutoCaptureOffByDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Memory.Enabled = true

	require.NoError(t, cfg.Validate())
	assert.False(t, cfg.Memory.AutoCapture.Enabled)
	assert.False(t, cfg.Memory.AutoCapture.OnSessionEnd)
	assert.False(t, cfg.Memory.AutoCapture.OnAgentCallComplete)
	assert.False(t, cfg.Memory.AutoCapture.OnUserConfirmed)
	assert.False(t, cfg.Memory.AutoCapture.OnHighValueDecision)
	assert.Equal(t, 0.7, cfg.Memory.AutoCapture.MinImportance)
}

func TestValidateFillsMemoryDirFromRuntimeDataDir(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.ProjectRoot = root
	cfg.Memory.Dir = ""

	require.NoError(t, cfg.Validate())
	assert.Equal(t, filepath.Join(root, "anyai", "memory"), cfg.Memory.Dir)
}

func TestValidateFillsOperationalDefaultsOnPartialConfig(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{{ID: "assistant", Model: "anthropic/claude-sonnet-4-5"}},
		},
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, "30m", cfg.Heartbeat.Interval)
	assert.Equal(t, "full", cfg.Security.ExecApprovals.Level)
	assert.Equal(t, defaultExecAllowlist, cfg.Security.ExecApprovals.Allowlist)
	assert.Equal(t, "ignore", cfg.Security.DMPolicy.UnknownSenders)
	assert.NotNil(t, cfg.Providers)
}

func TestValidateTurnsOnMemoryWhenAutoCaptureTriggersAreConfigured(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Memory.Enabled = false
	cfg.Memory.AutoCapture.OnSessionEnd = true

	require.NoError(t, cfg.Validate())
	assert.True(t, cfg.Memory.Enabled)
	assert.True(t, cfg.Memory.AutoCapture.Enabled)
	assert.True(t, cfg.Memory.AutoCapture.OnSessionEnd)
}

func TestValidateDoesNotInventAutoCaptureTriggers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Memory.Enabled = false
	cfg.Memory.AutoCapture.Enabled = true

	require.NoError(t, cfg.Validate())
	assert.True(t, cfg.Memory.Enabled)
	assert.True(t, cfg.Memory.AutoCapture.Enabled)
	assert.False(t, cfg.Memory.AutoCapture.OnSessionEnd)
	assert.False(t, cfg.Memory.AutoCapture.OnAgentCallComplete)
	assert.False(t, cfg.Memory.AutoCapture.OnUserConfirmed)
	assert.False(t, cfg.Memory.AutoCapture.OnHighValueDecision)
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/anyai.json5")
	require.NoError(t, err)
	assert.Equal(t, "default", cfg.Agents.List[0].ID)
}

func TestLoadJSON5(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "anyai.json5")

	content := `{
  // This is a comment
  "gateway": {
    "host": "0.0.0.0",
    "port": 9999,
  },
  "agents": {
    "list": [
      {
        "id": "test",
        "name": "Test Agent",
        "model": "openai/gpt-4o",
      },
    ],
  },
}`

	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0", cfg.Gateway.Host)
	assert.Equal(t, 9999, cfg.Gateway.Port)
	assert.Equal(t, "test", cfg.Agents.List[0].ID)
	assert.Equal(t, "openai/gpt-4o", cfg.Agents.List[0].Model)
}

func TestValidateNoAgents(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one agent")
}

func TestValidateNoModel(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{{ID: "x"}},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no model")
}

func TestValidateRejectsInvalidLoggingLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Logging.FileLevel = "verbose"

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logging.fileLevel")
}

func TestGetAgent(t *testing.T) {
	cfg := DefaultConfig()

	a, ok := cfg.GetAgent("default")
	assert.True(t, ok)
	assert.Equal(t, "Assistant", a.Name)

	_, ok = cfg.GetAgent("nonexistent")
	assert.False(t, ok)
}

func TestProjectDataDir(t *testing.T) {
	root := t.TempDir()
	assert.Equal(t, filepath.Join(root, "anyai"), ProjectDataDir(root))
}

func TestRuntimeDataDirUsesProjectRoot(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.ProjectRoot = root

	assert.Equal(t, filepath.Join(root, "anyai"), cfg.RuntimeDataDir())
}

func TestRuntimeDataDirFallsBackToDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ProjectRoot = ""

	assert.Equal(t, DefaultDataDir(), cfg.RuntimeDataDir())
}

func TestStripJSON5(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "strip single-line comment",
			input: "// comment\n{\"key\": \"value\"}",
			want:  "{\"key\": \"value\"}\n",
		},
		{
			name:  "strip trailing comma before }",
			input: `{"key": "value",}`,
			want:  "{\"key\": \"value\"}\n",
		},
		{
			name:  "strip trailing comma before ]",
			input: `["a", "b",]`,
			want:  "[\"a\", \"b\"]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripJSON5(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
