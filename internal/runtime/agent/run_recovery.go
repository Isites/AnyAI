package agent

import (
	"strings"

	"github.com/Isites/anyai/internal/runtime/session"
)

const (
	incompleteTurnMessage    = "本轮执行在中途失败，请重试或换一种方法继续。"
	incompleteTurnRiskNotice = "本轮执行在中途失败，部分工具动作可能已经执行，请先核实后再重试。"
)

func (r *Runtime) maybeSurfaceIncompleteTurn(events chan<- AgentEvent, partialText string, progress turnProgress) {
	if r == nil || r.Session == nil || !r.incompleteTurnEnabled() {
		return
	}
	if progress.SavedAssistant {
		return
	}
	if !progress.StreamedText && !progress.SawToolCall && !progress.ExecutedTool {
		return
	}

	fallback := incompleteTurnMessage
	if progress.SideEffectRisk {
		fallback = incompleteTurnRiskNotice
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return
	}

	r.Session.Append(session.AssistantMessageEntry(fallback))
	events <- AgentEvent{Type: EventRunIncomplete, Text: fallback}
	events <- AgentEvent{Type: EventFallbackReply, Text: fallback}
	if strings.TrimSpace(partialText) == "" {
		events <- AgentEvent{Type: EventTextDelta, Text: fallback}
	}
}

func (r *Runtime) incompleteTurnEnabled() bool {
	if r == nil {
		return true
	}
	if r.IncompleteTurn == (IncompleteTurnConfig{}) {
		return true
	}
	return r.IncompleteTurn.Enabled
}

func isReadOnlyToolName(name string) bool {
	return toolMetadataForCall(nil, name).IsReadOnly()
}
