package httpchannel

import (
	"fmt"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/task"
)

func (p *ControlPlane) rebuildProjections() error {
	if p == nil || p.runtime == nil {
		return fmt.Errorf("runtime not available")
	}
	return p.runtime.RebuildEventProjections()
}

func (p *ControlPlane) eventStorageDir() string {
	if p == nil || p.runtime == nil {
		return ""
	}
	return p.runtime.EventStorageDir()
}

func (p *ControlPlane) listRuns() []runtimeevents.RunRecord {
	if p == nil || p.run == nil {
		return nil
	}
	return p.run.ListRuns()
}

func (p *ControlPlane) getRun(runID string) (runtimeevents.RunRecord, bool) {
	if p == nil || p.run == nil {
		return runtimeevents.RunRecord{}, false
	}
	return p.run.GetRun(runID)
}

func (p *ControlPlane) listRunEvents(runID string) []runtimeevents.EventRecord {
	if p == nil || p.run == nil {
		return nil
	}
	return p.run.ListRunEvents(runID)
}

func (p *ControlPlane) getRunTree(runID string) (runtimeevents.RunTreeRecord, bool) {
	if p == nil || p.run == nil {
		return runtimeevents.RunTreeRecord{}, false
	}
	return p.run.GetRunTree(runID)
}

func (p *ControlPlane) runTree(runID string) ([]runtimeevents.RunNode, bool) {
	if p == nil || p.run == nil {
		return nil, false
	}
	return p.run.RunTree(runID)
}

func (p *ControlPlane) listSessionEvents(agentID, sessionID string) []runtimeevents.EventRecord {
	if p == nil || p.session == nil {
		return nil
	}
	return p.session.ListSessionEvents(agentID, sessionID)
}

func (p *ControlPlane) subscribeSession(agentID, sessionID string) (<-chan runtimeevents.EventRecord, func(), error) {
	if p == nil || p.session == nil {
		return nil, nil, fmt.Errorf("runtime not available")
	}
	return p.session.SubscribeSession(agentID, sessionID)
}

func (p *ControlPlane) listTasks() []task.Info {
	if p == nil || p.task == nil {
		return nil
	}
	return p.task.ListTasks()
}

func (p *ControlPlane) getTask(taskID string) (task.Info, bool) {
	if p == nil || p.task == nil {
		return task.Info{}, false
	}
	return p.task.GetTask(taskID)
}

func (p *ControlPlane) subscribeTask(taskID string) (<-chan runtimeevents.EventRecord, func(), error) {
	if p == nil || p.task == nil {
		return nil, nil, fmt.Errorf("runtime not available")
	}
	return p.task.SubscribeTask(taskID)
}

func (p *ControlPlane) cancelTask(taskID string) error {
	if p == nil || p.task == nil {
		return fmt.Errorf("runtime not available")
	}
	return p.task.CancelTask(taskID)
}
