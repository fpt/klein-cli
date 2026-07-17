package tool

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

const allowedFile = "allowed.go"

// rangeValidator allows lines 10-20 of allowed.go only.
func rangeValidator(path string, line, endLine int) error {
	if path != allowedFile {
		return fmt.Errorf("file %q is not part of the diff", path)
	}
	if line < 10 || endLine > 20 {
		return fmt.Errorf("lines %d-%d of %q are outside the diff", line, endLine, path)
	}
	return nil
}

func callReview(t *testing.T, m *ReviewToolManager, name string, args message.ToolArgumentValues) message.ToolResult {
	t.Helper()
	res, err := m.CallTool(context.Background(), message.ToolName(name), args)
	if err != nil {
		t.Fatalf("%s returned transport error: %v", name, err)
	}
	return res
}

// expectOK fails the test when the tool result is an error.
func expectOK(t *testing.T, res message.ToolResult, what string) {
	t.Helper()
	if res.Error != "" {
		t.Fatalf("%s failed: %s", what, res.Error)
	}
}

// expectErr fails the test unless the tool result is an error containing substr.
func expectErr(t *testing.T, res message.ToolResult, substr, what string) {
	t.Helper()
	if res.Error == "" || !strings.Contains(res.Error, substr) {
		t.Errorf("%s: expected error containing %q, got %q", what, substr, res.Error)
	}
}

func inline(path string, line int, extra message.ToolArgumentValues) message.ToolArgumentValues {
	args := message.ToolArgumentValues{"path": path, "line": line, "comment": "finding", "severity": "minor"}
	maps.Copy(args, extra)
	return args
}

func TestReviewToolManager_Flow(t *testing.T) {
	t.Parallel()
	m := NewReviewToolManager(rangeValidator, nil)

	expectErr(t, callReview(t, m, "FinalizeReview", nil),
		"no summary", "FinalizeReview before summary")

	expectOK(t, callReview(t, m, "AddInlineReview",
		inline(allowedFile, 12, message.ToolArgumentValues{"severity": "major"})),
		"valid AddInlineReview")
	expectErr(t, callReview(t, m, "AddInlineReview", inline(allowedFile, 12, nil)),
		"already exists", "duplicate comment")
	expectErr(t, callReview(t, m, "AddInlineReview", inline(allowedFile, 5, nil)),
		"outside the diff", "out-of-range line")
	expectErr(t, callReview(t, m, "AddInlineReview", inline("other.go", 12, nil)),
		"not part of the diff", "unknown file")
	expectErr(t, callReview(t, m, "AddInlineReview",
		inline(allowedFile, 14, message.ToolArgumentValues{"severity": "blocker"})),
		"invalid severity", "bad severity")
	expectErr(t, callReview(t, m, "AddInlineReview",
		message.ToolArgumentValues{"path": allowedFile, "line": 14, "comment": "x"}),
		"severity is required", "missing severity")

	// Multi-line with swapped bounds is normalized.
	expectOK(t, callReview(t, m, "AddInlineReview",
		inline(allowedFile, 18, message.ToolArgumentValues{"end_line": 15})),
		"swapped-bounds comment")

	expectErr(t, callReview(t, m, "AddSummaryReview",
		message.ToolArgumentValues{"summary": "s", "verdict": "block"}),
		"invalid verdict", "bad verdict")
	expectOK(t, callReview(t, m, "AddSummaryReview",
		message.ToolArgumentValues{"summary": "Overall fine", "verdict": "request_changes"}),
		"AddSummaryReview")
	expectOK(t, callReview(t, m, "FinalizeReview", nil), "FinalizeReview")

	// After finalize, everything is locked.
	expectErr(t, callReview(t, m, "AddInlineReview", inline(allowedFile, 19, nil)),
		"finalized", "AddInlineReview after finalize")
	expectErr(t, callReview(t, m, "AddSummaryReview", message.ToolArgumentValues{"summary": "late"}),
		"finalized", "AddSummaryReview after finalize")
	expectErr(t, callReview(t, m, "FinalizeReview", nil),
		"finalized", "double FinalizeReview")

	verifyFlowResult(t, m.Result())
}

func verifyFlowResult(t *testing.T, got ReviewResult) {
	t.Helper()
	if !got.Finalized || got.Verdict != "request_changes" || got.Summary != "Overall fine" {
		t.Errorf("result: %+v", got)
	}
	if len(got.Comments) != 2 {
		t.Fatalf("expected 2 comments, got %d: %+v", len(got.Comments), got.Comments)
	}
	if got.Comments[1].Line != 15 || got.Comments[1].EndLine != 18 {
		t.Errorf("swapped bounds not normalized: %+v", got.Comments[1])
	}
}

func TestReviewToolManager_Resolve(t *testing.T) {
	t.Parallel()
	m := NewReviewToolManager(rangeValidator, []string{"T1", "T2"})

	expectErr(t, callReview(t, m, "ResolveReviewComment", message.ToolArgumentValues{"id": "T9"}),
		"unknown comment id", "unknown id")
	expectOK(t, callReview(t, m, "ResolveReviewComment",
		message.ToolArgumentValues{"id": "T1", "note": "fixed"}), "valid resolve")
	expectErr(t, callReview(t, m, "ResolveReviewComment", message.ToolArgumentValues{"id": "T1"}),
		"already marked resolved", "double resolve")

	got := m.Result()
	if len(got.Resolved) != 1 || got.Resolved[0].ID != "T1" || got.Resolved[0].Note != "fixed" {
		t.Errorf("resolved: %+v", got.Resolved)
	}
}

func TestReviewToolManager_ResolveToolAbsentWithoutPrevious(t *testing.T) {
	t.Parallel()
	m := NewReviewToolManager(rangeValidator, nil)
	if _, ok := m.GetTools()["ResolveReviewComment"]; ok {
		t.Error("ResolveReviewComment should not be registered without previous comments")
	}
}

func TestReviewToolManager_SummaryReplacedAndDefaults(t *testing.T) {
	t.Parallel()
	m := NewReviewToolManager(rangeValidator, nil)
	expectOK(t, callReview(t, m, "AddSummaryReview", message.ToolArgumentValues{"summary": "v1"}), "first summary")
	res := callReview(t, m, "AddSummaryReview", message.ToolArgumentValues{"summary": "v2"})
	if res.Error != "" || !strings.Contains(res.Text, "replaced") {
		t.Errorf("second summary should replace: %s %s", res.Text, res.Error)
	}
	got := m.Result()
	if got.Summary != "v2" || got.Verdict != "comment" || got.Finalized {
		t.Errorf("result: %+v", got)
	}
}
