package state

import (
	"context"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/runtime/contract"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

// StateStore is the runtime-owned session workflow state adapter used by
// update_plan and todo tools. It keeps the session projection and durable
// event log in sync.
type StateStore struct {
	session  *session.Session
	recorder *runtimeevents.Recorder
	run      runtimeevents.RunRecord
	nowFn    func() time.Time
}

func NewStateStore(sess *session.Session, recorder *runtimeevents.Recorder, run runtimeevents.RunRecord) *StateStore {
	return &StateStore{
		session:  sess,
		recorder: recorder,
		run:      run,
		nowFn: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *StateStore) UpdateStructuredPlan(plan runtimeplan.Plan) error {
	if s == nil || s.session == nil {
		return nil
	}
	normalized := runtimeplan.Normalize(plan)
	entry := session.ApplyEntryRefs(session.StructuredPlanEntry(normalized), s.session.ActiveRefs())
	s.session.Append(entry)
	payload := map[string]any{}
	if structured, ok := structuredPlanPayload(normalized); ok {
		payload["structured"] = structured
	}
	if planID := strings.TrimSpace(entry.PlanID); planID != "" {
		payload["plan_id"] = planID
	}
	s.appendEvent("session.plan.updated", payload)
	return nil
}

func (s *StateStore) GetStructuredPlan() (runtimeplan.Plan, bool) {
	return session.LatestStructuredPlan(s.session)
}

func (s *StateStore) AddTodo(ctx context.Context, item string) string {
	if s == nil || s.session == nil {
		return ""
	}
	meta := tools.RuntimeContextFrom(ctx)
	todo := session.TodoData{
		ID:        contract.NewOpaqueID("todo"),
		Content:   item,
		Status:    "open",
		CreatedAt: s.now().Unix(),
		RunID:     strings.TrimSpace(meta.RunID),
	}
	s.session.Append(session.TodoEntry(todo))
	s.appendEvent("session.todo.updated", todoPayload(todo))
	return todo.ID
}

func (s *StateStore) CompleteTodo(_ context.Context, id string) bool {
	if s == nil || s.session == nil {
		return false
	}
	items := session.TodoSnapshot(s.session)
	item, ok := items[id]
	if !ok {
		return false
	}
	if item.Status == "completed" {
		return true
	}
	item.Status = "completed"
	item.CompletedAt = s.now().Unix()
	s.session.Append(session.TodoEntry(item))
	s.appendEvent("session.todo.updated", todoPayload(item))
	return true
}

func (s *StateStore) ListTodos(ctx context.Context) []session.TodoItem {
	meta := tools.RuntimeContextFrom(ctx)
	if runID := strings.TrimSpace(meta.RunID); runID != "" {
		return session.TodoItemsForRun(s.session, runID, true)
	}
	return session.TodoItems(s.session)
}

func (s *StateStore) now() time.Time {
	if s == nil || s.nowFn == nil {
		return time.Now().UTC()
	}
	return s.nowFn()
}

func (s *StateStore) appendEvent(name string, payload map[string]any) {
	if s == nil || s.recorder == nil {
		return
	}
	s.recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     s.run.ID,
		AgentID:   s.run.AgentID,
		SessionID: s.run.SessionID,
		Name:      name,
		Payload:   payload,
		Timestamp: s.now(),
	})
}

func todoPayload(item session.TodoData) map[string]any {
	payload := map[string]any{
		"id":         item.ID,
		"content":    item.Content,
		"status":     item.Status,
		"created_at": item.CreatedAt,
	}
	if strings.TrimSpace(item.RunID) != "" {
		payload["run_id"] = strings.TrimSpace(item.RunID)
	}
	if item.CompletedAt > 0 {
		payload["completed_at"] = item.CompletedAt
	}
	return payload
}

func structuredPlanPayload(plan runtimeplan.Plan) (map[string]any, bool) {
	data := make(map[string]any)
	if plan.ID != "" {
		data["id"] = plan.ID
	}
	if plan.RunID != "" {
		data["run_id"] = plan.RunID
	}
	if plan.Description != "" {
		data["description"] = plan.Description
	}
	if plan.State != "" {
		data["state"] = string(plan.State)
	}
	if plan.StartedAt != nil {
		data["started_at"] = plan.StartedAt
	}
	if plan.CompletedAt != nil {
		data["completed_at"] = plan.CompletedAt
	}
	if len(plan.Steps) > 0 {
		steps := make([]map[string]any, 0, len(plan.Steps))
		for _, step := range plan.Steps {
			item := map[string]any{
				"id":          step.ID,
				"description": step.Description,
				"state":       string(step.State),
			}
			if len(step.Dependencies) > 0 {
				item["dependencies"] = append([]string(nil), step.Dependencies...)
			}
			if step.Action.Type != "" || step.Action.Target != "" || step.Action.Input != "" {
				item["action"] = map[string]any{
					"type":   step.Action.Type,
					"target": step.Action.Target,
					"input":  step.Action.Input,
				}
			}
			if step.Output != "" {
				item["output"] = step.Output
			}
			if step.Error != "" {
				item["error"] = step.Error
			}
			if step.StartedAt != nil {
				item["started_at"] = step.StartedAt
			}
			if step.CompletedAt != nil {
				item["completed_at"] = step.CompletedAt
			}
			steps = append(steps, item)
		}
		data["steps"] = steps
	}
	return data, len(data) > 0
}
