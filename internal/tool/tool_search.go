package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// ToolSearchName is the name of the tool-discovery tool.
const ToolSearchName message.ToolName = "ToolSearch"

// defaultCoreTools are always exposed to the model (never deferred). They are
// the bread-and-butter file/shell/todo tools plus ToolSearch itself, so basic
// work needs no discovery round-trip.
var defaultCoreTools = map[message.ToolName]bool{
	"Read": true, "LS": true, "Glob": true, "Grep": true,
	"Write": true, "Edit": true, "Bash": true, "TodoWrite": true,
}

// DeferredToolManager wraps a full tool catalog but exposes only a small core
// set plus ToolSearch. The rest are "deferred": the model discovers and loads
// them by calling ToolSearch, after which they appear in subsequent requests.
//
// This lets skills omit allowed-tools entirely — every tool stays reachable
// without sending all schemas on every turn.
type DeferredToolManager struct {
	source domain.ToolManager
	core   map[message.ToolName]bool

	mu     sync.Mutex
	active map[message.ToolName]bool // loaded via ToolSearch (persists for the manager's life)

	searchTool message.Tool
}

// NewDeferredToolManager wraps source, exposing only the core set + ToolSearch
// until more tools are loaded via ToolSearch.
func NewDeferredToolManager(source domain.ToolManager) *DeferredToolManager {
	d := &DeferredToolManager{
		source: source,
		core:   defaultCoreTools,
		active: make(map[message.ToolName]bool),
	}
	d.searchTool = &deferredSearchTool{mgr: d}
	return d
}

// SetCore sets the initially-exposed ("core") tools — typically a skill's
// allowed-tools. An empty list restores the default core. Everything not in the
// core (including MCP tools) stays deferred and reachable via ToolSearch.
// Call before the ReAct loop; not safe to call concurrently with GetTools.
func (d *DeferredToolManager) SetCore(names []string) {
	if len(names) == 0 {
		d.core = defaultCoreTools
		return
	}
	core := make(map[message.ToolName]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			core[message.ToolName(n)] = true
		}
	}
	d.core = core
}

