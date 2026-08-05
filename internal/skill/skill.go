// Package skill loads and renders the three kinds of definition klein runs:
// skills, roles, and agents.
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

// Kind records which file a definition was loaded from. It is not a behavioral
// switch in itself; it decides how the legacy `allowed-tools` field is
// interpreted (see Definition.Tools/Preload) and gates where a name may be
// resolved. A later step replaces those gates with declared invocation modes.
type Kind int

// The three kinds of definition file. KindSkill is the zero value, so a bare
// Definition{} behaves as a skill — which is what the previous Skill type did.
const (
	KindSkill Kind = iota // skills/{name}/SKILL.md — reached from inside a session
	KindRole              // roles/{name}/ROLE.md — the startup prompt a session opens with
	KindAgent             // agents/{name}.md — a delegation target for the Task tool
)

// Definition is a parsed skill, role, or agent. All three are the same object —
// a named, purpose-specific prompt plus a tool policy — differing only in who
// invokes them and where their output goes, so they share one type and one
// registry.
type Definition struct {
	// Tools is a hard allowlist: when non-empty the definition can reach
	// nothing outside it, in any mode. Comes from `tools:`, and from
	// `allowed-tools:` on an agent, where that field has always been a cap.
	Tools []string

	// DisallowedTools is a denylist applied after Tools. Parsed but not yet
	// enforced (#78).
	DisallowedTools []string

	// Preload lists the tools exposed up front in the deferred/ToolSearch view.
	// It is a visibility hint, NOT a security boundary — the model can still
	// reach unexposed tools via ToolSearch, which is what lets the cad role
	// discover app MCP tools. Comes from `preload:`, and from `allowed-tools:`
	// on a role or skill, where that field has always meant exactly this.
	Preload []string

	Name         string // from frontmatter, or the file/directory name
	Description  string // from frontmatter
	Content      string // markdown body after the frontmatter; the system prompt
	SourcePath   string // filesystem path or "embedded:<name>"
	PluginName   string // owning plugin; empty for built-in and project/user definitions
	Model        string
	ArgumentHint string
	Color        string

	Priority int  // ladder position; larger wins a name collision
	Kind     Kind // which file this came from

	DisableModelInvocation bool
	UserInvocable          bool // default true
	Background             bool // load-only; sub-agents run synchronously today
}

// IsRole reports whether this definition is a startup prompt.
func (d *Definition) IsRole() bool { return d.Kind == KindRole }

// IsAgent reports whether this definition is a Task delegation target.
func (d *Definition) IsAgent() bool { return d.Kind == KindAgent }

// EffectiveTools returns the definition's declared tool list: Tools when set,
// otherwise Preload. It is used as a hard cap when the definition runs with its
// own tool manager, and as the list ReadSkill shows the model.
//
// The Preload fallback is what preserves behavior: spawn_agent has always
// treated a skill's allowed-tools as a cap, even though the same field is only
// a visibility hint on the startup path.
func (d *Definition) EffectiveTools() []string {
	if len(d.Tools) > 0 {
		return d.Tools
	}
	return d.Preload
}

// frontmatter maps YAML frontmatter fields, accepting both the skill/role
// schema (`allowed-tools`) and the agent schema (`tools`, `disallowedTools`).
//
// The tool lists are decoded into `any` because the format accepts both a YAML
// sequence (`["Read", "Write"]` or block form) and a comma-separated string.
// parseAllowedTools normalises both shapes.
type frontmatter struct {
	AllowedTools    any `yaml:"allowed-tools"`
	Tools           any `yaml:"tools"`
	DisallowedTools any `yaml:"disallowedTools"`
	Preload         any `yaml:"preload"`

	UserInvocable *bool `yaml:"user-invocable"`

	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
	Model        string `yaml:"model"`
	Color        string `yaml:"color"`
	// Accepted but not enforced today.
	PermissionMode string `yaml:"permissionMode"`

	MaxTurns               int  `yaml:"maxTurns"`
	DisableModelInvocation bool `yaml:"disable-model-invocation"`
	Background             bool `yaml:"background"`
}

