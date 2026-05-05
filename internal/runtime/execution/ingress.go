package execution

import (
	"context"
	"fmt"
	"strings"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/tool"
)

type Decision struct {
	AgentID string
	Source  string
}

func ResolveAgent(deps runtimeport.ExecutionDeps, req runtimeport.IngressRequest) Decision {
	requestedID := strings.TrimSpace(req.RequestedID)
	if requestedID != "" {
		return Decision{
			AgentID: requestedID,
			Source:  "requested",
		}
	}
	if deps.IngressResolver != nil {
		if agentID := strings.TrimSpace(deps.IngressResolver(req)); agentID != "" {
			return Decision{
				AgentID: agentID,
				Source:  "resolver",
			}
		}
	}
	if deps.Config != nil && len(deps.Config.Agents.List) > 0 {
		return Decision{
			AgentID: strings.TrimSpace(deps.Config.Agents.List[0].ID),
			Source:  "default",
		}
	}
	return Decision{}
}

func StartIngressRun(ctx context.Context, deps runtimeport.ExecutionDeps, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error) {
	if strings.TrimSpace(req.RunID) == "" {
		req.RunID = tools.NewRunID()
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = strings.TrimSpace(req.Envelope.SessionID)
	}
	if strings.TrimSpace(req.SessionID) != "" && strings.TrimSpace(req.Envelope.SessionID) == "" {
		req.Envelope.SessionID = strings.TrimSpace(req.SessionID)
	}

	decision := ResolveAgent(deps, req)
	if strings.TrimSpace(decision.AgentID) == "" {
		err := noAgentError(req)
		recordRouteRejected(deps.Recorder, req, decision, err.Error())
		return nil, err
	}

	recordRouteDecision(deps.Recorder, req, decision)
	recordInputLifecycle(deps.Recorder, req, decision.AgentID)
	req.RequestedID = decision.AgentID
	if deps.SessionCoordinator != nil {
		return deps.SessionCoordinator.Submit(ctx, deps, req)
	}
	return startDirect(ctx, deps, req)
}

func startDirect(ctx context.Context, deps runtimeport.ExecutionDeps, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error) {
	return StartManagedRun(ctx, deps, runtimeport.RunRequest{
		RunID:         req.RunID,
		AgentID:       strings.TrimSpace(req.RequestedID),
		Envelope:      req.Envelope,
		SessionID:     req.SessionID,
		ParentAgentID: req.ParentAgentID,
		Channel:       req.Channel,
	})
}

func noAgentError(req runtimeport.IngressRequest) error {
	channelName := strings.TrimSpace(req.Channel)
	if channelName == "" {
		channelName = "gateway"
	}
	return fmt.Errorf("no agent is configured for %s", channelName)
}

func recordRouteDecision(recorder *runtimeevents.Recorder, req runtimeport.IngressRequest, decision Decision) {
	if recorder == nil {
		return
	}
	runRecord := runtimeevents.RunRecord{
		ID:        strings.TrimSpace(req.RunID),
		AgentID:   strings.TrimSpace(decision.AgentID),
		SessionID: strings.TrimSpace(req.SessionID),
		Channel:   strings.TrimSpace(req.Channel),
		Input:     summarizeEnvelope(req.Envelope),
		Status:    runtimeevents.RunStatusQueued,
	}
	recorder.StartRun(runRecord)
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     runRecord.ID,
		AgentID:   runRecord.AgentID,
		SessionID: runRecord.SessionID,
		Name:      runtimeevents.EventRunAccepted,
		Payload: map[string]any{
			"channel":   strings.TrimSpace(req.Channel),
			"sender_id": strings.TrimSpace(req.SenderID),
			"chat_type": string(req.ChatType),
		},
	})
	payload := map[string]any{
		"requested_agent_id": strings.TrimSpace(req.RequestedID),
		"resolved_agent_id":  strings.TrimSpace(decision.AgentID),
		"source":             strings.TrimSpace(decision.Source),
		"channel":            strings.TrimSpace(req.Channel),
		"sender_id":          strings.TrimSpace(req.SenderID),
		"account_id":         strings.TrimSpace(req.AccountID),
		"chat_type":          string(req.ChatType),
	}
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     runRecord.ID,
		AgentID:   runRecord.AgentID,
		SessionID: runRecord.SessionID,
		Name:      runtimeevents.EventRunRouted,
		Payload:   payload,
	})
}

