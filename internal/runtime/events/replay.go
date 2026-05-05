package runtimeevents

import (
	"sort"
	"strings"
	"time"
)

// ReplayRunEvents augments persisted run events into a consumer-facing replay
// stream. Transient text deltas are not persisted, so terminal runs replay
// their final output as one synthetic text.delta when needed.
func ReplayRunEvents(run RunRecord, events []EventRecord) []EventRecord {
	if strings.TrimSpace(run.ID) == "" && len(events) == 0 {
		return nil
	}

	replay := append([]EventRecord(nil), events...)
	hasTextDelta := false
	terminalIndex := -1
	hasTerminal := false
	terminalTimestamp := time.Time{}

	for i, event := range replay {
		switch strings.TrimSpace(event.Name) {
		case EventTextDelta:
			hasTextDelta = true
		case EventRunCompleted, EventRunFailed, EventRunAborted:
			if terminalIndex < 0 {
				terminalIndex = i
				terminalTimestamp = event.Timestamp
			}
			hasTerminal = true
		}
	}

	changed := false
	if IsTerminalStatus(run.Status) && strings.TrimSpace(run.Output) != "" && !hasTextDelta {
		replay = insertReplayEvent(replay, terminalIndex, synthReplayTextDelta(run, terminalTimestamp))
		changed = true
		if terminalIndex >= 0 {
			terminalIndex++
		}
	}
	if IsTerminalStatus(run.Status) && !hasTerminal {
		replay = append(replay, synthReplayTerminalEvent(run))
		changed = true
	}
	if !changed {
		return replay
	}
	return resequenceReplayEvents(replay)
}

// ReplayRunTreeRecord augments a persisted run tree into a consumer-facing
// replay view by applying run replay semantics to each constituent run before
// the tree-level event stream is sorted and returned.
func ReplayRunTreeRecord(tree RunTreeRecord) RunTreeRecord {
	if len(tree.Runs) == 0 && len(tree.Events) == 0 {
		return tree
	}

	out := RunTreeRecord{
		Runs: append([]RunRecord(nil), tree.Runs...),
	}

	runEvents := make(map[string][]EventRecord, len(tree.Runs))
	var orphaned []EventRecord
	for _, event := range tree.Events {
		runID := strings.TrimSpace(event.RunID)
		if runID == "" {
			orphaned = append(orphaned, event)
			continue
		}
		runEvents[runID] = append(runEvents[runID], event)
	}
	for _, run := range out.Runs {
		out.Events = append(out.Events, ReplayRunEvents(run, runEvents[run.ID])...)
		delete(runEvents, run.ID)
	}
	for _, events := range runEvents {
		out.Events = append(out.Events, events...)
	}
	out.Events = append(out.Events, orphaned...)
	sortRunTreeReplayEvents(out.Runs, out.Events)
	return out
}

// ReplayRunTree applies run replay semantics to every node in a run tree.
func ReplayRunTree(nodes []RunNode) []RunNode {
	if len(nodes) == 0 {
		return nil
	}
	replayed := make([]RunNode, len(nodes))
	for i, node := range nodes {
		replayed[i] = RunNode{
			Run:      node.Run,
			Events:   ReplayRunEvents(node.Run, node.Events),
			Children: ReplayRunTree(node.Children),
		}
	}
	return replayed
}

func insertReplayEvent(events []EventRecord, index int, event EventRecord) []EventRecord {
	if index < 0 || index >= len(events) {
		return append(events, event)
	}
	events = append(events, EventRecord{})
	copy(events[index+1:], events[index:])
	events[index] = event
	return events
}

func synthReplayTextDelta(run RunRecord, terminalTimestamp time.Time) EventRecord {
	timestamp := terminalTimestamp
	if timestamp.IsZero() {
		timestamp = replayEventTime(run)
	}
	return EventRecord{
		SchemaVersion: CurrentEventSchemaVersion,
		RunID:         run.ID,
		AgentID:       run.AgentID,
		SessionID:     run.SessionID,
		Name:          EventTextDelta,
		Timestamp:     timestamp,
		Payload: map[string]any{
			"text": strings.TrimSpace(run.Output),
		},
	}
}

func synthReplayTerminalEvent(run RunRecord) EventRecord {
	record := EventRecord{
		SchemaVersion: CurrentEventSchemaVersion,
		RunID:         run.ID,
		AgentID:       run.AgentID,
		SessionID:     run.SessionID,
		Timestamp:     replayEventTime(run),
	}
	switch run.Status {
	case RunStatusFailed:
		record.Name = EventRunFailed
		record.Payload = map[string]any{"message": strings.TrimSpace(firstNonEmptyReplay(run.Error, "run failed"))}
	case RunStatusAborted:
		record.Name = EventRunAborted
		record.Payload = map[string]any{"message": strings.TrimSpace(firstNonEmptyReplay(run.Error, "run aborted")), "reason": "aborted"}
	default:
		record.Name = EventRunCompleted
	}
	return record
}

func replayEventTime(run RunRecord) time.Time {
	switch {
	case !run.CompletedAt.IsZero():
		return run.CompletedAt
	case !run.StartedAt.IsZero():
		return run.StartedAt
	case !run.CreatedAt.IsZero():
		return run.CreatedAt
	default:
		return time.Now().UTC()
	}
}

func resequenceReplayEvents(events []EventRecord) []EventRecord {
	for i := range events {
		events[i].SchemaVersion = CurrentEventSchemaVersion
		events[i].Sequence = i + 1
		if events[i].Timestamp.IsZero() {
			events[i].Timestamp = time.Now().UTC()
		}
	}
	return events
}

func sortReplayEvents(events []EventRecord) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].Timestamp.Equal(events[j].Timestamp) {
			if events[i].RunID == events[j].RunID {
				return events[i].Sequence < events[j].Sequence
			}
			return events[i].RunID < events[j].RunID
		}
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
}

func sortRunTreeReplayEvents(runs []RunRecord, events []EventRecord) {
	if len(events) == 0 {
		return
	}
	runStartedAt := make(map[string]time.Time, len(runs))
	for _, run := range runs {
		runStartedAt[run.ID] = run.StartedAt
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Timestamp.Equal(events[j].Timestamp) {
			if events[i].RunID == events[j].RunID {
				return events[i].Sequence < events[j].Sequence
			}
			startI := runStartedAt[events[i].RunID]
			startJ := runStartedAt[events[j].RunID]
			if !startI.Equal(startJ) {
				if startI.IsZero() {
					return false
				}
				if startJ.IsZero() {
					return true
				}
				return startI.Before(startJ)
			}
			return events[i].RunID < events[j].RunID
		}
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
}

func firstNonEmptyReplay(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
