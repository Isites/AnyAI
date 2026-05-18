package task

import (
	"context"
	"strings"
	"time"
)

const (
	defaultAgentTaskMaxAttempts = 3
	defaultTaskRetryBackoffMS   = 750
	retryMetadataMaxAttempts    = "retry_max_attempts"
	retryMetadataBackoffMS      = "retry_backoff_ms"
	retryMetadataSafe           = "retry_safe"
	retryMetadataAutoRetryable  = "auto_retryable"
	taskLifecycleStepRetry      = "retry"
)

func taskMaxAttempts(record Record) int {
	if attempts := metadataInt(record.Metadata, retryMetadataMaxAttempts); attempts > 0 {
		return attempts
	}
	switch record.Kind {
	case KindAgent:
		return defaultAgentTaskMaxAttempts
	default:
		return 1
	}
}

func taskRetryBackoff(record Record) time.Duration {
	if ms := metadataInt(record.Metadata, retryMetadataBackoffMS); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	switch record.Kind {
	case KindAgent:
		return defaultTaskRetryBackoffMS * time.Millisecond
	default:
		return 0
	}
}

func shouldRetryTask(record Record, result Result, err error, attempt, maxAttempts int, timedOut bool) bool {
	if attempt >= maxAttempts || maxAttempts <= 1 {
		return false
	}
	if result.Status == StatusCompleted {
		return false
	}
	switch record.Kind {
	case KindTool:
		return metadataBool(record.Metadata, retryMetadataAutoRetryable)
	case KindProcess:
		if !metadataBool(record.Metadata, retryMetadataSafe) {
			return false
		}
	}
	if timedOut {
		return true
	}
	if err != nil {
		if err == context.Canceled && !timedOut {
			return false
		}
		if taskErrorRetryable(err.Error()) {
			return true
		}
	}
	return taskErrorRetryable(result.Error)
}

func normalizeAttemptResult(result Result, err error, timedOut bool) (Result, error) {
	if !timedOut {
		return result, err
	}
	if result.Status == "" || result.Status == StatusCancelled {
		result.Status = StatusFailed
	}
	if strings.TrimSpace(result.Error) == "" || strings.TrimSpace(result.Error) == context.Canceled.Error() {
		result.Error = context.DeadlineExceeded.Error()
	}
	if err == nil || err == context.Canceled {
		err = context.DeadlineExceeded
	}
	return result, err
}

func taskRetryDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= 8*time.Second {
			return 8 * time.Second
		}
	}
	return delay
}

func waitForTaskRetry(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func taskErrorRetryable(message string) bool {
	text := strings.ToLower(strings.TrimSpace(message))
	if text == "" {
		return false
	}
	switch {
	case strings.Contains(text, "timeout"),
		strings.Contains(text, "timed out"),
		strings.Contains(text, "deadline exceeded"),
		strings.Contains(text, "context deadline exceeded"),
		strings.Contains(text, "connection refused"),
		strings.Contains(text, "connection reset"),
		strings.Contains(text, "temporary network"),
		strings.Contains(text, "service unavailable"),
		strings.Contains(text, "try again later"),
		strings.Contains(text, "temporarily unavailable"),
		strings.Contains(text, "bad gateway"),
		strings.Contains(text, "gateway timeout"),
		strings.Contains(text, "overloaded"),
		strings.Contains(text, "too many requests"),
		strings.Contains(text, "rate limit"),
		strings.Contains(text, " 429"),
		strings.Contains(text, " 502"),
		strings.Contains(text, " 503"),
		strings.Contains(text, " 504"),
		text == "eof":
		return true
	default:
		return false
	}
}

func metadataInt(metadata map[string]any, key string) int {
	if len(metadata) == 0 {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	value, _ := metadata[key].(bool)
	return value
}
