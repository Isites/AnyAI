package task

import (
	"context"
	"fmt"
)

// Executor is the interface for executing tasks of a specific kind.
// Each task kind (agent, tool, process) implements this interface.
type Executor interface {
	// Kind returns the kind of tasks this executor handles.
	Kind() Kind

	// Execute runs the task and returns the result.
	// The context is scoped to the task execution and can be cancelled.
	Execute(ctx context.Context, task Record) (Result, error)
}

// ExecutorRegistry holds task executors by kind.
type ExecutorRegistry struct {
	executors map[Kind]Executor
}

// NewExecutorRegistry creates a new executor registry.
func NewExecutorRegistry() *ExecutorRegistry {
	return &ExecutorRegistry{
		executors: make(map[Kind]Executor),
	}
}

// Register adds an executor for a task kind.
// Returns error if an executor for this kind is already registered.
func (r *ExecutorRegistry) Register(kind Kind, executor Executor) error {
	if r == nil {
		return fmt.Errorf("executor registry is nil")
	}
	if executor == nil {
		return fmt.Errorf("executor cannot be nil")
	}
	if r.executors == nil {
		r.executors = make(map[Kind]Executor)
	}
	if _, exists := r.executors[kind]; exists {
		return fmt.Errorf("executor for kind %q already registered", kind)
	}
	r.executors[kind] = executor
	return nil
}

// Get retrieves an executor for the given kind.
// Returns nil if not found.
func (r *ExecutorRegistry) Get(kind Kind) Executor {
	if r == nil || r.executors == nil {
		return nil
	}
	return r.executors[kind]
}

// Has checks if an executor is registered for the given kind.
func (r *ExecutorRegistry) Has(kind Kind) bool {
	return r.Get(kind) != nil
}

// List returns all registered executor kinds.
func (r *ExecutorRegistry) List() []Kind {
	if r == nil || r.executors == nil {
		return nil
	}
	kinds := make([]Kind, 0, len(r.executors))
	for kind := range r.executors {
		kinds = append(kinds, kind)
	}
	return kinds
}
