package session

import "context"

import runtimeplan "github.com/Isites/anyai/internal/runtime/plan"

// TodoItem represents a single todo entry for tool interfaces.
type TodoItem struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	RunID     string `json:"run_id,omitempty"`
}

// PlanStore manages plan state for the update_plan tool.
type PlanStore interface {
	UpdateStructuredPlan(plan runtimeplan.Plan) error
	GetStructuredPlan() (runtimeplan.Plan, bool)
}

// TodoStore manages todo items for the todo tool.
type TodoStore interface {
	AddTodo(ctx context.Context, item string) string
	CompleteTodo(ctx context.Context, id string) bool
	ListTodos(ctx context.Context) []TodoItem
}
