package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimecontrol "github.com/Isites/anyai/internal/runtime/control"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimememorylifecycle "github.com/Isites/anyai/internal/runtime/memory/lifecycle"
	runtimeprojection "github.com/Isites/anyai/internal/runtime/projection"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	runtimesessionops "github.com/Isites/anyai/internal/runtime/session/ops"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

// Runtime is the AnyAI core runtime. Higher layers should consume it through
// the isolated gateway exposure layer rather than mixing transport concerns
// into the runtime itself.
type Runtime struct {
	deps   *runtimeport.DependencySet
	active sync.Map

	AgentService      *AgentService
	SessionService    *runtimesessionops.Service
	MemoryService     *MemoryService
	ProjectionService *runtimeprojection.Service
	ControlService    *runtimecontrol.Service
}

var (
	_ runtimeport.Runtime        = (*Runtime)(nil)
	_ runtimeport.GatewayRuntime = (*Runtime)(nil)
)

func New(providers map[string]llm.LLMProvider, sessionStore *session.Store, cfg *config.Config) *Runtime {
	return WrapDependencies(runtimeport.NewDependencySet(providers, sessionStore, cfg))
}

func WrapDependencies(deps *runtimeport.DependencySet) *Runtime {
	if deps == nil {
		return nil
	}
	rt := &Runtime{deps: deps}
	rt.MemoryService = NewMemoryService(rt.Memory)
	rt.SessionService = runtimesessionops.NewService(rt.Config, rt.SessionStore, rt.Recorder)
	rt.ProjectionService = runtimeprojection.NewService(rt.Recorder, rt.TaskStore, rt.SessionService)
	rt.ControlService = runtimecontrol.NewService(rt.ProjectionService, rt.MemoryService, rt.TaskStore(), newManualSessionCompactor(deps), rt.Recorder)
	rt.AgentService = NewAgentService(rt.ExecutionDeps)
	if deps != nil {
		deps.SetRuntimeConfigurer(newSessionRuntimeConfigurer(rt.SessionService))
	}
	return rt
}

func (r *Runtime) Dependencies() *runtimeport.DependencySet {
	if r == nil {
		return nil
	}
	return r.deps
}

func (r *Runtime) Config() *config.Config {
	if r == nil || r.deps == nil {
		return nil
	}
	return r.deps.Config()
}

func (r *Runtime) Agents() []config.AgentConfig {
	if r == nil || r.deps == nil {
		return nil
	}
	return r.deps.Agents()
}

func (r *Runtime) Resources() *runtimeresources.Catalog {
	if r == nil || r.deps == nil {
		return nil
	}
	return r.deps.Resources()
}

func (r *Runtime) SessionStore() *session.Store {
	if r == nil || r.deps == nil {
		return nil
	}
	return r.deps.SessionStore()
}

func (r *Runtime) Recorder() *runtimeevents.Recorder {
	if r == nil || r.deps == nil {
		return nil
	}
	return r.deps.Recorder()
}

func (r *Runtime) Memory() *memory.Manager {
	if r == nil || r.deps == nil {
		return nil
	}
	return r.deps.Memory()
}

func (r *Runtime) TaskStore() *task.Store {
	if r == nil || r.deps == nil {
		return nil
	}
	return r.deps.TaskStore()
}

func (r *Runtime) ExecutionDeps() runtimeport.ExecutionDeps {
	if r == nil || r.deps == nil {
		return runtimeport.ExecutionDeps{}
	}
	return r.deps.ExecutionDeps()
}

func (r *Runtime) JobScheduler() tools.JobScheduler {
	return r.ExecutionDeps().JobScheduler
}

func (r *Runtime) UpdateConfig(cfg *config.Config) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.UpdateConfig(cfg)
}

func (r *Runtime) SetSessionCoordinator(coordinator runtimeport.IngressCoordinator) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetSessionCoordinator(coordinator)
}

func (r *Runtime) SetSender(sender tools.MessageSender) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetSender(sender)
}

func (r *Runtime) SetJobScheduler(js tools.JobScheduler) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetJobScheduler(js)
}

func (r *Runtime) SetAgentRunner(runner tools.AgentCallRunner) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetAgentRunner(runner)
}

