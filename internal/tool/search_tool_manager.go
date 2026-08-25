package tool

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// argPattern is the argument both search tools take: a glob for Glob, a regex
// for Grep.
const argPattern = "pattern"

// The three shapes a Grep result can take. They are a contract, not a hint:
// whichever of rg or grep ends up running, the same output_mode has to come
// back as the same shape.
const (
	outputModeContent          = "content"
	outputModeFilesWithMatches = "files_with_matches"
	outputModeCount            = "count"
)

// The Grep arguments named more than once: declared here, then honored or
// refused by each branch.
const (
	argOutputMode = "output_mode"
	argGlob       = "glob"
	argType       = "type"
	argMultiline  = "multiline"
)

// SearchToolManager provides Glob and Grep tools
type SearchToolManager struct {
	tools              map[message.ToolName]message.Tool
	workingDir         string
	allowedDirectories []string
}

type SearchConfig struct {
	WorkingDir string
	// AllowedDirectories bounds where a search may look, exactly as the
	// filesystem tools' allowlist bounds where a file may be read. Empty means
	// the working directory alone — searching is a read, and a read tool that
	// answers about any path on the machine is a hole whether or not the caller
	// remembered to configure one.
	AllowedDirectories []string
}

func NewSearchToolManager(cfg SearchConfig) domain.ToolManager {
	absWorkingDir, err := filepath.Abs(cfg.WorkingDir)
	if err != nil {
		absWorkingDir = cfg.WorkingDir
	}
	// Resolved once, here, so every path this manager hands to `rg`/`grep`/`find`
	// names where the workspace actually is — including the bare working
	// directory, which is what an omitted `path` argument searches.
	absWorkingDir = resolveSymlinks(absWorkingDir)
	m := &SearchToolManager{
		tools:              make(map[message.ToolName]message.Tool),
		workingDir:         absWorkingDir,
		allowedDirectories: ensureWorkingDirectoryInAllowedList(cfg.AllowedDirectories, absWorkingDir),
	}
	m.register()
	return m
}

func (m *SearchToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	return m.tools[name], m.tools[name] != nil
}
func (m *SearchToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }
func (m *SearchToolManager) RegisterTool(name message.ToolName, desc message.ToolDescription, args []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &searchTool{name: name, description: desc, arguments: args, handler: handler}
}
func (m *SearchToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *SearchToolManager) register() {
	// Glob tool: fast file listing by pattern
	m.RegisterTool("Glob", "Find files by glob pattern (e.g., **/*.go)",
		[]message.ToolArgument{
			{Name: argPattern, Description: "Glob pattern to match", Required: true, Type: "string"},
			{Name: "path", Description: "Base directory (optional)", Required: false, Type: "string"},
		}, m.handleGlob)

	// Grep tool: ripgrep-style content search
	m.RegisterTool("Grep", "Search file contents using ripgrep-compatible flags",
		[]message.ToolArgument{
			{Name: argPattern, Description: "Regex pattern to search", Required: true, Type: "string"},
			{Name: "path", Description: "File/dir to search (optional)", Required: false, Type: "string"},
			{Name: argGlob, Description: "Glob filter (--glob); a basename without ripgrep", Required: false, Type: "string"},
			{Name: argOutputMode, Description: "content|files_with_matches|count", Required: false, Type: "string"},
			{Name: "-B", Description: "Lines before (content mode)", Required: false, Type: "number"},
			{Name: "-A", Description: "Lines after (content mode)", Required: false, Type: "number"},
			{Name: "-C", Description: "Lines before/after (content mode)", Required: false, Type: "number"},
			{Name: "-n", Description: "Show line numbers (content mode)", Required: false, Type: "boolean"},
			{Name: "-i", Description: "Case-insensitive", Required: false, Type: "boolean"},
			{Name: argType, Description: "File type (rg --type); requires ripgrep", Required: false, Type: "string"},
			{Name: "head_limit", Description: "Limit lines/entries", Required: false, Type: "number"},
			{Name: argMultiline, Description: "Dot matches newlines; requires ripgrep", Required: false, Type: "boolean"},
		}, m.handleGrep)
}

// resolve path relative to working dir
// resolvePath turns a caller's path into an absolute one inside the allowlist,
// or refuses it.
//
// The refusal is the point. Glob and Grep hand this path to `rg`/`find`, which
// will happily answer about anywhere on the machine — so without the check they
// read outside the boundary the filesystem tools enforce, and report what they
// found. That gap is only cosmetic while klein's own loop is the caller; once
// these tools are offered to an app-server (see internal/agentbackend/workspace.go)
// it is a remote caller reading arbitrary paths.
//
// Cleaning before checking is what makes `..` a non-escape: filepath.Abs cleans,
// so "../../etc" is resolved and then measured against the allowlist rather than
// being matched as a prefix that happens to start inside it.
func (m *SearchToolManager) resolvePath(p string) (string, error) {
	if p == "" {
		return m.workingDir, nil
	}
	resolved := p
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(m.workingDir, resolved)
	}
	resolved, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", p, err)
	}
	if !pathWithinAllowedDirectories(resolved, m.allowedDirectories) {
		return "", fmt.Errorf("path %s is outside the allowed directories", p)
	}
	// Hand back the resolved location, not the name it was reached by, so the
	// search runs on exactly what was checked. Passing the symlink instead would
	// leave `rg`/`grep` to resolve it again, and a check that validates one path
	// while the command opens another is not a check.
	return resolveSymlinks(resolved), nil
}

