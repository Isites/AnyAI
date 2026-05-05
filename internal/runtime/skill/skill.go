package skill

import (
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v3"
)

// openclawMetadata holds OpenClaw-compatible metadata from YAML frontmatter.
type openclawMetadata struct {
	OpenClaw struct {
		Requires struct {
			Bins []string `yaml:"bins"`
		} `yaml:"requires"`
	} `yaml:"openclaw"`
}

// Skill represents a loaded skill with YAML frontmatter metadata and Markdown body.
type Skill struct {
	// From YAML frontmatter
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Tags        []string         `yaml:"tags,omitempty"`
	Metadata    openclawMetadata `yaml:"metadata,omitempty"`

	// Parsed content
	Body     string // Markdown body (after frontmatter)
	FilePath string // Source file path

	bodyState *skillBodyState
}

// PromptOptions controls how matched skills are summarized for prompt
// injection.
type PromptOptions struct {
	SummaryMaxLen int
}

// Loader scans directories for SKILL.md files and loads them.
type Loader struct {
	skills []Skill
	mu     sync.RWMutex
}

type skillBodyState struct {
	once sync.Once
	load func() (string, error)
	body string
	err  error
}

// NewLoader creates a new skill loader.
func NewLoader() *Loader {
	return &Loader{}
}

// NewLoaderFromSkills creates a loader from an in-memory skill set.
func NewLoaderFromSkills(skills []Skill) *Loader {
	return &Loader{skills: cloneSkills(skills)}
}

// LoadFrom scans directories for SKILL.md files and loads all found skills.
// It accepts multiple directories (e.g. workspace/skills/ and project anyai/skills/).
func (l *Loader) LoadFrom(dirs ...string) error {
	var loaded []Skill
	for _, dir := range dirs {
		skills, err := scanSkillsFromDir(dir)
		if err != nil {
			runtimelogging.Warn("error scanning skills directory", "dir", dir, "error", err)
			continue
		}
		loaded = append(loaded, skills...)
	}

	l.mu.Lock()
	l.skills = loaded
	l.mu.Unlock()

	return nil
}

func scanSkillsFromDir(dir string) ([]Skill, error) {
	dir = normalizeSkillDir(dir)
	if dir == "" {
		return nil, nil
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	var loaded []Skill
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		isSkillMD := strings.ToUpper(name) == "SKILL.MD"
		isDirectMD := strings.HasSuffix(strings.ToLower(name), ".md") && filepath.Dir(path) == dir
		if !isSkillMD && !isDirectMD {
			return nil
		}

		skill, err := parseSkillMetadataFile(path)
		if err != nil {
			runtimelogging.Warn("failed to parse skill file", "path", path, "error", err)
			return nil
		}

		if bins := skill.Metadata.OpenClaw.Requires.Bins; len(bins) > 0 {
			missing := false
			for _, bin := range bins {
				if _, err := exec.LookPath(bin); err != nil {
					runtimelogging.Info("skill skipped (missing binary)", "name", skill.Name, "binary", bin)
					missing = true
					break
				}
			}
			if missing {
				return nil
			}
		}

		loaded = append(loaded, skill)
		runtimelogging.Info("loaded skill", "name", skill.Name, "path", path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

func normalizeSkillDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}

	normalized := filepath.Clean(dir)
	if abs, err := filepath.Abs(normalized); err == nil {
		normalized = abs
	}
	if resolved, err := filepath.EvalSymlinks(normalized); err == nil && strings.TrimSpace(resolved) != "" {
		normalized = resolved
	}
	return normalized
}

func cloneSkills(skills []Skill) []Skill {
	if len(skills) == 0 {
		return nil
	}
	out := make([]Skill, len(skills))
	for i, item := range skills {
		out[i] = cloneSkill(item)
	}
	return out
}

func cloneSkill(item Skill) Skill {
	cloned := item
	if len(item.Tags) > 0 {
		cloned.Tags = append([]string(nil), item.Tags...)
	}
	if len(item.Metadata.OpenClaw.Requires.Bins) > 0 {
		cloned.Metadata.OpenClaw.Requires.Bins = append([]string(nil), item.Metadata.OpenClaw.Requires.Bins...)
	}
	return cloned
}

// LoadDir inspects one directory and returns all discovered skills.
func LoadDir(dir string) ([]Skill, error) {
	return scanSkillsFromDir(dir)
}

// Skills returns all loaded skills.
func (l *Loader) Skills() []Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return cloneSkills(l.skills)
}

