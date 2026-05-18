package execution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimefactory "github.com/Isites/anyai/internal/runtime/factory"
	"github.com/Isites/anyai/internal/runtime/input"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/tool"
)

type Coordinator struct {
	mu       sync.Mutex
	sessions map[string]*sessionRunState
}

type sessionRunState struct {
	active           *queuedIngressRun
	pending          []*queuedIngressRun
	droppedSummaries []string
}

type queuedIngressRun struct {
	request   runtimeport.IngressRequest
	agentID   string
	model     string
	sessionID string
	runID     string
	createdAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	out       chan runtimeevents.EventRecord
	managed   *runtimeport.ManagedRun
}

func NewCoordinator() *Coordinator {
	return &Coordinator{
		sessions: make(map[string]*sessionRunState),
	}
}

func (c *Coordinator) Submit(ctx context.Context, deps runtimeport.ExecutionDeps, req runtimeport.IngressRequest) (*runtimeport.ManagedRun, error) {
	agentID := strings.TrimSpace(req.RequestedID)
	if agentID == "" {
		agentID = strings.TrimSpace(ResolveAgent(deps, req).AgentID)
	}
	if agentID == "" {
		channelName := strings.TrimSpace(req.Channel)
		if channelName == "" {
			channelName = "gateway"
		}
		return nil, fmt.Errorf("no agent is configured for %s", channelName)
	}

	resolved, err := runtimefactory.ResolveAgentRuntime(deps, agentID)
	if err != nil {
		return nil, err
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return startManaged(req, ctx, withoutSessionCoordinator(deps), agentID)
	}

	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = tools.NewRunID()
	}

	runCtx, cancel := context.WithCancel(ctx)
	out := make(chan runtimeevents.EventRecord, 128)
	item := &queuedIngressRun{
		request:   req,
		agentID:   agentID,
		model:     resolved.Agent.Model,
		sessionID: sessionID,
		runID:     runID,
		createdAt: time.Now().UTC(),
		ctx:       runCtx,
		cancel:    cancel,
		out:       out,
	}
	item.managed = &runtimeport.ManagedRun{
		RunID:         runID,
		AgentID:       agentID,
		SessionID:     sessionID,
		Model:         resolved.Agent.Model,
		Events:        out,
		Cancel:        cancel,
		OwnsLifecycle: true,
	}

	key := sessionCoordinatorKey(agentID, sessionID)
	mode := sessionQueueMode(deps.Config)

	c.mu.Lock()
	state := c.sessions[key]
	if state == nil {
		state = &sessionRunState{}
		c.sessions[key] = state
	}

	if state.active == nil {
		state.active = item
		c.mu.Unlock()
		c.dispatch(key, item, withoutSessionCoordinator(deps))
		return item.managed, nil
	}

	if deps.Recorder != nil {
		deps.Recorder.StartRun(runtimeevents.RunRecord{
			ID:        item.runID,
			AgentID:   item.agentID,
			SessionID: item.sessionID,
			Model:     item.model,
			Channel:   req.Channel,
			Input:     summarizeEnvelopeForQueue(req.Envelope),
			Status:    runtimeevents.RunStatusQueued,
			CreatedAt: item.createdAt,
		})
		deps.Recorder.AppendEvent(runtimeevents.EventRecord{
			RunID:     item.runID,
			AgentID:   item.agentID,
			SessionID: item.sessionID,
			Name:      runtimeevents.EventRunQueued,
			Payload: map[string]any{
				"mode":          mode,
				"active_run_id": state.active.runID,
			},
		})
	}

	switch mode {
	case "interrupt":
		for _, pending := range state.pending {
			go c.completeQueuedRun(deps, pending, runtimeevents.RunStatusCompleted, "Queued request was superseded by a newer interrupting request.", "")
		}
		state.pending = nil
		state.pending = append(state.pending, item)
		if state.active != nil && state.active.cancel != nil {
			state.active.cancel()
		}
	default:
		dropped := enqueuePendingRun(state, item, deps.Config)
		c.mu.Unlock()
		for _, pending := range dropped {
			c.completeQueuedRun(deps, pending, runtimeevents.RunStatusCompleted, "Queued request was summarized into a later followup run because the session queue hit its limit.", "")
		}
		return item.managed, nil
	}
	c.mu.Unlock()
	return item.managed, nil
}

func (c *Coordinator) dispatch(key string, item *queuedIngressRun, deps runtimeport.ExecutionDeps) {
	go func() {
		run, err := startManaged(reqWithRun(item), item.ctx, deps, item.agentID)
		if err != nil {
			item.out <- queueRunRecord(item, runtimeevents.EventRunFailed, map[string]any{"message": err.Error()})
			close(item.out)
			c.onRunFinished(key, deps, item)
			return
		}

		for event := range run.Events {
			item.out <- event
		}
		close(item.out)
		c.onRunFinished(key, deps, item)
	}()
}

