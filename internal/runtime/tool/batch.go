package tools

import (
	"time"

	"github.com/Isites/anyai/internal/runtime/llm"
)

type ToolCallSpec struct {
	Index    int
	Call     llm.ToolCall
	Metadata ToolMetadata
}

type ToolCallOutcome struct {
	Index          int
	Call           llm.ToolCall
	Metadata       ToolMetadata
	Result         ToolResult
	Error          error
	StartedAt      time.Time
	CompletedAt    time.Time
	StartedOrder   int
	CompletedOrder int
}

type ToolBatchSummary struct {
	Calls          []ToolCallOutcome
	TotalCount     int
	StartedCount   int
	CompletedCount int
	FailedCount    int
	Status         string
	StartedAt      time.Time
	CompletedAt    time.Time
}

func PrepareToolCalls(executor Executor, calls []llm.ToolCall) []ToolCallSpec {
	if len(calls) == 0 {
		return nil
	}
	specs := make([]ToolCallSpec, 0, len(calls))
	for i, call := range calls {
		meta := DefaultToolMetadata(call.Name)
		if executor != nil {
			if tool, ok := executor.Get(call.Name); ok {
				meta = DescribeToolMetadata(tool)
			}
		}
		specs = append(specs, ToolCallSpec{
			Index:    i,
			Call:     call,
			Metadata: meta,
		})
	}
	return specs
}
