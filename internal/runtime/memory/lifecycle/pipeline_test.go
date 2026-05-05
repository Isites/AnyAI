package lifecycle

import (
	"fmt"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/memory"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipelineSessionEndDoesNotInferDurableMemory(t *testing.T) {
	manager := memory.NewManager(t.TempDir())
	require.NoError(t, manager.Load())

	pipeline := NewPipeline(manager, config.MemoryConfig{
		AutoCapture: config.MemoryAutoCaptureConfig{
			Enabled:             true,
			OnSessionEnd:        true,
			OnUserConfirmed:     true,
			OnHighValueDecision: true,
			MinImportance:       0.1,
		},
	})

	sess := session.NewSession("lead", "sess_1")
	sess.Append(session.UserMessageEntry("Yes, confirmed. We must use the new architecture boundary."))
	sess.Append(session.AssistantMessageEntry("Confirmed. Decision: the service must stay isolated."))

	pipeline.CaptureSessionEnd(runtimeevents.RunRecord{
		ID:        "run_1",
		AgentID:   "lead",
		SessionID: "sess_1",
	}, sess, runtimeevents.RunStatusCompleted, "Completed successfully", "")

	assert.Empty(t, manager.Entries())
}

func TestPipelineDoesNotPersistAgentCallResultsAutomatically(t *testing.T) {
	manager := memory.NewManager(t.TempDir())
	require.NoError(t, manager.Load())

	pipeline := NewPipeline(manager, config.MemoryConfig{
		AutoCapture: config.MemoryAutoCaptureConfig{
			Enabled:              true,
			OnAgentCallComplete:  true,
			OnHighValueDecision:  true,
			MinImportance:        0.1,
			PromoteMinImportance: 0.95,
		},
	})

	pipeline.CaptureAgentCallComplete(
		tools.RuntimeContext{AgentID: "lead"},
		tools.AgentCallRequest{Agent: "coder", Task: "Implement the architecture decision."},
		tools.AgentCallResult{
			Agent:     "coder",
			Status:    "completed",
			Summary:   "Decision: must keep the boundary isolated.",
			SessionID: "sess_child",
		},
	)

	assert.Empty(t, manager.Entries())
}

func TestPipelineCleanupExpiredEntriesOnSessionEnd(t *testing.T) {
	manager := memory.NewManager(t.TempDir())
	manager.SetRefreshInterval(0)
	require.NoError(t, manager.Load())

	now := time.Now().UTC()
	require.NoError(t, manager.SaveToLayer(memory.LayerCandidates, "expired", managedMemoryDoc("Expired", now.Add(-time.Hour))))
	require.NoError(t, manager.SaveToLayer(memory.LayerCandidates, "active", managedMemoryDoc("Active", now.Add(time.Hour))))

	pipeline := NewPipeline(manager, config.MemoryConfig{
		AutoCapture: config.MemoryAutoCaptureConfig{
			Enabled:      true,
			OnSessionEnd: true,
		},
	})

	sess := session.NewSession("lead", "sess_cleanup")
	sess.Append(session.UserMessageEntry("Just wrap up the run."))

	pipeline.CaptureSessionEnd(runtimeevents.RunRecord{
		ID:        "run_cleanup",
		AgentID:   "lead",
		SessionID: "sess_cleanup",
	}, sess, runtimeevents.RunStatusCompleted, "Completed successfully", "")

	entries := manager.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "candidates/active", entries[0].ID)
}

func TestPipelineSkipsAgentCallSessions(t *testing.T) {
	manager := memory.NewManager(t.TempDir())
	require.NoError(t, manager.Load())

	pipeline := NewPipeline(manager, config.MemoryConfig{
		AutoCapture: config.MemoryAutoCaptureConfig{
			Enabled:      true,
			OnSessionEnd: true,
		},
	})

	sess := session.NewSession("context-analyst", "agentcall_context-analyst_trace_1")
	sess.Append(session.UserMessageEntry("Yes, confirmed. We must keep the requirements traceable."))
	sess.Append(session.AssistantMessageEntry("Decision: every phase must write intermediate artifacts."))

	pipeline.CaptureSessionEnd(runtimeevents.RunRecord{
		ID:        "run_agentcall",
		AgentID:   "context-analyst",
		SessionID: "agentcall_context-analyst_trace_1",
		Channel:   "callagent",
	}, sess, runtimeevents.RunStatusCompleted, "Completed successfully", "")

	assert.Empty(t, manager.Entries())
}

func managedMemoryDoc(title string, expireAt time.Time) string {
	return fmt.Sprintf(`# %s

Generated at: %s

- Expire At: %s

## Notes
- test
`, title, time.Now().UTC().Format(time.RFC3339), expireAt.UTC().Format(time.RFC3339))
}
