package app

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// testReadCloser wraps a bytes.Reader as an io.ReadCloser for testing.
type testReadCloser struct {
	*bytes.Reader
}

func (t *testReadCloser) Close() error { return nil }

func newTestInput(data []byte) io.ReadCloser {
	return &testReadCloser{bytes.NewReader(data)}
}

// readAll reads all available output from the BracketedPasteReader.
func readAll(r *BracketedPasteReader) (string, error) {
	var buf bytes.Buffer
	p := make([]byte, 4096)
	for {
		n, err := r.Read(p)
		if n > 0 {
			buf.Write(p[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return buf.String(), err
		}
	}
	return buf.String(), nil
}

func TestBracketedPasteReader_NoPaste(t *testing.T) {
	input := []byte("hello world")
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if output != "hello world" {
		t.Errorf("expected 'hello world', got %q", output)
	}

	segments := reader.GetPasteSegments()
	if len(segments) != 0 {
		t.Errorf("expected no segments, got %d", len(segments))
	}
}

func TestBracketedPasteReader_ShortPaste(t *testing.T) {
	// Short single-line paste should pass through inline
	pastedText := "short paste"
	input := []byte("before" + string(pasteStartSeq) + pastedText + string(pasteEndSeq) + "after")
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "before" + pastedText + "after"
	if output != expected {
		t.Errorf("expected %q, got %q", expected, output)
	}

	// Short inline pastes should not create segments
	segments := reader.GetPasteSegments()
	if len(segments) != 0 {
		t.Errorf("expected no segments for short paste, got %d", len(segments))
	}
}

func TestBracketedPasteReader_LongPaste(t *testing.T) {
	// Long single-line paste (>80 runes) should become placeholder
	pastedText := strings.Repeat("x", 100)
	input := []byte(string(pasteStartSeq) + pastedText + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "[pasted") {
		t.Errorf("expected placeholder, got %q", output)
	}
	if !strings.Contains(output, "100 chars") {
		t.Errorf("expected '100 chars' in placeholder, got %q", output)
	}
	if !strings.Contains(output, "1 lines") {
		t.Errorf("expected '1 lines' in placeholder, got %q", output)
	}

	segments := reader.GetPasteSegments()
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0] != pastedText {
		t.Errorf("segment content mismatch: expected %d chars, got %d", len(pastedText), len(segments[0]))
	}
}

func TestBracketedPasteReader_MultilinePaste(t *testing.T) {
	// Multiline paste should always become placeholder regardless of length
	pastedText := "line1\nline2\nline3"
	input := []byte(string(pasteStartSeq) + pastedText + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "[pasted") {
		t.Errorf("expected placeholder for multiline paste, got %q", output)
	}
	if !strings.Contains(output, "3 lines") {
		t.Errorf("expected '3 lines' in placeholder, got %q", output)
	}

	segments := reader.GetPasteSegments()
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0] != pastedText {
		t.Errorf("segment content mismatch")
	}
}

func TestBracketedPasteReader_MultiplePastes(t *testing.T) {
	// Multiple paste events should accumulate segments
	paste1 := strings.Repeat("a", 100)
	paste2 := "line1\nline2"
	input := []byte(
		"typed1" +
			string(pasteStartSeq) + paste1 + string(pasteEndSeq) +
			"typed2" +
			string(pasteStartSeq) + paste2 + string(pasteEndSeq) +
			"typed3",
	)
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should contain typed text and two placeholders
	if !strings.Contains(output, "typed1") {
		t.Error("missing typed1")
	}
	if !strings.Contains(output, "typed2") {
		t.Error("missing typed2")
	}
	if !strings.Contains(output, "typed3") {
		t.Error("missing typed3")
	}
	if strings.Count(output, "[pasted") != 2 {
		t.Errorf("expected 2 placeholders, got output: %q", output)
	}

	segments := reader.GetPasteSegments()
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0] != paste1 {
		t.Error("first segment mismatch")
	}
	if segments[1] != paste2 {
		t.Error("second segment mismatch")
	}
}

func TestBracketedPasteReader_MixedInput(t *testing.T) {
	// Normal typed text with a short inline paste in between
	input := []byte("hello " + string(pasteStartSeq) + "world" + string(pasteEndSeq) + "!")
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if output != "hello world!" {
		t.Errorf("expected 'hello world!', got %q", output)
	}
}

func TestBracketedPasteReader_EmptyPaste(t *testing.T) {
	// Empty paste should produce no output and no segments
	input := []byte("before" + string(pasteStartSeq) + string(pasteEndSeq) + "after")
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if output != "beforeafter" {
		t.Errorf("expected 'beforeafter', got %q", output)
	}

	segments := reader.GetPasteSegments()
	if len(segments) != 0 {
		t.Errorf("expected no segments for empty paste, got %d", len(segments))
	}
}

func TestBracketedPasteReader_GetSegmentsClearsState(t *testing.T) {
	paste := strings.Repeat("x", 100)
	input := []byte(string(pasteStartSeq) + paste + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	readAll(reader)

	// First call should return segments
	segments1 := reader.GetPasteSegments()
	if len(segments1) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments1))
	}

	// Second call should return nil (cleared)
	segments2 := reader.GetPasteSegments()
	if len(segments2) != 0 {
		t.Errorf("expected 0 segments after clear, got %d", len(segments2))
	}
}

func TestBracketedPasteReader_ClearSegments(t *testing.T) {
	paste := strings.Repeat("x", 100)
	input := []byte(string(pasteStartSeq) + paste + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	readAll(reader)

	reader.ClearSegments()

	segments := reader.GetPasteSegments()
	if len(segments) != 0 {
		t.Errorf("expected 0 segments after ClearSegments, got %d", len(segments))
	}
}

func TestBracketedPasteReader_ExactThreshold(t *testing.T) {
	// Exactly 80 runes, single line — should pass through inline
	pastedText := strings.Repeat("a", maxInlinePasteRunes)
	input := []byte(string(pasteStartSeq) + pastedText + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if output != pastedText {
		t.Errorf("expected inline paste at threshold, got %q", output)
	}

	segments := reader.GetPasteSegments()
	if len(segments) != 0 {
		t.Errorf("expected no segments at threshold, got %d", len(segments))
	}
}

func TestBracketedPasteReader_OneOverThreshold(t *testing.T) {
	// 81 runes, single line — should become placeholder
	pastedText := strings.Repeat("a", maxInlinePasteRunes+1)
	input := []byte(string(pasteStartSeq) + pastedText + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "[pasted") {
		t.Errorf("expected placeholder for 81 chars, got %q", output)
	}

	segments := reader.GetPasteSegments()
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
}

func TestPartialMatchSuffix(t *testing.T) {
	reader := &BracketedPasteReader{}

	tests := []struct {
		name     string
		data     []byte
		seq      []byte
		expected int
	}{
		{"no match", []byte("hello"), pasteStartSeq, 0},
		{"match ESC", []byte("hello\x1b"), pasteStartSeq, 1},
		{"match ESC[", []byte("hello\x1b["), pasteStartSeq, 2},
		{"match ESC[2", []byte("hello\x1b[2"), pasteStartSeq, 3},
		{"match ESC[20", []byte("hello\x1b[20"), pasteStartSeq, 4},
		{"match ESC[200", []byte("hello\x1b[200"), pasteStartSeq, 5},
		{"full match not partial", []byte("hello\x1b[200~"), pasteStartSeq, 0}, // full match is found by bytes.Index, not this
		{"end seq partial", []byte("data\x1b[201"), pasteEndSeq, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reader.partialMatchSuffix(tt.data, tt.seq)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}