func (r *Runtime) SetRecorder(recorder *runtimeevents.Recorder) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetRecorder(recorder)
	if r.SessionService != nil {
		r.SessionService.AttachStoreRecorder()
	}
	if taskRuntime := r.deps.TaskRuntime(); taskRuntime != nil && recorder != nil {
		taskRuntime.SetEventAppender(func(event runtimeevents.EventRecord) {
			runtimeevents.AppendEventWithReplayPolicy(recorder, event)
		})
	}
}

func (r *Runtime) SetTaskStore(store *task.Store) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetTaskStore(store)
	// Re-create ControlService with new taskStore
	if r.ControlService != nil {
		r.ControlService = runtimecontrol.NewService(r.ProjectionService, r.MemoryService, store, newManualSessionCompactor(r.deps), r.Recorder)
	}
}

func (r *Runtime) SetTaskRuntime(runtime *task.Runtime) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetTaskRuntime(runtime)
	if runtime != nil {
		if turnStore := r.deps.TurnStore(); turnStore != nil {
			runtime.SetTurnStore(turnStore)
		}
		if recorder := r.Recorder(); recorder != nil {
			runtime.SetEventAppender(func(event runtimeevents.EventRecord) {
				runtimeevents.AppendEventWithReplayPolicy(recorder, event)
			})
		}
		if store := runtime.Store(); store != nil {
			r.SetTaskStore(store)
		}
	}
}

func (r *Runtime) SetSkills(skills *skill.Loader) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetSkills(skills)
}

func (r *Runtime) SetResources(resources *runtimeresources.Catalog) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetResources(resources)
}

func (r *Runtime) SetMemory(mem *memory.Manager) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetMemory(mem)
}

func (r *Runtime) SetMemoryPipeline(pipeline *runtimememorylifecycle.Pipeline) {
	if r == nil || r.deps == nil {
		return
	}
	r.deps.SetMemoryPipeline(pipeline)
}

func (r *Runtime) StartManagedRun(ctx context.Context, req runtimeport.RunRequest) (*runtimeport.ManagedRun, error) {
	if r != nil && r.AgentService != nil {
		run, err := r.AgentService.StartManagedRun(ctx, req)
		if err != nil {
			return nil, err
		}
		return r.trackRun(run), nil
	}
	return nil, fmt.Errorf("agent service not available")
}

func (r *Runtime) StartIngressRun(ctx context.Context, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error) {
	if r != nil && r.AgentService != nil {
		run, err := r.AgentService.StartIngressRun(ctx, req)
		if err != nil {
			return nil, err
		}
		return r.trackRun(run), nil
	}
	return nil, fmt.Errorf("agent service not available")
}

func (r *Runtime) StartTextRun(ctx context.Context, channelName, agentID, senderID, accountID, sessionID, text string, chatType runtimeport.ChatType) (*runtimeport.ManagedRun, error) {
	blocks := []input.InputBlock{{Type: "text", Text: strings.TrimSpace(text)}}
	return r.StartIngressRun(ctx, runtimeport.IngressRequest{
		Channel:     channelName,
		RequestedID: agentID,
		SenderID:    senderID,
		AccountID:   accountID,
		ChatType:    chatType,
		Envelope: input.InputEnvelope{
			SessionID: strings.TrimSpace(sessionID),
			Blocks:    blocks,
		},
		SessionID: strings.TrimSpace(sessionID),
	})
}

func (r *Runtime) GetRun(runID string) (runtimeevents.RunRecord, bool) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.GetRun(runID)
	}
	return runtimeevents.RunRecord{}, false
}

func (r *Runtime) ListRuns() []runtimeevents.RunRecord {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.ListRuns()
	}
	return nil
}

func (r *Runtime) ListRunEvents(runID string) []runtimeevents.EventRecord {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.ReplayRunEvents(runID)
	}
	return nil
}

func (r *Runtime) ListRawRunEvents(runID string) []runtimeevents.EventRecord {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.ListRawRunEvents(runID)
	}
	return nil
}

func (r *Runtime) GetRunTree(runID string) (runtimeevents.RunTreeRecord, bool) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.ReplayRunTreeRecord(runID)
	}
	return runtimeevents.RunTreeRecord{}, false
}

func (r *Runtime) GetRawRunTree(runID string) (runtimeevents.RunTreeRecord, bool) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.GetRawRunTree(runID)
	}
	return runtimeevents.RunTreeRecord{}, false
}

