package turn

import (
	"context"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/runtime/contract"
)

// ID is the stable identifier for one runtime turn lifecycle.
type ID string

// State is the lifecycle state for a turn.
type State string

const (
	StateActive    State = "active"
	StateCompleted State = "completed"
	StateCancelled State = "cancelled"
	StateTimedOut  State = "timed_out"
)

// Config describes how to create a turn.
type Config struct {
	ID           ID
	SessionID    string
	ParentTurnID ID
	IdleTimeout  time.Duration
}

// Turn groups a set of related tasks and runs under one shared lifecycle.
type Turn struct {
	ID           ID
	SessionID    string
	ParentTurnID ID
	IdleTimeout  time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.RWMutex
	state          State
	createdAt      time.Time
	startedAt      time.Time
	lastActivityAt time.Time
	completedAt    time.Time
	tasks          map[string]struct{}
	timer          *time.Timer
}

// Info is a snapshot of the current turn state.
type Info struct {
	ID             ID
	SessionID      string
	ParentTurnID   ID
	State          State
	IdleTimeout    time.Duration
	CreatedAt      time.Time
	StartedAt      time.Time
	CompletedAt    time.Time
	LastActivityAt time.Time
	TaskCount      int
}

// New creates a new turn derived from the provided parent context.
func New(parent context.Context, cfg Config) *Turn {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	now := time.Now().UTC()
	id := cfg.ID
	if id == "" {
		id = ID(contract.NewOpaqueID("turn"))
	}
	t := &Turn{
		ID:             id,
		SessionID:      cfg.SessionID,
		ParentTurnID:   cfg.ParentTurnID,
		IdleTimeout:    cfg.IdleTimeout,
		ctx:            ctx,
		cancel:         cancel,
		state:          StateActive,
		createdAt:      now,
		startedAt:      now,
		lastActivityAt: now,
		tasks:          make(map[string]struct{}),
	}
	if cfg.IdleTimeout > 0 {
		t.timer = time.AfterFunc(cfg.IdleTimeout, t.handleTimeout)
	}
	go t.watchContext()
	return t
}

// Context returns the shared lifecycle context for this turn.
func (t *Turn) Context() context.Context {
	if t == nil {
		return nil
	}
	return t.ctx
}

// Done returns the turn context done channel.
func (t *Turn) Done() <-chan struct{} {
	if t == nil || t.ctx == nil {
		return nil
	}
	return t.ctx.Done()
}

// Err returns the terminal context error for the turn.
func (t *Turn) Err() error {
	if t == nil || t.ctx == nil {
		return nil
	}
	return t.ctx.Err()
}

// Touch refreshes the idle timeout for the turn.
func (t *Turn) Touch() {
	if t == nil {
		return
	}
	now := time.Now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != StateActive {
		return
	}
	t.lastActivityAt = now
	if t.timer == nil || t.IdleTimeout <= 0 {
		return
	}
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
	}
	t.timer.Reset(t.IdleTimeout)
}

// RegisterTask marks a task as participating in this turn.
func (t *Turn) RegisterTask(taskID string) {
	if t == nil || taskID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[taskID] = struct{}{}
}

// UnregisterTask removes a task from the turn.
func (t *Turn) UnregisterTask(taskID string) {
	if t == nil || taskID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.tasks, taskID)
}

// TaskCount returns the number of registered tasks.
func (t *Turn) TaskCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.tasks)
}

// Complete marks the turn as completed and closes its context.
func (t *Turn) Complete() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.state != StateActive {
		t.mu.Unlock()
		return
	}
	t.state = StateCompleted
	t.completedAt = time.Now().UTC()
	t.mu.Unlock()
	t.cancel()
}

// Cancel marks the turn as cancelled and closes its context.
func (t *Turn) Cancel(_ string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.state != StateActive {
		t.mu.Unlock()
		return
	}
	t.state = StateCancelled
	t.completedAt = time.Now().UTC()
	t.mu.Unlock()
	t.cancel()
}

// State returns the current turn state.
func (t *Turn) State() State {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state
}

// Info returns a snapshot of the turn.
func (t *Turn) Info() Info {
	if t == nil {
		return Info{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return Info{
		ID:             t.ID,
		SessionID:      t.SessionID,
		ParentTurnID:   t.ParentTurnID,
		State:          t.state,
		IdleTimeout:    t.IdleTimeout,
		CreatedAt:      t.createdAt,
		StartedAt:      t.startedAt,
		CompletedAt:    t.completedAt,
		LastActivityAt: t.lastActivityAt,
		TaskCount:      len(t.tasks),
	}
}

func (t *Turn) handleTimeout() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.state != StateActive {
		t.mu.Unlock()
		return
	}
	t.state = StateTimedOut
	t.completedAt = time.Now().UTC()
	t.mu.Unlock()
	t.cancel()
}

func (t *Turn) watchContext() {
	if t == nil || t.ctx == nil {
		return
	}
	<-t.ctx.Done()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timer != nil {
		if !t.timer.Stop() {
			select {
			case <-t.timer.C:
			default:
			}
		}
	}
	if t.state == StateActive {
		t.state = StateCancelled
		t.completedAt = time.Now().UTC()
	}
}
