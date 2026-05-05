package session

import (
	"encoding/json"
	"sort"
	"strings"

	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
)

// LatestPlan returns the newest plan snapshot stored in the session.
func LatestPlan(sess *Session) (string, bool) {
	if sess == nil {
		return "", false
	}
	entries := sess.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type != EntryTypePlan {
			continue
		}
		var data PlanData
		if err := json.Unmarshal(entries[i].Data, &data); err != nil {
			continue
		}
		rendered := renderPlanData(data)
		if rendered != "" {
			return rendered, true
		}
	}
	return "", false
}

// LatestStructuredPlan returns the newest structured plan snapshot stored in
// the session. Legacy text-only plan entries do not hide older structured
// plan state.
func LatestStructuredPlan(sess *Session) (runtimeplan.Plan, bool) {
	if sess == nil {
		return runtimeplan.Plan{}, false
	}
	entries := sess.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type != EntryTypePlan {
			continue
		}
		var data PlanData
		if err := json.Unmarshal(entries[i].Data, &data); err != nil {
			continue
		}
		if data.Structured == nil {
			continue
		}
		return runtimeplan.Normalize(*data.Structured), true
	}
	return runtimeplan.Plan{}, false
}

// TodoSnapshot returns the latest known state for each todo item keyed by ID.
func TodoSnapshot(sess *Session) map[string]TodoData {
	if sess == nil {
		return nil
	}
	state := map[string]TodoData{}
	for _, entry := range sess.Entries() {
		if entry.Type != EntryTypeTodo {
			continue
		}
		var item TodoData
		if err := json.Unmarshal(entry.Data, &item); err != nil {
			continue
		}
		if item.ID == "" {
			continue
		}
		state[item.ID] = item
	}
	return state
}

// TodoItems returns todo items in creation order using their latest snapshots.
func TodoItems(sess *Session) []TodoItem {
	return todoItemsFromSnapshot(TodoSnapshot(sess), func(TodoData) bool { return true })
}

// TodoItemsForRun returns todo items scoped to one run in creation order.
func TodoItemsForRun(sess *Session, runID string, includeUnscoped bool) []TodoItem {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	items := TodoSnapshot(sess)
	scoped := todoItemsFromSnapshot(items, func(item TodoData) bool {
		itemRunID := strings.TrimSpace(item.RunID)
		return itemRunID == runID
	})
	if len(scoped) > 0 || !includeUnscoped {
		return scoped
	}
	return todoItemsFromSnapshot(items, func(item TodoData) bool {
		return strings.TrimSpace(item.RunID) == ""
	})
}

func todoItemsFromSnapshot(items map[string]TodoData, keep func(TodoData) bool) []TodoItem {
	if len(items) == 0 {
		return nil
	}
	ordered := make([]TodoData, 0, len(items))
	for _, item := range items {
		if keep != nil && !keep(item) {
			continue
		}
		ordered = append(ordered, item)
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt == ordered[j].CreatedAt {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].CreatedAt < ordered[j].CreatedAt
	})
	result := make([]TodoItem, 0, len(ordered))
	for _, item := range ordered {
		result = append(result, TodoItem{
			ID:        item.ID,
			Content:   item.Content,
			Status:    item.Status,
			CreatedAt: item.CreatedAt,
			RunID:     item.RunID,
		})
	}
	return result
}

func renderPlanData(data PlanData) string {
	if data.Structured != nil {
		if rendered := runtimeplan.Render(*data.Structured); rendered != "" {
			return rendered
		}
	}
	return data.Plan
}
