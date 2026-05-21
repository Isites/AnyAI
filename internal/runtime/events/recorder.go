package runtimeevents

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	"github.com/Isites/anyai/internal/runtime/tool"
)

type RunStatus string

const CurrentEventSchemaVersion = 1

const (
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusAborted   RunStatus = "aborted"
)

type RunRecord struct {
	ID                    string    `json:"id"`
	RunNodeID             string    `json:"run_node_id,omitempty"`
	ParentRunNodeID       string    `json:"parent_run_node_id,omitempty"`
	LegacyRunNodeID       string    `json:"trace_node_id,omitempty"`
	LegacyParentRunNodeID string    `json:"parent_trace_node_id,omitempty"`
	ParentAgentID         string    `json:"parent_agent_id,omitempty"`
	AgentID               string    `json:"agent_id"`
	SessionID             string    `json:"session_id"`
	TaskID                string    `json:"task_id,omitempty"`
	ParentTaskID          string    `json:"parent_task_id,omitempty"`
	Model                 string    `json:"model"`
	Channel               string    `json:"channel,omitempty"`
	Input                 string    `json:"input,omitempty"`
	Output                string    `json:"output,omitempty"`
	Error                 string    `json:"error,omitempty"`
	Status                RunStatus `json:"status"`
	CreatedAt             time.Time `json:"created_at"`
	StartedAt             time.Time `json:"started_at"`
	CompletedAt           time.Time `json:"completed_at,omitempty"`
}

type EventRecord struct {
	SchemaVersion         int            `json:"schema_version,omitempty"`
	Sequence              int            `json:"sequence"`
	RunID                 string         `json:"run_id"`
	RunNodeID             string         `json:"run_node_id,omitempty"`
	ParentRunNodeID       string         `json:"parent_run_node_id,omitempty"`
	LegacyRunNodeID       string         `json:"trace_node_id,omitempty"`
	LegacyParentRunNodeID string         `json:"parent_trace_node_id,omitempty"`
	AgentID               string         `json:"agent_id"`
	SessionID             string         `json:"session_id"`
	Name                  string         `json:"name"`
	Timestamp             time.Time      `json:"timestamp"`
	Payload               map[string]any `json:"payload,omitempty"`
}

type RunTreeRecord struct {
	Runs   []RunRecord   `json:"runs"`
	Events []EventRecord `json:"events"`
}

type RunNode struct {
	Run      RunRecord     `json:"run"`
	Events   []EventRecord `json:"events,omitempty"`
	Children []RunNode     `json:"children,omitempty"`
}

type Recorder struct {
	mu               sync.RWMutex
	runs             map[string]*RunRecord
	runEvents        map[string][]EventRecord
	runSequences     map[string]int
	runTrees         map[string]map[string]struct{}
	sessionRuns      map[string]map[string]struct{}
	subscribers      map[string]map[chan EventRecord]struct{}
	runTreeSubs      map[string]map[chan EventRecord]struct{}
	sessionSubs      map[string]map[chan EventRecord]struct{}
	listeners        map[int]Listener
	nextListenerID   int
	storage          *fileStore
	lastPersistError error
}

type persistedRecord struct {
	Kind  string       `json:"kind"`
	Run   *RunRecord   `json:"run,omitempty"`
	Event *EventRecord `json:"event,omitempty"`
}

type fileStore struct {
	dir     string
	runsDir string
}

func NewRecorder() *Recorder {
	return newRecorder(nil)
}

func NewPersistentRecorder(dir string) (*Recorder, error) {
	store, err := newFileStore(dir)
	if err != nil {
		return nil, err
	}
	recorder := newRecorder(store)
	if err := recorder.RebuildFromStorage(); err != nil {
		return nil, err
	}
	return recorder, nil
}

func newRecorder(storage *fileStore) *Recorder {
	return &Recorder{
		runs:         make(map[string]*RunRecord),
		runEvents:    make(map[string][]EventRecord),
		runSequences: make(map[string]int),
		runTrees:     make(map[string]map[string]struct{}),
		sessionRuns:  make(map[string]map[string]struct{}),
		subscribers:  make(map[string]map[chan EventRecord]struct{}),
		runTreeSubs:  make(map[string]map[chan EventRecord]struct{}),
		sessionSubs:  make(map[string]map[chan EventRecord]struct{}),
		listeners:    make(map[int]Listener),
		storage:      storage,
	}
}

func (r *Recorder) StorageDir() string {
	if r == nil || r.storage == nil {
		return ""
	}
	return r.storage.dir
}

func (r *Recorder) LastPersistenceError() error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastPersistError
}

func (r *Recorder) RebuildFromStorage() error {
	if r == nil || r.storage == nil {
		return nil
	}
	state, err := r.storage.load()
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = state.runs
	r.runEvents = state.runEvents
	r.runSequences = rebuildRunSequences(state.runEvents)
	r.runTrees = state.runTrees
	r.sessionRuns = state.sessionRuns
	r.lastPersistError = nil
	return nil
}

func (r *Recorder) StartRun(run RunRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	normalizeRunNodeFields(&run)
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.Status == "" {
		run.Status = RunStatusQueued
	}
	if run.Status == RunStatusRunning && run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	copyRun := run
	r.runs[run.ID] = &copyRun
	r.ensureRunTreeLocked(run.ID)
	r.attachSessionRunLocked(run.SessionID, run.ID)
	r.persistRunLocked(copyRun)
}

