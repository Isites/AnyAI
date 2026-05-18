package agent

import (
	"strings"

	"github.com/Isites/anyai/internal/runtime/session"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

const (
	runtimeControlKindGoalContinuation    = "goal_continuation"
	runtimeControlKindGoalCompletion      = "goal_completion"
	runtimeControlKindGoalCompletionRetry = "goal_completion_retry"
	runtimeControlKindHardStop            = "hard_stop"
	runtimeControlKindVisibleReply        = "visible_reply"
)

func (c *runController) appendRuntimeControl(kind, prompt string) {
	if c == nil || c.rt == nil || c.rt.Session == nil {
		return
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	kind = strings.TrimSpace(kind)
	entry := session.RuntimeControlEntry(kind, prompt)
	refs := runtimeControlEntryRefs(c)
	entry = session.ApplyEntryRefs(entry, refs)
	c.rt.Session.Append(entry)
	history := c.rt.Session.History()
	if len(history) > 0 {
		c.pendingRuntimeControlID = strings.TrimSpace(history[len(history)-1].ID)
	}
}

func runtimeControlEntryRefs(c *runController) session.EntryRefs {
	if c == nil {
		return session.EntryRefs{}
	}
	meta := tools.RuntimeContextFrom(c.ctx)
	runID := firstNonEmpty(strings.TrimSpace(meta.RunID), string(c.goalID))
	if strings.TrimSpace(runID) == "" && c.rt != nil && c.rt.Session != nil {
		runID = c.rt.Session.ActiveRefs().RunID
	}
	return session.EntryRefs{
		RunID:  strings.TrimSpace(runID),
		TaskID: strings.TrimSpace(meta.TaskID),
	}
}

func (c *runController) takePendingRuntimeControlText(history []session.SessionEntry) string {
	if c == nil || strings.TrimSpace(c.pendingRuntimeControlID) == "" {
		return ""
	}
	text, ok := runtimeControlTextByID(history, c.pendingRuntimeControlID)
	c.pendingRuntimeControlID = ""
	if !ok || strings.TrimSpace(text) == "" {
		return ""
	}
	return strings.TrimSpace(text)
}

func runtimeControlTextByID(history []session.SessionEntry, id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}
	for i := len(history) - 1; i >= 0; i-- {
		entry := history[i]
		if strings.TrimSpace(entry.ID) != id {
			continue
		}
		return session.RuntimeControlText(entry)
	}
	return "", false
}
