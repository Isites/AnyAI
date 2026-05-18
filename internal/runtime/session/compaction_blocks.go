package session

import "strings"

// HistoryBlock groups a complete conversational span so session compaction can
// preserve whole turns instead of slicing through tool-call boundaries.
type HistoryBlock struct {
	Entries       []SessionEntry
	ApproxTokens  int
	HasUserTurn   bool
	HasToolEvents bool
}

// BuildHistoryBlocks partitions history into coarse turn blocks rooted at user
// messages. Compaction summaries start their own block so they can be replaced
// cleanly on the next compaction pass.
func BuildHistoryBlocks(history []SessionEntry) []HistoryBlock {
	if len(history) == 0 {
		return nil
	}
	var (
		blocks  []HistoryBlock
		current []SessionEntry
	)
	flush := func() {
		if len(current) == 0 {
			return
		}
		block := HistoryBlock{
			Entries:      append([]SessionEntry(nil), current...),
			ApproxTokens: estimateEntriesTokens(current),
		}
		for _, entry := range current {
			switch entry.Type {
			case EntryTypeMessage:
				if entry.Role == "user" {
					block.HasUserTurn = true
				}
			case EntryTypeToolCall, EntryTypeToolResult:
				block.HasToolEvents = true
			}
		}
		blocks = append(blocks, block)
		current = nil
	}
	for _, entry := range history {
		if startsHistoryBlock(entry) && len(current) > 0 {
			flush()
		}
		if entry.Type == EntryTypeRuntimeControl {
			flush()
		}
		current = append(current, entry)
	}
	flush()
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

// SelectRecentBlocksByMinEntries returns a suffix of complete history blocks
// whose total entry count satisfies the requested minimum.
func SelectRecentBlocksByMinEntries(blocks []HistoryBlock, minEntries int) []HistoryBlock {
	if len(blocks) == 0 {
		return nil
	}
	if minEntries <= 0 {
		return append([]HistoryBlock(nil), blocks...)
	}
	total := 0
	start := len(blocks) - 1
	for ; start >= 0; start-- {
		total += len(blocks[start].Entries)
		if total >= minEntries {
			break
		}
	}
	if start < 0 {
		start = 0
	}
	return append([]HistoryBlock(nil), blocks[start:]...)
}

// RepairLeadingFragment drops broken leading tool fragments that can appear in
// legacy compacted sessions where history was sliced through a tool pair.
func RepairLeadingFragment(history []SessionEntry) []SessionEntry {
	if len(history) == 0 {
		return nil
	}
	prefix := make([]SessionEntry, 0, 4)
	for idx, entry := range history {
		switch entry.Type {
		case EntryTypeCompaction:
			return append(prefix, history[idx:]...)
		case EntryTypeRuntimeControl:
			prefix = append(prefix, entry)
		case EntryTypeMessage:
			if entry.Role == "user" {
				return append(prefix, history[idx:]...)
			}
			continue
		case EntryTypeMeta, EntryTypePlan, EntryTypeTodo:
			prefix = append(prefix, entry)
		}
	}
	if len(prefix) == 0 {
		return nil
	}
	return prefix
}

func startsHistoryBlock(entry SessionEntry) bool {
	switch entry.Type {
	case EntryTypeCompaction:
		return true
	case EntryTypeRuntimeControl:
		return true
	case EntryTypeMessage:
		return entry.Role == "user"
	default:
		return false
	}
}

func estimateEntriesTokens(entries []SessionEntry) int {
	if len(entries) == 0 {
		return 0
	}
	totalChars := 0
	for _, entry := range entries {
		totalChars += len(entry.Data)
		totalChars += len(entry.Role)
		totalChars += len(entry.Type)
	}
	// Keep the estimate conservative enough that compaction runs before the
	// provider sees malformed or oversized requests.
	approx := totalChars / 4
	if approx <= 0 {
		return 1
	}
	return approx
}

func StripCompactionAndMetaEntries(entries []SessionEntry) []SessionEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]SessionEntry, 0, len(entries))
	for _, entry := range entries {
		switch entry.Type {
		case EntryTypeCompaction, EntryTypeMeta:
			continue
		default:
			out = append(out, entry)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func FlattenHistoryBlocks(blocks []HistoryBlock) []SessionEntry {
	if len(blocks) == 0 {
		return nil
	}
	entries := make([]SessionEntry, 0)
	for _, block := range blocks {
		entries = append(entries, block.Entries...)
	}
	return entries
}

func CountUserTurnsInBlocks(blocks []HistoryBlock) int {
	count := 0
	for _, block := range blocks {
		if block.HasUserTurn {
			count++
		}
	}
	return count
}

func SummarizeRecentUserTurns(blocks []HistoryBlock) string {
	lines := make([]string, 0, len(blocks))
	for _, block := range blocks {
		for _, entry := range block.Entries {
			if entry.Type != EntryTypeMessage || entry.Role != "user" {
				continue
			}
			var msg MessageData
			if err := unmarshalMessageData(entry, &msg); err != nil {
				continue
			}
			text := strings.TrimSpace(msg.Text)
			if text != "" && !IsRuntimeControlEntry(entry) {
				lines = append(lines, text)
			}
			break
		}
	}
	return strings.Join(lines, " | ")
}
