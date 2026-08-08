package tomledit

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// decode is the assertion that matters most: after any edit the document still
// means what it should, not merely that the text looks right.
// newCommand is the replacement body these tests write, named because several
// of them assert on it.
const newCommand = "new"

func decode(t *testing.T, doc []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if _, err := toml.Decode(string(doc), &got); err != nil {
		t.Fatalf("result is not valid TOML: %v\n%s", err, doc)
	}
	return got
}

// The reason this package exists. A tool that eats the note you left above a
// server definition is a tool you stop pointing at your config.
func TestSetTable_KeepsCommentsAndUnrelatedContent(t *testing.T) {
	t.Parallel()
	src := []byte(`# klein settings
# edited by hand, mind the comments

[llm]
backend = "anthropic"  # my preferred one

# godoc gives the agent Go documentation
[mcp.godoc]
command = "godevmcp"
args = ["serve"]

[bash]
timeout = 120
`)

	got, err := SetTable(src, "mcp.godoc", []byte("command = \"godevmcp\"\nargs = [\"stdio\"]\n"))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}
	s := string(got)

	for _, keep := range []string{
		"# klein settings",
		"# edited by hand, mind the comments",
		`backend = "anthropic"  # my preferred one`,
		"# godoc gives the agent Go documentation",
		"[bash]",
		"timeout = 120",
	} {
		if !strings.Contains(s, keep) {
			t.Errorf("edit dropped %q:\n%s", keep, s)
		}
	}
	if !strings.Contains(s, `args = ["stdio"]`) {
		t.Errorf("the new body did not land:\n%s", s)
	}
	if strings.Contains(s, `args = ["serve"]`) {
		t.Errorf("the old body survived:\n%s", s)
	}
}

func TestSetTable_AppendsWhenAbsent(t *testing.T) {
	t.Parallel()
	src := []byte("[llm]\nbackend = \"openai\"\n")

	got, err := SetTable(src, "mcp.github", []byte(`url = "https://example.test"`))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}

	m := decode(t, got)
	mcp, _ := m["mcp"].(map[string]any)
	gh, _ := mcp["github"].(map[string]any)
	if gh["url"] != "https://example.test" {
		t.Errorf("appended table did not decode: %+v", m)
	}
	if llm, _ := m["llm"].(map[string]any); llm["backend"] != "openai" {
		t.Errorf("existing content was disturbed: %+v", m)
	}
}

// A table's child tables belong to it. Replacing `[mcp.godoc]` while leaving a
// stale `[mcp.godoc.env]` behind would silently keep environment variables from
// the definition that was just replaced.
func TestSetTable_ReplacesChildTablesToo(t *testing.T) {
	t.Parallel()
	src := []byte(`[mcp.godoc]
command = "old"

[mcp.godoc.env]
TOKEN = "stale"

[mcp.other]
command = "keep-me"
`)

	got, err := SetTable(src, "mcp.godoc", []byte(`command = "new"`))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}

	m := decode(t, got)
	mcp := m["mcp"].(map[string]any)
	godoc := mcp["godoc"].(map[string]any)
	if _, stale := godoc["env"]; stale {
		t.Errorf("the replaced table kept its child table: %+v", godoc)
	}
	if godoc["command"] != newCommand {
		t.Errorf("body not replaced: %+v", godoc)
	}
	if other := mcp["other"].(map[string]any); other["command"] != "keep-me" {
		t.Errorf("a sibling table was damaged: %+v", other)
	}
}

// The one construct that can put a `[` at the start of a line without it being a
// header. A scanner that misses it splices in the middle of someone's string.
func TestSetTable_IgnoresBracketsInsideMultilineStrings(t *testing.T) {
	t.Parallel()
	src := []byte(`[agent]
note = """
[mcp.godoc]
this is prose, not a table
"""

[mcp.godoc]
command = "real"
`)

	got, err := SetTable(src, "mcp.godoc", []byte(`command = "replaced"`))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}

	m := decode(t, got)
	agent := m["agent"].(map[string]any)
	note, _ := agent["note"].(string)
	if !strings.Contains(note, "this is prose, not a table") {
		t.Errorf("the multi-line string was mangled: %q", note)
	}
	if cmd := m["mcp"].(map[string]any)["godoc"].(map[string]any)["command"]; cmd != "replaced" {
		t.Errorf("the real table was not the one edited: %v", cmd)
	}
}

