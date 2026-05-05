package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBM25IndexBasic(t *testing.T) {
	idx := NewBM25Index()
	idx.Add("doc1", "the quick brown fox jumps over the lazy dog")
	idx.Add("doc2", "machine learning and artificial intelligence")
	idx.Add("doc3", "the quick brown fox and the lazy cat")

	results := idx.Search("quick fox", 5)
	require.NotEmpty(t, results)
	assert.Contains(t, []string{"doc1", "doc3"}, results[0].ID)
}

func TestBM25IndexEmpty(t *testing.T) {
	idx := NewBM25Index()
	results := idx.Search("test", 5)
	assert.Empty(t, results)
}

func TestBM25IndexNoMatch(t *testing.T) {
	idx := NewBM25Index()
	idx.Add("doc1", "hello world")
	results := idx.Search("quantum physics", 5)
	assert.Empty(t, results)
}

func TestBM25IndexMaxResults(t *testing.T) {
	idx := NewBM25Index()
	for i := 0; i < 10; i++ {
		idx.Add("doc"+string(rune('0'+i)), "test document about golang programming")
	}

	results := idx.Search("golang", 3)
	assert.Len(t, results, 3)
}

func TestTokenize(t *testing.T) {
	tokens := tokenize("Hello, World! This is a test 123.")
	assert.Contains(t, tokens, "hello")
	assert.Contains(t, tokens, "world")
	assert.Contains(t, tokens, "this")
	assert.Contains(t, tokens, "test")
	assert.Contains(t, tokens, "123")
	assert.NotContains(t, tokens, "a")
}

func TestMemoryManagerSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	err := mgr.Save("test-entry", "# Test Entry\n\nThis is a test memory about Go programming.")
	require.NoError(t, err)

	entry, ok := mgr.Get("test-entry")
	assert.True(t, ok)
	assert.Equal(t, "long-term/test-entry", entry.ID)
	assert.Equal(t, LayerLongTerm, entry.Layer)
	assert.Equal(t, "Test Entry", entry.Title)
	assert.Contains(t, entry.Content, "Go programming")

	path := filepath.Join(dir, "long-term", "test-entry.md")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Go programming")

	mgr2 := NewManager(dir)
	require.NoError(t, mgr2.Load())
	entry2, ok := mgr2.Get("test-entry")
	assert.True(t, ok)
	assert.Equal(t, "long-term/test-entry", entry2.ID)
	assert.Equal(t, LayerLongTerm, entry2.Layer)
	assert.Equal(t, "Test Entry", entry2.Title)
}

func TestMemoryManagerLoadsLegacyAndLayeredEntries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "entries"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "episodic"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "long-term"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "root-note.md"), []byte("# Root"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entries", "legacy.md"), []byte("# Legacy"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "episodic", "summary.md"), []byte("# Summary"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "long-term", "decision.md"), []byte("# Decision"), 0o644))

	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	rootEntry, ok := mgr.Get("root-note")
	require.True(t, ok)
	assert.Equal(t, "root-note", rootEntry.ID)
	assert.Equal(t, LayerLongTerm, rootEntry.Layer)

	legacyEntry, ok := mgr.Get("legacy")
	require.True(t, ok)
	assert.Equal(t, "legacy", legacyEntry.ID)
	assert.Equal(t, LayerLongTerm, legacyEntry.Layer)

	episodicEntry, ok := mgr.Get("summary")
	require.True(t, ok)
	assert.Equal(t, "episodic/summary", episodicEntry.ID)
	assert.Equal(t, LayerEpisodic, episodicEntry.Layer)

	longTermEntry, ok := mgr.Get("decision")
	require.True(t, ok)
	assert.Equal(t, "long-term/decision", longTermEntry.ID)
	assert.Equal(t, LayerLongTerm, longTermEntry.Layer)
}

func TestMemoryManagerSearch(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	require.NoError(t, mgr.Save("golang", "# Go Programming\n\nGo is a statically typed, compiled language designed at Google."))
	require.NoError(t, mgr.Save("python", "# Python Programming\n\nPython is a high-level interpreted programming language."))
	require.NoError(t, mgr.Save("recipes", "# Favorite Recipes\n\nChocolate cake recipe with vanilla frosting."))

	results := mgr.Search("programming language", 5)
	assert.NotEmpty(t, results)

	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	assert.Contains(t, ids, "long-term/golang")
	assert.Contains(t, ids, "long-term/python")
}

