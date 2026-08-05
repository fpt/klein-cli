package app

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/fpt/klein-cli/internal/permission"
	pluginpkg "github.com/fpt/klein-cli/internal/plugin"
	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// RegisterPlugins merges plugins into the agent's registry. Skills are merged
// into the existing skill map (existing entries win, so project skills override
// plugin skills). Commands and agents are indexed by both their scoped
// "<plugin>:<name>" identifier and — when unambiguous — by their bare name.
// A no-op if plugins is empty.
//
// Currently MCP servers from plugins are NOT auto-mounted by this call. The
// caller (typically main.go) merges them into config.Settings.MCP.Servers
// before initialising the MCP integration, because the integration is
// constructed before the agent.
func (a *Agent) RegisterPlugins(plugins []*pluginpkg.Plugin) {
	if a.pluginCommands == nil {
		a.pluginCommands = make(map[string]*pluginpkg.Command)
	}
	if a.definitions == nil {
		a.definitions = make(skill.DefinitionMap)
	}
	if a.ambiguousCommands == nil {
		a.ambiguousCommands = make(map[string]bool)
	}
	if a.ambiguousAgents == nil {
		a.ambiguousAgents = make(map[string]bool)
	}

	for _, p := range plugins {
		if p == nil {
			continue
		}
		a.plugins = append(a.plugins, p)

		// Merge skills (do not override existing entries — project/user
		// scopes already loaded by skill.LoadSkills win).
		for name, s := range p.Skills {
			if _, exists := a.definitions[name]; !exists {
				a.definitions[name] = s
			}
		}

		// Index commands.
		for name, cmd := range p.Commands {
			scoped := p.Name + ":" + name
			a.pluginCommands[scoped] = cmd

			if a.ambiguousCommands[name] {
				continue
			}
			if existing, ok := a.pluginCommands[name]; ok && existing != cmd {
				delete(a.pluginCommands, name)
				a.ambiguousCommands[name] = true
			} else {
				a.pluginCommands[name] = cmd
			}
		}

		a.indexPluginAgents(p)
	}
}

// indexPluginAgents registers one plugin's agents. Each is always reachable
// under its scoped "<plugin>:<agent>" name; the bare name is contested and may
// end up owned by someone else.
func (a *Agent) indexPluginAgents(p *pluginpkg.Plugin) {
	for name, ag := range p.Agents {
		a.definitions[p.Name+":"+name] = ag

		if a.ambiguousAgents[name] {
			continue
		}
		existing, ok := a.definitions[name]
		switch {
		case !ok || existing == ag:
			a.definitions[name] = ag
		case existing.PluginName == "":
			// A built-in or project/user definition — agent, role, or skill —
			// already owns this bare name. It wins, the same precedence skills
			// use where a local definition overrides a plugin's, and the
			// plugin's agent stays reachable as "<plugin>:<agent>". Not
			// ambiguous: there is a defined winner.
		default:
			// Two plugins claim the same bare name; neither wins and the
			// caller must scope it.
			delete(a.definitions, name)
			a.ambiguousAgents[name] = true
		}
	}
}

// ResolveCommand looks up a plugin command by its scoped "<plugin>:<name>"
// identifier or by bare name. Returns (cmd, ambiguous).
//   - cmd != nil, ambiguous == false: unique match.
//   - cmd == nil, ambiguous == true:  bare name matches >1 plugin; caller
//     should ask the user to scope it.
//   - cmd == nil, ambiguous == false: no such command.
func (a *Agent) ResolveCommand(name string) (*pluginpkg.Command, bool) {
	if name == "" {
		return nil, false
	}
	if cmd, ok := a.pluginCommands[name]; ok {
		return cmd, false
	}
	if a.ambiguousCommands[name] {
		return nil, true
	}
	return nil, false
}

