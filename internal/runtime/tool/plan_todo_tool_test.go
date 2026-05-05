package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPlanStore implements PlanStore for testing.
type mockPlanStore struct {
	mu         sync.Mutex
	structured runtimeplan.Plan
}

func (m *mockPlanStore) UpdateStructuredPlan(plan runtimeplan.Plan) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.structured = runtimeplan.Normalize(plan)
	return nil
}

func (m *mockPlanStore) GetStructuredPlan() (runtimeplan.Plan, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.structured, m.structured.ID != ""
}

// mockTodoStore implements TodoStore for testing.
type mockTodoStore struct {
	mu    sync.Mutex
	items []TodoItem
}

func (m *mockTodoStore) AddTodo(_ context.Context, item string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "todo_" + item[:4]
	m.items = append(m.items, TodoItem{
		ID:        id,
		Content:   item,
		Status:    "pending",
		CreatedAt: 1712345678,
	})
	return id
}

func (m *mockTodoStore) CompleteTodo(_ context.Context, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.items {
		if m.items[i].ID == id {
			m.items[i].Status = "completed"
			return true
		}
	}
	return false
}

func (m *mockTodoStore) ListTodos(_ context.Context) []TodoItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]TodoItem(nil), m.items...)
}

func TestUpdatePlanTool(t *testing.T) {
	store := &mockPlanStore{}
	tool := &UpdatePlanTool{Store: store}

	input, _ := json.Marshal(map[string]any{
		"plan": map[string]any{
			"description": "Ship the fix",
			"state":       "running",
			"steps": []map[string]any{
				{
					"id":          "inspect",
					"description": "Inspect the bug",
					"state":       "completed",
				},
				{
					"id":          "patch",
					"description": "Implement the patch",
					"state":       "pending",
				},
			},
		},
	})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "Structured plan updated.", result.Output)

	plan, ok := store.GetStructuredPlan()
	assert.True(t, ok)
	assert.Equal(t, "Ship the fix", plan.Description)
	require.Len(t, plan.Steps, 2)
	assert.Equal(t, runtimeplan.StepStatePending, plan.Steps[1].State)
}

func TestUpdatePlanToolCanonicalizesRunningAliases(t *testing.T) {
	store := &mockPlanStore{}
	tool := &UpdatePlanTool{Store: store}

	input, _ := json.Marshal(map[string]any{
		"plan": map[string]any{
			"description": "Ship the fix",
			"state":       "in_progress",
			"steps": []map[string]any{
				{
					"id":          "inspect",
					"description": "Inspect the bug",
					"state":       "in-progress",
				},
				{
					"id":          "patch",
					"description": "Implement the patch",
					"state":       "doing",
				},
			},
		},
	})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "Structured plan updated.", result.Output)

	plan, ok := store.GetStructuredPlan()
	require.True(t, ok)
	assert.Equal(t, runtimeplan.PlanStateRunning, plan.State)
	require.Len(t, plan.Steps, 2)
	assert.Equal(t, runtimeplan.StepStateRunning, plan.Steps[0].State)
	assert.Equal(t, runtimeplan.StepStateRunning, plan.Steps[1].State)
}

func TestUpdatePlanToolRejectsLegacyTextPayload(t *testing.T) {
	store := &mockPlanStore{}
	tool := &UpdatePlanTool{Store: store}

	input, _ := json.Marshal(map[string]any{"plan": "Step 1: Do X\nStep 2: Do Y"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "legacy text plan payload is no longer supported")
}

func TestTodoToolAdd(t *testing.T) {
	store := &mockTodoStore{}
	tool := &TodoTool{Store: store}

	input, _ := json.Marshal(map[string]any{"action": "add", "item": "Write tests"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "added")
}

func TestTodoToolComplete(t *testing.T) {
	store := &mockTodoStore{}
	tool := &TodoTool{Store: store}

	// Add first
	id := store.AddTodo(context.Background(), "Write tests")

	input, _ := json.Marshal(map[string]any{"action": "complete", "item": id})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "completed")
}

func TestTodoToolList(t *testing.T) {
	store := &mockTodoStore{}
	tool := &TodoTool{Store: store}
	store.AddTodo(context.Background(), "Task A")
	store.AddTodo(context.Background(), "Task B")

	input, _ := json.Marshal(map[string]any{"action": "list"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "Task A")
}

func TestTodoToolEmptyList(t *testing.T) {
	store := &mockTodoStore{}
	tool := &TodoTool{Store: store}

	input, _ := json.Marshal(map[string]any{"action": "list"})
	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "No todo items")
}
