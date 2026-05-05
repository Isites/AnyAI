package gateway

import (
	"time"

	"github.com/Isites/anyai/internal/runtime/memory"
)

func (s *Service) MemoryStats() memory.Stats {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return memory.Stats{}
	}
	return rt.MemoryStats()
}

func (s *Service) MemorySearch(query string, maxItems int, scope memory.SearchScope, layers ...memory.Layer) []memory.SearchMatch {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return rt.MemorySearch(query, maxItems, scope, layers...)
}

func (s *Service) MemoryGet(id string, scope memory.SearchScope) (memory.Entry, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return memory.Entry{}, false
	}
	return rt.MemoryGet(id, scope)
}

func (s *Service) MemoryStaleCleanup(now time.Time) (int, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return 0, err
	}
	return rt.MemoryStaleCleanup(now)
}

func (s *Service) MemoryReindex() (int, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return 0, err
	}
	return rt.MemoryReindex()
}

func (s *Service) MemoryPromoteEligible(now time.Time) (int, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return 0, err
	}
	return rt.MemoryPromoteEligible(now)
}
