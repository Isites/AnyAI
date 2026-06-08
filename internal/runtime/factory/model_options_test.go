package factory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/llm"
)

type fakeProvider struct {
	defaultOpts func(string) llm.ModelOptions
}

func (p *fakeProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	return nil, nil
}
func (p *fakeProvider) Compact(_ context.Context, _ llm.CompactRequest) (llm.CompactResponse, error) {
	return llm.CompactResponse{}, nil
}
func (p *fakeProvider) Models() []llm.ModelInfo { return nil }
func (p *fakeProvider) DefaultModelOptions(model string) llm.ModelOptions {
	if p.defaultOpts != nil {
		return p.defaultOpts(model)
	}
	return llm.ModelOptions{}
}

func TestResolveModelOptionsFallsBackToHardcodedTemperatureWhenConfigEmpty(t *testing.T) {
	provider := &fakeProvider{}
	got := resolveModelOptions(nil, provider, "gpt-4o", "openai/gpt-4o")

	if assert.NotNil(t, got.Temperature) {
		assert.InDelta(t, defaultRuntimeTemperature, *got.Temperature, 1e-9)
	}
	assert.False(t, got.OmitTemperature)
	assert.Equal(t, "", got.MaxTokensField)
}

func TestResolveModelOptionsAppliesProjectDefaultTemperature(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelsConfig{
			DefaultTemperature: float64Ptr(0.55),
		},
	}
	got := resolveModelOptions(cfg, &fakeProvider{}, "gpt-4o", "openai/gpt-4o")

	if assert.NotNil(t, got.Temperature) {
		assert.InDelta(t, 0.55, *got.Temperature, 1e-9)
	}
}

func TestResolveModelOptionsAppliesProviderFamilyDefault(t *testing.T) {
	provider := &fakeProvider{
		defaultOpts: func(_ string) llm.ModelOptions {
			return llm.ModelOptions{
				MaxTokensField:  "max_completion_tokens",
				OmitTemperature: true,
			}
		},
	}
	got := resolveModelOptions(nil, provider, "gpt-5", "openai/gpt-5")

	assert.Equal(t, "max_completion_tokens", got.MaxTokensField)
	assert.True(t, got.OmitTemperature)
	assert.Nil(t, got.Temperature, "family OmitTemperature must clear the runtime baseline")
}

func TestResolveModelOptionsAppliesUserOverride(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelsConfig{
			Options: map[string]config.ModelOptionsConfig{
				"openai/gpt-5": {
					Temperature: float64Ptr(1.0),
				},
			},
		},
	}
	provider := &fakeProvider{
		defaultOpts: func(_ string) llm.ModelOptions {
			return llm.ModelOptions{OmitTemperature: true}
		},
	}
	got := resolveModelOptions(cfg, provider, "gpt-5", "openai/gpt-5")

	if assert.NotNil(t, got.Temperature, "user-supplied temperature must override the family's force-omit") {
		assert.InDelta(t, 1.0, *got.Temperature, 1e-9)
	}
	assert.False(t, got.OmitTemperature, "explicit user value clears the family's omit signal")
}

func TestResolveModelOptionsPicksMatchingOverrideKey(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelsConfig{
			Options: map[string]config.ModelOptionsConfig{
				"openai/gpt-5":  {Temperature: float64Ptr(0.9)},
				"openai/gpt-4o": {Temperature: float64Ptr(0.1)},
			},
		},
	}
	got := resolveModelOptions(cfg, &fakeProvider{}, "gpt-5", "openai/gpt-5")

	if assert.NotNil(t, got.Temperature) {
		assert.InDelta(t, 0.9, *got.Temperature, 1e-9)
	}
}

func float64Ptr(v float64) *float64 { return &v }
