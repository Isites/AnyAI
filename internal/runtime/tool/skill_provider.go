package tools

import (
	"path/filepath"
	"strings"

	"github.com/Isites/anyai/internal/runtime/skill"
)

// SkillProviderAdapter adapts skill.Loader to the tools.SkillProvider interface.
type SkillProviderAdapter struct {
	Loader *skill.Loader
}

func (a SkillProviderAdapter) GetSkill(name string) (SkillDocument, bool) {
	if a.Loader == nil {
		return SkillDocument{}, false
	}
	item, ok := a.Loader.Get(name)
	if !ok {
		return SkillDocument{}, false
	}
	return SkillDocument{
		Name:        item.Name,
		Description: item.Description,
		Tags:        append([]string(nil), item.Tags...),
		Source:      shortSkillSource(item.FilePath),
		Content:     item.Body,
	}, true
}

func shortSkillSource(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	parent := filepath.Base(filepath.Dir(filePath))
	name := filepath.Base(filePath)
	if parent == "." || parent == "/" || parent == "" {
		return name
	}
	return filepath.ToSlash(filepath.Join(parent, name))
}
