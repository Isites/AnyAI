package gateway

import (
	"context"
	"strings"
	"time"
)

type dispatcher struct {
	runtime  ChannelPort
	dmPolicy string
}

func newDispatcher(runtime ChannelPort, dmPolicy string) *dispatcher {
	return &dispatcher{
		runtime:  runtime,
		dmPolicy: dmPolicy,
	}
}

func (d *dispatcher) handle(ctx context.Context, ch Channel, msg InboundMessage) {
	if d == nil || d.runtime == nil {
		Error("channel runtime is not configured", "channel", ch.Name())
		return
	}

	policy := d.runtime.EvaluateMessagePolicy(ch.Name(), d.dmPolicy, msg)
	if !policy.Accepted {
		if policy.Reason == "unknown_direct_sender_notify" {
			Info("message from unknown sender (dm policy: notify)",
				"channel", ch.Name(), "sender", msg.SenderID)
		} else {
			Debug("message from unknown sender ignored (dm policy: ignore)",
				"channel", ch.Name(), "sender", msg.SenderID)
		}
		return
	}

	ingressReq := ingressRequestForInboundMessage(ch.Name(), msg)
	resolvedAgentID := d.runtime.ResolveIngressAgent(ingressReq)

	Info("processing message",
		"channel", ch.Name(),
		"sender", msg.SenderID,
		"agent", resolvedAgentID,
	)

	run, err := d.runtime.StartIngressRun(ctx, ingressReq)
	if err != nil {
		Error("agent run error", "error", err, "channel", ch.Name(), "sender", msg.SenderID)
		if sendErr := ch.Send(ctx, OutboundMessage{
			ChatID:    responseChatID(msg),
			Text:      "Sorry, I encountered an error. Please try again.",
			AgentID:   resolvedAgentID,
			SessionID: ingressReq.SessionID,
		}); sendErr != nil {
			Error("send error response", "error", sendErr)
		}
		return
	}

	treeDone := d.forwardRunTree(ctx, ch, run)

	var response channelResponseBuffer
	for event := range run.Events {
		if eventAware, ok := ch.(RunEventAware); ok {
			if err := eventAware.HandleRunEvent(ctx, d.channelRunEventFromRecord(event)); err != nil {
				Warn("channel rejected run event", "channel", ch.Name(), "event", event.Name, "error", err)
			}
		}

		response.Observe(event)
		if message := failureMessage(event); message != "" {
			Error("agent run failed", "message", message, "agent", run.AgentID)
			response.SetFallbackIfEmpty("Sorry, I encountered an error processing your request.")
		}
	}

	if treeDone != nil {
		select {
		case <-treeDone:
		case <-time.After(250 * time.Millisecond):
		}
	}

	finalResponse := response.Final()
	if finalResponse == "" {
		return
	}
	if finalAware, ok := ch.(FinalResponseAware); ok && !finalAware.WantsFinalResponseSend() {
		return
	}

	if err := ch.Send(ctx, OutboundMessage{
		ChatID:    responseChatID(msg),
		Text:      finalResponse,
		AgentID:   run.AgentID,
		SessionID: run.SessionID,
		RunID:     run.RunID,
	}); err != nil {
		Error("send response", "error", err, "channel", ch.Name())
	}
}

type channelResponseBuffer struct {
	text strings.Builder
}

// channelResponseBuffer mirrors run-level replay buffering for channel.Send:
// progress text before context-producing tools is not a final chat reply.
func (b *channelResponseBuffer) Observe(event Event) {
	if text := textDelta(event); text != "" {
		b.text.WriteString(text)
		return
	}
	switch strings.TrimSpace(event.Name) {
	case EventToolRetrying,
		EventToolWarning,
		EventToolCallRequested,
		EventToolCallStarted,
		EventToolCompleted,
		EventToolFailed:
		if shouldResetChannelResponseForTool(event) {
			b.text.Reset()
		}
	case EventAgentCallStarted,
		EventAgentCallSubmitted,
		EventAgentCallCompleted,
		EventAgentCallFailed:
		b.text.Reset()
	}
}

func (b *channelResponseBuffer) SetFallbackIfEmpty(text string) {
	if strings.TrimSpace(b.text.String()) != "" {
		return
	}
	b.text.WriteString(text)
}

func (b *channelResponseBuffer) Final() string {
	return strings.TrimSpace(b.text.String())
}

func shouldResetChannelResponseForTool(event Event) bool {
	switch strings.TrimSpace(toolName(event)) {
	case "goal_complete", "await_user_input":
		return false
	default:
		return true
	}
}