// Get returns one loaded skill by name. Lookup is case-insensitive and also
// tolerates simple separator differences such as spaces vs hyphens.
func (l *Loader) Get(name string) (Skill, bool) {
	target := normalizeSkillLookupKey(name)
	if target == "" {
		return Skill{}, false
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	for _, item := range l.skills {
		if normalizeSkillLookupKey(item.Name) == target {
			loaded, err := loadSkillBody(cloneSkill(item))
			if err != nil {
				runtimelogging.Warn("failed to load skill body", "name", item.Name, "path", item.FilePath, "error", err)
				return Skill{}, false
			}
			return loaded, true
		}
	}
	return Skill{}, false
}

// MatchSkills returns skills relevant to the given user message.
// Relevance is determined by keyword matching against skill name,
// description, and tags.
func (l *Loader) MatchSkills(userMsg string, maxSkills int) []Skill {
	l.mu.RLock()
	skills := l.skills
	l.mu.RUnlock()

	if len(skills) == 0 {
		return nil
	}

	if maxSkills <= 0 {
		maxSkills = 3
	}

	queryPhrase := normalizeSkillPhrase(userMsg)
	queryTokens := tokenizeSkillText(userMsg)
	if queryPhrase == "" || len(queryTokens) == 0 {
		return nil
	}
	querySet := make(map[string]struct{}, len(queryTokens))
	for _, token := range queryTokens {
		querySet[token] = struct{}{}
	}

	type scored struct {
		skill        Skill
		score        int
		matchedTerms int
	}

	var results []scored

	for _, s := range skills {
		score, matchedTerms := scoreSkillMatch(s, queryPhrase, querySet)
		if score > 0 {
			results = append(results, scored{
				skill:        s,
				score:        score,
				matchedTerms: matchedTerms,
			})
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		if results[i].matchedTerms != results[j].matchedTerms {
			return results[i].matchedTerms > results[j].matchedTerms
		}
		return strings.ToLower(results[i].skill.Name) < strings.ToLower(results[j].skill.Name)
	})

	// Return top N
	var matched []Skill
	for i := 0; i < len(results) && i < maxSkills; i++ {
		item := cloneSkill(results[i].skill)
		if strings.TrimSpace(item.Description) == "" {
			if loaded, err := loadSkillBody(item); err == nil {
				item = loaded
			}
		}
		matched = append(matched, item)
	}

	return matched
}

func scoreSkillMatch(item Skill, queryPhrase string, querySet map[string]struct{}) (int, int) {
	score := 0
	matchedTerms := 0
	seenMatches := make(map[string]struct{})

	namePhrase := normalizeSkillPhrase(item.Name)
	nameTokens := tokenizeSkillText(item.Name)
	if namePhrase != "" && strings.Contains(queryPhrase, namePhrase) {
		score += 12
		matchedTerms += maxInt(1, len(nameTokens))
	}
	if len(nameTokens) > 1 && containsAllTokens(querySet, nameTokens) {
		score += 6
		matchedTerms += len(nameTokens)
	}
	score += overlapSkillTokens(nameTokens, querySet, 4, seenMatches, &matchedTerms)

	for _, tag := range item.Tags {
		tagPhrase := normalizeSkillPhrase(tag)
		tagTokens := tokenizeSkillText(tag)
		if tagPhrase != "" && strings.Contains(queryPhrase, tagPhrase) {
			score += 8
			matchedTerms += maxInt(1, len(tagTokens))
		}
		score += overlapSkillTokens(tagTokens, querySet, 3, seenMatches, &matchedTerms)
	}

	score += overlapSkillTokens(tokenizeSkillText(item.Description), querySet, 1, seenMatches, &matchedTerms)
	score += overlapSkillTokens(skillBodyMatchTokens(item), querySet, 1, seenMatches, &matchedTerms)
	if matchedTerms == 0 {
		return 0, 0
	}
	return score, matchedTerms
}

func skillBodyMatchTokens(item Skill) []string {
	loaded, err := loadSkillBody(item)
	if err != nil {
		return nil
	}
	return tokenizeSkillText(loaded.Body)
}

func overlapSkillTokens(tokens []string, querySet map[string]struct{}, weight int, seen map[string]struct{}, matchedTerms *int) int {
	if len(tokens) == 0 || len(querySet) == 0 || weight <= 0 {
		return 0
	}
	score := 0
	for _, token := range tokens {
		if _, ok := querySet[token]; !ok {
			continue
		}
		if _, duplicated := seen[token]; duplicated {
			continue
		}
		seen[token] = struct{}{}
		score += weight
		if matchedTerms != nil {
			*matchedTerms += 1
		}
	}
	return score
}

func containsAllTokens(querySet map[string]struct{}, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if _, ok := querySet[token]; !ok {
			return false
		}
	}
	return true
}

func normalizeSkillPhrase(text string) string {
	return strings.Join(tokenizeSkillText(text), " ")
}