func recordRouteRejected(recorder *runtimeevents.Recorder, req runtimeport.IngressRequest, decision Decision, errMsg string) {
	if recorder == nil {
		return
	}
	runRecord := runtimeevents.RunRecord{
		ID:        strings.TrimSpace(req.RunID),
		AgentID:   strings.TrimSpace(decision.AgentID),
		SessionID: strings.TrimSpace(req.SessionID),
		Channel:   strings.TrimSpace(req.Channel),
		Input:     summarizeEnvelope(req.Envelope),
		Status:    runtimeevents.RunStatusFailed,
	}
	recorder.StartRun(runRecord)
	recorder.AppendEvent(runtimeevents.EventRecord{
		RunID:     runRecord.ID,
		AgentID:   runRecord.AgentID,
		SessionID: runRecord.SessionID,
		Name:      runtimeevents.EventRunRouteRejected,
		Payload: map[string]any{
			"requested_agent_id": strings.TrimSpace(req.RequestedID),
			"channel":            strings.TrimSpace(req.Channel),
			"sender_id":          strings.TrimSpace(req.SenderID),
			"account_id":         strings.TrimSpace(req.AccountID),
			"chat_type":          string(req.ChatType),
			"error":              strings.TrimSpace(errMsg),
		},
	})
	recorder.FinishRun(runRecord.ID, runtimeevents.RunStatusFailed, "", strings.TrimSpace(errMsg))
}

func recordInputLifecycle(recorder *runtimeevents.Recorder, req runtimeport.IngressRequest, agentID string) {
	if recorder == nil {
		return
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		return
	}

	base := runtimeevents.EventRecord{
		RunID:     runID,
		AgentID:   strings.TrimSpace(agentID),
		SessionID: strings.TrimSpace(req.SessionID),
	}
	attachments := attachmentPayloads(req.Envelope)
	recorder.AppendEvent(withInputPayload(base, runtimeevents.EventInputReceived, req, attachments))
	for _, payload := range attachments {
		recorder.AppendEvent(runtimeevents.EventRecord{
			RunID:     base.RunID,
			AgentID:   base.AgentID,
			SessionID: base.SessionID,
			Name:      runtimeevents.EventAttachmentStored,
			Payload:   payload,
		})
	}
	recorder.AppendEvent(withInputPayload(base, runtimeevents.EventInputNormalized, req, attachments))
}

func withInputPayload(base runtimeevents.EventRecord, name string, req runtimeport.IngressRequest, attachments []map[string]any) runtimeevents.EventRecord {
	base.Name = name
	base.Payload = map[string]any{
		"channel":          strings.TrimSpace(req.Channel),
		"sender_id":        strings.TrimSpace(req.SenderID),
		"account_id":       strings.TrimSpace(req.AccountID),
		"chat_type":        string(req.ChatType),
		"block_count":      len(req.Envelope.Blocks),
		"attachment_count": len(attachments),
		"blocks":           blockPayloads(req.Envelope),
	}
	return base
}

func blockPayloads(env input.InputEnvelope) []map[string]any {
	if len(env.Blocks) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(env.Blocks))
	for i, block := range env.Blocks {
		payload := map[string]any{
			"index": i,
			"id":    blockID(block, i),
			"type":  strings.TrimSpace(block.Type),
		}
		if name := strings.TrimSpace(block.Name); name != "" {
			payload["name"] = name
		}
		if mimeType := strings.TrimSpace(block.MimeType); mimeType != "" {
			payload["mime_type"] = mimeType
		}
		if path := strings.TrimSpace(block.Path); path != "" {
			payload["path"] = path
		}
		if url := strings.TrimSpace(block.URL); url != "" {
			payload["url"] = url
		}
		out = append(out, payload)
	}
	return out
}

func attachmentPayloads(env input.InputEnvelope) []map[string]any {
	if len(env.Blocks) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(env.Blocks))
	for i, block := range env.Blocks {
		switch strings.TrimSpace(block.Type) {
		case "file", "image", "pdf":
		default:
			continue
		}
		payload := map[string]any{
			"attachment_id": blockID(block, i),
			"index":         i,
			"type":          strings.TrimSpace(block.Type),
			"storage":       "inline_manifest",
		}
		if name := strings.TrimSpace(block.Name); name != "" {
			payload["name"] = name
		}
		if mimeType := strings.TrimSpace(block.MimeType); mimeType != "" {
			payload["mime_type"] = mimeType
		}
		if path := strings.TrimSpace(block.Path); path != "" {
			payload["path"] = path
		}
		if url := strings.TrimSpace(block.URL); url != "" {
			payload["url"] = url
		}
		out = append(out, payload)
	}
	return out
}

func blockID(block input.InputBlock, index int) string {
	if id := strings.TrimSpace(block.ID); id != "" {
		return id
	}
	return fmt.Sprintf("input_%d", index+1)
}

func summarizeEnvelope(env input.InputEnvelope) string {
	parts := input.ResolveEnvelope(env)
	return strings.TrimSpace(strings.Join(parts, " "))
}
