package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

// needleAtHome is the match a search inside the workspace must find, so an
// empty result cannot be mistaken for a search that never ran.
const needleAtHome = "needle-at-home"

// searchIn returns a Glob/Grep manager rooted at workingDir, allowed to reach
// extra as well.
func searchIn(t *testing.T, workingDir string, extra ...string) *SearchToolManager {
	t.Helper()
	m, ok := NewSearchToolManager(SearchConfig{
		WorkingDir:         workingDir,
		AllowedDirectories: extra,
	}).(*SearchToolManager)
	if !ok {
		t.Fatal("NewSearchToolManager no longer returns a *SearchToolManager")
	}
	return m
}

// Searching is reading. Glob and Grep hand their path to `rg`/`find`, which will
// answer about anywhere on the machine, so an unchecked path is the same
// disclosure a Read outside the allowlist would be — by another route, and one
// that reports what it found.
func TestSearchToolManager_ResolvePathRefusesOutsideTheAllowlist(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	outside := t.TempDir()
	m := searchIn(t, workingDir)

	refused := []struct {
		name string
		path string
	}{
		{"an absolute path elsewhere", outside},
		{"a system directory", "/etc"},
		// Cleaning before checking is what makes this a non-escape: the path is
		// resolved first, then measured, so it cannot pass as a prefix that
		// merely starts inside the working directory.
		{"an escape by dot-dot", filepath.Join("..", "..", "etc")},
		{"an escape with a real prefix", workingDir + "/../.."},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, err := m.resolvePath(tc.path); err == nil {
				t.Errorf("%s resolved to %q instead of being refused", tc.path, got)
			}
		})
	}
}

// What must still work: the working directory itself, paths under it, and any
// directory the caller explicitly allowed (klein's memory notes, for one — the
// native loop searches those).
func TestSearchToolManager_ResolvePathAllowsTheWorkspaceAndNamedDirectories(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	memoryDir := t.TempDir()
	m := searchIn(t, workingDir, memoryDir)

	allowed := []struct {
		name string
		path string
		want string
	}{
		{"empty means the working directory", "", workingDir},
		{"a relative path under it", "pkg", filepath.Join(workingDir, "pkg")},
		{"the working directory itself", workingDir, workingDir},
		{"an explicitly allowed directory", memoryDir, memoryDir},
		{"a path under an allowed directory", filepath.Join(memoryDir, "daily"), filepath.Join(memoryDir, "daily")},
	}
	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := m.resolvePath(tc.path)
			if err != nil {
				t.Fatalf("%s was refused: %v", tc.path, err)
			}
			// Compared against the resolved form of the expectation, not the
			// literal: resolvePath hands back where the path leads, and on macOS
			// a temp directory leads through /var, itself a symlink to
			// /private/var. Hardcoding either spelling would pin a platform quirk
			// rather than the behavior.
			if want := resolveSymlinks(tc.want); got != want {
				t.Errorf("resolved to %q, want %q", got, want)
			}
		})
	}
}

// The bound applies whether or not the caller remembered to configure one: a
// SearchConfig naming only a working directory is bounded to it, rather than
// being unbounded because the list was empty.
func TestSearchToolManager_EmptyAllowlistStillBoundsToTheWorkingDirectory(t *testing.T) {
	t.Parallel()

	m := searchIn(t, t.TempDir())
	if _, err := m.resolvePath("/etc"); err == nil {
		t.Error("an unconfigured allowlist left the search tools unbounded")
	}
}

// The refusal has to reach the caller as a failed tool result, not as a search
// that quietly returns nothing — a model told "no matches" would conclude the
// file is not there, which is a different and wrong answer.
func TestSearchToolManager_GrepOutsideTheAllowlistFails(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("a needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := searchIn(t, t.TempDir())

	for _, name := range []message.ToolName{"Grep", "Glob"} {
		res, err := m.CallTool(context.Background(), name, message.ToolArgumentValues{
			argPattern:    "needle",
			reviewArgPath: outside,
		})
		if err != nil {
			t.Fatalf("%s returned a transport error rather than a result: %v", name, err)
		}
		if res.Error == "" {
			t.Errorf("%s searched outside the allowlist and returned %q", name, res.Text)
		}
		if !strings.Contains(res.Error, "outside") {
			t.Errorf("%s error does not say why: %q", name, res.Error)
		}
	}
}

// symlinkedEscape builds a workspace containing `link`, a directory symlink
// pointing at an outside directory that holds a file with `needle` in it.
// Returns the workspace and the outside directory.
func symlinkedEscape(t *testing.T) (workingDir, outside string) {
	t.Helper()
	workingDir, outside = t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle-in-the-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workingDir, "link")); err != nil {
		t.Fatal(err)
	}
	return workingDir, outside
}

