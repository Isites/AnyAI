package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"github.com/Isites/anyai/internal/runtime/tooldefaults"
)

const (
	defaultGatewayHost                          = "127.0.0.1"
	defaultGatewayPort                          = 2333
	defaultGatewayListen                        = "127.0.0.1:2333"
	defaultReloadMode                           = "hybrid"
	defaultRuntimeIdleTimeoutMS                 = 300000
	defaultAgentCallDepthLimit                  = 4
	defaultAgentCallMaxParallel                 = 4
	defaultToolMaxAttempts                      = tooldefaults.MaxAttempts
	defaultToolRetryBackoffMS                   = tooldefaults.RetryBackoffMS
	defaultToolLoopHistorySize                  = 24
	defaultToolLoopWarningThreshold             = 4
	defaultToolLoopBlockThreshold               = 6
	defaultSessionQueueMode                     = "collect"
	defaultSessionQueueDebounceMS               = 800
	defaultSessionQueueMaxPending               = 20
	defaultSessionQueueDropPolicy               = "summarize"
	defaultSessionCompactionTriggerMode         = "token_estimate"
	defaultSessionCompactionEntryThresh         = 96
	defaultSessionCompactionTokenThresh         = 12000
	defaultSessionCompactionKeepTurns           = 4
	defaultSessionCompactionKeepTokens          = 2400
	defaultSessionCompactionSummaryTokens       = 1600
	defaultMemoryInjectMaxItems                 = 3
	defaultMemoryMinImportance                  = 0.7
	defaultMemoryPromoteImportance              = 0.75
	defaultMemoryCandidateTTL                   = "168h"
	defaultMemoryEpisodicTTL                    = "72h"
	defaultMemoryCleanupInterval                = "2m"
	defaultHeartbeatInterval                    = "30m"
	defaultExecApprovalLevel                    = "full"
	defaultDMUnknownSenderPolicy                = "ignore"
	defaultLogFileLevel                         = "debug"
	defaultLogStderrLevel                       = "info"
	defaultLogWhatsMeowLevel                    = "warn"
	defaultLogFilename                          = "runtime.log"
	defaultLogMaxBytes                    int64 = 10 << 20
	defaultLogMaxBackups                        = 20
)

var (
	defaultExecAllowlist = []string{
		"ls", "cat", "find", "grep", "head", "tail", "wc", "pwd", "date",
		"cut", "tr", "uniq", "echo", "sed", "awk", "xargs", "jq", "git",
	}
	defaultProjectGatewayModes = []string{"cli", "http"}
)

// Config is the top-level AnyAI configuration.
type Config struct {
	Gateway   GatewayConfig             `json:"gateway"`
	Providers map[string]ProviderConfig `json:"providers"`
	Agents    AgentsConfig              `json:"agents"`
	Bindings  []Binding                 `json:"bindings"`
	Channels  ChannelsConfig            `json:"channels"`
	Heartbeat HeartbeatConfig           `json:"heartbeat"`
	Memory    MemoryConfig              `json:"memory"`
	Cortex    CortexConfig              `json:"cortex"`
	Runtime   RuntimeConfig             `json:"runtime,omitempty"`
	Logging   LoggingConfig             `json:"logging,omitempty"`
	Security  SecurityConfig            `json:"security"`

	ProjectName      string   `json:"projectName,omitempty"`
	ProjectRoot      string   `json:"-"`
	ProjectConfigDir string   `json:"-"`
	SharedSkillsDir  string   `json:"-"`
	SharedMCPsDir    string   `json:"-"`
	ActiveChannels   []string `json:"-"`

	mu   sync.RWMutex
	path string
}

// ProviderConfig holds connection details for an LLM provider.
type ProviderConfig struct {
	Kind    string            `json:"kind"`              // "openai", "anthropic", "openai-compatible", "anthropic-compatible", "anthropic-messages"
	BaseURL string            `json:"base_url"`          // custom API endpoint (e.g. LiteLLM)
	APIKey  string            `json:"api_key"`           // API key or auth token
	Headers map[string]string `json:"headers,omitempty"` // additional HTTP headers for provider verification/routing
}

type GatewayConfig struct {
	Host   string       `json:"host"`
	Port   int          `json:"port"`
	Auth   AuthConfig   `json:"auth"`
	Reload ReloadConfig `json:"reload"`
}

