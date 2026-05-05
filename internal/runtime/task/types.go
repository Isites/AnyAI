package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/Isites/anyai/internal/runtime/contract"
	"github.com/Isites/anyai/internal/runtime/llm"
)

type Kind string

const (
	KindAgent   Kind = "agent"
	KindTool    Kind = "tool"
	KindProcess Kind = "process"
)

type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

const (
	MetadataVisibilityKey = "visibility"
	VisibilityPublic      = "public"
	VisibilityInternal    = "internal"
)

// Contract is an alias for contract.Contract.
// Use contract.Contract for the canonical definition.
type Contract = contract.Contract

// Callback is invoked once when a submitted task settles. Callers should treat
// it as the continuation point instead of manually waiting on the task.
type Callback func(context.Context, Completion)

// Completion is the normalized terminal payload delivered by the task runtime
// to a Callback.
type Completion struct {
	TaskID string
	Record Record
	Result Result
}

// Spec is one doTask submission. Results are delivered through OnComplete.
type Spec struct {
	Kind          Kind
	AgentID       string
	RunID         string
	SessionID     string
	ParentTaskID  string
	IdleTimeoutMS int
	Input         string
	TargetAgent   string
	ToolName      string
	ProcessName   string
	Contract      Contract
	Metadata      map[string]any
	OnComplete    Callback
}

type Result struct {
	Status     Status
	Summary    string
	Error      string
	SessionID  string
	Metadata   map[string]any
	Images     []llm.ImageContent
}

type JoinSpec struct {
	Count       int
	MaxParallel int
}

type JoinChildResult struct {
	Record       Record
	StartedOrder int
	FinishOrder  int
	StartedAt    time.Time
	CompletedAt  time.Time
}

type JoinResult struct {
	MaxParallel int
	Results     []JoinChildResult
}

type Record struct {
	ID             string         `json:"id"`
	Kind           Kind           `json:"kind"`
	Status         Status         `json:"status"`
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
	Contract       Contract       `json:"contract,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	StartedAt      time.Time      `json:"started_at,omitempty"`
	LastActivityAt time.Time      `json:"last_activity_at,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at,omitempty"`
	CompletedAt    time.Time      `json:"completed_at,omitempty"`
}

type Info = Record

func NewID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "task_fallback"
	}
	return "task_" + hex.EncodeToString(buf)
}
