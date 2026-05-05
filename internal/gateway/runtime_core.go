package gateway

import (
	"github.com/Isites/anyai/internal/config"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

func (s *Service) Agents() []config.AgentConfig {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return rt.Agents()
}

func (s *Service) Resources() *runtimeresources.Catalog {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return rt.Resources()
}

func (s *Service) JobScheduler() tools.JobScheduler {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return rt.JobScheduler()
}

func (s *Service) EventStorageDir() string {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return ""
	}
	return rt.EventStorageDir()
}

func (s *Service) RebuildEventProjections() error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	return rt.RebuildEventProjections()
}