// parseAllowedTools normalises the `allowed-tools` frontmatter field. The
// accepted shapes are:
//   - nil/missing → no restriction (returns nil)
//   - YAML sequence → []any of strings
//   - comma-separated string → "Read, Write, Bash"
//   - bare string → treated as a single tool name
func parseAllowedTools(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		var out []string
		for _, part := range strings.Split(t, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// applyFrontmatter copies parsed frontmatter onto the definition, routing the
// tool lists by kind.
func (d *Definition) applyFrontmatter(fm *frontmatter, kind Kind) {
	d.Name = fm.Name
	d.Description = fm.Description
	d.ArgumentHint = fm.ArgumentHint
	d.DisableModelInvocation = fm.DisableModelInvocation
	d.Model = fm.Model
	d.Background = fm.Background
	d.Color = fm.Color

	if fm.UserInvocable != nil {
		d.UserInvocable = *fm.UserInvocable
	}

	d.Tools = parseAllowedTools(fm.Tools)
	d.Preload = parseAllowedTools(fm.Preload)
	d.DisallowedTools = parseAllowedTools(fm.DisallowedTools)

	// Legacy `allowed-tools:` lands in whichever field reproduces the behavior
	// that file type already had: a hard cap on an agent, a visibility hint on
	// a role or skill. Explicit `tools:`/`preload:` win when both are present.
	legacy := parseAllowedTools(fm.AllowedTools)
	switch {
	case len(legacy) == 0:
	case kind == KindAgent && len(d.Tools) == 0:
		d.Tools = legacy
	case kind != KindAgent && len(d.Preload) == 0:
		d.Preload = legacy
	}
}

// ParseSkillMD parses a SKILL.md file into a Definition.
func ParseSkillMD(data []byte, sourcePath string, priority int) (*Definition, error) {
	return ParseDefinition(data, sourcePath, priority, KindSkill)
}

// ParseDefinition parses a skill, role, or agent file. Format: optional YAML
// frontmatter between "---" delimiters, then the markdown body.
//
// kind decides how a legacy `allowed-tools:` list is interpreted, which is the
// one place the three file types have historically disagreed: on an agent it
// has always been a hard cap, on a role or skill only a visibility hint. See
// Definition.Tools and Definition.Preload.
func ParseDefinition(data []byte, sourcePath string, priority int, kind Kind) (*Definition, error) {
	content := string(data)
	s := &Definition{
		UserInvocable: true,
		SourcePath:    sourcePath,
		Priority:      priority,
		Kind:          kind,
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
			s.applyFrontmatter(&fm, kind)
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
func (d *Definition) RenderContent(arguments string, workingDir string) string {
	result := d.Content

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

	// Replace {{home}} with the user's home directory (used e.g. by create-skill
	// to write into ~/.klein/skills).
	if strings.Contains(result, "{{home}}") {
		if home, err := os.UserHomeDir(); err == nil {
			result = strings.ReplaceAll(result, "{{home}}", home)
		}
	}

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
				out = append(
					out,
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
// BuildSkillCatalog renders the skill list injected into the session so the
// model knows what it can reach with ReadSkill.
//
// Roles are left out on purpose: the catalog is about capabilities available
// mid-session, and a role is the startup prompt the session already opened with
// — not something to go and read.
func BuildSkillCatalog(skills DefinitionMap) string {
	names := make([]string, 0, len(skills))
	for name, s := range skills {
		// Roles and agents are both left out: the catalog is about capabilities
		// to read mid-session, not startup prompts or delegation targets.
		if s.IsRole() || s.IsAgent() {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("# Available Skills\n\n")
	b.WriteString("Use the `ReadSkill` tool to read the full content of any skill.\n\n")
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

// FilterTools returns a ToolManager hard-capped to the definition's tool list.
// An empty list returns the source manager unchanged.
func (d *Definition) FilterTools(source domain.ToolManager) domain.ToolManager {
	names := d.EffectiveTools()
	if len(names) == 0 {
		return source
	}
	return NewFilteredToolManager(source, names)
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

// GetTools returns every tool the filter exposes.
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

// CallTool invokes an exposed tool by name.
func (f *FilteredToolManager) CallTool(
	ctx context.Context, name message.ToolName, args message.ToolArgumentValues,
) (message.ToolResult, error) {
	if !f.allowedSet[name] {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' is not allowed by the active skill", name)), nil
	}
	return f.source.CallTool(ctx, name, args)
}

// RegisterTool registers a tool on the underlying manager.
func (f *FilteredToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	panic("FilteredToolManager does not support RegisterTool; register on the underlying manager")
}
