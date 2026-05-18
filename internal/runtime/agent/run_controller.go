package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/memory"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

type runController struct {
	rt        *Runtime
	ctx       context.Context
	userMsg   string
	events    chan AgentEvent
	maxTurns  int
	toolNames []string
	loopState *toolLoopState

	hadToolActivity bool
	currentTurn     int
	goalID          task.GoalID

	goalCompletionPrompted   bool
	goalCompletionEmptyTurns int
	lastTurnVisibleReply     bool
	awaitingVisibleReply     bool
	pendingRuntimeControlID  string
	backgroundProcesses      *tools.BackgroundProcessManager

	finishOnce sync.Once
}

func newRunController(rt *Runtime, ctx context.Context, userMsg string, events chan AgentEvent) *runController {
	maxTurns := rt.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultRuntimeMaxTurns
	}
	backgroundProcesses := tools.NewBackgroundProcessManager()
	ctx = tools.WithBackgroundProcessManager(ctx, backgroundProcesses)
	return &runController{
		rt:                  rt,
		ctx:                 ctx,
		userMsg:             userMsg,
		events:              events,
		maxTurns:            maxTurns,
		toolNames:           normalizeToolNames(rt.Tools.Names()),
		loopState:           newToolLoopState(),
		backgroundProcesses: backgroundProcesses,
	}
}

func (c *runController) start(images []llm.ImageContent) {
	defer func() {
		if recovered := recover(); recovered != nil {
			c.finish(AgentEvent{
				Type:  EventError,
				Error: fmt.Errorf("agent run panic: %v", recovered),
			})
		}
	}()

	c.emit(AgentEvent{Type: EventRunStarted})
	c.rt.guardSessionToolResults("tool execution was interrupted before the current run started")
	c.appendUserMessage(images)
	c.initializeGoal()
	c.runTurn(0)
}

func (c *runController) appendUserMessage(images []llm.ImageContent) {
	if c == nil || c.rt == nil || c.rt.Session == nil {
		return
	}
	messageID := strings.TrimSpace(tools.RuntimeContextFrom(c.ctx).InputMessageID)
	if len(images) == 0 {
		c.rt.Session.Append(session.UserMessageEntryWithID(messageID, c.userMsg))
		return
	}

	if refs := session.ImageRefsFromBlocks(tools.RuntimeInputBlocksFrom(c.ctx)); len(refs) > 0 {
		c.rt.Session.Append(session.UserMessageWithImagesEntryWithID(messageID, c.userMsg, refs))
		return
	}
	c.rt.Session.AppendWithPersistedEntry(
		session.UserMessageWithImagesEntryWithID(messageID, c.userMsg, session.ImagePayloads(images)),
		session.UserMessageWithImagesEntryWithID(messageID, c.userMsg, session.ImageRefs(images)),
	)
}

