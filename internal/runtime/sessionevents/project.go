package sessionevents

import (
	"encoding/json"
	"strings"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

type ToolCallState struct {
	name  string
	input json.RawMessage
}

// HistoryEventRecords projects persisted session history into transport-facing
// event records so session APIs can stream directly from session files.
func HistoryEventRecords(agentID, sessionID string, history []session.SessionEntry) []runtimeevents.EventRecord {
	toolCalls := make(map[string]ToolCallState)
	events := make([]runtimeevents.EventRecord, 0, len(history))
	for _, entry := range history {
		events = append(events, EntryEventRecords(agentID, sessionID, entry, toolCalls)...)
	}
	return events
}

// EntryEventRecords converts one persisted session entry into one or more
// event records. The provided toolCalls map preserves call metadata so tool
// result entries can update the right tool line and reconstruct child-agent
// summaries from callagent results.
func EntryEventRecords(agentID, sessionID string, entry session.SessionEntry, toolCalls map[string]ToolCallState) []runtimeevents.EventRecord {
	record := runtimeevents.EventRecord{
		RunID:     strings.TrimSpace(entry.RunID),
		AgentID:   strings.TrimSpace(agentID),
		SessionID: strings.TrimSpace(sessionID),
		Timestamp: time.Unix(entry.Timestamp, 0).UTC(),
	}

	switch entry.Type {
	case session.EntryTypeMessage:
		var msg session.MessageData
		if err := json.Unmarshal(entry.Data, &msg); err != nil {
			return nil
		}
		text := strings.TrimSpace(msg.Text)
		if text == "" && len(msg.Images) == 0 {
			return nil
		}
		payload := map[string]any{"text": text}
		if len(msg.Images) > 0 {
			payload["images"] = msg.Images
		}
		switch strings.TrimSpace(entry.Role) {
		case "user":
			record.Name = runtimeevents.EventSessionInputStored
		case "assistant":
			record.Name = runtimeevents.EventSessionOutputStored
		default:
			record.Name = "session.message.stored"
			payload["role"] = entry.Role
		}
		record.Payload = payload
		return []runtimeevents.EventRecord{record}
	case session.EntryTypeToolCall:
		var call session.ToolCallData
		if err := json.Unmarshal(entry.Data, &call); err != nil {
			return nil
		}
		callID := strings.TrimSpace(call.ID)
		toolName := strings.TrimSpace(call.Tool)
		if callID == "" || toolName == "" {
			return nil
		}
		toolCalls[callID] = ToolCallState{name: toolName, input: call.Input}
		record.Name = runtimeevents.EventToolCallStarted
		record.Payload = map[string]any{
			"id":    callID,
			"tool":  toolName,
			"input": rawJSONValue(call.Input),
		}
		events := []runtimeevents.EventRecord{record}
		if strings.EqualFold(toolName, "callagent") {
			events = append(events, agentCallStartedEvents(record, callID, call.Input)...)
		}
		return events
	case session.EntryTypeToolResult:
		var result session.ToolResultData
		if err := json.Unmarshal(entry.Data, &result); err != nil {
			return nil
		}
		callID := strings.TrimSpace(result.ToolCallID)
		if callID == "" {
			return nil
		}
		call := toolCalls[callID]
		payload := map[string]any{
			"id":     callID,
			"tool":   strings.TrimSpace(call.name),
			"output": strings.TrimSpace(result.Output),
			"error":  strings.TrimSpace(result.Error),
		}
		if len(result.Images) > 0 {
			payload["images"] = result.Images
		}
		if len(result.Metadata) > 0 {
			payload["metadata"] = result.Metadata
		}
		record.Name = runtimeevents.EventToolCompleted
		if strings.TrimSpace(result.Error) != "" || result.IsError {
			record.Name = runtimeevents.EventToolFailed
		}
		record.Payload = payload
		events := []runtimeevents.EventRecord{record}
		if strings.EqualFold(call.name, "callagent") {
			events = append(events, agentCallFinishedEvents(record, callID, call.input, result)...)
		}
		return events
	case session.EntryTypePlan:
		var plan session.PlanData
		if err := json.Unmarshal(entry.Data, &plan); err != nil {
			return nil
		}
		payload := map[string]any{
			"plan": renderPlanData(plan),
		}
		if plan.ID != "" {
			payload["plan_id"] = plan.ID
		}
		if plan.Structured != nil {
			payload["structured"] = plan.Structured
		}
		record.Name = "session.plan.updated"
		record.Payload = payload
		return []runtimeevents.EventRecord{record}
	case session.EntryTypeTodo:
		var todo session.TodoData
		if err := json.Unmarshal(entry.Data, &todo); err != nil {
			return nil
		}
		record.Name = "session.todo.updated"
		record.Payload = map[string]any{
			"id":           todo.ID,
			"content":      todo.Content,
			"status":       todo.Status,
			"created_at":   todo.CreatedAt,
			"completed_at": todo.CompletedAt,
			"run_id":       strings.TrimSpace(todo.RunID),
		}
		return []runtimeevents.EventRecord{record}
	case session.EntryTypeCompaction:
		var compact session.CompactionData
		if err := json.Unmarshal(entry.Data, &compact); err != nil {
			return nil
		}
		record.Name = runtimeevents.EventSessionCompactCompleted
		record.Payload = map[string]any{
			"summary":          compact.Text,
			"trigger":          compact.Trigger,
			"legacy_heuristic": compact.LegacyHeuristic,
		}
		return []runtimeevents.EventRecord{record}
	case session.EntryTypeMeta:
		var msg session.MessageData
		if err := json.Unmarshal(entry.Data, &msg); err != nil {
			return nil
		}
		record.Name = "session.meta.stored"
		record.Payload = map[string]any{
			"role": entry.Role,
			"text": strings.TrimSpace(msg.Text),
		}
		return []runtimeevents.EventRecord{record}
	default:
		return nil
	}
}

func agentCallStartedEvents(base runtimeevents.EventRecord, callID string, rawInput json.RawMessage) []runtimeevents.EventRecord {
	payload, err := tools.NormalizeCallAgentPayload(rawInput)
	if err != nil || len(payload) == 0 {
		return nil
	}
	if tasks, ok := payload["tasks"].([]map[string]any); ok && len(tasks) > 0 {
		events := make([]runtimeevents.EventRecord, 0, len(tasks))
		for _, task := range tasks {
			item := clonePayload(task)
			item["id"] = callID
			events = append(events, withEvent(base, runtimeevents.EventAgentCallStarted, item))
		}
		return events
	}
	payload = clonePayload(payload)
	payload["id"] = callID
	return []runtimeevents.EventRecord{withEvent(base, runtimeevents.EventAgentCallStarted, payload)}
}

func agentCallFinishedEvents(base runtimeevents.EventRecord, callID string, rawInput json.RawMessage, result session.ToolResultData) []runtimeevents.EventRecord {
	if strings.TrimSpace(result.Output) == "" && strings.TrimSpace(result.Error) == "" {
		return nil
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Output)), &parsed); err != nil {
		if strings.TrimSpace(result.Error) == "" {
			return nil
		}
		payload := fallbackAgentCallPayload(rawInput)
		payload["id"] = callID
		payload["status"] = "failed"
		payload["error"] = strings.TrimSpace(result.Error)
		return []runtimeevents.EventRecord{withEvent(base, runtimeevents.EventAgentCallFailed, payload)}
	}

	if items, ok := parsed["results"].([]any); ok && len(items) > 0 {
		events := make([]runtimeevents.EventRecord, 0, len(items))
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if len(item) == 0 {
				continue
			}
			payload := clonePayload(item)
			payload["id"] = callID
			if batchID := strings.TrimSpace(stringFromAny(parsed["batch_id"])); batchID != "" {
				payload["batch_id"] = batchID
			}
			if maxParallel, ok := parsed["max_parallel"]; ok {
				payload["max_parallel"] = maxParallel
			}
			events = append(events, withEvent(base, agentCallFinishedEventName(payload), payload))
		}
		return events
	}

	payload := clonePayload(parsed)
	if len(payload) == 0 {
		return nil
	}
	payload["id"] = callID
	return []runtimeevents.EventRecord{withEvent(base, agentCallFinishedEventName(payload), payload)}
}

