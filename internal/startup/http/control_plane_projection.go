package httpchannel

import (
	"fmt"

	"github.com/Isites/anyai/internal/gateway"
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

func (p *ControlPlane) listRuns() []gateway.Run {
	if p == nil || p.run == nil {
		return nil
	}
	return p.run.ListRuns()
}

func (p *ControlPlane) getRun(runID string) (gateway.Run, bool) {
	if p == nil || p.run == nil {
		return gateway.Run{}, false
	}
	return p.run.GetRun(runID)
}

func (p *ControlPlane) listRunEvents(runID string) []gateway.Event {
	if p == nil || p.run == nil {
		return nil
	}
	return p.run.ListRunEvents(runID)
}

func (p *ControlPlane) getRunTree(runID string) (gateway.RunTree, bool) {
	if p == nil || p.run == nil {
		return gateway.RunTree{}, false
	}
	return p.run.GetRunTree(runID)
}

func (p *ControlPlane) runTree(runID string) ([]gateway.RunNode, bool) {
	if p == nil || p.run == nil {
		return nil, false
	}
	return p.run.RunTree(runID)
}

func (p *ControlPlane) listSessionEvents(agentID, sessionID string) []gateway.Event {
	if p == nil || p.session == nil {
		return nil
	}
	return p.session.ListSessionEvents(agentID, sessionID)
}

func (p *ControlPlane) subscribeSession(agentID, sessionID string) (<-chan gateway.Event, func(), error) {
	if p == nil || p.session == nil {
		return nil, nil, fmt.Errorf("runtime not available")
	}
	return p.session.SubscribeSession(agentID, sessionID)
}

func (p *ControlPlane) listTasks() []gateway.Task {
	if p == nil || p.task == nil {
		return nil
	}
	return p.task.ListTasks()
}

func (p *ControlPlane) getTask(taskID string) (gateway.Task, bool) {
	if p == nil || p.task == nil {
		return gateway.Task{}, false
	}
	return p.task.GetTask(taskID)
}

func (p *ControlPlane) subscribeTask(taskID string) (<-chan gateway.Event, func(), error) {
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
