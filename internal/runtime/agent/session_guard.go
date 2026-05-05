package agent

import (
	"strings"

	"github.com/Isites/anyai/internal/runtime/session"
)

const defaultSyntheticToolResultMessage = "tool execution was interrupted"

func (r *Runtime) guardSessionToolResults(reason string) int {
	if r == nil || r.Session == nil {
		return 0
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = defaultSyntheticToolResultMessage
	}
	pending := session.PendingToolCalls(r.Session.History())
	if len(pending) == 0 {
		return 0
	}
	for _, call := range pending {
		r.Session.Append(session.ToolResultEntryWithMetadata(
			call.ID,
			"",
			reason,
			map[string]any{
				"tool_name":         call.Tool,
				"input_summary":     string(call.Input),
				"error_message":     reason,
				"error_class":       "interrupted",
				"auto_retryable":    false,
				"model_recoverable": false,
				"decision":          "synthetic_guard_result",
				"synthetic":         true,
				"synthetic_reason":  reason,
			},
			nil,
		))
	}
	return len(pending)
}

func toolGuardReasonForEvent(ev AgentEvent) string {
	switch ev.Type {
	case EventAborted:
		return "tool execution was interrupted because the run was aborted"
	case EventError:
		if ev.Error != nil && strings.TrimSpace(ev.Error.Error()) != "" {
			return "tool execution was interrupted because the run failed: " + strings.TrimSpace(ev.Error.Error())
		}
	}
	return defaultSyntheticToolResultMessage
}
