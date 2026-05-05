package memory

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// SearchMatch captures a memory search result plus lightweight explainability.
type SearchMatch struct {
	Entry        Entry    `json:"entry"`
	Score        float64  `json:"score"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
}

// SearchScope constrains memory lookup so session-scoped lifecycle memories do
// not bleed across unrelated conversations.
type SearchScope struct {
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// Stats is a compact operational summary of the current memory store.
type Stats struct {
	Total              int       `json:"total"`
	Active             int       `json:"active"`
	Episodic           int       `json:"episodic"`
	Candidates         int       `json:"candidates"`
	LongTerm           int       `json:"long_term"`
	Expired            int       `json:"expired"`
	LastReindexAt      time.Time `json:"last_reindex_at,omitempty"`
	LastPromotionAt    time.Time `json:"last_promotion_at,omitempty"`
	LastPromotionCount int       `json:"last_promotion_count,omitempty"`
	LastCleanupAt      time.Time `json:"last_cleanup_at,omitempty"`
	LastCleanupRemoved int       `json:"last_cleanup_removed,omitempty"`
}

type managedDocument struct {
	Title      string
	Generated  string
	Metadata   map[string]string
	Sections   map[string][]string
	SectionSeq []string
}

func parseManagedDocument(content string) (managedDocument, bool) {
	var doc managedDocument
	doc.Metadata = make(map[string]string)
	doc.Sections = make(map[string][]string)

	lines := strings.Split(content, "\n")
	currentSection := ""
	seenSection := map[string]struct{}{}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# "):
			if doc.Title == "" {
				doc.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			}
		case strings.HasPrefix(line, "Generated at:"):
			doc.Generated = strings.TrimSpace(strings.TrimPrefix(line, "Generated at:"))
		case strings.HasPrefix(line, "## "):
			currentSection = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if currentSection != "" {
				if _, ok := seenSection[currentSection]; !ok {
					doc.SectionSeq = append(doc.SectionSeq, currentSection)
					seenSection[currentSection] = struct{}{}
				}
				if doc.Sections[currentSection] == nil {
					doc.Sections[currentSection] = []string{}
				}
			}
		case strings.HasPrefix(line, "- "):
			value := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if currentSection == "" {
				if key, fieldValue, ok := splitManagedMetadata(value); ok {
					doc.Metadata[key] = fieldValue
				}
				continue
			}
			doc.Sections[currentSection] = append(doc.Sections[currentSection], value)
		default:
			if currentSection != "" {
				doc.Sections[currentSection] = append(doc.Sections[currentSection], line)
			}
		}
	}

	if strings.TrimSpace(doc.Title) == "" {
		return managedDocument{}, false
	}
	return doc, true
}

func renderManagedDocument(doc managedDocument) string {
	var b strings.Builder
	title := strings.TrimSpace(doc.Title)
	if title == "" {
		title = "Memory Entry"
	}
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")

	generated := strings.TrimSpace(doc.Generated)
	if generated == "" {
		generated = time.Now().UTC().Format(time.RFC3339)
	}
	b.WriteString("Generated at: ")
	b.WriteString(generated)
	b.WriteString("\n\n")

	metaKeys := make([]string, 0, len(doc.Metadata))
	for key := range doc.Metadata {
		if strings.TrimSpace(doc.Metadata[key]) == "" {
			continue
		}
		metaKeys = append(metaKeys, key)
	}
	sort.Strings(metaKeys)
	for _, key := range metaKeys {
		b.WriteString("- ")
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(doc.Metadata[key]))
		b.WriteString("\n")
	}

	sectionNames := append([]string(nil), doc.SectionSeq...)
	if len(sectionNames) == 0 {
		for name := range doc.Sections {
			sectionNames = append(sectionNames, name)
		}
		sort.Strings(sectionNames)
	}
	seen := map[string]struct{}{}
	for _, name := range sectionNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		lines := compactManagedLines(doc.Sections[name])
		if len(lines) == 0 {
			continue
		}
		b.WriteString("\n## ")
		b.WriteString(name)
		b.WriteString("\n")
		for _, line := range lines {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(line))
			b.WriteString("\n")
		}
	}

	return b.String()
}

func splitManagedMetadata(line string) (string, string, bool) {
	colon := strings.Index(line, ":")
	if colon <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:colon])
	value := strings.TrimSpace(line[colon+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func compactManagedLines(lines []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out
}

func metadataInt(meta map[string]string, key string) int {
	if len(meta) == 0 {
		return 0
	}
	value := strings.TrimSpace(meta[key])
	if value == "" {
		return 0
	}
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func metadataTime(meta map[string]string, key string) time.Time {
	if len(meta) == 0 {
		return time.Time{}
	}
	value := strings.TrimSpace(meta[key])
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func copyMetadata(meta map[string]string) map[string]string {
	if len(meta) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(meta))
	for key, value := range meta {
		cloned[key] = value
	}
	return cloned
}

func (s SearchScope) normalized() SearchScope {
	return SearchScope{
		AgentID:   strings.TrimSpace(s.AgentID),
		SessionID: strings.TrimSpace(s.SessionID),
	}
}

func (s SearchScope) allows(entry Entry) bool {
	s = s.normalized()
	if len(entry.Metadata) == 0 {
		return true
	}

	scope := strings.ToLower(strings.TrimSpace(entry.Metadata["Scope"]))
	sessionID := strings.TrimSpace(entry.Metadata["Session"])
	agentID := strings.TrimSpace(entry.Metadata["Agent"])

	if scope == "" {
		switch {
		case sessionID != "":
			scope = "session"
		case !isManagedLifecycleEntry(entry):
			scope = "global"
		default:
			scope = "global"
		}
	}

	switch scope {
	case "session":
		if sessionID == "" || s.SessionID == "" || sessionID != s.SessionID {
			return false
		}
		if agentID != "" && s.AgentID != "" && agentID != s.AgentID {
			return false
		}
		if agentID != "" && s.AgentID == "" {
			return false
		}
		return true
	case "agent":
		if agentID == "" {
			return true
		}
		return s.AgentID != "" && agentID == s.AgentID
	default:
		return true
	}
}

func cloneManagedSections(sections map[string][]string) map[string][]string {
	if len(sections) == 0 {
		return nil
	}
	cloned := make(map[string][]string, len(sections))
	for key, lines := range sections {
		cloned[key] = append([]string(nil), lines...)
	}
	return cloned
}
