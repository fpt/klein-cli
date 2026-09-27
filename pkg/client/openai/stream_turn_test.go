package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// sseServer answers every POST /responses with the given SSE events — and
// only SSE, whatever the request's `stream` says, as gallium's responses-api
// does — counting the requests it serves.
func sseServer(t *testing.T, events ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// newTestClient points a client at baseURL without going through the
// environment, so the tests can run in parallel.
func newTestClient(t *testing.T, baseURL string) *OpenAIClient {
	t.Helper()
	client := openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(baseURL))
	return &OpenAIClient{OpenAICore: &OpenAICore{
		client: &client, model: ModelLuna, reasoningEffort: defaultReasoningEffort,
	}}
}

// textDelta is one response.output_text.delta event on message msg_1.
func textDelta(seq int, delta string) string {
	return fmt.Sprintf(`{"type":"response.output_text.delta","sequence_number":%d,"item_id":"msg_1",`+
		`"output_index":0,"content_index":0,"delta":%q,"logprobs":[]}`, seq, delta)
}

const usageJSON = `"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,` +
	`"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`

func completedEvent(output string) string {
	return `{"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","object":"response",` +
		`"created_at":1,"status":"completed","model":"m","output":[` + output + `],` + usageJSON + `}}`
}

// A tool-calling turn is one request: the tool call comes from the
// response.completed event, not from a second, non-streaming request.
func TestChatWithToolChoice_ToolCallFromCompletedEvent(t *testing.T) {
	t.Parallel()

	srv, calls := sseServer(t,
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1",`+
			`"output_index":0,"delta":"{\"file_path\":"}`,
		completedEvent(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read",`+
			`"arguments":"{\"file_path\":\"a.go\"}","status":"completed"}`),
	)
	c := newTestClient(t, srv.URL)

	got, err := c.ChatWithToolChoice(context.Background(),
		[]message.Message{message.NewChatMessage(message.MessageTypeUser, "read a.go")},
		domain.NewToolChoiceAuto(), false, nil)
	if err != nil {
		t.Fatalf("ChatWithToolChoice: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d requests for one turn, want 1", n)
	}
	tc, ok := got.(*message.ToolCallMessage)
	if !ok {
		t.Fatalf("got %T, want a tool call", got)
	}
	if tc.ToolName() != "Read" || tc.ID() != "call_1" || tc.ToolArguments()["file_path"] != "a.go" {
		t.Errorf("tool call = %s %s %v", tc.ToolName(), tc.ID(), tc.ToolArguments())
	}
	if u, _ := c.LastTokenUsage(); u.TotalTokens != 18 {
		t.Errorf("usage not taken from the completed event: %+v", u)
	}
}

func TestChatWithToolChoice_TextFromStream(t *testing.T) {
	t.Parallel()

	srv, calls := sseServer(t,
		textDelta(1, "Hel"),
		textDelta(2, "lo"),
		completedEvent(`{"type":"message","id":"msg_1","role":"assistant","status":"completed",`+
			`"content":[{"type":"output_text","text":"Hello","annotations":[]}]}`),
	)
	c := newTestClient(t, srv.URL)

	got, err := c.ChatWithToolChoice(context.Background(),
		[]message.Message{message.NewChatMessage(message.MessageTypeUser, "hi")},
		domain.NewToolChoiceAuto(), false, nil)
	if err != nil {
		t.Fatalf("ChatWithToolChoice: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d requests for one turn, want 1", n)
	}
	if got.Content() != "Hello" {
		t.Errorf("text = %q, want Hello", got.Content())
	}
}

// A stream that stops without a terminal event is an error, not a cue to ask
// again — asking again is the double generation this path no longer does.
func TestChatWithToolChoice_StreamWithoutCompletedIsError(t *testing.T) {
	t.Parallel()

	srv, calls := sseServer(t,
		textDelta(1, "Hel"),
	)
	c := newTestClient(t, srv.URL)

	_, err := c.ChatWithToolChoice(context.Background(),
		[]message.Message{message.NewChatMessage(message.MessageTypeUser, "hi")},
		domain.NewToolChoiceAuto(), false, nil)
	if err == nil || !strings.Contains(err.Error(), "without a completed response") {
		t.Errorf("err = %v, want a missing-completion error", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d requests, want 1", n)
	}
}

func TestChatWithToolChoice_FailedEventIsError(t *testing.T) {
	t.Parallel()

	srv, _ := sseServer(t,
		`{"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,`+
			`"status":"failed","model":"m","output":[],"error":{"code":"server_error","message":"boom"}}}`,
	)
	c := newTestClient(t, srv.URL)

	_, err := c.ChatWithToolChoice(context.Background(),
		[]message.Message{message.NewChatMessage(message.MessageTypeUser, "hi")},
		domain.NewToolChoiceAuto(), false, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the failure message", err)
	}
}
