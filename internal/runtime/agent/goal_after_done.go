package agent

import (
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeplan "github.com/Isites/anyai/internal/runtime/plan"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
)

const maxGoalCompletionEmptyTurns = 1

type goalAfterDoneCoordinator struct {
	controller *runController
}

func newGoalAfterDoneCoordinator(controller *runController) goalAfterDoneCoordinator {
	return goalAfterDoneCoordinator{controller: controller}
}

func (c *runController) advanceGoalAfterDone() (bool, error) {
	return newGoalAfterDoneCoordinator(c).advance()
}

func (g goalAfterDoneCoordinator) advance() (bool, error) {
	c := g.controller
	if c == nil {
		return false, nil
	}
	for autoExecs := 0; autoExecs < maxAutoPlanExecutionsPerIdleCheck; autoExecs++ {
		status, err := g.status()
		if err != nil {
			return false, err
		}
		if status == nil {
			return false, nil
		}
		if status.HardStop {
			g.emitHardStopSummary(*status)
			return false, fmt.Errorf("goal forced to stop: %s", strings.TrimSpace(status.Reason))
		}
		if !status.ShouldContinue {
			if c.goalCompletionPrompted {
				return g.handlePendingGoalCompletion(status)
			}
			if g.shouldPromptForGoalCompletion(status) {
				if c.currentTurn+1 >= c.maxTurns {
					return false, fmt.Errorf("goal completion requires explicit goal_complete but maximum turns (%d) was reached", c.maxTurns)
				}
				prompt := g.buildGoalCompletionPrompt(*status)
				c.appendRuntimeControl(runtimeControlKindGoalCompletion, prompt)
				c.goalCompletionPrompted = true
				c.goalCompletionEmptyTurns = 0
				c.runTurn(c.currentTurn + 1)
				return true, nil
			}
			if _, err := c.rt.GoalRuntime.MaybeContinueIfIdle(c.ctx, c.goalID); err != nil {
				return false, err
			}
			c.goalCompletionPrompted = false
			return false, nil
		}
		c.goalCompletionPrompted = false
		c.goalCompletionEmptyTurns = 0
		if execResult, executed, err := c.maybeAutoExecuteReadyPlanStep(*status); err != nil {
			return false, err
		} else if executed {
			if execResult != nil && execResult.Status == "paused" {
				return false, nil
			}
			continue
		}
		if c.currentTurn+1 >= c.maxTurns {
			return false, fmt.Errorf("goal still has unfinished work but maximum turns (%d) was reached: %s", c.maxTurns, strings.TrimSpace(status.Reason))
		}

		prompt := g.buildGoalContinuationPrompt(*status)
		if strings.TrimSpace(prompt) == "" {
			return false, nil
		}
		c.appendRuntimeControl(runtimeControlKindGoalContinuation, prompt)
		c.runTurn(c.currentTurn + 1)
		return true, nil
	}
	return false, fmt.Errorf("runtime auto-executed too many structured plan steps without settling the goal state")
}

func (g goalAfterDoneCoordinator) handlePendingGoalCompletion(status *task.ShouldContinueResult) (bool, error) {
	c := g.controller
	if c == nil {
		return false, nil
	}
	if status == nil {
		return false, nil
	}
	if status.State != task.GoalStateInProgress || status.ShouldContinue || status.HardStop {
		c.goalCompletionPrompted = false
		c.goalCompletionEmptyTurns = 0
		return false, nil
	}
	if c.lastTurnVisibleReply {
		c.goalCompletionPrompted = false
		c.goalCompletionEmptyTurns = 0
		return false, fmt.Errorf("goal completion requires explicit goal_complete or await_user_input after runtime completion prompt")
	}
	c.goalCompletionEmptyTurns++
	if c.goalCompletionEmptyTurns > maxGoalCompletionEmptyTurns {
		c.goalCompletionPrompted = false
		c.goalCompletionEmptyTurns = 0
		return false, fmt.Errorf("model returned empty responses after runtime requested explicit goal_complete")
	}
	if c.currentTurn+1 >= c.maxTurns {
		c.goalCompletionPrompted = false
		return false, fmt.Errorf("goal completion requires explicit goal_complete but maximum turns (%d) was reached", c.maxTurns)
	}
	prompt := g.buildGoalCompletionRetryPrompt(*status, c.goalCompletionEmptyTurns+1)
	c.appendRuntimeControl(runtimeControlKindGoalCompletionRetry, prompt)
	c.runTurn(c.currentTurn + 1)
	return true, nil
}

