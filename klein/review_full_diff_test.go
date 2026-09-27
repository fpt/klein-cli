package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/review"
)

const (
	incrementDiff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n package x\n+var b = 1\n"
	wholePRDiff   = incrementDiff +
		"diff --git a/helper.go b/helper.go\nnew file mode 100644\n" +
		"--- /dev/null\n+++ b/helper.go\n@@ -0,0 +1,1 @@\n+package x\n"
)

func prepareFor(t *testing.T, req review.Request) preparedReview {
	t.Helper()
	dir := t.TempDir()
	input := filepath.Join(dir, "req.json")
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := prepareReviewPrompt(context.Background(),
		reviewOptions{input: input, workdir: dir}, infra.NewOSFilesystemRepository())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return p
}

// An incremental round carries the whole PR diff through to ReadFullDiff —
// including the file the increment never touched — and says so in the prompt.
func TestPrepareReviewPrompt_IncrementalOffersFullDiff(t *testing.T) {
	t.Parallel()

	p := prepareFor(t, review.Request{Title: "t", Diff: incrementDiff, FullDiff: wholePRDiff, Mode: "incremental"})
	if !p.hasFullDiff {
		t.Fatal("full diff not carried through for an incremental round")
	}
	var paths []string
	for _, f := range p.fullFiles {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "a.go,helper.go" {
		t.Errorf("full diff files = %v, want a.go and helper.go", paths)
	}
	if !strings.Contains(p.prompt, "ReadFullDiff") {
		t.Error("prompt does not mention ReadFullDiff")
	}
}

func TestPrepareReviewPrompt_FullRoundHasNoFullDiffTool(t *testing.T) {
	t.Parallel()

	p := prepareFor(t, review.Request{Title: "t", Diff: wholePRDiff})
	if p.hasFullDiff {
		t.Error("a full round offered ReadFullDiff")
	}
	if strings.Contains(p.prompt, "ReadFullDiff") {
		t.Error("a full round's prompt mentions ReadFullDiff")
	}
}

// full_diff alone does not make a round incremental: a full round already has
// the whole diff in its prompt, and an unadvertised tool is noise.
func TestPrepareReviewPrompt_FullModeIgnoresFullDiff(t *testing.T) {
	t.Parallel()

	p := prepareFor(t, review.Request{Title: "t", Diff: wholePRDiff, FullDiff: wholePRDiff})
	if p.hasFullDiff {
		t.Error("a full-mode request carrying full_diff offered ReadFullDiff")
	}
}
