package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/tool/memorydb"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// newMemoryAgent builds an Agent wired with a memorydb manager, plus the manager
// for seeding. Mirrors how serve/claw wire long-term memory.
func newMemoryAgent(t *testing.T) (*Agent, *memorydb.Manager) {
	t.Helper()
	mgr, err := memorydb.NewManager(filepath.Join(t.TempDir(), "mem.sqlite"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })

	a, cleanup, err := NewAgentWithOptions(context.Background(), AgentOptions{
		Settings:          config.GetDefaultSettings(),
		WorkingDir:        t.TempDir(),
		MCPToolManagers:   map[string]domain.ToolManager{"memorydb": mgr},
		Logger:            pkgLogger.NewLogger(pkgLogger.LogLevelError),
		Out:               io.Discard,
		FsRepo:            infra.NewOSFilesystemRepository(),
		IsInteractiveMode: false,
		// Inject a stub so the agent constructs without a provider API key.
		LLMClient: &stubLLM{},
	})
	if err != nil {
		t.Fatalf("NewAgentWithOptions: %v", err)
	}
	t.Cleanup(cleanup)
	return a, mgr
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint:errcheck // reading from an in-memory pipe
	return buf.String()
}

func TestSlashCandidates(t *testing.T) {
	t.Parallel()
	a, _ := newMemoryAgent(t)

	got := map[string]bool{}
	for _, c := range slashCandidates(a) {
		got[c.Name] = true
	}
	for _, want := range []string{"help", "memory", cmdGoal, cmdLoop} {
		if !got[want] {
			t.Errorf("slash candidates missing /%s", want)
		}
	}

	out := captureStdout(t, func() { printSlashCandidates(a) })
	if !strings.Contains(out, "/memory") || !strings.Contains(out, "Commands") {
		t.Fatalf("printSlashCandidates output:\n%s", out)
	}
}

func TestMemoryManagerDetected(t *testing.T) {
	t.Parallel()
	a, mgr := newMemoryAgent(t)
	if a.MemoryManager() != mgr {
		t.Fatal("MemoryManager() did not return the wired manager")
	}
}

func TestMemoryCommandDisabled(t *testing.T) {
	t.Parallel()
	// An agent with no memorydb manager reports memory disabled instead of panicking.
	a, cleanup, err := NewAgentWithOptions(context.Background(), AgentOptions{
		Settings:          config.GetDefaultSettings(),
		WorkingDir:        t.TempDir(),
		MCPToolManagers:   map[string]domain.ToolManager{},
		Logger:            pkgLogger.NewLogger(pkgLogger.LogLevelError),
		Out:               io.Discard,
		FsRepo:            infra.NewOSFilesystemRepository(),
		IsInteractiveMode: false,
		// Inject a stub so the agent constructs without a provider API key.
		LLMClient: &stubLLM{},
	})
	if err != nil {
		t.Fatalf("NewAgentWithOptions: %v", err)
	}
	defer cleanup()

	out := captureStdout(t, func() { handleMemoryCommand(a, "list") })
	if !strings.Contains(out, "not enabled") {
		t.Fatalf("expected disabled message, got %q", out)
	}
}

func TestMemoryCommandFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, mgr := newMemoryAgent(t)
	store := mgr.Store()

	m1, _ := store.Remember(ctx, "The user prefers modernc sqlite", "preference", 0.8, []string{"sqlite"}, "")
	store.Remember(ctx, "The deploy uses docker compose", "fact", 0.5, nil, "") //nolint:errcheck // test seed

	// list: shows content and reference stats.
	list := captureStdout(t, func() { handleMemoryCommand(a, "list") })
	if !strings.Contains(list, "modernc sqlite") || !strings.Contains(list, "used 0×") {
		t.Fatalf("list output missing content/refs:\n%s", list)
	}

	// search: recalls and prints matches with ids.
	search := captureStdout(t, func() { handleMemoryCommand(a, "search sqlite") })
	if !strings.Contains(search, "modernc sqlite") {
		t.Fatalf("search output:\n%s", search)
	}

	// show <id>: full content + stats. The prior search recorded an access.
	show := captureStdout(t, func() { handleMemoryCommand(a, "show 1") })
	if !strings.Contains(show, "The user prefers modernc sqlite") {
		t.Fatalf("show output missing content:\n%s", show)
	}
	if !strings.Contains(show, "entities: sqlite") {
		t.Fatalf("show output missing entities:\n%s", show)
	}
	if !strings.Contains(show, "used 1×") {
		t.Fatalf("show output missing access count after search:\n%s", show)
	}

	// forget <id>: soft-deletes; subsequent list drops it.
	forget := captureStdout(t, func() { handleMemoryCommand(a, "forget 1") })
	if !strings.Contains(forget, "Forgot memory #1") {
		t.Fatalf("forget output:\n%s", forget)
	}
	after := captureStdout(t, func() { handleMemoryCommand(a, "list") })
	if strings.Contains(after, "modernc sqlite") {
		t.Fatalf("forgotten memory still listed:\n%s", after)
	}
	_ = m1
}
