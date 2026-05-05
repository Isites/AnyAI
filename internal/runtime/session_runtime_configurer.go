package runtime

import (
	"context"

	runtimeagent "github.com/Isites/anyai/internal/runtime/agent"
	runtimesessionops "github.com/Isites/anyai/internal/runtime/session/ops"
)

type sessionRuntimeConfigurer struct {
	sessions *runtimesessionops.Service
}

func newSessionRuntimeConfigurer(sessions *runtimesessionops.Service) *sessionRuntimeConfigurer {
	if sessions == nil {
		return nil
	}
	return &sessionRuntimeConfigurer{sessions: sessions}
}

func (c *sessionRuntimeConfigurer) ConfigureAgentRuntime(rt any) error {
	if c == nil || c.sessions == nil || rt == nil {
		return nil
	}
	agentRT, ok := rt.(*runtimeagent.Runtime)
	if !ok || agentRT.Session == nil {
		return nil
	}
	c.sessionsHook(agentRT)
	return nil
}

func (c *sessionRuntimeConfigurer) sessionsHook(rt *runtimeagent.Runtime) {
	rt.Hooks.AfterCompaction = append(rt.Hooks.AfterCompaction, c.afterCompactionHook())
}

func (c *sessionRuntimeConfigurer) afterCompactionHook() runtimeagent.AfterCompactionHook {
	return func(_ context.Context, _ runtimeagent.CompactionState) error {
		return c.sessions.RebuildIndex()
	}
}
