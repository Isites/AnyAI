package runtimeevents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/runtime/contract"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"github.com/Isites/anyai/internal/runtime/memory"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

type RunStatus string

const CurrentEventSchemaVersion = 1

const (
	maxPersistentLiveRunEventsPerRun     = 256
	maxPersistentTerminalRunEventsPerRun = 16
	rawIndexLinePrefixLimit              = 256 * 1024
	rawIndexLineSuffixLimit              = 64 * 1024
)

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
	sessionChildren  map[string]map[string]struct{}
	runEventFiles    map[string]map[string]struct{}
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

type persistedIndexRecord struct {
	Kind       string            `json:"kind"`
	SourcePath string            `json:"source_path,omitempty"`
	Run        *RunRecord        `json:"run,omitempty"`
	Event      *eventIndexRecord `json:"event,omitempty"`
}

type eventIndexRecord struct {
	SchemaVersion         int       `json:"schema_version,omitempty"`
	Sequence              int       `json:"sequence"`
	RunID                 string    `json:"run_id"`
	RunNodeID             string    `json:"run_node_id,omitempty"`
	ParentRunNodeID       string    `json:"parent_run_node_id,omitempty"`
	LegacyRunNodeID       string    `json:"trace_node_id,omitempty"`
	LegacyParentRunNodeID string    `json:"parent_trace_node_id,omitempty"`
	AgentID               string    `json:"agent_id"`
	SessionID             string    `json:"session_id"`
	Name                  string    `json:"name"`
	Timestamp             time.Time `json:"timestamp"`
	ChildSessionID        string    `json:"child_session_id,omitempty"`
}

type rawPersistedIndexRecord struct {
	Kind  string          `json:"kind"`
	Run   json.RawMessage `json:"run,omitempty"`
	Event json.RawMessage `json:"event,omitempty"`
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
		runs:            make(map[string]*RunRecord),
		runEvents:       make(map[string][]EventRecord),
		runSequences:    make(map[string]int),
		runTrees:        make(map[string]map[string]struct{}),
		sessionRuns:     make(map[string]map[string]struct{}),
		sessionChildren: make(map[string]map[string]struct{}),
		runEventFiles:   make(map[string]map[string]struct{}),
		subscribers:     make(map[string]map[chan EventRecord]struct{}),
		runTreeSubs:     make(map[string]map[chan EventRecord]struct{}),
		sessionSubs:     make(map[string]map[chan EventRecord]struct{}),
		listeners:       make(map[int]Listener),
		storage:         storage,
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
	state, err := r.storage.loadIndex()
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = state.runs
	r.runEvents = make(map[string][]EventRecord)
	r.runSequences = state.runSequences
	r.runTrees = state.runTrees
	r.sessionRuns = state.sessionRuns
	r.sessionChildren = state.sessionChildren
	r.runEventFiles = state.runEventFiles
	r.lastPersistError = nil
	return nil
}

func (r *Recorder) StartRun(run RunRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	normalizeRunNodeFields(&run)
	run = compactDurableRun(run)
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
	run = compactDurableRun(run)

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
	run.Output, _ = contract.SanitizeDurableText(output)
	run.Error, _ = contract.SanitizeDurableText(errMsg)
	run.CompletedAt = time.Now().UTC()
	r.persistRunLocked(*run)
}

