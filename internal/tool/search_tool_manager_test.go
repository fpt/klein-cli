package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

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
			if got != tc.want {
				t.Errorf("resolved to %q, want %q", got, tc.want)
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
