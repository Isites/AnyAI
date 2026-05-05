package httpchannel

import (
	"context"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/logging"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

type inventorySurface interface {
	Agents() []config.AgentConfig
	Channels() []gateway.ChannelInfo
	Resources() *runtimeresources.Catalog
	JobScheduler() tools.JobScheduler
	Version() string
}

type runtimeSurface interface {
	RebuildEventProjections() error
	EventStorageDir() string
}

type runSurface interface {
	gateway.IngressFacade
	gateway.ObserveFacade
	CancelRun(runID string) error
}

type sessionSurface interface {
	ListSessions(agentID string) ([]session.SessionInfo, error)
	CreateSession(agentID, requestedKey, prefix string) (string, error)
	LoadSession(agentID, sessionID string) (*session.Session, error)
	DeleteSession(agentID, sessionID string) error
	ListSessionEvents(agentID, sessionID string) []runtimeevents.EventRecord
	SubscribeSession(agentID, sessionID string) (<-chan runtimeevents.EventRecord, func(), error)
}

type memorySurface interface {
	MemoryStats() memory.Stats
	MemorySearch(query string, maxItems int, scope memory.SearchScope, layers ...memory.Layer) []memory.SearchMatch
	MemoryGet(id string, scope memory.SearchScope) (memory.Entry, bool)
	MemoryStaleCleanup(now time.Time) (int, error)
	MemoryReindex() (int, error)
	MemoryPromoteEligible(now time.Time) (int, error)
}

type logSurface interface {
	LogEntriesPayload(limit int) []map[string]any
	SubscribeLogs() (<-chan logging.LogEntry, func())
}

type configSurface interface {
	ConfigSnapshot() *config.Config
	SaveConfig(raw []byte) error
}

type taskSurface interface {
	ListTasks() []task.Info
	GetTask(taskID string) (task.Info, bool)
	SubscribeTask(taskID string) (<-chan runtimeevents.EventRecord, func(), error)
	CancelTask(taskID string) error
}

type ingressSurface interface {
	ResolveIngressAgent(req runtimeport.IngressRequest) string
	StartIngressRun(ctx context.Context, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error)
}