func tokenizeSkillText(text string) []string {
	raw := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, token := range raw {
		token = strings.TrimSpace(token)
		if len(token) < 2 || isSkillStopword(token) {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func isSkillStopword(token string) bool {
	switch token {
	case "a", "an", "and", "are", "assistant", "before", "can", "context", "current", "earlier", "for", "from", "has", "have", "hello", "here", "how", "into", "its", "latest", "maybe", "more", "need", "now", "out", "pending", "please", "question", "request", "session", "some", "summary", "task", "that", "the", "their", "there", "these", "they", "this", "those", "today", "use", "user", "want", "what", "when", "with", "your":
		return true
	default:
		return false
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *skillBodyState) materialize() (string, error) {
	if s == nil {
		return "", nil
	}
	s.once.Do(func() {
		if s.load == nil {
			return
		}
		s.body, s.err = s.load()
		s.body = strings.TrimSpace(s.body)
	})
	return s.body, s.err
}

func loadSkillBody(item Skill) (Skill, error) {
	if strings.TrimSpace(item.Body) != "" || item.bodyState == nil {
		item.Body = strings.TrimSpace(item.Body)
		return item, nil
	}
	body, err := item.bodyState.materialize()
	if err != nil {
		return Skill{}, err
	}
	item.Body = body
	return item, nil
}

func lazyBodyStateForPath(path string) *skillBodyState {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &skillBodyState{
		load: func() (string, error) {
			item, err := parseSkillFile(path)
			if err != nil {
				return "", err
			}
			return item.Body, nil
		},
	}
}

func parseSkillMetadataFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	frontmatter, _ := splitFrontmatter(string(data))

	var skill Skill
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
			return Skill{}, err
		}
	}

	skill.FilePath = path
	skill.bodyState = lazyBodyStateForPath(path)

	if skill.Name == "" {
		if strings.ToUpper(filepath.Base(path)) == "SKILL.MD" {
			skill.Name = filepath.Base(filepath.Dir(path))
		} else {
			skill.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
	}

	return skill, nil
}

// parseSkillFile reads a SKILL.md file and parses its frontmatter and body.
func parseSkillFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	frontmatter, body := splitFrontmatter(string(data))

	var skill Skill
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
			return Skill{}, err
		}
	}

	skill.Body = strings.TrimSpace(body)
	skill.FilePath = path

	// Default name from directory name (for SKILL.md) or filename stem (for *.md)
	if skill.Name == "" {
		if strings.ToUpper(filepath.Base(path)) == "SKILL.MD" {
			skill.Name = filepath.Base(filepath.Dir(path))
		} else {
			skill.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
	}

	return skill, nil
}

// splitFrontmatter extracts YAML frontmatter (between --- delimiters) from Markdown.
func splitFrontmatter(content string) (frontmatter, body string) {
	content = strings.TrimSpace(content)

	if !strings.HasPrefix(content, "---") {
		return "", content
	}

	// Find the closing ---
	rest := content[3:]
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return "", content
	}

	frontmatter = rest[:endIdx]
	body = rest[endIdx+4:]

	// Trim the newline after closing ---
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	} else if len(body) > 1 && body[0] == '\r' && body[1] == '\n' {
		body = body[2:]
	}

	return frontmatter, body
}

// FormatForPrompt formats matched skills for injection into the system prompt.
func FormatForPrompt(skills []Skill) string {
	return FormatForPromptWithOptions(skills, PromptOptions{})
}

// FormatForPromptWithOptions formats matched skills as compact prompt cards.
func FormatForPromptWithOptions(skills []Skill, opts PromptOptions) string {
	if len(skills) == 0 {
		return ""
	}

	opts = normalizePromptOptions(opts)

	var b strings.Builder
	b.WriteString("## Relevant Skills\n\n")

	for _, s := range skills {
		b.WriteString("### ")
		b.WriteString(s.Name)
		b.WriteString("\n")
		if summary := promptSummaryForSkill(s, opts.SummaryMaxLen); summary != "" {
			b.WriteString("- Summary: ")
			b.WriteString(summary)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}

func normalizePromptOptions(opts PromptOptions) PromptOptions {
	if opts.SummaryMaxLen <= 0 {
		opts.SummaryMaxLen = 180
	}
	return opts
}

func promptSummaryForSkill(s Skill, maxLen int) string {
	summary := strings.TrimSpace(s.Description)
	if summary == "" {
		summary = strings.TrimSpace(s.Body)
	}
	return promptSnippet(summary, maxLen)
}

func promptSnippet(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	cut := maxLen - 3
	if cut < 1 {
		return text[:maxLen]
	}
	if idx := strings.LastIndex(text[:cut], " "); idx >= cut/2 {
		cut = idx
	}
	return strings.TrimSpace(text[:cut]) + "..."
}

func normalizeSkillLookupKey(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	replacer := strings.NewReplacer("_", "-", " ", "-", "\t", "-", "\n", "-")
	name = replacer.Replace(name)
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}
