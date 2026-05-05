package input

import (
	"fmt"
	"strings"
	"time"
)

const defaultSessionPrefix = "run"

func NormalizeBlocks(text string, inputs []InputBlock) ([]InputBlock, error) {
	if len(inputs) == 0 && strings.TrimSpace(text) != "" {
		inputs = []InputBlock{{Type: "text", Text: strings.TrimSpace(text)}}
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("text or inputs is required")
	}
	for _, block := range inputs {
		if block.Valid() {
			return inputs, nil
		}
	}
	return nil, fmt.Errorf("at least one valid input block is required")
}

func DefaultSessionID(prefix string, now time.Time) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = defaultSessionPrefix
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return prefix + "_" + now.UTC().Format("20060102T150405")
}