func (r *Runtime) RunTree(runID string) ([]runtimeevents.RunNode, bool) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.ReplayRunTree(runID)
	}
	return nil, false
}

func (r *Runtime) RawRunTree(runID string) ([]runtimeevents.RunNode, bool) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.RawRunTree(runID)
	}
	return nil, false
}

func (r *Runtime) SubscribeRawRun(runID string) (<-chan runtimeevents.EventRecord, func(), error) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.SubscribeRawRun(runID)
	}
	return nil, nil, fmt.Errorf("run projection not available")
}

func (r *Runtime) SubscribeRunReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.SubscribeRunReplay(runID)
	}
	return nil, nil, nil, fmt.Errorf("run projection not available")
}

func (r *Runtime) SubscribeRawRunTree(runID string) (<-chan runtimeevents.EventRecord, func(), error) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.SubscribeRawRunTree(runID)
	}
	return nil, nil, fmt.Errorf("run projection not available")
}

func (r *Runtime) SubscribeRunTreeReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.SubscribeRunTreeReplay(runID)
	}
	return nil, nil, nil, fmt.Errorf("run projection not available")
}

func (r *Runtime) ListSessionEvents(agentID, sessionID string) []runtimeevents.EventRecord {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Session != nil {
		return r.ProjectionService.Session.Events(agentID, sessionID)
	}
	return nil
}

func (r *Runtime) SubscribeSession(agentID, sessionID string) (<-chan runtimeevents.EventRecord, func(), error) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Session != nil {
		return r.ProjectionService.Session.Subscribe(agentID, sessionID)
	}
	return nil, nil, fmt.Errorf("session projection not available")
}

func (r *Runtime) ListTasks() []task.Info {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Task != nil {
		return r.ProjectionService.Task.List()
	}
	return nil
}

func (r *Runtime) GetTask(taskID string) (task.Info, bool) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Task != nil {
		return r.ProjectionService.Task.Get(taskID)
	}
	return task.Info{}, false
}

func (r *Runtime) SubscribeTask(taskID string) (<-chan runtimeevents.EventRecord, func(), error) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Task != nil {
		return r.ProjectionService.Task.Subscribe(taskID)
	}
	return nil, nil, fmt.Errorf("task projection not available")
}

func (r *Runtime) RebuildEventProjections() error {
	if r != nil && r.ControlService != nil {
		return r.ControlService.RebuildProjections()
	}
	return nil
}

func (r *Runtime) EventStorageDir() string {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Run != nil {
		return r.ProjectionService.Run.EventStorageDir()
	}
	return ""
}

func (r *Runtime) ListSessions(agentID string) ([]session.SessionInfo, error) {
	if r != nil && r.ProjectionService != nil && r.ProjectionService.Session != nil {
		return r.ProjectionService.Session.List(agentID)
	}
	return nil, fmt.Errorf("session projection not available")
}

func (r *Runtime) CreateSession(agentID, requestedSessionID, prefix string) (string, error) {
	sessionID := strings.TrimSpace(requestedSessionID)
	if sessionID == "" {
		sessionID = defaultSessionID(prefix)
	}
	if r == nil || r.ControlService == nil {
		return "", fmt.Errorf("session control not available")
	}
	if err := r.ControlService.CreateSession(agentID, sessionID); err != nil {
		return "", err
	}
	return sessionID, nil
}

func (r *Runtime) LoadSession(agentID, sessionID string) (*session.Session, error) {
	if r == nil || r.ProjectionService == nil || r.ProjectionService.Session == nil {
		return nil, fmt.Errorf("session projection not available")
	}
	return r.ProjectionService.Session.Load(agentID, sessionID)
}

func (r *Runtime) LoadSessionSnapshot(agentID, sessionID string) (runtimeport.SessionSnapshot, error) {
	if r == nil || r.ProjectionService == nil || r.ProjectionService.Session == nil {
		return runtimeport.SessionSnapshot{}, fmt.Errorf("session projection not available")
	}
	sess, err := r.ProjectionService.Session.Load(agentID, sessionID)
	if err != nil {
		return runtimeport.SessionSnapshot{}, err
	}
	return runtimeport.SessionSnapshot{
		AgentID: agentID,
		ID:      sessionID,
		History: session.SerializeHistory(sess),
		Events:  r.ListSessionEvents(agentID, sessionID),
	}, nil
}