type AuthConfig struct {
	Token string `json:"token"`
}

type ReloadConfig struct {
	Mode string `json:"mode"` // "hybrid", "manual", "auto-restart"
}

type AgentsConfig struct {
	List []AgentConfig `json:"list"`
}

type AgentConfig struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	Description         string       `json:"description,omitempty"`
	Workspace           string       `json:"workspace"`
	Model               string       `json:"model"`
	Fallbacks           []string     `json:"fallbacks"`
	Sandbox             string       `json:"sandbox"`                 // "none", "docker", "namespace"
	MaxTurns            int          `json:"maxTurns,omitempty"`      // max tool-use loop iterations (0 = default 500)
	SystemPrompt        string       `json:"system_prompt,omitempty"` // agent-specific instructions appended to the runtime prompt
	Tools               ToolPolicy   `json:"tools"`
	Cron                []CronConfig `json:"cron,omitempty"`
	Entry               bool         `json:"entry,omitempty"`
	Tags                []string     `json:"tags,omitempty"`
	PrivateSkillsDir    string       `json:"privateSkillsDir,omitempty"`
	InheritSharedSkills bool         `json:"inheritSharedSkills,omitempty"`
	PrivateMCPsDir      string       `json:"privateMCPsDir,omitempty"`
	InheritSharedMCPs   bool         `json:"inheritSharedMCPs,omitempty"`
}

type CronConfig struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"` // duration string: "30m", "1h", "24h"
	Prompt   string `json:"prompt"`
}

type ToolPolicy struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

type Binding struct {
	AgentID string       `json:"agentId"`
	Match   BindingMatch `json:"match"`
}

type BindingMatch struct {
	Channel   string     `json:"channel,omitempty"`
	AccountID string     `json:"accountId,omitempty"`
	ChatType  string     `json:"chatType,omitempty"`
	Peer      *PeerMatch `json:"peer,omitempty"`
}

type PeerMatch struct {
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind,omitempty"`
}

type ChannelsConfig struct {
	Telegram TelegramConfig `json:"telegram"`
	WhatsApp WhatsAppConfig `json:"whatsapp"`
	CLI      CLIConfig      `json:"cli"`
}

type WhatsAppConfig struct {
	PhoneNumber    string   `json:"phone_number"`    // for display/identification only
	DBPath         string   `json:"db_path"`         // SQLite path for device state (default: <project>/anyai/whatsapp.db)
	AllowedSenders []string `json:"allowed_senders"` // phone numbers or JIDs allowed to send messages (empty = allow all)
}

type TelegramConfig struct {
	Token string `json:"token"`
	Mode  string `json:"mode"` // "polling" or "webhook"
}

type CLIConfig struct {
	Enabled     bool `json:"enabled"`
	Interactive bool `json:"interactive"`
}

type HeartbeatConfig struct {
	Interval string `json:"interval"`
	Enabled  bool   `json:"enabled"`
}

type MemoryConfig struct {
	Enabled           bool                    `json:"enabled"`
	EmbeddingProvider string                  `json:"embeddingProvider"`
	EmbeddingModel    string                  `json:"embeddingModel"`
	MaxEntries        int                     `json:"maxEntries"`
	Dir               string                  `json:"dir,omitempty"`
	AutoCapture       MemoryAutoCaptureConfig `json:"autoCapture,omitempty"`
	Inject            MemoryInjectConfig      `json:"inject,omitempty"`
}

type MemoryAutoCaptureConfig struct {
	Enabled              bool    `json:"enabled,omitempty"`
	OnSessionEnd         bool    `json:"onSessionEnd,omitempty"`
	OnAgentCallComplete  bool    `json:"onAgentCallComplete,omitempty"`
	OnUserConfirmed      bool    `json:"onUserConfirmed,omitempty"`
	OnHighValueDecision  bool    `json:"onHighValueDecision,omitempty"`
	MinImportance        float64 `json:"minImportance,omitempty"`
	CandidateTTL         string  `json:"candidateTTL,omitempty"`
	EpisodicTTL          string  `json:"episodicTTL,omitempty"`
	CleanupInterval      string  `json:"cleanupInterval,omitempty"`
	PromoteMinImportance float64 `json:"promoteMinImportance,omitempty"`
}

type MemoryInjectConfig struct {
	MaxItems int `json:"maxItems,omitempty"`
}

