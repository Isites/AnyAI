package task

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
)

type Option func(*Store)

func WithEventAppender(appender func(runtimeevents.EventRecord)) Option {
	return func(s *Store) {
		if s == nil {
			return
		}
		s.appender = appender
	}
}

type Store struct {
	mu          sync.RWMutex
	tasks       map[string]*Record
	subscribers map[string]map[chan runtimeevents.EventRecord]struct{}
	cancels     map[string]context.CancelFunc
	appender    func(runtimeevents.EventRecord)
}

func NewStore(opts ...Option) *Store {
	s := &Store{
		tasks:       make(map[string]*Record),
		subscribers: make(map[string]map[chan runtimeevents.EventRecord]struct{}),
		cancels:     make(map[string]context.CancelFunc),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Store) SetEventAppender(appender func(runtimeevents.EventRecord)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appender = appender
}

func (s *Store) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = make(map[string]*Record)
	s.cancels = make(map[string]context.CancelFunc)
}

func (s *Store) Create(agentID, input, sessionID, runID string, contract Contract) *Record {
	return s.CreateSpec(Spec{
		Kind:      KindAgent,
		AgentID:   agentID,
		RunID:     runID,
		SessionID: sessionID,
		Input:     input,
		Contract:  contract,
	})
}

func (s *Store) CreateSpec(spec Spec) *Record {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	record := &Record{
		ID:            NewID(),
		Kind:          normalizeKind(spec.Kind),
		Status:        StatusRunning,
		AgentID:       strings.TrimSpace(spec.AgentID),
		RunID:         strings.TrimSpace(spec.RunID),
		SessionID:     strings.TrimSpace(spec.SessionID),
		ParentTaskID:  strings.TrimSpace(spec.ParentTaskID),
		IdleTimeoutMS: maxInt(spec.IdleTimeoutMS, 0),
		Input:         strings.TrimSpace(spec.Input),
		TargetAgent:   strings.TrimSpace(spec.TargetAgent),
		ToolName:      strings.TrimSpace(spec.ToolName),
		ProcessName:   strings.TrimSpace(spec.ProcessName),
		Contract:      spec.Contract.Normalized(),
		Metadata:      cloneMap(spec.Metadata),
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	s.mu.Lock()
	s.tasks[record.ID] = cloneRecordPtr(record)
	s.mu.Unlock()
	s.emit(runtimeevents.EventTaskQueued, *record)
	return cloneRecordPtr(record)
}

func (s *Store) Running(id string, metadata map[string]any) (Record, bool) {
	return s.transition(id, func(record *Record, now time.Time) string {
		if isTerminal(record.Status) {
			return ""
		}
		record.Status = StatusRunning
		record.UpdatedAt = now
		record.Metadata = mergeMaps(record.Metadata, metadata)
		if record.StartedAt.IsZero() {
			record.StartedAt = now
		}
		record.LastActivityAt = now
		return runtimeevents.EventTaskStarted
	})
}

func (s *Store) Touch(id string, metadata map[string]any) (Record, bool) {
	return s.transition(id, func(record *Record, now time.Time) string {
		if isTerminal(record.Status) {
			return ""
		}
		record.Status = StatusRunning
		record.UpdatedAt = now
		record.LastActivityAt = now
		record.Metadata = mergeMaps(record.Metadata, metadata)
		if record.StartedAt.IsZero() {
			record.StartedAt = now
			return runtimeevents.EventTaskStarted
		}
		return runtimeevents.EventTaskRunning
	})
}

func (s *Store) Complete(id string, result Result) (Record, bool) {
	return s.transition(id, func(record *Record, now time.Time) string {
		if isTerminal(record.Status) {
			return ""
		}
		record.Status = StatusCompleted
		record.UpdatedAt = now
		record.LastActivityAt = now
		record.CompletedAt = now
		record.Summary = strings.TrimSpace(result.Summary)
		record.Error = ""
		if sessionID := strings.TrimSpace(result.SessionID); sessionID != "" {
			record.SessionID = sessionID
		}
		record.Metadata = mergeMaps(record.Metadata, result.Metadata)
		return runtimeevents.EventTaskCompleted
	})
}

func (s *Store) Fail(id string, result Result) (Record, bool) {
	return s.transition(id, func(record *Record, now time.Time) string {
		if isTerminal(record.Status) {
			return ""
		}
		record.Status = StatusFailed
		record.UpdatedAt = now
		record.LastActivityAt = now
		record.CompletedAt = now
		record.Summary = strings.TrimSpace(result.Summary)
		record.Error = strings.TrimSpace(result.Error)
		if sessionID := strings.TrimSpace(result.SessionID); sessionID != "" {
			record.SessionID = sessionID
		}
		record.Metadata = mergeMaps(record.Metadata, result.Metadata)
		return runtimeevents.EventTaskFailed
	})
}

func (s *Store) Cancel(id string) bool {
	return s.CancelWithReason(id, "cancelled")
}

func (s *Store) CancelWithReason(id, reason string) bool {
	record, ok := s.transition(id, func(record *Record, now time.Time) string {
		if isTerminal(record.Status) {
			return ""
		}
		record.Status = StatusCancelled
		record.UpdatedAt = now
		record.LastActivityAt = now
		record.CompletedAt = now
		record.Error = strings.TrimSpace(reason)
		return runtimeevents.EventTaskCancelled
	})
	if !ok {
		return false
	}
	cancel := s.cancelFor(record.ID)
	if cancel != nil {
		cancel()
	}
	return true
}

func (s *Store) Get(id string) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record := s.tasks[strings.TrimSpace(id)]
	if record == nil {
		return Record{}, false
	}
	return cloneRecord(*record), true
}