func TestMemoryManagerSearchPrioritizesEpisodicBeforeLongTerm(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	require.NoError(t, mgr.SaveToLayer(LayerLongTerm, "project-rule", "# Project Rule\n\nApollo rollout requires blue green deployment."))
	require.NoError(t, mgr.SaveToLayer(LayerEpisodic, "current-task", "# Current Task\n\nApollo rollout is blocked on staging verification today."))

	results := mgr.Search("Apollo rollout staging deployment", 2)
	require.Len(t, results, 2)
	assert.Equal(t, LayerEpisodic, results[0].Layer)
	assert.Equal(t, "episodic/current-task", results[0].ID)
	assert.Equal(t, LayerLongTerm, results[1].Layer)
	assert.Equal(t, "long-term/project-rule", results[1].ID)
}

func TestMemoryManagerSearchAutoRefreshesExternalChanges(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	mgr.SetRefreshInterval(0)
	require.NoError(t, mgr.Load())

	externalPath := filepath.Join(dir, "episodic", "notes.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(externalPath), 0o755))
	require.NoError(t, os.WriteFile(externalPath, []byte("# External Note\n\nThis fact comes from disk."), 0o644))

	results := mgr.Search("external fact", 5)
	require.Len(t, results, 1)
	assert.Equal(t, "episodic/notes", results[0].ID)
	assert.Equal(t, LayerEpisodic, results[0].Layer)

	require.NoError(t, os.Remove(externalPath))
	results = mgr.Search("external fact", 5)
	assert.Empty(t, results)
}

func TestMemoryManagerReindexRefreshesExternalChangesAndStats(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())
	require.NoError(t, mgr.SaveToLayer(LayerLongTerm, "seed", "# Seed\n\nBaseline rule."))

	externalPath := filepath.Join(dir, "long-term", "external.md")
	require.NoError(t, os.WriteFile(externalPath, []byte("# External\n\nFreshly indexed content."), 0o644))

	total, err := mgr.Reindex()
	require.NoError(t, err)
	assert.Equal(t, 2, total)

	entry, ok := mgr.Get("long-term/external")
	require.True(t, ok)
	assert.Equal(t, LayerLongTerm, entry.Layer)

	stats := mgr.Stats()
	assert.Equal(t, 2, stats.Total)
	assert.False(t, stats.LastReindexAt.IsZero())
}

func TestMemoryManagerSupportsCandidateLayer(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	require.NoError(t, mgr.SaveToLayer(LayerCandidates, "decision-observation", "# Decision Observation\n\nObserved candidate knowledge."))

	entry, ok := mgr.Get("decision-observation")
	require.True(t, ok)
	assert.Equal(t, "candidates/decision-observation", entry.ID)
	assert.Equal(t, LayerCandidates, entry.Layer)
	assert.Equal(t, filepath.Join(dir, "episodic", "candidates", "decision-observation.md"), entry.FilePath)
}

func TestMemoryManagerSearchPrefersNewerEntriesOnEqualScore(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	mgr.SetRefreshInterval(0)

	oldPath := filepath.Join(dir, "old.md")
	newPath := filepath.Join(dir, "new.md")
	oldContent := "# Shared Memory\n\nalpha beta gamma old note"
	newContent := "# Shared Memory\n\nalpha beta gamma new note"

	require.NoError(t, os.WriteFile(oldPath, []byte(oldContent), 0o644))
	require.NoError(t, os.WriteFile(newPath, []byte(newContent), 0o644))

	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-1 * time.Hour)
	require.NoError(t, os.Chtimes(oldPath, oldTime, oldTime))
	require.NoError(t, os.Chtimes(newPath, newTime, newTime))

	require.NoError(t, mgr.Load())

	results := mgr.Search("alpha beta gamma", 2)
	require.Len(t, results, 2)
	assert.Equal(t, "new", results[0].ID)
	assert.Equal(t, "old", results[1].ID)
}

