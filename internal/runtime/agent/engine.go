package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	runtimeactivity "github.com/Isites/anyai/internal/runtime/activity"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"github.com/Isites/anyai/internal/runtime/memory"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/skill"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

// Runtime is the agent think-act loop.
type Runtime struct {
	LLM                       llm.LLMProvider
	Tools                     tools.Executor
	Session                   *session.Session
	AgentID                   string // agent identifier (e.g. "default", "coder")
	AgentName                 string // human-readable name (e.g. "Assistant", "Coder")
	Model                     string
	Workspace                 string
	ProjectRoot               string
	AgentDefinitionDir        string
	AgentsRootDir             string
	SharedSkillsDir           string
	MaxTurns                  int    // safety limit for tool-use loops
	SystemPrompt              string // optional: agent-specific instructions appended to the runtime prompt
	ConfigPath                string
	DataDir                   string
	MemoryDir                 string
	MemoryMaxItems            int
	TopologySummary           string
	RunMode                   string
	ParentAgentID             string
	AgentCallContract         tools.AgentCallContract
	LLMMaxAttempts            int
	LLMRetryBackoffMS         int
	ToolRecovery              ToolRecoveryConfig
	TranscriptHygiene         TranscriptHygieneConfig
	ToolPreflight             ToolPreflightConfig
	IncompleteTurn            IncompleteTurnConfig
	Skills                    *skill.Loader   // optional: skill loader for selective injection
	Memory                    *memory.Manager // optional: memory manager for context retrieval
	EventAppender             func(runtimeevents.EventRecord)
	TaskRuntime               *task.Runtime
	GoalRuntime               *task.GoalRuntime
	SessionCompactThreshold   int
	SessionCompactKeepEntries int
	Compaction                CompactionConfig
	Hooks                     RuntimeHooks
}

const (
	defaultRuntimeMaxTurns           = 10000
	defaultSessionCompactThreshold   = 64
	defaultSessionCompactKeepEntries = 24
	defaultLLMMaxAttempts            = 5
	defaultLLMRetryBackoffMS         = 1000
	maxLLMRetryBackoffMS             = 8000
)

type CompactionConfig struct {
	Enabled              bool
	TriggerMode          string
	EntryThreshold       int
	TokenThreshold       int
	KeepRecentUserTurns  int
	KeepRecentUserTokens int
	SummaryMaxTokens     int
}

type TranscriptHygieneConfig struct {
	Enabled                   bool
	MergeConsecutiveUserTurns bool
	RepairToolPairs           bool
	DropOrphanToolResults     bool
	TreatMetaAsSummaryContext bool
}

type ToolPreflightConfig struct {
	Enabled bool
}

type IncompleteTurnConfig struct {
	Enabled bool
}

type turnProgress struct {
	StreamedText   bool
	SawToolCall    bool
	ExecutedTool   bool
	SavedAssistant bool
	SideEffectRisk bool
}

// Run executes the agent loop for a user message, returning a channel of events.
// images is an optional slice of image attachments to include with the user message.
func (r *Runtime) Run(ctx context.Context, userMsg string, images []llm.ImageContent) (<-chan AgentEvent, error) {
	ctx = r.normalizeRunContext(ctx, userMsg)
	events := make(chan AgentEvent, 100)
	r.ensureTaskRuntime()
	r.ensureGoalRuntime()
	controller := newRunController(r, ctx, userMsg, events)
	go controller.start(images)

	return events, nil
}

func (r *Runtime) normalizeRunContext(ctx context.Context, userMsg string) context.Context {
	meta := tools.RuntimeContextFrom(ctx)
	if strings.TrimSpace(meta.RunID) == "" {
		meta.RunID = tools.NewRunID()
	}
	if strings.TrimSpace(meta.AgentID) == "" {
		meta.AgentID = strings.TrimSpace(r.AgentID)
	}
	if strings.TrimSpace(meta.SessionID) == "" && r != nil && r.Session != nil {
		meta.SessionID = strings.TrimSpace(r.Session.ID)
	}
	if strings.TrimSpace(meta.AssetsBaseDir) == "" && r != nil && strings.TrimSpace(r.ProjectRoot) != "" {
		meta.AssetsBaseDir = filepath.Join(strings.TrimSpace(r.ProjectRoot), "anyai", "assets")
	}
	if strings.TrimSpace(meta.CurrentRequest) == "" {
		meta.CurrentRequest = strings.TrimSpace(userMsg)
	}
	if len(meta.CallChain) == 0 && strings.TrimSpace(meta.AgentID) != "" {
		meta.CallChain = []string{strings.TrimSpace(meta.AgentID)}
	}
	return tools.WithRuntimeContext(ctx, meta)
}

