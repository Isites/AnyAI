package runtimeevents

// AgentEventType identifies the kind of agent event.
type AgentEventType int

const (
	AgentEventRunStarted AgentEventType = iota
	AgentEventMemoryRecall
	AgentEventActivity
	AgentEventLLMRetry
	AgentEventToolRetry
	AgentEventTextDelta
	AgentEventToolCallRequested
	AgentEventToolCallStart
	AgentEventToolWarning
	AgentEventToolResult
	AgentEventToolFanoutCompleted
	AgentEventRunIncomplete
	AgentEventFallbackReply
	AgentEventDone
	AgentEventError
	AgentEventAborted
)

// String returns the string representation of an event type.
func (t AgentEventType) String() string {
	switch t {
	case AgentEventRunStarted:
		return EventRunStarted
	case AgentEventMemoryRecall:
		return EventMemoryRecalled
	case AgentEventActivity:
		return EventRunActivity
	case AgentEventLLMRetry:
		return EventLLMRetrying
	case AgentEventToolRetry:
		return EventToolRetrying
	case AgentEventTextDelta:
		return EventTextDelta
	case AgentEventToolCallRequested:
		return EventToolCallRequested
	case AgentEventToolCallStart:
		return EventToolCallStarted
	case AgentEventToolWarning:
		return EventToolWarning
	case AgentEventToolResult:
		return EventToolCompleted
	case AgentEventToolFanoutCompleted:
		return EventToolFanoutComplete
	case AgentEventRunIncomplete:
		return EventRunIncomplete
	case AgentEventFallbackReply:
		return EventRunFallbackReply
	case AgentEventDone:
		return EventRunCompleted
	case AgentEventError:
		return EventRunFailed
	case AgentEventAborted:
		return EventRunAborted
	default:
		return "unknown"
	}
}
