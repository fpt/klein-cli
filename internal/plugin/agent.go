package plugin

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fpt/klein-cli/internal/skill"
)

// ParseAgentMD parses an agents/*.md file. pluginName is the owning plugin's
// name (empty for built-in and project/user-scoped agents).
//
// Agents, roles, and skills are one type parsed by one function; this wrapper
// only supplies the agent Kind — which is what makes a bare `allowed-tools:`
// list a hard cap here rather than the visibility hint it is on a role — plus
// the plugin attribution and the stricter `description` requirement agents have
// always had.
func ParseAgentMD(data []byte, sourcePath, pluginName string) (*Agent, error) {
	ag, err := skill.ParseDefinition(data, sourcePath, 0, skill.KindAgent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse agent at %s: %w", sourcePath, err)
	}
	ag.PluginName = pluginName

	// skill.ParseDefinition defaults a missing name from the parent directory,
	// which is right for {name}/SKILL.md but wrong for a flat {name}.md — it
	// would name every agent after the agents/ directory.
	if !frontmatterHasKey(data, "name") {
		ag.Name = strings.TrimSuffix(filepath.Base(sourcePath), ".md")
	}
	ag.Name = strings.TrimSpace(ag.Name)

	// An agent's description is what the model reads to decide whether to
	// delegate, so unlike a skill it may not be inferred from the body.
	if ag.Description == "" || !frontmatterHasKey(data, "description") {
		return nil, fmt.Errorf("agent at %s is missing required `description` frontmatter", sourcePath)
	}

	return ag, nil
}

// frontmatterHasKey reports whether the YAML frontmatter block declares key at
// the top level.
func frontmatterHasKey(data []byte, key string) bool {
	yamlBlock, _ := splitFrontmatter(string(data))
	for _, line := range strings.Split(yamlBlock, "\n") {
		if strings.HasPrefix(line, key+":") {
			return true
		}
	}
	return false
}
