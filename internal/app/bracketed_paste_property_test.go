package app

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/fpt/klein-cli/pkg/testhelper/pastegen"
)

// pasteState is everything processBytes leaves behind that a later read or
// the REPL can observe.
type pasteState struct {
	out      string
	held     string // bytes kept back as a possible partial marker
	segments []string
	inPaste  bool
}

// refPaste is the reference for the reader, scanning one byte at a time: at
// each position a whole marker switches mode; a proper prefix of the marker
// that runs to the end of the input is held back; anything else is input
// (outside a paste) or paste content (inside one). A finished paste is
// inlined when it has no newline and at most InlineLimit runes, dropped when
// empty, and otherwise becomes a segment plus a placeholder.
func refPaste(in []byte) pasteState {
	var st pasteState
	var out, paste strings.Builder
	for i := 0; i < len(in); {
		marker := pastegen.Start
		if st.inPaste {
			marker = pastegen.End
		}
		rest := string(in[i:])
		switch {
		case strings.HasPrefix(rest, marker):
			if st.inPaste {
				st.segments = finishPaste(&out, paste.String(), st.segments)
				paste.Reset()
			}
			st.inPaste = !st.inPaste
			i += len(marker)
			continue
		case len(rest) < len(marker) && strings.HasPrefix(marker, rest):
			st.held = rest
		case st.inPaste:
			paste.WriteByte(in[i])
		default:
			out.WriteByte(in[i])
		}
		if st.held != "" {
			break
		}
		i++
	}
	st.out = out.String()
	return st
}

func finishPaste(out *strings.Builder, text string, segments []string) []string {
	runes := utf8.RuneCountInString(text)
	switch {
	case text == "":
	case !strings.Contains(text, "\n") && runes <= pastegen.InlineLimit:
		out.WriteString(text)
	default:
		fmt.Fprintf(out, "[pasted %d lines, %d chars (#%d)]", strings.Count(text, "\n")+1, runes, len(segments))
		segments = append(segments, text)
	}
	return segments
}

func feed(in []byte, cuts []int) pasteState {
	r := NewBracketedPasteReader(nil)
	prev := 0
	for _, c := range append(cuts, len(in)) {
		r.processBytes(in[prev:c])
		prev = c
	}
	return pasteState{out: r.outBuf.String(), segments: r.segments, held: string(r.seqBuf), inPaste: r.inPaste}
}

func diffState(t *rapid.T, label string, got, want pasteState) {
	if got.out != want.out || fmt.Sprint(got.segments) != fmt.Sprint(want.segments) ||
		got.held != want.held || got.inPaste != want.inPaste {

		t.Fatalf("%s\n got: out=%q segments=%q held=%q inPaste=%v\nwant: out=%q segments=%q held=%q inPaste=%v",
			label, got.out, got.segments, got.held, got.inPaste, want.out, want.segments, want.held, want.inPaste)
	}
}

// TestBracketedPasteReader_ChunkingInvariant checks P1: however the input is
// split into reads, the reader ends in the same state as for one read.
func TestBracketedPasteReader_ChunkingInvariant(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		in := pastegen.Input().Draw(t, "in")
		cuts := pastegen.Cuts(len(in)).Draw(t, "cuts")
		diffState(t, "chunked vs whole", feed(in, cuts), feed(in, nil))
	})
}

// TestBracketedPasteReader_MatchesReference checks P2: input passes through,
// pastes are inlined or replaced by numbered placeholders, and a trailing
// partial marker is held back, exactly as the byte-at-a-time reference says.
func TestBracketedPasteReader_MatchesReference(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		in := pastegen.Input().Draw(t, "in")
		cuts := pastegen.Cuts(len(in)).Draw(t, "cuts")
		diffState(t, "reader vs reference", feed(in, cuts), refPaste(in))
	})
}

// chunkReader returns its chunks one per Read, then io.EOF.
type chunkReader struct{ chunks [][]byte }

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	if c.chunks[0] = c.chunks[0][n:]; len(c.chunks[0]) == 0 {
		c.chunks = c.chunks[1:]
	}
	return n, nil
}

func (c *chunkReader) Close() error { return nil }

// TestBracketedPasteReader_ReadDeliversReference checks P3: through Read, with
// any read sizes from the terminal and any buffer size from readline, the
// caller receives the reference output and GetPasteSegments the reference
// segments.
func TestBracketedPasteReader_ReadDeliversReference(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		in := pastegen.Input().Draw(t, "in")
		cuts := pastegen.Cuts(len(in)).Draw(t, "cuts")
		var chunks [][]byte
		prev := 0
		for _, c := range append(cuts, len(in)) {
			chunks = append(chunks, bytes.Clone(in[prev:c]))
			prev = c
		}
		r := NewBracketedPasteReader(&chunkReader{chunks: chunks})
		p := make([]byte, rapid.IntRange(1, 16).Draw(t, "bufSize"))
		var got bytes.Buffer
		for {
			n, err := r.Read(p)
			got.Write(p[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
		}
		want := refPaste(in)
		if got.String() != want.out {
			t.Fatalf("Read delivered %q, want %q", got.String(), want.out)
		}
		if segs := r.GetPasteSegments(); fmt.Sprint(segs) != fmt.Sprint(want.segments) {
			t.Fatalf("segments %q, want %q", segs, want.segments)
		}
	})
}

// TestBracketedPasteReader_LoneEscWaitsForNextRead pins current behavior that
// is not confirmed as intended: a read ending in ESC (or any proper prefix of
// ESC[200~) is held back until the next read, so a lone Esc keypress reaches
// readline only with the following keystroke.
func TestBracketedPasteReader_LoneEscWaitsForNextRead(t *testing.T) {
	t.Parallel()
	r := NewBracketedPasteReader(nil)
	r.processBytes([]byte("\x1b"))
	if r.outBuf.Len() != 0 || string(r.seqBuf) != "\x1b" {
		t.Fatalf("after ESC: out=%q held=%q, want nothing out and ESC held", r.outBuf.String(), r.seqBuf)
	}
	r.processBytes([]byte("x"))
	if got := r.outBuf.String(); got != "\x1bx" {
		t.Fatalf("after next key: out=%q, want %q", got, "\x1bx")
	}
}

// TestBracketedPasteReader_DropsHeldBytesAtEOF pins current behavior that is
// not confirmed as intended: at EOF, a held partial marker and the content of
// an unterminated paste are never delivered.
func TestBracketedPasteReader_DropsHeldBytesAtEOF(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"abc\x1b":                    "abc",
		"abc\x1b[20":                 "abc",
		"ab" + pastegen.Start + "cd": "ab",
	} {
		got, err := readAll(NewBracketedPasteReader(newTestInput([]byte(in))))
		if err != nil || got != want {
			t.Fatalf("readAll(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
