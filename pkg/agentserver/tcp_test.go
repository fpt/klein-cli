package agentserver

// Tests for the dialed transport: an app-server reached over TCP rather than
// spawned. They live together in one file, across the transport/runner split the
// rest of the package follows, because they share one fake — a real listener
// speaking real JSONL on a real socket. That is the point of them: the protocol
// is unchanged, so what is worth testing here is the byte stream and the things
// only a socket can do (hang up, be displaced, be reconnected to), none of which
// a Transport stub would exercise.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Protocol names and fixtures repeated across the tests below.
const (
	methodThreadStart = "thread/start"
	methodTurnStart   = "turn/start"
	firstThread       = "thread_1"
	pingTool          = "KleinPing"
	testAddress       = "10.0.0.2:4711" // never dialed: only ever configured
	spawnedBinary     = "gallium"       // never run: only ever configured
	appServerArg      = "app-server"    // the conventional subcommand
)

// jsonlServer is an app-server on a real socket: enough of the protocol to
// initialize, hand out threads, and finish a turn.
//
// It copies the one behavior that makes TCP different from stdio — one client at
// a time, newest wins, older socket closed — so a test can displace a connection
// the way a second klein would. Thread ids are per connection and restart at
// thread_1, as rs-gallium's do, which is what makes a stale id from a previous
// connection dangerous rather than merely unknown.
type jsonlServer struct {
	ln      net.Listener
	current net.Conn

	// toolCall, when set, is sent to the client mid-turn as an item/tool/call —
	// the one direction of this protocol that runs backwards.
	toolCall *toolCallScript

	seen []map[string]any

	mu        sync.Mutex
	closeOnce sync.Once
}

// toolCallScript is a tool call the fake makes during a turn, and the place its
// answer lands.
type toolCallScript struct {
	answered  chan string
	arguments map[string]any
	tool      string
}

func newJSONLServer(t *testing.T) *jsonlServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &jsonlServer{ln: ln}
	go srv.accept()
	t.Cleanup(srv.Close)
	return srv
}

func (s *jsonlServer) addr() string { return s.ln.Addr().String() }

func (s *jsonlServer) Close() {
	s.closeOnce.Do(func() {
		_ = s.ln.Close()
		s.mu.Lock()
		conn := s.current
		s.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
	})
}

// accept serves one client at a time: a new connection displaces the previous
// one, which sees a clean EOF.
func (s *jsonlServer) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		previous := s.current
		s.current = conn
		s.mu.Unlock()
		if previous != nil {
			_ = previous.Close()
		}
		go (&session{srv: s, conn: conn, replies: make(chan json.RawMessage, 1)}).serve()
	}
}

func (s *jsonlServer) record(method string, params map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, map[string]any{"method": method, "params": params})
}

// requests returns every request the server has seen, across all connections.
func (s *jsonlServer) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.seen...)
}

// methods returns just the method names, in order.
func (s *jsonlServer) methods() []string {
	out := []string{}
	for _, req := range s.requests() {
		out = append(out, req["method"].(string))
	}
	return out
}

// session is one connection's handler, with its own thread counter.
type session struct {
	srv     *jsonlServer
	conn    net.Conn
	replies chan json.RawMessage

	writeMu sync.Mutex
	threads int
}

// countMethod returns how many times the server was asked for a method.
func (s *jsonlServer) countMethod(method string) int {
	n := 0
	for _, seen := range s.methods() {
		if seen == method {
			n++
		}
	}
	return n
}