func (r *Runtime) DeleteSession(agentID, sessionID string) error {
	if r == nil || r.ControlService == nil {
		return fmt.Errorf("session control not available")
	}
	return r.ControlService.DeleteSession(agentID, sessionID)
}

func (r *Runtime) MemoryStats() memory.Stats {
	if r != nil && r.MemoryService != nil {
		return r.MemoryService.Stats()
	}
	return memory.Stats{}
}

func (r *Runtime) MemorySearch(query string, maxItems int, scope memory.SearchScope, layers ...memory.Layer) []memory.SearchMatch {
	if r != nil && r.MemoryService != nil {
		return r.MemoryService.SearchScoped(query, maxItems, scope, layers...)
	}
	return nil
}

func (r *Runtime) MemoryGet(id string, scope memory.SearchScope) (memory.Entry, bool) {
	if r != nil && r.MemoryService != nil {
		return r.MemoryService.GetScoped(id, scope)
	}
	return memory.Entry{}, false
}

func (r *Runtime) MemoryStaleCleanup(now time.Time) (int, error) {
	if r != nil && r.ControlService != nil {
		return r.ControlService.MemoryStaleCleanup(now)
	}
	return 0, fmt.Errorf("memory control not available")
}

func (r *Runtime) MemoryReindex() (int, error) {
	if r != nil && r.ControlService != nil {
		return r.ControlService.MemoryReindex()
	}
	return 0, fmt.Errorf("memory control not available")
}

func (r *Runtime) MemoryPromoteEligible(now time.Time) (int, error) {
	if r != nil && r.ControlService != nil {
		return r.ControlService.MemoryPromoteEligible(now)
	}
	return 0, fmt.Errorf("memory control not available")
}

func (r *Runtime) CancelRun(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id is required")
	}
	cancel, ok := r.active.Load(runID)
	if !ok {
		if run, ok := r.GetRun(runID); ok && runtimeevents.IsTerminalStatus(run.Status) {
			return fmt.Errorf("run %q is already terminal", runID)
		}
		return fmt.Errorf("run %q not found or not cancellable", runID)
	}
	cancelFn, ok := cancel.(context.CancelFunc)
	if !ok {
		return fmt.Errorf("run %q cancel hook is invalid", runID)
	}
	cancelFn()
	r.active.Delete(runID)
	return nil
}

func (r *Runtime) CancelTask(taskID string) error {
	if r != nil && r.ControlService != nil {
		return r.ControlService.CancelTask(taskID)
	}
	return fmt.Errorf("task control not available")
}

func (r *Runtime) DoTask(ctx context.Context, spec task.Spec) (string, error) {
	if r == nil || r.deps == nil || r.deps.TaskRuntime() == nil {
		return "", fmt.Errorf("task runtime not available")
	}
	return r.deps.TaskRuntime().DoTask(ctx, spec)
}

func (r *Runtime) CompactSession(agentID, sessionID string, keepEntries int) error {
	if r != nil && r.ControlService != nil {
		return r.ControlService.CompactSession(agentID, sessionID, keepEntries)
	}
	return fmt.Errorf("session control not available")
}

func (r *Runtime) AppendEvent(record runtimeevents.EventRecord) {
	if recorder := r.Recorder(); recorder != nil {
		recorder.AppendEvent(record)
	}
}

func defaultSessionID(prefix string) string {
	return input.DefaultSessionID(prefix, time.Now().UTC())
}

func (r *Runtime) trackRun(run *runtimeport.ManagedRun) *runtimeport.ManagedRun {
	if r == nil || run == nil || run.Cancel == nil || !run.OwnsLifecycle {
		return run
	}
	r.active.Store(run.RunID, run.Cancel)
	recorder := r.Recorder()
	if recorder == nil {
		return run
	}
	events, cancel := recorder.Subscribe(run.RunID)
	go func(runID string) {
		defer cancel()
		for event := range events {
			if runtimeevents.IsTerminalEvent(event) {
				r.active.Delete(runID)
				return
			}
		}
		r.active.Delete(runID)
	}(run.RunID)
	return run
}