// handleGlob tries rg --files with --glob when available; falls back to find
func (m *SearchToolManager) handleGlob(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pattern, ok := args[argPattern].(string)
	if !ok {
		return message.NewToolResultError("pattern parameter is required"), nil
	}
	base := m.workingDir
	if p, ok := args["path"].(string); ok && p != "" {
		rp, err := m.resolvePath(p)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", err)), nil
		}
		base = rp
	}

	if _, err := exec.LookPath("rg"); err == nil {
		// rg --files --glob <pattern>
		cmd := exec.CommandContext(ctx, "rg", "--files", "--glob", pattern)
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		if err == nil {
			files := strings.Split(strings.TrimSpace(string(out)), "\n")
			// Portable: alphabetic sort
			sort.Strings(files)
			var b strings.Builder
			for _, f := range files {
				if f == "" {
					continue
				}
				b.WriteString(f)
				b.WriteString("\n")
			}
			return message.NewToolResultText(strings.TrimSuffix(b.String(), "\n")), nil
		}
		// fall through to find on error
	}

	// Fallback: find by -name when pattern has a basename; otherwise list all
	// Note: this is a best-effort portable fallback and may not match ** semantics fully.
	findArgs := []string{"-type", "f"}
	if strings.Contains(pattern, "/") {
		// find supports -name on basename; use -name with last segment
		segs := strings.Split(pattern, "/")
		findArgs = append(findArgs, "-name", segs[len(segs)-1])
	} else {
		findArgs = append(findArgs, "-name", pattern)
	}
	findCallArgs := append([]string{base}, findArgs...)
	cmd := exec.CommandContext(ctx, "find", findCallArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("find failed: %v\nOutput: %s", err, string(out))), nil
	}
	return message.NewToolResultText(strings.TrimSpace(string(out))), nil
}

// handleGrep executes ripgrep when available; falls back to grep
func (m *SearchToolManager) handleGrep(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pattern, ok := args[argPattern].(string)
	if !ok {
		return message.NewToolResultError("pattern parameter is required"), nil
	}

	base := m.workingDir
	if p, ok := args["path"].(string); ok && p != "" {
		rp, err := m.resolvePath(p)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", err)), nil
		}
		base = rp
	}

	outputMode := outputModeFilesWithMatches
	if om, ok := args[argOutputMode].(string); ok && om != "" {
		outputMode = om
	}
	switch outputMode {
	case outputModeContent, outputModeFilesWithMatches, outputModeCount:
	default:
		// Refused rather than quietly treated as the default: output_mode
		// decides the *shape* of the result, so a caller that asked for one
		// shape and silently got another has no way to notice.
		return message.NewToolResultError(fmt.Sprintf(
			"unknown output_mode %q: expected %s, %s or %s",
			outputMode, outputModeContent, outputModeFilesWithMatches, outputModeCount)), nil
	}

	req := grepRequest{args: args, pattern: pattern, base: base, outputMode: outputMode}
	if _, err := exec.LookPath("rg"); err == nil {
		return runRipgrep(ctx, req)
	}
	return runGrep(ctx, req)
}

// grepRequest is one validated Grep call: the caller's arguments, the pattern,
// the path it resolved to, and the shape it asked for.
type grepRequest struct {
	args       message.ToolArgumentValues
	pattern    string
	base       string
	outputMode string
}

// runRipgrep is the branch taken where ripgrep is installed.
func runRipgrep(ctx context.Context, req grepRequest) (message.ToolResult, error) {
	rgArgs := append(ripgrepFlags(req), req.pattern, req.base)
	text, err := runSearch(ctx, "rg", rgArgs...)
	if err != nil {
		return message.NewToolResultError(err.Error()), nil
	}
	return message.NewToolResultText(strings.TrimRight(headLimit(text, req.args), "\n")), nil
}

// runGrep is the fallback branch, taken where ripgrep is not installed.
//
// Every argument rg honors has to be honored here too, or refused. The same
// Grep call must not come back in a different shape depending on whether
// ripgrep happens to be installed on the machine klein runs on — that is a
// contract with a model, which asks for files_with_matches and would otherwise
// be handed whole files.
func runGrep(ctx context.Context, req grepRequest) (message.ToolResult, error) {
	if err := grepCannotExpress(req.args); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}
	grepArgs, err := grepFlags(req)
	if err != nil {
		return message.NewToolResultError(err.Error()), nil
	}
	text, err := runSearch(ctx, "grep", append(grepArgs, "-E", req.pattern, req.base)...)
	if err != nil {
		return message.NewToolResultError(err.Error()), nil
	}
	if req.outputMode == outputModeCount {
		// grep -r -c reports every file it walked, matches or not; rg -c lists
		// only the files that matched. Same shape, or the count mode means two
		// different things on two machines.
		text = dropZeroCounts(text)
	}
	return message.NewToolResultText(strings.TrimRight(headLimit(text, req.args), "\n")), nil
}

