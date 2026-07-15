// Package skills implements Claude Code-style Skills: a directory
// convention plus progressive disclosure, not a special runtime feature.
// Each skill is a `<dir>/SKILL.md` with YAML frontmatter (name,
// description); only the frontmatter goes into the system prompt up
// front, and the full body is fetched on demand via the Skill tool —
// keeping unused skills nearly free in context.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string
	Description string
	Body        string
	Path        string
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// LoadAll scans each directory in dirs for `*/SKILL.md` and parses it.
// Directories are scanned in order and a skill name seen in an earlier
// directory wins over the same name in a later one — callers should list
// project-local skill dirs before the global one so a project can
// override a global skill.
func LoadAll(dirs ...string) ([]Skill, error) {
	var out []Skill
	seen := map[string]bool{}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name(), "SKILL.md")
			if _, err := os.Stat(path); err != nil {
				continue
			}

			sk, err := parseSkillFile(path)
			if err != nil {
				continue // skip malformed skills rather than failing startup
			}
			if sk.Name == "" {
				sk.Name = e.Name()
			}
			if seen[sk.Name] {
				continue
			}
			seen[sk.Name] = true
			out = append(out, sk)
		}
	}
	return out, nil
}

func parseSkillFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return Skill{}, fmt.Errorf("%s: missing YAML frontmatter (must start with \"---\")", path)
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Skill{}, fmt.Errorf("%s: unterminated YAML frontmatter", path)
	}

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return Skill{}, fmt.Errorf("%s: parse frontmatter: %w", path, err)
	}

	body := strings.TrimPrefix(rest[end+len("\n---"):], "\n")
	return Skill{
		Name:        fm.Name,
		Description: fm.Description,
		Body:        body,
		Path:        path,
	}, nil
}

// SystemPromptSection renders the skill index (name + description only)
// for inclusion in the system prompt. Returns "" if there are no skills.
func SystemPromptSection(list []Skill) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available skills (call the Skill tool with `name` to load full instructions when one is relevant to the task):\n")
	for _, s := range list {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return b.String()
}
