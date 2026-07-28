//go:build !windows

package agentserver

import (
	"context"
	"io"
	"os"
	"syscall"
	"testing"
)

// A terminal delivers Ctrl+C to every process in the foreground process group.
// The app-server does not survive it (codex-cli 0.144.1 dies with signal 2), so
// sharing klein's group meant the first Ctrl+C killed the backend and every
// later prompt failed with `codex turn/start EOF`.
//
// Being in a different group from klein is exactly the condition the tty driver
// checks, so that is what this asserts. It is not spot-checked by sending a real
// SIGINT: the only way to do that faithfully is to signal the test binary's own
// group, which under `go test` includes the go tool driving the run.
func TestSpawnStdio_ChildIsOutsideKleinsProcessGroup(t *testing.T) {
	t.Parallel()

	// cat blocks on stdin, so the child is still alive to be inspected.
	tr, err := spawnStdio(context.Background(), "/bin/cat", nil, nil, io.Discard)
	if err != nil {
		t.Fatalf("spawnStdio: %v", err)
	}
	defer tr.Close()

	childPgid, err := syscall.Getpgid(tr.cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid(child): %v", err)
	}
	kleinPgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("getpgid(self): %v", err)
	}

	if childPgid == kleinPgid {
		t.Fatalf("app-server shares klein's process group (%d); a terminal Ctrl+C would kill it", childPgid)
	}
}
