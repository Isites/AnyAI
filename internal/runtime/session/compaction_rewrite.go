package session

import "strings"

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
	history = RepairLeadingFragment(history)
	summary = strings.TrimSpace(summary)
	if len(history) == 0 || summary == "" {
		return append([]SessionEntry(nil), history...)
	}

	if keepEntries <= 0 {
		keepEntries = defaultCompactionReplayKeepEntries
	}

	blocks := BuildHistoryBlocks(history)
	if len(blocks) == 0 {
		return []SessionEntry{
			CompactionEntry(CompactionData{
				Text:            summary,
				Trigger:         trigger,
				LegacyHeuristic: legacyHeuristic,
			}),
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
	finalEntries = append(finalEntries, CompactionEntry(CompactionData{
		Text:            summary,
		Trigger:         trigger,
		LegacyHeuristic: legacyHeuristic,
	}))
	finalEntries = append(finalEntries, stateEntries...)
	finalEntries = append(finalEntries, recentEntries...)
	return RepairLeadingFragment(finalEntries)
}