// isExposed reports whether a tool is currently visible to the model.
func (d *DeferredToolManager) isExposed(name message.ToolName) bool {
	if name == ToolSearchName || d.core[name] {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active[name]
}

func (d *DeferredToolManager) GetTools() map[message.ToolName]message.Tool {
	out := make(map[message.ToolName]message.Tool)
	for name, t := range d.source.GetTools() {
		if name == ToolSearchName {
			continue // never expose a source-provided ToolSearch; ours wins
		}
		if d.isExposed(name) {
			out[name] = t
		}
	}
	out[ToolSearchName] = d.searchTool
	return out
}

func (d *DeferredToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	if name == ToolSearchName {
		return d.searchTool, true
	}
	t, ok := d.source.GetTools()[name]
	return t, ok
}

func (d *DeferredToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	if name == ToolSearchName {
		return d.handleSearch(args), nil
	}
	return d.source.CallTool(ctx, name, args)
}

// RegisterTool is not supported; register on the underlying manager.
func (d *DeferredToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	panic("DeferredToolManager does not support RegisterTool; register on the underlying manager")
}

// toolInfo is a catalog entry used for searching.
type toolInfo struct {
	name message.ToolName
	desc string
}

// deferredCatalog returns the searchable (non-core, non-ToolSearch) tools.
func (d *DeferredToolManager) deferredCatalog() []toolInfo {
	var out []toolInfo
	for name, t := range d.source.GetTools() {
		if name == ToolSearchName || d.core[name] {
			continue
		}
		out = append(out, toolInfo{name: name, desc: t.Description().String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// DeferredNames returns the sorted names of currently-deferred tools (not yet
// loaded). Used to tell the model what it can search for.
func (d *DeferredToolManager) DeferredNames() []string {
	d.mu.Lock()
	active := make(map[message.ToolName]bool, len(d.active))
	for k, v := range d.active {
		active[k] = v
	}
	d.mu.Unlock()

	var names []string
	for _, info := range d.deferredCatalog() {
		if !active[info.name] {
			names = append(names, string(info.name))
		}
	}
	return names
}

// CatalogHint returns a system-prompt line announcing the deferred tools, or ""
// when none remain deferred.
func (d *DeferredToolManager) CatalogHint() string {
	names := d.DeferredNames()
	if len(names) == 0 {
		return ""
	}
	return "# Additional tools (load on demand)\n\n" +
		"These tools are available but not loaded. Call `ToolSearch` with a keyword " +
		"query (or `select:Name1,Name2`) to load the ones you need; they become " +
		"callable on the next step.\n\n" +
		strings.Join(names, ", ")
}

func (d *DeferredToolManager) handleSearch(args message.ToolArgumentValues) message.ToolResult {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return message.NewToolResultError("query is required (a keyword, or 'select:Name1,Name2')")
	}
	maxResults := 5
	if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
		maxResults = int(v)
	}

	catalog := d.deferredCatalog()
	var matched []toolInfo

	if rest, ok := strings.CutPrefix(query, "select:"); ok {
		wanted := make(map[string]bool)
		for _, n := range strings.Split(rest, ",") {
			if n = strings.TrimSpace(n); n != "" {
				wanted[strings.ToLower(n)] = true
			}
		}
		for _, info := range catalog {
			if wanted[strings.ToLower(string(info.name))] {
				matched = append(matched, info)
			}
		}
	} else {
		matched = keywordSearch(catalog, query, maxResults)
	}

	if len(matched) == 0 {
		return message.NewToolResultText(fmt.Sprintf(
			"No tools matched %q. Deferred tools: %s", query, strings.Join(d.DeferredNames(), ", ")))
	}

	d.mu.Lock()
	for _, m := range matched {
		d.active[m.name] = true
	}
	d.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "Loaded %d tool(s) — callable from the next step:\n", len(matched))
	for _, m := range matched {
		fmt.Fprintf(&b, "- %s: %s\n", m.name, firstSentence(m.desc, 160))
	}
	return message.NewToolResultText(strings.TrimRight(b.String(), "\n"))
}

// keywordSearch ranks deferred tools by how many query terms appear in the
// tool's name or description, returning up to maxResults matches.
func keywordSearch(catalog []toolInfo, query string, maxResults int) []toolInfo {
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		info  toolInfo
		score int
	}
	var results []scored
	for _, info := range catalog {
		hay := strings.ToLower(string(info.name) + " " + info.desc)
		score := 0
		for _, t := range terms {
			if strings.Contains(hay, t) {
				score++
			}
		}
		if score > 0 {
			results = append(results, scored{info, score})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].info.name < results[j].info.name
	})
	out := make([]toolInfo, 0, maxResults)
	for i := 0; i < len(results) && i < maxResults; i++ {
		out = append(out, results[i].info)
	}
	return out
}

func firstSentence(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.IndexByte(s, '.'); i > 0 && i < max {
		return s[:i+1]
	}
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

// deferredSearchTool implements message.Tool for ToolSearch.
type deferredSearchTool struct{ mgr *DeferredToolManager }

func (t *deferredSearchTool) RawName() message.ToolName { return ToolSearchName }
func (t *deferredSearchTool) Name() message.ToolName    { return ToolSearchName }
func (t *deferredSearchTool) Description() message.ToolDescription {
	return "Find and load tools that are available but not currently loaded. " +
		"Pass a keyword query (e.g. 'web fetch', 'stock price', 'pdf', 'schedule') or " +
		"'select:Name1,Name2' to load specific tools by name. Loaded tools become callable " +
		"on the next step. Check the '# Additional tools' list for what can be loaded."
}
func (t *deferredSearchTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{Name: "query", Description: "Keyword(s) to match tool names/descriptions, or 'select:Name1,Name2'", Required: true, Type: "string"},
		{Name: "max_results", Description: "Max tools to load for a keyword query (default 5)", Required: false, Type: "number"},
	}
}
func (t *deferredSearchTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		return t.mgr.handleSearch(args), nil
	}
}

var _ domain.ToolManager = (*DeferredToolManager)(nil)
