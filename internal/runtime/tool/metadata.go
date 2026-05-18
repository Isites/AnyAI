package tools

import (
	"encoding/json"
	"strings"
	"time"
)

type ToolEffect string

const (
	ToolEffectReadOnly ToolEffect = "read_only"
	ToolEffectMutating ToolEffect = "mutating"
)

// ToolMetadata captures the runtime contract a tool declares for scheduling and
// safety decisions. Keep this surface small and explicit: runtime only cares
// about timeout hints, whether the tool is safe to fan out in parallel, whether
// it is read-only or mutating, and whether human approval is required.
type ToolMetadata struct {
	Name             string     `json:"name,omitempty"`
	TimeoutHintMS    int64      `json:"timeout_hint_ms,omitempty"`
	Effect           ToolEffect `json:"effect"`
	Tags             []string   `json:"tags,omitempty"`
	AllowParallel    bool       `json:"allow_parallel,omitempty"`
	RequiresApproval bool       `json:"requires_approval,omitempty"`
}

// MetadataProvider lets a tool override the default metadata inferred from its
// registered name.
type MetadataProvider interface {
	ToolMetadata() ToolMetadata
}

// InputTimeoutHintProvider lets a tool derive a task timeout from the concrete
// input payload instead of relying only on static registry metadata.
type InputTimeoutHintProvider interface {
	TimeoutHintForInput(input json.RawMessage, fallback time.Duration) (time.Duration, bool)
}

const (
	defaultToolTimeoutMS        = int64((30 * time.Second) / time.Millisecond)
	shortStateMutationTimeoutMS = int64((15 * time.Second) / time.Millisecond)
	longProcessTimeoutMS        = int64((120 * time.Second) / time.Millisecond)
	callAgentToolTimeoutMS      = int64((90 * time.Second) / time.Millisecond)
	browserToolTimeoutMS        = int64((60 * time.Second) / time.Millisecond)
	WebSearchTimeout            = 30 * time.Second
)

// DefaultToolMetadata returns the generic runtime contract fallback for a
// tool. Concrete tools should prefer declaring ToolMetadata explicitly.
func DefaultToolMetadata(name string) ToolMetadata {
	return ToolMetadata{
		Name:          strings.TrimSpace(name),
		TimeoutHintMS: defaultToolTimeoutMS,
		Effect:        ToolEffectMutating,
	}
}

func readOnlyToolMetadata(name string, timeoutMS int64) ToolMetadata {
	return buildToolMetadata(name, ToolEffectReadOnly, timeoutMS, true, false)
}

func workflowToolMetadata(name string, timeoutMS int64) ToolMetadata {
	return buildToolMetadata(name, ToolEffectMutating, timeoutMS, true, false, "workflow")
}

func workspaceWriteToolMetadata(name string, timeoutMS int64) ToolMetadata {
	return buildToolMetadata(name, ToolEffectMutating, timeoutMS, false, false, "workspace_write")
}

func stateMutationToolMetadata(name string, timeoutMS int64) ToolMetadata {
	return buildToolMetadata(name, ToolEffectMutating, timeoutMS, false, false, "state_mutation")
}

func externalIOToolMetadata(name string, timeoutMS int64, requiresApproval bool) ToolMetadata {
	return buildToolMetadata(name, ToolEffectMutating, timeoutMS, false, requiresApproval, "external_io")
}

func buildToolMetadata(name string, effect ToolEffect, timeoutMS int64, allowParallel bool, requiresApproval bool, tags ...string) ToolMetadata {
	meta := DefaultToolMetadata(name)
	meta.Effect = effect
	meta.AllowParallel = allowParallel
	meta.RequiresApproval = requiresApproval
	meta.Tags = normalizeToolTags(tags)
	if timeoutMS > 0 {
		meta.TimeoutHintMS = timeoutMS
	}
	return meta
}

// DescribeToolMetadata returns the normalized metadata for a concrete tool.
func DescribeToolMetadata(tool Tool) ToolMetadata {
	if tool == nil {
		return DefaultToolMetadata("")
	}
	if provider, ok := tool.(MetadataProvider); ok {
		return normalizeToolMetadata(provider.ToolMetadata(), tool.Name())
	}
	return DefaultToolMetadata(tool.Name())
}

func normalizeToolMetadata(meta ToolMetadata, name string) ToolMetadata {
	base := DefaultToolMetadata(name)
	base.Name = strings.TrimSpace(name)
	if meta.Name != "" {
		base.Name = strings.TrimSpace(meta.Name)
	}
	if meta.TimeoutHintMS > 0 {
		base.TimeoutHintMS = meta.TimeoutHintMS
	}
	if meta.Effect != "" {
		base.Effect = meta.Effect
	}
	if len(meta.Tags) > 0 {
		base.Tags = normalizeToolTags(meta.Tags)
	}
	base.AllowParallel = meta.AllowParallel
	base.RequiresApproval = base.RequiresApproval || meta.RequiresApproval
	return base
}

func normalizeToolTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m ToolMetadata) TimeoutHint() time.Duration {
	if m.TimeoutHintMS <= 0 {
		return 0
	}
	return time.Duration(m.TimeoutHintMS) * time.Millisecond
}

func (m ToolMetadata) AllowsParallelFanout() bool {
	return m.AllowParallel
}

func (m ToolMetadata) IsReadOnly() bool {
	return m.Effect == ToolEffectReadOnly
}
