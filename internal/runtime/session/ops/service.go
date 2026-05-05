package ops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	runtimesessionevents "github.com/Isites/anyai/internal/runtime/sessionevents"
)

const (
	defaultCompactKeepEntries = 24
	indexFileName             = "index.json"
)

type Index struct {
	GeneratedAt time.Time                        `json:"generated_at"`
	Agents      map[string][]session.SessionInfo `json:"agents"`
}

type Service struct {
	configFn   func() *config.Config
	storeFn    func() *session.Store
	recorderFn func() *runtimeevents.Recorder
	bindMu     sync.Mutex
	boundStore *session.Store
}

func NewService(
	configFn func() *config.Config,
	storeFn func() *session.Store,
	recorderFn func() *runtimeevents.Recorder,
) *Service {
	service := &Service{
		configFn:   configFn,
		storeFn:    storeFn,
		recorderFn: recorderFn,
	}
	service.AttachStoreRecorder()
	return service
}

func (s *Service) Store() *session.Store {
	if s == nil || s.storeFn == nil {
		return nil
	}
	return s.storeFn()
}

func (s *Service) Recorder() *runtimeevents.Recorder {
	if s == nil || s.recorderFn == nil {
		return nil
	}
	return s.recorderFn()
}

func (s *Service) AttachStoreRecorder() {
	if s == nil {
		return
	}
	store := s.Store()
	if store == nil {
		return
	}
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	if s.boundStore == store {
		return
	}
	store.SetChangeAppender(s.recordSessionChange)
	s.boundStore = store
}

func (s *Service) recordSessionChange(change session.Change) {
	if s == nil || change.Kind != session.ChangeAppend {
		return
	}
	if change.Entry.Type != session.EntryTypeMessage || strings.TrimSpace(change.Entry.Role) != "assistant" {
		return
	}
	if strings.TrimSpace(change.Entry.RunID) == "" {
		return
	}
	recorder := s.Recorder()
	if recorder == nil {
		return
	}
	toolCalls := make(map[string]runtimesessionevents.ToolCallState)
	for _, event := range runtimesessionevents.EntryEventRecords(change.AgentID, change.SessionID, change.Entry, toolCalls) {
		event.Timestamp = time.Now().UTC()
		runtimeevents.AppendEventWithReplayPolicy(recorder, event)
	}
}

func (s *Service) Config() *config.Config {
	if s == nil || s.configFn == nil {
		return nil
	}
	return s.configFn()
}

func (s *Service) List(agentID string) ([]session.SessionInfo, error) {
	store := s.Store()
	if store == nil {
		return nil, fmt.Errorf("session store not available")
	}
	sessions, err := store.List(agentID)
	if err != nil {
		return nil, err
	}
	_ = s.RebuildIndex()
	return sessions, nil
}

func (s *Service) Create(agentID, key string) error {
	store := s.Store()
	if store == nil {
		return fmt.Errorf("session store not available")
	}
	if err := store.Create(agentID, key); err != nil {
		return err
	}
	s.recordLifecycleEvent(agentID, key, "session.created", map[string]any{
		"agent_id":   agentID,
		"session_id": key,
	})
	return s.RebuildIndex()
}

func (s *Service) Load(agentID, key string) (*session.Session, error) {
	store := s.Store()
	if store == nil {
		return nil, fmt.Errorf("session store not available")
	}
	sess, err := store.Load(agentID, key)
	if err != nil {
		return nil, err
	}
	_ = s.RebuildIndex()
	return sess, nil
}

func (s *Service) EventRecords(agentID, sessionID string) []runtimeevents.EventRecord {
	store := s.Store()
	if store == nil {
		return nil
	}
	sess, err := store.Load(agentID, sessionID)
	if err != nil {
		return nil
	}
	return runtimesessionevents.HistoryEventRecords(agentID, sessionID, sess.History())
}

