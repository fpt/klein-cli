package message

import (
	"fmt"
	"unicode/utf8"
)

// Tool-argument summarization limits (display/log only).
const (
	summaryMaxStringLen  = 120 // max runes for a string/[]byte value
	summaryMaxArrayItems = 8   // max items shown from an array/slice
	summaryMaxMapEntries = 12  // max entries shown from a map
	summaryMaxDepth      = 2   // max nesting depth before collapsing
)

// SummarizeToolArgs returns a display/log-friendly copy of tool arguments:
// long strings are truncated (rune-safe), large arrays/maps are capped, and
// deeply nested structures are collapsed. The original arguments passed to the
// tool are unaffected.
//
// It is shared by the native ReAct loop and the app-server backends
// (codex/kessel) so tool-call input is reported identically regardless of which
// backend produced it.
func SummarizeToolArgs(args ToolArgumentValues) ToolArgumentValues {
	if summarized, ok := summarizeValue(map[string]any(args), 0).(map[string]any); ok {
		return ToolArgumentValues(summarized)
	}
	return args // unreachable: a map always summarizes to a map
}

func summarizeValue(v any, depth int) any {
	if depth > summaryMaxDepth {
		return "…"
	}
	switch t := v.(type) {
	case string:
		return truncateForDisplay(t)
	case []byte:
		return truncateForDisplay(string(t))
	case []string:
		return summarizeSlice(len(t), func(i int) any { return summarizeValue(t[i], depth+1) })
	case []any:
		return summarizeSlice(len(t), func(i int) any { return summarizeValue(t[i], depth+1) })
	case map[string]any:
		out := make(map[string]any, len(t))
		count := 0
		for k, val := range t {
			if count >= summaryMaxMapEntries {
				out["…"] = fmt.Sprintf("+%d more", len(t)-count)
				break
			}
			out[k] = summarizeValue(val, depth+1)
			count++
		}
		return out
	default:
		// Numbers, bools, nil, and other scalars pass through unchanged.
		return t
	}
}

// summarizeSlice builds a display slice of at most summaryMaxArrayItems items,
// appending a "…+N more" marker when the source is longer. item(i) yields the
// summarized element at index i.
func summarizeSlice(n int, item func(i int) any) []any {
	limit := min(n, summaryMaxArrayItems)
	out := make([]any, 0, limit+1)
	for i := range limit {
		out = append(out, item(i))
	}
	if n > limit {
		out = append(out, fmt.Sprintf("…+%d more", n-limit))
	}
	return out
}

// truncateForDisplay shortens s to summaryMaxStringLen runes with an ellipsis.
// Rune-based so it never splits a multibyte character (e.g. Japanese text).
func truncateForDisplay(s string) string {
	if utf8.RuneCountInString(s) <= summaryMaxStringLen {
		return s
	}
	return string([]rune(s)[:summaryMaxStringLen-1]) + "…"
}
