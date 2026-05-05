package lifecycle

import (
	"strings"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/memory"
	"github.com/Isites/anyai/internal/runtime/session"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

// Pipeline owns runtime-side memory lifecycle hooks.
//
// Durable memory writes are intentionally model-driven via memory_save. The
// runtime no longer tries to infer durable knowledge from free-form chat with
// regex or confirmation heuristics because that path is noisy, language-
// dependent, and hard to keep aligned with multi-agent workflows.
type Pipeline struct {
	manager *memory.Manager
	cfg     config.MemoryConfig
}

func NewPipeline(manager *memory.Manager, cfg config.MemoryConfig) *Pipeline {
	return &Pipeline{manager: manager, cfg: cfg}
}

func (p *Pipeline) enabled() bool {
	return p != nil && p.manager != nil && p.cfg.AutoCapture.Enabled
}

func (p *Pipeline) cleanupExpired() {
	if !p.enabled() {
		return
	}
	if _, err := p.manager.CleanupExpiredMaybe(time.Now().UTC()); err != nil {
		// Cleanup is opportunistic and should never block the main run path.
		return
	}
}

func (p *Pipeline) CaptureSessionEnd(run runtimeevents.RunRecord, sess *session.Session, status runtimeevents.RunStatus, output, errMsg string) {
	if !p.enabled() || !p.cfg.AutoCapture.OnSessionEnd || sess == nil {
		return
	}
	if shouldSkipSessionMemoryCapture(run, sess) {
		return
	}
	p.cleanupExpired()

	// Auto-capture is intentionally conservative: session-end hooks no longer
	// derive durable memory from free-form dialogue. Agents should persist
	// stable knowledge explicitly through memory_save.
	_ = status
	_ = output
	_ = errMsg
}

func (p *Pipeline) CaptureAgentCallComplete(parent tools.RuntimeContext, req tools.AgentCallRequest, result tools.AgentCallResult) {
	if !p.enabled() || !p.cfg.AutoCapture.OnAgentCallComplete {
		return
	}
	p.cleanupExpired()

	// Child-agent summaries frequently contain transient workflow chatter. Keep
	// agent-call completion hooks side-effect free unless a future structured
	// source is introduced.
	_ = parent
	_ = req
	_ = result
}

func shouldSkipSessionMemoryCapture(run runtimeevents.RunRecord, sess *session.Session) bool {
	if strings.EqualFold(strings.TrimSpace(run.Channel), "agent_call") {
		return true
	}
	sessionID := strings.ToLower(strings.TrimSpace(run.SessionID))
	if strings.HasPrefix(sessionID, "agentcall_") {
		return true
	}
	if sess != nil {
		sessionID := strings.ToLower(strings.TrimSpace(sess.ID))
		if strings.HasPrefix(sessionID, "agentcall_") {
			return true
		}
	}
	return false
}
