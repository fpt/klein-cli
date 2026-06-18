package plugin

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// pluginManifest is the shape of .claude-plugin/plugin.json.
type pluginManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Author      struct {
		Name string `json:"name"`
	} `json:"author"`
}

// marketplaceManifest is the shape of .claude-plugin/marketplace.json.
type marketplaceManifest struct {
	Name  string `json:"name"`
	Owner struct {
		Name string `json:"name"`
	} `json:"owner"`
	Plugins []struct {
		Name        string `json:"name"`
		Source      string `json:"source"`
		Description string `json:"description"`
	} `json:"plugins"`
}

// mcpManifest is the shape of a plugin's .mcp.json. Keys are server names.
type mcpManifest struct {
	MCPServers map[string]struct {
		Type    string            `json:"type"`
		Command string            `json:"command,omitempty"`
		Args    []string          `json:"args,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
		URL     string            `json:"url,omitempty"`
		// Optional auth. Plugins shipped to the marketplace usually omit
		// these — the user fills them in locally before enabling the plugin.
		AuthorizationToken string            `json:"authorization_token,omitempty"`
		Headers            map[string]string `json:"headers,omitempty"`
	} `json:"mcpServers"`
}

// LoadMarketplace loads a marketplace from the directory containing
// .claude-plugin/marketplace.json. Each plugin's `source` is resolved
// relative to that directory.
func LoadMarketplace(root string) (*Marketplace, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving marketplace root %q: %w", root, err)
	}
	manifestPath := filepath.Join(absRoot, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading marketplace manifest %s: %w", manifestPath, err)
	}
	var mm marketplaceManifest
	if err := json.Unmarshal(data, &mm); err != nil {
		return nil, fmt.Errorf("parsing marketplace manifest %s: %w", manifestPath, err)
	}

	mp := &Marketplace{
		Name:    mm.Name,
		Owner:   mm.Owner.Name,
		Root:    absRoot,
		Plugins: make(map[string]*Plugin, len(mm.Plugins)),
	}

	for _, entry := range mm.Plugins {
		source := entry.Source
		if source == "" {
			continue
		}
		// `source` is conventionally "./<dir>". Resolve relative to absRoot.
		pluginRoot := source
		if !filepath.IsAbs(pluginRoot) {
			pluginRoot = filepath.Join(absRoot, source)
		}
		p, err := LoadPlugin(pluginRoot, entry.Name)
		if err != nil {
			// Non-fatal: skip but record nothing — the caller can re-scan
			// individually if they want a hard error. Most marketplaces
			// have many plugins and a single broken one shouldn't sink
			// the whole startup.
			fmt.Fprintf(os.Stderr, "plugin: skipping %s: %v\n", entry.Name, err)
			continue
		}
		// Manifest entry's description wins if the plugin.json didn't set one.
		if p.Description == "" {
			p.Description = entry.Description
		}
		mp.Plugins[p.Name] = p
	}

	return mp, nil
}

// LoadPlugin loads a single plugin from `root`. If `nameHint` is non-empty
// and the plugin.json is missing or doesn't specify a name, the hint is used.
// As a last resort the directory basename is used.
func LoadPlugin(root, nameHint string) (*Plugin, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving plugin root %q: %w", root, err)
	}

	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("stat plugin root %s: %w", absRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plugin root %s is not a directory", absRoot)
	}

	p := &Plugin{
		Root:     absRoot,
		Commands: make(map[string]*Command),
		Agents:   make(map[string]*Agent),
		Skills:   make(skill.SkillMap),
	}

	// .claude-plugin/plugin.json — optional. If missing, we still try to
	// load commands/agents/skills/.mcp.json since some plugins in the wild
	// omit the manifest.
	manifestPath := filepath.Join(absRoot, ".claude-plugin", "plugin.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var pm pluginManifest
		if jerr := json.Unmarshal(data, &pm); jerr != nil {
			return nil, fmt.Errorf("parsing plugin manifest %s: %w", manifestPath, jerr)
		}
		p.Name = pm.Name
		p.Description = pm.Description
		p.Version = pm.Version
		p.Author = pm.Author.Name
	}

	if p.Name == "" {
		p.Name = nameHint
	}
	if p.Name == "" {
		p.Name = filepath.Base(absRoot)
	}

	if err := p.loadCommands(); err != nil {
		return nil, err
	}
	if err := p.loadAgents(); err != nil {
		return nil, err
	}
	if err := p.loadSkills(); err != nil {
		return nil, err
	}
	if err := p.loadMCPServers(); err != nil {
		return nil, err
	}

	return p, nil
}

func (p *Plugin) loadCommands() error {
	dir := filepath.Join(p.Root, "commands")
	if !isDir(dir) {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("reading %s: %w", path, rerr)
		}
		cmd, perr := ParseCommandMD(data, path, p.Name)
		if perr != nil {
			return perr
		}
		p.Commands[cmd.Name] = cmd
		return nil
	})
}

func (p *Plugin) loadAgents() error {
	dir := filepath.Join(p.Root, "agents")
	if !isDir(dir) {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("reading %s: %w", path, rerr)
		}
		ag, perr := ParseAgentMD(data, path, p.Name)
		if perr != nil {
			return perr
		}
		p.Agents[ag.Name] = ag
		return nil
	})
}

func (p *Plugin) loadSkills() error {
	dir := filepath.Join(p.Root, "skills")
	if !isDir(dir) {
		return nil
	}
	// Reuse the skill loader. Priority 2 puts plugin skills between embedded
	// (0) and project-local .claude/skills (3) — same as the official
	// precedence order. Skill names that collide with project-local skills
	// will be overridden by the project, which matches Claude Code.
	skills, err := skill.LoadSkillsFromDir(dir, 2)
	if err != nil {
		return fmt.Errorf("loading skills under %s: %w", dir, err)
	}
	for name, s := range skills {
		p.Skills[name] = s
	}
	return nil
}

func (p *Plugin) loadMCPServers() error {
	path := filepath.Join(p.Root, ".mcp.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var mm mcpManifest
	if err := json.Unmarshal(data, &mm); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	for name, server := range mm.MCPServers {
		cfg := domain.MCPServerConfig{
			Name:               name,
			Enabled:            true,
			Type:               domain.MCPServerType(server.Type),
			Command:            server.Command,
			Args:               server.Args,
			URL:                server.URL,
			AuthorizationToken: server.AuthorizationToken,
			Headers:            server.Headers,
		}
		// Flatten env map to []string ("KEY=VALUE") to match
		// domain.MCPServerConfig.Env shape used by stdio servers.
		for k, v := range server.Env {
			cfg.Env = append(cfg.Env, k+"="+v)
		}
		p.MCPServers = append(p.MCPServers, cfg)
	}
	return nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
