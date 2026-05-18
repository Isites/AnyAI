package execution

import (
	"context"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/runtime/agent"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimefactory "github.com/Isites/anyai/internal/runtime/factory"
	"github.com/Isites/anyai/internal/runtime/input"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	runtimesessionstate "github.com/Isites/anyai/internal/runtime/session/state"
	"github.com/Isites/anyai/internal/runtime/task"
	runtimetaskbuiltin "github.com/Isites/anyai/internal/runtime/task/builtin"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	"github.com/Isites/anyai/internal/runtime/turn"
)

func StartManagedRun(ctx context.Context, deps runtimeport.ExecutionDeps, req runtimeport.RunRequest) (*runtimeport.ManagedRun, error) {
	deps = ensureManagedRunDeps(deps)
	resolved, err := runtimefactory.ResolveAgentRuntime(deps, req.AgentID)
	if err != nil {
		return nil, err
	}

	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = tools.NewRunID()
	}
	prepared, _, err := input.PrepareEnvelope(req.Envelope, input.PrepareOptions{
		RunID:   runID,
		BaseDir: input.ProjectAssetsDir(deps.Config),
	})
	if err != nil {
		return nil, err
	}
	req.Envelope = prepared
	parentMeta := tools.RuntimeContextFrom(ctx)
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = "run_" + runID
	}
	parentAgentID := strings.TrimSpace(req.ParentAgentID)
	if parentAgentID == "" {
		parentAgentID = strings.TrimSpace(parentMeta.AgentID)
	}
	ownsLifecycle := strings.TrimSpace(parentMeta.RunID) == ""

	var sess *session.Session
	if deps.SessionStore != nil {
		sess, err = deps.SessionStore.Load(resolved.Agent.ID, sessionID)
		if err != nil {
			return nil, err
		}
	} else {
		sess = session.NewSession(resolved.Agent.ID, sessionID)
	}
	sess.SetActiveRefs(session.EntryRefs{
		RunID:  runID,
		TaskID: firstNonEmpty(strings.TrimSpace(req.TaskID), strings.TrimSpace(parentMeta.TaskID)),
	})

	runRecord := runtimeevents.RunRecord{
		ID:            runID,
		ParentAgentID: parentAgentID,
		AgentID:       resolved.Agent.ID,
		SessionID:     sessionID,
		Model:         resolved.Agent.Model,
		Channel:       req.Channel,
		Status:        runtimeevents.RunStatusRunning,
	}
	inputAdapter := tools.NewRunInputAdapter(req.Envelope)
	sessionState := runtimesessionstate.NewStateStore(sess, deps.Recorder, runRecord)
	var memAdapter *tools.MemoryProviderAdapter
	if deps.Memory != nil {
		memAdapter = &tools.MemoryProviderAdapter{
			Manager:   deps.Memory,
			AgentID:   resolved.Agent.ID,
			SessionID: sessionID,
		}
	}

	extras := tools.ExtraToolDeps{
		AttachmentProvider:    inputAdapter,
		InputManifestProvider: inputAdapter,
		PlanStore:             sessionState,
		TodoStore:             sessionState,
	}
	if memAdapter != nil {
		extras.MemoryProvider = memAdapter
	}

	rt, err := runtimefactory.BuildAgentRuntimeFromDeps(deps, resolved, sess, extras)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(req.Channel, "agent_call") || parentAgentID != "" {
		rt.RunMode = "agent_call"
	}
	rt.ParentAgentID = parentAgentID
	rt.AgentCallContract = req.Contract.Normalized()

	inputText, inputImages := tools.ResolveEnvelopeForRuntime(req.Envelope)
	if len(req.Images) > 0 {
		inputImages = append(inputImages, req.Images...)
	}

	var (
		runTurn  *turn.Turn
		ownsTurn bool
	)
	if deps.TurnStore != nil {
		runTurn, _ = deps.TurnStore.Get(turn.ID(runID))
		if runTurn == nil {
			runTurn = deps.TurnStore.Create(ctx, turn.Config{
				ID:          turn.ID(runID),
				SessionID:   sessionID,
				IdleTimeout: managedRunIdleTimeout(deps),
			})
			ownsTurn = true
		}
	}
	runCtxBase := ctx
	if runTurn != nil {
		if ownsTurn {
			runCtxBase = turn.RebindContext(runTurn.Context(), ctx, runTurn)
		} else {
			runCtxBase = turn.WithBoundContext(ctx, runTurn)
		}
	}
	runCtx, cancel := context.WithCancel(runCtxBase)
	callChain := append([]string(nil), parentMeta.CallChain...)
	depth := 0
	if parentMeta.RunID != "" {
		depth = parentMeta.Depth + 1
	}
	if len(callChain) == 0 && parentAgentID != "" {
		callChain = append(callChain, parentAgentID)
	}
	if len(callChain) == 0 || callChain[len(callChain)-1] != resolved.Agent.ID {
		callChain = append(callChain, resolved.Agent.ID)
	}
	runMeta := tools.RuntimeContext{
		RunID:          runID,
		AgentID:        resolved.Agent.ID,
		SessionID:      sessionID,
		TaskID:         firstNonEmpty(strings.TrimSpace(req.TaskID), strings.TrimSpace(parentMeta.TaskID)),
		InputMessageID: strings.TrimSpace(req.MessageID),
		AssetsBaseDir:  input.ProjectAssetsDir(deps.Config),
		CurrentRequest: strings.TrimSpace(inputText),
		CallChain:      callChain,
		Depth:          depth,
	}
	runCtx = tools.WithRuntimeContext(runCtx, runMeta)
	runCtx = tools.WithRuntimeInputBlocks(runCtx, req.Envelope.Blocks)

	events, err := rt.Run(runCtx, inputText, inputImages)
	if err != nil {
		if ownsLifecycle && runTurn != nil {
			runTurn.Cancel("run_error")
		}
		cancel()
		return nil, err
	}

	runRecord.Input = inputText
	if deps.Recorder != nil {
		if ownsLifecycle {
			deps.Recorder.BeginRun(runRecord)
		}
		if payload := sessionInputPayload(inputText, req.Envelope, runMeta); payload != nil {
			deps.Recorder.AppendEvent(runtimeevents.EventRecord{
				RunID:     runRecord.ID,
				AgentID:   runRecord.AgentID,
				SessionID: runRecord.SessionID,
				Name:      runtimeevents.EventSessionInputStored,
				Payload:   payload,
			})
		}
		recordAttachmentResolution(deps.Recorder, runRecord, inputAdapter)
		deps.Recorder.AppendEvent(runtimeevents.EventRecord{
			RunID:     runRecord.ID,
			AgentID:   runRecord.AgentID,
			SessionID: runRecord.SessionID,
			Name:      runtimeevents.EventRunStarted,
		})
	}

	out := make(chan runtimeevents.EventRecord, 128)
	startedRecord := runtimeevents.EventRecord{
		RunID:     runRecord.ID,
		AgentID:   runRecord.AgentID,
		SessionID: runRecord.SessionID,
		Name:      runtimeevents.EventRunStarted,
		Timestamp: time.Now().UTC(),
	}
	go func() {
		defer close(out)
		defer cancel()

		status := runtimeevents.RunStatusRunning
		var output managedRunOutputBuffer
		var errMsg string
		defer func() {
			if status == runtimeevents.RunStatusRunning {
				status = runtimeevents.RunStatusCompleted
			}
			finalOutput := output.Final()
			if ownsLifecycle && runTurn != nil {
				switch status {
				case runtimeevents.RunStatusAborted:
					runTurn.Cancel("run_aborted")
				default:
					runTurn.Complete()
				}
				if deps.TurnStore != nil && ownsTurn {
					deps.TurnStore.Remove(runTurn.ID)
				}
			}
			if deps.Recorder != nil && ownsLifecycle {
				deps.Recorder.FinishRun(runID, status, finalOutput, errMsg)
			}
			appendSessionTerminalMeta(sess, status, errMsg)
			if deps.Pipeline != nil && ownsLifecycle {
				deps.Pipeline.CaptureSessionEnd(runRecord, sess, status, finalOutput, errMsg)
			}
		}()

		out <- startedRecord

		for event := range events {
			output.Observe(event)
			switch event.Type {
			case agent.EventActivity:
			case agent.EventTextDelta:
			case agent.EventDone:
				status = runtimeevents.RunStatusCompleted
			case agent.EventAborted:
				status = runtimeevents.RunStatusAborted
				if errMsg == "" {
					errMsg = "run aborted"
				}
			case agent.EventError:
				status = runtimeevents.RunStatusFailed
				if event.Error != nil {
					errMsg = event.Error.Error()
				}
			}

			if event.Type == agent.EventRunStarted {
				continue
			}
			records := runtimeevents.EventRecordsForAgentEvent(runRecord, event)
			if deps.Recorder != nil {
				deps.Recorder.RecordAgentEvent(runRecord, event)
			}
			for _, record := range records {
				if !shouldPublishManagedRunEvent(record) {
					continue
				}
				out <- record
			}
		}
	}()

	return &runtimeport.ManagedRun{
		RunID:         runID,
		AgentID:       resolved.Agent.ID,
		SessionID:     sessionID,
		Model:         resolved.Agent.Model,
		Events:        out,
		Cancel:        cancel,
		OwnsLifecycle: ownsLifecycle,
	}, nil
}