func (s *session) serve() {
	defer func() { _ = s.conn.Close() }()

	reader := bufio.NewReader(s.conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		var msg struct {
			Params map[string]any  `json:"params"`
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if msg.Method == "" {
			// An answer to something this server asked the client for.
			select {
			case s.replies <- msg.Result:
			default:
			}
			continue
		}
		s.srv.record(msg.Method, msg.Params)
		if len(msg.ID) == 0 {
			continue // a notification: nothing to answer
		}

		switch msg.Method {
		case methodThreadStart:
			s.threads++
			s.reply(msg.ID, fmt.Sprintf(`{"thread":{"id":"thread_%d"}}`, s.threads))
		case methodTurnStart:
			s.reply(msg.ID, `{"turn":{"id":"turn_1"}}`)
			threadID, _ := msg.Params[keyThreadID].(string)
			// The rest of the turn runs alongside the read loop, because a tool
			// call has to be answered by a client this loop is still reading.
			go s.runTurn(threadID)
		default:
			s.reply(msg.ID, `{}`)
		}
	}
}

// runTurn plays out a turn: optionally one call back to the client, then the
// assistant's text and the turn's completion.
func (s *session) runTurn(threadID string) {
	if script := s.srv.toolCall; script != nil {
		args, _ := json.Marshal(script.arguments)
		s.write(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":9001,"method":"item/tool/call",`+
				`"params":{"threadId":%q,"callId":"c1","tool":%q,"arguments":%s}}`,
			threadID, script.tool, args))
		select {
		case reply := <-s.replies:
			script.answered <- string(reply)
		case <-time.After(5 * time.Second):
			script.answered <- ""
		}
	}
	s.write(fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":%q,"item":{"text":"done"}}}`, threadID))
	s.write(fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":%q,"turn":{"status":"completed"}}}`, threadID))
}

func (s *session) reply(id json.RawMessage, result string) {
	s.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result))
}

func (s *session) write(line string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = io.WriteString(s.conn, line+"\n")
}

