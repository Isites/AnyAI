package runtimeport

import (
	"context"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/contract"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimememorylifecycle "github.com/Isites/anyai/internal/runtime/memory/lifecycle"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/tool"
	"github.com/Isites/anyai/internal/runtime/turn"
)

// Contract is an alias for contract.Contract.
// Use contract.Contract for the canonical definition.
type Contract = contract.Contract

type RunRequest struct {
	RunID         string
	AgentID       string
	Envelope      input.InputEnvelope
	SessionID     string
	Images        []llm.ImageContent
	TaskID        string
	ParentAgentID string
	Channel       string
	Contract      // embedded
}

type ManagedRun struct {
	RunID         string
	AgentID       string
	SessionID     string
	Model         string
	Events        <-chan runtimeevents.EventRecord
	Cancel        context.CancelFunc
	OwnsLifecycle bool
}

// SubmissionSurface is the runtime ingress surface. It is the only surface
// that accepts new agent/task work; projection and control surfaces below are
// read/control paths over already-submitted work.
type SubmissionSurface interface {
	StartManagedRun(ctx context.Context, req RunRequest) (*ManagedRun, error)
	StartIngressRun(ctx context.Context, req IngressRequest) (*ManagedRun, error)
	StartTextRun(
		ctx context.Context,
		channelName, agentID, senderID, accountID, sessionID, text string,
		chatType ChatType,
	) (*ManagedRun, error)
	// DoTask submits one runtime-managed task and returns its task ID. Callers
	// receive terminal results through task.Spec.OnComplete instead of waiting.
	DoTask(ctx context.Context, spec task.Spec) (string, error)
}

// MetadataReader exposes static runtime metadata assembled during startup.
// These values describe the current project/runtime shape rather than a
// specific run, session, task, or memory query.
type MetadataReader interface {
	Config() *config.Config
	Agents() []config.AgentConfig
	Resources() *runtimeresources.Catalog
	JobScheduler() tools.JobScheduler
	EventStorageDir() string
}

// RunProjectionReader exposes recorder-backed run and run-tree read models.
type RunProjectionReader interface {
	GetRun(runID string) (runtimeevents.RunRecord, bool)
	ListRuns() []runtimeevents.RunRecord
	ListRunEvents(runID string) []runtimeevents.EventRecord
	GetRunTree(runID string) (runtimeevents.RunTreeRecord, bool)
	RunTree(runID string) ([]runtimeevents.RunNode, bool)
}

// SessionProjectionReader exposes persisted session state and session-scoped
// event history.
type SessionProjectionReader interface {
	ListSessionEvents(agentID, sessionID string) []runtimeevents.EventRecord
	ListSessions(agentID string) ([]session.SessionInfo, error)
	LoadSession(agentID, sessionID string) (*session.Session, error)
}

// TaskProjectionReader exposes runtime task state.
type TaskProjectionReader interface {
	ListTasks() []task.Info
	GetTask(taskID string) (task.Info, bool)
}

// MemoryReader exposes runtime memory query paths.
type MemoryReader interface {
	MemoryStats() memory.Stats
	MemorySearch(query string, maxItems int, scope memory.SearchScope, layers ...memory.Layer) []memory.SearchMatch
	MemoryGet(id string, scope memory.SearchScope) (memory.Entry, bool)
}

// ProjectionReader is the raw runtime read model surface. Higher layers should
// prefer gateway replay surfaces instead of exposing these projection records
// directly to transport consumers.
type ProjectionReader interface {
	MetadataReader
	RunProjectionReader
	SessionProjectionReader
	TaskProjectionReader
	MemoryReader
}

// RunEventStreamSource exposes live run/run-tree event streams.
type RunEventStreamSource interface {
	SubscribeRun(runID string) (<-chan runtimeevents.EventRecord, func(), error)
	SubscribeRunTree(runID string) (<-chan runtimeevents.EventRecord, func(), error)
}

// SessionEventStreamSource exposes live session event streams.
type SessionEventStreamSource interface {
	SubscribeSession(agentID, sessionID string) (<-chan runtimeevents.EventRecord, func(), error)
}

// TaskEventStreamSource exposes live task event streams.
type TaskEventStreamSource interface {
	SubscribeTask(taskID string) (<-chan runtimeevents.EventRecord, func(), error)
}

// ProjectionStreamSource exposes raw live event streams derived from runtime
// projections. Gateway consumes these streams and turns them into replay
// surfaces for channel/http consumers.
type ProjectionStreamSource interface {
	RunEventStreamSource
	SessionEventStreamSource
	TaskEventStreamSource
}

// ProjectionController exposes mutating operations for projection-backed
// runtime state.
type ProjectionController interface {
	RebuildEventProjections() error
	CreateSession(agentID, requestedKey, prefix string) (string, error)
	DeleteSession(agentID, sessionID string) error
	CompactSession(agentID, sessionID string, keepEntries int) error
}

// MemoryController exposes memory maintenance commands.
type MemoryController interface {
	MemoryStaleCleanup(now time.Time) (int, error)
	MemoryReindex() (int, error)
	MemoryPromoteEligible(now time.Time) (int, error)
}

// ExecutionController exposes cancellation commands for active runtime work.
type ExecutionController interface {
	CancelRun(runID string) error
	CancelTask(taskID string) error
}

// RuntimeController exposes mutating operations against runtime-maintained
// state.
type RuntimeController interface {
	ProjectionController
	MemoryController
	ExecutionController
}

// EventAppender is the narrow event sink used by runtime-internal coordinators.
type EventAppender interface {
	AppendEvent(record runtimeevents.EventRecord)
}

// Runtime is the complete in-process kernel surface. Transport and product
// layers should normally consume it through gateway facades instead of taking
// this broad interface directly.
type Runtime interface {
	SubmissionSurface
	ProjectionReader
	ProjectionStreamSource
	RuntimeController
}

// GatewayRuntime is the raw runtime surface consumed by gateway. Gateway owns
// route/control/observe adaptation and exposes narrower facades to channels,
// HTTP APIs, and UIs.
type GatewayRuntime interface {
	ProjectionReader
	ProjectionStreamSource
	RuntimeController
	StartIngressRun(ctx context.Context, req IngressRequest) (*ManagedRun, error)
}

type AgentRuntimeConfigurer interface {
	ConfigureAgentRuntime(rt any) error
}

type IngressCoordinator interface {
	Submit(ctx context.Context, deps ExecutionDeps, req IngressRequest) (*ManagedRun, error)
}

type IngressAgentResolver func(IngressRequest) string

type ExecutionDeps struct {
	Providers          map[string]llm.LLMProvider
	Config             *config.Config
	SessionStore       *session.Store
	SessionCoordinator IngressCoordinator
	IngressResolver    IngressAgentResolver
	Sender             tools.MessageSender
	AgentRunner        tools.AgentCallRunner
	JobScheduler       tools.JobScheduler
	Skills             *skill.Loader
	Resources          *runtimeresources.Catalog
	Memory             *memory.Manager
	Recorder           *runtimeevents.Recorder
	Pipeline           *runtimememorylifecycle.Pipeline
	TaskStore          *task.Store
	TaskRuntime        *task.Runtime
	TurnStore          *turn.Store
	RuntimeConfigurer  AgentRuntimeConfigurer
}