type RuntimeConfig struct {
	IdleTimeoutMS int                    `json:"idleTimeoutMs,omitempty"`
	AgentCall     AgentCallRuntimeConfig `json:"agentCall,omitempty"`
	Tools         ToolRuntimeConfig      `json:"tools,omitempty"`
	Sessions      SessionRuntimeConfig   `json:"sessions,omitempty"`
}

type LoggingConfig struct {
	FileLevel      string            `json:"fileLevel,omitempty"`
	StderrLevel    string            `json:"stderrLevel,omitempty"`
	WhatsMeowLevel string            `json:"whatsMeowLevel,omitempty"`
	MirrorStderr   *bool             `json:"mirrorStderr,omitempty"`
	Rotation       LogRotationConfig `json:"rotation,omitempty"`
}

type LogRotationConfig struct {
	Filename   string `json:"filename,omitempty"`
	MaxBytes   int64  `json:"maxBytes,omitempty"`
	MaxBackups int    `json:"maxBackups,omitempty"`
}

type AgentCallRuntimeConfig struct {
	DepthLimit  int `json:"depthLimit,omitempty"`
	MaxParallel int `json:"maxParallel,omitempty"`
}

type ToolRuntimeConfig struct {
	MaxAttempts    int                     `json:"maxAttempts,omitempty"`
	RetryBackoffMS int                     `json:"retryBackoffMS,omitempty"`
	LoopDetection  ToolLoopDetectionConfig `json:"loopDetection,omitempty"`
	Preflight      ToolPreflightConfig     `json:"preflight,omitempty"`
}

type ToolPreflightConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (c ToolPreflightConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type SessionRuntimeConfig struct {
	QueueMode         string                  `json:"queueMode,omitempty"`
	QueueDebounceMS   int                     `json:"queueDebounceMS,omitempty"`
	QueueMaxPending   int                     `json:"queueMaxPending,omitempty"`
	QueueDropPolicy   string                  `json:"queueDropPolicy,omitempty"`
	Compaction        SessionCompactionConfig `json:"compaction,omitempty"`
	TranscriptHygiene TranscriptHygieneConfig `json:"transcriptHygiene,omitempty"`
	IncompleteTurn    IncompleteTurnConfig    `json:"incompleteTurn,omitempty"`
}

type SessionCompactionConfig struct {
	Enabled              *bool  `json:"enabled,omitempty"`
	TriggerMode          string `json:"triggerMode,omitempty"`
	EntryThreshold       int    `json:"entryThreshold,omitempty"`
	TokenThreshold       int    `json:"tokenThreshold,omitempty"`
	KeepRecentUserTurns  int    `json:"keepRecentUserTurns,omitempty"`
	KeepRecentUserTokens int    `json:"keepRecentUserTokens,omitempty"`
	SummaryMaxTokens     int    `json:"summaryMaxTokens,omitempty"`
}

func (c SessionCompactionConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type TranscriptHygieneConfig struct {
	Enabled                   *bool `json:"enabled,omitempty"`
	MergeConsecutiveUserTurns *bool `json:"mergeConsecutiveUserTurns,omitempty"`
	RepairToolPairs           *bool `json:"repairToolPairs,omitempty"`
	DropOrphanToolResults     *bool `json:"dropOrphanToolResults,omitempty"`
	TreatMetaAsSummaryContext *bool `json:"treatMetaAsSummaryContext,omitempty"`
}

func (c TranscriptHygieneConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c TranscriptHygieneConfig) MergeConsecutiveUserTurnsValue() bool {
	if c.MergeConsecutiveUserTurns == nil {
		return true
	}
	return *c.MergeConsecutiveUserTurns
}

func (c TranscriptHygieneConfig) RepairToolPairsValue() bool {
	if c.RepairToolPairs == nil {
		return true
	}
	return *c.RepairToolPairs
}

func (c TranscriptHygieneConfig) DropOrphanToolResultsValue() bool {
	if c.DropOrphanToolResults == nil {
		return true
	}
	return *c.DropOrphanToolResults
}

func (c TranscriptHygieneConfig) TreatMetaAsSummaryContextValue() bool {
	if c.TreatMetaAsSummaryContext == nil {
		return true
	}
	return *c.TreatMetaAsSummaryContext
}

type IncompleteTurnConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (c IncompleteTurnConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type ToolLoopDetectionConfig struct {
	Enabled          *bool `json:"enabled,omitempty"`
	HistorySize      int   `json:"historySize,omitempty"`
	WarningThreshold int   `json:"warningThreshold,omitempty"`
	BlockThreshold   int   `json:"blockThreshold,omitempty"`
}

func (c ToolLoopDetectionConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type CortexConfig struct {
	Enabled  bool   `json:"enabled"`
	DBPath   string `json:"dbPath"`   // path to brain.db (default: <project>/anyai/brain.db)
	LLMModel string `json:"llmModel"` // model for extraction/decomposition (default: gpt-4o-mini)
}

type SecurityConfig struct {
	ExecApprovals ExecApprovalsConfig `json:"execApprovals"`
	DMPolicy      DMPolicyConfig      `json:"dmPolicy"`
	GroupPolicy   GroupPolicyConfig   `json:"groupPolicy"`
}

type ExecApprovalsConfig struct {
	Level     string   `json:"level"` // "deny", "allowlist", "full"
	Allowlist []string `json:"allowlist"`
}

type DMPolicyConfig struct {
	UnknownSenders string `json:"unknownSenders"` // "ignore", "respond", "notify"
}

type GroupPolicyConfig struct {
	RequireMention bool `json:"requireMention"`
}

// DefaultDataDir returns the default runtime data directory rooted at the
// current working directory.
func DefaultDataDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "anyai"
	}
	return filepath.Join(cwd, "anyai")
}

// ProjectDataDir returns the project-local runtime data directory.
// When projectRoot is empty, it falls back to the default runtime directory.
func ProjectDataDir(projectRoot string) string {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return DefaultDataDir()
	}
	if absRoot, err := filepath.Abs(projectRoot); err == nil {
		projectRoot = absRoot
	}
	return filepath.Join(projectRoot, "anyai")
}

// RuntimeDataDir returns the runtime data directory for this config.
// Project-backed runs use <project>/anyai. If the project root is not set yet,
// it falls back to the config directory and finally to the default runtime dir.
func (c *Config) RuntimeDataDir() string {
	if c == nil {
		return DefaultDataDir()
	}
	if dir := strings.TrimSpace(c.ProjectRoot); dir != "" {
		return ProjectDataDir(dir)
	}
	if dir := strings.TrimSpace(c.ProjectConfigDir); dir != "" {
		return ProjectDataDir(dir)
	}
	if path := strings.TrimSpace(c.Path()); path != "" {
		return ProjectDataDir(filepath.Dir(path))
	}
	return DefaultDataDir()
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	return filepath.Join(DefaultDataDir(), "anyai.json5")
}

// Load reads and parses a AnyAI config file. It supports JSON5 by
// stripping comments and trailing commas before unmarshalling.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			cfg.path = path
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Warn if config file is readable by group or others (may expose API keys).
	// Skip on Windows where Unix file permissions are not meaningful.
	if runtime.GOOS != "windows" {
		if info, statErr := os.Stat(path); statErr == nil {
			mode := info.Mode().Perm()
			if mode&0o077 != 0 {
				runtimelogging.Warn("config file has overly permissive permissions",
					"path", path,
					"mode", fmt.Sprintf("%04o", mode),
					"recommended", "0600",
					"fix", fmt.Sprintf("chmod 600 %s", path),
				)
			}
		}
	}

	// Strip JSON5 features (single-line comments, trailing commas) for stdlib JSON parsing.
	cleaned := stripJSON5(string(data))

	var cfg Config
	if err := json.Unmarshal([]byte(cleaned), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.path = path

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Gateway: GatewayConfig{
			Host:   defaultGatewayHost,
			Port:   defaultGatewayPort,
			Reload: ReloadConfig{Mode: defaultReloadMode},
		},
		Providers: map[string]ProviderConfig{},
		Agents: AgentsConfig{
			List: []AgentConfig{
				{
					ID:        "default",
					Name:      "Assistant",
					Workspace: filepath.Join(DefaultDataDir(), "workspace-default"),
					Model:     "anthropic/claude-sonnet-4-5-20250514",
					Sandbox:   "none",
					Tools: ToolPolicy{
						Allow: []string{"read_file", "write_file", "edit_file", "bash", "web_fetch", "web_search", "browser", "send_message", "cron"},
					},
				},
			},
		},
		Bindings: []Binding{
			{AgentID: "default", Match: BindingMatch{Channel: "cli"}},
		},
		Channels: ChannelsConfig{
			CLI: CLIConfig{Enabled: true, Interactive: true},
		},
		Heartbeat: HeartbeatConfig{
			Interval: defaultHeartbeatInterval,
			Enabled:  false,
		},
		Memory: MemoryConfig{
			Enabled: true,
			Dir:     filepath.Join(DefaultDataDir(), "memory"),
			AutoCapture: MemoryAutoCaptureConfig{
				MinImportance:        defaultMemoryMinImportance,
				CandidateTTL:         defaultMemoryCandidateTTL,
				EpisodicTTL:          defaultMemoryEpisodicTTL,
				CleanupInterval:      defaultMemoryCleanupInterval,
				PromoteMinImportance: defaultMemoryPromoteImportance,
			},
			Inject: MemoryInjectConfig{
				MaxItems: defaultMemoryInjectMaxItems,
			},
		},
		Runtime: RuntimeConfig{
			IdleTimeoutMS: defaultRuntimeIdleTimeoutMS,
			AgentCall: AgentCallRuntimeConfig{
				DepthLimit:  defaultAgentCallDepthLimit,
				MaxParallel: defaultAgentCallMaxParallel,
			},
			Tools: ToolRuntimeConfig{
				MaxAttempts:    defaultToolMaxAttempts,
				RetryBackoffMS: defaultToolRetryBackoffMS,
				LoopDetection: ToolLoopDetectionConfig{
					Enabled:          boolPtr(true),
					HistorySize:      defaultToolLoopHistorySize,
					WarningThreshold: defaultToolLoopWarningThreshold,
					BlockThreshold:   defaultToolLoopBlockThreshold,
				},
				Preflight: ToolPreflightConfig{
					Enabled: boolPtr(true),
				},
			},
			Sessions: SessionRuntimeConfig{
				QueueMode:       defaultSessionQueueMode,
				QueueDebounceMS: defaultSessionQueueDebounceMS,
				QueueMaxPending: defaultSessionQueueMaxPending,
				QueueDropPolicy: defaultSessionQueueDropPolicy,
				Compaction: SessionCompactionConfig{
					Enabled:              boolPtr(true),
					TriggerMode:          defaultSessionCompactionTriggerMode,
					EntryThreshold:       defaultSessionCompactionEntryThresh,
					TokenThreshold:       defaultSessionCompactionTokenThresh,
					KeepRecentUserTurns:  defaultSessionCompactionKeepTurns,
					KeepRecentUserTokens: defaultSessionCompactionKeepTokens,
					SummaryMaxTokens:     defaultSessionCompactionSummaryTokens,
				},
				TranscriptHygiene: TranscriptHygieneConfig{
					Enabled:                   boolPtr(true),
					MergeConsecutiveUserTurns: boolPtr(true),
					RepairToolPairs:           boolPtr(true),
					DropOrphanToolResults:     boolPtr(true),
					TreatMetaAsSummaryContext: boolPtr(true),
				},
				IncompleteTurn: IncompleteTurnConfig{
					Enabled: boolPtr(true),
				},
			},
		},
		Logging: LoggingConfig{
			FileLevel:      defaultLogFileLevel,
			StderrLevel:    defaultLogStderrLevel,
			WhatsMeowLevel: defaultLogWhatsMeowLevel,
			Rotation: LogRotationConfig{
				Filename:   defaultLogFilename,
				MaxBytes:   defaultLogMaxBytes,
				MaxBackups: defaultLogMaxBackups,
			},
		},
		Security: SecurityConfig{
			ExecApprovals: ExecApprovalsConfig{
				Level:     defaultExecApprovalLevel,
				Allowlist: append([]string(nil), defaultExecAllowlist...),
			},
			DMPolicy:    DMPolicyConfig{UnknownSenders: defaultDMUnknownSenderPolicy},
			GroupPolicy: GroupPolicyConfig{RequireMention: true},
		},
	}
}

