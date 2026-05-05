package factory

import (
	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/skill"
)

// RuntimeSpec is the assembled project/runtime specification used to build or
// refresh the runtime. Some live dependencies, such as sender and scheduler,
// are attached after the runtime is created.
type RuntimeSpec struct {
	Config    *config.Config
	Layout    ProjectLayout
	Providers map[string]llm.LLMProvider
	Resources *runtimeresources.Catalog
	Skills    *skill.Loader
}

func NewRuntimeSpec(cfg *config.Config, layout ProjectLayout, providers map[string]llm.LLMProvider) RuntimeSpec {
	return RuntimeSpec{
		Config:    cfg,
		Layout:    layout,
		Providers: providers,
	}
}

func BuildRuntimeSpec(cfg *config.Config, providerOverrides map[string]llm.LLMProvider) (RuntimeSpec, error) {
	layout, err := PrepareProjectLayout(cfg)
	if err != nil {
		return RuntimeSpec{}, err
	}
	return NewRuntimeSpec(cfg, layout, InitProviders(cfg, providerOverrides)), nil
}

func (s RuntimeSpec) WithRuntimeResources(deps runtimeport.ExecutionDeps) (RuntimeSpec, error) {
	resources, loader, err := BuildResourceCatalog(s.Config, deps)
	if err != nil {
		return s, err
	}
	s.Resources = resources
	s.Skills = loader
	return s, nil
}