func (s *Service) SubscribeEvents(agentID, sessionID string) (<-chan runtimeevents.EventRecord, func(), error) {
	store := s.Store()
	if store == nil {
		return nil, nil, fmt.Errorf("session store not available")
	}
	sess, err := store.Load(agentID, sessionID)
	if err != nil {
		return nil, nil, err
	}

	toolCalls := make(map[string]runtimesessionevents.ToolCallState)
	for _, entry := range sess.History() {
		_ = runtimesessionevents.EntryEventRecords(agentID, sessionID, entry, toolCalls)
	}

	changes, cancelStore := store.Subscribe(agentID, sessionID)
	out := make(chan runtimeevents.EventRecord, 64)
	go func() {
		defer close(out)
		for change := range changes {
			switch change.Kind {
			case session.ChangeCreate:
				out <- runtimeevents.EventRecord{
					AgentID:   agentID,
					SessionID: sessionID,
					Name:      "session.created",
					Timestamp: time.Now().UTC(),
					Payload: map[string]any{
						"agent_id":   agentID,
						"session_id": sessionID,
					},
				}
			case session.ChangeDelete:
				out <- runtimeevents.EventRecord{
					AgentID:   agentID,
					SessionID: sessionID,
					Name:      "session.deleted",
					Timestamp: time.Now().UTC(),
					Payload: map[string]any{
						"agent_id":   agentID,
						"session_id": sessionID,
					},
				}
			case session.ChangeRewrite:
				clear(toolCalls)
				for _, entry := range change.Snapshot {
					_ = runtimesessionevents.EntryEventRecords(agentID, sessionID, entry, toolCalls)
				}
				out <- runtimeevents.EventRecord{
					AgentID:   agentID,
					SessionID: sessionID,
					Name:      "session.rewritten",
					Timestamp: time.Now().UTC(),
					Payload: map[string]any{
						"agent_id":    agentID,
						"session_id":  sessionID,
						"entry_count": len(change.Snapshot),
					},
				}
			case session.ChangeAppend:
				for _, event := range runtimesessionevents.EntryEventRecords(agentID, sessionID, change.Entry, toolCalls) {
					out <- event
				}
			}
		}
	}()
	return out, cancelStore, nil
}

func (s *Service) Delete(agentID, key string) error {
	store := s.Store()
	if store == nil {
		return fmt.Errorf("session store not available")
	}
	if err := store.Delete(agentID, key); err != nil {
		return err
	}
	s.recordLifecycleEvent(agentID, key, "session.deleted", map[string]any{
		"agent_id":   agentID,
		"session_id": key,
	})
	return s.RebuildIndex()
}

