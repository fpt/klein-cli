package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	"gopkg.in/yaml.v3"
)

// Skill represents a parsed SKILL.md file following the Claude Code standard.
type Skill struct {
	Name                   string   // from frontmatter or directory name
	Description            string   // from frontmatter
	AllowedTools           []string // from "allowed-tools" (empty = all tools)
	ArgumentHint           string
	DisableModelInvocation bool
	UserInvocable          bool   // default true
	Model                  string
	Content                string // markdown body after frontmatter
	SourcePath             string // filesystem path or "embedded:<name>"
	Priority               int    // 0=embedded, 1=project, 2=personal
}

// frontmatter maps YAML frontmatter fields using kebab-case.
type frontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	AllowedTools           string `yaml:"allowed-tools"`
	ArgumentHint           string `yaml:"argument-hint"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
	UserInvocable          *bool  `yaml:"user-invocable"`
	Model                  string `yaml:"model"`
}

// ParseSkillMD parses a SKILL.md file content into a Skill.
// Format: optional YAML frontmatter between "---" delimiters, then markdown body.
func ParseSkillMD(data []byte, sourcePath string, priority int) (*Skill, error) {
	content := string(data)
	s := &Skill{
		UserInvocable: true,
		SourcePath:    sourcePath,
		Priority:      priority,
	}

	// Check for frontmatter
	trimmed := strings.TrimLeft(content, " \t\n\r")
	if strings.HasPrefix(trimmed, "---") {
		// Find the closing ---
		afterFirst := trimmed[3:]
		// Skip the rest of the first --- line
		idx := strings.Index(afterFirst, "\n")
		if idx < 0 {
			// Only frontmatter delimiter, no content
			s.Content = ""
			return s, nil
		}
		afterFirst = afterFirst[idx+1:]

		// Find closing ---
		closingIdx := strings.Index(afterFirst, "\n---")
		if closingIdx < 0 {
			// No closing delimiter — treat entire content as markdown
			s.Content = content
		} else {
			yamlBlock := afterFirst[:closingIdx]
			// Content starts after the closing --- line
			rest := afterFirst[closingIdx+4:] // skip \n---
			// Skip to end of closing line
			nlIdx := strings.Index(rest, "\n")
			if nlIdx >= 0 {
				s.Content = rest[nlIdx+1:]
			} else {
				s.Content = ""
			}

			// Parse YAML frontmatter
			var fm frontmatter
			if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
				return nil, fmt.Errorf("failed to parse SKILL.md frontmatter: %w", err)
			}

			s.Name = fm.Name
			s.Description = fm.Description
			s.ArgumentHint = fm.ArgumentHint
			s.DisableModelInvocation = fm.DisableModelInvocation
			s.Model = fm.Model

			if fm.UserInvocable != nil {
				s.UserInvocable = *fm.UserInvocable
			}

			// Parse allowed-tools as comma-separated list
			if fm.AllowedTools != "" {
				parts := strings.Split(fm.AllowedTools, ",")
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						s.AllowedTools = append(s.AllowedTools, p)
					}
				}
			}
		}
	} else {
		// No frontmatter
		s.Content = content
	}

	// Default name from directory name
	if s.Name == "" {
		dir := filepath.Dir(sourcePath)
		s.Name = filepath.Base(dir)
	}

	// Default description from first paragraph
	if s.Description == "" && s.Content != "" {
		lines := strings.SplitN(strings.TrimSpace(s.Content), "\n\n", 2)
		if len(lines) > 0 {
			s.Description = strings.TrimSpace(lines[0])
		}
	}

	return s, nil
}

// positionalArgRe matches $ARGUMENTS[N] and $N patterns.
var positionalArgRe = regexp.MustCompile(`\$(?:ARGUMENTS\[(\d+)\]|(\d+))`)

// RenderContent substitutes variables in the skill's markdown content.
// Supported: $ARGUMENTS, $ARGUMENTS[N], $N, {{workingDir}}, @filename includes.
func (s *Skill) RenderContent(arguments string, workingDir string) string {
	result := s.Content

	// Track whether any argument placeholder appears in the content
	hasArguments := strings.Contains(result, "$ARGUMENTS") || positionalArgRe.MatchString(result)

	// Replace $ARGUMENTS[N] and $N with positional arguments first (before $ARGUMENTS)
	args := splitArguments(arguments)
	result = positionalArgRe.ReplaceAllStringFunc(result, func(match string) string {
		subs := positionalArgRe.FindStringSubmatch(match)
		var idxStr string
		if subs[1] != "" {
			idxStr = subs[1]
		} else {
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

	// Replace $ARGUMENTS with the full arguments string
	result = strings.ReplaceAll(result, "$ARGUMENTS", arguments)

	// Replace {{workingDir}}
	result = strings.ReplaceAll(result, "{{workingDir}}", workingDir)

	// Expand line-based @filename includes
	result = expandAtFileIncludes(result, workingDir)

	// If $ARGUMENTS was not present and arguments are non-empty, append
	if !hasArguments && arguments != "" {
		result = result + "\nARGUMENTS: " + arguments
	}

	return result
}

// splitArguments splits an arguments string on whitespace, respecting quoted strings.
func splitArguments(arguments string) []string {
	if arguments == "" {
		return nil
	}
	return strings.Fields(arguments)
}

// expandAtFileIncludes expands lines starting with @ into file content.
func expandAtFileIncludes(content string, workingDir string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "@") {
			rel := strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
			if rel == "" {
				continue
			}
			var fullPath string
			if filepath.IsAbs(rel) {
				fullPath = rel
			} else {
				fullPath = filepath.Join(workingDir, rel)
			}
			if data, err := os.ReadFile(fullPath); err == nil {
				out = append(out,
					"----- BEGIN "+rel+" -----",
					string(data),
					"----- END "+rel+" -----",
				)
				continue
			}
			// File not found or unreadable; drop the directive
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// BuildSkillCatalog generates a concise catalog of available skills for system prompt injection.
func BuildSkillCatalog(skills SkillMap) string {
	if len(skills) == 0 {
		return ""
	}

	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("# Available Skills\n\n")
	b.WriteString("Use the `read_skill` tool to read the full content of any skill.\n\n")
	for _, name := range names {
		s := skills[name]
		desc := s.Description
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString(fmt.Sprintf("- **%s**: %s\n", name, desc))
	}
	return b.String()
}

// FilterTools returns a ToolManager filtered by the skill's AllowedTools.
// If AllowedTools is empty, returns the source manager unchanged.
func (s *Skill) FilterTools(source domain.ToolManager) domain.ToolManager {
	if len(s.AllowedTools) == 0 {
		return source
	}
	return NewFilteredToolManager(source, s.AllowedTools)
}

// FilteredToolManager wraps a ToolManager and only exposes a subset of tools by name.
type FilteredToolManager struct {
	source     domain.ToolManager
	allowedSet map[message.ToolName]bool
}

// NewFilteredToolManager creates a tool manager that only exposes tools named in allowedNames.
func NewFilteredToolManager(source domain.ToolManager, allowedNames []string) *FilteredToolManager {
	allowed := make(map[message.ToolName]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowed[message.ToolName(name)] = true
	}
	return &FilteredToolManager{
		source:     source,
		allowedSet: allowed,
	}
}

func (f *FilteredToolManager) GetTools() map[message.ToolName]message.Tool {
	all := f.source.GetTools()
	filtered := make(map[message.ToolName]message.Tool)
	for name, tool := range all {
		if f.allowedSet[name] {
			filtered[name] = tool
		}
	}
	return filtered
}

func (f *FilteredToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	if !f.allowedSet[name] {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' is not allowed by the active skill", name)), nil
	}
	return f.source.CallTool(ctx, name, args)
}

func (f *FilteredToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	panic("FilteredToolManager does not support RegisterTool; register on the underlying manager")
}
