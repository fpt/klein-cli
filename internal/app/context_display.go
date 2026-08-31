package app

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	"golang.org/x/term"
)

// ContextDisplay handles the status line shown above the REPL prompt.
type ContextDisplay struct{}

// NewContextDisplay creates a new context display instance
func NewContextDisplay() *ContextDisplay {
	return &ContextDisplay{}
}

// CalculateUsageDetails calculates context window usage details from message state and LLM client.
func (cd *ContextDisplay) CalculateUsageDetails(messageState domain.State, llmClient domain.LLM) (currentTokens, maxTokens, percentage int) {
	messages := messageState.GetMessages()
	if len(messages) == 0 {
		return 0, 0, 0
	}

	estimateTokensFor := func(msg message.Message) int {
		content := msg.Content()
		if t := msg.Thinking(); t != "" {
			content += "\n" + t
		}
		// ~4 chars per token + small per-message overhead
		approx := int(math.Ceil(float64(len(content))/4.0)) + 8
		if approx < 0 {
			approx = 0
		}
		return approx
	}

	for _, msg := range messages {
		currentTokens += estimateTokensFor(msg)
	}

	// Prefer the interface-based value; fall back to hardcoded estimates.
	if cwp, ok := llmClient.(domain.ContextWindowProvider); ok {
		maxTokens = cwp.MaxContextTokens()
	}
	if maxTokens <= 0 {
		return 0, 0, 0
	}

	percentage = int(math.Round(float64(currentTokens) * 100.0 / float64(maxTokens)))
	if percentage > 100 {
		percentage = 100
	}
	return
}

// BackendUsage is context accounting a whole-agent backend reported, as the
// display needs it. Window of 0 means the backend named none.
type BackendUsage struct {
	InputTokens int
	Window      int
}

// usageFrom picks the numbers to draw.
//
// A backend's report wins over klein's estimate whenever there is one, and the
// difference is not accuracy but authorship: on the app-server path the prompt
// is assembled by the backend, from a history klein does not hold, so klein's
// message state is not a smaller sample of the same thing — it is a different
// conversation that happens to overlap. Estimating from it would draw a gauge of
// the wrong prompt.
//
// A backend that reports tokens but no window returns its count with maxTokens
// zero, which ShowStatusLine renders as a bare count. That is the whole reason
// the window is carried separately: the count is known and the fraction is not.
func (cd *ContextDisplay) usageFrom(
	messageState domain.State, llmClient domain.LLM, backend *BackendUsage,
) (currentTokens, maxTokens, percentage int) {
	if backend == nil {
		return cd.CalculateUsageDetails(messageState, llmClient)
	}
	currentTokens = backend.InputTokens
	maxTokens = backend.Window
	if maxTokens <= 0 {
		return currentTokens, 0, 0
	}
	percentage = int(math.Round(float64(currentTokens) * 100.0 / float64(maxTokens)))
	if percentage > 100 {
		percentage = 100
	}
	return currentTokens, maxTokens, percentage
}

// ShowStatusLine renders the combined task + context status line printed above the prompt.
// taskSummary may be empty; contextState is always shown when available.
func (cd *ContextDisplay) ShowStatusLine(messageState domain.State, llmClient domain.LLM, taskSummary string) string {
	return cd.ShowStatusLineWithBackend(messageState, llmClient, taskSummary, nil)
}

// ShowStatusLineWithBackend is ShowStatusLine with a backend's own accounting,
// which supersedes klein's estimate when present.
func (cd *ContextDisplay) ShowStatusLineWithBackend(
	messageState domain.State, llmClient domain.LLM, taskSummary string, backend *BackendUsage,
) string {
	terminalWidth := 80
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		terminalWidth = width
	}

	currentTokens, maxTokens, percentage := cd.usageFrom(messageState, llmClient, backend)
	if maxTokens <= 0 && currentTokens <= 0 && taskSummary == "" {
		return ""
	}

	// Tokens known, window not: report the count and draw no bar. The backend
	// sends a null window when it will not vouch for one, and a percentage of an
	// assumed constant would read as a measurement while being a guess — the
	// failure a gauge like this exists to avoid, not to commit.
	if maxTokens <= 0 && currentTokens > 0 {
		countStr := "Context: " + formatTokens(currentTokens)
		return alignRight(countStr, "\033[2m"+countStr+"\033[0m", taskSummary, terminalWidth)
	}

	// Right side: context usage with color.
	if maxTokens > 0 {
		var colorCode string
		switch {
		case percentage < 50:
			colorCode = "\033[32m" // green
		case percentage < 80:
			colorCode = "\033[33m" // yellow
		default:
			colorCode = "\033[31m" // red
		}
		visible := fmt.Sprintf("Context: %s/%s (%d%%)",
			formatTokens(currentTokens), formatTokens(maxTokens), percentage)
		return alignRight(visible, colorCode+visible+"\033[0m", taskSummary, terminalWidth)
	}

	// No context info — show task summary left-aligned.
	return "\033[2m" + taskSummary + "\033[0m"
}

// formatTokens renders a token count compactly: whole thousands above 1k, the
// exact figure below it.
//
// Below 1k matters more than it looks. Integer-dividing by 1000 renders every
// short conversation as "0k", so the gauge reads empty for exactly as long as a
// user is most likely to glance at it and decide whether it works at all.
func formatTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.Itoa(n/1000) + "k"
}

// alignRight puts right against the right edge, with left dimmed at the left
// edge when there is one.
//
// visible is right with its ANSI escapes stripped — the width to reserve.
// Passing it separately rather than measuring `right` is the point: the escapes
// are bytes the terminal does not draw, and counting them pushes the text off
// the edge by exactly their length.
func alignRight(visible, right, left string, width int) string {
	if left == "" {
		pad := width - len(visible)
		if pad < 0 {
			pad = 0
		}
		return strings.Repeat(" ", pad) + right
	}
	gap := width - len(left) - len(visible)
	if gap < 1 {
		gap = 1
	}
	return "\033[2m" + left + "\033[0m" + strings.Repeat(" ", gap) + right
}

// ShowContextUsage is kept for backward compatibility; delegates to ShowStatusLine with no task summary.
func (cd *ContextDisplay) ShowContextUsage(messageState domain.State, llmClient domain.LLM) string {
	return cd.ShowStatusLine(messageState, llmClient, "")
}
