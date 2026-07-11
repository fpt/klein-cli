package memorydb

import (
	"strings"
	"unicode"
)

// tokenize converts arbitrary text into a normalized, space-separated
// "search_text" that SQLite FTS5's unicode61 tokenizer indexes well across
// languages — the improvement that makes Japanese recall workable without a
// morphological analyzer (kb.md: trigram tokenization has poor Japanese
// recall; pre-tokenize on the Go side instead).
//
// Strategy:
//   - Latin/digit runs are lowercased and kept as whole word tokens.
//   - CJK runs (Han/Hiragana/Katakana) are emitted as overlapping character
//     bigrams (e.g. "メモリ" -> "メモ モリ"), so a query bigram matches stored
//     text without needing word segmentation. Single-char runs emit that char.
//   - Everything else (punctuation, spaces) is a separator.
//
// The same function is applied to both stored content and queries, so the two
// representations line up.
func tokenize(s string) string {
	var out []string
	var latin strings.Builder
	var cjk []rune

	flushLatin := func() {
		if latin.Len() > 0 {
			out = append(out, latin.String())
			latin.Reset()
		}
	}
	flushCJK := func() {
		out = append(out, bigrams(cjk)...)
		cjk = cjk[:0]
	}

	for _, r := range s {
		switch {
		case isCJK(r):
			flushLatin()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			flushCJK()
			latin.WriteRune(unicode.ToLower(r))
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return strings.Join(out, " ")
}

// bigrams returns overlapping 2-rune windows of rs, or the single rune when
// len(rs)==1. Empty input yields nothing.
func bigrams(rs []rune) []string {
	switch len(rs) {
	case 0:
		return nil
	case 1:
		return []string{string(rs)}
	}
	out := make([]string, 0, len(rs)-1)
	for i := 0; i+1 < len(rs); i++ {
		out = append(out, string(rs[i:i+2]))
	}
	return out
}

// isCJK reports whether r is a Han ideograph or a Japanese kana.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}

// ftsMatch builds an FTS5 MATCH expression from free text: tokenize, then quote
// each token and OR them together (recall-oriented — any term may hit; bm25
// handles ranking). Returns "" when there is nothing to match.
func ftsMatch(q string) string {
	toks := strings.Fields(tokenize(q))
	if len(toks) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(toks))
	for _, t := range toks {
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}
