package runtimeevents

import "strings"

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func StringPayload(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func TextDelta(record EventRecord) string {
	if strings.TrimSpace(record.Name) != EventTextDelta {
		return ""
	}
	return StringPayload(record.Payload, "text")
}

func FailureMessage(record EventRecord) string {
	if strings.TrimSpace(record.Name) != EventRunFailed {
		return ""
	}
	return StringPayload(record.Payload, "message")
}

func ToolName(record EventRecord) string {
	return StringPayload(record.Payload, "tool")
}
