// Package tomledit makes a single named table in a TOML document appear,
// change, or disappear, leaving every other byte of the file exactly as the
// author wrote it.
//
// It exists because klein edits a file people write by hand. Decoding a
// document and re-encoding it is far less code, and it is what `klein mcp` did
// while settings were JSON — but JSON had no comments to lose. TOML does, and a
// tool that silently deletes the note you left above a server definition is a
// tool you stop pointing at your config.
//
// So this does not model TOML. It finds the line where a table starts, finds
// where it ends, and splices. What it must understand is exactly enough to know
// a table header when it sees one:
//
//   - a header is `[name]` or `[[name]]` at the start of a line, once leading
//     whitespace is dropped;
//   - a `[` inside a multi-line string is not a header, so the scanner tracks
//     `"""` and `”'` runs;
//   - a table's block runs to the next header that is not one of its own
//     children, so `[mcp.godoc.env]` travels with `[mcp.godoc]`.
//
// Line endings are the author's too. A document written on Windows keeps its
// CRLF on every line it already had, and lines this package inserts adopt the
// same ending — splitting on LF and rejoining with LF would turn an edit to one
// server into a diff against the whole file.
//
// Every edit is parsed before it is returned. A splice that produces something
// TOML cannot read is a bug here, and the caller gets an error instead of a
// broken config file.
package tomledit

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// SetTable inserts or replaces the table at path, whose body is the key/value
// lines to place under the header (without the header itself).
//
// Replacing keeps the existing header line and anything above it, so a comment
// the author wrote about the table survives the table being rewritten. A table
// that is not there yet is appended at the end of the document.
func SetTable(src []byte, path string, body []byte) ([]byte, error) {
	if err := validPath(path); err != nil {
		return nil, err
	}

	crlf := usesCRLF(src)
	lines := splitLines(src)
	start, end, found := findTable(lines, path)

	header := "[" + path + "]"
	replacement := asDocumentLines(append([]string{header}, splitLines(body)...), crlf)

	var out []string
	switch {
	case found:
		// Keep the author's own header line: it may carry a trailing comment,
		// and its spelling (quoted keys, spacing) is theirs, not ours.
		replacement[0] = lines[start]
		out = append(out, lines[:start]...)
		out = append(out, replacement...)
		out = append(out, lines[end:]...)
	default:
		out = append(out, lines...)
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, asDocumentLines([]string{""}, crlf)...)
		}
		out = append(out, replacement...)
	}

	return finish(out, path, crlf)
}

// DeleteTable removes the table at path and reports whether it was there.
//
// Comments above the header are left alone. One of them may be the author's
// note about the table and another may be a divider for the whole section, and
// nothing in the text says which — so the destructive reading is not the one to
// guess at. An orphaned comment is easy to delete by hand; a section heading
// this tool ate is not easy to notice.
func DeleteTable(src []byte, path string) ([]byte, bool, error) {
	if err := validPath(path); err != nil {
		return nil, false, err
	}

	lines := splitLines(src)
	start, end, found := findTable(lines, path)
	if !found {
		return src, false, nil
	}

	out := append([]string{}, lines[:start]...)
	out = append(out, lines[end:]...)

	doc, err := finish(out, path, usesCRLF(src))
	if err != nil {
		return nil, false, err
	}
	return doc, true, nil
}

// finish joins the edited lines and refuses to hand back a document TOML cannot
// read — the whole point of splicing text is that the result is still valid.
func finish(lines []string, path string, crlf bool) ([]byte, error) {
	doc := []byte(strings.Join(lines, "\n"))
	if len(doc) > 0 && !bytes.HasSuffix(doc, []byte("\n")) {
		// The last line already carries its own CR when the document has one;
		// this only restores the terminator the join left off.
		if crlf && !bytes.HasSuffix(doc, []byte("\r")) {
			doc = append(doc, '\r')
		}
		doc = append(doc, '\n')
	}
	var probe map[string]any
	if _, err := toml.Decode(string(doc), &probe); err != nil {
		return nil, fmt.Errorf("editing table %q produced invalid TOML: %w", path, err)
	}
	return doc, nil
}