// GetProvider returns the provider config for the given name, falling back to
// env vars if not explicitly configured.
func (c *Config) GetProvider(name string) ProviderConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.Providers[name]; ok {
		return p
	}
	return ProviderConfig{}
}

// Validate checks the config for required fields and applies defaults.
func (c *Config) Validate() error {
	if c.Providers == nil {
		c.Providers = map[string]ProviderConfig{}
	}
	if c.Gateway.Port == 0 {
		c.Gateway.Port = defaultGatewayPort
	}
	if c.Gateway.Host == "" {
		c.Gateway.Host = defaultGatewayHost
	}
	if c.Gateway.Reload.Mode == "" {
		c.Gateway.Reload.Mode = defaultReloadMode
	}
	if strings.TrimSpace(c.Heartbeat.Interval) == "" {
		c.Heartbeat.Interval = defaultHeartbeatInterval
	}
	if strings.TrimSpace(c.Security.ExecApprovals.Level) == "" {
		c.Security.ExecApprovals.Level = defaultExecApprovalLevel
	}
	if len(c.Security.ExecApprovals.Allowlist) == 0 {
		c.Security.ExecApprovals.Allowlist = append([]string(nil), defaultExecAllowlist...)
	}
	if strings.TrimSpace(c.Security.DMPolicy.UnknownSenders) == "" {
		c.Security.DMPolicy.UnknownSenders = defaultDMUnknownSenderPolicy
	}
	if c.Runtime.IdleTimeoutMS == 0 {
		c.Runtime.IdleTimeoutMS = defaultRuntimeIdleTimeoutMS
	}
	if c.Runtime.IdleTimeoutMS < 0 {
		return fmt.Errorf("runtime.idleTimeoutMs must be >= 0")
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
	if c.Runtime.Tools.Preflight.Enabled == nil {
		c.Runtime.Tools.Preflight.Enabled = boolPtr(true)
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
	if strings.TrimSpace(c.Runtime.Sessions.QueueMode) == "" {
		c.Runtime.Sessions.QueueMode = defaultSessionQueueMode
	}
	switch c.Runtime.Sessions.QueueMode {
	case "collect", "followup", "interrupt":
	default:
		return fmt.Errorf("runtime.sessions.queueMode must be one of collect, followup, interrupt")
	}
	if c.Runtime.Sessions.QueueDebounceMS == 0 {
		c.Runtime.Sessions.QueueDebounceMS = defaultSessionQueueDebounceMS
	}
	if c.Runtime.Sessions.QueueDebounceMS < 0 {
		return fmt.Errorf("runtime.sessions.queueDebounceMS must be >= 0")
	}
	if c.Runtime.Sessions.QueueMaxPending == 0 {
		c.Runtime.Sessions.QueueMaxPending = defaultSessionQueueMaxPending
	}
	if c.Runtime.Sessions.QueueMaxPending < 0 {
		return fmt.Errorf("runtime.sessions.queueMaxPending must be >= 0")
	}
	if strings.TrimSpace(c.Runtime.Sessions.QueueDropPolicy) == "" {
		c.Runtime.Sessions.QueueDropPolicy = defaultSessionQueueDropPolicy
	}
	switch c.Runtime.Sessions.QueueDropPolicy {
	case "summarize", "drop_oldest", "drop_newest":
	default:
		return fmt.Errorf("runtime.sessions.queueDropPolicy must be one of summarize, drop_oldest, drop_newest")
	}
	if c.Runtime.Sessions.Compaction.Enabled == nil {
		c.Runtime.Sessions.Compaction.Enabled = boolPtr(true)
	}
	if strings.TrimSpace(c.Runtime.Sessions.Compaction.TriggerMode) == "" {
		c.Runtime.Sessions.Compaction.TriggerMode = defaultSessionCompactionTriggerMode
	}
	switch c.Runtime.Sessions.Compaction.TriggerMode {
	case "entry_count", "token_estimate":
	default:
		return fmt.Errorf("runtime.sessions.compaction.triggerMode must be one of entry_count, token_estimate")
	}
	if c.Runtime.Sessions.Compaction.EntryThreshold == 0 {
		c.Runtime.Sessions.Compaction.EntryThreshold = defaultSessionCompactionEntryThresh
	}
	if c.Runtime.Sessions.Compaction.EntryThreshold < 0 {
		return fmt.Errorf("runtime.sessions.compaction.entryThreshold must be >= 0")
	}
	if c.Runtime.Sessions.Compaction.TokenThreshold == 0 {
		c.Runtime.Sessions.Compaction.TokenThreshold = defaultSessionCompactionTokenThresh
	}
	if c.Runtime.Sessions.Compaction.TokenThreshold < 0 {
		return fmt.Errorf("runtime.sessions.compaction.tokenThreshold must be >= 0")
	}
	if c.Runtime.Sessions.Compaction.KeepRecentUserTurns == 0 {
		c.Runtime.Sessions.Compaction.KeepRecentUserTurns = defaultSessionCompactionKeepTurns
	}
	if c.Runtime.Sessions.Compaction.KeepRecentUserTurns < 0 {
		return fmt.Errorf("runtime.sessions.compaction.keepRecentUserTurns must be >= 0")
	}
	if c.Runtime.Sessions.Compaction.KeepRecentUserTokens == 0 {
		c.Runtime.Sessions.Compaction.KeepRecentUserTokens = defaultSessionCompactionKeepTokens
	}
	if c.Runtime.Sessions.Compaction.KeepRecentUserTokens < 0 {
		return fmt.Errorf("runtime.sessions.compaction.keepRecentUserTokens must be >= 0")
	}
	if c.Runtime.Sessions.Compaction.SummaryMaxTokens == 0 {
		c.Runtime.Sessions.Compaction.SummaryMaxTokens = defaultSessionCompactionSummaryTokens
	}
	if c.Runtime.Sessions.Compaction.SummaryMaxTokens < 0 {
		return fmt.Errorf("runtime.sessions.compaction.summaryMaxTokens must be >= 0")
	}
	if c.Runtime.Sessions.TranscriptHygiene.Enabled == nil {
		c.Runtime.Sessions.TranscriptHygiene.Enabled = boolPtr(true)
	}
	if c.Runtime.Sessions.TranscriptHygiene.MergeConsecutiveUserTurns == nil {
		c.Runtime.Sessions.TranscriptHygiene.MergeConsecutiveUserTurns = boolPtr(true)
	}
	if c.Runtime.Sessions.TranscriptHygiene.RepairToolPairs == nil {
		c.Runtime.Sessions.TranscriptHygiene.RepairToolPairs = boolPtr(true)
	}
	if c.Runtime.Sessions.TranscriptHygiene.DropOrphanToolResults == nil {
		c.Runtime.Sessions.TranscriptHygiene.DropOrphanToolResults = boolPtr(true)
	}
	if c.Runtime.Sessions.TranscriptHygiene.TreatMetaAsSummaryContext == nil {
		c.Runtime.Sessions.TranscriptHygiene.TreatMetaAsSummaryContext = boolPtr(true)
	}
	if c.Runtime.Sessions.IncompleteTurn.Enabled == nil {
		c.Runtime.Sessions.IncompleteTurn.Enabled = boolPtr(true)
	}
	if c.Memory.Inject.MaxItems == 0 {
		c.Memory.Inject.MaxItems = defaultMemoryInjectMaxItems
	}
	if strings.TrimSpace(c.Memory.Dir) == "" {
		c.Memory.Dir = filepath.Join(c.RuntimeDataDir(), "memory")
	}
	applyMemoryAutoCaptureDefaults(&c.Memory)
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
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "memory.autoCapture.candidateTTL", value: c.Memory.AutoCapture.CandidateTTL},
		{name: "memory.autoCapture.episodicTTL", value: c.Memory.AutoCapture.EpisodicTTL},
		{name: "memory.autoCapture.cleanupInterval", value: c.Memory.AutoCapture.CleanupInterval},
	} {
		if _, err := time.ParseDuration(item.value); err != nil {
			return fmt.Errorf("%s must be a valid duration: %w", item.name, err)
		}
	}
	if c.Runtime.Tools.MaxAttempts < 0 {
		return fmt.Errorf("runtime.tools.maxAttempts must be >= 0")
	}
	if c.Runtime.Tools.RetryBackoffMS < 0 {
		return fmt.Errorf("runtime.tools.retryBackoffMS must be >= 0")
	}
	if c.Runtime.Tools.LoopDetection.HistorySize < 0 {
		return fmt.Errorf("runtime.tools.loopDetection.historySize must be >= 0")
	}
	if c.Runtime.Tools.LoopDetection.WarningThreshold < 0 {
		return fmt.Errorf("runtime.tools.loopDetection.warningThreshold must be >= 0")
	}
	if c.Runtime.Tools.LoopDetection.BlockThreshold < 0 {
		return fmt.Errorf("runtime.tools.loopDetection.blockThreshold must be >= 0")
	}
	if !isSupportedLogLevel(c.Logging.FileLevel) {
		return fmt.Errorf("logging.fileLevel must be one of debug, info, warn, error")
	}
	if !isSupportedLogLevel(c.Logging.StderrLevel) {
		return fmt.Errorf("logging.stderrLevel must be one of debug, info, warn, error")
	}
	if !isSupportedLogLevel(c.Logging.WhatsMeowLevel) {
		return fmt.Errorf("logging.whatsMeowLevel must be one of debug, info, warn, error")
	}
	if c.Logging.Rotation.MaxBytes < 0 {
		return fmt.Errorf("logging.rotation.maxBytes must be >= 0")
	}
	if c.Logging.Rotation.MaxBackups < 0 {
		return fmt.Errorf("logging.rotation.maxBackups must be >= 0")
	}

	if len(c.Agents.List) == 0 {
		return errors.New("at least one agent must be configured")
	}

	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		if a.ID == "" {
			return fmt.Errorf("agent at index %d has no id", i)
		}
		if a.Model == "" {
			return fmt.Errorf("agent %q has no model", a.ID)
		}
		if a.Workspace == "" {
			a.Workspace = filepath.Join(DefaultDataDir(), "workspace-"+a.ID)
		}
		if a.Sandbox == "" {
			a.Sandbox = "none"
		}
	}

	return nil
}

