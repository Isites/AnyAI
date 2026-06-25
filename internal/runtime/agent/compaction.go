package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"github.com/Isites/anyai/internal/runtime/session"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

const (
	compactionSummaryTag    = "[Compacted Session Summary]"
	compactionSummaryPrefix = compactionSummaryTag + "\n" +
		"Earlier session context was compacted into the handoff summary below. " +
		"Use it as authoritative prior context while relying on the remaining transcript for recent turns.\n\n"
	compactionSystemPrompt = `You are producing a checkpoint handoff summary for a long-running AnyAI agent session. The summary will replace older transcript history while the most recent turns remain verbatim in the session. Preserve only facts you can establish from the transcript. Keep the summary concise but information-dense, and make the user's current direction hard to lose.`
	compactionUserPrompt   = `Create a handoff summary for the older session context above.

Return exactly these markdown sections:

## Objective
- The user's current goal or task thread.

## Direction And Constraints
- Decisions, explicit constraints, preferences, and things that must not change.

## Important State
- Durable facts, file paths, commands, errors, artifacts, IDs, and outputs that still matter.

## Recent Progress
- What was completed or learned before the recent verbatim transcript begins.

## Open Issues
- Unresolved risks, blockers, or unknowns.

## Next Action
- The most likely next step.

Rules:
- Preserve only facts established by the transcript.
- The most recent turns remain separately, so avoid repeating them unless needed for continuity.
- Return a non-empty markdown summary. If sparse, fill the sections with known facts and an explicit next step.`
	defaultCompactionTriggerMode          = "token_estimate"
	defaultCompactionEntryThreshold       = 96
	defaultCompactionTokenThreshold       = 12000
	defaultCompactionKeepRecentUserTurns  = 4
	defaultCompactionKeepRecentUserTokens = 2400
	defaultCompactionSummaryMaxTokens     = 1600
	defaultCompactionMaxAttempts          = 2
	defaultManualCompactionKeepEntries    = 24
	manualCompactionTrigger               = "manual"
	defaultCompactionArchiveCompression   = session.ArchiveCompressionGzip
	defaultCompactionContextProjection    = "state_aware"
)

type compactionDecision struct {
	TriggerMode  string
	Threshold    int
	KeepEntries  int
	SummaryMax   int
	OlderHistory []session.SessionEntry
	RecentBlocks []session.HistoryBlock
}

type compactionScope struct {
	ProtectedPrefix []session.SessionEntry
	Compactable     []session.SessionEntry
}

type CompactionResult struct {
	Applied       bool
	Summary       string
	Trigger       string
	Strategy      llm.CompactStrategy
	SummarySource string
	KeepEntries   int
	Threshold     int
	CompactID     string
	ArchiveRef    string
	ArchiveSHA256 string
	SourceSHA256  string
}

func defaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		Enabled:              true,
		TriggerMode:          defaultCompactionTriggerMode,
		EntryThreshold:       defaultCompactionEntryThreshold,
		TokenThreshold:       defaultCompactionTokenThreshold,
		KeepRecentUserTurns:  defaultCompactionKeepRecentUserTurns,
		KeepRecentUserTokens: defaultCompactionKeepRecentUserTokens,
		SummaryMaxTokens:     defaultCompactionSummaryMaxTokens,
		ArchiveEnabled:       true,
		ArchiveCompression:   defaultCompactionArchiveCompression,
		FocusEnabled:         true,
		ContextProjection:    defaultCompactionContextProjection,
	}
}

