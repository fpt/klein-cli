package plugin

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Built-in agent definitions shipped in the binary. They live in this package
// because it owns the Agent type and its parser; unlike everything else here
// they are not plugin-scoped, and load with PluginName empty exactly as a
// project- or user-defined agent does.
//
//go:embed agents/*.md
var embeddedAgents embed.FS

// agentsDirName is the directory scanned for agents/*.md under each root in the
// priority ladder, and the directory the built-ins are embedded from.
const agentsDirName = "agents"

// AgentMap maps an agent's bare name to its definition.
type AgentMap map[string]*Agent

// LoadAgents loads every non-plugin agent — built-in, personal, and
// project-local — with highest-priority-wins ordering. For a given agent name
// the source with the largest priority value wins:
//
//	CWD/.agents/agents/ (5) > CWD/.claude/agents/ (4) > ~/.klein/agents/ (3) >
//	~/.agents/agents/ (2) > ~/.claude/agents/ (1) > embedded (0)
//
// This mirrors skill.LoadSkills so a project can override a built-in agent the
// same way it overrides a built-in skill. Agents from plugins are loaded
// separately by Plugin.loadAgents and merged by the app layer, which keeps them
// addressable under their scoped "<plugin>:<agent>" name.
//
// Unreadable or malformed files are skipped rather than failing the load: a
// single bad agent in a personal directory must not stop the CLI from starting.
// The error return is reserved for failures that make the whole set suspect.
func LoadAgents(workingDir string) (AgentMap, error) {
	result, err := loadBuiltinAgents()
	if err != nil {
		return nil, fmt.Errorf("loading built-in agents: %w", err)
	}

	for _, d := range agentSearchDirs(workingDir) {
		if !isDir(d.path) {
			continue
		}
		for name, ag := range loadAgentsFromDir(d.path, d.priority) {
			if existing, ok := result[name]; !ok || ag.Priority > existing.Priority {
				result[name] = ag
			}
		}
	}

	return result, nil
}

// agentSearchDir is one directory in the priority ladder; a larger priority
// wins a name collision.
type agentSearchDir struct {
	path     string
	priority int
}

// agentSearchDirs returns the five filesystem locations scanned for agent
// definitions, lowest priority first. The roots match skill.searchDirs so the
// three kinds of definition are configured in the same places.
func agentSearchDirs(workingDir string) []agentSearchDir {
	absWorkDir := workingDir
	if !filepath.IsAbs(absWorkDir) {
		if abs, err := filepath.Abs(absWorkDir); err == nil {
			absWorkDir = abs
		}
	}
	home, _ := os.UserHomeDir()

	return []agentSearchDir{
		{filepath.Join(home, ".claude", agentsDirName), 1},
		{filepath.Join(home, ".agents", agentsDirName), 2},
		{filepath.Join(home, ".klein", agentsDirName), 3},
		{filepath.Join(absWorkDir, ".claude", agentsDirName), 4},
		{filepath.Join(absWorkDir, ".agents", agentsDirName), 5},
	}
}

// loadAgentsFromDir parses every *.md directly under dir. Subdirectories are
// not walked: an agent is a single flat file, matching the plugin layout and
// Claude Code's .claude/agents/ convention.
func loadAgentsFromDir(dir string, priority int) AgentMap {
	result := make(AgentMap)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ag, err := ParseAgentMD(data, path, "")
		if err != nil {
			// One malformed agent must not sink the rest.
			continue
		}
		ag.Priority = priority
		result[ag.Name] = ag
	}

	return result
}

// loadBuiltinAgents parses the agents embedded in the binary, at priority 0 so
// any filesystem definition of the same name replaces them.
func loadBuiltinAgents() (AgentMap, error) {
	result := make(AgentMap)

	err := fs.WalkDir(embeddedAgents, agentsDirName, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		data, rerr := embeddedAgents.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("reading embedded %s: %w", path, rerr)
		}
		ag, perr := ParseAgentMD(data, "embedded:"+path, "")
		if perr != nil {
			return fmt.Errorf("parsing embedded %s: %w", path, perr)
		}
		ag.Priority = 0
		result[ag.Name] = ag
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking embedded agents: %w", err)
	}

	return result, nil
}
