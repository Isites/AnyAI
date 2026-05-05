package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/tool"
)

const (
	defaultToolMaxAttempts          = 1
	defaultToolRetryBackoffMS       = 750
	defaultToolLoopHistorySize      = 24
	defaultToolLoopWarningThreshold = 4
	defaultToolLoopBlockThreshold   = 6
	maxToolHashPreviewChars         = 512
)

type ToolRecoveryConfig struct {
	MaxAttempts    int
	RetryBackoffMS int
	LoopDetection  ToolLoopDetectionConfig
}

type ToolLoopDetectionConfig struct {
	Enabled          bool
	HistorySize      int
	WarningThreshold int
	BlockThreshold   int
}

type toolLoopRecord struct {
	ToolName    string
	ArgsHash    string
	OutcomeHash string
}

type toolLoopState struct {
	mu              sync.Mutex
	history         []toolLoopRecord
	warningCounters map[string]int
}

type toolRecoveryMetadata struct {
	ToolName           string
	InputSummary       string
	ErrorMessage       string
	ErrorClass         string
	AutoRetryable      bool
	ModelRecoverable   bool
	SuggestedNextMoves []string
	Attempt            int
	MaxAttempts        int
	RetryCount         int
	Decision           string
	Warning            *ToolWarningInfo
}

func (r *Runtime) toolRecoveryConfig() ToolRecoveryConfig {
	cfg := r.ToolRecovery
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultToolMaxAttempts
	}
	if cfg.RetryBackoffMS <= 0 {
		cfg.RetryBackoffMS = defaultToolRetryBackoffMS
	}
	if cfg.LoopDetection.HistorySize <= 0 {
		cfg.LoopDetection.HistorySize = defaultToolLoopHistorySize
	}
	if cfg.LoopDetection.WarningThreshold <= 0 {
		cfg.LoopDetection.WarningThreshold = defaultToolLoopWarningThreshold
	}
	if cfg.LoopDetection.BlockThreshold <= 0 {
		cfg.LoopDetection.BlockThreshold = defaultToolLoopBlockThreshold
	}
	if cfg.LoopDetection.BlockThreshold < cfg.LoopDetection.WarningThreshold {
		cfg.LoopDetection.BlockThreshold = cfg.LoopDetection.WarningThreshold
	}
	// Enable loop detection by default only when the runtime did not receive any
	// explicit loop-detection overrides.
	if !cfg.LoopDetection.Enabled &&
		r.ToolRecovery.LoopDetection.HistorySize == 0 &&
		r.ToolRecovery.LoopDetection.WarningThreshold == 0 &&
		r.ToolRecovery.LoopDetection.BlockThreshold == 0 {
		cfg.LoopDetection.Enabled = true
	}
	return cfg
}

func newToolLoopState() *toolLoopState {
	return &toolLoopState{
		warningCounters: make(map[string]int),
	}
}