func (r *Runtime) effectiveCompactionConfig() CompactionConfig {
	cfg := defaultCompactionConfig()
	if r == nil {
		return cfg
	}
	if r.Compaction != (CompactionConfig{}) {
		cfg = r.Compaction
		if strings.TrimSpace(cfg.TriggerMode) == "" {
			cfg.TriggerMode = defaultCompactionTriggerMode
		}
		if cfg.EntryThreshold <= 0 {
			cfg.EntryThreshold = defaultCompactionEntryThreshold
		}
		if cfg.TokenThreshold <= 0 {
			cfg.TokenThreshold = defaultCompactionTokenThreshold
		}
		if cfg.KeepRecentUserTurns <= 0 {
			cfg.KeepRecentUserTurns = defaultCompactionKeepRecentUserTurns
		}
		if cfg.KeepRecentUserTokens <= 0 {
			cfg.KeepRecentUserTokens = defaultCompactionKeepRecentUserTokens
		}
		if cfg.SummaryMaxTokens <= 0 {
			cfg.SummaryMaxTokens = defaultCompactionSummaryMaxTokens
		}
		if strings.TrimSpace(cfg.ArchiveCompression) == "" {
			cfg.ArchiveCompression = defaultCompactionArchiveCompression
		}
		if strings.TrimSpace(cfg.ContextProjection) == "" {
			cfg.ContextProjection = defaultCompactionContextProjection
		}
	}
	if r.SessionCompactThreshold > 0 {
		cfg.TriggerMode = "entry_count"
		cfg.EntryThreshold = r.SessionCompactThreshold
	}
	return cfg
}

func buildCompactionScope(history []session.SessionEntry) compactionScope {
	history = session.RepairLeadingFragment(history)
	if len(history) == 0 {
		return compactionScope{}
	}

	split := 0
	for split < len(history) && isCompactionProtectedPrefixEntry(history[split]) {
		split++
	}

	return compactionScope{
		ProtectedPrefix: append([]session.SessionEntry(nil), history[:split]...),
		Compactable:     append([]session.SessionEntry(nil), history[split:]...),
	}
}

func isCompactionProtectedPrefixEntry(entry session.SessionEntry) bool {
	if entry.Type == session.EntryTypeMeta {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(entry.Role), "system")
}

func (r *Runtime) shouldCompactSession(history []session.SessionEntry) (compactionDecision, bool) {
	if len(history) == 0 {
		return compactionDecision{}, false
	}
	cfg := r.effectiveCompactionConfig()
	if !cfg.Enabled {
		return compactionDecision{}, false
	}

	blocks := session.BuildHistoryBlocks(history)
	if len(blocks) < 2 {
		return compactionDecision{}, false
	}
	recentBlocks := selectRecentCompactionBlocks(blocks, cfg, r.SessionCompactKeepEntries)
	recentEntries := session.FlattenHistoryBlocks(recentBlocks)
	if len(recentEntries) == 0 || len(recentEntries) >= len(history) {
		return compactionDecision{}, false
	}

	shouldCompact := false
	threshold := 0
	switch cfg.TriggerMode {
	case "entry_count":
		threshold = cfg.EntryThreshold
		shouldCompact = threshold > 0 && len(history) > threshold
	default:
		threshold = cfg.TokenThreshold
		shouldCompact = threshold > 0 && estimateHistoryTokens(history) > threshold
	}
	if !shouldCompact {
		return compactionDecision{}, false
	}

	olderCount := len(history) - len(recentEntries)
	if olderCount <= 0 {
		return compactionDecision{}, false
	}
	return compactionDecision{
		TriggerMode:  cfg.TriggerMode,
		Threshold:    threshold,
		KeepEntries:  len(recentEntries),
		SummaryMax:   cfg.SummaryMaxTokens,
		OlderHistory: append([]session.SessionEntry(nil), history[:olderCount]...),
		RecentBlocks: append([]session.HistoryBlock(nil), recentBlocks...),
	}, true
}