func TestMemoryManagerDelete(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	require.NoError(t, mgr.Save("to-delete", "# Delete Me\n\nThis will be deleted."))

	_, ok := mgr.Get("to-delete")
	assert.True(t, ok)

	err := mgr.Delete("to-delete")
	require.NoError(t, err)

	_, ok = mgr.Get("to-delete")
	assert.False(t, ok)

	path := filepath.Join(dir, "long-term", "to-delete.md")
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestMemoryManagerDeleteNonexistent(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	err := mgr.Delete("nonexistent")
	assert.Error(t, err)
}

func TestMemoryManagerStatsAndCleanupExpired(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	now := time.Now().UTC()
	require.NoError(t, mgr.SaveToLayer(LayerCandidates, "expired-candidate", managedTestDoc("Expired Candidate", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "candidate",
		"Expire At":  now.Add(-time.Hour).Format(time.RFC3339),
	}, "Observed", "candidate fact")))
	require.NoError(t, mgr.SaveToLayer(LayerEpisodic, "active-episodic", managedTestDoc("Active Episodic", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "episodic",
		"Expire At":  now.Add(time.Hour).Format(time.RFC3339),
	}, "Summary", "active fact")))
	require.NoError(t, mgr.SaveToLayer(LayerLongTerm, "stable-rule", managedTestDoc("Stable Rule", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "long-term",
	}, "Rule", "never lose the invariant")))
	mgr.SetCleanupInterval(24 * time.Hour)
	mgr.lastCleanupAt = now

	statsBefore := mgr.Stats()
	assert.Equal(t, 3, statsBefore.Total)
	assert.Equal(t, 2, statsBefore.Active)
	assert.Equal(t, 1, statsBefore.Expired)
	assert.Equal(t, 1, statsBefore.Candidates)
	assert.Equal(t, 1, statsBefore.Episodic)
	assert.Equal(t, 1, statsBefore.LongTerm)

	removed, err := mgr.CleanupExpired(now)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	statsAfter := mgr.Stats()
	assert.Equal(t, 2, statsAfter.Total)
	assert.Equal(t, 2, statsAfter.Active)
	assert.Equal(t, 0, statsAfter.Expired)
	assert.Equal(t, 1, statsAfter.LastCleanupRemoved)

	_, ok := mgr.Get("candidates/expired-candidate")
	assert.False(t, ok)
}

func TestMemoryManagerReadPathsDoNotAutoCleanupExpiredEntries(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	now := time.Date(2026, 4, 24, 11, 0, 0, 0, time.UTC)
	require.NoError(t, mgr.SaveToLayer(LayerCandidates, "expired-candidate", managedTestDoc("Expired Candidate", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "candidate",
		"Expire At":  now.Add(-time.Hour).Format(time.RFC3339),
	}, "Observed", "candidate fact")))
	require.NoError(t, mgr.SaveToLayer(LayerLongTerm, "seed", "# Seed\n\nBaseline rule."))

	seed, ok := mgr.Get("long-term/seed")
	require.True(t, ok)
	assert.Equal(t, "long-term/seed", seed.ID)

	statsBeforeCleanup := mgr.Stats()
	assert.Equal(t, 2, statsBeforeCleanup.Total)
	assert.Equal(t, 1, statsBeforeCleanup.Expired)
	assert.Equal(t, 1, statsBeforeCleanup.Candidates)

	removed, err := mgr.CleanupExpired(now)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	statsAfterCleanup := mgr.Stats()
	assert.Equal(t, 1, statsAfterCleanup.Total)
	assert.Equal(t, 0, statsAfterCleanup.Expired)
}