func (g goalAfterDoneCoordinator) status() (*task.ShouldContinueResult, error) {
	c := g.controller
	if c == nil || c.rt == nil || c.rt.GoalRuntime == nil || c.goalID == "" {
		return nil, nil
	}
	return c.rt.GoalRuntime.Inspect(c.goalID)
}

func (g goalAfterDoneCoordinator) shouldPromptForGoalCompletion(status *task.ShouldContinueResult) bool {
	c := g.controller
	if c == nil || c.rt == nil || c.rt.Tools == nil || status == nil {
		return false
	}
	if c.goalCompletionPrompted || status.State != task.GoalStateInProgress || status.ShouldContinue || status.HardStop {
		return false
	}
	if _, ok := c.rt.Tools.Get("goal_complete"); !ok {
		return false
	}
	if c.lastTurnVisibleReply {
		return false
	}
	return c.hadToolActivity || status.ToolCalls > 0 || status.CurrentPlan != nil
}

func (g goalAfterDoneCoordinator) buildGoalCompletionPrompt(status task.ShouldContinueResult) string {
	lines := []string{
		"[Runtime goal continuation]",
		"Runtime currently sees no unfinished objective work for this goal, but do not end the run silently yet.",
		"Call `goal_complete` now instead of ending silently.",
	}
	if reason := strings.TrimSpace(status.Reason); reason != "" {
		lines = append(lines, "", "Runtime assessment: "+reason)
	}
	if status.CurrentPlan != nil {
		if rendered := strings.TrimSpace(runtimeplan.Render(*status.CurrentPlan)); rendered != "" {
			lines = append(lines, "", "Latest recorded plan:")
			lines = append(lines, rendered)
		}
	}
	lines = append(lines, "", "If anything is still unfinished or blocked, you must:")
	lines = append(lines, "1. State what is still unfinished or blocked in your visible reply")
	lines = append(lines, "2. Continue the work instead of calling `goal_complete`")
	if _, ok := g.controller.rt.Tools.Get("await_user_input"); ok {
		lines = append(lines, "If you still need the user to decide something before this can truly finish, call `await_user_input` and ask that question clearly.")
	}
	return strings.Join(lines, "\n")
}

func (g goalAfterDoneCoordinator) buildGoalCompletionRetryPrompt(status task.ShouldContinueResult, attempt int) string {
	lines := []string{
		"[Runtime goal continuation]",
		"The previous response was empty, but runtime completion now requires an explicit decision.",
		"Call `goal_complete` now if the goal is truly complete.",
	}
	if _, ok := g.controller.rt.Tools.Get("await_user_input"); ok {
		lines = append(lines, "If the goal is blocked on missing user input, call `await_user_input` and ask that question clearly.")
	}
	lines = append(lines, "If objective work is still unfinished, you must:")
	lines = append(lines, "1. State what is still unfinished in your visible reply")
	lines = append(lines, "2. Continue the work immediately")
	if reason := strings.TrimSpace(status.Reason); reason != "" {
		lines = append(lines, "", "Runtime assessment: "+reason)
	}
	if attempt > 0 {
		lines = append(lines, "", fmt.Sprintf("Completion prompt attempt: %d", attempt))
	}
	return strings.Join(lines, "\n")
}

