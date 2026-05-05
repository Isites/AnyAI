package runtimeevents

const (
	EventRunAccepted      = "run.accepted"
	EventRunQueued        = "run.queued"
	EventRunRouted        = "run.routed"
	EventRunRouteRejected = "run.route.rejected"
	EventRunStarted       = "run.started"
	EventRunActivity      = "run.activity"
	EventRunIncomplete    = "run.incomplete"
	EventRunFallbackReply = "run.fallback_reply"
	EventRunCompleted     = "run.completed"
	EventRunFailed        = "run.failed"
	EventRunAborted       = "run.aborted"
)

const (
	EventTaskQueued    = "task.queued"
	EventTaskStarted   = "task.started"
	EventTaskRunning   = "task.running"
	EventTaskCompleted = "task.completed"
	EventTaskFailed    = "task.failed"
	EventTaskCancelled = "task.cancelled"
)

const (
	EventTextDelta      = "text.delta"
	EventMemoryRecalled = "memory.recalled"
	EventLLMRetrying    = "llm.retrying"
)

const (
	EventToolRetrying       = "tool.retrying"
	EventToolWarning        = "tool.warning"
	EventToolCompleted      = "tool.completed"
	EventToolFailed         = "tool.failed"
	EventToolFanoutComplete = "tool.fanout.completed"
)

const (
	EventToolCallRequested = "tool.call.requested"
	EventToolCallStarted   = "tool.call.started"
)

const (
	EventAgentCallStarted   = "agent.call.started"
	EventAgentCallSubmitted = "agent.call.submitted"
	EventAgentCallCompleted = "agent.call.completed"
	EventAgentCallFailed    = "agent.call.failed"
)

const (
	EventSessionCompactRequested = "session.compact.requested"
	EventSessionCompactCompleted = "session.compact.completed"
	EventSessionInputStored      = "session.input.stored"
	EventSessionOutputStored     = "session.output.stored"
)

const (
	EventInputReceived    = "input.received"
	EventInputNormalized  = "input.normalized"
	EventAttachmentStored = "attachment.stored"
)

const (
	EventMemoryCaptured = "memory.captured"
)
