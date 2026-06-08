package llm

// ModelOptions carries per-model knobs that influence how providers shape the
// outgoing request. All fields are optional so they can be merged across layers
// (runtime defaults, provider family defaults, user overrides).
type ModelOptions struct {
	// Temperature, if non-nil, is sent on the wire. nil means omit the field
	// entirely — required for OpenAI reasoning models that lock temperature at 1.
	Temperature *float64

	// OmitTemperature is the explicit force-omit signal used during merge: when
	// an override layer sets it true, base.Temperature is cleared to nil. This
	// distinguishes "I have no opinion, fall through to base" (Temperature nil,
	// OmitTemperature false) from "I require Temperature to be omitted on the
	// wire" (Temperature nil, OmitTemperature true) — the OpenAI reasoning
	// family uses the latter to override the runtime's default-temperature
	// baseline.
	OmitTemperature bool

	// MaxTokensField selects the max-output field name for OpenAI-compatible
	// providers: "max_tokens" (default) or "max_completion_tokens" (reasoning
	// models). "" leaves the provider's built-in choice untouched.
	MaxTokensField string

	// EnableThinking, if non-nil, is sent as
	// chat_template_kwargs.enable_thinking on OpenAI-compatible endpoints.
	// Used by Qwen3-on-OpenAI to disable the visible reasoning trace.
	EnableThinking *bool

	// Stream, if non-nil and false, forces the OpenAI-compatible non-streaming
	// completion path. Some Qwen3 deployments need this.
	Stream *bool

	// NativeCompaction, if non-nil and true, routes Anthropic compaction through
	// the beta ContextManagement API instead of the chat-fallback summarizer.
	NativeCompaction *bool
}

// MergeModelOptions overlays override on top of base. Non-nil pointer fields and
// non-empty string fields in override replace the corresponding field in base.
// nil / "" in override leaves base untouched, so callers can safely pass partial
// overrides and have unspecified fields fall through to the lower layer.
//
// Temperature has dedicated tri-state semantics: an override with OmitTemperature
// true clears base.Temperature; an override with non-nil Temperature replaces it
// (and clears OmitTemperature, since an explicit value supersedes a force-omit).
func MergeModelOptions(base, override ModelOptions) ModelOptions {
	if override.OmitTemperature {
		base.Temperature = nil
		base.OmitTemperature = true
	}
	if override.Temperature != nil {
		base.Temperature = override.Temperature
		base.OmitTemperature = false
	}
	if override.MaxTokensField != "" {
		base.MaxTokensField = override.MaxTokensField
	}
	if override.EnableThinking != nil {
		base.EnableThinking = override.EnableThinking
	}
	if override.Stream != nil {
		base.Stream = override.Stream
	}
	if override.NativeCompaction != nil {
		base.NativeCompaction = override.NativeCompaction
	}
	return base
}

// Float64Ptr is a convenience for building ModelOptions literals.
func Float64Ptr(v float64) *float64 { return &v }

// BoolPtr is a convenience for building ModelOptions literals.
func BoolPtr(v bool) *bool { return &v }
