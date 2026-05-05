package runtime

import (
	"time"

	"github.com/Isites/anyai/internal/runtime/memory"
)

type MemoryService struct {
	managerFn func() *memory.Manager
}

func NewMemoryService(managerFn func() *memory.Manager) *MemoryService {
	return &MemoryService{managerFn: managerFn}
}

func (s *MemoryService) Manager() *memory.Manager {
	if s == nil || s.managerFn == nil {
		return nil
	}
	return s.managerFn()
}

func (s *MemoryService) Stats() memory.Stats {
	manager := s.Manager()
	if manager == nil {
		return memory.Stats{}
	}
	return manager.Stats()
}

func (s *MemoryService) Search(query string, maxResults int, layers ...memory.Layer) []memory.SearchMatch {
	manager := s.Manager()
	if manager == nil {
		return nil
	}
	return manager.SearchExplained(query, maxResults, layers...)
}

func (s *MemoryService) SearchScoped(query string, maxResults int, scope memory.SearchScope, layers ...memory.Layer) []memory.SearchMatch {
	manager := s.Manager()
	if manager == nil {
		return nil
	}
	scope = memory.NormalizeScope(scope)
	maxResults = memory.NormalizeMaxResults(maxResults, memory.DefaultSearchLimit)
	layers = memory.NormalizeLayers(layers)
	return manager.SearchExplainedScoped(query, maxResults, scope, layers...)
}

func (s *MemoryService) Get(id string) (memory.Entry, bool) {
	manager := s.Manager()
	if manager == nil {
		return memory.Entry{}, false
	}
	return manager.Get(id)
}

func (s *MemoryService) GetScoped(id string, scope memory.SearchScope) (memory.Entry, bool) {
	manager := s.Manager()
	if manager == nil {
		return memory.Entry{}, false
	}
	return manager.GetScoped(memory.NormalizeID(id), memory.NormalizeScope(scope))
}

func (s *MemoryService) SaveToLayer(layer memory.Layer, id, content string) error {
	manager := s.Manager()
	if manager == nil {
		return nil
	}
	return manager.SaveToLayer(layer, id, content)
}

func (s *MemoryService) Delete(id string) error {
	manager := s.Manager()
	if manager == nil {
		return nil
	}
	return manager.Delete(id)
}

func (s *MemoryService) Cleanup(now time.Time) (int, error) {
	manager := s.Manager()
	if manager == nil {
		return 0, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return manager.CleanupStale(now)
}

func (s *MemoryService) Reindex() (int, error) {
	manager := s.Manager()
	if manager == nil {
		return 0, nil
	}
	return manager.Reindex()
}

func (s *MemoryService) PromoteEligible(now time.Time) (int, error) {
	manager := s.Manager()
	if manager == nil {
		return 0, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return manager.PromoteEligible(now)
}
