package runtime

import (
	"context"
	"fmt"
	"strings"

	runtimeagent "github.com/Isites/anyai/internal/runtime/agent"
	runtimeingress "github.com/Isites/anyai/internal/runtime/execution"
	runtimerun "github.com/Isites/anyai/internal/runtime/execution"
	runtimeFactory "github.com/Isites/anyai/internal/runtime/factory"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

type AgentService struct {
	depsFn func() runtimeport.ExecutionDeps
}

func NewAgentService(depsFn func() runtimeport.ExecutionDeps) *AgentService {
	return &AgentService{depsFn: depsFn}
}

func (s *AgentService) deps() runtimeport.ExecutionDeps {
	if s == nil || s.depsFn == nil {
		return runtimeport.ExecutionDeps{}
	}
	return s.depsFn()
}

func (s *AgentService) Resolve(requestedAgentID string) (runtimeFactory.ResolvedAgentRuntime, error) {
	return runtimeFactory.ResolveAgentRuntime(s.deps(), requestedAgentID)
}

func (s *AgentService) Build(resolved runtimeFactory.ResolvedAgentRuntime, sess *session.Session, extras tools.ExtraToolDeps) (*runtimeagent.Runtime, error) {
	if sess == nil {
		return nil, fmt.Errorf("session is required")
	}
	return runtimeFactory.BuildAgentRuntimeFromDeps(s.deps(), resolved, sess, extras)
}

func (s *AgentService) LoadSession(agentID, sessionID string) (*session.Session, error) {
	deps := s.deps()
	if deps.SessionStore == nil {
		return session.NewSession(agentID, sessionID), nil
	}
	return deps.SessionStore.Load(agentID, sessionID)
}

func (s *AgentService) StartManagedRun(ctx context.Context, req runtimeport.RunRequest) (*runtimeport.ManagedRun, error) {
	return runtimerun.StartManagedRun(ctx, s.deps(), req)
}

func (s *AgentService) StartIngressRun(ctx context.Context, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error) {
	return runtimeingress.StartIngressRun(ctx, s.deps(), req)
}

func (s *AgentService) RunSync(ctx context.Context, agentID, sessionID, prompt string, extras tools.ExtraToolDeps) (string, error) {
	resolved, err := s.Resolve(agentID)
	if err != nil {
		return "", err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "runtime"
	}
	sess := session.NewSession(resolved.Agent.ID, sessionID)
	rt, err := s.Build(resolved, sess, extras)
	if err != nil {
		return "", err
	}
	return rt.RunSync(ctx, prompt, nil)
}

func NormalizeSessionID(sessionID, runID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		return sessionID
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ""
	}
	return "run_" + runID
}
