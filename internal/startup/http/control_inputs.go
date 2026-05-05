package httpchannel

import (
	"strings"

	"github.com/Isites/anyai/internal/runtime/input"
)

func summarizeInputsForRecord(blocks []input.InputBlock) string {
	return strings.TrimSpace(strings.Join(input.ResolveEnvelope(input.InputEnvelope{Blocks: blocks}), " "))
}
