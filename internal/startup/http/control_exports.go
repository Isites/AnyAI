package httpchannel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/logging"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	"github.com/Isites/anyai/internal/runtime/tool"
	httpapi "github.com/Isites/anyai/internal/startup/http/api"
	httpui "github.com/Isites/anyai/internal/startup/http/ui"
)

func (p *ControlPlane) AgentInventoryPayload() any         { return p.agentInventory() }
func (p *ControlPlane) ChannelViewsPayload() any           { return p.channels() }
func (p *ControlPlane) CatalogPayload() any                { return p.catalog() }
func (p *ControlPlane) CatalogEndpointsPayload() any       { return p.catalog().Endpoints }
func (p *ControlPlane) OverviewPayload() any               { return p.overview() }
func (p *ControlPlane) RebuildProjections() error          { return p.rebuildProjections() }
func (p *ControlPlane) RunList() []runtimeevents.RunRecord { return p.listRuns() }
func (p *ControlPlane) EventStorageDir() string            { return p.eventStorageDir() }

func (p *ControlPlane) MemoryStats() memory.Stats {
	if p != nil && p.memory != nil {
		return p.memory.MemoryStats()
	}
	return memory.Stats{}
}

func (p *ControlPlane) MemorySearch(query string, maxItems int, scope memory.SearchScope, layers ...memory.Layer) []memory.SearchMatch {
	if p != nil && p.memory != nil {
		return p.memory.MemorySearch(query, maxItems, scope, layers...)
	}
	return nil
}

func (p *ControlPlane) MemoryGet(id string, scope memory.SearchScope) (memory.Entry, bool) {
	if p != nil && p.memory != nil {
		return p.memory.MemoryGet(id, scope)
	}
	return memory.Entry{}, false
}

func (p *ControlPlane) MemoryStaleCleanup(now time.Time) (int, error) {
	if p == nil || p.memory == nil {
		return 0, fmt.Errorf("runtime not available")
	}
	return p.memory.MemoryStaleCleanup(now)
}

func (p *ControlPlane) MemoryReindex() (int, error) {
	if p == nil || p.memory == nil {
		return 0, fmt.Errorf("runtime not available")
	}
	return p.memory.MemoryReindex()
}

func (p *ControlPlane) MemoryPromoteEligible(now time.Time) (int, error) {
	if p == nil || p.memory == nil {
		return 0, fmt.Errorf("runtime not available")
	}
	return p.memory.MemoryPromoteEligible(now)
}

func (p *ControlPlane) StartAcceptedRun(
	ctx context.Context,
	agentID, text string,
	inputs []input.InputBlock,
	sessionID, channel, senderID, accountID string,
	chatType gateway.ChatType,
	sessionPrefix string,
) (*runtimeport.ManagedRun, runtimeevents.RunRecord, error) {
	accepted, err := p.startRun(ctx, runStartRequest{
		AgentID:       agentID,
		Text:          text,
		Inputs:        inputs,
		SessionID:     sessionID,
		Channel:       channel,
		SenderID:      senderID,
		AccountID:     accountID,
		ChatType:      chatType,
		SessionPrefix: sessionPrefix,
	})
	if err != nil {
		return nil, runtimeevents.RunRecord{}, err
	}
	return accepted.Run, accepted.Record, nil
}

func (p *ControlPlane) StartManagedRun(
	ctx context.Context,
	agentID, text string,
	inputs []input.InputBlock,
	sessionID, channel, senderID, accountID string,
	chatType gateway.ChatType,
	sessionPrefix string,
) (*runtimeport.ManagedRun, error) {
	run, _, err := p.StartAcceptedRun(ctx, agentID, text, inputs, sessionID, channel, senderID, accountID, chatType, sessionPrefix)
	return run, err
}

