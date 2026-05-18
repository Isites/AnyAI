package httpchannel

import (
	"strings"

	"github.com/Isites/anyai/internal/gateway"
)

func summarizeInputsForRecord(blocks []gateway.InputBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch strings.TrimSpace(block.Type) {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		case "file", "dir", "image", "pdf":
			if block.Path != "" {
				parts = append(parts, block.Path)
			} else if block.Name != "" {
				parts = append(parts, block.Name)
			}
		case "url":
			if block.URL != "" {
				parts = append(parts, block.URL)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
