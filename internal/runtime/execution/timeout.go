package execution

import (
	"context"
	"time"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
)

type Controller = runtimeactivity.Controller

func WithActivity(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc, *Controller) {
	return runtimeactivity.WithTimeout(parent, timeout)
}

func HasActivity(ctx context.Context) bool {
	return runtimeactivity.HasTimeout(ctx)
}

func ControllerFromContext(ctx context.Context) *Controller {
	return runtimeactivity.ControllerFromContext(ctx)
}

func Touch(ctx context.Context) {
	runtimeactivity.TouchTimeout(ctx)
}
