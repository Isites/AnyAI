package input

import "fmt"

// ResolveBlock generates a summary string for an input block.
// The summary is injected into the agent context; full content is loaded on demand via tools.
func ResolveBlock(block InputBlock) string {
	switch block.Type {
	case "text":
		return block.Text
	case "file":
		return fmt.Sprintf("[File: %s, MIME: %s]", block.Name, block.MimeType)
	case "dir":
		return fmt.Sprintf("[Directory: %s]", block.Name)
	case "image":
		return fmt.Sprintf("[Image: %s]", block.Name)
	case "pdf":
		return fmt.Sprintf("[PDF: %s]", block.Name)
	case "url":
		return fmt.Sprintf("[URL: %s]", block.URL)
	default:
		return fmt.Sprintf("[%s: %s]", block.Type, block.Name)
	}
}

// ResolveEnvelope generates summary strings for all blocks in an envelope.
func ResolveEnvelope(env InputEnvelope) []string {
	summaries := make([]string, len(env.Blocks))
	for i, b := range env.Blocks {
		summaries[i] = ResolveBlock(b)
	}
	return summaries
}
