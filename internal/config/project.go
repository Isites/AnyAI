package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProjectConfig is the project-level AnyAI config loaded from anyai.yaml.
type ProjectConfig struct {
	Name      string                           `yaml:"name"`
	Models    ProjectModelsConfig              `yaml:"models"`
	Providers map[string]ProjectProviderConfig `yaml:"providers"`
	Env       []string                         `yaml:"env"`
	Runtime   ProjectRuntimeConfig             `yaml:"runtime"`
	Memory    ProjectMemoryConfig              `yaml:"memory"`
	Channels  ProjectChannelsConfig            `yaml:"channels"`
	Logging   ProjectLoggingConfig             `yaml:"logging"`
	Security  ProjectSecurityConfig            `yaml:"security"`

	path string
}

type ProjectModelsConfig struct {
	Default string            `yaml:"default"`
	Aliases map[string]string `yaml:"aliases"`
}

type ProjectProviderConfig struct {
	Kind       string            `yaml:"kind"`
	APIKey     string            `yaml:"api_key"`
	APIKeyEnv  string            `yaml:"api_key_env"`
	BaseURL    string            `yaml:"base_url"`
	BaseURLEnv string            `yaml:"base_url_env"`
	Headers    map[string]string `yaml:"headers"`
	HeadersEnv string            `yaml:"headers_env"`
}

type ProjectRuntimeConfig struct {
	IdleTimeoutMS int                           `yaml:"idle_timeout_ms"`
	AgentCall     ProjectAgentCallRuntimeConfig `yaml:"agent_call"`
	Tools         ProjectToolRuntimeConfig      `yaml:"tools"`
	Sessions      ProjectSessionRuntimeConfig   `yaml:"sessions"`
}

type ProjectLoggingConfig struct {
	FileLevel      string                   `yaml:"file_level,omitempty"`
	StderrLevel    string                   `yaml:"stderr_level,omitempty"`
	WhatsMeowLevel string                   `yaml:"whatsmeow_level,omitempty"`
	MirrorStderr   *bool                    `yaml:"mirror_stderr,omitempty"`
	Rotation       ProjectLogRotationConfig `yaml:"rotation,omitempty"`
}

type ProjectLogRotationConfig struct {
	Filename   string `yaml:"filename,omitempty"`
	MaxBytes   int64  `yaml:"max_bytes,omitempty"`
	MaxBackups int    `yaml:"max_backups,omitempty"`
}

type ProjectAgentCallRuntimeConfig struct {
	DepthLimit  int `yaml:"depth_limit"`
	MaxParallel int `yaml:"max_parallel"`
}

type ProjectToolRuntimeConfig struct {
	MaxAttempts    int                            `yaml:"max_attempts"`
	RetryBackoffMS int                            `yaml:"retry_backoff_ms"`
	LoopDetection  ProjectToolLoopDetectionConfig `yaml:"loop_detection"`
}

type ProjectSessionRuntimeConfig struct {
	Compaction ProjectSessionCompactionConfig `yaml:"compaction"`
}

type ProjectSessionCompactionConfig struct {
	Enabled              *bool  `yaml:"enabled,omitempty"`
	TriggerMode          string `yaml:"trigger_mode"`
	EntryThreshold       int    `yaml:"entry_threshold"`
	TokenThreshold       int    `yaml:"token_threshold"`
	KeepRecentUserTurns  int    `yaml:"keep_recent_user_turns"`
	KeepRecentUserTokens int    `yaml:"keep_recent_user_tokens"`
	SummaryMaxTokens     int    `yaml:"summary_max_tokens"`
}

type ProjectToolLoopDetectionConfig struct {
	Enabled          *bool `yaml:"enabled,omitempty"`
	HistorySize      int   `yaml:"history_size"`
	WarningThreshold int   `yaml:"warning_threshold"`
	BlockThreshold   int   `yaml:"block_threshold"`
}

func (c ProjectToolLoopDetectionConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type ProjectMemoryConfig struct {
	Enabled     *bool                          `yaml:"enabled,omitempty"`
	AutoCapture ProjectMemoryAutoCaptureConfig `yaml:"auto_capture"`
	Inject      ProjectMemoryInjectConfig      `yaml:"inject"`
}

type ProjectMemoryAutoCaptureConfig struct {
	Enabled              bool    `yaml:"enabled"`
	OnSessionEnd         bool    `yaml:"on_session_end"`
	OnAgentCallComplete  bool    `yaml:"on_agent_call_complete"`
	OnUserConfirmed      bool    `yaml:"on_user_confirmed"`
	OnHighValueDecision  bool    `yaml:"on_high_value_decision"`
	MinImportance        float64 `yaml:"min_importance"`
	CandidateTTL         string  `yaml:"candidate_ttl"`
	EpisodicTTL          string  `yaml:"episodic_ttl"`
	CleanupInterval      string  `yaml:"cleanup_interval"`
	PromoteMinImportance float64 `yaml:"promote_min_importance"`
}

type ProjectMemoryInjectConfig struct {
	MaxItems int `yaml:"max_items"`
}

type ProjectChannelsConfig struct {
	Gateway  ProjectGatewayChannelsConfig `yaml:"gateway"`
	HTTP     ProjectHTTPChannelConfig     `yaml:"http"`
	Telegram ProjectTelegramChannelConfig `yaml:"telegram"`
	WhatsApp ProjectWhatsAppChannelConfig `yaml:"whatsapp"`
}

type ProjectGatewayChannelsConfig struct {
	Enabled []string `yaml:"enabled"`
}

type ProjectHTTPChannelConfig struct {
	Listen string `yaml:"listen"`
}

type ProjectTelegramChannelConfig struct {
	TokenEnv string `yaml:"token_env"`
	Token    string `yaml:"token"`
}

type ProjectWhatsAppChannelConfig struct {
	DBPath string `yaml:"db_path"`
}

type ProjectSecurityConfig struct {
	ExecApprovals ExecApprovalsConfig `yaml:"exec_approvals"`
	DMPolicy      DMPolicyConfig      `yaml:"dm_policy"`
	GroupPolicy   GroupPolicyConfig   `yaml:"group_policy"`
}

// LoadProject reads and validates an anyai.yaml file.
func LoadProject(path string) (*ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read project config: %w", err)
	}

	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse project config: %w", err)
	}
	cfg.path = path
	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate project config: %w", err)
	}

	return &cfg, nil
}

