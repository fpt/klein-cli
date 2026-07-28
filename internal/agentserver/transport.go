package agentserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// stdioCloseTimeout bounds how long Close waits for the app-server to exit
// after stdin is closed before killing it.
const stdioCloseTimeout = 5 * time.Second

// stdioTransport spawns an app-server and speaks JSONL over its stdin/stdout.
// It mirrors rpc.SpawnStdio from the codex SDK but additionally lets the caller
// set the child's environment — which the SDK's version does not expose, and
// which the appserver backend needs to pass its config-derived settings (MODEL_PATH,
// LLM_MODEL, …) to the child process.
//
// It satisfies rpc.Transport (ReadLine/WriteLine/Close).
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
}

// spawnStdio starts binary with args and returns a transport over its stdio.
// env, when non-empty, is the child's full environment (see childEnv); when
// empty the child inherits klein's environment.
func spawnStdio(
	ctx context.Context, binary string, args, env []string, stderr io.Writer,
) (*stdioTransport, error) {
	if binary == "" {
		return nil, errors.New("app-server binary path is empty")
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stderr = stderr
	if len(env) > 0 {
		cmd.Env = env
	}
	// Ctrl+C is klein's to interpret, not the backend's to die from.
	detachFromTerminalSignals(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}

	return &stdioTransport{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}, nil
}

// ReadLine reads a single JSONL line from the child's stdout.
func (t *stdioTransport) ReadLine() (string, error) {
	line, err := t.stdout.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line != "" {
			return strings.TrimRight(line, "\n"), nil
		}
		return "", err //nolint:wrapcheck // io.EOF must stay comparable for the rpc loop
	}
	return strings.TrimRight(line, "\n"), nil
}

// WriteLine writes a single JSONL line to the child's stdin.
func (t *stdioTransport) WriteLine(line string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	if _, err := io.WriteString(t.stdin, line); err != nil {
		return fmt.Errorf("write to app-server: %w", err)
	}
	return nil
}

// Close shuts the child down: close stdin, wait briefly, then kill.
func (t *stdioTransport) Close() error {
	var errs []error
	if t.stdin != nil {
		if err := t.stdin.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close stdin: %w", err))
		}
	}
	if t.cmd == nil {
		return errors.Join(errs...)
	}
	return errors.Join(append(errs, t.waitOrKill()...)...)
}

// waitOrKill waits for the child to exit, killing it after stdioCloseTimeout.
func (t *stdioTransport) waitOrKill() []error {
	var errs []error
	waitCh := make(chan error, 1)
	go func() { waitCh <- t.cmd.Wait() }()

	select {
	case err := <-waitCh:
		if err != nil {
			errs = append(errs, fmt.Errorf("wait for process: %w", err))
		}
	case <-time.After(stdioCloseTimeout):
		if t.cmd.Process != nil {
			if err := t.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				errs = append(errs, fmt.Errorf("kill process: %w", err))
			}
		}
		if err := <-waitCh; err != nil {
			errs = append(errs, fmt.Errorf("wait after kill: %w", err))
		}
	}
	return errs
}

// childEnv merges overrides onto klein's environment, producing the child's full
// env. Overrides win: a value the user configured explicitly (via the
// app-server's config) takes precedence over whatever is in the ambient shell.
// Returns nil when there is nothing to override (child inherits as-is).
func childEnv(overrides []string) []string {
	if len(overrides) == 0 {
		return nil
	}
	merged := map[string]string{}
	order := []string{}
	for _, kv := range append(os.Environ(), overrides...) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, seen := merged[k]; !seen {
			order = append(order, k)
		}
		merged[k] = v
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+merged[k])
	}
	return out
}
