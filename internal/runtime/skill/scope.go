package skill

import (
	"path/filepath"
	"strings"

	"github.com/Isites/anyai/internal/config"
)

// LoaderForAgent builds a scoped loader for one agent using the design's
// visibility rules: optional system skills, then shared skills, then private
// skills, with later layers overriding earlier ones.
func LoaderForAgent(cfg *config.Config, agentCfg *config.AgentConfig) (*Loader, error) {
	loader := NewLoader()
	if agentCfg == nil {
		return loader, nil
	}

	systemDir := cfg.SystemSkillsDir
	sharedDir := cfg.SharedSkillsDir
	privateDir := agentCfg.PrivateSkillsDir
	if strings.TrimSpace(privateDir) == "" && strings.TrimSpace(agentCfg.Workspace) != "" {
		privateDir = filepath.Join(agentCfg.Workspace, "skills")
	}

	ordered := map[string]Skill{}
	order := []string{}
	appendLayer := func(dir string) error {
		skills, err := scanSkillsFromDir(dir)
		if err != nil {
			return err
		}
		for _, skill := range skills {
			if _, seen := ordered[skill.Name]; !seen {
				order = append(order, skill.Name)
			}
			ordered[skill.Name] = skill
		}
		return nil
	}

	if strings.TrimSpace(systemDir) != "" {
		if err := appendLayer(systemDir); err != nil {
			return nil, err
		}
	}
	if agentCfg.InheritSharedSkills {
		if err := appendLayer(sharedDir); err != nil {
			return nil, err
		}
	}
	if err := appendLayer(privateDir); err != nil {
		return nil, err
	}

	for _, name := range order {
		loader.skills = append(loader.skills, ordered[name])
	}
	return loader, nil
}
