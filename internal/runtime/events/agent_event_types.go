package runtimeevents

import (
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

// AgentEvent is a single streaming event from the agent.
type AgentEvent struct {
	Type          AgentEventType
	Text          string
	Query         string
	MemoryMatches []memory.SearchMatch
	LLMRetry      *LLMRetryInfo
	ToolRetry     *ToolRetryInfo
	ToolWarning   *ToolWarningInfo
	ToolCall      *llm.ToolCall
	ToolMetadata  *tools.ToolMetadata
	Result        *tools.ToolResult
	ToolFanout    *ToolFanoutInfo
	Error         error
}

// LLMRetryInfo describes one automatic retry decision for an LLM call.
type LLMRetryInfo struct {
	Attempt     int
	MaxAttempts int
	WaitMS      int
	Stage       string
	Error       string
}

// ToolRetryInfo describes one automatic retry decision for a tool call.
type ToolRetryInfo struct {
	ToolName    string
	Attempt     int
	MaxAttempts int
	WaitMS      int
	ErrorClass  string
	Error       string
	Decision    string
}

// ToolWarningInfo describes a loop-detection or no-progress warning raised for
// a tool call.
type ToolWarningInfo struct {
	ToolName string
	Detector string
	Count    int
	Message  string
	Blocked  bool
}

// ToolFanoutInfo describes the terminal fan-in summary for one batch of tool
// effects emitted by the same agent turn.
type ToolFanoutInfo struct {
	TotalCount     int
	StartedCount   int
	CompletedCount int
	FailedCount    int
	Status         string
	Calls          []ToolFanoutCallInfo
}

// ToolFanoutCallInfo describes a single tool call in a fanout batch.
type ToolFanoutCallInfo struct {
	ID             string
	ToolName       string
	Status         string
	StartedOrder   int
	CompletedOrder int
	DurationMS     int64
	Index          int
	StartedAt      string
	CompletedAt    string
	Error          string
	OutputSize     int
	IsCancellation bool
}