// BeginRun moves an accepted or queued run into the running state. If the run
// does not exist yet, it is created. The explicit run.started event is emitted
// by callers so the event log remains the authoritative lifecycle narrative.
func (r *Recorder) BeginRun(run RunRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	normalizeRunNodeFields(&run)

	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	run.Status = RunStatusRunning

	if existing, ok := r.runs[run.ID]; ok {
		if run.CreatedAt.IsZero() {
			run.CreatedAt = existing.CreatedAt
		}
		if !existing.CreatedAt.IsZero() {
			run.CreatedAt = existing.CreatedAt
		}
		if run.Input == "" {
			run.Input = existing.Input
		}
		if run.Channel == "" {
			run.Channel = existing.Channel
		}
		if run.TaskID == "" {
			run.TaskID = existing.TaskID
		}
		if run.ParentTaskID == "" {
			run.ParentTaskID = existing.ParentTaskID
		}
		if run.RunNodeID == "" {
			run.RunNodeID = existing.RunNodeID
		}
		if run.ParentRunNodeID == "" {
			run.ParentRunNodeID = existing.ParentRunNodeID
		}
	}

	copyRun := run
	r.runs[run.ID] = &copyRun
	r.ensureRunTreeLocked(run.ID)
	r.attachSessionRunLocked(run.SessionID, run.ID)
	r.persistRunLocked(copyRun)
}

func (r *Recorder) FinishRun(runID string, status RunStatus, output, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok {
		return
	}
	run.Status = status
	run.Output = output
	run.Error = errMsg
	run.CompletedAt = time.Now().UTC()
	r.persistRunLocked(*run)
}

func (r *Recorder) RecordAgentEvent(run RunRecord, event AgentEvent) {
	for _, rec := range EventRecordsForAgentEvent(run, event) {
		if shouldRecordRuntimeEvent(rec) {
			r.AppendEvent(rec)
			continue
		}
		if shouldBroadcastTransientRuntimeEvent(rec) {
			r.PublishTransientEvent(rec)
		}
	}
}

func (r *Recorder) AppendEvent(event EventRecord) {
	r.publishEvent(event, true, true)
}

func (r *Recorder) PublishTransientEvent(event EventRecord) {
	r.publishEvent(event, false, false)
}

func (r *Recorder) publishEvent(event EventRecord, persist bool, notifyListeners bool) {
	if r == nil {
		return
	}
	var (
		runSubs     []chan EventRecord
		runTreeSubs []chan EventRecord
		sessionSubs []chan EventRecord
		listeners   []Listener
		treeRunID   string
	)
	r.mu.Lock()

	if event.SchemaVersion <= 0 {
		event.SchemaVersion = CurrentEventSchemaVersion
	}
	if event.Sequence <= 0 {
		r.runSequences[event.RunID]++
		event.Sequence = r.runSequences[event.RunID]
	} else if event.Sequence > r.runSequences[event.RunID] {
		r.runSequences[event.RunID] = event.Sequence
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	r.applyEventRunNodeFieldsLocked(&event)
	treeRunID = r.ensureRunTreeLocked(event.RunID)
	r.attachSessionRunLocked(event.SessionID, event.RunID)
	if persist {
		r.applyRunLifecycleEventLocked(event)
	}
	if persist {
		r.runEvents[event.RunID] = append(r.runEvents[event.RunID], event)
		r.persistEventLocked(event)
	}
	runSubs = copyEventSubscribers(r.subscribers[event.RunID])
	runTreeSubs = copyEventSubscribers(r.runTreeSubs[treeRunID])
	sessionSubs = copyEventSubscribers(r.sessionSubs[event.SessionID])
	if notifyListeners {
		listeners = copyEventListeners(r.listeners)
	}
	r.mu.Unlock()

	for _, ch := range runSubs {
		sendEventNonBlocking(ch, event)
	}
	for _, ch := range runTreeSubs {
		sendEventNonBlocking(ch, event)
	}
	for _, ch := range sessionSubs {
		sendEventNonBlocking(ch, event)
	}
	typed := ClassifyEvent(event)
	for _, listener := range listeners {
		if listener == nil {
			continue
		}
		listener.HandleEvent(typed)
	}
}

func (r *Recorder) applyRunLifecycleEventLocked(event EventRecord) {
	if r == nil || strings.TrimSpace(event.RunID) == "" {
		return
	}
	switch event.Name {
	case EventRunAccepted, EventRunRouted, EventRunQueued, EventRunStarted, EventRunCompleted, EventRunFailed, EventRunAborted:
	default:
		return
	}
	run := r.runs[event.RunID]
	if run == nil {
		run = &RunRecord{
			ID:        event.RunID,
			AgentID:   event.AgentID,
			SessionID: event.SessionID,
			Status:    RunStatusQueued,
			CreatedAt: event.Timestamp,
			StartedAt: event.Timestamp,
		}
		r.runs[event.RunID] = run
	}
	if strings.TrimSpace(run.AgentID) == "" {
		run.AgentID = event.AgentID
	}
	if strings.TrimSpace(run.SessionID) == "" {
		run.SessionID = event.SessionID
	}
	if strings.TrimSpace(run.TaskID) == "" {
		run.TaskID = eventTaskID(&event)
	}
	if strings.TrimSpace(run.ParentTaskID) == "" {
		run.ParentTaskID = eventParentTaskID(&event)
	}
	if strings.TrimSpace(run.RunNodeID) == "" {
		run.RunNodeID = event.RunNodeID
	}
	if strings.TrimSpace(run.ParentRunNodeID) == "" {
		run.ParentRunNodeID = event.ParentRunNodeID
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = event.Timestamp
	}
	if eventTrace := strings.TrimSpace(event.RunNodeID); eventTrace != "" && strings.TrimSpace(run.RunNodeID) != "" && eventTrace != strings.TrimSpace(run.RunNodeID) {
		r.persistRunLocked(*run)
		return
	}

	switch event.Name {
	case EventRunQueued, EventRunRouted, EventRunAccepted:
		if run.Status == "" {
			run.Status = RunStatusQueued
		}
	case EventRunStarted:
		run.Status = RunStatusRunning
		if run.StartedAt.IsZero() {
			run.StartedAt = event.Timestamp
		}
	case EventRunCompleted:
		run.Status = RunStatusCompleted
		run.CompletedAt = event.Timestamp
	case EventRunFailed:
		run.Status = RunStatusFailed
		run.CompletedAt = event.Timestamp
		if run.Error == "" {
			run.Error = strings.TrimSpace(FailureMessage(event))
		}
	case EventRunAborted:
		run.Status = RunStatusAborted
		run.CompletedAt = event.Timestamp
		if run.Error == "" {
			run.Error = strings.TrimSpace(FailureMessage(event))
		}
	}
	r.persistRunLocked(*run)
}

func sendEventNonBlocking(ch chan EventRecord, event EventRecord) {
	if ch == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	select {
	case ch <- event:
	default:
	}
}

func (r *Recorder) GetRun(runID string) (RunRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	run, ok := r.runs[runID]
	if !ok {
		return RunRecord{}, false
	}
	return *run, true
}

func (r *Recorder) ListRuns() []RunRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()

	runs := make([]RunRecord, 0, len(r.runs))
	for _, run := range r.runs {
		runs = append(runs, *run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	return runs
}

func (r *Recorder) ListRunEvents(runID string) []EventRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	events := append([]EventRecord(nil), r.runEvents[runID]...)
	sortEventRecords(events)
	return events
}

func (r *Recorder) ListSessionEvents(sessionID string) []EventRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()

	runIDs := r.relatedSessionRunIDsLocked(sessionID)
	events := make([]EventRecord, 0)
	for runID := range runIDs {
		events = append(events, r.runEvents[runID]...)
	}
	sortEventRecords(events)
	return events
}

