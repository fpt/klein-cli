package agentserver

import (
	"context"
	"strings"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// shellMetacharacters are the constructs that let one command line run a second
// command. A candidate carrying any of them is never auto-approved, however well
// its first word matches the allowlist: `gh --version && rm -rf ~` begins with an
// allowlisted `gh` and is not the command anybody allowlisted.
//
// This check is the whole security boundary, not a backstop. It would be
// reasonable to assume the app-server hands back one action per command and that
// a candidate is therefore a single command already — it does not. Verified
// against codex-cli 0.144.1, `gh --version && whoami` arrives as exactly one
// action whose command is the entire chain:
//
//	"command": "/bin/zsh -lc 'gh --version && whoami'",
//	"commandActions": [{"type": "unknown", "command": "gh --version && whoami"}]
//
// So an allowlist entry of `gh` would, without this, approve any command that
// could be appended to one.
var shellMetacharacters = []string{"&&", "||", ";", "|", "$(", "`", ">", "<", "\n", "\r", "&"}

// WithAutoApprove returns an Approver that accepts a command request outright
// when every command it would run is on the allowlist, and otherwise defers to
// next (the terminal prompt, or nil to accept — the headless default).
//
// It is deliberately narrow in three ways:
//
//   - Commands only. A file-change request carries no Commands, so it always
//     reaches next. Editing files is not something a command allowlist speaks to.
//   - All or nothing. Every command in the request must match; one unrecognized
//     command among them sends the whole request to the prompt.
//   - Nothing implied. An empty allowlist auto-approves nothing, and neither does
//     a request whose commands the backend did not parse into something klein can
//     read in full.
//
// Each auto-approval is logged. A silent one is indistinguishable from a command
// that was never proposed, and the point of an allowlist is to know what it let
// through.
func WithAutoApprove(allowlist []string, logger *pkgLogger.Logger, next Approver) Approver {
	if len(allowlist) == 0 {
		return next
	}
	return autoApprover{allowlist: allowlist, logger: logger, next: next}
}

// autoApprover is the Approver WithAutoApprove returns.
type autoApprover struct {
	logger    *pkgLogger.Logger
	next      Approver
	allowlist []string
}

func (a autoApprover) Approve(ctx context.Context, req ApprovalRequest) bool {
	if allowed, ok := autoApproved(req, a.allowlist); ok {
		if a.logger != nil {
			a.logger.InfoWithIntention(
				pkgLogger.IntentionTool,
				"auto-approved a command on the allowlist",
				"command", allowed,
			)
		}
		return true
	}
	if a.next == nil {
		return true
	}
	return a.next.Approve(ctx, req)
}

// autoApproved reports whether every command in req is allowlisted, returning
// them joined for the log line. A file change carries no commands and so is
// never auto-approved — an allowlist of programs says nothing about writing files.
func autoApproved(req ApprovalRequest, allowlist []string) (string, bool) {
	if req.Kind != ApprovalCommand || len(req.Commands) == 0 {
		return "", false
	}
	for _, cmd := range req.Commands {
		if !commandAllowed(cmd, allowlist) {
			return "", false
		}
	}
	return strings.Join(req.Commands, " && "), true
}

// commandAllowed reports whether command starts with an allowlist entry at a word
// boundary and carries nothing that could run a second command.
//
// The word boundary is what separates `gh` from `ghost`: a bare prefix match
// would allowlist every program whose name starts with an allowed one. Entries
// may be several words (`gh run list`), which is how an allowlist can permit
// reading workflow state without also permitting `gh api --method DELETE`.
func commandAllowed(command string, allowlist []string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	for _, meta := range shellMetacharacters {
		if strings.Contains(command, meta) {
			return false
		}
	}
	for _, entry := range allowlist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.HasPrefix(command, entry) {
			continue
		}
		if len(command) == len(entry) {
			return true
		}
		switch command[len(entry)] {
		case ' ', '\t':
			return true
		}
	}
	return false
}

// commandActionCommands pulls the parsed command out of each of the app-server's
// CommandAction entries, or returns nil if any entry cannot be read.
//
// The actions are typed (read/listFiles/search/unknown) but the type is only for
// friendlier display — every variant carries the same `command` string, and an
// ordinary program like `gh` arrives as `unknown`. So the type is ignored and the
// command is what matters.
//
// All or nothing is the point of the nil. Skipping an unreadable action and
// returning the rest would hand WithAutoApprove a list it could find entirely
// allowlisted while an action nobody could read went with it. Returning nothing
// sends the request to the prompt instead, which is the answer for a request
// klein does not fully understand.
func commandActionCommands(actions []interface{}) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		action, ok := a.(map[string]any)
		if !ok {
			return nil
		}
		cmd, ok := action["command"].(string)
		if !ok || strings.TrimSpace(cmd) == "" {
			return nil
		}
		out = append(out, strings.TrimSpace(cmd))
	}
	return out
}