func (r *Recorder) AbortActiveRuns(reason string) int {
	if r == nil {
		return 0
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "run interrupted by runtime restart"
	}

	now := time.Now().UTC()
	aborted := 0

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, run := range r.runs {
		if run == nil || (run.Status != RunStatusQueued && run.Status != RunStatusRunning) {
			continue
		}
		normalizeRunNodeFields(run)
		*run = compactDurableRun(*run)
		run.Status = RunStatusAborted
		run.Error, _ = contract.SanitizeDurableText(reason)
		run.CompletedAt = now
		r.ensureRunTreeLocked(run.ID)
		r.attachSessionRunLocked(run.SessionID, run.ID)
		r.persistRunLocked(*run)

		r.runSequences[run.ID]++
		event := EventRecord{
			SchemaVersion:   CurrentEventSchemaVersion,
			Sequence:        r.runSequences[run.ID],
			RunID:           run.ID,
			RunNodeID:       run.RunNodeID,
			ParentRunNodeID: run.ParentRunNodeID,
			AgentID:         run.AgentID,
			SessionID:       run.SessionID,
			Name:            EventRunAborted,
			Timestamp:       now,
			Payload: map[string]any{
				"message": reason,
				"reason":  "runtime_restart",
			},
		}
		r.appendLiveRunEventLocked(event)
		r.persistEventLocked(event)
		aborted++
	}

	return aborted
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
	event.Payload = contract.SanitizeDurablePayload(event.Payload)
	treeRunID = r.ensureRunTreeLocked(event.RunID)
	r.attachSessionRunLocked(event.SessionID, event.RunID)
	if persist {
		r.applyRunLifecycleEventLocked(event)
	}
	if persist {
		r.appendLiveRunEventLocked(event)
		r.attachSessionChildLocked(event.SessionID, childSessionIDFromPayload(event.Payload))
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
	runID = strings.TrimSpace(runID)
	if runID == "" || r == nil {
		return nil
	}
	events := r.loadStoredRunEvents(runID)
	r.mu.RLock()
	events = append(events, r.runEvents[runID]...)
	r.mu.RUnlock()
	events = dedupeEventRecords(events)
	sortEventRecords(events)
	return events
}

func (r *Recorder) ListRunEventsByNamePrefix(runID, prefix string) []EventRecord {
	runID = strings.TrimSpace(runID)
	prefix = strings.TrimSpace(prefix)
	if runID == "" || prefix == "" || r == nil {
		return nil
	}
	events := r.loadStoredRunEventsByNamePrefix(runID, prefix)
	r.mu.RLock()
	for _, event := range r.runEvents[runID] {
		if strings.HasPrefix(strings.TrimSpace(event.Name), prefix) {
			events = append(events, event)
		}
	}
	r.mu.RUnlock()
	events = dedupeEventRecords(events)
	sortEventRecords(events)
	return events
}

func (r *Recorder) ListSessionEvents(sessionID string) []EventRecord {
	r.mu.RLock()
	runIDs := r.relatedSessionRunIDsLocked(sessionID)
	r.mu.RUnlock()

	events := make([]EventRecord, 0)
	for runID := range runIDs {
		events = append(events, r.ListRunEvents(runID)...)
	}
	events = dedupeEventRecords(events)
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

		for runID := range r.sessionRuns[current] {
			relatedRuns[runID] = struct{}{}
		}
		for childSessionID := range r.sessionChildren[current] {
			childSessionID = strings.TrimSpace(childSessionID)
			if childSessionID != "" {
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
	runs, runIDs, ok := r.snapshotRunTreeLocked(runID)
	r.mu.RUnlock()
	if !ok {
		return RunTreeRecord{}, false
	}
	return r.buildRunTreeRecordFromSnapshot(runs, runIDs), true
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
	runs, runIDs, ok := r.snapshotRunTreeLocked(runID)
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}

	roots := make([]RunNode, 0, len(runIDs))
	for id := range runIDs {
		run, exists := runs[id]
		if !exists {
			continue
		}
		roots = append(roots, RunNode{
			Run:    run,
			Events: r.ListRunEvents(id),
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

func (r *Recorder) RunTreeSummary(runID string) ([]RunNode, bool) {
	r.mu.RLock()
	runs, runIDs, ok := r.snapshotRunTreeLocked(runID)
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}

	return buildRunTreeNodesFromRuns(runs, runIDs), true
}

func buildRunTreeNodesFromRuns(runs map[string]RunRecord, runIDs map[string]struct{}) []RunNode {
	nodesByKey := make(map[string]*RunNode, len(runs))
	orderByKey := make(map[string]time.Time, len(runs))
	childrenByKey := make(map[string][]string, len(runs))
	var rootKeys []string
	for id := range runIDs {
		run, exists := runs[id]
		if !exists {
			continue
		}
		runCopy := run
		normalizeRunNodeFields(&runCopy)
		key := strings.TrimSpace(runCopy.RunNodeID)
		if key == "" {
			key = RunNodeID(runCopy.ID, runCopy.AgentID, runCopy.TaskID)
		}
		if key == "" {
			key = runCopy.ID
		}
		nodesByKey[key] = &RunNode{Run: runCopy}
		orderByKey[key] = runTreeNodeSortTime(runCopy)
	}
	if len(nodesByKey) == 0 {
		return nil
	}
	for key, node := range nodesByKey {
		parentKey := strings.TrimSpace(node.Run.ParentRunNodeID)
		if parentKey == "" || parentKey == key || nodesByKey[parentKey] == nil {
			rootKeys = append(rootKeys, key)
			continue
		}
		childrenByKey[parentKey] = append(childrenByKey[parentKey], key)
	}
	sortRunNodeKeys(rootKeys, orderByKey)
	roots := make([]RunNode, 0, len(rootKeys))
	for _, key := range rootKeys {
		roots = append(roots, materializeRunNode(key, nodesByKey, childrenByKey, orderByKey))
	}
	return roots
}

func materializeRunNode(key string, nodesByKey map[string]*RunNode, childrenByKey map[string][]string, orderByKey map[string]time.Time) RunNode {
	node := nodesByKey[key]
	if node == nil {
		return RunNode{}
	}
	out := RunNode{Run: node.Run}
	childKeys := append([]string(nil), childrenByKey[key]...)
	sortRunNodeKeys(childKeys, orderByKey)
	for _, childKey := range childKeys {
		out.Children = append(out.Children, materializeRunNode(childKey, nodesByKey, childrenByKey, orderByKey))
	}
	return out
}

func sortRunNodeKeys(keys []string, orderByKey map[string]time.Time) {
	sort.Slice(keys, func(i, j int) bool {
		leftKey := strings.TrimSpace(keys[i])
		rightKey := strings.TrimSpace(keys[j])
		leftTime := orderByKey[leftKey]
		rightTime := orderByKey[rightKey]
		if leftTime.Equal(rightTime) {
			return leftKey < rightKey
		}
		return leftTime.Before(rightTime)
	})
}

func runTreeNodeSortTime(run RunRecord) time.Time {
	if !run.StartedAt.IsZero() {
		return run.StartedAt
	}
	return run.CreatedAt
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
		durableResult := tools.SanitizeToolResultForTranscript(*event.Result)
		payload := map[string]any{
			"id":     event.ToolCall.ID,
			"tool":   event.ToolCall.Name,
			"input":  tools.SanitizeToolInputForTranscript(event.ToolCall.Name, event.ToolCall.Input),
			"output": durableResult.Output,
			"error":  durableResult.Error,
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
		if len(durableResult.Metadata) > 0 {
			payload["metadata"] = durableResult.Metadata
		}
		if event.ToolMetadata != nil {
			payload["tool_metadata"] = serializeToolMetadata(*event.ToolMetadata)
		}
		eventName := EventToolCompleted
		if strings.TrimSpace(event.Result.Error) != "" {
			eventName = EventToolFailed
		}
		records := []EventRecord{build(eventName, payload)}
		if agentCallEventName, agentCallPayload, ok := AgentCallFinishedPayloadForTranscript(event.ToolCall, *event.Result); ok {
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

func dedupeEventRecords(events []EventRecord) []EventRecord {
	if len(events) < 2 {
		return events
	}
	out := make([]EventRecord, 0, len(events))
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		key := eventDedupeKey(event)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, event)
	}
	return out
}

func eventDedupeKey(event EventRecord) string {
	return strings.Join([]string{
		event.RunID,
		fmt.Sprint(event.Sequence),
		event.Name,
		event.Timestamp.UTC().Format(time.RFC3339Nano),
		event.AgentID,
		event.SessionID,
	}, "\x00")
}

func childSessionIDFromPayload(payload map[string]any) string {
	sessionID := strings.TrimSpace(StringPayload(payload, "session_id"))
	if sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(StringPayload(payload, "child_session_id"))
}

func childSessionIDFromRawPayload(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var partial struct {
		SessionID      string `json:"session_id"`
		ChildSessionID string `json:"child_session_id"`
	}
	if err := json.Unmarshal(payload, &partial); err != nil {
		return ""
	}
	if sessionID := strings.TrimSpace(partial.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(partial.ChildSessionID)
}

func indexRecordForRun(run RunRecord, sourcePath string) persistedIndexRecord {
	run = compactDurableRun(run)
	normalizeRunNodeFields(&run)
	return persistedIndexRecord{
		Kind:       "run",
		SourcePath: strings.TrimSpace(sourcePath),
		Run:        &run,
	}
}

func indexRecordForEvent(event EventRecord, sourcePath string) persistedIndexRecord {
	childSessionID := childSessionIDFromPayload(event.Payload)
	return persistedIndexRecord{
		Kind:       "event",
		SourcePath: strings.TrimSpace(sourcePath),
		Event: &eventIndexRecord{
			SchemaVersion:         event.SchemaVersion,
			Sequence:              event.Sequence,
			RunID:                 event.RunID,
			RunNodeID:             event.RunNodeID,
			ParentRunNodeID:       event.ParentRunNodeID,
			LegacyRunNodeID:       event.LegacyRunNodeID,
			LegacyParentRunNodeID: event.LegacyParentRunNodeID,
			AgentID:               event.AgentID,
			SessionID:             event.SessionID,
			Name:                  event.Name,
			Timestamp:             event.Timestamp,
			ChildSessionID:        childSessionID,
		},
	}
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

func (r *Recorder) attachSessionChildLocked(parentSessionID, childSessionID string) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	if parentSessionID == "" || childSessionID == "" || parentSessionID == childSessionID {
		return
	}
	if r.sessionChildren[parentSessionID] == nil {
		r.sessionChildren[parentSessionID] = make(map[string]struct{})
	}
	r.sessionChildren[parentSessionID][childSessionID] = struct{}{}
}

func (r *Recorder) appendLiveRunEventLocked(event EventRecord) {
	if r == nil || strings.TrimSpace(event.RunID) == "" {
		return
	}
	r.runEvents[event.RunID] = append(r.runEvents[event.RunID], event)
	if r.storage == nil {
		return
	}
	limit := maxPersistentLiveRunEventsPerRun
	if IsTerminalEvent(event) {
		limit = maxPersistentTerminalRunEventsPerRun
	}
	if limit <= 0 {
		delete(r.runEvents, event.RunID)
		return
	}
	if events := r.runEvents[event.RunID]; len(events) > limit {
		r.runEvents[event.RunID] = append([]EventRecord(nil), events[len(events)-limit:]...)
	}
}

func (r *Recorder) loadStoredRunEvents(runID string) []EventRecord {
	return r.loadStoredRunEventsFiltered(runID, "")
}

func (r *Recorder) loadStoredRunEventsByNamePrefix(runID, prefix string) []EventRecord {
	if r == nil || r.storage == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	prefix = strings.TrimSpace(prefix)
	if runID == "" || prefix == "" {
		return nil
	}
	paths := r.snapshotRunEventPaths(runID)
	events, err := r.storage.loadRunEventsByNamePrefixLight(runID, paths, prefix)
	if err != nil {
		r.mu.Lock()
		r.lastPersistError = err
		r.mu.Unlock()
		return nil
	}
	return events
}

func (r *Recorder) loadStoredRunEventsFiltered(runID, namePrefix string) []EventRecord {
	if r == nil || r.storage == nil {
		return nil
	}
	r.mu.RLock()
	runs := make(map[string]RunRecord, len(r.runs))
	for id, run := range r.runs {
		if run != nil {
			runs[id] = *run
		}
	}
	r.mu.RUnlock()
	paths := r.snapshotRunEventPaths(runID)
	events, err := r.storage.loadRunEventsFiltered(runID, paths, runs, namePrefix)
	if err != nil {
		r.mu.Lock()
		r.lastPersistError = err
		r.mu.Unlock()
		return nil
	}
	return events
}

func (r *Recorder) snapshotRunEventPaths(runID string) []string {
	runID = strings.TrimSpace(runID)
	if r == nil || runID == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	paths := make([]string, 0, len(r.runEventFiles[runID]))
	for path := range r.runEventFiles[runID] {
		paths = append(paths, path)
	}
	return paths
}

func (r *Recorder) snapshotRunTreeLocked(runID string) (map[string]RunRecord, map[string]struct{}, bool) {
	runID = strings.TrimSpace(runID)
	runIDs, ok := r.runTrees[runID]
	if !ok {
		return nil, nil, false
	}
	runCopies := make(map[string]RunRecord, len(runIDs))
	idCopies := make(map[string]struct{}, len(runIDs))
	for id := range runIDs {
		idCopies[id] = struct{}{}
		if run, exists := r.runs[id]; exists && run != nil {
			runCopies[id] = *run
		}
	}
	return runCopies, idCopies, true
}

func (r *Recorder) buildRunTreeRecordFromSnapshot(runs map[string]RunRecord, runIDs map[string]struct{}) RunTreeRecord {
	tree := RunTreeRecord{}
	for runID := range runIDs {
		run, ok := runs[runID]
		if !ok {
			continue
		}
		tree.Runs = append(tree.Runs, run)
		tree.Events = append(tree.Events, r.ListRunEvents(runID)...)
	}
	tree.Events = dedupeEventRecords(tree.Events)
	sort.Slice(tree.Runs, func(i, j int) bool {
		return tree.Runs[i].StartedAt.Before(tree.Runs[j].StartedAt)
	})
	sortEventRecords(tree.Events)
	return tree
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
	path := r.storage.pathForRun(event.RunID, event.AgentID, event.Timestamp)
	r.attachRunEventFileLocked(event.RunID, path)
	r.lastPersistError = nil
}

func (r *Recorder) attachRunEventFileLocked(runID, path string) {
	runID = strings.TrimSpace(runID)
	path = strings.TrimSpace(path)
	if runID == "" || path == "" {
		return
	}
	if r.runEventFiles[runID] == nil {
		r.runEventFiles[runID] = make(map[string]struct{})
	}
	r.runEventFiles[runID][path] = struct{}{}
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

func compactDurableRun(run RunRecord) RunRecord {
	if run.Input != "" {
		run.Input, _ = contract.SanitizeDurableText(run.Input)
	}
	if run.Output != "" {
		run.Output, _ = contract.SanitizeDurableText(run.Output)
	}
	if run.Error != "" {
		run.Error, _ = contract.SanitizeDurableText(run.Error)
	}
	return run
}

func normalizeLoadedEvent(event *EventRecord) {
	if event == nil {
		return
	}
	event.Payload = contract.SanitizeDurablePayload(event.Payload)
	event.RunID = strings.TrimSpace(event.RunID)
	event.AgentID = strings.TrimSpace(event.AgentID)
	event.SessionID = strings.TrimSpace(event.SessionID)
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
	if event.RunNodeID == "" {
		event.RunNodeID = RunNodeID(event.RunID, event.AgentID, eventTaskID(event))
	}
	event.LegacyRunNodeID = ""
	event.LegacyParentRunNodeID = ""
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
	runs             map[string]*RunRecord
	runFiles         map[string]map[string]struct{}
	runSequences     map[string]int
	runEventCounts   map[string]int
	indexEvents      []persistedIndexRecord
	indexEventSeen   map[string]struct{}
	indexSourceFiles map[string]struct{}
	runTrees         map[string]map[string]struct{}
	sessionRuns      map[string]map[string]struct{}
	sessionChildren  map[string]map[string]struct{}
	runEventFiles    map[string]map[string]struct{}
}

func newRecorderState() *recorderState {
	return &recorderState{
		runs:             make(map[string]*RunRecord),
		runFiles:         make(map[string]map[string]struct{}),
		runSequences:     make(map[string]int),
		runEventCounts:   make(map[string]int),
		indexEvents:      make([]persistedIndexRecord, 0),
		indexEventSeen:   make(map[string]struct{}),
		indexSourceFiles: make(map[string]struct{}),
		runTrees:         make(map[string]map[string]struct{}),
		sessionRuns:      make(map[string]map[string]struct{}),
		sessionChildren:  make(map[string]map[string]struct{}),
		runEventFiles:    make(map[string]map[string]struct{}),
	}
}

func (s *recorderState) apply(record persistedIndexRecord, sourcePath string) {
	if sourcePath = strings.TrimSpace(sourcePath); sourcePath == "" {
		sourcePath = strings.TrimSpace(record.SourcePath)
	}
	if sourcePath != "" {
		s.indexSourceFiles[sourcePath] = struct{}{}
	}
	switch record.Kind {
	case "run":
		if record.Run == nil {
			return
		}
		runCopy := *record.Run
		normalizeRunNodeFields(&runCopy)
		runCopy = compactDurableRun(runCopy)
		s.runs[runCopy.ID] = &runCopy
		s.attachRun(runCopy.ID)
		s.attachRunFile(runCopy.ID, sourcePath)
		s.attachSessionRun(runCopy.SessionID, runCopy.ID)
	case "event":
		if record.Event == nil {
			return
		}
		eventCopy := *record.Event
		eventCopy.RunID = strings.TrimSpace(eventCopy.RunID)
		eventCopy.AgentID = strings.TrimSpace(eventCopy.AgentID)
		eventCopy.SessionID = strings.TrimSpace(eventCopy.SessionID)
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
		if eventCopy.Sequence <= 0 {
			s.runEventCounts[eventCopy.RunID]++
			eventCopy.Sequence = s.runEventCounts[eventCopy.RunID]
		} else if eventCopy.Sequence > s.runEventCounts[eventCopy.RunID] {
			s.runEventCounts[eventCopy.RunID] = eventCopy.Sequence
		}
		if eventCopy.Sequence > s.runSequences[eventCopy.RunID] {
			s.runSequences[eventCopy.RunID] = eventCopy.Sequence
		}
		s.attachRun(eventCopy.RunID)
		s.attachSessionRun(eventCopy.SessionID, eventCopy.RunID)
		s.attachRunEventFile(eventCopy.RunID, sourcePath)
		s.attachSessionChild(eventCopy.SessionID, eventCopy.ChildSessionID)
		indexRecord := persistedIndexRecord{
			Kind:       "event",
			SourcePath: sourcePath,
			Event: &eventIndexRecord{
				SchemaVersion:         eventCopy.SchemaVersion,
				Sequence:              eventCopy.Sequence,
				RunID:                 eventCopy.RunID,
				RunNodeID:             eventCopy.RunNodeID,
				ParentRunNodeID:       eventCopy.ParentRunNodeID,
				LegacyRunNodeID:       eventCopy.LegacyRunNodeID,
				LegacyParentRunNodeID: eventCopy.LegacyParentRunNodeID,
				AgentID:               eventCopy.AgentID,
				SessionID:             eventCopy.SessionID,
				Name:                  eventCopy.Name,
				Timestamp:             eventCopy.Timestamp,
				ChildSessionID:        eventCopy.ChildSessionID,
			},
		}
		s.appendIndexEvent(indexRecord)
	}
}

func (s *recorderState) appendIndexEvent(record persistedIndexRecord) {
	if s == nil || record.Event == nil {
		return
	}
	key := indexEventDedupeKey(record)
	if _, ok := s.indexEventSeen[key]; ok {
		return
	}
	s.indexEventSeen[key] = struct{}{}
	s.indexEvents = append(s.indexEvents, record)
}

func indexEventDedupeKey(record persistedIndexRecord) string {
	event := record.Event
	if event == nil {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(record.SourcePath),
		strings.TrimSpace(event.RunID),
		fmt.Sprint(event.Sequence),
		strings.TrimSpace(event.Name),
		event.Timestamp.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(event.AgentID),
		strings.TrimSpace(event.SessionID),
	}, "\x00")
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

func (s *recorderState) attachRunFile(runID, path string) {
	runID = strings.TrimSpace(runID)
	path = strings.TrimSpace(path)
	if runID == "" || path == "" {
		return
	}
	if s.runFiles[runID] == nil {
		s.runFiles[runID] = make(map[string]struct{})
	}
	s.runFiles[runID][path] = struct{}{}
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

func (s *recorderState) attachSessionChild(parentSessionID, childSessionID string) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	if parentSessionID == "" || childSessionID == "" || parentSessionID == childSessionID {
		return
	}
	if s.sessionChildren[parentSessionID] == nil {
		s.sessionChildren[parentSessionID] = make(map[string]struct{})
	}
	s.sessionChildren[parentSessionID][childSessionID] = struct{}{}
}

func (s *recorderState) attachRunEventFile(runID, path string) {
	runID = strings.TrimSpace(runID)
	path = strings.TrimSpace(path)
	if runID == "" || path == "" {
		return
	}
	if s.runEventFiles[runID] == nil {
		s.runEventFiles[runID] = make(map[string]struct{})
	}
	s.runEventFiles[runID][path] = struct{}{}
}

func (s *recorderState) indexRecords() []persistedIndexRecord {
	records := make([]persistedIndexRecord, 0, len(s.runs)+len(s.indexEvents))
	runIDs := make([]string, 0, len(s.runs))
	for runID := range s.runs {
		runIDs = append(runIDs, runID)
	}
	sort.Strings(runIDs)
	for _, runID := range runIDs {
		run := s.runs[runID]
		if run == nil {
			continue
		}
		sourcePath := firstMapKey(s.runFiles[runID])
		if sourcePath == "" {
			sourcePath = firstMapKey(s.runEventFiles[runID])
		}
		records = append(records, indexRecordForRun(*run, sourcePath))
	}

	events := append([]persistedIndexRecord(nil), s.indexEvents...)
	sort.SliceStable(events, func(i, j int) bool {
		leftPath := strings.TrimSpace(events[i].SourcePath)
		rightPath := strings.TrimSpace(events[j].SourcePath)
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		left := events[i].Event
		right := events[j].Event
		if left == nil || right == nil {
			return left != nil
		}
		if left.RunID == right.RunID {
			return left.Sequence < right.Sequence
		}
		return left.RunID < right.RunID
	})
	records = append(records, events...)

	return records
}

func (s *recorderState) hasAllIndexSources(paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := s.indexSourceFiles[path]; !ok {
			return false
		}
	}
	return true
}

func firstMapKey(values map[string]struct{}) string {
	keys := mapKeys(values)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func mapKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
	path, err := s.append(run.ID, run.AgentID, run.CreatedAt, persistedRecord{Kind: "run", Run: &run})
	if err != nil {
		return err
	}
	if err := s.appendIndexRecord(indexRecordForRun(run, path)); err != nil {
		runtimelogging.Warn("failed to append event index record", "error", err)
	}
	return nil
}

func (s *fileStore) appendEvent(event EventRecord) error {
	path, err := s.append(event.RunID, event.AgentID, event.Timestamp, persistedRecord{Kind: "event", Event: &event})
	if err != nil {
		return err
	}
	if err := s.appendIndexRecord(indexRecordForEvent(event, path)); err != nil {
		runtimelogging.Warn("failed to append event index record", "error", err)
	}
	return nil
}

func (s *fileStore) append(runID, agentID string, timestamp time.Time, record persistedRecord) (string, error) {
	path := s.pathForRun(runID, agentID, timestamp)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(record); err != nil {
		return "", err
	}
	return path, nil
}

func (s *fileStore) appendIndexRecord(record persistedIndexRecord) error {
	file, err := os.OpenFile(s.indexPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	return encoder.Encode(record)
}

func (s *fileStore) loadIndex() (*recorderState, error) {
	rawPaths, err := s.eventLogPaths()
	if err != nil {
		return nil, err
	}
	if indexed, ok, err := s.loadSidecarIndex(rawPaths); err == nil && ok {
		return indexed, nil
	} else if err != nil {
		runtimelogging.Warn("failed to load event index, rebuilding from raw logs", "error", err)
	}

	state := newRecorderState()
	for _, path := range rawPaths {
		if err := s.loadRawIndexFile(path, state); err != nil {
			return nil, err
		}
	}
	if err := s.rewriteSidecarIndex(state); err != nil {
		runtimelogging.Warn("failed to rewrite event index, continuing with raw log index", "error", err)
	}
	return state, nil
}

func (s *fileStore) eventLogPaths() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(s.runsDir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	systemMatches, err := filepath.Glob(filepath.Join(s.dir, "system_*.jsonl"))
	if err != nil {
		return nil, err
	}
	matches = append(matches, systemMatches...)
	sort.Strings(matches)
	return matches, nil
}

func (s *fileStore) loadSidecarIndex(rawPaths []string) (*recorderState, bool, error) {
	indexPath := s.indexPath()
	if !s.isSidecarIndexFresh(indexPath, rawPaths) {
		return nil, false, nil
	}
	state := newRecorderState()
	if err := s.loadSidecarIndexFile(indexPath, state); err != nil {
		return nil, false, err
	}
	if !state.hasAllIndexSources(rawPaths) {
		return nil, false, nil
	}
	return state, true, nil
}

func (s *fileStore) isSidecarIndexFresh(indexPath string, rawPaths []string) bool {
	indexInfo, err := os.Stat(indexPath)
	if err != nil {
		return false
	}
	if indexInfo.Size() <= 0 && len(rawPaths) > 0 {
		return false
	}
	indexMod := indexInfo.ModTime()
	for _, path := range rawPaths {
		info, err := os.Stat(path)
		if err != nil {
			return false
		}
		if info.ModTime().After(indexMod) {
			return false
		}
	}
	return true
}

func (s *fileStore) loadSidecarIndexFile(path string, state *recorderState) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		record, ok, err := decodePersistedIndexLine(line)
		if err != nil {
			record, ok, err = decodePersistedIndexLineSample(line)
			if err != nil {
				return err
			}
		}
		if !ok {
			continue
		}
		state.apply(record, record.SourcePath)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (s *fileStore) loadRawIndexFile(path string, state *recorderState) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, result := readEventIndexLineSample(reader)
		if len(line) == 0 {
			if result.err == io.EOF {
				break
			}
			if result.err != nil {
				return fmt.Errorf("read event log index: %w", result.err)
			}
			continue
		}
		record, ok, err := decodePersistedIndexLine(line)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		record.SourcePath = path
		state.apply(record, path)
		if result.err == io.EOF {
			break
		}
		if result.err != nil {
			return fmt.Errorf("read event log index: %w", result.err)
		}
	}
	return nil
}

type eventIndexLineSampleRead struct {
	bytesRead int
	err       error
}

func readEventIndexLineSample(reader *bufio.Reader) ([]byte, eventIndexLineSampleRead) {
	if reader == nil {
		return nil, eventIndexLineSampleRead{err: io.EOF}
	}
	var prefix []byte
	suffix := make([]byte, 0, rawIndexLineSuffixLimit)
	total := 0
	for {
		chunk, err := reader.ReadSlice('\n')
		total += len(chunk)
		if len(chunk) > 0 {
			if len(prefix) < rawIndexLinePrefixLimit {
				remaining := rawIndexLinePrefixLimit - len(prefix)
				if remaining > len(chunk) {
					remaining = len(chunk)
				}
				prefix = append(prefix, chunk[:remaining]...)
			}
			suffix = appendSuffixWindow(suffix, chunk, rawIndexLineSuffixLimit)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && total == 0 {
			return nil, eventIndexLineSampleRead{err: io.EOF}
		}
		return buildEventIndexLineSample(prefix, suffix), eventIndexLineSampleRead{bytesRead: total, err: err}
	}
}

func appendSuffixWindow(dst, chunk []byte, limit int) []byte {
	if limit <= 0 || len(chunk) == 0 {
		return dst
	}
	if len(chunk) >= limit {
		return append(dst[:0], chunk[len(chunk)-limit:]...)
	}
	dst = append(dst, chunk...)
	if len(dst) > limit {
		copy(dst, dst[len(dst)-limit:])
		dst = dst[:limit]
	}
	return dst
}

func buildEventIndexLineSample(prefix, suffix []byte) []byte {
	prefix = bytes.TrimSpace(prefix)
	suffix = bytes.TrimSpace(suffix)
	if len(prefix) == 0 {
		return nil
	}
	if len(suffix) == 0 || bytes.HasSuffix(prefix, suffix) {
		return prefix
	}
	var sampled []byte
	sampled = append(sampled, prefix...)
	sampled = append(sampled, suffix...)
	return bytes.TrimSpace(sampled)
}

func (s *fileStore) rewriteSidecarIndex(state *recorderState) error {
	indexPath := s.indexPath()
	tmpPath := indexPath + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	records := state.indexRecords()
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, indexPath)
}

func decodePersistedIndexLine(line []byte) (persistedIndexRecord, bool, error) {
	var envelope struct {
		Kind       string `json:"kind"`
		SourcePath string `json:"source_path,omitempty"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return persistedIndexRecord{}, false, err
	}
	var raw rawPersistedIndexRecord
	if err := json.Unmarshal(line, &raw); err != nil {
		return persistedIndexRecord{}, false, err
	}
	switch raw.Kind {
	case "run":
		if len(raw.Run) == 0 {
			return persistedIndexRecord{Kind: raw.Kind, SourcePath: envelope.SourcePath}, true, nil
		}
		var run RunRecord
		if err := json.Unmarshal(raw.Run, &run); err != nil {
			return persistedIndexRecord{}, false, err
		}
		return persistedIndexRecord{Kind: raw.Kind, SourcePath: envelope.SourcePath, Run: &run}, true, nil
	case "event":
		if len(raw.Event) == 0 {
			return persistedIndexRecord{Kind: raw.Kind, SourcePath: envelope.SourcePath}, true, nil
		}
		event, err := decodeEventIndexRecord(raw.Event)
		if err != nil {
			return persistedIndexRecord{}, false, err
		}
		return persistedIndexRecord{Kind: raw.Kind, SourcePath: envelope.SourcePath, Event: &event}, true, nil
	default:
		return persistedIndexRecord{}, false, nil
	}
}

func decodePersistedIndexLineSample(line []byte) (persistedIndexRecord, bool, error) {
	kind := extractJSONPrefixString(line, "kind")
	switch kind {
	case "run":
		return decodePersistedRunIndexLineSample(line)
	case "event":
		return decodePersistedEventIndexLineSample(line)
	default:
		return persistedIndexRecord{}, false, nil
	}
}

func decodePersistedRunIndexLineSample(line []byte) (persistedIndexRecord, bool, error) {
	var raw rawPersistedIndexRecord
	if err := json.Unmarshal(line, &raw); err == nil && len(raw.Run) > 0 {
		var run RunRecord
		if err := json.Unmarshal(raw.Run, &run); err != nil {
			return persistedIndexRecord{}, false, err
		}
		return persistedIndexRecord{Kind: "run", Run: &run}, true, nil
	}
	id := extractJSONPrefixString(line, "id")
	if id == "" {
		return persistedIndexRecord{Kind: "run"}, true, nil
	}
	run := RunRecord{
		ID:                    id,
		RunNodeID:             extractJSONPrefixString(line, "run_node_id"),
		ParentRunNodeID:       extractJSONPrefixString(line, "parent_run_node_id"),
		LegacyRunNodeID:       extractJSONPrefixString(line, "trace_node_id"),
		LegacyParentRunNodeID: extractJSONPrefixString(line, "parent_trace_node_id"),
		ParentAgentID:         extractJSONPrefixString(line, "parent_agent_id"),
		AgentID:               extractJSONPrefixString(line, "agent_id"),
		SessionID:             extractJSONPrefixString(line, "session_id"),
		TaskID:                extractJSONPrefixString(line, "task_id"),
		ParentTaskID:          extractJSONPrefixString(line, "parent_task_id"),
		Model:                 extractJSONPrefixString(line, "model"),
		Channel:               extractJSONPrefixString(line, "channel"),
		Input:                 extractJSONPrefixString(line, "input"),
		Output:                extractJSONPrefixString(line, "output"),
		Error:                 extractJSONPrefixString(line, "error"),
		Status:                RunStatus(extractJSONPrefixString(line, "status")),
		CreatedAt:             extractJSONPrefixTime(line, "created_at"),
		StartedAt:             extractJSONPrefixTime(line, "started_at"),
		CompletedAt:           extractJSONPrefixTime(line, "completed_at"),
	}
	return persistedIndexRecord{Kind: "run", Run: &run}, true, nil
}

func decodePersistedEventIndexLineSample(line []byte) (persistedIndexRecord, bool, error) {
	event := eventIndexRecord{
		SchemaVersion:         int(extractJSONPrefixInt64(line, "schema_version")),
		Sequence:              int(extractJSONPrefixInt64(line, "sequence")),
		RunID:                 extractJSONPrefixString(line, "run_id"),
		RunNodeID:             extractJSONPrefixString(line, "run_node_id"),
		ParentRunNodeID:       extractJSONPrefixString(line, "parent_run_node_id"),
		LegacyRunNodeID:       extractJSONPrefixString(line, "trace_node_id"),
		LegacyParentRunNodeID: extractJSONPrefixString(line, "parent_trace_node_id"),
		AgentID:               extractJSONPrefixString(line, "agent_id"),
		SessionID:             extractJSONPrefixString(line, "session_id"),
		Name:                  extractJSONPrefixString(line, "name"),
		Timestamp:             extractJSONPrefixTime(line, "timestamp"),
		ChildSessionID:        childSessionIDFromSample(line),
	}
	if event.RunID == "" && event.Name == "" {
		return persistedIndexRecord{Kind: "event"}, true, nil
	}
	return persistedIndexRecord{Kind: "event", Event: &event}, true, nil
}

func childSessionIDFromSample(line []byte) string {
	if value := extractJSONPrefixString(line, "child_session_id"); value != "" {
		return value
	}
	if idx := bytes.Index(line, []byte(`"payload"`)); idx >= 0 {
		payloadSample := line[idx:]
		if value := extractJSONPrefixString(payloadSample, "session_id"); value != "" {
			return value
		}
		return extractJSONPrefixString(payloadSample, "child_session_id")
	}
	return ""
}

func extractJSONPrefixString(data []byte, field string) string {
	value := extractJSONPrefixRawValue(data, field)
	if len(value) == 0 || value[0] != '"' {
		return ""
	}
	end := 1
	escaped := false
	for end < len(value) {
		ch := value[end]
		if escaped {
			escaped = false
			end++
			continue
		}
		if ch == '\\' {
			escaped = true
			end++
			continue
		}
		if ch == '"' {
			var out string
			if err := json.Unmarshal(value[:end+1], &out); err != nil {
				return ""
			}
			return strings.TrimSpace(out)
		}
		end++
	}
	return ""
}

func extractJSONPrefixInt64(data []byte, field string) int64 {
	value := extractJSONPrefixRawValue(data, field)
	if len(value) == 0 {
		return 0
	}
	end := 0
	for end < len(value) {
		ch := value[end]
		if (ch >= '0' && ch <= '9') || (end == 0 && ch == '-') {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.ParseInt(string(value[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func extractJSONPrefixTime(data []byte, field string) time.Time {
	value := extractJSONPrefixString(data, field)
	if value == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return ts
}

func extractJSONPrefixRawValue(data []byte, field string) []byte {
	needle, err := json.Marshal(field)
	if err != nil {
		return nil
	}
	idx := bytes.Index(data, needle)
	if idx < 0 {
		return nil
	}
	rest := data[idx+len(needle):]
	rest = bytes.TrimLeft(rest, " \t\r\n")
	if len(rest) == 0 || rest[0] != ':' {
		return nil
	}
	rest = bytes.TrimLeft(rest[1:], " \t\r\n")
	return rest
}

func decodeEventIndexRecord(data []byte) (eventIndexRecord, error) {
	var raw struct {
		SchemaVersion         int       `json:"schema_version,omitempty"`
		Sequence              int       `json:"sequence"`
		RunID                 string    `json:"run_id"`
		RunNodeID             string    `json:"run_node_id,omitempty"`
		ParentRunNodeID       string    `json:"parent_run_node_id,omitempty"`
		LegacyRunNodeID       string    `json:"trace_node_id,omitempty"`
		LegacyParentRunNodeID string    `json:"parent_trace_node_id,omitempty"`
		AgentID               string    `json:"agent_id"`
		SessionID             string    `json:"session_id"`
		Name                  string    `json:"name"`
		Timestamp             time.Time `json:"timestamp"`
		ChildSessionID        string    `json:"child_session_id,omitempty"`
		Payload               struct {
			SessionID      string `json:"session_id"`
			ChildSessionID string `json:"child_session_id"`
		} `json:"payload,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return eventIndexRecord{}, err
	}
	childSessionID := strings.TrimSpace(raw.ChildSessionID)
	if childSessionID == "" {
		childSessionID = strings.TrimSpace(raw.Payload.SessionID)
	}
	if childSessionID == "" {
		childSessionID = strings.TrimSpace(raw.Payload.ChildSessionID)
	}
	return eventIndexRecord{
		SchemaVersion:         raw.SchemaVersion,
		Sequence:              raw.Sequence,
		RunID:                 raw.RunID,
		RunNodeID:             raw.RunNodeID,
		ParentRunNodeID:       raw.ParentRunNodeID,
		LegacyRunNodeID:       raw.LegacyRunNodeID,
		LegacyParentRunNodeID: raw.LegacyParentRunNodeID,
		AgentID:               raw.AgentID,
		SessionID:             raw.SessionID,
		Name:                  raw.Name,
		Timestamp:             raw.Timestamp,
		ChildSessionID:        childSessionID,
	}, nil
}

func (s *fileStore) loadRunEvents(runID string, paths []string, runs map[string]RunRecord) ([]EventRecord, error) {
	return s.loadRunEventsFiltered(runID, paths, runs, "")
}

func (s *fileStore) loadRunEventsFiltered(runID string, paths []string, runs map[string]RunRecord, namePrefix string) ([]EventRecord, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	namePrefix = strings.TrimSpace(namePrefix)
	if len(paths) == 0 {
		paths = []string{s.pathForRun(runID, "", time.Time{})}
	}
	sort.Strings(paths)
	events := make([]EventRecord, 0)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
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
			if record.Kind != "event" || record.Event == nil {
				continue
			}
			eventCopy := *record.Event
			if strings.TrimSpace(eventCopy.RunID) != runID {
				continue
			}
			if namePrefix != "" && !strings.HasPrefix(strings.TrimSpace(eventCopy.Name), namePrefix) {
				continue
			}
			normalizeLoadedEvent(&eventCopy)
			if run, ok := runs[eventCopy.RunID]; ok {
				applyEventRunNodeDefaults(&eventCopy, run.RunNodeID, run.ParentRunNodeID, run.AgentID)
			}
			events = append(events, eventCopy)
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	return dedupeEventRecords(events), nil
}

func (s *fileStore) loadRunEventsByNamePrefixLight(runID string, paths []string, namePrefix string) ([]EventRecord, error) {
	runID = strings.TrimSpace(runID)
	namePrefix = strings.TrimSpace(namePrefix)
	if runID == "" || namePrefix == "" {
		return nil, nil
	}
	if len(paths) == 0 {
		paths = []string{s.pathForRun(runID, "", time.Time{})}
	}
	sort.Strings(paths)
	events := make([]EventRecord, 0)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		reader := bufio.NewReaderSize(file, 64*1024)
		for {
			line, result := readEventIndexLineSample(reader)
			if len(line) == 0 {
				if result.err == io.EOF {
					break
				}
				if result.err != nil {
					_ = file.Close()
					return nil, fmt.Errorf("read event log prefix: %w", result.err)
				}
				continue
			}
			if result.err != nil && result.err != io.EOF {
				_ = file.Close()
				return nil, fmt.Errorf("read event log prefix: %w", result.err)
			}
			event, ok, err := decodeLightEventLineByNamePrefix(line, runID, namePrefix)
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			if ok {
				events = append(events, event)
			}
			if result.err == io.EOF {
				break
			}
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	return dedupeEventRecords(events), nil
}

func decodeLightEventLineByNamePrefix(line []byte, runID, namePrefix string) (EventRecord, bool, error) {
	if extractJSONPrefixString(line, "kind") != "event" {
		return EventRecord{}, false, nil
	}
	eventLine := line
	if idx := bytes.Index(line, []byte(`"event"`)); idx >= 0 {
		eventLine = line[idx:]
	}
	eventRunID := extractJSONPrefixString(eventLine, "run_id")
	name := extractJSONPrefixString(eventLine, "name")
	if eventRunID != runID || !strings.HasPrefix(name, namePrefix) {
		return EventRecord{}, false, nil
	}
	var payloadOnly struct {
		Event struct {
			Payload map[string]any `json:"payload,omitempty"`
		} `json:"event,omitempty"`
	}
	if err := json.Unmarshal(line, &payloadOnly); err != nil {
		return EventRecord{}, false, err
	}
	event := EventRecord{
		SchemaVersion:         int(extractJSONPrefixInt64(eventLine, "schema_version")),
		Sequence:              int(extractJSONPrefixInt64(eventLine, "sequence")),
		RunID:                 eventRunID,
		RunNodeID:             extractJSONPrefixString(eventLine, "run_node_id"),
		ParentRunNodeID:       extractJSONPrefixString(eventLine, "parent_run_node_id"),
		LegacyRunNodeID:       extractJSONPrefixString(eventLine, "trace_node_id"),
		LegacyParentRunNodeID: extractJSONPrefixString(eventLine, "parent_trace_node_id"),
		AgentID:               extractJSONPrefixString(eventLine, "agent_id"),
		SessionID:             extractJSONPrefixString(eventLine, "session_id"),
		Name:                  name,
		Timestamp:             extractJSONPrefixTime(eventLine, "timestamp"),
		Payload:               contract.SanitizeDurablePayload(payloadOnly.Event.Payload),
	}
	normalizeLoadedEvent(&event)
	return event, true, nil
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

func (s *fileStore) indexPath() string {
	return filepath.Join(s.dir, "index.jsonl")
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