// mergeDefinitions folds roles, skills, and agents into the single registry the
// agent resolves against. Which names a caller may reach is decided by declared
// mode at lookup time (lookupInvocable vs ResolveSubagent), not by keeping
// separate maps.
//
// Roles, skills, and agents occupy one namespace now, so a name claimed by two
// of them has to have a winner: higher Priority wins, and on a tie the
// non-agent keeps the name, which is what preserves today's Invoke behavior.
// No built-in collides; a user-authored one is logged loudly because half of
// what they wrote will be unreachable.
func mergeDefinitions(
	logger *pkgLogger.Logger, defs skill.DefinitionMap, agents pluginpkg.AgentMap,
) skill.DefinitionMap {
	merged := make(skill.DefinitionMap, len(defs)+len(agents))
	maps.Copy(merged, defs)

	for name, ag := range agents {
		existing, clash := merged[name]
		if !clash {
			merged[name] = ag
			continue
		}
		if ag.Priority > existing.Priority {
			merged[name] = ag
		}
		logger.Warn("name claimed by both an agent and a role/skill; one of them is unreachable",
			"name", name, "agent", ag.SourcePath, "other", existing.SourcePath,
			"winner", merged[name].SourcePath)
	}

	return merged
}

// selectToolManager picks the tool view a definition runs with, and reports
// whether that view is the deferred/ToolSearch one.
//
// The three cases are ordered by how hard a restriction they are. A CLI
// --allowed-tools override outranks everything. An explicit `tools:` list is a
// hard cap in every mode, so it *replaces* the deferred view rather than
// seeding it — seeding would let ToolSearch hand back exactly what the cap
// excluded. Otherwise `preload:` only decides what is visible up front, and
// anything else stays reachable through ToolSearch, which is what lets the cad
// role discover app MCP tools.
func (a *Agent) selectToolManager(def *skill.Definition) (domain.ToolManager, bool) {
	switch {
	case len(a.allowedToolsOverride) > 0:
		return skill.NewFilteredToolManager(a.allToolManagers, a.allowedToolsOverride), false
	case len(def.Tools) > 0:
		return skill.NewFilteredToolManager(a.allToolManagers, def.Tools), false
	case a.deferredTools != nil:
		a.deferredTools.SetCore(def.Preload) // empty → default core
		return a.deferredTools, true
	default:
		// Fallback for agents built without the constructor (tests).
		return def.FilterTools(a.allToolManagers), false
	}
}

// lookupInvocable resolves a name that Invoke may run. Invoke drives the
// session's own turn, which is startup mode for the role it opened with and
// inline mode for a skill run mid-session; a subagent-only definition is not
// reachable here, only through the Task tool.
func (a *Agent) lookupInvocable(name string) (*skill.Definition, bool) {
	d, ok := a.definitions[name]
	if !ok || !d.PermitsAny(skill.ModeStartup, skill.ModeInline) {
		return nil, false
	}
	return d, true
}

// ResolveSubagent looks up a definition the Task tool may dispatch to, the same
// way ResolveCommand resolves commands. Membership is by declared mode, not by
// which file the definition came from: a role that permits subagent mode is
// dispatchable, and an agent that somehow does not is no longer reachable.
func (a *Agent) ResolveSubagent(name string) (*skill.Definition, bool) {
	if name == "" {
		return nil, false
	}
	if d, ok := a.definitions[strings.ToLower(name)]; ok && d.Permits(skill.ModeSubagent) {
		return d, false
	}
	if a.ambiguousAgents[name] {
		return nil, true
	}
	return nil, false
}

