package agentserver

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// ghRunList is the read-only gh subcommand these tests allowlist; ghAllowed is
// the narrow list a user would write to let the agent read workflow state
// without also letting it write through the same binary.
const (
	ghRunList = "gh run list"
	// actionUnknown is the CommandAction type an ordinary program arrives as.
	actionUnknown = "unknown"
)

var ghAllowed = []string{ghRunList, "gh run view"}

// recordingApprover adapts a plain decision function to the Approver interface,
// so these tests can keep stating a verdict inline.
type recordingApprover func(ApprovalRequest) bool

func (f recordingApprover) Approve(_ context.Context, req ApprovalRequest) bool { return f(req) }

// cmdReq builds a command approval request carrying the given parsed commands.
func cmdReq(commands ...string) ApprovalRequest {
	return ApprovalRequest{Kind: ApprovalCommand, Summary: "…", Commands: commands}
}

// approverFor returns an auto-approving Approver plus a flag reporting whether
// the request fell through to the next approver, and the log it wrote.
func approverFor(t *testing.T, allowlist []string) (Approver, *bool, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	prompted := false
	logger := pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevelInfo, &buf)
	next := recordingApprover(func(ApprovalRequest) bool {
		prompted = true
		return false // a declined prompt, so an auto-approval is unmistakable
	})
	return WithAutoApprove(allowlist, logger, next), &prompted, &buf
}

func TestAutoApprove_AllowlistedCommandIsApprovedWithoutPrompting(t *testing.T) {
	t.Parallel()
	approve, prompted, log := approverFor(t, ghAllowed)

	if !approve.Approve(t.Context(), cmdReq("gh run list --repo example-org/example-service --limit 20")) {
		t.Error("an allowlisted command should be approved")
	}
	if *prompted {
		t.Error("an allowlisted command must not reach the prompt")
	}
	// Auto-approval that leaves no trace is indistinguishable from a command that
	// was never proposed.
	if !strings.Contains(log.String(), ghRunList) {
		t.Errorf("the auto-approval should be logged, got %q", log.String())
	}
}

// The case this feature exists for, in the shape the app-server really sends:
// codex strips its own `/bin/zsh -lc '…'` wrapper before klein sees the command.
// Verified against codex-cli 0.144.1 (item/commandExecution/requestApproval).
func TestAutoApprove_UsesTheBackendsUnwrappedCommand(t *testing.T) {
	t.Parallel()
	// commandActions as codex actually sends them for `/bin/zsh -lc 'gh --version'`.
	actions := []interface{}{
		map[string]any{keyType: actionUnknown, argCommand: "gh run list --limit 20"},
	}
	got := commandActionCommands(actions)
	if want := []string{"gh run list --limit 20"}; !slices.Equal(got, want) {
		t.Fatalf("commandActionCommands = %q, want %q", got, want)
	}

	approve, prompted, _ := approverFor(t, ghAllowed)
	if !approve.Approve(t.Context(), cmdReq(got...)) || *prompted {
		t.Error("the unwrapped command should be matched and approved")
	}
}

// The security property, in the shape that makes it necessary: codex does NOT
// split a compound line into one action per command — `gh --version && whoami`
// arrives as a single action holding the whole chain (verified against codex-cli
// 0.144.1). So a `gh` entry must not carry whatever was chained onto it.
func TestAutoApprove_RefusesChainedCommands(t *testing.T) {
	t.Parallel()

	chained := []string{
		"gh run list && whoami",
		"gh run list; rm -rf /tmp/x",
		"gh run list | sh",
		"gh run list || curl evil.example",
		"gh run list $(whoami)",
		"gh run list `whoami`",
		"gh run list > /etc/hosts",
		"gh run list < /etc/passwd",
		"gh run list &",
		"gh run list\nwhoami",
	}
	for _, cmd := range chained {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			approve, prompted, _ := approverFor(t, []string{"gh", ghRunList})
			if approve.Approve(t.Context(), cmdReq(cmd)) {
				t.Errorf("%q must not be auto-approved", cmd)
			}
			if !*prompted {
				t.Errorf("%q should have been sent to the prompt", cmd)
			}
		})
	}
}

