package tool

import (
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/review"
	"github.com/fpt/klein-cli/pkg/message"
)

// fullPRDiff is a two-commit PR in miniature: helper.go was added by the
// first commit, and is exactly what an incremental round cannot see.
const fullPRDiff = `diff --git a/helper.go b/helper.go
new file mode 100644
--- /dev/null
+++ b/helper.go
@@ -0,0 +1,3 @@
+package x
+
+func writeChunk() {}
diff --git a/old.go b/new.go
rename from old.go
rename to new.go
--- a/old.go
+++ b/new.go
@@ -1,2 +1,2 @@
 package x
-var a = 1
+var a = 2
`

const readFullDiffTool = "ReadFullDiff"

func fullDiffManager(t *testing.T, maxBytes int) *ReviewToolManager {
	t.Helper()
	files, err := review.ParseUnifiedDiff(fullPRDiff)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return NewReviewToolManager(rangeValidator, nil).WithFullDiff(files, maxBytes)
}

func TestReadFullDiff_ListsFiles(t *testing.T) {
	t.Parallel()

	res := callReview(t, fullDiffManager(t, 0), readFullDiffTool, message.ToolArgumentValues{})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	for _, want := range []string{"helper.go (new file) +3 -0", "new.go (renamed from old.go) +1 -1"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("listing lacks %q:\n%s", want, res.Text)
		}
	}
}

// The definition an incremental round could not see comes back with its
// new-side line number, which is what the model cites.
func TestReadFullDiff_ShowsFileWithLineNumbers(t *testing.T) {
	t.Parallel()

	res := callReview(t, fullDiffManager(t, 0), readFullDiffTool, message.ToolArgumentValues{reviewArgPath: "helper.go"})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Text, "[    3] +  | func writeChunk() {}") {
		t.Errorf("definition not shown with its line number:\n%s", res.Text)
	}
}

// A rename answers to either name: the model may have met the old one.
func TestReadFullDiff_FindsRenameByOldPath(t *testing.T) {
	t.Parallel()

	res := callReview(t, fullDiffManager(t, 0), readFullDiffTool, message.ToolArgumentValues{reviewArgPath: "old.go"})
	if res.Error != "" || !strings.Contains(res.Text, "var a = 2") {
		t.Errorf("rename not found by old path: err=%q text=%q", res.Error, res.Text)
	}
}

func TestReadFullDiff_UnknownPathListsFiles(t *testing.T) {
	t.Parallel()

	res := callReview(t, fullDiffManager(t, 0), readFullDiffTool, message.ToolArgumentValues{reviewArgPath: "nope.go"})
	if res.Error == "" {
		t.Fatal("unknown path was not rejected")
	}
	if !strings.Contains(res.Error, "helper.go") {
		t.Errorf("rejection does not name the files that would have worked: %s", res.Error)
	}
}

func TestReadFullDiff_TruncatesOnLineBoundary(t *testing.T) {
	t.Parallel()

	res := callReview(t, fullDiffManager(t, 40), readFullDiffTool, message.ToolArgumentValues{reviewArgPath: "helper.go"})
	if !strings.HasSuffix(res.Text, strings.TrimRight(fullDiffTruncated, "\n")) {
		t.Errorf("no truncation marker:\n%s", res.Text)
	}
	body := strings.TrimSuffix(res.Text, strings.TrimRight(fullDiffTruncated, "\n"))
	if !strings.HasSuffix(body, "\n") {
		t.Errorf("cut mid-line:\n%q", res.Text)
	}
}

// A full round has the whole diff in its prompt; the tool is not offered.
func TestReadFullDiff_AbsentWithoutFullDiff(t *testing.T) {
	t.Parallel()

	m := NewReviewToolManager(rangeValidator, nil)
	if _, ok := m.GetTools()[readFullDiffTool]; ok {
		t.Error("ReadFullDiff registered without a full diff")
	}
}