func (p *ControlPlane) RunRecord(runID string) (runtimeevents.RunRecord, bool) {
	return p.getRun(runID)
}
func (p *ControlPlane) RunEvents(runID string) []runtimeevents.EventRecord {
	return p.listRunEvents(runID)
}
func (p *ControlPlane) SubscribeRunReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	if p == nil || p.run == nil {
		return nil, nil, nil, fmt.Errorf("runtime not available")
	}
	return p.run.SubscribeRunReplay(runID)
}
func (p *ControlPlane) CancelRun(runID string) error {
	if p == nil || p.run == nil {
		return fmt.Errorf("runtime not available")
	}
	return p.run.CancelRun(runID)
}

func (p *ControlPlane) SessionList(agentID string) ([]session.SessionInfo, error) {
	return p.listSessions(agentID)
}
func (p *ControlPlane) CreateSession(agentID, requestedKey, prefix string) (string, error) {
	return p.createSession(agentID, requestedKey, prefix)
}
func (p *ControlPlane) SessionCreate(agentID, requestedKey, prefix string) (string, error) {
	return p.createSession(agentID, requestedKey, prefix)
}
func (p *ControlPlane) LoadSession(agentID, sessionID string) (*session.Session, error) {
	return p.loadSession(agentID, sessionID)
}
func (p *ControlPlane) SessionLoad(agentID, sessionID string) (*session.Session, error) {
	return p.loadSession(agentID, sessionID)
}
func (p *ControlPlane) SessionEvents(agentID, sessionID string) []runtimeevents.EventRecord {
	return p.listSessionEvents(agentID, sessionID)
}
func (p *ControlPlane) SubscribeSession(agentID, sessionID string) (<-chan runtimeevents.EventRecord, func(), error) {
	return p.subscribeSession(agentID, sessionID)
}
func (p *ControlPlane) SessionDelete(agentID, sessionID string) error {
	return p.deleteSession(agentID, sessionID)
}
func (p *ControlPlane) DeleteSession(agentID, sessionID string) error {
	return p.deleteSession(agentID, sessionID)
}

func (p *ControlPlane) RunTreeRecord(runID string) (runtimeevents.RunTreeRecord, bool) {
	return p.getRunTree(runID)
}
func (p *ControlPlane) RunTree(runID string) ([]runtimeevents.RunNode, bool) {
	return p.runTree(runID)
}
func (p *ControlPlane) SubscribeRunTreeReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	if p == nil || p.run == nil {
		return nil, nil, nil, fmt.Errorf("runtime not available")
	}
	return p.run.SubscribeRunTreeReplay(runID)
}

func (p *ControlPlane) JobScheduler() tools.JobScheduler {
	if p == nil || p.inventory == nil {
		return nil
	}
	return p.inventory.JobScheduler()
}

func (p *ControlPlane) LogEntriesPayload(limit int) []map[string]any { return p.logEntries(limit) }
func (p *ControlPlane) SubscribeLogs() (<-chan logging.LogEntry, func()) {
	if p == nil || p.logs == nil {
		return nil, nil
	}
	return p.logs.SubscribeLogs()
}

func (p *ControlPlane) ConfigSnapshot() *config.Config { return p.config() }
func (p *ControlPlane) SaveConfig(raw []byte) error    { return p.saveConfig(raw) }
func (p *ControlPlane) RuntimeAgents() []config.AgentConfig {
	if p == nil || p.inventory == nil {
		return nil
	}
	return p.inventory.Agents()
}
func (p *ControlPlane) ResolveIngressAgent(channelName, requestedAgentID, senderID, accountID string, chatType gateway.ChatType) string {
	return p.resolveIngressAgent(channelName, requestedAgentID, senderID, accountID, chatType)
}

func (p *ControlPlane) TaskList() []task.Info {
	return p.listTasks()
}

func (p *ControlPlane) TaskRecord(taskID string) (task.Info, bool) {
	return p.getTask(taskID)
}

