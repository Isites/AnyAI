package control

import (
	"fmt"
	"strings"
	"time"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeprojection "github.com/Isites/anyai/internal/runtime/projection"
)

type memoryCleaner interface {
	Cleanup(now time.Time) (int, error)
	Reindex() (int, error)
	PromoteEligible(now time.Time) (int, error)
}

type sessionCompactor interface {
	CompactSession(agentID, sessionID string, keepEntries int) error
}

type Service struct {
	projections *runtimeprojection.Service
	memory      memoryCleaner
	tasks       taskCanceller
	compactor   sessionCompactor
	recorderFn  func() *runtimeevents.Recorder
}

type taskCanceller interface {
	CancelWithReason(id, reason string) bool
}

func NewService(
	projections *runtimeprojection.Service,
	memory memoryCleaner,
	tasks taskCanceller,
	compactor sessionCompactor,
	recorderFn func() *runtimeevents.Recorder,
) *Service {
	return &Service{
		projections: projections,
		memory:      memory,
		tasks:       tasks,
		compactor:   compactor,
		recorderFn:  recorderFn,
	}
}

func (s *Service) RebuildProjections() error {
	if s == nil || s.projections == nil {
		return nil
	}
	if err := s.projections.Rebuild(); err != nil {
		return err
	}
	s.recordSystemEvent("control", "runtime.projections.rebuilt", nil, "projections rebuilt", "")
	return nil
}

func (s *Service) CompactSession(agentID, sessionID string, keepEntries int) error {
	if s != nil && s.compactor != nil {
		return s.compactor.CompactSession(agentID, sessionID, keepEntries)
	}
	if s == nil || s.projections == nil || s.projections.Session == nil {
		return fmt.Errorf("session projection not available")
	}
	_, err := s.projections.Session.Compact(agentID, sessionID, keepEntries)
	return err
}

func (s *Service) MemoryStaleCleanup(now time.Time) (int, error) {
	if s == nil || s.memory == nil {
		return 0, fmt.Errorf("memory maintenance not available")
	}
	removed, err := s.memory.Cleanup(now)
	if err != nil {
		return 0, err
	}
	s.recordSystemEvent("maintenance", "maintenance.memory.stale_cleanup.completed", map[string]any{
		"removed": removed,
		"at":      now.UTC(),
	}, "memory stale cleanup completed", "")
	return removed, nil
}

func (s *Service) MemoryReindex() (int, error) {
	if s == nil || s.memory == nil {
		return 0, fmt.Errorf("memory maintenance not available")
	}
	total, err := s.memory.Reindex()
	if err != nil {
		return 0, err
	}
	s.recordSystemEvent("maintenance", "maintenance.memory.reindexed", map[string]any{
		"total": total,
	}, "memory reindexed", "")
	return total, nil
}

func (s *Service) MemoryPromoteEligible(now time.Time) (int, error) {
	if s == nil || s.memory == nil {
		return 0, fmt.Errorf("memory maintenance not available")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	promoted, err := s.memory.PromoteEligible(now)
	if err != nil {
		return 0, err
	}
	s.recordSystemEvent("maintenance", "maintenance.memory.promotion.completed", map[string]any{
		"promoted": promoted,
		"at":       now.UTC(),
	}, "memory promotion completed", "")
	return promoted, nil
}

func (s *Service) CreateSession(agentID, sessionID string) error {
	if s == nil || s.projections == nil || s.projections.Session == nil {
		return fmt.Errorf("session projection not available")
	}
	return s.projections.Session.Create(agentID, sessionID)
}

func (s *Service) DeleteSession(agentID, sessionID string) error {
	if s == nil || s.projections == nil || s.projections.Session == nil {
		return fmt.Errorf("session projection not available")
	}
	return s.projections.Session.Delete(agentID, sessionID)
}

func (s *Service) CancelTask(taskID string) error {
	if s == nil || s.tasks == nil {
		return fmt.Errorf("task control not available")
	}
	if !s.tasks.CancelWithReason(taskID, "cancelled by control") {
		return fmt.Errorf("task %q not found or not cancellable", strings.TrimSpace(taskID))
	}
	return nil
}

func (s *Service) recorder() *runtimeevents.Recorder {
	if s == nil || s.recorderFn == nil {
		return nil
	}
	return s.recorderFn()
}

func (s *Service) recordSystemEvent(channel, name string, payload map[string]any, output, errMsg string) {
	recorder := s.recorder()
	if recorder == nil {
		return
	}
	runtimeevents.AppendSyntheticRunEvent(recorder, runtimeevents.SyntheticRunSpec{
		AgentID: runtimeevents.SystemAgentID,
		Model:   "system/control",
		Channel: channel,
	}, name, payload, output, errMsg)
}
