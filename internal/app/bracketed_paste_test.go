package app

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
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
	// Single-line paste past maxInlinePasteRunes should become placeholder
	pastedText := strings.Repeat("x", maxInlinePasteRunes+20)
	input := []byte(string(pasteStartSeq) + pastedText + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "[pasted") {
		t.Errorf("expected placeholder, got %q", output)
	}
	if want := fmt.Sprintf("%d chars", maxInlinePasteRunes+20); !strings.Contains(output, want) {
		t.Errorf("expected %q in placeholder, got %q", want, output)
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

// A pasted URL is the case the inline threshold exists for: one line, and the
// thing you most want to read back and tweak before sending. An Actions
// workflow URL of ordinary length lands just past the old 80-rune limit, so it
// used to vanish behind a placeholder.
//
// The bounds check below is the real subject: this asserts something about the
// band between the old limit and the new one, so a URL edited out of that band
// must fail loudly rather than start passing for free.
func TestBracketedPasteReader_URLStaysInline(t *testing.T) {
	t.Parallel()
	const oldLimit = 80
	url := "https://github.com/example-org/example-service/actions/workflows/nightly-dispatch.yml"
	if n := utf8.RuneCountInString(url); n <= oldLimit || n > maxInlinePasteRunes {
		t.Fatalf("this test needs a URL between %d and %d runes, got %d",
			oldLimit+1, maxInlinePasteRunes, n)
	}

	input := []byte("can you check workflow runs here? " + string(pasteStartSeq) + url + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, url) {
		t.Errorf("the URL should reach the line verbatim, got %q", output)
	}
	if strings.Contains(output, "[pasted") {
		t.Errorf("a single-line URL should not be hidden behind a placeholder: %q", output)
	}
	if segments := reader.GetPasteSegments(); len(segments) != 0 {
		t.Errorf("an inlined paste needs no segment, got %d", len(segments))
	}
}

// Length is not the only test: a multiline paste stays a placeholder however
// short it is, which is what keeps the threshold from swallowing the case the
// placeholder exists for.
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
	paste1 := strings.Repeat("a", maxInlinePasteRunes+20)
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
	paste := strings.Repeat("x", maxInlinePasteRunes+20)
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
	paste := strings.Repeat("x", maxInlinePasteRunes+20)
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
	// Exactly at the threshold, single line — should pass through inline
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
	// One rune past the threshold, single line — should become placeholder
	pastedText := strings.Repeat("a", maxInlinePasteRunes+1)
	input := []byte(string(pasteStartSeq) + pastedText + string(pasteEndSeq))
	reader := NewBracketedPasteReader(newTestInput(input))

	output, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "[pasted") {
		t.Errorf("expected placeholder one rune past the threshold, got %q", output)
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

// chunkedReadCloser hands back one pre-sliced chunk per Read, reproducing how a
// tty actually delivers a paste: in whatever pieces the kernel buffer yields,
// with no regard for where an escape sequence begins or ends.
type chunkedReadCloser struct {
	chunks [][]byte
}

func (c *chunkedReadCloser) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks = c.chunks[1:]
	return n, nil
}

func (c *chunkedReadCloser) Close() error { return nil }

// splitAt cuts s into chunks at the given byte offsets.
func splitAt(s string, offsets ...int) [][]byte {
	var out [][]byte
	prev := 0
	for _, off := range offsets {
		out = append(out, []byte(s[prev:off]))
		prev = off
	}
	return append(out, []byte(s[prev:]))
}

// A paste marker split across two reads must still be recognized. Before the
// seqBuf rejoin this dropped the marker entirely: its literal bytes reached
// readline, showing up as ^[[200~ / ^[[201~ and swallowing the next Return.
func TestBracketedPasteReader_MarkerSplitAcrossReads(t *testing.T) {
	t.Parallel()

	const payload = "hello world"
	full := "\x1b[200~" + payload + "\x1b[201~"

	// Every interesting cut: inside the start marker, and inside the end one.
	cases := map[string][]int{
		"start marker after ESC":     {1},
		"start marker mid-sequence":  {4},
		"start marker before tilde":  {5},
		"end marker after ESC":       {len(full) - 5},
		"end marker mid-sequence":    {len(full) - 3},
		"end marker before tilde":    {len(full) - 1},
		"both markers split":         {3, len(full) - 2},
		"one byte at a time is fine": {1, 2, 3, 4, 5, 6, 7},
	}

	for name, offsets := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := NewBracketedPasteReader(&chunkedReadCloser{chunks: splitAt(full, offsets...)})
			got, err := readAll(r)
			if err != nil {
				t.Fatalf("readAll: %v", err)
			}
			if got != payload {
				t.Errorf("got %q, want %q", got, payload)
			}
			if strings.ContainsRune(got, '\x1b') {
				t.Errorf("escape byte leaked into readline input: %q", got)
			}
		})
	}
}

// The end marker is the one most likely to straddle a boundary in real use: it
// sits at the tail of a large paste, which is exactly where the tty splits.
func TestBracketedPasteReader_LongPasteWithSplitEndMarker(t *testing.T) {
	t.Parallel()

	payload := strings.Repeat("x", 500) + "\nsecond line"
	full := "\x1b[200~" + payload + "\x1b[201~"

	r := NewBracketedPasteReader(&chunkedReadCloser{
		chunks: splitAt(full, len(full)-4), // cut inside ESC[201~
	})
	got, err := readAll(r)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}

	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("escape byte leaked into readline input: %q", got)
	}
	if !strings.HasPrefix(got, "[pasted 2 lines,") {
		t.Errorf("expected a placeholder, got %q", got)
	}
	segments := r.GetPasteSegments()
	if len(segments) != 1 || segments[0] != payload {
		t.Errorf("segment not captured intact: %#v", segments)
	}
}

// A partial match that turns out not to be a marker must still reach readline,
// in order, rather than being held or dropped.
func TestBracketedPasteReader_FalsePartialIsFlushed(t *testing.T) {
	t.Parallel()

	// "\x1b[2" looks like the start of ESC[200~ but is a cursor sequence.
	input := "abc\x1b[2K" + "def"
	r := NewBracketedPasteReader(&chunkedReadCloser{chunks: splitAt(input, 5)})
	got, err := readAll(r)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if got != input {
		t.Errorf("got %q, want the input passed through unchanged %q", got, input)
	}
}