func TestDeleteTable_RemovesTableAndChildren(t *testing.T) {
	t.Parallel()
	src := []byte(`[llm]
backend = "openai"

[mcp.godoc]
command = "godevmcp"

[mcp.godoc.env]
TOKEN = "x"

[mcp.other]
command = "keep-me"
`)

	got, found, err := DeleteTable(src, "mcp.godoc")
	if err != nil || !found {
		t.Fatalf("DeleteTable: found=%v err=%v", found, err)
	}

	m := decode(t, got)
	mcp := m["mcp"].(map[string]any)
	if _, still := mcp["godoc"]; still {
		t.Errorf("table survived deletion: %+v", mcp)
	}
	if other := mcp["other"].(map[string]any); other["command"] != "keep-me" {
		t.Errorf("a sibling was removed with it: %+v", mcp)
	}
	if llm := m["llm"].(map[string]any); llm["backend"] != "openai" {
		t.Errorf("unrelated content was removed: %+v", m)
	}
}

// Deleting something that is not there is not an error — the caller decides
// whether "no such server" is worth reporting, and the file must not change.
func TestDeleteTable_AbsentIsNotAnError(t *testing.T) {
	t.Parallel()
	src := []byte("[llm]\nbackend = \"openai\"\n")

	got, found, err := DeleteTable(src, "mcp.nope")
	if err != nil {
		t.Fatalf("DeleteTable: %v", err)
	}
	if found {
		t.Error("reported finding a table that is not there")
	}
	if string(got) != string(src) {
		t.Errorf("the document changed anyway:\n%s", got)
	}
}

// Comments above a header are left alone: one may be the table's own note and
// another the heading for the whole section, and the text does not say which.
func TestDeleteTable_LeavesCommentsAlone(t *testing.T) {
	t.Parallel()
	src := []byte(`# ==== MCP servers ====
# godoc gives the agent Go documentation
[mcp.godoc]
command = "godevmcp"
`)

	got, found, _ := DeleteTable(src, "mcp.godoc")
	if !found {
		t.Fatal("table not found")
	}
	if !strings.Contains(string(got), "# ==== MCP servers ====") {
		t.Errorf("a section heading was eaten:\n%s", got)
	}
}

func TestSetTable_RejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", "   ", "mcp.[evil]", "mcp.a\nb", `mcp."q"`, "mcp.x#y"} {
		if _, err := SetTable([]byte("[llm]\n"), path, []byte("a = 1")); err == nil {
			t.Errorf("path %q was accepted", path)
		}
	}
}

// An empty document is the first-run case: the file may not exist yet.
func TestSetTable_EmptyDocument(t *testing.T) {
	t.Parallel()
	got, err := SetTable(nil, "mcp.godoc", []byte(`command = "godevmcp"`))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}
	m := decode(t, got)
	if m["mcp"].(map[string]any)["godoc"].(map[string]any)["command"] != "godevmcp" {
		t.Errorf("table not written into an empty document: %s", got)
	}
}

// Add then remove should leave the document as it started, or repeated use
// accumulates blank lines and drift.
func TestRoundTrip_AddThenRemoveIsStable(t *testing.T) {
	t.Parallel()
	src := []byte("[llm]\nbackend = \"openai\"\n")

	added, err := SetTable(src, "mcp.tmp", []byte(`command = "x"`))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}
	removed, found, err := DeleteTable(added, "mcp.tmp")
	if err != nil || !found {
		t.Fatalf("DeleteTable: found=%v err=%v", found, err)
	}

	if got, want := strings.TrimSpace(string(removed)), strings.TrimSpace(string(src)); got != want {
		t.Errorf("round trip drifted:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// A row of a nested array also starts a line with `[`. Reading one as a header
// ends the enclosing table early, and the result can still parse — so this is
// the failure the validity check at the end would not catch.
func TestSetTable_IgnoresNestedArrayRows(t *testing.T) {
	t.Parallel()
	src := []byte(`[mcp.godoc]
command = "godevmcp"
matrix = [
  [1, 2],
  [3, 4],
]
tail = "still mine"

[mcp.other]
command = "keep-me"
`)

	got, found, err := DeleteTable(src, "mcp.godoc")
	if err != nil || !found {
		t.Fatalf("DeleteTable: found=%v err=%v", found, err)
	}

	s := string(got)
	// Everything in the table goes, including what follows the nested array.
	for _, gone := range []string{"godevmcp", "matrix", "still mine", "[1, 2]"} {
		if strings.Contains(s, gone) {
			t.Errorf("the table was cut short at the nested array; %q survived:\n%s", gone, s)
		}
	}
	if !strings.Contains(s, "keep-me") {
		t.Errorf("the sibling table was removed:\n%s", s)
	}
}

// A header may carry a trailing comment, and replacing the table must keep the
// author's line rather than substituting a canonical one.
func TestSetTable_KeepsHeaderLineVerbatim(t *testing.T) {
	t.Parallel()
	src := []byte("[mcp.godoc]  # the Go docs server\ncommand = \"old\"\n")

	got, err := SetTable(src, "mcp.godoc", []byte(`command = "new"`))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}
	if !strings.Contains(string(got), "# the Go docs server") {
		t.Errorf("the header's trailing comment was lost:\n%s", got)
	}
	if m := decode(t, got); m["mcp"].(map[string]any)["godoc"].(map[string]any)["command"] != newCommand {
		t.Errorf("body not replaced:\n%s", got)
	}
}

// The package's whole claim is that untouched bytes stay untouched, and a
// document written on Windows is the case where that is easiest to break: split
// on LF, join on LF, and every line in the file has quietly changed. An edit to
// one server would show up as a diff against the entire config.
func TestSetTable_PreservesCRLFLineEndings(t *testing.T) {
	t.Parallel()
	src := []byte("# klein settings\r\n\r\n[llm]\r\nbackend = \"openai\"\r\n\r\n[mcp.godoc]\r\ncommand = \"old\"\r\n")

	got, err := SetTable(src, "mcp.godoc", []byte(`command = "new"`))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}
	s := string(got)

	// Untouched lines keep their endings.
	for _, keep := range []string{"# klein settings\r\n", "[llm]\r\n", "backend = \"openai\"\r\n"} {
		if !strings.Contains(s, keep) {
			t.Errorf("a CRLF line ending was rewritten; %q missing from:\n%q", keep, s)
		}
	}
	// And the inserted line matches the file it landed in rather than seeding a
	// mixed-ending document.
	if !strings.Contains(s, "command = \"new\"\r\n") {
		t.Errorf("inserted line did not adopt the document's endings:\n%q", s)
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Errorf("document ended up with mixed line endings:\n%q", s)
	}
	if m := decode(t, got); m["mcp"].(map[string]any)["godoc"].(map[string]any)["command"] != newCommand {
		t.Errorf("body not replaced:\n%s", got)
	}
}