func selectRecentCompactionBlocks(blocks []session.HistoryBlock, cfg CompactionConfig, minEntries int) []session.HistoryBlock {
	if len(blocks) == 0 {
		return nil
	}
	needUserTurns := cfg.KeepRecentUserTurns
	if needUserTurns <= 0 {
		needUserTurns = 1
	}
	needTokens := cfg.KeepRecentUserTokens
	if cfg.TriggerMode == "entry_count" {
		needTokens = 0
	}
	selected := make([]session.HistoryBlock, 0, len(blocks))
	userTurns := 0
	tokenBudget := 0
	start := len(blocks)
	for start > 0 {
		start--
		selected = append(selected, blocks[start])
		tokenBudget += blocks[start].ApproxTokens
		if blocks[start].HasUserTurn {
			userTurns++
		}
		if userTurns >= needUserTurns {
			break
		}
	}
	for needTokens > 0 && tokenBudget < needTokens && start > 1 {
		start--
		selected = append(selected, blocks[start])
		tokenBudget += blocks[start].ApproxTokens
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	if minEntries > 0 && len(session.FlattenHistoryBlocks(selected)) < minEntries {
		fallback := session.SelectRecentBlocksByMinEntries(blocks, minEntries)
		if len(fallback) < len(blocks) || len(blocks) == 1 {
			return fallback
		}
	}
	return selected
}

func buildManualCompactionDecision(history []session.SessionEntry, keepEntries, summaryMax int) (compactionDecision, bool) {
	history = session.RepairLeadingFragment(history)
	if len(history) == 0 {
		return compactionDecision{}, false
	}
	if keepEntries <= 0 {
		keepEntries = defaultManualCompactionKeepEntries
	}
	blocks := session.BuildHistoryBlocks(history)
	if len(blocks) < 2 {
		return compactionDecision{}, false
	}
	recentBlocks := session.SelectRecentBlocksByMinEntries(blocks, keepEntries)
	recentEntries := session.FlattenHistoryBlocks(recentBlocks)
	if len(recentEntries) == 0 || len(recentEntries) >= len(history) {
		return compactionDecision{}, false
	}
	olderCount := len(history) - len(recentEntries)
	if olderCount <= 0 {
		return compactionDecision{}, false
	}
	if summaryMax <= 0 {
		summaryMax = defaultCompactionSummaryMaxTokens
	}
	return compactionDecision{
		TriggerMode:  manualCompactionTrigger,
		KeepEntries:  len(recentEntries),
		SummaryMax:   summaryMax,
		OlderHistory: append([]session.SessionEntry(nil), history[:olderCount]...),
		RecentBlocks: append([]session.HistoryBlock(nil), recentBlocks...),
	}, true
}

func approxTranscriptTokens(msgs []llm.Message, systemPrompt string) int {
	totalChars := len(systemPrompt)
	for _, msg := range msgs {
		totalChars += len(msg.Role)
		totalChars += len(msg.Content)
		totalChars += len(msg.ToolCallID)
		for _, tc := range msg.ToolCalls {
			totalChars += len(tc.ID)
			totalChars += len(tc.Name)
			totalChars += len(tc.Input)
		}
	}
	approx := totalChars / 4
	if approx <= 0 {
		return 1
	}
	return approx
}

func estimateHistoryTokens(history []session.SessionEntry) int {
	totalChars := 0
	for _, entry := range history {
		totalChars += len(entry.Type)
		totalChars += len(entry.Role)
		totalChars += len(entry.Data)
	}
	approx := totalChars / 4
	if approx <= 0 {
		return 1
	}
	return approx
}

func (r *Runtime) generateCompactionSummary(ctx context.Context, olderHistory []session.SessionEntry, maxTokens int) (llm.CompactResponse, error) {
	if len(olderHistory) == 0 {
		return llm.CompactResponse{}, nil
	}
	msgs := prepareTranscript(assembleMessages(olderHistory), r.transcriptPolicy()).Messages
	pruneToolResults(msgs, maxToolResultLen)
	if maxTokens <= 0 {
		maxTokens = defaultCompactionSummaryMaxTokens
	}
	baseUserPrompt := compactionUserPrompt
	req := llm.CompactRequest{
		Model:        r.Model,
		Messages:     msgs,
		MaxTokens:    maxTokens,
		SystemPrompt: compactionSystemPrompt,
		UserPrompt:   baseUserPrompt,
		Options:      r.ModelOptions,
	}
	maxAttempts := defaultCompactionMaxAttempts
	backoff := r.llmRetryBackoff()
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 && lastErr != nil {
			req.UserPrompt = baseUserPrompt + "\n\nRetry instruction: the previous compaction attempt failed or returned an empty summary (" + lastErr.Error() + "). Return a non-empty markdown handoff summary now. Do not return an empty response."
		}
		resp, err := llm.CompactWithProvider(ctx, r.LLM, req)
		if err == nil {
			result := strings.TrimSpace(resp.Summary)
			if result != "" {
				resp.Summary = result
				if strings.TrimSpace(string(resp.Strategy)) == "" {
					resp.Strategy = llm.CompactStrategyUnknown
				}
				return resp, nil
			}
			err = fmt.Errorf("compaction summary returned empty output")
		} else {
			err = fmt.Errorf("provider compaction failed: %w", err)
		}
		lastErr = err
		if attempt >= maxAttempts {
			break
		}

		wait := retryDelay(backoff, attempt)
		runtimelogging.Warn("session compaction attempt failed; retrying",
			"agent", r.AgentID,
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"next_attempt", attempt+1,
			"wait_ms", int(wait/time.Millisecond),
			"error", err.Error(),
		)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return llm.CompactResponse{}, ctx.Err()
		case <-timer.C:
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("compaction summary returned empty output")
	}
	return llm.CompactResponse{}, lastErr
}