// runSearch executes rg or grep and hands back what it printed. Exit 1 means no
// matches, which is an empty answer rather than a failure.
func runSearch(ctx context.Context, name string, cmdArgs ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, cmdArgs...).CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return "", fmt.Errorf("%s failed: %w\nOutput: %s", name, err, string(out))
}

// outputModeFlag is the flag that gives a result its shape. rg and grep spell
// all three the same way, which is what makes the two branches agreeable at
// all — everything else about them differs.
func outputModeFlag(outputMode string) []string {
	switch outputMode {
	case outputModeFilesWithMatches:
		return []string{"-l"}
	case outputModeCount:
		return []string{"-c"}
	}
	// content: both tools print the matching lines by default.
	return nil
}

// sharedFlags are the arguments rg and grep spell identically.
func sharedFlags(args message.ToolArgumentValues) []string {
	var flags []string
	for _, name := range []string{"-B", "-A", "-C"} {
		if v, ok := args[name].(float64); ok {
			flags = append(flags, name, strconv.Itoa(int(v)))
		}
	}
	for _, name := range []string{"-n", "-i"} {
		if v, ok := args[name].(bool); ok && v {
			flags = append(flags, name)
		}
	}
	return flags
}

func ripgrepFlags(req grepRequest) []string {
	flags := outputModeFlag(req.outputMode)
	flags = append(flags, sharedFlags(req.args)...)
	if v, ok := req.args[argType].(string); ok && v != "" {
		flags = append(flags, "--type", v)
	}
	if v, ok := req.args[argGlob].(string); ok && v != "" {
		flags = append(flags, "--glob", v)
	}
	if v, ok := req.args[argMultiline].(bool); ok && v {
		flags = append(flags, "-U", "--multiline-dotall")
	}
	return flags
}

func grepFlags(req grepRequest) ([]string, error) {
	// -r, never -R: GNU grep's -R is --dereference-recursive, so it walks out of
	// the workspace through any directory symlink inside it — past a boundary
	// that was checked on the way in. -r matches what rg and find already do by
	// default, which is not to follow.
	flags := append([]string{"-r"}, outputModeFlag(req.outputMode)...)
	if v, ok := req.args[argGlob].(string); ok && v != "" {
		include, err := grepIncludeForGlob(v)
		if err != nil {
			return nil, err
		}
		flags = append(flags, "--include", include)
	}
	return append(flags, sharedFlags(req.args)...), nil
}

// grepCannotExpress names the arguments the fallback has no way to honor.
// Refused rather than ignored: silently answering a narrower question than the
// one asked is how the two branches drifted apart in the first place.
func grepCannotExpress(args message.ToolArgumentValues) error {
	if v, ok := args[argType].(string); ok && v != "" {
		return errors.New("type requires ripgrep, which is not installed; filter with glob instead")
	}
	if v, ok := args[argMultiline].(bool); ok && v {
		return errors.New("multiline requires ripgrep, which is not installed")
	}
	return nil
}

// grepIncludeForGlob maps rg's --glob onto grep's --include, or says it cannot.
//
// grep matches --include against the file name alone, so only a basename glob
// survives the translation. A pattern with a directory component would silently
// match nothing, and an empty result reads to a model as "not there" — a wrong
// answer is worse than a refused one. A leading `**/` is the common spelling of
// "anywhere", and means exactly what grep's recursive walk already does.
func grepIncludeForGlob(glob string) (string, error) {
	include := strings.TrimPrefix(glob, "**/")
	if strings.Contains(include, "/") {
		return "", fmt.Errorf(
			"glob %q has a directory component, which requires ripgrep; use a basename pattern such as %q",
			glob, "*"+filepath.Ext(glob))
	}
	return include, nil
}

// dropZeroCounts removes grep -c's `path:0` lines for files that did not match.
func dropZeroCounts(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if line == "" || strings.HasSuffix(line, ":0") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// headLimit trims the result to the caller's head_limit lines, if any.
func headLimit(text string, args message.ToolArgumentValues) string {
	v, ok := args["head_limit"].(float64)
	if !ok || int(v) <= 0 {
		return text
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= int(v) {
		return text
	}
	return strings.Join(lines[:int(v)], "\n")
}

type searchTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *searchTool) RawName() message.ToolName            { return t.name }
func (t *searchTool) Name() message.ToolName               { return t.name }
func (t *searchTool) Description() message.ToolDescription { return t.description }
func (t *searchTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *searchTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
