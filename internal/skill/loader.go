package skill

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SkillMap maps skill name (lowercase) to *Skill.
type SkillMap map[string]*Skill

// LoadSkills loads all skills with highest-priority-wins ordering. For a given
// skill name the source with the largest priority value wins:
//
//	CWD/.agents/skills/ (5) > CWD/.claude/skills/ (4) > ~/.klein/skills/ (3) >
//	~/.agents/skills/ (2) > ~/.claude/skills/ (1) > embedded (0)
//
// In other words, project-local skills override personal skills, which override
// the embedded built-ins. ~/.klein/skills/ is klein's own personal-skill
// directory (where the create-skill skill writes new skills).
func LoadSkills(workingDir string) (SkillMap, error) {
	result := make(SkillMap)

	// 1. Embedded built-in skills (lowest priority)
	builtins, err := LoadBuiltinSkills()
	if err != nil {
		return nil, fmt.Errorf("failed to load built-in skills: %w", err)
	}
	for name, s := range builtins {
		result[name] = s
	}

	absWorkDir := workingDir
	if !filepath.IsAbs(absWorkDir) {
		if abs, err := filepath.Abs(absWorkDir); err == nil {
			absWorkDir = abs
		}
	}

	home, _ := os.UserHomeDir()

	dirs := []struct {
		path     string
		priority int
	}{
		{filepath.Join(home, ".claude", "skills"), 1},
		{filepath.Join(home, ".agents", "skills"), 2},
		{filepath.Join(home, ".klein", "skills"), 3}, // klein-native personal skills (where create-skill writes)
		{filepath.Join(absWorkDir, ".claude", "skills"), 4},
		{filepath.Join(absWorkDir, ".agents", "skills"), 5},
	}

	for _, d := range dirs {
		if info, err := os.Stat(d.path); err != nil || !info.IsDir() {
			continue
		}
		skills, err := LoadSkillsFromDir(d.path, d.priority)
		if err != nil {
			return nil, fmt.Errorf("failed to load skills from %s: %w", d.path, err)
		}
		for name, s := range skills {
			if existing, ok := result[name]; !ok || s.Priority > existing.Priority {
				result[name] = s
			}
		}
	}

	return result, nil
}

// LoadSkillsFromDir loads SKILL.md files from a directory.
// Each subdirectory containing a SKILL.md is treated as one skill.
func LoadSkillsFromDir(dir string, priority int) (SkillMap, error) {
	result := make(SkillMap)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read skills directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			// No SKILL.md in this subdirectory; skip
			continue
		}

		s, err := ParseSkillMD(data, skillFile, priority)
		if err != nil {
			return nil, fmt.Errorf("failed to parse skill %s: %w", skillFile, err)
		}

		key := strings.ToLower(s.Name)
		result[key] = s
	}

	return result, nil
}

// LoadBuiltinSkills loads embedded built-in skills from the embed.FS.
func LoadBuiltinSkills() (SkillMap, error) {
	result := make(SkillMap)

	err := fs.WalkDir(embeddedSkills, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}

		data, err := embeddedSkills.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read embedded skill %s: %w", path, err)
		}

		s, err := ParseSkillMD(data, "embedded:"+path, 0)
		if err != nil {
			return fmt.Errorf("failed to parse embedded skill %s: %w", path, err)
		}

		key := strings.ToLower(s.Name)
		result[key] = s
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}
