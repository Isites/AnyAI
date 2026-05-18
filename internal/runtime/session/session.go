package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/runtime/contract"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
)

// EntryType describes the kind of session entry.
type EntryType string

const (
	EntryTypeMessage        EntryType = "message"
	EntryTypeToolCall       EntryType = "tool_call"
	EntryTypeToolResult     EntryType = "tool_result"
	EntryTypeMeta           EntryType = "meta"
	EntryTypeCompaction     EntryType = "compaction"
	EntryTypeRuntimeControl EntryType = "runtime_control"
	EntryTypePlan           EntryType = "plan"
	EntryTypeTodo           EntryType = "todo"
)

// SessionEntry is a single node in the session DAG.
type SessionEntry struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId,omitempty"`
	Type      EntryType       `json:"type"`
	Role      string          `json:"role,omitempty"` // user, assistant, system
	RunID     string          `json:"run_id,omitempty"`
	TaskID    string          `json:"task_id,omitempty"`
	ToolID    string          `json:"tool_id,omitempty"`
	PlanID    string          `json:"plan_id,omitempty"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type EntryRefs struct {
	RunID  string
	TaskID string
	ToolID string
	PlanID string
}

// ImageData holds image metadata for session persistence. Data is only used by
// legacy or transient in-memory entries; durable replay stores compact refs.
type ImageData struct {
	ID       string `json:"id,omitempty"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data,omitempty"` // base64-encoded legacy payload
	Path     string `json:"path,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int    `json:"size,omitempty"`
}

// MessageData holds text message content.
type MessageData struct {
	Text   string      `json:"text"`
	Images []ImageData `json:"images,omitempty"`
}

// ToolCallData holds a tool call's details.
type ToolCallData struct {
	Tool  string          `json:"tool"`
	ID    string          `json:"id"`
	Input json.RawMessage `json:"input"`
}

// ToolResultData holds the result of a tool call.
type ToolResultData struct {
	ToolCallID string         `json:"tool_call_id"`
	Output     string         `json:"output"`
	Error      string         `json:"error,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Images     []ImageData    `json:"images,omitempty"`
}

// CompactionData stores a model-authored handoff summary used to replace older
// transcript history while preserving recent turns.
type CompactionData struct {
	Text            string `json:"text"`
	Trigger         string `json:"trigger,omitempty"`
	LegacyHeuristic bool   `json:"legacy_heuristic,omitempty"`
}