func (p *ControlPlane) TaskEvents(taskID string) []runtimeevents.EventRecord {
	if p == nil || p.task == nil || p.run == nil {
		return nil
	}
	tk, ok := p.task.GetTask(taskID)
	if !ok || strings.TrimSpace(tk.RunID) == "" {
		return nil
	}
	runEvents := p.run.ListRunEvents(tk.RunID)
	if len(runEvents) == 0 {
		return synthesizeTaskEvents(tk)
	}
	filtered := make([]runtimeevents.EventRecord, 0, len(runEvents))
	for _, event := range runEvents {
		if !strings.HasPrefix(event.Name, "task.") {
			continue
		}
		if runtimeevents.StringPayload(event.Payload, "task_id") != taskID {
			continue
		}
		filtered = append(filtered, event)
	}
	if len(filtered) == 0 {
		return synthesizeTaskEvents(tk)
	}
	return filtered
}

func (p *ControlPlane) SubscribeTask(taskID string) (<-chan runtimeevents.EventRecord, func(), error) {
	return p.subscribeTask(taskID)
}

func (p *ControlPlane) CancelTask(taskID string) error {
	return p.cancelTask(taskID)
}

func synthesizeTaskEvents(tk task.Info) []runtimeevents.EventRecord {
	payload := taskEventPayload(tk)
	queuedAt := firstTaskEventTime(tk.CreatedAt, tk.UpdatedAt, tk.StartedAt, tk.CompletedAt)
	startedAt := firstTaskEventTime(tk.StartedAt, queuedAt)
	completedAt := firstTaskEventTime(tk.CompletedAt, tk.UpdatedAt, startedAt)

	events := []runtimeevents.EventRecord{
		{
			RunID:     tk.RunID,
			AgentID:   tk.AgentID,
			SessionID: tk.SessionID,
			Name:      "task.queued",
			Timestamp: queuedAt,
			Payload:   payload,
		},
		{
			RunID:     tk.RunID,
			AgentID:   tk.AgentID,
			SessionID: tk.SessionID,
			Name:      "task.started",
			Timestamp: startedAt,
			Payload:   payload,
		},
	}

	switch tk.Status {
	case task.StatusCompleted:
		events = append(events, runtimeevents.EventRecord{
			RunID:     tk.RunID,
			AgentID:   tk.AgentID,
			SessionID: tk.SessionID,
			Name:      "task.completed",
			Timestamp: completedAt,
			Payload:   payload,
		})
	case task.StatusFailed:
		events = append(events, runtimeevents.EventRecord{
			RunID:     tk.RunID,
			AgentID:   tk.AgentID,
			SessionID: tk.SessionID,
			Name:      "task.failed",
			Timestamp: completedAt,
			Payload:   payload,
		})
	case task.StatusCancelled:
		events = append(events, runtimeevents.EventRecord{
			RunID:     tk.RunID,
			AgentID:   tk.AgentID,
			SessionID: tk.SessionID,
			Name:      "task.cancelled",
			Timestamp: completedAt,
			Payload:   payload,
		})
	}

	return events
}

func taskEventPayload(tk task.Info) map[string]any {
	payload := map[string]any{
		"task_id":   tk.ID,
		"task_kind": string(tk.Kind),
		"status":    string(tk.Status),
	}
	if strings.TrimSpace(tk.ParentTaskID) != "" {
		payload["parent_task_id"] = tk.ParentTaskID
	}
	if strings.TrimSpace(tk.Input) != "" {
		payload["input"] = tk.Input
	}
	if strings.TrimSpace(tk.TargetAgent) != "" {
		payload["target_agent"] = tk.TargetAgent
	}
	if strings.TrimSpace(tk.ToolName) != "" {
		payload["tool"] = tk.ToolName
	}
	if strings.TrimSpace(tk.ProcessName) != "" {
		payload["process"] = tk.ProcessName
	}
	if strings.TrimSpace(tk.Summary) != "" {
		payload["summary"] = tk.Summary
	}
	if strings.TrimSpace(tk.Error) != "" {
		payload["error"] = tk.Error
	}
	if len(tk.Metadata) > 0 {
		payload["metadata"] = cloneTaskEventMetadata(tk.Metadata)
	}
	return payload
}

func cloneTaskEventMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func firstTaskEventTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Now().UTC()
}

var _ httpapi.Plane = (*ControlPlane)(nil)
var _ httpui.Plane = (*ControlPlane)(nil)
