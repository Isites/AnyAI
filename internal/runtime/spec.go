package runtime

import (
	"fmt"

	runtimefactory "github.com/Isites/anyai/internal/runtime/factory"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	"github.com/Isites/anyai/internal/runtime/skill"
)

type resourceSkillConfigurer interface {
	SetResources(*runtimeresources.Catalog)
	SetSkills(*skill.Loader)
}

// ApplySpec applies runtime-owned portions of a refreshed runtime spec.
// Gateway/channel views remain owned by their outer layers.
func (r *Runtime) ApplySpec(spec runtimefactory.RuntimeSpec) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	if spec.Config != nil {
		r.UpdateConfig(spec.Config)
	}
	if spec.Providers != nil && r.deps != nil {
		r.deps.SetProviders(spec.Providers)
	}
	if spec.Resources != nil {
		r.SetResources(spec.Resources)
	}
	if spec.Skills != nil {
		r.SetSkills(spec.Skills)
	}
	if r.deps != nil {
		runner := r.deps.ExecutionDeps().AgentRunner
		if runner != nil && runner != r {
			if configurable, ok := runner.(resourceSkillConfigurer); ok {
				if spec.Resources != nil {
					configurable.SetResources(spec.Resources)
				}
				if spec.Skills != nil {
					configurable.SetSkills(spec.Skills)
				}
			}
		}
	}
	return nil
}
