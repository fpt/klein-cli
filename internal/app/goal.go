package app

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fpt/klein-cli/pkg/message"
)

// goalMaxTurns caps how many turns a single /goal run may take, as a safety
// stop even when the condition is never met. The condition itself may specify a
// lower bound (e.g. "stop after 10 turns").
const goalMaxTurns = 25

// goalClearAliases are the argument words that clear/stop an active goal.
var goalClearAliases = map[string]bool{
	"clear": true, "stop": true, "off": true, "reset": true, "none": true, "cancel": true,
}

// reStopAfterTurns extracts an explicit turn budget from a goal condition,
// e.g. "... or stop after 12 turns".
var reStopAfterTurns = regexp.MustCompile(`(?i)stop after (\d+) turns?`)

// runGoal drives the /goal command. It blocks, running agent turns toward the
// condition and consulting a fast evaluator after each turn, until the condition
// is met, the user presses Ctrl+C, or a turn cap is reached.
//
// Unlike Claude Code's asynchronous, cross-turn goal, klein's synchronous REPL
// runs the goal to completion in place (one /goal invocation == the whole loop).
func runGoal(ctx context.Context, a *Agent, skillName, args string) {
	w := a.OutWriter()
	args = strings.TrimSpace(args)

	// `/goal` with no argument or a clear alias: nothing to clear in the
	// synchronous model, so just report.
	if args == "" {
		fmt.Fprintln(w, "Usage: /goal <condition>")
		fmt.Fprintln(w, "  Sets a completion condition and keeps working until a fast")
		fmt.Fprintln(w, "  evaluator confirms it is met (Ctrl+C to stop).")
		fmt.Fprintln(w, "  Example: /goal all tests in ./pkg pass and `go vet` is clean")
		return
	}
	if goalClearAliases[strings.ToLower(args)] {
		fmt.Fprintln(w, "No active goal to clear (klein runs a goal to completion in place).")
		return
	}

	condition := args
	maxTurns := goalMaxTurns
	if m := reStopAfterTurns.FindStringSubmatch(condition); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n < goalMaxTurns {
			maxTurns = n
		}
	}

	fmt.Fprintf(w, "\n◎ Goal set: %s\n", condition)
	fmt.Fprintf(w, "  Working toward it (max %d turns; Ctrl+C to stop)...\n", maxTurns)

	// The first directive is the condition itself.
	directive := fmt.Sprintf("Work toward this goal: %s\n\nTake the next concrete step now.", condition)
	start := time.Now()

	for turn := 1; turn <= maxTurns; turn++ {
		fmt.Fprintf(w, "\n◎ Goal turn %d/%d\n", turn, maxTurns)

		_, canceled, err := executeTurn(ctx, a, directive, skillName)
		if canceled {
			fmt.Fprintf(w, "\n◎ Goal stopped by user after %d turn(s), %s.\n", turn, time.Since(start).Round(time.Second))
			return
		}
		if err != nil {
			fmt.Fprintf(w, "\n◎ Goal aborted: turn failed: %v\n", err)
			return
		}

		met, reason := a.evaluateGoal(ctx, condition)
		if met {
			fmt.Fprintf(w, "\n◎ Goal achieved after %d turn(s), %s: %s\n", turn, time.Since(start).Round(time.Second), reason)
			return
		}
		fmt.Fprintf(w, "◎ Not yet: %s\n", reason)
		// Feed the evaluator's reason back as guidance for the next turn.
		directive = fmt.Sprintf("Continue working toward this goal: %s\n\nThe goal is not met yet: %s\nTake the next concrete step now.", condition, reason)
	}

	fmt.Fprintf(w, "\n◎ Goal stopped: reached the %d-turn limit without confirming completion.\n", maxTurns)
}

// goalEvalReason is returned when the evaluator cannot be parsed.
const goalEvalReason = "evaluator response could not be parsed; continuing"

// evaluateGoal asks the session model (no tools) whether the goal condition is
// satisfied by what the agent has surfaced in the conversation so far. It
// returns whether the goal is met and a one-line reason.
func (a *Agent) evaluateGoal(ctx context.Context, condition string) (bool, string) {
	transcript := a.recentTranscript(16)

	prompt := fmt.Sprintf(`You are a strict completion evaluator. Decide whether the GOAL below is satisfied based ONLY on what the assistant has already demonstrated in the TRANSCRIPT. You cannot run commands or read files yourself.

GOAL:
%s

TRANSCRIPT (most recent turns):
%s

Respond with EXACTLY two lines and nothing else:
MET: yes
REASON: <one short sentence>

Use "MET: yes" only if the transcript clearly demonstrates the goal is satisfied; otherwise "MET: no".`, condition, transcript)

	resp, err := a.GetLLMClient().Chat(ctx, []message.Message{
		message.NewChatMessage(message.MessageTypeUser, prompt),
	}, false, nil)
	if err != nil || resp == nil {
		return false, fmt.Sprintf("evaluator error: %v", err)
	}

	return parseGoalEvaluation(resp.Content())
}

// parseGoalEvaluation extracts (met, reason) from the evaluator's reply.
func parseGoalEvaluation(content string) (bool, string) {
	met := false
	metSeen := false
	reason := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "met:"):
			val := strings.TrimSpace(trimmed[len("met:"):])
			met = strings.HasPrefix(strings.ToLower(val), "y")
			metSeen = true
		case strings.HasPrefix(lower, "reason:"):
			reason = strings.TrimSpace(trimmed[len("reason:"):])
		}
	}
	if reason == "" {
		if !metSeen {
			return false, goalEvalReason
		}
		reason = "(no reason given)"
	}
	return met, reason
}

// recentTranscript renders the last maxMessages non-empty messages as a compact
// transcript for the evaluator, skipping injected system prompts.
func (a *Agent) recentTranscript(maxMessages int) string {
	msgs := a.GetMessageState().GetMessages()
	// Collect renderable messages from the end.
	var lines []string
	for i := len(msgs) - 1; i >= 0 && len(lines) < maxMessages; i-- {
		m := msgs[i]
		// Skip injected system scaffolding ([[SKILL_PROMPT]], memory, catalog).
		if m.Type() == message.MessageTypeSystem {
			continue
		}
		s := strings.TrimSpace(m.TruncatedString())
		if s == "" {
			continue
		}
		lines = append([]string{s}, lines...)
	}
	if len(lines) == 0 {
		return "(no conversation yet)"
	}
	return strings.Join(lines, "\n")
}