func fallbackAgentCallPayload(rawInput json.RawMessage) map[string]any {
	payload, err := tools.NormalizeCallAgentPayload(rawInput)
	if err != nil || len(payload) == 0 {
		return map[string]any{}
	}
	if tasks, ok := payload["tasks"]; ok && tasks != nil {
		return map[string]any{"tasks": tasks}
	}
	return clonePayload(payload)
}

func agentCallFinishedEventName(payload map[string]any) string {
	status := strings.ToLower(strings.TrimSpace(stringFromAny(payload["status"])))
	switch {
	case status == "running" || status == "queued":
		return runtimeevents.EventAgentCallSubmitted
	case status == "failed":
		return runtimeevents.EventAgentCallFailed
	case strings.TrimSpace(stringFromAny(payload["error"])) != "":
		return runtimeevents.EventAgentCallFailed
	default:
		return runtimeevents.EventAgentCallCompleted
	}
}

func withEvent(base runtimeevents.EventRecord, name string, payload map[string]any) runtimeevents.EventRecord {
	return runtimeevents.EventRecord{
		RunID:         base.RunID,
		AgentID:       base.AgentID,
		SessionID:     base.SessionID,
		Name:          name,
		Timestamp:     base.Timestamp,
		Payload:       payload,
		SchemaVersion: base.SchemaVersion,
		Sequence:      base.Sequence,
	}
}

func clonePayload(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func renderPlanData(plan session.PlanData) string {
	if plan.Structured != nil {
		if rendered := runtimeplan.Render(*plan.Structured); rendered != "" {
			return rendered
		}
	}
	return strings.TrimSpace(plan.Plan)
}

func rawJSONValue(raw json.RawMessage) any {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return value
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
