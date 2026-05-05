package httpchannel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/gateway"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/tool"
)

type runStartRequest struct {
	AgentID       string
	Text          string
	Inputs        []input.InputBlock
	SessionID     string
	Channel       string
	SenderID      string
	AccountID     string
	ChatType      gateway.ChatType
	SessionPrefix string
}

type acceptedRun struct {
	Run    *runtimeport.ManagedRun
	Record runtimeevents.RunRecord
	Inputs []input.InputBlock
}

func (p *ControlPlane) resolveIngressAgent(channelName, requestedAgentID, senderID, accountID string, chatType gateway.ChatType) string {
	if p == nil || p.ingress == nil {
		return strings.TrimSpace(requestedAgentID)
	}
	return p.ingress.ResolveIngressAgent(runtimeport.IngressRequest{
		Channel:     channelName,
		RequestedID: requestedAgentID,
		SenderID:    senderID,
		AccountID:   accountID,
		ChatType:    runtimeChatType(chatType),
	})
}

func (p *ControlPlane) startRun(ctx context.Context, req runStartRequest) (*acceptedRun, error) {
	if p == nil || p.ingress == nil {
		return nil, fmt.Errorf("runtime not available")
	}

	inputs, err := input.NormalizeBlocks(req.Text, req.Inputs)
	if err != nil {
		return nil, err
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = input.DefaultSessionID(req.SessionPrefix, time.Now().UTC())
	}

	runCtx := tools.WithRuntimeContext(ctx, tools.RuntimeContextFrom(ctx))
	envelope := input.InputEnvelope{
		SessionID: sessionID,
		Blocks:    inputs,
	}
	run, err := p.startIngressManagedRun(runCtx, runtimeport.IngressRequest{
		Channel:     req.Channel,
		RequestedID: req.AgentID,
		SenderID:    req.SenderID,
		AccountID:   firstNonEmpty(strings.TrimSpace(req.AccountID), strings.TrimSpace(req.SenderID)),
		ChatType:    runtimeChatType(req.ChatType),
		Envelope:    envelope,
		SessionID:   sessionID,
	})
	if err != nil {
		return nil, err
	}

	return &acceptedRun{
		Run:    run,
		Record: p.acceptedRunRecord(run, inputs),
		Inputs: inputs,
	}, nil
}

func runtimeChatType(chatType gateway.ChatType) runtimeport.ChatType {
	switch chatType {
	case gateway.ChatTypeGroup:
		return runtimeport.ChatTypeGroup
	case gateway.ChatTypeDirect:
		fallthrough
	default:
		return runtimeport.ChatTypeDirect
	}
}

func (p *ControlPlane) acceptedRunRecord(run *runtimeport.ManagedRun, inputs []input.InputBlock) runtimeevents.RunRecord {
	if run == nil {
		return runtimeevents.RunRecord{}
	}
	if p != nil && p.run != nil {
		if stored, ok := p.run.GetRun(run.RunID); ok {
			return stored
		}
	}
	return runtimeevents.RunRecord{
		ID:          run.RunID,
		AgentID:     run.AgentID,
		SessionID:   run.SessionID,
		Model:       run.Model,
		Status:      runtimeevents.RunStatusRunning,
		Input:       summarizeInputsForRecord(inputs),
		StartedAt:   time.Now().UTC(),
		CompletedAt: time.Time{},
	}
}

func (p *ControlPlane) listSessions(agentID string) ([]session.SessionInfo, error) {
	if p == nil || p.session == nil {
		return nil, fmt.Errorf("runtime not available")
	}
	return p.session.ListSessions(agentID)
}

func (p *ControlPlane) createSession(agentID, requestedSessionID, prefix string) (string, error) {
	if p == nil || p.session == nil {
		return "", fmt.Errorf("runtime not available")
	}
	return p.session.CreateSession(agentID, requestedSessionID, prefix)
}

func (p *ControlPlane) loadSession(agentID, sessionID string) (*session.Session, error) {
	if p == nil || p.session == nil {
		return nil, fmt.Errorf("runtime not available")
	}
	return p.session.LoadSession(agentID, sessionID)
}

func (p *ControlPlane) deleteSession(agentID, sessionID string) error {
	if p == nil || p.session == nil {
		return fmt.Errorf("runtime not available")
	}
	return p.session.DeleteSession(agentID, sessionID)
}

func (p *ControlPlane) startIngressManagedRun(ctx context.Context, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error) {
	if p == nil || p.ingress == nil {
		return nil, fmt.Errorf("runtime not available")
	}
	return p.ingress.StartIngressRun(ctx, req)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
