package skill

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillMap maps skill name (lowercase) to *Skill.
type SkillMap map[string]*Skill

// Filenames and directory names for the two kinds of definition. Roles and
// skills are loaded by the same code with these substituted in; the frontmatter
// format is identical, only the meaning differs (see Skill.IsRole).
const (
	skillFileName = "SKILL.md"
	roleFileName  = "ROLE.md"
	skillsDirName = "skills"
	rolesDirName  = "roles"
)

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
	return loadDefinitions(workingDir, skillsDirName, skillFileName, false)
}

// LoadRoles loads all roles — the startup prompts a session can open with —
// using the same priority ladder as LoadSkills with "roles" in place of
// "skills", so a project or personal ROLE.md overrides a built-in of the same
// name exactly as it would for a skill.
func LoadRoles(workingDir string) (SkillMap, error) {
	return loadDefinitions(workingDir, rolesDirName, roleFileName, true)
}

// LoadRolesAndSkills returns one registry holding both, which is what the agent
// resolves names against. Roles win a name collision: a role has to be
// selectable by its own name, and shadowing it with a same-named skill would
// make that startup prompt unreachable.
func LoadRolesAndSkills(workingDir string) (SkillMap, error) {
	skills, err := LoadSkills(workingDir)
	if err != nil {
		return nil, err
	}
	roles, err := LoadRoles(workingDir)
	if err != nil {
		return nil, err
	}
	maps.Copy(skills, roles)
	return skills, nil
}

// RoleNames returns the sorted names of the roles in a registry, for error
// messages that have to tell the user what they could have picked instead.
func RoleNames(defs SkillMap) []string {
	names := make([]string, 0, len(defs))
	for name, s := range defs {
		if s.IsRole {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// loadDefinitions walks the embedded built-ins and then the five filesystem
// directories, highest priority winning.
func loadDefinitions(workingDir, dirName, fileName string, isRole bool) (SkillMap, error) {
	result, err := loadBuiltins(dirName, fileName, isRole)
	if err != nil {
		return nil, fmt.Errorf("failed to load built-in %s: %w", dirName, err)
	}

	for _, d := range searchDirs(workingDir, dirName) {
		if info, err := os.Stat(d.path); err != nil || !info.IsDir() {
			continue
		}
		loaded, err := loadFromDir(d.path, d.priority, fileName, isRole)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s from %s: %w", dirName, d.path, err)
		}
		for name, s := range loaded {
			if existing, ok := result[name]; !ok || s.Priority > existing.Priority {
				result[name] = s
			}
		}
	}

	return result, nil
}

// searchDir is one directory in the priority ladder; a larger priority wins a
// name collision.
type searchDir struct {
	path     string
	priority int
}

// searchDirs returns the five filesystem locations scanned for definitions of
// one kind, lowest priority first. dirName is "roles" or "skills", so the two
// kinds never read each other's directories.
func searchDirs(workingDir, dirName string) []searchDir {
	absWorkDir := workingDir
	if !filepath.IsAbs(absWorkDir) {
		if abs, err := filepath.Abs(absWorkDir); err == nil {
			absWorkDir = abs
		}
	}
	home, _ := os.UserHomeDir()

	return []searchDir{
		{filepath.Join(home, ".claude", dirName), 1},
		{filepath.Join(home, ".agents", dirName), 2},
		{filepath.Join(home, ".klein", dirName), 3}, // klein-native personal defs (where create-skill writes)
		{filepath.Join(absWorkDir, ".claude", dirName), 4},
		{filepath.Join(absWorkDir, ".agents", dirName), 5},
	}
}

// LoadSkillsFromDir loads SKILL.md files from a directory.
// Each subdirectory containing a SKILL.md is treated as one skill.
func LoadSkillsFromDir(dir string, priority int) (SkillMap, error) {
	return loadFromDir(dir, priority, skillFileName, false)
}

// LoadRolesFromDir loads ROLE.md files from a directory, one role per
// subdirectory.
func LoadRolesFromDir(dir string, priority int) (SkillMap, error) {
	return loadFromDir(dir, priority, roleFileName, true)
}

func loadFromDir(dir string, priority int, fileName string, isRole bool) (SkillMap, error) {
	result := make(SkillMap)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		path := filepath.Join(dir, entry.Name(), fileName)
		data, err := os.ReadFile(path)
		if err != nil {
			// No definition file in this subdirectory; skip
			continue
		}

		s, err := ParseSkillMD(data, path, priority)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", path, err)
		}
		s.IsRole = isRole

		result[strings.ToLower(s.Name)] = s
	}

	return result, nil
}

// LoadBuiltinSkills loads embedded built-in skills from the embed.FS.
func LoadBuiltinSkills() (SkillMap, error) {
	return loadBuiltins(skillsDirName, skillFileName, false)
}

// LoadBuiltinRoles loads the embedded built-in roles.
func LoadBuiltinRoles() (SkillMap, error) {
	return loadBuiltins(rolesDirName, roleFileName, true)
}

func loadBuiltins(dirName, fileName string, isRole bool) (SkillMap, error) {
	result := make(SkillMap)
	source := embeddedSkills
	if isRole {
		source = embeddedRoles
	}

	err := fs.WalkDir(source, dirName, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != fileName {
			return nil
		}

		data, err := source.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read embedded %s: %w", path, err)
		}

		s, err := ParseSkillMD(data, "embedded:"+path, 0)
		if err != nil {
			return fmt.Errorf("failed to parse embedded %s: %w", path, err)
		}
		s.IsRole = isRole

		result[strings.ToLower(s.Name)] = s
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}
