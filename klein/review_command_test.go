package main

import (
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/tool"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

func mkComments(severities ...string) []tool.ReviewComment {
	out := make([]tool.ReviewComment, len(severities))
	for i, s := range severities {
		out[i] = tool.ReviewComment{Path: "a.go", Line: i + 1, Severity: s, Body: "x"}
	}
	return out
}

func severitiesOf(cs []tool.ReviewComment) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Severity
	}
	return out
}

func TestCapComments_UnderCapUntouched(t *testing.T) {
	t.Parallel()
	in := tool.ReviewResult{Summary: "s", Comments: mkComments("nits", "must")}
	got := capComments(in, 15, pkgLogger.NewLogger("error"))
	if got.Trimmed != 0 || got.ForceFullNext || len(got.Comments) != 2 {
		t.Fatalf("unexpected: trimmed=%d force=%v n=%d", got.Trimmed, got.ForceFullNext, len(got.Comments))
	}
	if strings.Contains(got.Summary, "trimmed") {
		t.Error("summary should not mention trimming when nothing trimmed")
	}
}

func TestCapComments_SortsAndTrimsLowestFirst(t *testing.T) {
	t.Parallel()
	// 2 must, 1 major, 2 minor, 1 nits; cap 3 keeps must,must,major.
	in := tool.ReviewResult{Summary: "s", Comments: mkComments("minor", "must", "nits", "major", "must", "minor")}
	got := capComments(in, 3, pkgLogger.NewLogger("error"))
	if got.Trimmed != 3 {
		t.Fatalf("trimmed = %d, want 3", got.Trimmed)
	}
	if want := []string{"must", "must", "major"}; !equalStrings(severitiesOf(got.Comments), want) {
		t.Fatalf("kept severities = %v, want %v", severitiesOf(got.Comments), want)
	}
	if got.ForceFullNext {
		t.Error("must count (2) <= cap (3): should not force full")
	}
	if !strings.Contains(got.Summary, "trimmed") {
		t.Error("summary should note trimming")
	}
}

func TestCapComments_MustOverCapForcesFull(t *testing.T) {
	t.Parallel()
	in := tool.ReviewResult{Summary: "s", Comments: mkComments("must", "must", "must", "minor")}
	got := capComments(in, 2, pkgLogger.NewLogger("error"))
	if !got.ForceFullNext {
		t.Error("must count (3) > cap (2): should force full next")
	}
	if len(got.Comments) != 2 || got.Comments[0].Severity != "must" {
		t.Errorf("kept = %v", severitiesOf(got.Comments))
	}
	if !strings.Contains(got.Summary, "re-scan the full PR") {
		t.Error("summary should explain the forced full review")
	}
}

func TestCapComments_ZeroIsUnlimited(t *testing.T) {
	t.Parallel()
	got := capComments(tool.ReviewResult{Comments: mkComments("must", "must", "must")}, 0, pkgLogger.NewLogger("error"))
	if got.Trimmed != 0 || len(got.Comments) != 3 {
		t.Errorf("cap 0 should not trim: trimmed=%d n=%d", got.Trimmed, len(got.Comments))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
