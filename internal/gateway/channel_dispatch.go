package gateway

import (
	"context"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"strings"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
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
		runtimelogging.Error("channel runtime is not configured", "channel", ch.Name())
		return
	}

	policy := d.runtime.EvaluateMessagePolicy(ch.Name(), d.dmPolicy, msg)
	if !policy.Accepted {
		if policy.Reason == "unknown_direct_sender_notify" {
			runtimelogging.Info("message from unknown sender (dm policy: notify)",
				"channel", ch.Name(), "sender", msg.SenderID)
		} else {
			runtimelogging.Debug("message from unknown sender ignored (dm policy: ignore)",
				"channel", ch.Name(), "sender", msg.SenderID)
		}
		return
	}

	ingressReq := ingressRequestForInboundMessage(ch.Name(), msg)
	resolvedAgentID := d.runtime.ResolveIngressAgent(ingressReq)

	runtimelogging.Info("processing message",
		"channel", ch.Name(),
		"sender", msg.SenderID,
		"agent", resolvedAgentID,
	)

	run, err := d.runtime.StartIngressRun(ctx, ingressReq)
	if err != nil {
		runtimelogging.Error("agent run error", "error", err, "channel", ch.Name(), "sender", msg.SenderID)
		if sendErr := ch.Send(ctx, OutboundMessage{
			ChatID: responseChatID(msg),
			Text:   "Sorry, I encountered an error. Please try again.",
		}); sendErr != nil {
			runtimelogging.Error("send error response", "error", sendErr)
		}
		return
	}

	treeDone := d.forwardRunTree(ctx, ch, run)

	var response channelResponseBuffer
	for event := range run.Events {
		if eventAware, ok := ch.(RunEventAware); ok {
			if err := eventAware.HandleRunEvent(ctx, d.channelRunEventFromRecord(event)); err != nil {
				runtimelogging.Warn("channel rejected run event", "channel", ch.Name(), "event", event.Name, "error", err)
			}
		}

		response.Observe(event)
		if message := runtimeevents.FailureMessage(event); message != "" {
			runtimelogging.Error("agent run failed", "message", message, "agent", run.AgentID)
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

	if err := ch.Send(ctx, OutboundMessage{
		ChatID: responseChatID(msg),
		Text:   finalResponse,
	}); err != nil {
		runtimelogging.Error("send response", "error", err, "channel", ch.Name())
	}
}

type channelResponseBuffer struct {
	text strings.Builder
}

// channelResponseBuffer mirrors run-level replay buffering for channel.Send:
// progress text before context-producing tools is not a final chat reply.
func (b *channelResponseBuffer) Observe(event runtimeevents.EventRecord) {
	if text := runtimeevents.TextDelta(event); text != "" {
		b.text.WriteString(text)
		return
	}
	switch strings.TrimSpace(event.Name) {
	case runtimeevents.EventToolRetrying,
		runtimeevents.EventToolWarning,
		runtimeevents.EventToolCallRequested,
		runtimeevents.EventToolCallStarted,
		runtimeevents.EventToolCompleted,
		runtimeevents.EventToolFailed:
		if shouldResetChannelResponseForTool(event) {
			b.text.Reset()
		}
	case runtimeevents.EventAgentCallStarted,
		runtimeevents.EventAgentCallSubmitted,
		runtimeevents.EventAgentCallCompleted,
		runtimeevents.EventAgentCallFailed:
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

func shouldResetChannelResponseForTool(event runtimeevents.EventRecord) bool {
	switch strings.TrimSpace(runtimeevents.ToolName(event)) {
	case "goal_complete", "await_user_input", "save_output":
		return false
	default:
		return true
	}
}

func (d *dispatcher) forwardRunTree(ctx context.Context, ch Channel, run *runtimeport.ManagedRun) chan struct{} {
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

func (d *dispatcher) forwardRunTreeRecord(ctx context.Context, ch Channel, eventAware RunEventAware, entryRunID, entryAgentID string, record runtimeevents.EventRecord) bool {
	if record.RunID == entryRunID && record.AgentID == entryAgentID {
		return true
	}
	if err := eventAware.HandleRunEvent(ctx, d.channelRunEventFromRecord(record)); err != nil {
		runtimelogging.Warn("channel rejected run tree event", "channel", ch.Name(), "event", record.Name, "error", err)
	}
	return true
}

func (d *dispatcher) channelRunEventFromRecord(record runtimeevents.EventRecord) RunEvent {
	out := RunEvent{
		RunID:     record.RunID,
		AgentID:   record.AgentID,
		SessionID: record.SessionID,
		Name:      record.Name,
		Timestamp: record.Timestamp,
		Payload:   record.Payload,
	}
	if d == nil || d.runtime == nil {
		return out
	}
	if run, ok := d.runtime.GetRun(record.RunID); ok {
		out.ParentAgentID = run.ParentAgentID
	}
	return out
}