func (c *runController) buildChatRequest(
	baseCtx context.Context,
	turn int,
	toolNames []string,
	toolDefs []llm.ToolDef,
) (context.Context, llm.ChatRequest, error) {
	if c == nil || c.rt == nil || c.rt.Session == nil {
		return baseCtx, llm.ChatRequest{}, fmt.Errorf("agent session is not available")
	}

	turnCtx := baseCtx
	turnMeta := tools.RuntimeContextFrom(turnCtx)
	turnCtx = tools.WithRuntimeContext(turnCtx, turnMeta)
	history := c.rt.Session.History()
	if !session.HistoryEndsWithRuntimeControl(history) {
		if err := c.rt.maybeCompactSession(turnCtx); err != nil {
			return turnCtx, llm.ChatRequest{}, err
		}
	}

	normalizedToolNames := normalizeToolNames(toolNames)
	history = c.rt.Session.History()
	focus := deriveRequestFocus(history, c.userMsg)
	promptQuery := derivePromptQueryWithFocus(history, focus, c.userMsg)
	turnMeta.CurrentRequest = strings.TrimSpace(firstNonEmpty(focus.CurrentRequest, c.userMsg))
	turnCtx = tools.WithRuntimeContext(turnCtx, turnMeta)
	dynamicPromptContext := PromptContext{
		Tools: ToolSurface{
			Names: normalizedToolNames,
		},
		Retrieval: RetrievalSurface{
			CurrentQuery:          promptQuery,
			CurrentRequest:        focus.CurrentRequest,
			PendingContextSummary: focus.PendingContextSummary,
			SessionSummary:        focus.SessionSummary,
		},
	}
	if c.rt.GoalRuntime != nil && c.goalID != "" {
		if status, err := c.rt.GoalRuntime.Inspect(c.goalID); err == nil {
			dynamicPromptContext.Goal = goalSurfaceFromStatus(status)
		}
	}

	if c.rt.Skills != nil {
		dynamicPromptContext.Retrieval.MatchedSkills = c.rt.Skills.MatchSkills(promptQuery, 3)
	}
	if c.rt.Memory != nil {
		maxItems := c.rt.MemoryMaxItems
		if maxItems <= 0 {
			maxItems = 3
		}
		memScope := memory.SearchScope{AgentID: c.rt.AgentID}
		if c.rt.Session != nil {
			memScope.SessionID = c.rt.Session.ID
		}
		memMatches := c.rt.Memory.SearchExplainedScoped(promptQuery, maxItems, memScope, memory.LayerEpisodic, memory.LayerLongTerm)
		memEntries := make([]memory.Entry, 0, len(memMatches))
		for _, match := range memMatches {
			memEntries = append(memEntries, match.Entry)
		}
		if len(memMatches) > 0 {
			c.emit(AgentEvent{Type: EventMemoryRecall, Query: promptQuery, MemoryMatches: memMatches})
		}
		dynamicPromptContext.Retrieval.MemoryEntries = memEntries
	}

	systemPrompt, err := c.rt.composeSystemPrompt(turnCtx, turn, c.userMsg, history, normalizedToolNames, dynamicPromptContext)
	if err != nil {
		return turnCtx, llm.ChatRequest{}, err
	}

	history = c.rt.Session.History()
	runtimeDirective := c.takePendingRuntimeControlText(history)
	prepared := newTranscriptBuilder(history, c.rt.transcriptPolicy()).
		WithRuntimeDirective(runtimeDirective).
		Build()
	msgs := prepared.Messages
	if prepared.SummaryContext != "" && dynamicPromptContext.Retrieval.SessionSummary == "" {
		dynamicPromptContext.Retrieval.SessionSummary = prepared.SummaryContext
		systemPrompt, err = c.rt.composeSystemPrompt(turnCtx, turn, c.userMsg, history, normalizedToolNames, dynamicPromptContext)
		if err != nil {
			return turnCtx, llm.ChatRequest{}, err
		}
	}

	pruneToolResults(msgs, maxToolResultLen)

	req := llm.ChatRequest{
		Model:        c.rt.Model,
		Messages:     msgs,
		Tools:        toolDefs,
		MaxTokens:    8192,
		Temperature:  0.01,
		SystemPrompt: systemPrompt,
	}
	reqState, err := c.rt.applyBeforeModelResolveHooks(turnCtx, ModelResolveState{
		Turn:        turn,
		UserMessage: c.userMsg,
		History:     history,
		Request:     req,
	})
	if err != nil {
		return turnCtx, llm.ChatRequest{}, err
	}
	return turnCtx, reqState.Request, nil
}