func (c *ProjectConfig) applyDefaults() {
	if c.Providers == nil {
		c.Providers = map[string]ProjectProviderConfig{}
	}
	if c.Models.Aliases == nil {
		c.Models.Aliases = map[string]string{}
	}
	if c.Runtime.IdleTimeoutMS == 0 {
		c.Runtime.IdleTimeoutMS = defaultRuntimeIdleTimeoutMS
	}
	c.Logging.FileLevel = normalizeLogLevel(c.Logging.FileLevel, defaultLogFileLevel)
	c.Logging.StderrLevel = normalizeLogLevel(c.Logging.StderrLevel, defaultLogStderrLevel)
	c.Logging.WhatsMeowLevel = normalizeLogLevel(c.Logging.WhatsMeowLevel, defaultLogWhatsMeowLevel)
	if strings.TrimSpace(c.Logging.Rotation.Filename) == "" {
		c.Logging.Rotation.Filename = defaultLogFilename
	}
	if c.Logging.Rotation.MaxBytes == 0 {
		c.Logging.Rotation.MaxBytes = defaultLogMaxBytes
	}
	if c.Logging.Rotation.MaxBackups == 0 {
		c.Logging.Rotation.MaxBackups = defaultLogMaxBackups
	}
	if c.Runtime.AgentCall.DepthLimit == 0 {
		c.Runtime.AgentCall.DepthLimit = defaultAgentCallDepthLimit
	}
	if c.Runtime.AgentCall.MaxParallel == 0 {
		c.Runtime.AgentCall.MaxParallel = defaultAgentCallMaxParallel
	}
	if c.Runtime.Tools.MaxAttempts == 0 {
		c.Runtime.Tools.MaxAttempts = defaultToolMaxAttempts
	}
	if c.Runtime.Tools.RetryBackoffMS == 0 {
		c.Runtime.Tools.RetryBackoffMS = defaultToolRetryBackoffMS
	}
	if c.Runtime.Tools.LoopDetection.Enabled == nil {
		c.Runtime.Tools.LoopDetection.Enabled = boolPtr(true)
	}
	if c.Runtime.Tools.LoopDetection.HistorySize == 0 {
		c.Runtime.Tools.LoopDetection.HistorySize = defaultToolLoopHistorySize
	}
	if c.Runtime.Tools.LoopDetection.WarningThreshold == 0 {
		c.Runtime.Tools.LoopDetection.WarningThreshold = defaultToolLoopWarningThreshold
	}
	if c.Runtime.Tools.LoopDetection.BlockThreshold == 0 {
		c.Runtime.Tools.LoopDetection.BlockThreshold = defaultToolLoopBlockThreshold
	}
	if c.Runtime.Tools.LoopDetection.BlockThreshold < c.Runtime.Tools.LoopDetection.WarningThreshold {
		c.Runtime.Tools.LoopDetection.BlockThreshold = c.Runtime.Tools.LoopDetection.WarningThreshold
	}
	if c.Runtime.Sessions.Compaction.Enabled == nil {
		c.Runtime.Sessions.Compaction.Enabled = boolPtr(true)
	}
	if strings.TrimSpace(c.Runtime.Sessions.Compaction.TriggerMode) == "" {
		c.Runtime.Sessions.Compaction.TriggerMode = defaultSessionCompactionTriggerMode
	}
	if c.Runtime.Sessions.Compaction.EntryThreshold == 0 {
		c.Runtime.Sessions.Compaction.EntryThreshold = defaultSessionCompactionEntryThresh
	}
	if c.Runtime.Sessions.Compaction.TokenThreshold == 0 {
		c.Runtime.Sessions.Compaction.TokenThreshold = defaultSessionCompactionTokenThresh
	}
	if c.Runtime.Sessions.Compaction.KeepRecentUserTurns == 0 {
		c.Runtime.Sessions.Compaction.KeepRecentUserTurns = defaultSessionCompactionKeepTurns
	}
	if c.Runtime.Sessions.Compaction.KeepRecentUserTokens == 0 {
		c.Runtime.Sessions.Compaction.KeepRecentUserTokens = defaultSessionCompactionKeepTokens
	}
	if c.Runtime.Sessions.Compaction.SummaryMaxTokens == 0 {
		c.Runtime.Sessions.Compaction.SummaryMaxTokens = defaultSessionCompactionSummaryTokens
	}
	if c.Memory.Enabled == nil {
		enabled := true
		c.Memory.Enabled = &enabled
	}
	if len(c.Channels.Gateway.Enabled) == 0 {
		c.Channels.Gateway.Enabled = append([]string(nil), defaultProjectGatewayModes...)
	}
	if strings.TrimSpace(c.Channels.HTTP.Listen) == "" {
		c.Channels.HTTP.Listen = defaultGatewayListen
	}
	if c.Memory.Inject.MaxItems == 0 {
		c.Memory.Inject.MaxItems = defaultMemoryInjectMaxItems
	}
	applyProjectMemoryAutoCaptureDefaults(&c.Memory)
	if c.Memory.AutoCapture.MinImportance == 0 {
		c.Memory.AutoCapture.MinImportance = defaultMemoryMinImportance
	}
	if c.Memory.AutoCapture.PromoteMinImportance == 0 {
		c.Memory.AutoCapture.PromoteMinImportance = defaultMemoryPromoteImportance
	}
	if strings.TrimSpace(c.Memory.AutoCapture.CandidateTTL) == "" {
		c.Memory.AutoCapture.CandidateTTL = defaultMemoryCandidateTTL
	}
	if strings.TrimSpace(c.Memory.AutoCapture.EpisodicTTL) == "" {
		c.Memory.AutoCapture.EpisodicTTL = defaultMemoryEpisodicTTL
	}
	if strings.TrimSpace(c.Memory.AutoCapture.CleanupInterval) == "" {
		c.Memory.AutoCapture.CleanupInterval = defaultMemoryCleanupInterval
	}
}