func TestMemoryManagerSearchExplainedTracksRecallAndPromotesObservedEpisodic(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	require.NoError(t, mgr.SaveToLayer(LayerEpisodic, "observed-decision", managedTestDoc("Observed Decision", map[string]string{
		"Managed By":                 "test",
		"Lifecycle":                  "episodic",
		"Recall Count":               "0",
		"Promote After Recall Count": "2",
		"Promotion Status":           "observing",
		"Expire At":                  time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	}, "Key Decisions", "Keep the boundary isolated for runtime validation.")))

	first := mgr.SearchExplained("boundary validation", 2, LayerEpisodic)
	require.Len(t, first, 1)
	assert.Equal(t, "episodic/observed-decision", first[0].Entry.ID)
	assert.ElementsMatch(t, []string{"boundary", "validation"}, first[0].MatchedTerms)

	entry, ok := entryByID(mgr.Entries(), "episodic/observed-decision")
	require.True(t, ok)
	assert.Equal(t, "1", entry.Metadata["Recall Count"])
	assert.Equal(t, "observing", entry.Metadata["Promotion Status"])
	assert.NotEmpty(t, entry.Metadata["Last Recalled At"])
	assert.Equal(t, "boundary, validation", entry.Metadata["Last Matched Terms"])

	second := mgr.SearchExplained("boundary validation", 2, LayerEpisodic)
	require.Len(t, second, 1)

	episodicEntry, ok := entryByID(mgr.Entries(), "episodic/observed-decision")
	require.True(t, ok)
	assert.Equal(t, "promoted", episodicEntry.Metadata["Promotion Status"])
	assert.Equal(t, "long-term/observed-decision", episodicEntry.Metadata["Promoted To"])

	longTermEntry, ok := entryByID(mgr.Entries(), "long-term/observed-decision")
	require.True(t, ok)
	assert.Equal(t, LayerLongTerm, longTermEntry.Layer)
	assert.Equal(t, "recall_promoted", longTermEntry.Metadata["Promotion"])
	assert.Equal(t, "recalled 2 times", longTermEntry.Metadata["Promotion Reason"])
	assert.Equal(t, "episodic/observed-decision", longTermEntry.Metadata["Promoted From"])
	assert.Equal(t, "2", longTermEntry.Metadata["Recall Count"])
}

func TestMemoryManagerPromoteEligiblePromotesExistingCandidatesAndUpdatesStats(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	require.NoError(t, mgr.SaveToLayer(LayerEpisodic, "promote-ready", managedTestDoc("Promote Ready", map[string]string{
		"Managed By":                 "test",
		"Lifecycle":                  "episodic",
		"Recall Count":               "3",
		"Promote After Recall Count": "2",
		"Promotion Status":           "observing",
		"Expire At":                  now.Add(time.Hour).Format(time.RFC3339),
	}, "Outcome", "This decision has already proven stable.")))

	promoted, err := mgr.PromoteEligible(now)
	require.NoError(t, err)
	assert.Equal(t, 1, promoted)

	episodicEntry, ok := entryByID(mgr.Entries(), "episodic/promote-ready")
	require.True(t, ok)
	assert.Equal(t, "promoted", episodicEntry.Metadata["Promotion Status"])
	assert.Equal(t, "long-term/promote-ready", episodicEntry.Metadata["Promoted To"])

	longTermEntry, ok := entryByID(mgr.Entries(), "long-term/promote-ready")
	require.True(t, ok)
	assert.Equal(t, "recall_promoted", longTermEntry.Metadata["Promotion"])
	assert.Equal(t, "episodic/promote-ready", longTermEntry.Metadata["Promoted From"])
	assert.Equal(t, "recalled 3 times", longTermEntry.Metadata["Promotion Reason"])

	stats := mgr.Stats()
	assert.Equal(t, now, stats.LastPromotionAt)
	assert.Equal(t, 1, stats.LastPromotionCount)
}

func TestMemoryManagerSearchScopedFiltersSessionLifecycleEntries(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	require.NoError(t, mgr.SaveToLayer(LayerEpisodic, "shared-note", "# Shared Note\n\nThis note is shared across sessions."))
	require.NoError(t, mgr.SaveToLayer(LayerEpisodic, "session-a", managedTestDoc("Session A", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "episodic",
		"Scope":      "session",
		"Agent":      "lead",
		"Session":    "sess-a",
	}, "Outcome", "Polaris launch code remains active.")))
	require.NoError(t, mgr.SaveToLayer(LayerLongTerm, "session-b", managedTestDoc("Session B", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "long-term",
		"Scope":      "session",
		"Agent":      "lead",
		"Session":    "sess-b",
	}, "Decision", "Use the session-b architecture baseline.")))

	resultsA := mgr.SearchExplainedScoped("session shared architecture", 5, SearchScope{AgentID: "lead", SessionID: "sess-a"}, LayerEpisodic, LayerLongTerm)
	idsA := make([]string, 0, len(resultsA))
	for _, match := range resultsA {
		idsA = append(idsA, match.Entry.ID)
	}
	assert.Contains(t, idsA, "episodic/shared-note")
	assert.Contains(t, idsA, "episodic/session-a")
	assert.NotContains(t, idsA, "long-term/session-b")

	resultsB := mgr.SearchExplainedScoped("session shared architecture", 5, SearchScope{AgentID: "lead", SessionID: "sess-b"}, LayerEpisodic, LayerLongTerm)
	idsB := make([]string, 0, len(resultsB))
	for _, match := range resultsB {
		idsB = append(idsB, match.Entry.ID)
	}
	assert.Contains(t, idsB, "episodic/shared-note")
	assert.Contains(t, idsB, "long-term/session-b")
	assert.NotContains(t, idsB, "episodic/session-a")
}

