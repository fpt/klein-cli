package sanitize

import "testing"

func TestControlTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		// The exact line that failed the rs-gallium review (protocol.rs:29).
		{
			"harmony analysis channel",
			"//! <|channel|>analysis<|message|>REASONING<|end|>",
			"//! <｜channel｜>analysis<｜message｜>REASONING<｜end｜>",
		},
		{
			"harmony assistant turn",
			"<|start|>assistant<|channel|>final<|message|>ANSWER<|end|>",
			"<｜start｜>assistant<｜channel｜>final<｜message｜>ANSWER<｜end｜>",
		},
		{"unterminated open form", "<|channel>thought", "<｜channel>thought"},
		{"trailing pipe form", `s.rfind("<channel|>")`, `s.rfind("<channel｜>")`},
		{"think token", "<|think|>reasoning<|/think|>", "<｜think｜>reasoning<｜/think｜>"},
		{"endoftext", "<|endoftext|>", "<｜endoftext｜>"},
		{"im_start underscore name", "<|im_start|>user", "<｜im_start｜>user"},

		// Left alone: no pipes, or pipes that are real operators.
		{"plain text", "just a sentence", "just a sentence"},
		{"html tag", "<think>plain</think>", "<think>plain</think>"},
		{"go generics", "Map[K, V] and Vec<u8>", "Map[K, V] and Vec<u8>"},
		{"rust closure", "iter.map(|x| x + 1)", "iter.map(|x| x + 1)"},
		{"elixir pipe operator", "list |> Enum.map(&f/1)", "list |> Enum.map(&f/1)"},
		{"fsharp backward pipe", "printfn \"%d\" <| f x", "printfn \"%d\" <| f x"},
		{"boolean or in comparison", "if a < b || c > d {", "if a < b || c > d {"},
		{"markdown table row", "| shape | emitted by |", "| shape | emitted by |"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := ControlTokens(c.in); got != c.want {
				t.Errorf("ControlTokens(%q)\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}

// The rewrite must not change how much text there is around the tokens, only
// the pipes themselves — a reviewer's line numbers depend on it.
func TestControlTokensPreservesLineStructure(t *testing.T) {
	t.Parallel()

	in := "line one\n<|channel|>analysis<|message|>x<|end|>\nline three\n"
	got := ControlTokens(in)
	if gotLines, wantLines := countNewlines(got), countNewlines(in); gotLines != wantLines {
		t.Errorf("newline count changed: got %d, want %d", gotLines, wantLines)
	}
	if got == in {
		t.Error("expected the control tokens to be rewritten")
	}
}

func countNewlines(s string) int {
	n := 0
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
}

// Sanitizing already-sanitized text must be a no-op, since the prompt and the
// tool results are rewritten independently.
func TestControlTokensIdempotent(t *testing.T) {
	t.Parallel()

	once := ControlTokens("<|channel|>analysis<|message|>R<|end|>")
	if twice := ControlTokens(once); twice != once {
		t.Errorf("not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
}