func (c *Coordinator) onRunFinished(key string, deps runtimeport.ExecutionDeps, finished *queuedIngressRun) {
	mode := sessionQueueMode(deps.Config)
	c.mu.Lock()
	state := c.sessions[key]
	if state == nil {
		c.mu.Unlock()
		return
	}
	if state.active != nil && state.active.runID == finished.runID {
		state.active = nil
	}
	if len(state.pending) == 0 {
		if state.active == nil {
			delete(c.sessions, key)
		}
		c.mu.Unlock()
		return
	}

	pending := append([]*queuedIngressRun(nil), state.pending...)
	state.pending = nil
	droppedSummaries := append([]string(nil), state.droppedSummaries...)
	state.droppedSummaries = nil
	c.mu.Unlock()
	pending = filterPendingQueuedRuns(deps, pending)
	if len(pending) == 0 {
		c.mu.Lock()
		state = c.sessions[key]
		if state != nil && state.active == nil && len(state.pending) == 0 {
			delete(c.sessions, key)
		}
		c.mu.Unlock()
		return
	}

	if mode == "collect" {
		// Collect mode is an ingress/session convenience only: it merges queued
		// user-facing turns into one later managed run. It is not part of the
		// runtime's internal doTask orchestration model.
		debounce := sessionQueueDebounce(deps.Config)
		if debounce > 0 {
			time.Sleep(debounce)
		}
	}

	switch mode {
	case "collect":
		owner, merged := selectCollectOwner(pending)
		for _, item := range merged {
			c.completeQueuedRun(deps, item, runtimeevents.RunStatusCompleted, fmt.Sprintf("Queued request was merged into followup run %s.", owner.runID), "")
		}
		req := buildCollectedFollowupRequest(owner, pending, droppedSummaries)
		c.mu.Lock()
		state = c.sessions[key]
		if state == nil {
			state = &sessionRunState{}
			c.sessions[key] = state
		}
		state.active = owner
		c.mu.Unlock()
		owner.request = req
		c.dispatch(key, owner, withoutSessionCoordinator(deps))
	default:
		next := pending[0]
		rest := pending[1:]
		c.mu.Lock()
		state = c.sessions[key]
		if state == nil {
			state = &sessionRunState{}
			c.sessions[key] = state
		}
		state.active = next
		state.pending = append(state.pending, rest...)
		c.mu.Unlock()
		c.dispatch(key, next, withoutSessionCoordinator(deps))
	}
}

func (c *Coordinator) completeQueuedRun(deps runtimeport.ExecutionDeps, item *queuedIngressRun, status runtimeevents.RunStatus, output, errMsg string) {
	if item == nil {
		return
	}
	if strings.TrimSpace(output) != "" {
		item.out <- queueRunRecord(item, runtimeevents.EventTextDelta, map[string]any{"text": output})
	}
	if status == runtimeevents.RunStatusFailed {
		item.out <- queueRunRecord(item, runtimeevents.EventRunFailed, map[string]any{
			"message": strings.TrimSpace(firstNonEmptyQueue(errMsg, output)),
		})
	} else {
		item.out <- queueRunRecord(item, runtimeevents.EventRunCompleted, nil)
	}
	close(item.out)
	if deps.Recorder != nil {
		if _, ok := deps.Recorder.GetRun(item.runID); !ok {
			deps.Recorder.StartRun(runtimeevents.RunRecord{
				ID:        item.runID,
				AgentID:   item.agentID,
				SessionID: item.sessionID,
				Model:     item.model,
				Status:    status,
				CreatedAt: item.createdAt,
			})
		}
		deps.Recorder.FinishRun(item.runID, status, strings.TrimSpace(output), strings.TrimSpace(errMsg))
	}
}

func startManaged(req runtimeport.IngressRequest, ctx context.Context, deps runtimeport.ExecutionDeps, agentID string) (*runtimeport.ManagedRun, error) {
	return StartManagedRun(ctx, deps, runtimeport.RunRequest{
		RunID:         strings.TrimSpace(req.RunID),
		AgentID:       strings.TrimSpace(agentID),
		Envelope:      req.Envelope,
		SessionID:     strings.TrimSpace(req.SessionID),
		MessageID:     strings.TrimSpace(req.MessageID),
		ParentAgentID: strings.TrimSpace(req.ParentAgentID),
		Channel:       strings.TrimSpace(req.Channel),
	})
}

func reqWithRun(item *queuedIngressRun) runtimeport.IngressRequest {
	req := item.request
	req.RunID = item.runID
	req.SessionID = item.sessionID
	req.RequestedID = item.agentID
	return req
}

func selectCollectOwner(items []*queuedIngressRun) (*queuedIngressRun, []*queuedIngressRun) {
	if len(items) == 0 {
		return nil, nil
	}
	owner := items[len(items)-1]
	return owner, items[:len(items)-1]
}