// Appending to a CRLF document has to match too — including the blank separator
// line the append inserts.
func TestSetTable_AppendsWithCRLF(t *testing.T) {
	t.Parallel()
	src := []byte("[llm]\r\nbackend = \"openai\"\r\n")

	got, err := SetTable(src, "mcp.github", []byte(`url = "https://example.test"`))
	if err != nil {
		t.Fatalf("SetTable: %v", err)
	}
	if strings.Contains(strings.ReplaceAll(string(got), "\r\n", ""), "\n") {
		t.Errorf("append introduced LF endings into a CRLF document:\n%q", got)
	}
	m := decode(t, got)
	if m["mcp"].(map[string]any)["github"].(map[string]any)["url"] != "https://example.test" {
		t.Errorf("appended table did not decode: %+v", m)
	}
}

func TestDeleteTable_PreservesCRLFLineEndings(t *testing.T) {
	t.Parallel()
	src := []byte("[llm]\r\nbackend = \"openai\"\r\n\r\n[mcp.godoc]\r\ncommand = \"x\"\r\n")

	got, found, err := DeleteTable(src, "mcp.godoc")
	if err != nil || !found {
		t.Fatalf("DeleteTable: found=%v err=%v", found, err)
	}
	if strings.Contains(strings.ReplaceAll(string(got), "\r\n", ""), "\n") {
		t.Errorf("deletion rewrote line endings:\n%q", got)
	}
	if !strings.Contains(string(got), "backend = \"openai\"\r\n") {
		t.Errorf("an untouched CRLF line was changed:\n%q", got)
	}
}

// A CRLF document is still found by the scanner: a header line ends with a CR,
// and every comparison has to see past it.
func TestFindTable_HeaderWithCarriageReturn(t *testing.T) {
	t.Parallel()
	src := []byte("[mcp.godoc]\r\ncommand = \"x\"\r\n")

	if _, found, err := DeleteTable(src, "mcp.godoc"); err != nil || !found {
		t.Errorf("a CRLF header was not recognized: found=%v err=%v", found, err)
	}
}

// A comment above a deleted table is kept (see DeleteTable), and the blank line
// that separated the table from the next one is kept with it — otherwise the
// orphan is pulled down against the following header and reads as its note.
func TestDeleteTable_KeepsTheSeparatorAboveTheNextTable(t *testing.T) {
	t.Parallel()
	src := []byte(`# godoc: gives the agent Go documentation
[mcp.godoc]
command = "godevmcp"

[agent]
max_iterations = 30
`)

	got, found, err := DeleteTable(src, "mcp.godoc")
	if err != nil || !found {
		t.Fatalf("DeleteTable: found=%v err=%v", found, err)
	}
	if strings.Contains(string(got), "Go documentation\n[agent]") {
		t.Errorf("the orphaned comment was pulled onto the next table:\n%s", got)
	}
	if !strings.Contains(string(got), "Go documentation\n\n[agent]") {
		t.Errorf("the blank separator was not kept:\n%q", got)
	}
}