func appendSessionTerminalMeta(sess *session.Session, status runtimeevents.RunStatus, errMsg string) {
	if sess == nil {
		return
	}
	errMsg = strings.TrimSpace(errMsg)
	switch status {
	case runtimeevents.RunStatusFailed:
		if errMsg == "" {
			errMsg = "run failed"
		}
		sess.Append(session.MetaEntry("Run failed: " + errMsg))
	case runtimeevents.RunStatusAborted:
		if errMsg == "" {
			errMsg = "run aborted"
		}
		sess.Append(session.MetaEntry("Run aborted: " + errMsg))
	}
}

func ensureManagedRunDeps(deps runtimeport.ExecutionDeps) runtimeport.ExecutionDeps {
	if deps.TurnStore == nil {
		deps.TurnStore = turn.NewStore()
	}
	if deps.TaskRuntime != nil {
		deps.TaskRuntime.SetTurnStore(deps.TurnStore)
		return deps
	}

	registry := task.NewExecutorRegistry()
	_ = registry.Register(task.KindTool, runtimetaskbuiltin.NewToolExecutor(nil))
	_ = registry.Register(task.KindProcess, runtimetaskbuiltin.NewProcessExecutor())
	deps.TaskRuntime = task.NewRuntime(task.NewStore(), registry)
	deps.TaskRuntime.SetTurnStore(deps.TurnStore)
	if deps.Recorder != nil {
		deps.TaskRuntime.SetEventAppender(func(event runtimeevents.EventRecord) {
			runtimeevents.AppendEventWithReplayPolicy(deps.Recorder, event)
		})
	}
	deps.TaskStore = deps.TaskRuntime.Store()
	return deps
}