func (g goalAfterDoneCoordinator) emitHardStopSummary(status task.ShouldContinueResult) {
	c := g.controller
	if c == nil || c.rt == nil {
		return
	}
	summary, _ := g.runModelOnlyTurn(c.currentTurn+1, g.buildHardStopSummaryPrompt(status))
	if strings.TrimSpace(summary) != "" {
		return
	}
	fallback := g.buildHardStopFallbackSummary(status)
	if strings.TrimSpace(fallback) == "" {
		return
	}
	if c.rt.Session != nil {
		c.rt.Session.Append(session.AssistantMessageEntry(fallback))
	}
	c.emit(AgentEvent{Type: EventTextDelta, Text: fallback})
}

func (g goalAfterDoneCoordinator) buildHardStopSummaryPrompt(status task.ShouldContinueResult) string {
	lines := []string{
		"[Runtime hard stop]",
		"A hard runtime limit was reached while objective work still remains, so this run must stop now.",
		"Do not call tools. Do not claim the goal is finished.",
		"Write one concise handoff for the user that includes:",
		"- what you completed in this run",
		"- what is still unfinished or blocked",
		"- the next recommended step or plan",
		"- any user input or action needed before continuing",
		"Use the user's language when it is clear from the conversation.",
	}
	if reason := strings.TrimSpace(status.Reason); reason != "" {
		lines = append(lines, "", "Hard-stop reason: "+reason)
	}
	return strings.Join(lines, "\n")
}

func (g goalAfterDoneCoordinator) buildHardStopFallbackSummary(status task.ShouldContinueResult) string {
	lines := []string{
		"The run stopped because a hard runtime limit was reached before the goal was finished.",
	}
	if reason := strings.TrimSpace(status.Reason); reason != "" {
		lines = append(lines, "Reason: "+reason)
	}
	if remaining := goalStatusRemainingItems(status); len(remaining) > 0 {
		lines = append(lines, "", "Remaining work:")
		for _, item := range remaining {
			lines = append(lines, "- "+item)
		}
	}
	if next := goalStatusNextStep(status); next != "" {
		lines = append(lines, "", "Next step:", "- "+next)
	}
	return strings.Join(lines, "\n")
}

func (g goalAfterDoneCoordinator) runModelOnlyTurn(turn int, prompt string) (string, error) {
	c := g.controller
	if c == nil || c.rt == nil {
		return "", nil
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", nil
	}
	c.appendRuntimeControl(runtimeControlKindHardStop, prompt)
	turnCtx, req, err := c.buildChatRequest(c.ctx, turn, nil, nil)
	if err != nil {
		return "", err
	}
	req.Tools = nil
	req.MaxTokens = 2048

	var textContent strings.Builder
	var toolCalls []llm.ToolCall
	progress := turnProgress{}
	err = c.rt.collectLLMResponse(turnCtx, c.events, req, &textContent, &toolCalls, &progress)
	if textContent.Len() > 0 && c.rt.Session != nil {
		c.rt.Session.Append(session.AssistantMessageEntry(textContent.String()))
		progress.SavedAssistant = true
	}
	if err != nil {
		c.rt.maybeSurfaceIncompleteTurn(c.events, textContent.String(), progress)
	}
	return strings.TrimSpace(textContent.String()), err
}

