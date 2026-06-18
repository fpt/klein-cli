package react

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/state"
	"github.com/fpt/klein-cli/pkg/message"
)

// TestParallelToolCallBatch_TaskRunsConcurrently verifies that a batch of
// Task calls is executed by goroutines rather than serially. We use the
// "maximum concurrent observers" trick: each tool handler increments a
// counter on entry and decrements on exit; the maximum observed value is
// the actual concurrency. With N=4 calls each sleeping 80ms, sequential
// execution would observe a max of 1 and take ~320ms; parallel should
// observe 4 and take ~100ms.
func TestParallelToolCallBatch_TaskRunsConcurrently(t *testing.T) {
	const numCalls = 4
	const sleepDur = 80 * time.Millisecond

	var concurrent int32
	var maxConcurrent int32

	tools := map[message.ToolName]message.Tool{
		"Task": &stubTool{name: "Task", desc: "stub Task"},
	}

	tm := &mockToolManager{
		getToolsFunc: func() map[message.ToolName]message.Tool { return tools },
		callToolFunc: func(ctx context.Context, _ message.ToolName, _ message.ToolArgumentValues) (message.ToolResult, error) {
			c := atomic.AddInt32(&concurrent, 1)
			for {
				m := atomic.LoadInt32(&maxConcurrent)
				if c <= m || atomic.CompareAndSwapInt32(&maxConcurrent, m, c) {
					break
				}
			}
			time.Sleep(sleepDur)
			atomic.AddInt32(&concurrent, -1)
			return message.ToolResult{Text: "ok"}, nil
		},
	}

	// Pre-canned LLM that returns a batch of N Task calls on first turn,
	// then a final assistant message on the second turn.
	turn := 0
	llm := &mockLLM{
		chatWithToolChoiceFunc: func(_ context.Context, _ []message.Message, _ domain.ToolChoice) (message.Message, error) {
			turn++
			if turn == 1 {
				calls := make([]*message.ToolCallMessage, numCalls)
				for i := range calls {
					calls[i] = message.NewToolCallMessage("Task", message.ToolArgumentValues{
						"subagent_type": "repo-searcher",
						"prompt":        "search " + string(rune('A'+i)),
					})
				}
				return message.NewToolCallBatch(calls), nil
			}
			return message.NewChatMessage(message.MessageTypeAssistant, "done"), nil
		},
	}

	r, _ := NewReAct(llm, tm, state.NewMessageState(), noopSituation{}, 5)
	defer r.Close()

	start := time.Now()
	resp, err := r.Run(context.Background(), "search docs")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if resp == nil || resp.Content() != "done" {
		t.Errorf("unexpected response: %#v", resp)
	}

	gotMax := atomic.LoadInt32(&maxConcurrent)
	if gotMax < int32(numCalls) {
		t.Errorf("maxConcurrent: got %d, want >= %d (batch did not run in parallel)", gotMax, numCalls)
	}

	// Allow generous slack — CI machines can be slow. The point is that
	// parallel execution is < sum-of-sleeps.
	maxAllowed := sleepDur*time.Duration(numCalls) - 50*time.Millisecond
	if elapsed >= maxAllowed {
		t.Errorf("elapsed %v >= %v — batch appears to have run sequentially", elapsed, maxAllowed)
	}
}

// TestParallelToolCallBatch_MixedBatchRunsSequentially confirms that a batch
// containing a non-sub-agent tool falls back to sequential execution (to
// avoid races on shared state like the filesystem or todo list).
func TestParallelToolCallBatch_MixedBatchRunsSequentially(t *testing.T) {
	var concurrent int32
	var maxConcurrent int32

	tools := map[message.ToolName]message.Tool{
		"Task": &stubTool{name: "Task"},
		"Read": &stubTool{name: "Read"},
	}
	tm := &mockToolManager{
		getToolsFunc: func() map[message.ToolName]message.Tool { return tools },
		callToolFunc: func(ctx context.Context, _ message.ToolName, _ message.ToolArgumentValues) (message.ToolResult, error) {
			c := atomic.AddInt32(&concurrent, 1)
			for {
				m := atomic.LoadInt32(&maxConcurrent)
				if c <= m || atomic.CompareAndSwapInt32(&maxConcurrent, m, c) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&concurrent, -1)
			return message.ToolResult{Text: "ok"}, nil
		},
	}

	turn := 0
	llm := &mockLLM{
		chatWithToolChoiceFunc: func(_ context.Context, _ []message.Message, _ domain.ToolChoice) (message.Message, error) {
			turn++
			if turn == 1 {
				calls := []*message.ToolCallMessage{
					message.NewToolCallMessage("Task", message.ToolArgumentValues{"subagent_type": "x", "prompt": "p"}),
					message.NewToolCallMessage("Read", message.ToolArgumentValues{"file_path": "/etc/hosts"}),
				}
				return message.NewToolCallBatch(calls), nil
			}
			return message.NewChatMessage(message.MessageTypeAssistant, "done"), nil
		},
	}

	r, _ := NewReAct(llm, tm, state.NewMessageState(), noopSituation{}, 5)
	defer r.Close()
	if _, err := r.Run(context.Background(), "test"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := atomic.LoadInt32(&maxConcurrent); got > 1 {
		t.Errorf("maxConcurrent: got %d, want 1 (mixed batch should be sequential)", got)
	}
}

// TestSkipApprovalBypassesWriteApproval verifies that when SetSkipApproval(true)
// is called, an otherwise approval-requiring tool (Write) runs without
// returning ErrWaitingForApproval. Mirrors the behaviour used for background
// subagents.
func TestSkipApprovalBypassesWriteApproval(t *testing.T) {
	called := false
	tools := map[message.ToolName]message.Tool{
		"Write": &stubTool{name: "Write"},
	}
	tm := &mockToolManager{
		getToolsFunc: func() map[message.ToolName]message.Tool { return tools },
		callToolFunc: func(ctx context.Context, _ message.ToolName, _ message.ToolArgumentValues) (message.ToolResult, error) {
			called = true
			return message.ToolResult{Text: "wrote"}, nil
		},
	}

	turn := 0
	llm := &mockLLM{
		chatWithToolChoiceFunc: func(_ context.Context, _ []message.Message, _ domain.ToolChoice) (message.Message, error) {
			turn++
			if turn == 1 {
				return message.NewToolCallMessage("Write", message.ToolArgumentValues{"path": "x", "content": "y"}), nil
			}
			return message.NewChatMessage(message.MessageTypeAssistant, "done"), nil
		},
	}

	r, _ := NewReAct(llm, tm, state.NewMessageState(), noopSituation{}, 5)
	r.SetSkipApproval(true)
	defer r.Close()

	if _, err := r.Run(context.Background(), "write a file"); err != nil {
		t.Fatalf("expected Run to succeed with skipApproval=true, got: %v", err)
	}
	if !called {
		t.Error("tool handler was never invoked")
	}
}

// stubTool is a minimal message.Tool used by these tests.
type stubTool struct {
	name string
	desc string
}

func (s *stubTool) RawName() message.ToolName            { return message.ToolName(s.name) }
func (s *stubTool) Name() message.ToolName               { return message.ToolName(s.name) }
func (s *stubTool) Description() message.ToolDescription { return message.ToolDescription(s.desc) }
func (s *stubTool) Arguments() []message.ToolArgument    { return nil }
func (s *stubTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		return message.ToolResult{Text: "stub"}, nil
	}
}

// noopSituation satisfies domain.Situation without adding any context.
type noopSituation struct{}

func (noopSituation) InjectMessage(_ domain.State, _ int, _ int) {}