func (r *Recorder) relatedSessionRunIDsLocked(sessionID string) map[string]struct{} {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	visitedSessions := map[string]struct{}{}
	relatedRuns := map[string]struct{}{}
	queue := []string{sessionID}
	for len(queue) > 0 {
		current := strings.TrimSpace(queue[0])
		queue = queue[1:]
		if current == "" {
			continue
		}
		if _, seen := visitedSessions[current]; seen {
			continue
		}
		visitedSessions[current] = struct{}{}

		runIDs, ok := r.sessionRuns[current]
		if !ok {
			continue
		}
		for runID := range runIDs {
			relatedRuns[runID] = struct{}{}
			for _, event := range r.runEvents[runID] {
				childSessionID := strings.TrimSpace(StringPayload(event.Payload, "session_id"))
				if childSessionID == "" {
					continue
				}
				if _, seen := visitedSessions[childSessionID]; !seen {
					queue = append(queue, childSessionID)
				}
			}
		}
	}
	return relatedRuns
}

func (r *Recorder) GetRunTree(runID string) (RunTreeRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return buildRunTreeRecord(r.runs, r.runEvents, r.runTrees, runID)
}

func (r *Recorder) Subscribe(runID string) (<-chan EventRecord, func()) {
	ch := make(chan EventRecord, 64)

	r.mu.Lock()
	if r.subscribers[runID] == nil {
		r.subscribers[runID] = make(map[chan EventRecord]struct{})
	}
	r.subscribers[runID][ch] = struct{}{}
	r.mu.Unlock()

	cancel := func() {
		r.mu.Lock()
		if subs := r.subscribers[runID]; subs != nil {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(r.subscribers, runID)
			}
		}
		r.mu.Unlock()
		close(ch)
	}

	return ch, cancel
}

func (r *Recorder) SubscribeRunTree(runID string) (<-chan EventRecord, func()) {
	ch := make(chan EventRecord, 128)
	runID = strings.TrimSpace(runID)

	r.mu.Lock()
	if r.runTreeSubs[runID] == nil {
		r.runTreeSubs[runID] = make(map[chan EventRecord]struct{})
	}
	r.runTreeSubs[runID][ch] = struct{}{}
	r.mu.Unlock()

	cancel := func() {
		r.mu.Lock()
		if subs := r.runTreeSubs[runID]; subs != nil {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(r.runTreeSubs, runID)
			}
		}
		r.mu.Unlock()
		close(ch)
	}

	return ch, cancel
}

