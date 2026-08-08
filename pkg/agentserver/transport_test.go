package agentserver

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

// TestSpawnStdio_PassesEnvToChild is the crux of the app-server config support: the
// SDK's rpc.SpawnStdio cannot set the child's environment, so klein uses its own
// transport. Spawn a real process and confirm it observes the override.
func TestSpawnStdio_PassesEnvToChild(t *testing.T) {
	// t.Setenv marks this test non-parallel (it mutates process env).
	t.Setenv("LLM_MODEL", "from-shell")

	tr, err := spawnStdio(
		context.Background(), "/bin/sh",
		[]string{"-c", `printf '%s\n' "$LLM_MODEL:$MODEL_PATH"`},
		childEnv([]string{"LLM_MODEL=gemma4:e4b", "MODEL_PATH=hf:org/repo/m.gguf"}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("spawnStdio: %v", err)
	}
	defer tr.Close()

	line, err := tr.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	// The config-derived env must reach the child and beat the ambient shell value.
	if want := "gemma4:e4b:hf:org/repo/m.gguf"; line != want {
		t.Fatalf("child observed %q, want %q", line, want)
	}
}

// TestSpawnStdio_InheritsEnvWhenNoOverrides confirms the default path (codex,
// or appserver with no config) leaves the child's environment untouched.
func TestSpawnStdio_InheritsEnvWhenNoOverrides(t *testing.T) {
	t.Setenv("KLEIN_TEST_PASSTHROUGH", "inherited")

	tr, err := spawnStdio(
		context.Background(), "/bin/sh",
		[]string{"-c", `printf '%s\n' "$KLEIN_TEST_PASSTHROUGH"`},
		childEnv(nil), // nil env → inherit
		io.Discard,
	)
	if err != nil {
		t.Fatalf("spawnStdio: %v", err)
	}
	defer tr.Close()

	line, err := tr.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "inherited" {
		t.Fatalf("child observed %q, want inherited", line)
	}
}

func TestSpawnStdio_EmptyBinary(t *testing.T) {
	t.Parallel()
	if _, err := spawnStdio(context.Background(), "", nil, nil, io.Discard); err == nil {
		t.Error("empty binary should error")
	}
}

// parseEnvKVs turns KEY=VALUE pairs into a map for assertions.
func parseEnvKVs(kvs []string) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func TestChildEnv_OverridesWinAndInherit(t *testing.T) {
	// t.Setenv marks this test non-parallel.
	t.Setenv("KLEIN_TEST_INHERITED", "keep-me")
	t.Setenv("LLM_MODEL", "from-shell")

	env := childEnv([]string{"LLM_MODEL=from-config", "MODEL_PATH=hf:x/y.gguf"})
	got := parseEnvKVs(env)

	// The config value wins over the ambient shell.
	if got["LLM_MODEL"] != "from-config" {
		t.Errorf("LLM_MODEL = %q, want from-config (config must beat the shell)", got["LLM_MODEL"])
	}
	// Config-only keys are added.
	if got["MODEL_PATH"] != "hf:x/y.gguf" {
		t.Errorf("MODEL_PATH = %q", got["MODEL_PATH"])
	}
	// Unrelated ambient vars (e.g. OPENAI_API_KEY, PATH) are still inherited.
	if got["KLEIN_TEST_INHERITED"] != "keep-me" {
		t.Errorf("ambient env not inherited: %q", got["KLEIN_TEST_INHERITED"])
	}
	// No duplicate keys in the result (exec would otherwise be ambiguous).
	keys := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
	}
	slices.Sort(keys)
	n := len(keys)
	if len(slices.Compact(keys)) != n {
		t.Error("child env contains duplicate keys")
	}
}

func TestChildEnv_NilWhenNoOverrides(t *testing.T) {
	t.Parallel()
	if env := childEnv(nil); env != nil {
		t.Errorf("no overrides should yield nil (inherit as-is), got %v", env)
	}
}
