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
	compactionSystemPrompt = `You are producing a checkpoint handoff summary for a long-running AnyAI agent session. The summary will replace older transcript history while the most recent turns remain verbatim in the session. Preserve only facts you can establish from the transcript. Keep the summary concise but information-dense.`
	compactionUserPrompt   = `Create a handoff summary for the older session context above.

Requirements:
- State the overall goal or task thread if it is clear.
- Capture established facts, constraints, and important decisions.
- Preserve concrete technical breadcrumbs that still matter: file paths, commands, error messages, tool outputs, and artifacts.
- List unresolved issues, risks, and the next recommended steps.
- The most recent turns will remain in the transcript separately, so avoid repeating them unless they are essential for continuity.
- Write short markdown sections with compact bullet points.`
	defaultCompactionTriggerMode          = "token_estimate"
	defaultCompactionEntryThreshold       = 96
	defaultCompactionTokenThreshold       = 12000
	defaultCompactionKeepRecentUserTurns  = 4
	defaultCompactionKeepRecentUserTokens = 2400
	defaultCompactionSummaryMaxTokens     = 1600
	defaultCompactionMaxAttempts          = 2
	defaultManualCompactionKeepEntries    = 24
	manualCompactionTrigger               = "manual"
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
		msgs := prepareTranscript(assembleMessages(history), r.transcriptPolicy()).Messages
		threshold = cfg.TokenThreshold
		shouldCompact = threshold > 0 && approxTranscriptTokens(msgs, "") > threshold
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

func (r *Runtime) generateCompactionSummary(ctx context.Context, olderHistory []session.SessionEntry, maxTokens int) (llm.CompactResponse, error) {
	if len(olderHistory) == 0 {
		return llm.CompactResponse{}, nil
	}
	msgs := prepareTranscript(assembleMessages(olderHistory), r.transcriptPolicy()).Messages
	pruneToolResults(msgs, maxToolResultLen)
	if maxTokens <= 0 {
		maxTokens = defaultCompactionSummaryMaxTokens
	}
	req := llm.CompactRequest{
		Model:        r.Model,
		Messages:     msgs,
		MaxTokens:    maxTokens,
		Temperature:  0.01,
		SystemPrompt: compactionSystemPrompt,
		UserPrompt:   compactionUserPrompt,
	}
	maxAttempts := defaultCompactionMaxAttempts
	backoff := r.llmRetryBackoff()
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
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

func (r *Runtime) rewriteSessionForCompaction(summary string, decision compactionDecision, scope compactionScope) error {
	if r == nil || r.Session == nil {
		return nil
	}
	rewritten := session.RewriteHistoryWithCompaction(
		scope.Compactable,
		summary,
		decision.KeepEntries,
		decision.TriggerMode,
		false,
	)
	finalHistory := make([]session.SessionEntry, 0, len(scope.ProtectedPrefix)+len(rewritten))
	finalHistory = append(finalHistory, scope.ProtectedPrefix...)
	finalHistory = append(finalHistory, rewritten...)
	r.Session.ReplaceHistory(finalHistory)
	return nil
}

func (r *Runtime) executeCompaction(ctx context.Context, history []session.SessionEntry, scope compactionScope, decision compactionDecision) (CompactionResult, error) {
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
	requestedPayload := map[string]any{
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
	if err := r.rewriteSessionForCompaction(summary, decision, scope); err != nil {
		return CompactionResult{}, err
	}
	currentHistory := session.RepairLeadingFragment(r.Session.History())
	currentScope := buildCompactionScope(currentHistory)
	if strings.TrimSpace(string(strategy)) == "" {
		strategy = llm.CompactStrategyUnknown
	}
	completedPayload := map[string]any{
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
