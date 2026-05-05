package session

import (
	"encoding/json"

	"github.com/Isites/anyai/internal/runtime/contract"
)

// SerializeHistory converts stored session entries into a transport-safe
// JSON-friendly structure shared by HTTP observability surfaces.
func SerializeHistory(sess *Session) []map[string]any {
	history := sess.History()
	var entries []map[string]any

	for _, entry := range history {
		switch entry.Type {
		case EntryTypeMessage:
			var msg MessageData
			if err := json.Unmarshal(entry.Data, &msg); err != nil {
				continue
			}
			item := map[string]any{
				"type": "message",
				"role": entry.Role,
				"text": msg.Text,
			}
			if len(msg.Images) > 0 {
				item["images"] = msg.Images
			}
			entries = append(entries, item)
		case EntryTypeToolCall:
			var tc ToolCallData
			if err := json.Unmarshal(entry.Data, &tc); err != nil {
				continue
			}
			entries = append(entries, map[string]any{
				"type":  "tool_call",
				"id":    tc.ID,
				"tool":  tc.Tool,
				"input": contract.SanitizeRawJSON(tc.Input),
			})
		case EntryTypeToolResult:
			var tr ToolResultData
			if err := json.Unmarshal(entry.Data, &tr); err != nil {
				continue
			}
			entries = append(entries, map[string]any{
				"type":         "tool_result",
				"tool_call_id": tr.ToolCallID,
				"output":       contract.SanitizeText(tr.Output),
				"error":        contract.SanitizeText(tr.Error),
				"is_error":     tr.IsError,
				"metadata":     contract.SanitizeMetadata(tr.Metadata),
				"images":       tr.Images,
			})
		case EntryTypeMeta:
			var msg MessageData
			if err := json.Unmarshal(entry.Data, &msg); err != nil {
				continue
			}
			entries = append(entries, map[string]any{
				"type": "meta",
				"role": entry.Role,
				"text": msg.Text,
			})
		case EntryTypeCompaction:
			var compact CompactionData
			if err := json.Unmarshal(entry.Data, &compact); err != nil {
				continue
			}
			entries = append(entries, map[string]any{
				"type":             "compaction",
				"role":             entry.Role,
				"text":             compact.Text,
				"trigger":          compact.Trigger,
				"legacy_heuristic": compact.LegacyHeuristic,
			})
		case EntryTypePlan:
			var plan PlanData
			if err := json.Unmarshal(entry.Data, &plan); err != nil {
				continue
			}
			item := map[string]any{
				"type": "plan",
				"role": entry.Role,
				"plan": renderPlanData(plan),
			}
			if plan.Structured != nil {
				item["structured"] = plan.Structured
			}
			entries = append(entries, item)
		case EntryTypeTodo:
			var todo TodoData
			if err := json.Unmarshal(entry.Data, &todo); err != nil {
				continue
			}
			item := map[string]any{
				"type":         "todo",
				"role":         entry.Role,
				"id":           todo.ID,
				"content":      todo.Content,
				"status":       todo.Status,
				"created_at":   todo.CreatedAt,
				"completed_at": todo.CompletedAt,
			}
			if todo.RunID != "" {
				item["run_id"] = todo.RunID
			}
			entries = append(entries, item)
		}
	}

	return entries
}