func (s *Service) Compact(agentID, key string, keepEntries int) (*session.Session, error) {
	sess, err := s.Load(agentID, key)
	if err != nil {
		return nil, err
	}
	history := sess.History()
	if len(history) == 0 {
		return sess, nil
	}
	if keepEntries <= 0 {
		keepEntries = defaultCompactKeepEntries
	}
	if len(history) <= keepEntries {
		return sess, nil
	}

	cutoff := len(history) - keepEntries
	summary := session.BuildRollingSummary(history[:cutoff])
	if strings.TrimSpace(summary) == "" {
		return sess, nil
	}

	recorder := s.Recorder()
	run, _ := runtimeevents.StartSyntheticRun(recorder, runtimeevents.SyntheticRunSpec{
		AgentID:   agentID,
		SessionID: key,
		Model:     "system/session",
		Channel:   "control",
	})
	runtimeevents.AppendRunEvent(recorder, run, runtimeevents.EventSessionCompactRequested, map[string]any{
		"keep_entries": keepEntries,
		"trigger":      "legacy_entry_count",
		"history_len":  len(history),
	})

	sess.Compact(summary, keepEntries)

	runtimeevents.AppendRunEvent(recorder, run, runtimeevents.EventSessionCompactCompleted, map[string]any{
		"keep_entries":     keepEntries,
		"trigger":          "legacy_entry_count",
		"history_len":      len(sess.History()),
		"summary":          summary,
		"legacy_heuristic": true,
	})
	runtimeevents.FinishSyntheticRun(recorder, run, summary, "")

	if err := s.RebuildIndex(); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Service) RebuildFromEvents() error {
	store := s.Store()
	recorder := s.Recorder()
	if store == nil || recorder == nil {
		return nil
	}

	type sessionRef struct {
		agentID   string
		sessionID string
	}
	type replaySegment struct {
		run    runtimeevents.RunRecord
		events []runtimeevents.EventRecord
	}

	splitSegments := func(run runtimeevents.RunRecord, events []runtimeevents.EventRecord) map[sessionRef]replaySegment {
		segments := make(map[sessionRef]replaySegment)
		rootAgentID := strings.TrimSpace(run.AgentID)
		rootSessionID := strings.TrimSpace(run.SessionID)
		rootRef := sessionRef{agentID: rootAgentID, sessionID: rootSessionID}
		if rootAgentID != "" && rootSessionID != "" {
			segments[rootRef] = replaySegment{run: run}
		}
		for _, event := range events {
			agentID := strings.TrimSpace(event.AgentID)
			sessionID := strings.TrimSpace(event.SessionID)
			if agentID == "" || sessionID == "" {
				continue
			}
			ref := sessionRef{agentID: agentID, sessionID: sessionID}
			segment := segments[ref]
			if strings.TrimSpace(segment.run.ID) == "" {
				segment.run = run
				segment.run.AgentID = agentID
				segment.run.SessionID = sessionID
				segment.run.Input = ""
				segment.run.Output = ""
				segment.run.Error = ""
			}
			segment.events = append(segment.events, event)
			segments[ref] = segment
		}
		return segments
	}

	chooseSegmentTime := func(segment replaySegment) time.Time {
		if ts := chooseRunTime(segment.run); !ts.IsZero() {
			return ts
		}
		if len(segment.events) > 0 {
			return segment.events[0].Timestamp.UTC()
		}
		return time.Time{}
	}

	hasReplayableSegment := func(segment replaySegment) bool {
		if isControlReplayRun(segment.run) {
			return false
		}
		if strings.TrimSpace(segment.run.Input) != "" || strings.TrimSpace(segment.run.Output) != "" {
			return true
		}
		for _, event := range segment.events {
			switch strings.TrimSpace(event.Name) {
			case runtimeevents.EventSessionInputStored,
				runtimeevents.EventSessionOutputStored,
				runtimeevents.EventToolCallStarted,
				runtimeevents.EventToolCompleted,
				runtimeevents.EventToolFailed,
				runtimeevents.EventRunFallbackReply,
				runtimeevents.EventTextDelta,
				"session.created",
				"session.deleted",
				"session.plan.updated",
				"session.todo.updated",
				"session.compact.completed":
				return true
			}
		}
		return false
	}

	hasReplayableSegments := func(segments []replaySegment) bool {
		for _, segment := range segments {
			if hasReplayableSegment(segment) {
				return true
			}
		}
		return false
	}

	grouped := make(map[sessionRef][]replaySegment)
	for _, run := range recorder.ListRuns() {
		for key, segment := range splitSegments(run, recorder.ListRunEvents(run.ID)) {
			if key.agentID == "" || key.sessionID == "" {
				continue
			}
			grouped[key] = append(grouped[key], segment)
		}
	}

	for key, segments := range grouped {
		sort.SliceStable(segments, func(i, j int) bool {
			left := chooseSegmentTime(segments[i])
			right := chooseSegmentTime(segments[j])
			if left.Equal(right) {
				return segments[i].run.ID < segments[j].run.ID
			}
			return left.Before(right)
		})

		replay := newSessionReplay(key.agentID, key.sessionID)
		for _, segment := range segments {
			replayRunIntoSession(replay, segment.run, segment.events)
		}
		if replay.deleted {
			if err := store.Delete(key.agentID, key.sessionID); err != nil {
				return err
			}
			continue
		}
		if replay.session == nil {
			if !hasReplayableSegments(segments) {
				continue
			}
			replay.reset()
		}
		replay.session.SetStore(store)
		store.Rewrite(replay.session)
	}

	return nil
}