// RuntimeControlData stores runtime-authored control text for audit without
// treating it as a normal user turn in future model transcripts.
type RuntimeControlData struct {
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`
}

// PlanData stores a structured plan snapshot for the session.
type PlanData struct {
	ID         string            `json:"id,omitempty"`
	Plan       string            `json:"plan,omitempty"`
	Structured *runtimeplan.Plan `json:"structured,omitempty"`
}

// TodoData stores the latest state for a single todo item.
type TodoData struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	RunID       string `json:"run_id,omitempty"`
}

// Session holds a conversation session with DAG-structured entries.
type Session struct {
	ID      string
	AgentID string

	entries  []SessionEntry
	entryMap map[string]*SessionEntry
	leafID   string // current leaf for history traversal
	active   EntryRefs
	store    *Store
}

// NewSession creates a new empty session.
func NewSession(agentID, sessionID string) *Session {
	sessionID = strings.TrimSpace(sessionID)
	return &Session{
		ID:       sessionID,
		AgentID:  agentID,
		entryMap: make(map[string]*SessionEntry),
	}
}

// Append adds an entry to the session.
func (s *Session) Append(entry SessionEntry) {
	s.append(entry, nil, true)
}

// AppendTransient adds an entry to in-memory history without persisting it.
func (s *Session) AppendTransient(entry SessionEntry) {
	s.append(entry, nil, false)
}

// AppendWithPersistedEntry appends one in-memory entry while persisting a
// compact equivalent to disk/subscribers.
func (s *Session) AppendWithPersistedEntry(entry, persisted SessionEntry) {
	s.append(entry, &persisted, true)
}

func (s *Session) append(entry SessionEntry, persisted *SessionEntry, persist bool) {
	if entry.ID == "" {
		entry.ID = generateID("e")
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}
	if s.leafID != "" && entry.ParentID == "" {
		entry.ParentID = s.leafID
	}
	entry = withResolvedEntryRefs(entry, s.active)
	if persisted != nil {
		normalized := *persisted
		normalized.ID = entry.ID
		normalized.ParentID = entry.ParentID
		normalized.Timestamp = entry.Timestamp
		normalized.Role = entry.Role
		normalized.Type = entry.Type
		normalized = withResolvedEntryRefs(normalized, s.active)
		persisted = &normalized
	}

	s.entries = append(s.entries, entry)
	s.entryMap[entry.ID] = &s.entries[len(s.entries)-1]
	s.leafID = entry.ID

	// Persist if store is set
	if persist && s.store != nil {
		if persisted != nil {
			s.store.AppendEntry(s, *persisted)
			return
		}
		s.store.AppendEntry(s, entry)
	}
}

// History walks the DAG from root to current leaf and returns the path.
func (s *Session) History() []SessionEntry {
	if len(s.entries) == 0 {
		return nil
	}

	// Build path from leaf back to root
	var path []SessionEntry
	current := s.leafID
	for current != "" {
		entry, ok := s.entryMap[current]
		if !ok {
			break
		}
		path = append(path, *entry)
		current = entry.ParentID
	}

	// Reverse to get root→leaf order
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return path
}

// Entries returns all entries in append order.
func (s *Session) Entries() []SessionEntry {
	return s.entries
}

// LeafID returns the current leaf entry ID.
func (s *Session) LeafID() string {
	return s.leafID
}

// Branch moves the leaf pointer to the specified entry ID, creating a branch.
// New entries appended after this will have the branch point as their parent.
func (s *Session) Branch(entryID string) error {
	if _, ok := s.entryMap[entryID]; !ok {
		return fmt.Errorf("entry %q not found in session", entryID)
	}
	s.leafID = entryID
	return nil
}

// EstimateTokens returns a rough token estimate for the current history.
// Uses a simple heuristic of ~4 characters per token.
func (s *Session) EstimateTokens() int {
	history := s.History()
	totalChars := 0
	for _, entry := range history {
		totalChars += len(entry.Data)
		totalChars += len(entry.Role)
	}
	return totalChars / 4
}

// Compact replaces older history entries with a summary entry.
// It keeps the most recent keepEntries entries and replaces everything
// before them with a single summary meta entry.
// The summary text should be generated by the caller (typically by asking
// the LLM to summarize).
func (s *Session) Compact(summary string, keepEntries int) {
	history := s.History()
	if len(history) <= keepEntries {
		return // nothing to compact
	}
	s.ReplaceHistory(RewriteHistoryWithCompaction(
		history,
		summary,
		keepEntries,
		"legacy_entry_count",
		true,
	))
}

// ReplaceHistory rewrites the session as a single linear history rooted at the
// provided entries in order.
func (s *Session) ReplaceHistory(entries []SessionEntry) {
	s.entries = nil
	s.entryMap = make(map[string]*SessionEntry)
	s.leafID = ""
	prevID := ""
	for _, entry := range entries {
		if entry.ID == "" {
			entry.ID = generateID("e")
		}
		if entry.Timestamp == 0 {
			entry.Timestamp = time.Now().Unix()
		}
		entry.ParentID = prevID
		s.entries = append(s.entries, entry)
		s.entryMap[entry.ID] = &s.entries[len(s.entries)-1]
		s.leafID = entry.ID
		prevID = entry.ID
	}
	if s.store != nil {
		s.store.Rewrite(s)
	}
}

// SetStore associates a Store for automatic persistence.
func (s *Session) SetStore(store *Store) {
	s.store = store
}

func (s *Session) SetActiveRefs(refs EntryRefs) {
	if s == nil {
		return
	}
	s.active = normalizeEntryRefs(refs)
}

func (s *Session) ActiveRefs() EntryRefs {
	if s == nil {
		return EntryRefs{}
	}
	return s.active
}

func ApplyEntryRefs(entry SessionEntry, refs EntryRefs) SessionEntry {
	return withResolvedEntryRefs(entry, refs)
}

func NewEntryID() string {
	return generateID("e")
}

// Helper constructors for common entry types

// UserMessageEntry creates a user message entry.
func UserMessageEntry(text string) SessionEntry {
	data, _ := json.Marshal(MessageData{Text: text})
	return SessionEntry{
		Type: EntryTypeMessage,
		Role: "user",
		Data: data,
	}
}

func UserMessageEntryWithID(id, text string) SessionEntry {
	entry := UserMessageEntry(text)
	entry.ID = strings.TrimSpace(id)
	return entry
}

// UserMessageWithImagesEntry creates a user message entry with image attachments.
func UserMessageWithImagesEntry(text string, images []ImageData) SessionEntry {
	data, _ := json.Marshal(MessageData{Text: text, Images: images})
	return SessionEntry{
		Type: EntryTypeMessage,
		Role: "user",
		Data: data,
	}
}

func UserMessageWithImagesEntryWithID(id, text string, images []ImageData) SessionEntry {
	entry := UserMessageWithImagesEntry(text, images)
	entry.ID = strings.TrimSpace(id)
	return entry
}

// AssistantMessageEntry creates an assistant message entry.
func AssistantMessageEntry(text string) SessionEntry {
	data, _ := json.Marshal(MessageData{Text: text})
	return SessionEntry{
		Type: EntryTypeMessage,
		Role: "assistant",
		Data: data,
	}
}

// ToolCallEntry creates a tool call entry.
func ToolCallEntry(toolCallID, toolName string, input json.RawMessage) SessionEntry {
	data, _ := json.Marshal(ToolCallData{
		Tool:  toolName,
		ID:    toolCallID,
		Input: input,
	})
	return SessionEntry{
		Type: EntryTypeToolCall,
		Data: data,
	}
}

// ToolResultEntry creates a tool result entry.
func ToolResultEntry(toolCallID, output, errMsg string, images []ImageData) SessionEntry {
	return ToolResultEntryWithMetadata(toolCallID, output, errMsg, nil, images)
}

// ToolResultEntryWithMetadata creates a tool result entry with structured metadata.
func ToolResultEntryWithMetadata(toolCallID, output, errMsg string, metadata map[string]any, images []ImageData) SessionEntry {
	data, _ := json.Marshal(ToolResultData{
		ToolCallID: toolCallID,
		Output:     output,
		Error:      errMsg,
		IsError:    errMsg != "",
		Metadata:   metadata,
		Images:     images,
	})
	return SessionEntry{
		Type: EntryTypeToolResult,
		Data: data,
	}
}

// PlanEntry creates a plan state entry.
func PlanEntry(plan string) SessionEntry {
	data, _ := json.Marshal(PlanData{
		ID:   contract.NewOpaqueID("plan"),
		Plan: strings.TrimSpace(plan),
	})
	return SessionEntry{
		Type: EntryTypePlan,
		Role: "assistant",
		Data: data,
	}
}

// StructuredPlanEntry creates a structured plan state entry.
// Human-readable text is rendered on demand from the structured payload.
func StructuredPlanEntry(plan runtimeplan.Plan) SessionEntry {
	normalized := runtimeplan.Normalize(plan)
	data, _ := json.Marshal(PlanData{
		ID:         normalized.ID,
		Structured: &normalized,
	})
	return SessionEntry{
		Type: EntryTypePlan,
		Role: "assistant",
		Data: data,
	}
}

// MetaEntry creates a session summary/meta entry.
func MetaEntry(text string) SessionEntry {
	data, _ := json.Marshal(MessageData{Text: text})
	return SessionEntry{
		Type: EntryTypeMeta,
		Role: "system",
		Data: data,
	}
}

// CompactionEntry creates a model-authored session compaction entry.
func CompactionEntry(data CompactionData) SessionEntry {
	payload, _ := json.Marshal(data)
	return SessionEntry{
		Type: EntryTypeCompaction,
		Role: "user",
		Data: payload,
	}
}

// RuntimeControlEntry creates a runtime-authored control entry.
func RuntimeControlEntry(kind, text string) SessionEntry {
	payload, _ := json.Marshal(RuntimeControlData{
		Kind: strings.TrimSpace(kind),
		Text: strings.TrimSpace(text),
	})
	return SessionEntry{
		Type: EntryTypeRuntimeControl,
		Role: "system",
		Data: payload,
	}
}

// RuntimeControlText extracts runtime-authored control text from either the
// structured runtime_control entry or legacy user messages that carried the
// same control prompt markers.
func RuntimeControlText(entry SessionEntry) (string, bool) {
	switch entry.Type {
	case EntryTypeRuntimeControl:
		var data RuntimeControlData
		if err := json.Unmarshal(entry.Data, &data); err != nil {
			return "", false
		}
		text := strings.TrimSpace(data.Text)
		return text, text != ""
	case EntryTypeMessage:
		if strings.TrimSpace(entry.Role) != "user" {
			return "", false
		}
		var data MessageData
		if err := json.Unmarshal(entry.Data, &data); err != nil {
			return "", false
		}
		text := strings.TrimSpace(data.Text)
		if !IsRuntimeControlText(text) {
			return "", false
		}
		return text, true
	default:
		return "", false
	}
}

// IsRuntimeControlEntry reports whether an entry carries structured or legacy
// runtime control text.
func IsRuntimeControlEntry(entry SessionEntry) bool {
	_, ok := RuntimeControlText(entry)
	return ok
}

func HistoryEndsWithRuntimeControl(history []SessionEntry) bool {
	if len(history) == 0 {
		return false
	}
	return IsRuntimeControlEntry(history[len(history)-1])
}

// IsRuntimeControlText recognizes legacy control prompts that were stored as
// normal user messages before runtime_control entries existed.
func IsRuntimeControlText(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "[Runtime goal continuation]") ||
		strings.HasPrefix(text, "[Runtime hard stop]")
}

func ModelVisibleEntries(history []SessionEntry) []SessionEntry {
	if len(history) == 0 {
		return nil
	}
	visible := make([]SessionEntry, 0, len(history))
	for _, entry := range history {
		if IsRuntimeControlEntry(entry) {
			continue
		}
		visible = append(visible, entry)
	}
	return visible
}

func VisibleUserMessages(history []SessionEntry) []MessageData {
	if len(history) == 0 {
		return nil
	}
	messages := make([]MessageData, 0)
	for _, entry := range history {
		if entry.Type != EntryTypeMessage || strings.TrimSpace(entry.Role) != "user" || IsRuntimeControlEntry(entry) {
			continue
		}
		var msg MessageData
		if err := json.Unmarshal(entry.Data, &msg); err != nil {
			continue
		}
		messages = append(messages, msg)
	}
	return messages
}

// TodoEntry creates a todo state entry.
func TodoEntry(item TodoData) SessionEntry {
	data, _ := json.Marshal(item)
	return SessionEntry{
		Type: EntryTypeTodo,
		Role: "assistant",
		Data: data,
	}
}

// BuildRollingSummary creates a compact textual summary from older session
// history so long-running sessions can keep continuity without replaying the
// full transcript every turn.
func BuildRollingSummary(history []SessionEntry) string {
	if len(history) == 0 {
		return ""
	}

	var previousSummary string
	var userPrompts []string
	var assistantNotes []string
	toolNames := make([]string, 0, 4)
	seenTools := map[string]struct{}{}
	var latestPlan string
	todos := map[string]TodoData{}
	todoOrder := make([]string, 0, 4)

	for _, entry := range history {
		switch entry.Type {
		case EntryTypeMeta:
			var msg MessageData
			if err := json.Unmarshal(entry.Data, &msg); err == nil {
				text := strings.TrimSpace(msg.Text)
				if text != "" {
					previousSummary = text
				}
			}
		case EntryTypeCompaction:
			var compact CompactionData
			if err := json.Unmarshal(entry.Data, &compact); err == nil {
				text := strings.TrimSpace(compact.Text)
				if text != "" {
					previousSummary = text
				}
			}
		case EntryTypeMessage:
			if IsRuntimeControlEntry(entry) {
				continue
			}
			var msg MessageData
			if err := json.Unmarshal(entry.Data, &msg); err != nil {
				continue
			}
			text := compactSummaryLine(msg.Text)
			if text == "" {
				continue
			}
			switch entry.Role {
			case "user":
				userPrompts = append(userPrompts, text)
			case "assistant":
				assistantNotes = append(assistantNotes, text)
			}
		case EntryTypeToolCall:
			var call ToolCallData
			if err := json.Unmarshal(entry.Data, &call); err != nil {
				continue
			}
			name := strings.TrimSpace(call.Tool)
			if name == "" {
				continue
			}
			if _, ok := seenTools[name]; ok {
				continue
			}
			seenTools[name] = struct{}{}
			toolNames = append(toolNames, name)
		case EntryTypePlan:
			var plan PlanData
			if err := json.Unmarshal(entry.Data, &plan); err == nil {
				latestPlan = compactSummaryLine(renderPlanData(plan))
			}
		case EntryTypeTodo:
			var todo TodoData
			if err := json.Unmarshal(entry.Data, &todo); err != nil {
				continue
			}
			if todo.ID == "" {
				continue
			}
			if _, ok := todos[todo.ID]; !ok {
				todoOrder = append(todoOrder, todo.ID)
			}
			todos[todo.ID] = todo
		}
	}

	var sections []string
	if previousSummary != "" {
		sections = append(sections, "## Previous Summary\n- "+previousSummary)
	}
	if items := tailSummaryItems(userPrompts, 3); len(items) > 0 {
		sections = append(sections, "## Earlier User Intent\n- "+strings.Join(items, "\n- "))
	}
	if items := tailSummaryItems(assistantNotes, 3); len(items) > 0 {
		sections = append(sections, "## Earlier Assistant Notes\n- "+strings.Join(items, "\n- "))
	}
	if len(toolNames) > 0 {
		sections = append(sections, "## Tools Used\n- "+strings.Join(toolNames, ", "))
	}
	if latestPlan != "" {
		sections = append(sections, "## Latest Plan\n- "+latestPlan)
	}
	if items := openTodoItems(todos, todoOrder, 4); len(items) > 0 {
		sections = append(sections, "## Open Todos\n- "+strings.Join(items, "\n- "))
	}

	if len(sections) == 0 {
		return ""
	}

	return "Rolling summary of earlier session context.\n\n" + strings.Join(sections, "\n\n")
}

func generateID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

func withResolvedEntryRefs(entry SessionEntry, active EntryRefs) SessionEntry {
	active = normalizeEntryRefs(active)
	if entry.RunID == "" {
		entry.RunID = active.RunID
	}
	if entry.TaskID == "" {
		entry.TaskID = active.TaskID
	}
	if entry.ToolID == "" {
		entry.ToolID = toolIDFromEntry(entry)
	}
	if entry.ToolID == "" {
		entry.ToolID = active.ToolID
	}
	if entry.PlanID == "" {
		entry.PlanID = planIDFromEntry(entry)
	}
	if entry.PlanID == "" {
		entry.PlanID = active.PlanID
	}
	return entry
}

func normalizeEntryRefs(refs EntryRefs) EntryRefs {
	refs.RunID = strings.TrimSpace(refs.RunID)
	refs.TaskID = strings.TrimSpace(refs.TaskID)
	refs.ToolID = strings.TrimSpace(refs.ToolID)
	refs.PlanID = strings.TrimSpace(refs.PlanID)
	return refs
}

func toolIDFromEntry(entry SessionEntry) string {
	switch entry.Type {
	case EntryTypeToolCall:
		var data ToolCallData
		if err := json.Unmarshal(entry.Data, &data); err == nil {
			return strings.TrimSpace(data.ID)
		}
	case EntryTypeToolResult:
		var data ToolResultData
		if err := json.Unmarshal(entry.Data, &data); err == nil {
			return strings.TrimSpace(data.ToolCallID)
		}
	}
	return ""
}

func planIDFromEntry(entry SessionEntry) string {
	if entry.Type != EntryTypePlan {
		return ""
	}
	var data PlanData
	if err := json.Unmarshal(entry.Data, &data); err != nil {
		return ""
	}
	if data.Structured != nil {
		return strings.TrimSpace(data.Structured.ID)
	}
	return strings.TrimSpace(data.ID)
}

func latestStateEntries(history []SessionEntry, recentIDs map[string]struct{}) []SessionEntry {
	var lastPlan *SessionEntry
	todos := map[string]SessionEntry{}
	order := make([]string, 0, 4)

	for _, entry := range history {
		switch entry.Type {
		case EntryTypePlan:
			copy := entry
			lastPlan = &copy
		case EntryTypeTodo:
			var todo TodoData
			if err := json.Unmarshal(entry.Data, &todo); err != nil || todo.ID == "" {
				continue
			}
			if _, ok := todos[todo.ID]; !ok {
				order = append(order, todo.ID)
			}
			todos[todo.ID] = entry
		}
	}

	var entries []SessionEntry
	if lastPlan != nil {
		if _, ok := recentIDs[lastPlan.ID]; !ok {
			entries = append(entries, *lastPlan)
		}
	}
	for _, id := range order {
		entry := todos[id]
		if _, ok := recentIDs[entry.ID]; ok {
			continue
		}
		entries = append(entries, entry)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Timestamp == entries[j].Timestamp {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].Timestamp < entries[j].Timestamp
	})

	return entries
}

// LatestStateEntries returns the latest plan/todo state entries that are not
// already preserved in the recent history suffix selected for compaction.
func LatestStateEntries(history []SessionEntry, recentIDs map[string]struct{}) []SessionEntry {
	return latestStateEntries(history, recentIDs)
}

func tailSummaryItems(items []string, limit int) []string {
	items = compactSummaryItems(items)
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func compactSummaryItems(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		line := compactSummaryLine(item)
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out
}

func compactSummaryLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= 180 {
		return text
	}
	return strings.TrimSpace(string(runes[:180])) + "..."
}

func openTodoItems(todos map[string]TodoData, order []string, limit int) []string {
	if len(todos) == 0 {
		return nil
	}
	items := make([]string, 0, len(order))
	for _, id := range order {
		todo, ok := todos[id]
		if !ok || strings.EqualFold(todo.Status, "completed") {
			continue
		}
		line := compactSummaryLine(todo.Content)
		if line == "" {
			continue
		}
		items = append(items, line)
	}
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
}
