package gateway

import "testing"

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		in       string
		wantCmd  string
		wantText string
	}{
		{"/research-stock 7203", "research-stock", "7203"},
		{"  /code fix the bug  ", "code", "fix the bug"},
		{"/research-stock", "research-stock", "Run the /research-stock command."},
		{"/x line1\nline2", "x", "line1\nline2"},
		{"hello world", "", "hello world"}, // not a command
		{"/", "", "/"},                     // bare slash, no name
		{"/123bad", "", "/123bad"},         // name must start with a letter
		{"", "", ""},
	}
	for _, tc := range cases {
		gotCmd, gotText := parseSlashCommand(tc.in)
		if gotCmd != tc.wantCmd {
			t.Errorf("parseSlashCommand(%q) cmd = %q, want %q", tc.in, gotCmd, tc.wantCmd)
		}
		if gotText != tc.wantText {
			t.Errorf("parseSlashCommand(%q) text = %q, want %q", tc.in, gotText, tc.wantText)
		}
	}
}