// findTable returns the half-open line range [start, end) covering the header of
// path and everything belonging to it, including its child tables.
func findTable(lines []string, path string) (start, end int, found bool) {
	start = -1
	for i, name := range headerNames(lines) {
		if name == "" {
			continue
		}
		switch {
		case start < 0 && name == path:
			start = i
		case start >= 0 && !isChildOf(name, path):
			return start, i, true
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	return start, len(lines), true
}

// headerNames returns, for each line, the table name it declares — or "" when it
// declares none. Multi-line strings are skipped here so findTable can be about
// nothing but where a table begins and ends.
func headerNames(lines []string) []string {
	out := make([]string, len(lines))
	var delim string
	for i, line := range lines {
		if delim != "" {
			if strings.Contains(line, delim) {
				delim = ""
			}
			continue
		}
		if d, opens := opensMultilineString(line); opens {
			delim = d
			continue
		}
		if name, ok := headerName(line); ok {
			out[i] = name
		}
	}
	return out
}

// headerName returns the dotted name of a table header line, if it is one.
// `[[x]]` counts: an array-of-tables entry is still something to splice around.
//
// The trailing check is what separates a header from a row of a nested array,
// which also starts a line with `[`:
//
//	matrix = [
//	  [1, 2],      ← not a header, and treating it as one would end a table early
//	]
//
// A header has nothing after its closing bracket but whitespace and perhaps a
// comment. That is cheap to require and it removes the whole class.
func headerName(line string) (string, bool) {
	t := strings.TrimSpace(line)
	openTok, closeTok := "[", "]"
	if strings.HasPrefix(t, "[[") {
		openTok, closeTok = "[[", "]]"
	} else if !strings.HasPrefix(t, "[") {
		return "", false
	}

	name, rest, ok := strings.Cut(strings.TrimPrefix(t, openTok), closeTok)
	if !ok {
		return "", false
	}
	if rest = strings.TrimSpace(rest); rest != "" && !strings.HasPrefix(rest, "#") {
		return "", false
	}
	if name = strings.TrimSpace(name); name == "" {
		return "", false
	}
	return name, true
}

// isChildOf reports whether name is a sub-table of parent, so that
// `[mcp.godoc.env]` is understood to belong inside `[mcp.godoc]`.
func isChildOf(name, parent string) bool {
	return strings.HasPrefix(name, parent+".")
}

// opensMultilineString reports whether line starts a `"""` or `”'` run that is
// still open at the end of the line — the one construct that can put a `[` at
// the start of a line without it being a header.
func opensMultilineString(line string) (delim string, opens bool) {
	for _, d := range []string{`"""`, `'''`} {
		if n := strings.Count(line, d); n%2 == 1 {
			return d, true
		}
	}
	return "", false
}

// validPath rejects a table name this package cannot splice safely: an empty
// name, or one carrying the delimiters the scanner uses to recognize a header.
func validPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("table path is empty")
	}
	if strings.ContainsAny(path, "[]\n\"'#") {
		return fmt.Errorf("table path %q contains characters this editor cannot place safely", path)
	}
	return nil
}

// splitLines splits on LF and keeps whatever else the line held, so a line that
// ended CRLF keeps its CR and rejoins byte-identical. The trailing newline is
// trimmed rather than becoming an empty final line, so a round trip does not
// grow the file.
//
// Every line-shaped comparison downstream goes through strings.TrimSpace, which
// treats a stray CR as the whitespace it is.
func splitLines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// usesCRLF reports the document's line-ending convention, so lines this package
// inserts match the ones already there rather than seeding a mixed file.
func usesCRLF(src []byte) bool {
	return bytes.Contains(src, []byte("\r\n"))
}

// asDocumentLines restates caller-supplied lines in the document's convention.
// The body comes from klein, not from the author, so it has no bytes worth
// preserving — only an ending worth matching.
func asDocumentLines(lines []string, crlf bool) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		l = strings.TrimSuffix(l, "\r")
		if crlf {
			l += "\r"
		}
		out[i] = l
	}
	return out
}