// A lexical prefix test answers the wrong question: `<workspace>/link` is
// inside the workspace by every string comparison, and outside it by the only
// measure that matters — which file gets opened. The check has to be against
// where the path leads.
func TestSearchToolManager_RefusesAPathReachedThroughASymlink(t *testing.T) {
	t.Parallel()

	workingDir, _ := symlinkedEscape(t)
	m := searchIn(t, workingDir)

	link := filepath.Join(workingDir, "link")
	if got, err := m.resolvePath(link); err == nil {
		t.Errorf("a symlink out of the workspace resolved to %q instead of being refused", got)
	}
	if got, err := m.resolvePath(filepath.Join(link, "secret.txt")); err == nil {
		t.Errorf("a file beyond the symlink resolved to %q instead of being refused", got)
	}
}

// The traversal case, which is the one that survives a correct path check: the
// search is aimed squarely at the workspace, and the escape happens inside the
// walk. GNU grep's -R dereferences directory symlinks and would walk straight
// out; -r, rg and find do not.
//
// The assertion is what came back rather than which flag was passed, so it holds
// whatever tool is installed — but only if it asks for the shape it inspects.
// Grep's default output_mode is files_with_matches, so a run that asserted on
// file *bodies* was really asserting "ripgrep is absent", and Fataled before
// reaching the symlink check on every machine that had it. Each mode therefore
// names the shape it wants and looks for the leak in that shape.
func TestSearchToolManager_SearchDoesNotWalkOutThroughASymlink(t *testing.T) {
	t.Parallel()

	shapes := []struct {
		mode string
		// inside is what a search that actually ran must show, so an empty
		// result cannot be mistaken for one that never happened.
		inside string
		// leaked is what walking out through the symlink would put in the
		// result in this shape.
		leaked string
	}{
		{outputModeContent, needleAtHome, "needle-in-the-secret"},
		{outputModeFilesWithMatches, "inside.txt", "secret.txt"},
		{outputModeCount, "inside.txt:1", "secret.txt"},
	}
	for _, shape := range shapes {
		t.Run(shape.mode, func(t *testing.T) {
			t.Parallel()

			workingDir, outside := symlinkedEscape(t)
			if err := os.WriteFile(filepath.Join(workingDir, "inside.txt"), []byte("needle-at-home\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			m := searchIn(t, workingDir)

			res, err := m.CallTool(context.Background(), "Grep", message.ToolArgumentValues{
				argPattern:    "needle",
				argOutputMode: shape.mode,
			})
			if err != nil || res.Error != "" {
				t.Fatalf("searching the workspace failed: err=%v result=%q", err, res.Error)
			}
			if !strings.Contains(res.Text, shape.inside) {
				t.Fatalf("the search did not run: %q", res.Text)
			}
			if strings.Contains(res.Text, shape.leaked) || strings.Contains(res.Text, outside) {
				t.Errorf("the search walked out through the symlink: %q", res.Text)
			}
		})
	}
}

// searchPath points PATH at a directory holding only the tools named, so a test
// can decide which branch of handleGrep runs. Everything klein shells out to is
// looked up through PATH at call time, so this is the whole switch.
func searchPath(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		installed, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not installed", tool)
		}
		if err := os.Symlink(installed, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// standInRipgrep puts an `rg` on PATH alongside grep. It is not ripgrep — it is
// the smallest thing that answers -l/-c/content the way ripgrep does, which is
// enough to run the branch klein takes when rg is installed. Machines with the
// real thing are covered by the same assertions; this covers the ones without.
func standInRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	dir := searchPath(t, "grep", "bash")
	script := `#!/usr/bin/env bash
set -o pipefail
mode=content
flags=()
while [ $# -gt 0 ]; do
  case "$1" in
    -l) mode=files; shift ;;
    -c) mode=count; shift ;;
    -n|-i|-H) flags+=("$1"); shift ;;
    -B|-A|-C) flags+=("$1" "$2"); shift 2 ;;
    --glob) case "$2" in !*) flags+=(--exclude "${2#!}") ;; *) flags+=(--include "$2") ;; esac; shift 2 ;;
    --type) flags+=(--include "*.$2"); shift 2 ;;
    -U|--multiline-dotall) shift ;;
    *) break ;;
  esac
done
pattern="$1"; target="$2"
# rg prints the path only when more than one file is searched, unless -H says so.
if [ -f "$target" ] && [[ ! " ${flags[*]} " == *" -H "* ]]; then
  flags+=(-h)
fi
case "$mode" in
  files) exec grep -rl "${flags[@]}" -E "$pattern" "$target" ;;
  count) grep -rc "${flags[@]}" -E "$pattern" "$target" | grep -v ':0$' ;;
  *)     exec grep -r "${flags[@]}" -E "$pattern" "$target" ;;
esac
`
	rg := filepath.Join(dir, "rg")
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil { //nolint:gosec // it has to be executable
		t.Fatal(err)
	}
}

// The bug behind the broken test: output_mode decides the *shape* of the
// result, the rg branch read it and the grep fallback ignored it, so the same
// Grep call came back as a path list on one machine and as file bodies on
// another. That was cosmetic while klein's own loop was the only caller; over
// dynamicTools it is a contract with a model on another host, which asks for
// files_with_matches and must not be handed whole files.
func TestSearchToolManager_GrepShapeSurvivesRipgrepsAbsence(t *testing.T) { //nolint:paralleltest // t.Setenv
	modes := []struct {
		mode     string
		want     []string
		unwanted []string
	}{
		// A path list, not the line that matched.
		{outputModeFilesWithMatches, []string{"inside.txt"}, []string{needleAtHome}},
		// The matching line, which carries the needle itself.
		{outputModeContent, []string{needleAtHome}, nil},
		// `path:count`, and only for files that matched.
		{outputModeCount, []string{"inside.txt:1"}, []string{"quiet.txt", needleAtHome}},
	}
	branches := []struct {
		setup func(*testing.T)
		name  string
	}{
		{standInRipgrep, "with ripgrep"},
		{func(t *testing.T) { _ = searchPath(t, "grep") }, "without ripgrep"},
	}
	for _, branch := range branches { //nolint:paralleltest // PATH is process-wide
		for _, tc := range modes {
			t.Run(branch.name+"/"+tc.mode, func(t *testing.T) {
				workingDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(workingDir, "inside.txt"), []byte("needle-at-home\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				// A file the search must not report, so count mode cannot pass
				// by listing everything it walked.
				if err := os.WriteFile(filepath.Join(workingDir, "quiet.txt"), []byte("nothing here\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				m := searchIn(t, workingDir)
				branch.setup(t)

				res, err := m.CallTool(context.Background(), "Grep", message.ToolArgumentValues{
					argPattern:    "needle",
					argOutputMode: tc.mode,
				})
				if err != nil || res.Error != "" {
					t.Fatalf("Grep failed: err=%v result=%q", err, res.Error)
				}
				assertShape(t, tc.mode, res.Text, tc.want, tc.unwanted)
			})
		}
	}
}

// assertShape checks a result carries everything the shape calls for and
// nothing belonging to a different one.
func assertShape(t *testing.T, mode, text string, want, unwanted []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("%s result is missing %q: %q", mode, w, text)
		}
	}
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			t.Errorf("%s result has the wrong shape, it contains %q: %q", mode, u, text)
		}
	}
}

// An output_mode klein does not implement is refused rather than quietly
// treated as the default. The caller asked for a shape; handing back a
// different one without saying so is how the two branches drifted apart in the
// first place.
func TestSearchToolManager_GrepRefusesAnUnknownOutputMode(t *testing.T) {
	t.Parallel()

	m := searchIn(t, t.TempDir())
	res, err := m.CallTool(context.Background(), "Grep", message.ToolArgumentValues{
		argPattern:    "needle",
		argOutputMode: "files",
	})
	if err != nil {
		t.Fatalf("Grep returned a transport error rather than a result: %v", err)
	}
	if res.Error == "" {
		t.Fatalf("an unknown output_mode was accepted and returned %q", res.Text)
	}
	if !strings.Contains(res.Error, outputModeFilesWithMatches) {
		t.Errorf("the refusal does not name the modes that do work: %q", res.Error)
	}
}

// The arguments the fallback cannot express are refused too. grep has no
// --type and no multiline, and its --include matches a file name rather than a
// path — silently ignoring any of them would answer a narrower question than
// the one asked, and an unnoticed wrong answer is worse than a refusal.
func TestSearchToolManager_GrepFallbackRefusesWhatItCannotExpress(t *testing.T) { //nolint:paralleltest // t.Setenv
	unsupported := []struct {
		name string
		args message.ToolArgumentValues
		says string
	}{
		{argType, message.ToolArgumentValues{argType: "go"}, "ripgrep"},
		{argMultiline, message.ToolArgumentValues{argMultiline: true}, "ripgrep"},
		{"a glob with a directory component", message.ToolArgumentValues{argGlob: "pkg/**/*.go"}, "directory component"},
		{"a negated glob with a directory", message.ToolArgumentValues{argGlob: "!pkg/**/*.go"}, "directory component"},
		{"a brace list", message.ToolArgumentValues{argGlob: "*.{go,txt}"}, "brace list"},
	}
	for _, tc := range unsupported { //nolint:paralleltest // PATH is process-wide
		t.Run(tc.name, func(t *testing.T) {
			m := searchIn(t, t.TempDir())
			_ = searchPath(t, "grep")

			args := message.ToolArgumentValues{argPattern: "needle"}
			for k, v := range tc.args {
				args[k] = v
			}
			res, err := m.CallTool(context.Background(), "Grep", args)
			if err != nil {
				t.Fatalf("Grep returned a transport error rather than a result: %v", err)
			}
			if res.Error == "" {
				t.Fatalf("%s was accepted by the fallback and returned %q", tc.name, res.Text)
			}
			if !strings.Contains(res.Error, tc.says) {
				t.Errorf("the refusal does not say why: %q", res.Error)
			}
		})
	}
}

// A basename glob is the shape both tools agree on, so it is honored rather
// than refused — including the `**/`-anywhere spelling, which is what grep's
// recursive walk already does, and ripgrep's `!` exclusion, which is what
// grep's --exclude already means. Mapping a `!` glob onto --include would ask
// for files *named* `!foo` and answer with nothing: the empty result a model
// reads as "not there".
func TestSearchToolManager_GrepFallbackHonorsABasenameGlob(t *testing.T) { //nolint:paralleltest // t.Setenv
	globs := []struct {
		glob string
		// kept is the file the glob should leave in the result, dropped the
		// one it should filter out. Both hold the same needle, so only the
		// glob can tell them apart.
		kept    string
		dropped string
	}{
		{"*.go", "match.go", "skip.txt"},
		{"**/*.go", "match.go", "skip.txt"},
		{"!*.txt", "match.go", "skip.txt"},
		{"!**/*.go", "skip.txt", "match.go"},
	}
	for _, tc := range globs { //nolint:paralleltest // PATH is process-wide
		t.Run(tc.glob, func(t *testing.T) {
			workingDir := t.TempDir()
			for _, name := range []string{"match.go", "skip.txt"} {
				if err := os.WriteFile(filepath.Join(workingDir, name), []byte("needle\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			m := searchIn(t, workingDir)
			_ = searchPath(t, "grep")

			res, err := m.CallTool(context.Background(), "Grep", message.ToolArgumentValues{
				argPattern:    "needle",
				argGlob:       tc.glob,
				argOutputMode: outputModeFilesWithMatches,
			})
			if err != nil || res.Error != "" {
				t.Fatalf("Grep failed: err=%v result=%q", err, res.Error)
			}
			if !strings.Contains(res.Text, tc.kept) {
				t.Errorf("the glob excluded %s, which it should have matched: %q", tc.kept, res.Text)
			}
			if strings.Contains(res.Text, tc.dropped) {
				t.Errorf("the glob did not filter out %s: %q", tc.dropped, res.Text)
			}
		})
	}
}

// A file grep cannot open is not a reason to throw away the matches it did
// find. grep exits 2 for that — "an error occurred" — even though it searched
// everything else successfully and printed the results, so treating exit 2 as
// failure would put one unreadable file anywhere under the workspace between
// the caller and every result. On an rg host the identical call answers
// normally (rg warns on stderr and exits 0), which makes this the same
// host-dependent divergence one layer down.
//
// The warning itself must stay out of the result: in a path list a
// "Permission denied" line reads as a path.
func TestSearchToolManager_GrepKeepsMatchesFoundBesideAnUnreadableFile(t *testing.T) { //nolint:paralleltest // t.Setenv
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable directories, so there is nothing to fail on")
	}
	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, "readable.txt"), []byte(needleAtHome+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(workingDir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "b.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	m := searchIn(t, workingDir)
	_ = searchPath(t, "grep")

	res, err := m.CallTool(context.Background(), "Grep", message.ToolArgumentValues{
		argPattern:    "needle",
		argOutputMode: outputModeFilesWithMatches,
	})
	if err != nil || res.Error != "" {
		t.Fatalf("one unreadable file failed the whole search: err=%v result=%q", err, res.Error)
	}
	if !strings.Contains(res.Text, "readable.txt") {
		t.Errorf("the matches found alongside it were discarded: %q", res.Text)
	}
	if strings.Contains(res.Text, "Permission denied") {
		t.Errorf("grep's stderr was folded into the path list: %q", res.Text)
	}
}

// rg prints the path prefix only when more than one file is searched; grep -r
// prints it always. klein pins -H on both so a single-file Grep — the call a
// model makes to follow up a files_with_matches search — does not come back as
// `1` on one host and `file:1` on the other.
func TestSearchToolManager_GrepPrefixesASingleFileEitherWay(t *testing.T) { //nolint:paralleltest // t.Setenv
	branches := []struct {
		setup func(*testing.T)
		name  string
	}{
		{standInRipgrep, "with ripgrep"},
		{func(t *testing.T) { _ = searchPath(t, "grep") }, "without ripgrep"},
	}
	for _, branch := range branches { //nolint:paralleltest // PATH is process-wide
		for _, mode := range []string{outputModeContent, outputModeCount} {
			t.Run(branch.name+"/"+mode, func(t *testing.T) {
				workingDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(workingDir, "one.txt"), []byte(needleAtHome+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				m := searchIn(t, workingDir)
				branch.setup(t)

				res, err := m.CallTool(context.Background(), "Grep", message.ToolArgumentValues{
					argPattern:    "needle",
					reviewArgPath: filepath.Join(workingDir, "one.txt"),
					argOutputMode: mode,
				})
				if err != nil || res.Error != "" {
					t.Fatalf("Grep failed: err=%v result=%q", err, res.Error)
				}
				if !strings.Contains(res.Text, "one.txt:") {
					t.Errorf("%s dropped the path prefix on a single file: %q", mode, res.Text)
				}
			})
		}
	}
}

// The rg-only arguments are otherwise exercised by nothing: the fallback
// refuses them, and standInRipgrep replaces PATH even on a host that has real
// ripgrep. This pins ripgrepFlags' side of `type` — that it reaches the tool as
// --type rather than being dropped the way the fallback used to drop it.
// `multiline` has no honest stand-in, so it stays covered only by its refusal.
func TestSearchToolManager_RipgrepBranchPassesType(t *testing.T) { //nolint:paralleltest // t.Setenv
	workingDir := t.TempDir()
	for _, name := range []string{"match.go", "skip.txt"} {
		if err := os.WriteFile(filepath.Join(workingDir, name), []byte("needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := searchIn(t, workingDir)
	standInRipgrep(t)

	res, err := m.CallTool(context.Background(), "Grep", message.ToolArgumentValues{
		argPattern:    "needle",
		argType:       "go",
		argOutputMode: outputModeFilesWithMatches,
	})
	if err != nil || res.Error != "" {
		t.Fatalf("Grep failed: err=%v result=%q", err, res.Error)
	}
	if !strings.Contains(res.Text, "match.go") {
		t.Errorf("type excluded the file it should have matched: %q", res.Text)
	}
	if strings.Contains(res.Text, "skip.txt") {
		t.Errorf("type was dropped on the way to rg: %q", res.Text)
	}
}
