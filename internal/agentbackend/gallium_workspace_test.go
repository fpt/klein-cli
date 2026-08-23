package agentbackend

// An integration test for the arrangement this backend exists to serve: a real
// rs-gallium reached over the network, which lends no filesystem or shell tools
// of its own, driven by klein, which supplies them.
//
// Everything in workspace_test.go tests klein's half against klein's beliefs —
// the names it offers, the schema it renders, the approval it asks for. None of
// that says the server will resolve a call to klein's tool, and that is the half
// that cannot be tested without the pair: gallium has to have matched the name
// klein registered and dispatched back over the connection to it.
//
// Skipped unless GALLIUM_BIN points at a gallium build, matching the convention
// in pkg/agentserver:
//
//	GALLIUM_BIN=/path/to/gallium go test ./internal/agentbackend/ -run Gallium
//
// It needs no model: gallium's `scripted` engine replays a JSON script, so a
// whole turn including a tool call finishes in milliseconds.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fpt/klein-cli/pkg/agentserver"
)

// galliumBin returns the gallium to test against, or skips. Deliberately only
// GALLIUM_BIN, with no fall back to `gallium` on PATH: an installed gallium is
// usually whatever was last built, and running these against a stale one turns
// "you did not configure this" into a confusing assertion failure.
func galliumBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("GALLIUM_BIN")
	if bin == "" {
		t.Skip("set GALLIUM_BIN to a gallium build to run the integration tests")
	}
	//nolint:gosec // G703: GALLIUM_BIN is the tester's own path, and checking it is the point
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("GALLIUM_BIN=%q is not usable: %v", bin, err)
	}
	return bin
}

// jsonString renders s as a JSON string literal, so a temp-directory path can be
// interpolated into a script without a backslash or a quote breaking it.
func jsonString(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(out)
}

// writeFixture stores content in its own temp directory and returns the path.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// listeningGallium starts `gallium app-server` serving on a loopback address,
// replaying script, and returns the address once it accepts connections.
//
// The port is chosen by binding one and letting it go, which is racy in
// principle — gallium does not report which port it bound, so the choice is this
// or parsing its output — but a lost race fails loudly (gallium exits saying it
// cannot bind) rather than quietly.
//
// --config, pointing at a file that configures nothing, is not decoration:
// without it gallium falls back to ~/.config/gallium/config.toml, and a
// developer whose own config sets `[agent] listen` gets a server listening
// somewhere else entirely. Observed exactly that.
func listeningGallium(t *testing.T, script string) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	emptyConfig := writeFixture(t, "gallium.toml", "[agent]\n")
	//nolint:gosec // G204: the binary is GALLIUM_BIN, named by whoever ran the test
	cmd := exec.Command(galliumBin(t), appServerSubcommand, "--config", emptyConfig)
	cmd.Env = append(os.Environ(),
		"GALLIUM_LISTEN="+address,
		"INFERENCE_ENGINE=scripted",
		"MODEL_PATH="+script,
	)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a listening gallium: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(20 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			return address
		}
		if time.Now().After(deadline) {
			t.Fatalf("gallium never listened on %s: %s", address, stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockedBuffer collects the child's stderr, which it writes from its own
// goroutine while the test reads it from another.
type lockedBuffer struct {
	buf strings.Builder
	mu  sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p) //nolint:wrapcheck // strings.Builder never returns one
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// recordedHost is klein's tools plus a note of which ones the backend actually
// called. The note is what makes the assertions specific: a file appearing on
// disk says something wrote it, and only this says klein did.
type recordedHost struct {
	inner  agentserver.DynamicTools
	called []string
	mu     sync.Mutex
}

func (r *recordedHost) Specs() []agentserver.ToolSpec { return r.inner.Specs() }

func (r *recordedHost) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	r.mu.Lock()
	r.called = append(r.called, name)
	r.mu.Unlock()
	return r.inner.Call(ctx, name, args) //nolint:wrapcheck // a recorder, not a layer
}

func (r *recordedHost) served() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.called)
}

