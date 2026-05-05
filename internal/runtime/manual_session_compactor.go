package runtime

import (
	"context"
	"fmt"
	"strings"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimefactory "github.com/Isites/anyai/internal/runtime/factory"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

type manualSessionCompactor struct {
	deps *runtimeport.DependencySet
}

func newManualSessionCompactor(deps *runtimeport.DependencySet) *manualSessionCompactor {
	if deps == nil {
		return nil
	}
	return &manualSessionCompactor{deps: deps}
}

func (c *manualSessionCompactor) CompactSession(agentID, sessionID string, keepEntries int) error {
	if c == nil || c.deps == nil {
		return fmt.Errorf("session compactor not available")
	}
	agentID = strings.TrimSpace(agentID)
	sessionID = strings.TrimSpace(sessionID)
	if agentID == "" || sessionID == "" {
		return fmt.Errorf("agent_id and session_id are required")
	}

	deps := c.deps.ExecutionDeps()
	if deps.SessionStore == nil {
		return fmt.Errorf("session store not available")
	}
	sess, err := deps.SessionStore.Load(agentID, sessionID)
	if err != nil {
		return err
	}
	resolved, err := runtimefactory.ResolveAgentRuntime(deps, agentID)
	if err != nil {
		return err
	}
	rt, err := runtimefactory.BuildAgentRuntimeFromDeps(deps, resolved, sess, tools.ExtraToolDeps{})
	if err != nil {
		return err
	}

	ctx := context.Background()
	var run runtimeevents.RunRecord
	if recorder := deps.Recorder; recorder != nil {
		run, _ = runtimeevents.StartSyntheticRun(recorder, runtimeevents.SyntheticRunSpec{
			AgentID:   resolved.Agent.ID,
			SessionID: sessionID,
			Model:     resolved.ModelName,
			Channel:   "control",
		})
		if strings.TrimSpace(run.ID) != "" {
			ctx = tools.WithRuntimeContext(ctx, tools.RuntimeContext{
				RunID:     run.ID,
				AgentID:   resolved.Agent.ID,
				SessionID: sessionID,
			})
		}
	}

	result, err := rt.CompactSessionNow(ctx, keepEntries)
	if strings.TrimSpace(run.ID) != "" && deps.Recorder != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		runtimeevents.FinishSyntheticRun(deps.Recorder, run, result.Summary, errMsg)
	}
	return err
}