func (r *Runtime) executeToolWithRecovery(
	ctx context.Context,
	call llm.ToolCall,
	emit func(AgentEvent),
	loopState *toolLoopState,
	turn int,
) (tools.ToolResult, error) {
	cfg := r.toolRecoveryConfig()
	preflightMeta := map[string]any(nil)
	if repairedCall, meta := r.maybeRepairToolCall(call); meta != nil {
		call = repairedCall
		preflightMeta = meta
	}

	var warning *ToolWarningInfo
	if loopState != nil {
		var err error
		warning, err = r.detectToolLoopWithHooks(ctx, turn, loopState, call, cfg.LoopDetection)
		if err != nil {
			return tools.ToolResult{}, err
		}
		if warning != nil {
			if emit != nil {
				emit(AgentEvent{
					Type:        EventToolWarning,
					ToolCall:    &call,
					ToolWarning: warning,
				})
			}
			if warning.Blocked {
				result := tools.SanitizeToolResult(blockedToolResult(call, cfg, warning))
				loopState.record(call, result, cfg.LoopDetection.HistorySize)
				return result, nil
			}
		}
	}

	maxAttempts := cfg.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	retryCount := 0
	var last tools.ToolResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		callState, err := r.applyBeforeToolCallHooks(ctx, ToolCallState{
			Turn: turn,
			Call: call,
		})
		if err != nil {
			return tools.ToolResult{}, fmt.Errorf("before_tool_call hook failed: %w", err)
		}
		if callState.BlockedResult != nil {
			last = tools.SanitizeToolResult(*callState.BlockedResult)
			break
		}

		call = callState.State.Call
		result, execErr := r.executeToolAttempt(ctx, call)
		afterState, err := r.applyAfterToolCallHooks(ctx, AfterToolCallState{
			Turn:    turn,
			Call:    call,
			Result:  result,
			Err:     execErr,
			Attempt: attempt,
		})
		if err != nil {
			return tools.ToolResult{}, fmt.Errorf("after_tool_call hook failed: %w", err)
		}
		result = afterState.Result
		execErr = afterState.Err

		metadata := buildToolRecoveryMetadata(call, result, execErr, attempt, maxAttempts, retryCount, warning)
		result = mergeToolRecoveryMetadata(result, metadata)
		if len(preflightMeta) > 0 {
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			for key, value := range preflightMeta {
				result.Metadata[key] = value
			}
		}
		shapeState, err := r.applyToolResultShapeHooks(ctx, ToolResultShapeState{
			Turn:        turn,
			Call:        call,
			Result:      result,
			Attempt:     attempt,
			MaxAttempts: maxAttempts,
			RetryCount:  retryCount,
			Warning:     warning,
		})
		if err != nil {
			return tools.ToolResult{}, fmt.Errorf("tool_result_shape hook failed: %w", err)
		}
		result = shapeState.Result
		result = tools.SanitizeToolResult(result)
		last = result

		if !shouldRetryToolFailure(result) || attempt >= maxAttempts {
			break
		}

		retryCount++
		wait := toolRetryDelay(time.Duration(cfg.RetryBackoffMS)*time.Millisecond, attempt)
		if emit != nil {
			emit(AgentEvent{
				Type:     EventToolRetry,
				ToolCall: &call,
				ToolRetry: &ToolRetryInfo{
					ToolName:    call.Name,
					Attempt:     attempt + 1,
					MaxAttempts: maxAttempts,
					WaitMS:      int(wait / time.Millisecond),
					ErrorClass:  metadata.ErrorClass,
					Error:       metadata.ErrorMessage,
					Decision:    "retry",
				},
			})
		}
		if err := waitForToolRetry(ctx, wait); err != nil {
			break
		}
	}

	if loopState != nil {
		loopState.record(call, last, cfg.LoopDetection.HistorySize)
	}

	return last, nil
}

func (r *Runtime) detectToolLoopWithHooks(
	ctx context.Context,
	turn int,
	loopState *toolLoopState,
	call llm.ToolCall,
	cfg ToolLoopDetectionConfig,
) (*ToolWarningInfo, error) {
	if warning := detectToolLoop(loopState, call, cfg); warning != nil {
		return warning, nil
	}
	return r.applyLoopDetectHooks(ctx, LoopDetectState{
		Turn:      turn,
		Call:      call,
		LoopState: loopState,
		Config:    cfg,
	})
}

func (r *Runtime) executeToolAttempt(ctx context.Context, call llm.ToolCall) (tools.ToolResult, error) {
	meta := tools.RuntimeContextFrom(ctx)
	toolCtx := tools.WithRuntimeContext(ctx, tools.RuntimeContext{
		RunID:          meta.RunID,
		AgentID:        r.AgentID,
		SessionID:      r.Session.ID,
		TaskID:         meta.TaskID,
		ToolCallID:     call.ID,
		CurrentRequest: meta.CurrentRequest,
		CallChain:      meta.CallChain,
		Depth:          meta.Depth,
	})

	result, err := r.Tools.Execute(toolCtx, call.Name, call.Input)
	if err != nil {
		return result, err
	}
	return result, nil
}