func (r *Runtime) collectLLMResponse(
	ctx context.Context,
	events chan<- AgentEvent,
	req llm.ChatRequest,
	textContent *strings.Builder,
	toolCalls *[]llm.ToolCall,
	progress *turnProgress,
) error {
	maxAttempts := r.llmMaxAttempts()
	backoff := r.llmRetryBackoff()
	var lastErr error
	var lastStage = "request"
	attemptsUsed := 0

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptsUsed = attempt
		stream, err := r.LLM.ChatStream(ctx, req)
		if err != nil {
			lastErr = fmt.Errorf("llm error: %w", err)
			lastStage = "request"
			if attempt >= maxAttempts {
				break
			}
			if err := r.emitRetryAndWait(ctx, events, attempt, maxAttempts, backoff, lastStage, lastErr); err != nil {
				return err
			}
			continue
		}

		textContent.Reset()
		*toolCalls = nil
		sawMeaningfulOutput := false
		var streamErr error

		for event := range stream {
			switch event.Type {
			case llm.EventActivity:
				runtimeactivity.Emit(ctx, nil)
				events <- AgentEvent{Type: EventActivity}

			case llm.EventTextDelta:
				runtimeactivity.Emit(ctx, nil)
				sawMeaningfulOutput = true
				textContent.WriteString(event.Text)
				if progress != nil {
					progress.StreamedText = true
				}
				events <- AgentEvent{Type: EventTextDelta, Text: event.Text}

			case llm.EventToolCallStart:
				runtimeactivity.Emit(ctx, nil)
				sawMeaningfulOutput = true
				if progress != nil {
					progress.SawToolCall = true
				}
				if event.ToolCall == nil || !isInlineGoalTool(event.ToolCall.Name) {
					events <- AgentEvent{Type: EventToolCallRequested, ToolCall: event.ToolCall}
				}

			case llm.EventToolCallDone:
				runtimeactivity.Emit(ctx, nil)
				sawMeaningfulOutput = true
				if event.ToolCall != nil {
					*toolCalls = append(*toolCalls, *event.ToolCall)
				}

			case llm.EventError:
				streamErr = event.Error
			}
		}

		if streamErr == nil {
			if sawMeaningfulOutput {
				return nil
			}
			// An empty turn is not a transport failure. Let the controller decide
			// whether runtime facts require another turn or whether the run can
			// settle cleanly without forcing ambiguous fallback text.
			return nil
		}

		lastErr = streamErr
		lastStage = "stream"
		if sawMeaningfulOutput || attempt >= maxAttempts {
			break
		}
		if err := r.emitRetryAndWait(ctx, events, attempt, maxAttempts, backoff, lastStage, lastErr); err != nil {
			return err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("llm error: call failed without details")
	}
	if attemptsUsed > 1 {
		return fmt.Errorf("%w (after %d/%d attempts, stage=%s)", lastErr, attemptsUsed, maxAttempts, lastStage)
	}
	return lastErr
}

func (r *Runtime) emitRetryAndWait(
	ctx context.Context,
	events chan<- AgentEvent,
	attempt, maxAttempts int,
	baseBackoff time.Duration,
	stage string,
	err error,
) error {
	wait := retryDelay(baseBackoff, attempt)
	runtimelogging.Warn("llm attempt failed; scheduling retry",
		"agent", r.AgentID,
		"attempt", attempt,
		"next_attempt", attempt+1,
		"max_attempts", maxAttempts,
		"wait_ms", int(wait/time.Millisecond),
		"stage", stage,
		"error", err,
	)
	events <- AgentEvent{
		Type: EventLLMRetry,
		LLMRetry: &LLMRetryInfo{
			Attempt:     attempt + 1,
			MaxAttempts: maxAttempts,
			WaitMS:      int(wait / time.Millisecond),
			Stage:       stage,
			Error:       err.Error(),
		},
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Runtime) llmMaxAttempts() int {
	if r.LLMMaxAttempts > 0 {
		return r.LLMMaxAttempts
	}
	return defaultLLMMaxAttempts
}

func (r *Runtime) llmRetryBackoff() time.Duration {
	backoffMS := r.LLMRetryBackoffMS
	if backoffMS <= 0 {
		backoffMS = defaultLLMRetryBackoffMS
	}
	return time.Duration(backoffMS) * time.Millisecond
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = time.Duration(defaultLLMRetryBackoffMS) * time.Millisecond
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxLLMRetryBackoffMS*time.Millisecond {
			return maxLLMRetryBackoffMS * time.Millisecond
		}
	}
	return delay
}

func (r *Runtime) maybeCompactSession(ctx context.Context) error {
	return r.runModelCompactionIfNeeded(ctx)
}

// RunSync is a convenience method that runs the agent and collects the full text response.
func (r *Runtime) RunSync(ctx context.Context, userMsg string, images []llm.ImageContent) (string, error) {
	events, err := r.Run(ctx, userMsg, images)
	if err != nil {
		return "", err
	}

	var response strings.Builder
	for event := range events {
		switch event.Type {
		case EventTextDelta:
			response.WriteString(event.Text)
		case EventError:
			return response.String(), event.Error
		}
	}

	return response.String(), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