// Validate applies basic correctness checks.
func (c *ProjectConfig) Validate() error {
	if c.Runtime.IdleTimeoutMS < 0 {
		return fmt.Errorf("runtime.idle_timeout_ms must be >= 0")
	}
	if c.Runtime.AgentCall.DepthLimit < 0 {
		return fmt.Errorf("runtime.agent_call.depth_limit must be >= 0")
	}
	if c.Runtime.AgentCall.MaxParallel < 0 {
		return fmt.Errorf("runtime.agent_call.max_parallel must be >= 0")
	}
	if c.Runtime.Tools.MaxAttempts < 0 {
		return fmt.Errorf("runtime.tools.max_attempts must be >= 0")
	}
	if c.Runtime.Tools.RetryBackoffMS < 0 {
		return fmt.Errorf("runtime.tools.retry_backoff_ms must be >= 0")
	}
	if c.Runtime.Tools.LoopDetection.HistorySize < 0 {
		return fmt.Errorf("runtime.tools.loop_detection.history_size must be >= 0")
	}
	if c.Runtime.Tools.LoopDetection.WarningThreshold < 0 {
		return fmt.Errorf("runtime.tools.loop_detection.warning_threshold must be >= 0")
	}
	if c.Runtime.Tools.LoopDetection.BlockThreshold < 0 {
		return fmt.Errorf("runtime.tools.loop_detection.block_threshold must be >= 0")
	}
	switch c.Runtime.Sessions.Compaction.TriggerMode {
	case "entry_count", "token_estimate":
	default:
		return fmt.Errorf("runtime.sessions.compaction.trigger_mode must be one of entry_count, token_estimate")
	}
	if c.Runtime.Sessions.Compaction.EntryThreshold < 0 {
		return fmt.Errorf("runtime.sessions.compaction.entry_threshold must be >= 0")
	}
	if c.Runtime.Sessions.Compaction.TokenThreshold < 0 {
		return fmt.Errorf("runtime.sessions.compaction.token_threshold must be >= 0")
	}
	if c.Runtime.Sessions.Compaction.KeepRecentUserTurns < 0 {
		return fmt.Errorf("runtime.sessions.compaction.keep_recent_user_turns must be >= 0")
	}
	if c.Runtime.Sessions.Compaction.KeepRecentUserTokens < 0 {
		return fmt.Errorf("runtime.sessions.compaction.keep_recent_user_tokens must be >= 0")
	}
	if c.Runtime.Sessions.Compaction.SummaryMaxTokens < 0 {
		return fmt.Errorf("runtime.sessions.compaction.summary_max_tokens must be >= 0")
	}
	if !isSupportedLogLevel(c.Logging.FileLevel) {
		return fmt.Errorf("logging.file_level must be one of debug, info, warn, error")
	}
	if !isSupportedLogLevel(c.Logging.StderrLevel) {
		return fmt.Errorf("logging.stderr_level must be one of debug, info, warn, error")
	}
	if !isSupportedLogLevel(c.Logging.WhatsMeowLevel) {
		return fmt.Errorf("logging.whatsmeow_level must be one of debug, info, warn, error")
	}
	if c.Logging.Rotation.MaxBytes < 0 {
		return fmt.Errorf("logging.rotation.max_bytes must be >= 0")
	}
	if c.Logging.Rotation.MaxBackups < 0 {
		return fmt.Errorf("logging.rotation.max_backups must be >= 0")
	}
	if c.Memory.Inject.MaxItems < 0 {
		return fmt.Errorf("memory.inject.max_items must be >= 0")
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "memory.auto_capture.candidate_ttl", value: c.Memory.AutoCapture.CandidateTTL},
		{name: "memory.auto_capture.episodic_ttl", value: c.Memory.AutoCapture.EpisodicTTL},
		{name: "memory.auto_capture.cleanup_interval", value: c.Memory.AutoCapture.CleanupInterval},
	} {
		if _, err := time.ParseDuration(item.value); err != nil {
			return fmt.Errorf("%s must be a valid duration: %w", item.name, err)
		}
	}
	return nil
}

func (c ProjectMemoryConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func applyProjectMemoryAutoCaptureDefaults(cfg *ProjectMemoryConfig) {
	if cfg == nil {
		return
	}

	hasTriggers := cfg.AutoCapture.OnSessionEnd ||
		cfg.AutoCapture.OnAgentCallComplete ||
		cfg.AutoCapture.OnUserConfirmed ||
		cfg.AutoCapture.OnHighValueDecision

	if cfg.AutoCapture.Enabled || hasTriggers {
		enabled := true
		cfg.Enabled = &enabled
		if hasTriggers {
			cfg.AutoCapture.Enabled = true
		}
	}
}

// Path returns the anyai.yaml path this config was loaded from.
func (c *ProjectConfig) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Dir returns the directory containing the anyai.yaml file.
func (c *ProjectConfig) Dir() string {
	if c == nil || c.path == "" {
		return ""
	}
	return filepath.Dir(c.path)
}

// ResolveModel resolves an alias or empty input to a concrete model string.
func (c *ProjectConfig) ResolveModel(model string) string {
	if c == nil {
		return model
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(c.Models.Default)
	}
	if alias, ok := c.Models.Aliases[model]; ok {
		return alias
	}
	return model
}
