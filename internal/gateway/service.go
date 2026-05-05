package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
)

// ChannelPort is the narrow gateway surface consumed by message channel
// adapters. Channel code should talk to gateway, not reach into runtime
// directly.
type ChannelPort interface {
	channelPort()
	EvaluateMessagePolicy(channelName, dmPolicy string, msg InboundMessage) MessagePolicyDecision
	ResolveIngressAgent(req runtimeport.IngressRequest) string
	StartIngressRun(ctx context.Context, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error)
	SubscribeRunTreeReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error)
	GetRun(runID string) (runtimeevents.RunRecord, bool)
}

// IngressFacade is the gateway surface for accepting new external work.
type IngressFacade interface {
	ResolveIngressAgent(req runtimeport.IngressRequest) string
	StartIngressRun(ctx context.Context, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error)
}

// ObserveFacade is the gateway surface for run read models and replay streams.
type ObserveFacade interface {
	GetRun(runID string) (runtimeevents.RunRecord, bool)
	ListRuns() []runtimeevents.RunRecord
	ListRunEvents(runID string) []runtimeevents.EventRecord
	GetRunTree(runID string) (runtimeevents.RunTreeRecord, bool)
	RunTree(runID string) ([]runtimeevents.RunNode, bool)
	SubscribeRunReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error)
	SubscribeRunTreeReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error)
}

// ControlFacade is the gateway surface for runtime mutations and cancellation.
type ControlFacade interface {
	CancelRun(runID string) error
	CancelTask(taskID string) error
	CreateSession(agentID, requestedKey, prefix string) (string, error)
	DeleteSession(agentID, sessionID string) error
	RebuildEventProjections() error
}

// Service is the isolated gateway layer that exposes runtime abilities,
// observability, and control to transport-facing packages.
type Service struct {
	runtime        runtimeport.GatewayRuntime
	routeResolver  RouteResolver
	channelManager *ChannelManager
	logBuffer      runtimelogging.Stream
	version        string
	onConfigSave   func(*config.Config)
}

// New creates a new gateway service over the authoritative runtime.
func New(runtime runtimeport.GatewayRuntime) *Service {
	return &Service{runtime: runtime}
}

// RawRuntime returns the wrapped low-level runtime surface used beneath
// gateway replay/read-model adaptation.
func (s *Service) RawRuntime() runtimeport.GatewayRuntime {
	if s == nil {
		return nil
	}
	return s.runtime
}

// SetVersion stores the user-facing gateway version string.
func (s *Service) SetVersion(version string) {
	if s == nil {
		return
	}
	s.version = strings.TrimSpace(version)
}

// Version returns the user-facing gateway version string.
func (s *Service) Version() string {
	if s == nil {
		return ""
	}
	return s.version
}

// SetLogBuffer wires the log stream exposed by the gateway.
func (s *Service) SetLogBuffer(buffer runtimelogging.Stream) {
	if s == nil {
		return
	}
	s.logBuffer = buffer
}

// SetConfigSaveHook registers the callback used after config persistence.
func (s *Service) SetConfigSaveHook(fn func(*config.Config)) {
	if s == nil {
		return
	}
	s.onConfigSave = fn
}

// SetChannelManager wires the shared channel supervisor owned by gateway.
func (s *Service) SetChannelManager(manager *ChannelManager) {
	if s == nil {
		return
	}
	s.channelManager = manager
}

// SetRouteResolver wires the gateway-owned routing policy.
func (s *Service) SetRouteResolver(resolver RouteResolver) {
	if s == nil {
		return
	}
	s.routeResolver = resolver
}

// ConfigSnapshot returns the active runtime configuration snapshot.
func (s *Service) ConfigSnapshot() *config.Config {
	if s == nil || s.runtime == nil {
		return nil
	}
	return s.runtime.Config()
}

// SaveConfig validates and persists a new config snapshot through the gateway
// control surface.
func (s *Service) SaveConfig(raw []byte) error {
	current := s.ConfigSnapshot()
	if current == nil {
		return fmt.Errorf("config not available")
	}

	var next config.Config
	if err := json.Unmarshal(raw, &next); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := next.Validate(); err != nil {
		return fmt.Errorf("validation: %w", err)
	}
	next.SetPath(current.Path())
	if err := next.Save(); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	if s.onConfigSave != nil {
		s.onConfigSave(&next)
	}
	return nil
}

