package contract

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	sensitiveInlinePatterns = []struct {
		repl string
		re   *regexp.Regexp
	}{
		{re: regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`), repl: "Bearer [REDACTED]"},
		{re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{12,}\b`), repl: "[REDACTED]"},
		{re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`), repl: "[REDACTED]"},
		{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{16,}\b`), repl: "[REDACTED]"},
		{re: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), repl: "[REDACTED]"},
		{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9._-]{8,}\.[A-Za-z0-9._-]{8,}\b`), repl: "[REDACTED]"},
	}
)

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	switch normalized {
	case "authorization", "apikey", "accesstoken", "authtoken", "refreshtoken", "token", "secret", "clientsecret", "password":
		return true
	default:
		return false
	}
}

func sanitizeAny(value any) any {
	switch v := value.(type) {
	case map[string]any:
		sanitized := make(map[string]any, len(v))
		for key, inner := range v {
			if isSensitiveKey(key) {
				if s, ok := inner.(string); ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "bearer ") {
					sanitized[key] = "Bearer [REDACTED]"
				} else {
					sanitized[key] = "[REDACTED]"
				}
				continue
			}
			sanitized[key] = sanitizeAny(inner)
		}
		return sanitized
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = sanitizeAny(v[i])
		}
		return out
	case string:
		return SanitizeText(v)
	default:
		return value
	}
}

// SanitizeText redacts obvious credentials and secrets from a free-form string.
func SanitizeText(text string) string {
	out := text
	authorizationRE := regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(bearer\s+)?[^\s,\n\r"'` + "`" + `}]{4,}`)
	out = authorizationRE.ReplaceAllString(out, `${1}[REDACTED]`)
	for _, key := range []string{"api_key", "api-key", "access_token", "auth_token", "refresh_token", "token", "secret", "password"} {
		re := regexp.MustCompile(`(?i)(` + regexp.QuoteMeta(key) + `\s*[:=]\s*)(?:"[^"]{4,}"|'[^']{4,}'|[^,\s"'` + "`" + `}]{4,})`)
		out = re.ReplaceAllString(out, `${1}[REDACTED]`)
	}
	for _, pattern := range sensitiveInlinePatterns {
		out = pattern.re.ReplaceAllString(out, pattern.repl)
	}
	return out
}

// SanitizeMetadata recursively redacts obvious credentials from metadata maps.
func SanitizeMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return metadata
	}
	sanitized := sanitizeAny(metadata)
	if out, ok := sanitized.(map[string]any); ok {
		return out
	}
	return metadata
}

// SanitizeRawJSON redacts obvious credentials from structured JSON payloads.
func SanitizeRawJSON(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return raw
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		sanitized := sanitizeAny(decoded)
		if out, err := json.Marshal(sanitized); err == nil {
			return out
		}
	}

	return json.RawMessage(SanitizeText(trimmed))
}
