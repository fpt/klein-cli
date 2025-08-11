package app

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// Bracketed paste escape sequences
var (
	pasteStartSeq = []byte("\x1b[200~")
	pasteEndSeq   = []byte("\x1b[201~")
)

const (
	// maxInlinePasteRunes is the threshold for inline vs placeholder paste.
	// Pastes with more runes or containing newlines become placeholders.
	maxInlinePasteRunes = 80
)

// BracketedPasteReader wraps an io.ReadCloser to intercept bracketed paste
// escape sequences. Normal input passes through unchanged. Pasted content
// (between ESC[200~ and ESC[201~) is buffered:
//   - Short, single-line pastes are forwarded inline to readline
//   - Long or multiline pastes are replaced with a visible placeholder,
//     and the actual content is stored in segments for later retrieval
type BracketedPasteReader struct {
	underlying io.ReadCloser
	outBuf     bytes.Buffer // output buffer for readline to consume
	pasteBuf   bytes.Buffer // accumulates pasted content
	inPaste    bool
	seqBuf     []byte   // partial escape sequence accumulator
	segments   []string // completed paste segment contents
	mu         sync.Mutex
}

// NewBracketedPasteReader creates a new paste-aware stdin wrapper.
func NewBracketedPasteReader(underlying io.ReadCloser) *BracketedPasteReader {
	return &BracketedPasteReader{
		underlying: underlying,
	}
}

// Read implements io.Reader. It reads from the underlying reader, intercepts
// bracketed paste sequences, and returns processed bytes to the caller (readline).
func (r *BracketedPasteReader) Read(p []byte) (int, error) {
	r.mu.Lock()

	// If we have buffered output, drain it first
	if r.outBuf.Len() > 0 {
		n, err := r.outBuf.Read(p)
		r.mu.Unlock()
		return n, err
	}
	r.mu.Unlock()

	// Read from underlying stdin
	tmp := make([]byte, 4096)
	n, err := r.underlying.Read(tmp)
	if n > 0 {
		r.mu.Lock()
		r.processBytes(tmp[:n])
		nn, readErr := r.outBuf.Read(p)
		r.mu.Unlock()
		if nn > 0 {
			return nn, nil
		}
		// If processBytes consumed everything (all went to pasteBuf) and
		// outBuf is empty, we need to try reading more from underlying.
		// But only if there was no error.
		if err != nil {
			return 0, err
		}
		if readErr != nil && readErr != io.EOF {
			return 0, readErr
		}
		// Recurse to get more data (paste might still be accumulating)
		return r.Read(p)
	}
	return n, err
}

// Close closes the underlying reader.
func (r *BracketedPasteReader) Close() error {
	return r.underlying.Close()
}

// GetPasteSegments returns accumulated paste segments and clears the list.
func (r *BracketedPasteReader) GetPasteSegments() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.segments) == 0 {
		return nil
	}
	result := r.segments
	r.segments = nil
	return result
}

// ClearSegments discards any accumulated paste segments.
func (r *BracketedPasteReader) ClearSegments() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.segments = nil
}

// processBytes scans data for paste start/end sequences, routing bytes to
// either outBuf (for readline) or pasteBuf (during paste).
// Must be called with r.mu held.
func (r *BracketedPasteReader) processBytes(data []byte) {
	i := 0
	for i < len(data) {
		if r.inPaste {
			// Look for paste end sequence ESC[201~
			idx := bytes.Index(data[i:], pasteEndSeq)
			if idx >= 0 {
				// Everything before the end sequence is paste content
				r.pasteBuf.Write(data[i : i+idx])
				r.finalizePaste()
				i += idx + len(pasteEndSeq)
				// Drain any partial sequence buffer since we found a complete sequence
				r.seqBuf = nil
			} else {
				// Check if data ends with a partial match for pasteEndSeq
				remaining := data[i:]
				partialLen := r.partialMatchSuffix(remaining, pasteEndSeq)
				if partialLen > 0 {
					// Write everything except the potential partial sequence
					r.pasteBuf.Write(remaining[:len(remaining)-partialLen])
					r.seqBuf = append(r.seqBuf[:0], remaining[len(remaining)-partialLen:]...)
				} else {
					// Flush any previous partial sequence buffer that turned out not to match
					if len(r.seqBuf) > 0 {
						r.pasteBuf.Write(r.seqBuf)
						r.seqBuf = nil
					}
					r.pasteBuf.Write(remaining)
				}
				return
			}
		} else {
			// Look for paste start sequence ESC[200~
			idx := bytes.Index(data[i:], pasteStartSeq)
			if idx >= 0 {
				// Everything before the start sequence is normal input
				r.outBuf.Write(data[i : i+idx])
				r.inPaste = true
				r.pasteBuf.Reset()
				i += idx + len(pasteStartSeq)
				// Clear partial sequence buffer
				r.seqBuf = nil
			} else {
				// Check for partial match at the end
				remaining := data[i:]
				partialLen := r.partialMatchSuffix(remaining, pasteStartSeq)
				if partialLen > 0 {
					// Write everything except the potential partial sequence
					r.outBuf.Write(remaining[:len(remaining)-partialLen])
					r.seqBuf = append(r.seqBuf[:0], remaining[len(remaining)-partialLen:]...)
				} else {
					// Flush any previous partial sequence that didn't match
					if len(r.seqBuf) > 0 {
						r.outBuf.Write(r.seqBuf)
						r.seqBuf = nil
					}
					r.outBuf.Write(remaining)
				}
				return
			}
		}
	}
}

// finalizePaste processes the accumulated paste content.
// Must be called with r.mu held.
func (r *BracketedPasteReader) finalizePaste() {
	r.inPaste = false

	// Flush any partial sequence buffer into paste content
	if len(r.seqBuf) > 0 {
		r.pasteBuf.Write(r.seqBuf)
		r.seqBuf = nil
	}

	text := r.pasteBuf.String()
	r.pasteBuf.Reset()

	if text == "" {
		return
	}

	runeCount := utf8.RuneCountInString(text)
	lineCount := strings.Count(text, "\n")

	// Short, single-line pastes: pass through inline
	if lineCount == 0 && runeCount <= maxInlinePasteRunes {
		r.outBuf.WriteString(text)
		return
	}

	// Long or multiline paste: store as segment and insert placeholder
	idx := len(r.segments)
	r.segments = append(r.segments, text)
	placeholder := fmt.Sprintf("[pasted %d lines, %d chars (#%d)]", lineCount+1, runeCount, idx)
	r.outBuf.WriteString(placeholder)
}

// partialMatchSuffix checks if the suffix of data matches a prefix of seq.
// Returns the length of the partial match (0 if none).
func (r *BracketedPasteReader) partialMatchSuffix(data []byte, seq []byte) int {
	maxCheck := len(seq) - 1
	if maxCheck > len(data) {
		maxCheck = len(data)
	}
	for l := maxCheck; l > 0; l-- {
		if bytes.Equal(data[len(data)-l:], seq[:l]) {
			return l
		}
	}
	return 0
}
