// Package pastegen provides rapid generators for terminal input with bracketed
// paste, for property tests over internal/app's BracketedPasteReader.
package pastegen

import (
	"strings"

	"pgregory.net/rapid"
)

const (
	// Start opens a bracketed paste; terminals send it once the REPL enables
	// mode 2004 (internal/app/repl.go).
	Start = "\x1b[200~"
	// End closes a bracketed paste.
	End = "\x1b[201~"

	// InlineLimit mirrors maxInlinePasteRunes in internal/app.
	InlineLimit = 120
)

// Input generates the bytes a terminal sends to the REPL's stdin: typed
// keys, escape sequences, and pastes wrapped in Start/End.
//
// Boundaries drawn on purpose:
//   - pastes of exactly InlineLimit runes and one either side, with and
//     without a newline, plus empty pastes;
//   - multi-byte runes, so byte length and rune count disagree;
//   - escape sequences that share a prefix with the markers ("\x1b[2", arrow
//     keys, a lone ESC) — a false partial match must be released as input;
//   - a marker out of place: End while typing, Start inside a paste;
//   - input that stops mid-marker or inside an unterminated paste.
func Input() *rapid.Generator[[]byte] {
	return rapid.Custom(func(t *rapid.T) []byte {
		var b strings.Builder
		for range rapid.IntRange(0, 8).Draw(t, "tokens") {
			switch rapid.IntRange(0, 9).Draw(t, "kind") {
			case 0, 1, 2:
				b.WriteString(typed(t))
			case 3:
				b.WriteString(rapid.SampledFrom(escapes).Draw(t, "escape"))
			case 4:
				b.WriteString(rapid.SampledFrom([]string{End, Start}).Draw(t, "strayMarker"))
			default:
				b.WriteString(Start)
				b.WriteString(pasteBody(t))
				b.WriteString(End)
			}
		}
		if rapid.IntRange(0, 4).Draw(t, "truncateOdds") == 0 {
			// Stop mid-marker or mid-paste, as a read boundary would.
			b.WriteString(rapid.SampledFrom([]string{
				Start[:rapid.IntRange(1, len(Start)-1).Draw(t, "startPrefix")],
				Start + typed(t),
				Start + typed(t) + End[:rapid.IntRange(1, len(End)-1).Draw(t, "endPrefix")],
			}).Draw(t, "tail"))
		}
		return []byte(b.String())
	})
}

// escapes share a prefix with the markers without being one.
var escapes = []string{"\x1b", "\x1b[", "\x1b[2", "\x1b[20", "\x1b[200", "\x1b[A", "\x1b[201", "\x1b[2~"}

var alphabet = []rune{'a', 'Z', ' ', '\n', 'é', '日', '~', '['}

func typed(t *rapid.T) string {
	return string(rapid.SliceOfN(rapid.SampledFrom(alphabet), 0, 6).Draw(t, "typed"))
}

func pasteBody(t *rapid.T) string {
	switch rapid.IntRange(0, 3).Draw(t, "bodyKind") {
	case 0:
		return ""
	case 1:
		// Straddle the inline limit; maybe one newline to force a placeholder.
		n := InlineLimit + rapid.IntRange(-1, 1).Draw(t, "limitOffset")
		r := rapid.SampledFrom([]string{"x", "é"}).Draw(t, "limitRune")
		body := strings.Repeat(r, n)
		if rapid.Bool().Draw(t, "limitNewline") {
			body = body[:len(body)-len(r)] + "\n"
		}
		return body
	default:
		// Escapes inside a paste are content, including a nested Start.
		part := rapid.SampledFrom([]string{"ab", "\n", "é", "\x1b[2", "\x1b[A", Start})
		parts := rapid.SliceOfN(part, 0, 5).Draw(t, "bodyParts")
		return strings.Join(parts, "")
	}
}

// Cuts generates split points for feeding n bytes in several reads: sorted,
// distinct, in (0, n). Small gaps are favored so markers straddle reads.
func Cuts(n int) *rapid.Generator[[]int] {
	return rapid.Custom(func(t *rapid.T) []int {
		var cuts []int
		for p := rapid.IntRange(1, 4).Draw(t, "first"); p < n; p += rapid.IntRange(1, 8).Draw(t, "gap") {
			cuts = append(cuts, p)
		}
		return cuts
	})
}