func (r *Runtime) rewriteSessionForCompaction(data session.CompactionData, decision compactionDecision, scope compactionScope, cfg CompactionConfig) (session.RewriteResult, error) {
	if r == nil || r.Session == nil {
		return session.RewriteResult{}, nil
	}
	rewritten := session.RewriteHistoryWithCompactionData(
		scope.Compactable,
		data,
		decision.KeepEntries,
	)
	prefix := append([]session.SessionEntry(nil), scope.ProtectedPrefix...)
	if cfg.FocusEnabled {
		prefix = withSessionFocus(prefix, data.Text, scope.Compactable)
	}
	finalHistory := make([]session.SessionEntry, 0, len(prefix)+len(rewritten))
	finalHistory = append(finalHistory, prefix...)
	finalHistory = append(finalHistory, rewritten...)
	finalHistory = stampCompactionAfterCount(finalHistory, data, len(finalHistory))
	result, err := r.Session.ReplaceHistoryWithOptions(finalHistory, session.RewriteOptions{
		Archive:            cfg.ArchiveEnabled,
		CompactID:          data.CompactID,
		ArchiveCompression: cfg.ArchiveCompression,
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func withSessionFocus(prefix []session.SessionEntry, summary string, history []session.SessionEntry) []session.SessionEntry {
	focus := session.BuildSessionFocus(summary, history)
	if strings.TrimSpace(focus) == "" {
		return prefix
	}
	out := make([]session.SessionEntry, 0, len(prefix)+1)
	for _, entry := range prefix {
		if session.IsSessionFocusEntry(entry) {
			continue
		}
		out = append(out, entry)
	}
	out = append(out, session.SessionFocusEntry(focus))
	return out
}

func stampCompactionAfterCount(history []session.SessionEntry, data session.CompactionData, afterCount int) []session.SessionEntry {
	if len(history) == 0 {
		return history
	}
	data.AfterEntryCount = afterCount
	for i := range history {
		if history[i].Type != session.EntryTypeCompaction {
			continue
		}
		history[i] = session.CompactionEntry(data)
		return history
	}
	return history
}

func (r *Runtime) executeCompaction(ctx context.Context, history []session.SessionEntry, scope compactionScope, decision compactionDecision) (CompactionResult, error) {
	cfg := r.effectiveCompactionConfig()
	state, err := r.applyBeforeCompactionHooks(ctx, CompactionState{
		History:     append([]session.SessionEntry(nil), scope.Compactable...),
		Threshold:   decision.Threshold,
		KeepEntries: decision.KeepEntries,
		Trigger:     decision.TriggerMode,
	})
	if err != nil {
		return CompactionResult{}, fmt.Errorf("before_compaction hook failed: %w", err)
	}
	if state.KeepEntries > 0 && state.KeepEntries != decision.KeepEntries {
		allBlocks := session.BuildHistoryBlocks(scope.Compactable)
		decision.RecentBlocks = session.SelectRecentBlocksByMinEntries(allBlocks, state.KeepEntries)
		decision.KeepEntries = len(session.FlattenHistoryBlocks(decision.RecentBlocks))
		if decision.KeepEntries > 0 && decision.KeepEntries < len(scope.Compactable) {
			decision.OlderHistory = append([]session.SessionEntry(nil), scope.Compactable[:len(scope.Compactable)-decision.KeepEntries]...)
		} else {
			decision.OlderHistory = nil
		}
	}
	if len(decision.OlderHistory) == 0 {
		return CompactionResult{}, nil
	}
	caps := llm.DescribeProviderCapabilities(r.LLM)
	compactID := session.NewCompactionID()
	previousCompactID := ""
	if previous, ok := session.LatestCompactionData(history); ok {
		previousCompactID = strings.TrimSpace(previous.CompactID)
	}
	archiveRef := ""
	if cfg.ArchiveEnabled && r.Session != nil && strings.TrimSpace(r.Session.ID) != "" {
		archiveAgentID := firstNonEmpty(r.Session.AgentID, r.AgentID)
		archiveRef = session.CompactionArchiveRef(archiveAgentID, r.Session.ID, compactID, cfg.ArchiveCompression)
	}
	requestedPayload := map[string]any{
		"compact_id":               compactID,
		"previous_compact_id":      previousCompactID,
		"archive_ref":              archiveRef,
		"trigger":                  decision.TriggerMode,
		"threshold":                decision.Threshold,
		"history_len":              len(scope.Compactable),
		"history_len_before":       len(scope.Compactable),
		"history_len_total":        len(history),
		"history_len_total_before": len(history),
		"protected_prefix_len":     len(scope.ProtectedPrefix),
		"older_history_len":        len(decision.OlderHistory),
		"keep_entries":             decision.KeepEntries,
		"model":                    strings.TrimSpace(r.Model),
		"provider":                 caps.Provider,
		"compact_capability":       string(caps.Compact),
	}
	r.appendCompactionEvent(ctx, runtimeevents.EventSessionCompactRequested, requestedPayload)

	summary := strings.TrimSpace(state.Summary)
	strategy := llm.CompactStrategyHookSupplied
	summarySource := "hook"
	if summary == "" {
		var resp llm.CompactResponse
		resp, err = r.generateCompactionSummary(ctx, decision.OlderHistory, decision.SummaryMax)
		if err != nil {
			return CompactionResult{}, err
		}
		summary = strings.TrimSpace(resp.Summary)
		strategy = resp.Strategy
		summarySource = "provider_model"
	}
	if strings.TrimSpace(string(strategy)) == "" {
		strategy = llm.CompactStrategyUnknown
	}
	beforeLeafID := ""
	if len(history) > 0 {
		beforeLeafID = history[len(history)-1].ID
	}
	compactionData := session.CompactionData{
		Text:              summary,
		Trigger:           decision.TriggerMode,
		CompactID:         compactID,
		PreviousCompactID: previousCompactID,
		ArchiveRef:        archiveRef,
		BeforeEntryCount:  len(history),
		BeforeLeafID:      beforeLeafID,
		SummarySource:     summarySource,
		CompactStrategy:   string(strategy),
		CreatedAt:         time.Now().Unix(),
	}
	rewriteResult, err := r.rewriteSessionForCompaction(compactionData, decision, scope, cfg)
	if err != nil {
		return CompactionResult{}, err
	}
	currentHistory := session.RepairLeadingFragment(r.Session.History())
	currentScope := buildCompactionScope(currentHistory)
	afterLeafID := ""
	if len(currentHistory) > 0 {
		afterLeafID = currentHistory[len(currentHistory)-1].ID
	}
	if archiveRef == "" {
		archiveRef = rewriteResult.ArchiveRef
	}
	completedPayload := map[string]any{
		"compact_id":               compactID,
		"previous_compact_id":      previousCompactID,
		"archive_ref":              archiveRef,
		"archive_sha256":           rewriteResult.ArchiveSHA256,
		"source_sha256":            rewriteResult.SourceSHA256,
		"before_entry_count":       len(history),
		"after_entry_count":        len(currentHistory),
		"before_leaf_id":           beforeLeafID,
		"after_leaf_id":            afterLeafID,
		"trigger":                  decision.TriggerMode,
		"threshold":                decision.Threshold,
		"history_len":              len(currentScope.Compactable),
		"history_len_before":       len(scope.Compactable),
		"history_len_after":        len(currentScope.Compactable),
		"history_len_total":        len(currentHistory),
		"history_len_total_before": len(history),
		"history_len_total_after":  len(currentHistory),
		"protected_prefix_len":     len(scope.ProtectedPrefix),
		"older_history_len":        len(decision.OlderHistory),
		"keep_entries":             decision.KeepEntries,
		"model":                    strings.TrimSpace(r.Model),
		"provider":                 caps.Provider,
		"compact_capability":       string(caps.Compact),
		"compact_strategy":         string(strategy),
		"summary_source":           summarySource,
		"summary":                  summary,
	}
	r.appendCompactionEvent(ctx, runtimeevents.EventSessionCompactCompleted, completedPayload)

	runtimelogging.Info("session compacted with checkpoint summary",
		"agent", r.AgentID,
		"trigger", decision.TriggerMode,
		"history_len_before", len(scope.Compactable),
		"history_len_after", len(currentScope.Compactable),
		"history_len_total_before", len(history),
		"history_len_total_after", len(currentHistory),
		"protected_prefix_len", len(scope.ProtectedPrefix),
		"keep_entries", decision.KeepEntries,
		"model", strings.TrimSpace(r.Model),
		"provider", caps.Provider,
		"compact_capability", string(caps.Compact),
		"compact_strategy", string(strategy),
		"summary_source", summarySource,
		"compact_id", compactID,
		"archive_ref", archiveRef,
		"archive_sha256", rewriteResult.ArchiveSHA256,
		"source_sha256", rewriteResult.SourceSHA256,
	)
	if err := r.applyAfterCompactionHooks(ctx, CompactionState{
		History:     append([]session.SessionEntry(nil), r.Session.History()...),
		Threshold:   decision.Threshold,
		KeepEntries: decision.KeepEntries,
		Trigger:     decision.TriggerMode,
		Summary:     summary,
	}); err != nil {
		return CompactionResult{}, fmt.Errorf("after_compaction hook failed: %w", err)
	}
	return CompactionResult{
		Applied:       true,
		Summary:       summary,
		Trigger:       decision.TriggerMode,
		Strategy:      strategy,
		SummarySource: summarySource,
		KeepEntries:   decision.KeepEntries,
		Threshold:     decision.Threshold,
		CompactID:     compactID,
		ArchiveRef:    archiveRef,
		ArchiveSHA256: rewriteResult.ArchiveSHA256,
		SourceSHA256:  rewriteResult.SourceSHA256,
	}, nil
}

func (r *Runtime) CompactSessionNow(ctx context.Context, keepEntries int) (CompactionResult, error) {
	if r == nil || r.Session == nil {
		return CompactionResult{}, nil
	}
	history := session.RepairLeadingFragment(r.Session.History())
	if len(history) == 0 {
		return CompactionResult{}, nil
	}
	scope := buildCompactionScope(history)
	if len(scope.Compactable) == 0 {
		return CompactionResult{}, nil
	}
	decision, ok := buildManualCompactionDecision(scope.Compactable, keepEntries, r.effectiveCompactionConfig().SummaryMaxTokens)
	if !ok {
		return CompactionResult{}, nil
	}
	return r.executeCompaction(ctx, history, scope, decision)
}

func (r *Runtime) runModelCompactionIfNeeded(ctx context.Context) error {
	if r == nil || r.Session == nil {
		return nil
	}
	history := session.RepairLeadingFragment(r.Session.History())
	if len(history) == 0 {
		return nil
	}
	scope := buildCompactionScope(history)
	if len(scope.Compactable) == 0 {
		return nil
	}

	decision, ok := r.shouldCompactSession(scope.Compactable)
	if !ok {
		return nil
	}
	_, err := r.executeCompaction(ctx, history, scope, decision)
	return err
}

func (r *Runtime) appendCompactionEvent(ctx context.Context, name string, payload map[string]any) {
	if r == nil || r.EventAppender == nil || strings.TrimSpace(name) == "" {
		return
	}
	meta := tools.RuntimeContextFrom(ctx)
	runID := strings.TrimSpace(meta.RunID)
	if runID == "" {
		return
	}
	r.EventAppender(runtimeevents.EventRecord{
		RunID:     runID,
		AgentID:   firstNonEmpty(meta.AgentID, r.AgentID),
		SessionID: firstNonEmpty(meta.SessionID, sessionIDFromRuntime(r)),
		Name:      name,
		Payload:   payload,
	})
}
