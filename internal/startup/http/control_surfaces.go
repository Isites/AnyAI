package httpchannel

import (
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
)

type inventorySurface interface {
	Agents() []config.AgentConfig
	Channels() []gateway.ChannelInfo
	ResourceCatalog() gateway.ResourceCatalog
	ListJobs() []gateway.Job
	PauseJob(name string) error
	ResumeJob(name string) error
	RemoveJob(name string) error
	UpdateJobSchedule(name, schedule string) error
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
	ListSessions(agentID string) ([]gateway.SessionInfo, error)
	CreateSession(agentID, requestedKey, prefix string) (string, error)
	LoadSession(agentID, sessionID string) (gateway.SessionView, error)
	DeleteSession(agentID, sessionID string) error
	ListSessionEvents(agentID, sessionID string) []gateway.Event
	SubscribeSession(agentID, sessionID string) (<-chan gateway.Event, func(), error)
}

type memorySurface interface {
	MemoryStats() gateway.MemoryStats
	MemorySearch(query string, maxItems int, scope gateway.MemoryScope, layers ...gateway.MemoryLayer) []gateway.MemorySearchMatch
	MemoryGet(id string, scope gateway.MemoryScope) (gateway.MemoryEntry, bool)
	MemoryStaleCleanup(now time.Time) (int, error)
	MemoryReindex() (int, error)
	MemoryPromoteEligible(now time.Time) (int, error)
}

type logSurface interface {
	LogEntriesPayload(limit int) []map[string]any
	SubscribeLogs() (<-chan gateway.LogEntry, func())
}

type configSurface interface {
	ConfigSnapshot() *config.Config
	SaveConfig(raw []byte) error
}

type taskSurface interface {
	ListTasks() []gateway.Task
	GetTask(taskID string) (gateway.Task, bool)
	SubscribeTask(taskID string) (<-chan gateway.Event, func(), error)
	CancelTask(taskID string) error
}

type ingressSurface interface {
	gateway.IngressFacade
}
