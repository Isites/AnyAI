package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CloneHeaders returns a trimmed copy of the provided header map.
func CloneHeaders(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]string, len(src))
	for key, value := range src {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		dst[trimmedKey] = strings.TrimSpace(value)
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

// MergeHeaders overlays one or more header maps on top of a base map.
func MergeHeaders(base map[string]string, overlays ...map[string]string) map[string]string {
	merged := CloneHeaders(base)
	for _, overlay := range overlays {
		if len(overlay) == 0 {
			continue
		}
		if merged == nil {
			merged = map[string]string{}
		}
		for key, value := range CloneHeaders(overlay) {
			merged[key] = value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// ParseHeadersJSON parses a JSON object string into a header map.
func ParseHeadersJSON(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("parse headers json: %w", err)
	}
	return CloneHeaders(headers), nil
}