func TestMemoryManagerGetScopedBlocksCrossSessionLifecycleEntry(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	require.NoError(t, mgr.SaveToLayer(LayerEpisodic, "session-locked", managedTestDoc("Session Locked", map[string]string{
		"Managed By": "test",
		"Lifecycle":  "episodic",
		"Scope":      "session",
		"Agent":      "lead",
		"Session":    "sess-a",
	}, "Outcome", "Only session A may use this memory.")))

	_, ok := mgr.GetScoped("episodic/session-locked", SearchScope{AgentID: "lead", SessionID: "sess-b"})
	assert.False(t, ok)

	entry, ok := mgr.GetScoped("episodic/session-locked", SearchScope{AgentID: "lead", SessionID: "sess-a"})
	require.True(t, ok)
	assert.Equal(t, "episodic/session-locked", entry.ID)
}

func TestFormatMemoryForPrompt(t *testing.T) {
	entries := []Entry{
		{ID: "episodic/test", Layer: LayerEpisodic, Title: "Test Entry", Content: "Content here", Metadata: map[string]string{
			"Capture Trigger":    "memory_save",
			"Memory Source":      "explicit_save",
			"Last Matched Terms": "content",
			"Promotion Reason":   "explicit_save",
		}},
	}

	result := FormatForPrompt(entries)
	assert.Contains(t, result, "## Relevant Memory")
	assert.Contains(t, result, "### [Episodic] Test Entry")
	assert.Contains(t, result, "- ID: episodic/test")
	assert.Contains(t, result, "- Summary: Content here")
	assert.Contains(t, result, "- Why: trigger memory_save")
	assert.Contains(t, result, "reason explicit_save")
}

func TestFormatMemoryForPromptEmpty(t *testing.T) {
	result := FormatForPrompt(nil)
	assert.Equal(t, "", result)
}

func TestFormatMemoryForPromptTruncation(t *testing.T) {
	longContent := ""
	for i := 0; i < 300; i++ {
		longContent += "This is line number " + string(rune('0'+i%10)) + " of a very long memory entry.\n"
	}

	entries := []Entry{
		{ID: "long-term/long", Layer: LayerLongTerm, Title: "Long Entry", Content: longContent},
	}

	result := FormatForPrompt(entries)
	assert.Contains(t, result, "### [Long-term] Long Entry")
	assert.Contains(t, result, "- ID: long-term/long")
	assert.Contains(t, result, "...")
}

func managedTestDoc(title string, meta map[string]string, section string, lines ...string) string {
	doc := managedDocument{
		Title:      title,
		Generated:  time.Now().UTC().Format(time.RFC3339),
		Metadata:   copyMetadata(meta),
		Sections:   map[string][]string{section: append([]string(nil), lines...)},
		SectionSeq: []string{section},
	}
	return renderManagedDocument(doc)
}

func entryByID(entries []Entry, id string) (Entry, bool) {
	for _, entry := range entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return Entry{}, false
}

func TestPromptSummarySkipsGeneratedMetadata(t *testing.T) {
	entry := Entry{
		ID:      "episodic/session-note",
		Layer:   LayerEpisodic,
		Title:   "Session Note",
		Content: "# Session Note\n\nGenerated at: 2026-04-17T00:00:00Z\n\n- Agent: lead\n- Session: s1\n\n## Outcome\n- Runtime validation is active.\n- Keep responses concise.\n",
	}

	summary := PromptSummary(entry)
	assert.Equal(t, "Runtime validation is active. Keep responses concise.", summary)
}