func (s *Service) RebuildIndex() error {
	store := s.Store()
	if store == nil {
		return nil
	}
	index := Index{
		GeneratedAt: time.Now().UTC(),
		Agents:      make(map[string][]session.SessionInfo),
	}
	for _, agentID := range s.agentIDs(store) {
		items, err := store.List(agentID)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].LastActivity.Equal(items[j].LastActivity) {
				return items[i].ID < items[j].ID
			}
			return items[i].LastActivity.After(items[j].LastActivity)
		})
		index.Agents[agentID] = items
	}

	path := s.indexPath(store)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s *Service) ReadIndex() (Index, error) {
	store := s.Store()
	if store == nil {
		return Index{}, fmt.Errorf("session store not available")
	}
	path := s.indexPath(store)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Index{}, nil
		}
		return Index{}, err
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return Index{}, err
	}
	return index, nil
}

func replayRunIntoSession(state *sessionReplay, run runtimeevents.RunRecord, events []runtimeevents.EventRecord) {
	if state == nil {
		return
	}
	if entry, ok := sessionInputEntryFromEvents(events); ok {
		state.ensureSession().Append(entry)
	} else if input := strings.TrimSpace(run.Input); input != "" {
		state.ensureSession().Append(session.ApplyEntryRefs(session.UserMessageEntry(input), session.EntryRefs{
			RunID: run.ID,
		}))
	}

	var assistantText strings.Builder
	var fallbackReply string
	for _, event := range events {
		switch strings.TrimSpace(event.Name) {
		case runtimeevents.EventSessionInputStored:
			continue
		case runtimeevents.EventSessionOutputStored:
			text := strings.TrimSpace(stringPayload(event.Payload, "text"))
			if text != "" {
				assistantText.Reset()
				assistantText.WriteString(text)
			}
		case "session.created":
			state.reset()
		case "session.deleted":
			state.delete()
		case "session.plan.updated":
			if structured, ok := structuredPlanFromPayload(event.Payload); ok {
				state.ensureSession().Append(session.ApplyEntryRefs(session.StructuredPlanEntry(structured), entryRefsFromEvent(event)))
				continue
			}
			plan := strings.TrimSpace(stringPayload(event.Payload, "plan"))
			if plan == "" {
				continue
			}
			state.ensureSession().Append(session.ApplyEntryRefs(session.PlanEntry(plan), entryRefsFromEvent(event)))
		case "session.todo.updated":
			item, ok := todoDataFromPayload(event.Payload)
			if !ok {
				continue
			}
			state.ensureSession().Append(session.ApplyEntryRefs(session.TodoEntry(item), entryRefsFromEvent(event)))
		case runtimeevents.EventTextDelta:
			if text := strings.TrimSpace(stringPayload(event.Payload, "text")); text != "" {
				assistantText.WriteString(text)
			}
		case runtimeevents.EventRunFallbackReply:
			fallbackReply = strings.TrimSpace(stringPayload(event.Payload, "text"))
		case runtimeevents.EventToolCallStarted:
			toolCallID := strings.TrimSpace(stringPayload(event.Payload, "id"))
			toolName := strings.TrimSpace(stringPayload(event.Payload, "tool"))
			if toolCallID == "" || toolName == "" {
				continue
			}
			state.ensureSession().Append(session.ApplyEntryRefs(
				session.ToolCallEntry(toolCallID, toolName, rawPayload(event.Payload["input"])),
				entryRefsFromEvent(event),
			))
		case runtimeevents.EventToolCompleted, runtimeevents.EventToolFailed:
			toolCallID := strings.TrimSpace(stringPayload(event.Payload, "id"))
			if toolCallID == "" {
				continue
			}
			state.ensureSession().Append(session.ApplyEntryRefs(
				session.ToolResultEntryWithMetadata(
					toolCallID,
					strings.TrimSpace(stringPayload(event.Payload, "output")),
					strings.TrimSpace(stringPayload(event.Payload, "error")),
					mapPayload(event.Payload["metadata"]),
					imagePayload(event.Payload["images"]),
				),
				entryRefsFromEvent(event),
			))
		case "session.compact.completed":
			summary := strings.TrimSpace(stringPayload(event.Payload, "summary"))
			if summary == "" {
				continue
			}
			trigger := strings.TrimSpace(stringPayload(event.Payload, "trigger"))
			if trigger == "" {
				trigger = "legacy_entry_count"
			}
			history := state.ensureSession().History()
			state.ensureSession().ReplaceHistory(session.RewriteHistoryWithCompaction(
				history,
				summary,
				intPayload(event.Payload, "keep_entries"),
				trigger,
				boolPayload(event.Payload, "legacy_heuristic"),
			))
		}
	}

	text := strings.TrimSpace(assistantText.String())
	if text == "" && !isControlReplayRun(run) {
		text = strings.TrimSpace(firstNonEmpty(fallbackReply, run.Output))
	}
	if text != "" {
		state.ensureSession().Append(session.ApplyEntryRefs(session.AssistantMessageEntry(text), session.EntryRefs{
			RunID: run.ID,
		}))
	}
}