func (g goalAfterDoneCoordinator) buildGoalContinuationPrompt(status task.ShouldContinueResult) string {
	lines := []string{
		"[Runtime goal continuation]",
		"Runtime detected that the current goal still has unfinished objective work. Continue the task instead of ending now.",
	}
	if reason := strings.TrimSpace(status.Reason); reason != "" {
		lines = append(lines, "", "Reason: "+reason)
	}
	if len(status.PendingTaskIDs) > 0 {
		lines = append(lines, "", "Pending runtime tasks:")
		for _, taskID := range status.PendingTaskIDs {
			lines = append(lines, "- "+taskID)
		}
	}
	if len(status.PendingToolCalls) > 0 {
		lines = append(lines, "", "Pending tool calls:")
		for _, toolCallID := range status.PendingToolCalls {
			lines = append(lines, "- "+toolCallID)
		}
	}
	if len(status.OpenTodos) > 0 {
		lines = append(lines, "", "Open todo items:")
		for _, item := range status.OpenTodos {
			content := strings.TrimSpace(item.Content)
			if content == "" {
				content = item.ID
			}
			lines = append(lines, "- "+content+" ("+item.ID+")")
		}
	}
	if len(status.IncompleteCheckpoints) > 0 {
		lines = append(lines, "", "Incomplete checkpoints:")
		for _, checkpoint := range status.IncompleteCheckpoints {
			label := strings.TrimSpace(checkpoint.Description)
			if label == "" {
				label = checkpoint.ID
			}
			lines = append(lines, "- "+label)
		}
	}
	if status.ReadyPlanStep != nil {
		label := strings.TrimSpace(status.ReadyPlanStep.Description)
		if label == "" {
			label = status.ReadyPlanStep.ID
		}
		lines = append(lines, "", "Next ready plan step:", "- "+label)
	}
	if len(status.BlockedPlanSteps) > 0 {
		lines = append(lines, "", "Blocked plan steps:")
		for _, item := range status.BlockedPlanSteps {
			lines = append(lines, "- "+item)
		}
	}
	if status.CurrentPlan != nil {
		if rendered := strings.TrimSpace(runtimeplan.Render(*status.CurrentPlan)); rendered != "" {
			lines = append(lines, "", "Latest recorded plan:")
			lines = append(lines, rendered)
		}
	}
	lines = append(lines, "", "If any todo item is already done, update its status before ending. Otherwise continue the work and only stop once runtime accepts completion.")
	return strings.Join(lines, "\n")
}

func goalStatusRemainingItems(status task.ShouldContinueResult) []string {
	items := make([]string, 0, 8)
	for _, taskID := range status.PendingTaskIDs {
		if item := strings.TrimSpace(taskID); item != "" {
			items = append(items, "pending runtime task "+item)
		}
	}
	for _, toolCallID := range status.PendingToolCalls {
		if item := strings.TrimSpace(toolCallID); item != "" {
			items = append(items, "pending tool call "+item)
		}
	}
	for _, todo := range status.OpenTodos {
		label := strings.TrimSpace(todo.Content)
		if label == "" {
			label = strings.TrimSpace(todo.ID)
		}
		if label != "" {
			items = append(items, "open todo "+label)
		}
	}
	for _, checkpoint := range status.IncompleteCheckpoints {
		label := strings.TrimSpace(checkpoint.Description)
		if label == "" {
			label = strings.TrimSpace(checkpoint.ID)
		}
		if label != "" {
			items = append(items, "incomplete checkpoint "+label)
		}
	}
	for _, step := range status.BlockedPlanSteps {
		if item := strings.TrimSpace(step); item != "" {
			items = append(items, "blocked plan step "+item)
		}
	}
	return items
}

func goalStatusNextStep(status task.ShouldContinueResult) string {
	if status.ReadyPlanStep != nil {
		label := strings.TrimSpace(status.ReadyPlanStep.Description)
		if label == "" {
			label = strings.TrimSpace(status.ReadyPlanStep.ID)
		}
		if label != "" {
			return label
		}
	}
	for _, todo := range status.OpenTodos {
		label := strings.TrimSpace(todo.Content)
		if label == "" {
			label = strings.TrimSpace(todo.ID)
		}
		if label != "" {
			return label
		}
	}
	if len(status.PendingTaskIDs) > 0 {
		return "resume the pending runtime task work"
	}
	if len(status.PendingToolCalls) > 0 {
		return "wait for or repair the unresolved tool call state before continuing"
	}
	return "start a new run and continue from the remaining work listed above"
}
