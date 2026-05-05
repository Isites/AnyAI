package activity

import (
	"context"
	"sync"
	"time"
)

type timeoutContextKey struct{}
type timeoutControllerKey struct{}
type hookContextKey struct{}

type Hook func(map[string]any)

type Controller struct {
	cancel  context.CancelFunc
	timeout time.Duration

	mu       sync.Mutex
	timer    *time.Timer
	closed   bool
	timedOut bool
}

func WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc, *Controller) {
	ctx, cancel := context.WithCancel(parent)
	ctx = context.WithValue(ctx, timeoutContextKey{}, true)
	if timeout <= 0 {
		return ctx, cancel, nil
	}

	controller := &Controller{
		cancel:  cancel,
		timeout: timeout,
	}
	controller.timer = time.AfterFunc(timeout, func() {
		controller.mu.Lock()
		if controller.closed {
			controller.mu.Unlock()
			return
		}
		controller.timedOut = true
		controller.mu.Unlock()
		cancel()
	})
	ctx = context.WithValue(ctx, timeoutControllerKey{}, controller)

	stop := func() {
		controller.Stop()
	}
	return ctx, stop, controller
}

func HasTimeout(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	active, _ := ctx.Value(timeoutContextKey{}).(bool)
	return active
}

func ControllerFromContext(ctx context.Context) *Controller {
	if ctx == nil {
		return nil
	}
	controller, _ := ctx.Value(timeoutControllerKey{}).(*Controller)
	return controller
}

func TouchTimeout(ctx context.Context) {
	if controller := ControllerFromContext(ctx); controller != nil {
		controller.Touch()
	}
}

func (c *Controller) Touch() {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.timer == nil {
		return
	}
	c.timedOut = false
	if !c.timer.Stop() {
		select {
		case <-c.timer.C:
		default:
		}
	}
	c.timer.Reset(c.timeout)
}

func (c *Controller) Stop() {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.timer != nil {
		if !c.timer.Stop() {
			select {
			case <-c.timer.C:
			default:
			}
		}
	}
}

func (c *Controller) TimedOut() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timedOut
}

func WithHook(ctx context.Context, hook Hook) context.Context {
	if ctx == nil || hook == nil {
		return ctx
	}
	if existing := HookFromContext(ctx); existing != nil {
		return context.WithValue(ctx, hookContextKey{}, Hook(func(metadata map[string]any) {
			existing(metadata)
			hook(metadata)
		}))
	}
	return context.WithValue(ctx, hookContextKey{}, hook)
}

func HookFromContext(ctx context.Context) Hook {
	if ctx == nil {
		return nil
	}
	hook, _ := ctx.Value(hookContextKey{}).(Hook)
	return hook
}

func Emit(ctx context.Context, metadata map[string]any) {
	if hook := HookFromContext(ctx); hook != nil {
		hook(metadata)
	}
}
