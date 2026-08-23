package agentserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/codex-sdk-go/rpc"
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

// ErrServerHungUp reports that a dialed app-server closed the connection.
//
// It earns a sentinel because over TCP a clean EOF is not the same news as a
// crashed backend. An app-server that serves one client at a time hands the
// session to whoever connects last and shuts the older socket down — rs-gallium
// does exactly this, deliberately, so that a laptop which slept and left a
// zombie connection behind cannot lock its owner out of their own box. Telling
// that user "the backend died" would be a lie about which machine has the
// problem.
var ErrServerHungUp = errors.New(
	"the app-server closed the connection: another client may have taken over the session, or the server stopped")

// tcpDialTimeout bounds the dial only. It is short because reaching a listening
// socket is fast or hopeless; it says nothing about how long a turn may take.
const tcpDialTimeout = 10 * time.Second

// tcpKeepAlivePeriod is how often the kernel probes an idle connection. This,
// not a read deadline, is how a vanished peer is noticed: see tcpTransport.
const tcpKeepAlivePeriod = 30 * time.Second

// tcpTransport speaks the same line-delimited JSON-RPC as stdioTransport, over a
// socket to an app-server that is already running elsewhere
// (`gallium app-server --listen host:port`). Same methods, same
// reverse-direction `item/tool/call` — the tools still run in *this* process,
// which is the entire point: the model can sit on a GPU box while klein's
// dynamic tools stay on the user's laptop.
//
// There is deliberately no read deadline. A turn can run for minutes with
// nothing on the wire while the remote model works, so a deadline would kill
// live turns rather than detect dead ones; liveness is TCP keepalive's job, set
// once at dial.
//
// It satisfies rpc.Transport (ReadLine/WriteLine/Close).
type tcpTransport struct {
	conn    net.Conn
	reader  *bufio.Reader
	address string

	writeMu sync.Mutex

	lostMu sync.Mutex
	lost   bool
}

// dialTCP connects to an app-server listening at address ("host:port").
//
// Unlike spawnStdio there is no environment to pass: the process on the other
// end was started by someone else and read its own configuration there.
func dialTCP(ctx context.Context, address string) (*tcpTransport, error) {
	if address == "" {
		return nil, errors.New("app-server address is empty")
	}

	dialer := net.Dialer{Timeout: tcpDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial app-server at %s: %w", address, err)
	}
	// Keepalive notices a peer that vanished without noticing a turn that is
	// merely thinking. (TCP_NODELAY is already Go's default.)
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(tcpKeepAlivePeriod)
	}

	return &tcpTransport{conn: conn, reader: bufio.NewReader(conn), address: address}, nil
}

// ReadLine reads a single JSONL line from the socket.
func (t *tcpTransport) ReadLine() (string, error) {
	// Limited, unlike the stdio path: bytes from a socket are bytes from another
	// machine, and an unbounded read would let it choose klein's memory usage.
	line, err := rpc.ReadLineLimited(t.reader, rpc.DefaultMaxMessageBytes)
	if err != nil {
		t.markLost()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%w (%s): %w", ErrServerHungUp, t.address, err)
		}
		return "", fmt.Errorf("read from app-server at %s: %w", t.address, err)
	}
	return line, nil
}

// WriteLine writes a single JSONL line to the socket.
func (t *tcpTransport) WriteLine(line string) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	if _, err := io.WriteString(t.conn, line); err != nil {
		t.markLost()
		return fmt.Errorf("write to app-server at %s: %w", t.address, err)
	}
	return nil
}

// Close closes the socket. There is no child process here, so none of the
// stdio path's shutdown dance (close stdin, wait, kill) applies — and the
// server on the other end goes on running for the next client.
func (t *tcpTransport) Close() error {
	if err := t.conn.Close(); err != nil {
		return fmt.Errorf("close connection to app-server at %s: %w", t.address, err)
	}
	return nil
}

// markLost records that this connection is finished, so a later turn can decide
// to redial rather than talk into a socket nobody is reading. Set from the read
// loop and read from RunTurn — different goroutines, hence the mutex.
func (t *tcpTransport) markLost() {
	t.lostMu.Lock()
	defer t.lostMu.Unlock()
	t.lost = true
}

// hungUp reports whether this connection has failed.
func (t *tcpTransport) hungUp() bool {
	t.lostMu.Lock()
	defer t.lostMu.Unlock()
	return t.lost
}

// envKeys names the variables in a "KEY=VAL" slice, for an error message that
// says which settings are the problem.
func envKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		keys = append(keys, key)
	}
	return keys
}
