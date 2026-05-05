package session

import "encoding/json"

// PendingToolCalls returns tool calls that do not yet have a matching tool
// result in the current history order.
func PendingToolCalls(history []SessionEntry) []ToolCallData {
	if len(history) == 0 {
		return nil
	}

	pendingByID := make(map[string]ToolCallData)
	order := make([]string, 0)
	for _, entry := range history {
		switch entry.Type {
		case EntryTypeToolCall:
			var tc ToolCallData
			if err := json.Unmarshal(entry.Data, &tc); err != nil {
				continue
			}
			if tc.ID == "" {
				continue
			}
			if _, exists := pendingByID[tc.ID]; !exists {
				order = append(order, tc.ID)
			}
			pendingByID[tc.ID] = tc
		case EntryTypeToolResult:
			var tr ToolResultData
			if err := json.Unmarshal(entry.Data, &tr); err != nil {
				continue
			}
			if tr.ToolCallID == "" {
				continue
			}
			delete(pendingByID, tr.ToolCallID)
		}
	}

	result := make([]ToolCallData, 0, len(pendingByID))
	for _, id := range order {
		tc, ok := pendingByID[id]
		if !ok {
			continue
		}
		result = append(result, tc)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