func buildToolRecoveryMetadata(
	call llm.ToolCall,
	result tools.ToolResult,
	execErr error,
	attempt, maxAttempts, retryCount int,
	warning *ToolWarningInfo,
) toolRecoveryMetadata {
	errorMessage := strings.TrimSpace(result.Error)
	if errorMessage == "" && execErr != nil {
		errorMessage = strings.TrimSpace(execErr.Error())
	}
	errorClass, autoRetryable, modelRecoverable := classifyToolFailure(call.Name, errorMessage)
	if warning != nil && warning.Blocked {
		errorClass = "loop_detected"
		autoRetryable = false
		modelRecoverable = true
	}
	if errorClass == "" && warning != nil {
		errorClass = "loop_detected"
		modelRecoverable = true
	}

	decision := "completed"
	if errorMessage != "" {
		decision = "final"
		if autoRetryable && attempt < maxAttempts {
			decision = "retrying"
		}
	}
	if retryCount > 0 && errorMessage == "" {
		decision = "completed_after_retry"
	}

	return toolRecoveryMetadata{
		ToolName:           call.Name,
		InputSummary:       summarizeToolInput(call.Input),
		ErrorMessage:       errorMessage,
		ErrorClass:         errorClass,
		AutoRetryable:      autoRetryable,
		ModelRecoverable:   modelRecoverable,
		SuggestedNextMoves: suggestedNextMoves(call.Name, errorClass),
		Attempt:            attempt,
		MaxAttempts:        maxAttempts,
		RetryCount:         retryCount,
		Decision:           decision,
		Warning:            warning,
	}
}

func mergeToolRecoveryMetadata(result tools.ToolResult, meta toolRecoveryMetadata) tools.ToolResult {
	if meta.ErrorMessage == "" && meta.Warning == nil && meta.RetryCount == 0 {
		return result
	}
	merged := make(map[string]any, len(result.Metadata)+10)
	for key, value := range result.Metadata {
		merged[key] = value
	}
	if meta.ToolName != "" {
		merged["tool_name"] = meta.ToolName
	}
	if meta.InputSummary != "" {
		merged["input_summary"] = meta.InputSummary
	}
	if meta.ErrorMessage != "" {
		merged["error_message"] = meta.ErrorMessage
	}
	if meta.ErrorClass != "" {
		merged["error_class"] = meta.ErrorClass
	}
	if meta.ErrorMessage != "" || meta.Warning != nil {
		merged["auto_retryable"] = meta.AutoRetryable
		merged["model_recoverable"] = meta.ModelRecoverable
	}
	if len(meta.SuggestedNextMoves) > 0 && (meta.ErrorMessage != "" || meta.Warning != nil) {
		merged["suggested_next_moves"] = append([]string(nil), meta.SuggestedNextMoves...)
	}
	if meta.Attempt > 0 {
		merged["attempt"] = meta.Attempt
	}
	if meta.MaxAttempts > 0 {
		merged["max_attempts"] = meta.MaxAttempts
	}
	if meta.RetryCount > 0 {
		merged["retry_count"] = meta.RetryCount
	}
	if meta.Decision != "" {
		merged["decision"] = meta.Decision
	}
	if meta.Warning != nil {
		merged["warning"] = map[string]any{
			"tool_name": meta.Warning.ToolName,
			"detector":  meta.Warning.Detector,
			"count":     meta.Warning.Count,
			"message":   meta.Warning.Message,
			"blocked":   meta.Warning.Blocked,
		}
	}
	result.Metadata = merged
	if strings.TrimSpace(result.Error) == "" && meta.ErrorMessage != "" {
		result.Error = meta.ErrorMessage
	}
	return result
}

func shouldRetryToolFailure(result tools.ToolResult) bool {
	if strings.TrimSpace(result.Error) == "" {
		return false
	}
	retryable, ok := metadataBool(result.Metadata, "auto_retryable")
	return ok && retryable
}

func blockedToolResult(call llm.ToolCall, cfg ToolRecoveryConfig, warning *ToolWarningInfo) tools.ToolResult {
	result := tools.ToolResult{
		Error: warning.Message,
	}
	return mergeToolRecoveryMetadata(result, toolRecoveryMetadata{
		ToolName:           call.Name,
		InputSummary:       summarizeToolInput(call.Input),
		ErrorMessage:       warning.Message,
		ErrorClass:         "loop_detected",
		AutoRetryable:      false,
		ModelRecoverable:   true,
		SuggestedNextMoves: suggestedNextMoves(call.Name, "loop_detected"),
		Attempt:            1,
		MaxAttempts:        cfg.MaxAttempts,
		Decision:           "blocked",
		Warning:            warning,
	})
}

func summarizeToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	sanitized := string(tools.SanitizeRawJSON(raw))
	sanitized = strings.TrimSpace(sanitized)
	if len(sanitized) > maxToolHashPreviewChars {
		return sanitized[:maxToolHashPreviewChars] + "...(truncated)"
	}
	return sanitized
}

func classifyToolFailure(toolName, errMsg string) (string, bool, bool) {
	errLower := strings.ToLower(strings.TrimSpace(errMsg))
	if errLower == "" {
		return "", false, false
	}
	switch {
	case strings.Contains(errLower, "timed out"),
		strings.Contains(errLower, "timeout"),
		strings.Contains(errLower, "deadline exceeded"),
		strings.Contains(errLower, "context deadline exceeded"):
		return "timeout", true, true
	case strings.Contains(errLower, "is a directory"):
		return "path_is_directory", false, true
	case strings.Contains(errLower, "permission denied"),
		strings.Contains(errLower, "not allowed by policy"),
		strings.Contains(errLower, "disabled by policy"),
		strings.Contains(errLower, "allowlist"):
		return "permission_denied", false, true
	case isPathNotFound(toolName, errLower):
		return "path_not_found", false, true
	case strings.Contains(errLower, "dial tcp"),
		strings.Contains(errLower, "connection refused"),
		strings.Contains(errLower, "connection reset"),
		strings.Contains(errLower, "tls handshake timeout"),
		strings.Contains(errLower, "temporary network"),
		errLower == "eof":
		return "network_error", true, true
	case strings.Contains(errLower, "rate limit"),
		strings.Contains(errLower, "too many requests"),
		strings.Contains(errLower, "service unavailable"),
		strings.Contains(errLower, "bad gateway"),
		strings.Contains(errLower, "gateway timeout"),
		strings.Contains(errLower, "overloaded"),
		strings.Contains(errLower, "try again later"),
		strings.Contains(errLower, "temporarily unavailable"),
		strings.Contains(errLower, " 429"),
		strings.Contains(errLower, " 502"),
		strings.Contains(errLower, " 503"),
		strings.Contains(errLower, " 504"):
		return "transient_provider_error", true, true
	case strings.Contains(errLower, "invalid input"),
		strings.Contains(errLower, "required"),
		strings.Contains(errLower, "not found in file"),
		strings.Contains(errLower, "must be unique"),
		strings.Contains(errLower, "unknown tool"),
		strings.Contains(errLower, "task \""),
		strings.Contains(errLower, "not available"):
		return "validation_error", false, true
	case strings.Contains(errLower, "loop"), strings.Contains(errLower, "no progress"):
		return "loop_detected", false, true
	default:
		return "unknown", false, true
	}
}

func isPathNotFound(toolName, errLower string) bool {
	if strings.Contains(errLower, "no such file") || strings.Contains(errLower, "cannot find the file") {
		return true
	}
	if toolName == "read_file" || toolName == "write_file" || toolName == "edit_file" || toolName == "save_output" {
		return strings.Contains(errLower, "failed to read file") ||
			strings.Contains(errLower, "failed to write file") ||
			strings.Contains(errLower, "file does not exist")
	}
	return false
}

