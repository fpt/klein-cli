package memorydb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(filepath.Join(t.TempDir(), "mem.sqlite"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func call(t *testing.T, m *Manager, name string, args message.ToolArgumentValues) message.ToolResult {
	t.Helper()
	res, err := m.CallTool(context.Background(), message.ToolName(name), args)
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.Error != "" {
		t.Fatalf("CallTool(%s) tool error: %s", name, res.Error)
	}
	return res
}

func TestManagerRegistersTools(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	for _, name := range []string{"Remember", "Recall", "Revise", "Reinforce", "Forget", "MemoryHistory"} {
		if _, ok := m.GetTool(message.ToolName(name)); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
	if len(m.GetTools()) != 6 {
		t.Errorf("tool count = %d, want 6", len(m.GetTools()))
	}
}

func TestManagerRoundTrip(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)

	// Remember → Recall → Reinforce → Revise → History, driven purely through the
	// tool interface (as codex dynamic tools / ReAct would call them).
	call(t, m, "Remember", message.ToolArgumentValues{
		pContent:    "The user prefers modernc sqlite over cgo bindings",
		"kind":      "preference",
		pImportance: 0.8,
		pEntities:   "sqlite, modernc",
	})

	rec := call(t, m, "Recall", message.ToolArgumentValues{pQuery: "sqlite preference"})
	if !strings.Contains(rec.Text, "modernc sqlite") {
		t.Fatalf("recall text = %q", rec.Text)
	}
	if !strings.Contains(rec.Text, "#1") {
		t.Fatalf("recall should expose the memory id: %q", rec.Text)
	}

	// Feedback via a named signal.
	fb := call(t, m, "Reinforce", message.ToolArgumentValues{pIDs: "1", pSignal: SignalConfirmed})
	if !strings.Contains(fb.Text, "#1") {
		t.Fatalf("reinforce text = %q", fb.Text)
	}

	// Revise supersedes v1 and reports version 2.
	rev := call(t, m, "Revise", message.ToolArgumentValues{
		pID: float64(1), pContent: "The user requires modernc sqlite (no cgo)",
	})
	if !strings.Contains(rev.Text, "version 2") {
		t.Fatalf("revise text = %q", rev.Text)
	}

	hist := call(t, m, "MemoryHistory", message.ToolArgumentValues{pID: float64(1)})
	if !strings.Contains(hist.Text, "v1") || !strings.Contains(hist.Text, "v2") {
		t.Fatalf("history text = %q", hist.Text)
	}
}

func TestManagerRecallDefaultsAndForget(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	call(t, m, "Remember", message.ToolArgumentValues{pContent: "ephemeral fact about widgets"})

	// String-typed numeric args (as some models emit) are accepted.
	fb := call(t, m, "Reinforce", message.ToolArgumentValues{pIDs: "1", pSignal: SignalUsed})
	if strings.Contains(fb.Text, "Not found") {
		t.Fatalf("string id not accepted: %q", fb.Text)
	}

	call(t, m, "Forget", message.ToolArgumentValues{pID: float64(1)})
	rec := call(t, m, "Recall", message.ToolArgumentValues{pQuery: "widgets"})
	if !strings.Contains(rec.Text, "No memories") {
		t.Fatalf("forgotten memory still recalled: %q", rec.Text)
	}
}

func TestManagerValidationErrors(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)

	res, _ := m.CallTool(context.Background(), "Remember", message.ToolArgumentValues{})
	if res.Error == "" {
		t.Error("Remember without content should error")
	}
	res, _ = m.CallTool(context.Background(), "Reinforce", message.ToolArgumentValues{pIDs: "1", pSignal: "bogus"})
	if res.Error == "" {
		t.Error("Reinforce with unknown signal should error")
	}
}
