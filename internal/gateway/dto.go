package gateway

import (
	"time"

	"github.com/Isites/anyai/internal/config"
)

type InputBlock struct {
	ID           string         `json:"id,omitempty"`
	Type         string         `json:"type"`
	Name         string         `json:"name,omitempty"`
	Text         string         `json:"text,omitempty"`
	Path         string         `json:"path,omitempty"`
	AttachmentID string         `json:"attachment_id,omitempty"`
	URL          string         `json:"url,omitempty"`
	MimeType     string         `json:"mime_type,omitempty"`
	Data         []byte         `json:"data,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

type IngressRequest struct {
	Channel       string
	RequestedID   string
	SenderID      string
	AccountID     string
	ChatType      ChatType
	Text          string
	MessageID     string
	Inputs        []InputBlock
	SessionID     string
	SessionPrefix string
}

type ManagedRun struct {
	RunID         string
	AgentID       string
	SessionID     string
	Model         string
	Events        <-chan Event
	Cancel        func()
	OwnsLifecycle bool
}

type RunStatus string

const (
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusAborted   RunStatus = "aborted"
)

const (
	EventRunAccepted      = "run.accepted"
	EventRunQueued        = "run.queued"
	EventRunRouted        = "run.routed"
	EventRunRouteRejected = "run.route.rejected"
	EventRunStarted       = "run.started"
	EventRunActivity      = "run.activity"
	EventRunIncomplete    = "run.incomplete"
	EventRunFallbackReply = "run.fallback_reply"
	EventRunCompleted     = "run.completed"
	EventRunFailed        = "run.failed"
	EventRunAborted       = "run.aborted"

	EventTextDelta = "text.delta"

	EventToolRetrying       = "tool.retrying"
	EventToolWarning        = "tool.warning"
	EventToolCompleted      = "tool.completed"
	EventToolFailed         = "tool.failed"
	EventToolFanoutComplete = "tool.fanout.completed"
	EventToolCallRequested  = "tool.call.requested"
	EventToolCallStarted    = "tool.call.started"

	EventAgentCallStarted   = "agent.call.started"
	EventAgentCallSubmitted = "agent.call.submitted"
	EventAgentCallCompleted = "agent.call.completed"
	EventAgentCallFailed    = "agent.call.failed"
)

type Run struct {
	ID                string    `json:"id"`
	TraceID           string    `json:"trace_id,omitempty"`
	TraceNodeID       string    `json:"trace_node_id,omitempty"`
	ParentTraceNodeID string    `json:"parent_trace_node_id,omitempty"`
	ParentAgentID     string    `json:"parent_agent_id,omitempty"`
	AgentID           string    `json:"agent_id"`
	SessionID         string    `json:"session_id"`
	Model             string    `json:"model"`
	Channel           string    `json:"channel,omitempty"`
	Input             string    `json:"input,omitempty"`
	Output            string    `json:"output,omitempty"`
	Error             string    `json:"error,omitempty"`
	Status            RunStatus `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	StartedAt         time.Time `json:"started_at"`
	CompletedAt       time.Time `json:"completed_at,omitempty"`
}

type Event struct {
	SchemaVersion     int            `json:"schema_version,omitempty"`
	Sequence          int            `json:"sequence"`
	RunID             string         `json:"run_id"`
	TraceID           string         `json:"trace_id,omitempty"`
	TraceNodeID       string         `json:"trace_node_id,omitempty"`
	ParentTraceNodeID string         `json:"parent_trace_node_id,omitempty"`
	AgentID           string         `json:"agent_id"`
	SessionID         string         `json:"session_id"`
	Name              string         `json:"name"`
	Timestamp         time.Time      `json:"timestamp"`
	Payload           map[string]any `json:"payload,omitempty"`
}

type RunTree struct {
	Runs   []Run   `json:"runs"`
	Events []Event `json:"events"`
}

type RunNode struct {
	Run      Run       `json:"run"`
	Events   []Event   `json:"events,omitempty"`
	Children []RunNode `json:"children,omitempty"`
}

