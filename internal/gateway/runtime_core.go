package gateway

import (
	"github.com/Isites/anyai/internal/config"
)

func (s *Service) Agents() []config.AgentConfig {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return rt.Agents()
}

func (s *Service) ResourceCatalog() ResourceCatalog {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return ResourceCatalog{}
	}
	return gatewayResourceCatalog(rt.Resources())
}

func (s *Service) ListJobs() []Job {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	js := rt.JobScheduler()
	if js == nil {
		return nil
	}
	return gatewayJobs(js.ListJobs())
}

func (s *Service) PauseJob(name string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	js := rt.JobScheduler()
	if js == nil {
		return nil
	}
	return js.PauseJob(name)
}

func (s *Service) ResumeJob(name string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	js := rt.JobScheduler()
	if js == nil {
		return nil
	}
	return js.ResumeJob(name)
}

func (s *Service) RemoveJob(name string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	js := rt.JobScheduler()
	if js == nil {
		return nil
	}
	return js.RemoveJob(name)
}

func (s *Service) UpdateJobSchedule(name, schedule string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	js := rt.JobScheduler()
	if js == nil {
		return nil
	}
	return js.UpdateJobSchedule(name, schedule)
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
