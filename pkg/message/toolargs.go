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
// (codex/appserver) so tool-call input is reported identically regardless of
// which backend produced it.
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

// Argument envelopes a model sometimes wraps real arguments in, instead of
// passing them at the top level as the tool's schema declares.
const (
	argEnvelopeParameters = "parameters"
	argEnvelopeArguments  = "arguments"
	argEnvelopeArgs       = "args"
	argEnvelopeInput      = "input"
)

var wrapperArgNames = map[ToolName]bool{
	argEnvelopeParameters: true,
	argEnvelopeArguments:  true,
	argEnvelopeArgs:       true,
	argEnvelopeInput:      true,
}

// maxUnwrapDepth bounds UnwrapToolArgs; a model that wrapped once has been seen
// to wrap twice, but the nesting is never deep.
const maxUnwrapDepth = 3

// UnwrapToolArgs flattens tool arguments a model nested inside a single
// envelope key — {"parameters":{"pattern":"x"}} for a tool whose schema says
// {"pattern":"x"}. Models emit this intermittently, and mid-conversation: the
// call then fails on a missing required argument, which reads to the model like
// a legitimate empty result rather than a malformed call.
//
// declared is the tool's own argument list. An envelope name the tool actually
// declares is left alone, so a tool with a real "input" object parameter keeps
// working; only an undeclared envelope is stripped.
func UnwrapToolArgs(args ToolArgumentValues, declared []ToolArgument) ToolArgumentValues {
	for range maxUnwrapDepth {
		if len(args) != 1 {
			return args
		}
		inner, ok := soleEnvelope(args, declared)
		if !ok {
			return args
		}
		args = inner
	}
	return args
}

// soleEnvelope reports the map held by args' single envelope key, if that is
// what args is. args is expected to hold exactly one entry.
func soleEnvelope(args ToolArgumentValues, declared []ToolArgument) (ToolArgumentValues, bool) {
	for k, v := range args {
		if !wrapperArgNames[ToolName(k)] || isDeclaredArg(ToolName(k), declared) {
			return nil, false
		}
		switch t := v.(type) {
		case ToolArgumentValues:
			return t, true
		case map[string]any:
			return ToolArgumentValues(t), true
		}
	}
	return nil, false
}

func isDeclaredArg(name ToolName, declared []ToolArgument) bool {
	for _, a := range declared {
		if a.Name == name {
			return true
		}
	}
	return false
}
