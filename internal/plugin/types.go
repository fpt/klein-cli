// Package plugin loads Claude Code-style plugin marketplaces and individual
// plugins. A plugin is a directory containing some combination of
// .claude-plugin/plugin.json, commands/*.md, agents/*.md, skills/*/SKILL.md,
// hooks/hooks.json, and .mcp.json — the same layout used by the official
// Claude Code CLI. A marketplace is a directory containing
// .claude-plugin/marketplace.json which lists multiple plugins by relative
// path.
package plugin

import (
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// Plugin is a loaded Claude Code plugin.
type Plugin struct {
	// Name from .claude-plugin/plugin.json (or directory name as fallback).
	// This is the namespace prefix for /plugin:command invocations.
	Name        string
	Description string
	Version     string
	Author      string
	Root        string // absolute path to plugin directory

	// Commands are keyed by their bare command name (filename without .md
	// extension). The scoped name "<plugin>:<command>" is constructed at
	// dispatch time.
	Commands map[string]*Command

	// Agents are keyed by the agent's frontmatter `name` field, falling back
	// to the filename. The scoped name "<plugin>:<agent>" is constructed at
	// dispatch time.
	Agents map[string]*Agent

	// Skills loaded from this plugin's skills/ tree. Use them with skill.SkillMap-
	// compatible APIs.
	Skills skill.SkillMap

	// MCPServers parsed from the plugin's .mcp.json. Names are taken from the
	// JSON keys. Configurations are returned with Enabled=true since the
	// plugin opted in by shipping the file.
	MCPServers []domain.MCPServerConfig
}

// ScopedCommands returns commands keyed by "<plugin>:<name>".
func (p *Plugin) ScopedCommands() map[string]*Command {
	out := make(map[string]*Command, len(p.Commands))
	for name, cmd := range p.Commands {
		out[p.Name+":"+name] = cmd
	}
	return out
}

// ScopedAgents returns agents keyed by "<plugin>:<name>".
func (p *Plugin) ScopedAgents() map[string]*Agent {
	out := make(map[string]*Agent, len(p.Agents))
	for name, ag := range p.Agents {
		out[p.Name+":"+name] = ag
	}
	return out
}

// Command is a slash command loaded from <plugin>/commands/*.md.
//
// The body may reference $ARGUMENTS, $N, $ARGUMENTS[N], {{workingDir}}, and
// @filename includes; expansion is identical to the skill renderer (see
// (*Command).Render).
type Command struct {
	Name         string   // bare name (filename without .md)
	PluginName   string   // owning plugin's name; empty for project-local commands
	Description  string   // from frontmatter
	ArgumentHint string   // from frontmatter
	AllowedTools []string // from frontmatter (may be YAML list OR comma-separated string)
	Model        string   // optional model override (ignored for now)
	Body         string   // markdown body, with placeholders intact
	SourcePath   string
}

// Agent is a subagent loaded from <plugin>/agents/*.md or .claude/agents/*.md.
type Agent struct {
	Name        string   // from frontmatter (required); falls back to filename
	PluginName  string   // owning plugin's name; empty for project/user agents
	Description string   // from frontmatter — used by the parent to decide when to delegate
	Tools       []string // empty = inherit all
	Model       string   // sonnet/opus/haiku/fable/<full-id>/inherit
	Background  bool     // load-only; klein currently runs sub-agents synchronously
	Color       string
	Body        string // system-prompt body
	SourcePath  string
}

// Marketplace is a collection of plugins discovered via
// .claude-plugin/marketplace.json.
type Marketplace struct {
	Name    string
	Owner   string
	Root    string             // absolute path to the directory containing .claude-plugin/
	Plugins map[string]*Plugin // by plugin name
}