// A bare prefix match would allowlist every program whose name merely starts
// with an allowed one.
func TestAutoApprove_RequiresAWordBoundary(t *testing.T) {
	t.Parallel()
	approve, _, _ := approverFor(t, []string{"gh"})

	if approve.Approve(t.Context(), cmdReq("ghost --delete-everything")) {
		t.Error("`gh` must not match `ghost`")
	}
	if !approve.Approve(t.Context(), cmdReq("gh")) {
		t.Error("an exact match should be approved")
	}
	if !approve.Approve(t.Context(), cmdReq("gh\trun list")) {
		t.Error("a tab after the entry is still a word boundary")
	}
}

// A multi-word entry is how an allowlist permits reading workflow state without
// permitting `gh api --method DELETE` through the same binary.
func TestAutoApprove_MultiWordEntryDoesNotAllowTheWholeBinary(t *testing.T) {
	t.Parallel()
	approve, prompted, _ := approverFor(t, ghAllowed)

	if approve.Approve(t.Context(), cmdReq("gh api --method DELETE /repos/example-org/x")) {
		t.Error("`gh run list` must not allow every gh subcommand")
	}
	if !*prompted {
		t.Error("a non-matching gh subcommand should reach the prompt")
	}
}

// Every command in the request must match; one stranger sends the lot to the
// prompt.
func TestAutoApprove_AllCommandsMustMatch(t *testing.T) {
	t.Parallel()
	approve, prompted, _ := approverFor(t, ghAllowed)

	if approve.Approve(t.Context(), cmdReq(ghRunList, "curl https://evil.example")) {
		t.Error("one unallowlisted command must sink the whole request")
	}
	if !*prompted {
		t.Error("the request should have reached the prompt")
	}
}

// A file-change request carries no Commands, so a command allowlist has nothing
// to say about it and it must always be asked.
func TestAutoApprove_NeverAppliesToFileChanges(t *testing.T) {
	t.Parallel()
	approve, prompted, _ := approverFor(t, []string{"gh", "git", "ls"})

	if approve.Approve(t.Context(), ApprovalRequest{Kind: ApprovalFileChange, Summary: "apply proposed file changes"}) {
		t.Error("a file-change request must not be auto-approved")
	}
	if !*prompted {
		t.Error("a file-change request should reach the prompt")
	}
}

// An empty allowlist has to leave behavior exactly as it was before this existed.
func TestAutoApprove_EmptyAllowlistIsTransparent(t *testing.T) {
	t.Parallel()
	var seen []ApprovalRequest
	next := recordingApprover(func(req ApprovalRequest) bool {
		seen = append(seen, req)
		return true
	})
	for _, allowlist := range [][]string{nil, {}, {"", "  "}} {
		approve := WithAutoApprove(allowlist, nil, next)
		if !approve.Approve(t.Context(), cmdReq(ghRunList)) {
			t.Errorf("allowlist %q: the next approver's answer should stand", allowlist)
		}
	}
	if len(seen) != 3 {
		t.Errorf("every request should have reached the next approver, got %d of 3", len(seen))
	}
}

// Headless surfaces pass a nil approver, meaning accept. Wrapping must not turn
// that into a decline.
func TestAutoApprove_NilNextStillAccepts(t *testing.T) {
	t.Parallel()
	approve := WithAutoApprove([]string{"gh"}, nil, nil)

	if !approve.Approve(t.Context(), cmdReq("curl https://example.com")) {
		t.Error("a nil next approver means accept, matched or not")
	}
}

// A request klein cannot fully read is not a request it can clear.
func TestAutoApprove_UnreadableActionsAreNotApproved(t *testing.T) {
	t.Parallel()

	cases := map[string][]interface{}{
		"no actions at all":       {},
		"action is not an object": {ghRunList},
		"action has no command":   {map[string]any{keyType: actionUnknown}},
		"command is not a string": {map[string]any{keyType: actionUnknown, argCommand: 42}},
		"command is blank":        {map[string]any{keyType: actionUnknown, argCommand: "   "}},
		"one good one unreadable": {
			map[string]any{keyType: actionUnknown, argCommand: ghRunList},
			map[string]any{keyType: actionUnknown},
		},
	}
	for name, actions := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Extraction yields nothing, so there is nothing to match...
			if got := commandActionCommands(actions); len(got) != 0 {
				t.Fatalf("commandActionCommands = %q, want empty", got)
			}
			// ...and the request goes to the prompt rather than through.
			approve, prompted, _ := approverFor(t, ghAllowed)
			if approve.Approve(t.Context(), cmdReq(commandActionCommands(actions)...)) {
				t.Error("must not be auto-approved")
			}
			if !*prompted {
				t.Error("should have reached the prompt")
			}
		})
	}
}