// AgentCatalog returns one entry per loaded subagent, for the Task tool's
// available-agents listing.
//
// The listing is curated, not exhaustive: it names agent-kind definitions,
// which are the ones written to be delegated to. ResolveSubagent is deliberately
// wider — any definition permitting subagent mode is dispatchable, including
// skills — but listing all of those made the description ten entries of prose
// written for a different audience ("also invocable as /report <topic>"), with
// create-skill offered as a delegation target. The prose below names the wider
// set in one sentence instead. Agents are indexed twice (scoped and bare), so entries are
// deduplicated by definition pointer and reported under the name the model
// should actually pass: the bare name when it is unambiguous, otherwise the
// scoped "<plugin>:<agent>" form, which is all that survives indexing for a
// contested name. The result is sorted so the tool description — and thus the
// prompt cache — is stable across runs.
func (a *Agent) AgentCatalog() []tool.AgentCatalogEntry {
	preferred := make(map[*pluginpkg.Agent]string, len(a.definitions))
	for name, ag := range a.definitions {
		if !ag.IsAgent() || !ag.Permits(skill.ModeSubagent) {
			continue
		}
		current, seen := preferred[ag]
		if !seen || (strings.Contains(current, ":") && !strings.Contains(name, ":")) {
			preferred[ag] = name
		}
	}

	entries := make([]tool.AgentCatalogEntry, 0, len(preferred))
	for ag, name := range preferred {
		entries = append(entries, tool.AgentCatalogEntry{
			Name:        name,
			Description: ag.Description,
			Tools:       ag.Tools,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// ListPluginCommands returns the names of every command available, scoped
// (`plugin:cmd`) and — for unambiguous bare names — also without scope.
// Used by the REPL autocompleter.
func (a *Agent) ListPluginCommands() []string {
	out := make([]string, 0, len(a.pluginCommands))
	for name := range a.pluginCommands {
		out = append(out, name)
	}
	return out
}

// InvokeCommand renders a plugin command's body and runs it through the
// agent's normal Invoke path, with the command's `allowed-tools` taking
// precedence over any active skill restriction for this turn only.
func (a *Agent) InvokeCommand(ctx context.Context, cmd *pluginpkg.Command, args string, skillName string) (message.Message, error) {
	if cmd == nil {
		return nil, fmt.Errorf("nil command")
	}
	prompt := cmd.Render(args, a.workingDir)

	// Per-invocation allowed-tools override. We save and restore the
	// existing override so the command doesn't leak state into subsequent
	// turns.
	prevOverride := a.allowedToolsOverride
	if len(cmd.AllowedTools) > 0 {
		a.allowedToolsOverride = cmd.AllowedTools
	}
	defer func() { a.allowedToolsOverride = prevOverride }()

	// Plugin commands frequently shell out via Bash; the official Claude Code
	// behaviour is to auto-approve based on the command's `allowed-tools`.
	// We replicate that by adding session rules for each allowed tool for
	// the duration of this invocation. The rules are removed on return.
	addedRules := 0
	if len(cmd.AllowedTools) > 0 {
		for _, tool := range cmd.AllowedTools {
			a.sessionRules.Rules = append(a.sessionRules.Rules,
				permission.PermissionRule{Tool: tool, Pattern: "", Behavior: permission.RuleAllow})
			addedRules++
		}
	}
	defer func() {
		if addedRules > 0 && len(a.sessionRules.Rules) >= addedRules {
			a.sessionRules.Rules = a.sessionRules.Rules[:len(a.sessionRules.Rules)-addedRules]
		}
	}()

	return a.Invoke(ctx, prompt, skillName)
}

// PluginMCPServers returns the merged MCP server configurations contributed
// by all registered plugins. Used by main.go to fold plugin MCP servers into
// the global MCP integration on startup.
func (a *Agent) PluginMCPServers() []domain.MCPServerConfig {
	var out []domain.MCPServerConfig
	for _, p := range a.plugins {
		out = append(out, p.MCPServers...)
	}
	return out
}

// SplitSlashCommand splits "/<name> <args>" into its name and arg string.
// The leading "/" is stripped. Returns empty name when input has no command.
func SplitSlashCommand(input string) (name, args string) {
	input = strings.TrimSpace(input)
	input = strings.TrimPrefix(input, "/")
	if input == "" {
		return "", ""
	}
	parts := strings.SplitN(input, " ", 2)
	name = parts[0]
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return name, args
}
