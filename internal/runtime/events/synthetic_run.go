package runtimeevents

import (
	"strings"

	tools "github.com/Isites/anyai/internal/runtime/tool"
)

const SystemAgentID = "system"

type SyntheticRunSpec struct {
	RunID     string
	AgentID   string
	SessionID string
	Model     string
	Channel   string
}

func StartSyntheticRun(recorder *Recorder, spec SyntheticRunSpec) (RunRecord, bool) {
	if recorder == nil {
		return RunRecord{}, false
	}

	runID := strings.TrimSpace(spec.RunID)
	if runID == "" {
		runID = tools.NewRunID()
	}

	run := RunRecord{
		ID:        runID,
		AgentID:   firstNonEmptyString(spec.AgentID, SystemAgentID),
		SessionID: strings.TrimSpace(spec.SessionID),
		Model:     firstNonEmptyString(spec.Model, "system/synthetic"),
		Channel:   firstNonEmptyString(spec.Channel, "system"),
		Status:    RunStatusQueued,
	}
	recorder.StartRun(run)
	run.Status = RunStatusRunning
	recorder.BeginRun(run)
	AppendRunEvent(recorder, run, EventRunStarted, nil)
	return run, true
}

func AppendRunEvent(recorder *Recorder, run RunRecord, name string, payload map[string]any) bool {
	if recorder == nil || strings.TrimSpace(run.ID) == "" || strings.TrimSpace(name) == "" {
		return false
	}
	recorder.AppendEvent(EventRecord{
		RunID:     run.ID,
		AgentID:   run.AgentID,
		SessionID: run.SessionID,
		Name:      strings.TrimSpace(name),
		Payload:   payload,
	})
	return true
}

func FinishSyntheticRun(recorder *Recorder, run RunRecord, output, errMsg string) bool {
	if recorder == nil || strings.TrimSpace(run.ID) == "" {
		return false
	}
	status := RunStatusCompleted
	if strings.TrimSpace(errMsg) != "" {
		status = RunStatusFailed
	}
	recorder.FinishRun(run.ID, status, strings.TrimSpace(output), strings.TrimSpace(errMsg))
	return true
}

func AppendSyntheticRunEvent(
	recorder *Recorder,
	spec SyntheticRunSpec,
	name string,
	payload map[string]any,
	output, errMsg string,
) bool {
	run, ok := StartSyntheticRun(recorder, spec)
	if !ok {
		return false
	}
	AppendRunEvent(recorder, run, name, payload)
	FinishSyntheticRun(recorder, run, output, errMsg)
	return true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
