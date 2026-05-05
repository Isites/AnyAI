package examples

import (
	"strconv"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
)

func isToolStartEvent(name string) bool {
	switch strings.TrimSpace(name) {
	case "tool.call.started", "tool.called":
		return true
	default:
		return false
	}
}

func waitForRecordedRunTree(t *testing.T, recorder *runtimeevents.Recorder, runID string, timeout, settle time.Duration) runtimeevents.RunTreeRecord {
	t.Helper()

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if settle <= 0 {
		settle = 150 * time.Millisecond
	}

	deadline := time.Now().Add(timeout)
	var (
		latest     runtimeevents.RunTreeRecord
		haveTree   bool
		lastShape  string
		stableFrom time.Time
	)

	for time.Now().Before(deadline) {
		tree, ok := recorder.GetRunTree(runID)
		if ok {
			haveTree = true
			latest = tree
			shape := traceShape(tree)
			if shape != lastShape {
				lastShape = shape
				stableFrom = time.Now()
			} else if !stableFrom.IsZero() && time.Since(stableFrom) >= settle {
				return tree
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if haveTree {
		return latest
	}

	t.Fatalf("recorded run tree %s did not appear in time", runID)
	return runtimeevents.RunTreeRecord{}
}

func traceShape(trace runtimeevents.RunTreeRecord) string {
	return strings.Join([]string{
		"runs=" + strconv.Itoa(len(trace.Runs)),
		"events=" + strconv.Itoa(len(trace.Events)),
	}, ";")
}
