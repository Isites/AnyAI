package mcp

import "strings"

const toolNamePrefix = "mcp__"

func ToolName(serverName, remoteToolName string) string {
	server := safeToolPart(serverName)
	tool := safeToolPart(remoteToolName)
	if server == "" || tool == "" {
		return ""
	}
	return toolNamePrefix + server + "__" + tool
}

func safeToolPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