func suggestedNextMoves(toolName, errorClass string) []string {
	switch errorClass {
	case "path_is_directory":
		return []string{
			"Use `bash` to inspect the directory contents instead of trying to read it as a file.",
			"Choose a concrete file path, preferably an absolute path rooted at the injected workspace or project root.",
		}
	case "path_not_found":
		return []string{
			"Check whether the path is relative to the agent workspace and switch to an absolute path if needed.",
			"Use `bash` to verify the file exists before calling the file tool again.",
		}
	case "permission_denied":
		return []string{
			"Choose a tool or path that is allowed in the current runtime policy.",
			"Explain the permission boundary clearly and continue with other verifiable work when possible.",
		}
	case "validation_error":
		return []string{
			"Read the exact parameter error and fix the arguments before retrying.",
			"Switch tools if another tool matches the task more directly.",
		}
	case "timeout":
		return []string{
			"Reduce the scope, split the work into smaller calls, or retry with a shorter-running command.",
			"Use `bash` plus a short script when the task is repetitive or easier to control programmatically.",
		}
	case "network_error", "transient_provider_error":
		return []string{
			"Retry after a short wait because the failure looks transient.",
			"If transient retries keep failing, switch methods or continue with the parts that can still be verified locally.",
		}
	case "loop_detected":
		return []string{
			"Stop repeating the same tool call unchanged; change arguments, switch tools, or report the blockage explicitly.",
			"Use the most recent tool error or result to decide on a different next step.",
		}
	default:
		moves := []string{
			"Inspect the exact error and adjust the next step instead of repeating the same call unchanged.",
			"Switch tools or use `bash` when it gives you more control over the task.",
		}
		if toolName == "read_file" {
			moves = append(moves, "If the path might be a directory, use `bash` first to inspect it.")
		}
		return moves
	}
}

func toolRetryDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = time.Duration(defaultToolRetryBackoffMS) * time.Millisecond
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxLLMRetryBackoffMS*time.Millisecond {
			return maxLLMRetryBackoffMS * time.Millisecond
		}
	}
	return delay
}