// Channels returns the current gateway-visible channel inventory.
func (s *Service) Channels() []ChannelInfo {
	if s == nil {
		return nil
	}
	if s.channelManager != nil {
		return s.channelManager.Channels()
	}

	cfg := s.ConfigSnapshot()
	if cfg == nil {
		return nil
	}
	return defaultChannelInventory(cfg.ActiveChannels)
}

// LogEntriesPayload returns a JSON-ready slice of structured log entries.
func (s *Service) LogEntriesPayload(limit int) []map[string]any {
	if s == nil || s.logBuffer == nil {
		return nil
	}
	entries := s.logBuffer.Snapshot()
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		result = append(result, map[string]any{
			"time":    entry.Time.UTC().Format(time.RFC3339Nano),
			"level":   entry.Level.String(),
			"message": entry.Message,
			"attrs":   entry.Attrs,
		})
	}
	return result
}

// SubscribeLogs subscribes to the gateway log stream.
func (s *Service) SubscribeLogs() (<-chan runtimelogging.LogEntry, func()) {
	if s == nil || s.logBuffer == nil {
		return nil, nil
	}
	ch := s.logBuffer.Subscribe()
	return ch, func() { s.logBuffer.Unsubscribe(ch) }
}

// EvaluateMessagePolicy asks runtime ingress policy whether the message should
// be accepted.
func (s *Service) EvaluateMessagePolicy(channelName, dmPolicy string, msg InboundMessage) MessagePolicyDecision {
	if s == nil {
		return MessagePolicyDecision{}
	}
	if strings.TrimSpace(channelName) == "cli" {
		return MessagePolicyDecision{Accepted: true}
	}
	if msg.ChatType != ChatTypeDirect {
		return MessagePolicyDecision{Accepted: true}
	}
	if strings.EqualFold(strings.TrimSpace(dmPolicy), "respond") {
		return MessagePolicyDecision{Accepted: true}
	}
	if s.routeResolver != nil && s.routeResolver.IsKnownPeer(strings.TrimSpace(msg.SenderID)) {
		return MessagePolicyDecision{Accepted: true}
	}
	if strings.EqualFold(strings.TrimSpace(dmPolicy), "notify") {
		return MessagePolicyDecision{Accepted: false, Reason: "unknown_direct_sender_notify"}
	}
	return MessagePolicyDecision{Accepted: false, Reason: "unknown_direct_sender_ignored"}
}

// ResolveIngressAgent resolves the target agent using explicit request,
// gateway routing rules, and finally runtime config fallback ordering.
func (s *Service) ResolveIngressAgent(req runtimeport.IngressRequest) string {
	if s == nil {
		return strings.TrimSpace(req.RequestedID)
	}
	requestedID := strings.TrimSpace(req.RequestedID)
	if requestedID != "" {
		return requestedID
	}
	if s.routeResolver != nil {
		agentID := strings.TrimSpace(s.routeResolver.Resolve(
			req.Channel,
			req.SenderID,
			firstNonEmpty(req.AccountID, req.SenderID),
			string(req.ChatType),
		))
		if agentID != "" {
			return agentID
		}
	}
	cfg := s.ConfigSnapshot()
	if cfg != nil && len(cfg.Agents.List) > 0 {
		return strings.TrimSpace(cfg.Agents.List[0].ID)
	}
	return ""
}

// StartIngressRun submits an ingress request into runtime.
func (s *Service) StartIngressRun(ctx context.Context, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error) {
	if s == nil || s.runtime == nil {
		return nil, fmt.Errorf("runtime not available")
	}
	return s.runtime.StartIngressRun(ctx, req)
}

// GetRun returns a gateway-visible run record.
func (s *Service) GetRun(runID string) (runtimeevents.RunRecord, bool) {
	if s == nil || s.runtime == nil {
		return runtimeevents.RunRecord{}, false
	}
	return s.runtime.GetRun(runID)
}

func (*Service) channelPort() {}

var _ ChannelPort = (*Service)(nil)
var _ IngressFacade = (*Service)(nil)
var _ ObserveFacade = (*Service)(nil)
var _ ControlFacade = (*Service)(nil)