func (r *Recorder) SubscribeSession(sessionID string) (<-chan EventRecord, func()) {
	ch := make(chan EventRecord, 128)
	sessionID = strings.TrimSpace(sessionID)

	r.mu.Lock()
	if r.sessionSubs[sessionID] == nil {
		r.sessionSubs[sessionID] = make(map[chan EventRecord]struct{})
	}
	r.sessionSubs[sessionID][ch] = struct{}{}
	r.mu.Unlock()

	cancel := func() {
		r.mu.Lock()
		if subs := r.sessionSubs[sessionID]; subs != nil {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(r.sessionSubs, sessionID)
			}
		}
		r.mu.Unlock()
		close(ch)
	}

	return ch, cancel
}

func (r *Recorder) AddListener(listener Listener) func() {
	if r == nil || listener == nil {
		return func() {}
	}
	r.mu.Lock()
	r.nextListenerID++
	id := r.nextListenerID
	r.listeners[id] = listener
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.listeners, id)
		r.mu.Unlock()
	}
}

func (r *Recorder) RunTree(runID string) ([]RunNode, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	runIDs, ok := r.runTrees[strings.TrimSpace(runID)]
	if !ok {
		return nil, false
	}

	roots := make([]RunNode, 0, len(runIDs))
	for id := range runIDs {
		run, exists := r.runs[id]
		if !exists {
			continue
		}
		roots = append(roots, RunNode{
			Run:    *run,
			Events: append([]EventRecord(nil), r.runEvents[id]...),
		})
	}
	for idx := range roots {
		sortEventRecords(roots[idx].Events)
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Run.StartedAt.Equal(roots[j].Run.StartedAt) {
			return roots[i].Run.ID < roots[j].Run.ID
		}
		return roots[i].Run.StartedAt.Before(roots[j].Run.StartedAt)
	})
	return roots, true
}