func waitForToolRetry(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func detectToolLoop(state *toolLoopState, call llm.ToolCall, cfg ToolLoopDetectionConfig) *ToolWarningInfo {
	if state == nil || !cfg.Enabled {
		return nil
	}

	argsHash := hashToolCall(call.Name, call.Input)
	noProgress := state.noProgressStreak(call.Name, argsHash) + 1
	if noProgress >= cfg.BlockThreshold {
		return &ToolWarningInfo{
			ToolName: call.Name,
			Detector: "generic_repeat",
			Count:    noProgress,
			Message:  fmt.Sprintf("tool call blocked: %s has repeated the same no-progress outcome %d times; change arguments or switch methods", call.Name, noProgress),
			Blocked:  true,
		}
	}

	if knownPollTool(call.Name) {
		if noProgress >= cfg.WarningThreshold && state.shouldEmitWarning("poll:"+argsHash, noProgress) {
			return &ToolWarningInfo{
				ToolName: call.Name,
				Detector: "known_poll_no_progress",
				Count:    noProgress,
				Message:  fmt.Sprintf("tool warning: %s has been polled %d times with no progress; stop polling unchanged and choose a different action", call.Name, noProgress),
				Blocked:  false,
			}
		}
	}

	pingPongCount, pairedTool, noProgressPingPong := state.pingPongStreak(argsHash)
	if noProgressPingPong && pingPongCount >= cfg.BlockThreshold {
		return &ToolWarningInfo{
			ToolName: call.Name,
			Detector: "ping_pong",
			Count:    pingPongCount,
			Message:  fmt.Sprintf("tool call blocked: alternating between %s and %s is not making progress; stop retrying unchanged", call.Name, pairedTool),
			Blocked:  true,
		}
	}
	if pingPongCount >= cfg.WarningThreshold && state.shouldEmitWarning("pingpong:"+argsHash, pingPongCount) {
		return &ToolWarningInfo{
			ToolName: call.Name,
			Detector: "ping_pong",
			Count:    pingPongCount,
			Message:  fmt.Sprintf("tool warning: alternating tool calls are repeating without visible progress; adjust the strategy before continuing"),
			Blocked:  false,
		}
	}

	recentCount := state.recentCount(call.Name, argsHash) + 1
	if recentCount >= cfg.WarningThreshold && state.shouldEmitWarning("generic:"+argsHash, recentCount) {
		return &ToolWarningInfo{
			ToolName: call.Name,
			Detector: "generic_repeat",
			Count:    recentCount,
			Message:  fmt.Sprintf("tool warning: %s has been called %d times with identical arguments; if this is not progressing, change methods", call.Name, recentCount),
			Blocked:  false,
		}
	}

	return nil
}

func (s *toolLoopState) record(call llm.ToolCall, result tools.ToolResult, historySize int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := toolLoopRecord{
		ToolName:    call.Name,
		ArgsHash:    hashToolCall(call.Name, call.Input),
		OutcomeHash: hashToolOutcome(result),
	}
	s.history = append(s.history, record)

	cfgHistorySize := historySize
	if cfgHistorySize <= 0 {
		cfgHistorySize = defaultToolLoopHistorySize
	}
	if len(s.history) > cfgHistorySize {
		s.history = append([]toolLoopRecord(nil), s.history[len(s.history)-cfgHistorySize:]...)
	}
}

func (s *toolLoopState) recentCount(toolName, argsHash string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	for _, item := range s.history {
		if item.ToolName == toolName && item.ArgsHash == argsHash {
			count++
		}
	}
	return count
}

func (s *toolLoopState) noProgressStreak(toolName, argsHash string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	var latestOutcome string
	for i := len(s.history) - 1; i >= 0; i-- {
		item := s.history[i]
		if item.ToolName != toolName || item.ArgsHash != argsHash {
			continue
		}
		if latestOutcome == "" {
			latestOutcome = item.OutcomeHash
			if latestOutcome == "" {
				return 0
			}
			count = 1
			continue
		}
		if item.OutcomeHash != latestOutcome {
			break
		}
		count++
	}
	return count
}

func (s *toolLoopState) pingPongStreak(currentHash string) (count int, pairedTool string, noProgress bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) < 2 {
		return 0, "", false
	}
	last := s.history[len(s.history)-1]
	var other toolLoopRecord
	foundOther := false
	for i := len(s.history) - 2; i >= 0; i-- {
		if s.history[i].ArgsHash == last.ArgsHash {
			continue
		}
		other = s.history[i]
		foundOther = true
		break
	}
	if !foundOther || currentHash != other.ArgsHash {
		return 0, "", false
	}

	expected := last.ArgsHash
	altCount := 0
	noProgress = true
	outcomeByHash := map[string]string{}
	for i := len(s.history) - 1; i >= 0; i-- {
		item := s.history[i]
		if item.ArgsHash != expected {
			break
		}
		if existing, ok := outcomeByHash[item.ArgsHash]; ok && existing != item.OutcomeHash {
			noProgress = false
		}
		if _, ok := outcomeByHash[item.ArgsHash]; !ok {
			outcomeByHash[item.ArgsHash] = item.OutcomeHash
		}
		altCount++
		if expected == last.ArgsHash {
			expected = other.ArgsHash
		} else {
			expected = last.ArgsHash
		}
	}
	if altCount < 2 {
		return 0, "", false
	}
	if len(outcomeByHash) < 2 {
		noProgress = false
	}
	return altCount + 1, last.ToolName, noProgress
}

func (s *toolLoopState) shouldEmitWarning(key string, count int) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lastCount := s.warningCounters[key]
	if count <= lastCount {
		return false
	}
	s.warningCounters[key] = count
	return true
}

func hashToolCall(toolName string, input json.RawMessage) string {
	return hashStrings(toolName, canonicalizeJSON(input))
}

func hashToolOutcome(result tools.ToolResult) string {
	if strings.TrimSpace(result.Error) != "" {
		class, _, _ := classifyToolFailure("", result.Error)
		return hashStrings("error", class, previewHashText(result.Error))
	}
	if strings.TrimSpace(result.Output) != "" {
		return hashStrings("output", previewHashText(result.Output))
	}
	if len(result.Metadata) > 0 {
		encoded, _ := json.Marshal(result.Metadata)
		return hashStrings("metadata", string(encoded))
	}
	return hashStrings("empty")
}

func canonicalizeJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return trimmed
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return trimmed
	}
	return string(encoded)
}

func hashStrings(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func previewHashText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > maxToolHashPreviewChars {
		return text[:maxToolHashPreviewChars]
	}
	return text
}

func knownPollTool(toolName string) bool {
	return false
}

func metadataBool(metadata map[string]any, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	value, ok := metadata[key]
	if !ok {
		return false, false
	}
	v, ok := value.(bool)
	return v, ok
}