func (d *dispatcher) forwardRunTree(ctx context.Context, ch Channel, run *ManagedRun) chan struct{} {
	eventAware, ok := ch.(RunEventAware)
	if !ok || d == nil || d.runtime == nil || run == nil || strings.TrimSpace(run.RunID) == "" {
		return nil
	}

	snapshot, treeEvents, cancelTree, err := d.runtime.SubscribeRunTreeReplay(run.RunID)
	if err != nil || cancelTree == nil {
		return nil
	}

	done := make(chan struct{})
	go func(entryRunID, entryAgentID string) {
		defer close(done)
		defer cancelTree()

		for _, record := range snapshot {
			if !d.forwardRunTreeRecord(ctx, ch, eventAware, entryRunID, entryAgentID, record) {
				return
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case record, ok := <-treeEvents:
				if !ok {
					return
				}
				if !d.forwardRunTreeRecord(ctx, ch, eventAware, entryRunID, entryAgentID, record) {
					return
				}
			}
		}
	}(run.RunID, run.AgentID)

	return done
}

func (d *dispatcher) forwardRunTreeRecord(ctx context.Context, ch Channel, eventAware RunEventAware, entryRunID, entryAgentID string, record Event) bool {
	if record.RunID == entryRunID && record.AgentID == entryAgentID {
		return true
	}
	if err := eventAware.HandleRunEvent(ctx, d.channelRunEventFromRecord(record)); err != nil {
		Warn("channel rejected run tree event", "channel", ch.Name(), "event", record.Name, "error", err)
	}
	return true
}

func (d *dispatcher) channelRunEventFromRecord(record Event) RunEvent {
	out := RunEvent{
		RunID:           record.RunID,
		RunNodeID:       record.RunNodeID,
		ParentRunNodeID: record.ParentRunNodeID,
		AgentID:         record.AgentID,
		SessionID:       record.SessionID,
		Name:            record.Name,
		Timestamp:       record.Timestamp,
		Payload:         record.Payload,
	}
	if d == nil || d.runtime == nil {
		return out
	}
	if run, ok := d.runtime.GetRun(record.RunID); ok {
		out.ParentAgentID = run.ParentAgentID
		applyChannelRunEventNodeDefaults(&out, run.RunNodeID, run.ParentRunNodeID, run.AgentID)
	}
	if out.RunNodeID == "" {
		out.RunNodeID = runNodeID(out.RunID, out.AgentID, eventTaskID(out.Payload))
	}
	return out
}

func applyChannelRunEventNodeDefaults(event *RunEvent, runRunNodeID, runParentRunNodeID, runAgentID string) {
	if event == nil {
		return
	}
	agentID := strings.TrimSpace(event.AgentID)
	runAgentID = strings.TrimSpace(runAgentID)
	runRunNodeID = strings.TrimSpace(runRunNodeID)
	runParentRunNodeID = strings.TrimSpace(runParentRunNodeID)
	if runRunNodeID == "" && runAgentID != "" {
		runRunNodeID = runNodeID(event.RunID, runAgentID, "")
	}
	if event.RunNodeID == "" {
		if agentID == "" || runAgentID == "" || agentID == runAgentID {
			event.RunNodeID = runRunNodeID
		} else {
			event.RunNodeID = runNodeID(event.RunID, agentID, eventTaskID(event.Payload))
			if event.ParentRunNodeID == "" {
				event.ParentRunNodeID = runRunNodeID
			}
		}
	}
	if event.ParentRunNodeID == "" {
		if strings.TrimSpace(event.RunNodeID) == runRunNodeID {
			event.ParentRunNodeID = runParentRunNodeID
		} else if runRunNodeID != "" {
			event.ParentRunNodeID = runRunNodeID
		}
	}
}

func textDelta(event Event) string {
	if event.Payload == nil {
		return ""
	}
	if value, ok := event.Payload["text"].(string); ok {
		return value
	}
	return ""
}

func failureMessage(event Event) string {
	switch strings.TrimSpace(event.Name) {
	case EventRunFailed:
		if event.Payload != nil {
			if msg, ok := event.Payload["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return strings.TrimSpace(msg)
			}
			if errMsg, ok := event.Payload["error"].(string); ok && strings.TrimSpace(errMsg) != "" {
				return strings.TrimSpace(errMsg)
			}
		}
	case EventToolFailed:
		if event.Payload != nil {
			if errMsg, ok := event.Payload["error"].(string); ok && strings.TrimSpace(errMsg) != "" {
				return strings.TrimSpace(errMsg)
			}
		}
	}
	return ""
}

func toolName(event Event) string {
	if event.Payload == nil {
		return ""
	}
	for _, key := range []string{"tool", "name", "tool_name"} {
		if value, ok := event.Payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func eventTaskID(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if value, ok := payload["task_id"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func runNodeID(runID, agentID, taskID string) string {
	runID = strings.TrimSpace(runID)
	agentID = strings.TrimSpace(agentID)
	taskID = strings.TrimSpace(taskID)
	if runID == "" {
		return ""
	}
	if taskID == "" {
		return runID + "::" + agentID
	}
	return runID + "::" + agentID + "::" + taskID
}