func EventRecordsForAgentEvent(run RunRecord, event AgentEvent) []EventRecord {
	normalizeRunNodeFields(&run)
	base := EventRecord{
		RunID:           run.ID,
		RunNodeID:       run.RunNodeID,
		ParentRunNodeID: run.ParentRunNodeID,
		AgentID:         run.AgentID,
		SessionID:       run.SessionID,
		Timestamp:       time.Now().UTC(),
	}

	build := func(name string, payload map[string]any) EventRecord {
		return EventRecord{
			RunID:           base.RunID,
			RunNodeID:       base.RunNodeID,
			ParentRunNodeID: base.ParentRunNodeID,
			AgentID:         base.AgentID,
			SessionID:       base.SessionID,
			Name:            name,
			Timestamp:       base.Timestamp,
			Payload:         payload,
		}
	}

	switch event.Type {
	case AgentEventRunStarted:
		return []EventRecord{build(EventRunStarted, nil)}
	case AgentEventActivity:
		return []EventRecord{build(EventRunActivity, nil)}
	case AgentEventMemoryRecall:
		payload := map[string]any{
			"query":   event.Query,
			"entries": serializeMemoryMatches(event.MemoryMatches),
		}
		return []EventRecord{build(EventMemoryRecalled, payload)}
	case AgentEventLLMRetry:
		if event.LLMRetry == nil {
			return nil
		}
		return []EventRecord{build(EventLLMRetrying, map[string]any{
			"attempt":      event.LLMRetry.Attempt,
			"max_attempts": event.LLMRetry.MaxAttempts,
			"wait_ms":      event.LLMRetry.WaitMS,
			"stage":        event.LLMRetry.Stage,
			"error":        event.LLMRetry.Error,
		})}
	case AgentEventToolRetry:
		if event.ToolRetry == nil {
			return nil
		}
		payload := map[string]any{
			"tool":         event.ToolRetry.ToolName,
			"attempt":      event.ToolRetry.Attempt,
			"max_attempts": event.ToolRetry.MaxAttempts,
			"wait_ms":      event.ToolRetry.WaitMS,
			"error_class":  event.ToolRetry.ErrorClass,
			"error":        event.ToolRetry.Error,
			"decision":     event.ToolRetry.Decision,
		}
		if event.ToolCall != nil {
			payload["id"] = event.ToolCall.ID
			payload["input"] = tools.SanitizeToolInputForTranscript(event.ToolCall.Name, event.ToolCall.Input)
		}
		return []EventRecord{build(EventToolRetrying, payload)}
	case AgentEventTextDelta:
		return []EventRecord{build(EventTextDelta, map[string]any{"text": event.Text})}
	case AgentEventToolCallRequested:
		if event.ToolCall == nil {
			return nil
		}
		payload := map[string]any{
			"id":    event.ToolCall.ID,
			"tool":  event.ToolCall.Name,
			"input": normalizedToolInput(event.ToolCall),
		}
		if event.ToolMetadata != nil {
			payload["metadata"] = serializeToolMetadata(*event.ToolMetadata)
		}
		return []EventRecord{build(EventToolCallRequested, payload)}
	case AgentEventToolCallStart:
		if event.ToolCall == nil {
			return nil
		}
		payload := map[string]any{
			"id":    event.ToolCall.ID,
			"tool":  event.ToolCall.Name,
			"input": normalizedToolInput(event.ToolCall),
		}
		if event.ToolMetadata != nil {
			payload["metadata"] = serializeToolMetadata(*event.ToolMetadata)
		}
		records := []EventRecord{build(EventToolCallStarted, payload)}
		if payload, ok := AgentCallStartedPayload(event.ToolCall); ok {
			payload["id"] = event.ToolCall.ID
			records = append(records, build(EventAgentCallStarted, payload))
		}
		return records
	case AgentEventToolWarning:
		if event.ToolWarning == nil {
			return nil
		}
		payload := map[string]any{
			"tool":     event.ToolWarning.ToolName,
			"detector": event.ToolWarning.Detector,
			"count":    event.ToolWarning.Count,
			"message":  event.ToolWarning.Message,
			"blocked":  event.ToolWarning.Blocked,
		}
		if event.ToolCall != nil {
			payload["id"] = event.ToolCall.ID
			payload["input"] = tools.SanitizeToolInputForTranscript(event.ToolCall.Name, event.ToolCall.Input)
		}
		return []EventRecord{build(EventToolWarning, payload)}
	case AgentEventToolResult:
		if event.ToolCall == nil || event.Result == nil {
			return nil
		}
		payload := map[string]any{
			"id":     event.ToolCall.ID,
			"tool":   event.ToolCall.Name,
			"input":  tools.SanitizeToolInputForTranscript(event.ToolCall.Name, event.ToolCall.Input),
			"output": event.Result.Output,
			"error":  event.Result.Error,
		}
		if len(event.Result.Images) > 0 {
			var images []map[string]any
			for i, img := range event.Result.Images {
				size := img.Size
				if size <= 0 {
					size = len(img.Data)
				}
				item := map[string]any{
					"id":   fmt.Sprintf("tool_image_%d", i+1),
					"size": size,
				}
				if strings.TrimSpace(img.ID) != "" {
					item["id"] = strings.TrimSpace(img.ID)
				}
				if strings.TrimSpace(img.Name) != "" {
					item["name"] = strings.TrimSpace(img.Name)
				}
				if strings.TrimSpace(img.Path) != "" {
					item["path"] = strings.TrimSpace(img.Path)
				}
				if strings.TrimSpace(img.MimeType) != "" {
					item["mime_type"] = img.MimeType
				}
				images = append(images, item)
			}
			payload["images"] = images
		}
		if len(event.Result.Metadata) > 0 {
			payload["metadata"] = tools.SanitizeMetadata(event.Result.Metadata)
		}
		if event.ToolMetadata != nil {
			payload["tool_metadata"] = serializeToolMetadata(*event.ToolMetadata)
		}
		eventName := EventToolCompleted
		if strings.TrimSpace(event.Result.Error) != "" {
			eventName = EventToolFailed
		}
		records := []EventRecord{build(eventName, payload)}
		if agentCallEventName, agentCallPayload, ok := AgentCallFinishedPayload(event.ToolCall, event.Result); ok {
			agentCallPayload["id"] = event.ToolCall.ID
			records = append(records, build(agentCallEventName, agentCallPayload))
		}
		if capturePayload, ok := memoryCapturedPayload(event.ToolCall, event.Result); ok {
			records = append(records, build(EventMemoryCaptured, capturePayload))
		}
		return records
	case AgentEventToolFanoutCompleted:
		if event.ToolFanout == nil {
			return nil
		}
		payload := map[string]any{
			"total_count":     event.ToolFanout.TotalCount,
			"started_count":   event.ToolFanout.StartedCount,
			"completed_count": event.ToolFanout.CompletedCount,
			"failed_count":    event.ToolFanout.FailedCount,
			"status":          event.ToolFanout.Status,
			"calls":           serializeToolFanoutCalls(event.ToolFanout.Calls),
		}
		return []EventRecord{build(EventToolFanoutComplete, payload)}
	case AgentEventRunIncomplete:
		return []EventRecord{build(EventRunIncomplete, map[string]any{"message": event.Text})}
	case AgentEventFallbackReply:
		return []EventRecord{build(EventRunFallbackReply, map[string]any{"text": event.Text})}
	case AgentEventDone:
		return []EventRecord{build(EventRunCompleted, nil)}
	case AgentEventError:
		message := "run failed"
		if event.Error != nil {
			message = event.Error.Error()
		}
		return []EventRecord{build(EventRunFailed, map[string]any{"message": message})}
	case AgentEventAborted:
		return []EventRecord{build(EventRunAborted, map[string]any{"message": "run aborted", "reason": "aborted"})}
	default:
		return nil
	}
}