type SessionInfo struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"createdAt"`
	LastActivity time.Time `json:"lastActivity"`
	EntryCount   int       `json:"entryCount"`
}

type SessionView struct {
	AgentID string           `json:"agent_id"`
	ID      string           `json:"id"`
	History []map[string]any `json:"history"`
	Events  []Event          `json:"events,omitempty"`
}

type MemoryLayer string

const (
	MemoryLayerCandidates MemoryLayer = "candidates"
	MemoryLayerEpisodic   MemoryLayer = "episodic"
	MemoryLayerLongTerm   MemoryLayer = "long-term"
)

type MemoryScope struct {
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type MemoryStats struct {
	Total              int       `json:"total"`
	Active             int       `json:"active"`
	Episodic           int       `json:"episodic"`
	Candidates         int       `json:"candidates"`
	LongTerm           int       `json:"long_term"`
	Expired            int       `json:"expired"`
	LastReindexAt      time.Time `json:"last_reindex_at,omitempty"`
	LastPromotionAt    time.Time `json:"last_promotion_at,omitempty"`
	LastPromotionCount int       `json:"last_promotion_count,omitempty"`
	LastCleanupAt      time.Time `json:"last_cleanup_at,omitempty"`
	LastCleanupRemoved int       `json:"last_cleanup_removed,omitempty"`
}

type MemoryEntry struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Content  string            `json:"content"`
	FilePath string            `json:"file_path,omitempty"`
	ModTime  time.Time         `json:"mod_time,omitempty"`
	Layer    MemoryLayer       `json:"layer"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type MemorySearchMatch struct {
	Entry        MemoryEntry `json:"entry"`
	Score        float64     `json:"score"`
	MatchedTerms []string    `json:"matched_terms,omitempty"`
}

type Task struct {
	ID             string         `json:"id"`
	Kind           string         `json:"kind"`
	Status         string         `json:"status"`
	AgentID        string         `json:"agent_id,omitempty"`
	RunID          string         `json:"run_id,omitempty"`
	SessionID      string         `json:"session_id,omitempty"`
	ParentTaskID   string         `json:"parent_task_id,omitempty"`
	IdleTimeoutMS  int            `json:"idle_timeout_ms,omitempty"`
	Input          string         `json:"input,omitempty"`
	TargetAgent    string         `json:"target_agent,omitempty"`
	ToolName       string         `json:"tool,omitempty"`
	ProcessName    string         `json:"process,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	Error          string         `json:"error,omitempty"`
	Contract       any            `json:"contract,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	StartedAt      time.Time      `json:"started_at,omitempty"`
	LastActivityAt time.Time      `json:"last_activity_at,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at,omitempty"`
	CompletedAt    time.Time      `json:"completed_at,omitempty"`
}

type Job struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	Paused   bool   `json:"paused"`
}

type ToolMetadata struct {
	Name             string   `json:"name,omitempty"`
	TimeoutHintMS    int64    `json:"timeout_hint_ms,omitempty"`
	Effect           string   `json:"effect"`
	Tags             []string `json:"tags,omitempty"`
	AllowParallel    bool     `json:"allow_parallel,omitempty"`
	RequiresApproval bool     `json:"requires_approval,omitempty"`
}

type ToolDescriptor struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Metadata    ToolMetadata `json:"metadata"`
}

type SkillDescriptor struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Scope       string   `json:"scope"`
	Source      string   `json:"source,omitempty"`
}

type AgentResources struct {
	Agent           config.AgentConfig `json:"agent"`
	SharedSkills    []SkillDescriptor  `json:"shared_skills,omitempty"`
	PrivateSkills   []SkillDescriptor  `json:"private_skills,omitempty"`
	EffectiveSkills []SkillDescriptor  `json:"effective_skills,omitempty"`
	Tools           []ToolDescriptor   `json:"tools,omitempty"`
}

type ResourceCatalog struct {
	SharedSkills []SkillDescriptor `json:"shared_skills,omitempty"`
	Agents       []AgentResources  `json:"agents,omitempty"`
}

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Attrs   string    `json:"attrs,omitempty"`
}
