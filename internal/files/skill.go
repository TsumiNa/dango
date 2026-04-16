package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"
)

// Skill represents a loaded skill with metadata and instructions.
type Skill struct {
	// Metadata from YAML frontmatter
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	// Body is the markdown instruction content (everything after frontmatter)
	Body string

	// Dir is the skill's root directory (for accessing bundled resources)
	Dir string
}

// LoadFromFile parses a SKILL.md file into a Skill.
func LoadFromFile(path string) (*Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open skill: %w", err)
	}
	defer f.Close()

	var s Skill
	body, err := frontmatter.Parse(f, &s)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	s.Body = strings.TrimSpace(string(body))
	s.Dir = filepath.Dir(path)
	return &s, nil
}

// LoadFromDir loads a skill from a directory containing SKILL.md.
func LoadFromDir(dir string) (*Skill, error) {
	return LoadFromFile(filepath.Join(dir, "SKILL.md"))
}

// ResourcePath returns the absolute path to a bundled resource file.
func (s *Skill) ResourcePath(rel string) string {
	return filepath.Join(s.Dir, rel)
}

// ReadResource reads a bundled resource file's content.
func (s *Skill) ReadResource(rel string) (string, error) {
	data, err := os.ReadFile(s.ResourcePath(rel))
	if err != nil {
		return "", fmt.Errorf("read resource %s: %w", rel, err)
	}
	return string(data), nil
}
