package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a parsed agent.md file.
type Document struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Entry       bool     `yaml:"entry"`
	Model       string   `yaml:"model"`
	Fallbacks   []string `yaml:"fallbacks"`
	Workspace   string   `yaml:"workspace"`
	MaxTurns    int      `yaml:"max_turns"`
	Tools       Tools    `yaml:"tools"`
	Skills      Skills   `yaml:"skills"`
	MCPs        MCPs     `yaml:"mcps"`
	Tags        []string `yaml:"tags"`

	Body     string
	FilePath string
	Dir      string
}

type documentAlias struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Entry       bool     `yaml:"entry"`
	Model       string   `yaml:"model"`
	Fallbacks   []string `yaml:"fallbacks"`
	Workspace   string   `yaml:"workspace"`
	MaxTurns    int      `yaml:"max_turns"`
	MaxTurnsOld int      `yaml:"maxTurns"`
	Tools       Tools    `yaml:"tools"`
	Skills      Skills   `yaml:"skills"`
	MCPs        MCPs     `yaml:"mcps"`
	Tags        []string `yaml:"tags"`
}

type Tools struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

type Skills struct {
	InheritShared *bool `yaml:"inherit_shared"`
}

type MCPs struct {
	InheritShared *bool `yaml:"inherit_shared"`
}

// ParseFile parses an agent.md file with YAML frontmatter and Markdown body.
func ParseFile(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent.md: %w", err)
	}

	frontmatter, body := splitFrontmatter(string(data))

	var doc Document
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), &doc); err != nil {
			return nil, fmt.Errorf("parse frontmatter: %w", err)
		}
	}

	doc.Body = strings.TrimSpace(body)
	doc.FilePath = path
	doc.Dir = filepath.Dir(path)

	return &doc, nil
}

// UnmarshalYAML supports the canonical max_turns field while remaining
// tolerant of older maxTurns examples.
func (d *Document) UnmarshalYAML(value *yaml.Node) error {
	var aux documentAlias
	if err := value.Decode(&aux); err != nil {
		return err
	}

	d.ID = aux.ID
	d.Name = aux.Name
	d.Description = aux.Description
	d.Entry = aux.Entry
	d.Model = aux.Model
	d.Fallbacks = aux.Fallbacks
	d.Workspace = aux.Workspace
	d.MaxTurns = aux.MaxTurns
	if d.MaxTurns == 0 {
		d.MaxTurns = aux.MaxTurnsOld
	}
	d.Tools = aux.Tools
	d.Skills = aux.Skills
	d.MCPs = aux.MCPs
	d.Tags = aux.Tags

	return nil
}

func splitFrontmatter(content string) (frontmatter, body string) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return "", content
	}

	rest := strings.TrimLeft(content[3:], " \t")
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return "", content
	}

	frontmatter = rest[:endIdx]
	body = rest[endIdx+4:]
	if strings.HasPrefix(body, "\r\n") {
		body = body[2:]
	} else if strings.HasPrefix(body, "\n") {
		body = body[1:]
	}

	return frontmatter, body
}
