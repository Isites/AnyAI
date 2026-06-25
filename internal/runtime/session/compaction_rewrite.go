package session

import (
	"strings"
	"time"
)

const defaultCompactionReplayKeepEntries = 24

// RewriteHistoryWithCompaction rebuilds session history so a compaction
// summary replaces older transcript entries while preserving the latest
// stateful entries and a recent verbatim suffix.
func RewriteHistoryWithCompaction(
	history []SessionEntry,
	summary string,
	keepEntries int,
	trigger string,
	legacyHeuristic bool,
) []SessionEntry {
	return RewriteHistoryWithCompactionData(history, CompactionData{
		Text:            summary,
		Trigger:         trigger,
		LegacyHeuristic: legacyHeuristic,
	}, keepEntries)
}

func RewriteHistoryWithCompactionData(
	history []SessionEntry,
	data CompactionData,
	keepEntries int,
) []SessionEntry {
	history = RepairLeadingFragment(history)
	data.Text = strings.TrimSpace(data.Text)
	if len(history) == 0 || data.Text == "" {
		return append([]SessionEntry(nil), history...)
	}
	if data.CreatedAt == 0 {
		data.CreatedAt = time.Now().Unix()
	}

	if keepEntries <= 0 {
		keepEntries = defaultCompactionReplayKeepEntries
	}

	blocks := BuildHistoryBlocks(history)
	if len(blocks) == 0 {
		data.AfterEntryCount = 1
		return []SessionEntry{
			CompactionEntry(data),
		}
	}

	recentBlocks := SelectRecentBlocksByMinEntries(blocks, keepEntries)
	recentEntries := StripCompactionAndMetaEntries(FlattenHistoryBlocks(recentBlocks))
	if len(recentEntries) == 0 {
		recentEntries = StripCompactionAndMetaEntries(history)
	}

	recentIDs := make(map[string]struct{}, len(recentEntries))
	for _, entry := range recentEntries {
		recentIDs[entry.ID] = struct{}{}
	}

	stateEntries := LatestStateEntries(history, recentIDs)
	finalEntries := make([]SessionEntry, 0, 1+len(stateEntries)+len(recentEntries))
	data.AfterEntryCount = 1 + len(stateEntries) + len(recentEntries)
	finalEntries = append(finalEntries, CompactionEntry(data))
	finalEntries = append(finalEntries, stateEntries...)
	finalEntries = append(finalEntries, recentEntries...)
	return RepairLeadingFragment(finalEntries)
}