func memoryCapturedPayload(call *llm.ToolCall, result *tools.ToolResult) (map[string]any, bool) {
	if call == nil || result == nil {
		return nil, false
	}
	if strings.TrimSpace(call.Name) != "memory_save" || strings.TrimSpace(result.Error) != "" {
		return nil, false
	}
	var entry struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Layer   string `json:"layer"`
		Source  string `json:"source"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result.Output), &entry); err != nil {
		return nil, false
	}
	id := strings.TrimSpace(entry.ID)
	if id == "" {
		return nil, false
	}
	payload := map[string]any{
		"id":           id,
		"tool_call_id": strings.TrimSpace(call.ID),
		"source":       "memory_save",
	}
	if title := strings.TrimSpace(entry.Title); title != "" {
		payload["title"] = title
	}
	if layer := strings.TrimSpace(entry.Layer); layer != "" {
		payload["layer"] = layer
	}
	if source := strings.TrimSpace(entry.Source); source != "" {
		payload["file"] = source
	}
	if summary := strings.TrimSpace(entry.Summary); summary != "" {
		payload["summary"] = summary
	}
	return payload, true
}

func normalizedToolInput(toolCall *llm.ToolCall) any {
	if toolCall == nil {
		return nil
	}
	if payload, err := tools.NormalizeCallAgentPayload(tools.SanitizeRawJSON(toolCall.Input)); err == nil && strings.TrimSpace(toolCall.Name) == "callagent" {
		return payload
	}
	return tools.SanitizeToolInputForTranscript(toolCall.Name, toolCall.Input)
}

func shouldRecordRuntimeEvent(event EventRecord) bool {
	switch strings.TrimSpace(event.Name) {
	case EventRunActivity, EventTextDelta, EventRunIncomplete, EventRunFallbackReply, EventLLMRetrying:
		return false
	default:
		return true
	}
}

func shouldBroadcastTransientRuntimeEvent(event EventRecord) bool {
	switch strings.TrimSpace(event.Name) {
	case EventRunActivity, EventTextDelta:
		return true
	default:
		return false
	}
}

func IsTerminalStatus(status RunStatus) bool {
	return status == RunStatusCompleted || status == RunStatusFailed || status == RunStatusAborted
}

func IsTerminalEvent(event EventRecord) bool {
	return event.Name == EventRunCompleted || event.Name == EventRunFailed || event.Name == EventRunAborted
}

func rebuildRunSequences(runEvents map[string][]EventRecord) map[string]int {
	if len(runEvents) == 0 {
		return make(map[string]int)
	}
	sequences := make(map[string]int, len(runEvents))
	for runID, events := range runEvents {
		maxSequence := 0
		for _, event := range events {
			if event.Sequence > maxSequence {
				maxSequence = event.Sequence
			}
		}
		sequences[runID] = maxSequence
	}
	return sequences
}

func serializeToolMetadata(meta tools.ToolMetadata) map[string]any {
	return map[string]any{
		"name":              meta.Name,
		"timeout_hint_ms":   meta.TimeoutHintMS,
		"effect":            meta.Effect,
		"tags":              meta.Tags,
		"allow_parallel":    meta.AllowParallel,
		"requires_approval": meta.RequiresApproval,
	}
}

func serializeToolFanoutCalls(calls []ToolFanoutCallInfo) []map[string]any {
	if len(calls) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		items = append(items, map[string]any{
			"id":              call.ID,
			"tool":            call.ToolName,
			"status":          call.Status,
			"started_order":   call.StartedOrder,
			"completed_order": call.CompletedOrder,
			"duration_ms":     call.DurationMS,
		})
	}
	return items
}

func buildRunTreeRecord(runs map[string]*RunRecord, runEvents map[string][]EventRecord, runTrees map[string]map[string]struct{}, runID string) (RunTreeRecord, bool) {
	runIDs, ok := runTrees[strings.TrimSpace(runID)]
	if !ok {
		return RunTreeRecord{}, false
	}

	tree := RunTreeRecord{}
	for runID := range runIDs {
		if run, ok := runs[runID]; ok {
			tree.Runs = append(tree.Runs, *run)
			tree.Events = append(tree.Events, runEvents[runID]...)
		}
	}
	sort.Slice(tree.Runs, func(i, j int) bool {
		return tree.Runs[i].StartedAt.Before(tree.Runs[j].StartedAt)
	})
	sortEventRecords(tree.Events)
	return tree, true
}

func sortEventRecords(events []EventRecord) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].Timestamp.Equal(events[j].Timestamp) {
			if events[i].RunID == events[j].RunID {
				return events[i].Sequence < events[j].Sequence
			}
			return events[i].RunID < events[j].RunID
		}
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
}

func (r *Recorder) ensureRunTreeLocked(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ""
	}
	if r.runTrees[runID] == nil {
		r.runTrees[runID] = make(map[string]struct{})
	}
	r.runTrees[runID][runID] = struct{}{}
	return runID
}

func (r *Recorder) attachSessionRunLocked(sessionID, runID string) {
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return
	}
	if r.sessionRuns[sessionID] == nil {
		r.sessionRuns[sessionID] = make(map[string]struct{})
	}
	r.sessionRuns[sessionID][runID] = struct{}{}
}

func (r *Recorder) persistRunLocked(run RunRecord) {
	if r.storage == nil {
		return
	}
	if err := r.storage.appendRun(run); err != nil {
		r.lastPersistError = err
		return
	}
	r.lastPersistError = nil
}

func (r *Recorder) persistEventLocked(event EventRecord) {
	if r.storage == nil {
		return
	}
	if err := r.storage.appendEvent(event); err != nil {
		r.lastPersistError = err
		return
	}
	r.lastPersistError = nil
}

func copyEventSubscribers(src map[chan EventRecord]struct{}) []chan EventRecord {
	if len(src) == 0 {
		return nil
	}
	out := make([]chan EventRecord, 0, len(src))
	for ch := range src {
		out = append(out, ch)
	}
	return out
}

func copyEventListeners(src map[int]Listener) []Listener {
	if len(src) == 0 {
		return nil
	}
	out := make([]Listener, 0, len(src))
	for _, listener := range src {
		out = append(out, listener)
	}
	return out
}

func normalizeRunNodeFields(run *RunRecord) {
	if run == nil {
		return
	}
	run.ID = strings.TrimSpace(run.ID)
	run.AgentID = strings.TrimSpace(run.AgentID)
	run.SessionID = strings.TrimSpace(run.SessionID)
	run.TaskID = strings.TrimSpace(run.TaskID)
	run.ParentTaskID = strings.TrimSpace(run.ParentTaskID)
	run.RunNodeID = strings.TrimSpace(run.RunNodeID)
	run.ParentRunNodeID = strings.TrimSpace(run.ParentRunNodeID)
	run.LegacyRunNodeID = strings.TrimSpace(run.LegacyRunNodeID)
	run.LegacyParentRunNodeID = strings.TrimSpace(run.LegacyParentRunNodeID)
	if run.RunNodeID == "" {
		run.RunNodeID = run.LegacyRunNodeID
	}
	if run.ParentRunNodeID == "" {
		run.ParentRunNodeID = run.LegacyParentRunNodeID
	}
	if run.RunNodeID == "" {
		run.RunNodeID = RunNodeID(run.ID, run.AgentID, run.TaskID)
	}
	run.LegacyRunNodeID = ""
	run.LegacyParentRunNodeID = ""
}

func (r *Recorder) applyEventRunNodeFieldsLocked(event *EventRecord) {
	if event == nil {
		return
	}
	event.RunNodeID = strings.TrimSpace(event.RunNodeID)
	event.ParentRunNodeID = strings.TrimSpace(event.ParentRunNodeID)
	event.LegacyRunNodeID = strings.TrimSpace(event.LegacyRunNodeID)
	event.LegacyParentRunNodeID = strings.TrimSpace(event.LegacyParentRunNodeID)
	if event.RunNodeID == "" {
		event.RunNodeID = event.LegacyRunNodeID
	}
	if event.ParentRunNodeID == "" {
		event.ParentRunNodeID = event.LegacyParentRunNodeID
	}
	if run := r.runs[event.RunID]; run != nil {
		normalizeRunNodeFields(run)
		applyEventRunNodeDefaults(event, run.RunNodeID, run.ParentRunNodeID, run.AgentID)
	}
	if event.RunNodeID == "" {
		event.RunNodeID = RunNodeID(event.RunID, event.AgentID, eventTaskID(event))
	}
	event.LegacyRunNodeID = ""
	event.LegacyParentRunNodeID = ""
}

func applyEventRunNodeDefaults(event *EventRecord, runRunNodeID, runParentRunNodeID, runAgentID string) {
	if event == nil {
		return
	}
	agentID := strings.TrimSpace(event.AgentID)
	runAgentID = strings.TrimSpace(runAgentID)
	runRunNodeID = strings.TrimSpace(runRunNodeID)
	runParentRunNodeID = strings.TrimSpace(runParentRunNodeID)
	if runRunNodeID == "" && runAgentID != "" {
		runRunNodeID = RunNodeID(event.RunID, runAgentID, "")
	}
	if event.RunNodeID == "" {
		if agentID == "" || runAgentID == "" || agentID == runAgentID {
			event.RunNodeID = runRunNodeID
		} else {
			event.RunNodeID = RunNodeID(event.RunID, agentID, eventTaskID(event))
			if event.ParentRunNodeID == "" {
				event.ParentRunNodeID = runRunNodeID
			}
		}
	}
	if event.ParentRunNodeID == "" {
		if strings.TrimSpace(event.RunNodeID) == runRunNodeID {
			event.ParentRunNodeID = runParentRunNodeID
		} else if runRunNodeID != "" {
			event.ParentRunNodeID = runRunNodeID
		}
	}
}

func RunNodeID(runID, agentID, taskID string) string {
	runID = strings.TrimSpace(runID)
	agentID = strings.TrimSpace(agentID)
	taskID = strings.TrimSpace(taskID)
	if runID == "" {
		return ""
	}
	if taskID == "" {
		return runID + "::" + agentID
	}
	return runID + "::" + agentID + "::" + taskID
}

func eventTaskID(event *EventRecord) string {
	if event == nil || event.Payload == nil {
		return ""
	}
	if value, ok := event.Payload["task_id"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func eventParentTaskID(event *EventRecord) string {
	if event == nil || event.Payload == nil {
		return ""
	}
	if value, ok := event.Payload["parent_task_id"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

type recorderState struct {
	runs        map[string]*RunRecord
	runEvents   map[string][]EventRecord
	runTrees    map[string]map[string]struct{}
	sessionRuns map[string]map[string]struct{}
}

func newRecorderState() *recorderState {
	return &recorderState{
		runs:        make(map[string]*RunRecord),
		runEvents:   make(map[string][]EventRecord),
		runTrees:    make(map[string]map[string]struct{}),
		sessionRuns: make(map[string]map[string]struct{}),
	}
}

func (s *recorderState) apply(record persistedRecord) {
	switch record.Kind {
	case "run":
		if record.Run == nil {
			return
		}
		runCopy := *record.Run
		normalizeRunNodeFields(&runCopy)
		s.runs[runCopy.ID] = &runCopy
		s.attachRun(runCopy.ID)
		s.attachSessionRun(runCopy.SessionID, runCopy.ID)
	case "event":
		if record.Event == nil {
			return
		}
		eventCopy := *record.Event
		eventCopy.RunNodeID = strings.TrimSpace(eventCopy.RunNodeID)
		eventCopy.ParentRunNodeID = strings.TrimSpace(eventCopy.ParentRunNodeID)
		eventCopy.LegacyRunNodeID = strings.TrimSpace(eventCopy.LegacyRunNodeID)
		eventCopy.LegacyParentRunNodeID = strings.TrimSpace(eventCopy.LegacyParentRunNodeID)
		if eventCopy.RunNodeID == "" {
			eventCopy.RunNodeID = eventCopy.LegacyRunNodeID
		}
		if eventCopy.ParentRunNodeID == "" {
			eventCopy.ParentRunNodeID = eventCopy.LegacyParentRunNodeID
		}
		if run := s.runs[eventCopy.RunID]; run != nil {
			applyEventRunNodeDefaults(&eventCopy, run.RunNodeID, run.ParentRunNodeID, run.AgentID)
		}
		if eventCopy.RunNodeID == "" {
			eventCopy.RunNodeID = RunNodeID(eventCopy.RunID, eventCopy.AgentID, eventTaskID(&eventCopy))
		}
		eventCopy.LegacyRunNodeID = ""
		eventCopy.LegacyParentRunNodeID = ""
		if eventCopy.Sequence <= 0 {
			eventCopy.Sequence = len(s.runEvents[eventCopy.RunID]) + 1
		}
		s.runEvents[eventCopy.RunID] = append(s.runEvents[eventCopy.RunID], eventCopy)
		s.attachRun(eventCopy.RunID)
		s.attachSessionRun(eventCopy.SessionID, eventCopy.RunID)
	}
}

func (s *recorderState) attachRun(runID string) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	if s.runTrees[runID] == nil {
		s.runTrees[runID] = make(map[string]struct{})
	}
	s.runTrees[runID][runID] = struct{}{}
}

func (s *recorderState) attachSessionRun(sessionID, runID string) {
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return
	}
	if s.sessionRuns[sessionID] == nil {
		s.sessionRuns[sessionID] = make(map[string]struct{})
	}
	s.sessionRuns[sessionID][runID] = struct{}{}
}

func newFileStore(dir string) (*fileStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("events dir is required")
	}
	store := &fileStore{
		dir:     dir,
		runsDir: filepath.Join(dir, "runs"),
	}
	if err := os.MkdirAll(store.runsDir, 0o755); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *fileStore) appendRun(run RunRecord) error {
	return s.append(run.ID, run.AgentID, run.CreatedAt, persistedRecord{Kind: "run", Run: &run})
}

func (s *fileStore) appendEvent(event EventRecord) error {
	return s.append(event.RunID, event.AgentID, event.Timestamp, persistedRecord{Kind: "event", Event: &event})
}

func (s *fileStore) append(runID, agentID string, timestamp time.Time, record persistedRecord) error {
	path := s.pathForRun(runID, agentID, timestamp)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	return encoder.Encode(record)
}

func (s *fileStore) load() (*recorderState, error) {
	state := newRecorderState()

	// 加载普通 runs
	matches, err := filepath.Glob(filepath.Join(s.runsDir, "*.jsonl"))
	if err != nil {
		return nil, err
	}

	// 加载按天滚动的 system 文件
	systemMatches, err := filepath.Glob(filepath.Join(s.dir, "system_*.jsonl"))
	if err != nil {
		return nil, err
	}
	matches = append(matches, systemMatches...)

	sort.Strings(matches)
	for _, path := range matches {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(file)
		decoder.UseNumber()
		for {
			var record persistedRecord
			if err := decoder.Decode(&record); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				_ = file.Close()
				return nil, err
			}
			state.apply(record)
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (s *fileStore) pathForRun(runID, agentID string, timestamp time.Time) string {
	runID = strings.TrimSpace(runID)
	agentID = strings.TrimSpace(agentID)

	// 系统 run 按天滚动
	if agentID == "system" {
		dateStr := timestamp.Format("2006-01-02")
		return filepath.Join(s.dir, "system_"+dateStr+".jsonl")
	}

	if runID == "" {
		return filepath.Join(s.runsDir, "unknown.jsonl")
	}
	return filepath.Join(s.runsDir, sanitizeSessionFilename(runID)+".jsonl")
}

func sanitizeSessionFilename(sessionID string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	sessionID = replacer.Replace(strings.TrimSpace(sessionID))
	if sessionID == "" {
		return "system"
	}
	return sessionID
}

func serializeMemoryMatches(matches []memory.SearchMatch) []map[string]any {
	if len(matches) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		out = append(out, map[string]any{
			"id":            match.Entry.ID,
			"title":         match.Entry.Title,
			"summary":       memory.PromptSummary(match.Entry),
			"layer":         match.Entry.Layer,
			"source":        match.Entry.FilePath,
			"matched_terms": append([]string(nil), match.MatchedTerms...),
			"metadata":      tools.SanitizeMetadata(mapFromStrings(match.Entry.Metadata)),
		})
	}
	return out
}

func mapFromStrings(meta map[string]string) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		out[key] = value
	}
	return out
}
