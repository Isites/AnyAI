package agent

import (
	"context"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/tool"
)

// RuntimeHooks collects optional lifecycle hooks for prompt assembly,
// compaction, tool execution, and run completion.
type RuntimeHooks struct {
	BeforePromptBuild  []BeforePromptBuildHook
	BeforeModelResolve []BeforeModelResolveHook
	BeforeToolCall     []BeforeToolCallHook
	AfterToolCall      []AfterToolCallHook
	ToolResultShape    []ToolResultShapeHook
	LoopDetect         []LoopDetectHook
	BeforeCompaction   []BeforeCompactionHook
	AfterCompaction    []AfterCompactionHook
	AgentEnd           []AgentEndHook
}

type PromptBuildState struct {
	Turn          int
	UserMessage   string
	History       []session.SessionEntry
	StaticPrompt  PromptContext
	DynamicPrompt PromptContext
}

type BeforePromptBuildHook func(context.Context, PromptBuildState) (PromptBuildState, error)

type ModelResolveState struct {
	Turn        int
	UserMessage string
	History     []session.SessionEntry
	Request     llm.ChatRequest
}

type BeforeModelResolveHook func(context.Context, ModelResolveState) (ModelResolveState, error)

type ToolCallState struct {
	Turn int
	Call llm.ToolCall
}

type BeforeToolCallResult struct {
	State         ToolCallState
	BlockedResult *tools.ToolResult
}

type BeforeToolCallHook func(context.Context, BeforeToolCallResult) (BeforeToolCallResult, error)

type AfterToolCallState struct {
	Turn    int
	Call    llm.ToolCall
	Result  tools.ToolResult
	Err     error
	Attempt int
}

type AfterToolCallHook func(context.Context, AfterToolCallState) (AfterToolCallState, error)

type ToolResultShapeState struct {
	Turn        int
	Call        llm.ToolCall
	Result      tools.ToolResult
	Attempt     int
	MaxAttempts int
	RetryCount  int
	Warning     *ToolWarningInfo
}

type ToolResultShapeHook func(context.Context, ToolResultShapeState) (ToolResultShapeState, error)

type LoopDetectState struct {
	Turn      int
	Call      llm.ToolCall
	LoopState *toolLoopState
	Config    ToolLoopDetectionConfig
}

type LoopDetectHook func(context.Context, LoopDetectState) (*ToolWarningInfo, error)

type CompactionState struct {
	History     []session.SessionEntry
	Threshold   int
	KeepEntries int
	Trigger     string
	Summary     string
}

type BeforeCompactionHook func(context.Context, CompactionState) (CompactionState, error)
type AfterCompactionHook func(context.Context, CompactionState) error

type AgentEndState struct {
	FinalEvent AgentEvent
	Session    *session.Session
}

type AgentEndHook func(context.Context, AgentEndState) error

func (r *Runtime) applyBeforePromptBuildHooks(ctx context.Context, state PromptBuildState) (PromptBuildState, error) {
	for _, hook := range r.Hooks.BeforePromptBuild {
		next, err := hook(ctx, state)
		if err != nil {
			return state, err
		}
		state = next
	}
	return state, nil
}

func (r *Runtime) applyBeforeModelResolveHooks(ctx context.Context, state ModelResolveState) (ModelResolveState, error) {
	for _, hook := range r.Hooks.BeforeModelResolve {
		next, err := hook(ctx, state)
		if err != nil {
			return state, err
		}
		state = next
	}
	return state, nil
}

func (r *Runtime) applyBeforeToolCallHooks(ctx context.Context, state ToolCallState) (BeforeToolCallResult, error) {
	result := BeforeToolCallResult{State: state}
	for _, hook := range r.Hooks.BeforeToolCall {
		next, err := hook(ctx, result)
		if err != nil {
			return result, err
		}
		result = next
		if result.BlockedResult != nil {
			return result, nil
		}
	}
	return result, nil
}

func (r *Runtime) applyAfterToolCallHooks(ctx context.Context, state AfterToolCallState) (AfterToolCallState, error) {
	for _, hook := range r.Hooks.AfterToolCall {
		next, err := hook(ctx, state)
		if err != nil {
			return state, err
		}
		state = next
	}
	return state, nil
}

func (r *Runtime) applyToolResultShapeHooks(ctx context.Context, state ToolResultShapeState) (ToolResultShapeState, error) {
	for _, hook := range r.Hooks.ToolResultShape {
		next, err := hook(ctx, state)
		if err != nil {
			return state, err
		}
		state = next
	}
	return state, nil
}

func (r *Runtime) applyLoopDetectHooks(ctx context.Context, state LoopDetectState) (*ToolWarningInfo, error) {
	for _, hook := range r.Hooks.LoopDetect {
		warning, err := hook(ctx, state)
		if err != nil {
			return nil, err
		}
		if warning != nil {
			return warning, nil
		}
	}
	return nil, nil
}

func (r *Runtime) applyBeforeCompactionHooks(ctx context.Context, state CompactionState) (CompactionState, error) {
	for _, hook := range r.Hooks.BeforeCompaction {
		next, err := hook(ctx, state)
		if err != nil {
			return state, err
		}
		state = next
	}
	return state, nil
}

func (r *Runtime) applyAfterCompactionHooks(ctx context.Context, state CompactionState) error {
	for _, hook := range r.Hooks.AfterCompaction {
		if err := hook(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) applyAgentEndHooks(ctx context.Context, state AgentEndState) error {
	for _, hook := range r.Hooks.AgentEnd {
		if err := hook(ctx, state); err != nil {
			return err
		}
	}
	return nil
}