func (s *Store) List() []Record {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, 0, len(s.tasks))
	for _, record := range s.tasks {
		if !isVisibleTask(*record) {
			continue
		}
		out = append(out, cloneRecord(*record))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (s *Store) ListAll() []Record {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, 0, len(s.tasks))
	for _, record := range s.tasks {
		out = append(out, cloneRecord(*record))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (s *Store) Subscribe(id string) (<-chan runtimeevents.EventRecord, func()) {
	ch := make(chan runtimeevents.EventRecord, 16)
	if s == nil {
		close(ch)
		return ch, func() {}
	}
	taskID := strings.TrimSpace(id)
	s.mu.Lock()
	if s.subscribers[taskID] == nil {
		s.subscribers[taskID] = make(map[chan runtimeevents.EventRecord]struct{})
	}
	s.subscribers[taskID][ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if subs := s.subscribers[taskID]; subs != nil {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(s.subscribers, taskID)
			}
		}
		s.mu.Unlock()
		close(ch)
	}
}

func (s *Store) finishExecution(taskID string, runCtx context.Context, result Result, err error) (Record, bool) {
	if err != nil {
		if strings.TrimSpace(result.Error) == "" {
			result.Error = err.Error()
		}
		if isCancellation(err) && result.Status == StatusCancelled {
			return s.cancelWithResult(taskID, result)
		}
		return s.Fail(taskID, result)
	}
	switch result.Status {
	case StatusCancelled:
		return s.cancelWithResult(taskID, result)
	case StatusFailed:
		return s.Fail(taskID, result)
	case StatusCompleted:
		return s.Complete(taskID, result)
	}
	if runCtx != nil && runCtx.Err() != nil && isCancellation(runCtx.Err()) {
		if strings.TrimSpace(result.Error) == "" {
			result.Error = runCtx.Err().Error()
		}
		return s.cancelWithResult(taskID, result)
	}
	switch result.Status {
	default:
		return s.Complete(taskID, result)
	}
}

func (s *Store) cancelWithResult(taskID string, result Result) (Record, bool) {
	return s.transition(taskID, func(record *Record, now time.Time) string {
		if isTerminal(record.Status) {
			return ""
		}
		record.Status = StatusCancelled
		record.UpdatedAt = now
		record.LastActivityAt = now
		record.CompletedAt = now
		record.Summary = strings.TrimSpace(result.Summary)
		record.Error = strings.TrimSpace(result.Error)
		if sessionID := strings.TrimSpace(result.SessionID); sessionID != "" {
			record.SessionID = sessionID
		}
		record.Metadata = mergeMaps(record.Metadata, result.Metadata)
		return runtimeevents.EventTaskCancelled
	})
}

func (s *Store) Rebuild(recorder *runtimeevents.Recorder) error {
	if s == nil {
		return nil
	}
	if recorder == nil {
		s.Reset()
		return nil
	}

	runs := recorder.ListRuns()
	eventsByTask := make(map[string][]runtimeevents.EventRecord)
	for _, run := range runs {
		for _, event := range recorder.ListRunEvents(run.ID) {
			taskID := taskIDFromPayload(event.Payload)
			if taskID == "" || !strings.HasPrefix(event.Name, "task.") {
				continue
			}
			eventsByTask[taskID] = append(eventsByTask[taskID], event)
		}
	}

	s.Reset()
	for _, events := range eventsByTask {
		sort.Slice(events, func(i, j int) bool {
			if events[i].Timestamp.Equal(events[j].Timestamp) {
				return events[i].Sequence < events[j].Sequence
			}
			return events[i].Timestamp.Before(events[j].Timestamp)
		})
		for _, event := range events {
			s.applyEvent(event)
		}
	}
	return nil
}

func (s *Store) applyEvent(event runtimeevents.EventRecord) {
	taskID := taskIDFromPayload(event.Payload)
	if taskID == "" {
		return
	}
	s.mu.Lock()
	record := s.tasks[taskID]
	if record == nil {
		record = &Record{
			ID:        taskID,
			Kind:      parseKind(payloadString(event.Payload, "task_kind")),
			AgentID:   strings.TrimSpace(event.AgentID),
			RunID:     strings.TrimSpace(event.RunID),
			SessionID: strings.TrimSpace(event.SessionID),
			CreatedAt: event.Timestamp,
		}
		s.tasks[taskID] = record
	}
	record.Kind = normalizeKind(firstNonEmptyKind(record.Kind, parseKind(payloadString(event.Payload, "task_kind"))))
	if value := strings.TrimSpace(event.AgentID); value != "" {
		record.AgentID = value
	}
	if value := strings.TrimSpace(event.RunID); value != "" {
		record.RunID = value
	}
	if value := strings.TrimSpace(event.SessionID); value != "" {
		record.SessionID = value
	}
	record.ParentTaskID = firstNonEmpty(record.ParentTaskID, payloadString(event.Payload, "parent_task_id"))
	if idleTimeoutMS := payloadInt(event.Payload, "idle_timeout_ms"); idleTimeoutMS > 0 && record.IdleTimeoutMS <= 0 {
		record.IdleTimeoutMS = idleTimeoutMS
	}
	record.Input = firstNonEmpty(record.Input, payloadString(event.Payload, "input"))
	record.TargetAgent = firstNonEmpty(record.TargetAgent, payloadString(event.Payload, "target_agent"))
	record.ToolName = firstNonEmpty(record.ToolName, payloadString(event.Payload, "tool"))
	record.ProcessName = firstNonEmpty(record.ProcessName, payloadString(event.Payload, "process"))
	if value := payloadString(event.Payload, "summary"); value != "" {
		record.Summary = value
	}
	if value := payloadString(event.Payload, "error"); value != "" {
		record.Error = value
	}
	record.Metadata = mergeMaps(record.Metadata, payloadMap(event.Payload, "metadata"))
	record.Contract = mergeContract(record.Contract, payloadContract(event.Payload))
	if lastActivityAt := payloadTime(event.Payload, "last_activity_at"); !lastActivityAt.IsZero() {
		record.LastActivityAt = lastActivityAt
	}
	record.UpdatedAt = event.Timestamp
	switch event.Name {
	case runtimeevents.EventTaskQueued:
		record.Status = StatusRunning
	case runtimeevents.EventTaskStarted, runtimeevents.EventTaskRunning:
		record.Status = StatusRunning
		if record.StartedAt.IsZero() {
			record.StartedAt = event.Timestamp
		}
		if record.LastActivityAt.IsZero() || record.LastActivityAt.Before(event.Timestamp) {
			record.LastActivityAt = event.Timestamp
		}
	case runtimeevents.EventTaskCompleted:
		record.Status = StatusCompleted
		record.CompletedAt = event.Timestamp
	case runtimeevents.EventTaskFailed:
		record.Status = StatusFailed
		record.CompletedAt = event.Timestamp
	case runtimeevents.EventTaskCancelled:
		record.Status = StatusCancelled
		record.CompletedAt = event.Timestamp
	}
	s.mu.Unlock()
}

func (s *Store) transition(id string, mutate func(*Record, time.Time) string) (Record, bool) {
	if s == nil || mutate == nil {
		return Record{}, false
	}
	taskID := strings.TrimSpace(id)
	var (
		record    Record
		eventName string
	)
	s.mu.Lock()
	current := s.tasks[taskID]
	if current == nil {
		s.mu.Unlock()
		return Record{}, false
	}
	eventName = mutate(current, time.Now().UTC())
	if eventName == "" {
		record = cloneRecord(*current)
		s.mu.Unlock()
		return record, false
	}
	record = cloneRecord(*current)
	s.mu.Unlock()

	s.emit(eventName, record)
	return record, true
}

func (s *Store) emit(name string, record Record) {
	if s == nil {
		return
	}
	event := runtimeevents.EventRecord{
		RunID:     record.RunID,
		AgentID:   record.AgentID,
		SessionID: record.SessionID,
		Name:      name,
		Timestamp: time.Now().UTC(),
		Payload:   buildPayload(record),
	}
	s.mu.RLock()
	appender := s.appender
	subs := copySubscribers(s.subscribers[record.ID])
	s.mu.RUnlock()

	if appender != nil {
		appender(event)
	}
	for _, ch := range subs {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Store) cancelFor(taskID string) context.CancelFunc {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cancels[strings.TrimSpace(taskID)]
}

func (s *Store) setCancel(taskID string, cancel context.CancelFunc) {
	if s == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel == nil {
		delete(s.cancels, taskID)
		return
	}
	s.cancels[taskID] = cancel
}

func (s *Store) clearCancel(taskID string) {
	s.setCancel(taskID, nil)
}

func buildPayload(record Record) map[string]any {
	payload := map[string]any{
		"task_id":   record.ID,
		"task_kind": string(record.Kind),
		"status":    string(record.Status),
	}
	if record.ParentTaskID != "" {
		payload["parent_task_id"] = record.ParentTaskID
	}
	if record.IdleTimeoutMS > 0 {
		payload["idle_timeout_ms"] = record.IdleTimeoutMS
	}
	if record.Input != "" {
		payload["input"] = record.Input
	}
	if record.TargetAgent != "" {
		payload["target_agent"] = record.TargetAgent
	}
	if record.ToolName != "" {
		payload["tool"] = record.ToolName
	}
	if record.ProcessName != "" {
		payload["process"] = record.ProcessName
	}
	if record.Summary != "" {
		payload["summary"] = record.Summary
	}
	if record.Error != "" {
		payload["error"] = record.Error
	}
	if contract := record.Contract.ToMap(); len(contract) > 0 {
		payload["contract"] = contract
	}
	if !record.LastActivityAt.IsZero() {
		payload["last_activity_at"] = record.LastActivityAt
	}
	if len(record.Metadata) > 0 {
		payload["metadata"] = cloneMap(record.Metadata)
	}
	return payload
}

func isVisibleTask(record Record) bool {
	value, _ := record.Metadata[MetadataVisibilityKey].(string)
	switch strings.TrimSpace(value) {
	case "", VisibilityPublic:
		return true
	case VisibilityInternal:
		return false
	default:
		return true
	}
}

func taskIDFromPayload(payload map[string]any) string {
	return payloadString(payload, "task_id")
}

func payloadString(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func payloadMap(payload map[string]any, key string) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	value, _ := payload[key].(map[string]any)
	return cloneMap(value)
}

func payloadInt(payload map[string]any, key string) int {
	if len(payload) == 0 {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func payloadTime(payload map[string]any, key string) time.Time {
	if len(payload) == 0 {
		return time.Time{}
	}
	switch value := payload[key].(type) {
	case time.Time:
		return value
	case string:
		parsed, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
		return parsed
	default:
		return time.Time{}
	}
}

func payloadContract(payload map[string]any) Contract {
	if len(payload) == 0 {
		return Contract{}
	}
	raw, _ := payload["contract"].(map[string]any)
	if len(raw) == 0 {
		return Contract{}
	}
	contract := Contract{
		TaskGoal:   payloadString(raw, "task_goal"),
		Workspace:  payloadString(raw, "workspace"),
		ReturnMode: payloadString(raw, "return_mode"),
	}
	if values, ok := raw["expected_outputs"].([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				contract.ExpectedOutputs = append(contract.ExpectedOutputs, strings.TrimSpace(text))
			}
		}
	}
	if values, ok := raw["input_artifacts"].([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				contract.InputArtifacts = append(contract.InputArtifacts, strings.TrimSpace(text))
			}
		}
	}
	return contract.Normalized()
}

func mergeContract(current, next Contract) Contract {
	current = current.Normalized()
	next = next.Normalized()
	if current.TaskGoal == "" {
		current.TaskGoal = next.TaskGoal
	}
	if current.Workspace == "" {
		current.Workspace = next.Workspace
	}
	if current.ReturnMode == "" {
		current.ReturnMode = next.ReturnMode
	}
	if len(current.ExpectedOutputs) == 0 {
		current.ExpectedOutputs = append([]string(nil), next.ExpectedOutputs...)
	}
	if len(current.InputArtifacts) == 0 {
		current.InputArtifacts = append([]string(nil), next.InputArtifacts...)
	}
	return current
}

func cloneRecord(record Record) Record {
	record.Contract = record.Contract.Normalized()
	record.Metadata = cloneMap(record.Metadata)
	return record
}

func cloneRecordPtr(record *Record) *Record {
	if record == nil {
		return nil
	}
	copy := cloneRecord(*record)
	return &copy
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func mergeMaps(base, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := cloneMap(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func copySubscribers(src map[chan runtimeevents.EventRecord]struct{}) []chan runtimeevents.EventRecord {
	if len(src) == 0 {
		return nil
	}
	out := make([]chan runtimeevents.EventRecord, 0, len(src))
	for ch := range src {
		out = append(out, ch)
	}
	return out
}

func normalizeKind(kind Kind) Kind {
	switch kind {
	case KindTool, KindProcess:
		return kind
	default:
		return KindAgent
	}
}

func parseKind(raw string) Kind {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(KindTool):
		return KindTool
	case string(KindProcess):
		return KindProcess
	default:
		return KindAgent
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}

func firstNonEmptyKind(values ...Kind) Kind {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return KindAgent
}

func isTerminal(status Status) bool {
	return status == StatusCompleted || status == StatusFailed || status == StatusCancelled
}

func isCancellation(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
