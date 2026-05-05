package agent

import runtimeevents "github.com/Isites/anyai/internal/runtime/events"

type AgentEvent = runtimeevents.AgentEvent
type EventType = runtimeevents.AgentEventType
type LLMRetryInfo = runtimeevents.LLMRetryInfo
type ToolRetryInfo = runtimeevents.ToolRetryInfo
type ToolWarningInfo = runtimeevents.ToolWarningInfo
type ToolFanoutInfo = runtimeevents.ToolFanoutInfo
type ToolFanoutCallInfo = runtimeevents.ToolFanoutCallInfo

const (
	EventRunStarted          = runtimeevents.AgentEventRunStarted
	EventMemoryRecall        = runtimeevents.AgentEventMemoryRecall
	EventActivity            = runtimeevents.AgentEventActivity
	EventLLMRetry            = runtimeevents.AgentEventLLMRetry
	EventToolRetry           = runtimeevents.AgentEventToolRetry
	EventTextDelta           = runtimeevents.AgentEventTextDelta
	EventToolCallRequested   = runtimeevents.AgentEventToolCallRequested
	EventToolCallStart       = runtimeevents.AgentEventToolCallStart
	EventToolWarning         = runtimeevents.AgentEventToolWarning
	EventToolResult          = runtimeevents.AgentEventToolResult
	EventToolFanoutCompleted = runtimeevents.AgentEventToolFanoutCompleted
	EventRunIncomplete       = runtimeevents.AgentEventRunIncomplete
	EventFallbackReply       = runtimeevents.AgentEventFallbackReply
	EventDone                = runtimeevents.AgentEventDone
	EventError               = runtimeevents.AgentEventError
	EventAborted             = runtimeevents.AgentEventAborted
)
