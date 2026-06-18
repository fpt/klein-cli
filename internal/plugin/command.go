package plugin

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// commandFrontmatter mirrors the YAML fields used by command .md files in the
// official Claude Code plugin format. Only `description` is required.
type commandFrontmatter struct {
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
	// allowed-tools accepts either a YAML sequence (["Task","Bash"]) or a
	// comma-separated string. We decode into any and normalise later.
	AllowedTools any    `yaml:"allowed-tools"`
	Model        string `yaml:"model"`
}

// ParseCommandMD parses a commands/*.md file. The bare command name is the
// filename without the .md extension. pluginName is the owning plugin's name
// (empty for project-local commands at .claude/commands/).
func ParseCommandMD(data []byte, sourcePath, pluginName string) (*Command, error) {
	yamlBlock, body := splitFrontmatter(string(data))

	cmd := &Command{
		Name:       strings.TrimSuffix(filepath.Base(sourcePath), ".md"),
		PluginName: pluginName,
		Body:       body,
		SourcePath: sourcePath,
	}

	if yamlBlock != "" {
		var fm commandFrontmatter
		if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
			return nil, fmt.Errorf("failed to parse command frontmatter at %s: %w", sourcePath, err)
		}
		cmd.Description = fm.Description
		cmd.ArgumentHint = fm.ArgumentHint
		cmd.AllowedTools = parseStringList(fm.AllowedTools)
		cmd.Model = fm.Model
	}

	return cmd, nil
}

// positionalArgRe matches $ARGUMENTS[N] and $N — same as the skill renderer.
var positionalArgRe = regexp.MustCompile(`\$(?:ARGUMENTS\[(\d+)\]|(\d+))`)

// Render expands placeholders in the command body. Supported:
//   - $ARGUMENTS — the full argument string after the command name.
//   - $N, $ARGUMENTS[N] — positional arguments (0-indexed, whitespace-split).
//   - {{workingDir}} — the agent's working directory.
//
// If the body contains no $ARGUMENTS placeholder and arguments is non-empty,
// the arguments are appended as a trailing ARGUMENTS: line (matching the
// skill renderer's behaviour so command UX is consistent).
func (c *Command) Render(arguments, workingDir string) string {
	result := c.Body

	hasArguments := strings.Contains(result, "$ARGUMENTS") || positionalArgRe.MatchString(result)

	args := strings.Fields(arguments)
	result = positionalArgRe.ReplaceAllStringFunc(result, func(match string) string {
		subs := positionalArgRe.FindStringSubmatch(match)
		idxStr := subs[1]
		if idxStr == "" {
			idxStr = subs[2]
		}
		idx := 0
		for _, c := range idxStr {
			idx = idx*10 + int(c-'0')
		}
		if idx < len(args) {
			return args[idx]
		}
		return match
	})

	result = strings.ReplaceAll(result, "$ARGUMENTS", arguments)
	result = strings.ReplaceAll(result, "{{workingDir}}", workingDir)

	if !hasArguments && arguments != "" {
		result = result + "\nARGUMENTS: " + arguments
	}
	return result
}