func enqueuePendingRun(state *sessionRunState, item *queuedIngressRun, cfg *config.Config) []*queuedIngressRun {
	state.pending = append(state.pending, item)
	maxPending := 0
	dropPolicy := "summarize"
	if cfg != nil {
		maxPending = cfg.Runtime.Sessions.QueueMaxPending
		dropPolicy = strings.TrimSpace(cfg.Runtime.Sessions.QueueDropPolicy)
	}
	if maxPending <= 0 || len(state.pending) <= maxPending {
		return nil
	}

	var dropped []*queuedIngressRun
	for len(state.pending) > maxPending {
		switch dropPolicy {
		case "drop_newest":
			dropped = append(dropped, state.pending[len(state.pending)-1])
			state.pending = state.pending[:len(state.pending)-1]
		default:
			dropped = append(dropped, state.pending[0])
			if dropPolicy == "summarize" {
				state.droppedSummaries = append(state.droppedSummaries, summarizeEnvelopeForQueue(state.pending[0].request.Envelope))
			}
			state.pending = state.pending[1:]
		}
	}
	return dropped
}

func buildCollectedFollowupRequest(owner *queuedIngressRun, items []*queuedIngressRun, droppedSummaries []string) runtimeport.IngressRequest {
	req := reqWithRun(owner)
	req.Envelope = buildCollectedFollowupEnvelope(owner, items, droppedSummaries)
	return req
}

func buildCollectedFollowupEnvelope(owner *queuedIngressRun, items []*queuedIngressRun, droppedSummaries []string) input.InputEnvelope {
	contextLines := make([]string, 0, len(items)+len(droppedSummaries))
	for _, summary := range droppedSummaries {
		summary = strings.TrimSpace(summary)
		if summary != "" {
			contextLines = append(contextLines, "- "+summary)
		}
	}
	for _, item := range items {
		if item == nil || item.runID == owner.runID {
			continue
		}
		summary := summarizeEnvelopeForQueue(item.request.Envelope)
		if summary != "" {
			contextLines = append(contextLines, "- "+summary)
		}
	}

	current := strings.TrimSpace(owner.request.Envelope.TextContent())
	if current == "" {
		current = summarizeEnvelopeForQueue(owner.request.Envelope)
	}

	var blocks []input.InputBlock
	var body strings.Builder
	if len(contextLines) > 0 {
		body.WriteString("[Earlier pending user turns for context]\n")
		body.WriteString(strings.Join(contextLines, "\n"))
		body.WriteString("\n\n")
	}
	body.WriteString("[Current message - respond to this]\n")
	body.WriteString(strings.TrimSpace(current))
	blocks = append(blocks, input.InputBlock{Type: "text", Text: body.String()})
	for _, block := range owner.request.Envelope.Blocks {
		if block.Type == "text" {
			continue
		}
		blocks = append(blocks, block)
	}
	return input.InputEnvelope{
		SessionID: owner.request.Envelope.SessionID,
		Blocks:    blocks,
	}
}

func summarizeEnvelopeForQueue(env input.InputEnvelope) string {
	parts := input.ResolveEnvelope(env)
	return strings.TrimSpace(strings.Join(parts, " "))
}

func sessionCoordinatorKey(agentID, sessionID string) string {
	return strings.TrimSpace(agentID) + "::" + strings.TrimSpace(sessionID)
}

func queueRunRecord(item *queuedIngressRun, name string, payload map[string]any) runtimeevents.EventRecord {
	if item == nil {
		return runtimeevents.EventRecord{Name: strings.TrimSpace(name), Payload: payload}
	}
	return runtimeevents.EventRecord{
		RunID:     item.runID,
		AgentID:   item.agentID,
		SessionID: item.sessionID,
		Name:      strings.TrimSpace(name),
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	}
}

func sessionQueueMode(cfg *config.Config) string {
	if cfg == nil || strings.TrimSpace(cfg.Runtime.Sessions.QueueMode) == "" {
		return "collect"
	}
	return strings.TrimSpace(cfg.Runtime.Sessions.QueueMode)
}

func sessionQueueDebounce(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Runtime.Sessions.QueueDebounceMS <= 0 {
		return 0
	}
	return time.Duration(cfg.Runtime.Sessions.QueueDebounceMS) * time.Millisecond
}

func firstNonEmptyQueue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func withoutSessionCoordinator(deps runtimeport.ExecutionDeps) runtimeport.ExecutionDeps {
	deps.SessionCoordinator = nil
	return deps
}

func filterPendingQueuedRuns(deps runtimeport.ExecutionDeps, items []*queuedIngressRun) []*queuedIngressRun {
	filtered := make([]*queuedIngressRun, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		if item.ctx != nil && item.ctx.Err() != nil {
			if deps.Recorder != nil {
				deps.Recorder.FinishRun(item.runID, runtimeevents.RunStatusAborted, "", "queued run canceled before dispatch")
			}
			close(item.out)
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}