func managedRunIdleTimeout(deps runtimeport.ExecutionDeps) time.Duration {
	if deps.Config == nil || deps.Config.Runtime.IdleTimeoutMS <= 0 {
		return 0
	}
	return time.Duration(deps.Config.Runtime.IdleTimeoutMS) * time.Millisecond
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func shouldPublishManagedRunEvent(record runtimeevents.EventRecord) bool {
	switch strings.TrimSpace(record.Name) {
	case runtimeevents.EventRunIncomplete,
		runtimeevents.EventRunFallbackReply,
		runtimeevents.EventLLMRetrying:
		return false
	default:
		return true
	}
}

func recordAttachmentResolution(recorder *runtimeevents.Recorder, run runtimeevents.RunRecord, adapter *tools.RunInputAdapter) {
	if recorder == nil || adapter == nil {
		return
	}
	for _, block := range adapter.InputManifest() {
		if strings.TrimSpace(block.ID) == "" {
			continue
		}
		switch strings.TrimSpace(block.Type) {
		case "file", "image", "pdf", "dir":
		default:
			continue
		}
		payload := map[string]any{
			"attachment_id": block.ID,
			"type":          strings.TrimSpace(block.Type),
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
		recorder.AppendEvent(runtimeevents.EventRecord{
			RunID:     run.ID,
			AgentID:   run.AgentID,
			SessionID: run.SessionID,
			Name:      "attachment.resolved",
			Payload:   payload,
		})
	}
}

func sessionInputPayload(text string, env input.InputEnvelope, meta tools.RuntimeContext) map[string]any {
	text = strings.TrimSpace(text)
	payload := map[string]any{}
	if text != "" {
		payload["text"] = text
	}
	if runID := strings.TrimSpace(meta.RunID); runID != "" {
		payload["run_id"] = runID
	}
	if taskID := strings.TrimSpace(meta.TaskID); taskID != "" {
		payload["task_id"] = taskID
	}
	if messageID := strings.TrimSpace(meta.InputMessageID); messageID != "" {
		payload["entry_id"] = messageID
	}
	if refs := session.ImageRefsFromBlocks(env.Blocks); len(refs) > 0 {
		items := make([]map[string]any, 0, len(refs))
		for _, image := range refs {
			item := map[string]any{}
			if id := strings.TrimSpace(image.ID); id != "" {
				item["id"] = id
			}
			if name := strings.TrimSpace(image.Name); name != "" {
				item["name"] = name
			}
			if mimeType := strings.TrimSpace(image.MimeType); mimeType != "" {
				item["mime_type"] = mimeType
			}
			if path := strings.TrimSpace(image.Path); path != "" {
				item["path"] = path
			}
			if id := strings.TrimSpace(image.ID); id != "" {
				item["attachment_id"] = id
			}
			if image.Size > 0 {
				item["size"] = image.Size
			}
			if len(item) == 0 {
				continue
			}
			items = append(items, item)
		}
		if len(items) > 0 {
			payload["images"] = items
		}
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

// managedRunOutputBuffer is run-level replay state only. Session history is
// projected from session appends, so this buffer must never write
// session.output.stored.
type managedRunOutputBuffer struct {
	text strings.Builder
}

func (b *managedRunOutputBuffer) Observe(event agent.AgentEvent) {
	switch event.Type {
	case agent.EventTextDelta:
		b.text.WriteString(event.Text)
	case agent.EventToolRetry,
		agent.EventToolCallRequested,
		agent.EventToolCallStart,
		agent.EventToolWarning,
		agent.EventToolResult:
		if shouldResetManagedRunOutputForTool(event) {
			b.text.Reset()
		}
	}
}

func (b *managedRunOutputBuffer) Final() string {
	return strings.TrimSpace(b.text.String())
}

func shouldResetManagedRunOutputForTool(event agent.AgentEvent) bool {
	toolName := ""
	if event.ToolCall != nil {
		toolName = event.ToolCall.Name
	}
	if toolName == "" && event.ToolRetry != nil {
		toolName = event.ToolRetry.ToolName
	}
	if toolName == "" && event.ToolWarning != nil {
		toolName = event.ToolWarning.ToolName
	}
	return shouldResetOutputForTool(toolName)
}

func shouldResetOutputForTool(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "goal_complete", "await_user_input":
		return false
	default:
		return true
	}
}
