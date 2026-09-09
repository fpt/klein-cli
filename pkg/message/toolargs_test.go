package message

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummarizeToolArgs_TruncatesLongStringRuneSafe(t *testing.T) {
	t.Parallel()
	// Multibyte (Japanese) input: truncation must not split a rune.
	long := strings.Repeat("あ", 400)
	out := SummarizeToolArgs(ToolArgumentValues{"content": long})

	got, _ := out["content"].(string)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated string is not valid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) > summaryMaxStringLen {
		t.Fatalf("truncated to %d runes, want <= %d", utf8.RuneCountInString(got), summaryMaxStringLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got[len(got)-6:])
	}
}

func TestSummarizeToolArgs_ShortValuesPassThrough(t *testing.T) {
	t.Parallel()
	out := SummarizeToolArgs(ToolArgumentValues{
		"path":    "/tmp/x",
		"count":   42,
		"enabled": true,
	})
	if out["path"] != "/tmp/x" || out["count"] != 42 || out["enabled"] != true {
		t.Fatalf("scalars should pass through unchanged: %+v", out)
	}
}

func TestSummarizeToolArgs_CapsArray(t *testing.T) {
	t.Parallel()
	items := make([]any, 20)
	for i := range items {
		items[i] = i
	}
	out := SummarizeToolArgs(ToolArgumentValues{"items": items})

	arr, ok := out["items"].([]any)
	if !ok {
		t.Fatalf("items = %T, want []any", out["items"])
	}
	if len(arr) != summaryMaxArrayItems+1 { // capped items + the "…+N more" marker
		t.Fatalf("array len = %d, want %d", len(arr), summaryMaxArrayItems+1)
	}
	last, _ := arr[len(arr)-1].(string)
	if !strings.Contains(last, "more") {
		t.Errorf("expected overflow marker, got %v", arr[len(arr)-1])
	}
}

func TestSummarizeToolArgs_CapsMap(t *testing.T) {
	t.Parallel()
	nested := map[string]any{}
	for i := range 30 {
		nested[string(rune('a'+i%26))+strings.Repeat("x", i)] = i
	}
	out := SummarizeToolArgs(ToolArgumentValues{"m": nested})

	m, ok := out["m"].(map[string]any)
	if !ok {
		t.Fatalf("m = %T, want map", out["m"])
	}
	// At most the entry cap plus the single "…" overflow key.
	if len(m) > summaryMaxMapEntries+1 {
		t.Fatalf("map kept %d entries, want <= %d", len(m), summaryMaxMapEntries+1)
	}
	if _, ok := m["…"]; !ok {
		t.Errorf("expected overflow key '…' in capped map: %+v", m)
	}
}

func TestSummarizeToolArgs_CollapsesDeepNesting(t *testing.T) {
	t.Parallel()
	deep := map[string]any{"l1": map[string]any{"l2": map[string]any{"l3": "buried"}}}
	out := SummarizeToolArgs(ToolArgumentValues{"root": deep})

	// root(0) -> l1 map(1) -> l2 map(2) -> l3 collapsed at depth 3.
	l1 := out["root"].(map[string]any)
	l2 := l1["l1"].(map[string]any)
	if l2["l2"] != "…" {
		t.Fatalf("depth beyond limit should collapse to '…', got %v", l2["l2"])
	}
}

func TestSummarizeToolArgs_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("z", 400)
	in := ToolArgumentValues{"content": long}
	SummarizeToolArgs(in)
	if in["content"] != long {
		t.Fatal("SummarizeToolArgs must not mutate the caller's arguments")
	}
}

// unwrapCase is field-ordered pointer-first to keep fieldalignment quiet.
type unwrapCase struct {
	args     ToolArgumentValues
	want     ToolArgumentValues
	name     string
	declared []ToolArgument
}

func unwrapToolArgsCases() []unwrapCase {
	const (
		argPattern = "pattern"
		argPath    = "path"
		dir        = "src"
	)
	declared := []ToolArgument{{Name: argPattern}, {Name: argPath}}
	inner := map[string]any{argPattern: "clear_all_selection", argPath: dir}

	return []unwrapCase{
		{
			name:     "top-level args pass through",
			args:     ToolArgumentValues{argPattern: "x"},
			want:     ToolArgumentValues{argPattern: "x"},
			declared: declared,
		},
		{
			name:     "parameters envelope is stripped",
			args:     ToolArgumentValues{argEnvelopeParameters: inner},
			want:     ToolArgumentValues(inner),
			declared: declared,
		},
		{
			name:     "arguments envelope is stripped",
			args:     ToolArgumentValues{argEnvelopeArguments: ToolArgumentValues{argPattern: "x"}},
			want:     ToolArgumentValues{argPattern: "x"},
			declared: declared,
		},
		{
			name:     "double envelope is stripped",
			args:     ToolArgumentValues{argEnvelopeInput: map[string]any{argEnvelopeParameters: inner}},
			want:     ToolArgumentValues(inner),
			declared: declared,
		},
		{
			name:     "declared envelope name is left alone",
			args:     ToolArgumentValues{argEnvelopeInput: map[string]any{argPattern: "x"}},
			want:     ToolArgumentValues{argEnvelopeInput: map[string]any{argPattern: "x"}},
			declared: []ToolArgument{{Name: argEnvelopeInput}},
		},
		{
			name:     "non-map envelope value is left alone",
			args:     ToolArgumentValues{argEnvelopeInput: "some text"},
			want:     ToolArgumentValues{argEnvelopeInput: "some text"},
			declared: declared,
		},
		{
			name:     "envelope alongside a real argument is left alone",
			args:     ToolArgumentValues{argEnvelopeParameters: inner, argPath: dir},
			want:     ToolArgumentValues{argEnvelopeParameters: inner, argPath: dir},
			declared: declared,
		},
		{
			name:     "empty args pass through",
			args:     ToolArgumentValues{},
			want:     ToolArgumentValues{},
			declared: declared,
		},
	}
}

func TestUnwrapToolArgs(t *testing.T) {
	t.Parallel()
	for _, tt := range unwrapToolArgsCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := UnwrapToolArgs(tt.args, tt.declared)
			if !reflect.DeepEqual(map[string]any(got), map[string]any(tt.want)) {
				t.Errorf("UnwrapToolArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}