// displace connects a second client, which the server prefers — the same thing
// that happens to klein when someone opens a second one against a shared box.
func displace(t *testing.T, srv *jsonlServer) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", srv.addr())
	if err != nil {
		t.Fatalf("dialing as a second client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// waitHungUp waits for a connection to notice it was closed. The noticing
// happens in the rpc client's read loop, not at the moment the server hangs up,
// so there is nothing to synchronize on but the flag itself.
func waitHungUp(t *testing.T, link *tcpTransport) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !link.hungUp() {
		if time.Now().After(deadline) {
			t.Fatal("the displaced connection never noticed it had been closed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runnerDialing returns a Runner connected to srv over TCP.
func runnerDialing(t *testing.T, srv *jsonlServer, tools DynamicTools) *Runner {
	t.Helper()
	runner, err := NewRunner(context.Background(), Config{
		Address: srv.addr(),
		Dialect: DialectGeneric,
		Tools:   tools,
	})
	if err != nil {
		t.Fatalf("NewRunner over TCP: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	return runner
}

// The transport's own job: lines out, lines in, and a Close that ends the reads.
func TestTCPTransport_ReadsAndWritesLines(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Echo one line back, prefixed so the test can tell direction.
		line, readErr := bufio.NewReader(conn).ReadString('\n')
		if readErr != nil {
			return
		}
		_, _ = io.WriteString(conn, "echo:"+line)
	}()

	transport, err := dialTCP(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatalf("dialTCP: %v", err)
	}
	defer func() { _ = transport.Close() }()

	// WriteLine adds the delimiter the protocol is framed on.
	if err = transport.WriteLine(`{"jsonrpc":"2.0"}`); err != nil {
		t.Fatalf("WriteLine: %v", err)
	}
	line, err := transport.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if want := `echo:{"jsonrpc":"2.0"}`; line != want {
		t.Fatalf("read %q, want %q", line, want)
	}
	if transport.hungUp() {
		t.Error("a healthy connection reported itself lost")
	}
}

// A clean EOF over TCP is not the same news as a dead child process, and the
// error has to carry that difference to whoever renders it for the user.
func TestTCPTransport_EOFReportsAHangUpNotACrash(t *testing.T) {
	t.Parallel()

	srv := newJSONLServer(t)
	transport, err := dialTCP(context.Background(), srv.addr())
	if err != nil {
		t.Fatalf("dialTCP: %v", err)
	}
	defer func() { _ = transport.Close() }()

	displace(t, srv) // the server hangs up on the older client

	// Nothing reads this transport but the test, so the hang-up surfaces here,
	// in the read that was waiting for the server's next line.
	if _, err := transport.ReadLine(); err == nil {
		t.Fatal("want an error after the server hung up")
	} else {
		if !errors.Is(err, ErrServerHungUp) {
			t.Errorf("error does not identify a hang-up: %v", err)
		}
		// Still an EOF underneath, so callers matching on it keep working.
		if !errors.Is(err, io.EOF) {
			t.Errorf("error no longer wraps io.EOF: %v", err)
		}
		if !strings.Contains(err.Error(), srv.addr()) {
			t.Errorf("error does not name the address it was talking to: %v", err)
		}
	}
	if !transport.hungUp() {
		t.Error("a connection that read EOF should report itself lost")
	}
}

// Dialing something that is not there fails at once, naming the address, rather
// than hanging until a turn times out.
func TestDialTCP_UnreachableAddressFailsNamingIt(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := ln.Addr().String()
	_ = ln.Close() // nothing is listening there any more

	if _, err := dialTCP(context.Background(), address); err == nil {
		t.Fatal("want an error dialing a closed port")
	} else if !strings.Contains(err.Error(), address) {
		t.Errorf("error does not name the address: %v", err)
	}
}

// assertNamesStraySettings checks that a rejection names every launch setting
// that was configured alongside an address. Naming them is the whole point: a
// user told only that "something" cannot reach the server cannot tell which half
// of their configuration to drop.
func assertNamesStraySettings(t *testing.T, err error, cfg Config) {
	t.Helper()
	if cfg.Address == "" {
		return
	}
	for _, arg := range cfg.Args {
		if !strings.Contains(err.Error(), arg) {
			t.Errorf("error %q does not name the argument %q", err, arg)
		}
	}
	for _, kv := range cfg.Env {
		key, _, _ := strings.Cut(kv, "=")
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name the variable %q", err, key)
		}
	}
}

// The config says either "spawn this" or "dial that", never both and never
// neither, and the settings that configure a spawned child cannot reach a server
// on another machine. Each of these is caught before anything is opened.
func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"neither", Config{}, "no command configured"},
		{
			"both",
			Config{Command: spawnedBinary, Address: testAddress},
			"not both",
		},
		{
			"env with an address",
			Config{Address: testAddress, Env: []string{"MODEL_PATH=/models/x.gguf"}},
			"MODEL_PATH",
		},
		{
			"args with an address",
			Config{Address: testAddress, Args: []string{appServerArg, "--config", "x.toml"}},
			"--config",
		},
		{
			// Both stray settings in one diagnostic: a user who set two of them
			// should not have to fix one, rerun, and discover the other.
			"args and env with an address",
			Config{
				Address: testAddress,
				Args:    []string{appServerArg},
				Env:     []string{"MODEL_PATH=/models/x.gguf"},
			},
			"and",
		},
		{"an address alone", Config{Address: testAddress}, ""},
		{"a command alone", Config{Command: spawnedBinary}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			assertNamesStraySettings(t, err, tc.cfg)
		})
	}
}

// The whole feature, end to end over a socket: initialize, a thread, a turn, an
// answer.
func TestRunner_OverTCP_RunsATurn(t *testing.T) {
	t.Parallel()

	srv := newJSONLServer(t)
	runner := runnerDialing(t, srv, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	threadID, text, err := runner.RunTurn(ctx, "", "hello", "", nil)
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if threadID != firstThread {
		t.Errorf("thread id = %q, want %s", threadID, firstThread)
	}
	if text != "done" {
		t.Errorf("turn text = %q, want done", text)
	}
	if got := srv.methods(); len(got) < 3 || got[0] != "initialize" {
		t.Errorf("server saw %v, want initialize first", got)
	}
}

// The reverse direction — the server calling the client mid-turn — is the reason
// to run the agent remotely at all: the model sits on the GPU box while the
// tools it calls run here. Nothing about it changes over a socket, which is
// exactly why it is worth proving rather than assuming.
func TestRunner_OverTCP_ServesADynamicToolCall(t *testing.T) {
	t.Parallel()

	srv := newJSONLServer(t)
	srv.toolCall = &toolCallScript{
		tool:      pingTool,
		arguments: map[string]any{"note": "from the backend"},
		answered:  make(chan string, 1),
	}

	var called string
	runner := runnerDialing(t, srv, stubTools{
		called: &called,
		specs:  []ToolSpec{{Name: pingTool, Description: "records a note"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := runner.RunTurn(ctx, "", "ping yourself", "", nil); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	if called != pingTool {
		t.Errorf("the tool klein ran was %q, want %s", called, pingTool)
	}
	select {
	case answer := <-srv.toolCall.answered:
		// The server gets the tool's output back, over the same socket.
		if !strings.Contains(answer, "ran "+pingTool) {
			t.Errorf("the server got back %q", answer)
		}
	default:
		t.Fatal("the server never received an answer to its tool call")
	}

	// The tools were offered at thread/start, or the server could not have
	// asked for one.
	for _, req := range srv.requests() {
		if req["method"] == methodThreadStart {
			params, _ := req["params"].(map[string]any)
			if params["dynamicTools"] == nil {
				t.Error("thread/start carried no dynamicTools")
			}
		}
	}
}

// A thread id belongs to the connection that issued it. After a reconnect the
// server is handing out thread_1 again, to a thread klein never started, so
// carrying the old id across would send turn/start against a thread that names
// nothing — the first thing that breaks if the started map survives a redial.
func TestRunner_OverTCP_ReconnectsAndStartsAFreshThread(t *testing.T) {
	t.Parallel()

	srv := newJSONLServer(t)
	runner := runnerDialing(t, srv, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	first, _, err := runner.RunTurn(ctx, "", "hello", "", nil)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if !runner.started[first] {
		t.Fatalf("thread %q was not recorded as started", first)
	}

	link := runner.link
	displace(t, srv)
	waitHungUp(t, link)

	// The user asks for another turn on the thread they had. It has to survive:
	// a redial, a new thread, and an answer.
	if _, text, err := runner.RunTurn(ctx, first, "still there?", "", nil); err != nil {
		t.Fatalf("turn after the connection was taken over: %v", err)
	} else if text != "done" {
		t.Errorf("turn text = %q, want done", text)
	}

	// Proof it restarted the thread rather than trusting the id it held: one
	// thread/start per connection. Asserting on the ids themselves would prove
	// nothing — both connections number from thread_1, which is precisely why a
	// remembered id is dangerous rather than merely unknown.
	if starts := srv.countMethod(methodThreadStart); starts != 2 {
		t.Errorf("%s count = %d, want 2 (one per connection)", methodThreadStart, starts)
	}
	if turns := srv.countMethod(methodTurnStart); turns != 2 {
		t.Errorf("%s count = %d, want 2", methodTurnStart, turns)
	}
	// And the redial really happened: a new connection, not the dead one.
	if runner.link == link {
		t.Error("the runner is still holding the connection that hung up")
	}
}

// A spawned server has no link to lose, and must not be dragged into any of the
// reconnect machinery: a dead child process is simply dead.
func TestEnsureConnected_IsANoOpForASpawnedServer(t *testing.T) {
	t.Parallel()

	// A nil client makes the check load-bearing: reconnecting would panic.
	r := &Runner{cfg: Config{Command: spawnedBinary}}
	if err := r.ensureConnected(context.Background()); err != nil {
		t.Fatalf("ensureConnected on a spawned backend: %v", err)
	}
}

// A reconnect that fails must leave the runner able to try again. The window
// this covers is real — a server rebooting, a tunnel not yet back up — and the
// wrong answer is not "an error" but the state left behind it: a runner holding
// no client that the next turn calls through anyway.
func TestRunner_OverTCP_FailedReconnectStaysRetryable(t *testing.T) {
	t.Parallel()

	srv := newJSONLServer(t)
	runner := runnerDialing(t, srv, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	threadID, _, err := runner.RunTurn(ctx, "", "hello", "", nil)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}

	link := runner.link
	srv.Close() // the server goes away entirely: nothing left to dial
	waitHungUp(t, link)

	// Both turns must fail the same way. The second is the point: it goes
	// through the redial again rather than reaching a client that is not there.
	for attempt := 1; attempt <= 2; attempt++ {
		if _, _, err := runner.RunTurn(ctx, threadID, "anyone home?", "", nil); err == nil {
			t.Fatalf("attempt %d: want an error while the server is down", attempt)
		} else if !strings.Contains(err.Error(), "reconnecting") {
			t.Errorf("attempt %d failed for the wrong reason: %v", attempt, err)
		}
	}
}
