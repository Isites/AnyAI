package httpchannel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/gateway"
)

type runStartRequest struct {
	AgentID       string
	Text          string
	Inputs        []gateway.InputBlock
	SessionID     string
	MessageID     string
	Channel       string
	SenderID      string
	AccountID     string
	ChatType      gateway.ChatType
	SessionPrefix string
}

type acceptedRun struct {
	Run    *gateway.ManagedRun
	Record gateway.Run
	Inputs []gateway.InputBlock
}

func (p *ControlPlane) resolveIngressAgent(channelName, requestedAgentID, senderID, accountID string, chatType gateway.ChatType) string {
	if p == nil || p.ingress == nil {
		return strings.TrimSpace(requestedAgentID)
	}
	return p.ingress.ResolveIngressAgent(gateway.IngressRequest{
		Channel:     channelName,
		RequestedID: requestedAgentID,
		SenderID:    senderID,
		AccountID:   accountID,
		ChatType:    chatType,
	})
}

func (p *ControlPlane) startRun(ctx context.Context, req runStartRequest) (*acceptedRun, error) {
	if p == nil || p.ingress == nil {
		return nil, fmt.Errorf("runtime not available")
	}

	run, err := p.startIngressManagedRun(ctx, gateway.IngressRequest{
		Channel:       req.Channel,
		RequestedID:   req.AgentID,
		SenderID:      req.SenderID,
		AccountID:     firstNonEmpty(strings.TrimSpace(req.AccountID), strings.TrimSpace(req.SenderID)),
		ChatType:      req.ChatType,
		Text:          req.Text,
		Inputs:        req.Inputs,
		SessionID:     strings.TrimSpace(req.SessionID),
		MessageID:     strings.TrimSpace(req.MessageID),
		SessionPrefix: req.SessionPrefix,
	})
	if err != nil {
		return nil, err
	}

	return &acceptedRun{
		Run:    run,
		Record: p.acceptedRunRecord(run, req.Inputs),
		Inputs: req.Inputs,
	}, nil
}

func (p *ControlPlane) acceptedRunRecord(run *gateway.ManagedRun, inputs []gateway.InputBlock) gateway.Run {
	if run == nil {
		return gateway.Run{}
	}
	if p != nil && p.run != nil {
		if stored, ok := p.run.GetRun(run.RunID); ok {
			return stored
		}
	}
	return gateway.Run{
		ID:          run.RunID,
		AgentID:     run.AgentID,
		SessionID:   run.SessionID,
		Model:       run.Model,
		Status:      gateway.RunStatusRunning,
		Input:       summarizeInputsForRecord(inputs),
		StartedAt:   time.Now().UTC(),
		CompletedAt: time.Time{},
	}
}

func (p *ControlPlane) listSessions(agentID string) ([]gateway.SessionInfo, error) {
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

func (p *ControlPlane) loadSession(agentID, sessionID string) (gateway.SessionView, error) {
	if p == nil || p.session == nil {
		return gateway.SessionView{}, fmt.Errorf("runtime not available")
	}
	return p.session.LoadSession(agentID, sessionID)
}

func (p *ControlPlane) deleteSession(agentID, sessionID string) error {
	if p == nil || p.session == nil {
		return fmt.Errorf("runtime not available")
	}
	return p.session.DeleteSession(agentID, sessionID)
}

func (p *ControlPlane) startIngressManagedRun(ctx context.Context, req gateway.IngressRequest) (*gateway.ManagedRun, error) {
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
