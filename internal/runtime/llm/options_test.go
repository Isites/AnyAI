package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeModelOptionsOverridesNonNilFields(t *testing.T) {
	base := ModelOptions{
		Temperature:      Float64Ptr(0.2),
		MaxTokensField:   "max_tokens",
		EnableThinking:   BoolPtr(true),
		Stream:           BoolPtr(true),
		NativeCompaction: BoolPtr(false),
	}
	override := ModelOptions{
		Temperature:      Float64Ptr(0.7),
		MaxTokensField:   "max_completion_tokens",
		EnableThinking:   BoolPtr(false),
		Stream:           BoolPtr(false),
		NativeCompaction: BoolPtr(true),
	}

	got := MergeModelOptions(base, override)

	assert.NotNil(t, got.Temperature)
	assert.InDelta(t, 0.7, *got.Temperature, 1e-9)
	assert.Equal(t, "max_completion_tokens", got.MaxTokensField)
	assert.Equal(t, false, *got.EnableThinking)
	assert.Equal(t, false, *got.Stream)
	assert.Equal(t, true, *got.NativeCompaction)
}

func TestMergeModelOptionsKeepsBaseWhenOverrideUnset(t *testing.T) {
	base := ModelOptions{
		Temperature:    Float64Ptr(0.2),
		MaxTokensField: "max_tokens",
		EnableThinking: BoolPtr(true),
	}

	got := MergeModelOptions(base, ModelOptions{})

	assert.NotNil(t, got.Temperature)
	assert.InDelta(t, 0.2, *got.Temperature, 1e-9)
	assert.Equal(t, "max_tokens", got.MaxTokensField)
	assert.Equal(t, true, *got.EnableThinking)
	assert.Nil(t, got.Stream)
	assert.Nil(t, got.NativeCompaction)
	assert.False(t, got.OmitTemperature)
}

func TestMergeModelOptionsOmitTemperatureClearsBase(t *testing.T) {
	base := ModelOptions{Temperature: Float64Ptr(0.2)}
	override := ModelOptions{OmitTemperature: true}

	got := MergeModelOptions(base, override)

	assert.Nil(t, got.Temperature, "OmitTemperature must drop the inherited temperature so the wire payload omits it")
	assert.True(t, got.OmitTemperature)
}

func TestMergeModelOptionsExplicitTemperatureSupersedesOmit(t *testing.T) {
	// User override of `temperature: 1` on a reasoning-family model: family
	// default sets OmitTemperature, then the user override comes on top and
	// re-introduces an explicit value. The explicit value must win.
	familyDefault := ModelOptions{OmitTemperature: true}
	userOverride := ModelOptions{Temperature: Float64Ptr(1.0)}

	withFamily := MergeModelOptions(ModelOptions{Temperature: Float64Ptr(0.2)}, familyDefault)
	final := MergeModelOptions(withFamily, userOverride)

	assert.NotNil(t, final.Temperature)
	assert.InDelta(t, 1.0, *final.Temperature, 1e-9)
	assert.False(t, final.OmitTemperature, "explicit override must clear the omit signal")
}

func TestOpenAIDefaultModelOptionsForReasoningFamily(t *testing.T) {
	provider := &OpenAIProvider{}
	for _, model := range []string{"gpt-5", "gpt-5.5-light", "o1-preview", "o3-mini", "o4-mini"} {
		t.Run(model, func(t *testing.T) {
			opts := provider.DefaultModelOptions(model)
			assert.Equal(t, "max_completion_tokens", opts.MaxTokensField)
			assert.True(t, opts.OmitTemperature)
			assert.Nil(t, opts.EnableThinking)
		})
	}
}

func TestOpenAIDefaultModelOptionsForQwen3(t *testing.T) {
	provider := &OpenAIProvider{}
	opts := provider.DefaultModelOptions("qwen3:1.7b")
	assert.Equal(t, "", opts.MaxTokensField)
	assert.False(t, opts.OmitTemperature)
	if assert.NotNil(t, opts.EnableThinking) {
		assert.False(t, *opts.EnableThinking)
	}
	if assert.NotNil(t, opts.Stream) {
		assert.False(t, *opts.Stream)
	}
}

func TestOpenAIDefaultModelOptionsForUnconstrainedModel(t *testing.T) {
	provider := &OpenAIProvider{}
	opts := provider.DefaultModelOptions("gpt-4o")
	assert.Equal(t, ModelOptions{}, opts)
}

func TestAnthropicDefaultModelOptionsForNativeCompactFamily(t *testing.T) {
	provider := &AnthropicProvider{}
	for _, model := range []string{"claude-sonnet-4-6", "claude-opus-4-6"} {
		t.Run(model, func(t *testing.T) {
			opts := provider.DefaultModelOptions(model)
			if assert.NotNil(t, opts.NativeCompaction) {
				assert.True(t, *opts.NativeCompaction)
			}
		})
	}
}

func TestAnthropicDefaultModelOptionsForOlderModels(t *testing.T) {
	provider := &AnthropicProvider{}
	opts := provider.DefaultModelOptions("claude-3-5-sonnet")
	assert.Nil(t, opts.NativeCompaction)
}
