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

	// Skills loaded from this plugin's skills/ tree. Use them with skill.DefinitionMap-
	// compatible APIs.
	Skills skill.DefinitionMap

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

// Agent is a subagent definition. It comes from one of three places, all using
// the same file format: <plugin>/agents/*.md (loaded by Plugin.loadAgents),
// the embedded built-ins, or a personal/project agents/ directory (both loaded
// by LoadAgents).
//
// An agent, a role, and a skill are the same object — a named prompt plus a
// tool policy — differing only in who invokes them and where the output goes,
// so this is an alias for the shared type rather than a struct of its own.
// The alias keeps the ~40 existing `plugin.Agent` references compiling.
type Agent = skill.Definition

// AgentMap maps an agent's bare name to its definition.
type AgentMap = skill.DefinitionMap

// Marketplace is a collection of plugins discovered via
// .claude-plugin/marketplace.json.
type Marketplace struct {
	Name    string
	Owner   string
	Root    string             // absolute path to the directory containing .claude-plugin/
	Plugins map[string]*Plugin // by plugin name
}
