package plugin

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// agentFrontmatter mirrors the YAML fields used by agents/*.md files in the
// official Claude Code plugin format. Only `name` and `description` are
// required. Many fields (hooks, mcpServers, permissionMode, isolation,
// memory, initialPrompt) are accepted but currently ignored by klein —
// loading is best-effort so the user gets a clear "unsupported field" log
// rather than a parse failure.
type agentFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// tools accepts a YAML sequence or comma-separated string. Empty means
	// the agent inherits all parent tools.
	Tools           any    `yaml:"tools"`
	DisallowedTools any    `yaml:"disallowedTools"`
	Model           string `yaml:"model"`
	Background      bool   `yaml:"background"`
	Color           string `yaml:"color"`
	// Fields accepted but not enforced today:
	PermissionMode string `yaml:"permissionMode"`
	MaxTurns       int    `yaml:"maxTurns"`
}

// ParseAgentMD parses an agents/*.md file. pluginName is the owning plugin's
// name (empty for project/user-scoped agents).
func ParseAgentMD(data []byte, sourcePath, pluginName string) (*Agent, error) {
	yamlBlock, body := splitFrontmatter(string(data))

	ag := &Agent{
		PluginName: pluginName,
		Body:       body,
		SourcePath: sourcePath,
	}

	if yamlBlock != "" {
		var fm agentFrontmatter
		if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
			return nil, fmt.Errorf("failed to parse agent frontmatter at %s: %w", sourcePath, err)
		}
		ag.Name = strings.TrimSpace(fm.Name)
		ag.Description = fm.Description
		ag.Tools = parseStringList(fm.Tools)
		ag.Model = fm.Model
		ag.Background = fm.Background
		ag.Color = fm.Color
		// disallowedTools/permissionMode/maxTurns parsed but unused today.
		_ = fm.DisallowedTools
		_ = fm.PermissionMode
		_ = fm.MaxTurns
	}

	if ag.Name == "" {
		ag.Name = strings.TrimSuffix(filepath.Base(sourcePath), ".md")
	}

	if ag.Description == "" {
		return nil, fmt.Errorf("agent at %s is missing required `description` frontmatter", sourcePath)
	}

	return ag, nil
}
