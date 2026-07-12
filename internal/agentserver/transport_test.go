package agentserver

import (
	"context"
	"io"
	"testing"
)

// TestSpawnStdio_PassesEnvToChild is the crux of the kessel config support: the
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
// or kessel with no config) leaves the child's environment untouched.
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