type sessionReplay struct {
	agentID   string
	sessionID string
	session   *session.Session
	deleted   bool
}

func newSessionReplay(agentID, sessionID string) *sessionReplay {
	return &sessionReplay{
		agentID:   agentID,
		sessionID: sessionID,
	}
}

func (r *sessionReplay) ensureSession() *session.Session {
	if r == nil {
		return nil
	}
	if r.session == nil {
		r.session = session.NewSession(r.agentID, r.sessionID)
	}
	r.deleted = false
	return r.session
}

func (r *sessionReplay) reset() {
	if r == nil {
		return
	}
	r.session = session.NewSession(r.agentID, r.sessionID)
	r.deleted = false
}

func (r *sessionReplay) delete() {
	if r == nil {
		return
	}
	r.session = nil
	r.deleted = true
}

func chooseRunTime(run runtimeevents.RunRecord) time.Time {
	switch {
	case !run.StartedAt.IsZero():
		return run.StartedAt.UTC()
	case !run.CreatedAt.IsZero():
		return run.CreatedAt.UTC()
	case !run.CompletedAt.IsZero():
		return run.CompletedAt.UTC()
	default:
		return time.Time{}
	}
}

func stringPayload(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}

func firstNonEmptyStringPayload(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringPayload(payload, key)); value != "" {
			return value
		}
	}
	return ""
}

func boolPayload(payload map[string]any, key string) bool {
	if len(payload) == 0 {
		return false
	}
	value, ok := payload[key]
	if !ok {
		return false
	}
	flag, _ := value.(bool)
	return flag
}

