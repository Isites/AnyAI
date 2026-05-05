package memory

import (
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultRefreshInterval = 2 * time.Second

// Layer identifies the storage/retrieval tier of a memory entry.
type Layer string

const (
	LayerCandidates Layer = "candidates"
	LayerEpisodic   Layer = "episodic"
	LayerLongTerm   Layer = "long-term"
)

var defaultSearchOrder = []Layer{LayerEpisodic, LayerLongTerm}

// Entry represents a single memory entry stored as a Markdown file.
type Entry struct {
	ID       string            `json:"id"` // derived from filename
	Title    string            `json:"title"`
	Content  string            `json:"content"`
	FilePath string            `json:"file_path,omitempty"`
	ModTime  time.Time         `json:"mod_time,omitempty"`
	Layer    Layer             `json:"layer"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Manager handles persistent memory stored as Markdown files with BM25 search.
type Manager struct {
	baseDir            string
	entries            map[string]Entry
	index              *BM25Index
	lastSync           time.Time
	lastReindexAt      time.Time
	lastPromotionAt    time.Time
	lastPromotionCount int
	refreshInterval    time.Duration
	lastCleanupAt      time.Time
	lastCleanupRemoved int
	cleanupInterval    time.Duration
	mu                 sync.RWMutex
}

// NewManager creates a new memory manager rooted at the given directory.
func NewManager(baseDir string) *Manager {
	return &Manager{
		baseDir:         baseDir,
		entries:         make(map[string]Entry),
		index:           NewBM25Index(),
		refreshInterval: defaultRefreshInterval,
		cleanupInterval: defaultRefreshInterval,
	}
}

// Load scans the memory directory and indexes all Markdown files.
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloadLocked(true)
}

// Sync forces a rescan from disk so external file changes become visible
// without restarting a long-running process.
func (m *Manager) Sync() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloadLocked(true)
}

// Reindex forces a full rescan from disk and rebuilds the search index.
func (m *Manager) Reindex() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reloadLocked(true); err != nil {
		return 0, err
	}
	m.lastReindexAt = time.Now().UTC()
	return len(m.entries), nil
}

// SetRefreshInterval controls how often read operations rescan the memory
// directory for external file changes. Non-positive values force a refresh on
// every read.
func (m *Manager) SetRefreshInterval(interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshInterval = interval
}

// SetCleanupInterval controls how often maintenance hooks may perform
// opportunistic TTL cleanup.
func (m *Manager) SetCleanupInterval(interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupInterval = interval
}

func (m *Manager) refreshMaybe() {
	if err := m.syncIfStale(); err != nil {
		runtimelogging.Warn("failed to refresh memory from disk", "dir", m.baseDir, "error", err)
	}
}

func (m *Manager) syncIfStale() error {
	m.mu.RLock()
	interval := m.refreshInterval
	lastSync := m.lastSync
	m.mu.RUnlock()

	if interval > 0 && !lastSync.IsZero() && time.Since(lastSync) < interval {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if interval > 0 && !m.lastSync.IsZero() && time.Since(m.lastSync) < interval {
		return nil
	}
	return m.reloadLocked(false)
}

func (m *Manager) cleanupIfStale(now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	m.mu.RLock()
	interval := m.cleanupInterval
	lastCleanup := m.lastCleanupAt
	m.mu.RUnlock()

	if interval > 0 && !lastCleanup.IsZero() && time.Since(lastCleanup) < interval {
		return 0, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if interval > 0 && !m.lastCleanupAt.IsZero() && time.Since(m.lastCleanupAt) < interval {
		return 0, nil
	}
	return m.cleanupExpiredLocked(now)
}

// CleanupExpiredMaybe removes expired lifecycle entries when the configured
// cleanup interval has elapsed. Unlike CleanupExpired, it is intended for
// maintenance hooks and never runs implicitly on read paths.
func (m *Manager) CleanupExpiredMaybe(now time.Time) (int, error) {
	return m.cleanupIfStale(now)
}

func (m *Manager) reloadLocked(logLoad bool) error {
	if err := os.MkdirAll(m.baseDir, 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	m.entries = make(map[string]Entry)

	err := filepath.WalkDir(m.baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			runtimelogging.Warn("failed to read memory entry", "path", path, "error", err)
			return nil
		}

		info, _ := d.Info()
		modTime := time.Now()
		if info != nil {
			modTime = info.ModTime()
		}

		relPath, err := filepath.Rel(m.baseDir, path)
		if err != nil {
			relPath = d.Name()
		}
		content := string(data)
		layer, logicalID, canonicalID := parseEntryPath(relPath)
		managedDoc, managed := parseManagedDocument(content)

		entry := Entry{
			ID:       canonicalID,
			Title:    extractTitle(logicalID, content),
			Content:  content,
			FilePath: path,
			ModTime:  modTime,
			Layer:    layer,
		}
		if managed {
			entry.Metadata = copyMetadata(managedDoc.Metadata)
			if generated := strings.TrimSpace(managedDoc.Generated); generated != "" {
				if entry.Metadata == nil {
					entry.Metadata = map[string]string{}
				}
				entry.Metadata["Generated At"] = generated
			}
		}
		if !managed && entry.Metadata == nil {
			entry.Metadata = nil
		}

		m.entries[canonicalID] = entry
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan memory dir: %w", err)
	}

	m.rebuildIndexLocked()
	m.lastSync = time.Now()
	if logLoad {
		runtimelogging.Info("loaded memory entries", "count", len(m.entries))
	}
	return nil
}

func (m *Manager) rebuildIndexLocked() {
	m.index = NewBM25Index()
	ids := make([]string, 0, len(m.entries))
	for id := range m.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m.index.Add(id, m.entries[id].Content)
	}
}

// CleanupExpired removes TTL-governed entries whose expiry has elapsed.
func (m *Manager) CleanupExpired(now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reloadLocked(false); err != nil {
		return 0, err
	}
	return m.cleanupExpiredLocked(now)
}

// CleanupStale explicitly removes expired lifecycle entries as a maintenance action.
func (m *Manager) CleanupStale(now time.Time) (int, error) {
	return m.CleanupExpired(now)
}

func (m *Manager) cleanupExpiredLocked(now time.Time) (int, error) {
	expiredIDs := make([]string, 0)
	for id, entry := range m.entries {
		if expiresAt := entryExpiresAt(entry); expiresAt.IsZero() || expiresAt.After(now) {
			continue
		}
		expiredIDs = append(expiredIDs, id)
	}
	if len(expiredIDs) == 0 {
		m.lastCleanupAt = now
		m.lastCleanupRemoved = 0
		return 0, nil
	}
	sort.Strings(expiredIDs)
	for _, id := range expiredIDs {
		entry := m.entries[id]
		if err := os.Remove(entry.FilePath); err != nil && !os.IsNotExist(err) {
			return 0, fmt.Errorf("cleanup memory entry %s: %w", id, err)
		}
		delete(m.entries, id)
	}
	m.rebuildIndexLocked()
	m.lastSync = now
	m.lastCleanupAt = now
	m.lastCleanupRemoved = len(expiredIDs)
	return len(expiredIDs), nil
}

// Save writes a long-term memory entry to disk and updates the index.
func (m *Manager) Save(id, content string) error {
	return m.SaveToLayer(LayerLongTerm, id, content)
}

// SaveToLayer writes a memory entry into the requested layer and updates the index.
func (m *Manager) SaveToLayer(layer Layer, id, content string) error {
	layer = normalizeLayer(layer)
	entryID, path, err := m.entryLocation(layer, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write memory entry: %w", err)
	}

	entry := Entry{
		ID:       entryID,
		Title:    extractTitle(entryID, content),
		Content:  content,
		FilePath: path,
		ModTime:  time.Now(),
		Layer:    layer,
	}
	if managedDoc, ok := parseManagedDocument(content); ok {
		entry.Metadata = copyMetadata(managedDoc.Metadata)
		if generated := strings.TrimSpace(managedDoc.Generated); generated != "" {
			if entry.Metadata == nil {
				entry.Metadata = map[string]string{}
			}
			entry.Metadata["Generated At"] = generated
		}
	}

	m.mu.Lock()
	m.entries[entry.ID] = entry
	m.rebuildIndexLocked()
	m.lastSync = time.Now()
	m.mu.Unlock()

	return nil
}

// Search queries the memory using BM25 and returns relevant entries.
func (m *Manager) Search(query string, maxResults int) []Entry {
	return m.SearchScoped(query, maxResults, SearchScope{}, defaultSearchOrder...)
}

// SearchLayers searches specific layers and records recall metadata for the
// returned entries when they are managed lifecycle documents.
func (m *Manager) SearchLayers(query string, maxResults int, layers ...Layer) []Entry {
	return m.SearchScoped(query, maxResults, SearchScope{}, layers...)
}

// SearchScoped searches layers using a runtime scope such as agent/session.
func (m *Manager) SearchScoped(query string, maxResults int, scope SearchScope, layers ...Layer) []Entry {
	matches := m.SearchExplainedScoped(query, maxResults, scope, layers...)
	out := make([]Entry, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.Entry)
	}
	return out
}

// SearchExplained searches memory and returns explainable matches.
func (m *Manager) SearchExplained(query string, maxResults int, layers ...Layer) []SearchMatch {
	return m.SearchExplainedScoped(query, maxResults, SearchScope{}, layers...)
}

// SearchExplainedScoped searches memory and returns explainable matches within
// the provided scope.
func (m *Manager) SearchExplainedScoped(query string, maxResults int, scope SearchScope, layers ...Layer) []SearchMatch {
	if maxResults <= 0 {
		maxResults = 5
	}
	if len(layers) == 0 {
		layers = append([]Layer(nil), defaultSearchOrder...)
	}

	m.refreshMaybe()
	m.mu.Lock()
	defer m.mu.Unlock()

	matches := m.searchMatchesLocked(query, maxResults, scope, layers...)
	if len(matches) > 0 {
		m.recordRecallLocked(time.Now().UTC(), matches...)
	}
	return matches
}

// Entries returns all memory entries.
func (m *Manager) Entries() []Entry {
	m.refreshMaybe()
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.After(entries[j].ModTime)
		}
		return entries[i].ID < entries[j].ID
	})
	return entries
}

// Stats reports a compact operational summary of the current memory store.
func (m *Manager) Stats() Stats {
	m.refreshMaybe()
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UTC()
	stats := Stats{
		Total:              len(m.entries),
		LastReindexAt:      m.lastReindexAt,
		LastPromotionAt:    m.lastPromotionAt,
		LastPromotionCount: m.lastPromotionCount,
		LastCleanupAt:      m.lastCleanupAt,
		LastCleanupRemoved: m.lastCleanupRemoved,
	}
	for _, entry := range m.entries {
		expiresAt := entryExpiresAt(entry)
		if !expiresAt.IsZero() && !expiresAt.After(now) {
			stats.Expired++
		} else {
			stats.Active++
		}
		switch normalizeLayer(entry.Layer) {
		case LayerCandidates:
			stats.Candidates++
		case LayerEpisodic:
			stats.Episodic++
		case LayerLongTerm:
			stats.LongTerm++
		}
	}
	return stats
}

// PromoteEligible scans managed episodic entries and promotes those whose
// recall counters already satisfy their configured promotion threshold.
func (m *Manager) PromoteEligible(now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reloadLocked(false); err != nil {
		return 0, err
	}

	ids := make([]string, 0, len(m.entries))
	for id := range m.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var promotedCount int
	changed := false
	for _, id := range ids {
		entry := m.entries[id]
		doc, ok := parseManagedDocument(entry.Content)
		if !ok {
			continue
		}
		targetID, eligible := eligiblePromotionTarget(entry, doc)
		if !eligible {
			continue
		}

		if _, exists := m.entries[targetID]; !exists {
			if promotedID, promoted := m.promoteOnRecallLocked(entry, doc, now); promoted {
				promotedCount++
				changed = true
				targetID = promotedID
			}
		}

		if m.markSourcePromotedLocked(entry, doc, targetID, now) {
			changed = true
		}
	}

	if changed {
		m.rebuildIndexLocked()
		m.lastSync = now
	}
	m.lastPromotionAt = now
	m.lastPromotionCount = promotedCount
	return promotedCount, nil
}

// Get returns a specific memory entry by ID.
func (m *Manager) Get(id string) (Entry, bool) {
	return m.GetScoped(id, SearchScope{})
}

// GetScoped returns a memory entry only if it is visible to the provided
// runtime scope.
func (m *Manager) GetScoped(id string, scope SearchScope) (Entry, bool) {
	m.refreshMaybe()
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.resolveIDLocked(id)
	if !ok {
		return Entry{}, false
	}
	e, ok := m.entries[id]
	if ok && !scope.allows(e) {
		return Entry{}, false
	}
	if ok {
		m.recordRecallLocked(time.Now().UTC(), SearchMatch{Entry: e})
		e = m.entries[id]
	}
	return e, ok
}

// Delete removes a memory entry.
func (m *Manager) Delete(id string) error {
	if err := m.syncIfStale(); err != nil {
		runtimelogging.Warn("failed to refresh memory before delete", "dir", m.baseDir, "error", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	requestedID := id
	id, ok := m.resolveIDLocked(id)
	if !ok {
		return fmt.Errorf("memory entry not found: %s", requestedID)
	}

	entry, ok := m.entries[id]
	if !ok {
		return fmt.Errorf("memory entry not found: %s", id)
	}

	if err := os.Remove(entry.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete memory file: %w", err)
	}

	delete(m.entries, id)

	m.rebuildIndexLocked()
	m.lastSync = time.Now()

	return nil
}

const promptSummaryMaxLen = 280

// FormatForPrompt formats relevant memory entries as compact memory cards for
// injection into the system prompt.
func FormatForPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n## Relevant Memory\n\n")

	for _, e := range entries {
		b.WriteString("### ")
		if label := promptLayerLabel(e.Layer); label != "" {
			b.WriteString("[")
			b.WriteString(label)
			b.WriteString("] ")
		}
		b.WriteString(e.Title)
		b.WriteString("\n")
		b.WriteString("- ID: ")
		b.WriteString(e.ID)
		b.WriteString("\n")
		if summary := PromptSummary(e); summary != "" {
			b.WriteString("- Summary: ")
			b.WriteString(summary)
			b.WriteString("\n")
		}
		if explanation := promptExplainability(e); explanation != "" {
			b.WriteString("- Why: ")
			b.WriteString(explanation)
			b.WriteString("\n")
		}
		b.WriteString("\n\n")
	}

	return b.String()
}

// PromptSummary extracts a compact summary suitable for prompt injection and
// search results. It intentionally strips generated metadata and keeps the most
// relevant human-meaningful lines.
func PromptSummary(entry Entry) string {
	return summarizePromptContent(entry.Content, promptSummaryMaxLen)
}

func (m *Manager) searchLocked(query string, maxResults int, order ...Layer) []Entry {
	matches := m.searchMatchesLocked(query, maxResults, SearchScope{}, order...)
	out := make([]Entry, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.Entry)
	}
	return out
}

func (m *Manager) searchMatchesLocked(query string, maxResults int, scope SearchScope, order ...Layer) []SearchMatch {
	results := m.index.Search(query, len(m.entries))

	type scoredEntry struct {
		entry Entry
		score float64
	}

	layered := make(map[Layer][]scoredEntry, len(order))
	for _, r := range results {
		e, ok := m.entries[r.ID]
		if !ok {
			continue
		}
		if !scope.allows(e) {
			continue
		}
		layer := normalizeLayer(e.Layer)
		layered[layer] = append(layered[layer], scoredEntry{
			entry: e,
			score: r.Score,
		})
	}

	for layer := range layered {
		sort.Slice(layered[layer], func(i, j int) bool {
			if layered[layer][i].score != layered[layer][j].score {
				return layered[layer][i].score > layered[layer][j].score
			}
			if !layered[layer][i].entry.ModTime.Equal(layered[layer][j].entry.ModTime) {
				return layered[layer][i].entry.ModTime.After(layered[layer][j].entry.ModTime)
			}
			return layered[layer][i].entry.ID < layered[layer][j].entry.ID
		})
	}

	out := make([]SearchMatch, 0, minInt(maxResults, len(results)))
	seen := map[string]struct{}{}
	queryTokens := tokenize(query)
	for _, layer := range order {
		for _, item := range layered[normalizeLayer(layer)] {
			key := recallDedupeKey(item.entry)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, SearchMatch{
				Entry:        item.entry,
				Score:        item.score,
				MatchedTerms: matchedTerms(queryTokens, item.entry),
			})
			if len(out) >= maxResults {
				return out
			}
		}
	}
	return out
}

func matchedTerms(queryTokens []string, entry Entry) []string {
	if len(queryTokens) == 0 {
		return nil
	}
	content := strings.ToLower(entry.Content)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(queryTokens))
	for _, token := range queryTokens {
		token = strings.TrimSpace(strings.ToLower(token))
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		if strings.Contains(content, token) {
			seen[token] = struct{}{}
			out = append(out, token)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Manager) recordRecallLocked(now time.Time, matches ...SearchMatch) {
	changed := false
	promotionCount := 0
	for _, match := range matches {
		entry, ok := m.entries[match.Entry.ID]
		if !ok {
			continue
		}
		if !isManagedLifecycleEntry(entry) {
			continue
		}
		doc, ok := parseManagedDocument(entry.Content)
		if !ok {
			continue
		}
		if doc.Metadata == nil {
			doc.Metadata = make(map[string]string)
		}
		recallCount := metadataInt(doc.Metadata, "Recall Count")
		doc.Metadata["Recall Count"] = strconv.Itoa(recallCount + 1)
		doc.Metadata["Last Recalled At"] = now.UTC().Format(time.RFC3339)
		if len(match.MatchedTerms) > 0 {
			doc.Metadata["Last Matched Terms"] = strings.Join(match.MatchedTerms, ", ")
		}
		if normalizeLayer(entry.Layer) == LayerEpisodic {
			if promotedID, promoted := m.promoteOnRecallLocked(entry, doc, now); promoted {
				doc.Metadata["Promotion Status"] = "promoted"
				doc.Metadata["Promoted To"] = promotedID
				promotionCount++
			}
		}

		updated := renderManagedDocument(doc)
		if err := os.WriteFile(entry.FilePath, []byte(updated), 0o644); err != nil {
			runtimelogging.Warn("failed to update memory recall metadata", "id", entry.ID, "path", entry.FilePath, "error", err)
			continue
		}
		entry.Content = updated
		entry.ModTime = now
		entry.Metadata = copyMetadata(doc.Metadata)
		m.entries[entry.ID] = entry
		changed = true
	}
	if changed {
		m.rebuildIndexLocked()
		m.lastSync = now
	}
	if promotionCount > 0 {
		m.lastPromotionAt = now
		m.lastPromotionCount = promotionCount
	}
}

func isManagedLifecycleEntry(entry Entry) bool {
	if len(entry.Metadata) == 0 {
		return false
	}
	if strings.TrimSpace(entry.Metadata["Managed By"]) != "" {
		return true
	}
	return strings.TrimSpace(entry.Metadata["Lifecycle"]) != ""
}

func entryExpiresAt(entry Entry) time.Time {
	if len(entry.Metadata) == 0 {
		return time.Time{}
	}
	return metadataTime(entry.Metadata, "Expire At")
}

func (m *Manager) promoteOnRecallLocked(entry Entry, doc managedDocument, now time.Time) (string, bool) {
	logicalID, promotedID, ok := promotionTarget(entry, doc)
	if !ok {
		return "", false
	}
	if _, exists := m.entries[promotedID]; exists {
		return promotedID, false
	}

	promotedDoc := managedDocument{
		Title:      doc.Title,
		Generated:  doc.Generated,
		Metadata:   copyMetadata(doc.Metadata),
		Sections:   cloneManagedSections(doc.Sections),
		SectionSeq: append([]string(nil), doc.SectionSeq...),
	}
	if promotedDoc.Metadata == nil {
		promotedDoc.Metadata = make(map[string]string)
	}
	recallCount := metadataInt(promotedDoc.Metadata, "Recall Count")
	delete(promotedDoc.Metadata, "Expire At")
	delete(promotedDoc.Metadata, "Promote After Recall Count")
	promotedDoc.Metadata["Lifecycle"] = "long-term"
	promotedDoc.Metadata["Promotion"] = "recall_promoted"
	promotedDoc.Metadata["Promotion Reason"] = fmt.Sprintf("recalled %d times", recallCount)
	promotedDoc.Metadata["Promoted From"] = entry.ID
	promotedDoc.Metadata["Promoted At"] = now.UTC().Format(time.RFC3339)

	_, targetPath, err := m.entryLocation(LayerLongTerm, logicalID)
	if err != nil {
		runtimelogging.Warn("failed to resolve promoted memory path", "id", entry.ID, "error", err)
		return "", false
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		runtimelogging.Warn("failed to create promoted memory dir", "path", targetPath, "error", err)
		return "", false
	}

	updated := renderManagedDocument(promotedDoc)
	if err := os.WriteFile(targetPath, []byte(updated), 0o644); err != nil {
		runtimelogging.Warn("failed to promote memory entry", "source", entry.ID, "target", promotedID, "error", err)
		return "", false
	}

	m.entries[promotedID] = Entry{
		ID:       promotedID,
		Title:    extractTitle(promotedID, updated),
		Content:  updated,
		FilePath: targetPath,
		ModTime:  now,
		Layer:    LayerLongTerm,
		Metadata: copyMetadata(promotedDoc.Metadata),
	}
	return promotedID, true
}

func promotionTarget(entry Entry, doc managedDocument) (string, string, bool) {
	if normalizeLayer(entry.Layer) != LayerEpisodic {
		return "", "", false
	}
	if strings.TrimSpace(doc.Metadata["Lifecycle"]) != "episodic" {
		return "", "", false
	}

	threshold := metadataInt(doc.Metadata, "Promote After Recall Count")
	if threshold <= 0 {
		return "", "", false
	}
	recallCount := metadataInt(doc.Metadata, "Recall Count")
	if recallCount < threshold {
		return "", "", false
	}

	logicalID := strings.TrimPrefix(entry.ID, string(LayerEpisodic)+"/")
	return logicalID, canonicalEntryID(LayerLongTerm, logicalID), true
}

func eligiblePromotionTarget(entry Entry, doc managedDocument) (string, bool) {
	_, promotedID, ok := promotionTarget(entry, doc)
	if !ok {
		return "", false
	}
	return promotedID, true
}

func (m *Manager) markSourcePromotedLocked(entry Entry, doc managedDocument, promotedID string, now time.Time) bool {
	if strings.TrimSpace(promotedID) == "" {
		return false
	}
	if doc.Metadata == nil {
		doc.Metadata = make(map[string]string)
	}
	changed := false
	if strings.TrimSpace(doc.Metadata["Promotion Status"]) != "promoted" {
		doc.Metadata["Promotion Status"] = "promoted"
		changed = true
	}
	if strings.TrimSpace(doc.Metadata["Promoted To"]) != promotedID {
		doc.Metadata["Promoted To"] = promotedID
		changed = true
	}
	if !changed {
		return false
	}

	updated := renderManagedDocument(doc)
	if err := os.WriteFile(entry.FilePath, []byte(updated), 0o644); err != nil {
		runtimelogging.Warn("failed to mark promoted source memory entry", "source", entry.ID, "target", promotedID, "error", err)
		return false
	}

	entry.Content = updated
	entry.ModTime = now
	entry.Metadata = copyMetadata(doc.Metadata)
	m.entries[entry.ID] = entry
	return true
}

func (m *Manager) resolveIDLocked(id string) (string, bool) {
	normalized := normalizeLookupID(id)
	if normalized == "" {
		return "", false
	}
	if strings.Contains(normalized, "/") {
		_, ok := m.entries[normalized]
		return normalized, ok
	}
	candidates := []string{
		canonicalEntryID(LayerLongTerm, normalized),
		canonicalEntryID(LayerCandidates, normalized),
		normalized,
		canonicalEntryID(LayerEpisodic, normalized),
	}
	for _, candidate := range candidates {
		if _, ok := m.entries[candidate]; ok {
			return candidate, true
		}
	}
	return "", false
}

func (m *Manager) entryLocation(layer Layer, id string) (string, string, error) {
	cleanID := normalizeLookupID(id)
	if cleanID == "" {
		return "", "", fmt.Errorf("memory entry id is required")
	}

	baseDir := m.baseDir
	entryID := cleanID
	switch normalizeLayer(layer) {
	case LayerCandidates:
		baseDir = filepath.Join(m.baseDir, string(LayerEpisodic), string(LayerCandidates))
		entryID = canonicalEntryID(LayerCandidates, cleanID)
	case LayerEpisodic:
		baseDir = filepath.Join(m.baseDir, string(LayerEpisodic))
		entryID = canonicalEntryID(LayerEpisodic, cleanID)
	case LayerLongTerm:
		baseDir = filepath.Join(m.baseDir, string(LayerLongTerm))
		entryID = canonicalEntryID(LayerLongTerm, cleanID)
	default:
		baseDir = filepath.Join(m.baseDir, string(LayerLongTerm))
		entryID = canonicalEntryID(LayerLongTerm, cleanID)
	}

	return entryID, filepath.Join(baseDir, filepath.FromSlash(cleanID)+".md"), nil
}

func normalizeLayer(layer Layer) Layer {
	switch strings.TrimSpace(string(layer)) {
	case string(LayerCandidates), "candidate":
		return LayerCandidates
	case string(LayerEpisodic):
		return LayerEpisodic
	case string(LayerLongTerm), "long_term":
		return LayerLongTerm
	default:
		return LayerLongTerm
	}
}

func normalizeLookupID(id string) string {
	id = strings.TrimSpace(filepath.ToSlash(id))
	id = strings.TrimSuffix(id, ".md")
	if id == "" {
		return ""
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+id), "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func canonicalEntryID(layer Layer, id string) string {
	return string(normalizeLayer(layer)) + "/" + normalizeLookupID(id)
}

func parseEntryPath(relPath string) (Layer, string, string) {
	rel := normalizeLookupID(relPath)
	layer := LayerLongTerm
	canonicalID := rel
	logicalID := rel

	switch {
	case strings.HasPrefix(rel, "episodic/candidates/"):
		logicalID = strings.TrimPrefix(rel, "episodic/candidates/")
		canonicalID = canonicalEntryID(LayerCandidates, logicalID)
		layer = LayerCandidates
	case strings.HasPrefix(rel, "candidates/"):
		logicalID = strings.TrimPrefix(rel, "candidates/")
		canonicalID = canonicalEntryID(LayerCandidates, logicalID)
		layer = LayerCandidates
	case strings.HasPrefix(rel, "episodic/"):
		logicalID = strings.TrimPrefix(rel, "episodic/")
		logicalID = strings.TrimPrefix(logicalID, "entries/")
		canonicalID = canonicalEntryID(LayerEpisodic, logicalID)
		layer = LayerEpisodic
	case strings.HasPrefix(rel, "long-term/"):
		logicalID = strings.TrimPrefix(rel, "long-term/")
		logicalID = strings.TrimPrefix(logicalID, "entries/")
		canonicalID = canonicalEntryID(LayerLongTerm, logicalID)
		layer = LayerLongTerm
	case strings.HasPrefix(rel, "long_term/"):
		logicalID = strings.TrimPrefix(rel, "long_term/")
		logicalID = strings.TrimPrefix(logicalID, "entries/")
		canonicalID = canonicalEntryID(LayerLongTerm, logicalID)
		layer = LayerLongTerm
	case strings.HasPrefix(rel, "entries/"):
		logicalID = strings.TrimPrefix(rel, "entries/")
		canonicalID = logicalID
		layer = LayerLongTerm
	default:
		canonicalID = logicalID
		layer = LayerLongTerm
	}

	return layer, logicalID, canonicalID
}

func extractTitle(id, content string) string {
	title := id
	if idx := strings.Index(content, "# "); idx >= 0 {
		end := strings.Index(content[idx:], "\n")
		if end > 0 {
			return strings.TrimPrefix(content[idx:idx+end], "# ")
		}
	}
	if strings.Contains(title, "/") {
		parts := strings.Split(title, "/")
		title = parts[len(parts)-1]
	}
	return title
}

func promptLayerLabel(layer Layer) string {
	switch normalizeLayer(layer) {
	case LayerCandidates:
		return "Candidate"
	case LayerEpisodic:
		return "Episodic"
	case LayerLongTerm:
		return "Long-term"
	default:
		return ""
	}
}

func promptExplainability(entry Entry) string {
	if len(entry.Metadata) == 0 {
		return ""
	}
	var parts []string
	if trigger := strings.TrimSpace(entry.Metadata["Capture Trigger"]); trigger != "" {
		parts = append(parts, "trigger "+trigger)
	}
	if mode := strings.TrimSpace(entry.Metadata["Confirmation Mode"]); mode != "" {
		parts = append(parts, "mode "+mode)
	}
	if promotedFrom := strings.TrimSpace(entry.Metadata["Promoted From"]); promotedFrom != "" {
		parts = append(parts, "from "+promotedFrom)
	}
	if reason := strings.TrimSpace(entry.Metadata["Promotion Reason"]); reason != "" {
		parts = append(parts, "reason "+reason)
	}
	if matchedTerms := strings.TrimSpace(entry.Metadata["Last Matched Terms"]); matchedTerms != "" {
		parts = append(parts, "matched "+matchedTerms)
	}
	return strings.Join(parts, " · ")
}

func dedupeKey(content string) string {
	return strings.ToLower(strings.Join(strings.Fields(content), " "))
}

func recallDedupeKey(entry Entry) string {
	summary := strings.TrimSpace(PromptSummary(entry))
	if summary != "" {
		return strings.ToLower(summary)
	}
	return dedupeKey(entry.Content)
}

func summarizePromptContent(content string, limit int) string {
	lines := strings.Split(content, "\n")
	fragments := make([]string, 0, len(lines))
	inSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			inSection = true
			continue
		}
		if strings.HasPrefix(trimmed, "Generated at:") {
			continue
		}

		if strings.HasPrefix(trimmed, "- ") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if !inSection && looksLikePromptMetadata(value) {
				continue
			}
			fragments = append(fragments, value)
			continue
		}

		fragments = append(fragments, trimmed)
	}

	summary := strings.Join(fragments, " ")
	summary = strings.Join(strings.Fields(summary), " ")
	return trimPromptSummary(summary, limit)
}

func looksLikePromptMetadata(line string) bool {
	if line == "" {
		return false
	}
	colon := strings.Index(line, ":")
	if colon <= 0 {
		return false
	}
	key := strings.TrimSpace(line[:colon])
	if key == "" {
		return false
	}
	if strings.Contains(key, " ") {
		return true
	}
	r := []rune(key)
	return len(r) > 0 && ((r[0] >= 'A' && r[0] <= 'Z') || (r[0] >= 'a' && r[0] <= 'z'))
}

func trimPromptSummary(summary string, limit int) string {
	summary = strings.TrimSpace(summary)
	runes := []rune(summary)
	if limit <= 0 || len(runes) <= limit {
		return summary
	}

	cut := limit
	for cut > limit/2 && cut < len(runes) && runes[cut] != ' ' {
		cut--
	}
	if cut <= limit/2 {
		cut = limit
	}
	return strings.TrimSpace(string(runes[:cut])) + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
