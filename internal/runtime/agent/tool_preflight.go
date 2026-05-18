package agent

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/tool"
)

func (r *Runtime) maybeRepairToolCall(call llm.ToolCall) (llm.ToolCall, map[string]any) {
	if r == nil || !r.toolPreflightEnabled() {
		return call, nil
	}

	originalName := call.Name
	originalInput := append([]byte(nil), call.Input...)
	notes := make([]string, 0, 4)

	if repairedName, ok := normalizeToolName(call.Name, r.Tools.Names()); ok && repairedName != call.Name {
		call.Name = repairedName
		notes = append(notes, "normalized tool name to registered runtime tool")
	}

	if repairedInput, inputNotes, ok := repairToolInput(call.Name, call.Input); ok {
		call.Input = repairedInput
		notes = append(notes, inputNotes...)
	}

	if call.Name == originalName && bytes.Equal(call.Input, originalInput) {
		return call, nil
	}

	meta := map[string]any{
		"repair_applied": true,
		"repair_notes":   notes,
	}
	if strings.TrimSpace(originalName) != "" && originalName != call.Name {
		meta["original_tool_name"] = originalName
	}
	if summary := summarizeRawInput(originalInput); summary != "" && !bytes.Equal(call.Input, originalInput) {
		meta["original_input_summary"] = summary
	}
	return call, meta
}

func (r *Runtime) toolPreflightEnabled() bool {
	if r == nil {
		return true
	}
	if r.ToolPreflight == (ToolPreflightConfig{}) {
		return true
	}
	return r.ToolPreflight.Enabled
}

func normalizeToolName(name string, available []string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	lookup := make(map[string]string, len(available))
	for _, item := range available {
		lookup[normalizeToolKey(item)] = item
		if item == name {
			return item, true
		}
	}
	if repaired, ok := lookup[normalizeToolKey(name)]; ok {
		return repaired, true
	}
	return name, false
}

func normalizeToolKey(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

func repairToolInput(toolName string, raw json.RawMessage) (json.RawMessage, []string, bool) {
	notes := make([]string, 0, 4)
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return raw, nil, false
	}

	current := append([]byte(nil), raw...)
	if unquoted, ok := unwrapJSONString(current); ok {
		current = unquoted
		notes = append(notes, "unwrapped stringified JSON payload")
	}

	var payload map[string]any
	if err := json.Unmarshal(current, &payload); err != nil {
		return raw, nil, false
	}

	switch strings.TrimSpace(toolName) {
	case "read_file", "write_file", "edit_file":
		renameFirst(payload, "path", "file", "filepath", "filename")
	case "bash":
		renameFirst(payload, "command", "cmd", "script")
	}

	normalized, err := json.Marshal(payload)
	if err != nil {
		return raw, nil, false
	}
	if bytes.Equal(bytes.TrimSpace(normalized), bytes.TrimSpace(raw)) {
		return raw, nil, false
	}
	notes = append(notes, "normalized deterministic tool input aliases")
	return normalized, notes, true
}

func unwrapJSONString(raw json.RawMessage) (json.RawMessage, bool) {
	var quoted string
	if err := json.Unmarshal(raw, &quoted); err != nil {
		return nil, false
	}
	trimmed := strings.TrimSpace(quoted)
	if !json.Valid([]byte(trimmed)) {
		return nil, false
	}
	return json.RawMessage(trimmed), true
}

func renameFirst(payload map[string]any, canonical string, aliases ...string) {
	if payload == nil {
		return
	}
	if value, ok := payload[canonical]; ok && !isEmptyValue(value) {
		return
	}
	for _, alias := range aliases {
		value, ok := payload[alias]
		if !ok || isEmptyValue(value) {
			continue
		}
		payload[canonical] = value
		delete(payload, alias)
		return
	}
}

func isEmptyValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	default:
		return false
	}
}

func summarizeRawInput(raw json.RawMessage) string {
	raw = tools.SanitizeRawJSON(raw)
	if len(raw) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}
	switch v := decoded.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return "{}"
		}
		return "keys=" + strings.Join(keys, ",")
	default:
		encoded, _ := json.Marshal(v)
		return string(encoded)
	}
}