func (c *runController) runTurn(turn int) {
	if c == nil || c.rt == nil {
		return
	}
	c.currentTurn = turn
	if c.rt.GoalRuntime != nil && c.goalID != "" {
		c.rt.GoalRuntime.RecordTurn(c.goalID, turn+1)
	}
	if turn >= c.maxTurns {
		c.finish(AgentEvent{
			Type:  EventError,
			Error: fmt.Errorf("agent exceeded maximum turns (%d)", c.maxTurns),
		})
		return
	}
	if c.ctx.Err() != nil {
		c.finish(AgentEvent{Type: EventAborted})
		return
	}

	turnCtx, req, err := c.buildChatRequest(c.ctx, turn, c.toolNames, c.rt.Tools.ToolDefs())
	if err != nil {
		c.finish(AgentEvent{Type: EventError, Error: err})
		return
	}

	var textContent strings.Builder
	var toolCalls []llm.ToolCall
	progress := turnProgress{}
	if err := c.rt.collectLLMResponse(turnCtx, c.events, req, &textContent, &toolCalls, &progress); err != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, textContent.String(), progress)
		c.finish(AgentEvent{Type: EventError, Error: err})
		return
	}

	if textContent.Len() > 0 {
		c.rt.Session.Append(session.AssistantMessageEntry(textContent.String()))
		progress.SavedAssistant = true
	}
	c.lastTurnVisibleReply = strings.TrimSpace(textContent.String()) != ""
	if len(toolCalls) == 0 {
		if c.awaitingVisibleReply {
			if !c.lastTurnVisibleReply {
				c.finish(AgentEvent{
					Type:  EventError,
					Error: fmt.Errorf("model ended without a visible reply after tool work"),
				})
				return
			}
			c.awaitingVisibleReply = false
			c.finish(AgentEvent{Type: EventDone})
			return
		}
		if c.shouldRequestVisibleReplyAfterTools(turn) {
			c.awaitingVisibleReply = true
			c.appendRuntimeControl(runtimeControlKindVisibleReply, c.buildVisibleReplyPrompt())
			c.runTurn(turn + 1)
			return
		}
		c.awaitingVisibleReply = false
		c.finish(AgentEvent{Type: EventDone})
		return
	}
	c.awaitingVisibleReply = false
	c.goalCompletionPrompted = false
	c.goalCompletionEmptyTurns = 0
	if c.rt.GoalRuntime != nil && c.goalID != "" {
		c.rt.GoalRuntime.RecordToolCalls(c.goalID, len(toolCalls))
	}

	for _, tc := range toolCalls {
		progress.SawToolCall = true
		meta := toolMetadataForCall(c.rt.Tools, tc.Name)
		if !meta.IsReadOnly() {
			progress.SideEffectRisk = true
		}
		c.rt.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tools.SanitizeToolInputForTranscript(tc.Name, tc.Input)))
	}

	toolWriter := newAgentEventWriter(c.events)
	preparedToolCalls := tools.PrepareToolCalls(c.rt.Tools, toolCalls)
	responseText := textContent.String()
	progressSnapshot := progress
	if err := c.rt.startTaskToolBatch(turnCtx, preparedToolCalls, toolWriter.Emit, c.loopState, turn, func(batchSummary tools.ToolBatchSummary, batchErr error) {
		c.handleToolBatchCompletion(turn, turnCtx, len(toolCalls), responseText, progressSnapshot, toolWriter, batchSummary, batchErr)
	}); err != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, responseText, progressSnapshot)
		c.finish(AgentEvent{Type: EventError, Error: err})
	}
}

func (c *runController) handleToolBatchCompletion(
	turn int,
	turnCtx context.Context,
	_ int,
	responseText string,
	progress turnProgress,
	toolWriter *agentEventWriter,
	batchSummary tools.ToolBatchSummary,
	batchErr error,
) {
	onlyInlineGoalTools := batchContainsOnlyInlineGoalTools(batchSummary)
	if toolWriter != nil && !onlyInlineGoalTools {
		toolWriter.Emit(AgentEvent{
			Type:       EventToolFanoutCompleted,
			ToolFanout: buildToolFanoutInfo(batchSummary),
		})
	}

	fatalBatchErr := firstFatalBatchExecutionError(batchSummary)
	if batchSummary.StartedCount > 0 {
		progress.ExecutedTool = true
		c.hadToolActivity = true
	}
	c.rt.appendToolBatchResults(batchSummary.Calls)

	if batchErr != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, responseText, progress)
		if errors.Is(batchErr, context.Canceled) || errors.Is(batchErr, context.DeadlineExceeded) || turnCtx.Err() != nil {
			c.finish(AgentEvent{Type: EventAborted})
			return
		}
		c.finish(AgentEvent{Type: EventError, Error: batchErr})
		return
	}
	if fatalBatchErr != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, responseText, progress)
		c.finish(AgentEvent{Type: EventError, Error: fatalBatchErr})
		return
	}
	if turnCtx.Err() != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, responseText, progress)
		c.finish(AgentEvent{Type: EventAborted})
		return
	}
	if onlyInlineGoalTools && (c.goalAlreadyCompleted() || c.goalAwaitingUserInput()) {
		if c.goalAlreadyCompleted() && !c.lastTurnVisibleReply {
			if turn+1 >= c.maxTurns {
				c.finish(AgentEvent{
					Type:  EventError,
					Error: fmt.Errorf("goal completed without a visible reply"),
				})
				return
			}
			c.awaitingVisibleReply = true
			c.appendRuntimeControl(runtimeControlKindVisibleReply, c.buildVisibleReplyPrompt())
			c.runTurn(turn + 1)
			return
		}
		if c.goalAwaitingUserInput() && !c.lastTurnVisibleReply {
			c.surfaceAwaitingUserInput()
		}
		c.finish(AgentEvent{Type: EventDone})
		return
	}
	if !onlyInlineGoalTools {
		c.awaitingVisibleReply = false
	}

	c.runTurn(turn + 1)
}