func intPayload(payload map[string]any, key string) int {
	if len(payload) == 0 {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func rawPayload(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func mapPayload(value any) map[string]any {
	result, _ := value.(map[string]any)
	if len(result) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(result))
	for key, item := range result {
		cloned[key] = item
	}
	return cloned
}

func structuredPlanFromPayload(payload map[string]any) (runtimeplan.Plan, bool) {
	raw := rawPayload(payload["structured"])
	if len(raw) == 0 || string(raw) == "null" {
		return runtimeplan.Plan{}, false
	}
	var current runtimeplan.Plan
	if err := json.Unmarshal(raw, &current); err != nil {
		return runtimeplan.Plan{}, false
	}
	return runtimeplan.Normalize(current), true
}

func imagePayload(value any) []session.ImageData {
	appendImage := func(images []session.ImageData, payload map[string]any) []session.ImageData {
		image := session.ImageData{
			MimeType: firstNonEmptyStringPayload(payload, "mime_type", "mimeType"),
			Data:     strings.TrimSpace(stringPayload(payload, "data")),
		}
		if image.MimeType == "" && image.Data == "" {
			return images
		}
		return append(images, image)
	}

	switch items := value.(type) {
	case []any:
		images := make([]session.ImageData, 0, len(items))
		for _, item := range items {
			payload, ok := item.(map[string]any)
			if !ok {
				continue
			}
			images = appendImage(images, payload)
		}
		if len(images) == 0 {
			return nil
		}
		return images
	case []map[string]any:
		images := make([]session.ImageData, 0, len(items))
		for _, payload := range items {
			images = appendImage(images, payload)
		}
		if len(images) == 0 {
			return nil
		}
		return images
	default:
		return nil
	}
}

func sessionInputEntryFromEvents(events []runtimeevents.EventRecord) (session.SessionEntry, bool) {
	for _, event := range events {
		if strings.TrimSpace(event.Name) != runtimeevents.EventSessionInputStored {
			continue
		}
		text := strings.TrimSpace(stringPayload(event.Payload, "text"))
		images := imagePayload(event.Payload["images"])
		if len(images) > 0 {
			return session.ApplyEntryRefs(session.UserMessageWithImagesEntry(text, images), entryRefsFromEvent(event)), true
		}
		if text != "" {
			return session.ApplyEntryRefs(session.UserMessageEntry(text), entryRefsFromEvent(event)), true
		}
	}
	return session.SessionEntry{}, false
}

func entryRefsFromEvent(event runtimeevents.EventRecord) session.EntryRefs {
	refs := session.EntryRefs{
		RunID:  strings.TrimSpace(event.RunID),
		TaskID: strings.TrimSpace(firstNonEmptyStringPayload(event.Payload, "task_id")),
	}
	switch strings.TrimSpace(event.Name) {
	case "session.plan.updated":
		refs.PlanID = strings.TrimSpace(firstNonEmptyStringPayload(event.Payload, "plan_id", "id"))
	case runtimeevents.EventToolCallStarted, runtimeevents.EventToolCompleted, runtimeevents.EventToolFailed:
		refs.ToolID = strings.TrimSpace(firstNonEmptyStringPayload(event.Payload, "tool_id", "tool_call_id", "id"))
	}
	return refs
}

func todoDataFromPayload(payload map[string]any) (session.TodoData, bool) {
	item := session.TodoData{
		ID:          strings.TrimSpace(stringPayload(payload, "id")),
		Content:     strings.TrimSpace(stringPayload(payload, "content")),
		Status:      strings.TrimSpace(stringPayload(payload, "status")),
		CreatedAt:   int64(intPayload(payload, "created_at")),
		CompletedAt: int64(intPayload(payload, "completed_at")),
		RunID:       strings.TrimSpace(firstNonEmptyStringPayload(payload, "run_id")),
	}
	if item.ID == "" {
		return session.TodoData{}, false
	}
	return item, true
}

func isControlReplayRun(run runtimeevents.RunRecord) bool {
	switch strings.TrimSpace(run.Channel) {
	case "control", "maintenance":
		return true
	default:
		return false
	}
}

func hasReplayableSessionRun(runs []runtimeevents.RunRecord) bool {
	for _, run := range runs {
		if isControlReplayRun(run) {
			continue
		}
		return true
	}
	return false
}

func (s *Service) agentIDs(store *session.Store) []string {
	seen := map[string]struct{}{}
	if cfg := s.Config(); cfg != nil {
		for _, agentCfg := range cfg.Agents.List {
			id := strings.TrimSpace(agentCfg.ID)
			if id == "" {
				continue
			}
			seen[id] = struct{}{}
		}
	}
	if baseDir := strings.TrimSpace(store.BaseDir()); baseDir != "" {
		if entries, err := os.ReadDir(baseDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					seen[entry.Name()] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *Service) indexPath(store *session.Store) string {
	if store == nil {
		return ""
	}
	baseDir := strings.TrimSpace(store.BaseDir())
	if baseDir == "" {
		return ""
	}
	return filepath.Join(baseDir, indexFileName)
}

func (s *Service) recordLifecycleEvent(agentID, sessionID, name string, payload map[string]any) {
	runtimeevents.AppendSyntheticRunEvent(s.Recorder(), runtimeevents.SyntheticRunSpec{
		AgentID:   agentID,
		SessionID: sessionID,
		Model:     "system/session",
		Channel:   "control",
	}, name, payload, name, "")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
