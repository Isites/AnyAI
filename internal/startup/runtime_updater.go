package startup

import (
	"context"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	"github.com/Isites/anyai/internal/registry"
	runtimefactory "github.com/Isites/anyai/internal/runtime/factory"
	"github.com/Isites/anyai/internal/runtime/llm"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
)

type RuntimeUpdater interface {
	ApplyConfig(ctx context.Context, cfg *config.Config, source string) error
	ApplyProject(ctx context.Context, project *registry.Project, source string) error
}

type runtimeUpdater struct {
	core              *CoreRuntime
	gateway           *gateway.Service
	launchMode        LaunchMode
	fallbackAgentID   string
	providerOverrides map[string]llm.LLMProvider
	dataDir           string
	onApplied         func(*config.Config)
}

func newRuntimeUpdater(core *CoreRuntime, opt Options, onApplied func(*config.Config)) *runtimeUpdater {
	if core == nil {
		return &runtimeUpdater{launchMode: opt.LaunchMode, fallbackAgentID: opt.FallbackAgentID, onApplied: onApplied}
	}
	return &runtimeUpdater{
		core:              core,
		gateway:           core.Gateway,
		launchMode:        opt.LaunchMode,
		fallbackAgentID:   opt.FallbackAgentID,
		providerOverrides: opt.ProviderOverrides,
		dataDir:           core.DataDir,
		onApplied:         onApplied,
	}
}

func (u *runtimeUpdater) ApplyProject(ctx context.Context, project *registry.Project, source string) error {
	if project == nil {
		return nil
	}
	return u.ApplyConfig(ctx, project.Config, source)
}

func (u *runtimeUpdater) ApplyConfig(_ context.Context, nextCfg *config.Config, source string) error {
	if u == nil || nextCfg == nil {
		return nil
	}
	spec, err := runtimefactory.BuildRuntimeSpec(nextCfg, u.providerOverrides)
	if err != nil {
		return err
	}
	applyLaunchModeChannels(spec.Config, u.launchMode, spec.Layout.DataDir)
	spec, err = u.specWithResources(spec)
	if err != nil {
		runtimelogging.Warn("failed to rebuild runtime resource catalog", "error", err)
	}
	if u.core != nil && u.core.Runtime != nil {
		if applyErr := u.core.Runtime.ApplySpec(spec); applyErr != nil {
			return applyErr
		}
		u.core.Config = spec.Config
		u.core.DataDir = spec.Layout.DataDir
		u.core.Providers = spec.Providers
		if spec.Skills != nil {
			u.core.SkillLoader = spec.Skills
		}
		u.dataDir = spec.Layout.DataDir
	}
	if u.gateway != nil {
		u.gateway.ApplyRuntimeConfig(spec.Config, u.fallbackAgentID)
	}
	if u.onApplied != nil {
		u.onApplied(spec.Config)
	}
	if source != "" {
		runtimelogging.Info(source)
	}
	return nil
}

func (u *runtimeUpdater) specWithResources(spec runtimefactory.RuntimeSpec) (runtimefactory.RuntimeSpec, error) {
	if u == nil || u.core == nil || u.core.Runtime == nil {
		return spec, nil
	}
	return spec.WithRuntimeResources(u.core.Runtime.ExecutionDeps())
}