func (c *runController) emit(event AgentEvent) {
	if c == nil || c.events == nil {
		return
	}
	c.events <- event
}

func (c *runController) finish(ev AgentEvent) {
	if c == nil {
		return
	}
	if ev.Type == EventDone {
		continued, err := c.advanceGoalAfterDone()
		if err != nil {
			ev = AgentEvent{Type: EventError, Error: err}
		} else if continued {
			return
		}
	}
	c.finalize(ev)
}

func (c *runController) finalize(ev AgentEvent) {
	if c == nil {
		return
	}
	c.finishOnce.Do(func() {
		if c.backgroundProcesses != nil {
			c.backgroundProcesses.Cleanup()
		}
		if ev.Type == EventError || ev.Type == EventAborted {
			c.rt.guardSessionToolResults(toolGuardReasonForEvent(ev))
		}
		if err := c.rt.applyAgentEndHooks(c.ctx, AgentEndState{
			FinalEvent: ev,
			Session:    c.rt.Session,
		}); err != nil {
			ev = AgentEvent{Type: EventError, Error: fmt.Errorf("agent end hook failed: %w", err)}
			c.rt.guardSessionToolResults(toolGuardReasonForEvent(ev))
		}
		c.emit(ev)
		close(c.events)
	})
}

func (c *runController) initializeGoal() {
	if c == nil || c.rt == nil || c.rt.GoalRuntime == nil || c.goalID != "" {
		return
	}
	meta := tools.RuntimeContextFrom(c.ctx)
	goal := c.rt.GoalRuntime.StartGoal(
		firstNonEmpty(strings.TrimSpace(c.rt.AgentCallContract.TaskGoal), strings.TrimSpace(c.userMsg)),
		firstNonEmpty(meta.SessionID, sessionIDFromRuntime(c.rt)),
		meta.RunID,
	)
	if goal != nil {
		c.goalID = goal.ID
	}
}

func (c *runController) goalAlreadyCompleted() bool {
	if c == nil || c.rt == nil || c.rt.GoalRuntime == nil || c.goalID == "" {
		return false
	}
	current, ok := c.rt.GoalRuntime.GetGoal(c.goalID)
	if !ok || current == nil {
		return false
	}
	return current.State == task.GoalStateCompleted
}

func (c *runController) goalAwaitingUserInput() bool {
	if c == nil || c.rt == nil || c.rt.GoalRuntime == nil || c.goalID == "" {
		return false
	}
	current, ok := c.rt.GoalRuntime.GetGoal(c.goalID)
	if !ok || current == nil {
		return false
	}
	return current.State == task.GoalStateAwaitingInput
}

func (c *runController) surfaceAwaitingUserInput() {
	if c == nil || c.rt == nil || c.rt.GoalRuntime == nil || c.goalID == "" {
		return
	}
	current, ok := c.rt.GoalRuntime.GetGoal(c.goalID)
	if !ok || current == nil || current.State != task.GoalStateAwaitingInput {
		return
	}
	message := strings.TrimSpace(current.AwaitingInputMessage)
	if message == "" {
		message = "I need more information from you before I can continue."
	}
	if c.rt.Session != nil {
		c.rt.Session.Append(session.AssistantMessageEntry(message))
	}
	c.emit(AgentEvent{Type: EventTextDelta, Text: message})
	c.lastTurnVisibleReply = true
}

func batchContainsOnlyInlineGoalTools(summary tools.ToolBatchSummary) bool {
	if len(summary.Calls) == 0 {
		return false
	}
	for _, call := range summary.Calls {
		if !isInlineGoalTool(call.Call.Name) {
			return false
		}
	}
	return true
}

func (c *runController) shouldRequestVisibleReplyAfterTools(turn int) bool {
	if c == nil {
		return false
	}
	if c.lastTurnVisibleReply || !c.hadToolActivity || c.awaitingVisibleReply {
		return false
	}
	if c.rt != nil && c.rt.Tools != nil {
		if _, ok := c.rt.Tools.Get("goal_complete"); ok {
			return false
		}
	}
	if c.goalID != "" && c.rt != nil && c.rt.Tools == nil {
		return false
	}
	return turn+1 < c.maxTurns
}

func (c *runController) buildVisibleReplyPrompt() string {
	return strings.Join([]string{
		"[Runtime visible reply required]",
		"The previous turn completed tool work but produced no visible assistant text for the user.",
		"Write a concise user-facing summary of what happened and the next state now.",
		"Do not call more tools unless they are objectively required to finish the user's request.",
	}, "\n")
}