// dialGalliumWithKleinsTools starts a listening gallium and dials it with
// klein's workspace tools registered — the whole arrangement, both halves real.
func dialGalliumWithKleinsTools(t *testing.T, script, workDir string) (*agentserver.Runner, *recordedHost) {
	t.Helper()

	settings := dialedSettings()
	// Unset, on purpose: a dialed server is exactly the case where klein's tools
	// are supposed to be on without anyone having said so.
	if !wantsWorkspaceTools(settings) {
		t.Fatal("a dialed backend no longer offers klein's workspace tools by default")
	}
	host := &recordedHost{inner: newToolHost(newWorkspaceTools(settings, workDir))}

	runner, err := agentserver.NewRunner(context.Background(), agentserver.Config{
		Address:        listeningGallium(t, script),
		Dialect:        agentserver.DialectGeneric,
		Cwd:            workDir,
		ApprovalPolicy: agentserver.ApprovalNever,
		Tools:          host,
	})
	if err != nil {
		t.Fatalf("dialing gallium: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	return runner, host
}

// The claim under test: a turn on a server that has no tools of its own writes a
// file *here*, through klein's Write, into klein's working directory. That is
// the whole arrangement — the model reasons wherever it runs, and the file lands
// where the user is.
func TestGallium_OverTCP_WritesThroughKleinsTools(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	target := filepath.Join(workDir, "written-by-klein.txt")
	script := writeFixture(t, "script.json", fmt.Sprintf(`{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "Write", "arguments": {
	        "file_path": %s, "content": "the hands are local\n" } }] },
	    { "text": "I wrote the file." }
	  ]
	}`, jsonString(target)))

	runner, host := dialGalliumWithKleinsTools(t, script, workDir)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, text, err := runner.RunTurn(ctx, "", "write the file", "", nil)
	if err != nil {
		t.Fatalf("running a turn: %v", err)
	}
	if !strings.Contains(text, "I wrote the file") {
		t.Errorf("turn text: got %q", text)
	}

	// klein served the call — the server dispatched to the name klein registered
	// rather than to anything of its own.
	if served := host.served(); !slices.Contains(served, "Write") {
		t.Fatalf("klein was never asked to write; it served %v", served)
	}
	onDisk, err := os.ReadFile(target) //nolint:gosec // a path this test built from t.TempDir()
	if err != nil {
		t.Fatalf("the file the turn reported writing is not there: %v", err)
	}
	if !strings.Contains(string(onDisk), "the hands are local") {
		t.Errorf("file contains %q", onDisk)
	}
}

// Bash is the other half of "hands": a command has to run on klein's machine, in
// klein's working directory, and its output has to come back over the same
// connection. Read is in the same script because a turn that can write but not
// read back is not a workspace.
func TestGallium_OverTCP_RunsCommandsAndReadsThroughKleinsTools(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	target := filepath.Join(workDir, "greeting.txt")
	if err := os.WriteFile(target, []byte("hello from the client side\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := writeFixture(t, "script.json", fmt.Sprintf(`{
	  "steps": [
	    { "toolCalls": [{ "id": "c1", "name": "Read", "arguments": { "file_path": %s } }] },
	    { "toolCalls": [{ "id": "c2", "name": "Bash", "arguments": {
	        "command": "printf 'ran here' > ran.txt" } }] },
	    { "text": "I read it and ran a command." }
	  ]
	}`, jsonString(target)))

	runner, host := dialGalliumWithKleinsTools(t, script, workDir)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, _, err := runner.RunTurn(ctx, "", "read it, then run something", "", nil); err != nil {
		t.Fatalf("running a turn: %v", err)
	}

	served := host.served()
	for _, want := range []string{toolRead, toolBash} {
		if !slices.Contains(served, want) {
			t.Errorf("klein did not serve %s; it served %v", want, served)
		}
	}

	// The command ran with klein's working directory as its cwd — the relative
	// path in the script resolved here, not wherever the backend thinks it is.
	ran, err := os.ReadFile(filepath.Join(workDir, "ran.txt")) //nolint:gosec // built from t.TempDir()
	if err != nil {
		t.Fatalf("the command did not run in klein's working directory: %v", err)
	}
	if string(ran) != "ran here" {
		t.Errorf("command output file contains %q", ran)
	}
}