func applyMemoryAutoCaptureDefaults(cfg *MemoryConfig) {
	if cfg == nil {
		return
	}

	hasTriggers := cfg.AutoCapture.OnSessionEnd ||
		cfg.AutoCapture.OnAgentCallComplete ||
		cfg.AutoCapture.OnUserConfirmed ||
		cfg.AutoCapture.OnHighValueDecision

	if cfg.AutoCapture.Enabled || hasTriggers {
		cfg.Enabled = true
		if hasTriggers {
			cfg.AutoCapture.Enabled = true
		}
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func CloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	cloned := *v
	return &cloned
}

func normalizeLogLevel(value, defaultValue string) string {
	level := strings.TrimSpace(strings.ToLower(value))
	if level == "" {
		level = strings.TrimSpace(strings.ToLower(defaultValue))
	}
	switch level {
	case "warning":
		return "warn"
	default:
		return level
	}
}

func isSupportedLogLevel(value string) bool {
	switch normalizeLogLevel(value, "") {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

// GetAgent returns the agent config for the given ID.
func (c *Config) GetAgent(id string) (*AgentConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i := range c.Agents.List {
		if c.Agents.List[i].ID == id {
			return &c.Agents.List[i], true
		}
	}
	return nil, false
}

// Path returns the file path this config was loaded from.
func (c *Config) Path() string {
	return c.path
}

// SetPath sets the file path for saving.
func (c *Config) SetPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = path
}

// Save writes the config to disk as formatted JSON.
func (c *Config) Save() error {
	c.mu.RLock()
	path := c.path
	c.mu.RUnlock()

	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// stripJSON5 removes single-line comments and trailing commas from JSON5
// to produce valid JSON for the stdlib parser.
func stripJSON5(s string) string {
	var b strings.Builder
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip full-line comments
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Remove inline comments (naive: doesn't handle // inside strings,
		// but sufficient for typical config files)
		if idx := strings.Index(line, "//"); idx >= 0 {
			// Only strip if not inside a quoted string
			if !inString(line, idx) {
				line = line[:idx]
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}

	// Remove trailing commas before } or ]
	result := b.String()
	result = removeTrailingCommas(result)
	return result
}

// inString checks if position pos in line is inside a JSON string literal.
func inString(line string, pos int) bool {
	inStr := false
	for i := 0; i < pos; i++ {
		if line[i] == '"' && (i == 0 || line[i-1] != '\\') {
			inStr = !inStr
		}
	}
	return inStr
}

// removeTrailingCommas removes commas that appear before } or ] (with optional whitespace).
func removeTrailingCommas(s string) string {
	runes := []rune(s)
	var out []rune
	for i := 0; i < len(runes); i++ {
		if runes[i] == ',' {
			// Look ahead past whitespace for } or ]
			j := i + 1
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t' || runes[j] == '\n' || runes[j] == '\r') {
				j++
			}
			if j < len(runes) && (runes[j] == '}' || runes[j] == ']') {
				continue // skip this trailing comma
			}
		}
		out = append(out, runes[i])
	}
	return string(out)
}
